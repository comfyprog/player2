package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/library"
	"player2/internal/playlist"
	"player2/internal/queue"
)

// browserLoadedMsg is the result of an asynchronous directory or CUE load.
// The generation guards against a slow load overwriting a newer location.
type browserLoadedMsg struct {
	gen      uint64
	path     string
	cue      bool
	entries  []library.Entry
	err      error
	selected int
	offset   int
	focus    string
}

// browserPlayMsg is the result of building a queue for a selection.
type browserPlayMsg struct {
	gen      uint64
	items    []queue.Item
	selected int
	status   string
	err      error
}

// browserPlayResultMsg reports the outcome of the asynchronous Play call.
type browserPlayResultMsg struct {
	gen    uint64
	status string
	err    error
}

// browserAddMsg is the result of expanding a selection into queue items.
type browserAddMsg struct {
	gen    uint64
	items  []queue.Item
	status string
	err    error
}

// browserTitle renders the browser breadcrumb, including any active filter.
// Paths are home-abbreviated and left-truncated so their tail stays visible.
func (m *Model) browserTitle(width int) string {
	const prefix = "Files: "

	suffix := ""
	switch {
	case m.browserSearching:
		suffix = "  /" + m.browserSearch
	case m.browserFilter != "":
		suffix = "  [" + m.browserFilter + "]"
	}

	available := width - ansi.StringWidth(prefix) - ansi.StringWidth(suffix)
	if available <= 0 {
		return truncatePlain(prefix+suffix, width)
	}
	path := truncateLeft(abbreviateHome(m.browserPath), available)
	return truncatePlain(prefix+path+suffix, width)
}

// browserLines renders the browser pane's title, rows, and empty/loading
// states. Every returned line is exactly width cells once styled.
func (m *Model) browserLines(width, height int) []string {
	lines := make([]string, height)
	if height == 0 {
		return lines
	}

	titleStyle := m.styles.Dim
	if m.focus == PaneBrowser {
		titleStyle = m.styles.PaneFocus
	}
	lines[0] = titleStyle.Render(fitPlain(m.browserTitle(width), width))

	blank := strings.Repeat(" ", max(width, 0))
	for i := 1; i < height; i++ {
		lines[i] = blank
	}

	body := height - 1
	if body <= 0 {
		return lines
	}

	if m.browserLoading {
		lines[1] = m.styles.Dim.Render(fitPlain("Loading…", width))
		return lines
	}

	visible := m.browserVisibleEntries()
	if len(visible) == 0 {
		message := "Empty directory"
		if m.browserFilter != "" {
			message = "No matches"
		}
		lines[1] = m.styles.Dim.Render(fitPlain(message, width))
		return lines
	}

	selected := m.browserSelected
	if selected < 0 {
		selected = 0
	}
	if selected >= len(visible) {
		selected = len(visible) - 1
	}

	offset := m.browserOffset
	if offset < 0 {
		offset = 0
	}
	if maxOffset := max(len(visible)-body, 0); offset > maxOffset {
		offset = maxOffset
	}
	if selected < offset {
		offset = selected
	}
	if selected >= offset+body {
		offset = selected - body + 1
	}

	for row := 0; row < body; row++ {
		index := offset + row
		if index >= len(visible) {
			break
		}
		entry := visible[index]
		style := m.browserEntryStyle(entry.Kind, index == selected)
		lines[row+1] = style.Render(fitPlain(entry.Name, width))
	}
	return lines
}

// browserEntryStyle chooses the visual role for one entry, with selection
// taking precedence over the entry kind.
func (m *Model) browserEntryStyle(kind library.Kind, selected bool) lipgloss.Style {
	if selected {
		return m.styles.Selection
	}
	switch kind {
	case library.KindDir:
		return m.styles.Dir
	case library.KindCueDir, library.KindCueTrack:
		return m.styles.Cue
	case library.KindAudio:
		return m.styles.Audio
	case library.KindPlaylist:
		return m.styles.Playlist
	case library.KindInvalidCue:
		return m.styles.Invalid
	default:
		return m.styles.Fg
	}
}

