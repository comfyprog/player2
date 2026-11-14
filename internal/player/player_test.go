package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"player2/internal/audio"
	"player2/internal/audio/ffmpeg"
	"player2/internal/queue"
)

const (
	testRate     = 48000
	testChannels = 2
	// bytesPerSecond is the PCM byte rate for testRate/testChannels s16 audio.
	bytesPerSecond = testRate * testChannels * 2
)

// fakeDecoder records requests and returns streams built by openFn.
type fakeDecoder struct {
	mu      sync.Mutex
	reqs    []audio.Request
	openErr error
	openFn  func(audio.Request) audio.Stream
}

func (d *fakeDecoder) Open(_ context.Context, req audio.Request) (audio.Stream, error) {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	err := d.openErr
	fn := d.openFn
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if fn != nil {
		return fn(req), nil
	}
	return newFakeStream(nil, nil), nil
}

func (d *fakeDecoder) setOpenErr(err error) {
	d.mu.Lock()
	d.openErr = err
	d.mu.Unlock()
}

func (d *fakeDecoder) requests() []audio.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]audio.Request(nil), d.reqs...)
}

func (d *fakeDecoder) requestCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.reqs)
}

// fakeStream serves data in small chunks, then terminalErr (or EOF). Reads
// fail with io.ErrClosedPipe once the stream is closed.
type fakeStream struct {
	data    []byte
	chunk   int
	termErr error

	closed chan struct{}
	once   sync.Once
}

func newFakeStream(data []byte, termErr error) *fakeStream {
	return &fakeStream{
		data:    data,
		chunk:   4096,
		termErr: termErr,
		closed:  make(chan struct{}),
	}
}

func (s *fakeStream) Read(p []byte) (int, error) {
	select {
	case <-s.closed:
		return 0, io.ErrClosedPipe
	default:
	}

	if len(s.data) == 0 {
		if s.termErr != nil {
			err := s.termErr
			s.termErr = nil
			return 0, err
		}
		return 0, io.EOF
	}

	n := len(p)
	if n > s.chunk {
		n = s.chunk
	}
	if n > len(s.data) {
		n = len(s.data)
	}
	copy(p, s.data[:n])
	s.data = s.data[n:]
	return n, nil
}

func (s *fakeStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// fakeSink captures the ring and transport settings. Tests drain the ring
// explicitly to simulate an output device.
type fakeSink struct {
	mu       sync.Mutex
	ring     *audio.Ring
	rate     int
	channels int
	paused   bool
	volume   float64
	starts   int
	startErr error
	closed   bool
}

func (s *fakeSink) Start(ring *audio.Ring, rate, channels int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	s.ring = ring
	s.rate = rate
	s.channels = channels
	return s.startErr
}

func (s *fakeSink) SetPaused(paused bool) {
	s.mu.Lock()
	s.paused = paused
	s.mu.Unlock()
}

func (s *fakeSink) SetVolume(volume float64) {
	s.mu.Lock()
	s.volume = volume
	s.mu.Unlock()
}

func (s *fakeSink) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *fakeSink) Ring() *audio.Ring {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring
}

func (s *fakeSink) settings() (paused bool, volume float64, rate, channels int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused, s.volume, s.rate, s.channels
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

func waitWritten(t *testing.T, ring *audio.Ring, n int64) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d ring bytes", n), func() bool { return ring.Written() >= n })
}

// consume transfers exactly n bytes from the sink's ring, waiting for the
// decoder to supply them. It fails the test if the bytes never arrive.
func consume(t *testing.T, sink *fakeSink, n int) {
	t.Helper()
	ring := sink.Ring()
	if ring == nil {
		t.Fatal("sink was not started")
	}
	buf := make([]byte, 4096)
	got := 0
	deadline := time.Now().Add(3 * time.Second)
	for got < n {
		want := n - got
		if want > len(buf) {
			want = len(buf)
		}
		read := ring.Read(buf[:want])
		got += read
		if read == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("timed out consuming ring: got %d of %d bytes", got, n)
			}
			runtime.Gosched()
			time.Sleep(time.Millisecond)
		}
	}
}

