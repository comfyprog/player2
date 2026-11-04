package audio

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// nopStream is a Stream that immediately reports EOF.
type nopStream struct{}

func (nopStream) Read([]byte) (int, error) { return 0, io.EOF }
func (nopStream) Close() error             { return nil }

// stubDecoder records every Open request so tests can assert pass-through.
type stubDecoder struct {
	mu       sync.Mutex
	requests []Request
	err      error
}

func (d *stubDecoder) Open(_ context.Context, req Request) (Stream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, req)
	if d.err != nil {
		return nil, d.err
	}
	return nopStream{}, nil
}

func (d *stubDecoder) lastRequest(t *testing.T) Request {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) == 0 {
		t.Fatal("decoder was not called")
	}
	return d.requests[len(d.requests)-1]
}

func (d *stubDecoder) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

func TestRegisterNormalizesAndExtensionsSorted(t *testing.T) {
	first := &stubDecoder{}
	second := &stubDecoder{}

	var registry Registry // zero value must work
	registry.Register([]string{"MP3", ".flac", "ogg"}, first)
	registry.Register([]string{".mp3"}, second) // last registration wins
	registry.Register(nil, second)
	registry.Register([]string{}, second)
	registry.Register([]string{".wav"}, nil)
	registry.Register([]string{"", "  ", ".  ", "."}, second)

	want := []string{".flac", ".mp3", ".ogg"}
	got := registry.Extensions()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extensions() = %q, want %q", got, want)
	}

	// Extensions must be a copy: callers cannot mutate registry state.
	got[0] = ".zzz"
	if again := registry.Extensions(); !reflect.DeepEqual(again, want) {
		t.Errorf("Extensions() after caller mutation = %q, want %q", again, want)
	}
}

func TestRegisterLastRegistrationWins(t *testing.T) {
	first := &stubDecoder{}
	second := &stubDecoder{}
	registry := NewRegistry()
	registry.Register([]string{".mp3"}, first)
	registry.Register([]string{".MP3"}, second)

	stream, err := registry.Open(context.Background(), Request{Path: "/music/a.mp3"})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer stream.Close()

	if first.callCount() != 0 {
		t.Errorf("first decoder called %d times, want 0 (last registration wins)", first.callCount())
	}
	if second.callCount() != 1 {
		t.Errorf("second decoder called %d times, want 1", second.callCount())
	}
}

func TestOpenPassesRequestThrough(t *testing.T) {
	decoder := &stubDecoder{}
	registry := NewRegistry()
	registry.Register([]string{".mp3"}, decoder)

	req := Request{
		Path:       "/music/Album/Song.MP3",
		Start:      1500 * time.Millisecond,
		SampleRate: 44100,
		Channels:   1,
	}
	stream, err := registry.Open(context.Background(), req)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer stream.Close()

	got := decoder.lastRequest(t)
	if got != req {
		t.Errorf("decoder received %+v, want %+v", got, req)
	}
}

func TestOpenPathDefaults(t *testing.T) {
	decoder := &stubDecoder{}
	registry := NewRegistry()
	registry.Register([]string{".flac"}, decoder)

	start := 2 * time.Second
	stream, err := registry.OpenPath(context.Background(), "file:///music/with%20space/TRACK.FLAC", start)
	if err != nil {
		t.Fatalf("OpenPath() error = %v", err)
	}
	defer stream.Close()

	want := Request{
		Path:       "/music/with space/TRACK.FLAC",
		Start:      start,
		SampleRate: 48000,
		Channels:   2,
	}
	if got := decoder.lastRequest(t); got != want {
		t.Errorf("decoder received %+v, want %+v", got, want)
	}
}

func TestOpenUnsupportedExtension(t *testing.T) {
	decoder := &stubDecoder{}
	registry := NewRegistry()
	registry.Register([]string{".mp3"}, decoder)

	for _, path := range []string{"/music/a.flac", "/music/no-extension"} {
		_, err := registry.Open(context.Background(), Request{Path: path})
		if err == nil {
			t.Fatalf("Open(%q) error = nil, want unsupported extension error", path)
		}
		for _, want := range []string{"unsupported", path} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Open(%q) error = %q, want it to contain %q", path, err, want)
			}
		}
	}
	if decoder.callCount() != 0 {
		t.Errorf("decoder called %d times for unsupported extensions, want 0", decoder.callCount())
	}
}

