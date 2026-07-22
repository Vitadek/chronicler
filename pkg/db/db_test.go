package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestVerifyIntegrityOkOnFreshDB(t *testing.T) {
	database, err := InitDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := VerifyIntegrity(database); err != nil {
		t.Fatalf("expected a freshly-migrated database to pass integrity check: %v", err)
	}
}

func TestVerifyIntegrityFailsOnCorruptedDB(t *testing.T) {
	dataDir := t.TempDir()
	database, err := InitDB(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, PrimaryDB)

	// InitDB runs in WAL mode: recent writes (the migrations that just ran)
	// live in a separate -wal file and get merged in on read, so corrupting
	// only the base file wouldn't touch what's actually queried. Force a
	// full checkpoint first so the schema is really in the base file, then
	// close before touching it on disk.
	if _, err := database.Exec("PRAGMA wal_checkpoint(FULL)"); err != nil {
		t.Fatal(err)
	}
	database.Close()

	// Corrupt the file on disk. Truncation is the most deterministic way to
	// trigger a real integrity failure in a test (SQLite immediately notices
	// the file is shorter than the page count declared in its header) — it's
	// also realistic: a crash mid-write is a common real-world corruption
	// cause. Byte-flipping was tried first and proved unreliable: a flipped
	// byte inside unused padding or a row payload the b-tree traversal never
	// needs to touch can leave quick_check reporting "ok".
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 4096 {
		t.Fatalf("expected a real multi-page database, got %d bytes", info.Size())
	}
	if err := os.Truncate(dbPath, info.Size()/2); err != nil {
		t.Fatal(err)
	}

	reopened, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	if err := VerifyIntegrity(reopened); err == nil {
		t.Fatal("expected VerifyIntegrity to fail on a database with corrupted page data")
	}
}
