// Package malgo provides the cgo/miniaudio-backed PCM output sink.
//
// The sink renders signed 16-bit interleaved PCM from an audio.Ring. It is the
// only part of the application that opens a sound device. Tests run without a
// device unless PLAYER2_TEST_AUDIO=1 is set.
package malgo

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"

	ma "github.com/gen2brain/malgo"

	"player2/internal/audio"
)

// defaultChannels is used when Start is called with zero channels.
const defaultChannels = 2

// rampMilliseconds is the duration of a volume ramp. Ten milliseconds is short
// enough to feel immediate and long enough to avoid audible clicks.
const rampMilliseconds = 10

const (
	minInt16 = -1 << 15
	maxInt16 = 1<<15 - 1
)

// Sink is a malgo playback device that consumes PCM from an audio.Ring. The
// zero value is usable, although New is the preferred constructor.
type Sink struct {
	mu sync.Mutex

	device  *ma.Device
	context *ma.AllocatedContext
	proc    *processor

	started bool
	closed  bool

	// Settings are retained across Start so callers may configure the sink
	// before a device exists.
	paused    bool
	volume    float64
	volumeSet bool
}

var _ audio.Sink = (*Sink)(nil)

// New returns a sink that is ready to Start at full volume.
func New() *Sink {
	return &Sink{volume: 1, volumeSet: true}
}

// Start initializes the default playback device for signed 16-bit interleaved
// audio at rate and channels. A zero channel count defaults to stereo. A nil
// ring, non-positive rate, or negative channel count is rejected before any
// device is opened.
//
// Start may be called only once; a second call returns an error. SetPaused and
// SetVolume values configured before Start are retained. Start returns an
// error after Close.
func (s *Sink) Start(ring *audio.Ring, rate, channels int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("malgo: sink is closed")
	}
	if s.started {
		return fmt.Errorf("malgo: sink is already started")
	}
	if ring == nil {
		return fmt.Errorf("malgo: Start requires a non-nil ring")
	}
	if rate <= 0 {
		return fmt.Errorf("malgo: sample rate must be positive, got %d", rate)
	}
	if channels < 0 {
		return fmt.Errorf("malgo: channels must not be negative, got %d", channels)
	}
	if channels == 0 {
		channels = defaultChannels
	}

	proc := newProcessor(ring, rate, channels, s.paused, s.volumeValue())

	ctx, err := ma.InitContext(nil, ma.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("malgo: initialize audio context: %w", err)
	}

	cfg := ma.DefaultDeviceConfig(ma.Playback)
	cfg.SampleRate = uint32(rate)
	cfg.Playback.Format = ma.FormatS16
	cfg.Playback.Channels = uint32(channels)

	device, err := ma.InitDevice(ctx.Context, cfg, ma.DeviceCallbacks{Data: proc.render})
	if err != nil {
		ctx.Free()
		return fmt.Errorf("malgo: initialize playback device (sample rate %d, channels %d): %w", rate, channels, err)
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		ctx.Free()
		return fmt.Errorf("malgo: start playback device: %w", err)
	}

	s.device = device
	s.context = ctx
	s.proc = proc
	s.started = true
	return nil
}

// SetPaused pauses or resumes rendering. While paused the callback outputs
// silence without reading (or consuming) the ring. The value is retained if
// called before Start.
func (s *Sink) SetPaused(paused bool) {
	s.mu.Lock()
	s.paused = paused
	if s.proc != nil {
		s.proc.setPaused(paused)
	}
	s.mu.Unlock()
}

// SetVolume changes the output gain. Values outside [0, 1] are clamped; NaN is
// treated as zero. Changes are applied as a short linear ramp to avoid clicks.
// The value is retained if called before Start.
func (s *Sink) SetVolume(volume float64) {
	volume = clampVolume(volume)

	s.mu.Lock()
	s.volume = volume
	s.volumeSet = true
	if s.proc != nil {
		s.proc.setVolume(volume)
	}
	s.mu.Unlock()
}

