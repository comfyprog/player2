package tui

import (
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"player2/internal/config"
	"player2/internal/library"
	"player2/internal/player"
	"player2/internal/queue"
)

const (
	// Minimum supported terminal size.
	minWidth  = 60
	minHeight = 10

	// Layout rows: one header, two footer rows.
	headerRows = 1
	footerRows = 2

	// tickInterval drives the cached player status and status expiry.
	tickInterval = 250 * time.Millisecond

	// Initial size used before the first WindowSizeMsg arrives.
	defaultWidth  = 80
	defaultHeight = 24
)

// Pane identifies the focused pane.
type Pane int

// Panes.
const (
	PaneBrowser Pane = iota
	PanePlaylist
)

// browserLocation is a saved browser position used by the navigation stack.
type browserLocation struct {
	Path     string
	Selected int
	Offset   int
	Cue      bool
}

// Model is the Bubble Tea model for the player2 shell.
type Model struct {
	cfg     config.Config
	scanner Scanner
	player  Player
	store   QueueStore
	prober  Prober
	keys    KeyMap
	styles  Styles

	startDir   string
	startupErr error

	width  int
	height int
	focus  Pane
	help   bool

	// helpOffset is the first help binding row shown while the overlay is
	// open. It lets the full keymap be reached on short terminals.
	helpOffset int

	// now is injectable for deterministic status-expiry tests.
	now func() time.Time

	queue *queue.Queue

	// status caches the last player snapshot so rendering never calls into
	// the engine. statusMsg is a transient user-facing message.
	status        player.Status
	statusMsg     string
	statusExpiry  time.Time
	statusIsError bool

	// Browser pane state. Tasks 11 and 13 extend behavior; the fields are
	// intentionally kept here so the shell and later panes share one model.
	browserPath        string
	browserEntries     []library.Entry
	browserEntriesPath string
	browserSelected    int
	browserOffset      int
	browserStack       []browserLocation
	browserSearch      string
	browserFilter      string
	browserSearching   bool
	browserLoading     bool
	browserCue         bool
	browserGen         uint64

	// Playlist pane state.
	playlistSelected int
	playlistOffset   int
}

// tickMsg is delivered by the periodic status refresh.
type tickMsg time.Time

// Model implements the Bubble Tea model contract.
var _ tea.Model = (*Model)(nil)

// New constructs the shell. Invalid configuration is recorded in the model so
// View can render the error without panicking; callers that need a hard failure
// should call Validate first.
func New(opts Options) *Model {
	cfg := opts.Config
	m := &Model{
		cfg:       cfg,
		scanner:   opts.Scanner,
		player:    opts.Player,
		store:     opts.Store,
		prober:    opts.Prober,
		width:     defaultWidth,
		height:    defaultHeight,
		focus:     PaneBrowser,
		queue:     queue.New(),
		now:       time.Now,
		status:    player.Status{State: player.StateStopped, Volume: cfg.Volume},
		statusMsg: "ready",
		styles:    NewStyles(cfg.Colors),
	}

	keys, err := NewKeyMap(cfg)
	if err != nil {
		m.startupErr = err
		// Defaults are known-good; render and update remain safe.
		keys, _ = NewKeyMap(config.Default())
	}
	m.keys = keys

	startDir := opts.StartDir
	if startDir == "" {
		startDir = cfg.StartDir
	}
	if startDir == "" {
		startDir = "."
		if cwd, err := os.Getwd(); err == nil && cwd != "" {
			startDir = cwd
		}
	}
	m.startDir = startDir
	m.browserPath = startDir

	return m
}

// StartDir returns the initial browser root.
func (m *Model) StartDir() string { return m.startDir }

// Err returns the startup validation error, if any.
func (m *Model) Err() error { return m.startupErr }

// Init starts the periodic status tick and the initial browser load.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.tick()}
	if m.scanner != nil {
		cmds = append(cmds, m.loadBrowser(m.browserPath, m.browserCue, 0, 0, ""))
	}
	return tea.Batch(cmds...)
}

