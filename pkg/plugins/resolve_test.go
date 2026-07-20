package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmptyResolutionCollectionsEncodeAsArrays(t *testing.T) {
	resolution := Resolve([]ResolveInput{{PluginDeps: PluginDeps{ID: "chronicle.proofreader"}}}, []string{"host:grammar"})
	payload, err := json.Marshal(resolution)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, field := range []string{"missing", "unmetWants", "conflictsWith", "shadowedCore", "activationOrder"} {
		if strings.Contains(encoded, `"`+field+`":null`) {
			t.Fatalf("%s must encode as []: %s", field, encoded)
		}
	}
}

func TestManifestAndDiskPluginCollectionsEncodeAsArrays(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "chronicle.proofreader")
	if err := os.Mkdir(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := []byte(`{"id":"chronicle.proofreader","name":"Proofreader","version":"2.1.0"}`)
	if err := os.WriteFile(filepath.Join(pluginDir, "chronicle-plugin.json"), manifestJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	manifest, err := ReadManifest(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := DescribePlugin(dir, "chronicle.proofreader")
	if err != nil {
		t.Fatal(err)
	}

	for label, value := range map[string]interface{}{"manifest": manifest, "disk plugin": disk, "update status": emptyUpdateStatus()} {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		encoded := string(payload)
		for _, field := range []string{"provides", "requires", "wants", "conflicts", "replaces", "incoming"} {
			if strings.Contains(encoded, `"`+field+`":null`) {
				t.Fatalf("%s %s must encode as []: %s", label, field, encoded)
			}
		}
	}
}
