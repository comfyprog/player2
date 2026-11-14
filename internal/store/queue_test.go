package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"player2/internal/queue"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func saveAndLoad(t *testing.T, path string, snap queue.Snapshot) (queue.Snapshot, int) {
	t.Helper()
	if err := New(path).SaveTabs(0, []queue.Snapshot{snap}); err != nil {
		t.Fatalf("SaveTabs: %v", err)
	}
	_, tabs, skipped, err := New(path).LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	if len(tabs) == 0 {
		return queue.Snapshot{Items: []queue.Item{}, Current: -1}, skipped
	}
	return tabs[0], skipped
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	audioA := filepath.Join(dir, "a.mp3")
	audioB := filepath.Join(dir, "b.flac")
	writeFile(t, audioA, "a")
	writeFile(t, audioB, "b")

	path := filepath.Join(dir, "state", "queue.json")
	s := New(path)
	in := queue.Snapshot{
		Items: []queue.Item{
			{
				ID:       7,
				Path:     audioA,
				Start:    1500 * time.Millisecond,
				Duration: 125 * time.Second,
				Title:    "Song A",
				Artist:   "Artist",
				Album:    "Album",
				TrackNo:  1,
			},
			{
				ID:       42,
				Path:     audioB,
				Duration: 3 * time.Minute,
				Title:    "Song B",
				Artist:   "Other Artist",
				Album:    "Other Album",
				TrackNo:  2,
			},
		},
		Current: 1,
		Shuffle: true,
		Repeat:  queue.RepeatAll,
	}
	before := in
	before.Items = append([]queue.Item(nil), in.Items...)

	if err := s.SaveTabs(0, []queue.Snapshot{in}); err != nil {
		t.Fatalf("SaveTabs: %v", err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Errorf("SaveTabs mutated its input:\n got %+v\nwant %+v", in, before)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat queue: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("queue mode = %o, want 600", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if !strings.Contains(string(data), `"version":2`) {
		t.Errorf("queue file has no version 2 marker: %s", data)
	}
	if !strings.Contains(string(data), `"tabs":`) {
		t.Errorf("queue file has no tabs array: %s", data)
	}

	_, tabs, skipped, err := s.LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(tabs))
	}
	if !reflect.DeepEqual(tabs[0], in) {
		t.Errorf("LoadTabs did not roundtrip:\n got %+v\nwant %+v", tabs[0], in)
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "queue.json")
	_, tabs, skipped, err := New(path).LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	if len(tabs) != 0 {
		t.Fatalf("tabs = %d, want 0", len(tabs))
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("LoadTabs created the missing queue file: %v", err)
	}
}

func TestLoadInvalidData(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"truncated", `{"version":1`},
		{"wrong version", `{"version":3,"queue":{"items":[],"current":-1}}`},
		{"missing version", `{"queue":{"items":[],"current":-1}}`},
		{"wrong queue type", `{"version":1,"queue":"not an object"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "queue.json")
			writeFile(t, path, tc.data)
			if _, _, _, err := New(path).LoadTabs(); err == nil {
				t.Fatalf("LoadTabs accepted invalid data %q", tc.data)
			}
		})
	}

	t.Run("unreadable path", func(t *testing.T) {
		if _, _, _, err := New(t.TempDir()).LoadTabs(); err == nil {
			t.Fatal("Load accepted a directory as a queue file")
		}
	})
}

func TestLoadPrunesMissingFiles(t *testing.T) {
	dir := t.TempDir()
	audioA := filepath.Join(dir, "a.mp3")
	audioB := filepath.Join(dir, "b.mp3")
	audioC := filepath.Join(dir, "c.mp3")
	audioD := filepath.Join(dir, "d.mp3")
	audioE := filepath.Join(dir, "e.mp3")
	for _, path := range []string{audioA, audioB, audioC, audioD, audioE} {
		writeFile(t, path, "audio")
	}
	missingAudio := filepath.Join(dir, "missing.mp3")
	cueOK := filepath.Join(dir, "ok.cue")
	cueMissing := filepath.Join(dir, "missing.cue")
	cueDir := filepath.Join(dir, "cue-dir")
	audioDir := filepath.Join(dir, "audio-dir")
	writeFile(t, cueOK, "cue")
	if err := os.Mkdir(cueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(audioDir, 0o755); err != nil {
		t.Fatal(err)
	}

	in := queue.Snapshot{
		Items: []queue.Item{
			{ID: 1, Path: audioA},
			{ID: 2, Path: missingAudio},
			{ID: 3, Path: audioB},
			{ID: 4, Path: audioC, CuePath: cueMissing},
			{ID: 5, Path: audioD, CuePath: cueOK},
			{ID: 6, Path: audioE, CuePath: cueDir},
			{ID: 7, Path: audioDir},
		},
		Current: 4, // audioD, which survives pruning
		Shuffle: true,
		Repeat:  queue.RepeatOne,
	}

	got, skipped := saveAndLoad(t, filepath.Join(dir, "queue.json"), in)
	if skipped != 4 {
		t.Errorf("skipped = %d, want 4", skipped)
	}
	wantIDs := []uint64{1, 3, 5}
	if len(got.Items) != len(wantIDs) {
		t.Fatalf("got %d items, want %d: %+v", len(got.Items), len(wantIDs), got.Items)
	}
	for i, id := range wantIDs {
		if got.Items[i].ID != id {
			t.Errorf("item %d ID = %d, want %d", i, got.Items[i].ID, id)
		}
	}
	if got.Current != 2 {
		t.Errorf("Current = %d, want 2 (the surviving current item)", got.Current)
	}
	if got.Items[2].Path != audioD || got.Items[2].CuePath != cueOK {
		t.Errorf("surviving CUE item = %+v", got.Items[2])
	}
	if !got.Shuffle || got.Repeat != queue.RepeatOne {
		t.Errorf("shuffle/repeat not preserved: %+v", got)
	}
}

func TestLoadPreservesAndClampsCurrent(t *testing.T) {
	t.Run("current item pruned", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.mp3")
		b := filepath.Join(dir, "b.mp3")
		writeFile(t, a, "a")
		writeFile(t, b, "b")

		got, skipped := saveAndLoad(t, filepath.Join(dir, "queue.json"), queue.Snapshot{
			Items: []queue.Item{
				{ID: 1, Path: a},
				{ID: 2, Path: b},
				{ID: 3, Path: filepath.Join(dir, "missing.mp3")},
			},
			Current: 2,
		})
		if skipped != 1 {
			t.Errorf("skipped = %d, want 1", skipped)
		}
		if got.Current != 1 {
			t.Errorf("Current = %d, want 1", got.Current)
		}
		if got.Items[got.Current].ID != 2 {
			t.Errorf("current item ID = %d, want 2", got.Items[got.Current].ID)
		}
	})

	t.Run("current survives after earlier item pruned", func(t *testing.T) {
		dir := t.TempDir()
		b := filepath.Join(dir, "b.mp3")
		c := filepath.Join(dir, "c.mp3")
		writeFile(t, b, "b")
		writeFile(t, c, "c")

		got, skipped := saveAndLoad(t, filepath.Join(dir, "queue.json"), queue.Snapshot{
			Items: []queue.Item{
				{ID: 10, Path: filepath.Join(dir, "missing.mp3")},
				{ID: 20, Path: b},
				{ID: 30, Path: c},
			},
			Current: 1,
		})
		if skipped != 1 {
			t.Errorf("skipped = %d, want 1", skipped)
		}
		if got.Current != 0 {
			t.Errorf("Current = %d, want 0 (tracked by ID, not clamped)", got.Current)
		}
		if got.Items[got.Current].ID != 20 {
			t.Errorf("current item ID = %d, want 20", got.Items[got.Current].ID)
		}
	})

	t.Run("clamps to last remaining", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.mp3")
		b := filepath.Join(dir, "b.mp3")
		writeFile(t, a, "a")
		writeFile(t, b, "b")

		got, skipped := saveAndLoad(t, filepath.Join(dir, "queue.json"), queue.Snapshot{
			Items: []queue.Item{
				{ID: 1, Path: a},
				{ID: 2, Path: b},
				{ID: 3, Path: filepath.Join(dir, "missing-c.mp3")},
				{ID: 4, Path: filepath.Join(dir, "missing-d.mp3")},
			},
			Current: 3,
		})
		if skipped != 2 {
			t.Errorf("skipped = %d, want 2", skipped)
		}
		if got.Current != 1 {
			t.Errorf("Current = %d, want 1", got.Current)
		}
	})

	t.Run("empty after pruning", func(t *testing.T) {
		dir := t.TempDir()
		got, skipped := saveAndLoad(t, filepath.Join(dir, "queue.json"), queue.Snapshot{
			Items: []queue.Item{
				{ID: 1, Path: filepath.Join(dir, "missing-a.mp3")},
				{ID: 2, Path: filepath.Join(dir, "missing-b.mp3")},
			},
			Current: 0,
		})
		if skipped != 2 {
			t.Errorf("skipped = %d, want 2", skipped)
		}
		if len(got.Items) != 0 {
			t.Errorf("Items = %+v, want empty", got.Items)
		}
		if got.Current != -1 {
			t.Errorf("Current = %d, want -1", got.Current)
		}
	})

	t.Run("no current stays no current", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.mp3")
		b := filepath.Join(dir, "b.mp3")
		writeFile(t, a, "a")
		writeFile(t, b, "b")

		got, skipped := saveAndLoad(t, filepath.Join(dir, "queue.json"), queue.Snapshot{
			Items: []queue.Item{
				{ID: 1, Path: a},
				{ID: 2, Path: b},
			},
			Current: -1,
		})
		if skipped != 0 {
			t.Errorf("skipped = %d, want 0", skipped)
		}
		if got.Current != -1 {
			t.Errorf("Current = %d, want -1", got.Current)
		}
	})
}

func TestSaveCreatesParentDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state", "player2", "queue.json")
	if err := New(path).SaveTabs(0, []queue.Snapshot{{Current: -1}}); err != nil {
		t.Fatalf("SaveTabs: %v", err)
	}

	for _, dir := range []string{
		filepath.Join(root, "state"),
		filepath.Join(root, "state", "player2"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s mode = %o, want 700", dir, perm)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat queue: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("queue mode = %o, want 600", perm)
	}

	_, tabs, skipped, err := New(path).LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	if skipped != 0 || len(tabs) != 1 || len(tabs[0].Items) != 0 || tabs[0].Current != -1 {
		t.Errorf("LoadTabs after empty Save = %+v, skipped %d", tabs, skipped)
	}
}

func TestSaveOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	audioA := filepath.Join(dir, "a.mp3")
	audioB := filepath.Join(dir, "b.mp3")
	writeFile(t, audioA, "a")
	writeFile(t, audioB, "b")
	stateDir := filepath.Join(dir, "state")
	path := filepath.Join(stateDir, "queue.json")
	s := New(path)

	if err := s.SaveTabs(0, []queue.Snapshot{{
		Items:   []queue.Item{{ID: 1, Path: audioA, Title: "A"}},
		Current: 0,
	}}); err != nil {
		t.Fatalf("first SaveTabs: %v", err)
	}
	if err := s.SaveTabs(0, []queue.Snapshot{{
		Items: []queue.Item{
			{ID: 1, Path: audioA, Title: "A"},
			{ID: 2, Path: audioB, Title: "B"},
		},
		Current: 1,
		Shuffle: true,
		Repeat:  queue.RepeatOne,
	}}); err != nil {
		t.Fatalf("second SaveTabs: %v", err)
	}

	_, tabs, skipped, err := s.LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(tabs))
	}
	got := tabs[0]
	if len(got.Items) != 2 || got.Current != 1 || !got.Shuffle || got.Repeat != queue.RepeatOne {
		t.Errorf("LoadTabs after overwrite = %+v", got)
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "queue.json" {
			t.Errorf("unexpected leftover file %q", entry.Name())
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("queue mode = %o, want 600", perm)
	}
}

func TestSaveCleansUpTempOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "queue.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := New(target).SaveTabs(0, []queue.Snapshot{{Current: -1}}); err == nil {
		t.Fatal("SaveTabs succeeded when the destination was a directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "queue.json" {
			t.Errorf("temporary file left behind: %q", entry.Name())
		}
	}
}

func saveTabsAndLoad(t *testing.T, path string, active int, tabs []queue.Snapshot) (int, []queue.Snapshot, int) {
	t.Helper()
	if err := New(path).SaveTabs(active, tabs); err != nil {
		t.Fatalf("SaveTabs: %v", err)
	}
	gotActive, gotTabs, skipped, err := New(path).LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	return gotActive, gotTabs, skipped
}

func TestSaveTabsLoadTabsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	audioA := filepath.Join(dir, "a.mp3")
	audioB := filepath.Join(dir, "b.flac")
	audioC := filepath.Join(dir, "c.ogg")
	for _, path := range []string{audioA, audioB, audioC} {
		writeFile(t, path, "audio")
	}

	path := filepath.Join(dir, "state", "queue.json")
	tabs := []queue.Snapshot{
		{
			Items:   []queue.Item{{ID: 7, Path: audioA, Title: "A", Duration: time.Minute}},
			Current: 0,
			Shuffle: true,
			Repeat:  queue.RepeatAll,
		},
		{
			Items:   []queue.Item{{ID: 9, Path: audioB, Title: "B", Duration: 2 * time.Minute}},
			Current: 0,
			Repeat:  queue.RepeatOne,
		},
		{
			Items:   []queue.Item{{ID: 11, Path: audioC, Title: "C"}},
			Current: 0,
		},
	}
	before := make([]queue.Snapshot, len(tabs))
	for i := range tabs {
		before[i] = tabs[i]
		before[i].Items = append([]queue.Item(nil), tabs[i].Items...)
	}

	if err := New(path).SaveTabs(1, tabs); err != nil {
		t.Fatalf("SaveTabs: %v", err)
	}
	if !reflect.DeepEqual(tabs, before) {
		t.Errorf("SaveTabs mutated its input:\n got %+v\nwant %+v", tabs, before)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat queue: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("queue mode = %o, want 600", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	for _, marker := range []string{`"version":2`, `"active_tab":1`, `"tabs":`} {
		if !strings.Contains(string(data), marker) {
			t.Errorf("queue file missing %s: %s", marker, data)
		}
	}

	active, got, skipped := saveTabsAndLoad(t, path, 1, tabs)
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if active != 1 {
		t.Errorf("active = %d, want 1", active)
	}
	if !reflect.DeepEqual(got, tabs) {
		t.Errorf("LoadTabs did not roundtrip:\n got %+v\nwant %+v", got, tabs)
	}
}

func TestLoadTabsMigratesVersion1ToTabZero(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "legacy.mp3")
	writeFile(t, audio, "audio")

	legacy := map[string]any{
		"version": 1,
		"queue": queue.Snapshot{
			Items:   []queue.Item{{ID: 3, Path: audio, Title: "Legacy", Duration: time.Minute}},
			Current: 0,
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "queue.json")
	writeFile(t, path, string(data))

	active, tabs, skipped, err := New(path).LoadTabs()
	if err != nil {
		t.Fatalf("LoadTabs: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if active != 0 {
		t.Errorf("active = %d, want 0", active)
	}
	if len(tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(tabs))
	}
	if len(tabs[0].Items) != 1 || tabs[0].Items[0].Title != "Legacy" || tabs[0].Current != 0 {
		t.Errorf("migrated tab = %+v", tabs[0])
	}
}

func TestLoadTabsPrunesEachTab(t *testing.T) {
	dir := t.TempDir()
	audioA := filepath.Join(dir, "a.mp3")
	audioB := filepath.Join(dir, "b.mp3")
	writeFile(t, audioA, "a")
	writeFile(t, audioB, "b")
	missingA := filepath.Join(dir, "missing-a.mp3")
	missingB := filepath.Join(dir, "missing-b.mp3")

	tabs := []queue.Snapshot{
		{
			Items: []queue.Item{
				{ID: 10, Path: audioA, Title: "A"},
				{ID: 20, Path: missingA, Title: "Gone A"},
			},
			Current: 0,
		},
		{
			Items: []queue.Item{
				{ID: 30, Path: missingB, Title: "Gone B"},
				{ID: 40, Path: audioB, Title: "B"},
			},
			Current: 1,
		},
	}

	active, got, skipped := saveTabsAndLoad(t, filepath.Join(dir, "queue.json"), 1, tabs)
	if active != 1 {
		t.Errorf("active = %d, want 1", active)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if len(got) != 2 {
		t.Fatalf("tabs = %d, want 2", len(got))
	}
	if len(got[0].Items) != 1 || got[0].Items[0].Path != audioA || got[0].Current != 0 {
		t.Errorf("tab 0 after prune = %+v", got[0])
	}
	if len(got[1].Items) != 1 || got[1].Items[0].Path != audioB || got[1].Current != 0 {
		t.Errorf("tab 1 after prune = %+v", got[1])
	}
}

func TestLoadTabsClampsActiveTab(t *testing.T) {
	dir := t.TempDir()
	tabs := []queue.Snapshot{{Current: -1}, {Current: -1}}
	for _, active := range []int{2, 99, -1} {
		active, got, skipped := saveTabsAndLoad(t, filepath.Join(dir, "queue.json"), active, tabs)
		if active != 0 {
			t.Errorf("active = %d, want 0", active)
		}
		if skipped != 0 || len(got) != len(tabs) {
			t.Errorf("load = %d tabs, skipped %d", len(got), skipped)
		}
	}

	active, got, skipped := saveTabsAndLoad(t, filepath.Join(dir, "empty.json"), 7, nil)
	if active != 0 || len(got) != 0 || skipped != 0 {
		t.Errorf("empty load = active %d, %d tabs, skipped %d", active, len(got), skipped)
	}
}

func TestSaveTabsRejectsTooManyTabs(t *testing.T) {
	tabs := make([]queue.Snapshot, 11)
	for i := range tabs {
		tabs[i].Current = -1
	}
	if err := New(filepath.Join(t.TempDir(), "queue.json")).SaveTabs(0, tabs); err == nil {
		t.Fatal("SaveTabs accepted more than ten tabs")
	}

	path := filepath.Join(t.TempDir(), "queue.json")
	writeFile(t, path, `{"version":2,"active_tab":0,"tabs":[{},{},{},{},{},{},{},{},{},{},{}]}`)
	if _, _, _, err := New(path).LoadTabs(); err == nil {
		t.Fatal("LoadTabs accepted more than ten tabs")
	}
}
