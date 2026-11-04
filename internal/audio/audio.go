// Package audio defines the playback decoder abstraction and the extension
// registry that maps audio files to decoders.
//
// Decoders return raw PCM. The registry deliberately knows nothing about
// process execution or sound devices; concrete backends live in subpackages
// (for example player2/internal/audio/ffmpeg) and register themselves with the
// application at startup.
package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Request describes one decode. Path must name a local file. Start is the
// position at which decoding begins. SampleRate and Channels describe the
// requested output format; decoders may apply their own defaults for
// non-positive values.
type Request struct {
	Path       string
	Start      time.Duration
	SampleRate int
	Channels   int
}

// Stream is a byte stream of raw PCM samples. Callers must Close it to release
// the decoder and any child process resources.
type Stream interface {
	io.ReadCloser
}

// Decoder opens a local audio file as a raw PCM stream.
type Decoder interface {
	Open(ctx context.Context, req Request) (Stream, error)
}

// Registry maps file extensions to decoders. The zero value is ready to use;
// NewRegistry is provided for convenience. Registrations normally happen at
// startup, but lookups are safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	decoders map[string]Decoder
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{decoders: make(map[string]Decoder)}
}

// Register maps each extension in exts to d. Extensions are case-insensitive
// and may be written with or without a leading dot. Later registrations win,
// so a backend can override a default. Empty or invalid extensions and nil
// decoders are ignored.
func (r *Registry) Register(exts []string, d Decoder) {
	if d == nil {
		return
	}

	normalized := make([]string, 0, len(exts))
	for _, ext := range exts {
		if ext = normalizeExtension(ext); ext != "" {
			normalized = append(normalized, ext)
		}
	}
	if len(normalized) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.decoders == nil {
		r.decoders = make(map[string]Decoder)
	}
	for _, ext := range normalized {
		r.decoders[ext] = d
	}
}

// Extensions returns a sorted copy of the registered extensions, each
// lowercase and prefixed with a dot.
func (r *Registry) Extensions() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	exts := make([]string, 0, len(r.decoders))
	for ext := range r.decoders {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return exts
}

// Open resolves req.Path to a local path, selects a decoder by extension, and
// opens the stream. It implements Decoder, so a populated Registry can be used
// anywhere a Decoder is expected.
func (r *Registry) Open(ctx context.Context, req Request) (Stream, error) {
	local, err := LocalPath(req.Path)
	if err != nil {
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(local))
	r.mu.RLock()
	decoder := r.decoders[ext]
	r.mu.RUnlock()
	if decoder == nil {
		return nil, fmt.Errorf("audio: unsupported file extension %q for %q", ext, req.Path)
	}

	req.Path = local
	stream, err := decoder.Open(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("audio: open %q: %w", local, err)
	}
	return stream, nil
}

// OpenPath is a convenience wrapper around Open that requests the default
// playback format (48 kHz, stereo). It has the same local-path validation as
// Open.
func (r *Registry) OpenPath(ctx context.Context, path string, start time.Duration) (Stream, error) {
	return r.Open(ctx, Request{
		Path:       path,
		Start:      start,
		SampleRate: 48000,
		Channels:   2,
	})
}

// normalizeExtension returns a lowercase extension with a leading dot, or the
// empty string when ext has no extension text.
func normalizeExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if ext == "." {
		return ""
	}
	return ext
}

var defaultExtensions = []string{
	".aac", ".ac3", ".aif", ".aifc", ".aiff", ".alac", ".ape", ".caf",
	".dff", ".dsf", ".dts", ".flac", ".m4a", ".m4b", ".mka", ".mp2",
	".mp3", ".mpc", ".oga", ".ogg", ".opus", ".spx", ".tta", ".w64",
	".wav", ".wma", ".wv",
}

// DefaultAudioExtensions returns the file extensions handled by the default
// FFmpeg-backed decoder. It covers the common lossy and lossless formats,
// including MP3, Ogg/Vorbis, Opus, FLAC, ALAC/M4A, APE, WavPack, WAV, AIFF,
// DSF/DFF, MKA, MPC, TTA, and WMA. The returned slice is a fresh copy.
func DefaultAudioExtensions() []string {
	return append([]string(nil), defaultExtensions...)
}

var schemePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.\-]*)://`)

// LocalPath validates raw as a local file path and returns its normalized
// form. Plain paths and Windows drive-like paths are accepted as-is. file://
// URLs are converted to local paths when their host is empty or localhost.
// Other URL schemes and remote file hosts are rejected so decoders never
// invoke a network-capable backend for remote input.
func LocalPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("audio: empty path")
	}

	match := schemePattern.FindStringSubmatch(raw)
	if match == nil {
		return raw, nil
	}

	scheme := match[1]
	if len(scheme) == 1 && !strings.EqualFold(scheme, "file") {
		// A single-letter scheme is a Windows drive, not a URL.
		return raw, nil
	}
	if !strings.EqualFold(scheme, "file") {
		return "", fmt.Errorf("audio: remote URL %q is not supported; only local files are allowed", raw)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("audio: invalid file URL %q: %w", raw, err)
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "", fmt.Errorf("audio: remote file URL host %q is not supported", u.Host)
	}
	if u.Path == "" {
		return "", fmt.Errorf("audio: file URL %q has no path", raw)
	}
	return u.Path, nil
}
