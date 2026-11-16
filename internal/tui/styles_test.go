package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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
	if got := styles.Accent.Render("x"); !strings.Contains(got, "38;5;201") {
		t.Errorf("accent render = %q, want ANSI 256 color 201", got)
	}

	// Bg and Fg exist as explicit roles and default to the terminal's own
	// colors (no escape sequences injected).
	if got := styles.Bg.Render("x"); got != "x" {
		t.Errorf("default Bg render = %q, want plain x", got)
	}
	if got := styles.Fg.Render("x"); got != "x" {
		t.Errorf("default Fg render = %q, want plain x", got)
	}
	if got := styles.ScreenBackground(); got != nil {
		t.Errorf("default ScreenBackground() = %v, want nil", got)
	}
}

func TestNewStylesUsesConfiguredBackground(t *testing.T) {
	styles := NewStyles(config.Colors{Background: "234"})

	if got := styles.Bg.Render("x"); !strings.Contains(got, "48;5;234") {
		t.Errorf("Bg render = %q, want ANSI 256 background 234", got)
	}
	if got, want := styles.ScreenBackground(), lipgloss.Color("234"); got != want {
		t.Errorf("ScreenBackground() = %v, want %v", got, want)
	}
}

func TestNewStylesUsesConfiguredElementColors(t *testing.T) {
	styles := NewStyles(config.Colors{
		Accent:    "39",
		Header:    "201",
		PaneTitle: "202",
		Hints:     "203",
		Progress:  "204",
	})

	cases := []struct {
		name  string
		style lipgloss.Style
		want  string
	}{
		{"header", styles.Header, "38;5;201"},
		{"pane title", styles.PaneTitle(true), "38;5;202"},
		{"hints", styles.Hints, "38;5;203"},
		{"progress", styles.Progress, "38;5;204"},
	}
	for _, tc := range cases {
		if got := tc.style.Render("x"); !strings.Contains(got, tc.want) {
			t.Errorf("%s render = %q, want %q", tc.name, got, tc.want)
		}
	}

	if !styles.Header.GetBold() {
		t.Error("header style is not bold")
	}
	if !styles.Hints.GetFaint() {
		t.Error("hints style is not faint")
	}
	if !styles.PaneTitle(true).GetBold() {
		t.Error("focused pane title is not bold")
	}
	if styles.PaneTitle(false).Render("x") != styles.Dim.Render("x") {
		t.Error("unfocused pane title does not use the dim style")
	}
}

func TestNewStylesElementColorsFallBack(t *testing.T) {
	styles := NewStyles(config.Colors{})

	if got := styles.Header.Render("x"); strings.Contains(got, "38;5;") {
		t.Errorf("default header render = %q, want no foreground color", got)
	}
	if got := styles.Hints.Render("x"); strings.Contains(got, "38;5;") {
		t.Errorf("default hints render = %q, want no foreground color", got)
	}
	if got := styles.PaneTitle(true).Render("x"); !strings.Contains(got, "38;5;39") {
		t.Errorf("default focused pane title = %q, want accent 39", got)
	}
	if got := styles.Progress.Render("x"); !strings.Contains(got, "38;5;39") {
		t.Errorf("default progress = %q, want accent 39", got)
	}
}

func TestNewStylesInvalidElementColorsFallBack(t *testing.T) {
	styles := NewStyles(config.Colors{
		Header:    "not-a-color",
		PaneTitle: "not-a-color",
		Hints:     "not-a-color",
		Progress:  "not-a-color",
	})

	if got := styles.Header.Render("x"); strings.Contains(got, "38;5;") {
		t.Errorf("invalid header render = %q, want no foreground color", got)
	}
	if got := styles.Hints.Render("x"); strings.Contains(got, "38;5;") {
		t.Errorf("invalid hints render = %q, want no foreground color", got)
	}
	if got := styles.PaneTitle(true).Render("x"); !strings.Contains(got, "38;5;39") {
		t.Errorf("invalid focused pane title = %q, want accent 39", got)
	}
	if got := styles.Progress.Render("x"); !strings.Contains(got, "38;5;39") {
		t.Errorf("invalid progress = %q, want accent 39", got)
	}
}

func TestNewStylesUsesConfiguredSelectionBackground(t *testing.T) {
	styles := NewStyles(config.Colors{SelectionBackground: "235"})

	if got := styles.Selection.Render("x"); !strings.Contains(got, "48;5;235") {
		t.Errorf("selection render = %q, want ANSI 256 background 235", got)
	}
	if styles.Selection.GetReverse() {
		t.Error("configured selection still uses reverse video")
	}
}

func TestNewStylesUsesConfiguredStatusAndTabNameColors(t *testing.T) {
	styles := NewStyles(config.Colors{Status: "250", TabName: "214"})

	if got := styles.Status.Render("x"); !strings.Contains(got, "38;5;250") {
		t.Errorf("status render = %q, want ANSI 256 color 250", got)
	}
	if !styles.Status.GetFaint() {
		t.Error("status style is not faint")
	}
	if got := styles.TabName.Render("x"); !strings.Contains(got, "38;5;214") {
		t.Errorf("tab name render = %q, want ANSI 256 color 214", got)
	}
	if !styles.TabName.GetBold() {
		t.Error("tab name style is not bold")
	}
}

