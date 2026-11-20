package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

func TestSeekReplacingPendingPreviewClearsPreview(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.prober = nil
	m.queue.Append(playlistItem("Queued", "Artist", 1, time.Minute))
	m.queue.SetCurrent(0)
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	// Pretend a transport operation is already in flight so the preview Play
	// can only be queued as the pending action.
	m.transportBusy = true
	m.transportSeq = 7

	next, cmd := pressForCmd(t, m, previewKey())
	m = next
	if cmd != nil {
		t.Fatalf("queued preview returned command %T, want nil", cmd)
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "preview") {
		t.Fatalf("now playing label = %q, want preview queued", label)
	}

	// Seeking replaces the pending preview Play. The preview must not stay
	// active while the queue stream is the only thing that can start.
	next, cmd = pressForCmd(t, m, specialKey(tea.KeyRight))
	m = next
	if cmd != nil {
		t.Fatalf("seek while transport busy returned command %T, want nil", cmd)
	}
	if label := m.nowPlayingLabel(); strings.Contains(label, "song") || !strings.Contains(label, "Queued") {
		t.Errorf("now playing label = %q, want queue item after preview was dropped", label)
	}

	m = runCmd(t, m, m.applyTransportResult(transportResultMsg{seq: 0}))
	if len(fp.seekDeltas) != 1 || fp.seekDeltas[0] != 5*time.Second {
		t.Errorf("seek deltas = %v, want [5s] after pending seek runs", fp.seekDeltas)
	}
}

func TestPreviewHeaderAndProgressUsePreviewItem(t *testing.T) {
	m, _ := newBrowserModel(t, &testScanner{})
	m.queue.Append(playlistItem("Queued", "Artist", 1, time.Minute))
	m.queue.SetCurrent(0)
	m.browserEntries = []library.Entry{
		cueTrackEntry("Long", "/music/song.flac", "/music/Album.cue", 1, 0, 2*time.Minute),
	}

	m = startTestPreview(t, m)

	header := ansi.Strip(m.headerLine())
	if !strings.Contains(header, "preview: Long") {
		t.Errorf("header = %q, want preview label", header)
	}
	plain, _ := m.progress()
	if !strings.Contains(plain, "0:00 / 2:00") {
		t.Errorf("progress = %q, want preview duration 2:00", plain)
	}
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

func TestPreviewAutoAdvancesThroughBrowserListing(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.Append(playlistItem("Queued", "Artist", 1, time.Minute), playlistItem("Next", "Artist", 2, time.Minute))
	m.queue.SetCurrent(0)
	before := m.queue.Snapshot()
	store := m.store.(*fakeStore)
	m.browserEntries = []library.Entry{
		audioEntry("one.flac", "/music/one.flac"),
		audioEntry("two.flac", "/music/two.flac"),
		audioEntry("three.flac", "/music/three.flac"),
	}
	m.browserSelected = 0

	m = startTestPreview(t, m)
	if got := len(fp.played); got != 1 {
		t.Fatalf("Play calls = %d, want 1", got)
	}

	cmd := m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventFinished, ItemID: fp.played[0].ID},
		ok:    true,
	})
	m = runCmd(t, m, cmd)

	if got := len(fp.played); got != 2 {
		t.Fatalf("Play calls after finish = %d, want 2", got)
	}
	if got := fp.played[1].Path; got != "/music/two.flac" {
		t.Errorf("advanced path = %q, want /music/two.flac", got)
	}
	if after := m.queue.Snapshot(); !snapshotsEqual(before, after) {
		t.Errorf("queue changed during preview advance:\n before %+v\n after  %+v", before, after)
	}
	if got := store.saveCount(); got != 0 {
		t.Errorf("queue saves = %d, want 0", got)
	}
	if m.browserSelected != 0 {
		t.Errorf("browser selection = %d, want 0", m.browserSelected)
	}
	if !m.previewActive {
		t.Error("previewActive = false, want the next preview track active")
	}
}

