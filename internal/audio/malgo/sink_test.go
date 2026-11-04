package malgo

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"player2/internal/audio"
)

func putS16(buf []byte, v int16) {
	binary.LittleEndian.PutUint16(buf, uint16(v))
}

func getS16(buf []byte) int16 {
	return int16(binary.LittleEndian.Uint16(buf))
}

func allZero(p []byte) bool {
	for _, b := range p {
		if b != 0 {
			return false
		}
	}
	return true
}

func s16StereoFrames(n int, left, right int16) []byte {
	out := make([]byte, n*4)
	for i := 0; i < n; i++ {
		putS16(out[i*4:], left)
		putS16(out[i*4+2:], right)
	}
	return out
}

func writeAll(t *testing.T, r *audio.Ring, p []byte) {
	t.Helper()
	if n, err := r.Write(p); n != len(p) || err != nil {
		t.Fatalf("Ring.Write() = (%d, %v), want (%d, nil)", n, err, len(p))
	}
}

func TestProcessorStereoInterleaving(t *testing.T) {
	ring := audio.NewRing(1 << 12)
	data := make([]byte, 8)
	putS16(data[0:], 1000)
	putS16(data[2:], -1000)
	putS16(data[4:], 2000)
	putS16(data[6:], -2000)
	writeAll(t, ring, data)

	p := newProcessor(ring, 48000, 2, false, 1)
	out := make([]byte, len(data))
	p.render(out, nil, 2)

	if !bytes.Equal(out, data) {
		t.Fatalf("render() = %v, want %v", out, data)
	}
	if consumed := ring.Consumed(); consumed != int64(len(data)) {
		t.Errorf("Consumed() = %d, want %d", consumed, len(data))
	}
}

func TestProcessorGainRamp(t *testing.T) {
	const (
		rate   = 1000 // 10 frames == 10ms
		frames = 100
	)
	ring := audio.NewRing(1 << 16)
	writeAll(t, ring, s16StereoFrames(frames, 10000, -10000))
	p := newProcessor(ring, rate, 2, false, 1)

	// Ramp down to silence and let it settle.
	p.setVolume(0)
	out := make([]byte, 10*4)
	p.render(out, nil, 10)
	p.render(out, nil, 10)
	if !allZero(out) {
		t.Fatalf("render at volume 0 = %v, want silence", out)
	}

	// A volume change must ramp instead of jumping.
	p.setVolume(1)
	ramp := make([]byte, 10*4)
	p.render(ramp, nil, 10)

	if got := getS16(ramp[0:]); got != 0 {
		t.Errorf("first ramped sample = %d, want 0 (ramp must not jump)", got)
	}
	prev := int16(0)
	for i := 0; i < 10; i++ {
		left := getS16(ramp[i*4:])
		right := getS16(ramp[i*4+2:])
		if left < prev {
			t.Errorf("ramp is not monotonic at frame %d: %d after %d", i, left, prev)
		}
		if left != -right {
			t.Errorf("frame %d channels = (%d, %d), want equal magnitude", i, left, right)
		}
		prev = left
	}
	if got := getS16(ramp[9*4:]); got != 9000 {
		t.Errorf("last ramped sample = %d, want 9000", got)
	}

	// After the ramp window the target gain is reached.
	full := make([]byte, 10*4)
	p.render(full, nil, 10)
	for i := 0; i < 10; i++ {
		if left, right := getS16(full[i*4:]), getS16(full[i*4+2:]); left != 10000 || right != -10000 {
			t.Fatalf("frame %d after ramp = (%d, %d), want (10000, -10000)", i, left, right)
		}
	}
}

func TestProcessorVolumeClamp(t *testing.T) {
	ring := audio.NewRing(1 << 16)
	writeAll(t, ring, s16StereoFrames(100, 10000, -10000))
	p := newProcessor(ring, 1000, 2, false, 1)

	p.setVolume(2)
	p.mu.Lock()
	target := p.target
	p.mu.Unlock()
	if target != 1 {
		t.Errorf("target after SetVolume(2) = %v, want 1", target)
	}

	p.setVolume(-3)
	p.mu.Lock()
	target = p.target
	p.mu.Unlock()
	if target != 0 {
		t.Errorf("target after SetVolume(-3) = %v, want 0", target)
	}

	out := make([]byte, 10*4)
	p.render(out, nil, 10)
	p.render(out, nil, 10)
	if !allZero(out) {
		t.Errorf("render at clamped volume 0 = %v, want silence", out)
	}

	p.setVolume(math.NaN())
	p.mu.Lock()
	target = p.target
	p.mu.Unlock()
	if target != 0 {
		t.Errorf("target after SetVolume(NaN) = %v, want 0", target)
	}
}

