package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/queue"
)

func TestNameTabPromptPrefillsRenamesAndPersists(t *testing.T) {
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Config.RestoreQueue = false })
	m.tabs[0].name = "Jazz"
	store := m.store.(*fakeStore)

	m = press(t, m, runeKey('T'))
	if m.prompt.mode != promptNameTab {
		t.Fatalf("prompt mode = %v after T, want name tab", m.prompt.mode)
	}
	if got := m.prompt.input.Value(); got != "Jazz" {
		t.Errorf("prefilled name = %q, want Jazz", got)
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"Name tab", "Tab 0", "Enter save", "Esc cancel"} {
		if !strings.Contains(plain, want) {
			t.Errorf("name prompt missing %q:\n%s", want, plain)
		}
	}

	m = press(t, m, ctrlKey('u'))
	m = typeText(t, m, "Live")
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	if m.prompt.mode != promptNone {
		t.Error("prompt remained open after submit")
	}
	if got := m.tabs[0].name; got != "Live" {
		t.Errorf("tab name = %q, want Live", got)
	}
	if !strings.Contains(m.statusMsg, "Live") {
		t.Errorf("status = %q, want the new name", m.statusMsg)
	}
	if cmd == nil {
		t.Fatal("rename returned no save command")
	}
	m = runCmd(t, m, cmd)

	active, tabs, ok := store.lastSavedTabs()
	if !ok {
		t.Fatal("rename was not persisted")
	}
	if active != 0 || len(tabs) != tabCount {
		t.Fatalf("saved active/tabs = %d/%d, want 0/%d", active, len(tabs), tabCount)
	}
	if tabs[0].Name != "Live" {
		t.Errorf("saved name = %q, want Live", tabs[0].Name)
	}
}

func TestNameTabPromptEmptyInputClearsName(t *testing.T) {
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Config.RestoreQueue = false })
	m.tabs[0].name = "Jazz"
	store := m.store.(*fakeStore)

	m = press(t, m, runeKey('T'))
	m = press(t, m, ctrlKey('u'))
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	if m.prompt.mode != promptNone {
		t.Error("prompt remained open after clearing")
	}
	if got := m.tabs[0].name; got != "" {
		t.Errorf("tab name = %q, want empty", got)
	}
	if !strings.Contains(m.statusMsg, "cleared") {
		t.Errorf("status = %q, want a cleared message", m.statusMsg)
	}
	m = runCmd(t, m, cmd)
	_, tabs, ok := store.lastSavedTabs()
	if !ok {
		t.Fatal("cleared name was not persisted")
	}
	if tabs[0].Name != "" {
		t.Errorf("saved name = %q, want empty", tabs[0].Name)
	}
}

func TestNameTabPromptEscLeavesNameAlone(t *testing.T) {
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Config.RestoreQueue = false })
	m.tabs[0].name = "Jazz"
	store := m.store.(*fakeStore)

	m = press(t, m, runeKey('T'))
	m = press(t, m, ctrlKey('u'))
	m = typeText(t, m, "Live")
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEscape))
	if cmd != nil {
		t.Errorf("cancel returned a command")
	}
	if m.prompt.mode != promptNone {
		t.Error("prompt stayed open after Esc")
	}
	if got := m.tabs[0].name; got != "Jazz" {
		t.Errorf("tab name = %q after Esc, want Jazz", got)
	}
	if store.saveCount() != 0 {
		t.Errorf("cancel saved %d time(s)", store.saveCount())
	}
}

func TestNameTabPromptOpensFromEitherPane(t *testing.T) {
	for _, focus := range []Pane{PaneBrowser, PanePlaylist} {
		m, _ := newPlaylistModel(t, nil)
		m.focus = focus

		m = press(t, m, runeKey('T'))
		if m.prompt.mode != promptNameTab {
			t.Errorf("focus %v: prompt mode = %v, want name tab", focus, m.prompt.mode)
		}
	}
}

func TestNameTabPromptAppliesToTabCapturedAtOpen(t *testing.T) {
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Config.RestoreQueue = false })
	m = runCmd(t, m, m.switchTab(2))

	m = press(t, m, runeKey('T'))
	m = runCmd(t, m, m.switchTab(4))
	m = typeText(t, m, "Focus")
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	m = runCmd(t, m, cmd)

	if got := m.tabs[2].name; got != "Focus" {
		t.Errorf("tab 2 name = %q, want Focus", got)
	}
	if got := m.tabs[4].name; got != "" {
		t.Errorf("tab 4 name = %q, want empty", got)
	}
}

