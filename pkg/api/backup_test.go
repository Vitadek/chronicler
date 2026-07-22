package api

import (
	"bytes"
	"database/sql"
	"embed"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"chronicle-server/pkg/collab"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"

	_ "modernc.org/sqlite"
)

func newBackupTestServer(t *testing.T) (http.Handler, *config.Config, *db.ManuscriptRecord) {
	t.Helper()
	// Backup routes are only mounted when LocalAdmin is set (router.go: "only
	// enabled if LocalAdmin is true").
	cfg := &config.Config{DataDir: t.TempDir(), Auth: config.AuthConfig{Mode: config.AuthModeNone}, LocalAdmin: true}
	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	record := &db.ManuscriptRecord{
		Metadata: db.ManuscriptMetadata{ID: "backup-ms", Title: "Backup Test", Author: "Author"},
		Chapters: []db.ChapterRecord{{ID: "c1", Title: "One", Content: "<p>one</p>"}},
	}
	if _, err := db.SaveLegacyManuscript(database, db.LocalUserID, record, true); err != nil {
		t.Fatal(err)
	}
	hub := collab.NewHub(database, cfg)
	t.Cleanup(func() {
		hub.Close()
		database.Close()
	})
	return NewServerRouter(cfg, database, hub, nil, embed.FS{}).Init(), cfg, record
}

func requireXz(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz not available in this environment")
	}
}

func TestBackupImportRejectsCorruptedArchive(t *testing.T) {
	requireXz(t)
	handler, cfg, _ := newBackupTestServer(t)

	// Take a real export first (a valid .chron the server itself produced),
	// then corrupt it by truncation before feeding it back to import —
	// truncation reliably breaks SQLite's integrity check (see
	// pkg/db/db_test.go's TestVerifyIntegrityFailsOnCorruptedDB for why
	// byte-flipping was rejected as unreliable).
	exportReq := httptest.NewRequest(http.MethodPost, "/api/backup/export", nil)
	exportRes := httptest.NewRecorder()
	handler.ServeHTTP(exportRes, exportReq)
	if exportRes.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", exportRes.Code, exportRes.Body.String())
	}
	compressed := exportRes.Body.Bytes()
	if len(compressed) == 0 {
		t.Fatal("expected a non-empty .chron export")
	}

	decompressed, err := runFilter("xz", []string{"-d", "-c"}, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(decompressed) < 4096 {
		t.Fatalf("expected a real multi-page export, got %d bytes", len(decompressed))
	}
	truncated := decompressed[:len(decompressed)/2]
	recompressed, err := runFilter("xz", []string{"-z", "-c", "-T0"}, truncated)
	if err != nil {
		t.Fatal(err)
	}

	_ = cfg // (DataDir is where a real import would stage — not asserted here)
	importReq := httptest.NewRequest(http.MethodPost, "/api/backup/import", bytes.NewReader(recompressed))
	importRes := httptest.NewRecorder()
	handler.ServeHTTP(importRes, importReq)

	if importRes.Code != http.StatusBadRequest {
		t.Fatalf("import of a truncated archive: status = %d, want 400: %s", importRes.Code, importRes.Body.String())
	}
}

func TestBackupExportRefusesOnCorruptedDB(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitDB(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, db.PrimaryDB)

	// Same proven corruption technique as pkg/db/db_test.go's
	// TestVerifyIntegrityFailsOnCorruptedDB: checkpoint so the schema is
	// really in the base file, close, then truncate — byte-flipping was
	// tried first and found unreliable (quick_check can tolerate a flipped
	// byte inside a row payload the b-tree traversal never touches).
	if _, err := database.Exec("PRAGMA wal_checkpoint(FULL)"); err != nil {
		t.Fatal(err)
	}
	database.Close()

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

	cfg := &config.Config{DataDir: dataDir, Auth: config.AuthConfig{Mode: config.AuthModeNone}}
	handler := &BackupHandler{cfg: cfg, database: reopened}

	req := httptest.NewRequest(http.MethodPost, "/api/backup/export", nil)
	res := httptest.NewRecorder()
	handler.PostExport(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("export of a truncated (corrupted) DB: status = %d, want 500: %s", res.Code, res.Body.String())
	}
}
