package tui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/meta"
	"player2/internal/player"
	"player2/internal/queue"
)

func newPlaylistModel(t *testing.T, mutate func(*Options)) (*Model, *fakePlayer) {
	t.Helper()
	fp := newFakePlayer()
	m := newTestModel(t, func(opts *Options) {
		opts.Player = fp
		opts.StartDir = "/music"
		if mutate != nil {
			mutate(opts)
		}
	})
	m = resize(t, m, 100, 30)
	m.focus = PanePlaylist
	return m, fp
}

// runCmd executes a command tree and feeds every resulting message back into
// the model. It must not be used on commands that wait for player events.
func runCmd(t *testing.T, m *Model, cmd tea.Cmd) *Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			m = runCmd(t, m, sub)
		}
		return m
	}
	next, follow := m.Update(msg)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return runCmd(t, updated, follow)
}

func playlistItem(title, artist string, track int, duration time.Duration) queue.Item {
	return queue.Item{
		Title:    title,
		Artist:   artist,
		TrackNo:  track,
		Duration: duration,
		Path:     "/music/" + strings.ToLower(strings.ReplaceAll(title, " ", "-")) + ".flac",
	}
}

func playlistPlain(m *Model) string {
	return ansi.Strip(strings.Join(m.playlistLines(40, 6), "\n"))
}

func TestPlaylistRenderMarkerSelectionDurationsAndCUE(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m.queue.Append(
		playlistItem("One", "Artist", 1, 65*time.Second),
		playlistItem("Two", "Artist", 2, 3*time.Minute+5*time.Second),
		playlistItem("Long", "Band", 3, time.Hour+2*time.Minute+3*time.Second),
		queue.Item{Path: "/music/live.flac", Title: "Live", CuePath: "/music/live.cue", CueTrack: 4, TrackNo: 4},
	)
	m.queue.SetCurrent(1)
	m.playlistSelected = 2

	plain := playlistPlain(m)
	for _, want := range []string{
		"01. One — Artist",
		"▶ 02. Two — Artist",
		"03. Long — Band",
		"04. Live",
		"3:05",
		"1:02:03",
		"--:--",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("playlist render missing %q\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "▶ 02.") {
		t.Errorf("current marker not attached to current item:\n%s", plain)
	}

	lines := m.playlistLines(40, 6)
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != 40 {
			t.Errorf("line %d width = %d, want 40", i, got)
		}
	}
}

func TestPlaylistRenderSelectionOnlyWhileFocused(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m.queue.Append(playlistItem("One", "", 0, time.Minute))

	m.playlistSelected = 0
	m.focus = PanePlaylist
	focused := m.playlistLines(30, 3)
	if !strings.Contains(focused[1], "\x1b[7m") {
		t.Errorf("focused playlist row lacks reverse selection styling: %q", focused[1])
	}

	m.focus = PaneBrowser
	unfocused := m.playlistLines(30, 3)
	if strings.Contains(unfocused[1], "\x1b[7m") {
		t.Errorf("unfocused playlist row kept reverse selection styling: %q", unfocused[1])
	}
	if focused[1] == unfocused[1] {
		t.Error("focused and unfocused rows rendered identically")
	}
}

func TestPlaylistRenderScrollingAndWidthSafety(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	for i := 0; i < 20; i++ {
		m.queue.Append(queue.Item{
			Path:     "/music/track.flac",
			Title:    strings.Repeat("very-long-title", 3),
			Artist:   "Artist",
			TrackNo:  i + 1,
			Duration: time.Minute,
		})
	}
	m.playlistSelected = 15
	m.playlistOffset = 0

	lines := m.playlistLines(60, 5)
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5", len(lines))
	}
	if m.playlistOffset != 12 {
		t.Errorf("offset = %d, want 12 so item 15 is visible", m.playlistOffset)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "16.") {
		t.Errorf("scrolled playlist does not show the selected item:\n%s", plain)
	}
	if strings.Contains(plain, "01.") {
		t.Errorf("scrolled playlist still shows the first item:\n%s", plain)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != 60 {
			t.Errorf("line %d width = %d, want 60", i, got)
		}
	}
}

func TestPlaylistEmptyState(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	plain := playlistPlain(m)
	if !strings.Contains(plain, "Queue is empty") {
		t.Errorf("empty playlist render missing empty state:\n%s", plain)
	}
	if !strings.Contains(plain, "Playlist (0)") {
		t.Errorf("empty playlist title lacks count:\n%s", plain)
	}
}

func TestPlaylistEnterPlaysSelectedAndKeepsSelection(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	items := []queue.Item{
		playlistItem("One", "Artist", 1, time.Minute),
		playlistItem("Two", "Artist", 2, time.Minute),
		playlistItem("Three", "Artist", 3, time.Minute),
	}
	m.queue.Append(items...)
	m.playlistSelected = 1

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = runCmd(t, m, cmd)

	if snapshot := m.queue.Snapshot(); snapshot.Current != 1 {
		t.Errorf("queue current = %d, want 1", snapshot.Current)
	}
	if m.playlistSelected != 1 {
		t.Errorf("playlist selection = %d, want 1", m.playlistSelected)
	}
	if len(fp.played) != 1 || fp.played[0].Title != "Two" {
		t.Fatalf("played = %#v, want Two", fp.played)
	}
}

