package languagetool

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const (
	probeTimeout  = 2 * time.Second
	probeCacheTtl = 60 * time.Second
)

// Prober answers "is the sidecar actually up", live-probed and cached —
// mirrors the deleted Node pluginCapabilities.ts's languagetoolReachable().
// Deliberately separate from pkg/plugins.CapabilitiesChecker, which must
// keep advertising host:grammar/host:languagetool unconditionally for other
// plugins; LT reachability here is a new, proofreader-only runtime signal.
type Prober struct {
	client  *Client
	http    *http.Client
	mu      sync.RWMutex
	cached  bool
	cacheAt time.Time
}

func NewProber(client *Client) *Prober {
	return &Prober{client: client, http: &http.Client{Timeout: probeTimeout}}
}

// Available reports sidecar reachability. A nil Prober or one built from a
// nil Client (LANGUAGETOOL_URL unset) always reports false.
func (p *Prober) Available() bool {
	if p == nil || p.client == nil {
		return false
	}

	p.mu.RLock()
	if !p.cacheAt.IsZero() && time.Since(p.cacheAt) < probeCacheTtl {
		cached := p.cached
		p.mu.RUnlock()
		return cached
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.cacheAt.IsZero() && time.Since(p.cacheAt) < probeCacheTtl {
		return p.cached
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	ok := false
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.client.baseURL+"/v2/languages", nil)
	if err == nil {
		resp, doErr := p.http.Do(req)
		if doErr == nil {
			ok = resp.StatusCode == http.StatusOK
			resp.Body.Close()
		}
	}

	p.cached = ok
	p.cacheAt = time.Now()
	return ok
}
