package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"player2/internal/library"
	"player2/internal/meta"
	"player2/internal/player"
	"player2/internal/queue"
)

func previewKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
}

func startTestPreview(t *testing.T, m *Model) *Model {
	t.Helper()
	next, cmd := pressForCmd(t, m, previewKey())
	m = next
	if cmd == nil {
		t.Fatal("shift+enter returned no command")
	}
	return runCmd(t, m, cmd)
}

func TestBrowserPreviewPlaysWithoutChangingQueue(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.Append(playlistItem("Queued", "Artist", 1, time.Minute), playlistItem("Next", "Artist", 2, time.Minute))
	m.queue.SetCurrent(0)
	m.playlistSelected = 1
	before := m.queue.Snapshot()
	store := m.store.(*fakeStore)
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)

	if len(fp.played) != 1 {
		t.Fatalf("Play calls = %d, want 1", len(fp.played))
	}
	played := fp.played[0]
	if played.Path != "/music/song.flac" || played.Title != "song" {
		t.Errorf("played item = %+v, want /music/song.flac titled song", played)
	}
	if after := m.queue.Snapshot(); !snapshotsEqual(before, after) {
		t.Errorf("queue changed during preview:\n before %+v\n after  %+v", before, after)
	}
	if m.playlistSelected != 1 {
		t.Errorf("playlist selection = %d, want 1", m.playlistSelected)
	}
	if got := store.saveCount(); got != 0 {
		t.Errorf("queue saves = %d, want 0", got)
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "song") {
		t.Errorf("now playing label = %q, want preview track", label)
	}
	if !strings.Contains(m.statusMsg, "previewing") {
		t.Errorf("statusMsg = %q, want previewing", m.statusMsg)
	}
}

func TestBrowserPreviewCueTrack(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	entry := cueTrackEntry("One", "/music/song.flac", "/music/Album.cue", 1, 30*time.Second, 3*time.Minute)
	m.browserEntries = []library.Entry{entry}

	m = startTestPreview(t, m)

	if len(fp.played) != 1 {
		t.Fatalf("Play calls = %d, want 1", len(fp.played))
	}
	got := fp.played[0]
	if got.Path != entry.Path || got.CuePath != entry.CuePath || got.CueTrack != entry.TrackNo {
		t.Errorf("played item = %+v, want CUE track %+v", got, entry)
	}
	if got.Start != 30*time.Second || got.Duration != 3*time.Minute {
		t.Errorf("played range = %v+%v, want 30s+3m", got.Start, got.Duration)
	}
	if got.Title != "One" || got.Artist != "Artist" {
		t.Errorf("played metadata = %q/%q, want One/Artist", got.Title, got.Artist)
	}
	if got := len(m.queue.Snapshot().Items); got != 0 {
		t.Errorf("queue length = %d after CUE preview, want 0", got)
	}
}

func TestPreviewFinishStopsWithoutAdvancingQueue(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.Append(playlistItem("Queued", "Artist", 1, time.Minute), playlistItem("Next", "Artist", 2, time.Minute))
	m.queue.SetCurrent(0)
	m.queue.SetRepeat(queue.RepeatOne)
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)
	previewID := fp.played[0].ID
	if cmd := m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventFinished, ItemID: previewID},
		ok:    true,
	}); cmd != nil {
		t.Fatalf("preview finish returned command %T, want nil", cmd)
	}

	if got := len(fp.played); got != 1 {
		t.Errorf("Play calls after preview finish = %d, want 1", got)
	}
	if item, ok := m.queue.Current(); !ok || item.Title != "Queued" {
		t.Errorf("queue current = %+v (ok=%v), want Queued", item, ok)
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "Queued") || strings.Contains(label, "song") {
		t.Errorf("now playing label = %q, want queue item", label)
	}
	if !strings.Contains(m.statusMsg, "preview finished") {
		t.Errorf("statusMsg = %q, want preview finished", m.statusMsg)
	}
}

