// Package languagetool is an optional, opt-in proxy to a self-hosted
// LanguageTool sidecar. It exists purely so chronicle-plugin-proofreader can
// offer LanguageTool's checks as an alternative to the built-in pkg/grammar
// checker — every other plugin keeps using pkg/grammar unchanged. See
// pkg/api/grammar.go for how the two engines are selected per-request.
package languagetool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/grammar"
)

// Client talks to a self-hosted LanguageTool's HTTP API (POST /v2/check).
type Client struct {
	baseURL  string
	lang     string
	username string
	apiKey   string
	http     *http.Client
}

// New returns nil when LANGUAGETOOL_URL isn't configured. Callers must
// nil-check before use, exactly like any other unset optional dependency.
func New(cfg *config.Config) *Client {
	if cfg.Grammar.LanguagetoolUrl == "" {
		return nil
	}
	return NewAt(cfg.Grammar.LanguagetoolUrl, cfg.Grammar.LanguagetoolLang, "", "", 10*time.Second)
}

// NewAt creates a client for either a self-hosted or hosted LanguageTool API.
// Credentials remain server-side and are sent only when both values exist.
func NewAt(baseURL, lang, username, apiKey string, timeout time.Duration) *Client {
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		lang:     lang,
		username: username,
		apiKey:   apiKey,
		http:     &http.Client{Timeout: timeout},
	}
}

func (c *Client) BaseURL() string { return c.baseURL }

type ltMatch struct {
	Offset  int    `json:"offset"`
	Length  int    `json:"length"`
	Message string `json:"message"`
	Rule    struct {
		Id        string `json:"id"`
		IssueType string `json:"issueType"`
		Category  struct {
			Id string `json:"id"`
		} `json:"category"`
	} `json:"rule"`
	Replacements []struct {
		Value string `json:"value"`
	} `json:"replacements"`
}

type ltCheckResponse struct {
	Matches []ltMatch `json:"matches"`
}

// Check lints text with the requested LanguageTool level. "picky" enables
// LanguageTool's additional style rules; any other value uses its normal
// level. Keeping the choice server-side means plugins never talk to a remote
// checker directly or need to know the sidecar URL. Callers should treat any
// error as "engine unavailable right now" and fall back to the native checker.
func (c *Client) Check(ctx context.Context, text, level string) ([]grammar.Hit, error) {
	form := url.Values{}
	form.Set("language", c.lang)
	form.Set("text", text)
	if c.username != "" && c.apiKey != "" {
		form.Set("username", c.username)
		form.Set("apiKey", c.apiKey)
	}
	if level == "picky" {
		form.Set("level", "picky")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/check", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("languagetool: unexpected status %d", resp.StatusCode)
	}

	var parsed ltCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	hits := make([]grammar.Hit, 0, len(parsed.Matches))
	for _, m := range parsed.Matches {
		replacements := make([]string, 0, len(m.Replacements))
		for _, r := range m.Replacements {
			if len(replacements) >= 5 {
				break
			}
			replacements = append(replacements, r.Value)
		}
		hits = append(hits, grammar.Hit{
			Start:        m.Offset,
			End:          m.Offset + m.Length,
			Kind:         kindFor(m),
			Message:      m.Message,
			Replacements: replacements,
			RuleID:       m.Rule.Id,
			Category:     m.Rule.Category.Id,
		})
	}
	return hits, nil
}
