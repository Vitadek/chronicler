package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

var SharedExternals = []string{
	"react",
	"react/jsx-runtime",
	"react/jsx-dev-runtime",
	"react-dom",
	"@tiptap/core",
	"@tiptap/react",
	"@tiptap/react/menus",
	"@tiptap/pm/state",
	"@tiptap/pm/view",
	"@tiptap/pm/model",
	"motion/react",
	"lucide-react",
	"@chronicle/plugin-api",
}

const (
	BuildDir     = ".chronicle-build"
	OutFile      = "plugin.js"
	ErrFile      = "error.txt"
	DepsHashFile = "deps.hash"
	NpmTimeout   = 120 * time.Second
)

func installDependencies(dir string, id string, dependencies map[string]string) error {
	buildDir := filepath.Join(dir, BuildDir)
	hashFile := filepath.Join(dir, BuildDir, DepsHashFile)
	modulesDir := filepath.Join(buildDir, "node_modules")

	if len(dependencies) == 0 {
		os.RemoveAll(modulesDir)
		os.Remove(hashFile)
		return nil
	}

	// Calculate dependency map hash deterministically
	var keys []string
	for k := range dependencies {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var orderedDeps []string
	for _, k := range keys {
		orderedDeps = append(orderedDeps, fmt.Sprintf("%s:%s", k, dependencies[k]))
	}
	hashInput := strings.Join(orderedDeps, ",")
	h := sha256.New()
	h.Write([]byte(hashInput))
	currentHash := hex.EncodeToString(h.Sum(nil))

	cachedHashBytes, err := os.ReadFile(hashFile)
	if err == nil && string(cachedHashBytes) == currentHash {
		if _, statErr := os.Stat(modulesDir); statErr == nil {
			return nil // No change
		}
	}

	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return err
	}

	pkgJson := map[string]interface{}{
		"name":         fmt.Sprintf("chronicle-plugin-%s", id),
		"version":      "0.0.0",
		"private":      true,
		"dependencies": dependencies,
	}

	pkgJsonBytes, err := json.MarshalIndent(pkgJson, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(buildDir, "package.json"), pkgJsonBytes, 0644); err != nil {
		return err
	}

	// Shell out to execute npm install
	cmd := exec.Command("npm", "install", "--ignore-scripts", "--omit=dev", "--no-audit", "--no-fund", "--loglevel=error")
	cmd.Dir = buildDir

	// Implement custom timeout for the command execution
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case <-time.After(NpmTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		os.Remove(hashFile)
		return fmt.Errorf("npm install timed out after %v", NpmTimeout)
	case runErr := <-done:
		if runErr != nil {
			os.Remove(hashFile)
			return fmt.Errorf("npm install failed: %w", runErr)
		}
	}

	return os.WriteFile(hashFile, []byte(currentHash), 0644)
}

func BuildPlugin(pluginsDir string, id string, projectCwd string) (bool, error) {
	dir, err := PluginDir(pluginsDir, id)
	if err != nil {
		return false, err
	}

	buildFolder := filepath.Join(dir, BuildDir)
	outfile := filepath.Join(buildFolder, OutFile)
	errfile := filepath.Join(buildFolder, ErrFile)

	_ = os.MkdirAll(buildFolder, 0755)
	_ = os.Remove(errfile)

	// Custom manifest reader structure to extract "entry"
	var manifest struct {
		Entry        string            `json:"entry"`
		Dependencies map[string]string `json:"dependencies"`
	}

	manifestBytes, err := os.ReadFile(filepath.Join(dir, "chronicle-plugin.json"))
	if err != nil {
		writeBuildError(errfile, err.Error())
		return false, err
	}

	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		writeBuildError(errfile, err.Error())
		return false, err
	}

	entry := filepath.Clean(filepath.Join(dir, manifest.Entry))
	if !strings.HasPrefix(entry, dir+string(filepath.Separator)) {
		errStr := fmt.Sprintf("entry %q escapes the plugin directory", manifest.Entry)
		writeBuildError(errfile, errStr)
		return false, errors.New(errStr)
	}

	if _, err := os.Stat(entry); os.IsNotExist(err) {
		errStr := fmt.Sprintf("entry %q does not exist in the repo", manifest.Entry)
		writeBuildError(errfile, errStr)
		return false, errors.New(errStr)
	}

	// Install dependencies
	if err := installDependencies(dir, id, manifest.Dependencies); err != nil {
		writeBuildError(errfile, err.Error())
		return false, err
	}

	// Compile using esbuild Go API
	res := api.Build(api.BuildOptions{
		EntryPoints:       []string{entry},
		Outfile:           outfile,
		Bundle:            true,
		Format:            api.FormatCommonJS,
		Platform:          api.PlatformBrowser,
		Target:            api.ES2020,
		JSX:               api.JSXAutomatic,
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		External:          SharedExternals,
		LogLevel:          api.LogLevelSilent,
		Write:             true,
		AbsWorkingDir:     dir,
		NodePaths: []string{
			filepath.Join(dir, BuildDir, "node_modules"),
			filepath.Join(projectCwd, "node_modules"),
		},
	})

	if len(res.Errors) > 0 {
		var errMsgs []string
		for _, e := range res.Errors {
			errMsgs = append(errMsgs, fmt.Sprintf("%s:%d:%d: %s", e.Location.File, e.Location.Line, e.Location.Column, e.Text))
		}
		joinedErr := strings.Join(errMsgs, "\n")
		writeBuildError(errfile, joinedErr)
		_ = os.Remove(outfile)
		return false, fmt.Errorf("esbuild compilation errors:\n%s", joinedErr)
	}

	return true, nil
}

func writeBuildError(errfile string, errStr string) {
	_ = os.WriteFile(errfile, []byte(errStr), 0644)
}

func BuiltModulePath(pluginsDir string, id string) (string, error) {
	dir, err := PluginDir(pluginsDir, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, BuildDir, OutFile), nil
}