// browserVisibleEntries applies the case-insensitive substring filter without
// disturbing the scanner's natural sort order.
func (m *Model) browserVisibleEntries() []library.Entry {
	if m.browserFilter == "" {
		return m.browserEntries
	}
	needle := strings.ToLower(m.browserFilter)
	visible := make([]library.Entry, 0, len(m.browserEntries))
	for _, entry := range m.browserEntries {
		if strings.Contains(strings.ToLower(entry.Name), needle) {
			visible = append(visible, entry)
		}
	}
	return visible
}

// browserBodyRows is the number of list rows available below the pane title.
func (m *Model) browserBodyRows() int {
	return max(m.height-headerRows-footerRows-1, 1)
}

// browserSelectedEntry returns the currently selected visible entry.
func (m *Model) browserSelectedEntry() (library.Entry, bool) {
	visible := m.browserVisibleEntries()
	if m.browserSelected < 0 || m.browserSelected >= len(visible) {
		return library.Entry{}, false
	}
	return visible[m.browserSelected], true
}

// clampBrowser keeps the selection and scroll offset inside the filtered
// list and the visible rows.
func (m *Model) clampBrowser() {
	visible := len(m.browserVisibleEntries())
	if visible == 0 {
		m.browserSelected = 0
		m.browserOffset = 0
		return
	}
	if m.browserSelected < 0 {
		m.browserSelected = 0
	}
	if m.browserSelected >= visible {
		m.browserSelected = visible - 1
	}

	rows := m.browserBodyRows()
	maxOffset := max(visible-rows, 0)
	if m.browserOffset < 0 {
		m.browserOffset = 0
	}
	if m.browserOffset > maxOffset {
		m.browserOffset = maxOffset
	}
	if m.browserSelected < m.browserOffset {
		m.browserOffset = m.browserSelected
	}
	if m.browserSelected >= m.browserOffset+rows {
		m.browserOffset = m.browserSelected - rows + 1
	}
}

// moveBrowserSelection moves the selection by delta and keeps it visible.
func (m *Model) moveBrowserSelection(delta int) {
	visible := len(m.browserVisibleEntries())
	if visible == 0 {
		m.clampBrowser()
		return
	}
	m.browserSelected += delta
	if m.browserSelected < 0 {
		m.browserSelected = 0
	}
	if m.browserSelected >= visible {
		m.browserSelected = visible - 1
	}
	m.clampBrowser()
}

// selectBrowserFirst and selectBrowserLast jump within the filtered list.
func (m *Model) selectBrowserFirst() {
	m.browserSelected = 0
	m.browserOffset = 0
	m.clampBrowser()
}

func (m *Model) selectBrowserLast() {
	m.browserSelected = max(len(m.browserVisibleEntries())-1, 0)
	m.browserOffset = m.browserSelected
	m.clampBrowser()
}

// entryKey identifies an entry across rescans, including CUE tracks that
// share their backing audio file.
func entryKey(entry library.Entry) string {
	return fmt.Sprintf("%s\x00%s\x00%d", entry.Path, entry.CuePath, entry.TrackNo)
}

