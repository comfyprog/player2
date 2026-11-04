package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/player"
	"player2/internal/queue"
)

const (
	// progressWidth is the number of cells in the footer progress bar.
	progressWidth = 20

	// header markers.
	playingMarker = "▶"
	pausedMarker  = "⏸"
)

// View renders the shell as a Bubble Tea view.
func (m *Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "player2"
	return view
}

// render builds the full fixed-size screen.
func (m *Model) render() string {
	if m.startupErr != nil {
		return m.styles.Error.Render(fitPlain("player2: "+m.startupErr.Error(), max(m.width, 1)))
	}
	if m.width < minWidth || m.height < minHeight {
		message := fmt.Sprintf("terminal too small (minimum %dx%d)", minWidth, minHeight)
		return m.styles.Error.Render(fitPlain(message, max(m.width, 1)))
	}

	bodyHeight := m.height - headerRows - footerRows
	lines := make([]string, 0, m.height)
	lines = append(lines, m.headerLine())
	if m.help {
		lines = append(lines, m.helpLines(bodyHeight)...)
	} else {
		lines = append(lines, m.bodyLines(bodyHeight)...)
	}
	lines = append(lines, m.statusLine(), m.hintsLine())

	return strings.Join(lines, "\n")
}

// bodyLines joins the two panes with a dim divider.
func (m *Model) bodyLines(height int) []string {
	if height <= 0 {
		return nil
	}

	leftWidth := (m.width - 1) / 2
	rightWidth := m.width - leftWidth - 1
	left := m.paneLines(PaneBrowser, leftWidth, height)
	right := m.paneLines(PanePlaylist, rightWidth, height)
	divider := m.styles.Dim.Render("│")

	lines := make([]string, height)
	for i := range lines {
		lines[i] = left[i] + divider + right[i]
	}
	return lines
}

// paneLines renders one pane's title and content.
func (m *Model) paneLines(pane Pane, width, height int) []string {
	switch pane {
	case PaneBrowser:
		return m.browserLines(width, height)
	case PanePlaylist:
		return m.playlistLines(width, height)
	default:
		return nil
	}
}

// paneTitle returns the plain title for a pane, fitted to width.
func (m *Model) paneTitle(pane Pane, width int) string {
	switch pane {
	case PanePlaylist:
		return truncatePlain(fmt.Sprintf("Playlist (%d)", len(m.queue.Snapshot().Items)), width)
	default:
		return m.browserTitle(width)
	}
}

// headerLine renders the now-playing header.
func (m *Model) headerLine() string {
	parts := []string{"player2"}

	status := m.status
	if status.State == player.StatePlaying || status.State == player.StatePaused {
		marker := playingMarker
		if status.State == player.StatePaused {
			marker = pausedMarker
		}
		parts = append(parts, marker+" "+m.nowPlayingLabel())
		parts = append(parts, formatDuration(status.Position)+" / "+m.durationLabel())
	} else {
		parts = append(parts, "stopped")
	}

	parts = append(parts, "vol "+strconv.Itoa(m.volume()))
	parts = append(parts, "R:"+m.repeatLabel(), "S:"+m.shuffleLabel())

	plain := strings.Join(parts, " ─ ")
	return m.styles.Header.Render(fitPlain(plain, m.width))
}

// nowPlayingLabel describes the current queue item.
func (m *Model) nowPlayingLabel() string {
	item, ok := m.queue.Current()
	if !ok {
		return "nothing playing"
	}

	title := item.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(item.Path), filepath.Ext(item.Path))
	}
	if title == "" {
		title = "unknown"
	}
	if item.Artist != "" {
		return title + " — " + item.Artist
	}
	return title
}

// durationLabel formats the current item's duration.
func (m *Model) durationLabel() string {
	if item, ok := m.queue.Current(); ok && item.Duration > 0 {
		return formatDuration(item.Duration)
	}
	return "--:--"
}

// volume returns the volume to display.
func (m *Model) volume() int {
	if m.player != nil || m.status.Volume != 0 {
		return m.status.Volume
	}
	return m.cfg.Volume
}

// repeatLabel renders the queue repeat mode compactly.
func (m *Model) repeatLabel() string {
	switch m.queue.Snapshot().Repeat {
	case queue.RepeatAll:
		return "all"
	case queue.RepeatOne:
		return "one"
	default:
		return "off"
	}
}

// shuffleLabel renders the queue shuffle state compactly.
func (m *Model) shuffleLabel() string {
	if m.queue.Snapshot().Shuffle {
		return "on"
	}
	return "off"
}

