package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"player2/internal/config"
	"player2/internal/player"
	"player2/internal/queue"
)

func TestViewSmoke(t *testing.T) {
	m := resize(t, newTestModel(t, nil), 100, 30)
	v := m.View()

	if !v.AltScreen {
		t.Error("View().AltScreen = false, want true")
	}
	if v.WindowTitle != "player2" {
		t.Errorf("View().WindowTitle = %q, want player2", v.WindowTitle)
	}

	plain := ansi.Strip(v.Content)
	wants := []string{
		"player2",
		"Files:",
		"Playlist",
		"Status: [FILENAME] ready",
		"░",
		"? help",
		"tab pane",
		"enter play",
		"a add",
		"space pause",
		"/ search",
		"q quit",
	}
	for _, want := range wants {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q\n%s", want, plain)
		}
	}

	lines := strings.Split(plain, "\n")
	if len(lines) != 30 {
		t.Errorf("view line count = %d, want 30", len(lines))
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != 100 {
			t.Errorf("line %d width = %d, want 100", i, got)
		}
	}
}

func TestViewResizeAndMinimumSize(t *testing.T) {
	m := newTestModel(t, nil)

	for _, size := range []struct{ width, height int }{
		{80, 24},
		{60, 10},
	} {
		m = resize(t, m, size.width, size.height)
		content := m.View().Content
		if strings.Contains(ansi.Strip(content), "terminal too small") {
			t.Errorf("%dx%d incorrectly reported too small", size.width, size.height)
		}
		lines := strings.Split(content, "\n")
		if len(lines) != size.height {
			t.Errorf("%dx%d line count = %d, want %d", size.width, size.height, len(lines), size.height)
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got != size.width {
				t.Errorf("%dx%d line %d width = %d, want %d", size.width, size.height, i, got, size.width)
			}
		}
	}

	for _, size := range []struct{ width, height int }{{59, 9}, {60, 9}} {
		m = resize(t, m, size.width, size.height)
		plain := ansi.Strip(m.View().Content)
		if !strings.Contains(plain, "terminal too small (minimum 60x10)") {
			t.Errorf("%dx%d missing too-small message: %q", size.width, size.height, plain)
		}
	}
}

func TestZoomShowsOnlyFocusedPane(t *testing.T) {
	m := resize(t, newTestModel(t, nil), 80, 24)

	m = press(t, m, runeKey('z'))
	content := m.View().Content
	plain := ansi.Strip(content)
	if !strings.Contains(plain, "Files:") {
		t.Errorf("zoomed browser view missing browser pane: %q", plain)
	}
	if strings.Contains(plain, "Playlist (") {
		t.Errorf("zoomed browser view still shows playlist pane: %q", plain)
	}
	for i, line := range strings.Split(content, "\n") {
		if width := ansi.StringWidth(line); width != 80 {
			t.Errorf("zoomed browser line %d width = %d, want 80", i, width)
		}
	}

	m = press(t, m, runeKey('z'))
	plain = ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "Files:") || !strings.Contains(plain, "Playlist (") {
		t.Errorf("unzoomed view missing a pane: %q", plain)
	}
}

func TestZoomPlaylistAndTabExitsZoom(t *testing.T) {
	m := resize(t, newTestModel(t, nil), 60, 10)

	m = press(t, m, specialKey(tea.KeyTab))
	m = press(t, m, runeKey('z'))
	content := m.View().Content
	plain := ansi.Strip(content)
	if strings.Contains(plain, "Files:") {
		t.Errorf("zoomed playlist view still shows browser pane: %q", plain)
	}
	if !strings.Contains(plain, "Playlist (") {
		t.Errorf("zoomed playlist view missing playlist pane: %q", plain)
	}
	for i, line := range strings.Split(content, "\n") {
		if width := ansi.StringWidth(line); width != 60 {
			t.Errorf("zoomed playlist line %d width = %d, want 60", i, width)
		}
	}

	m = press(t, m, specialKey(tea.KeyTab))
	if m.focus != PaneBrowser {
		t.Errorf("focus = %v after Tab from zoomed playlist, want browser", m.focus)
	}
	plain = ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "Files:") || !strings.Contains(plain, "Playlist (") {
		t.Errorf("Tab in zoomed mode did not restore both panes: %q", plain)
	}
}

