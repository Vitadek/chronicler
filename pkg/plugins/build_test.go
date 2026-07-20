package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPluginWritesLoadableModule(t *testing.T) {
	pluginsDir := t.TempDir()
	id := "chronicle.proofreader"
	dir := filepath.Join(pluginsDir, id)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"id":"chronicle.proofreader","entry":"src/index.ts","requires":["host:grammar"]}`)
	if err := os.WriteFile(filepath.Join(dir, "chronicle-plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "index.ts"), []byte(`export default { apiVersion: 4, activate() {} };`), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, err := BuildPlugin(pluginsDir, id, t.TempDir())
	if err != nil || !ok {
		t.Fatalf("BuildPlugin() = %v, %v", ok, err)
	}
	module, err := os.ReadFile(filepath.Join(dir, BuildDir, OutFile))
	if err != nil {
		t.Fatalf("successful build did not write %s: %v", OutFile, err)
	}
	if len(module) == 0 {
		t.Fatalf("successful build wrote an empty %s", OutFile)
	}
}
