// Package meta reads audio metadata and duration with ffprobe.
//
// It probes local files only, never uses a shell, and caches successful
// results by path, size, and modification time.
package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultTimeout bounds a single ffprobe invocation.
const defaultTimeout = 5 * time.Second

// Info is the metadata relevant to browsing, playing, and describing a file.
// Tags holds every non-empty string tag reported by ffprobe, keyed by
// lowercased tag name. Callers must treat Tags as read-only.
type Info struct {
	Title, Artist, Album, AlbumArtist string
	TrackNo                           int
	Duration                          time.Duration
	Codec, Container                  string
	BitRate                           int64
	SampleRate, Channels, BitDepth    int
	ChannelLayout                     string
	Tags                              map[string]string
}

// Prober reports metadata for a local audio file.
type Prober interface {
	Probe(ctx context.Context, path string) (Info, error)
}

// FFProbe probes files with the ffprobe executable and caches successful
// results. The zero value defaults to "ffprobe"; NewFFProbe is preferred.
type FFProbe struct {
	bin     string
	timeout time.Duration

	mu    sync.Mutex
	cache map[cacheKey]Info
}

// cacheKey identifies a file version. A different path, size, or modification
// time is a cache miss.
type cacheKey struct {
	path    string
	size    int64
	modTime time.Time
}

// NewFFProbe returns a prober that runs bin (normally "ffprobe"). An empty
// bin falls back to "ffprobe".
func NewFFProbe(bin string) *FFProbe {
	return &FFProbe{
		bin:     bin,
		timeout: defaultTimeout,
		cache:   make(map[cacheKey]Info),
	}
}

// Probe returns metadata for path, consulting the cache first. The caller's
// deadline is honored; otherwise each ffprobe run is bounded by the default
// timeout.
func (p *FFProbe) Probe(ctx context.Context, path string) (Info, error) {
	local, err := localPath(path)
	if err != nil {
		return Info{}, err
	}

	stat, err := os.Stat(local)
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe: stat %q: %w", local, err)
	}
	key := cacheKey{path: local, size: stat.Size(), modTime: stat.ModTime()}

	if info, ok := p.lookup(key); ok {
		return info, nil
	}

	info, err := p.run(ctx, local)
	if err != nil {
		return Info{}, err
	}
	p.store(key, info)
	return info, nil
}

func (p *FFProbe) lookup(key cacheKey) (Info, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = make(map[cacheKey]Info)
	}
	info, ok := p.cache[key]
	return info, ok
}

func (p *FFProbe) store(key cacheKey, info Info) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = make(map[cacheKey]Info)
	}
	p.cache[key] = info
}

// run executes ffprobe for one file. timeout <= 0 means the default.
func (p *FFProbe) run(ctx context.Context, path string) (Info, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	bin := p.binary()
	cmd := exec.CommandContext(probeCtx, bin, probeArgs(path)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		switch {
		case errors.Is(probeCtx.Err(), context.DeadlineExceeded):
			return Info{}, fmt.Errorf("ffprobe timed out after %s probing %q (binary %q)",
				time.Since(start).Round(time.Millisecond), path, bin)
		case errors.Is(probeCtx.Err(), context.Canceled):
			return Info{}, fmt.Errorf("ffprobe probe of %q canceled (binary %q)", path, bin)
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
			return Info{}, fmt.Errorf("ffprobe executable %q not found; install FFmpeg or set ffprobe_bin (probing %q)", bin, path)
		}

		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return Info{}, fmt.Errorf("ffprobe %q failed for %q: %w: %s", bin, path, err, detail)
		}
		return Info{}, fmt.Errorf("ffprobe %q failed for %q: %w", bin, path, err)
	}

	info, err := parseProbeOutput(stdout.Bytes())
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe %q output for %q: %w", bin, path, err)
	}
	return info, nil
}

func (p *FFProbe) binary() string {
	if p.bin == "" {
		return "ffprobe"
	}
	return p.bin
}

// probeArgs constructs the ffprobe argument list. The path is passed as a
// single argument, never through a shell, so spaces, quotes, and leading
// dashes are safe.
func probeArgs(path string) []string {
	return []string{
		"-v", "error",
		"-protocol_whitelist", "file",
		"-show_format", "-show_streams",
		"-of", "json",
		"-i", path,
	}
}

var schemePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.\-]*)://`)

// localPath accepts plain local paths and local file:// URLs. Remote URLs are
// rejected because playback is local only.
func localPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("ffprobe: empty path")
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
		return "", fmt.Errorf("ffprobe: remote URL %q is not supported; only local files are allowed", raw)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("ffprobe: invalid file URL %q: %w", raw, err)
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "", fmt.Errorf("ffprobe: remote file URL host %q is not supported", u.Host)
	}
	if u.Path == "" {
		return "", fmt.Errorf("ffprobe: file URL %q has no path", raw)
	}
	return u.Path, nil
}

// probeOutput mirrors the subset of ffprobe's JSON output that this package
// consumes. Tags are decoded as any values so a single odd tag cannot make the
// whole probe fail.
type probeOutput struct {
	Format  probeFormat   `json:"format"`
	Streams []probeStream `json:"streams"`
}