// Close stops and uninitializes the playback device. It is idempotent and is
// safe to call before Start. No callbacks run after Close returns; any callback
// that races with closing outputs silence.
func (s *Sink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true

	proc := s.proc
	device := s.device
	ctx := s.context

	s.proc = nil
	s.device = nil
	s.context = nil
	s.started = false
	s.mu.Unlock()

	// Mark the processor closed before tearing down the device so a callback
	// racing with Close renders silence instead of touching the ring.
	if proc != nil {
		proc.close()
	}
	if device != nil {
		device.Uninit()
	}
	if ctx != nil {
		ctx.Free()
	}
	return nil
}

// volumeValue returns the effective volume, defaulting to full volume for a
// zero-value Sink.
func (s *Sink) volumeValue() float64 {
	if !s.volumeSet {
		return 1
	}
	return s.volume
}

func clampVolume(volume float64) float64 {
	if math.IsNaN(volume) || volume < 0 {
		return 0
	}
	if volume > 1 {
		return 1
	}
	return volume
}

// processor turns ring bytes into output samples. It is separated from the
// malgo device so gain, pause, and underrun behavior can be unit tested without
// opening a sound device.
type processor struct {
	mu sync.Mutex

	ring     *audio.Ring
	channels int
	paused   bool
	closed   bool

	target     float64
	gain       float64
	step       float64
	rampFrames int
}

func newProcessor(ring *audio.Ring, rate, channels int, paused bool, volume float64) *processor {
	rampFrames := rate * rampMilliseconds / 1000
	if rampFrames < 1 {
		rampFrames = 1
	}
	return &processor{
		ring:       ring,
		channels:   channels,
		paused:     paused,
		target:     volume,
		gain:       volume,
		rampFrames: rampFrames,
	}
}

func (p *processor) setPaused(paused bool) {
	p.mu.Lock()
	p.paused = paused
	p.mu.Unlock()
}

func (p *processor) setVolume(volume float64) {
	p.mu.Lock()
	p.target = clampVolume(volume)
	delta := p.target - p.gain
	if delta < 0 {
		delta = -delta
	}
	p.step = delta / float64(p.rampFrames)
	p.mu.Unlock()
}

func (p *processor) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

// render is the malgo data callback. It must not allocate and is safe to call
// concurrently with setPaused, setVolume, and close.
func (p *processor) render(out, _ []byte, _ uint32) {
	if len(out) == 0 {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed || p.paused || p.ring == nil {
		clear(out)
		return
	}

	// Read returns only the number of real audio bytes; any underrun tail has
	// already been zero-filled and does not advance Consumed.
	n := p.ring.Read(out)
	if n == 0 {
		return
	}

	frameSize := p.channels * 2
	frames := n / frameSize

	idx := 0
	for frame := 0; frame < frames; frame++ {
		gain := p.gain
		for channel := 0; channel < p.channels; channel++ {
			sample := int16(binary.LittleEndian.Uint16(out[idx:]))
			scaled := int32(math.Round(float64(sample) * gain))
			if scaled > maxInt16 {
				scaled = maxInt16
			} else if scaled < minInt16 {
				scaled = minInt16
			}
			binary.LittleEndian.PutUint16(out[idx:], uint16(int16(scaled)))
			idx += 2
		}

		// Advance after applying so the first frame uses the current gain and
		// the ramp spans rampFrames frames.
		switch {
		case p.gain < p.target:
			p.gain += p.step
			if p.gain > p.target {
				p.gain = p.target
			}
		case p.gain > p.target:
			p.gain -= p.step
			if p.gain < p.target {
				p.gain = p.target
			}
		}
	}

	// A callback buffer should always contain whole frames, but never leak a
	// partially scaled frame if it does not.
	if idx < n {
		clear(out[idx:n])
	}
}
