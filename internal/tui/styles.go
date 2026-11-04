package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/config"
)

// Built-in colors for roles that are not configurable. Values are ANSI 256
// indexes, matching the config color surface.
const (
	defaultAccent  = "39"
	defaultDir     = "33"
	defaultCue     = "141"
	defaultPlaying = "46"
	defaultError   = "196"

	defaultAudio    = "250"
	defaultPlaylist = "81"
)

// Styles holds every visual role used by the TUI. Only the five config colors
// are user-configurable; the remaining roles use built-in defaults.
type Styles struct {
	Base      lipgloss.Style
	Dim       lipgloss.Style
	Accent    lipgloss.Style
	Dir       lipgloss.Style
	Audio     lipgloss.Style
	Cue       lipgloss.Style
	Playlist  lipgloss.Style
	Invalid   lipgloss.Style
	Playing   lipgloss.Style
	Selection lipgloss.Style
	PaneFocus lipgloss.Style
	Error     lipgloss.Style
	Progress  lipgloss.Style
	Header    lipgloss.Style
	Status    lipgloss.Style
}

// NewStyles builds the style set, falling back to built-in colors when a
// configured value is empty.
func NewStyles(colors config.Colors) Styles {
	accent := colorValue(colors.Accent, defaultAccent)
	dir := colorValue(colors.Dir, defaultDir)
	cue := colorValue(colors.Cue, defaultCue)
	playing := colorValue(colors.Playing, defaultPlaying)
	invalid := colorValue(colors.Error, defaultError)

	return Styles{
		Base:      lipgloss.NewStyle(),
		Dim:       lipgloss.NewStyle().Faint(true),
		Accent:    lipgloss.NewStyle().Foreground(accent),
		Dir:       lipgloss.NewStyle().Foreground(dir),
		Audio:     lipgloss.NewStyle().Foreground(lipgloss.Color(defaultAudio)),
		Cue:       lipgloss.NewStyle().Foreground(cue),
		Playlist:  lipgloss.NewStyle().Foreground(lipgloss.Color(defaultPlaylist)),
		Invalid:   lipgloss.NewStyle().Foreground(invalid),
		Playing:   lipgloss.NewStyle().Foreground(playing).Bold(true),
		Selection: lipgloss.NewStyle().Reverse(true),
		PaneFocus: lipgloss.NewStyle().Foreground(accent).Bold(true),
		Error:     lipgloss.NewStyle().Foreground(invalid).Bold(true),
		Progress:  lipgloss.NewStyle().Foreground(accent),
		Header:    lipgloss.NewStyle().Bold(true),
		Status:    lipgloss.NewStyle().Faint(true),
	}
}

// PaneTitle returns the style for a pane title.
func (s Styles) PaneTitle(focused bool) lipgloss.Style {
	if focused {
		return s.PaneFocus
	}
	return s.Dim
}

// Row returns the style for a list row, applying the selection style last.
func (s Styles) Row(selected bool) lipgloss.Style {
	if selected {
		return s.Selection
	}
	return s.Base
}

// colorValue falls back to fallback when value is empty or whitespace.
func colorValue(value, fallback string) color.Color {
	if strings.TrimSpace(value) == "" {
		value = fallback
	}
	return lipgloss.Color(value)
}

// fitPlain truncates plain text to width cells and pads it with spaces. It is
// the single place layout rows are made width-safe; styles are applied after.
func fitPlain(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = oneLine(text)
	text = ansi.Truncate(text, width, "…")
	if padding := width - ansi.StringWidth(text); padding > 0 {
		text += strings.Repeat(" ", padding)
	}
	return text
}

// truncatePlain clamps plain text to width cells, adding an ellipsis.
func truncatePlain(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(oneLine(text), width, "…")
}

// truncateLeft keeps the tail of text and prefixes an ellipsis, which is the
// right behavior for filesystem paths.
func truncateLeft(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = oneLine(text)
	if ansi.StringWidth(text) <= width {
		return text
	}

	const ellipsis = "…"
	ellipsisWidth := ansi.StringWidth(ellipsis)
	if width <= ellipsisWidth {
		return ansi.Truncate(ellipsis, width, "")
	}

	cut := ansi.StringWidth(text) - (width - ellipsisWidth)
	return ansi.TruncateLeft(text, cut, ellipsis)
}

// oneLine makes text safe for a fixed row: ANSI escapes and control
// characters are removed so a hostile filename cannot restyle or break the
// layout. Visible Unicode (including wide characters) is preserved.
func oneLine(text string) string {
	text = ansi.Strip(text)
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
}