func waitEvent(t *testing.T, p *Player) Event {
	t.Helper()
	select {
	case ev, ok := <-p.Events():
		if !ok {
			t.Fatal("events channel closed before an event arrived")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for player event")
		return Event{}
	}
}

func expectNoEvent(t *testing.T, p *Player, window time.Duration) {
	t.Helper()
	select {
	case ev, ok := <-p.Events():
		if ok {
			t.Fatalf("unexpected event: type=%v err=%v", ev.Type, ev.Err)
		}
		t.Fatal("events channel closed unexpectedly")
	case <-time.After(window):
	}
}

func mustNew(t *testing.T, dec audio.Decoder, sink audio.Sink, opts Options) *Player {
	t.Helper()
	p, err := New(dec, sink, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestNewDefaultsAndStartFailure(t *testing.T) {
	dec := &fakeDecoder{}
	sink := &fakeSink{}
	p, err := New(dec, sink, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p == nil {
		t.Fatal("New() returned a nil player")
	}

	paused, volume, rate, channels := sink.settings()
	if rate != 48000 || channels != 2 {
		t.Errorf("sink format = %d Hz/%d ch, want 48000 Hz/2 ch", rate, channels)
	}
	if volume != 0.7 {
		t.Errorf("initial sink volume = %v, want 0.7", volume)
	}
	if !paused {
		t.Error("sink should start paused while the player is stopped")
	}

	st := p.Status()
	if st.State != StateStopped || st.Position != 0 || st.Volume != 70 || st.Err != nil {
		t.Errorf("initial Status() = %+v, want stopped/0/70/nil", st)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Custom options are passed through.
	dec = &fakeDecoder{}
	sink = &fakeSink{}
	p, err = New(dec, sink, Options{SampleRate: 44100, Channels: 1, BufferSeconds: 1})
	if err != nil {
		t.Fatalf("New(custom) error = %v", err)
	}
	_, _, rate, channels = sink.settings()
	if rate != 44100 || channels != 1 {
		t.Errorf("sink format = %d Hz/%d ch, want 44100 Hz/1 ch", rate, channels)
	}
	_ = p.Close()

	// A sink that cannot start makes New fail.
	wantErr := errors.New("no device")
	dec = &fakeDecoder{}
	sink = &fakeSink{startErr: wantErr}
	if p, err := New(dec, sink, Options{}); err == nil {
		_ = p.Close()
		t.Fatal("New() with failing sink returned no error")
	} else if !errors.Is(err, wantErr) {
		t.Fatalf("New() error = %v, want %v", err, wantErr)
	}
	if !sink.closed {
		t.Error("sink was not closed after Start failure")
	}

	// Nil dependencies are rejected.
	if _, err := New(nil, &fakeSink{}, Options{}); err == nil {
		t.Error("New(nil decoder) returned no error")
	}
	if _, err := New(&fakeDecoder{}, nil, Options{}); err == nil {
		t.Error("New(nil sink) returned no error")
	}
}

func TestPlayFinishesAtKnownDuration(t *testing.T) {
	pcm := bytes.Repeat([]byte{0x11, 0x22}, bytesPerSecond/2)
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Clone(pcm), nil)
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	item := queue.Item{ID: 7, Path: "/music/one.wav", Title: "one", Duration: time.Second}
	if err := p.Play(item); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	if st := p.Status(); st.State != StatePlaying || st.Position != 0 {
		t.Fatalf("Status() after Play = %+v, want playing/0", st)
	}
	reqs := dec.requests()
	if len(reqs) != 1 {
		t.Fatalf("decoder requests = %d, want 1", len(reqs))
	}
	if reqs[0].Path != item.Path || reqs[0].Start != 0 || reqs[0].SampleRate != testRate || reqs[0].Channels != testChannels {
		t.Errorf("decoder request = %+v, want path/start 0/48000/2", reqs[0])
	}
	if paused, _, _, _ := sink.settings(); paused {
		t.Error("sink should be unpaused while playing")
	}

	consume(t, sink, bytesPerSecond)

	ev := waitEvent(t, p)
	if ev.Type != EventFinished {
		t.Fatalf("event = %+v, want finished", ev)
	}
	if ev.ItemID != item.ID {
		t.Errorf("event ItemID = %d, want %d", ev.ItemID, item.ID)
	}
	st := p.Status()
	if st.State != StateStopped {
		t.Errorf("state after finish = %v, want stopped", st.State)
	}
	if st.Position != time.Second {
		t.Errorf("position after finish = %v, want %v", st.Position, time.Second)
	}
	if st.Err != nil {
		t.Errorf("Status().Err after finish = %v, want nil", st.Err)
	}
	if paused, _, _, _ := sink.settings(); !paused {
		t.Error("sink should be paused after finish")
	}
	expectNoEvent(t, p, 80*time.Millisecond)
}

func TestUnknownDurationFinishesAfterEOFDrained(t *testing.T) {
	pcm := bytes.Repeat([]byte{0x33}, bytesPerSecond/5) // 200 ms
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Clone(pcm), nil)
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	if err := p.Play(queue.Item{Path: "/music/unknown.wav"}); err != nil {
		t.Fatalf("Play() error = %v", err)
	}
	waitWritten(t, sink.Ring(), int64(len(pcm)))

	// The decoder has reached EOF but the ring still holds audio, so playback
	// must not finish yet.
	expectNoEvent(t, p, 80*time.Millisecond)

	consume(t, sink, len(pcm))
	ev := waitEvent(t, p)
	if ev.Type != EventFinished {
		t.Fatalf("event = %+v, want finished", ev)
	}

	want := time.Duration(len(pcm)) * time.Second / bytesPerSecond
	if st := p.Status(); st.State != StateStopped || st.Position != want {
		t.Errorf("Status() after unknown-duration finish = %+v, want stopped/%v", st, want)
	}
	expectNoEvent(t, p, 80*time.Millisecond)
}

func TestFinishPositionIncludesTailDrainedDuringTick(t *testing.T) {
	pcm := bytes.Repeat([]byte{0xcd}, bytesPerSecond) // exactly one second
	tests := []struct {
		name     string
		duration time.Duration
		want     time.Duration
	}{
		{name: "known duration", duration: time.Second, want: time.Second},
		{name: "unknown duration", duration: 0, want: time.Second},
		{name: "shorter than metadata", duration: 2 * time.Second, want: time.Second},
		{name: "clamped to track duration", duration: 500 * time.Millisecond, want: 500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
				return newFakeStream(bytes.Clone(pcm), nil)
			}}
			sink := &fakeSink{}
			p := mustNew(t, dec, sink, Options{})

			if err := p.Play(queue.Item{Path: "/music/tail.wav", Duration: tt.duration}); err != nil {
				t.Fatalf("Play() error = %v", err)
			}

			ring := sink.Ring()
			waitWritten(t, ring, int64(len(pcm)))
			waitFor(t, "decoder EOF", func() bool {
				p.ctrl.Lock()
				defer p.ctrl.Unlock()
				if p.stream == nil {
					return false
				}
				_, _, done := p.stream.snapshot()
				return done
			})

			// Drain the entire tail inside the sample-to-completion window. The
			// finished position must reflect the newly consumed bytes, not the
			// position sampled before the hook ran.
			var drained bool
			p.ctrl.Lock()
			p.afterSample = func() {
				if drained {
					return
				}
				drained = true
				buf := make([]byte, 8192)
				for ring.Read(buf) > 0 {
				}
			}
			p.ctrl.Unlock()

			ev := waitEvent(t, p)
			if ev.Type != EventFinished {
				t.Fatalf("event = %+v, want finished", ev)
			}
			st := p.Status()
			if st.State != StateStopped {
				t.Errorf("state = %v, want stopped", st.State)
			}
			if st.Position != tt.want {
				t.Errorf("final position = %v, want %v", st.Position, tt.want)
			}
		})
	}
}

