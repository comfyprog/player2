package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/library"
	"player2/internal/meta"
	"player2/internal/queue"
)

// testScanner is a small in-memory Scanner used by the browser tests.
type testScanner struct {
	dirs      map[string][]library.Entry
	cues      map[string][]library.Entry
	walks     map[string][]library.Entry
	dirErr    map[string]error
	cueErr    map[string]error
	resolve   func(cuePath string, track int) (library.Entry, error)
	dirCalls  int
	cueCalls  int
	walkCalls int
}

func (s *testScanner) ReadDir(_ context.Context, dir string) ([]library.Entry, error) {
	s.dirCalls++
	if err := s.dirErr[dir]; err != nil {
		return nil, err
	}
	return append([]library.Entry(nil), s.dirs[dir]...), nil
}

func (s *testScanner) ReadCue(_ context.Context, cuePath string) ([]library.Entry, error) {
	s.cueCalls++
	if err := s.cueErr[cuePath]; err != nil {
		return nil, err
	}
	return append([]library.Entry(nil), s.cues[cuePath]...), nil
}

func (s *testScanner) Walk(_ context.Context, dir string) ([]library.Entry, error) {
	s.walkCalls++
	return append([]library.Entry(nil), s.walks[dir]...), nil
}

func (s *testScanner) ResolveCueTrack(_ context.Context, cuePath string, track int) (library.Entry, error) {
	if s.resolve != nil {
		return s.resolve(cuePath, track)
	}
	return library.Entry{}, fmt.Errorf("no resolver for %s track %d", cuePath, track)
}

func newBrowserModel(t *testing.T, scanner Scanner) (*Model, *fakePlayer) {
	t.Helper()
	fp := newFakePlayer()
	m := newTestModel(t, func(opts *Options) {
		opts.Scanner = scanner
		opts.Player = fp
		opts.StartDir = "/music"
	})
	m = resize(t, m, 100, 30)
	return m, fp
}

func pressForCmd(t *testing.T, m *Model, msg tea.KeyPressMsg) (*Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return updated, cmd
}

func applyBrowserCmd(t *testing.T, m *Model, cmd tea.Cmd) *Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("command is nil")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		t.Fatalf("unexpected batch message with %d commands", len(batch))
	}
	next, _ := m.Update(msg)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return updated
}

func audioEntry(name, path string) library.Entry {
	return library.Entry{Kind: library.KindAudio, Name: name, Path: path}
}

