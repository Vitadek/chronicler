package api

import (
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chronicle-server/pkg/collab"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
)

func TestRemovedRoutesAndGrammarContract(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), Auth: config.AuthConfig{Mode: config.AuthModeNone}}
	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	hub := collab.NewHub(database, cfg)
	defer hub.Close()

	handler := NewServerRouter(cfg, database, hub, nil, embed.FS{}).Init()

	authReq := httptest.NewRequest(http.MethodGet, "/api/auth/config", nil)
	authRes := httptest.NewRecorder()
	handler.ServeHTTP(authRes, authReq)
	if authRes.Code != http.StatusOK {
		t.Fatalf("auth config status = %d: %s", authRes.Code, authRes.Body.String())
	}
	var authPayload map[string]interface{}
	if err := json.Unmarshal(authRes.Body.Bytes(), &authPayload); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"aiAvailable", "aiProviders"} {
		if _, exists := authPayload[removed]; exists {
			t.Fatalf("auth config still advertises %s: %s", removed, authRes.Body.String())
		}
	}

	removedReq := httptest.NewRequest(http.MethodPost, "/api/ai/respond", strings.NewReader(`{}`))
	removedRes := httptest.NewRecorder()
	handler.ServeHTTP(removedRes, removedReq)
	if removedRes.Code != http.StatusNotFound {
		t.Fatalf("removed route status = %d, want 404: %s", removedRes.Code, removedRes.Body.String())
	}

	grammarReq := httptest.NewRequest(http.MethodPost, "/api/grammar/check", strings.NewReader(`{"text":"This is teh test."}`))
	grammarReq.Header.Set("Content-Type", "application/json")
	grammarRes := httptest.NewRecorder()
	handler.ServeHTTP(grammarRes, grammarReq)
	if grammarRes.Code != http.StatusOK {
		t.Fatalf("grammar status = %d: %s", grammarRes.Code, grammarRes.Body.String())
	}
	var grammarPayload struct {
		Hits []json.RawMessage `json:"hits"`
	}
	if err := json.Unmarshal(grammarRes.Body.Bytes(), &grammarPayload); err != nil {
		t.Fatal(err)
	}
	if grammarPayload.Hits == nil || len(grammarPayload.Hits) == 0 {
		t.Fatalf("grammar remains available and should flag the typo: %s", grammarRes.Body.String())
	}
}
