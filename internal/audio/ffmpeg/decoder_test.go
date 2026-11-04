package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"player2/internal/audio"
)

func asStream(t *testing.T, st audio.Stream) *stream {
	t.Helper()
	s, ok := st.(*stream)
	if !ok {
		t.Fatalf("stream type = %T, want *stream", st)
	}
	return s
}

func TestBuildArgs(t *testing.T) {
	req := audio.Request{
		Path:       `/music/with space/quote's "- dash"; $(not-a-shell).flac`,
		Start:      1500 * time.Millisecond,
		SampleRate: 44100,
		Channels:   1,
	}
	want := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-protocol_whitelist", "file",
		"-ss", "1.5",
		"-i", req.Path,
		"-map", "0:a:0", "-vn", "-sn", "-dn",
		"-f", "s16le", "-acodec", "pcm_s16le",
		"-ar", "44100", "-ac", "1",
		"pipe:1",
	}
	if got := buildArgs(req); !reflect.DeepEqual(got, want) {
		t.Errorf("buildArgs() = %q, want %q", got, want)
	}
}

func TestBuildArgsDefaults(t *testing.T) {
	tests := []struct {
		name     string
		sample   int
		channels int
	}{
		{name: "zero", sample: 0, channels: 0},
		{name: "negative", sample: -1, channels: -2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildArgs(audio.Request{Path: "/music/a.mp3", SampleRate: tt.sample, Channels: tt.channels})
			if got := valueAfter(args, "-ss"); got != "0" {
				t.Errorf("-ss = %q, want %q", got, "0")
			}
			if got := valueAfter(args, "-ar"); got != "48000" {
				t.Errorf("-ar = %q, want %q", got, "48000")
			}
			if got := valueAfter(args, "-ac"); got != "2" {
				t.Errorf("-ac = %q, want %q", got, "2")
			}
		})
	}
}

// TestFormatSeconds pins the argv seconds format: up to six decimal places,
// rounded to the nearest microsecond, with trailing zeros trimmed. Always
// emitting -ss (even "0") keeps the argv stable and easy to assert.
func TestFormatSeconds(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{in: 0, want: "0"},
		{in: -time.Second, want: "0"},
		{in: time.Microsecond, want: "0.000001"},
		{in: 250 * time.Millisecond, want: "0.25"},
		{in: 1500 * time.Millisecond, want: "1.5"},
		{in: 2 * time.Second, want: "2"},
		{in: 2*time.Second + 100*time.Millisecond, want: "2.1"},
		{in: 1234567 * time.Microsecond, want: "1.234567"},
		{in: 1234567 * time.Nanosecond, want: "0.001235"}, // rounds to microseconds
		{in: 999999950 * time.Nanosecond, want: "1"},
		{in: time.Hour, want: "3600"},
	}

	for _, tt := range tests {
		if got := formatSeconds(tt.in); got != tt.want {
			t.Errorf("formatSeconds(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func valueAfter(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func writeScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", data, err)
	}
	return pid
}

func waitForLineCount(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Count(string(data), "\n") >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lines in %s", want, path)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still running after close", pid)
}