func TestPauseFreezesPositionAndConsumption(t *testing.T) {
	pcm := bytes.Repeat([]byte{0x44}, bytesPerSecond) // 1 s
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Clone(pcm), nil)
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	if err := p.Play(queue.Item{Path: "/music/pause.wav", Duration: time.Second}); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	quarter := bytesPerSecond / 4
	consume(t, sink, quarter)
	waitFor(t, "position to reach 250ms", func() bool {
		return p.Status().Position == 250*time.Millisecond
	})

	p.Pause()
	if st := p.Status(); st.State != StatePaused || st.Position != 250*time.Millisecond {
		t.Fatalf("Status() after Pause = %+v, want paused/250ms", st)
	}
	if paused, _, _, _ := sink.settings(); !paused {
		t.Error("sink not paused after Pause")
	}

	ring := sink.Ring()
	before := ring.Consumed()
	time.Sleep(60 * time.Millisecond)
	if after := ring.Consumed(); after != before {
		t.Errorf("ring consumed advanced while paused: %d -> %d", before, after)
	}
	if st := p.Status(); st.Position != 250*time.Millisecond {
		t.Errorf("position while paused = %v, want 250ms", st.Position)
	}

	// Stop is not required here; resume and finish the track.
	p.Resume()
	if st := p.Status(); st.State != StatePlaying {
		t.Fatalf("state after Resume = %v, want playing", st.State)
	}
	if paused, _, _, _ := sink.settings(); paused {
		t.Error("sink still paused after Resume")
	}

	consume(t, sink, bytesPerSecond-quarter)
	ev := waitEvent(t, p)
	if ev.Type != EventFinished {
		t.Fatalf("event = %+v, want finished", ev)
	}
	if st := p.Status(); st.Position != time.Second {
		t.Errorf("final position = %v, want 1s", st.Position)
	}
}

