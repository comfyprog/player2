package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"player2/internal/queue"
)

func TestSaveTabsPersistsTabNames(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.flac")
	writeFile(t, audio, "audio")
	path := filepath.Join(dir, "state", "queue.json")

	tabs := []queue.TabSnapshot{
		{
			Name: "Jazz",
			Snapshot: queue.Snapshot{
				Items:   []queue.Item{{ID: 1, Path: audio, Title: "Song", Duration: time.Minute}},
				Current: 0,
			},
		},
		{
			Name:     "",
			Snapshot: queue.Snapshot{Items: []queue.Item{}, Current: -1},
		},
	}
	if err := New(path).SaveTabs(0, tabs); err != nil {
		t.Fatalf("SaveTabs: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	for _, marker := range []string{`"version":3`, `"name":"Jazz"`} {
		if !strings.Contains(string(data), marker) {
			t.Errorf("queue file missing %s: %s", marker, data)
		}
	}

	active, got, skipped, err := New(path).LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	if active != 0 || skipped != 0 {
		t.Errorf("LoadTabs = active %d, skipped %d; want 0, 0", active, skipped)
	}
	if len(got) != 2 {
		t.Fatalf("tabs = %d, want 2", len(got))
	}
	if got[0].Name != "Jazz" {
		t.Errorf("tab 0 name = %q, want Jazz", got[0].Name)
	}
	if len(got[0].Items) != 1 || got[0].Items[0].Title != "Song" {
		t.Errorf("tab 0 queue = %+v", got[0].Snapshot)
	}
	if got[1].Name != "" {
		t.Errorf("tab 1 name = %q, want empty", got[1].Name)
	}
}

func TestLoadTabsReadsVersion2TabsWithoutNames(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "legacy.flac")
	writeFile(t, audio, "audio")
	path := filepath.Join(dir, "queue.json")

	state := map[string]any{
		"version":    2,
		"active_tab": 0,
		"tabs": []queue.Snapshot{{
			Items:   []queue.Item{{ID: 3, Path: audio, Title: "Legacy", Duration: time.Minute}},
			Current: 0,
		}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))

	active, tabs, skipped, err := New(path).LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	if active != 0 || skipped != 0 {
		t.Errorf("LoadTabs = active %d, skipped %d; want 0, 0", active, skipped)
	}
	if len(tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(tabs))
	}
	if tabs[0].Name != "" {
		t.Errorf("migrated name = %q, want empty", tabs[0].Name)
	}
	if len(tabs[0].Items) != 1 || tabs[0].Items[0].Title != "Legacy" {
		t.Errorf("migrated tab = %+v", tabs[0].Snapshot)
	}
}