func cueTrackEntry(name, path, cuePath string, track int, start, duration time.Duration) library.Entry {
	return library.Entry{
		Kind:     library.KindCueTrack,
		Name:     name,
		Path:     path,
		CuePath:  cuePath,
		TrackNo:  track,
		Start:    start,
		Duration: duration,
		Meta: meta.Info{
			Title:    name,
			Artist:   "Artist",
			Album:    "Album",
			TrackNo:  track,
			Duration: duration,
		},
	}
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

const testCueSheet = `PERFORMER "Artist"
TITLE "Album"
FILE "song.flac" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    PERFORMER "Artist"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Two"
    PERFORMER "Artist"
    INDEX 01 03:00:00
`

func TestBrowserEnterDirectoryAndBackRestoresPosition(t *testing.T) {
	entries := make([]library.Entry, 10)
	for i := range entries {
		entries[i] = audioEntry(fmt.Sprintf("track%02d.flac", i), fmt.Sprintf("/music/track%02d.flac", i))
	}
	entries[6] = library.Entry{Kind: library.KindDir, Name: "Album", Path: "/music/Album"}
	sc := &testScanner{dirs: map[string][]library.Entry{
		"/music":       entries,
		"/music/Album": {audioEntry("inside.flac", "/music/Album/inside.flac")},
	}}
	m, _ := newBrowserModel(t, sc)
	m = resize(t, m, 80, 10)
	m.browserEntries = entries
	m.browserSelected = 6
	m.browserOffset = 3

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = applyBrowserCmd(t, m, cmd)
	if m.browserPath != "/music/Album" {
		t.Fatalf("browserPath = %q, want /music/Album", m.browserPath)
	}
	if len(m.browserEntries) != 1 {
		t.Fatalf("entries after descend = %d, want 1", len(m.browserEntries))
	}
	if len(m.browserStack) != 1 {
		t.Fatalf("stack depth = %d, want 1", len(m.browserStack))
	}

	m, cmd = pressForCmd(t, m, specialKey(tea.KeyBackspace))
	m = applyBrowserCmd(t, m, cmd)
	if m.browserPath != "/music" {
		t.Errorf("browserPath after back = %q, want /music", m.browserPath)
	}
	if m.browserSelected != 6 {
		t.Errorf("selected after back = %d, want 6", m.browserSelected)
	}
	if m.browserOffset != 3 {
		t.Errorf("offset after back = %d, want 3", m.browserOffset)
	}
	if len(m.browserStack) != 0 {
		t.Errorf("stack depth after back = %d, want 0", len(m.browserStack))
	}
}

func TestBrowserEnterCueFolderAndBack(t *testing.T) {
	cuePath := "/music/Album.cue"
	tracks := []library.Entry{
		cueTrackEntry("One", "/music/song.flac", cuePath, 1, 0, 3*time.Minute),
		cueTrackEntry("Two", "/music/song.flac", cuePath, 2, 3*time.Minute, 2*time.Minute),
	}
	sc := &testScanner{
		dirs: map[string][]library.Entry{
			"/music": {{Kind: library.KindCueDir, Name: "Album.cue/", Path: cuePath, CuePath: cuePath}},
		},
		cues: map[string][]library.Entry{cuePath: tracks},
	}
	m, _ := newBrowserModel(t, sc)
	m.browserEntries = sc.dirs["/music"]

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = applyBrowserCmd(t, m, cmd)
	if !m.browserCue {
		t.Error("browserCue = false after entering a CUE folder")
	}
	if len(m.browserEntries) != 2 || m.browserEntries[0].Kind != library.KindCueTrack {
		t.Fatalf("CUE entries = %#v, want two tracks", m.browserEntries)
	}

	m, cmd = pressForCmd(t, m, specialKey(tea.KeyBackspace))
	m = applyBrowserCmd(t, m, cmd)
	if m.browserCue {
		t.Error("browserCue = true after leaving a CUE folder")
	}
	if m.browserPath != "/music" || len(m.browserEntries) != 1 {
		t.Errorf("back location = %q with %d entries, want /music with 1", m.browserPath, len(m.browserEntries))
	}
}

func TestBrowserEnterInvalidCueShowsErrorAndDoesNotPlay(t *testing.T) {
	sc := &testScanner{}
	m, fp := newBrowserModel(t, sc)
	m.browserEntries = []library.Entry{{
		Kind: library.KindInvalidCue,
		Name: "Broken.cue",
		Path: "/music/Broken.cue",
		Err:  errors.New("corrupt cue sheet"),
	}}

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	if cmd != nil {
		t.Fatalf("Enter on invalid CUE returned command %T, want nil", cmd)
	}
	if !m.statusIsError {
		t.Error("statusIsError = false, want true")
	}
	if !strings.Contains(m.statusMsg, "corrupt cue sheet") {
		t.Errorf("statusMsg = %q, want the CUE error", m.statusMsg)
	}
	if len(fp.played) != 0 {
		t.Errorf("player played %d items, want none", len(fp.played))
	}
}

func TestBrowserEnterAudioQueuesLevelAndPlaysSelection(t *testing.T) {
	cuePath := "/music/Album.cue"
	tracks := []library.Entry{
		cueTrackEntry("One", "/music/song.flac", cuePath, 1, 0, 2*time.Minute),
		cueTrackEntry("Two", "/music/song.flac", cuePath, 2, 2*time.Minute, 90*time.Second),
	}
	entries := []library.Entry{
		audioEntry("a.flac", "/music/a.flac"),
		{Kind: library.KindCueDir, Name: "Album.cue/", Path: cuePath, CuePath: cuePath},
		audioEntry("b.flac", "/music/b.flac"),
		{Kind: library.KindInvalidCue, Name: "Bad.cue", Path: "/music/Bad.cue", Err: errors.New("bad")},
		{Kind: library.KindPlaylist, Name: "list.m3u", Path: "/music/list.m3u"},
	}
	entries[0].Duration = time.Minute
	entries[2].Duration = 2 * time.Minute
	sc := &testScanner{cues: map[string][]library.Entry{cuePath: tracks}}
	m, fp := newBrowserModel(t, sc)
	m.browserEntries = entries
	m.browserSelected = 2

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = applyBrowserCmd(t, m, cmd)

	snapshot := m.queue.Snapshot()
	if len(snapshot.Items) != 4 {
		t.Fatalf("queue length = %d, want 4: %#v", len(snapshot.Items), snapshot.Items)
	}
	wantTitles := []string{"a", "One", "Two", "b"}
	for i, want := range wantTitles {
		if got := snapshot.Items[i].Title; got != want {
			t.Errorf("item %d title = %q, want %q", i, got, want)
		}
	}
	if got := snapshot.Items[1].Duration; got != 2*time.Minute {
		t.Errorf("CUE track duration = %v, want 2m", got)
	}
	if got := snapshot.Items[2].Duration; got != 90*time.Second {
		t.Errorf("second CUE track duration = %v, want 90s", got)
	}
	if snapshot.Current != 3 {
		t.Errorf("queue current = %d, want 3 (selected audio b)", snapshot.Current)
	}
	if len(fp.played) != 1 {
		t.Fatalf("player played %d items, want 1", len(fp.played))
	}
	if fp.played[0].Path != "/music/b.flac" {
		t.Errorf("played path = %q, want /music/b.flac", fp.played[0].Path)
	}
}

func TestBrowserEnterCueTrackQueuesWholeCueFolder(t *testing.T) {
	cuePath := "/music/Album.cue"
	tracks := []library.Entry{
		cueTrackEntry("One", "/music/song.flac", cuePath, 1, 0, time.Minute),
		cueTrackEntry("Two", "/music/song.flac", cuePath, 2, time.Minute, time.Minute),
		cueTrackEntry("Three", "/music/song.flac", cuePath, 3, 2*time.Minute, time.Minute),
	}
	sc := &testScanner{cues: map[string][]library.Entry{cuePath: tracks}}
	m, fp := newBrowserModel(t, sc)
	m.browserPath = cuePath
	m.browserCue = true
	m.browserEntries = tracks
	m.browserSelected = 1

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = applyBrowserCmd(t, m, cmd)

	snapshot := m.queue.Snapshot()
	if len(snapshot.Items) != 3 {
		t.Fatalf("queue length = %d, want 3", len(snapshot.Items))
	}
	if snapshot.Current != 1 {
		t.Errorf("queue current = %d, want 1", snapshot.Current)
	}
	if len(fp.played) != 1 || fp.played[0].CueTrack != 2 {
		t.Fatalf("played = %#v, want CUE track 2", fp.played)
	}
	if fp.played[0].CuePath != cuePath {
		t.Errorf("played cue path = %q, want %q", fp.played[0].CuePath, cuePath)
	}
}

func TestBrowserAddSelection(t *testing.T) {
	t.Run("directory walks recursively", func(t *testing.T) {
		sc := &testScanner{walks: map[string][]library.Entry{
			"/music/Album": {
				audioEntry("one.flac", "/music/Album/one.flac"),
				audioEntry("two.flac", "/music/Album/two.flac"),
			},
		}}
		m, _ := newBrowserModel(t, sc)
		m.browserEntries = []library.Entry{{Kind: library.KindDir, Name: "Album", Path: "/music/Album"}}

		m, cmd := pressForCmd(t, m, runeKey('a'))
		m = applyBrowserCmd(t, m, cmd)
		if sc.walkCalls != 1 {
			t.Errorf("Walk calls = %d, want 1", sc.walkCalls)
		}
		if got := len(m.queue.Snapshot().Items); got != 2 {
			t.Errorf("queue length = %d, want 2", got)
		}
	})

	t.Run("cue folder expands tracks", func(t *testing.T) {
		cuePath := "/music/Album.cue"
		sc := &testScanner{cues: map[string][]library.Entry{
			cuePath: {
				cueTrackEntry("One", "/music/song.flac", cuePath, 1, 0, time.Minute),
				cueTrackEntry("Two", "/music/song.flac", cuePath, 2, time.Minute, time.Minute),
			},
		}}
		m, _ := newBrowserModel(t, sc)
		m.browserEntries = []library.Entry{{Kind: library.KindCueDir, Name: "Album.cue/", Path: cuePath, CuePath: cuePath}}

		m, cmd := pressForCmd(t, m, runeKey('a'))
		m = applyBrowserCmd(t, m, cmd)
		if got := len(m.queue.Snapshot().Items); got != 2 {
			t.Errorf("queue length = %d, want 2", got)
		}
	})

	t.Run("playlist expands items", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, dir, "one.flac", "")
		playlistPath := writeTestFile(t, dir, "list.m3u", "#EXTM3U\n#EXTINF:60,Artist - Song\none.flac\n")
		m, _ := newBrowserModel(t, &testScanner{})
		m.browserEntries = []library.Entry{{Kind: library.KindPlaylist, Name: "list.m3u", Path: playlistPath}}

		m, cmd := pressForCmd(t, m, runeKey('a'))
		m = applyBrowserCmd(t, m, cmd)
		items := m.queue.Snapshot().Items
		if len(items) != 1 {
			t.Fatalf("queue length = %d, want 1", len(items))
		}
		if items[0].Title != "Song" || items[0].Artist != "Artist" {
			t.Errorf("playlist item = %#v, want Song by Artist", items[0])
		}
	})

	t.Run("add all appends current location", func(t *testing.T) {
		sc := &testScanner{walks: map[string][]library.Entry{
			"/music": {
				audioEntry("one.flac", "/music/one.flac"),
				audioEntry("two.flac", "/music/two.flac"),
			},
		}}
		m, _ := newBrowserModel(t, sc)
		m.queue.Append(queue.Item{Title: "existing"})
		m.browserEntries = []library.Entry{audioEntry("one.flac", "/music/one.flac")}

		m, cmd := pressForCmd(t, m, runeKey('A'))
		m = applyBrowserCmd(t, m, cmd)
		snapshot := m.queue.Snapshot()
		if len(snapshot.Items) != 3 {
			t.Fatalf("queue length = %d, want 3", len(snapshot.Items))
		}
		if snapshot.Items[0].Title != "existing" {
			t.Errorf("first item = %q, want existing", snapshot.Items[0].Title)
		}
	})
}

