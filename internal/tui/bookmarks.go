package tui

import (
	"fmt"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"player2/internal/library"
)

// bookmarksSavedMsg reports the result of one asynchronous bookmark save.
type bookmarksSavedMsg struct {
	err error
}

// normalizeBookmarks copies paths, dropping empty entries and exact duplicates
// while preserving the original order.
func normalizeBookmarks(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized
}

// addBookmark appends the current browser directory to the bookmark list.
func (m *Model) addBookmark() tea.Cmd {
	if m.bookmarkLoadErr != nil {
		m.setStatusError("bookmarks unavailable: " + m.bookmarkLoadErr.Error())
		return nil
	}
	if m.browserCue {
		m.setStatusError("cannot bookmark a CUE folder")
		return nil
	}
	if m.browserPath == "" || m.browserEntriesPath != m.browserPath {
		m.setStatusError("cannot bookmark this location")
		return nil
	}

	path := filepath.Clean(m.browserPath)
	for _, existing := range m.bookmarks {
		if existing == path {
			m.setStatusOK("already bookmarked " + abbreviateHome(path))
			return nil
		}
	}

	m.bookmarks = append(m.bookmarks, path)
	m.bookmarksDirty = true
	m.setStatusOK("bookmarked " + abbreviateHome(path))
	return m.saveBookmarks()
}

// showBookmarks replaces the browser listing with the bookmark list and
// remembers where to return. A second toggle or Backspace leaves the list.
func (m *Model) showBookmarks() {
	m.bookmarkReturn = browserLocation{
		Path:     m.browserPath,
		Selected: m.browserSelected,
		Offset:   m.browserOffset,
		Cue:      m.browserCue,
	}
	m.browserBookmarks = true
	// Invalidate a directory load that was already in flight so it cannot
	// replace the rows while the bookmark view is visible.
	m.browserGen++
	m.browserLoading = false
	m.clearBrowserFilter()
	m.browserSelected = 0
	m.browserOffset = 0
	m.clampBrowser()
}

// closeBookmarks restores the directory that opened the bookmark view.
func (m *Model) closeBookmarks() tea.Cmd {
	location := m.bookmarkReturn
	m.browserBookmarks = false
	m.bookmarkReturn = browserLocation{}
	m.clearBrowserFilter()
	return m.loadBrowser(location.Path, location.Cue, location.Selected, location.Offset, "")
}

// toggleBookmarks opens the bookmark list, or closes it when already open.
func (m *Model) toggleBookmarks() tea.Cmd {
	if m.browserBookmarks {
		return m.closeBookmarks()
	}
	m.showBookmarks()
	return nil
}

// openBookmark jumps to a bookmarked directory. A bookmark is a fresh
// navigation root: the previous history is dropped so Backspace goes to the
// bookmarked directory's filesystem parent.
func (m *Model) openBookmark(path string) tea.Cmd {
	m.browserBookmarks = false
	m.bookmarkReturn = browserLocation{}
	m.browserStack = nil
	m.clearBrowserFilter()
	return m.loadBrowser(path, false, 0, 0, "")
}

// removeBookmark deletes the selected bookmark and persists the new list.
func (m *Model) removeBookmark() tea.Cmd {
	if m.bookmarkLoadErr != nil {
		m.setStatusError("bookmarks unavailable: " + m.bookmarkLoadErr.Error())
		return nil
	}
	if !m.browserBookmarks {
		return nil
	}
	entry, ok := m.browserSelectedEntry()
	if !ok {
		m.setStatusOK("no bookmark selected")
		return nil
	}

	index := -1
	for i, path := range m.bookmarks {
		if path == entry.Path {
			index = i
			break
		}
	}
	if index < 0 {
		m.setStatusOK("no bookmark selected")
		return nil
	}

	m.bookmarks = append(m.bookmarks[:index], m.bookmarks[index+1:]...)
	m.bookmarksDirty = true
	m.clampBrowser()
	m.setStatusOK("removed bookmark " + abbreviateHome(entry.Path))
	return m.saveBookmarks()
}

// bookmarkEntries converts the stored paths into synthetic directory entries
// so the existing selection, scrolling, filtering, and render paths apply.
func (m *Model) bookmarkEntries() []library.Entry {
	entries := make([]library.Entry, 0, len(m.bookmarks))
	for _, path := range m.bookmarks {
		entries = append(entries, library.Entry{
			Kind: library.KindDir,
			Name: abbreviateHome(path),
			Path: path,
		})
	}
	return entries
}

// bookmarkListTitle returns the bookmark view title, including the entry count.
func (m *Model) bookmarkListTitle() string {
	return fmt.Sprintf("Bookmarks: %d", len(m.bookmarks))
}

// saveBookmarks persists a snapshot of the bookmark list with single-flight
// coalescing. A change while a save is in flight marks the list for a follow-up
// write of the latest snapshot.
func (m *Model) saveBookmarks() tea.Cmd {
	if m.bookmarkStore == nil || m.bookmarkLoadErr != nil {
		return nil
	}
	if m.bookmarkSaving {
		m.bookmarkResave = true
		return nil
	}
	m.bookmarkSaving = true
	paths := append([]string(nil), m.bookmarks...)
	store := m.bookmarkStore
	return func() tea.Msg {
		m.bookmarkSaveMu.Lock()
		defer m.bookmarkSaveMu.Unlock()
		if m.bookmarksFlushed {
			// The exit-time flush already persisted the newest snapshot.
			return bookmarksSavedMsg{}
		}
		return bookmarksSavedMsg{err: store.Save(paths)}
	}
}

// applyBookmarksSaved finishes one save and starts the coalesced follow-up.
func (m *Model) applyBookmarksSaved(msg bookmarksSavedMsg) tea.Cmd {
	m.bookmarkSaving = false
	if msg.err != nil {
		m.setStatusError("bookmarks: " + msg.err.Error())
	} else if !m.bookmarkResave {
		m.bookmarksDirty = false
	}
	if m.bookmarkResave {
		m.bookmarkResave = false
		return m.saveBookmarks()
	}
	return nil
}

// FlushBookmarks synchronously saves bookmark changes that are pending or in
// flight. A clean list is left untouched. It shares bookmarkSaveMu with the
// asynchronous commands so an older save cannot overwrite the exit-time state.
func (m *Model) FlushBookmarks() error {
	if m.bookmarkStore == nil || m.bookmarkLoadErr != nil {
		return nil
	}
	if !m.bookmarkSaving && !m.bookmarksDirty {
		return nil
	}

	paths := append([]string(nil), m.bookmarks...)
	m.bookmarkSaveMu.Lock()
	defer m.bookmarkSaveMu.Unlock()
	m.bookmarksFlushed = true
	if err := m.bookmarkStore.Save(paths); err != nil {
		return err
	}
	m.bookmarksDirty = false
	return nil
}
