// Package player implements the playback engine.
//
// The engine owns a PCM ring, one decoder goroutine per stream, and one monitor
// goroutine for its lifetime. It emits finished and error events but never
// advances the queue itself; auto-advance belongs to the TUI.
package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"player2/internal/audio"
	"player2/internal/queue"
)

const (
	defaultSampleRate    = 48000
	defaultChannels      = 2
	defaultBufferSeconds = 2
	defaultVolume        = 70

	// monitorInterval is how often position and end-of-stream conditions are
	// checked. It is short enough to feel immediate without busy-looping.
	monitorInterval = 10 * time.Millisecond

	// eventBuffer absorbs bursts of events. The monitor blocks when it is full;
	// Close closes stopCh to unblock it during shutdown.
	eventBuffer = 32

	// decodeChunk is the read size handed to the decoder.
	decodeChunk = 32 << 10
)

var (
	errClosed      = errors.New("player: closed")
	errSeekStopped = errors.New("player: seek requires playing or paused state")
	errNilStream   = errors.New("player: decoder returned a nil stream")
	errEmptyRead   = errors.New("player: decoder returned no data and no error")
)

// State is the playback transport state.
type State int

// Transport states.
const (
	StateStopped State = iota
	StatePlaying
	StatePaused
)

// Status is a snapshot of the player.
type Status struct {
	State    State
	Position time.Duration
	Volume   int
	Err      error
}

// EventType distinguishes playback events.
type EventType int

// Event types.
const (
	EventFinished EventType = iota
	EventError
)

// Event reports that the current stream finished or failed. ItemID identifies
// the queue item the event belongs to so a late event from a replaced stream
// cannot advance a newer item.
type Event struct {
	Type   EventType
	ItemID uint64
	Err    error
}

// Options configures the PCM ring and decoder output format. Non-positive
// fields fall back to 48000 Hz, stereo, and a two-second buffer.
type Options struct {
	SampleRate    int
	Channels      int
	BufferSeconds int
}

// streamState is the per-stream decoder result. It is written only by the
// decoder goroutine and read by the monitor under the control mutex; gen lets
// the monitor ignore streams that have been replaced or stopped.
type streamState struct {
	gen    uint64
	stream audio.Stream
	done   chan struct{}

	closeOnce sync.Once
	closeErr  error

	mu  sync.Mutex
	err error
	eof bool
}

// close releases the decoder stream exactly once, even when both the decoder
// goroutine and a control operation race to close it.
func (s *streamState) close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.stream.Close()
	})
	return s.closeErr
}

func (s *streamState) setEOF() {
	s.mu.Lock()
	s.eof = true
	s.mu.Unlock()
}

func (s *streamState) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// snapshot returns the decoder's terminal state. done is true once the decoder
// goroutine has exited; eof and err are meaningful only after that.
func (s *streamState) snapshot() (eof bool, err error, done bool) {
	select {
	case <-s.done:
		done = true
	default:
	}
	s.mu.Lock()
	eof, err = s.eof, s.err
	s.mu.Unlock()
	return eof, err, done
}

// Player is the playback engine. Control methods are safe for concurrent use.
type Player struct {
	dec audio.Decoder
	out audio.Sink

	rate     int
	channels int
	ring     *audio.Ring
	events   chan Event

	// ctrl serializes transport operations and guards all fields below. The
	// decoder goroutine never takes it.
	ctrl sync.Mutex

	state      State
	position   time.Duration
	streamBase time.Duration
	volume     int
	err        error
	item       queue.Item

	closed    bool
	closeErr  error
	closeDone chan struct{}

	gen    uint64
	stream *streamState
	cancel context.CancelFunc

	stopCh      chan struct{}
	stopOnce    sync.Once
	monitorDone chan struct{}

	// afterSample is a test-only hook invoked after tickLocked samples the
	// position and before it decides the stream is finished. Tests use it to
	// drain the ring inside the sample-to-completion window.
	afterSample func()
}