func TestBrowserPreviewMarkerFollowsAdvance(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.browserEntriesPath = "/music"
	m.browserEntries = []library.Entry{
		audioEntry("one.flac", "/music/one.flac"),
		audioEntry("two.flac", "/music/two.flac"),
	}
	m.browserSelected = 0

	m = startTestPreview(t, m)
	lines := m.browserLines(80, 20)
	first := ansi.Strip(lines[1])
	second := ansi.Strip(lines[2])
	if !strings.HasPrefix(first, "▶ ") {
		t.Fatalf("first row = %q, want preview marker", first)
	}
	if strings.HasPrefix(second, "▶") {
		t.Fatalf("second row = %q, want a blank gutter before advance", second)
	}

	cmd := m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventFinished, ItemID: fp.played[0].ID},
		ok:    true,
	})
	m = runCmd(t, m, cmd)

	lines = m.browserLines(80, 20)
	first = ansi.Strip(lines[1])
	second = ansi.Strip(lines[2])
	if strings.HasPrefix(first, "▶") {
		t.Errorf("first row = %q, want the marker to advance away", first)
	}
	if !strings.HasPrefix(second, "▶ ") {
		t.Errorf("second row = %q, want the preview marker after advance", second)
	}
	if m.browserSelected != 0 {
		t.Errorf("browser selection = %d, want 0", m.browserSelected)
	}
}

func TestBrowserPreviewMarkerClearsOutsideSource(t *testing.T) {
	m, _ := newBrowserModel(t, &testScanner{})
	m.browserEntriesPath = "/music"
	m.browserEntries = []library.Entry{
		audioEntry("one.flac", "/music/one.flac"),
		audioEntry("two.flac", "/music/two.flac"),
	}
	m.browserSelected = 0

	m = startTestPreview(t, m)

	m.browserEntriesPath = "/elsewhere"
	for i, line := range m.browserLines(80, 20) {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "▶") {
			t.Errorf("line %d = %q, want no preview marker outside the source listing", i, plain)
		}
	}
}

func TestBrowserPreviewCueRowMarkerKeepsDuration(t *testing.T) {
	m, _ := newBrowserModel(t, &testScanner{})
	m.browserEntriesPath = "/music"
	entry := cueTrackEntry("02. Two — Artist", "/music/song.flac", "/music/Album.cue", 2, 3*time.Minute, 2*time.Minute)
	m.browserEntries = []library.Entry{entry}
	m.browserSelected = 0

	m = startTestPreview(t, m)

	row := ansi.Strip(m.browserLines(40, 2)[1])
	if !strings.Contains(row, "▶") {
		t.Errorf("CUE row = %q, want preview marker", row)
	}
	if !strings.Contains(row, "2:00") {
		t.Errorf("CUE row = %q, want the right-aligned duration", row)
	}
	if got := ansi.StringWidth(row); got != 40 {
		t.Errorf("CUE row width = %d, want 40", got)
	}
}

func TestPreviewStopsAfterLastWithRepeatOff(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.browserEntries = []library.Entry{
		audioEntry("one.flac", "/music/one.flac"),
		audioEntry("two.flac", "/music/two.flac"),
		audioEntry("three.flac", "/music/three.flac"),
	}
	m.browserSelected = 2

	m = startTestPreview(t, m)
	if got := len(fp.played); got != 1 {
		t.Fatalf("Play calls = %d, want 1", got)
	}
	cmd := m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventFinished, ItemID: fp.played[0].ID},
		ok:    true,
	})
	if cmd != nil {
		t.Fatalf("preview finish at end returned command %T, want nil", cmd)
	}

	if got := len(fp.played); got != 1 {
		t.Errorf("Play calls after preview finish = %d, want 1", got)
	}
	if m.previewActive {
		t.Error("previewActive = true after last preview, want false")
	}
	if m.statusMsg != "preview finished" {
		t.Errorf("statusMsg = %q, want \"preview finished\"", m.statusMsg)
	}
}

func TestPreviewRepeatOneReplaysSameTrack(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.SetRepeat(queue.RepeatOne)
	m.browserEntries = []library.Entry{
		audioEntry("one.flac", "/music/one.flac"),
		audioEntry("two.flac", "/music/two.flac"),
	}
	m.browserSelected = 0

	m = startTestPreview(t, m)
	if got := len(fp.played); got != 1 {
		t.Fatalf("Play calls = %d, want 1", got)
	}
	first := fp.played[0]

	cmd := m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventFinished, ItemID: first.ID},
		ok:    true,
	})
	m = runCmd(t, m, cmd)

	if got := len(fp.played); got != 2 {
		t.Fatalf("Play calls after repeat-one finish = %d, want 2", got)
	}
	second := fp.played[1]
	if second.Path != first.Path {
		t.Errorf("replayed path = %q, want %q", second.Path, first.Path)
	}
	if second.ID == first.ID {
		t.Errorf("replayed item ID = %d, want a fresh high-range ID", second.ID)
	}
	highRange := uint64(1) << 63
	if first.ID < highRange || second.ID < highRange {
		t.Errorf("preview IDs = %d/%d, want both in the high uint64 range", first.ID, second.ID)
	}
}

