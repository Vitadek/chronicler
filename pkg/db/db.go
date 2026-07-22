package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var DB *sql.DB

const (
	PrimaryDB   = "chronicle.db"
	LegacyDB    = "scribe.db"
	LocalUserID = "local"
)

func InitDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	// Apply pending import if any (handled later in backup, but we keep DB path logic)
	primaryPath := filepath.Join(dataDir, PrimaryDB)
	legacyPath := filepath.Join(dataDir, LegacyDB)

	dbPath := primaryPath
	if _, err := os.Stat(primaryPath); os.IsNotExist(err) {
		if _, errLegacy := os.Stat(legacyPath); errLegacy == nil {
			dbPath = legacyPath
		}
	}

	// Pragmas MUST travel in the DSN, not via db.Exec.
	//
	// database/sql hands out connections from a pool, and `db.Exec("PRAGMA ...")`
	// runs on exactly one of them — whichever the pool happened to lend. Every
	// other connection then keeps SQLite's defaults, most damagingly
	// busy_timeout=0, so the moment two requests write concurrently the loser
	// fails instantly with SQLITE_BUSY instead of waiting. (Found by the formal
	// suite: 9 of 13 foundation failures were "database is locked".)
	// Pragmas in the DSN are applied by the driver to every new connection.
	dsn := "file:" + dbPath + "?" + strings.Join([]string{
		// Start every transaction with BEGIN IMMEDIATE, taking the write lock
		// up front.
		//
		// SQLite's default DEFERRED transaction acquires a read snapshot and
		// only asks for the write lock at the first write. If another
		// connection committed in between, that upgrade cannot succeed — WAL
		// returns SQLITE_BUSY_SNAPSHOT (517), and busy_timeout does NOT help,
		// because waiting cannot repair an already-stale snapshot; only a
		// retry from the beginning can. Taking the lock at BEGIN turns that
		// unrecoverable error into an ordinary wait that busy_timeout covers.
		// (Observed as "database is locked (517)" once the plain SQLITE_BUSY
		// below was fixed.)
		"_txlock=immediate",
		// Wait rather than erroring when another connection holds the write
		// lock. The driver deliberately applies this one first.
		"_pragma=busy_timeout(5000)",
		// WAL lets readers proceed during a write — required for any real
		// concurrency, and the format the existing databases already use.
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(ON)",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// SQLite permits exactly one writer at a time. busy_timeout above makes
	// contending writers wait, but capping the pool keeps that contention low
	// enough that they rarely have to, and bounds the file descriptors a
	// long-running server holds open.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(time.Hour)

	// Verify the pragmas actually took, so a driver or DSN change can never
	// silently reintroduce the SQLITE_BUSY class of failure above.
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to read journal_mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		db.Close()
		return nil, fmt.Errorf("expected WAL journal mode, got %q", journalMode)
	}
	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to read busy_timeout: %w", err)
	}
	if busyTimeout == 0 {
		db.Close()
		return nil, fmt.Errorf("busy_timeout did not apply; concurrent writes would fail with SQLITE_BUSY")
	}

	// Catch page-level corruption before it ever reaches a request handler,
	// the S3 replica, or a .chron backup. CHRONICLE_SKIP_INTEGRITY_CHECK is
	// an escape hatch for emergency recovery on an already-corrupt DB (e.g.
	// to boot just long enough to export a partial backup or inspect data).
	if os.Getenv("CHRONICLE_SKIP_INTEGRITY_CHECK") != "1" {
		if err := VerifyIntegrity(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("%w — restore from a .chron backup or the S3 replica, or set CHRONICLE_SKIP_INTEGRITY_CHECK=1 to boot anyway for recovery", err)
		}
	} else {
		log.Println("[db] WARNING: CHRONICLE_SKIP_INTEGRITY_CHECK=1 — booting without verifying database integrity")
	}

	// Run migrations
	if err := RunMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("database migrations failed: %w", err)
	}

	// Scrub tombstones check
	tx, err := db.Begin()
	if err == nil {
		if errScrub := scrubRetainedTombstonePayloads(tx); errScrub == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}

	// Ensure Local User exists for single-user auth modes
	var existsID string
	err = db.QueryRow("SELECT id FROM users WHERE id = ?", LocalUserID).Scan(&existsID)
	if err == sql.ErrNoRows {
		now := time.Now().UnixNano() / int64(time.Millisecond)
		_, errInsert := db.Exec("INSERT INTO users (id, display_name, created_at) VALUES (?, ?, ?)", LocalUserID, "Local User", now)
		if errInsert != nil {
			db.Close()
			return nil, fmt.Errorf("failed to insert local user: %w", errInsert)
		}
	}

	DB = db
	return db, nil
}

// VerifyIntegrity runs SQLite's PRAGMA quick_check — a fast structural scan
// (page linkage, index consistency) that catches corruption a normal query
// would silently read through or panic on. It is NOT a substitute for
// PRAGMA integrity_check (which also verifies row/foreign-key content) — see
// the full integrity_check used on .chron backup import, where the cost of
// a slower, more thorough scan is worth it for data entering from outside
// the system.
func VerifyIntegrity(database *sql.DB) error {
	var result string
	if err := database.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("failed to run integrity check: %w", err)
	}
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("database failed integrity check: %s", result)
	}
	return nil
}

func GC() {
	if DB == nil {
		return
	}
	now := time.Now().UnixNano() / int64(time.Millisecond)
	_, _ = DB.Exec("DELETE FROM sessions WHERE expires_at < ?", now)
	_, _ = DB.Exec("DELETE FROM kv WHERE expires_at IS NOT NULL AND expires_at < ?", now)
}

func StartGCLoop() {
	go func() {
		for {
			time.Sleep(24 * time.Hour)
			GC()
			// Log-only: a running server must never crash on a scheduled
			// check. Startup already refuses to boot on a corrupt DB; this
			// is early warning for corruption that develops mid-uptime.
			if DB != nil {
				if err := VerifyIntegrity(DB); err != nil {
					log.Printf("[db] WARNING: daily integrity check failed: %v", err)
				}
			}
		}
	}()
}
