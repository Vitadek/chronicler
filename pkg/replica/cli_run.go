package replica

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chronicle-server/pkg/config"
)

func BackupDB(db *sql.DB, targetPath string) error {
	escapedPath := strings.ReplaceAll(targetPath, "'", "''")
	query := fmt.Sprintf("VACUUM INTO '%s'", escapedPath)
	_, err := db.Exec(query)
	return err
}

func stamp() string {
	return time.Now().Format("2006-01-02T15-04-05Z07-00")
}

func checkConflicts(db *sql.DB, plan *RestorePlan) []string {
	var conflicts []string

	for _, ms := range plan.Manuscripts {
		var one int
		err := db.QueryRow("SELECT 1 FROM manuscripts WHERE user_id = ? AND id = ?", ms.Record.UserID, ms.Record.ID).Scan(&one)
		if err == nil {
			conflicts = append(conflicts, fmt.Sprintf("manuscript:%s/%s", ms.Record.UserID, ms.Record.ID))
		}
	}

	for _, ch := range plan.Chapters {
		var one int
		err := db.QueryRow("SELECT 1 FROM chapters WHERE user_id = ? AND manuscript_id = ? AND id = ?", ch.Metadata.UserID, ch.Metadata.ManuscriptID, ch.Metadata.ID).Scan(&one)
		if err == nil {
			conflicts = append(conflicts, fmt.Sprintf("chapter:%s/%s/%s", ch.Metadata.UserID, ch.Metadata.ManuscriptID, ch.Metadata.ID))
		}
	}

	for _, pr := range plan.Profiles {
		var one int
		err := db.QueryRow("SELECT 1 FROM profiles WHERE user_id = ?", pr.Record.UserID).Scan(&one)
		if err == nil {
			conflicts = append(conflicts, fmt.Sprintf("profile:%s", pr.Record.UserID))
		}
	}

	for _, bl := range plan.Blobs {
		var one int
		err := db.QueryRow("SELECT 1 FROM storage_blobs WHERE key = ?", bl.LocalKey).Scan(&one)
		if err == nil {
			conflicts = append(conflicts, fmt.Sprintf("blob:%s", bl.LocalKey))
		}
	}

	return conflicts
}

