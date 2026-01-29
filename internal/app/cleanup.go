package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// CleanupOldOutputDirs removes directories in root that are older than retentionDays.
// It uses ModTime as a proxy for age.
func CleanupOldOutputDirs(root string, retentionDays int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		slog.Error("Cleanup failed to read root dir", "dir", root, "error", err)
		return
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// User requested to use system tools to retrieve date, ModTime is the standard system call for this.
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(root, entry.Name())
			slog.Info("Removing old output directory", "dir", path, "age_days", int(time.Since(info.ModTime()).Hours()/24))
			if err := os.RemoveAll(path); err != nil {
				slog.Error("Failed to remove old directory", "dir", path, "error", err)
			}
		}
	}
}
