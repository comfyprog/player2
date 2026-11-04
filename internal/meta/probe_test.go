package meta

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

// fakeFFProbe writes a shell script that records its arguments and invocation
// count, then prints the JSON in output.json from its own directory.
func fakeFFProbe(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "ffprobe")
	writeExecutable(t, bin, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf 'x\n' >> "$d/count"
printf '%s\n' "$@" > "$d/args"
cat "$d/output.json"
`)
	return bin
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

func probeCount(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "count"))
	if err != nil {
		t.Fatalf("read probe count: %v", err)
	}
	return strings.Count(string(data), "\n")
}

func TestParseProbeOutput(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    Info
		wantErr string
	}{
		{
			name:    "case-insensitive tags, track range, format duration",
			fixture: "tagged.json",
			want: Info{
				Title:       "Song Title",
				Artist:      "The Artist",
				Album:       "The Album",
				AlbumArtist: "Album Artist",
				TrackNo:     3,
				Duration:    12345 * time.Millisecond,
				Codec:       "flac",
				SampleRate:  44100,
				Channels:    2,
			},
		},
		{
			name:    "first audio stream and stream duration fallback",
			fixture: "stream_duration.json",
			want: Info{
				Title:       "Fallback",
				Artist:      "A",
				Album:       "B",
				AlbumArtist: "C",
				TrackNo:     7,
				Duration:    9500 * time.Millisecond,
				Codec:       "mp3",
				SampleRate:  48000,
				Channels:    1,
			},
		},
		{
			name:    "missing duration is allowed as zero",
			fixture: "missing_duration.json",
			want: Info{
				Title:      "No Duration",
				Codec:      "pcm_s16le",
				SampleRate: 44100,
				Channels:   2,
			},
		},
		{
			name:    "unparseable optional tags are ignored",
			fixture: "bad_tags.json",
			want: Info{
				Title: "Bad Tags",
				Codec: "flac",
			},
		},
		{
			name:    "no audio stream is an error",
			fixture: "no_audio.json",
			wantErr: "no audio stream",
		},
		{
			name:    "malformed json is an error",
			fixture: "malformed.json",
			wantErr: "parse ffprobe output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProbeOutput(readFixture(t, tt.fixture))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseProbeOutput() = %+v, want error containing %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseProbeOutput() error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProbeOutput() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("parseProbeOutput() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestProbeArgs(t *testing.T) {
	for _, path := range []string{
		"/music/plain.mp3",
		`/music/with space/quote's "track".flac`,
		"-leading-dash.mp3",
		"/music/semi;colon $(subshell).wav",
	} {
		got := probeArgs(path)
		want := []string{
			"-v", "error",
			"-protocol_whitelist", "file",
			"-show_format", "-show_streams",
			"-of", "json",
			"-i", path,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("probeArgs(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestNewFFProbeDefaults(t *testing.T) {
	p := NewFFProbe("ffprobe")
	if p.bin != "ffprobe" {
		t.Errorf("bin = %q, want %q", p.bin, "ffprobe")
	}
	if p.timeout != 5*time.Second {
		t.Errorf("timeout = %s, want 5s", p.timeout)
	}
	if p.cache == nil {
		t.Error("cache is nil, want initialized map")
	}
}

func TestLocalPath(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "/music/a.mp3", want: "/music/a.mp3"},
		{in: "relative/a.mp3", want: "relative/a.mp3"},
		{in: "file:///music/a%20b.mp3", want: "/music/a b.mp3"},
		{in: "file://localhost/music/a.mp3", want: "/music/a.mp3"},
		{in: "http://example.com/a.mp3", wantErr: "remote URL"},
		{in: "https://example.com/a.mp3", wantErr: "remote URL"},
		{in: "ftp://example.com/a.mp3", wantErr: "remote URL"},
		{in: "file://otherhost/music/a.mp3", wantErr: "host"},
	}

	for _, tt := range tests {
		got, err := localPath(tt.in)
		if tt.wantErr != "" {
			if err == nil {
				t.Errorf("localPath(%q) = %q, want error containing %q", tt.in, got, tt.wantErr)
				continue
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("localPath(%q) error = %q, want it to contain %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("localPath(%q) error = %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("localPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestProbeCacheKeyedByPathSizeAndModTime(t *testing.T) {
	dir := t.TempDir()
	bin := fakeFFProbe(t, dir)
	writeFile(t, filepath.Join(dir, "output.json"), string(readFixture(t, "tagged.json")))

	audio := filepath.Join(dir, "track.mp3")
	writeFile(t, audio, "first")

	p := NewFFProbe(bin)
	first, err := p.Probe(context.Background(), audio)
	if err != nil {
		t.Fatalf("first Probe() error = %v", err)
	}
	if count := probeCount(t, dir); count != 1 {
		t.Fatalf("probe executions after first call = %d, want 1", count)
	}

	second, err := p.Probe(context.Background(), audio)
	if err != nil {
		t.Fatalf("cached Probe() error = %v", err)
	}
	if second != first {
		t.Errorf("cached Probe() = %+v, want same values as %+v", second, first)
	}
	if count := probeCount(t, dir); count != 1 {
		t.Errorf("probe executions after cache hit = %d, want 1", count)
	}

	args := readLines(t, filepath.Join(dir, "args"))
	if got := args[len(args)-1]; got != audio {
		t.Errorf("ffprobe path argument = %q, want %q", got, audio)
	}
	if len(args) != len(probeArgs(audio)) {
		t.Errorf("ffprobe received %d args, want %d: %q", len(args), len(probeArgs(audio)), args)
	}

	// A size change is a cache miss.
	writeFile(t, audio, "second, longer contents")
	if _, err := p.Probe(context.Background(), audio); err != nil {
		t.Fatalf("Probe() after size change error = %v", err)
	}
	if count := probeCount(t, dir); count != 2 {
		t.Errorf("probe executions after size change = %d, want 2", count)
	}

	// Same size, changed mtime is also a cache miss.
	old := time.Now().Add(-time.Hour).Round(0)
	if err := os.Chtimes(audio, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if _, err := p.Probe(context.Background(), audio); err != nil {
		t.Fatalf("Probe() after mtime change error = %v", err)
	}
	if count := probeCount(t, dir); count != 3 {
		t.Errorf("probe executions after mtime change = %d, want 3", count)
	}

	// A different path is a different key even with identical metadata.
	other := filepath.Join(dir, "other.mp3")
	writeFile(t, other, "second, longer contents")
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if _, err := p.Probe(context.Background(), other); err != nil {
		t.Fatalf("Probe() for different path error = %v", err)
	}
	if count := probeCount(t, dir); count != 4 {
		t.Errorf("probe executions after different path = %d, want 4", count)
	}
}

func TestProbeDoesNotCacheErrors(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffprobe")
	writeExecutable(t, bin, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf 'x\n' >> "$d/count"
echo 'decoder exploded' >&2
exit 3
`)

	audio := filepath.Join(dir, "track.mp3")
	writeFile(t, audio, "not really audio")

	p := NewFFProbe(bin)
	for i := 0; i < 2; i++ {
		_, err := p.Probe(context.Background(), audio)
		if err == nil {
			t.Fatalf("Probe() call %d error = nil, want failure", i+1)
		}
		for _, want := range []string{"ffprobe", audio, "decoder exploded"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Probe() error = %q, want it to contain %q", err, want)
			}
		}
	}
	if count := probeCount(t, dir); count != 2 {
		t.Errorf("probe executions = %d, want 2 (failures must not be cached)", count)
	}
}

func TestProbeBadOutput(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		wantErr string
	}{
		{name: "malformed json", fixture: "malformed.json", wantErr: "parse ffprobe output"},
		{name: "no audio stream", fixture: "no_audio.json", wantErr: "no audio stream"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "ffprobe")
			writeExecutable(t, bin, `d=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cat "$d/output.json"
`)
			writeFile(t, filepath.Join(dir, "output.json"), string(readFixture(t, tt.fixture)))
			audio := filepath.Join(dir, "track.mp3")
			writeFile(t, audio, "not really audio")

			_, err := NewFFProbe(bin).Probe(context.Background(), audio)
			if err == nil {
				t.Fatalf("Probe() error = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Probe() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestProbeMissingBinary(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "track.mp3")
	writeFile(t, audio, "not really audio")
	bin := filepath.Join(dir, "missing-ffprobe")

	_, err := NewFFProbe(bin).Probe(context.Background(), audio)
	if err == nil {
		t.Fatal("Probe() error = nil, want missing binary error")
	}
	for _, want := range []string{bin, "not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Probe() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestProbeTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffprobe")
	writeExecutable(t, bin, "exec sleep 5\n")
	audio := filepath.Join(dir, "track.mp3")
	writeFile(t, audio, "not really audio")

	check := func(t *testing.T, p *FFProbe, ctx context.Context) {
		t.Helper()
		start := time.Now()
		_, err := p.Probe(ctx, audio)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Probe() took %s, want it to respect the timeout", elapsed)
		}
		if err == nil {
			t.Fatal("Probe() error = nil, want timeout error")
		}
		for _, want := range []string{"timed out", bin, audio} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Probe() error = %q, want it to contain %q", err, want)
			}
		}
	}

	t.Run("configured timeout", func(t *testing.T) {
		p := NewFFProbe(bin)
		p.timeout = 50 * time.Millisecond
		check(t, p, context.Background())
	})

	t.Run("earlier caller deadline", func(t *testing.T) {
		p := NewFFProbe(bin)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		check(t, p, ctx)
	})
}

func TestProbeRejectsRemoteURL(t *testing.T) {
	_, err := NewFFProbe("ffprobe").Probe(context.Background(), "https://example.com/track.mp3")
	if err == nil {
		t.Fatal("Probe() error = nil, want remote URL rejection")
	}
	if !strings.Contains(err.Error(), "remote URL") {
		t.Errorf("Probe() error = %q, want remote URL rejection", err)
	}
}

func TestProbePassesPathVerbatim(t *testing.T) {
	dir := t.TempDir()
	bin := fakeFFProbe(t, dir)
	writeFile(t, filepath.Join(dir, "output.json"), string(readFixture(t, "tagged.json")))

	name := `- odd 'quoted' name; $(not-a-shell).mp3`
	audio := filepath.Join(dir, name)
	writeFile(t, audio, "not really audio")

	t.Chdir(dir)
	p := NewFFProbe(bin)
	if _, err := p.Probe(context.Background(), name); err != nil {
		t.Fatalf("Probe(%q) error = %v", name, err)
	}

	args := readLines(t, filepath.Join(dir, "args"))
	if got := args[len(args)-1]; got != name {
		t.Errorf("ffprobe path argument = %q, want verbatim %q", got, name)
	}
}

func TestProbeConcurrent(t *testing.T) {
	dir := t.TempDir()
	bin := fakeFFProbe(t, dir)
	writeFile(t, filepath.Join(dir, "output.json"), string(readFixture(t, "tagged.json")))
	audio := filepath.Join(dir, "track.mp3")
	writeFile(t, audio, "not really audio")

	p := NewFFProbe(bin)
	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := p.Probe(context.Background(), audio)
			if err != nil {
				errs <- err
				return
			}
			if info.Title != "Song Title" {
				errs <- fmt.Errorf("Title = %q, want %q", info.Title, "Song Title")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if count := probeCount(t, dir); count < 1 {
		t.Errorf("probe executions = %d, want at least 1", count)
	}
}

// TestProbeIntegration exercises the real ffprobe binary against a file
// generated by ffmpeg. It is skipped when either tool is unavailable.
func TestProbeIntegration(t *testing.T) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not available")
	}

	path := filepath.Join(t.TempDir(), "tagged track.flac")
	cmd := exec.Command(ffmpegBin,
		"-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2.0",
		"-metadata", "title=Integration Title",
		"-metadata", "artist=Integration Artist",
		"-metadata", "album=Integration Album",
		"-metadata", "album_artist=Integration Album Artist",
		"-metadata", "track=3/12",
		"-c:a", "flac",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture with ffmpeg: %v: %s", err, out)
	}

	info, err := NewFFProbe(ffprobeBin).Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}

	if info.Title != "Integration Title" {
		t.Errorf("Title = %q, want %q", info.Title, "Integration Title")
	}
	if info.Artist != "Integration Artist" {
		t.Errorf("Artist = %q, want %q", info.Artist, "Integration Artist")
	}
	if info.Album != "Integration Album" {
		t.Errorf("Album = %q, want %q", info.Album, "Integration Album")
	}
	if info.AlbumArtist != "Integration Album Artist" {
		t.Errorf("AlbumArtist = %q, want %q", info.AlbumArtist, "Integration Album Artist")
	}
	if info.TrackNo != 3 {
		t.Errorf("TrackNo = %d, want 3", info.TrackNo)
	}
	if info.Codec != "flac" {
		t.Errorf("Codec = %q, want %q", info.Codec, "flac")
	}
	if info.SampleRate != 44100 {
		t.Errorf("SampleRate = %d, want 44100", info.SampleRate)
	}
	if info.Channels != 1 {
		t.Errorf("Channels = %d, want 1", info.Channels)
	}
	if diff := info.Duration - 2*time.Second; diff < -250*time.Millisecond || diff > 250*time.Millisecond {
		t.Errorf("Duration = %s, want about 2s", info.Duration)
	}
}