func TestPreviewErrorClearsPreview(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)
	previewID := fp.played[0].ID
	m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventError, ItemID: previewID, Err: errors.New("decoder failed")},
		ok:    true,
	})

	if label := m.nowPlayingLabel(); strings.Contains(label, "song") {
		t.Errorf("now playing label = %q, want preview cleared", label)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "decoder failed") {
		t.Errorf("status = %q (error=%v), want decoder error", m.statusMsg, m.statusIsError)
	}
}

func TestPreviewIgnoresQueueEvents(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.Append(playlistItem("Queued", "Artist", 1, time.Minute), playlistItem("Next", "Artist", 2, time.Minute))
	m.queue.SetCurrent(0)
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)
	queued, _ := m.queue.Current()
	m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventFinished, ItemID: queued.ID},
		ok:    true,
	})

	if got := len(fp.played); got != 1 {
		t.Errorf("Play calls = %d after stale queue event, want 1 (preview only)", got)
	}
	if item, _ := m.queue.Current(); item.Title != "Queued" {
		t.Errorf("queue current after stale event = %q, want Queued", item.Title)
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "song") {
		t.Errorf("now playing label = %q, want preview still active", label)
	}
}

func TestPreviewNextAndPreviousIgnored(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.Append(playlistItem("Queued", "Artist", 1, time.Minute), playlistItem("Next", "Artist", 2, time.Minute))
	m.queue.SetCurrent(0)
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)
	for _, key := range []tea.KeyPressMsg{runeKey('n'), runeKey('b')} {
		next, cmd := pressForCmd(t, m, key)
		m = next
		if cmd != nil {
			t.Fatalf("%q returned command %T during preview, want nil", key.String(), cmd)
		}
	}

	if item, _ := m.queue.Current(); item.Title != "Queued" {
		t.Errorf("queue current after next/previous = %q, want Queued", item.Title)
	}
	if got := len(fp.played); got != 1 {
		t.Errorf("Play calls = %d, want 1", got)
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "song") {
		t.Errorf("now playing label = %q, want preview still active", label)
	}
}

func TestPreviewStopClearsPreview(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)
	next, cmd := pressForCmd(t, m, runeKey('x'))
	m = next
	m = runCmd(t, m, cmd)

	if fp.stops != 1 {
		t.Errorf("Stop calls = %d, want 1", fp.stops)
	}
	if label := m.nowPlayingLabel(); strings.Contains(label, "song") {
		t.Errorf("now playing label = %q, want preview cleared", label)
	}
	if m.statusMsg != "stopped" {
		t.Errorf("statusMsg = %q, want stopped", m.statusMsg)
	}
}

func TestBrowserPreviewIgnoresNonTracks(t *testing.T) {
	entries := []library.Entry{
		{Kind: library.KindDir, Name: "Album", Path: "/music/Album"},
		{Kind: library.KindCueDir, Name: "Cue.cue/", Path: "/music/Cue.cue", CuePath: "/music/Cue.cue"},
		{Kind: library.KindPlaylist, Name: "list.m3u", Path: "/music/list.m3u"},
		{Kind: library.KindInvalidCue, Name: "Bad.cue", Path: "/music/Bad.cue", Err: errors.New("bad")},
	}
	for _, entry := range entries {
		t.Run(entry.Name, func(t *testing.T) {
			m, fp := newBrowserModel(t, &testScanner{})
			m.browserEntries = []library.Entry{entry}
			next, cmd := pressForCmd(t, m, previewKey())
			m = next
			if cmd != nil {
				t.Fatalf("shift+enter returned command %T for %v", cmd, entry.Kind)
			}
			if len(fp.played) != 0 {
				t.Errorf("Play calls = %d, want 0", len(fp.played))
			}
			if label := m.nowPlayingLabel(); label != "nothing playing" {
				t.Errorf("now playing label = %q, want nothing playing", label)
			}
		})
	}
}

func TestNormalPlayClearsPreview(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.browserEntries = []library.Entry{
		audioEntry("song.flac", "/music/song.flac"),
		audioEntry("other.flac", "/music/other.flac"),
	}

	m = startTestPreview(t, m)
	m.browserSelected = 1
	next, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = next
	m = runCmd(t, m, cmd)

	if got := len(m.queue.Snapshot().Items); got != 2 {
		t.Fatalf("queue length = %d after normal play, want 2", got)
	}
	if got := len(fp.played); got != 2 {
		t.Fatalf("Play calls = %d, want preview + queue play", got)
	}
	if item, _ := m.queue.Current(); item.Title != "other" {
		t.Errorf("queue current = %q, want other", item.Title)
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "other") {
		t.Errorf("now playing label = %q, want queue item", label)
	}
}