func TestNameRendersOnlyForActiveTab(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m.tabs[0].name = "Jazz"
	m.tabs[1].name = "Rock"
	fillTab(t, m, 0, playlistItem("Zero", "", 1, time.Minute))
	fillTab(t, m, 1, playlistItem("One", "", 1, time.Minute))
	m = runCmd(t, m, m.switchTab(1))

	lines := m.playlistLines(40, 6)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "[1]") {
		t.Errorf("active tab marker missing:\n%s", plain)
	}
	if strings.Contains(plain, "Jazz") {
		t.Errorf("inactive tab name is visible:\n%s", plain)
	}
	if got := strings.TrimSpace(ansi.Strip(lines[1])); got != "Rock" {
		t.Errorf("name row = %q, want Rock", got)
	}
	if !strings.Contains(ansi.Strip(lines[2]), "01. One") {
		t.Errorf("queue does not start below the name row:\n%s", plain)
	}
}

func TestUnnamedActiveTabKeepsQueueOnFirstBodyRow(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	fillTab(t, m, 0, playlistItem("Zero", "", 1, time.Minute))

	lines := m.playlistLines(40, 6)
	if got := strings.TrimSpace(ansi.Strip(lines[1])); got == "" {
		t.Fatal("unnamed tab rendered an empty name row")
	}
	if !strings.Contains(ansi.Strip(lines[1]), "01. Zero") {
		t.Errorf("first body row = %q, want the queue item", ansi.Strip(lines[1]))
	}
}

func TestNamedEmptyTabShowsNameThenEmptyState(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m.tabs[0].name = "Jazz"

	lines := m.playlistLines(40, 6)
	if got := strings.TrimSpace(ansi.Strip(lines[1])); got != "Jazz" {
		t.Errorf("name row = %q, want Jazz", got)
	}
	if got := ansi.Strip(lines[2]); !strings.Contains(got, "Tab 0 is empty") {
		t.Errorf("empty state row = %q, want Tab 0 is empty", got)
	}
}

func TestNameLineReducesVisibleQueueRows(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	items := make([]queue.Item, 6)
	for i := range items {
		items[i] = playlistItem("Track", "", i+1, time.Minute)
	}
	fillTab(t, m, 0, items...)

	m.tabs[0].name = "Jazz"
	plain := ansi.Strip(strings.Join(m.playlistLines(40, 6), "\n"))
	if !strings.Contains(plain, "04.") || strings.Contains(plain, "05.") {
		t.Errorf("named tab rows = %q, want four visible items", plain)
	}

	m.tabs[0].name = ""
	plain = ansi.Strip(strings.Join(m.playlistLines(40, 6), "\n"))
	if !strings.Contains(plain, "05.") || strings.Contains(plain, "06.") {
		t.Errorf("unnamed tab rows = %q, want five visible items", plain)
	}
}

func TestLongTabNameTruncatesWidthSafe(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m.tabs[0].name = strings.Repeat("界", 30)

	lines := m.playlistLines(20, 4)
	if !strings.Contains(ansi.Strip(lines[1]), "…") {
		t.Errorf("long name was not truncated: %q", ansi.Strip(lines[1]))
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != 20 {
			t.Errorf("line %d width = %d, want 20", i, got)
		}
	}
}

func TestRestoreLoadsAndSanitizesTabNames(t *testing.T) {
	store := &fakeStore{
		active: 1,
		tabs: []queue.Snapshot{
			{Items: []queue.Item{playlistItem("Zero", "", 1, time.Minute)}, Current: 0},
			{Items: []queue.Item{playlistItem("One", "", 1, time.Minute)}, Current: 0},
		},
		names: []string{"  Jazz  ", "Rock\n"},
	}
	m, _ := newPlaylistModel(t, func(opts *Options) { opts.Store = store })
	m = runCmd(t, m, m.restoreCmd())

	if got := m.tabs[0].name; got != "Jazz" {
		t.Errorf("tab 0 name = %q, want Jazz", got)
	}
	if got := m.tabs[1].name; got != "Rock" {
		t.Errorf("tab 1 name = %q, want Rock", got)
	}
	if got := m.tabs[2].name; got != "" {
		t.Errorf("tab 2 name = %q, want empty", got)
	}
	if m.activeTab != 1 {
		t.Errorf("activeTab = %d, want 1", m.activeTab)
	}
}

func TestNameTabPromptResizeKeepsInputWidthSafe(t *testing.T) {
	m, _ := newPlaylistModel(t, nil)
	m = resize(t, m, 60, 10)
	m = press(t, m, runeKey('T'))
	m = typeText(t, m, strings.Repeat("wide界", 20))

	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != 10 {
		t.Fatalf("line count = %d, want 10", len(lines))
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != 60 {
			t.Errorf("line %d width = %d, want 60: %q", i, got, ansi.Strip(line))
		}
	}
}
