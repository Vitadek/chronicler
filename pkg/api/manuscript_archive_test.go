package api

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chronicle-server/pkg/collab"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
	"chronicle-server/pkg/replica"
)

type archiveTestServer struct {
	cfg      *config.Config
	database *sql.DB
	hub      *collab.Hub
	handler  http.Handler
}

func newArchiveTestServer(t *testing.T) *archiveTestServer {
	t.Helper()
	cfg := &config.Config{DataDir: t.TempDir(), Auth: config.AuthConfig{Mode: config.AuthModeNone}}
	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	hub := collab.NewHub(database, cfg)
	t.Cleanup(func() {
		hub.Close()
		database.Close()
	})
	return &archiveTestServer{
		cfg:      cfg,
		database: database,
		hub:      hub,
		handler:  NewServerRouter(cfg, database, hub, nil, embed.FS{}).Init(),
	}
}

func saveArchiveTestManuscript(t *testing.T, database *sql.DB, id, title, content string, coverName string) {
	t.Helper()
	extra := map[string]interface{}{"genre": "Fantasy", "synopsis": "A portable test."}
	if coverName != "" {
		extra["coverArt"] = coverName
	}
	record := &db.ManuscriptRecord{
		Metadata: db.ManuscriptMetadata{ID: id, Title: title, Author: "Archive Author", ExtraFields: extra},
		Chapters: []db.ChapterRecord{{
			ID:      "chapter-one",
			Title:   "Opening",
			Content: content,
		}},
	}
	result, err := db.SaveLegacyManuscript(database, db.LocalUserID, record, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("unexpected save conflicts: %#v", result.Conflicts)
	}
}

func archiveEntry(t *testing.T, body []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		result, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Fatalf("archive entry %q not found", name)
	return nil
}

func importArchiveRequest(t *testing.T, server *archiveTestServer, archive []byte) manuscriptArchiveImportResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/manuscripts/archive/import", bytes.NewReader(archive))
	req.Header.Set("Content-Type", manuscriptArchiveMime)
	res := httptest.NewRecorder()
	server.handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", res.Code, res.Body.String())
	}
	var result manuscriptArchiveImportResult
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func tinyPNG() []byte {
	// The cover API validates only the format signature, as browsers do before
	// decode; using the shortest signature keeps this contract test focused.
	return []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
}