func TestBrowserFilterSearchMode(t *testing.T) {
	m, _ := newBrowserModel(t, &testScanner{})
	m = resize(t, m, 80, 10)
	m.browserEntries = []library.Entry{
		audioEntry("Alpha.flac", "/music/Alpha.flac"),
		audioEntry("Alpine.flac", "/music/Alpine.flac"),
		audioEntry("Beta.flac", "/music/Beta.flac"),
		audioEntry("Gamma.flac", "/music/Gamma.flac"),
	}

	m, _ = pressForCmd(t, m, runeKey('/'))
	if !m.browserSearching {
		t.Fatal("browserSearching = false after /")
	}
	for _, r := range "ALP" {
		m, _ = pressForCmd(t, m, runeKey(r))
	}
	if m.browserFilter != "ALP" {
		t.Fatalf("filter = %q, want ALP", m.browserFilter)
	}
	if got := len(m.browserVisibleEntries()); got != 2 {
		t.Fatalf("visible entries = %d, want 2", got)
	}

	m, _ = pressForCmd(t, m, specialKey(tea.KeyDown))
	if m.browserSelected != 1 {
		t.Errorf("selected after down = %d, want 1", m.browserSelected)
	}
	m, _ = pressForCmd(t, m, specialKey(tea.KeyUp))
	if m.browserSelected != 0 {
		t.Errorf("selected after up = %d, want 0", m.browserSelected)
	}

	m, _ = pressForCmd(t, m, specialKey(tea.KeyEnter))
	if m.browserSearching {
		t.Error("search mode still active after Enter")
	}
	if m.browserFilter != "ALP" {
		t.Errorf("filter after Enter = %q, want ALP", m.browserFilter)
	}

	m, _ = pressForCmd(t, m, runeKey('/'))
	m, _ = pressForCmd(t, m, specialKey(tea.KeyBackspace))
	for range 2 {
		m, _ = pressForCmd(t, m, runeKey('z'))
	}
	if got := len(m.browserVisibleEntries()); got != 0 {
		t.Errorf("visible entries for no-match query = %d, want 0", got)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "No matches") {
		t.Errorf("no-match view lacks empty state:\n%s", plain)
	}

	m, _ = pressForCmd(t, m, specialKey(tea.KeyEscape))
	if m.browserSearching || m.browserFilter != "" {
		t.Errorf("Esc left search active=%v filter=%q", m.browserSearching, m.browserFilter)
	}
	if got := len(m.browserVisibleEntries()); got != 4 {
		t.Errorf("visible entries after Esc = %d, want 4", got)
	}
}