func TestPauseResumeStopFromStopped(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.queue.Append(playlistItem("One", "Artist", 0, time.Minute))

	fp.status.State = player.StatePlaying
	m = press(t, m, specialKey(tea.KeySpace))
	if fp.pauses != 1 {
		t.Errorf("pauses = %d, want 1", fp.pauses)
	}
	if m.status.State != player.StatePaused {
		t.Errorf("cached state = %v, want paused", m.status.State)
	}

	m = press(t, m, specialKey(tea.KeySpace))
	if fp.pauses != 2 {
		t.Errorf("pauses = %d, want 2", fp.pauses)
	}
	if m.status.State != player.StatePlaying {
		t.Errorf("cached state = %v, want playing", m.status.State)
	}

	m = press(t, m, runeKey('x'))
	if fp.stops != 1 {
		t.Errorf("stops = %d, want 1", fp.stops)
	}
	if m.statusMsg != "stopped" {
		t.Errorf("statusMsg = %q, want stopped", m.statusMsg)
	}

	fp.status.State = player.StateStopped
	m, cmd := pressForCmd(t, m, specialKey(tea.KeySpace))
	m = runCmd(t, m, cmd)
	if len(fp.played) != 1 {
		t.Fatalf("played = %#v, want the current item", fp.played)
	}
}

func TestPauseWithEmptyQueueSaysNothingToPlay(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m = press(t, m, specialKey(tea.KeySpace))
	if m.statusIsError {
		t.Error("empty pause set an error status")
	}
	if !strings.Contains(m.statusMsg, "nothing to play") {
		t.Errorf("statusMsg = %q, want nothing to play", m.statusMsg)
	}
}

func TestSeekRunsInCommandAndErrorsSetStatus(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyRight))
	if cmd == nil {
		t.Fatal("seek returned no command")
	}
	if len(fp.seekDeltas) != 0 {
		t.Fatal("SeekBy ran inside Update")
	}
	m = runCmd(t, m, cmd)
	if len(fp.seekDeltas) != 1 || fp.seekDeltas[0] != 5*time.Second {
		t.Errorf("seek deltas = %v, want +5s", fp.seekDeltas)
	}

	fp.seekErr = errors.New("seek failed")
	_, cmd = pressForCmd(t, m, specialKey(tea.KeyLeft))
	m = runCmd(t, m, cmd)
	if !m.statusIsError || !strings.Contains(m.statusMsg, "seek failed") {
		t.Errorf("status after failed seek = %q (error=%v)", m.statusMsg, m.statusIsError)
	}
	if len(fp.seekDeltas) != 2 || fp.seekDeltas[1] != -5*time.Second {
		t.Errorf("seek deltas = %v, want a -5s entry", fp.seekDeltas)
	}
}

func TestVolumeAndMute(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)

	m = press(t, m, runeKey('+'))
	if got := fp.volumes[len(fp.volumes)-1]; got != 75 {
		t.Errorf("volume after + = %d, want 75", got)
	}
	if m.status.Volume != 75 {
		t.Errorf("cached volume = %d, want 75", m.status.Volume)
	}
	if !strings.Contains(m.statusMsg, "75") {
		t.Errorf("statusMsg = %q, want volume", m.statusMsg)
	}

	fp.status.Volume = 98
	m.status.Volume = 98
	m = press(t, m, runeKey('+'))
	if got := fp.volumes[len(fp.volumes)-1]; got != 100 {
		t.Errorf("volume clamp = %d, want 100", got)
	}

	fp.status.Volume = 2
	m.status.Volume = 2
	m = press(t, m, runeKey('-'))
	if got := fp.volumes[len(fp.volumes)-1]; got != 0 {
		t.Errorf("volume clamp = %d, want 0", got)
	}

	fp.status.Volume = 70
	m.status.Volume = 70
	m = press(t, m, runeKey('m'))
	if got := fp.volumes[len(fp.volumes)-1]; got != 0 {
		t.Errorf("muted volume = %d, want 0", got)
	}
	m = press(t, m, runeKey('m'))
	if got := fp.volumes[len(fp.volumes)-1]; got != 70 {
		t.Errorf("unmuted volume = %d, want 70", got)
	}
}

func TestNextPreviousRepeatAndSelectionFollow(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
		playlistItem("Three", "", 3, time.Minute),
	)
	m.playlistSelected = 0

	m, cmd := pressForCmd(t, m, runeKey('n'))
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 1 {
		t.Errorf("current after next = %d, want 1", got)
	}
	if m.playlistSelected != 1 {
		t.Errorf("selection after next = %d, want 1 (followed current)", m.playlistSelected)
	}
	if len(fp.played) != 1 || fp.played[0].Title != "Two" {
		t.Fatalf("played after next = %#v", fp.played)
	}

	// Moving the selection away keeps it in place on the next advance.
	m.playlistSelected = 0
	m, cmd = pressForCmd(t, m, runeKey('n'))
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 2 {
		t.Errorf("current after second next = %d, want 2", got)
	}
	if m.playlistSelected != 0 {
		t.Errorf("selection = %d after moving away, want 0", m.playlistSelected)
	}

	// Repeat off at the end leaves the current item unchanged.
	m, cmd = pressForCmd(t, m, runeKey('n'))
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 2 {
		t.Errorf("current after end = %d, want 2", got)
	}
	if !strings.Contains(m.statusMsg, "end of queue") {
		t.Errorf("status = %q, want end of queue", m.statusMsg)
	}

	// Repeat all wraps to the start.
	m.queue.SetRepeat(queue.RepeatAll)
	m, cmd = pressForCmd(t, m, runeKey('n'))
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 0 {
		t.Errorf("current after wrap = %d, want 0", got)
	}

	m, cmd = pressForCmd(t, m, runeKey('b'))
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 2 {
		t.Errorf("current after previous wrap = %d, want 2", got)
	}

	// Shuffle still advances in playback order without touching selection when
	// it was moved away.
	m.queue.SetShuffle(true)
	m.playlistSelected = 0
	m, cmd = pressForCmd(t, m, runeKey('n'))
	m = runCmd(t, m, cmd)
	if len(fp.played) < 4 {
		t.Fatalf("shuffle next did not play: %#v", fp.played)
	}
	if m.playlistSelected != 0 {
		t.Errorf("shuffle next moved a selection that was not on the old current")
	}
}

