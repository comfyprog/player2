package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/library"
	"player2/internal/meta"
)

// infoMaxWidth bounds the track-info window on wide terminals.
const infoMaxWidth = 90

// infoState is the read-only track-info modal. It stays open across queue and
// player changes; only Esc/i close it.
type infoState struct {
	open     bool
	loading  bool
	path     string
	duration time.Duration
	info     meta.Info
	err      error
	offset   int
}

// trackInfoMsg carries a completed probe for one info request. seq guards
// against a late result for a window that was closed or reopened.
type trackInfoMsg struct {
	seq  uint64
	path string
	info meta.Info
	err  error
}

// infoOpen reports whether the track-info modal is capturing keys.
func (m *Model) infoOpen() bool { return m.info.open }

// openBrowserInfo opens info for the highlighted browser entry. Only plain
// audio files are eligible; CUE tracks are explicitly out of scope for now.
func (m *Model) openBrowserInfo() tea.Cmd {
	entry, ok := m.browserSelectedEntry()
	if !ok || entry.Kind != library.KindAudio {
		if ok && entry.Kind == library.KindCueTrack {
			m.setStatusOK("info: CUE tracks not supported yet")
		} else {
			m.setStatusOK("info: select an audio file")
		}
		return nil
	}
	if entry.Path == "" {
		m.setStatusOK("info: select an audio file")
		return nil
	}
	return m.openInfo(entry.Path, entry.Duration)
}

// openPlaylistInfo opens info for the highlighted queue item. CUE tracks are
// skipped for now.
func (m *Model) openPlaylistInfo() tea.Cmd {
	item, ok := m.selectedPlaylistItem()
	if !ok {
		m.setStatusOK("info: queue is empty")
		return nil
	}
	if item.CuePath != "" {
		m.setStatusOK("info: CUE tracks not supported yet")
		return nil
	}
	if item.Path == "" {
		m.setStatusOK("info: select an audio file")
		return nil
	}
	return m.openInfo(item.Path, item.Duration)
}

// openInfo shows the modal immediately and starts an asynchronous probe. The
// existing ffprobe cache makes repeated opens cheap.
func (m *Model) openInfo(path string, duration time.Duration) tea.Cmd {
	m.infoSeq++
	seq := m.infoSeq
	m.info = infoState{
		open:     true,
		loading:  m.prober != nil && path != "",
		path:     path,
		duration: duration,
	}
	if m.prober == nil || path == "" {
		return nil
	}

	prober, sem := m.prober, m.probeSem
	return func() tea.Msg {
		if sem != nil {
			sem <- struct{}{}
			defer func() { <-sem }()
		}
		info, err := prober.Probe(context.Background(), path)
		return trackInfoMsg{seq: seq, path: path, info: info, err: err}
	}
}

// applyTrackInfo installs a probe result unless the request is stale.
func (m *Model) applyTrackInfo(msg trackInfoMsg) tea.Cmd {
	if !m.info.open || msg.seq != m.infoSeq || msg.path != m.info.path {
		return nil
	}
	m.info.loading = false
	m.info.err = msg.err
	m.info.info = msg.info
	if msg.info.Duration > 0 {
		m.info.duration = msg.info.Duration
	}
	m.clampInfoOffset()
	return nil
}

// closeInfo dismisses the modal and invalidates any in-flight probe.
func (m *Model) closeInfo() {
	m.info = infoState{}
	m.infoSeq++
}

// handleInfoKey routes keys while the info modal is open. Only closing,
// quitting, and scrolling are accepted.
func (m *Model) handleInfoKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if key == "esc" || key == "escape" {
		m.closeInfo()
		return m, nil
	}

	action, ok := m.keys.Action(key)
	if !ok {
		return m, nil
	}
	switch action {
	case ActionInfo:
		m.closeInfo()
	case ActionQuit:
		return m, tea.Quit
	case ActionUp:
		m.scrollInfo(-1)
	case ActionDown:
		m.scrollInfo(1)
	case ActionPageUp:
		m.scrollInfo(-m.infoVisibleRows())
	case ActionPageDown:
		m.scrollInfo(m.infoVisibleRows())
	case ActionFirst:
		m.info.offset = 0
	case ActionLast:
		m.info.offset = m.infoMaxOffset()
	}
	return m, nil
}

