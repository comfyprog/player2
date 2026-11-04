// Package tui implements the Bubble Tea shell for player2.
//
// The model owns presentation state only: the browser, queue mutations,
// playback transport, and prompts are layered on by later tasks. The shell is
// deliberately width-safe so hostile filenames cannot break the layout.
package tui

import (
	"context"
	"time"

	"player2/internal/config"
	"player2/internal/library"
	"player2/internal/meta"
	"player2/internal/player"
	"player2/internal/queue"
)

// Scanner is the subset of the library scanner used by the browser.
type Scanner interface {
	ReadDir(ctx context.Context, dir string) ([]library.Entry, error)
	ReadCue(ctx context.Context, cuePath string) ([]library.Entry, error)
	Walk(ctx context.Context, dir string) ([]library.Entry, error)
	ResolveCueTrack(ctx context.Context, cuePath string, n int) (library.Entry, error)
}

// Player is the playback engine surface used by the TUI.
type Player interface {
	Play(queue.Item) error
	TogglePause()
	Stop()
	SeekBy(time.Duration) error
	SetVolume(int)
	Status() player.Status
	Events() <-chan player.Event
	Close() error
}

// QueueStore persists and restores the queue.
type QueueStore interface {
	Load() (queue.Snapshot, int, error)
	Save(queue.Snapshot) error
}

// Prober supplies metadata for plain audio files.
type Prober interface {
	Probe(ctx context.Context, path string) (meta.Info, error)
}

// Options configures a TUI model. Scanner, Player, Store, and Prober may be nil
// so the shell can render before (or without) playback wiring.
type Options struct {
	Config   config.Config
	StartDir string
	Scanner  Scanner
	Player   Player
	Store    QueueStore
	Prober   Prober
}

// Validate checks the parts of opts that must fail startup, currently the
// keymap. Task 14 calls this before constructing the program.
func Validate(opts Options) error {
	_, err := NewKeyMap(opts.Config)
	if err != nil {
		return err
	}
	return nil
}
