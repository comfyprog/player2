package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/playlist"
)

const defaultPlaylistName = "playlist.m3u"

// promptMode identifies the single modal currently owned by the model.
type promptMode int

const (
	promptNone promptMode = iota
	promptSavePlaylist
	promptNameTab
	promptConfirmOverwrite
	promptConfirmClear
)

// promptState is either a text-input prompt or a yes/no confirmation. Only one
// modal can be active at a time.
type promptState struct {
	mode    promptMode
	title   string
	message string
	hint    string
	input   textinput.Model

	// target is the resolved save path an overwrite confirmation will write.
	target string
	// dir is the browser directory captured when the save prompt opened.
	dir string
	// tab is the playlist tab captured when the name prompt opened.
	tab int
}

// playlistSavedMsg is the result of writing the queue as an M3U playlist.
type playlistSavedMsg struct {
	path  string
	count int
	err   error
}

// promptOpen reports whether a modal is capturing input.
func (m *Model) promptOpen() bool { return m.prompt.mode != promptNone }

// promptInputWidth leaves room for the textinput prompt prefix.
func (m *Model) promptInputWidth() int {
	return max(m.width-2, 1)
}

// newPromptInput builds an end-positioned text input.
func (m *Model) newPromptInput(value string) textinput.Model {
	input := textinput.New()
	input.SetValue(value)
	input.CursorEnd()
	input.SetWidth(m.promptInputWidth())
	return input
}

// openSavePrompt starts the `w` flow from either pane. The default filename is
// relative to the current browser directory and is shown as a placeholder so
// typing replaces it instead of appending to it.
func (m *Model) openSavePrompt() tea.Cmd {
	dir := m.saveDirectory()
	input := m.newPromptInput("")
	input.Placeholder = defaultPlaylistName
	m.prompt = promptState{
		mode:    promptSavePlaylist,
		title:   "Save playlist",
		message: fmt.Sprintf("Directory: %s", abbreviateHome(dir)),
		hint:    "Enter save — Esc cancel",
		input:   input,
		dir:     dir,
	}
	return m.prompt.input.Focus()
}

// openNamePrompt starts the name flow for the active playlist tab. The current
// name is editable in place so Enter without changes is a no-op and clearing
// the input removes the name.
func (m *Model) openNamePrompt() tea.Cmd {
	tab := m.activeTab
	input := m.newPromptInput(m.tabs[tab].name)
	input.Placeholder = "unnamed"
	m.prompt = promptState{
		mode:    promptNameTab,
		title:   "Name tab",
		message: fmt.Sprintf("Tab %d — empty clears the name", tab),
		hint:    "Enter save — Esc cancel",
		input:   input,
		tab:     tab,
	}
	return m.prompt.input.Focus()
}

// saveDirectory returns the directory a relative playlist path resolves
// against: the CUE file's directory, a selected file's directory, or the
// browser directory itself.
func (m *Model) saveDirectory() string {
	if m.browserCue {
		return filepath.Dir(m.browserPath)
	}
	if info, err := os.Stat(m.browserPath); err == nil && !info.IsDir() {
		return filepath.Dir(m.browserPath)
	}
	if m.browserPath == "" {
		return m.startDir
	}
	return m.browserPath
}

// handlePromptKey routes keys to the active modal. Only Esc, Enter, and Ctrl+C
// are global while a text-input prompt is open; a confirmation swallows every
// other key.
func (m *Model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.prompt.mode {
	case promptSavePlaylist:
		switch key {
		case "esc", "escape":
			m.prompt = promptState{}
			return m, nil
		case "enter":
			return m.submitSavePrompt()
		}
		input, cmd := m.prompt.input.Update(msg)
		m.prompt.input = input
		return m, cmd

	case promptNameTab:
		switch key {
		case "esc", "escape":
			m.prompt = promptState{}
			return m, nil
		case "enter":
			return m.submitNamePrompt()
		}
		input, cmd := m.prompt.input.Update(msg)
		m.prompt.input = input
		return m, cmd

	case promptConfirmOverwrite:
		switch key {
		case "y", "Y":
			target := m.prompt.target
			m.prompt = promptState{}
			return m, m.savePlaylistCmd(target)
		case "enter", "n", "N", "esc", "escape":
			m.prompt = promptState{}
		}
		return m, nil

	case promptConfirmClear:
		switch key {
		case "y", "Y":
			m.prompt = promptState{}
			return m, m.playlistClear()
		case "enter", "n", "N", "esc", "escape":
			m.prompt = promptState{}
		}
		return m, nil
	}
	return m, nil
}