// loadBrowser starts an asynchronous load and records the destination. The
// generation is incremented before the command runs so a stale result can be
// recognised and ignored.
func (m *Model) loadBrowser(path string, cue bool, selected, offset int, focus string) tea.Cmd {
	// A new location must not expose entries from the previous one while the
	// asynchronous read is in flight. Rescans of the same path keep them.
	if m.browserEntriesPath != path {
		m.browserEntries = nil
		m.browserEntriesPath = ""
	}
	m.browserPath = path
	m.browserCue = cue
	m.browserSelected = selected
	m.browserOffset = offset
	m.browserLoading = true
	m.browserGen++
	gen := m.browserGen

	scanner := m.scanner
	if scanner == nil {
		m.browserLoading = false
		m.browserEntries = nil
		m.browserEntriesPath = ""
		return nil
	}

	return func() tea.Msg {
		ctx := context.Background()
		var (
			entries []library.Entry
			err     error
		)
		if cue {
			entries, err = scanner.ReadCue(ctx, path)
		} else {
			entries, err = scanner.ReadDir(ctx, path)
		}
		return browserLoadedMsg{
			gen:      gen,
			path:     path,
			cue:      cue,
			entries:  entries,
			err:      err,
			selected: selected,
			offset:   offset,
			focus:    focus,
		}
	}
}

// applyBrowserLoaded installs a directory listing, ignoring stale results.
func (m *Model) applyBrowserLoaded(msg browserLoadedMsg) {
	if msg.gen != m.browserGen {
		return
	}
	m.browserLoading = false

	if msg.err != nil {
		m.setStatusError(fmt.Sprintf("load %s: %v", abbreviateHome(msg.path), msg.err))
		// Keep the previous listing only when the failure belongs to the
		// same location (a rescan); otherwise stale rows would be shown
		// under a different breadcrumb.
		if m.browserEntriesPath != msg.path {
			m.browserEntries = nil
			m.browserEntriesPath = ""
		}
		m.clampBrowser()
		return
	}

	m.browserEntries = msg.entries
	m.browserEntriesPath = msg.path
	m.browserCue = msg.cue
	m.browserSelected = msg.selected
	m.browserOffset = msg.offset

	if msg.focus != "" {
		for index, entry := range m.browserVisibleEntries() {
			if entryKey(entry) == msg.focus {
				m.browserSelected = index
				break
			}
		}
	}
	m.clampBrowser()
	if m.statusIsError {
		m.setStatusOK("ready")
	}
}

// clearBrowserFilter removes the active filter and search input. Navigation
// starts with an unfiltered listing; a rescan deliberately keeps the filter.
func (m *Model) clearBrowserFilter() {
	m.browserFilter = ""
	m.browserSearch = ""
	m.browserSearching = false
}

// descend pushes the current location and loads a child directory or CUE
// folder.
func (m *Model) descend(entry library.Entry) tea.Cmd {
	m.browserStack = append(m.browserStack, browserLocation{
		Path:     m.browserPath,
		Selected: m.browserSelected,
		Offset:   m.browserOffset,
		Cue:      m.browserCue,
	})
	m.clearBrowserFilter()
	if entry.Kind == library.KindCueDir {
		return m.loadBrowser(entry.Path, true, 0, 0, "")
	}
	return m.loadBrowser(entry.Path, false, 0, 0, "")
}

// browserBack restores the previous location, selection, and offset.
func (m *Model) browserBack() tea.Cmd {
	if len(m.browserStack) == 0 {
		return nil
	}
	location := m.browserStack[len(m.browserStack)-1]
	m.browserStack = m.browserStack[:len(m.browserStack)-1]
	m.clearBrowserFilter()
	return m.loadBrowser(location.Path, location.Cue, location.Selected, location.Offset, "")
}

// rescanBrowser reloads the current location while trying to keep the selected
// entry, even when its index shifts.
func (m *Model) rescanBrowser() tea.Cmd {
	focus := ""
	if entry, ok := m.browserSelectedEntry(); ok {
		focus = entryKey(entry)
	}
	return m.loadBrowser(m.browserPath, m.browserCue, m.browserSelected, m.browserOffset, focus)
}

// browserOpen handles Enter at the browser.
func (m *Model) browserOpen() tea.Cmd {
	if m.browserLoading {
		return nil
	}
	entry, ok := m.browserSelectedEntry()
	if !ok {
		return nil
	}

	switch entry.Kind {
	case library.KindDir, library.KindCueDir:
		return m.descend(entry)
	case library.KindInvalidCue:
		message := "invalid CUE"
		if entry.Err != nil {
			message = entry.Err.Error()
		}
		m.setStatusError(message)
		return nil
	case library.KindPlaylist:
		return m.playPlaylist(entry.Path)
	case library.KindAudio, library.KindCueTrack:
		return m.playLevel(entry)
	default:
		return nil
	}
}