func TestNewStylesRowColorsFallBack(t *testing.T) {
	styles := NewStyles(config.Colors{})

	if !styles.Selection.GetReverse() {
		t.Error("default selection does not use reverse video")
	}
	if got := styles.Status.Render("x"); strings.Contains(got, "38;5;") {
		t.Errorf("default status render = %q, want no foreground color", got)
	}
	if got := styles.TabName.Render("x"); !strings.Contains(got, "38;5;39") {
		t.Errorf("default tab name = %q, want accent 39", got)
	}
}

func TestNewStylesInvalidRowColorsFallBack(t *testing.T) {
	styles := NewStyles(config.Colors{
		SelectionBackground: "not-a-color",
		Status:              "not-a-color",
		TabName:             "not-a-color",
	})

	if !styles.Selection.GetReverse() {
		t.Error("invalid selection background did not fall back to reverse video")
	}
	if got := styles.Status.Render("x"); strings.Contains(got, "38;5;") {
		t.Errorf("invalid status render = %q, want no foreground color", got)
	}
	if got := styles.TabName.Render("x"); !strings.Contains(got, "38;5;39") {
		t.Errorf("invalid tab name = %q, want accent 39", got)
	}
}

func TestNewStylesUsesConfiguredRemainingColors(t *testing.T) {
	styles := NewStyles(config.Colors{
		Foreground:          "250",
		Dim:                 "240",
		Audio:               "111",
		Playlist:            "112",
		Invalid:             "113",
		Focus:               "114",
		SelectionForeground: "115",
		SelectionBackground: "235",
	})

	cases := []struct {
		name  string
		style lipgloss.Style
		want  string
	}{
		{"foreground", styles.Fg, "38;5;250"},
		{"base", styles.Base, "38;5;250"},
		{"dim", styles.Dim, "38;5;240"},
		{"audio", styles.Audio, "38;5;111"},
		{"playlist", styles.Playlist, "38;5;112"},
		{"invalid", styles.Invalid, "38;5;113"},
		{"focus", styles.PaneFocus, "38;5;114"},
	}
	for _, tc := range cases {
		if got := tc.style.Render("x"); !strings.Contains(got, tc.want) {
			t.Errorf("%s render = %q, want %q", tc.name, got, tc.want)
		}
	}

	if !styles.Dim.GetFaint() {
		t.Error("dim style is not faint")
	}
	if !styles.PaneFocus.GetBold() {
		t.Error("focus style is not bold")
	}

	selection := styles.Selection.Render("x")
	if !strings.Contains(selection, "38;5;115") || !strings.Contains(selection, "48;5;235") {
		t.Errorf("selection render = %q, want foreground 115 and background 235", selection)
	}
	if styles.Selection.GetReverse() {
		t.Error("configured selection still uses reverse video")
	}
}

func TestNewStylesRemainingColorsFallBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		colors config.Colors
	}{
		{"empty", config.Colors{}},
		{"invalid", config.Colors{
			Foreground:          "not-a-color",
			Dim:                 "not-a-color",
			Audio:               "not-a-color",
			Playlist:            "not-a-color",
			Invalid:             "not-a-color",
			Focus:               "not-a-color",
			SelectionForeground: "not-a-color",
			SelectionBackground: "not-a-color",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			styles := NewStyles(tc.colors)

			if got := styles.Fg.Render("x"); got != "x" {
				t.Errorf("foreground render = %q, want plain x", got)
			}
			if got := styles.Dim.Render("x"); strings.Contains(got, "38;5;") {
				t.Errorf("dim render = %q, want no foreground color", got)
			}
			if got := styles.Audio.Render("x"); !strings.Contains(got, "38;5;250") {
				t.Errorf("audio render = %q, want built-in 250", got)
			}
			if got := styles.Playlist.Render("x"); !strings.Contains(got, "38;5;81") {
				t.Errorf("playlist render = %q, want built-in 81", got)
			}
			if got := styles.Invalid.Render("x"); !strings.Contains(got, "38;5;196") {
				t.Errorf("invalid render = %q, want error 196", got)
			}
			if got := styles.PaneFocus.Render("x"); !strings.Contains(got, "38;5;39") {
				t.Errorf("focus render = %q, want accent 39", got)
			}
			if !styles.Selection.GetReverse() {
				t.Error("selection does not use reverse video")
			}
		})
	}
}

func TestNewStylesInvalidBackgroundKeepsTerminalDefault(t *testing.T) {
	styles := NewStyles(config.Colors{Background: "not-a-color"})

	if got := styles.ScreenBackground(); got != nil {
		t.Errorf("ScreenBackground() = %v, want nil for an invalid color", got)
	}
	if got := styles.Bg.Render("x"); got != "x" {
		t.Errorf("Bg render = %q, want plain x for an invalid color", got)
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