// New creates a playback engine, starts the sink, and launches the monitor.
// The default volume is 70 (the config default).
func New(dec audio.Decoder, out audio.Sink, opts Options) (*Player, error) {
	if dec == nil {
		return nil, errors.New("player: nil decoder")
	}
	if out == nil {
		return nil, errors.New("player: nil sink")
	}
	if opts.SampleRate <= 0 {
		opts.SampleRate = defaultSampleRate
	}
	if opts.Channels <= 0 {
		opts.Channels = defaultChannels
	}
	if opts.BufferSeconds <= 0 {
		opts.BufferSeconds = defaultBufferSeconds
	}

	capacity := opts.SampleRate * opts.Channels * 2 * opts.BufferSeconds
	p := &Player{
		dec:         dec,
		out:         out,
		rate:        opts.SampleRate,
		channels:    opts.Channels,
		ring:        audio.NewRing(capacity),
		events:      make(chan Event, eventBuffer),
		state:       StateStopped,
		volume:      defaultVolume,
		closeDone:   make(chan struct{}),
		stopCh:      make(chan struct{}),
		monitorDone: make(chan struct{}),
	}

	out.SetVolume(float64(p.volume) / 100)
	out.SetPaused(true)
	if err := out.Start(p.ring, p.rate, p.channels); err != nil {
		_ = out.Close()
		return nil, fmt.Errorf("player: start output: %w", err)
	}
	go p.monitor()
	return p, nil
}

// Play replaces the current stream with item. Decoder open errors are returned
// and also recorded in Status.Err; they are not duplicated as events because
// the caller already sees them.
func (p *Player) Play(item queue.Item) error {
	p.ctrl.Lock()
	defer p.ctrl.Unlock()

	if p.closed {
		return errClosed
	}

	p.out.SetPaused(true)
	p.resetRingLocked()
	p.item = item
	p.state = StateStopped
	p.position = 0
	p.streamBase = 0
	p.err = nil

	stream, cancel, err := p.open(item.Path, item.Start)
	if err != nil {
		p.err = err
		return err
	}

	p.startStreamLocked(cancel, stream)
	p.state = StatePlaying
	p.out.SetPaused(false)
	return nil
}

// open starts a decoder stream and rejects the pathological (nil, nil) result.
// On any error a partially returned stream is closed so no decoder resources
// leak.
func (p *Player) open(path string, start time.Duration) (audio.Stream, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := p.dec.Open(ctx, audio.Request{
		Path:       path,
		Start:      start,
		SampleRate: p.rate,
		Channels:   p.channels,
	})
	if err != nil {
		cancel()
		if stream != nil {
			_ = stream.Close()
		}
		return nil, nil, err
	}
	if stream == nil {
		cancel()
		return nil, nil, errNilStream
	}
	return stream, cancel, nil
}

// Pause pauses output without consuming ring data. It is a no-op unless the
// player is playing.
func (p *Player) Pause() {
	p.ctrl.Lock()
	defer p.ctrl.Unlock()
	if p.closed || p.state != StatePlaying {
		return
	}
	p.out.SetPaused(true)
	p.state = StatePaused
}

// Resume resumes output. It is a no-op unless the player is paused.
func (p *Player) Resume() {
	p.ctrl.Lock()
	defer p.ctrl.Unlock()
	if p.closed || p.state != StatePaused {
		return
	}
	p.out.SetPaused(false)
	p.state = StatePlaying
}

// TogglePause flips between playing and paused. It is a no-op when stopped.
func (p *Player) TogglePause() {
	p.ctrl.Lock()
	defer p.ctrl.Unlock()
	if p.closed {
		return
	}
	switch p.state {
	case StatePlaying:
		p.out.SetPaused(true)
		p.state = StatePaused
	case StatePaused:
		p.out.SetPaused(false)
		p.state = StatePlaying
	}
}

// Stop stops the current stream and resets the position to zero. The current
// item is retained so a later seek/play can refer to it. No event is emitted.
func (p *Player) Stop() {
	p.ctrl.Lock()
	defer p.ctrl.Unlock()
	if p.closed {
		return
	}

	p.out.SetPaused(true)
	p.resetRingLocked()
	p.state = StateStopped
	p.position = 0
	p.streamBase = 0
	p.err = nil
}

