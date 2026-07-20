package plugins

import (
	"sync"
	"time"

	"chronicle-server/pkg/config"
)

type CapabilitiesChecker struct {
	cfg     *config.Config
	cache   []string
	cacheAt time.Time
	mu      sync.RWMutex
}

func NewCapabilitiesChecker(cfg *config.Config) *CapabilitiesChecker {
	return &CapabilitiesChecker{
		cfg: cfg,
	}
}

func (cc *CapabilitiesChecker) HostCapabilities() []string {
	cc.mu.RLock()
	if !cc.cacheAt.IsZero() && time.Since(cc.cacheAt) < 60*time.Second {
		caps := cc.cache
		cc.mu.RUnlock()
		return caps
	}
	cc.mu.RUnlock()

	cc.mu.Lock()
	defer cc.mu.Unlock()

	// Recheck since lock was acquired
	if !cc.cacheAt.IsZero() && time.Since(cc.cacheAt) < 60*time.Second {
		return cc.cache
	}

	// The prose checker is built into the binary (pkg/grammar) rather than
	// proxied to a sidecar, so this capability is always satisfied.
	//
	// "host:languagetool" is published alongside it as a back-compat alias.
	// The capability was always really "this host offers server-side prose
	// linting at /api/grammar/check" — no plugin ever cared which engine sat
	// behind it — and the Node server still publishes only that older name
	// (chronicle/server/lib/pluginCapabilities.ts). Existing plugins declare
	// `requires: ["host:languagetool"]`, so dropping it here would make them
	// refuse to enable on chronicle-go while continuing to work on Node. New
	// plugins should require "host:grammar"; the alias can go once Node is
	// retired and the plugin manifests have moved over.
	caps := []string{"host:grammar", "host:languagetool"}

	anyKey := cc.cfg.OpenAIKey != "" || cc.cfg.AnthropicKey != "" || cc.cfg.GeminiKey != ""
	if cc.cfg.AIUIEnabled && anyKey {
		caps = append(caps, "host:ai")
	}
	if cc.cfg.AIUIEnabled && cc.cfg.GeminiKey != "" {
		caps = append(caps, "host:gemini")
	}

	cc.cache = caps
	cc.cacheAt = time.Now()
	return caps
}

func (cc *CapabilitiesChecker) Invalidate() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.cacheAt = time.Time{}
	cc.cache = nil
}

func ExplainMissingHostCapability(cfg *config.Config, cap string) string {
	switch cap {
	case "host:grammar", "host:languagetool":
		// Not reachable in practice: the checker is compiled in and both names
		// are published unconditionally (see HostCapabilities).
		return "The built-in prose checker failed to initialise on this server."
	case "host:ai":
		if cfg.AIUIEnabled {
			return "No AI provider key is configured. Set OPENAI_API_KEY, ANTHROPIC_API_KEY or GEMINI_API_KEY."
		}
		return "AI is disabled on this instance (AI_UI=off)."
	case "host:gemini":
		if cfg.AIUIEnabled {
			return "This needs GEMINI_API_KEY (it uses Gemini structured output)."
		}
		return "AI is disabled on this instance (AI_UI=off)."
	default:
		return "The host does not provide \"" + cap + "\"."
	}
}