func TestRepeatKeyCyclesAndPersists(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	store := m.store.(*fakeStore)

	m, cmd := pressForCmd(t, m, runeKey('r'))
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Repeat; got != queue.RepeatAll {
		t.Errorf("repeat = %v, want all", got)
	}
	if !strings.Contains(m.statusMsg, "all") {
		t.Errorf("status = %q, want repeat all", m.statusMsg)
	}
	if store.saveCount() == 0 {
		t.Error("repeat change was not persisted")
	}

	m, cmd = pressForCmd(t, m, runeKey('r'))
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Repeat; got != queue.RepeatOne {
		t.Errorf("repeat = %v, want one", got)
	}
}

func TestShuffleKeyTogglesAndPersists(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	store := m.store.(*fakeStore)

	m, cmd := pressForCmd(t, m, runeKey('s'))
	m = runCmd(t, m, cmd)
	if !m.queue.Snapshot().Shuffle {
		t.Error("shuffle did not toggle on")
	}
	if store.saveCount() == 0 {
		t.Error("shuffle change was not persisted")
	}
	m, cmd = pressForCmd(t, m, runeKey('s'))
	m = runCmd(t, m, cmd)
	if m.queue.Snapshot().Shuffle {
		t.Error("shuffle did not toggle off")
	}
}

func TestAutoAdvanceFinished(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
	)
	m.playlistSelected = 0

	cmd := m.handlePlayerEvent(playerEventMsg{event: player.Event{Type: player.EventFinished}, ok: true})
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 1 {
		t.Errorf("current = %d after finish, want 1", got)
	}
	if m.playlistSelected != 1 {
		t.Errorf("selection = %d, want 1 (followed old current)", m.playlistSelected)
	}
	if len(fp.played) != 1 || fp.played[0].Title != "Two" {
		t.Fatalf("played = %#v, want Two", fp.played)
	}

	// The selection moved away earlier, so auto-advance leaves it alone.
	m.playlistSelected = 0
	cmd = m.handlePlayerEvent(playerEventMsg{event: player.Event{Type: player.EventFinished}, ok: true})
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 1 {
		t.Errorf("current at repeat-off end = %d, want 1", got)
	}
	if m.playlistSelected != 0 {
		t.Errorf("selection = %d, want 0", m.playlistSelected)
	}
	if !strings.Contains(m.statusMsg, "end of queue") {
		t.Errorf("status = %q, want end of queue", m.statusMsg)
	}
}

func TestAutoAdvanceRepeatOneAndRepeatAll(t *testing.T) {
	t.Run("repeat one restarts the same item", func(t *testing.T) {
		m, fp := newPlaylistModel(t, nil)
		m.queue.Append(
			playlistItem("One", "", 1, time.Minute),
			playlistItem("Two", "", 2, time.Minute),
		)
		m.queue.SetRepeat(queue.RepeatOne)

		cmd := m.handlePlayerEvent(playerEventMsg{event: player.Event{Type: player.EventFinished}, ok: true})
		m = runCmd(t, m, cmd)
		if got := m.queue.Snapshot().Current; got != 0 {
			t.Errorf("current = %d, want 0", got)
		}
		if len(fp.played) != 1 || fp.played[0].Title != "One" {
			t.Fatalf("played = %#v, want One restarted", fp.played)
		}
	})

	t.Run("repeat all wraps", func(t *testing.T) {
		m, fp := newPlaylistModel(t, nil)
		m.queue.Append(
			playlistItem("One", "", 1, time.Minute),
			playlistItem("Two", "", 2, time.Minute),
		)
		m.queue.SetCurrent(1)
		m.queue.SetRepeat(queue.RepeatAll)

		cmd := m.handlePlayerEvent(playerEventMsg{event: player.Event{Type: player.EventFinished}, ok: true})
		m = runCmd(t, m, cmd)
		if got := m.queue.Snapshot().Current; got != 0 {
			t.Errorf("current = %d, want wrap to 0", got)
		}
		if len(fp.played) != 1 || fp.played[0].Title != "One" {
			t.Fatalf("played = %#v, want One", fp.played)
		}
	})
}

func TestPlayerErrorEventDoesNotAdvance(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
	)

	cmd := m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventError, Err: errors.New("decoder died")},
		ok:    true,
	})
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 0 {
		t.Errorf("current = %d after error, want 0", got)
	}
	if len(fp.played) != 0 {
		t.Errorf("played = %#v after error, want none", fp.played)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "decoder died") {
		t.Errorf("status = %q (error=%v), want the event error", m.statusMsg, m.statusIsError)
	}
}

