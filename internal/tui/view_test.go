package tui

import (
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
		"Status: ready",
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
	for _, want := range []string{"Keys", "Quit", "Switch pane", "Save playlist", "Go to playing"} {
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

func TestTickRefreshesPlayerStatus(t *testing.T) {
	fp := newFakePlayer()
	fp.status = player.Status{State: player.StatePlaying, Position: 12 * time.Second, Volume: 55}
	m := newTestModel(t, func(opts *Options) { opts.Player = fp })

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
			if line == ansi.Strip(line) {
				t.Error("Files: title was not styled with the configured accent")
			}
			return
		}
	}
	t.Fatal("Files: title not found")
}
