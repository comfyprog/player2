// Package integration exercises the real library, metadata, decoder, and
// playback layers together. Media is generated with the system FFmpeg; the
// playback test uses a fake sink and never opens an audio device.
package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"player2/internal/audio"
	"player2/internal/audio/ffmpeg"
	"player2/internal/library"
	"player2/internal/meta"
	"player2/internal/player"
	"player2/internal/queue"
)

// TestLibraryCueAndPlayback generates a small library, scans it, expands a CUE
// sheet, and plays one CUE track through the real FFmpeg decoder and a fake
// sink.
func TestLibraryCueAndPlayback(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available in PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available in PATH")
	}

	dir := t.TempDir()
	encoders := ffmpegEncoders(t, ffmpegPath)

	// At least FLAC is required for the CUE album and the basic library.
	generateAudio(t, ffmpegPath, filepath.Join(dir, "tone.flac"), "0.7", "flac")
	generated := []string{"tone.flac"}
	for _, spec := range []struct {
		file  string
		codec string
	}{
		{"tone.mp3", "libmp3lame"},
		{"tone.opus", "libopus"},
		{"tone.m4a", "alac"},
	} {
		if !encoders[spec.codec] {
			t.Logf("encoder %s unavailable; skipping %s", spec.codec, spec.file)
			continue
		}
		generateAudio(t, ffmpegPath, filepath.Join(dir, spec.file), "0.7", spec.codec)
		generated = append(generated, spec.file)
	}

	// A six-second single-file album with three two-second CUE tracks.
	generateAudio(t, ffmpegPath, filepath.Join(dir, "album.flac"), "6", "flac")
	writeFile(t, filepath.Join(dir, "album.cue"), `TITLE "Test Album"
PERFORMER "Test Artist"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "First"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Second"
    INDEX 01 00:02:00
  TRACK 03 AUDIO
    TITLE "Third"
    INDEX 01 00:04:00
`)

	// This sheet references a real audio file but has no INDEX 01, so it must
	// be reported as invalid and must not hide tone.flac.
	writeFile(t, filepath.Join(dir, "bad.cue"), `FILE "tone.flac" WAVE
  TRACK 01 AUDIO
    TITLE "Broken"
    INDEX 02 00:00:00
`)

	registry := audio.NewRegistry()
	registry.Register(audio.DefaultAudioExtensions(), ffmpeg.New(ffmpegPath))
	prober := meta.NewFFProbe("ffprobe")
	scanner := library.New(library.Options{
		CueFallbackEncoding: "windows-1252",
		AudioExtensions:     registry.Extensions(),
	}, prober)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	entries, err := scanner.ReadDir(ctx, dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}

	cueEntry, ok := findEntry(entries, func(e library.Entry) bool { return e.Kind == library.KindCueDir })
	if !ok {
		t.Fatalf("no CUE folder in ReadDir entries: %v", entryNames(entries))
	}
	if cueEntry.Name != "album.cue/" {
		t.Errorf("CUE folder name = %q, want album.cue/", cueEntry.Name)
	}
	if filepath.Base(cueEntry.Path) != "album.cue" {
		t.Errorf("CUE folder path = %q, want .../album.cue", cueEntry.Path)
	}

	if _, ok := findEntry(entries, func(e library.Entry) bool { return e.Name == "album.flac" }); ok {
		t.Errorf("album.flac was not hidden by the valid CUE: %v", entryNames(entries))
	}

	for _, name := range generated {
		if _, ok := findEntry(entries, func(e library.Entry) bool {
			return e.Kind == library.KindAudio && e.Name == name
		}); !ok {
			t.Errorf("generated audio %q missing from ReadDir: %v", name, entryNames(entries))
		}
	}

	invalid, ok := findEntry(entries, func(e library.Entry) bool { return e.Kind == library.KindInvalidCue })
	if !ok {
		t.Fatalf("bad.cue was not reported as an invalid CUE: %v", entryNames(entries))
	}
	if invalid.Err == nil {
		t.Error("invalid CUE entry has a nil error")
	}
	if _, ok := findEntry(entries, func(e library.Entry) bool {
		return e.Kind == library.KindAudio && e.Name == "tone.flac"
	}); !ok {
		t.Errorf("invalid CUE hid tone.flac: %v", entryNames(entries))
	}

	tracks, err := scanner.ReadCue(ctx, cueEntry.Path)
	if err != nil {
		t.Fatalf("ReadCue: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("ReadCue returned %d tracks, want 3", len(tracks))
	}

	wantStarts := []time.Duration{0, 2 * time.Second, 4 * time.Second}
	wantTitles := []string{"First", "Second", "Third"}
	for i, track := range tracks {
		if track.Kind != library.KindCueTrack {
			t.Errorf("track %d kind = %v, want KindCueTrack", i+1, track.Kind)
		}
		if track.TrackNo != i+1 {
			t.Errorf("track %d number = %d", i+1, track.TrackNo)
		}
		if track.Start != wantStarts[i] {
			t.Errorf("track %d start = %v, want %v", i+1, track.Start, wantStarts[i])
		}
		if !durationNear(track.Duration, 2*time.Second, 100*time.Millisecond) {
			t.Errorf("track %d duration = %v, want ~2s", i+1, track.Duration)
		}
		if track.Meta.Title != wantTitles[i] {
			t.Errorf("track %d title = %q, want %q", i+1, track.Meta.Title, wantTitles[i])
		}
		if track.Meta.Album != "Test Album" {
			t.Errorf("track %d album = %q, want Test Album", i+1, track.Meta.Album)
		}
		if track.Meta.Artist != "Test Artist" {
			t.Errorf("track %d artist = %q, want Test Artist", i+1, track.Meta.Artist)
		}
		if track.Meta.TrackNo != i+1 {
			t.Errorf("track %d metadata number = %d", i+1, track.Meta.TrackNo)
		}
	}

	resolved, err := scanner.ResolveCueTrack(ctx, cueEntry.Path, 2)
	if err != nil {
		t.Fatalf("ResolveCueTrack(2): %v", err)
	}
	if resolved.TrackNo != 2 || resolved.Start != 2*time.Second || resolved.Meta.Title != "Second" {
		t.Errorf("ResolveCueTrack(2) = %+v, want track 2 at 2s titled Second", resolved)
	}

	// Build a queue containing the three CUE tracks and play the second one.
	q := queue.New()
	for _, track := range tracks {
		q.Append(queue.Item{
			Path:     track.Path,
			CuePath:  track.CuePath,
			CueTrack: track.TrackNo,
			Start:    track.Start,
			Duration: track.Duration,
			Title:    track.Meta.Title,
			Artist:   track.Meta.Artist,
			Album:    track.Meta.Album,
			TrackNo:  track.TrackNo,
		})
	}
	if !q.SetCurrent(1) {
		t.Fatal("SetCurrent(1) failed")
	}
	item, ok := q.Current()
	if !ok {
		t.Fatal("queue has no current item")
	}
	if item.CueTrack != 2 || item.Start != 2*time.Second {
		t.Fatalf("current item = %+v, want CUE track 2 starting at 2s", item)
	}

	sink := &fakeSink{}
	engine, err := player.New(registry, sink, player.Options{
		SampleRate:    48000,
		Channels:      2,
		BufferSeconds: 2,
	})
	if err != nil {
		t.Fatalf("player.New: %v", err)
	}
	defer func() {
		if err := engine.Close(); err != nil {
			t.Errorf("player.Close: %v", err)
		}
	}()

	if err := engine.Play(item); err != nil {
		t.Fatalf("Play(%q at %v): %v", item.Path, item.Start, err)
	}

	// Drain exactly the track's PCM. The monitor completes the item once the
	// consumed position reaches its duration.
	bytesPerSecond := 48000 * 2 * 2
	wantBytes := int(item.Duration * time.Duration(bytesPerSecond) / time.Second)
	drainRing(t, sink, wantBytes, 10*time.Second)

	select {
	case event, ok := <-engine.Events():
		if !ok {
			t.Fatal("events channel closed before EventFinished")
		}
		if event.Type != player.EventFinished {
			t.Fatalf("event = %+v (err %v), want EventFinished", event, event.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for EventFinished")
	}

	status := engine.Status()
	if !durationNear(status.Position, item.Duration, 50*time.Millisecond) {
		t.Errorf("final position = %v, want ~%v", status.Position, item.Duration)
	}
	if status.State != player.StateStopped {
		t.Errorf("final state = %v, want stopped", status.State)
	}
	if item.Start != 2*time.Second || !durationNear(item.Duration, 2*time.Second, 100*time.Millisecond) {
		t.Errorf("played item start/duration = %v/%v, want 2s/~2s", item.Start, item.Duration)
	}

	if err := engine.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !sink.isClosed() {
		t.Error("sink was not closed")
	}
	// Close closes the event channel; this also proves the monitor goroutine
	// exited instead of hanging.
	select {
	case _, ok := <-engine.Events():
		if ok {
			t.Error("received an unexpected event after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event channel did not close")
	}
}

// fakeSink captures the player's ring without opening a device.
type fakeSink struct {
	mu       sync.Mutex
	ring     *audio.Ring
	channels int
	rate     int
	closed   bool
}

func (s *fakeSink) Start(ring *audio.Ring, rate, channels int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring = ring
	s.rate = rate
	s.channels = channels
	return nil
}

func (s *fakeSink) SetPaused(bool)    {}
func (s *fakeSink) SetVolume(float64) {}
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

func (s *fakeSink) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// ffmpegEncoders returns the set of encoder names compiled into ffmpeg.
func ffmpegEncoders(t *testing.T, ffmpegPath string) map[string]bool {
	t.Helper()
	out, err := exec.Command(ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil {
		t.Fatalf("list ffmpeg encoders: %v", err)
	}
	encoders := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		encoders[fields[1]] = true
	}
	return encoders
}

// generateAudio renders a sine tone with the requested encoder.
func generateAudio(t *testing.T, ffmpegPath, path, seconds, codec string) {
	t.Helper()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=440:sample_rate=44100:duration=%s", seconds),
		"-c:a", codec,
		path,
	}
	if out, err := exec.Command(ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("generate %s with %s: %v\n%s", path, codec, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// findEntry returns the first entry matching pred.
func findEntry(entries []library.Entry, pred func(library.Entry) bool) (library.Entry, bool) {
	for _, entry := range entries {
		if pred(entry) {
			return entry, true
		}
	}
	return library.Entry{}, false
}

func entryNames(entries []library.Entry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = fmt.Sprintf("%s(%d)", entry.Name, entry.Kind)
	}
	return names
}

// durationNear reports whether got is within tolerance of want.
func durationNear(got, want, tolerance time.Duration) bool {
	delta := got - want
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

// drainRing consumes n bytes of PCM from the sink, waiting for the decoder to
// supply them and failing the test on timeout.
func drainRing(t *testing.T, sink *fakeSink, n int, timeout time.Duration) {
	t.Helper()
	ring := sink.Ring()
	if ring == nil {
		t.Fatal("sink was not started")
	}
	buf := make([]byte, 4096)
	got := 0
	deadline := time.Now().Add(timeout)
	for got < n {
		want := n - got
		if want > len(buf) {
			want = len(buf)
		}
		read := ring.Read(buf[:want])
		got += read
		if read == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("timed out draining ring: got %d of %d bytes", got, n)
			}
			time.Sleep(time.Millisecond)
		}
	}
}