func TestPreviewMetadataProbeUpdatesOnlyPreview(t *testing.T) {
	prober := &fakeProber{info: map[string]meta.Info{
		"/music/song.mp3": {Title: "Tagged", Artist: "Band", Duration: 2 * time.Minute},
	}}
	m, fp := newBrowserModel(t, &testScanner{})
	m.prober = prober
	store := m.store.(*fakeStore)
	m.browserEntries = []library.Entry{audioEntry("song.mp3", "/music/song.mp3")}

	m = startTestPreview(t, m)

	if got := prober.callCount(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if len(fp.played) != 1 {
		t.Fatalf("Play calls = %d, want 1", len(fp.played))
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "Tagged") || !strings.Contains(label, "Band") {
		t.Errorf("now playing label = %q, want probed metadata", label)
	}
	if duration := m.durationLabel(); duration != "2:00" {
		t.Errorf("duration label = %q, want 2:00", duration)
	}
	if got := len(m.queue.Snapshot().Items); got != 0 {
		t.Errorf("queue length = %d, want 0", got)
	}
	if got := store.saveCount(); got != 0 {
		t.Errorf("queue saves = %d, want 0", got)
	}
}

func TestStaleLevelPlayDoesNotReplacePreview(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.browserEntries = []library.Entry{
		audioEntry("first.flac", "/music/first.flac"),
		audioEntry("second.flac", "/music/second.flac"),
	}

	next, stalePlay := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = next
	if stalePlay == nil {
		t.Fatal("Enter returned no level build command")
	}

	m.browserSelected = 1
	m = startTestPreview(t, m)
	updated, _ := m.Update(stalePlay())
	m = updated.(*Model)

	if got := len(m.queue.Snapshot().Items); got != 0 {
		t.Errorf("stale level play replaced queue with %d items", got)
	}
	if got := len(fp.played); got != 1 {
		t.Errorf("Play calls = %d, want preview only", got)
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "second") {
		t.Errorf("now playing label = %q, want preview still active", label)
	}
}

func TestQueueRemoveDoesNotStopPreview(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.Append(playlistItem("Queued", "Artist", 1, time.Minute), playlistItem("Next", "Artist", 2, time.Minute))
	m.queue.SetCurrent(0)
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)
	m.focus = PanePlaylist
	m.playlistSelected = 0
	next, cmd := pressForCmd(t, m, runeKey('d'))
	m = next
	m = runCmd(t, m, cmd)

	if fp.stops != 0 {
		t.Errorf("Stop calls = %d after queue remove, want 0", fp.stops)
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "song") {
		t.Errorf("now playing label = %q, want preview still active", label)
	}
}

func TestPreviewPlayFailureClearsPreview(t *testing.T) {
	m, _ := newBrowserModel(t, &testScanner{})
	failing := &failingPreviewPlayer{fakePlayer: newFakePlayer(), err: errors.New("decoder failed")}
	m.player = failing
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)

	if label := m.nowPlayingLabel(); strings.Contains(label, "song") {
		t.Errorf("now playing label = %q, want preview cleared", label)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "decoder failed") {
		t.Errorf("status = %q (error=%v), want decoder error", m.statusMsg, m.statusIsError)
	}
}

type failingPreviewPlayer struct {
	*fakePlayer
	err error
}

func (p *failingPreviewPlayer) Play(queue.Item) error {
	return p.err
}

func snapshotsEqual(a, b queue.Snapshot) bool {
	if a.Current != b.Current || a.Shuffle != b.Shuffle || a.Repeat != b.Repeat || len(a.Items) != len(b.Items) {
		return false
	}
	for i := range a.Items {
		if a.Items[i] != b.Items[i] {
			return false
		}
	}
	return true
}