// playLevel replaces the queue with every playable item at the current level
// and starts at entry. Playlists are played only when selected directly;
// invalid CUEs and playlists are skipped here.
func (m *Model) playLevel(entry library.Entry) tea.Cmd {
	gen := m.browserGen
	scanner := m.scanner
	entries := append([]library.Entry(nil), m.browserEntries...)
	cue := m.browserCue
	selected := indexOfEntry(entries, entry)

	return func() tea.Msg {
		ctx := context.Background()
		items, start, err := buildLevelItems(ctx, scanner, entries, selected, cue)
		if err != nil {
			return browserPlayMsg{gen: gen, err: err, status: err.Error()}
		}
		if len(items) == 0 {
			return browserPlayMsg{gen: gen, status: "nothing to play"}
		}
		return browserPlayMsg{
			gen:      gen,
			items:    items,
			selected: start,
			status:   summariseQueue("playing", items, 0),
		}
	}
}

// playPlaylist expands a playlist and plays it from its first item.
func (m *Model) playPlaylist(path string) tea.Cmd {
	gen := m.browserGen
	scanner := m.scanner

	return func() tea.Msg {
		items, skipped, err := loadPlaylistItems(context.Background(), scanner, path)
		if err != nil {
			return browserPlayMsg{gen: gen, err: err, status: fmt.Sprintf("playlist %s: %v", filepath.Base(path), err)}
		}
		if len(items) == 0 {
			return browserPlayMsg{gen: gen, status: "nothing to play"}
		}
		return browserPlayMsg{
			gen:      gen,
			items:    items,
			selected: 0,
			status:   summariseQueue("playing", items, skipped),
		}
	}
}

// addSelected handles `a` at the browser.
func (m *Model) addSelected() tea.Cmd {
	if m.browserLoading {
		return nil
	}
	entry, ok := m.browserSelectedEntry()
	if !ok {
		return nil
	}

	switch entry.Kind {
	case library.KindDir:
		return m.walkAdd(entry.Path)
	case library.KindCueDir:
		return m.readCueAdd(entry.Path)
	case library.KindPlaylist:
		return m.playlistAdd(entry.Path)
	case library.KindAudio, library.KindCueTrack:
		return m.singleAdd(queueItemFromEntry(entry))
	case library.KindInvalidCue:
		message := "invalid CUE"
		if entry.Err != nil {
			message = entry.Err.Error()
		}
		m.setStatusError(message)
		return nil
	default:
		return nil
	}
}

// addAll handles `A`: the whole current location, recursively.
func (m *Model) addAll() tea.Cmd {
	if m.browserCue {
		return m.readCueAdd(m.browserPath)
	}
	return m.walkAdd(m.browserPath)
}

// walkAdd recursively expands a directory through Scanner.Walk.
func (m *Model) walkAdd(dir string) tea.Cmd {
	gen := m.browserGen
	scanner := m.scanner
	if scanner == nil {
		return nil
	}
	return func() tea.Msg {
		entries, err := scanner.Walk(context.Background(), dir)
		if err != nil {
			return browserAddMsg{gen: gen, err: err, status: err.Error()}
		}
		items := make([]queue.Item, 0, len(entries))
		for _, entry := range entries {
			items = append(items, queueItemFromEntry(entry))
		}
		return browserAddMsg{gen: gen, items: items, status: summariseQueue("added", items, 0)}
	}
}

