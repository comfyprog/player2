// Package store persists playlist tabs and filesystem bookmarks as versioned
// JSON.
//
// LoadTabs prunes references to missing or non-regular local files in every
// tab and preserves each tab's current item when it survives. SaveTabs and the
// bookmark store write atomically through a temporary file and leave the
// destination with mode 0600. Version 1 queue files are read as a single
// unnamed tab and version 2 files as unnamed tabs. The package performs local
// file I/O only, starts no goroutines, and keeps no cache; the caller
// serializes access.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"player2/internal/queue"
)

const (
	// currentVersion is the JSON schema version this package writes.
	currentVersion = 3

	// maxTabs is the fixed number of playlist tabs supported by the UI.
	maxTabs = 10
)

// Store reads and writes a queue file at a fixed path.
type Store struct {
	path string
}

// New returns a store backed by path. No I/O is performed until LoadTabs or
// SaveTabs.
func New(path string) *Store {
	return &Store{path: path}
}

// diskHeader is decoded first so the rest of the file can be decoded according
// to its schema version.
type diskHeader struct {
	Version int `json:"version"`
}

// diskStateV1 is the original single-queue envelope.
type diskStateV1 struct {
	Version int            `json:"version"`
	Queue   queue.Snapshot `json:"queue"`
}

// diskStateV2 is the original tabbed envelope, without tab names.
type diskStateV2 struct {
	Version   int              `json:"version"`
	ActiveTab int              `json:"active_tab"`
	Tabs      []queue.Snapshot `json:"tabs"`
}

// diskStateV3 is the current tabbed envelope with optional tab names. Tabs is
// ordered by tab index.
type diskStateV3 struct {
	Version   int                 `json:"version"`
	ActiveTab int                 `json:"active_tab"`
	Tabs      []queue.TabSnapshot `json:"tabs"`
}

// LoadTabs reads the persisted playlist tabs. A missing file yields active tab
// 0 and no tabs. Version 1 files are returned as a single unnamed tab at index
// 0, and version 2 files as unnamed tabs. Items whose audio or CUE file is
// missing or not a regular file are pruned; skipped reports how many items were
// dropped across all tabs.
func (s *Store) LoadTabs() (active int, tabs []queue.TabSnapshot, skipped int, err error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil, 0, nil
		}
		return 0, nil, 0, fmt.Errorf("store: read %s: %w", s.path, err)
	}

	var header diskHeader
	if err := json.Unmarshal(data, &header); err != nil {
		return 0, nil, 0, fmt.Errorf("store: parse %s: %w", s.path, err)
	}

	switch header.Version {
	case 1:
		var state diskStateV1
		if err := json.Unmarshal(data, &state); err != nil {
			return 0, nil, 0, fmt.Errorf("store: parse %s: %w", s.path, err)
		}
		snapshot, skipped := pruneSnapshot(state.Queue)
		return 0, []queue.TabSnapshot{{Snapshot: snapshot}}, skipped, nil

	case 2:
		var state diskStateV2
		if err := json.Unmarshal(data, &state); err != nil {
			return 0, nil, 0, fmt.Errorf("store: parse %s: %w", s.path, err)
		}
		if len(state.Tabs) > maxTabs {
			return 0, nil, 0, fmt.Errorf(
				"store: %s: %d tabs exceeds maximum %d",
				s.path, len(state.Tabs), maxTabs,
			)
		}
		active, tabs, skipped := pruneTabs(state.ActiveTab, state.Tabs)
		return active, tabs, skipped, nil

	case 3:
		var state diskStateV3
		if err := json.Unmarshal(data, &state); err != nil {
			return 0, nil, 0, fmt.Errorf("store: parse %s: %w", s.path, err)
		}
		if len(state.Tabs) > maxTabs {
			return 0, nil, 0, fmt.Errorf(
				"store: %s: %d tabs exceeds maximum %d",
				s.path, len(state.Tabs), maxTabs,
			)
		}

		tabs := make([]queue.TabSnapshot, len(state.Tabs))
		for i, tab := range state.Tabs {
			var dropped int
			tabs[i] = tab
			tabs[i].Snapshot, dropped = pruneSnapshot(tab.Snapshot)
			skipped += dropped
		}
		return clampActive(state.ActiveTab, len(tabs)), tabs, skipped, nil

	default:
		return 0, nil, 0, fmt.Errorf(
			"store: %s: unsupported schema version %d (want 1, 2 or %d)",
			s.path, header.Version, currentVersion,
		)
	}
}

// pruneTabs converts legacy unnamed snapshots into tab snapshots and prunes
// every queue.
func pruneTabs(active int, snapshots []queue.Snapshot) (int, []queue.TabSnapshot, int) {
	tabs := make([]queue.TabSnapshot, len(snapshots))
	skipped := 0
	for i, snapshot := range snapshots {
		var dropped int
		tabs[i].Snapshot, dropped = pruneSnapshot(snapshot)
		skipped += dropped
	}
	return clampActive(active, len(tabs)), tabs, skipped
}

// clampActive keeps the visible tab inside the loaded tab set.
func clampActive(active, count int) int {
	if active < 0 || active >= count {
		return 0
	}
	return active
}

// SaveTabs writes all playlist tabs to the store's path. Missing parent
// directories are created with mode 0700. The write is atomic and the final
// file has mode 0600. SaveTabs does not mutate its input.
func (s *Store) SaveTabs(active int, tabs []queue.TabSnapshot) error {
	if len(tabs) > maxTabs {
		return fmt.Errorf("store: cannot save %d tabs (maximum %d)", len(tabs), maxTabs)
	}
	if tabs == nil {
		tabs = []queue.TabSnapshot{}
	}

	data, err := json.Marshal(diskStateV3{
		Version:   currentVersion,
		ActiveTab: active,
		Tabs:      tabs,
	})
	if err != nil {
		return fmt.Errorf("store: encode tabs: %w", err)
	}
	return writeFileAtomic(s.path, data, ".player2-queue-*")
}

// pruneSnapshot drops items whose audio or CUE file is missing or not a
// regular file. It preserves the current item by ID where possible and returns
// how many items were dropped.
func pruneSnapshot(snapshot queue.Snapshot) (queue.Snapshot, int) {
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
	return snapshot, skipped
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