func TestRemoveCurrentStopsAndSelectsNext(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
		playlistItem("Three", "", 3, time.Minute),
	)
	m.queue.SetCurrent(1)
	m.playlistSelected = 1

	m, cmd := pressForCmd(t, m, runeKey('d'))
	m = runCmd(t, m, cmd)
	snapshot := m.queue.Snapshot()
	if len(snapshot.Items) != 2 {
		t.Fatalf("queue length = %d, want 2", len(snapshot.Items))
	}
	if snapshot.Current != 1 || snapshot.Items[1].Title != "Three" {
		t.Errorf("current after removing current = %d, want Three at 1", snapshot.Current)
	}
	if fp.stops != 1 {
		t.Errorf("stops = %d, want 1", fp.stops)
	}
	if m.playlistSelected != 1 {
		t.Errorf("selection = %d, want 1", m.playlistSelected)
	}
}

func TestRemoveNonCurrentKeepsPlaybackAndCoherence(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
		playlistItem("Three", "", 3, time.Minute),
	)
	m.playlistSelected = 2

	m, cmd := pressForCmd(t, m, runeKey('d'))
	m = runCmd(t, m, cmd)
	snapshot := m.queue.Snapshot()
	if len(snapshot.Items) != 2 {
		t.Fatalf("queue length = %d, want 2", len(snapshot.Items))
	}
	if snapshot.Current != 0 || snapshot.Items[0].Title != "One" {
		t.Errorf("current changed after removing a non-current item: %#v", snapshot)
	}
	if fp.stops != 0 {
		t.Errorf("stops = %d, want 0", fp.stops)
	}
	if m.playlistSelected != 1 {
		t.Errorf("selection = %d, want 1 (clamped)", m.playlistSelected)
	}
}

func TestMovePlaylistItems(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
		playlistItem("Three", "", 3, time.Minute),
	)
	m.playlistSelected = 1

	m, cmd := pressForCmd(t, m, runeKey('K'))
	m = runCmd(t, m, cmd)
	if titles := queueTitles(m); titles[0] != "Two" || titles[1] != "One" {
		t.Errorf("titles after move up = %v", titles)
	}
	if m.playlistSelected != 0 {
		t.Errorf("selection after move up = %d, want 0", m.playlistSelected)
	}

	m, cmd = pressForCmd(t, m, runeKey('J'))
	m = runCmd(t, m, cmd)
	if titles := queueTitles(m); titles[0] != "One" || titles[1] != "Two" {
		t.Errorf("titles after move down = %v", titles)
	}
	if m.playlistSelected != 1 {
		t.Errorf("selection after move down = %d, want 1", m.playlistSelected)
	}
}

func queueTitles(m *Model) []string {
	items := m.queue.Snapshot().Items
	titles := make([]string, len(items))
	for i, item := range items {
		titles[i] = item.Title
	}
	return titles
}

func TestClearStopsAndEmpties(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
	)
	m.playlistSelected = 1

	m, cmd := pressForCmd(t, m, runeKey('c'))
	m = runCmd(t, m, cmd)
	if got := len(m.queue.Snapshot().Items); got != 0 {
		t.Errorf("queue length = %d, want 0", got)
	}
	if fp.stops != 1 {
		t.Errorf("stops = %d, want 1", fp.stops)
	}
	if m.playlistSelected != 0 || m.playlistOffset != 0 {
		t.Errorf("selection/offset = %d/%d, want 0/0", m.playlistSelected, m.playlistOffset)
	}
}

func TestGotoPlayingJumpsToCurrent(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	for i := 0; i < 10; i++ {
		m.queue.Append(playlistItem(strings.Repeat("x", i+1), "", i+1, time.Minute))
	}
	m.queue.SetCurrent(7)
	m.playlistSelected = 0
	m.playlistOffset = 0

	m = press(t, m, runeKey('g'))
	if m.playlistSelected != 7 {
		t.Errorf("selection = %d, want 7", m.playlistSelected)
	}
	if m.playlistOffset > 7 || m.playlistSelected >= m.playlistOffset+m.playlistBodyRows() {
		t.Errorf("current item not visible: selected=%d offset=%d rows=%d",
			m.playlistSelected, m.playlistOffset, m.playlistBodyRows())
	}
}

func TestMetadataProbeUpdatesQueueByIDAndHeader(t *testing.T) {
	prober := &fakeProber{info: map[string]meta.Info{
		"/music/song.mp3": {
			Title: "Real Title", Artist: "Real Artist", Album: "Album",
			TrackNo: 4, Duration: 3 * time.Minute,
		},
	}}
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Prober = prober })
	item := playlistItem("song", "", 0, 0)
	item.Path = "/music/song.mp3"
	m.queue.Append(item)

	cmd := m.probeItems()
	if cmd == nil {
		t.Fatal("probeItems returned no command")
	}
	m = runCmd(t, m, cmd)

	got := m.queue.Snapshot().Items[0]
	if got.Title != "Real Title" || got.Artist != "Real Artist" || got.TrackNo != 4 {
		t.Errorf("item metadata = %#v", got)
	}
	if got.Duration != 3*time.Minute {
		t.Errorf("duration = %v, want 3m", got.Duration)
	}
	if prober.callCount() != 1 {
		t.Errorf("probe calls = %d, want 1", prober.callCount())
	}
	m.status.State = player.StatePlaying
	m.status.Volume = 70
	m = resize(t, m, 100, 30)
	if header := firstLine(ansi.Strip(m.View().Content)); !strings.Contains(header, "Real Title") {
		t.Errorf("header = %q, want updated title", header)
	}
}

