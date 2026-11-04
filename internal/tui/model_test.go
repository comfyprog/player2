package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/config"
)

func TestNewInitialisesModel(t *testing.T) {
	m := newTestModel(t, nil)

	if m.cfg.Volume != 70 {
		t.Errorf("cfg.Volume = %d, want 70", m.cfg.Volume)
	}
	if m.queue == nil {
		t.Fatal("queue is nil")
	}
	if m.statusMsg != "ready" {
		t.Errorf("statusMsg = %q, want ready", m.statusMsg)
	}
	if m.startupErr != nil {
		t.Errorf("startupErr = %v, want nil", m.startupErr)
	}
	if m.focus != PaneBrowser {
		t.Errorf("focus = %v, want browser", m.focus)
	}
	if m.width != 80 || m.height != 24 {
		t.Errorf("default size = %dx%d, want 80x24", m.width, m.height)
	}
	if m.Init() == nil {
		t.Error("Init returned nil command, want the first status tick")
	}
}

func TestTabSwitchesFocus(t *testing.T) {
	m := newTestModel(t, nil)

	m = press(t, m, specialKey(tea.KeyTab))
	if m.focus != PanePlaylist {
		t.Errorf("focus = %v after tab, want playlist", m.focus)
	}
	m = press(t, m, specialKey(tea.KeyTab))
	if m.focus != PaneBrowser {
		t.Errorf("focus = %v after second tab, want browser", m.focus)
	}
}

func TestConfiguredQuitKeyQuits(t *testing.T) {
	m := newTestModel(t, func(opts *Options) {
		opts.Config.Keys = map[string][]string{"quit": {"Q"}}
	})

	next, cmd := m.Update(runeKey('q'))
	if next.(*Model) != m {
		t.Error("unbound q changed the model")
	}
	if cmd != nil {
		t.Error("unbound q returned a command")
	}

	_, cmd = m.Update(runeKey('Q'))
	if cmd == nil {
		t.Fatal("configured Q returned no quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("configured Q command returned %T, want tea.QuitMsg", cmd())
	}
}

func TestNewWithConfigDefaultsWhenStartDirEmpty(t *testing.T) {
	m := newTestModel(t, func(opts *Options) {
		opts.StartDir = ""
		opts.Config = config.Default()
	})
	if m.startDir == "" {
		t.Error("startDir is empty, want config start_dir fallback")
	}
	if m.browserPath != m.startDir {
		t.Errorf("browserPath = %q, want startDir %q", m.browserPath, m.startDir)
	}
}

func TestNewAppliesConfiguredVolumeToPlayer(t *testing.T) {
	fp := newFakePlayer()
	m := newTestModel(t, func(opts *Options) {
		opts.Player = fp
		opts.Config.Volume = 42
	})
	if len(fp.volumes) != 1 || fp.volumes[0] != 42 {
		t.Fatalf("SetVolume calls = %v, want [42]", fp.volumes)
	}
	if got := m.status.Volume; got != 42 {
		t.Errorf("cached volume = %d, want 42", got)
	}

	m = resize(t, m, 100, 30)
	header := firstLine(ansi.Strip(m.View().Content))
	if !strings.Contains(header, "vol 42") {
		t.Errorf("header = %q, want configured volume", header)
	}
}
