package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

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