func TestSeekByRestartsAtRequestedOffset(t *testing.T) {
	long := bytes.Repeat([]byte{0x55}, bytesPerSecond*5)
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Clone(long), nil)
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	item := queue.Item{Path: "/music/cue.flac", CuePath: "/music/album.cue", CueTrack: 2, Start: 10 * time.Second, Duration: 20 * time.Second}
	if err := p.Play(item); err != nil {
		t.Fatalf("Play() error = %v", err)
	}
	if req := dec.requests()[0]; req.Start != 10*time.Second {
		t.Fatalf("initial request start = %v, want 10s", req.Start)
	}

	consume(t, sink, bytesPerSecond)
	waitFor(t, "position 1s", func() bool { return p.Status().Position == time.Second })

	if err := p.SeekBy(2 * time.Second); err != nil {
		t.Fatalf("SeekBy(+2s) error = %v", err)
	}
	reqs := dec.requests()
	if len(reqs) != 2 {
		t.Fatalf("decoder requests after seek = %d, want 2", len(reqs))
	}
	if reqs[1].Start != 13*time.Second {
		t.Errorf("seek request start = %v, want 13s", reqs[1].Start)
	}
	if st := p.Status(); st.State != StatePlaying || st.Position != 3*time.Second {
		t.Errorf("Status() after seek = %+v, want playing/3s", st)
	}

	// Below-zero clamps to the beginning of the track.
	if err := p.SeekBy(-10 * time.Second); err != nil {
		t.Fatalf("SeekBy(-10s) error = %v", err)
	}
	if req := dec.requests()[2]; req.Start != 10*time.Second {
		t.Errorf("low-clamped seek request start = %v, want 10s", req.Start)
	}
	if st := p.Status(); st.Position != 0 {
		t.Errorf("position after low clamp = %v, want 0", st.Position)
	}

	// Pause survives a seek.
	p.Pause()
	if err := p.SeekBy(3 * time.Second); err != nil {
		t.Fatalf("SeekBy while paused error = %v", err)
	}
	if req := dec.requests()[3]; req.Start != 13*time.Second {
		t.Errorf("paused seek request start = %v, want 13s", req.Start)
	}
	if st := p.Status(); st.State != StatePaused || st.Position != 3*time.Second {
		t.Errorf("Status() after paused seek = %+v, want paused/3s", st)
	}
	if paused, _, _, _ := sink.settings(); !paused {
		t.Error("seek resumed a paused sink")
	}
	p.Resume()

	// Above-duration clamps to the track end. Such a seek may finish.
	if err := p.SeekBy(time.Hour); err != nil {
		t.Fatalf("SeekBy(1h) error = %v", err)
	}
	if req := dec.requests()[4]; req.Start != 30*time.Second {
		t.Errorf("high-clamped seek request start = %v, want 30s", req.Start)
	}
	ev := waitEvent(t, p)
	if ev.Type != EventFinished {
		t.Fatalf("event after seeking to end = %+v, want finished", ev)
	}
	if st := p.Status(); st.State != StateStopped || st.Position != item.Duration {
		t.Errorf("Status() after end seek = %+v, want stopped/%v", st, item.Duration)
	}

	// Seeking while stopped is invalid.
	if err := p.SeekBy(time.Second); err == nil {
		t.Fatal("SeekBy while stopped returned no error")
	}
}