func TestBrowserLoadErrorAndStaleGeneration(t *testing.T) {
	sc := &testScanner{dirErr: map[string]error{"/music/Bad": errors.New("permission denied")}}
	m, _ := newBrowserModel(t, sc)
	m.browserEntries = []library.Entry{{Kind: library.KindDir, Name: "Bad", Path: "/music/Bad"}}

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = applyBrowserCmd(t, m, cmd)
	if !m.statusIsError {
		t.Error("statusIsError = false, want true after load error")
	}
	if !strings.Contains(m.statusMsg, "permission denied") {
		t.Errorf("statusMsg = %q, want load error", m.statusMsg)
	}
	if m.browserLoading {
		t.Error("browserLoading = true after failed load")
	}
	_ = m.View()

	before := m.browserEntries
	next, _ := m.Update(browserLoadedMsg{
		gen:     m.browserGen - 1,
		path:    m.browserPath,
		entries: []library.Entry{audioEntry("stale.flac", "/music/stale.flac")},
	})
	updated := next.(*Model)
	if len(updated.browserEntries) != len(before) || (len(before) > 0 && updated.browserEntries[0].Name != before[0].Name) {
		t.Errorf("stale generation replaced entries with %#v", updated.browserEntries)
	}
}

func TestBrowserPlaylistCueResolutionAndMissingFiles(t *testing.T) {
	t.Run("resolves cue track", func(t *testing.T) {
		dir := t.TempDir()
		audio := writeTestFile(t, dir, "song.flac", "")
		cuePath := writeTestFile(t, dir, "Album.cue", testCueSheet)
		playlistPath := writeTestFile(t, dir, "list.m3u",
			"#EXTM3U\n#EXTINF:180,Artist - One\n#EXT-X-PLAYER2-CUE:cue=Album.cue;track=1\nsong.flac\n")
		sc := &testScanner{resolve: func(cue string, track int) (library.Entry, error) {
			if cue != cuePath || track != 1 {
				return library.Entry{}, fmt.Errorf("unexpected resolve %q %d", cue, track)
			}
			return library.Entry{
				Kind: library.KindCueTrack, Path: audio, CuePath: cuePath, TrackNo: 1,
				Start: 10 * time.Second, Duration: 2 * time.Minute,
				Meta: meta.Info{Title: "One", Artist: "Artist", Album: "Resolved Album", TrackNo: 1},
			}, nil
		}}
		m, _ := newBrowserModel(t, sc)
		m.browserEntries = []library.Entry{{Kind: library.KindPlaylist, Name: "list.m3u", Path: playlistPath}}

		m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
		m = applyBrowserCmd(t, m, cmd)
		items := m.queue.Snapshot().Items
		if len(items) != 1 {
			t.Fatalf("queue length = %d, want 1", len(items))
		}
		item := items[0]
		if item.Path != audio || item.CuePath != cuePath || item.CueTrack != 1 {
			t.Errorf("resolved item = %#v", item)
		}
		if item.Start != 10*time.Second {
			t.Errorf("start = %v, want 10s", item.Start)
		}
		if item.Duration != 180*time.Second {
			t.Errorf("duration = %v, want playlist duration 3m", item.Duration)
		}
		if item.Album != "Resolved Album" || item.Title != "One" {
			t.Errorf("metadata = %#v, want resolved album and playlist title", item)
		}
	})

	t.Run("falls back to whole file when resolution fails", func(t *testing.T) {
		dir := t.TempDir()
		audio := writeTestFile(t, dir, "song.flac", "")
		writeTestFile(t, dir, "Album.cue", testCueSheet)
		playlistPath := writeTestFile(t, dir, "list.m3u",
			"#EXTM3U\n#EXTINF:180,Artist - One\n#EXT-X-PLAYER2-CUE:cue=Album.cue;track=1\nsong.flac\n")
		sc := &testScanner{resolve: func(string, int) (library.Entry, error) {
			return library.Entry{}, errors.New("track vanished")
		}}
		m, _ := newBrowserModel(t, sc)
		m.browserEntries = []library.Entry{{Kind: library.KindPlaylist, Name: "list.m3u", Path: playlistPath}}

		m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
		m = applyBrowserCmd(t, m, cmd)
		items := m.queue.Snapshot().Items
		if len(items) != 1 {
			t.Fatalf("queue length = %d, want 1", len(items))
		}
		item := items[0]
		if item.Path != audio || item.CuePath != "" || item.CueTrack != 0 || item.Start != 0 {
			t.Errorf("fallback item = %#v, want whole-file entry", item)
		}
	})

	t.Run("skips missing files with a count", func(t *testing.T) {
		dir := t.TempDir()
		present := writeTestFile(t, dir, "present.flac", "")
		playlistPath := writeTestFile(t, dir, "list.m3u", "#EXTM3U\nmissing.flac\npresent.flac\n")
		m, _ := newBrowserModel(t, &testScanner{})
		m.browserEntries = []library.Entry{{Kind: library.KindPlaylist, Name: "list.m3u", Path: playlistPath}}

		m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
		m = applyBrowserCmd(t, m, cmd)
		items := m.queue.Snapshot().Items
		if len(items) != 1 || items[0].Path != present {
			t.Fatalf("items = %#v, want only present.flac", items)
		}
		if !strings.Contains(m.statusMsg, "skipped 1") {
			t.Errorf("statusMsg = %q, want a skipped count", m.statusMsg)
		}
	})
}