func validCoverBytes(content []byte, ct string) bool {
	if ct == "image/png" {
		pngHeader := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
		return len(content) >= 8 && bytesEqual(content[:8], pngHeader)
	}
	if ct == "image/jpeg" {
		return len(content) >= 3 && content[0] == 0xff && content[1] == 0xd8 && content[2] == 0xff
	}
	// WebP checking (RIFF .... WEBP)
	return len(content) >= 12 &&
		string(content[:4]) == "RIFF" &&
		string(content[8:12]) == "WEBP"
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func RunCLI(cfg *config.Config, db *sql.DB, manager *Manager, args []string) bool {
	if len(args) == 0 {
		return false
	}

	command := args[0]
	cmdArgs := args[1:]

	switch command {
	case "status":
		statusFlags := flag.NewFlagSet("status", flag.ExitOnError)
		_ = statusFlags.Parse(cmdArgs)

		if manager.Provider() != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			manager.recordInitializePublic(manager.Provider().Initialize(ctx))
			cancel()
		}

		status := manager.GetStatus()
		out, _ := json.MarshalIndent(status, "", "  ")
		fmt.Println(string(out))

		if status["state"] == "degraded" {
			os.Exit(2)
		}
		os.Exit(0)

	case "verify":
		verifyFlags := flag.NewFlagSet("verify", flag.ExitOnError)
		prefix := verifyFlags.String("prefix", "", "Replica prefix")
		_ = verifyFlags.Parse(cmdArgs)

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		result, err := manager.Verify(ctx, *prefix)
		if err != nil {
			fmt.Printf("Verification error: %v\n", err)
			os.Exit(1)
		}

		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))

		if len(result.Missing) > 0 || len(result.Unexpected) > 0 || len(result.Mismatched) > 0 || len(result.Unverifiable) > 0 {
			os.Exit(2)
		}
		os.Exit(0)

	case "retry":
		retryFlags := flag.NewFlagSet("retry", flag.ExitOnError)
		key := retryFlags.String("key", "", "Specific outbox key to retry")
		_ = retryFlags.Parse(cmdArgs)

		var keyPtr *string
		if *key != "" {
			keyPtr = key
		}

		retried, err := manager.RetryDeadLetters(keyPtr)
		if err != nil {
			fmt.Printf("Retry error: %v\n", err)
			os.Exit(1)
		}

		_ = manager.ProcessDue(1000)

		summary := map[string]interface{}{
			"retried": retried,
			"status":  manager.GetStatus(),
		}
		out, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(out))
		os.Exit(0)

	case "seed":
		seedFlags := flag.NewFlagSet("seed", flag.ExitOnError)
		_ = seedFlags.Parse(cmdArgs)

		database, enqueuedDb, err := manager.SeedPortableDatabaseManifest()
		if err != nil {
			fmt.Printf("Database seed error: %v\n", err)
			os.Exit(1)
		}

		blobs := manager.SeedLocalBlobs()
		targetChanged, manifest := manager.ReconcileReplicaTarget()

		_ = manager.ProcessDue(50)

		summary := map[string]interface{}{
			"database":      map[string]int{"checked": database, "enqueued": enqueuedDb},
			"blobs":         blobs,
			"targetChanged": targetChanged,
			"manifest":      manifest,
			"status":        manager.GetStatus(),
		}
		out, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(out))
		os.Exit(0)

	case "backup":
		backupFlags := flag.NewFlagSet("backup", flag.ExitOnError)
		output := backupFlags.String("output", "", "Output file path")
		_ = backupFlags.Parse(cmdArgs)

		outPath := *output
		if outPath == "" {
			outPath = filepath.Join(cfg.DataDir, fmt.Sprintf("chronicle-backup-%s.db", stamp()))
		} else {
			var errAbs error
			outPath, errAbs = filepath.Abs(outPath)
			if errAbs != nil {
				fmt.Printf("Invalid output path: %v\n", errAbs)
				os.Exit(1)
			}
		}

		if _, errCheck := os.Stat(outPath); errCheck == nil {
			fmt.Printf("Refusing to overwrite existing backup: %s\n", outPath)
			os.Exit(1)
		}

		if errMk := os.MkdirAll(filepath.Dir(outPath), 0755); errMk != nil {
			fmt.Printf("Failed to create directory for backup: %v\n", errMk)
			os.Exit(1)
		}

		if errBackup := BackupDB(db, outPath); errBackup != nil {
			fmt.Printf("Backup failed: %v\n", errBackup)
			os.Exit(1)
		}

		summary := map[string]string{
			"backupPath": outPath,
		}
		out, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(out))
		os.Exit(0)

	case "restore":
		restoreFlags := flag.NewFlagSet("restore", flag.ExitOnError)
		user := restoreFlags.String("user", "", "Filter by user ID")
		apply := restoreFlags.Bool("apply", false, "Apply the changes")
		force := restoreFlags.Bool("force", false, "Force applying and overwriting conflicts")
		_ = restoreFlags.Parse(cmdArgs)

		ctx := context.Background()

		plan, errPlan := manager.BuildRestorePlan(ctx, *user)
		if errPlan != nil {
			fmt.Printf("Restore plan failed: %v\n", errPlan)
			os.Exit(1)
		}

		conflicts := checkConflicts(db, plan)

		summary := map[string]interface{}{
			"dryRun":       !*apply,
			"user":         *user,
			"manuscripts":  len(plan.Manuscripts),
			"chapters":     len(plan.Chapters),
			"profiles":     len(plan.Profiles),
			"blobs":        len(plan.Blobs),
			"ignored":      len(plan.Ignored),
			"conflicts":    len(conflicts),
			"conflictKeys": conflicts,
		}

		if !*apply {
			out, _ := json.MarshalIndent(summary, "", "  ")
			fmt.Println(string(out))
			os.Exit(0)
		}

		if len(conflicts) > 0 && !*force {
			fmt.Printf("Restore would overwrite %d existing record(s). Run the dry-run, then repeat with --apply --force if that is intentional.\n", len(conflicts))
			os.Exit(1)
		}

		// Hydrate restore blobs from remote provider
		totalHydratedBytes := int64(0)
		for i, bl := range plan.Blobs {
			bytesVal, errGet := manager.Provider().Get(ctx, bl.RemoteKey)
			if errGet != nil {
				fmt.Printf("Failed to download restore blob %s: %v\n", bl.RemoteKey, errGet)
				os.Exit(1)
			}
			totalHydratedBytes += int64(len(bytesVal))
			if totalHydratedBytes > 1024*1024*1024 {
				fmt.Println("Restore blob payload exceeds the 1 GiB safety limit.")
				os.Exit(1)
			}

			if bl.ContentType == "application/json" {
				if len(bytesVal) > 128*1024 {
					fmt.Printf("Settings payload is too large: %s\n", bl.RemoteKey)
					os.Exit(1)
				}
				var check map[string]interface{}
				if errUn := json.Unmarshal(bytesVal, &check); errUn != nil {
					fmt.Printf("Settings payload is not valid JSON: %s\n", bl.RemoteKey)
					os.Exit(1)
				}
			} else {
				if len(bytesVal) > 8*1024*1024 {
					fmt.Printf("Cover payload is too large: %s\n", bl.RemoteKey)
					os.Exit(1)
				}
				if !validCoverBytes(bytesVal, bl.ContentType) {
					fmt.Printf("Cover bytes do not match their extension: %s\n", bl.RemoteKey)
					os.Exit(1)
				}
			}
			plan.Blobs[i].Content = bytesVal
		}

		// Create pre-restore safety backup
		if errMk := os.MkdirAll(cfg.DataDir, 0755); errMk != nil {
			fmt.Printf("Failed to ensure data directory for safety backup: %v\n", errMk)
			os.Exit(1)
		}
		safetyBackupPath := filepath.Join(cfg.DataDir, fmt.Sprintf("chronicle-before-restore-%s.db", stamp()))
		if errSafety := BackupDB(db, safetyBackupPath); errSafety != nil {
			fmt.Printf("Failed to create pre-restore safety backup: %v\n", errSafety)
			os.Exit(1)
		}

		// Apply the plan
		applyResult, errApply := manager.ApplyRestorePlan(plan)
		if errApply != nil {
			fmt.Printf("Restore apply failed: %v\n", errApply)
			os.Exit(1)
		}

		// Re-seed manifests and outbox
		databaseChecked, enqueuedDb, _ := manager.SeedPortableDatabaseManifest()
		targetChanged, manifest := manager.ReconcileReplicaTarget()

		// Run SQLite checkpoint
		_, _ = db.Exec("PRAGMA wal_checkpoint(PASSIVE)")

		resultSummary := map[string]interface{}{
			"dryRun":       false,
			"user":         *user,
			"manuscripts":  len(plan.Manuscripts),
			"chapters":     len(plan.Chapters),
			"profiles":     len(plan.Profiles),
			"blobs":        len(plan.Blobs),
			"ignored":      len(plan.Ignored),
			"conflicts":    len(conflicts),
			"backupPath":   safetyBackupPath,
			"cascaded":     applyResult.CascadedChapters,
			"skipped":      applyResult.SkippedCovers,
			"database":      map[string]int{"checked": databaseChecked, "enqueued": enqueuedDb},
			"targetChanged": targetChanged,
			"manifest":      manifest,
		}

		out, _ := json.MarshalIndent(resultSummary, "", "  ")
		fmt.Println(string(out))
		os.Exit(0)

	default:
		// Not a known administrative CLI command, let the main caller proceed to web server startup
		return false
	}

	return true
}