func TestViewWideCharacterTruncation(t *testing.T) {
	m := newTestModel(t, func(opts *Options) {
		opts.StartDir = "/tmp/" + strings.Repeat("音楽ファイル", 20) + "/😀😀😀😀😀"
	})
	m.browserPath = m.startDir
	m = resize(t, m, 80, 24)

	content := m.View().Content
	lines := strings.Split(content, "\n")
	if len(lines) != 24 {
		t.Fatalf("line count = %d, want 24", len(lines))
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got > 80 {
			t.Errorf("line %d width = %d, want <= 80: %q", i, got, ansi.Strip(line))
		}
	}

	plain := ansi.Strip(content)
	if !strings.Contains(plain, "Files:") {
		t.Error("view missing Files: title")
	}
	if !strings.Contains(plain, "…") {
		t.Error("truncated wide path has no ellipsis")
	}
}

func TestViewHelpToggle(t *testing.T) {
	m := resize(t, newTestModel(t, nil), 100, 30)

	m = press(t, m, runeKey('?'))
	if !m.help {
		t.Fatal("help flag not set after ?")
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"Keys", "Quit", "Switch pane", "Zoom active pane", "Save playlist", "Go to playing"} {
		if !strings.Contains(plain, want) {
			t.Errorf("help overlay missing %q\n%s", want, plain)
		}
	}

	m = press(t, m, specialKey(tea.KeyEscape))
	if m.help {
		t.Fatal("help flag still set after Esc")
	}
	if strings.Contains(ansi.Strip(m.View().Content), "Esc closes") {
		t.Error("help overlay still rendered after Esc")
	}
}

func TestViewHelpShowsAllBindingsAtNormalSize(t *testing.T) {
	m := resize(t, newTestModel(t, nil), 100, 30)
	m = press(t, m, runeKey('?'))

	plain := ansi.Strip(m.View().Content)
	bindings := m.keys.Bindings()
	if len(bindings) != 34 {
		t.Fatalf("bindings = %d, want 34", len(bindings))
	}
	for _, binding := range bindings {
		if !strings.Contains(plain, binding.Description) {
			t.Errorf("help overlay missing action %q (%s)", binding.Action, binding.Description)
		}
	}
	if !strings.Contains(plain, "Keys 1-17/17") {
		t.Errorf("help title lacks full-range indicator: %q", firstLine(plain))
	}
}

func TestViewHelpScrollsToLastBindingAtMinimumSize(t *testing.T) {
	m := resize(t, newTestModel(t, nil), 60, 10)
	m = press(t, m, runeKey('?'))

	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "▼ more") {
		t.Errorf("help overlay has no more indicator at 60x10: %q", plain)
	}
	if strings.Contains(plain, "Go to playing") {
		t.Fatal("last binding visible before scrolling at 60x10")
	}

	// The overlay keeps the viewport in range and reaches the final row.
	for range 20 {
		m = press(t, m, specialKey(tea.KeyDown))
	}
	plain = ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "Go to playing") {
		t.Errorf("last binding unreachable after scrolling: %q", plain)
	}
	if !strings.Contains(plain, "Keys 12-17/17") {
		t.Errorf("help title does not show the scrolled position: %q", firstLine(plain))
	}
	for i, line := range strings.Split(m.View().Content, "\n") {
		if width := ansi.StringWidth(line); width != 60 {
			t.Errorf("help line %d width = %d, want 60", i, width)
		}
	}
}

func TestHelpOverlayIsModal(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, func(opts *Options) { opts.Player = fp })
	m = resize(t, m, 100, 30)
	m = press(t, m, runeKey('?'))

	m = press(t, m, specialKey(tea.KeyTab))
	if m.focus != PaneBrowser {
		t.Error("tab switched focus underneath the help overlay")
	}
	if !m.help {
		t.Fatal("help closed unexpectedly")
	}

	// Transport actions are swallowed while the overlay is open.
	m = press(t, m, specialKey(tea.KeySpace))
	if fp.pauses != 0 {
		t.Error("space toggled pause underneath the help overlay")
	}
	if !m.help {
		t.Fatal("help closed on space")
	}

	// Up/down scroll instead of moving a pane selection.
	m = press(t, m, specialKey(tea.KeyDown))
	if !m.help {
		t.Fatal("help closed on down")
	}
}

