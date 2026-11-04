package tui

import (
	"strings"
	"testing"

	"player2/internal/config"

	"github.com/charmbracelet/x/ansi"
)

func TestNewStylesUsesConfiguredColors(t *testing.T) {
	styles := NewStyles(config.Colors{
		Accent:  "201",
		Dir:     "33",
		Cue:     "141",
		Playing: "46",
		Error:   "196",
	})

	cases := []struct {
		name  string
		style string
	}{
		{"accent", styles.Accent.Render("x")},
		{"dir", styles.Dir.Render("x")},
		{"cue", styles.Cue.Render("x")},
		{"playing", styles.Playing.Render("x")},
		{"error", styles.Error.Render("x")},
	}
	for _, tc := range cases {
		if tc.style == "x" {
			t.Errorf("%s style emitted no ANSI escape", tc.name)
		}
		if got := ansi.Strip(tc.style); got != "x" {
			t.Errorf("%s style stripped text = %q, want x", tc.name, got)
		}
	}

	if styles.Accent.Render("x") == styles.Dir.Render("x") {
		t.Error("accent and dir styles render identically despite different colors")
	}
}

func TestNewStylesEmptyColorsFallBack(t *testing.T) {
	styles := NewStyles(config.Colors{})
	if styles.Accent.Render("x") == "x" {
		t.Error("empty accent did not fall back to the built-in color")
	}
	if !strings.Contains(styles.Playing.Render("x"), "\x1b[") {
		t.Error("playing style has no ANSI styling")
	}
	if !styles.Selection.GetReverse() {
		t.Error("selection style is not reverse")
	}
	if !styles.PaneFocus.GetBold() {
		t.Error("pane focus style is not bold")
	}
}
