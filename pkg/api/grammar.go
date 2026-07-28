package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/grammar"
	"chronicle-server/pkg/languagetool"

	"github.com/go-chi/chi/v5"
)

// GrammarHandler serves the built-in prose checker, with an optional,
// opt-in LanguageTool engine behind an `engine` field on the request body.
// LanguageTool is used ONLY when a caller explicitly asks for it (currently
// just chronicle-plugin-proofreader) and silently falls back to the native
// checker on any failure — see postCheck. The response shape — and,
// critically, the `kind` vocabulary — is unchanged either way, so the
// frontend needs no per-engine modification: the custom dictionary still
// filters on "misspelling", ProofreadView still splits Spelling from
// Grammar, and the proofreader plugin's Word-choice pane still filters on
// "confusion". See pkg/grammar for the details.
type GrammarHandler struct {
	dict     *grammar.Dictionary
	lt       *languagetool.Client
	ltProber *languagetool.Prober
	database *sql.DB
}

// NewGrammarHandler loads the embedded dictionary and, if LANGUAGETOOL_URL
// is configured, an optional LanguageTool client/prober.
//
// A dictionary load error here is not fatal to the server: the handler then
// degrades to returning empty results so the editor keeps working without
// squiggles, rather than taking down an app whose primary job is writing,
// not linting.
func NewGrammarHandler(cfg *config.Config, database *sql.DB) (*GrammarHandler, error) {
	lt := languagetool.New(cfg)
	h := &GrammarHandler{lt: lt, ltProber: languagetool.NewProber(lt), database: database}

	dict, err := grammar.Load()
	if err != nil {
		return h, err
	}
	h.dict = dict
	return h, nil
}

func (h *GrammarHandler) Mount(r chi.Router) {
	r.Post("/check", h.postCheck)
	r.Get("/capabilities", h.getCapabilities)
}

// maxCheckBytes bounds a single request. The client lints one paragraph at a
// time, so this is far above any legitimate payload while still refusing a
// whole manuscript in one call.
const maxCheckBytes = 256 * 1024

type grammarCheckRequest struct {
	Text string `json:"text"`
	// Engine selects the checking engine. Empty/"native" (default) uses the
	// built-in checker; "languagetool" opts into the sidecar, currently only
	// requested by chronicle-plugin-proofreader when the writer has switched
	// it on in Settings. Any other value is treated as "native".
	Engine string `json:"engine,omitempty"`
}

type grammarCheckResponse struct {
	Hits []grammar.Hit `json:"hits"`
}

func (h *GrammarHandler) postCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req grammarCheckRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCheckBytes)).Decode(&req); err != nil {
		// Mirrors the Node route: a bad payload yields an empty hit list rather
		// than an error the editor would have to special-case mid-keystroke.
		writeHits(w, http.StatusBadRequest, nil)
		return
	}

	if req.Engine == grammar.EngineLanguagetool && h.lt != nil && h.ltProber.Available() {
		hits, err := grammar.CheckCached(h.database, req.Text, grammar.EngineLanguagetool, func() ([]grammar.Hit, error) {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			return h.lt.Check(ctx, req.Text)
		})
		if err == nil {
			writeHits(w, http.StatusOK, hits)
			return
		}
		// Any LT failure (timeout, non-200, bad JSON) falls through to the
		// native checker below rather than erroring the request.
	}

	if h.dict == nil {
		// Dictionary failed to load at startup — report cleanly instead of
		// pretending the text is clean.
		writeHits(w, http.StatusServiceUnavailable, nil)
		return
	}

	hits, _ := grammar.CheckCached(h.database, req.Text, grammar.EngineNative, func() ([]grammar.Hit, error) {
		return h.dict.Check(req.Text), nil
	})
	writeHits(w, http.StatusOK, hits)
}

type grammarCapabilitiesResponse struct {
	Languagetool struct {
		Available bool `json:"available"`
	} `json:"languagetool"`
}

// getCapabilities reports whether the optional LanguageTool engine is
// currently reachable. Read only by chronicle-plugin-proofreader — it is
// NOT the same signal as pkg/plugins.CapabilitiesChecker's host:grammar /
// host:languagetool, which stay unconditionally true for every other plugin.
func (h *GrammarHandler) getCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var resp grammarCapabilitiesResponse
	resp.Languagetool.Available = h.ltProber.Available()
	json.NewEncoder(w).Encode(resp)
}

// writeHits always emits `hits` as an array, never null: the client narrows
// with `data.hits || []` and ProofreadView indexes each hit's `replacements`
// without a guard.
func writeHits(w http.ResponseWriter, status int, hits []grammar.Hit) {
	if hits == nil {
		hits = []grammar.Hit{}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(grammarCheckResponse{Hits: hits})
}
