// Package atomicfile publishes small local-state files without exposing a
// partially written destination to concurrent readers.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes data to a private sibling temporary file, syncs it, and
// atomically replaces path. Replacement uses the native replace primitive on
// Windows rather than deleting the prior destination first.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ptv-atomic-*")
	if err != nil {
		return fmt.Errorf("creating atomic temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("securing atomic temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing atomic temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing atomic temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing atomic temporary file: %w", err)
	}
	if err := Replace(tmpPath, path); err != nil {
		return fmt.Errorf("replacing atomic destination: %w", err)
	}
	remove = false
	return nil
}

// Replace atomically replaces destination with an already closed, synced
// sibling file. Source and destination must share a directory so the operation
// cannot cross filesystems.
func Replace(source, destination string) error {
	sourceDir, err := filepath.Abs(filepath.Dir(source))
	if err != nil {
		return err
	}
	destinationDir, err := filepath.Abs(filepath.Dir(destination))
	if err != nil {
		return err
	}
	if filepath.Clean(sourceDir) != filepath.Clean(destinationDir) {
		return fmt.Errorf("atomic replacement requires sibling paths")
	}
	if err := replace(source, destination); err != nil {
		return err
	}
	return syncDirectory(destinationDir)
}

// SyncDirectory makes a completed entry update durable where the platform
// supports directory fsync. It is a no-op on Windows, where the native replace
// primitive is requested with write-through semantics.
func SyncDirectory(path string) error {
	return syncDirectory(path)
}