func TestMetadataProbeSkipsCUEAndDuplicates(t *testing.T) {
	t.Run("CUE never probed", func(t *testing.T) {
		prober := &fakeProber{}
		m, _ := newPlaylistModel(t, func(opts *Options) { opts.Prober = prober })
		m.queue.Append(queue.Item{Path: "/music/album.flac", CuePath: "/music/album.cue", Title: "Track"})
		if cmd := m.probeItems(); cmd != nil {
			t.Fatalf("probeItems returned a command for a CUE track")
		}
		if prober.callCount() != 0 {
			t.Errorf("probe calls = %d, want 0", prober.callCount())
		}
	})

	t.Run("duplicate probes avoided while in flight", func(t *testing.T) {
		prober := &fakeProber{}
		m, _ := newPlaylistModel(t, func(opts *Options) { opts.Prober = prober })
		item := playlistItem("song", "", 0, 0)
		item.Path = "/music/song.mp3"
		m.queue.Append(item)
		id := m.queue.Snapshot().Items[0].ID

		if cmd := m.probeItems(); cmd == nil {
			t.Fatal("first probeItems returned nil")
		}
		if cmd := m.probeItems(); cmd != nil {
			t.Fatal("second probeItems scheduled a duplicate probe")
		}
		if path, ok := m.probing[id]; !ok || path != "/music/song.mp3" {
			t.Errorf("in-flight probe = %q (ok=%v), want the item path", path, ok)
		}
	})
}

func TestFailedProbeIgnored(t *testing.T) {
	prober := &fakeProber{err: map[string]error{"/music/song.mp3": errors.New("ffprobe missing")}}
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Prober = prober })
	item := playlistItem("song", "", 0, 0)
	item.Path = "/music/song.mp3"
	m.queue.Append(item)

	m = runCmd(t, m, m.probeItems())
	if m.statusIsError {
		t.Errorf("failed probe set an error status: %q", m.statusMsg)
	}
	if got := m.queue.Snapshot().Items[0]; got.Title != "song" || got.Duration != 0 {
		t.Errorf("failed probe changed item: %#v", got)
	}
	if len(m.probing) != 0 {
		t.Errorf("in-flight probes = %v, want cleared", m.probing)
	}
}

func TestSaveAfterMutations(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	store := m.store.(*fakeStore)
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
	)

	m, cmd := pressForCmd(t, m, runeKey('d'))
	m = runCmd(t, m, cmd)
	snapshot, ok := store.lastSaved()
	if !ok {
		t.Fatal("remove did not save the queue")
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Title != "Two" {
		t.Errorf("saved snapshot = %#v, want the remaining item", snapshot)
	}
	if snapshot.Current != 0 {
		t.Errorf("saved current = %d, want 0", snapshot.Current)
	}
}

type blockingStore struct {
	mu      sync.Mutex
	saved   []queue.Snapshot
	started chan struct{}
	release chan struct{}
}

func (s *blockingStore) Load() (queue.Snapshot, int, error) {
	return queue.Snapshot{}, 0, nil
}

func (s *blockingStore) Save(snapshot queue.Snapshot) error {
	s.mu.Lock()
	s.saved = append(s.saved, snapshot)
	s.mu.Unlock()
	s.started <- struct{}{}
	<-s.release
	return nil
}

func (s *blockingStore) snapshots() []queue.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]queue.Snapshot(nil), s.saved...)
}

func TestSaveQueueCoalescesWhileSaving(t *testing.T) {
	store := &blockingStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Store = store })
	m.queue.Append(playlistItem("One", "", 0, time.Minute))

	first := m.saveQueue()
	if first == nil {
		t.Fatal("first saveQueue returned nil")
	}
	if !m.saving {
		t.Fatal("saving = false after scheduling a save")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- first() }()
	<-store.started

	m.queue.Append(playlistItem("Two", "", 0, time.Minute))
	if cmd := m.saveQueue(); cmd != nil {
		t.Fatal("saveQueue scheduled a second concurrent save")
	}
	if !m.saveDirty {
		t.Fatal("saveDirty = false after a mutation during save")
	}

	close(store.release)
	msg := <-done
	next, follow := m.Update(msg)
	updated := next.(*Model)
	if !updated.saving {
		t.Error("follow-up save was not marked in flight")
	}
	if updated.saveDirty {
		t.Error("saveDirty stayed true after the follow-up save was scheduled")
	}
	if follow == nil {
		t.Fatal("dirty save did not schedule a follow-up")
	}
	updated = runCmd(t, updated, follow)
	if updated.saving {
		t.Error("saving stayed true after the follow-up save completed")
	}

	snapshots := store.snapshots()
	if len(snapshots) != 2 {
		t.Fatalf("saves = %d, want 2", len(snapshots))
	}
	if got := len(snapshots[1].Items); got != 2 {
		t.Errorf("final saved snapshot has %d items, want 2", got)
	}
}

func TestSaveErrorSetsStatus(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	store := m.store.(*fakeStore)
	store.saveErr = errors.New("disk full")
	m.queue.Append(playlistItem("One", "", 0, time.Minute))

	m = runCmd(t, m, m.saveQueue())
	if !m.statusIsError || !strings.Contains(m.statusMsg, "disk full") {
		t.Errorf("status = %q (error=%v), want save error", m.statusMsg, m.statusIsError)
	}
}

