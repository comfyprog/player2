package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/library"
	"player2/internal/meta"
	"player2/internal/queue"
)

// pressWithCmd delivers a key and returns both the updated model and the
// command it produced, so tests can execute asynchronous work explicitly.
func pressWithCmd(t *testing.T, m *Model, msg tea.KeyPressMsg) (*Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return updated, cmd
}

// applyCmd executes one command and feeds its message back into the model.
func applyCmd(t *testing.T, m *Model, cmd tea.Cmd) *Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("command = nil, want a probe command")
	}
	msg := cmd()
	next, _ := m.Update(msg)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return updated
}

func TestInfoBrowserAudioShowsProbedDetails(t *testing.T) {
	path := "/tmp/music/track.flac"
	prober := &fakeProber{info: map[string]meta.Info{
		path: {
			Duration:      3*time.Minute + 45*time.Second,
			Codec:         "flac",
			Container:     "FLAC (Free Lossless Audio Codec)",
			BitRate:       1411200,
			SampleRate:    44100,
			Channels:      2,
			ChannelLayout: "stereo",
			BitDepth:      16,
			Tags: map[string]string{
				"album_artist": "Album Artist",
				"artist":       "The Artist",
				"title":        "Song Title",
			},
		},
	}}

	m := newTestModel(t, func(opts *Options) { opts.Prober = prober })
	m = resize(t, m, 100, 30)
	m.browserEntries = []library.Entry{{
		Kind: library.KindAudio,
		Name: "track.flac",
		Path: path,
	}}
	m.browserSelected = 0

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	if !m.infoOpen() {
		t.Fatal("info window did not open for an audio entry")
	}
	if !m.info.loading {
		t.Error("info window is not loading before the probe returns")
	}

	m = applyCmd(t, m, cmd)
	if m.info.loading {
		t.Error("info window still loading after the probe returned")
	}

	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"Track info",
		"Name: track.flac",
		"Path: " + path,
		"Duration: 3:45",
		"Container: FLAC (Free Lossless Audio Codec)",
		"Codec: flac",
		"Bitrate: 1411 kbps",
		"Sample rate: 44100 Hz",
		"Channels: 2 (stereo)",
		"Bit depth: 16-bit",
		"ALBUM ARTIST: Album Artist",
		"ARTIST: The Artist",
		"TITLE: Song Title",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("info window missing %q\n%s", want, plain)
		}
	}
}

func TestInfoBrowserRejectsNonAudioEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry library.Entry
		want  string
	}{
		{
			name:  "directory",
			entry: library.Entry{Kind: library.KindDir, Name: "Artist", Path: "/tmp/music/Artist"},
			want:  "info: select an audio file",
		},
		{
			name: "cue track",
			entry: library.Entry{
				Kind: library.KindCueTrack, Name: "01. Song", Path: "/tmp/music/album.flac",
				CuePath: "/tmp/music/album.cue", TrackNo: 1, Duration: time.Minute,
			},
			want: "info: CUE tracks not supported yet",
		},
		{
			name:  "playlist",
			entry: library.Entry{Kind: library.KindPlaylist, Name: "mix.m3u", Path: "/tmp/music/mix.m3u"},
			want:  "info: select an audio file",
		},
		{
			name:  "invalid cue",
			entry: library.Entry{Kind: library.KindInvalidCue, Name: "bad.cue", Path: "/tmp/music/bad.cue"},
			want:  "info: select an audio file",
		},
		{
			name:  "cue folder",
			entry: library.Entry{Kind: library.KindCueDir, Name: "album.cue/", Path: "/tmp/music/album.cue"},
			want:  "info: select an audio file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t, nil)
			m = resize(t, m, 100, 30)
			m.browserEntries = []library.Entry{tt.entry}

			m, cmd := pressWithCmd(t, m, runeKey('i'))
			if m.infoOpen() {
				t.Fatal("info window opened for a non-audio entry")
			}
			if cmd != nil {
				t.Fatal("non-audio entry produced an info command")
			}
			if !strings.Contains(m.statusMsg, tt.want) {
				t.Errorf("status = %q, want it to contain %q", m.statusMsg, tt.want)
			}
		})
	}
}