func TestStopKeepsItemAndEmitsNoEvent(t *testing.T) {
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Repeat([]byte{0x66}, bytesPerSecond), nil)
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	item := queue.Item{Path: "/music/stop.wav", Duration: time.Second}
	if err := p.Play(item); err != nil {
		t.Fatalf("Play() error = %v", err)
	}
	consume(t, sink, bytesPerSecond/2)

	p.Stop()
	st := p.Status()
	if st.State != StateStopped || st.Position != 0 || st.Err != nil {
		t.Errorf("Status() after Stop = %+v, want stopped/0/nil", st)
	}
	if paused, _, _, _ := sink.settings(); !paused {
		t.Error("sink not paused after Stop")
	}
	if p.item.Path != item.Path {
		t.Errorf("retained item path = %q, want %q", p.item.Path, item.Path)
	}
	expectNoEvent(t, p, 80*time.Millisecond)

	// Stopping again is harmless.
	p.Stop()
	expectNoEvent(t, p, 30*time.Millisecond)
}

func TestDecoderReadErrorEmitsEventOnce(t *testing.T) {
	wantErr := errors.New("corrupt frame")
	pcm := bytes.Repeat([]byte{0x77}, bytesPerSecond/10)
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Clone(pcm), wantErr)
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	if err := p.Play(queue.Item{Path: "/music/bad.flac", Duration: time.Second}); err != nil {
		t.Fatalf("Play() error = %v", err)
	}
	ev := waitEvent(t, p)
	if ev.Type != EventError {
		t.Fatalf("event = %+v, want error", ev)
	}
	if !errors.Is(ev.Err, wantErr) {
		t.Errorf("event error = %v, want %v", ev.Err, wantErr)
	}
	st := p.Status()
	if st.State != StateStopped {
		t.Errorf("state after decoder error = %v, want stopped", st.State)
	}
	if !errors.Is(st.Err, wantErr) {
		t.Errorf("Status().Err = %v, want %v", st.Err, wantErr)
	}
	if paused, _, _, _ := sink.settings(); !paused {
		t.Error("sink not paused after decoder error")
	}
	expectNoEvent(t, p, 80*time.Millisecond)
}

func TestDecoderOpenErrorIsReturnedWithoutEvent(t *testing.T) {
	wantErr := errors.New("cannot open")
	dec := &fakeDecoder{}
	dec.setOpenErr(wantErr)
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	if err := p.Play(queue.Item{Path: "/music/missing.flac"}); err == nil {
		t.Fatal("Play() returned no error")
	} else if !errors.Is(err, wantErr) {
		t.Fatalf("Play() error = %v, want %v", err, wantErr)
	}

	st := p.Status()
	if st.State != StateStopped {
		t.Errorf("state after open error = %v, want stopped", st.State)
	}
	if !errors.Is(st.Err, wantErr) {
		t.Errorf("Status().Err = %v, want %v", st.Err, wantErr)
	}
	expectNoEvent(t, p, 80*time.Millisecond)
}

