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
// Task 11 fills in traversal; the shape lives here so the model is stable.
type browserLocation struct {
	Path     string
	Selected int
	Offset   int
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

	queue *queue.Queue

	// status caches the last player snapshot so rendering never calls into
	// the engine. statusMsg is a transient user-facing message.
	status       player.Status
	statusMsg    string
	statusExpiry time.Time

	// Browser pane state. Tasks 11 and 13 extend behavior; the fields are
	// intentionally kept here so the shell and later panes share one model.
	browserPath     string
	browserEntries  []library.Entry
	browserSelected int
	browserOffset   int
	browserStack    []browserLocation
	browserSearch   string
	browserFilter   string
	browserLoading  bool
	browserGen      uint64

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
		if cwd, err := os.Getwd(); err == nil {
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

// Init starts the periodic status tick.
func (m *Model) Init() tea.Cmd { return m.tick() }

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
		return m, nil

	case tickMsg:
		m.refresh()
		return m, m.tick()

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
	if !m.statusExpiry.IsZero() && !time.Now().Before(m.statusExpiry) {
		m.statusMsg = ""
		m.statusExpiry = time.Time{}
	}
}

// setStatus sets a transient status message. A zero timeout keeps the message
// until it is replaced; a negative timeout places the deadline in the past so
// the next tick clears it (used by tests).
func (m *Model) setStatus(message string, timeout time.Duration) {
	m.statusMsg = message
	if timeout == 0 {
		m.statusExpiry = time.Time{}
		return
	}
	m.statusExpiry = time.Now().Add(timeout)
}

// handleKey dispatches global actions. Pane-specific actions are intentionally
// left unhandled for Tasks 11 and 12.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "esc" || key == "escape" {
		if m.help {
			m.help = false
		}
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
		m.help = !m.help
	case ActionSwitchPane:
		if m.focus == PaneBrowser {
			m.focus = PanePlaylist
		} else {
			m.focus = PaneBrowser
		}
	}
	return m, nil
}