func TestInfoBrowserEmptyPathShowsStatus(t *testing.T) {
	m := newTestModel(t, nil)
	m = resize(t, m, 100, 30)
	m.browserEntries = []library.Entry{{Kind: library.KindAudio, Name: "broken.flac"}}

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	if m.infoOpen() || cmd != nil {
		t.Fatal("empty-path audio entry opened the info window")
	}
	if !strings.Contains(m.statusMsg, "select an audio file") {
		t.Errorf("status = %q, want select-audio-file message", m.statusMsg)
	}
}

func TestInfoPlaylistPlainItemProbesPath(t *testing.T) {
	path := "/tmp/music/song.mp3"
	prober := &fakeProber{info: map[string]meta.Info{
		path: {Codec: "mp3", Duration: 2 * time.Minute, SampleRate: 48000, Channels: 1},
	}}
	m := newTestModel(t, func(opts *Options) { opts.Prober = prober })
	m = resize(t, m, 100, 30)
	m.focus = PanePlaylist
	m.queue.Append(queue.Item{Path: path, Title: "Song"})
	m.playlistSelected = 0

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	if !m.infoOpen() {
		t.Fatal("info window did not open for a playlist item")
	}

	m = applyCmd(t, m, cmd)
	if got := prober.callCount(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if got := prober.calls[0]; got != path {
		t.Errorf("probed path = %q, want %q", got, path)
	}

	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"Name: song.mp3",
		"Path: " + path,
		"Duration: 2:00",
		"Codec: mp3",
		"Sample rate: 48000 Hz",
		"Channels: 1",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("info window missing %q\n%s", want, plain)
		}
	}
}

func TestInfoPlaylistEmptyQueueAndEmptyPath(t *testing.T) {
	t.Run("empty queue", func(t *testing.T) {
		m := newTestModel(t, nil)
		m = resize(t, m, 100, 30)
		m.focus = PanePlaylist

		m, cmd := pressWithCmd(t, m, runeKey('i'))
		if m.infoOpen() || cmd != nil {
			t.Fatal("empty queue opened the info window")
		}
		if !strings.Contains(m.statusMsg, "queue is empty") {
			t.Errorf("status = %q, want queue-is-empty message", m.statusMsg)
		}
	})

	t.Run("empty path", func(t *testing.T) {
		m := newTestModel(t, nil)
		m = resize(t, m, 100, 30)
		m.focus = PanePlaylist
		m.queue.Append(queue.Item{Title: "broken"})

		m, cmd := pressWithCmd(t, m, runeKey('i'))
		if m.infoOpen() || cmd != nil {
			t.Fatal("empty path opened the info window")
		}
		if !strings.Contains(m.statusMsg, "select an audio file") {
			t.Errorf("status = %q, want select-audio-file message", m.statusMsg)
		}
	})
}

func TestInfoPlaylistSkipsCueTracks(t *testing.T) {
	prober := &fakeProber{}
	m := newTestModel(t, func(opts *Options) { opts.Prober = prober })
	m = resize(t, m, 100, 30)
	m.focus = PanePlaylist
	m.queue.Append(queue.Item{
		Path:     "/tmp/music/album.flac",
		CuePath:  "/tmp/music/album.cue",
		CueTrack: 1,
		Duration: time.Minute,
	})

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	if m.infoOpen() {
		t.Fatal("info window opened for a CUE track")
	}
	if cmd != nil {
		t.Fatal("CUE track produced an info command")
	}
	if !strings.Contains(m.statusMsg, "CUE tracks not supported yet") {
		t.Errorf("status = %q, want CUE-track message", m.statusMsg)
	}
	if got := prober.callCount(); got != 0 {
		t.Errorf("probe calls = %d, want 0", got)
	}
}