type probeFormat struct {
	Duration       string         `json:"duration"`
	FormatName     string         `json:"format_name"`
	FormatLongName string         `json:"format_long_name"`
	BitRate        probeNumber    `json:"bit_rate"`
	Tags           map[string]any `json:"tags"`
}

type probeStream struct {
	CodecName        string         `json:"codec_name"`
	CodecType        string         `json:"codec_type"`
	SampleRate       string         `json:"sample_rate"`
	Channels         int            `json:"channels"`
	ChannelLayout    string         `json:"channel_layout"`
	Duration         string         `json:"duration"`
	BitRate          probeNumber    `json:"bit_rate"`
	BitsPerSample    probeNumber    `json:"bits_per_sample"`
	BitsPerRawSample probeNumber    `json:"bits_per_raw_sample"`
	Tags             map[string]any `json:"tags"`
}

// probeNumber accepts either a JSON number or a JSON string. ffprobe uses
// both representations for numeric fields across formats and versions.
type probeNumber string

// UnmarshalJSON decodes a JSON number or string into probeNumber. Null and
// empty values become the empty string.
func (n *probeNumber) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*n = ""
		return nil
	}
	if data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*n = probeNumber(value)
		return nil
	}
	*n = probeNumber(string(data))
	return nil
}

// parseProbeOutput converts ffprobe JSON into Info.
func parseProbeOutput(data []byte) (Info, error) {
	var out probeOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return Info{}, fmt.Errorf("parse ffprobe output: %w", err)
	}

	var audio *probeStream
	for i := range out.Streams {
		if out.Streams[i].CodecType == "audio" {
			audio = &out.Streams[i]
			break
		}
	}
	if audio == nil {
		return Info{}, errors.New("no audio stream found")
	}

	// Merge stream and format tags. Format tags are the tag source the rest of
	// the application has always used, so they take precedence; stream tags
	// fill in fields the container does not expose.
	tags := normalizeTags(audio.Tags)
	for name, value := range normalizeTags(out.Format.Tags) {
		tags[name] = value
	}

	container := strings.TrimSpace(out.Format.FormatLongName)
	if container == "" {
		container = strings.TrimSpace(out.Format.FormatName)
	}

	bitRate := parsePositiveInt64(string(audio.BitRate))
	if bitRate == 0 {
		bitRate = parsePositiveInt64(string(out.Format.BitRate))
	}

	bitDepth := parsePositiveInt(string(audio.BitsPerSample))
	if bitDepth == 0 {
		bitDepth = parsePositiveInt(string(audio.BitsPerRawSample))
	}

	info := Info{
		Title:         firstTag(tags, "title"),
		Artist:        firstTag(tags, "artist"),
		Album:         firstTag(tags, "album"),
		AlbumArtist:   firstTag(tags, "album_artist", "albumartist"),
		TrackNo:       parseTrack(firstTag(tags, "track")),
		Codec:         audio.CodecName,
		Container:     container,
		BitRate:       bitRate,
		SampleRate:    parsePositiveInt(audio.SampleRate),
		Channels:      audio.Channels,
		BitDepth:      bitDepth,
		ChannelLayout: strings.TrimSpace(audio.ChannelLayout),
	}
	if info.Channels < 0 {
		info.Channels = 0
	}
	if len(tags) > 0 {
		info.Tags = tags
	}

	// Prefer the container duration, falling back to the first audio stream
	// when the container has none. A missing duration is valid and stays zero.
	if duration, ok := parseSeconds(out.Format.Duration); ok && duration > 0 {
		info.Duration = duration
	} else if duration, ok := parseSeconds(audio.Duration); ok && duration > 0 {
		info.Duration = duration
	}

	return info, nil
}

// normalizeTags lowercases tag names and keeps string values. Non-string and
// empty tags are ignored.
func normalizeTags(tags map[string]any) map[string]string {
	normalized := make(map[string]string, len(tags))

	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value, ok := tags[key].(string)
		if !ok {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		if _, exists := normalized[name]; exists {
			continue
		}
		normalized[name] = value
	}
	return normalized
}

// firstTag returns the first non-empty value among the given normalized names.
func firstTag(tags map[string]string, names ...string) string {
	for _, name := range names {
		if value := tags[name]; value != "" {
			return value
		}
	}
	return ""
}

// parseTrack accepts "3" and "3/12" and returns 3. Unparseable values are
// ignored, yielding zero.
func parseTrack(value string) int {
	value = strings.TrimSpace(value)
	if i := strings.IndexByte(value, '/'); i >= 0 {
		value = value[:i]
	}
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number <= 0 {
		return 0
	}
	return number
}

// parsePositiveInt parses a non-negative integer string, returning zero for
// malformed values.
func parsePositiveInt(value string) int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number < 0 {
		return 0
	}
	return number
}

// parsePositiveInt64 parses a non-negative 64-bit integer string, returning
// zero for malformed values. Bitrates routinely exceed 32-bit sizes.
func parsePositiveInt64(value string) int64 {
	number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || number < 0 {
		return 0
	}
	return number
}

// parseSeconds parses fractional seconds into a duration. Missing, negative,
// NaN, infinite, and overflowing values are reported as not ok.
func parseSeconds(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return 0, false
	}
	if seconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, false
	}
	return time.Duration(math.Round(seconds * float64(time.Second))), true
}
