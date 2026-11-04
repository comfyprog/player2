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
	mu      sync.Mutex
	status  player.Status
	events  chan player.Event
	played  []queue.Item
	pauses  int
	stops   int
	volumes []int
	closed  bool
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
	p.status.State = player.StateStopped
	p.status.Position = 0
}

func (p *fakePlayer) SeekBy(time.Duration) error { return nil }

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

// fakeStore records saved snapshots and returns a configured snapshot.
type fakeStore struct {
	snapshot queue.Snapshot
	skipped  int
	loadErr  error
	saved    []queue.Snapshot
}

func (s *fakeStore) Load() (queue.Snapshot, int, error) {
	return s.snapshot, s.skipped, s.loadErr
}

func (s *fakeStore) Save(snapshot queue.Snapshot) error {
	s.saved = append(s.saved, snapshot)
	return nil
}

// fakeProber returns empty metadata.
type fakeProber struct{}

func (fakeProber) Probe(context.Context, string) (meta.Info, error) {
	return meta.Info{}, nil
}

func baseOptions() Options {
	return Options{
		Config:   config.Default(),
		StartDir: "/tmp/music",
		Scanner:  fakeScanner{},
		Player:   newFakePlayer(),
		Store:    &fakeStore{},
		Prober:   fakeProber{},
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