func TestOpenArgsAndStdout(t *testing.T) {
	dir := t.TempDir()
	payload := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	if err := os.WriteFile(filepath.Join(dir, "payload"), payload, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	bin := writeScript(t, dir, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$@" > "$d/args"
cat "$d/payload"
`)

	path := filepath.Join(dir, `- odd 'quoted' name; $(not-a-shell).mp3`)
	req := audio.Request{Path: path, Start: 2 * time.Second, SampleRate: 44100, Channels: 1}

	stream, err := New(bin).Open(context.Background(), req)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decoded %v, want %v", got, payload)
	}

	s := asStream(t, stream)
	if s.cmd.ProcessState == nil {
		t.Error("ProcessState = nil after natural EOF, want non-nil")
	}
	if err := stream.Close(); err != nil {
		t.Errorf("Close() after EOF error = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("second Close() error = %v, want idempotent close", err)
	}

	args := readLines(t, filepath.Join(dir, "args"))
	want := buildArgs(req)
	if !reflect.DeepEqual(args, want) {
		t.Errorf("ffmpeg received args %q, want %q", args, want)
	}
}

func TestOpenNormalizesFileURL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	bin := writeScript(t, dir, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$@" > "$d/args"
cat "$d/payload"
`)

	stream, err := New(bin).Open(context.Background(), audio.Request{
		Path: "file:///music/with%20space/track.mp3",
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	args := readLines(t, filepath.Join(dir, "args"))
	if got := valueAfter(args, "-i"); got != "/music/with space/track.mp3" {
		t.Errorf("-i = %q, want normalized local path", got)
	}
}

func TestOpenAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$@" > "$d/args"
`)

	stream, err := New(bin).Open(context.Background(), audio.Request{Path: "/music/a.mp3", SampleRate: -1, Channels: 0})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	args := readLines(t, filepath.Join(dir, "args"))
	if got := valueAfter(args, "-ar"); got != "48000" {
		t.Errorf("-ar = %q, want 48000", got)
	}
	if got := valueAfter(args, "-ac"); got != "2" {
		t.Errorf("-ac = %q, want 2", got)
	}
	if got := valueAfter(args, "-ss"); got != "0" {
		t.Errorf("-ss = %q, want 0", got)
	}
}

func TestOpenRejectsRemotePath(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "echo should-not-run\n")

	for _, path := range []string{
		"https://example.com/track.mp3",
		"ftp://example.com/track.flac",
		"file://otherhost/music/track.mp3",
	} {
		_, err := New(bin).Open(context.Background(), audio.Request{Path: path})
		if err == nil {
			t.Fatalf("Open(%q) error = nil, want remote path rejection", path)
		}
		if !strings.Contains(err.Error(), "remote") && !strings.Contains(err.Error(), "host") {
			t.Errorf("Open(%q) error = %q, want remote/host rejection", path, err)
		}
	}
}

func TestOpenMissingBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "missing-ffmpeg")
	path := "/music/track.mp3"

	_, err := New(bin).Open(context.Background(), audio.Request{Path: path})
	if err == nil {
		t.Fatal("Open() error = nil, want missing binary error")
	}
	for _, want := range []string{bin, path, "not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Open() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestCloseKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
echo $$ > "$d/main.pid"
sleep 30 &
echo $! > "$d/child.pid"
wait
`)

	stream, err := New(bin).Open(context.Background(), audio.Request{Path: "/music/a.mp3"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	waitForFile(t, filepath.Join(dir, "main.pid"))
	waitForFile(t, filepath.Join(dir, "child.pid"))
	mainPID := readPID(t, filepath.Join(dir, "main.pid"))
	childPID := readPID(t, filepath.Join(dir, "child.pid"))

	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	s := asStream(t, stream)
	if s.cmd.ProcessState == nil {
		t.Fatal("ProcessState = nil after Close, want non-nil")
	}
	if err := stream.Close(); err != nil {
		t.Errorf("second Close() error = %v, want idempotent close", err)
	}
	if _, err := stream.Read(make([]byte, 8)); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Read() after Close error = %v, want %v", err, io.ErrClosedPipe)
	}

	waitGone(t, mainPID)
	waitGone(t, childPID)
}

func TestContextCancelKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
echo $$ > "$d/main.pid"
sleep 30 &
echo $! > "$d/child.pid"
wait
`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := New(bin).Open(ctx, audio.Request{Path: "/music/a.mp3"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	waitForFile(t, filepath.Join(dir, "main.pid"))
	waitForFile(t, filepath.Join(dir, "child.pid"))
	mainPID := readPID(t, filepath.Join(dir, "main.pid"))
	childPID := readPID(t, filepath.Join(dir, "child.pid"))

	cancel()

	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(stream)
		readDone <- err
	}()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Read blocked after context cancellation")
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	s := asStream(t, stream)
	if s.cmd.ProcessState == nil {
		t.Fatal("ProcessState = nil after cancellation, want non-nil")
	}

	waitGone(t, mainPID)
	waitGone(t, childPID)
}

func TestStderrIncludedInDecodeError(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, `echo 'decoder exploded' >&2
exit 2
`)

	stream, err := New(bin).Open(context.Background(), audio.Request{Path: "/music/broken.mp3"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_, err = io.ReadAll(stream)
	if err == nil {
		t.Fatal("ReadAll() error = nil, want decode failure")
	}
	for _, want := range []string{"decoder exploded", "/music/broken.mp3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ReadAll() error = %q, want it to contain %q", err, want)
		}
	}
	// Closing after a failed decode must not panic or wait twice.
	_ = stream.Close()
	if s := asStream(t, stream); s.cmd.ProcessState == nil {
		t.Error("ProcessState = nil after failed decode, want non-nil")
	}
}

func TestRepeatedOpenCloseLeavesNoProcess(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
echo $$ >> "$d/pids"
sleep 30
`)

	for i := 0; i < 3; i++ {
		stream, err := New(bin).Open(context.Background(), audio.Request{Path: "/music/a.mp3"})
		if err != nil {
			t.Fatalf("Open() iteration %d error = %v", i, err)
		}
		waitForLineCount(t, filepath.Join(dir, "pids"), i+1)
		if err := stream.Close(); err != nil {
			t.Fatalf("Close() iteration %d error = %v", i, err)
		}
		if s := asStream(t, stream); s.cmd.ProcessState == nil {
			t.Fatalf("iteration %d: ProcessState = nil after Close", i)
		}
	}

	pids := readLines(t, filepath.Join(dir, "pids"))
	if len(pids) != 3 {
		t.Fatalf("recorded %d pids, want 3", len(pids))
	}
	for _, raw := range pids {
		pid, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("parse pid %q: %v", raw, err)
		}
		waitGone(t, pid)
	}
}

