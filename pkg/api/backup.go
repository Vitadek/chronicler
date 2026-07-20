package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
)

type BackupHandler struct {
	cfg      *config.Config
	database *sql.DB
}

func NewBackupHandler(cfg *config.Config, database *sql.DB) *BackupHandler {
	return &BackupHandler{
		cfg:      cfg,
		database: database,
	}
}

func (h *BackupHandler) Mount(r chi.Router) {
	r.Get("/status", h.GetStatus)
	r.Post("/export", h.PostExport)
	r.Post("/import", h.PostImport)
}

func stamp() string {
	return strings.ReplaceAll(strings.ReplaceAll(time.Now().Format("2006-01-02T15:04:05.000Z"), ":", "-"), ".", "-")
}

func runFilter(cmdName string, args []string, input []byte) ([]byte, error) {
	cmd := exec.Command(cmdName, args...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s failed: %w (stderr: %s)", cmdName, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (h *BackupHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"available":true}`))
}

func (h *BackupHandler) PostExport(w http.ResponseWriter, r *http.Request) {
	tmpFile := filepath.Join(h.cfg.DataDir, fmt.Sprintf("export-%s.db", stamp()))
	defer os.Remove(tmpFile)

	// Run SQLite VACUUM INTO to create online backup
	escapedPath := strings.ReplaceAll(tmpFile, "'", "''")
	query := fmt.Sprintf("VACUUM INTO '%s'", escapedPath)
	if _, err := h.database.Exec(query); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Backup creation failed: " + err.Error()})
		return
	}

	raw, err := os.ReadFile(tmpFile)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read backup: " + err.Error()})
		return
	}

	compressed, err := runFilter("xz", []string{"-z", "-c", "-T0"}, raw)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Compression failed: " + err.Error()})
		return
	}

	name := fmt.Sprintf("chronicler-%s.chron", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Write(compressed)
}

const maxImportBytes = 1024 * 1024 * 1024 // 1 GB
var sqliteMagic = []byte("SQLite format 3\x00")

func (h *BackupHandler) PostImport(w http.ResponseWriter, r *http.Request) {
	// Limit request body size to 1 GB
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	compressed, err := ioReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read body: " + err.Error()})
		return
	}

	if len(compressed) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Send the .chron file as the raw request body."}`))
		return
	}

	decompressed, err := runFilter("xz", []string{"-d", "-c"}, compressed)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Not a valid .chron file (could not decompress)."}`))
		return
	}

	if len(decompressed) < len(sqliteMagic) || !bytes.Equal(decompressed[:len(sqliteMagic)], sqliteMagic) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Not a valid .chron file (not a SQLite database)."}`))
		return
	}

	// Validate Chronicler tables using temporary probe file
	probe := filepath.Join(h.cfg.DataDir, fmt.Sprintf("import-probe-%s.db", stamp()))
	if err := os.WriteFile(probe, decompressed, 0644); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to write probe file: " + err.Error()})
		return
	}
	defer os.Remove(probe)

	probeDb, err := sql.Open("sqlite", probe)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Not a valid SQLite database."}`))
		return
	}
	defer probeDb.Close()

	rows, err := probeDb.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Could not read database schema."}`))
		return
	}
	defer rows.Close()

	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			tables[name] = true
		}
	}

	for _, required := range []string{"schema_migrations", "manuscripts", "chapters"} {
		if !tables[required] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(fmt.Sprintf(`{"error":"Not a Chronicler backup (missing \"%s\" table)."}`, required)))
			return
		}
	}
	probeDb.Close() // Close early before removing file

	// Safety backup of CURRENT database
	safetyBackup := filepath.Join(h.cfg.DataDir, fmt.Sprintf("chronicle-before-restore-%s.db", stamp()))
	escapedSafetyPath := strings.ReplaceAll(safetyBackup, "'", "''")
	safetyQuery := fmt.Sprintf("VACUUM INTO '%s'", escapedSafetyPath)
	if _, err := h.database.Exec(safetyQuery); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to take safety backup: " + err.Error()})
		return
	}

	// Stage atomically
	stagedPath := filepath.Join(h.cfg.DataDir, db.StagedImport)
	tmpStagedPath := stagedPath + ".partial"
	if err := os.WriteFile(tmpStagedPath, decompressed, 0644); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to stage import: " + err.Error()})
		return
	}

	if err := os.Rename(tmpStagedPath, stagedPath); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to stage import: " + err.Error()})
		return
	}

	markerPath := filepath.Join(h.cfg.DataDir, db.ImportMarker)
	markerContent := fmt.Sprintf("%d\n", time.Now().UnixNano()/int64(time.Millisecond))
	if err := os.WriteFile(markerPath, []byte(markerContent), 0644); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to write marker file: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"restartRequired": true,
		"safetyBackup":    filepath.Base(safetyBackup),
	})
}

// Inline helper to read request body bytes to avoid dependency issues
func ioReadAll(r ioReader) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

type ioReader interface {
	Read(p []byte) (n int, err error)
}