// SeekBy restarts the decoder at the current track position plus delta. The
// target is clamped to the track. Paused/playing state is preserved.
func (p *Player) SeekBy(delta time.Duration) error {
	p.ctrl.Lock()
	defer p.ctrl.Unlock()

	if p.closed {
		return errClosed
	}
	if p.state != StatePlaying && p.state != StatePaused {
		return errSeekStopped
	}

	item := p.item
	target := p.currentPositionLocked() + delta
	if target < 0 {
		target = 0
	}
	if item.Duration > 0 && target > item.Duration {
		target = item.Duration
	}
	paused := p.state == StatePaused

	p.out.SetPaused(true)
	p.resetRingLocked()
	p.state = StateStopped
	p.position = 0
	p.streamBase = 0
	p.err = nil

	stream, cancel, err := p.open(item.Path, item.Start+target)
	if err != nil {
		p.err = err
		return err
	}

	p.startStreamLocked(cancel, stream)
	p.position = target
	p.streamBase = target
	if paused {
		p.state = StatePaused
		p.out.SetPaused(true)
	} else {
		p.state = StatePlaying
		p.out.SetPaused(false)
	}
	return nil
}

// SetVolume sets the output volume percentage, clamped to 0..100.
func (p *Player) SetVolume(percent int) {
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}

	p.ctrl.Lock()
	defer p.ctrl.Unlock()
	if p.closed {
		return
	}
	p.volume = percent
	p.out.SetVolume(float64(percent) / 100)
}

// Status returns an immediate snapshot of the player.
func (p *Player) Status() Status {
	p.ctrl.Lock()
	defer p.ctrl.Unlock()
	return Status{
		State:    p.state,
		Position: p.currentPositionLocked(),
		Volume:   p.volume,
		Err:      p.err,
	}
}

// Events returns the playback event channel. It is closed by Close.
func (p *Player) Events() <-chan Event {
	return p.events
}

// Close stops playback, stops the monitor, closes the sink, and closes the
// event channel. It is idempotent.
func (p *Player) Close() error {
	// Unblock a monitor that is waiting to deliver an event. This happens
	// before taking ctrl so a full event buffer cannot deadlock Close.
	p.stopOnce.Do(func() { close(p.stopCh) })

	p.ctrl.Lock()
	if p.closed {
		done := p.closeDone
		p.ctrl.Unlock()
		<-done
		p.ctrl.Lock()
		err := p.closeErr
		p.ctrl.Unlock()
		return err
	}
	p.closed = true
	p.stopStreamLocked()
	p.state = StateStopped
	p.position = 0
	p.streamBase = 0
	p.ring.Close()
	p.ctrl.Unlock()

	<-p.monitorDone
	err := p.out.Close()

	p.ctrl.Lock()
	p.closeErr = err
	p.ctrl.Unlock()
	close(p.events)
	close(p.closeDone)
	return err
}

// startStreamLocked installs a new generation and launches its decoder. The
// caller must hold ctrl.
func (p *Player) startStreamLocked(cancel context.CancelFunc, stream audio.Stream) {
	p.gen++
	st := &streamState{
		gen:    p.gen,
		stream: stream,
		done:   make(chan struct{}),
	}
	p.stream = st
	p.cancel = cancel
	go p.decode(st)
}

// stopStreamLocked cancels and closes the current stream and waits for its
// decoder goroutine to exit. The ring is aborted first so a decoder blocked in
// Write can return. Reset must only happen after this returns (R19).
func (p *Player) stopStreamLocked() {
	p.gen++
	st := p.stream
	p.stream = nil
	cancel := p.cancel
	p.cancel = nil

	if cancel != nil {
		cancel()
	}
	p.ring.Abort()
	if st == nil {
		return
	}
	st.close()
	<-st.done
}

// resetRingLocked discards any buffered audio. It always waits for the old
// decoder to exit before resetting so stale bytes cannot enter a fresh stream.
func (p *Player) resetRingLocked() {
	p.stopStreamLocked()
	p.ring.Abort()
	p.ring.Reset()
}

// decode copies PCM from one stream into the ring until EOF, error, or abort.
// It never calls a control method that takes ctrl.
func (p *Player) decode(st *streamState) {
	defer close(st.done)
	defer st.close()

	buf := make([]byte, decodeChunk)
	for {
		n, err := st.stream.Read(buf)
		if n > 0 {
			if _, werr := p.ring.Write(buf[:n]); werr != nil {
				// ErrAborted/ErrClosed are caused by transport operations; the
				// generation check keeps them from surfacing as events.
				if !errors.Is(werr, audio.ErrAborted) && !errors.Is(werr, audio.ErrClosed) {
					st.setErr(werr)
				}
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				st.setEOF()
			} else {
				st.setErr(err)
			}
			return
		}
		if n == 0 {
			// A reader returning (0, nil) is not progress; surface it instead
			// of spinning the decoder goroutine.
			st.setErr(errEmptyRead)
			return
		}
	}
}