func TestDecoderNilStreamIsAnError(t *testing.T) {
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream { return nil }}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	err := p.Play(queue.Item{Path: "/music/nil.flac"})
	if err == nil {
		t.Fatal("Play() with nil stream returned no error")
	}
	if !errors.Is(err, errNilStream) {
		t.Errorf("Play() error = %v, want %v", err, errNilStream)
	}
	st := p.Status()
	if st.State != StateStopped {
		t.Errorf("state after nil stream = %v, want stopped", st.State)
	}
	if !errors.Is(st.Err, errNilStream) {
		t.Errorf("Status().Err = %v, want %v", st.Err, errNilStream)
	}
	expectNoEvent(t, p, 80*time.Millisecond)
}

// emptyReadStream reports (0, nil) forever, violating the io.Reader contract.
type emptyReadStream struct {
	closed chan struct{}
	once   sync.Once
}

func newEmptyReadStream() *emptyReadStream {
	return &emptyReadStream{closed: make(chan struct{})}
}

func (s *emptyReadStream) Read([]byte) (int, error) {
	select {
	case <-s.closed:
		return 0, io.ErrClosedPipe
	default:
		return 0, nil
	}
}

func (s *emptyReadStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestDecoderEmptyReadIsAnError(t *testing.T) {
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newEmptyReadStream()
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	if err := p.Play(queue.Item{Path: "/music/stalled.flac"}); err != nil {
		t.Fatalf("Play() error = %v", err)
	}
	ev := waitEvent(t, p)
	if ev.Type != EventError {
		t.Fatalf("event = %+v, want error", ev)
	}
	if !errors.Is(ev.Err, errEmptyRead) {
		t.Errorf("event error = %v, want %v", ev.Err, errEmptyRead)
	}
	if st := p.Status(); st.State != StateStopped || !errors.Is(st.Err, errEmptyRead) {
		t.Errorf("Status() = %+v, want stopped with %v", st, errEmptyRead)
	}
	expectNoEvent(t, p, 80*time.Millisecond)
}

// blockingStream yields a little data and then blocks in Read until it is
// closed. It is used to check that a replaced decoder cannot emit events.
type blockingStream struct {
	data   []byte
	closed chan struct{}
	once   sync.Once
}

func newBlockingStream(data []byte) *blockingStream {
	return &blockingStream{data: data, closed: make(chan struct{})}
}

func (s *blockingStream) Read(p []byte) (int, error) {
	if len(s.data) > 0 {
		n := copy(p, s.data)
		s.data = s.data[n:]
		return n, nil
	}
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *blockingStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestStaleDecoderCannotEmitEvents(t *testing.T) {
	pcm := bytes.Repeat([]byte{0x88}, bytesPerSecond/5)
	dec := &fakeDecoder{}
	dec.openFn = func(req audio.Request) audio.Stream {
		if req.Path == "old" {
			return newBlockingStream([]byte{1, 2, 3, 4})
		}
		return newFakeStream(bytes.Clone(pcm), nil)
	}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	if err := p.Play(queue.Item{Path: "old", Duration: time.Second}); err != nil {
		t.Fatalf("Play(old) error = %v", err)
	}
	// Replacing the stream closes the old one; its ErrClosedPipe must not turn
	// into an EventError.
	if err := p.Play(queue.Item{Path: "new"}); err != nil {
		t.Fatalf("Play(new) error = %v", err)
	}
	waitWritten(t, sink.Ring(), int64(len(pcm)))
	consume(t, sink, len(pcm))

	ev := waitEvent(t, p)
	if ev.Type != EventFinished {
		t.Fatalf("event = %+v, want finished from the replacement stream", ev)
	}
	expectNoEvent(t, p, 80*time.Millisecond)
}

func TestRapidTransportIsSafe(t *testing.T) {
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Repeat([]byte{0x99}, 1<<20), nil)
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})
	item := queue.Item{Path: "/music/rapid.wav", Duration: 10 * time.Second}

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				switch (worker + i) % 6 {
				case 0:
					_ = p.Play(item)
				case 1:
					_ = p.SeekBy(time.Duration(i) * time.Millisecond)
				case 2:
					p.Pause()
				case 3:
					p.Resume()
				case 4:
					p.TogglePause()
				case 5:
					p.Stop()
				}
			}
		}(worker)
	}
	wg.Wait()

	// The engine must still be usable and emit exactly one finish for a fresh
	// stream.
	p.Stop()
	pcm := bytes.Repeat([]byte{0xaa}, bytesPerSecond)
	dec.mu.Lock()
	dec.openFn = func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Clone(pcm), nil)
	}
	dec.mu.Unlock()
	if err := p.Play(queue.Item{Path: "/music/final.wav", Duration: time.Second}); err != nil {
		t.Fatalf("Play(final) error = %v", err)
	}
	consume(t, sink, bytesPerSecond)
	ev := waitEvent(t, p)
	if ev.Type != EventFinished {
		t.Fatalf("event = %+v, want finished", ev)
	}
	expectNoEvent(t, p, 80*time.Millisecond)
}

