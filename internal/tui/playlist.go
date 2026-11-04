package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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

// transportPlayResultMsg reports the outcome of an asynchronous Play call made
// by a transport or playlist action.
type transportPlayResultMsg struct {
	status string
	err    error
}

// seekResultMsg reports the outcome of an asynchronous SeekBy call.
type seekResultMsg struct {
	delta time.Duration
	err   error
}

// queueRestoredMsg is the result of loading a saved queue. epoch records the
// queue generation when the load started so a late result cannot overwrite a
// queue changed in the meantime.
type queueRestoredMsg struct {
	snapshot queue.Snapshot
	skipped  int
	err      error
	epoch    uint64
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

// playlistBodyRows is the number of list rows available below the pane title.
func (m *Model) playlistBodyRows() int {
	return max(m.height-headerRows-footerRows-1, 1)
}

// playlistBounds clamps the selection and scroll offset for count items and
// rows visible rows, keeping the selection on screen.
func (m *Model) playlistBounds(count, rows int) (selected, offset int) {
	if count <= 0 || rows <= 0 {
		return 0, 0
	}
	selected = m.playlistSelected
	if selected < 0 {
		selected = 0
	}
	if selected >= count {
		selected = count - 1
	}

	offset = m.playlistOffset
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

// clampPlaylist keeps the selection and offset valid for the current queue and
// terminal size.
func (m *Model) clampPlaylist() {
	selected, offset := m.playlistBounds(len(m.queue.Snapshot().Items), m.playlistBodyRows())
	m.playlistSelected = selected
	m.playlistOffset = offset
}

// playlistLines renders the playlist pane's title and rows. Every returned line
// is exactly width cells once styled.
func (m *Model) playlistLines(width, height int) []string {
	lines := make([]string, height)
	if height == 0 {
		return lines
	}

	titleStyle := m.styles.Dim
	if m.focus == PanePlaylist {
		titleStyle = m.styles.PaneFocus
	}
	lines[0] = titleStyle.Render(fitPlain(m.paneTitle(PanePlaylist, width), width))

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
		lines[1] = m.styles.Dim.Render(fitPlain("Queue is empty", width))
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

// selectedPlaylistItem returns the item under the playlist selection.
func (m *Model) selectedPlaylistItem() (queue.Item, bool) {
	items := m.queue.Snapshot().Items
	if len(items) == 0 {
		return queue.Item{}, false
	}
	index := m.playlistSelected
	if index < 0 {
		index = 0
	}
	if index >= len(items) {
		index = len(items) - 1
	}
	return items[index], true
}

// queueIndex returns the index of id in the queue, or -1.
func (m *Model) queueIndex(id uint64) int {
	for index, item := range m.queue.Snapshot().Items {
		if item.ID == id {
			return index
		}
	}
	return -1
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

// followSelection applies the selection-follow rule: the selection moves to
// the new current item only when it was on the previous current item.
func (m *Model) followSelection(previous, selected queue.Item, hadPrevious, hadSelected bool) {
	if hadPrevious && hadSelected && previous.ID == selected.ID {
		m.selectCurrent()
		return
	}
	m.clampPlaylist()
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

// playItem returns a command that starts playback. Play opens a decoder and may
// block, so it never runs inside Update.
func (m *Model) playItem(item queue.Item, status string) tea.Cmd {
	if m.player == nil {
		return nil
	}
	player := m.player
	return func() tea.Msg {
		return transportPlayResultMsg{status: status, err: player.Play(item)}
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
	return m.playItem(item, "playing "+displayTitle(item))
}

// actionStop stops playback.
func (m *Model) actionStop() tea.Cmd {
	if m.player != nil {
		m.player.Stop()
		m.refresh()
	}
	m.setStatusOK("stopped")
	return nil
}

// actionNext advances the queue and plays the next item.
func (m *Model) actionNext() tea.Cmd {
	previous, hadPrevious := m.queue.Current()
	selected, hadSelected := m.selectedPlaylistItem()
	item, ok := m.queue.Next(false)
	if !ok {
		m.clampPlaylist()
		m.setStatusOK("end of queue")
		return nil
	}
	m.bumpQueueEpoch()
	m.followSelection(previous, selected, hadPrevious, hadSelected)
	return tea.Batch(m.playItem(item, "playing "+displayTitle(item)), m.saveQueue())
}

// actionPrevious moves back in the queue and plays the previous item.
func (m *Model) actionPrevious() tea.Cmd {
	previous, hadPrevious := m.queue.Current()
	selected, hadSelected := m.selectedPlaylistItem()
	item, ok := m.queue.Prev()
	if !ok {
		m.clampPlaylist()
		m.setStatusOK("start of queue")
		return nil
	}
	m.bumpQueueEpoch()
	m.followSelection(previous, selected, hadPrevious, hadSelected)
	return tea.Batch(m.playItem(item, "playing "+displayTitle(item)), m.saveQueue())
}

// actionSeek returns a command that seeks by delta.
func (m *Model) actionSeek(delta time.Duration) tea.Cmd {
	if m.player == nil {
		return nil
	}
	player := m.player
	return func() tea.Msg {
		return seekResultMsg{delta: delta, err: player.SeekBy(delta)}
	}
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
	m.bumpQueueEpoch()
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
	m.bumpQueueEpoch()
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
	case ActionGotoPlaying:
		m.selectCurrent()
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
	return tea.Batch(m.playItem(item, "playing "+displayTitle(item)), m.saveQueue())
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
	if hadPrevious && removed.ID == previous.ID {
		if m.player != nil {
			m.player.Stop()
			m.refresh()
		}
	}
	m.clampPlaylist()
	m.setStatusOK("removed " + displayTitle(removed))
	return m.saveQueue()
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
	if m.player != nil {
		m.player.Stop()
		m.refresh()
	}
	m.playlistSelected = 0
	m.playlistOffset = 0
	m.probing = make(map[uint64]string)
	m.setStatusOK("queue cleared")
	return m.saveQueue()
}

// bumpQueueEpoch records a user/browser queue mutation so a late restore is
// ignored instead of clobbering the newer queue.
func (m *Model) bumpQueueEpoch() {
	m.queueEpoch++
}

// applyTransportPlayResult applies the outcome of an asynchronous Play call.
func (m *Model) applyTransportPlayResult(msg transportPlayResultMsg) {
	if msg.err != nil {
		m.setStatusError("play: " + msg.err.Error())
		return
	}
	m.refresh()
	status := msg.status
	if status == "" {
		status = "playing"
	}
	m.setStatusOK(status)
}

// applySeekResult reports a seek failure and refreshes the cached status.
func (m *Model) applySeekResult(msg seekResultMsg) {
	if msg.err != nil {
		m.setStatusError("seek: " + msg.err.Error())
		return
	}
	m.refresh()
	m.setStatusOK(fmt.Sprintf("seek %+ds", int(msg.delta/time.Second)))
}

// saveQueue persists the current queue with single-flight coalescing. A
// mutation while a save is in flight marks the queue dirty so the latest
// snapshot is written afterwards.
func (m *Model) saveQueue() tea.Cmd {
	if m.store == nil {
		return nil
	}
	if m.saving {
		m.saveDirty = true
		return nil
	}
	m.saving = true
	snapshot := m.queue.Snapshot()
	store := m.store
	return func() tea.Msg {
		m.saveMu.Lock()
		defer m.saveMu.Unlock()
		if m.flushed {
			// An exit-time flush has already persisted the newest snapshot;
			// this older async save must not overwrite it.
			return queueSavedMsg{}
		}
		return queueSavedMsg{err: store.Save(snapshot)}
	}
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
	snapshot := m.queue.Snapshot()
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.flushed = true
	return m.store.Save(snapshot)
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

	var cmds []tea.Cmd
	for _, item := range m.queue.Snapshot().Items {
		if _, inFlight := m.probing[item.ID]; inFlight || !m.needsProbe(item) {
			continue
		}
		m.probing[item.ID] = item.Path
		id, path, prober := item.ID, item.Path, m.prober
		cmds = append(cmds, func() tea.Msg {
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
		snapshot, skipped, err := store.Load()
		return queueRestoredMsg{snapshot: snapshot, skipped: skipped, err: err, epoch: epoch}
	}
}

// applyQueueRestored installs a loaded queue, selects its current item, and
// schedules probes and a save when items were pruned.
func (m *Model) applyQueueRestored(msg queueRestoredMsg) tea.Cmd {
	if msg.epoch != m.queueEpoch {
		m.setStatusOK("queue changed; restore skipped")
		return nil
	}
	if msg.err != nil {
		m.setStatusError("restore: " + msg.err.Error())
		return nil
	}
	if err := m.queue.Restore(msg.snapshot); err != nil {
		m.setStatusError("restore: " + err.Error())
		return nil
	}

	m.probing = make(map[uint64]string)
	m.selectCurrent()

	status := fmt.Sprintf("restored %d item(s)", len(msg.snapshot.Items))
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

// handlePlayerEvent advances on a finished stream and reports errors. It never
// advances for anything else.
func (m *Model) handlePlayerEvent(msg playerEventMsg) tea.Cmd {
	if !msg.ok {
		return nil
	}
	m.refresh()

	switch msg.event.Type {
	case player.EventFinished:
		previous, hadPrevious := m.queue.Current()
		selected, hadSelected := m.selectedPlaylistItem()
		item, ok := m.queue.Next(true)
		if !ok {
			m.clampPlaylist()
			m.setStatusOK("end of queue")
			return nil
		}
		m.bumpQueueEpoch()
		m.followSelection(previous, selected, hadPrevious, hadSelected)
		return tea.Batch(m.playItem(item, "playing "+displayTitle(item)), m.saveQueue())

	case player.EventError:
		err := msg.event.Err
		if err == nil {
			err = errors.New("playback error")
		}
		m.setStatusError("playback: " + err.Error())
	}
	return nil
}