func TestEscWithoutHelpIsNotConsumedGlobally(t *testing.T) {
	m := newTestModel(t, func(opts *Options) {
		opts.Config.Keys = map[string][]string{"quit": {"esc"}}
	})

	_, cmd := m.Update(specialKey(tea.KeyEscape))
	if cmd == nil {
		t.Fatal("Esc bound to quit returned no command when help is closed")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Esc command = %T, want tea.QuitMsg", cmd())
	}
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

func TestViewHelpShowsConfiguredKeys(t *testing.T) {
	m := newTestModel(t, func(opts *Options) {
		opts.Config.Keys = map[string][]string{
			"help":        {"F1"},
			"switch_pane": {"ctrl+t"},
		}
	})
	m = resize(t, m, 100, 30)
	m.help = true

	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"F1", "ctrl+t", "Quit", "Switch pane"} {
		if !strings.Contains(plain, want) {
			t.Errorf("help overlay missing configured label %q\n%s", want, plain)
		}
	}
}

func TestStatusMessageExpiresOnTick(t *testing.T) {
	m := newTestModel(t, nil)
	m.setStatus("saved", -time.Second)
	if m.statusMsg != "saved" {
		t.Fatalf("statusMsg = %q, want saved", m.statusMsg)
	}

	next, cmd := m.Update(tickMsg(time.Now()))
	if cmd == nil {
		t.Error("tick update returned no follow-up command")
	}
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	if updated.statusMsg != "" {
		t.Errorf("statusMsg = %q after expiry, want empty", updated.statusMsg)
	}
}

func TestStatusMessageExpiresAfterPositiveDeadline(t *testing.T) {
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	m := newTestModel(t, func(opts *Options) {})
	m.now = func() time.Time { return base }
	m.setStatus("saved", 2*time.Second)
	if got, want := m.statusExpiry, base.Add(2*time.Second); !got.Equal(want) {
		t.Fatalf("statusExpiry = %v, want %v", got, want)
	}

	// Before the deadline the message survives a tick.
	m.now = func() time.Time { return base.Add(time.Second) }
	next, _ := m.Update(tickMsg(base.Add(time.Second)))
	if got := next.(*Model).statusMsg; got != "saved" {
		t.Errorf("statusMsg = %q before deadline, want saved", got)
	}

	// After the positive timeout elapses the next tick clears it.
	m.now = func() time.Time { return base.Add(3 * time.Second) }
	next, _ = m.Update(tickMsg(base.Add(3 * time.Second)))
	if got := next.(*Model).statusMsg; got != "" {
		t.Errorf("statusMsg = %q after positive deadline, want empty", got)
	}
}

func TestTickRefreshesPlayerStatus(t *testing.T) {
	fp := newFakePlayer()
	fp.status = player.Status{State: player.StatePlaying, Position: 12 * time.Second, Volume: 55}
	m := newTestModel(t, func(opts *Options) {
		opts.Player = fp
		opts.Config.Volume = 55
	})

	next, _ := m.Update(tickMsg(time.Now()))
	updated := next.(*Model)
	if got := updated.status.State; got != player.StatePlaying {
		t.Errorf("cached state = %v, want playing", got)
	}
	if got := updated.status.Position; got != 12*time.Second {
		t.Errorf("cached position = %v, want 12s", got)
	}
	if got := updated.status.Volume; got != 55 {
		t.Errorf("cached volume = %d, want 55", got)
	}
}

func TestViewWithNilPlayer(t *testing.T) {
	m := newTestModel(t, func(opts *Options) { opts.Player = nil })
	m = resize(t, m, 80, 24)

	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"player2", "stopped", "Status:", "? help"} {
		if !strings.Contains(plain, want) {
			t.Errorf("view with nil player missing %q\n%s", want, plain)
		}
	}
}

func TestViewHeaderWhileStopped(t *testing.T) {
	m := newTestModel(t, nil)
	m.queue.Append(queue.Item{Title: "Song", Artist: "Artist", Duration: 4 * time.Minute})
	m = resize(t, m, 100, 30)

	header := strings.Split(ansi.Strip(m.View().Content), "\n")[0]
	if !strings.Contains(header, "stopped") {
		t.Errorf("header = %q, want stopped while player is stopped", header)
	}
}

func TestProgressBarScalesWithPlayback(t *testing.T) {
	m := newTestModel(t, nil)
	m.queue.Append(queue.Item{Title: "Song", Duration: 4 * time.Minute})
	m = resize(t, m, 100, 30)

	// Stopped: the bar stays empty even if the cached position is mid-track.
	m.status = player.Status{State: player.StateStopped, Position: 2 * time.Minute, Volume: 70}
	if content := ansi.Strip(m.View().Content); strings.Contains(content, "█") {
		t.Error("stopped view has a filled progress bar")
	}

	// Halfway through a four-minute track fills roughly half the bar.
	m.status = player.Status{State: player.StatePlaying, Position: 2 * time.Minute, Volume: 70}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "█") || !strings.Contains(content, "░") {
		t.Error("playing view has no partly filled progress bar")
	}

	// Unknown duration: no fill.
	m.queue.Clear()
	m.queue.Append(queue.Item{Title: "Stream"})
	m.status = player.Status{State: player.StatePlaying, Position: 30 * time.Second, Volume: 70}
	content = ansi.Strip(m.View().Content)
	if strings.Contains(content, "█") {
		t.Error("unknown-duration view has a filled progress bar")
	}
}