func TestSetVolumeClampsAndReachesSink(t *testing.T) {
	dec := &fakeDecoder{}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})

	tests := []struct {
		in      int
		want    int
		sinkVol float64
	}{
		{in: -20, want: 0, sinkVol: 0},
		{in: 0, want: 0, sinkVol: 0},
		{in: 42, want: 42, sinkVol: 0.42},
		{in: 100, want: 100, sinkVol: 1},
		{in: 250, want: 100, sinkVol: 1},
	}
	for _, tt := range tests {
		p.SetVolume(tt.in)
		if got := p.Status().Volume; got != tt.want {
			t.Errorf("Status().Volume after SetVolume(%d) = %d, want %d", tt.in, got, tt.want)
		}
		if _, volume, _, _ := sink.settings(); volume != tt.sinkVol {
			t.Errorf("sink volume after SetVolume(%d) = %v, want %v", tt.in, volume, tt.sinkVol)
		}
	}
}

func TestCloseIsIdempotentAndTerminal(t *testing.T) {
	// A blocking stream guarantees no finished event races with Close; the
	// stream is only released by Close itself.
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newBlockingStream([]byte{1, 2, 3, 4})
	}}
	sink := &fakeSink{}
	p, err := New(dec, sink, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := p.Play(queue.Item{Path: "/music/close.wav"}); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, ok := <-p.Events(); ok {
		t.Error("events channel is still open after Close")
	}
	if !sink.closed {
		t.Error("sink was not closed")
	}
	if err := p.Play(queue.Item{Path: "/music/after.wav"}); err == nil {
		t.Error("Play() after Close returned no error")
	}
	if err := p.SeekBy(time.Second); err == nil {
		t.Error("SeekBy() after Close returned no error")
	}

	// Transport calls after Close are no-ops, not panics.
	p.Pause()
	p.Resume()
	p.TogglePause()
	p.Stop()
	p.SetVolume(10)
}