func TestStaleProbeResultsIgnored(t *testing.T) {
	t.Run("reused ID with a different path", func(t *testing.T) {
		m, _ := newPlaylistModel(t, nil)
		m.queue.Append(queue.Item{Path: "/music/new.mp3", Title: "New", Duration: time.Minute})
		id := m.queue.Snapshot().Items[0].ID
		m.probing = map[uint64]string{id: "/music/new.mp3"}

		cmd := m.applyQueueMetadata(queueMetadataMsg{
			id:   id,
			path: "/music/old.mp3",
			info: meta.Info{Title: "Stale", Duration: 9 * time.Minute},
		})
		if cmd != nil {
			t.Error("stale probe result scheduled a save")
		}
		if got := m.queue.Snapshot().Items[0].Title; got != "New" {
			t.Errorf("stale probe changed the reused item to %q", got)
		}
		if path, ok := m.probing[id]; !ok || path != "/music/new.mp3" {
			t.Errorf("in-flight marker = %q (ok=%v), want the new path", path, ok)
		}
	})

	t.Run("CUE item with the same path", func(t *testing.T) {
		m, _ := newPlaylistModel(t, nil)
		m.queue.Append(queue.Item{
			Path:     "/music/song.flac",
			CuePath:  "/music/song.cue",
			CueTrack: 1,
			Title:    "Track",
			Duration: time.Minute,
		})
		id := m.queue.Snapshot().Items[0].ID
		m.probing = map[uint64]string{id: "/music/song.flac"}

		cmd := m.applyQueueMetadata(queueMetadataMsg{
			id:   id,
			path: "/music/song.flac",
			info: meta.Info{Title: "Whole File", Duration: 9 * time.Minute},
		})
		if cmd != nil {
			t.Error("CUE probe result scheduled a save")
		}
		if got := m.queue.Snapshot().Items[0].Title; got != "Track" {
			t.Errorf("CUE item title = %q, want Track", got)
		}
	})
}

func TestStalePlaylistSelectionIsClamped(t *testing.T) {
	t.Run("open clamps to the last item", func(t *testing.T) {
		m, fp := newPlaylistModel(t, nil)
		m.queue.Append(
			playlistItem("One", "", 1, time.Minute),
			playlistItem("Two", "", 2, time.Minute),
		)
		m.playlistSelected = 99

		m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
		m = runCmd(t, m, cmd)
		if got := m.queue.Snapshot().Current; got != 1 {
			t.Errorf("current = %d, want the clamped last item", got)
		}
		if len(fp.played) != 1 || fp.played[0].Title != "Two" {
			t.Errorf("played = %#v, want Two", fp.played)
		}
	})

	t.Run("remove clamps and then reports an empty queue", func(t *testing.T) {
		m, _ := newPlaylistModel(t, nil)
		m.queue.Append(playlistItem("One", "", 1, time.Minute))
		m.playlistSelected = 99

		m, cmd := pressForCmd(t, m, runeKey('d'))
		m = runCmd(t, m, cmd)
		if got := len(m.queue.Snapshot().Items); got != 0 {
			t.Errorf("queue length = %d, want 0", got)
		}

		m.playlistSelected = 99
		m = press(t, m, runeKey('d'))
		if got := m.statusMsg; got != "nothing to remove" {
			t.Errorf("statusMsg = %q, want nothing to remove", got)
		}
	})
}

// blockingLoadStore delays Load so a test can mutate the queue before the
// startup restore result is delivered.
type blockingLoadStore struct {
	mu       sync.Mutex
	snapshot queue.Snapshot
	skipped  int
	started  chan struct{}
	release  chan struct{}
	saved    []queue.Snapshot
}

func (s *blockingLoadStore) Load() (queue.Snapshot, int, error) {
	s.started <- struct{}{}
	<-s.release
	return s.snapshot, s.skipped, nil
}

func (s *blockingLoadStore) Save(snapshot queue.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, snapshot)
	return nil
}

func (s *blockingLoadStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved)
}

func (s *blockingLoadStore) lastSaved() (queue.Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saved) == 0 {
		return queue.Snapshot{}, false
	}
	return s.saved[len(s.saved)-1], true
}

func TestLateRestoreDoesNotClobberUserQueue(t *testing.T) {
	store := &blockingLoadStore{
		snapshot: queue.Snapshot{
			Items: []queue.Item{
				{Path: "/music/restored.flac", Title: "Restored", Duration: time.Minute},
			},
			Current: 0,
		},
		skipped: 2,
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Store = store })

	loadCmd := m.restoreCmd()
	result := make(chan tea.Msg, 1)
	go func() { result <- loadCmd() }()
	<-store.started

	// The user adds a track while the restore is still loading.
	next, cmd := m.Update(browserAddMsg{
		gen:    m.browserGen,
		items:  []queue.Item{{Path: "/music/new.flac", Title: "New", Duration: time.Minute}},
		status: "added 1 item(s)",
	})
	m = next.(*Model)
	m = runCmd(t, m, cmd)

	close(store.release)
	next, follow := m.Update(<-result)
	m = next.(*Model)
	if follow != nil {
		t.Error("late restore scheduled queue work")
	}

	snapshot := m.queue.Snapshot()
	if len(snapshot.Items) != 1 || snapshot.Items[0].Title != "New" {
		t.Fatalf("late restore clobbered the queue: %#v", snapshot.Items)
	}
	if snapshot.Current != 0 || m.playlistSelected != 0 {
		t.Errorf("current/selection = %d/%d, want 0/0", snapshot.Current, m.playlistSelected)
	}
	if got := store.saveCount(); got != 1 {
		t.Errorf("saves = %d, want only the user mutation save", got)
	}
	saved, ok := store.lastSaved()
	if !ok {
		t.Fatal("user mutation was not saved")
	}
	if len(saved.Items) != 1 || saved.Items[0].Title != "New" {
		t.Errorf("persisted snapshot = %#v, want the newer queue", saved.Items)
	}
	if !strings.Contains(m.statusMsg, "restore skipped") {
		t.Errorf("statusMsg = %q, want restore skipped", m.statusMsg)
	}
}