type integrationCase struct {
	name string
	ext  string
	args []string
}

func TestIntegrationDecode(t *testing.T) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}

	cases := []integrationCase{
		{name: "mp3", ext: ".mp3", args: []string{"-c:a", "libmp3lame", "-b:a", "128k"}},
		{name: "flac", ext: ".flac", args: []string{"-c:a", "flac"}},
		{name: "opus", ext: ".opus", args: []string{"-c:a", "libopus", "-b:a", "96k"}},
		{name: "alac", ext: ".m4a", args: []string{"-c:a", "alac"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tone"+tc.ext)
			args := []string{
				"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "sine=frequency=440:duration=1.5",
			}
			args = append(args, tc.args...)
			args = append(args, path)
			if out, err := exec.Command(ffmpegBin, args...).CombinedOutput(); err != nil {
				t.Skipf("encoder for %s unavailable: %v: %s", tc.name, err, out)
			}

			decoder := New(ffmpegBin)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			t.Run("full length", func(t *testing.T) {
				data, err := readAll(ctx, decoder, audio.Request{
					Path: path, SampleRate: 48000, Channels: 2,
				})
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				assertDuration(t, data, 48000, 2, 1500*time.Millisecond, 300*time.Millisecond)
			})

			t.Run("seek", func(t *testing.T) {
				data, err := readAll(ctx, decoder, audio.Request{
					Path: path, Start: 500 * time.Millisecond, SampleRate: 48000, Channels: 2,
				})
				if err != nil {
					t.Fatalf("decode with seek: %v", err)
				}
				assertDuration(t, data, 48000, 2, time.Second, 300*time.Millisecond)
			})
		})
	}
}

func TestRegistryIntegrationDecode(t *testing.T) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}

	path := filepath.Join(t.TempDir(), "registry tone.flac")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1.5",
		"-c:a", "flac", path,
	}
	if out, err := exec.Command(ffmpegBin, args...).CombinedOutput(); err != nil {
		t.Skipf("flac encoder unavailable: %v: %s", err, out)
	}

	registry := audio.NewRegistry()
	registry.Register(audio.DefaultAudioExtensions(), New(ffmpegBin))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	data, err := readAllWithOpener(ctx, func(req audio.Request) (audio.Stream, error) {
		return registry.OpenPath(ctx, req.Path, req.Start)
	}, audio.Request{Path: path})
	if err != nil {
		t.Fatalf("registry decode: %v", err)
	}
	assertDuration(t, data, 48000, 2, 1500*time.Millisecond, 300*time.Millisecond)
}

func readAll(ctx context.Context, decoder *Decoder, req audio.Request) ([]byte, error) {
	return readAllWithOpener(ctx, func(req audio.Request) (audio.Stream, error) {
		return decoder.Open(ctx, req)
	}, req)
}

func readAllWithOpener(_ context.Context, open func(audio.Request) (audio.Stream, error), req audio.Request) ([]byte, error) {
	stream, err := open(req)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	return data, closeErr
}

func assertDuration(t *testing.T, data []byte, rate, channels int, want, tolerance time.Duration) {
	t.Helper()
	bytesPerSecond := rate * channels * 2
	if bytesPerSecond == 0 {
		t.Fatal("bytesPerSecond is zero")
	}
	got := time.Duration(float64(len(data)) / float64(bytesPerSecond) * float64(time.Second))
	if diff := got - want; diff < -tolerance || diff > tolerance {
		t.Errorf("decoded %s (%d bytes), want %s ± %s", got, len(data), want, tolerance)
	}
}
