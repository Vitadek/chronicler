package db

import (
	"os"
	"path/filepath"
)

const (
	StagedImport = "import-staged.db"
	ImportMarker = "import-staged.marker"
)

// ApplyPendingImport swaps chronicle.db with the staged import file at boot.
// Called before opening the database connection.
func ApplyPendingImport(dataDir string) bool {
	marker := filepath.Join(dataDir, ImportMarker)
	staged := filepath.Join(dataDir, StagedImport)

	_, errMarker := os.Stat(marker)
	_, errStaged := os.Stat(staged)

	if os.IsNotExist(errMarker) || os.IsNotExist(errStaged) {
		// Cleanup stale staging files if only one exists
		_ = os.Remove(marker)
		_ = os.Remove(staged)
		return false
	}

	primary := filepath.Join(dataDir, PrimaryDB)

	// Remove WAL and SHM sidecars so they don't corrupt the fresh import
	_ = os.Remove(primary + "-wal")
	_ = os.Remove(primary + "-shm")

	if err := os.Rename(staged, primary); err != nil {
		return false
	}

	_ = os.Remove(marker)
	return true
}