func TestQueueItemFromEntryMetadata(t *testing.T) {
	cue := library.Entry{
		Kind:     library.KindCueTrack,
		Name:     "03. Three — Artist",
		Path:     "/music/song.flac",
		CuePath:  "/music/Album.cue",
		TrackNo:  3,
		Start:    30 * time.Second,
		Duration: 2 * time.Minute,
		Meta: meta.Info{
			Title: "Three", Artist: "Artist", Album: "Album", TrackNo: 3,
		},
	}
	item := queueItemFromEntry(cue)
	if item.Path != cue.Path || item.CuePath != cue.CuePath || item.CueTrack != 3 {
		t.Errorf("CUE location = %#v", item)
	}
	if item.Start != 30*time.Second || item.Duration != 2*time.Minute {
		t.Errorf("CUE timing = %#v", item)
	}
	if item.Title != "Three" || item.Artist != "Artist" || item.Album != "Album" || item.TrackNo != 3 {
		t.Errorf("CUE metadata = %#v", item)
	}

	plain := queueItemFromEntry(library.Entry{Kind: library.KindAudio, Path: "/music/song.flac", Duration: 5 * time.Second})
	if plain.Title != "song" {
		t.Errorf("plain filename fallback = %q, want song", plain.Title)
	}
	if plain.Duration != 5*time.Second {
		t.Errorf("plain duration = %v, want 5s", plain.Duration)
	}

	titled := queueItemFromEntry(library.Entry{
		Kind: library.KindAudio,
		Path: "/music/song.flac",
		Meta: meta.Info{Title: "A Title", Artist: "Artist", Album: "Album", TrackNo: 4},
	})
	if titled.Title != "A Title" || titled.Artist != "Artist" || titled.Album != "Album" || titled.TrackNo != 4 {
		t.Errorf("plain metadata = %#v", titled)
	}
}

