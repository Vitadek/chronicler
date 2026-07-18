package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SeedPlugins finds any seed plugins inside process.cwd()/plugins-seed or dist/plugins-seed,
// copies them to dataDir/plugins, and runs esbuild compilation on them.
func SeedPlugins(dataDir string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	candidates := []string{
		filepath.Join(cwd, "plugins-seed"),
		filepath.Join(cwd, "dist", "plugins-seed"),
	}

	var seedDir string
	for _, path := range candidates {
		if fi, err := os.Stat(path); err == nil && fi.IsDir() {
			seedDir = path
			break
		}
	}

	if seedDir == "" {
		return nil // No seed plugins dir found
	}

	pluginsDir := filepath.Join(dataDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(seedDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() || !ValidatePluginID(entry.Name()) {
			continue
		}

		src := filepath.Join(seedDir, entry.Name())
		dest := filepath.Join(pluginsDir, entry.Name())

		// Skip if already installed
		if _, err := os.Stat(dest); err == nil {
			continue
		}

		fmt.Printf("[plugins] Seeding plugin %s...\n", entry.Name())

		// Copy recursively
		if err := copyDir(src, dest); err != nil {
			fmt.Printf("[plugins] Failed to copy seed plugin %s: %v\n", entry.Name(), err)
			continue
		}

		// Write meta.json
		meta := RepoMeta{Source: "seed"}
		_ = WriteMeta(dest, meta)

		// Build plugin
		_, buildErr := BuildPlugin(pluginsDir, entry.Name(), cwd)
		if buildErr != nil {
			fmt.Printf("[plugins] Failed to compile seed plugin %s: %v\n", entry.Name(), buildErr)
		} else {
			fmt.Printf("[plugins] Successfully seeded and compiled plugin %s\n", entry.Name())
		}
	}

	return nil
}

func copyDir(src string, dest string) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		destPath := filepath.Join(dest, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, destPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, destPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src string, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
