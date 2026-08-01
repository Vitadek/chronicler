package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/grammar"
	"chronicle-server/pkg/grammarproviders"
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
	dict      *grammar.Dictionary
	lt        *languagetool.Client
	ltProber  *languagetool.Prober
	database  *sql.DB
	providers *grammarproviders.Registry
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
	h.providers = grammarproviders.New(cfg, dict)
	return h, nil
}

func (h *GrammarHandler) Mount(r chi.Router) {
	r.Post("/check", h.postCheck)
	r.Get("/capabilities", h.getCapabilities)
	r.Get("/providers", h.getProviders)
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
	// Level applies only to the LanguageTool engine. "picky" opts into its
	// additional style rules; every other value is deliberately normalized to
	// standard so this public endpoint cannot forward arbitrary LT parameters.
	Level     string                   `json:"level,omitempty"`
	Providers []grammarProviderRequest `json:"providers,omitempty"`
}

type grammarProviderRequest struct {
	ID   string `json:"id"`
	Mode string `json:"mode,omitempty"`
}

type grammarProviderRun struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs"`
	FromCache  bool   `json:"fromCache,omitempty"`
	Error      string `json:"error,omitempty"`
}

type grammarCheckResponse struct {
	Hits      []grammar.Hit        `json:"hits"`
	Providers []grammarProviderRun `json:"providers,omitempty"`
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

	if len(req.Providers) > 0 {
		h.postProviderCheck(w, r, req)
		return
	}

	if req.Engine == grammar.EngineLanguagetool && h.providers != nil {
		provider, configured := h.providers.Get("languagetool")
		level := "standard"
		if req.Level == "picky" {
			level = "picky"
		}
		// The cache must distinguish levels: Picky is a strict superset of the
		// standard rule set, so sharing would make changing the UI setting appear
		// to do nothing for already-seen paragraphs.
		cacheEngine := grammar.EngineLanguagetool + ":" + level
		hits, err := grammar.CheckCached(h.database, req.Text, cacheEngine, func() ([]grammar.Hit, error) {
			if !configured {
				return nil, fmt.Errorf("LanguageTool is not configured")
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			return provider.Check(ctx, req.Text, level)
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

func (h *GrammarHandler) postProviderCheck(w http.ResponseWriter, r *http.Request, req grammarCheckRequest) {
	if h.providers == nil {
		writeProviderHits(w, http.StatusServiceUnavailable, nil, nil)
		return
	}
	seen := map[string]bool{}
	if len(req.Providers) > 8 {
		writeProviderHits(w, http.StatusBadRequest, nil, []grammarProviderRun{{Status: "invalid", Error: "At most 8 providers may run in one request"}})
		return
	}
	for _, selected := range req.Providers {
		if selected.ID == "" || seen[selected.ID] {
			writeProviderHits(w, http.StatusBadRequest, nil, []grammarProviderRun{{ID: selected.ID, Status: "invalid", Error: "Provider IDs must be non-empty and unique"}})
			return
		}
		seen[selected.ID] = true
		if _, ok := h.providers.Get(selected.ID); !ok {
			writeProviderHits(w, http.StatusBadRequest, nil, []grammarProviderRun{{ID: selected.ID, Status: "invalid", Error: "Provider is not configured"}})
			return
		}
	}

	type result struct {
		hits []grammar.Hit
		run  grammarProviderRun
	}
	results := make([]result, len(req.Providers))
	var wg sync.WaitGroup
	for i, selected := range req.Providers {
		i, selected := i, selected
		wg.Add(1)
		go func() {
			defer wg.Done()
			provider, _ := h.providers.Get(selected.ID)
			meta := provider.Metadata()
			mode := "standard"
			if selected.Mode == "picky" {
				for _, supported := range meta.Modes {
					if supported == "picky" {
						mode = "picky"
						break
					}
				}
			}
			started := time.Now()
			cacheKey := "provider:" + selected.ID + ":" + provider.Fingerprint(mode)
			hits, err := grammar.CheckCached(h.database, req.Text, cacheKey, func() ([]grammar.Hit, error) {
				ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
				defer cancel()
				return provider.Check(ctx, req.Text, mode)
			})
			run := grammarProviderRun{ID: selected.ID, Status: "ok", DurationMs: time.Since(started).Milliseconds()}
			if err != nil {
				run.Status = "unavailable"
				run.Error = "Provider did not return a result"
				results[i] = result{run: run}
				return
			}
			for j := range hits {
				hits[j].SourceID = selected.ID
				hits[j].SourceLabel = meta.Label
			}
			results[i] = result{hits: hits, run: run}
		}()
	}
	wg.Wait()
	var hits []grammar.Hit
	runs := make([]grammarProviderRun, 0, len(results))
	for _, result := range results {
		hits = append(hits, result.hits...)
		runs = append(runs, result.run)
	}
	groupOverlappingHits(hits)
	writeProviderHits(w, http.StatusOK, hits, runs)
}

func groupOverlappingHits(hits []grammar.Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Start != hits[j].Start {
			return hits[i].Start < hits[j].Start
		}
		if hits[i].End != hits[j].End {
			return hits[i].End < hits[j].End
		}
		return hits[i].SourceID < hits[j].SourceID
	})
	groupStart, groupEnd, group := -1, -1, 0
	for i := range hits {
		if groupStart < 0 || hits[i].Start >= groupEnd {
			group++
			groupStart, groupEnd = hits[i].Start, hits[i].End
		} else if hits[i].End > groupEnd {
			groupEnd = hits[i].End
		}
		hits[i].GroupID = fmt.Sprintf("%d:%d", groupStart, group)
	}
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
	if !resp.Languagetool.Available && h.providers != nil {
		if provider, ok := h.providers.Get("languagetool"); ok {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			resp.Languagetool.Available = provider.Probe(ctx) == nil
		}
	}
	json.NewEncoder(w).Encode(resp)
}

func (h *GrammarHandler) getProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	providers := []grammarproviders.Metadata{}
	if h.providers != nil {
		providers = h.providers.List(r.Context())
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"providers": providers})
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

func writeProviderHits(w http.ResponseWriter, status int, hits []grammar.Hit, providers []grammarProviderRun) {
	if hits == nil {
		hits = []grammar.Hit{}
	}
	if providers == nil {
		providers = []grammarProviderRun{}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(grammarCheckResponse{Hits: hits, Providers: providers})
}