// monitor checks end-of-stream conditions for the player's lifetime.
func (p *Player) monitor() {
	defer close(p.monitorDone)

	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.tick()
		}
	}
}

// tick samples the current stream and delivers any terminal event outside the
// control mutex. Delivery may block when the event buffer is full, but Close
// closes stopCh and always unblocks it.
func (p *Player) tick() {
	ev, ok := p.tickLocked()
	if ok {
		p.emit(ev)
	}
}

// tickLocked updates player state for the current stream and returns the event
// to deliver, if any. It acquires ctrl itself; callers must not hold it.
func (p *Player) tickLocked() (Event, bool) {
	p.ctrl.Lock()
	defer p.ctrl.Unlock()

	if p.closed || p.stream == nil {
		return Event{}, false
	}
	if p.state != StatePlaying && p.state != StatePaused {
		return Event{}, false
	}

	st := p.stream
	if st.gen != p.gen {
		return Event{}, false
	}
	eof, decErr, done := st.snapshot()
	pos := p.currentPositionLocked()
	if p.afterSample != nil {
		p.afterSample()
	}

	// The logical end of a known-duration track wins even if the decoder is
	// still producing data (a CUE track is a slice of a longer file). pos is
	// monotonic, so a stale low sample only delays this by one tick; the final
	// position is always the exact item duration.
	if p.item.Duration > 0 && pos >= p.item.Duration {
		p.completeLocked(p.item.Duration)
		return Event{Type: EventFinished, ItemID: p.item.ID}, true
	}

	if done && decErr != nil {
		if errors.Is(decErr, audio.ErrAborted) || errors.Is(decErr, audio.ErrClosed) {
			return Event{}, false
		}
		p.err = decErr
		p.completeLocked(pos)
		return Event{Type: EventError, ItemID: p.item.ID, Err: decErr}, true
	}

	// Unknown duration, or a file shorter than its metadata: the track ends
	// when the decoder is done and the ring has been fully consumed. Once that
	// condition holds the byte count is stable, so recompute the position here
	// rather than using the earlier sample: a consumer may have drained the
	// tail between the sample and this check.
	if done && eof && p.ring.Written() == p.ring.Consumed() {
		final := p.currentPositionLocked()
		if p.item.Duration > 0 && final > p.item.Duration {
			final = p.item.Duration
		}
		p.completeLocked(final)
		return Event{Type: EventFinished, ItemID: p.item.ID}, true
	}

	return Event{}, false
}

// completeLocked stops the stream and freezes the final position. The caller
// must hold ctrl.
func (p *Player) completeLocked(final time.Duration) {
	p.stopStreamLocked()
	p.state = StateStopped
	p.streamBase = 0
	p.position = final
	p.out.SetPaused(true)
}

// emit delivers an event, blocking while the buffer is full so a finished or
// error event is never lost during normal operation. Closing the player stops
// the monitor and drops any event still waiting to be delivered.
func (p *Player) emit(ev Event) {
	select {
	case p.events <- ev:
	case <-p.stopCh:
	}
}

// currentPositionLocked returns the track-relative playback position. While a
// stream is active it is derived from the ring's consumed byte count, so
// underrun silence and paused output never advance it.
func (p *Player) currentPositionLocked() time.Duration {
	if p.state == StatePlaying || p.state == StatePaused {
		return p.streamBase + p.consumedDuration()
	}
	return p.position
}

func (p *Player) consumedDuration() time.Duration {
	return bytesToDuration(p.ring.Consumed(), p.rate, p.channels)
}

// bytesToDuration converts interleaved s16 PCM bytes to a duration without
// overflowing for long inputs.
func bytesToDuration(n int64, rate, channels int) time.Duration {
	bytesPerSecond := int64(rate * channels * 2)
	if n <= 0 || bytesPerSecond <= 0 {
		return 0
	}
	seconds := n / bytesPerSecond
	remainder := n % bytesPerSecond
	return time.Duration(seconds)*time.Second +
		time.Duration(remainder*int64(time.Second)/bytesPerSecond)
}