// tick schedules the next status refresh.
func (m *Model) tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles window, tick, and global key messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampBrowser()
		if m.help {
			m.clampHelpOffset()
		}
		return m, nil

	case tickMsg:
		m.refresh()
		return m, m.tick()

	case browserLoadedMsg:
		m.applyBrowserLoaded(msg)
		return m, nil

	case browserPlayMsg:
		m.applyBrowserPlay(msg)
		return m, nil

	case browserAddMsg:
		m.applyBrowserAdd(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// refresh updates the cached player status and expires stale messages.
func (m *Model) refresh() {
	if m.player != nil {
		m.status = m.player.Status()
	}
	if !m.statusExpiry.IsZero() && !m.currentTime().Before(m.statusExpiry) {
		m.statusMsg = ""
		m.statusExpiry = time.Time{}
		m.statusIsError = false
	}
}

// currentTime returns the model clock, falling back to the wall clock for a
// zero-value model.
func (m *Model) currentTime() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// setStatus sets a transient status message. A zero timeout keeps the message
// until it is replaced; a negative timeout places the deadline in the past so
// the next tick clears it (used by tests).
func (m *Model) setStatus(message string, timeout time.Duration) {
	m.statusMsg = message
	m.statusIsError = false
	if timeout == 0 {
		m.statusExpiry = time.Time{}
		return
	}
	m.statusExpiry = m.currentTime().Add(timeout)
}

// setStatusError shows a persistent error in the status line.
func (m *Model) setStatusError(message string) {
	m.setStatus(message, 0)
	m.statusIsError = true
}

// setStatusOK shows a persistent normal status message.
func (m *Model) setStatusOK(message string) {
	m.setStatus(message, 0)
	m.statusIsError = false
}

// handleKey dispatches global and pane-specific actions.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// The help overlay is modal: it swallows pane/transport actions while it
	// is open and only supports closing, quitting, and scrolling.
	if m.help {
		return m.handleHelpKey(key)
	}

	// Ctrl+C remains an escape hatch while the browser filter captures
	// printable keys.
	if m.browserSearching && m.focus == PaneBrowser && key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.browserSearching && m.focus == PaneBrowser {
		return m.handleBrowserSearchKey(msg)
	}

	action, ok := m.keys.Action(key)
	if !ok {
		return m, nil
	}

	switch action {
	case ActionQuit:
		return m, tea.Quit
	case ActionHelp:
		m.help = true
		m.helpOffset = 0
		return m, nil
	case ActionSwitchPane:
		if m.focus == PaneBrowser {
			m.focus = PanePlaylist
		} else {
			m.focus = PaneBrowser
		}
		return m, nil
	}

	if m.focus == PaneBrowser {
		return m, m.handleBrowserAction(action)
	}
	return m, nil
}

// handleHelpKey handles keys while the help overlay is open.
func (m *Model) handleHelpKey(key string) (tea.Model, tea.Cmd) {
	if key == "esc" || key == "escape" {
		m.help = false
		return m, nil
	}

	action, ok := m.keys.Action(key)
	if !ok {
		return m, nil
	}

	switch action {
	case ActionQuit:
		return m, tea.Quit
	case ActionHelp:
		m.help = false
	case ActionUp:
		m.scrollHelp(-1)
	case ActionDown:
		m.scrollHelp(1)
	case ActionPageUp:
		m.scrollHelp(-m.helpVisibleRows())
	case ActionPageDown:
		m.scrollHelp(m.helpVisibleRows())
	case ActionFirst:
		m.scrollHelp(-m.helpRows())
	case ActionLast:
		m.scrollHelp(m.helpRows())
	}
	return m, nil
}

// helpRows returns the number of two-column binding rows.
func (m *Model) helpRows() int {
	return (len(actionOrder) + 1) / 2
}

// helpVisibleRows returns how many binding rows fit under the overlay title.
func (m *Model) helpVisibleRows() int {
	return max(m.height-headerRows-footerRows-1, 1)
}

// scrollHelp moves the help offset by delta and clamps it to the available
// range.
func (m *Model) scrollHelp(delta int) {
	m.helpOffset += delta
	m.clampHelpOffset()
}

// clampHelpOffset keeps the help window within the binding rows.
func (m *Model) clampHelpOffset() {
	maxOffset := max(m.helpRows()-m.helpVisibleRows(), 0)
	if m.helpOffset < 0 {
		m.helpOffset = 0
	}
	if m.helpOffset > maxOffset {
		m.helpOffset = maxOffset
	}
}
