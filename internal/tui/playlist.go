package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/meta"
	"player2/internal/player"
	"player2/internal/queue"
)

// playerEventMsg carries one event from the playback engine. ok is false when
// the event channel has been closed; the model must not resubscribe then.
type playerEventMsg struct {
	event player.Event
	ok    bool
}

// transportKind identifies a serialized playback-changing operation.
type transportKind int

const (
	transportPlay transportKind = iota
	transportSeek
	transportStop
)

// transportAction is one queued Play, Seek, or Stop request. preview marks a
// Play that started one-off playback outside the queue so replacing it can
// clear the preview state instead of leaving it stuck.
type transportAction struct {
	kind    transportKind
	item    queue.Item
	status  string
	delta   time.Duration
	seq     uint64
	preview bool
}

// transportResultMsg reports the outcome of one serialized transport
// operation. seq identifies the request so a stale result cannot apply its
// status over a newer request.
type transportResultMsg struct {
	seq    uint64
	kind   transportKind
	status string
	delta  time.Duration
	err    error
}

// queueRestoredMsg is the result of loading saved playlist tabs. epoch records
// the queue generation when the load started so a late result cannot overwrite
// a queue changed in the meantime.
type queueRestoredMsg struct {
	active  int
	tabs    []queue.Snapshot
	skipped int
	err     error
	epoch   uint64
}

// queueSavedMsg is the result of an asynchronous queue save.
type queueSavedMsg struct {
	err error
}

// queueMetadataMsg carries probed metadata for one queue item.
type queueMetadataMsg struct {
	id   uint64
	path string
	info meta.Info
	err  error
}

// probeConcurrency bounds how many metadata probes may run at once.
const probeConcurrency = 4

// playlistBodyRows is the number of list rows available below the pane title.
func (m *Model) playlistBodyRows() int {
	return max(m.height-headerRows-footerRows-1, 1)
}

