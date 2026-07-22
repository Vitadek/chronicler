package api

import (
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"chronicle-server/pkg/collab"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
)

func newManuscriptsTestHandler(t *testing.T) (http.Handler, *config.Config) {
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
	return NewServerRouter(cfg, database, hub, nil, embed.FS{}).Init(), cfg
}

func TestManuscriptPutOverSizeLimitReturns413(t *testing.T) {
	handler, _ := newManuscriptsTestHandler(t)

	// Build a payload comfortably over maxManuscriptWriteBytes (32 MiB)
	// without allocating something absurd: one long chapter content string.
	big := strings.Repeat("a", maxManuscriptWriteBytes+1024)
	payload := `{"metadata":{"id":"huge","title":"t","author":"a"},"chapters":[{"id":"c1","title":"c","content":"` + big + `"}]}`

	req := httptest.NewRequest(http.MethodPut, "/api/manuscripts/huge", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", res.Code, res.Body.String())
	}
}

func TestManuscriptPutUnderSizeLimitSucceeds(t *testing.T) {
	handler, _ := newManuscriptsTestHandler(t)

	payload := `{"metadata":{"id":"normal","title":"t","author":"a"},"chapters":[{"id":"c1","title":"c","content":"<p>hi</p>"}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/manuscripts/normal", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
}

func TestPartialManuscriptPutRoundTripsThroughHandler(t *testing.T) {
	handler, _ := newManuscriptsTestHandler(t)

	full := `{"metadata":{"id":"ms","title":"Book","author":"Author"},"chapters":[
		{"id":"c1","title":"One","content":"<p>one</p>"},
		{"id":"c2","title":"Two","content":"<p>two</p>"}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/api/manuscripts/", strings.NewReader(full))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", res.Code, res.Body.String())
	}

	var created db.ManuscriptRecord
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// Partial PUT: only chapter c2, with an explicit position, everything
	// else omitted.
	partial := `{"metadata":{"id":"ms","title":"Book","author":"Author","revision":` + strconv.Itoa(created.Metadata.Revision) + `},"chapters":[
		{"id":"c2","title":"Two (revised)","content":"<p>two revised</p>","revision":` + strconv.Itoa(chapterRevision(created, "c2")) + `,"position":1}
	]}`
	putReq := httptest.NewRequest(http.MethodPut, "/api/manuscripts/ms", strings.NewReader(partial))
	putReq.Header.Set("Content-Type", "application/json")
	putRes := httptest.NewRecorder()
	handler.ServeHTTP(putRes, putReq)
	if putRes.Code != http.StatusOK {
		t.Fatalf("partial update status = %d: %s", putRes.Code, putRes.Body.String())
	}

	var updated db.ManuscriptRecord
	if err := json.Unmarshal(putRes.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Chapters) != 2 {
		t.Fatalf("expected both chapters in the response, got %d", len(updated.Chapters))
	}
	if updated.Chapters[0].ID != "c1" || updated.Chapters[0].Content != "<p>one</p>" {
		t.Fatalf("c1 (omitted from the partial payload) must survive unchanged: %#v", updated.Chapters[0])
	}
	if updated.Chapters[1].ID != "c2" || updated.Chapters[1].Content != "<p>two revised</p>" {
		t.Fatalf("c2 (in the partial payload) must be updated: %#v", updated.Chapters[1])
	}
}

func chapterRevision(m db.ManuscriptRecord, id string) int {
	for _, c := range m.Chapters {
		if c.ID == id {
			return c.Revision
		}
	}
	return 0
}
