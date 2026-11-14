package tui

import (
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"player2/internal/config"
	"player2/internal/library"
	"player2/internal/meta"
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

	// tabCount is the fixed number of playlist tabs. It matches the store's
	// schema limit and the alt+0..alt+9 default bindings.
	tabCount = 10

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

// playlistTab is one independent playlist slot. selected and offset preserve
// its view position while another tab is visible.
type playlistTab struct {
	queue    *queue.Queue
	name     string
	selected int
	offset   int
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
	zoomed bool

	// prompt is the single modal (text input or confirmation) currently
	// capturing keys.
	prompt promptState

	// info is the read-only track-info modal. infoSeq invalidates a probe
	// result whose window has since been closed or reopened.
	info    infoState
	infoSeq uint64

	// helpOffset is the first help binding row shown while the overlay is
	// open. It lets the full keymap be reached on short terminals.
	helpOffset int

	// now is injectable for deterministic status-expiry tests.
	now func() time.Time

	// tabs holds every playlist slot. activeTab is the visible slot and queue
	// aliases its queue for the playlist actions. playbackTab is the slot that
	// owns the current stream, or -1 when no queue owns playback.
	tabs        [tabCount]playlistTab
	activeTab   int
	playbackTab int
	playbackGen uint64
	queue       *queue.Queue

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
	browserShowTags    bool
	browserMeta        map[string]meta.Info
	browserProbing     map[browserProbeKey]struct{}

	// Playlist pane state.
	playlistSelected int
	playlistOffset   int

	// lastVolume restores the previous level after a mute toggle.
	lastVolume int

	// Queue persistence is single-flight; saveDirty records a mutation that
	// arrived while a save was in flight. saveMu serializes store writes so an
	// exit-time FlushQueue cannot be overtaken by an older async save; flushed
	// makes saves that have not started yet skip after the exit flush.
	saving    bool
	saveDirty bool
	saveMu    sync.Mutex
	flushed   bool

	// probing tracks metadata probes currently in flight, keyed by tab and
	// item ID; the value is the probed path so reused IDs cannot be mistaken.
	probing map[probeKey]string

	// probeSem bounds concurrent metadata probes. Bubble Tea runs each command
	// in its own goroutine, so a shared semaphore prevents a large add/restore
	// from spawning one ffprobe per item at once.
	probeSem chan struct{}

	// Transport operations that change playback (Play, Seek, Stop) are
	// serialized. transportBusy records that one is running; pendingTransport
	// holds the single latest request made while it was busy. transportSeq
	// identifies the latest request so a stale result cannot overwrite newer
	// status.
	transportBusy    bool
	pendingTransport *transportAction
	transportSeq     uint64

	// queueEpoch increments on every user/browser queue mutation. A restore
	// started earlier is dropped if the queue changed while it was loading.
	queueEpoch uint64

	// restorePending records that the startup queue restore has not completed
	// successfully. FlushQueue must leave the on-disk queue alone while it is
	// true so an early exit cannot overwrite a valid saved queue with an empty
	// in-memory one.
	restorePending bool

	// preview is one-off playback of a browser track outside the queue.
	// previewSeq hands out IDs from the top of the uint64 range so a preview
	// item cannot collide with a queue item. playSeq identifies the latest
	// playback intent so an older asynchronous level/playlist build cannot
	// replace a newer preview or transport request.
	previewActive bool
	previewItem   queue.Item
	previewSeq    uint64
	playSeq       uint64
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
		cfg:            cfg,
		scanner:        opts.Scanner,
		player:         opts.Player,
		store:          opts.Store,
		prober:         opts.Prober,
		width:          defaultWidth,
		height:         defaultHeight,
		focus:          PaneBrowser,
		playbackTab:    -1,
		now:            time.Now,
		status:         player.Status{State: player.StateStopped, Volume: cfg.Volume},
		lastVolume:     cfg.Volume,
		statusMsg:      "ready",
		styles:         NewStyles(cfg.Colors),
		probing:        make(map[probeKey]string),
		browserMeta:    make(map[string]meta.Info),
		browserProbing: make(map[browserProbeKey]struct{}),
		probeSem:       make(chan struct{}, probeConcurrency),

		// Restore is asynchronous, so until it succeeds (or the user changes
		// the queue) exit must not overwrite the persisted queue.
		restorePending: cfg.RestoreQueue && opts.Store != nil,
	}

	for i := range m.tabs {
		m.tabs[i].queue = queue.New()
	}
	m.queue = m.tabs[0].queue

	// The engine defaults to 70; apply the configured level before the first
	// render so the header and the output device agree.
	if opts.Player != nil {
		opts.Player.SetVolume(cfg.Volume)
		m.status.Volume = cfg.Volume
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

// Init starts the periodic status tick, the initial browser load, an optional
// queue restore, and the player event subscription.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.tick()}
	if m.scanner != nil {
		cmds = append(cmds, m.loadBrowser(m.browserPath, m.browserCue, 0, 0, ""))
	}
	if m.cfg.RestoreQueue && m.store != nil {
		cmds = append(cmds, m.restoreCmd())
	}
	if m.player != nil {
		cmds = append(cmds, m.waitForPlayerEvent())
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
		m.clampPlaylist()
		if m.promptOpen() {
			m.prompt.input.SetWidth(m.promptInputWidth())
		}
		if m.help {
			m.clampHelpOffset()
		}
		if m.infoOpen() {
			m.clampInfoOffset()
		}
		return m, nil

	case tickMsg:
		m.refresh()
		return m, m.tick()

	case browserLoadedMsg:
		return m, m.applyBrowserLoaded(msg)

	case browserMetadataMsg:
		return m, m.applyBrowserMetadata(msg)

	case browserPlayMsg:
		return m, m.applyBrowserPlay(msg)

	case browserAddMsg:
		return m, m.applyBrowserAdd(msg)

	case previewMetadataMsg:
		m.applyPreviewMetadata(msg)
		return m, nil

	case playerEventMsg:
		if !msg.ok {
			return m, nil
		}
		return m, tea.Batch(m.handlePlayerEvent(msg), m.waitForPlayerEvent())

	case transportResultMsg:
		return m, m.applyTransportResult(msg)

	case queueRestoredMsg:
		return m, m.applyQueueRestored(msg)

	case queueSavedMsg:
		return m, m.applyQueueSaved(msg)

	case queueMetadataMsg:
		return m, m.applyQueueMetadata(msg)

	case playlistSavedMsg:
		m.applyPlaylistSaved(msg)
		return m, nil

	case trackInfoMsg:
		return m, m.applyTrackInfo(msg)

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

	// A prompt is the most modal surface: it sees keys before help, search,
	// and pane actions.
	if m.promptOpen() {
		return m.handlePromptKey(msg)
	}

	// The info window is modal: it captures scrolling and closing before help,
	// search, pane, and transport actions.
	if m.infoOpen() {
		return m.handleInfoKey(key)
	}

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
		m.zoomed = false
		m.clampPlaylist()
		return m, nil
	case ActionToggleZoom:
		m.zoomed = !m.zoomed
		return m, nil
	case ActionSavePlaylist:
		return m, m.openSavePrompt()
	case ActionNameTab:
		return m, m.openNamePrompt()
	}

	if index, ok := tabActionIndex(action); ok {
		return m, m.switchTab(index)
	}

	if cmd, handled := m.handleTransportAction(action); handled {
		return m, cmd
	}

	if m.focus == PaneBrowser {
		return m, m.handleBrowserAction(action)
	}
	return m, m.handlePlaylistAction(action)
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