func TestRestoreQueueOnInit(t *testing.T) {
	snapshot := queue.Snapshot{
		Items: []queue.Item{
			{ID: 1, Path: "/music/a.flac", Title: "A", Duration: time.Minute},
			{ID: 2, Path: "/music/b.flac", Title: "B", Duration: time.Minute},
		},
		Current: 1,
	}
	t.Run("success selects current", func(t *testing.T) {
		store := &fakeStore{snapshot: snapshot}
		m, _ := newPlaylistModel(t, func(opts *Options) { opts.Store = store })

		m = runCmd(t, m, m.restoreCmd())
		if got := len(m.queue.Snapshot().Items); got != 2 {
			t.Fatalf("queue length = %d, want 2", got)
		}
		if m.queue.Snapshot().Current != 1 || m.playlistSelected != 1 {
			t.Errorf("current/selection = %d/%d, want 1/1", m.queue.Snapshot().Current, m.playlistSelected)
		}
		if !strings.Contains(m.statusMsg, "restored 2 item(s)") {
			t.Errorf("status = %q, want restored count", m.statusMsg)
		}
	})

	t.Run("skipped items are reported and saved", func(t *testing.T) {
		store := &fakeStore{snapshot: snapshot, skipped: 3}
		m, _ := newPlaylistModel(t, func(opts *Options) { opts.Store = store })

		m = runCmd(t, m, m.restoreCmd())
		if !strings.Contains(m.statusMsg, "skipped 3") {
			t.Errorf("status = %q, want skipped count", m.statusMsg)
		}
		if store.saveCount() == 0 {
			t.Error("pruned restore did not save the queue")
		}
	})

	t.Run("error keeps the queue empty", func(t *testing.T) {
		store := &fakeStore{loadErr: errors.New("corrupt queue")}
		m, _ := newPlaylistModel(t, func(opts *Options) { opts.Store = store })

		m = runCmd(t, m, m.restoreCmd())
		if got := len(m.queue.Snapshot().Items); got != 0 {
			t.Errorf("queue length = %d, want 0", got)
		}
		if !m.statusIsError || !strings.Contains(m.statusMsg, "corrupt queue") {
			t.Errorf("status = %q (error=%v), want restore error", m.statusMsg, m.statusIsError)
		}
	})

	t.Run("restore disabled skips the load", func(t *testing.T) {
		withRestore := newTestModel(t, nil)
		withoutRestore := newTestModel(t, func(opts *Options) { opts.Config.RestoreQueue = false })

		count := func(m *Model) int {
			msg := m.Init()()
			batch, ok := msg.(tea.BatchMsg)
			if !ok {
				t.Fatalf("Init returned %T, want BatchMsg", msg)
			}
			return len(batch)
		}
		if got, want := count(withoutRestore), count(withRestore)-1; got != want {
			t.Errorf("Init commands without restore = %d, want %d", got, want)
		}
	})
}

func TestRestoreSchedulesProbesForPlainItems(t *testing.T) {
	prober := &fakeProber{info: map[string]meta.Info{
		"/music/song.mp3": {Title: "Probed", Duration: 2 * time.Minute},
	}}
	store := &fakeStore{snapshot: queue.Snapshot{
		Items: []queue.Item{
			{Path: "/music/song.mp3", Title: "song", Duration: 0},
		},
		Current: 0,
	}}
	m, _ := newPlaylistModel(t, func(opts *Options) {
		opts.Store = store
		opts.Prober = prober
	})

	m = runCmd(t, m, m.restoreCmd())
	if prober.callCount() != 1 {
		t.Fatalf("restore probes = %d, want 1", prober.callCount())
	}
	item := m.queue.Snapshot().Items[0]
	if item.Title != "Probed" || item.Duration != 2*time.Minute {
		t.Errorf("restored item = %#v, want probed metadata", item)
	}
}

func TestBrowserAppendSchedulesProbes(t *testing.T) {
	prober := &fakeProber{info: map[string]meta.Info{
		"/music/song.mp3": {Title: "Probed", Duration: 2 * time.Minute},
	}}
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Prober = prober })

	next, cmd := m.Update(browserAddMsg{
		gen:    m.browserGen,
		items:  []queue.Item{{Path: "/music/song.mp3", Title: "song"}},
		status: "added 1 item(s)",
	})
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("browser add returned no probe command")
	}
	m = runCmd(t, m, cmd)
	if prober.callCount() != 1 {
		t.Fatalf("append probes = %d, want 1", prober.callCount())
	}
	if got := m.queue.Snapshot().Items[0].Title; got != "Probed" {
		t.Errorf("appended item title = %q, want Probed", got)
	}
}

