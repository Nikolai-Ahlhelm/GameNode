package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Staging and backup files live beside the executable so every step is a
// same-directory rename: atomic on one volume, and never a cross-device copy.
// (A data directory on another volume must not turn the swap into a copy.)

func siblingPath(exe, marker string) string {
	ext := filepath.Ext(exe)
	base := exe[:len(exe)-len(ext)]
	return base + "." + marker + ext
}

// StagedPath is where a downloaded, not-yet-installed binary is kept.
func StagedPath(exe string) string { return siblingPath(exe, "staged") }

// BackupPath is where the previously installed binary is kept after an update
// so a failed update can be rolled back.
func BackupPath(exe string) string { return siblingPath(exe, "previous") }

func failedPath(exe string) string { return siblingPath(exe, "failed") }

// renameRetry tolerates the short-lived sharing violations Windows antivirus
// and indexers cause on a freshly written executable.
func renameRetry(from, to string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 150 * time.Millisecond)
	}
	return err
}

// replaceExecutable installs staged as exe, keeping the old binary as the
// backup. Renaming a running executable is permitted on both Windows and
// Linux (only overwriting or deleting it is not), so the running process is
// undisturbed. On any failure the original executable is restored.
func replaceExecutable(exe, staged string) (string, error) {
	backup := BackupPath(exe)
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove stale backup: %w", err)
	}
	if err := renameRetry(exe, backup); err != nil {
		return "", fmt.Errorf("move current executable aside: %w", err)
	}
	if err := renameRetry(staged, exe); err != nil {
		if restoreErr := renameRetry(backup, exe); restoreErr != nil {
			return "", fmt.Errorf("install staged executable failed (%v) and the original could not be restored: %w", err, restoreErr)
		}
		return "", fmt.Errorf("install staged executable: %w", err)
	}
	return backup, nil
}

// restoreBackup puts the backup back in place of the current (failed)
// executable. The failed binary is kept aside for diagnosis, not deleted.
func restoreBackup(exe, backup string) error {
	if _, err := os.Stat(backup); err != nil {
		return fmt.Errorf("backup executable unavailable: %w", err)
	}
	failed := failedPath(exe)
	if err := os.Remove(failed); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale failed binary: %w", err)
	}
	if err := renameRetry(exe, failed); err != nil {
		return fmt.Errorf("move failed executable aside: %w", err)
	}
	if err := renameRetry(backup, exe); err != nil {
		if undo := renameRetry(failed, exe); undo != nil {
			return fmt.Errorf("restore backup failed (%v) and the failed binary could not be put back: %w", err, undo)
		}
		return fmt.Errorf("restore backup: %w", err)
	}
	return nil
}