// scrollInfo moves the content viewport by delta and clamps it.
func (m *Model) scrollInfo(delta int) {
	m.info.offset += delta
	m.clampInfoOffset()
}

// infoMaxOffset returns the greatest valid scroll offset.
func (m *Model) infoMaxOffset() int {
	bodyHeight := m.height - headerRows - footerRows
	if bodyHeight < 4 {
		return 0
	}
	total := len(m.infoContentLines(m.infoInnerWidth()))
	visible := min(total, bodyHeight-4)
	return max(total-visible, 0)
}

// infoVisibleRows returns how many content rows fit in the window body.
func (m *Model) infoVisibleRows() int {
	bodyHeight := m.height - headerRows - footerRows
	if bodyHeight < 4 {
		return 1
	}
	total := len(m.infoContentLines(m.infoInnerWidth()))
	return min(total, bodyHeight-4)
}

// clampInfoOffset keeps the scroll offset within the current content.
func (m *Model) clampInfoOffset() {
	bodyHeight := m.height - headerRows - footerRows
	if bodyHeight < 4 {
		m.info.offset = 0
		return
	}
	total := len(m.infoContentLines(m.infoInnerWidth()))
	visible := min(total, bodyHeight-4)
	m.clampInfoOffsetFor(total, visible)
}

// clampInfoOffsetFor clamps offset for a known content and viewport size.
func (m *Model) clampInfoOffsetFor(total, visible int) {
	maxOffset := max(total-visible, 0)
	if m.info.offset < 0 {
		m.info.offset = 0
	}
	if m.info.offset > maxOffset {
		m.info.offset = maxOffset
	}
}

// infoBoxWidth returns the outer width of the centered window.
func (m *Model) infoBoxWidth() int {
	width := min(m.width-4, infoMaxWidth)
	if width < 20 {
		width = m.width
	}
	return max(width, 2)
}

// infoInnerWidth returns the content width between the window borders.
func (m *Model) infoInnerWidth() int {
	return max(m.infoBoxWidth()-2, 1)
}

// infoLines renders the modal centered in the body. Every returned line is
// exactly m.width cells.
func (m *Model) infoLines(height int) []string {
	if height <= 0 {
		return nil
	}

	blank := strings.Repeat(" ", max(m.width, 0))
	lines := make([]string, height)
	for i := range lines {
		lines[i] = blank
	}
	if height < 4 {
		lines[0] = m.styles.PaneFocus.Render(fitPlain("Track info — Esc closes", m.width))
		return lines
	}

	content := m.infoContentLines(m.infoInnerWidth())
	boxWidth := m.infoBoxWidth()
	innerWidth := max(boxWidth-2, 1)
	boxHeight := min(len(content)+4, height)
	visible := boxHeight - 4
	m.clampInfoOffsetFor(len(content), visible)

	top := (height - boxHeight) / 2
	left := (m.width - boxWidth) / 2
	right := m.width - boxWidth - left
	padLeft := strings.Repeat(" ", max(left, 0))
	padRight := strings.Repeat(" ", max(right, 0))
	border := m.styles.Dim

	lines[top] = padLeft + border.Render("╭"+strings.Repeat("─", innerWidth)+"╮") + padRight

	title := m.infoTitle(len(content), visible)
	lines[top+1] = padLeft +
		border.Render("│") +
		m.styles.PaneFocus.Render(fitStyled(title, innerWidth)) +
		border.Render("│") +
		padRight

	lines[top+2] = padLeft + border.Render("├"+strings.Repeat("─", innerWidth)+"┤") + padRight

	for row := 0; row < visible; row++ {
		text := ""
		if index := m.info.offset + row; index < len(content) {
			text = content[index]
		}
		lines[top+3+row] = padLeft +
			border.Render("│") +
			m.styles.Fg.Render(fitPlain(text, innerWidth)) +
			border.Render("│") +
			padRight
	}

	lines[top+boxHeight-1] = padLeft + border.Render("╰"+strings.Repeat("─", innerWidth)+"╯") + padRight
	return lines
}

