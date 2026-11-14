package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"player2/internal/library"
	"player2/internal/meta"
	"player2/internal/player"
	"player2/internal/queue"
)

// errPreviewPlayback is the fallback when the engine reports an error event
// without a cause.
var errPreviewPlayback = errors.New("preview playback")

// previewMetadataMsg carries probed metadata for the active preview item.
type previewMetadataMsg struct {
	id   uint64
	path string
	info meta.Info
	err  error
}

// browserPreview handles the preview action at the browser. Only playable
// tracks have a one-off preview; directories and playlists keep their normal
// open behavior.
func (m *Model) browserPreview() tea.Cmd {
	if m.browserLoading {
		return nil
	}
	entry, ok := m.browserSelectedEntry()
	if !ok {
		return nil
	}
	switch entry.Kind {
	case library.KindAudio, library.KindCueTrack:
		return m.startPreview(entry)
	default:
		return nil
	}
}

// startPreview starts one track outside the queue. The queue, its current
// item, shuffle/repeat settings, and persistence are left untouched.
func (m *Model) startPreview(entry library.Entry) tea.Cmd {
	if m.player == nil {
		return nil
	}
	item := queueItemFromEntry(entry)
	m.previewSeq++
	item.ID = ^uint64(0) - (m.previewSeq - 1)
	m.previewItem = item
	m.previewActive = true
	m.beginPlaybackIntent()

	play := m.requestTransport(transportAction{
		kind:    transportPlay,
		item:    item,
		status:  "previewing " + displayTitle(item),
		preview: true,
	})
	probe := m.previewProbeCmd(item)
	if play == nil && probe == nil {
		return nil
	}
	return tea.Batch(play, probe)
}

// previewProbeCmd probes a preview item that lacks usable metadata. Results
// update only the preview state and are never written to the queue.
func (m *Model) previewProbeCmd(item queue.Item) tea.Cmd {
	if m.prober == nil || !m.needsProbe(item) {
		return nil
	}
	if m.probeSem == nil {
		m.probeSem = make(chan struct{}, probeConcurrency)
	}
	id, path, prober, sem := item.ID, item.Path, m.prober, m.probeSem
	return func() tea.Msg {
		sem <- struct{}{}
		defer func() { <-sem }()
		info, err := prober.Probe(context.Background(), path)
		return previewMetadataMsg{id: id, path: path, info: info, err: err}
	}
}

// applyPreviewMetadata applies non-zero probed fields to the active preview.
// Results for a finished, replaced, or different-item preview are ignored.
func (m *Model) applyPreviewMetadata(msg previewMetadataMsg) {
	if !m.previewActive || m.previewItem.ID != msg.id || m.previewItem.Path != msg.path {
		return
	}
	if msg.err != nil {
		return
	}

	info := msg.info
	changed := false
	if info.Title != "" && m.previewItem.Title != info.Title {
		m.previewItem.Title = info.Title
		changed = true
	}
	if info.Artist != "" && m.previewItem.Artist != info.Artist {
		m.previewItem.Artist = info.Artist
		changed = true
	}
	if info.Album != "" && m.previewItem.Album != info.Album {
		m.previewItem.Album = info.Album
		changed = true
	}
	if info.TrackNo != 0 && m.previewItem.TrackNo != info.TrackNo {
		m.previewItem.TrackNo = info.TrackNo
		changed = true
	}
	if info.Duration != 0 && m.previewItem.Duration != info.Duration {
		m.previewItem.Duration = info.Duration
		changed = true
	}
	if changed && strings.HasPrefix(m.statusMsg, "previewing ") {
		m.setStatusOK("previewing " + displayTitle(m.previewItem))
	}
}

// handlePreviewEvent consumes player events while a preview is active. Queue
// events are ignored so a late event from the interrupted queue stream cannot
// advance playback over the preview.
func (m *Model) handlePreviewEvent(msg playerEventMsg) (tea.Cmd, bool) {
	if !m.previewActive {
		return nil, false
	}
	if msg.event.ItemID != m.previewItem.ID {
		return nil, true
	}

	m.refresh()
	switch msg.event.Type {
	case player.EventFinished:
		m.clearPreview()
		m.setStatusOK("preview finished")
	case player.EventError:
		err := msg.event.Err
		if err == nil {
			err = errPreviewPlayback
		}
		m.clearPreview()
		m.setStatusError("playback: " + err.Error())
	}
	return nil, true
}

// clearPreview forgets the one-off item. It does not stop the player; callers
// that need silence use stopPlayback.
func (m *Model) clearPreview() {
	m.previewActive = false
	m.previewItem = queue.Item{}
}

// playbackItem returns the item currently producing sound: the preview when
// one is active, otherwise the queue's current item.
func (m *Model) playbackItem() (queue.Item, bool) {
	if m.previewActive {
		return m.previewItem, true
	}
	return m.queue.Current()
}

// beginPlaybackIntent invalidates asynchronous level/playlist builds started
// before this request.
func (m *Model) beginPlaybackIntent() uint64 {
	m.playSeq++
	return m.playSeq
}

// stopPlayback stops the player and ends any preview. The serialized stop
// still runs outside Update.
func (m *Model) stopPlayback() tea.Cmd {
	m.clearPreview()
	m.beginPlaybackIntent()
	return m.requestTransport(transportAction{kind: transportStop})
}
