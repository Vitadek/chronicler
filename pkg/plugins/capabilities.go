package plugins

import (
	"context"
	"net/http"
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

func (cc *CapabilitiesChecker) languagetoolReachable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	url := cc.cfg.Grammar.LanguagetoolUrl + "/v2/languages"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
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

	var caps []string
	if cc.languagetoolReachable() {
		caps = append(caps, "host:languagetool")
	}

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
	case "host:languagetool":
		return "LanguageTool is not reachable at " + cfg.Grammar.LanguagetoolUrl + ". Start the sidecar (see docker-compose.yml) or set LANGUAGETOOL_URL."
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
