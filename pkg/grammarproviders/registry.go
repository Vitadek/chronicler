package grammarproviders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/grammar"
	"chronicle-server/pkg/languagetool"
)

type Metadata struct {
	ID              string   `json:"id"`
	Label           string   `json:"label"`
	Adapter         string   `json:"adapter"`
	DataBoundary    string   `json:"dataBoundary"`
	Modes           []string `json:"modes"`
	DefaultEnabled  bool     `json:"defaultEnabled"`
	AllowBackground bool     `json:"allowBackground"`
	Available       bool     `json:"available"`
	Error           string   `json:"error,omitempty"`
}

type Provider interface {
	Metadata() Metadata
	Fingerprint(mode string) string
	Check(context.Context, string, string) ([]grammar.Hit, error)
	Probe(context.Context) error
}

type Registry struct {
	providers map[string]Provider
	invalid   []Metadata
}

func New(cfg *config.Config, dict *grammar.Dictionary) *Registry {
	r := &Registry{providers: map[string]Provider{}}
	r.providers["native"] = &nativeProvider{dict: dict}

	configs, errs := LoadFile(cfg.Grammar.ProvidersFile)
	for i, err := range errs {
		r.invalid = append(r.invalid, Metadata{ID: fmt.Sprintf("configuration-%d", i+1), Label: "Invalid provider", Available: false, Error: err.Error()})
	}
	for _, providerCfg := range configs {
		provider, err := buildProvider(providerCfg)
		if err != nil {
			r.invalid = append(r.invalid, Metadata{ID: providerCfg.ID, Label: providerCfg.Label, Adapter: providerCfg.Adapter, DataBoundary: providerCfg.DataBoundary, Available: false, Error: err.Error()})
			continue
		}
		r.providers[providerCfg.ID] = provider
	}

	// Backward-compatible environment configuration becomes an ordinary
	// provider when a file has not already claimed the conventional ID.
	if cfg.Grammar.LanguagetoolUrl != "" {
		if _, exists := r.providers["languagetool"]; !exists {
			lt := languagetool.New(cfg)
			r.providers["languagetool"] = &languageToolProvider{
				meta:             Metadata{ID: "languagetool", Label: "LanguageTool", Adapter: "languagetool", DataBoundary: "local", Modes: []string{"standard", "picky"}, AllowBackground: true},
				client:           lt,
				fingerprintValue: "legacy",
			}
		}
	}
	return r
}

func buildProvider(cfg ProviderConfig) (Provider, error) {
	timeout, err := providerTimeout(cfg.Timeout)
	if err != nil {
		return nil, err
	}
	secrets := map[string]string{}
	for name, ref := range cfg.Secrets {
		value, err := resolveSecret(ref)
		if err != nil {
			return nil, err
		}
		secrets[name] = value
	}
	modes := cfg.Modes
	if len(modes) == 0 {
		modes = []string{"standard"}
	}
	label := cfg.Label
	if label == "" {
		label = cfg.ID
	}
	meta := Metadata{ID: cfg.ID, Label: label, Adapter: cfg.Adapter, DataBoundary: cfg.DataBoundary, Modes: modes, DefaultEnabled: cfg.DefaultEnabled, AllowBackground: cfg.AllowBackground}
	fp := fingerprint(cfg, secrets)
	if cfg.Adapter == "languagetool" {
		return limitProvider(&languageToolProvider{meta: meta, client: languagetool.NewAt(cfg.Endpoint, defaultString(cfg.Language, "en-US"), secrets["username"], secrets["api_key"], timeout), fingerprintValue: fp}, cfg.Concurrency), nil
	}
	return limitProvider(&protocolProvider{meta: meta, endpoint: strings.TrimSuffix(cfg.Endpoint, "/"), bearer: secrets["bearer_token"], fingerprintValue: fp, http: &http.Client{Timeout: timeout}}, cfg.Concurrency), nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.providers[id]
	return p, ok
}

