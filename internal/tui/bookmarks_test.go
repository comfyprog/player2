package tui

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/library"
	"player2/internal/store"
)

func TestAddBookmarkStoresCurrentDirectory(t *testing.T) {
	bookmarks := &fakeBookmarkStore{}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, cmd := pressForCmd(t, m, runeKey('f'))
	m = applyBrowserCmd(t, m, cmd)

	if want := []string{"/music"}; !reflect.DeepEqual(m.bookmarks, want) {
		t.Errorf("bookmarks = %v, want %v", m.bookmarks, want)
	}
	if got, ok := bookmarks.lastSaved(); !ok || !reflect.DeepEqual(got, []string{"/music"}) {
		t.Errorf("saved bookmarks = %v (saved=%v), want [/music]", got, ok)
	}
	if !strings.Contains(m.statusMsg, "bookmarked") {
		t.Errorf("status = %q, want a bookmarked message", m.statusMsg)
	}
}

func TestAddBookmarkRejectsDuplicate(t *testing.T) {
	bookmarks := &fakeBookmarkStore{paths: []string{"/music"}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, cmd := pressForCmd(t, m, runeKey('f'))
	if cmd != nil {
		t.Fatalf("duplicate add returned a command, want nil")
	}

	if want := []string{"/music"}; !reflect.DeepEqual(m.bookmarks, want) {
		t.Errorf("bookmarks = %v, want %v", m.bookmarks, want)
	}
	if got := bookmarks.saveCount(); got != 0 {
		t.Errorf("save count = %d, want 0 for a duplicate", got)
	}
	if !strings.Contains(m.statusMsg, "already bookmarked") {
		t.Errorf("status = %q, want an already-bookmarked message", m.statusMsg)
	}
}

func TestAddBookmarkRejectsCueFolder(t *testing.T) {
	bookmarks := &fakeBookmarkStore{}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music/Album.cue"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserCue = true
	m.browserEntriesPath = "/music/Album.cue"

	m, cmd := pressForCmd(t, m, runeKey('f'))
	if cmd != nil {
		t.Fatalf("CUE add returned a command, want nil")
	}
	if len(m.bookmarks) != 0 {
		t.Errorf("bookmarks = %v, want empty", m.bookmarks)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "CUE") {
		t.Errorf("status = %q (error=%v), want a CUE error", m.statusMsg, m.statusIsError)
	}
}

func TestAddBookmarkRequiresLoadedListing(t *testing.T) {
	bookmarks := &fakeBookmarkStore{}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/missing"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)

	m, cmd := pressForCmd(t, m, runeKey('f'))
	if cmd != nil {
		t.Fatalf("unreadable add returned a command, want nil")
	}
	if len(m.bookmarks) != 0 {
		t.Errorf("bookmarks = %v, want empty", m.bookmarks)
	}
	if !m.statusIsError {
		t.Errorf("status = %q, want an error", m.statusMsg)
	}
}

func TestShowBookmarksRendersPaths(t *testing.T) {
	t.Setenv("HOME", "/home/alex")
	bookmarks := &fakeBookmarkStore{paths: []string{"/home/alex/Music", "/home/alex/Downloads"}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, cmd := pressForCmd(t, m, runeKey('F'))
	if cmd != nil {
		t.Fatalf("show bookmarks returned a command, want nil")
	}
	if !m.browserBookmarks {
		t.Fatal("bookmark view is not open")
	}
	if title := ansi.Strip(m.browserTitle(40)); title != "Bookmarks: 2" {
		t.Errorf("title = %q, want %q", title, "Bookmarks: 2")
	}

	lines := m.browserLines(40, 4)
	got := []string{
		strings.TrimRight(ansi.Strip(lines[1]), " "),
		strings.TrimRight(ansi.Strip(lines[2]), " "),
	}
	want := []string{"~/Music", "~/Downloads"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bookmark rows = %v, want %v", got, want)
	}
	if plain := ansi.Strip(m.statusLine()); !strings.Contains(plain, "[BOOKMARKS]") {
		t.Errorf("status line = %q, want [BOOKMARKS]", plain)
	}
}

func TestShowBookmarksEmptyAndFiltered(t *testing.T) {
	t.Setenv("HOME", "/home/alex")
	bookmarks := &fakeBookmarkStore{paths: []string{"/home/alex/Music", "/home/alex/Downloads"}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, _ = pressForCmd(t, m, runeKey('F'))
	m, _ = pressForCmd(t, m, runeKey('/'))
	for _, r := range "down" {
		m, _ = pressForCmd(t, m, runeKey(r))
	}
	m, _ = pressForCmd(t, m, specialKey(tea.KeyEnter))

	visible := m.browserVisibleEntries()
	if len(visible) != 1 || visible[0].Path != "/home/alex/Downloads" {
		t.Errorf("filtered bookmarks = %#v, want only Downloads", visible)
	}

	m, _ = pressForCmd(t, m, runeKey('F'))
	if m.browserBookmarks {
		t.Error("second F did not close the bookmark view")
	}
	m.bookmarks = nil
	m, _ = pressForCmd(t, m, runeKey('F'))
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "No bookmarks") {
		t.Errorf("empty bookmark view lacks empty message:\n%s", plain)
	}
}

func TestBookmarkFilterMatchesRawPath(t *testing.T) {
	t.Setenv("HOME", "/home/alex")
	bookmarks := &fakeBookmarkStore{paths: []string{"/home/alex/Music", "/home/alex/Downloads"}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"
	m, _ = pressForCmd(t, m, runeKey('F'))

	m.browserFilter = "home"
	visible := m.browserVisibleEntries()
	if len(visible) != 2 {
		t.Fatalf("visible bookmarks for raw path query = %d, want 2", len(visible))
	}

	m.browserFilter = "downloads"
	visible = m.browserVisibleEntries()
	if len(visible) != 1 || visible[0].Path != "/home/alex/Downloads" {
		t.Errorf("visible bookmarks = %#v, want only Downloads", visible)
	}
}

func TestShowBookmarksBackRestoresDirectory(t *testing.T) {
	bookmarks := &fakeBookmarkStore{paths: []string{"/downloads"}}
	sc := &testScanner{dirs: map[string][]library.Entry{
		"/music": {{Kind: library.KindDir, Name: "Album", Path: "/music/Album"}},
	}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = sc
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"
	m.browserEntries = sc.dirs["/music"]
	m.browserSelected = 0
	m.browserOffset = 0

	m, _ = pressForCmd(t, m, runeKey('F'))
	if !m.browserBookmarks {
		t.Fatal("bookmark view is not open")
	}
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyBackspace))
	m = applyBrowserCmd(t, m, cmd)

	if m.browserBookmarks {
		t.Error("bookmark view is still open after Backspace")
	}
	if m.browserPath != "/music" {
		t.Errorf("browserPath = %q, want /music", m.browserPath)
	}
	if m.browserEntriesPath != "/music" {
		t.Errorf("browserEntriesPath = %q, want /music", m.browserEntriesPath)
	}
}

func TestOpenBookmarkNavigatesAndBackGoesToParent(t *testing.T) {
	bookmarks := &fakeBookmarkStore{paths: []string{"/downloads/albums"}}
	sc := &testScanner{dirs: map[string][]library.Entry{
		"/downloads/albums": {{Kind: library.KindAudio, Name: "song.flac", Path: "/downloads/albums/song.flac"}},
		"/downloads":        {{Kind: library.KindDir, Name: "albums", Path: "/downloads/albums"}},
	}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = sc
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"
	m.browserEntries = []library.Entry{{Kind: library.KindDir, Name: "Album", Path: "/music/Album"}}
	m.browserStack = []browserLocation{{Path: "/older"}}

	m, _ = pressForCmd(t, m, runeKey('F'))
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = applyBrowserCmd(t, m, cmd)

	if m.browserBookmarks {
		t.Error("bookmark view is still open after opening a bookmark")
	}
	if m.browserPath != "/downloads/albums" {
		t.Errorf("browserPath = %q, want /downloads/albums", m.browserPath)
	}
	if m.browserEntriesPath != "/downloads/albums" {
		t.Errorf("browserEntriesPath = %q, want /downloads/albums", m.browserEntriesPath)
	}
	if len(m.browserStack) != 0 {
		t.Errorf("browser stack depth = %d, want 0 after a bookmark jump", len(m.browserStack))
	}

	m, cmd = pressForCmd(t, m, specialKey(tea.KeyBackspace))
	m = applyBrowserCmd(t, m, cmd)
	if m.browserPath != "/downloads" {
		t.Errorf("browserPath after Backspace = %q, want /downloads", m.browserPath)
	}
}

func TestRemoveBookmarkPersistsSelection(t *testing.T) {
	bookmarks := &fakeBookmarkStore{paths: []string{"/music", "/downloads"}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, _ = pressForCmd(t, m, runeKey('F'))
	m, _ = pressForCmd(t, m, specialKey(tea.KeyDown))
	m, cmd := pressForCmd(t, m, runeKey('d'))
	m = applyBrowserCmd(t, m, cmd)

	if want := []string{"/music"}; !reflect.DeepEqual(m.bookmarks, want) {
		t.Errorf("bookmarks = %v, want %v", m.bookmarks, want)
	}
	if got, ok := bookmarks.lastSaved(); !ok || !reflect.DeepEqual(got, []string{"/music"}) {
		t.Errorf("saved bookmarks = %v (saved=%v), want [/music]", got, ok)
	}
	if !strings.Contains(m.statusMsg, "removed") {
		t.Errorf("status = %q, want a removed message", m.statusMsg)
	}
}

func TestRemoveLastBookmarkShowsEmptyList(t *testing.T) {
	bookmarks := &fakeBookmarkStore{paths: []string{"/music"}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, _ = pressForCmd(t, m, runeKey('F'))
	m, cmd := pressForCmd(t, m, runeKey('d'))
	m = applyBrowserCmd(t, m, cmd)

	if len(m.bookmarks) != 0 {
		t.Errorf("bookmarks = %v, want empty", m.bookmarks)
	}
	if got, ok := bookmarks.lastSaved(); !ok || len(got) != 0 {
		t.Errorf("saved bookmarks = %v (saved=%v), want an empty list", got, ok)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "No bookmarks") {
		t.Errorf("empty bookmark view lacks empty message:\n%s", plain)
	}
}

func TestBookmarkSaveFailureIsReported(t *testing.T) {
	bookmarks := &fakeBookmarkStore{saveErr: errors.New("disk full")}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, cmd := pressForCmd(t, m, runeKey('f'))
	m = applyBrowserCmd(t, m, cmd)

	if want := []string{"/music"}; !reflect.DeepEqual(m.bookmarks, want) {
		t.Errorf("bookmarks = %v, want %v", m.bookmarks, want)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "disk full") {
		t.Errorf("status = %q (error=%v), want the save error", m.statusMsg, m.statusIsError)
	}
	if !m.bookmarksDirty {
		t.Error("bookmarksDirty = false after a failed save, want true for the exit flush")
	}

	bookmarks.setSaveErr(nil)
	if err := m.FlushBookmarks(); err != nil {
		t.Fatalf("FlushBookmarks: %v", err)
	}
	if got, ok := bookmarks.lastSaved(); !ok || !reflect.DeepEqual(got, []string{"/music"}) {
		t.Errorf("flushed bookmarks = %v (saved=%v), want [/music]", got, ok)
	}
	if m.bookmarksDirty {
		t.Error("bookmarksDirty = true after a successful flush")
	}
}

func TestBookmarkSaveCoalescesPendingChanges(t *testing.T) {
	bookmarks := &fakeBookmarkStore{}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, first := pressForCmd(t, m, runeKey('f'))
	if first == nil {
		t.Fatal("first add returned no save command")
	}

	m.browserPath = "/downloads"
	m.browserEntriesPath = "/downloads"
	m, second := pressForCmd(t, m, runeKey('f'))
	if second != nil {
		t.Errorf("second add returned a save command, want coalescing")
	}

	m = applyBrowserCmd(t, m, first)
	want := []string{"/music", "/downloads"}
	if got, ok := bookmarks.lastSaved(); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("saved bookmarks = %v (saved=%v), want %v", got, ok, want)
	}
	if !reflect.DeepEqual(m.bookmarks, want) {
		t.Errorf("bookmarks = %v, want %v", m.bookmarks, want)
	}
}

func TestFlushBookmarksWritesPendingChange(t *testing.T) {
	bookmarks := &fakeBookmarkStore{}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, cmd := pressForCmd(t, m, runeKey('f'))
	if cmd == nil {
		t.Fatal("add returned no save command")
	}
	if err := m.FlushBookmarks(); err != nil {
		t.Fatalf("FlushBookmarks: %v", err)
	}
	if got, ok := bookmarks.lastSaved(); !ok || !reflect.DeepEqual(got, []string{"/music"}) {
		t.Errorf("flushed bookmarks = %v (saved=%v), want [/music]", got, ok)
	}

	// The older asynchronous save must not write after the flush.
	if msg, ok := cmd().(bookmarksSavedMsg); ok && msg.err != nil {
		t.Fatalf("late save: %v", msg.err)
	}
	if got := bookmarks.saveCount(); got != 1 {
		t.Errorf("save count = %d, want 1 after flush", got)
	}
}

func TestFlushBookmarksSkipsCleanList(t *testing.T) {
	bookmarks := &fakeBookmarkStore{paths: []string{"/music"}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})

	if err := m.FlushBookmarks(); err != nil {
		t.Fatalf("FlushBookmarks: %v", err)
	}
	if got := bookmarks.saveCount(); got != 0 {
		t.Errorf("save count = %d, want 0 for a clean bookmark list", got)
	}
}

func TestBookmarkViewDisablesDirectoryActions(t *testing.T) {
	bookmarks := &fakeBookmarkStore{paths: []string{"/bookmarked"}}
	sc := &testScanner{dirs: map[string][]library.Entry{
		"/music":      {{Kind: library.KindAudio, Name: "song.flac", Path: "/music/song.flac"}},
		"/bookmarked": {{Kind: library.KindAudio, Name: "other.flac", Path: "/bookmarked/other.flac"}},
	}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = sc
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"
	m.browserEntries = sc.dirs["/music"]
	m, _ = pressForCmd(t, m, runeKey('F'))

	for _, key := range []rune{'a', 'A', 't', 'i', 'R'} {
		m, cmd := pressForCmd(t, m, runeKey(key))
		if cmd != nil {
			t.Errorf("%q returned command %T in the bookmark view, want nil", key, cmd)
		}
		if !m.browserBookmarks {
			t.Fatalf("%q closed the bookmark view", key)
		}
	}

	if m.queue.Len() != 0 {
		t.Errorf("queue length = %d, want 0", m.queue.Len())
	}
	if m.browserShowTags {
		t.Error("tag mode toggled from the bookmark view")
	}
	if m.infoOpen() {
		t.Error("info window opened from the bookmark view")
	}
	if m.browserEntriesPath != "/music" {
		t.Errorf("browserEntriesPath = %q, want /music", m.browserEntriesPath)
	}
}

func TestBookmarkViewInvalidatesInFlightDirectoryLoad(t *testing.T) {
	bookmarks := &fakeBookmarkStore{}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	gen := m.browserGen
	m.browserLoading = true

	m, _ = pressForCmd(t, m, runeKey('F'))
	next, _ := m.Update(browserLoadedMsg{
		gen:     gen,
		path:    "/music",
		entries: []library.Entry{{Kind: library.KindAudio, Name: "late.flac", Path: "/music/late.flac"}},
	})
	m = next.(*Model)

	if len(m.browserEntries) != 0 {
		t.Errorf("stale directory load installed %d entries", len(m.browserEntries))
	}
	if !m.browserBookmarks {
		t.Error("bookmark view closed by a stale load")
	}
}

func TestLoadBrowserLeavesBookmarkView(t *testing.T) {
	bookmarks := &fakeBookmarkStore{paths: []string{"/downloads"}}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.showBookmarks()

	m.loadBrowser("/music", false, 0, 0, "")
	if m.browserBookmarks {
		t.Error("loadBrowser left the bookmark view open")
	}
	if m.bookmarkReturn != (browserLocation{}) {
		t.Errorf("bookmarkReturn = %+v, want zero after loading a directory", m.bookmarkReturn)
	}
}

func TestBookmarkLoadErrorSurvivesBrowserLoadAndBlocksWrites(t *testing.T) {
	bookmarks := &fakeBookmarkStore{loadErr: errors.New("bad bookmark file")}
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)

	next, _ := m.Update(browserLoadedMsg{gen: m.browserGen, path: "/music"})
	m = next.(*Model)
	if !m.statusIsError || !strings.Contains(m.statusMsg, "bad bookmark file") {
		t.Fatalf("status after a successful browser load = %q (error=%v), want the bookmark error", m.statusMsg, m.statusIsError)
	}

	m.browserEntriesPath = "/music"
	m, cmd := pressForCmd(t, m, runeKey('f'))
	if cmd != nil {
		t.Fatalf("add returned a save command after a bookmark load error")
	}
	if got := bookmarks.saveCount(); got != 0 {
		t.Errorf("save count = %d, want 0 while bookmark loading failed", got)
	}
	if !strings.Contains(m.statusMsg, "unavailable") || !strings.Contains(m.statusMsg, "bad bookmark file") {
		t.Errorf("status = %q, want an unavailable-bookmarks error", m.statusMsg)
	}
}

func TestBookmarksPersistWithRealStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bookmarks.json")
	bookmarks := store.NewBookmarks(path)
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = bookmarks
	})
	m = resize(t, m, 100, 30)
	m.browserEntriesPath = "/music"

	m, cmd := pressForCmd(t, m, runeKey('f'))
	m = applyBrowserCmd(t, m, cmd)
	if want := []string{"/music"}; !reflect.DeepEqual(m.bookmarks, want) {
		t.Fatalf("bookmarks = %v, want %v", m.bookmarks, want)
	}

	reloaded := newTestModel(t, func(opts *Options) {
		opts.Scanner = &testScanner{}
		opts.StartDir = "/music"
		opts.BookmarkStore = store.NewBookmarks(path)
	})
	if want := []string{"/music"}; !reflect.DeepEqual(reloaded.bookmarks, want) {
		t.Errorf("reloaded bookmarks = %v, want %v", reloaded.bookmarks, want)
	}
}

func TestNewReportsBookmarkLoadError(t *testing.T) {
	bookmarks := &fakeBookmarkStore{loadErr: errors.New("bad bookmark file")}
	m := newTestModel(t, func(opts *Options) {
		opts.BookmarkStore = bookmarks
	})

	if len(m.bookmarks) != 0 {
		t.Errorf("bookmarks = %v, want empty after a load error", m.bookmarks)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "bad bookmark file") {
		t.Errorf("status = %q (error=%v), want the load error", m.statusMsg, m.statusIsError)
	}
}
