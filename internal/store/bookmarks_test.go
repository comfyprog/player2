package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBookmarksSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "bookmarks.json")
	paths := []string{"/home/alex/Music", "/home/alex/Downloads"}
	before := append([]string(nil), paths...)

	store := NewBookmarks(path)
	if err := store.Save(paths); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !reflect.DeepEqual(paths, before) {
		t.Errorf("Save mutated its input:\n got %v\nwant %v", paths, before)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat bookmarks: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("bookmarks mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat bookmark directory: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("bookmark directory mode = %o, want 700", perm)
	}

	got, err := NewBookmarks(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, paths) {
		t.Errorf("Load did not roundtrip:\n got %v\nwant %v", got, paths)
	}
}

func TestBookmarksLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "bookmarks.json")

	got, err := NewBookmarks(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Load returned %v, want empty", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Load created the missing bookmark file: %v", err)
	}
}

func TestBookmarksLoadDropsEmptyAndDuplicatePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bookmarks.json")
	writeFile(t, path, `{"version":1,"paths":["/music","", "/music/","/downloads","/downloads/"]}`)

	got, err := NewBookmarks(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"/music", "/downloads"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %v, want %v", got, want)
	}
}

func TestBookmarksSaveEmptyListAsArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bookmarks.json")
	if err := NewBookmarks(path).Save(nil); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bookmarks: %v", err)
	}
	if string(data) != "{\"version\":1,\"paths\":[]}" {
		t.Errorf("bookmark file = %s, want an empty paths array", data)
	}

	got, err := NewBookmarks(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load = %v, want empty", got)
	}
}

func TestBookmarksLoadInvalidData(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"truncated", `{"version":1`},
		{"wrong version", `{"version":2,"paths":[]}`},
		{"missing version", `{"paths":[]}`},
		{"wrong paths type", `{"version":1,"paths":"not an array"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bookmarks.json")
			writeFile(t, path, tc.data)
			if _, err := NewBookmarks(path).Load(); err == nil {
				t.Fatal("Load accepted invalid bookmark data")
			}
		})
	}
}