func TestBrowserBreadcrumbIsHomeAbbreviatedAndWidthSafe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m, _ := newBrowserModel(t, &testScanner{})
	m = resize(t, m, 60, 10)

	m.browserPath = filepath.Join(home, "Music", "Album.cue")
	m.browserCue = true
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "Files: ~/Music/Album.cue") {
		t.Errorf("breadcrumb lacks home abbreviation: %q", plain)
	}

	m.browserPath = filepath.Join(home, "Music", strings.Repeat("VeryLongArtistName", 4), "Album.cue")
	content := m.View().Content
	lines := strings.Split(content, "\n")
	if len(lines) != 10 {
		t.Fatalf("line count = %d, want 10", len(lines))
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != 60 {
			t.Errorf("line %d width = %d, want 60", i, got)
		}
	}
	title := ""
	for _, line := range lines {
		if strings.Contains(ansi.Strip(line), "Files:") {
			title = ansi.Strip(line)
			break
		}
	}
	if !strings.Contains(title, "Files:") {
		t.Errorf("title = %q, want Files breadcrumb", title)
	}
	if !strings.Contains(title, "…") {
		t.Errorf("long breadcrumb = %q, want left truncation", title)
	}
}

func TestBrowserEmptyDirectoryAndRescanPreservesSelection(t *testing.T) {
	sc := &testScanner{dirs: map[string][]library.Entry{"/music/empty": nil}}
	m, _ := newBrowserModel(t, sc)
	m = resize(t, m, 80, 24)
	m.browserPath = "/music/empty"
	m.browserEntries = nil
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "Empty") {
		t.Errorf("empty directory view lacks empty state:\n%s", plain)
	}

	// Rescan preserves the selected entry even when its index moves.
	sc.dirs["/music"] = []library.Entry{
		audioEntry("a.flac", "/music/a.flac"),
		audioEntry("b.flac", "/music/b.flac"),
	}
	m.browserPath = "/music"
	m.browserEntries = []library.Entry{audioEntry("b.flac", "/music/b.flac")}
	m.browserSelected = 0
	m.browserOffset = 0

	m, cmd := pressForCmd(t, m, runeKey('R'))
	m = applyBrowserCmd(t, m, cmd)
	if len(m.browserEntries) != 2 {
		t.Fatalf("rescan entries = %d, want 2", len(m.browserEntries))
	}
	if m.browserSelected != 1 {
		t.Errorf("selected after rescan = %d, want 1 (b.flac)", m.browserSelected)
	}
}