func TestEventBufferFullDoesNotDropEvents(t *testing.T) {
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(nil, nil) // immediate EOF with an empty ring
	}}
	sink := &fakeSink{}
	p, err := New(dec, sink, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	playToFinish := func(i int) {
		t.Helper()
		if err := p.Play(queue.Item{Path: fmt.Sprintf("/music/event-%d.wav", i)}); err != nil {
			t.Fatalf("Play(%d) error = %v", i, err)
		}
		waitFor(t, fmt.Sprintf("item %d to finish", i), func() bool {
			return p.Status().State == StateStopped
		})
	}

	// Produce one more event than the buffer can hold without reading. The
	// monitor must block on delivery rather than drop the last finished event.
	total := eventBuffer + 1
	for i := 0; i < total; i++ {
		playToFinish(i)
	}
	for i := 0; i < total; i++ {
		select {
		case ev, ok := <-p.Events():
			if !ok {
				t.Fatalf("events channel closed after %d of %d events", i, total)
			}
			if ev.Type != EventFinished {
				t.Fatalf("event %d = %+v, want finished", i, ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for event %d of %d", i+1, total)
		}
	}
	expectNoEvent(t, p, 80*time.Millisecond)

	// Refill the buffer and leave the monitor blocked delivering one more
	// event. Close must unblock it rather than deadlock.
	for i := 0; i < eventBuffer; i++ {
		playToFinish(i)
	}
	playToFinish(eventBuffer)

	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close deadlocked with a full event buffer")
	}

	// Buffered events are still readable; then the channel is closed.
	for range p.Events() {
	}
}

func TestNoGoroutineLeaks(t *testing.T) {
	base := runtime.NumGoroutine()
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Repeat([]byte{0xbb}, bytesPerSecond), nil)
	}}

	for i := 0; i < 10; i++ {
		sink := &fakeSink{}
		p, err := New(dec, sink, Options{})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if err := p.Play(queue.Item{Path: "/music/leak.wav", Duration: time.Second}); err != nil {
			t.Fatalf("Play() error = %v", err)
		}
		if err := p.SeekBy(time.Millisecond); err != nil {
			t.Fatalf("SeekBy() error = %v", err)
		}
		p.Stop()
		if err := p.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := p.Close(); err != nil {
			t.Fatalf("second Close() error = %v", err)
		}
	}

	waitFor(t, "engine goroutines to exit", func() bool {
		return runtime.NumGoroutine() <= base+1
	})
}

func TestFFmpegDecodeToFinished(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}

	path := filepath.Join(t.TempDir(), "tone.wav")
	cmd := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.3",
		"-ar", "48000", "-ac", "2", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg generate WAV: %v\n%s", err, out)
	}

	sink := &fakeSink{}
	p, err := New(ffmpeg.New(""), sink, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer p.Close()

	if err := p.Play(queue.Item{Path: path, Duration: 300 * time.Millisecond}); err != nil {
		t.Fatalf("Play() error = %v", err)
	}

	// Drain the ring like an output device until playback finishes.
	stopDrain := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		ring := sink.Ring()
		buf := make([]byte, 4096)
		for {
			select {
			case <-stopDrain:
				return
			default:
			}
			if ring.Read(buf) == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}()
	defer func() {
		close(stopDrain)
		<-drained
	}()

	ev := waitEvent(t, p)
	if ev.Type != EventFinished {
		t.Fatalf("event = %+v, want finished", ev)
	}
	st := p.Status()
	if st.State != StateStopped {
		t.Errorf("state = %v, want stopped", st.State)
	}
	if st.Position <= 0 || st.Position > 300*time.Millisecond {
		t.Errorf("position = %v, want >0 and <=300ms", st.Position)
	}
}

func TestEventGenerationIdentifiesTheStream(t *testing.T) {
	pcm := bytes.Repeat([]byte{0x11, 0x22}, bytesPerSecond/2)
	dec := &fakeDecoder{openFn: func(audio.Request) audio.Stream {
		return newFakeStream(bytes.Clone(pcm), nil)
	}}
	sink := &fakeSink{}
	p := mustNew(t, dec, sink, Options{})
	item := queue.Item{ID: 1, Path: "/music/one.wav", Title: "one", Duration: time.Second}

	if err := p.Play(item); err != nil {
		t.Fatalf("first Play() error = %v", err)
	}
	first := p.Status().Generation
	if first == 0 {
		t.Fatal("Status().Generation = 0 after Play")
	}
	consume(t, sink, len(pcm))
	ev := waitEvent(t, p)
	if ev.Generation != first {
		t.Errorf("event generation = %d, want %d", ev.Generation, first)
	}

	if err := p.Play(item); err != nil {
		t.Fatalf("second Play() error = %v", err)
	}
	second := p.Status().Generation
	if second == first {
		t.Errorf("generation did not advance across Play calls: %d", second)
	}
	p.Stop()
}