// playlistBoundsFor clamps a selection and scroll offset for count items and
// rows visible rows, keeping the selection on screen.
func playlistBoundsFor(selected, offset, count, rows int) (int, int) {
	if count <= 0 || rows <= 0 {
		return 0, 0
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= count {
		selected = count - 1
	}

	if offset < 0 {
		offset = 0
	}
	if maxOffset := max(count-rows, 0); offset > maxOffset {
		offset = maxOffset
	}
	if selected < offset {
		offset = selected
	}
	if selected >= offset+rows {
		offset = selected - rows + 1
	}
	return selected, offset
}

// playlistBounds clamps the active tab's selection and scroll offset.
func (m *Model) playlistBounds(count, rows int) (selected, offset int) {
	return playlistBoundsFor(m.playlistSelected, m.playlistOffset, count, rows)
}

// tabSelection returns tab's remembered selection and offset. The active tab
// keeps those values in the model's top-level fields.
func (m *Model) tabSelection(tab int) (selected, offset int) {
	if tab == m.activeTab {
		return m.playlistSelected, m.playlistOffset
	}
	if tab < 0 || tab >= tabCount {
		return 0, 0
	}
	return m.tabs[tab].selected, m.tabs[tab].offset
}

// setTabSelection stores selection and offset for tab.
func (m *Model) setTabSelection(tab, selected, offset int) {
	if tab == m.activeTab {
		m.playlistSelected = selected
		m.playlistOffset = offset
		return
	}
	if tab < 0 || tab >= tabCount {
		return
	}
	m.tabs[tab].selected = selected
	m.tabs[tab].offset = offset
}

// clampTabSelection keeps tab's selection and offset valid for its queue and
// the current terminal size.
func (m *Model) clampTabSelection(tab int) {
	if tab < 0 || tab >= tabCount {
		return
	}
	selected, offset := m.tabSelection(tab)
	selected, offset = playlistBoundsFor(selected, offset, m.tabs[tab].queue.Len(), m.playlistBodyRows())
	m.setTabSelection(tab, selected, offset)
}

// clampPlaylist keeps the selection and offset valid for the current queue and
// terminal size.
func (m *Model) clampPlaylist() {
	selected, offset := m.playlistBounds(len(m.queue.Snapshot().Items), m.playlistBodyRows())
	m.playlistSelected = selected
	m.playlistOffset = offset
}

// switchTab makes index the visible playlist tab. It preserves the previous
// tab's selection and does not change playback.
func (m *Model) switchTab(index int) tea.Cmd {
	if index < 0 || index >= tabCount || index == m.activeTab {
		return nil
	}

	m.tabs[m.activeTab].selected = m.playlistSelected
	m.tabs[m.activeTab].offset = m.playlistOffset
	m.activeTab = index
	m.queue = m.tabs[index].queue
	m.playlistSelected = m.tabs[index].selected
	m.playlistOffset = m.tabs[index].offset
	m.clampPlaylist()

	if count := m.queue.Len(); count == 0 {
		m.setStatusOK(fmt.Sprintf("tab %d: empty", index))
	} else {
		m.setStatusOK(fmt.Sprintf("tab %d: %d item(s)", index, count))
	}
	return m.probeItems()
}

// queueCurrentIndex returns the index of q's current item, or 0 when there is
// no current item.
func queueCurrentIndex(q *queue.Queue) int {
	current, ok := q.Current()
	if !ok {
		return 0
	}
	for index, item := range q.Snapshot().Items {
		if item.ID == current.ID {
			return index
		}
	}
	return 0
}

// playlistLines renders the playlist pane's title and rows. Every returned line
// is exactly width cells once styled.
func (m *Model) playlistLines(width, height int) []string {
	lines := make([]string, height)
	if height == 0 {
		return lines
	}

	lines[0] = m.tabStrip(width)

	blank := strings.Repeat(" ", max(width, 0))
	for i := 1; i < height; i++ {
		lines[i] = blank
	}

	body := height - 1
	if body <= 0 {
		return lines
	}

	items := m.queue.Snapshot().Items
	if len(items) == 0 {
		empty := fmt.Sprintf("Tab %d is empty", m.activeTab)
		lines[1] = m.styles.Dim.Render(fitPlain(empty, width))
		return lines
	}

	selected, offset := m.playlistBounds(len(items), body)
	m.playlistSelected = selected
	m.playlistOffset = offset

	for row := 0; row < body; row++ {
		index := offset + row
		if index >= len(items) {
			break
		}
		item := items[index]
		current := m.nowPlaying(item.ID)
		plain := playlistRow(item, current, width)
		style := m.styles.Fg
		if current {
			style = m.styles.Playing
		}
		if m.focus == PanePlaylist && index == selected {
			style = m.styles.Selection
		}
		lines[row+1] = style.Render(plain)
	}
	return lines
}

// tabStrip renders the ten fixed playlist tabs. The active tab is bracketed,
// the sounding tab carries the playing marker, and empty tabs are dimmed. The
// result is width cells wide.
func (m *Model) tabStrip(width int) string {
	if width <= 0 {
		return ""
	}

	var b strings.Builder
	used := 0
	for i := 0; i < tabCount; i++ {
		label := strconv.Itoa(i)
		if i == m.playbackTab {
			label += playingMarker
		}
		if i == m.activeTab {
			label = "[" + label + "]"
		}

		cellWidth := ansi.StringWidth(label)
		if i > 0 {
			cellWidth++
		}
		if used+cellWidth > width {
			break
		}
		if i > 0 {
			b.WriteByte(' ')
			used++
		}

		style := m.styles.Fg
		if m.tabs[i].queue.Len() == 0 {
			style = m.styles.Dim
		}
		if i == m.playbackTab {
			style = m.styles.Playing
		}
		if i == m.activeTab {
			if m.focus == PanePlaylist {
				style = m.styles.Selection
			} else {
				style = m.styles.PaneFocus
			}
		}

		b.WriteString(style.Render(label))
		used += ansi.StringWidth(label)
	}

	if used < width {
		b.WriteString(strings.Repeat(" ", width-used))
	}
	return b.String()
}

// playlistRow formats one row: current marker, track number, title/artist, and
// a right-aligned duration. The result is exactly width cells of plain text.
func playlistRow(item queue.Item, current bool, width int) string {
	if width <= 0 {
		return ""
	}

	marker := "  "
	if current {
		marker = playingMarker + " "
	}

	label := ""
	if item.TrackNo > 0 {
		label = fmt.Sprintf("%02d. ", item.TrackNo)
	}

	title := displayTitle(item)
	left := marker + label + title

	right := "--:--"
	if item.Duration > 0 {
		right = formatDuration(item.Duration)
	}
	rightWidth := ansi.StringWidth(right)
	if rightWidth >= width {
		return truncatePlain(right, width)
	}

	leftWidth := width - rightWidth - 1
	if leftWidth <= 0 {
		return strings.Repeat(" ", width-rightWidth) + right
	}
	return fitPlain(left, leftWidth) + " " + right
}

// displayTitle returns a one-line title with an optional artist suffix.
func displayTitle(item queue.Item) string {
	title := item.Title
	if title == "" {
		base := filepath.Base(item.Path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if title == "" {
		title = "unknown"
	}
	if item.Artist != "" {
		return title + " — " + item.Artist
	}
	return title
}

// selectedPlaylistItem returns the item under the active tab's selection.
func (m *Model) selectedPlaylistItem() (queue.Item, bool) {
	return m.selectedPlaylistItemFor(m.activeTab)
}

// selectedPlaylistItemFor returns the item under tab's selection.
func (m *Model) selectedPlaylistItemFor(tab int) (queue.Item, bool) {
	if tab < 0 || tab >= tabCount {
		return queue.Item{}, false
	}
	items := m.tabs[tab].queue.Snapshot().Items
	if len(items) == 0 {
		return queue.Item{}, false
	}
	index, _ := m.tabSelection(tab)
	if index < 0 {
		index = 0
	}
	if index >= len(items) {
		index = len(items) - 1
	}
	return items[index], true
}

// queueIndexOf returns the index of id in q, or -1.
func queueIndexOf(q *queue.Queue, id uint64) int {
	for index, item := range q.Snapshot().Items {
		if item.ID == id {
			return index
		}
	}
	return -1
}

// queueIndex returns the index of id in the active queue, or -1.
func (m *Model) queueIndex(id uint64) int {
	return queueIndexOf(m.queue, id)
}

// selectCurrent moves the playlist selection to the current item.
func (m *Model) selectCurrent() {
	current, ok := m.queue.Current()
	if !ok {
		m.playlistSelected = 0
		m.playlistOffset = 0
		return
	}
	if index := m.queueIndex(current.ID); index >= 0 {
		m.playlistSelected = index
	}
	m.clampPlaylist()
}

// followSelection applies the active tab's selection-follow rule.
func (m *Model) followSelection(previous, selected queue.Item, hadPrevious, hadSelected bool) {
	m.followSelectionFor(m.activeTab, previous, selected, hadPrevious, hadSelected)
}

// followSelectionFor moves tab's selection to the new current item only when
// it was on the previous current item.
func (m *Model) followSelectionFor(tab int, previous, selected queue.Item, hadPrevious, hadSelected bool) {
	if tab < 0 || tab >= tabCount {
		return
	}
	if hadPrevious && hadSelected && previous.ID == selected.ID {
		q := m.tabs[tab].queue
		if current, ok := q.Current(); ok {
			if index := queueIndexOf(q, current.ID); index >= 0 {
				_, offset := m.tabSelection(tab)
				m.setTabSelection(tab, index, offset)
			}
		}
	}
	m.clampTabSelection(tab)
}

// movePlaylistSelection moves the selection by delta and keeps it visible.
func (m *Model) movePlaylistSelection(delta int) {
	count := len(m.queue.Snapshot().Items)
	if count == 0 {
		m.clampPlaylist()
		return
	}
	m.playlistSelected += delta
	m.clampPlaylist()
}

// selectPlaylistFirst and selectPlaylistLast jump within the queue.
func (m *Model) selectPlaylistFirst() {
	m.playlistSelected = 0
	m.playlistOffset = 0
	m.clampPlaylist()
}

func (m *Model) selectPlaylistLast() {
	m.playlistSelected = max(len(m.queue.Snapshot().Items)-1, 0)
	m.playlistOffset = m.playlistSelected
	m.clampPlaylist()
}

// playItem queues a Play request and makes tab the sounding tab. Play opens a
// decoder and may block, so it never runs inside Update.
func (m *Model) playItem(tab int, item queue.Item, status string) tea.Cmd {
	m.clearPreview()
	if tab >= 0 && tab < tabCount {
		m.playbackTab = tab
	}
	return m.requestTransport(transportAction{
		kind:   transportPlay,
		item:   item,
		status: status,
	})
}

// transportTab returns the tab that next/previous should target: the sounding
// tab while one is active, otherwise the visible tab.
func (m *Model) transportTab() int {
	if m.playbackTab >= 0 && m.playbackTab < tabCount {
		return m.playbackTab
	}
	return m.activeTab
}

// activeOwnsPlayback reports whether clearing or removing from the visible tab
// should stop the stream. With no sounding tab there is nothing else to
// protect, so the legacy stop behavior applies.
func (m *Model) activeOwnsPlayback() bool {
	return m.playbackTab < 0 || m.playbackTab == m.activeTab
}

// requestTransport serializes playback-changing operations. While one is in
// flight, the latest new request is kept as the single pending action (Stop
// replaces any pending Play/Seek). A request made while idle becomes a command
// immediately. The returned command must run outside Update.
func (m *Model) requestTransport(action transportAction) tea.Cmd {
	if m.player == nil {
		return nil
	}
	m.transportSeq++
	action.seq = m.transportSeq
	if m.transportBusy {
		if m.pendingTransport != nil && m.pendingTransport.preview && !action.preview {
			// The preview Play never reached the player. Drop the preview
			// state with it so a following Seek/Stop cannot leave the header
			// claiming a preview owns the queue's stream.
			m.clearPreview()
		}
		m.pendingTransport = &action
		return nil
	}
	m.transportBusy = true
	return m.dispatchTransport(action)
}

// dispatchTransport returns the command for one action. The caller has already
// marked the transport busy.
func (m *Model) dispatchTransport(action transportAction) tea.Cmd {
	player := m.player
	return func() tea.Msg {
		result := transportResultMsg{
			seq:    action.seq,
			kind:   action.kind,
			status: action.status,
			delta:  action.delta,
		}
		switch action.kind {
		case transportPlay:
			result.err = player.Play(action.item)
		case transportSeek:
			result.err = player.SeekBy(action.delta)
		case transportStop:
			player.Stop()
		}
		return result
	}
}

// handleTransportAction dispatches actions that work from either pane.
func (m *Model) handleTransportAction(action Action) (tea.Cmd, bool) {
	switch action {
	case ActionPause:
		return m.actionPause(), true
	case ActionStop:
		return m.actionStop(), true
	case ActionNext:
		return m.actionNext(), true
	case ActionPrevious:
		return m.actionPrevious(), true
	case ActionSeekBack:
		return m.actionSeek(-5 * time.Second), true
	case ActionSeekForward:
		return m.actionSeek(5 * time.Second), true
	case ActionVolumeUp:
		m.changeVolume(5)
		return nil, true
	case ActionVolumeDown:
		m.changeVolume(-5)
		return nil, true
	case ActionMute:
		m.toggleMute()
		return nil, true
	case ActionShuffle:
		return m.toggleShuffle(), true
	case ActionRepeat:
		return m.cycleRepeat(), true
	default:
		return nil, false
	}
}

// actionPause toggles a playing/paused player, or starts the current item when
// playback is stopped.
func (m *Model) actionPause() tea.Cmd {
	if m.player != nil {
		state := m.player.Status().State
		if state == player.StatePlaying || state == player.StatePaused {
			m.player.TogglePause()
			m.refresh()
			if m.status.State == player.StatePaused {
				m.setStatusOK("paused")
			} else {
				m.setStatusOK("playing")
			}
			return nil
		}
	}

	item, ok := m.queue.Current()
	if !ok || m.player == nil {
		m.setStatusOK("nothing to play")
		return nil
	}
	m.beginPlaybackIntent()
	return m.playItem(m.activeTab, item, "playing "+displayTitle(item))
}

// actionStop stops playback. The status is immediate; the actual Stop is
// serialized behind any Play already in flight.
func (m *Model) actionStop() tea.Cmd {
	m.setStatusOK("stopped")
	if m.player == nil {
		m.clearPreview()
		m.playbackTab = -1
		m.beginPlaybackIntent()
		return nil
	}
	return m.stopPlayback()
}

// actionNext advances the sounding queue and plays the next item. Next and
// previous are ignored while a one-off preview owns playback.
func (m *Model) actionNext() tea.Cmd {
	if m.previewActive {
		return nil
	}
	tab := m.transportTab()
	q := m.tabs[tab].queue
	previous, hadPrevious := q.Current()
	selected, hadSelected := m.selectedPlaylistItemFor(tab)
	item, ok := q.Next(false)
	if !ok {
		m.clampTabSelection(tab)
		m.setStatusOK(m.queueEndStatus(tab, "end of queue"))
		return nil
	}
	m.bumpQueueEpoch()
	m.followSelectionFor(tab, previous, selected, hadPrevious, hadSelected)
	m.beginPlaybackIntent()
	return tea.Batch(m.playItem(tab, item, "playing "+displayTitle(item)), m.saveQueue())
}

// actionPrevious moves back in the sounding queue and plays the previous item.
func (m *Model) actionPrevious() tea.Cmd {
	if m.previewActive {
		return nil
	}
	tab := m.transportTab()
	q := m.tabs[tab].queue
	previous, hadPrevious := q.Current()
	selected, hadSelected := m.selectedPlaylistItemFor(tab)
	item, ok := q.Prev()
	if !ok {
		m.clampTabSelection(tab)
		m.setStatusOK(m.queueEndStatus(tab, "start of queue"))
		return nil
	}
	m.bumpQueueEpoch()
	m.followSelectionFor(tab, previous, selected, hadPrevious, hadSelected)
	m.beginPlaybackIntent()
	return tea.Batch(m.playItem(tab, item, "playing "+displayTitle(item)), m.saveQueue())
}

// queueEndStatus names the tab for queue-boundary messages when it is not the
// visible one.
func (m *Model) queueEndStatus(tab int, message string) string {
	if tab == m.activeTab {
		return message
	}
	return fmt.Sprintf("tab %d: %s", tab, message)
}

// actionSeek queues a seek by delta.
func (m *Model) actionSeek(delta time.Duration) tea.Cmd {
	return m.requestTransport(transportAction{kind: transportSeek, delta: delta})
}

// changeVolume moves the volume by delta within 0..100.
func (m *Model) changeVolume(delta int) {
	if m.player == nil {
		return
	}
	m.setVolume(min(max(m.volume()+delta, 0), 100))
}

// setVolume applies a volume, updates the cached status, and reports it.
func (m *Model) setVolume(volume int) {
	if m.player != nil {
		m.player.SetVolume(volume)
	}
	m.status.Volume = volume
	if volume > 0 {
		m.lastVolume = volume
		m.setStatusOK(fmt.Sprintf("vol %d", volume))
		return
	}
	m.setStatusOK("muted")
}

// toggleMute switches between zero and the last non-zero volume.
func (m *Model) toggleMute() {
	if m.player == nil {
		return
	}
	if volume := m.volume(); volume != 0 {
		m.lastVolume = volume
		m.setVolume(0)
		return
	}
	volume := m.lastVolume
	if volume <= 0 {
		volume = m.cfg.Volume
	}
	if volume <= 0 {
		volume = 70
	}
	m.setVolume(min(volume, 100))
}

// toggleShuffle flips shuffle playback and persists the queue.
func (m *Model) toggleShuffle() tea.Cmd {
	enabled := !m.queue.Snapshot().Shuffle
	m.queue.SetShuffle(enabled)
	// Shuffle/repeat do not replace items or the current item, so they do not
	// invalidate a pending startup restore. saveQueue no-ops until the restore
	// finishes, which lets the successful restore decide what to persist.
	if enabled {
		m.setStatusOK("shuffle on")
	} else {
		m.setStatusOK("shuffle off")
	}
	return m.saveQueue()
}

// cycleRepeat advances the repeat mode and persists the queue.
func (m *Model) cycleRepeat() tea.Cmd {
	mode := m.queue.CycleRepeat()
	// Like shuffle, repeat is a setting change rather than a queue mutation.
	switch mode {
	case queue.RepeatAll:
		m.setStatusOK("repeat all")
	case queue.RepeatOne:
		m.setStatusOK("repeat one")
	default:
		m.setStatusOK("repeat off")
	}
	return m.saveQueue()
}

// handlePlaylistAction dispatches actions available while the playlist pane is
// focused.
func (m *Model) handlePlaylistAction(action Action) tea.Cmd {
	switch action {
	case ActionOpen:
		return m.playlistOpen()
	case ActionRemove:
		return m.playlistRemove()
	case ActionMoveUp:
		return m.playlistMove(-1)
	case ActionMoveDown:
		return m.playlistMove(1)
	case ActionClear:
		return m.requestClear()
	case ActionInfo:
		return m.openPlaylistInfo()
	case ActionGotoPlaying:
		return m.gotoPlaying()
	case ActionUp:
		m.movePlaylistSelection(-1)
	case ActionDown:
		m.movePlaylistSelection(1)
	case ActionPageUp:
		m.movePlaylistSelection(-m.playlistBodyRows())
	case ActionPageDown:
		m.movePlaylistSelection(m.playlistBodyRows())
	case ActionFirst:
		m.selectPlaylistFirst()
	case ActionLast:
		m.selectPlaylistLast()
	}
	return nil
}

// gotoPlaying selects the sounding tab's current item, switching to that tab
// when it is not visible. With no sounding tab it selects the visible tab's
// current item.
func (m *Model) gotoPlaying() tea.Cmd {
	if m.playbackTab >= 0 && m.playbackTab < tabCount && m.playbackTab != m.activeTab {
		cmd := m.switchTab(m.playbackTab)
		m.selectCurrent()
		return cmd
	}
	m.selectCurrent()
	return nil
}

// playlistOpen makes the selected item current and plays it.
func (m *Model) playlistOpen() tea.Cmd {
	m.clampPlaylist()
	if !m.queue.SetCurrent(m.playlistSelected) {
		m.setStatusOK("nothing to play")
		return nil
	}
	m.bumpQueueEpoch()
	m.clampPlaylist()
	item, ok := m.queue.Current()
	if !ok {
		m.setStatusOK("nothing to play")
		return nil
	}
	m.beginPlaybackIntent()
	return tea.Batch(m.playItem(m.activeTab, item, "playing "+displayTitle(item)), m.saveQueue())
}

// playlistRemove removes the selected item. Removing the current item stops
// playback; the queue selects the next (or previous) item.
func (m *Model) playlistRemove() tea.Cmd {
	m.clampPlaylist()
	previous, hadPrevious := m.queue.Current()
	removed, ok := m.queue.Remove(m.playlistSelected)
	if !ok {
		m.setStatusOK("nothing to remove")
		return nil
	}
	m.bumpQueueEpoch()
	var stop tea.Cmd
	if hadPrevious && removed.ID == previous.ID && !m.previewActive && m.activeOwnsPlayback() {
		m.playbackTab = -1
		stop = m.requestTransport(transportAction{kind: transportStop})
	}
	m.clampPlaylist()
	m.setStatusOK("removed " + displayTitle(removed))
	if stop == nil {
		return m.saveQueue()
	}
	return tea.Batch(stop, m.saveQueue())
}

// playlistMove moves the selected item by direction (-1 up, +1 down).
func (m *Model) playlistMove(direction int) tea.Cmd {
	if !m.queue.Move(m.playlistSelected, direction) {
		m.clampPlaylist()
		return nil
	}
	m.bumpQueueEpoch()
	m.playlistSelected += direction
	m.clampPlaylist()
	return m.saveQueue()
}

// playlistClear empties the queue and stops playback. Task 13 wraps this in a
// confirmation prompt.
func (m *Model) playlistClear() tea.Cmd {
	m.queue.Clear()
	m.bumpQueueEpoch()
	var stop tea.Cmd
	if !m.previewActive && m.activeOwnsPlayback() {
		m.playbackTab = -1
		stop = m.requestTransport(transportAction{kind: transportStop})
	}
	m.playlistSelected = 0
	m.playlistOffset = 0
	m.probing = make(map[uint64]string)
	m.setStatusOK("queue cleared")
	if stop == nil {
		return m.saveQueue()
	}
	return tea.Batch(stop, m.saveQueue())
}

// bumpQueueEpoch records a user/browser queue mutation so a late restore is
// ignored instead of clobbering the newer queue. It also clears restorePending:
// the in-memory queue is now intentional user state and must be saved even if
// the startup restore never succeeds.
func (m *Model) bumpQueueEpoch() {
	m.queueEpoch++
	m.restorePending = false
}

// applyTransportResult applies the outcome of one serialized transport
// operation and then starts the pending request, if any. Status and cached
// player state are only refreshed when this result belongs to the latest
// request.
func (m *Model) applyTransportResult(msg transportResultMsg) tea.Cmd {
	m.transportBusy = false

	if msg.seq == m.transportSeq {
		switch {
		case msg.err != nil:
			if msg.kind == transportPlay {
				m.clearPreview()
			}
			switch msg.kind {
			case transportSeek:
				m.setStatusError("seek: " + msg.err.Error())
			default:
				m.setStatusError("play: " + msg.err.Error())
			}
		default:
			m.refresh()
			switch msg.kind {
			case transportSeek:
				m.setStatusOK(fmt.Sprintf("seek %+ds", int(msg.delta/time.Second)))
			case transportStop:
				m.setStatusOK("stopped")
			default:
				status := msg.status
				if status == "" {
					status = "playing"
				}
				m.setStatusOK(status)
			}
		}
	}

	if pending := m.pendingTransport; pending != nil {
		m.pendingTransport = nil
		m.transportBusy = true
		return m.dispatchTransport(*pending)
	}
	return nil
}

// saveQueue persists the current queue with single-flight coalescing. A
// mutation while a save is in flight marks the queue dirty so the latest
// snapshot is written afterwards.
func (m *Model) saveQueue() tea.Cmd {
	if m.store == nil {
		return nil
	}
	// While the startup restore is pending the in-memory queue is not
	// authoritative. Do not write (not even an empty queue); the successful
	// restore path will save if it pruned items.
	if m.restorePending {
		if m.saving {
			m.saveDirty = true
		}
		return nil
	}
	if m.saving {
		m.saveDirty = true
		return nil
	}
	m.saving = true
	active, tabs := m.stateSnapshot()
	store := m.store
	return func() tea.Msg {
		m.saveMu.Lock()
		defer m.saveMu.Unlock()
		if m.flushed {
			// An exit-time flush has already persisted the newest snapshot;
			// this older async save must not overwrite it.
			return queueSavedMsg{}
		}
		return queueSavedMsg{err: store.SaveTabs(active, tabs)}
	}
}

// stateSnapshot copies every tab and the active index for persistence. Queue
// values are snapshotted on the UI goroutine because queues are not
// thread-safe.
func (m *Model) stateSnapshot() (int, []queue.Snapshot) {
	tabs := make([]queue.Snapshot, tabCount)
	for i := range m.tabs {
		tabs[i] = m.tabs[i].queue.Snapshot()
	}
	return m.activeTab, tabs
}

// FlushQueue synchronously saves the latest queue snapshot. It shares saveMu
// with the asynchronous save commands, so it waits for any save already in
// flight and then writes the current state. Saves that have not started yet
// are skipped afterwards, so an older in-flight snapshot can never overwrite
// the exit-time save. A nil store is a no-op.
func (m *Model) FlushQueue() error {
	if m.store == nil {
		return nil
	}
	// The startup restore is asynchronous. Until it lands (or the user changes
	// the queue), the in-memory queue is not authoritative and saving it would
	// destroy a valid persisted queue on an immediate exit or Run error.
	if m.restorePending {
		return nil
	}
	active, tabs := m.stateSnapshot()
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.flushed = true
	return m.store.SaveTabs(active, tabs)
}

// QueueSnapshot returns a copy of the current queue state.
func (m *Model) QueueSnapshot() queue.Snapshot {
	return m.queue.Snapshot()
}

// applyQueueSaved finishes one save and starts the coalesced follow-up.
func (m *Model) applyQueueSaved(msg queueSavedMsg) tea.Cmd {
	m.saving = false
	if msg.err != nil {
		m.setStatusError("save: " + msg.err.Error())
	}
	if m.saveDirty {
		m.saveDirty = false
		return m.saveQueue()
	}
	return nil
}

// afterQueueChange schedules metadata probes and a coalesced, single-flight
// save after the queue contents changed.
func (m *Model) afterQueueChange() tea.Cmd {
	return tea.Batch(m.probeItems(), m.saveQueue())
}

// needsProbe reports whether a plain audio item lacks a duration or usable
// title. CUE tracks are never probed because their metadata comes from the cue
// sheet.
func (m *Model) needsProbe(item queue.Item) bool {
	if item.CuePath != "" || item.Path == "" {
		return false
	}
	if item.Duration <= 0 {
		return true
	}
	return !usableTitle(item)
}

// usableTitle reports whether the title is more than a filename fallback.
func usableTitle(item queue.Item) bool {
	if strings.TrimSpace(item.Title) == "" {
		return false
	}
	base := strings.TrimSuffix(filepath.Base(item.Path), filepath.Ext(item.Path))
	return item.Title != base
}

// probeItems starts one asynchronous probe per eligible item. IDs already in
// flight are skipped so a mutation cannot schedule the same probe twice.
func (m *Model) probeItems() tea.Cmd {
	if m.prober == nil {
		return nil
	}
	if m.probing == nil {
		m.probing = make(map[uint64]string)
	}
	if m.probeSem == nil {
		m.probeSem = make(chan struct{}, probeConcurrency)
	}

	var cmds []tea.Cmd
	for _, item := range m.queue.Snapshot().Items {
		if _, inFlight := m.probing[item.ID]; inFlight || !m.needsProbe(item) {
			continue
		}
		m.probing[item.ID] = item.Path
		id, path, prober, sem := item.ID, item.Path, m.prober, m.probeSem
		cmds = append(cmds, func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()
			info, err := prober.Probe(context.Background(), path)
			return queueMetadataMsg{id: id, path: path, info: info, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// applyQueueMetadata applies non-zero probed fields to the item with the given
// ID. Results for removed, replaced, reused, or CUE items are ignored.
func (m *Model) applyQueueMetadata(msg queueMetadataMsg) tea.Cmd {
	// Clear the in-flight marker only when it still belongs to this probe;
	// Restore can reuse an ID for a different item.
	if path, ok := m.probing[msg.id]; ok && path == msg.path {
		delete(m.probing, msg.id)
	}
	if msg.err != nil {
		return nil
	}

	found := false
	for _, item := range m.queue.Snapshot().Items {
		if item.ID == msg.id && item.Path == msg.path && item.CuePath == "" {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	info := msg.info
	changed := false
	m.queue.Update(msg.id, func(item *queue.Item) {
		if info.Title != "" && item.Title != info.Title {
			item.Title = info.Title
			changed = true
		}
		if info.Artist != "" && item.Artist != info.Artist {
			item.Artist = info.Artist
			changed = true
		}
		if info.Album != "" && item.Album != info.Album {
			item.Album = info.Album
			changed = true
		}
		if info.TrackNo != 0 && item.TrackNo != info.TrackNo {
			item.TrackNo = info.TrackNo
			changed = true
		}
		if info.Duration != 0 && item.Duration != info.Duration {
			item.Duration = info.Duration
			changed = true
		}
	})
	if !changed {
		return nil
	}
	return m.saveQueue()
}

// restoreCmd loads the persisted queue asynchronously. The captured epoch lets
// the result be dropped when the user changed the queue while it was loading.
func (m *Model) restoreCmd() tea.Cmd {
	if m.store == nil {
		return nil
	}
	store := m.store
	epoch := m.queueEpoch
	return func() tea.Msg {
		active, tabs, skipped, err := store.LoadTabs()
		return queueRestoredMsg{active: active, tabs: tabs, skipped: skipped, err: err, epoch: epoch}
	}
}

// applyQueueRestored installs every loaded tab, selects the active tab's
// current item, and schedules probes and a save when items were pruned.
func (m *Model) applyQueueRestored(msg queueRestoredMsg) tea.Cmd {
	if msg.epoch != m.queueEpoch {
		m.setStatusOK("queue changed; restore skipped")
		return nil
	}
	if msg.err != nil {
		m.setStatusError("restore: " + msg.err.Error())
		return nil
	}
	if len(msg.tabs) > tabCount {
		m.setStatusError(fmt.Sprintf("restore: %d tabs exceeds maximum %d", len(msg.tabs), tabCount))
		return nil
	}

	// Validate all tabs into fresh queues before installing any of them. A bad
	// snapshot must not leave a half-restored model.
	restored := make([]*queue.Queue, tabCount)
	items := 0
	nonEmpty := 0
	for i := 0; i < tabCount; i++ {
		snapshot := queue.Snapshot{Items: []queue.Item{}, Current: -1}
		if i < len(msg.tabs) {
			snapshot = msg.tabs[i]
		}
		q := queue.New()
		if err := q.Restore(snapshot); err != nil {
			m.setStatusError(fmt.Sprintf("restore tab %d: %v", i, err))
			return nil
		}
		restored[i] = q
		items += q.Len()
		if q.Len() > 0 {
			nonEmpty++
		}
	}

	for i := range m.tabs {
		m.tabs[i].queue = restored[i]
		m.tabs[i].selected = queueCurrentIndex(restored[i])
		m.tabs[i].offset = m.tabs[i].selected
	}
	m.activeTab = msg.active
	if m.activeTab < 0 || m.activeTab >= tabCount {
		m.activeTab = 0
	}
	m.queue = m.tabs[m.activeTab].queue
	m.playlistSelected = m.tabs[m.activeTab].selected
	m.playlistOffset = m.tabs[m.activeTab].offset
	m.playbackTab = -1
	m.restorePending = false
	m.probing = make(map[uint64]string)
	m.clampPlaylist()

	status := fmt.Sprintf("restored %d item(s) in %d tab(s)", items, nonEmpty)
	if msg.skipped > 0 {
		status += fmt.Sprintf(", skipped %d", msg.skipped)
	}
	m.setStatusOK(status)

	var cmds []tea.Cmd
	if msg.skipped > 0 {
		cmds = append(cmds, m.saveQueue())
	}
	cmds = append(cmds, m.probeItems())
	return tea.Batch(cmds...)
}

// waitForPlayerEvent subscribes to the next player event. A nil player or a nil
// channel yields no command.
func (m *Model) waitForPlayerEvent() tea.Cmd {
	if m.player == nil {
		return nil
	}
	events := m.player.Events()
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-events
		return playerEventMsg{event: event, ok: ok}
	}
}

// handlePlayerEvent advances the sounding queue on a finished stream and
// reports errors. It never advances for anything else.
func (m *Model) handlePlayerEvent(msg playerEventMsg) tea.Cmd {
	if !msg.ok {
		return nil
	}
	if cmd, handled := m.handlePreviewEvent(msg); handled {
		return cmd
	}
	// Match against the sounding tab, not the visible one. Item IDs restart at
	// 1 in every queue, so comparing against the visible tab could consume an
	// event that belongs elsewhere.
	tab := m.playbackTab
	if tab < 0 || tab >= tabCount {
		return nil
	}
	q := m.tabs[tab].queue
	current, hasCurrent := q.Current()
	if !hasCurrent || msg.event.ItemID != current.ID {
		return nil
	}
	m.refresh()

	switch msg.event.Type {
	case player.EventFinished:
		previous, hadPrevious := q.Current()
		selected, hadSelected := m.selectedPlaylistItemFor(tab)
		item, ok := q.Next(true)
		if !ok {
			m.playbackTab = -1
			m.clampTabSelection(tab)
			m.setStatusOK(m.queueEndStatus(tab, "end of queue"))
			return nil
		}
		m.bumpQueueEpoch()
		m.followSelectionFor(tab, previous, selected, hadPrevious, hadSelected)
		return tea.Batch(m.playItem(tab, item, "playing "+displayTitle(item)), m.saveQueue())

	case player.EventError:
		err := msg.event.Err
		if err == nil {
			err = errors.New("playback error")
		}
		m.setStatusError("playback: " + err.Error())
	}
	return nil
}
