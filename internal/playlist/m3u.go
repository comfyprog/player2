// Package playlist reads and writes M3U/M3U8 playlists.
//
// The reader accepts plain local paths, file:// URLs, comments, EXTINF
// metadata, and the #EXT-X-PLAYER2-CUE extension used to reference one track
// in a CUE sheet. Remote URLs are rejected. The writer emits standard M3U with
// relative paths and the same CUE extension.
//
// The package performs local file I/O only, starts no goroutines, and does not
// touch playback or UI state.
package playlist

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"player2/internal/cue"
	"player2/internal/queue"
)

const (
	extinfPrefix = "#EXTINF:"
	cuePrefix    = "#EXT-X-PLAYER2-CUE:"
	header       = "#EXTM3U"
)

// Entry is one playlist entry. CuePath/CueTrack are set only for CUE track
// references; plain entries leave them zero.
type Entry struct {
	Path, CuePath string
	CueTrack      int
	Title, Artist string
	Duration      time.Duration
}

// pendingMeta is EXTINF metadata waiting for the next path line.
type pendingMeta struct {
	set      bool
	duration time.Duration
	title    string
	artist   string
}

// Load reads the playlist at path. Relative entries are resolved against the
// playlist's directory. A missing or unreadable playlist is an error.
//
// A malformed #EXT-X-PLAYER2-CUE extension is ignored, and a CUE reference
// that cannot be parsed or whose track does not exist is downgraded to the
// whole-file entry rather than failing the playlist.
func Load(path string) ([]Entry, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("playlist: resolve %q: %w", path, err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("playlist: read %s: %w", absPath, err)
	}
	baseDir := filepath.Dir(absPath)

	text := strings.TrimPrefix(string(data), "\uFEFF")
	entries := make([]Entry, 0)

	var meta pendingMeta
	var cueRef string
	var cueTrack int
	hasCue := false

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			// Directives retain trailing spaces: EXTINF display titles may
			// legitimately end in " - " with an empty track title.
			tagLine := strings.TrimLeft(line, " \t")
			directive := strings.ToUpper(tagLine[:directiveLen(tagLine)])
			switch {
			case strings.HasPrefix(directive, extinfPrefix):
				meta = parseEXTINF(tagLine[len(extinfPrefix):])
			case strings.HasPrefix(directive, cuePrefix):
				if ref, track, ok := parseCueExtension(tagLine[len(cuePrefix):]); ok {
					cueRef, cueTrack, hasCue = ref, track, true
				}
			}
			continue
		}

		local, err := resolveLocal(trimmed, baseDir)
		if err != nil {
			return nil, fmt.Errorf("playlist: %s: %w", absPath, err)
		}

		entry := Entry{Path: local}
		if meta.set {
			entry.Title = meta.title
			entry.Artist = meta.artist
			entry.Duration = meta.duration
		}
		if hasCue {
			if cuePath, ok := validateCueReference(cueRef, cueTrack, baseDir); ok {
				entry.CuePath = cuePath
				entry.CueTrack = cueTrack
			}
		}
		entries = append(entries, entry)

		meta = pendingMeta{}
		cueRef, cueTrack, hasCue = "", 0, false
	}

	return entries, nil
}

// Write saves items to path as an M3U playlist. Paths are written relative to
// the playlist's directory when possible. The destination is replaced
// atomically.
func Write(path string, items []queue.Item) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("playlist: resolve %q: %w", path, err)
	}
	dir := filepath.Dir(absPath)

	var buf bytes.Buffer
	buf.WriteString(header)
	buf.WriteByte('\n')

	for _, item := range items {
		if strings.ContainsAny(item.Path, "\r\n") {
			return fmt.Errorf("playlist: path %q contains a newline", item.Path)
		}
		if item.CuePath != "" && strings.ContainsAny(item.CuePath, "\r\n") {
			return fmt.Errorf("playlist: cue path %q contains a newline", item.CuePath)
		}

		title := sanitizeLine(item.Title)
		artist := sanitizeLine(item.Artist)
		if title != "" || artist != "" || item.Duration > 0 {
			seconds := "-1"
			if item.Duration > 0 {
				seconds = strconv.FormatInt(int64(item.Duration/time.Second), 10)
			}
			fmt.Fprintf(&buf, "%s%s,%s\n", extinfPrefix, seconds, displayName(artist, title, item.Path))
		}

		if item.CuePath != "" {
			cueRel, err := relativePath(dir, item.CuePath)
			if err != nil {
				return fmt.Errorf("playlist: cue path %q: %w", item.CuePath, err)
			}
			fmt.Fprintf(&buf, "%scue=%s;track=%d\n", cuePrefix, url.PathEscape(filepath.ToSlash(cueRel)), item.CueTrack)
		}

		pathRel, err := relativePath(dir, item.Path)
		if err != nil {
			return fmt.Errorf("playlist: path %q: %w", item.Path, err)
		}
		buf.WriteString(pathRel)
		buf.WriteByte('\n')
	}

	return writeAtomic(absPath, buf.Bytes())
}

