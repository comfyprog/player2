package store

import (
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
	if err := New(path).Save(snap); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, skipped, err := New(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return got, skipped
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

	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Errorf("Save mutated its input:\n got %+v\nwant %+v", in, before)
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
	if !strings.Contains(string(data), `"version":1`) {
		t.Errorf("queue file has no version 1 marker: %s", data)
	}
	if !strings.Contains(string(data), `"queue":`) {
		t.Errorf("queue file has no queue object: %s", data)
	}

	got, skipped, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("Load did not roundtrip:\n got %+v\nwant %+v", got, in)
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "queue.json")
	got, skipped, err := New(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(got.Items) != 0 {
		t.Errorf("Items = %+v, want empty", got.Items)
	}
	if got.Current != -1 {
		t.Errorf("Current = %d, want -1", got.Current)
	}
	if got.Shuffle || got.Repeat != queue.RepeatOff {
		t.Errorf("state = shuffle %v repeat %d, want false/off", got.Shuffle, got.Repeat)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Load created the missing queue file: %v", err)
	}
}

func TestLoadInvalidData(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"truncated", `{"version":1`},
		{"wrong version", `{"version":2,"queue":{"items":[],"current":-1}}`},
		{"missing version", `{"queue":{"items":[],"current":-1}}`},
		{"wrong queue type", `{"version":1,"queue":"not an object"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "queue.json")
			writeFile(t, path, tc.data)
			if _, _, err := New(path).Load(); err == nil {
				t.Fatalf("Load accepted invalid data %q", tc.data)
			}
		})
	}

	t.Run("unreadable path", func(t *testing.T) {
		if _, _, err := New(t.TempDir()).Load(); err == nil {
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
	if err := New(path).Save(queue.Snapshot{Current: -1}); err != nil {
		t.Fatalf("Save: %v", err)
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

	got, skipped, err := New(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if skipped != 0 || len(got.Items) != 0 || got.Current != -1 {
		t.Errorf("Load after empty Save = %+v, skipped %d", got, skipped)
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

	if err := s.Save(queue.Snapshot{
		Items:   []queue.Item{{ID: 1, Path: audioA, Title: "A"}},
		Current: 0,
	}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := s.Save(queue.Snapshot{
		Items: []queue.Item{
			{ID: 1, Path: audioA, Title: "A"},
			{ID: 2, Path: audioB, Title: "B"},
		},
		Current: 1,
		Shuffle: true,
		Repeat:  queue.RepeatOne,
	}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, skipped, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(got.Items) != 2 || got.Current != 1 || !got.Shuffle || got.Repeat != queue.RepeatOne {
		t.Errorf("Load after overwrite = %+v", got)
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

	if err := New(target).Save(queue.Snapshot{Current: -1}); err == nil {
		t.Fatal("Save succeeded when the destination was a directory")
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
