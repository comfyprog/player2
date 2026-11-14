package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/player"
	"player2/internal/queue"
)

// fillTab replaces one tab's queue with items and selects the first item.
func fillTab(t *testing.T, m *Model, tab int, items ...queue.Item) {
	t.Helper()
	q := m.tabs[tab].queue
	q.Replace(items...)
	if len(items) > 0 {
		q.SetCurrent(0)
	}
}

func TestNewCreatesTenIndependentTabs(t *testing.T) {
	m := newTestModel(t, nil)

	if m.activeTab != 0 {
		t.Errorf("activeTab = %d, want 0", m.activeTab)
	}
	if m.playbackTab != -1 {
		t.Errorf("playbackTab = %d, want -1", m.playbackTab)
	}
	if m.queue != m.tabs[0].queue {
		t.Error("queue is not the active tab's queue")
	}

	seen := make(map[*queue.Queue]bool, tabCount)
	for i := 0; i < tabCount; i++ {
		q := m.tabs[i].queue
		if q == nil {
			t.Fatalf("tab %d has a nil queue", i)
		}
		if seen[q] {
			t.Fatalf("tab %d shares a queue with another tab", i)
		}
		seen[q] = true
	}
}

func TestSwitchTabSwapsQueueAndSelection(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	var zeroItems []queue.Item
	for i := 0; i < 60; i++ {
		zeroItems = append(zeroItems, playlistItem("Zero", "", i+1, time.Minute))
	}
	fillTab(t, m, 0, zeroItems...)
	m.playlistSelected = 25
	m.playlistOffset = 5

	var threeItems []queue.Item
	for i := 0; i < 60; i++ {
		threeItems = append(threeItems, playlistItem("Three", "", i+1, time.Minute))
	}
	fillTab(t, m, 3, threeItems...)
	m.tabs[3].selected = 30
	m.tabs[3].offset = 10

	m = runCmd(t, m, m.switchTab(3))
	if m.activeTab != 3 {
		t.Fatalf("activeTab = %d, want 3", m.activeTab)
	}
	if m.queue != m.tabs[3].queue {
		t.Fatal("active queue was not swapped to tab 3")
	}
	if m.playlistSelected != 30 || m.playlistOffset != 10 {
		t.Errorf("tab 3 selection/offset = %d/%d, want 30/10", m.playlistSelected, m.playlistOffset)
	}
	if !strings.Contains(m.statusMsg, "tab 3") {
		t.Errorf("statusMsg = %q, want a tab 3 message", m.statusMsg)
	}

	m = runCmd(t, m, m.switchTab(0))
	if m.activeTab != 0 {
		t.Fatalf("activeTab = %d, want 0", m.activeTab)
	}
	if m.playlistSelected != 25 || m.playlistOffset != 5 {
		t.Errorf("tab 0 selection/offset = %d/%d, want 25/5", m.playlistSelected, m.playlistOffset)
	}
}

func TestRestoreLoadsAllTabsAndActiveTab(t *testing.T) {
	tabs := []queue.Snapshot{
		{
			Items:   []queue.Item{{Path: "/music/zero.flac", Title: "Zero", Duration: time.Minute}},
			Current: 0,
		},
		{
			Items:   []queue.Item{{Path: "/music/one.flac", Title: "One", Duration: time.Minute}},
			Current: 0,
		},
		{
			Items: []queue.Item{
				{Path: "/music/two-a.flac", Title: "Two A", Duration: time.Minute},
				{Path: "/music/two-b.flac", Title: "Two B", Duration: time.Minute},
			},
			Current: 1,
		},
	}
	store := &fakeStore{active: 2, tabs: tabs}
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Store = store })

	m = runCmd(t, m, m.restoreCmd())

	if m.restorePending {
		t.Error("restorePending = true after a successful restore")
	}
	if m.activeTab != 2 {
		t.Fatalf("activeTab = %d, want 2", m.activeTab)
	}
	if m.playbackTab != -1 {
		t.Errorf("playbackTab = %d, want -1 after restore", m.playbackTab)
	}
	if got := m.queue.Len(); got != 2 {
		t.Errorf("active tab length = %d, want 2", got)
	}
	if m.playlistSelected != 1 {
		t.Errorf("playlistSelected = %d, want 1", m.playlistSelected)
	}
	if got := m.tabs[0].queue.Len(); got != 1 {
		t.Errorf("tab 0 length = %d, want 1", got)
	}
	if got := m.tabs[1].queue.Len(); got != 1 {
		t.Errorf("tab 1 length = %d, want 1", got)
	}
	if got := m.tabs[3].queue.Len(); got != 0 {
		t.Errorf("tab 3 length = %d, want 0", got)
	}
}