// statusLine lays out the status message and progress bar on one row.
func (m *Model) statusLine() string {
	left := "Status: " + m.statusMsg
	rightPlain, rightStyled := m.progress()
	rightWidth := ansi.StringWidth(rightPlain)

	leftStyle := m.styles.Status
	if m.statusIsError {
		leftStyle = m.styles.Error
	}

	if rightWidth >= m.width {
		return leftStyle.Render(fitPlain(left, m.width))
	}

	leftWidth := m.width - rightWidth - 1
	if rightWidth == 0 {
		leftWidth = m.width
	}

	left = fitPlain(left, leftWidth)
	gap := m.width - leftWidth - rightWidth
	if gap < 0 {
		gap = 0
	}
	return leftStyle.Render(left) + strings.Repeat(" ", gap) + rightStyled
}

// progress returns the plain and styled progress indicator. The fill is zero
// while stopped or when the duration is unknown.
func (m *Model) progress() (plain, styled string) {
	position := m.status.Position
	if position < 0 {
		position = 0
	}

	duration := time.Duration(0)
	if item, ok := m.queue.Current(); ok {
		duration = item.Duration
	}

	filled := 0
	playing := m.status.State == player.StatePlaying || m.status.State == player.StatePaused
	if playing && duration > 0 {
		ratio := float64(position) / float64(duration)
		switch {
		case ratio <= 0:
			filled = 0
		case ratio >= 1:
			filled = progressWidth
		default:
			filled = int(ratio*progressWidth + 0.5)
		}
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", progressWidth-filled)
	times := " 0:00 / --:--"
	if duration > 0 {
		displayPosition := time.Duration(0)
		if playing {
			displayPosition = position
			if displayPosition > duration {
				displayPosition = duration
			}
		}
		times = " " + formatDuration(displayPosition) + " / " + formatDuration(duration)
	}

	plain = "[" + bar + "]" + times
	styled = m.styles.Progress.Render("["+bar+"]") + m.styles.Dim.Render(times)
	return plain, styled
}

// hintsLine renders a compact key hint row from the active keymap.
func (m *Model) hintsLine() string {
	hints := []struct {
		action Action
		label  string
	}{
		{ActionHelp, "help"},
		{ActionSwitchPane, "pane"},
		{ActionOpen, "play"},
		{ActionAdd, "add"},
		{ActionPause, "pause"},
		{ActionSearch, "search"},
		{ActionQuit, "quit"},
	}

	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		keys := m.keys.Keys(hint.action)
		if len(keys) == 0 {
			continue
		}
		parts = append(parts, keys[0]+" "+hint.label)
	}

	plain := fitPlain(strings.Join(parts, "  "), m.width)
	return m.styles.Dim.Render(plain)
}

// helpLines renders the two-column keybinding overlay. The overlay scrolls by
// whole rows when all bindings do not fit, with a position indicator.
func (m *Model) helpLines(height int) []string {
	m.clampHelpOffset()

	if height <= 0 {
		return nil
	}

	rowsTotal := m.helpRows()
	visible := min(m.helpVisibleRows(), rowsTotal)
	start := m.helpOffset
	end := min(start+visible, rowsTotal)

	title := fmt.Sprintf("Keys %d-%d/%d", start+1, end, rowsTotal)
	if end < rowsTotal {
		title += " ▼ more"
	}
	title += " — Esc closes"
	lines := []string{m.styles.PaneFocus.Render(fitPlain(title, m.width))}
	if height == 1 {
		return lines
	}

	leftWidth := (m.width - 1) / 2
	rightWidth := m.width - leftWidth - 1
	divider := m.styles.Dim.Render("│")

	bindings := m.keys.Bindings()
	for i := start; i < end && len(lines) < height; i++ {
		left := helpEntry(bindings[i])
		right := ""
		if j := i + rowsTotal; j < len(bindings) {
			right = helpEntry(bindings[j])
		}
		lines = append(lines,
			m.styles.Fg.Render(fitPlain(left, leftWidth))+
				divider+
				m.styles.Fg.Render(fitPlain(right, rightWidth)),
		)
	}

	blank := strings.Repeat(" ", m.width)
	for len(lines) < height {
		lines = append(lines, blank)
	}
	return lines[:height]
}

// helpEntry formats one binding row without styling.
func helpEntry(binding Binding) string {
	keys := strings.Join(binding.Keys, ", ")
	if keys == "" {
		keys = "(unbound)"
	}
	return keys + "  " + binding.Description
}

// nowPlaying reports whether item is the queue's current item. Task 12 uses it
// to draw the marker and playing style.
func (m *Model) nowPlaying(itemID uint64) bool {
	current, ok := m.queue.Current()
	return ok && current.ID == itemID
}

// abbreviateHome replaces a leading home directory with "~".
func abbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	prefix := home + string(os.PathSeparator)
	if strings.HasPrefix(path, prefix) {
		return "~" + path[len(home):]
	}
	return path
}

// formatDuration renders a duration as m:ss or h:mm:ss.
func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	totalSeconds := int(duration / time.Second)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}