// submitSavePrompt resolves the typed path, refusing directories and asking
// before overwriting an existing file.
func (m *Model) submitSavePrompt() (tea.Model, tea.Cmd) {
	raw := strings.TrimSpace(m.prompt.input.Value())
	if raw == "" {
		raw = defaultPlaylistName
	}

	path := raw
	if !filepath.IsAbs(path) {
		dir := m.prompt.dir
		if dir == "" {
			dir = m.saveDirectory()
		}
		path = filepath.Join(dir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		m.setStatusError(fmt.Sprintf("save %s: %v", abbreviateHome(path), err))
		return m, nil
	}
	abs = filepath.Clean(abs)

	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			m.setStatusError(fmt.Sprintf("save: %s is a directory", abbreviateHome(abs)))
			return m, nil
		}
		m.prompt = promptState{
			mode:    promptConfirmOverwrite,
			title:   "Overwrite playlist",
			message: fmt.Sprintf("Overwrite %s? [y/N]", abbreviateHome(abs)),
			hint:    "y confirm — n/Esc cancel",
			target:  abs,
		}
		return m, nil
	}

	m.prompt = promptState{}
	return m, m.savePlaylistCmd(abs)
}

// submitNamePrompt applies the typed name to the tab captured when the prompt
// opened. Empty and whitespace-only input clears the name.
func (m *Model) submitNamePrompt() (tea.Model, tea.Cmd) {
	tab := m.prompt.tab
	name := strings.TrimSpace(oneLine(m.prompt.input.Value()))
	m.prompt = promptState{}

	if tab < 0 || tab >= tabCount {
		m.setStatusError("name tab: invalid tab")
		return m, nil
	}
	if m.tabs[tab].name == name {
		m.setStatusOK("tab name unchanged")
		return m, nil
	}

	m.tabs[tab].name = name
	if name == "" {
		m.setStatusOK(fmt.Sprintf("tab %d name cleared", tab))
	} else {
		m.setStatusOK(fmt.Sprintf("tab %d named %q", tab, name))
	}
	return m, m.saveQueue()
}

// savePlaylistCmd writes the current queue snapshot as an M3U playlist. File
// I/O runs in a command so Update never blocks.
func (m *Model) savePlaylistCmd(path string) tea.Cmd {
	items := m.queue.Snapshot().Items
	count := len(items)
	return func() tea.Msg {
		return playlistSavedMsg{path: path, count: count, err: playlist.Write(path, items)}
	}
}

// applyPlaylistSaved reports the outcome of an asynchronous playlist write.
func (m *Model) applyPlaylistSaved(msg playlistSavedMsg) {
	if msg.err != nil {
		m.setStatusError(fmt.Sprintf("save %s: %v", abbreviateHome(msg.path), msg.err))
		return
	}
	m.setStatusOK(fmt.Sprintf("saved %d item(s) to %s", msg.count, abbreviateHome(msg.path)))
}

// requestClear opens the clear-queue confirmation. An empty queue has nothing
// to confirm.
func (m *Model) requestClear() tea.Cmd {
	if len(m.queue.Snapshot().Items) == 0 {
		m.setStatusOK("queue is empty")
		return nil
	}
	m.prompt = promptState{
		mode:    promptConfirmClear,
		title:   "Clear queue",
		message: "Clear queue? [y/N]",
		hint:    "y confirm — n/Esc cancel",
	}
	return nil
}

// promptLines renders the active modal as a width-safe body replacing the
// panes. It always returns exactly height lines.
func (m *Model) promptLines(height int) []string {
	if height <= 0 {
		return nil
	}

	lines := []string{
		m.styles.PaneFocus.Render(fitPlain(m.prompt.title, m.width)),
	}

	if height > 1 {
		lines = append(lines, m.styles.Fg.Render(fitPlain(m.prompt.message, m.width)))
	}
	if height > 2 && (m.prompt.mode == promptSavePlaylist || m.prompt.mode == promptNameTab) {
		// textinput renders its own cursor and prompt prefix; it is already
		// constrained to m.width-2 cells, so the finished line is width-safe.
		lines = append(lines, fitStyled(m.prompt.input.View(), m.width))
	}
	if height > len(lines) {
		lines = append(lines, m.styles.Dim.Render(fitPlain(m.prompt.hint, m.width)))
	}

	blank := strings.Repeat(" ", m.width)
	for len(lines) < height {
		lines = append(lines, blank)
	}
	return lines[:height]
}

// fitStyled pads or truncates text that may contain ANSI styling while keeping
// its visible width exactly width cells.
func fitStyled(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = strings.ReplaceAll(text, "\n", " ")
	text = ansi.Truncate(text, width, "…")
	if padding := width - ansi.StringWidth(text); padding > 0 {
		text += strings.Repeat(" ", padding)
	}
	return text
}