func TestSavePersistsAllTabsAndActiveTab(t *testing.T) {
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Config.RestoreQueue = false })
	store := m.store.(*fakeStore)
	fillTab(t, m, 0, playlistItem("Zero", "", 1, time.Minute))
	fillTab(t, m, 4, playlistItem("Four", "", 1, time.Minute))

	m = runCmd(t, m, m.switchTab(4))
	m = runCmd(t, m, m.saveQueue())

	active, tabs, ok := store.lastSavedTabs()
	if !ok {
		t.Fatal("saveQueue did not save")
	}
	if active != 4 {
		t.Errorf("saved active tab = %d, want 4", active)
	}
	if len(tabs) != tabCount {
		t.Fatalf("saved %d tabs, want %d", len(tabs), tabCount)
	}
	if len(tabs[0].Items) != 1 || tabs[0].Items[0].Title != "Zero" {
		t.Errorf("saved tab 0 = %+v", tabs[0])
	}
	if len(tabs[4].Items) != 1 || tabs[4].Items[0].Title != "Four" {
		t.Errorf("saved tab 4 = %+v", tabs[4])
	}
}

func TestFlushQueueSavesAllTabs(t *testing.T) {
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Config.RestoreQueue = false })
	store := m.store.(*fakeStore)
	fillTab(t, m, 0, playlistItem("Zero", "", 1, time.Minute))
	fillTab(t, m, 6, playlistItem("Six", "", 1, time.Minute))
	m = runCmd(t, m, m.switchTab(6))

	if err := m.FlushQueue(); err != nil {
		t.Fatalf("FlushQueue() error = %v", err)
	}

	active, tabs, ok := store.lastSavedTabs()
	if !ok {
		t.Fatal("FlushQueue did not save")
	}
	if active != 6 {
		t.Errorf("flushed active tab = %d, want 6", active)
	}
	if len(tabs) != tabCount {
		t.Fatalf("flushed %d tabs, want %d", len(tabs), tabCount)
	}
	if len(tabs[6].Items) != 1 || tabs[6].Items[0].Title != "Six" {
		t.Errorf("flushed tab 6 = %+v", tabs[6])
	}
}

func TestTabsRenderTenSlotsWithActiveMarker(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	fillTab(t, m, 0, playlistItem("Zero", "", 1, time.Minute))
	fillTab(t, m, 2, playlistItem("Two", "", 1, time.Minute))
	m = runCmd(t, m, m.switchTab(2))

	lines := m.playlistLines(40, 6)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	for _, digit := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		if !strings.Contains(plain, digit) {
			t.Errorf("tab strip missing %q:\n%s", digit, plain)
		}
	}
	if !strings.Contains(plain, "[2]") {
		t.Errorf("tab strip missing active marker [2]:\n%s", plain)
	}
	for i, line := range lines {
		if width := ansi.StringWidth(line); width != 40 {
			t.Errorf("line %d width = %d, want 40", i, width)
		}
	}
}

func TestEmptyTabShowsTabNumber(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m = runCmd(t, m, m.switchTab(5))
	plain := ansi.Strip(strings.Join(m.playlistLines(40, 6), "\n"))
	if !strings.Contains(plain, "Tab 5 is empty") {
		t.Errorf("empty tab render missing tab number:\n%s", plain)
	}
}

// playSelected starts the visible tab's selected item and returns the model
// after the serialized transport result lands.
func playSelected(t *testing.T, m *Model) *Model {
	t.Helper()
	m.focus = PanePlaylist
	next, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	return runCmd(t, next, cmd)
}

