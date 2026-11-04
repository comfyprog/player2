// Package store persists the playback queue as versioned JSON.
//
// Load prunes references to missing or non-regular local files and preserves
// the current item when it survives. Save writes atomically through a
// temporary file and leaves the destination with mode 0600. The package
// performs local file I/O only, starts no goroutines, and keeps no cache; the
// caller serializes access.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"player2/internal/queue"
)

// currentVersion is the only JSON schema version this package reads or writes.
const currentVersion = 1

// Store reads and writes a queue file at a fixed path.
type Store struct {
	path string
}

// New returns a store backed by path. No I/O is performed until Load or Save.
func New(path string) *Store {
	return &Store{path: path}
}

// diskState is the on-disk envelope. A queue file written by Save looks like
// {"version":1,"queue":{"items":...}}.
type diskState struct {
	Version int            `json:"version"`
	Queue   queue.Snapshot `json:"queue"`
}

// Load reads the persisted queue. A missing file yields an empty snapshot and
// is not an error. Items whose audio or CUE file is missing or not a regular
// file are pruned; skipped reports how many items were dropped.
func (s *Store) Load() (queue.Snapshot, int, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return emptySnapshot(), 0, nil
		}
		return queue.Snapshot{}, 0, fmt.Errorf("store: read %s: %w", s.path, err)
	}

	var state diskState
	if err := json.Unmarshal(data, &state); err != nil {
		return queue.Snapshot{}, 0, fmt.Errorf("store: parse %s: %w", s.path, err)
	}
	if state.Version != currentVersion {
		return queue.Snapshot{}, 0, fmt.Errorf(
			"store: %s: unsupported schema version %d (want %d)",
			s.path, state.Version, currentVersion,
		)
	}

	snapshot := state.Queue
	currentID := uint64(0)
	currentIndex := snapshot.Current
	hasCurrent := currentIndex >= 0 && currentIndex < len(snapshot.Items)
	if hasCurrent {
		currentID = snapshot.Items[currentIndex].ID
	}

	kept := make([]queue.Item, 0, len(snapshot.Items))
	keptCurrent := -1
	for i, item := range snapshot.Items {
		if !regularFile(item.Path) {
			continue
		}
		if item.CuePath != "" && !regularFile(item.CuePath) {
			continue
		}
		if hasCurrent && keptCurrent == -1 && isCurrent(item, i, currentIndex, currentID) {
			keptCurrent = len(kept)
		}
		kept = append(kept, item)
	}

	skipped := len(snapshot.Items) - len(kept)
	switch {
	case len(kept) == 0:
		keptCurrent = -1
	case keptCurrent == -1:
		keptCurrent = snapshot.Current
		if keptCurrent >= len(kept) {
			keptCurrent = len(kept) - 1
		}
		if keptCurrent < -1 {
			keptCurrent = -1
		}
	}

	snapshot.Items = kept
	snapshot.Current = keptCurrent
	return snapshot, skipped, nil
}

// Save writes snapshot to the store's path. Missing parent directories are
// created with mode 0700. The write is atomic and the final file has mode
// 0600. Save does not mutate snapshot.
func (s *Store) Save(snapshot queue.Snapshot) error {
	data, err := json.Marshal(diskState{Version: currentVersion, Queue: snapshot})
	if err != nil {
		return fmt.Errorf("store: encode queue: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".player2-queue-*")
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
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("store: replace %s: %w", s.path, err)
	}
	cleanup = false
	return nil
}

// emptySnapshot is the state used when no queue file exists.
func emptySnapshot() queue.Snapshot {
	return queue.Snapshot{
		Items:   []queue.Item{},
		Current: -1,
	}
}

// regularFile reports whether path names an existing regular file. Symlinks to
// regular files count as regular.
func regularFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// isCurrent identifies the original current item. IDs are authoritative; the
// index is used as a fallback for snapshots whose IDs are still zero (for
// example hand-built test data).
func isCurrent(item queue.Item, index, originalIndex int, id uint64) bool {
	if id != 0 {
		return item.ID == id
	}
	return index == originalIndex
}
