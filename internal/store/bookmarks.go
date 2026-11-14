package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const bookmarksVersion = 1

// Bookmarks reads and writes the filesystem bookmark list at a fixed path.
type Bookmarks struct {
	path string
}

// NewBookmarks returns a bookmark store backed by path. No I/O is performed
// until Load or Save.
func NewBookmarks(path string) *Bookmarks {
	return &Bookmarks{path: path}
}

// diskBookmarksV1 is the current bookmark file shape.
type diskBookmarksV1 struct {
	Version int      `json:"version"`
	Paths   []string `json:"paths"`
}

// Load reads the bookmark paths in insertion order. A missing file yields an
// empty list. Empty paths and duplicates are dropped while preserving the
// first occurrence.
func (b *Bookmarks) Load() ([]string, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: read %s: %w", b.path, err)
	}

	var state diskBookmarksV1
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", b.path, err)
	}
	if state.Version != bookmarksVersion {
		return nil, fmt.Errorf(
			"store: %s: unsupported schema version %d (want %d)",
			b.path, state.Version, bookmarksVersion,
		)
	}

	seen := make(map[string]struct{}, len(state.Paths))
	paths := make([]string, 0, len(state.Paths))
	for _, path := range state.Paths {
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths, nil
}

// Save writes the bookmark paths in order. Missing parent directories are
// created with mode 0700. The write is atomic and the final file has mode 0600.
// Save does not mutate its input.
func (b *Bookmarks) Save(paths []string) error {
	if paths == nil {
		paths = []string{}
	}
	paths = append([]string{}, paths...)

	data, err := json.Marshal(diskBookmarksV1{
		Version: bookmarksVersion,
		Paths:   paths,
	})
	if err != nil {
		return fmt.Errorf("store: encode bookmarks: %w", err)
	}
	return writeFileAtomic(b.path, data, ".player2-bookmarks-*")
}