func TestSwitchTabKeepsAudioOnSoundingTab(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	fillTab(t, m, 0,
		playlistItem("Zero One", "", 1, time.Minute),
		playlistItem("Zero Two", "", 2, time.Minute),
	)
	fillTab(t, m, 1, playlistItem("One One", "", 1, time.Minute))
	m.playlistSelected = 0

	m = playSelected(t, m)
	if m.playbackTab != 0 {
		t.Fatalf("playbackTab = %d after play, want 0", m.playbackTab)
	}

	m = runCmd(t, m, m.switchTab(1))
	if m.activeTab != 1 {
		t.Fatalf("activeTab = %d, want 1", m.activeTab)
	}
	if m.playbackTab != 0 {
		t.Errorf("switching tabs changed playbackTab to %d", m.playbackTab)
	}
	if len(fp.played) != 1 || fp.played[0].Title != "Zero One" {
		t.Fatalf("played = %#v, want only Zero One", fp.played)
	}

	header := ansi.Strip(m.headerLine())
	if !strings.Contains(header, "Zero One") || !strings.Contains(header, "tab 0") {
		t.Errorf("header = %q, want sounding item from tab 0", header)
	}

	plain := ansi.Strip(strings.Join(m.playlistLines(40, 6), "\n"))
	if !strings.Contains(plain, "One One") {
		t.Errorf("visible tab does not show tab 1's item:\n%s", plain)
	}
	if !strings.Contains(plain, "0▶") || !strings.Contains(plain, "[1]") {
		t.Errorf("tab strip lacks sounding/active markers:\n%s", plain)
	}
}

func TestNextFollowsSoundingTabWhileAnotherTabIsVisible(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	fillTab(t, m, 0,
		playlistItem("Zero One", "", 1, time.Minute),
		playlistItem("Zero Two", "", 2, time.Minute),
	)
	fillTab(t, m, 1,
		playlistItem("One One", "", 1, time.Minute),
		playlistItem("One Two", "", 2, time.Minute),
	)
	m.playlistSelected = 0
	m = playSelected(t, m)
	m = runCmd(t, m, m.switchTab(1))
	m.playlistSelected = 0

	next, cmd := pressForCmd(t, m, runeKey('n'))
	m = runCmd(t, next, cmd)

	if m.activeTab != 1 {
		t.Errorf("activeTab = %d, want 1", m.activeTab)
	}
	if got := m.tabs[0].queue.Snapshot().Current; got != 1 {
		t.Errorf("sounding tab current = %d, want 1", got)
	}
	if got := m.tabs[1].queue.Snapshot().Current; got != 0 {
		t.Errorf("visible tab current = %d, want 0", got)
	}
	if len(fp.played) != 2 || fp.played[1].Title != "Zero Two" {
		t.Fatalf("played = %#v, want Zero Two second", fp.played)
	}
	if m.tabs[0].selected != 1 {
		t.Errorf("sounding tab selection = %d, want 1", m.tabs[0].selected)
	}
	if m.playlistSelected != 0 {
		t.Errorf("visible tab selection = %d, want 0", m.playlistSelected)
	}
}

func TestAutoAdvanceHiddenTabUpdatesThatQueue(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	fillTab(t, m, 0,
		playlistItem("Zero One", "", 1, time.Minute),
		playlistItem("Zero Two", "", 2, time.Minute),
	)
	fillTab(t, m, 1,
		playlistItem("One One", "", 1, time.Minute),
		playlistItem("One Two", "", 2, time.Minute),
	)
	m.playlistSelected = 0
	m = playSelected(t, m)
	m = runCmd(t, m, m.switchTab(1))

	current, ok := m.tabs[0].queue.Current()
	if !ok {
		t.Fatal("tab 0 has no current item")
	}
	if current.ID != 1 {
		t.Fatalf("tab 0 current ID = %d, want 1", current.ID)
	}
	other, _ := m.tabs[1].queue.Current()
	if other.ID != current.ID {
		t.Fatalf("test setup: tab 1 current ID = %d, want collision with %d", other.ID, current.ID)
	}

	cmd := m.handlePlayerEvent(playerEventMsg{
		event: player.Event{Type: player.EventFinished, ItemID: current.ID},
		ok:    true,
	})
	m = runCmd(t, m, cmd)

	if got := m.tabs[0].queue.Snapshot().Current; got != 1 {
		t.Errorf("sounding tab current = %d, want 1", got)
	}
	if got := m.tabs[1].queue.Snapshot().Current; got != 0 {
		t.Errorf("visible tab current = %d, want 0 (must not consume tab 0's event)", got)
	}
	if m.activeTab != 1 {
		t.Errorf("activeTab = %d, want 1", m.activeTab)
	}
	if m.playbackTab != 0 {
		t.Errorf("playbackTab = %d, want 0", m.playbackTab)
	}
	if len(fp.played) != 2 || fp.played[1].Title != "Zero Two" {
		t.Fatalf("played = %#v, want Zero Two second", fp.played)
	}
}