func TestViewHeaderWhilePlayingAndPaused(t *testing.T) {
	m := newTestModel(t, nil)
	m.queue.Append(queue.Item{Title: "Song", Artist: "Artist", Duration: 4*time.Minute + 12*time.Second})
	m = resize(t, m, 100, 30)

	m.status = player.Status{State: player.StatePlaying, Position: time.Minute + 23*time.Second, Volume: 70}
	header := firstLine(ansi.Strip(m.View().Content))
	for _, want := range []string{"▶ Song — Artist", "1:23 / 4:12", "vol 70", "R:off", "S:off"} {
		if !strings.Contains(header, want) {
			t.Errorf("playing header %q missing %q", header, want)
		}
	}

	m.status.State = player.StatePaused
	header = firstLine(ansi.Strip(m.View().Content))
	if !strings.Contains(header, "⏸ Song — Artist") {
		t.Errorf("paused header = %q, want paused marker", header)
	}
}

func TestHomeAbbreviationAndLeftTruncation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got, want := abbreviateHome(filepath.Join(home, "Music")), "~/Music"; got != want {
		t.Errorf("abbreviateHome = %q, want %q", got, want)
	}
	if got := abbreviateHome(home); got != "~" {
		t.Errorf("abbreviateHome(home) = %q, want ~", got)
	}

	long := filepath.Join(home, "Music", "Artist", "Album", "Track.flac")
	got := truncateLeft(long, 20)
	if !strings.HasPrefix(got, "…") {
		t.Errorf("truncateLeft = %q, want leading ellipsis", got)
	}
	if !strings.HasSuffix(got, "Track.flac") {
		t.Errorf("truncateLeft = %q, want to keep the path tail", got)
	}
	if width := ansi.StringWidth(got); width > 20 {
		t.Errorf("truncateLeft width = %d, want <= 20", width)
	}
}

func TestViewSanitizesControlCharacters(t *testing.T) {
	m := newTestModel(t, func(opts *Options) {
		opts.StartDir = "/tmp/\x1b[31mred\nname\x07"
	})
	m.browserPath = m.startDir
	m = resize(t, m, 80, 24)

	content := m.View().Content
	lines := strings.Split(content, "\n")
	if len(lines) != 24 {
		t.Fatalf("control characters changed line count to %d", len(lines))
	}
	if strings.Contains(content, "\x1b[31m") {
		t.Error("injected ANSI color survived sanitization")
	}
	if strings.Contains(content, "\x07") {
		t.Error("control character survived sanitization")
	}
	plain := ansi.Strip(content)
	if !strings.Contains(plain, "red name") {
		t.Errorf("sanitized path = %q, want red name", plain)
	}
	for i, line := range lines {
		if width := ansi.StringWidth(line); width != 80 {
			t.Errorf("line %d width = %d, want 80", i, width)
		}
	}
}

func TestViewStartupError(t *testing.T) {
	opts := baseOptions()
	opts.Config.Keys = map[string][]string{"help": {"q"}}

	if err := Validate(opts); err == nil {
		t.Fatal("Validate accepted duplicate key binding")
	} else if !strings.Contains(err.Error(), `"q"`) {
		t.Errorf("Validate error %q does not mention the offending key", err)
	}

	m := New(opts)
	m = resize(t, m, 80, 24)
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "duplicate") || !strings.Contains(plain, "q") {
		t.Errorf("startup error view = %q, want duplicate-key error", plain)
	}
}

func TestViewAccentTitleFromConfig(t *testing.T) {
	m := newTestModel(t, func(opts *Options) {
		opts.Config.Colors = config.Colors{
			Accent: "201", Dir: "33", Cue: "141", Playing: "46", Error: "196",
		}
	})
	m = resize(t, m, 100, 30)

	content := m.View().Content
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(ansi.Strip(line), "Files:") {
			if !strings.Contains(line, "38;5;201") {
				t.Errorf("Files: title lacks configured accent code: %q", line)
			}
			return
		}
	}
	t.Fatal("Files: title not found")
}
