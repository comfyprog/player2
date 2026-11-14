package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"player2/internal/config"
	"player2/internal/library"
	"player2/internal/meta"
	"player2/internal/player"
	"player2/internal/queue"
)

// fakePlayer is an in-memory Player implementation used by model and view
// tests. It never opens an audio device.
type fakePlayer struct {
	mu         sync.Mutex
	status     player.Status
	generation uint64
	events     chan player.Event
	played     []queue.Item
	pauses     int
	stops      int
	volumes    []int
	seekDeltas []time.Duration
	seekErr    error
	closed     bool
}

func newFakePlayer() *fakePlayer {
	return &fakePlayer{
		status: player.Status{State: player.StateStopped, Volume: 70},
		events: make(chan player.Event),
	}
}

func (p *fakePlayer) Play(item queue.Item) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.played = append(p.played, item)
	p.generation++
	p.status.Generation = p.generation
	p.status.State = player.StatePlaying
	p.status.Position = 0
	return nil
}

func (p *fakePlayer) TogglePause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pauses++
	switch p.status.State {
	case player.StatePlaying:
		p.status.State = player.StatePaused
	case player.StatePaused:
		p.status.State = player.StatePlaying
	}
}

func (p *fakePlayer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	p.generation++
	p.status.Generation = p.generation
	p.status.State = player.StateStopped
	p.status.Position = 0
}

func (p *fakePlayer) SeekBy(delta time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seekDeltas = append(p.seekDeltas, delta)
	if p.seekErr == nil {
		p.generation++
		p.status.Generation = p.generation
	}
	return p.seekErr
}

func (p *fakePlayer) SetVolume(percent int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.volumes = append(p.volumes, percent)
	p.status.Volume = percent
}

func (p *fakePlayer) Status() player.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *fakePlayer) Events() <-chan player.Event { return p.events }

func (p *fakePlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.events)
	}
	return nil
}

// fakeScanner returns no entries; the browser is implemented in Task 11.
type fakeScanner struct{}

func (fakeScanner) ReadDir(context.Context, string) ([]library.Entry, error) {
	return nil, nil
}

func (fakeScanner) ReadCue(context.Context, string) ([]library.Entry, error) {
	return nil, nil
}

func (fakeScanner) Walk(context.Context, string) ([]library.Entry, error) {
	return nil, nil
}

func (fakeScanner) ResolveCueTrack(context.Context, string, int) (library.Entry, error) {
	return library.Entry{}, nil
}

// savedTabs is one recorded SaveTabs call.
type savedTabs struct {
	active int
	tabs   []queue.TabSnapshot
}

// fakeStore records saved tab state and returns configured tabs. When tabs is
// nil, snapshot is loaded as tab 0 for tests that only exercise one queue.
// names supplies optional tab names by index.
type fakeStore struct {
	mu       sync.Mutex
	active   int
	tabs     []queue.Snapshot
	names    []string
	snapshot queue.Snapshot
	skipped  int
	loadErr  error
	saveErr  error
	saved    []savedTabs
}

func fakeTabSnapshots(tabs []queue.Snapshot, names []string) []queue.TabSnapshot {
	out := make([]queue.TabSnapshot, len(tabs))
	for i := range tabs {
		out[i] = queue.TabSnapshot{Snapshot: tabs[i]}
		if i < len(names) {
			out[i].Name = names[i]
		}
	}
	return out
}

func (s *fakeStore) LoadTabs() (int, []queue.TabSnapshot, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return 0, nil, 0, s.loadErr
	}
	if s.tabs != nil {
		return s.active, fakeTabSnapshots(s.tabs, s.names), s.skipped, nil
	}
	return s.active, []queue.TabSnapshot{{Snapshot: s.snapshot}}, s.skipped, nil
}

func (s *fakeStore) SaveTabs(active int, tabs []queue.TabSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = append(s.saved, savedTabs{
		active: active,
		tabs:   append([]queue.TabSnapshot(nil), tabs...),
	})
	return nil
}

func (s *fakeStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved)
}

func (s *fakeStore) lastSaved() (queue.Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saved) == 0 {
		return queue.Snapshot{}, false
	}
	last := s.saved[len(s.saved)-1]
	if last.active < 0 || last.active >= len(last.tabs) {
		return queue.Snapshot{}, false
	}
	return last.tabs[last.active].Snapshot, true
}

func (s *fakeStore) lastSavedTabs() (int, []queue.TabSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saved) == 0 {
		return 0, nil, false
	}
	last := s.saved[len(s.saved)-1]
	return last.active, append([]queue.TabSnapshot(nil), last.tabs...), true
}

// fakeProber returns configured metadata and records probed paths.
type fakeProber struct {
	mu    sync.Mutex
	info  map[string]meta.Info
	err   map[string]error
	calls []string
}

func (p *fakeProber) Probe(_ context.Context, path string) (meta.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = make([]string, 0, 1)
	}
	p.calls = append(p.calls, path)
	return p.info[path], p.err[path]
}

func (p *fakeProber) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func baseOptions() Options {
	return Options{
		Config:   config.Default(),
		StartDir: "/tmp/music",
		Scanner:  fakeScanner{},
		Player:   newFakePlayer(),
		Store:    &fakeStore{},
		Prober:   &fakeProber{},
	}
}

func newTestModel(t *testing.T, mutate func(*Options)) *Model {
	t.Helper()
	opts := baseOptions()
	if mutate != nil {
		mutate(&opts)
	}
	m := New(opts)
	if m == nil {
		t.Fatalf("New returned nil")
	}
	return m
}

// resize delivers a WindowSizeMsg and returns the updated model.
func resize(t *testing.T, m *Model, width, height int) *Model {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	resized, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return resized
}

// press delivers a key message and returns the updated model.
func press(t *testing.T, m *Model, msg tea.KeyPressMsg) *Model {
	t.Helper()
	next, _ := m.Update(msg)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return updated
}

func runeKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func specialKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}