func TestBrowserPlaySchedulesQueueWorkBeforePlayCompletes(t *testing.T) {
	prober := &fakeProber{info: map[string]meta.Info{
		"/music/song.mp3": {Title: "Probed", Duration: time.Minute},
	}}
	player := newControlledPlayer()
	store := &fakeStore{}
	m := newTestModel(t, func(opts *Options) {
		opts.Player = player
		opts.Store = store
		opts.Prober = prober
		opts.StartDir = "/music"
	})
	m = resize(t, m, 100, 30)

	cmd := m.applyBrowserPlay(browserPlayMsg{
		gen:      m.browserGen,
		items:    []queue.Item{{Path: "/music/song.mp3", Title: "song"}},
		selected: 0,
		status:   "playing 1 item(s)",
	})
	if cmd == nil {
		t.Fatal("browser play returned no command")
	}

	msgs := make(chan tea.Msg, 32)
	runAsyncCmd(cmd, msgs)
	select {
	case <-player.started:
	case <-time.After(time.Second):
		t.Fatal("Play did not start")
	}

	// Play is blocked on release here: probing and saving must already have run.
	deadline := time.After(2 * time.Second)
	seenProbe, seenSave := false, false
	for !seenProbe || !seenSave {
		select {
		case msg := <-msgs:
			switch msg.(type) {
			case queueMetadataMsg:
				seenProbe = true
			case queueSavedMsg:
				seenSave = true
			}
			next, follow := m.Update(msg)
			m = next.(*Model)
			if follow != nil {
				runAsyncCmd(follow, msgs)
			}
		case <-deadline:
			t.Fatalf("queue work waited for Play: probe=%v save=%v", seenProbe, seenSave)
		}
	}
	if len(player.played) != 0 {
		t.Fatal("Play completed before the queue work")
	}

	close(player.release)
	deadline = time.After(2 * time.Second)
	for {
		select {
		case msg := <-msgs:
			next, follow := m.Update(msg)
			m = next.(*Model)
			if follow != nil {
				runAsyncCmd(follow, msgs)
			}
			if _, ok := msg.(browserPlayResultMsg); ok {
				if m.statusIsError {
					t.Errorf("statusIsError = true after successful play: %q", m.statusMsg)
				}
				if !strings.Contains(m.statusMsg, "playing") {
					t.Errorf("statusMsg = %q, want playing status", m.statusMsg)
				}
				return
			}
		case <-deadline:
			t.Fatal("Play result was not delivered")
		}
	}
}

func TestTransportWorksFromBrowserFocus(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.focus = PaneBrowser
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
	)

	m, cmd := pressForCmd(t, m, runeKey('n'))
	m = runCmd(t, m, cmd)
	if got := m.queue.Snapshot().Current; got != 1 {
		t.Errorf("current after browser-focus next = %d, want 1", got)
	}
	if len(fp.played) != 1 {
		t.Errorf("played = %#v, want one item", fp.played)
	}

	m = press(t, m, specialKey(tea.KeySpace))
	if fp.pauses != 1 {
		t.Errorf("browser-focus pause did not reach the player")
	}
}

func TestPlaylistOnlyKeysIgnoredInBrowserFocus(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	m.focus = PaneBrowser
	m.queue.Append(
		playlistItem("One", "", 1, time.Minute),
		playlistItem("Two", "", 2, time.Minute),
	)
	m.queue.SetCurrent(1)
	m.playlistSelected = 0

	for _, r := range []rune{'d', 'c', 'g', 'K', 'J'} {
		m = press(t, m, runeKey(r))
	}
	if got := len(m.queue.Snapshot().Items); got != 2 {
		t.Errorf("queue length = %d, want 2", got)
	}
	if got := m.queue.Snapshot().Current; got != 1 {
		t.Errorf("current = %d, want 1", got)
	}
	if m.playlistSelected != 0 {
		t.Errorf("selection = %d, want 0", m.playlistSelected)
	}
	if fp.stops != 0 {
		t.Errorf("stops = %d, want 0", fp.stops)
	}
}

func TestClosedEventChannelDoesNotResubscribe(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	if err := fp.Close(); err != nil {
		t.Fatalf("close fake player: %v", err)
	}
	cmd := m.waitForPlayerEvent()
	if cmd == nil {
		t.Fatal("waitForPlayerEvent returned nil with a player")
	}
	msg, ok := cmd().(playerEventMsg)
	if !ok {
		t.Fatalf("event command returned %T, want playerEventMsg", msg)
	}
	if msg.ok {
		t.Error("closed channel reported ok = true")
	}
	next, follow := m.Update(msg)
	if follow != nil {
		t.Error("closed event channel resubscribed")
	}
	if next.(*Model) != m {
		t.Error("closed event channel changed the model")
	}
}

func TestPlayerEventUpdateResubscribes(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m.queue.Append(playlistItem("One", "", 1, time.Minute))

	// Update returns the event handler work plus a fresh wait command.
	_, follow := m.Update(playerEventMsg{event: player.Event{Type: player.EventFinished}, ok: true})
	if follow == nil {
		t.Fatal("finished event did not return a follow-up command")
	}
	// Do not execute the follow-up: its event wait would block until the next
	// real event; handlePlayerEvent behavior is covered above.
}