// readCueAdd expands every track in a CUE folder.
func (m *Model) readCueAdd(cuePath string) tea.Cmd {
	gen := m.browserGen
	scanner := m.scanner
	if scanner == nil {
		return nil
	}
	return func() tea.Msg {
		entries, err := scanner.ReadCue(context.Background(), cuePath)
		if err != nil {
			return browserAddMsg{gen: gen, err: err, status: err.Error()}
		}
		items := make([]queue.Item, 0, len(entries))
		for _, entry := range entries {
			items = append(items, queueItemFromEntry(entry))
		}
		return browserAddMsg{gen: gen, items: items, status: summariseQueue("added", items, 0)}
	}
}

// playlistAdd expands a playlist for the queue.
func (m *Model) playlistAdd(path string) tea.Cmd {
	gen := m.browserGen
	scanner := m.scanner
	return func() tea.Msg {
		items, skipped, err := loadPlaylistItems(context.Background(), scanner, path)
		if err != nil {
			return browserAddMsg{gen: gen, err: err, status: fmt.Sprintf("playlist %s: %v", filepath.Base(path), err)}
		}
		return browserAddMsg{gen: gen, items: items, status: summariseQueue("added", items, skipped)}
	}
}

// singleAdd appends one audio file or CUE track.
func (m *Model) singleAdd(item queue.Item) tea.Cmd {
	gen := m.browserGen
	return func() tea.Msg {
		items := []queue.Item{item}
		return browserAddMsg{gen: gen, items: items, status: summariseQueue("added", items, 0)}
	}
}

// applyBrowserPlay installs the replacement queue and returns a command that
// starts playback. Play is blocking (it opens a decoder subprocess), so it must
// run in a Bubble Tea command rather than in Update.
func (m *Model) applyBrowserPlay(msg browserPlayMsg) tea.Cmd {
	if msg.gen != m.browserGen {
		return nil
	}
	if msg.err != nil {
		status := msg.status
		if status == "" {
			status = msg.err.Error()
		}
		m.setStatusError(status)
		return nil
	}
	if len(msg.items) == 0 {
		m.setStatusOK("nothing to play")
		return nil
	}

	// A replacement queue starts with fresh item IDs.
	m.probing = make(map[uint64]bool)
	m.queue.Replace(msg.items...)
	selected := msg.selected
	if selected < 0 || selected >= len(msg.items) {
		selected = 0
	}
	m.queue.SetCurrent(selected)
	m.playlistSelected = selected
	m.clampPlaylist()

	player := m.player
	item, ok := m.queue.Current()
	if player == nil || !ok {
		m.setStatusOK(msg.status)
		return m.afterQueueChange()
	}

	gen := msg.gen
	status := msg.status
	return func() tea.Msg {
		return browserPlayResultMsg{
			gen:    gen,
			status: status,
			err:    player.Play(item),
		}
	}
}

// applyBrowserPlayResult applies the status of an asynchronous Play call.
func (m *Model) applyBrowserPlayResult(msg browserPlayResultMsg) {
	if msg.gen != m.browserGen {
		return
	}
	if msg.err != nil {
		m.setStatusError("play: " + msg.err.Error())
		return
	}
	m.setStatusOK(msg.status)
}

// applyBrowserAdd appends expanded items to the existing queue.
func (m *Model) applyBrowserAdd(msg browserAddMsg) tea.Cmd {
	if msg.gen != m.browserGen {
		return nil
	}
	if msg.err != nil {
		status := msg.status
		if status == "" {
			status = msg.err.Error()
		}
		m.setStatusError(status)
		return nil
	}
	if len(msg.items) > 0 {
		m.queue.Append(msg.items...)
		m.clampPlaylist()
	}
	m.setStatusOK(msg.status)
	return m.afterQueueChange()
}