// parseEXTINF parses "<seconds>,<display title>" and returns the duration,
// title, and artist. Non-positive or invalid durations mean unknown (0). The
// display title splits on the first " - ".
func parseEXTINF(value string) pendingMeta {
	secondsPart, display, _ := strings.Cut(value, ",")
	seconds, err := strconv.ParseFloat(strings.TrimSpace(secondsPart), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		seconds = 0
	}

	var duration time.Duration
	if seconds > 0 {
		if seconds >= float64(math.MaxInt64)/float64(time.Second) {
			seconds = 0
		} else {
			duration = time.Duration(math.Round(seconds * float64(time.Second)))
		}
	}

	meta := pendingMeta{set: true, duration: duration}
	if i := strings.Index(display, " - "); i >= 0 {
		meta.artist = display[:i]
		meta.title = display[i+len(" - "):]
	} else {
		meta.title = display
	}
	return meta
}

// parseCueExtension parses "cue=<url-escaped-path>;track=<n>". Unknown keys are
// ignored; a missing, empty, or malformed required key returns ok=false.
func parseCueExtension(value string) (string, int, bool) {
	var cueRef string
	var track int
	hasCue, hasTrack := false, false

	for _, field := range strings.Split(value, ";") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, val, found := strings.Cut(field, "=")
		if !found {
			return "", 0, false
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "cue":
			cueRef = strings.TrimSpace(val)
			hasCue = true
		case "track":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return "", 0, false
			}
			track = n
			hasTrack = true
		}
	}

	if !hasCue || !hasTrack || cueRef == "" || track <= 0 {
		return "", 0, false
	}
	unescaped, err := url.PathUnescape(cueRef)
	if err != nil {
		return "", 0, false
	}
	return unescaped, track, true
}

// validateCueReference resolves and parses a CUE sheet, then checks that the
// requested track number exists. Any failure is reported as ok=false.
func validateCueReference(cueRef string, track int, baseDir string) (string, bool) {
	cuePath, err := resolveLocal(cueRef, baseDir)
	if err != nil {
		return "", false
	}
	sheet, err := cue.ParseFile(cuePath, cue.Options{})
	if err != nil {
		return "", false
	}
	for _, candidate := range sheet.AllTracks() {
		if candidate.Number == track {
			return cuePath, true
		}
	}
	return "", false
}

// schemePattern matches URL schemes such as "http://" or "file://".
var schemePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.\-]*)://`)

// resolveLocal turns a playlist path line into a cleaned absolute local path.
// Only file:// URLs are accepted; plain paths and Windows drive paths are
// local. Remote hosts and schemes are rejected.
func resolveLocal(name, baseDir string) (string, error) {
	if name == "" {
		return "", errors.New("empty path")
	}

	if strings.HasPrefix(strings.ToLower(name), "file://") {
		rest := name[len("file://"):]
		host := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			host = rest[:i]
			rest = rest[i:]
		} else {
			rest = ""
		}
		if host != "" && !strings.EqualFold(host, "localhost") {
			return "", fmt.Errorf("remote file URL host %q is not supported; only local files are allowed", host)
		}
		if rest == "" {
			return "", fmt.Errorf("file URL %q has no path", name)
		}
		unescaped, err := url.PathUnescape(rest)
		if err != nil {
			return "", fmt.Errorf("invalid file URL %q: %w", name, err)
		}
		name = unescaped
	} else if match := schemePattern.FindStringSubmatch(name); match != nil && len(match[1]) > 1 {
		return "", fmt.Errorf("remote URL scheme %q is not supported; only local files are allowed", match[1])
	}

	if runtime.GOOS != "windows" {
		name = strings.ReplaceAll(name, `\`, "/")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(baseDir, name)
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", name, err)
	}
	return filepath.Clean(abs), nil
}

// relativePath returns target relative to base, falling back to the absolute
// target when a relative path cannot be formed.
func relativePath(base, target string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(base, abs); err == nil {
		return rel, nil
	}
	return abs, nil
}

// displayName builds the EXTINF display string. The file basename is used when
// no metadata exists.
func displayName(artist, title, path string) string {
	switch {
	case artist != "" && title != "":
		return artist + " - " + title
	case artist != "":
		return artist
	case title != "":
		return title
	default:
		return filepath.Base(path)
	}
}

// sanitizeLine replaces newlines so metadata cannot break the line-oriented
// playlist format.
func sanitizeLine(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return value
}

// writeAtomic writes data to path through a temporary file in the destination
// directory followed by a rename. The temporary file is removed on failure.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".player2-m3u-*")
	if err != nil {
		return fmt.Errorf("playlist: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("playlist: write temporary file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("playlist: set temporary file mode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("playlist: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("playlist: close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("playlist: replace %s: %w", path, err)
	}
	cleanup = false
	return nil
}

// directiveLen returns the length of the leading tag portion of a comment
// line, capped defensively at the line length.
func directiveLen(line string) int {
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return i + 1
	}
	return len(line)
}