func TestManuscriptArchiveRoundTripAndCollisionSafety(t *testing.T) {
	server := newArchiveTestServer(t)
	annotated := `<p>She <span data-comment="keep me">walked</span>.</p>` +
		`<p><span data-audio-token="voice-7">Listen</span> <u>now</u>.</p>`
	coverName := "portable.abcdef.png"
	saveArchiveTestManuscript(t, server.database, "portable", "Portable Novel", annotated, coverName)
	cover := tinyPNG()
	_, err := server.database.Exec(`
		INSERT INTO storage_blobs (key, content, content_type, generation, checksum, updated_at)
		VALUES (?, ?, ?, 1, ?, ?)
	`, "covers/local/"+coverName, cover, "image/png", replica.Sha256(cover), time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}

	exportReq := httptest.NewRequest(http.MethodGet, "/api/manuscripts/archive/export", nil)
	exportRes := httptest.NewRecorder()
	server.handler.ServeHTTP(exportRes, exportReq)
	if exportRes.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", exportRes.Code, exportRes.Body.String())
	}
	if got := exportRes.Header().Get("Content-Type"); !strings.HasPrefix(got, manuscriptArchiveMime) {
		t.Fatalf("content type = %q", got)
	}
	if got := exportRes.Header().Get("Content-Disposition"); !strings.Contains(got, ".chron") {
		t.Fatalf("content disposition = %q", got)
	}
	archive := exportRes.Body.Bytes()
	var manifest manuscriptArchiveManifest
	if err = json.Unmarshal(archiveEntry(t, archive, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != manuscriptArchiveFormat || manifest.Version != 1 || manifest.Compression != "zip-deflate" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if manifest.ManuscriptCount != 1 || len(manifest.Manuscripts) != 1 {
		t.Fatalf("unexpected manuscript manifest: %#v", manifest.Manuscripts)
	}
	var archived db.ManuscriptRecord
	if err = json.Unmarshal(archiveEntry(t, archive, manifest.Manuscripts[0].Path), &archived); err != nil {
		t.Fatal(err)
	}
	if archived.Chapters[0].Content != annotated {
		t.Fatalf("annotated HTML changed in archive: %s", archived.Chapters[0].Content)
	}
	if got := archiveEntry(t, archive, manifest.Manuscripts[0].CoverPath); !bytes.Equal(got, cover) {
		t.Fatal("cover changed in archive")
	}

	// A clean destination keeps stable manuscript IDs and titles.
	cleanDestination := newArchiveTestServer(t)
	cleanResult := importArchiveRequest(t, cleanDestination, archive)
	if cleanResult.Imported != 1 || cleanResult.Renamed != 0 || cleanResult.Covers != 1 || cleanResult.Manuscripts[0].Copied {
		t.Fatalf("unexpected clean import result: %#v", cleanResult)
	}
	cleanRecord, err := db.LoadManuscript(cleanDestination.database, db.LocalUserID, "portable")
	if err != nil || cleanRecord == nil || cleanRecord.Metadata.Title != "Portable Novel" || cleanRecord.Chapters[0].Content != annotated {
		t.Fatalf("clean round trip changed manuscript: record=%#v err=%v", cleanRecord, err)
	}

	// Importing into the same account must make a copy, never overwrite the
	// source record. A second import remains safe for the same reason.
	for pass := 0; pass < 2; pass++ {
		result := importArchiveRequest(t, server, archive)
		if result.Imported != 1 || result.Renamed != 1 || result.Covers != 1 || !result.Manuscripts[0].Copied {
			t.Fatalf("unexpected import result: %#v", result)
		}
		copyRecord, loadErr := db.LoadManuscript(server.database, db.LocalUserID, result.Manuscripts[0].ID)
		if loadErr != nil || copyRecord == nil {
			t.Fatalf("could not load imported copy: %v", loadErr)
		}
		if copyRecord.Chapters[0].Content != annotated || copyRecord.Metadata.Title != "Portable Novel (Imported copy)" {
			t.Fatalf("imported manuscript changed: %#v", copyRecord)
		}
		importedCover := coverReference(copyRecord)
		var importedCoverBytes []byte
		if err = server.database.QueryRow(
			"SELECT content FROM storage_blobs WHERE key = ?", "covers/local/"+importedCover,
		).Scan(&importedCoverBytes); err != nil || !bytes.Equal(importedCoverBytes, cover) {
			t.Fatalf("imported cover missing or changed: %v", err)
		}
	}

	list, err := db.ListManuscripts(server.database, db.LocalUserID)
	if err != nil || len(list) != 3 {
		t.Fatalf("import overwrote instead of adding: count=%d err=%v", len(list), err)
	}
	original, err := db.LoadManuscript(server.database, db.LocalUserID, "portable")
	if err != nil || original.Chapters[0].Content != annotated || original.Metadata.Title != "Portable Novel" {
		t.Fatal("original manuscript was changed by import")
	}
}

func TestManuscriptArchiveRejectsInvalidInput(t *testing.T) {
	server := newArchiveTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/manuscripts/archive/import", strings.NewReader("not a zip"))
	res := httptest.NewRecorder()
	server.handler.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "valid .chron") {
		t.Fatalf("invalid import status = %d: %s", res.Code, res.Body.String())
	}
}

func TestManuscriptArchiveCompressesLargeProseLibrary(t *testing.T) {
	server := newArchiveTestServer(t)
	var prose strings.Builder
	prose.Grow(4 * 1024 * 1024)
	paragraph := []string{
		"The", "traveler", "crossed", "the", "quiet", "valley", "before", "sunrise,",
		"carrying", "a", "letter", "whose", "answer", "could", "change", "everything.",
	}
	for word := 0; word < 360000; word++ {
		prose.WriteString(paragraph[word%len(paragraph)])
		if word%137 == 0 {
			fmt.Fprintf(&prose, " scene-%d", word/137)
		}
		prose.WriteByte(' ')
	}
	saveArchiveTestManuscript(t, server.database, "large", "A 360,000 Word Library", "<p>"+prose.String()+"</p>", "")

	var compressed bytes.Buffer
	archiveHandler := NewManuscriptArchiveHandler(server.cfg, server.database)
	if err := archiveHandler.writeArchive(&compressed, db.LocalUserID); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() >= 5*1024*1024 {
		t.Fatalf("360,000-word archive = %.2f MiB, expected under 5 MiB", float64(compressed.Len())/(1024*1024))
	}
	t.Logf("360,000-word representative archive: %.2f MiB raw prose -> %.2f MiB .chron", float64(prose.Len())/(1024*1024), float64(compressed.Len())/(1024*1024))
}