// handleBrowserAction dispatches pane actions while the browser is focused.
func (m *Model) handleBrowserAction(action Action) tea.Cmd {
	switch action {
	case ActionOpen:
		return m.browserOpen()
	case ActionParent:
		return m.browserBack()
	case ActionUp:
		m.moveBrowserSelection(-1)
	case ActionDown:
		m.moveBrowserSelection(1)
	case ActionPageUp:
		m.moveBrowserSelection(-m.browserBodyRows())
	case ActionPageDown:
		m.moveBrowserSelection(m.browserBodyRows())
	case ActionFirst:
		m.selectBrowserFirst()
	case ActionLast:
		m.selectBrowserLast()
	case ActionAdd:
		return m.addSelected()
	case ActionAddAll:
		return m.addAll()
	case ActionSearch:
		m.browserSearching = true
		m.browserSearch = m.browserFilter
	case ActionRescan:
		return m.rescanBrowser()
	}
	return nil
}

// handleBrowserSearchKey captures editing keys while the filter is active.
func (m *Model) handleBrowserSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	switch key {
	case "esc", "escape":
		m.browserSearching = false
		m.browserSearch = ""
		m.browserFilter = ""
		m.browserSelected = 0
		m.browserOffset = 0
		m.clampBrowser()
		return m, nil
	case "enter":
		m.browserSearching = false
		m.browserFilter = m.browserSearch
		m.clampBrowser()
		return m, nil
	case "backspace":
		if m.browserSearch != "" {
			runes := []rune(m.browserSearch)
			m.browserSearch = string(runes[:len(runes)-1])
		}
		m.browserFilter = m.browserSearch
		m.browserSelected = 0
		m.browserOffset = 0
		m.clampBrowser()
		return m, nil
	}

	switch key {
	case "up":
		m.moveBrowserSelection(-1)
		return m, nil
	case "down":
		m.moveBrowserSelection(1)
		return m, nil
	case "pgup":
		m.moveBrowserSelection(-m.browserBodyRows())
		return m, nil
	case "pgdown":
		m.moveBrowserSelection(m.browserBodyRows())
		return m, nil
	case "home":
		m.selectBrowserFirst()
		return m, nil
	case "end":
		m.selectBrowserLast()
		return m, nil
	}

	if text, ok := printableKeyText(msg); ok {
		m.browserSearch += text
		m.browserFilter = m.browserSearch
		m.browserSelected = 0
		m.browserOffset = 0
		m.clampBrowser()
	}
	return m, nil
}

// printableKeyText returns the literal text for a printable key press.
func printableKeyText(msg tea.KeyPressMsg) (string, bool) {
	text := msg.Text
	if text == "" && msg.Mod == 0 && msg.Code >= 0x20 && msg.Code <= 0x7e {
		text = string(msg.Code)
	}
	if text == "" {
		return "", false
	}
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return text, true
}

// buildLevelItems expands the current level in listing order. CUE folders
// contribute their tracks in place; playlists and invalid CUEs are skipped.
func buildLevelItems(ctx context.Context, scanner Scanner, entries []library.Entry, selectedIndex int, cue bool) ([]queue.Item, int, error) {
	items := make([]queue.Item, 0, len(entries))
	selected := 0

	if cue {
		for index, entry := range entries {
			if entry.Kind != library.KindCueTrack {
				continue
			}
			if index == selectedIndex {
				selected = len(items)
			}
			items = append(items, queueItemFromEntry(entry))
		}
	} else {
		for index, entry := range entries {
			switch entry.Kind {
			case library.KindAudio:
				if index == selectedIndex {
					selected = len(items)
				}
				items = append(items, queueItemFromEntry(entry))
			case library.KindCueDir:
				if index == selectedIndex {
					selected = len(items)
				}
				if scanner == nil {
					continue
				}
				tracks, err := scanner.ReadCue(ctx, entry.Path)
				if err != nil {
					return nil, 0, err
				}
				for _, track := range tracks {
					items = append(items, queueItemFromEntry(track))
				}
			}
		}
	}

	if len(items) == 0 {
		return items, 0, nil
	}
	if selected < 0 || selected >= len(items) {
		selected = 0
	}
	return items, selected, nil
}

