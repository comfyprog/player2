package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path through a same-directory temporary file.
// It creates missing parent directories with mode 0700, syncs the file, and
// renames it into place so readers never observe a partial write.
func writeFileAtomic(path string, data []byte, pattern string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("store: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write temporary file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: set temporary file mode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: replace %s: %w", path, err)
	}
	cleanup = false
	return nil
}