func TestPreviewRepeatAllWraps(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.SetRepeat(queue.RepeatAll)
	m.browserEntries = []library.Entry{
		audioEntry("one.flac", "/music/one.flac"),
		audioEntry("two.flac", "/music/two.flac"),
	}
	m.browserSelected = 1

	m = startTestPreview(t, m)
	if got := len(fp.played); got != 1 {
		t.Fatalf("Play calls = %d, want 1", got)
	}
	if got := fp.played[0].Path; got != "/music/two.flac" {
		t.Fatalf("initial preview path = %q, want /music/two.flac", got)
	}

	cmd := m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventFinished, ItemID: fp.played[0].ID},
		ok:    true,
	})
	m = runCmd(t, m, cmd)

	if got := len(fp.played); got != 2 {
		t.Fatalf("Play calls after repeat-all finish = %d, want 2", got)
	}
	if got := fp.played[1].Path; got != "/music/one.flac" {
		t.Errorf("wrapped path = %q, want /music/one.flac", got)
	}
}

func TestPreviewShuffleUsesPreviewSet(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.SetShuffle(true)
	m.queue.SetRepeat(queue.RepeatAll)
	m.browserEntries = []library.Entry{
		audioEntry("one.flac", "/music/one.flac"),
		audioEntry("two.flac", "/music/two.flac"),
		audioEntry("three.flac", "/music/three.flac"),
	}
	m.browserSelected = 0

	m = startTestPreview(t, m)
	if got := len(fp.played); got != 1 {
		t.Fatalf("Play calls = %d, want 1", got)
	}

	for i := 0; i < 3; i++ {
		current := fp.played[len(fp.played)-1]
		cmd := m.handlePlayerEvent(playerEventMsg{
			event: player.Event{Type: player.EventFinished, ItemID: current.ID},
			ok:    true,
		})
		m = runCmd(t, m, cmd)
	}

	if got := len(fp.played); got != 4 {
		t.Fatalf("Play calls after three finishes = %d, want 4", got)
	}
	available := map[string]bool{
		"/music/one.flac":   true,
		"/music/two.flac":   true,
		"/music/three.flac": true,
	}
	seen := make(map[string]bool, 3)
	for _, item := range fp.played[1:] {
		if !available[item.Path] {
			t.Errorf("shuffled path = %q, want one of the preview entries", item.Path)
		}
		if seen[item.Path] {
			t.Errorf("shuffled path %q repeated before the set was exhausted", item.Path)
		}
		seen[item.Path] = true
	}
	if len(seen) != 3 {
		t.Errorf("shuffled set = %v, want all three preview entries", seen)
	}
	if !m.previewActive {
		t.Error("previewActive = false, want preview still active under repeat-all")
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
		{Kind: library.KindPlaylist, Name: "list.m3u/", Path: "/music/list.m3u"},
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

func TestPlaylistPlayClearsPreview(t *testing.T) {
	m, fp := newBrowserModel(t, &testScanner{})
	m.queue.Append(
		playlistItem("Alpha", "Artist", 1, time.Minute),
		playlistItem("Beta", "Artist", 2, time.Minute),
	)
	m.queue.SetCurrent(0)
	m.browserEntries = []library.Entry{audioEntry("song.flac", "/music/song.flac")}

	m = startTestPreview(t, m)
	m.focus = PanePlaylist
	m.playlistSelected = 1
	next, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = next
	m = runCmd(t, m, cmd)

	if got := len(fp.played); got != 2 {
		t.Fatalf("Play calls = %d, want preview + queue play", got)
	}
	if item, _ := m.queue.Current(); item.Title != "Beta" {
		t.Errorf("queue current = %q, want Beta", item.Title)
	}
	if m.previewActive {
		t.Error("previewActive = true after playlist play, want false")
	}
	if label := m.nowPlayingLabel(); !strings.Contains(label, "Beta") {
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
