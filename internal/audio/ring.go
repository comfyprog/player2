// Ring buffer and output sink interfaces for PCM playback.
package audio

import (
	"errors"
	"sync"
)

// ErrAborted is returned by Ring.Write when the buffer was aborted while the
// write was in progress. Reads continue to drain buffered data. Reset clears
// the aborted state.
var ErrAborted = errors.New("audio: ring write aborted")

// ErrClosed is returned by Ring.Write when the buffer was closed. Close is
// terminal for writes: Reset clears buffered data and counters but does not
// revive writes after Close.
var ErrClosed = errors.New("audio: ring closed")

// Sink is a PCM output device. Start begins playback from ring at the given
// sample rate and channel count. SetPaused and SetVolume may be called before
// Start; their values are retained and applied when playback begins. Close is
// idempotent.
type Sink interface {
	Start(ring *Ring, rate, channels int) error
	SetPaused(bool)
	SetVolume(float64)
	Close() error
}

// Ring is a fixed-capacity byte-oriented circular buffer. Writers block while
// the buffer is full; readers never block and zero-fill any part of their
// destination that is not backed by buffered audio.
//
// Consumed counts only bytes actually read from buffered audio. Zero-filled
// silence from an underrun is not counted, so playback position does not
// advance while the decoder is starting or falling behind.
//
// A Ring must be created with NewRing. Its methods are safe for concurrent
// use by one or more writers and readers.
type Ring struct {
	mu   sync.Mutex
	cond *sync.Cond

	buf  []byte
	head int
	size int

	consumed int64
	written  int64

	aborted bool
	closed  bool
}

// NewRing returns a ring buffer with the given capacity in bytes. It panics
// when capacity is not positive.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		panic("audio: ring capacity must be positive")
	}

	r := &Ring{buf: make([]byte, capacity)}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// Write copies all of p into the ring, blocking while the ring is full. It
// returns the number of bytes accepted. If the ring is aborted or closed
// while the write is blocked (or before it starts), Write returns the bytes
// accepted so far together with ErrAborted or ErrClosed. A zero-length write
// is a no-op and returns (0, nil), even after Close.
func (r *Ring) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	total := 0
	for len(p) > 0 {
		for r.size == len(r.buf) && !r.aborted && !r.closed {
			r.cond.Wait()
		}
		if r.aborted {
			return total, ErrAborted
		}
		if r.closed {
			return total, ErrClosed
		}

		n := len(r.buf) - r.size
		if n > len(p) {
			n = len(p)
		}

		tail := (r.head + r.size) % len(r.buf)
		first := copy(r.buf[tail:], p[:n])
		if first < n {
			// The remaining bytes wrap around to the start of the buffer.
			copy(r.buf, p[first:n])
		}

		r.size += n
		r.written += int64(n)
		total += n
		p = p[n:]
	}

	return total, nil
}

// Read copies buffered audio into p without blocking and zero-fills the rest
// of p. It returns the number of bytes actually taken from the buffer.
// Consumed advances only by that amount. Read is safe after Close; an empty
// or closed ring simply zero-fills p.
func (r *Ring) Read(p []byte) int {
	if len(p) == 0 {
		return 0
	}

	r.mu.Lock()
	n := len(p)
	if n > r.size {
		n = r.size
	}

	first := len(r.buf) - r.head
	if first > n {
		first = n
	}
	copy(p[:first], r.buf[r.head:r.head+first])
	if n > first {
		copy(p[first:n], r.buf[:n-first])
	}

	r.head = (r.head + n) % len(r.buf)
	r.size -= n
	r.consumed += int64(n)
	if n > 0 {
		// Wake blocked writers; some space is now available.
		r.cond.Broadcast()
	}
	r.mu.Unlock()

	// Zero-fill the underrun outside the lock.
	clear(p[n:])
	return n
}

// Abort wakes blocked writers. Writes fail with ErrAborted until Reset is
// called. Reads still drain whatever audio is buffered.
func (r *Ring) Abort() {
	r.mu.Lock()
	r.aborted = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

// Reset discards buffered audio, clears Consumed and Written, and clears the
// aborted state. It does not revive writes after Close: Close is terminal for
// writers.
func (r *Ring) Reset() {
	r.mu.Lock()
	r.head = 0
	r.size = 0
	r.consumed = 0
	r.written = 0
	r.aborted = false
	r.cond.Broadcast()
	r.mu.Unlock()
}

// Consumed returns the number of audio bytes read from the buffer. Silence
// produced by an underrun is not counted.
func (r *Ring) Consumed() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.consumed
}

// Written returns the number of audio bytes accepted by the buffer.
func (r *Ring) Written() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written
}

// Close wakes all blocked writers and makes subsequent writes fail with
// ErrClosed. Buffered audio can still be read. Close is idempotent and is
// terminal for writers.
func (r *Ring) Close() {
	r.mu.Lock()
	r.closed = true
	r.cond.Broadcast()
	r.mu.Unlock()
}
