package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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

	// Open connection with modernc.org/sqlite
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Configure pragmas
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to execute pragma (%s): %w", pragma, err)
		}
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
		}
	}()
}