// indexOfEntry finds an entry in a listing by its identity.
func indexOfEntry(entries []library.Entry, target library.Entry) int {
	key := entryKey(target)
	for index, entry := range entries {
		if entryKey(entry) == key {
			return index
		}
	}
	return -1
}

// queueItemFromEntry builds a queue item from a scanner entry. CUE tracks
// carry their start, duration, and metadata; plain audio falls back to its
// filename when no title metadata exists.
func queueItemFromEntry(entry library.Entry) queue.Item {
	if entry.Kind == library.KindCueTrack {
		return queue.Item{
			Path:     entry.Path,
			CuePath:  entry.CuePath,
			CueTrack: entry.TrackNo,
			Start:    entry.Start,
			Duration: entry.Duration,
			Title:    entry.Meta.Title,
			Artist:   entry.Meta.Artist,
			Album:    entry.Meta.Album,
			TrackNo:  entry.TrackNo,
		}
	}

	title := entry.Meta.Title
	if title == "" {
		base := filepath.Base(entry.Path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return queue.Item{
		Path:     entry.Path,
		Duration: entry.Duration,
		Title:    title,
		Artist:   entry.Meta.Artist,
		Album:    entry.Meta.Album,
		TrackNo:  entry.Meta.TrackNo,
	}
}

// loadPlaylistItems loads and expands a playlist into playable queue items.
// Unresolvable CUE references fall back to the whole-file playlist entry and
// missing audio files are skipped and counted.
func loadPlaylistItems(ctx context.Context, scanner Scanner, path string) ([]queue.Item, int, error) {
	entries, err := playlist.Load(path)
	if err != nil {
		return nil, 0, err
	}

	items := make([]queue.Item, 0, len(entries))
	skipped := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		item, missing := playlistQueueItem(ctx, scanner, entry)
		if missing {
			skipped++
			continue
		}
		items = append(items, item)
	}
	return items, skipped, nil
}

// playlistQueueItem converts one playlist entry. A resolvable CUE reference
// becomes a CUE track; otherwise the whole file is used.
func playlistQueueItem(ctx context.Context, scanner Scanner, entry playlist.Entry) (queue.Item, bool) {
	item := queue.Item{
		Path:     entry.Path,
		CuePath:  entry.CuePath,
		CueTrack: entry.CueTrack,
		Title:    entry.Title,
		Artist:   entry.Artist,
		Duration: entry.Duration,
	}

	if entry.CuePath != "" {
		resolved := false
		if scanner != nil {
			track, err := scanner.ResolveCueTrack(ctx, entry.CuePath, entry.CueTrack)
			if err == nil {
				resolved = true
				item.Path = track.Path
				item.CuePath = entry.CuePath
				item.CueTrack = entry.CueTrack
				item.Start = track.Start
				if item.Duration <= 0 {
					item.Duration = track.Duration
				}
				item.Album = track.Meta.Album
				item.TrackNo = track.TrackNo
				if item.Title == "" {
					item.Title = track.Meta.Title
				}
				if item.Artist == "" {
					item.Artist = track.Meta.Artist
				}
			}
		}
		if !resolved {
			// Controller ruling R11: an unresolved CUE reference falls back
			// to the whole-file playlist entry.
			item.CuePath = ""
			item.CueTrack = 0
			item.Start = 0
		}
	}

	if item.Path == "" {
		return queue.Item{}, true
	}
	if _, err := os.Stat(item.Path); err != nil {
		return queue.Item{}, true
	}
	if item.Title == "" {
		base := filepath.Base(item.Path)
		item.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return item, false
}

// summariseQueue builds a short status message, including skipped files.
func summariseQueue(verb string, items []queue.Item, skipped int) string {
	status := fmt.Sprintf("%s %d item(s)", verb, len(items))
	if skipped > 0 {
		status += fmt.Sprintf(", skipped %d", skipped)
	}
	return status
}
