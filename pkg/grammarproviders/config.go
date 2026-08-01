package grammarproviders

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type FileConfig struct {
	Version   int              `yaml:"version"`
	Providers []ProviderConfig `yaml:"providers"`
}

type ProviderConfig struct {
	ID                string            `yaml:"id"`
	Label             string            `yaml:"label"`
	Adapter           string            `yaml:"adapter"`
	Endpoint          string            `yaml:"endpoint"`
	Language          string            `yaml:"language"`
	DataBoundary      string            `yaml:"data_boundary"`
	Modes             []string          `yaml:"modes"`
	DefaultEnabled    bool              `yaml:"default_enabled"`
	AllowBackground   bool              `yaml:"allow_background"`
	AllowInsecureHTTP bool              `yaml:"allow_insecure_http"`
	Timeout           string            `yaml:"timeout"`
	Concurrency       int               `yaml:"concurrency"`
	Secrets           map[string]string `yaml:"secrets"`
}

var providerID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

func LoadFile(path string) ([]ProviderConfig, []error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, []error{fmt.Errorf("read grammar providers file: %w", err)}
	}
	var parsed FileConfig
	if err := yaml.Unmarshal(b, &parsed); err != nil {
		return nil, []error{fmt.Errorf("parse grammar providers file: %w", err)}
	}
	if parsed.Version != 1 {
		return nil, []error{fmt.Errorf("grammar providers version %d is unsupported; expected 1", parsed.Version)}
	}
	seen := map[string]bool{}
	var valid []ProviderConfig
	var errs []error
	for _, cfg := range parsed.Providers {
		if err := validateProvider(cfg, seen); err != nil {
			errs = append(errs, err)
			continue
		}
		seen[cfg.ID] = true
		valid = append(valid, cfg)
	}
	return valid, errs
}

func validateProvider(cfg ProviderConfig, seen map[string]bool) error {
	if !providerID.MatchString(cfg.ID) {
		return fmt.Errorf("provider id %q must match %s", cfg.ID, providerID.String())
	}
	if seen[cfg.ID] || cfg.ID == "native" {
		return fmt.Errorf("provider id %q is duplicated or reserved", cfg.ID)
	}
	if cfg.Adapter != "languagetool" && cfg.Adapter != "chronicle-v1" {
		return fmt.Errorf("provider %q adapter must be languagetool or chronicle-v1", cfg.ID)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("provider %q endpoint must be an absolute HTTP(S) URL", cfg.ID)
	}
	if u.Scheme == "http" && !cfg.AllowInsecureHTTP {
		return fmt.Errorf("provider %q endpoint uses HTTP; set allow_insecure_http only for a trusted LAN/container network", cfg.ID)
	}
	if cfg.DataBoundary != "local" && cfg.DataBoundary != "cloud" {
		return fmt.Errorf("provider %q data_boundary must be local or cloud", cfg.ID)
	}
	for _, mode := range cfg.Modes {
		if mode != "standard" && mode != "picky" {
			return fmt.Errorf("provider %q mode %q must be standard or picky", cfg.ID, mode)
		}
		if mode == "picky" && cfg.Adapter != "languagetool" {
			return fmt.Errorf("provider %q may use picky mode only with the languagetool adapter", cfg.ID)
		}
	}
	for name, ref := range cfg.Secrets {
		if _, err := resolveSecret(ref); err != nil {
			return fmt.Errorf("provider %q secret %q: %w", cfg.ID, name, err)
		}
	}
	return nil
}

func resolveSecret(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "env:"):
		name := strings.TrimSpace(strings.TrimPrefix(ref, "env:"))
		if name == "" || os.Getenv(name) == "" {
			return "", fmt.Errorf("environment secret %q is unset", name)
		}
		return os.Getenv(name), nil
	case strings.HasPrefix(ref, "file:"):
		path := strings.TrimSpace(strings.TrimPrefix(ref, "file:"))
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read secret file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	default:
		return "", fmt.Errorf("must use env:NAME or file:/run/secrets/name")
	}
}

func providerTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return 10 * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Second || d > 60*time.Second {
		return 0, fmt.Errorf("timeout must be between 1s and 60s")
	}
	return d, nil
}

func fingerprint(cfg ProviderConfig, secrets map[string]string) string {
	parts := []string{cfg.ID, cfg.Adapter, cfg.Endpoint, cfg.Language, cfg.DataBoundary, cfg.Timeout, strconv.Itoa(cfg.Concurrency), strings.Join(cfg.Modes, ",")}
	for _, name := range []string{"username", "api_key", "bearer_token"} {
		parts = append(parts, name+"="+secrets[name])
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}