func TestInfoStaleProbeResultIsIgnored(t *testing.T) {
	pathA := "/tmp/music/a.flac"
	pathB := "/tmp/music/b.flac"
	prober := &fakeProber{info: map[string]meta.Info{
		pathA: {Title: "A", Codec: "flac"},
		pathB: {Title: "B", Codec: "flac"},
	}}
	m := newTestModel(t, func(opts *Options) { opts.Prober = prober })
	m = resize(t, m, 100, 30)
	m.browserEntries = []library.Entry{
		{Kind: library.KindAudio, Name: "a.flac", Path: pathA},
		{Kind: library.KindAudio, Name: "b.flac", Path: pathB},
	}

	m, cmdA := pressWithCmd(t, m, runeKey('i'))
	m = press(t, m, specialKey(tea.KeyEscape))
	if m.infoOpen() {
		t.Fatal("info window did not close on Esc")
	}

	m.browserSelected = 1
	m, cmdB := pressWithCmd(t, m, runeKey('i'))
	if !m.infoOpen() || m.info.path != pathB {
		t.Fatalf("reopened info path = %q, want %q", m.info.path, pathB)
	}

	// A late result for A must not replace the open B window.
	m = applyCmd(t, m, cmdA)
	if m.info.path != pathB {
		t.Fatalf("stale result changed info path to %q, want %q", m.info.path, pathB)
	}
	if m.info.info.Title != "" || !m.info.loading {
		t.Errorf("stale result applied: info=%+v loading=%v", m.info.info, m.info.loading)
	}

	m = applyCmd(t, m, cmdB)
	if m.info.loading {
		t.Error("info window still loading after B result")
	}
	if m.info.info.Title != "B" {
		t.Errorf("info title = %q, want B", m.info.info.Title)
	}
}

func TestInfoWithoutProberShowsBasics(t *testing.T) {
	path := "/tmp/music/track.flac"
	m := newTestModel(t, func(opts *Options) { opts.Prober = nil })
	m = resize(t, m, 100, 30)
	m.browserEntries = []library.Entry{{Kind: library.KindAudio, Name: "track.flac", Path: path}}

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	if !m.infoOpen() {
		t.Fatal("info window did not open without a prober")
	}
	if cmd != nil {
		t.Fatal("nil prober produced a command")
	}

	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"Name: track.flac",
		"Path: " + path,
		"Duration: --:--",
		"Tags:",
		"(none)",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("info window missing %q\n%s", want, plain)
		}
	}
}

func TestInfoProbeErrorShowsBasics(t *testing.T) {
	path := "/tmp/music/broken.flac"
	prober := &fakeProber{err: map[string]error{path: errors.New("invalid audio stream")}}
	m := newTestModel(t, func(opts *Options) { opts.Prober = prober })
	m = resize(t, m, 100, 30)
	m.browserEntries = []library.Entry{{
		Kind: library.KindAudio, Name: "broken.flac", Path: path, Duration: 90 * time.Second,
	}}

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	m = applyCmd(t, m, cmd)

	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"Name: broken.flac",
		"Path: " + path,
		"Duration: 1:30",
		"Details unavailable: invalid audio stream",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("error info window missing %q\n%s", want, plain)
		}
	}
}

func TestInfoHonorsCustomKeyBinding(t *testing.T) {
	path := "/tmp/music/track.flac"
	prober := &fakeProber{info: map[string]meta.Info{path: {Codec: "flac"}}}
	m := newTestModel(t, func(opts *Options) {
		opts.Prober = prober
		opts.Config.Keys = map[string][]string{"info": {"I"}}
	})
	m = resize(t, m, 100, 30)
	m.browserEntries = []library.Entry{{Kind: library.KindAudio, Name: "track.flac", Path: path}}

	if m, cmd := pressWithCmd(t, m, runeKey('i')); m.infoOpen() || cmd != nil {
		t.Fatal("default i still opened info after the info action was remapped")
	}

	m, cmd := pressWithCmd(t, m, runeKey('I'))
	if !m.infoOpen() {
		t.Fatal("custom info key did not open the window")
	}
	m = applyCmd(t, m, cmd)

	m = press(t, m, runeKey('I'))
	if m.infoOpen() {
		t.Error("custom info key did not close the window")
	}
}

func TestInfoModalSwallowsPaneAndTransportKeys(t *testing.T) {
	prober := &fakeProber{info: map[string]meta.Info{
		"/tmp/music/a.flac": {Codec: "flac", Duration: time.Minute},
		"/tmp/music/b.flac": {Codec: "flac", Duration: time.Minute},
	}}
	m := newTestModel(t, func(opts *Options) { opts.Prober = prober })
	m = resize(t, m, 100, 30)
	m.browserEntries = []library.Entry{
		{Kind: library.KindAudio, Name: "a.flac", Path: "/tmp/music/a.flac"},
		{Kind: library.KindAudio, Name: "b.flac", Path: "/tmp/music/b.flac"},
	}

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	m = applyCmd(t, m, cmd)

	m = press(t, m, specialKey(tea.KeyTab))
	if m.focus != PaneBrowser {
		t.Error("Tab switched panes underneath the info window")
	}
	m = press(t, m, runeKey('j'))
	if m.browserSelected != 0 {
		t.Error("j moved the browser selection underneath the info window")
	}
	if !m.infoOpen() {
		t.Fatal("info window closed unexpectedly")
	}

	m = press(t, m, runeKey('i'))
	if m.infoOpen() {
		t.Error("i did not close the info window")
	}
}