func (r *Registry) BackgroundProviders() []Provider {
	providers := []Provider{}
	for id, provider := range r.providers {
		if id != "native" && provider.Metadata().AllowBackground {
			providers = append(providers, provider)
		}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Metadata().ID < providers[j].Metadata().ID })
	return providers
}

func (r *Registry) List(ctx context.Context) []Metadata {
	items := make([]Metadata, 0, len(r.providers)+len(r.invalid))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range r.providers {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			meta := p.Metadata()
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if err := p.Probe(probeCtx); err != nil {
				meta.Available = false
				meta.Error = "Unavailable"
			} else {
				meta.Available = true
			}
			mu.Lock()
			items = append(items, meta)
			mu.Unlock()
		}()
	}
	wg.Wait()
	items = append(items, r.invalid...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ID == "native" {
			return true
		}
		if items[j].ID == "native" {
			return false
		}
		return items[i].Label < items[j].Label
	})
	return items
}

type nativeProvider struct{ dict *grammar.Dictionary }

func (p *nativeProvider) Metadata() Metadata {
	return Metadata{ID: "native", Label: "Chronicle Native", Adapter: "native", DataBoundary: "local", Modes: []string{"standard"}, DefaultEnabled: true}
}
func (p *nativeProvider) Fingerprint(mode string) string { return "native-v1" }
func (p *nativeProvider) Probe(context.Context) error {
	if p.dict == nil {
		return fmt.Errorf("dictionary unavailable")
	}
	return nil
}
func (p *nativeProvider) Check(_ context.Context, text, _ string) ([]grammar.Hit, error) {
	if p.dict == nil {
		return nil, fmt.Errorf("dictionary unavailable")
	}
	return p.dict.Check(text), nil
}

type languageToolProvider struct {
	meta             Metadata
	client           *languagetool.Client
	fingerprintValue string
}

func (p *languageToolProvider) Metadata() Metadata { return p.meta }
func (p *languageToolProvider) Fingerprint(mode string) string {
	return p.fingerprintValue + ":" + normalizedMode(mode)
}
func (p *languageToolProvider) Check(ctx context.Context, text, mode string) ([]grammar.Hit, error) {
	return p.client.Check(ctx, text, normalizedMode(mode))
}
func (p *languageToolProvider) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.client.BaseURL()+"/v2/languages", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

type protocolProvider struct {
	meta                               Metadata
	endpoint, bearer, fingerprintValue string
	http                               *http.Client
}

func (p *protocolProvider) Metadata() Metadata { return p.meta }
func (p *protocolProvider) Fingerprint(mode string) string {
	return p.fingerprintValue + ":" + normalizedMode(mode)
}
func (p *protocolProvider) Probe(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/healthz", nil)
	if p.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
func (p *protocolProvider) Check(ctx context.Context, text, mode string) ([]grammar.Hit, error) {
	payload, _ := json.Marshal(map[string]string{"text": text, "language": "en-US", "mode": normalizedMode(mode)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/v1/check", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("adapter status %d", resp.StatusCode)
	}
	var parsed struct {
		Findings []grammar.Hit `json:"findings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if parsed.Findings == nil {
		parsed.Findings = []grammar.Hit{}
	}
	return parsed.Findings, nil
}

func normalizedMode(mode string) string {
	if mode == "picky" {
		return "picky"
	}
	return "standard"
}

type limitedProvider struct {
	Provider
	slots chan struct{}
}

func limitProvider(provider Provider, concurrency int) Provider {
	if concurrency <= 0 {
		concurrency = 2
	}
	if concurrency > 16 {
		concurrency = 16
	}
	return &limitedProvider{Provider: provider, slots: make(chan struct{}, concurrency)}
}
func (p *limitedProvider) Check(ctx context.Context, text, mode string) ([]grammar.Hit, error) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
		return p.Provider.Check(ctx, text, mode)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