func TestStopClearsSoundingContext(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	fillTab(t, m, 0,
		playlistItem("Zero One", "", 1, time.Minute),
		playlistItem("Zero Two", "", 2, time.Minute),
	)
	fillTab(t, m, 1,
		playlistItem("One One", "", 1, time.Minute),
		playlistItem("One Two", "", 2, time.Minute),
	)
	m.playlistSelected = 0
	m = playSelected(t, m)
	m = runCmd(t, m, m.switchTab(1))

	next, cmd := pressForCmd(t, m, runeKey('x'))
	m = runCmd(t, next, cmd)
	if m.playbackTab != -1 {
		t.Fatalf("playbackTab = %d after stop, want -1", m.playbackTab)
	}

	next, cmd = pressForCmd(t, m, runeKey('n'))
	m = runCmd(t, next, cmd)
	if m.activeTab != 1 {
		t.Errorf("activeTab = %d, want 1", m.activeTab)
	}
	if got := m.tabs[1].queue.Snapshot().Current; got != 1 {
		t.Errorf("visible tab current = %d after stop+next, want 1", got)
	}
	if got := m.tabs[0].queue.Snapshot().Current; got != 0 {
		t.Errorf("old sounding tab current = %d, want 0", got)
	}
	if m.playbackTab != 1 {
		t.Errorf("playbackTab = %d after next, want 1", m.playbackTab)
	}
	if last := fp.played[len(fp.played)-1]; last.Title != "One Two" {
		t.Errorf("last played = %q, want One Two", last.Title)
	}
}

func TestGotoPlayingSwitchesToSoundingTab(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	for i := 0; i < 10; i++ {
		m.tabs[0].queue.Append(playlistItem("Zero", "", i+1, time.Minute))
	}
	m.tabs[0].queue.SetCurrent(7)
	fillTab(t, m, 1, playlistItem("One", "", 1, time.Minute))
	m.playlistSelected = 7
	m = playSelected(t, m)
	m = runCmd(t, m, m.switchTab(1))
	m.playlistSelected = 0

	next, cmd := pressForCmd(t, m, runeKey('g'))
	m = runCmd(t, next, cmd)

	if m.activeTab != 0 {
		t.Errorf("activeTab = %d, want 0", m.activeTab)
	}
	if m.playlistSelected != 7 {
		t.Errorf("selection = %d, want 7", m.playlistSelected)
	}
}

func TestClearingNonSoundingTabKeepsPlayback(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	fillTab(t, m, 0,
		playlistItem("Zero One", "", 1, time.Minute),
		playlistItem("Zero Two", "", 2, time.Minute),
	)
	fillTab(t, m, 1,
		playlistItem("One One", "", 1, time.Minute),
		playlistItem("One Two", "", 2, time.Minute),
	)
	m.playlistSelected = 0
	m = playSelected(t, m)
	m = runCmd(t, m, m.switchTab(1))

	m = runCmd(t, m, m.playlistClear())

	if fp.stops != 0 {
		t.Errorf("clear of non-sounding tab stopped playback %d time(s)", fp.stops)
	}
	if m.playbackTab != 0 {
		t.Errorf("playbackTab = %d, want 0", m.playbackTab)
	}
	if got := m.tabs[0].queue.Len(); got != 2 {
		t.Errorf("sounding tab length = %d, want 2", got)
	}
	if got := m.tabs[1].queue.Len(); got != 0 {
		t.Errorf("cleared tab length = %d, want 0", got)
	}
}

func TestRemovingCurrentFromNonSoundingTabKeepsPlayback(t *testing.T) {
	m, fp := newPlaylistModel(t, nil)
	fillTab(t, m, 0,
		playlistItem("Zero One", "", 1, time.Minute),
		playlistItem("Zero Two", "", 2, time.Minute),
	)
	fillTab(t, m, 1,
		playlistItem("One One", "", 1, time.Minute),
		playlistItem("One Two", "", 2, time.Minute),
	)
	m.playlistSelected = 0
	m = playSelected(t, m)
	m = runCmd(t, m, m.switchTab(1))
	m.playlistSelected = 0

	m = runCmd(t, m, m.playlistRemove())

	if fp.stops != 0 {
		t.Errorf("removing a non-sounding tab's current item stopped playback %d time(s)", fp.stops)
	}
	if m.playbackTab != 0 {
		t.Errorf("playbackTab = %d, want 0", m.playbackTab)
	}
	if got := m.tabs[0].queue.Len(); got != 2 {
		t.Errorf("sounding tab length = %d, want 2", got)
	}
	if got := m.tabs[1].queue.Len(); got != 1 {
		t.Errorf("edited tab length = %d, want 1", got)
	}
}