func TestInfoScrollsAndClampsAtMinimumSize(t *testing.T) {
	path := "/tmp/music/long.flac"
	tags := make(map[string]string, 12)
	for i := 0; i < 12; i++ {
		tags[fmt.Sprintf("tag_%02d", i)] = fmt.Sprintf("value %02d", i)
	}
	prober := &fakeProber{info: map[string]meta.Info{
		path: {Codec: "flac", Duration: 4 * time.Minute, Tags: tags},
	}}
	m := newTestModel(t, func(opts *Options) { opts.Prober = prober })
	m = resize(t, m, 60, 10)
	m.browserEntries = []library.Entry{{Kind: library.KindAudio, Name: "long.flac", Path: path}}

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	m = applyCmd(t, m, cmd)

	visible := m.infoVisibleRows()
	if visible != 3 {
		t.Fatalf("visible rows = %d, want 3 at 60x10", visible)
	}
	if m.info.offset != 0 {
		t.Fatalf("initial offset = %d, want 0", m.info.offset)
	}

	m = press(t, m, runeKey('j'))
	if m.info.offset != 1 {
		t.Errorf("offset after down = %d, want 1", m.info.offset)
	}

	m = press(t, m, specialKey(tea.KeyEnd))
	if want := m.infoMaxOffset(); m.info.offset != want {
		t.Errorf("offset after End = %d, want %d", m.info.offset, want)
	}
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "/") {
		t.Errorf("scrolled view has no position indicator:\n%s", plain)
	}

	m = press(t, m, specialKey(tea.KeyHome))
	if m.info.offset != 0 {
		t.Errorf("offset after Home = %d, want 0", m.info.offset)
	}

	m = press(t, m, specialKey(tea.KeyPgDown))
	if m.info.offset == 0 || m.info.offset > m.infoMaxOffset() {
		t.Errorf("offset after PageDown = %d, want within 1..%d", m.info.offset, m.infoMaxOffset())
	}

	for i, line := range strings.Split(m.View().Content, "\n") {
		if width := ansi.StringWidth(line); width != 60 {
			t.Errorf("info line %d width = %d, want 60", i, width)
		}
	}
}

func TestInfoWrapsLongPathAndTagValues(t *testing.T) {
	path := "/tmp/music/" + strings.Repeat("very-long-directory/", 6) + "track.flac"
	tagValue := strings.Repeat("a long tag value ", 10)
	prober := &fakeProber{info: map[string]meta.Info{
		path: {
			Codec: "flac",
			Tags:  map[string]string{"comment": tagValue},
		},
	}}
	m := newTestModel(t, func(opts *Options) { opts.Prober = prober })
	m = resize(t, m, 60, 10)
	m.browserEntries = []library.Entry{{Kind: library.KindAudio, Name: "track.flac", Path: path}}

	m, cmd := pressWithCmd(t, m, runeKey('i'))
	m = applyCmd(t, m, cmd)

	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "Path: /tmp/music/") {
		t.Errorf("info window missing wrapped path prefix:\n%s", plain)
	}

	foundTag := false
	for range m.infoMaxOffset() + 1 {
		plain = ansi.Strip(m.View().Content)
		if strings.Contains(plain, "COMMENT:") {
			foundTag = true
			break
		}
		m = press(t, m, runeKey('j'))
	}
	if !foundTag {
		t.Errorf("info window missing wrapped tag label:\n%s", ansi.Strip(m.View().Content))
	}

	m = press(t, m, specialKey(tea.KeyEnd))
	plain = ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "long tag value") {
		t.Errorf("info window missing wrapped tag value:\n%s", plain)
	}
	for i, line := range strings.Split(m.View().Content, "\n") {
		if width := ansi.StringWidth(line); width != 60 {
			t.Errorf("info line %d width = %d, want 60: %q", i, width, ansi.Strip(line))
		}
	}
}
