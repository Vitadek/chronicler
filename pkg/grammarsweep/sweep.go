// Package grammarsweep is an idle-triggered background job that proactively
// warms pkg/grammar's content-addressed check cache (pkg/grammar/cache.go)
// across the whole library, so that when a writer actually opens Proofread,
// most paragraphs already have a cached result instead of needing a live
// check — particularly valuable for the optional LanguageTool engine, whose
// round-trip is the slow part this whole feature exists to hide.
//
// Off by default (GRAMMAR_BACKGROUND_SWEEP=false) — see config.GrammarConfig.
package grammarsweep

import (
	"context"
	"database/sql"
	"time"

	"chronicle-server/pkg/activity"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
	"chronicle-server/pkg/grammar"
	"chronicle-server/pkg/grammarproviders"
)

// tickInterval is how often we check whether it's time to sweep.
const tickInterval = 1 * time.Minute

// perParagraphDelay keeps the sweep from competing meaningfully with a real
// request that happens to land mid-pass, on top of the mid-pass activity
// check below.
const perParagraphDelay = 200 * time.Millisecond

type Sweeper struct {
	cfg       *config.Config
	database  *sql.DB
	dict      *grammar.Dictionary
	providers *grammarproviders.Registry
	stopChan  chan struct{}

	// idleMs and sleep are overridden in tests so the gating logic can be
	// exercised without real wall-clock waits or the process-global
	// pkg/activity state.
	idleMs func() int64
	sleep  func(time.Duration)
}

// NewSweeper loads the embedded dictionary once, shared across every pass —
// mirrors NewGrammarHandler, but deliberately separate: the sweep runs on
// its own goroutine/lifecycle (see main.go), not per-request.
func NewSweeper(cfg *config.Config, database *sql.DB) (*Sweeper, error) {
	dict, err := grammar.Load()
	if err != nil {
		return nil, err
	}
	return &Sweeper{
		cfg:       cfg,
		database:  database,
		dict:      dict,
		providers: grammarproviders.New(cfg, dict),
		stopChan:  make(chan struct{}),
		idleMs:    activity.MillisSinceLastRequest,
		sleep:     time.Sleep,
	}, nil
}

// Start is a no-op unless GRAMMAR_BACKGROUND_SWEEP is set — see
// config.GrammarConfig.BackgroundSweep's doc comment for why it defaults off.
func (s *Sweeper) Start() {
	if !s.cfg.Grammar.BackgroundSweep {
		return
	}
	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if s.idleMs() > s.cfg.Grammar.SweepIdleThresholdMs {
					s.runPass()
				}
			case <-s.stopChan:
				return
			}
		}
	}()
}

func (s *Sweeper) Close() {
	close(s.stopChan)
}

// runPass walks the whole library once, warming the cache for every
// paragraph not already in it. It's safe to call repeatedly/resume freely:
// grammar.CheckCached is itself a cache-check-first, so re-visiting an
// already-warm paragraph costs one cheap SELECT, not a recompute — there's
// no separate "where did I leave off" bookkeeping to maintain.
func (s *Sweeper) runPass() {
	chapters, err := db.AllChapters(s.database)
	if err != nil {
		return
	}

	for _, chapter := range chapters {
		for _, para := range extractParagraphs(chapter.Content) {
			// Check before EVERY paragraph, not just at pass start: real
			// activity resuming mid-pass must stop the sweep immediately.
			if s.idleMs() <= s.cfg.Grammar.SweepIdleThresholdMs {
				return
			}
			s.checkParagraph(para)
			s.sleep(perParagraphDelay)
		}
	}
}

func (s *Sweeper) checkParagraph(text string) {
	_, _ = grammar.CheckCached(s.database, text, grammar.EngineNative, func() ([]grammar.Hit, error) {
		return s.dict.Check(text), nil
	})

	if s.providers == nil {
		return
	}
	for _, provider := range s.providers.BackgroundProviders() {
		provider := provider
		meta := provider.Metadata()
		cacheKey := "provider:" + meta.ID + ":" + provider.Fingerprint("standard")
		_, _ = grammar.CheckCached(s.database, text, cacheKey, func() ([]grammar.Hit, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			return provider.Check(ctx, text, "standard")
		})
	}
}
