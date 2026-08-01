package grammarproviders

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileRejectsRawSecretsAndInsecureCloudURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.yml")
	err := os.WriteFile(path, []byte(`version: 1
providers:
  - id: cloud
    label: Cloud
    adapter: languagetool
    endpoint: http://example.com
    data_boundary: cloud
    secrets:
      api_key: raw-value
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	providers, errs := LoadFile(path)
	if len(providers) != 0 || len(errs) == 0 {
		t.Fatalf("providers=%v errs=%v", providers, errs)
	}
}

func TestLoadFileResolvesEnvironmentSecret(t *testing.T) {
	t.Setenv("TEST_LT_KEY", "secret")
	path := filepath.Join(t.TempDir(), "providers.yml")
	err := os.WriteFile(path, []byte(`version: 1
providers:
  - id: cloud
    label: Cloud
    adapter: languagetool
    endpoint: https://example.com
    data_boundary: cloud
    modes: [standard, picky]
    secrets:
      api_key: env:TEST_LT_KEY
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	providers, errs := LoadFile(path)
	if len(errs) != 0 || len(providers) != 1 {
		t.Fatalf("providers=%v errs=%v", providers, errs)
	}
}