// infoTitle returns the window title, including a scroll position when the
// content does not fit.
func (m *Model) infoTitle(total, visible int) string {
	title := "Track info"
	if visible > 0 && total > visible {
		start := m.info.offset
		end := min(start+visible, total)
		title = fmt.Sprintf("Track info %d-%d/%d", start+1, end, total)
	}
	return title + " — Esc closes"
}

// infoContentLines builds the scrollable body text for the current width.
func (m *Model) infoContentLines(width int) []string {
	if width <= 0 {
		return nil
	}

	var lines []string
	add := func(label, value string) {
		lines = append(lines, wrapInfoField(label, value, width)...)
	}

	add("Name", filepath.Base(m.info.path))
	add("Path", m.info.path)
	if m.info.duration > 0 {
		add("Duration", formatDuration(m.info.duration))
	} else {
		add("Duration", "--:--")
	}

	if m.info.loading {
		lines = append(lines, "", "Loading metadata…")
		return lines
	}
	if m.info.err != nil {
		lines = append(lines, "")
		lines = append(lines, wrapInfoField("Details unavailable", m.info.err.Error(), width)...)
		return lines
	}

	info := m.info.info
	if info.Container != "" {
		add("Container", info.Container)
	}
	if info.Codec != "" {
		add("Codec", info.Codec)
	}
	if info.BitRate > 0 {
		add("Bitrate", formatBitrate(info.BitRate))
	}
	if info.SampleRate > 0 {
		add("Sample rate", strconv.Itoa(info.SampleRate)+" Hz")
	}
	if info.Channels > 0 {
		add("Channels", formatChannels(info.Channels, info.ChannelLayout))
	}
	if info.BitDepth > 0 {
		add("Bit depth", strconv.Itoa(info.BitDepth)+"-bit")
	}

	lines = append(lines, "", "Tags:")
	names := make([]string, 0, len(info.Tags))
	for name, value := range info.Tags {
		if name != "" && strings.TrimSpace(value) != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		lines = append(lines, "  (none)")
		return lines
	}
	sort.Strings(names)
	for _, name := range names {
		lines = append(lines, wrapInfoField("  "+displayTagName(name), info.Tags[name], width)...)
	}
	return lines
}

// wrapInfoField formats "label: value", wrapping long values and indenting
// continuation lines under the value.
func wrapInfoField(label, value string, width int) []string {
	if width <= 0 {
		return nil
	}
	prefix := oneLine(label) + ": "
	prefixWidth := ansi.StringWidth(prefix)
	if prefixWidth >= width {
		return strings.Split(ansi.Hardwrap(oneLine(prefix+value), width, false), "\n")
	}

	chunks := strings.Split(ansi.Hardwrap(oneLine(value), width-prefixWidth, false), "\n")
	lines := make([]string, 0, len(chunks))
	indent := strings.Repeat(" ", prefixWidth)
	for i, chunk := range chunks {
		if i == 0 {
			lines = append(lines, prefix+chunk)
			continue
		}
		lines = append(lines, indent+chunk)
	}
	return lines
}

// formatBitrate renders bits per second as kbps with rounding.
func formatBitrate(bitsPerSecond int64) string {
	if bitsPerSecond >= 1000 {
		return fmt.Sprintf("%d kbps", (bitsPerSecond+500)/1000)
	}
	return fmt.Sprintf("%d bps", bitsPerSecond)
}

// formatChannels renders the channel count with the layout when known.
func formatChannels(channels int, layout string) string {
	layout = strings.TrimSpace(layout)
	if layout == "" {
		return strconv.Itoa(channels)
	}
	return fmt.Sprintf("%d (%s)", channels, layout)
}

// displayTagName converts a normalized ffprobe tag name to a readable label.
func displayTagName(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", " ")
	return strings.Join(strings.Fields(name), " ")
}