func TestRegistryRejectsRemotePaths(t *testing.T) {
	registry := NewRegistry()
	registry.Register(DefaultAudioExtensions(), &stubDecoder{})

	openers := []struct {
		name string
		call func(path string) error
	}{
		{
			name: "Open",
			call: func(path string) error {
				_, err := registry.Open(context.Background(), Request{Path: path})
				return err
			},
		},
		{
			name: "OpenPath",
			call: func(path string) error {
				_, err := registry.OpenPath(context.Background(), path, time.Second)
				return err
			},
		},
	}

	for _, opener := range openers {
		for _, path := range []string{
			"https://example.com/track.mp3",
			"http://example.com/track.flac",
			"ftp://example.com/track.ogg",
			"file://otherhost/music/track.mp3",
		} {
			t.Run(opener.name+"/"+path, func(t *testing.T) {
				err := opener.call(path)
				if err == nil {
					t.Fatalf("%s(%q) error = nil, want remote path rejection", opener.name, path)
				}
				if !strings.Contains(err.Error(), "remote") && !strings.Contains(err.Error(), "host") {
					t.Errorf("%s(%q) error = %q, want remote/host rejection", opener.name, path, err)
				}
			})
		}
	}
}

func TestRegistryAcceptsLocalFileURL(t *testing.T) {
	decoder := &stubDecoder{}
	registry := NewRegistry()
	registry.Register([]string{".mp3"}, decoder)

	for _, path := range []string{
		"/music/a.mp3",
		"relative/a.mp3",
		"file:///music/a%20b.mp3",
		"file://localhost/music/a.mp3",
	} {
		if _, err := registry.Open(context.Background(), Request{Path: path}); err != nil {
			t.Errorf("Open(%q) error = %v, want local path accepted", path, err)
		}
	}
	if decoder.callCount() != 4 {
		t.Errorf("decoder called %d times, want 4", decoder.callCount())
	}
}

func TestRegistryConcurrentLookups(t *testing.T) {
	decoder := &stubDecoder{}
	registry := NewRegistry()
	registry.Register([]string{".mp3", ".flac"}, decoder)

	const goroutines = 32
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ext := ".mp3"
			if i%2 == 0 {
				ext = ".flac"
			}
			stream, err := registry.Open(context.Background(), Request{Path: "/music/track" + ext})
			if err != nil {
				errs <- err
				return
			}
			stream.Close()
			_ = registry.Extensions()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestDefaultAudioExtensions(t *testing.T) {
	exts := DefaultAudioExtensions()
	if len(exts) == 0 {
		t.Fatal("DefaultAudioExtensions() is empty")
	}

	required := []string{
		".mp3", ".ogg", ".opus", ".flac", ".m4a", ".alac", ".ape", ".wv",
		".wav", ".aiff", ".dsf", ".dff", ".mka", ".mpc", ".tta", ".wma",
	}
	set := make(map[string]bool, len(exts))
	for _, ext := range exts {
		if !strings.HasPrefix(ext, ".") {
			t.Errorf("extension %q does not start with a dot", ext)
		}
		if ext != strings.ToLower(ext) {
			t.Errorf("extension %q is not lowercase", ext)
		}
		if set[ext] {
			t.Errorf("extension %q appears more than once", ext)
		}
		set[ext] = true
	}
	for _, ext := range required {
		if !set[ext] {
			t.Errorf("DefaultAudioExtensions() missing %q", ext)
		}
	}

	// Each call returns a fresh copy.
	exts[0] = ".mutated"
	if DefaultAudioExtensions()[0] == ".mutated" {
		t.Error("DefaultAudioExtensions() returned shared slice storage")
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
		{in: `C:\music\a.mp3`, want: `C:\music\a.mp3`},
		{in: "C://music/a.mp3", want: "C://music/a.mp3"},
		{in: "file:///music/a%20b.mp3", want: "/music/a b.mp3"},
		{in: "file://localhost/music/a.mp3", want: "/music/a.mp3"},
		{in: "", wantErr: "empty path"},
		{in: "http://example.com/a.mp3", wantErr: "remote URL"},
		{in: "https://example.com/a.mp3", wantErr: "remote URL"},
		{in: "ftp://example.com/a.mp3", wantErr: "remote URL"},
		{in: "file://otherhost/music/a.mp3", wantErr: "host"},
	}

	for _, tt := range tests {
		got, err := LocalPath(tt.in)
		if tt.wantErr != "" {
			if err == nil {
				t.Errorf("LocalPath(%q) = %q, want error containing %q", tt.in, got, tt.wantErr)
				continue
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("LocalPath(%q) error = %q, want it to contain %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("LocalPath(%q) error = %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("LocalPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRegistryPropagatesDecoderError(t *testing.T) {
	sentinel := errors.New("decoder failed")
	registry := NewRegistry()
	registry.Register([]string{".mp3"}, &stubDecoder{err: sentinel})

	_, err := registry.Open(context.Background(), Request{Path: "/music/a.mp3"})
	if !errors.Is(err, sentinel) {
		t.Errorf("Open() error = %v, want it to wrap %v", err, sentinel)
	}
}
