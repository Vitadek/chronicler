package api

import (
	"encoding/json"
	"net/http"

	"chronicle-server/pkg/grammar"

	"github.com/go-chi/chi/v5"
)

// GrammarHandler serves the built-in prose checker.
//
// It replaces the Node server's proxy to a Java LanguageTool sidecar
// (chronicle/server/routes/grammar.ts). The response shape — and, critically,
// the `kind` vocabulary — is unchanged, so the frontend needs no modification:
// the custom dictionary still filters on "misspelling", ProofreadView still
// splits Spelling from Grammar, and the proofreader plugin's Word-choice pane
// still filters on "confusion". See pkg/grammar for the details.
type GrammarHandler struct {
	dict *grammar.Dictionary
}

// NewGrammarHandler loads the embedded dictionary.
//
// An error here is not fatal to the server: the handler degrades to returning
// empty results so the editor keeps working without squiggles, rather than
// taking down an app whose primary job is writing, not linting.
func NewGrammarHandler() (*GrammarHandler, error) {
	dict, err := grammar.Load()
	if err != nil {
		return &GrammarHandler{}, err
	}
	return &GrammarHandler{dict: dict}, nil
}

func (h *GrammarHandler) Mount(r chi.Router) {
	r.Post("/check", h.postCheck)
}

// maxCheckBytes bounds a single request. The client lints one paragraph at a
// time, so this is far above any legitimate payload while still refusing a
// whole manuscript in one call.
const maxCheckBytes = 256 * 1024

type grammarCheckRequest struct {
	Text string `json:"text"`
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

	if h.dict == nil {
		// Dictionary failed to load at startup — report cleanly instead of
		// pretending the text is clean.
		writeHits(w, http.StatusServiceUnavailable, nil)
		return
	}

	writeHits(w, http.StatusOK, h.dict.Check(req.Text))
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