func TestProcessorPauseDoesNotConsumeRing(t *testing.T) {
	ring := audio.NewRing(1 << 12)
	data := s16StereoFrames(2, 1234, -1234)
	writeAll(t, ring, data)

	p := newProcessor(ring, 48000, 2, true, 1)
	out := make([]byte, len(data))
	p.render(out, nil, 2)

	if !allZero(out) {
		t.Errorf("paused render = %v, want silence", out)
	}
	if consumed := ring.Consumed(); consumed != 0 {
		t.Errorf("Consumed() while paused = %d, want 0", consumed)
	}

	p.setPaused(false)
	p.render(out, nil, 2)
	if !bytes.Equal(out, data) {
		t.Errorf("render after resume = %v, want %v", out, data)
	}
	if consumed := ring.Consumed(); consumed != int64(len(data)) {
		t.Errorf("Consumed() after resume = %d, want %d", consumed, len(data))
	}
}

func TestProcessorEmptyRingSilence(t *testing.T) {
	ring := audio.NewRing(1 << 12)
	p := newProcessor(ring, 48000, 2, false, 1)

	out := bytes.Repeat([]byte{0xff}, 32)
	p.render(out, nil, 4)

	if !allZero(out) {
		t.Errorf("render(empty) = %v, want silence", out)
	}
	if consumed, written := ring.Consumed(), ring.Written(); consumed != 0 || written != 0 {
		t.Errorf("Consumed/Written = %d/%d, want 0/0", consumed, written)
	}
}

func TestProcessorUnderrunZeroFillsRemainder(t *testing.T) {
	ring := audio.NewRing(1 << 12)
	data := s16StereoFrames(1, 2000, -2000)
	writeAll(t, ring, data)
	p := newProcessor(ring, 48000, 2, false, 1)

	out := bytes.Repeat([]byte{0xff}, 4*4)
	p.render(out, nil, 4)

	if !bytes.Equal(out[:4], data) {
		t.Errorf("render() audio = %v, want %v", out[:4], data)
	}
	if !allZero(out[4:]) {
		t.Errorf("render() underrun tail = %v, want zero-filled", out[4:])
	}
	if consumed := ring.Consumed(); consumed != int64(len(data)) {
		t.Errorf("Consumed() = %d, want %d", consumed, len(data))
	}
}

func TestProcessorCloseSilencesWithoutConsuming(t *testing.T) {
	ring := audio.NewRing(1 << 12)
	writeAll(t, ring, s16StereoFrames(2, 1000, -1000))
	p := newProcessor(ring, 48000, 2, false, 1)
	p.close()

	out := bytes.Repeat([]byte{0xff}, 16)
	p.render(out, nil, 2)

	if !allZero(out) {
		t.Errorf("render after close = %v, want silence", out)
	}
	if consumed := ring.Consumed(); consumed != 0 {
		t.Errorf("Consumed() after close = %d, want 0", consumed)
	}
}

func TestSinkStartValidation(t *testing.T) {
	ring := audio.NewRing(16)
	tests := []struct {
		name     string
		ring     *audio.Ring
		rate     int
		channels int
		want     string
	}{
		{name: "nil ring", ring: nil, rate: 48000, channels: 2, want: "nil ring"},
		{name: "zero rate", ring: ring, rate: 0, channels: 2, want: "sample rate"},
		{name: "negative rate", ring: ring, rate: -48000, channels: 2, want: "sample rate"},
		{name: "negative channels", ring: ring, rate: 48000, channels: -1, want: "channels"},
	}

	sink := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sink.Start(tt.ring, tt.rate, tt.channels)
			if err == nil {
				t.Fatalf("Start() error = nil, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Start() error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestSinkCloseBeforeStartIsIdempotent(t *testing.T) {
	sink := New()
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := sink.Start(audio.NewRing(16), 48000, 2); err == nil {
		t.Error("Start() after Close error = nil, want closed error")
	}
}

func TestSinkDeviceLifecycle(t *testing.T) {
	if os.Getenv("PLAYER2_TEST_AUDIO") != "1" {
		t.Skip("set PLAYER2_TEST_AUDIO=1 to open a real audio device")
	}

	ring := audio.NewRing(1 << 16)
	sink := New()
	sink.SetPaused(true)
	sink.SetVolume(0.25)

	if err := sink.Start(ring, 48000, 2); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = sink.Close() })

	sink.mu.Lock()
	proc := sink.proc
	sink.mu.Unlock()
	if proc == nil {
		t.Fatal("Start() succeeded but processor is nil")
	}
	proc.mu.Lock()
	paused, target := proc.paused, proc.target
	proc.mu.Unlock()
	if !paused {
		t.Error("pre-start SetPaused(true) was not retained")
	}
	if target != 0.25 {
		t.Errorf("pre-start SetVolume(0.25) target = %v, want 0.25", target)
	}

	if err := sink.Start(ring, 48000, 2); err == nil {
		t.Error("second Start() error = nil, want already-started error")
	}

	sink.SetPaused(false)
	sink.SetVolume(0.5)
	time.Sleep(50 * time.Millisecond)

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
