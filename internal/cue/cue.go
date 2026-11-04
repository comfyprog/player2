// Package cue parses CUE sheets describing audio files and tracks.
//
// The parser is deliberately permissive about unknown directives (they are
// ignored) but strict about the directives that affect playback: every TRACK
// must have an INDEX 01 and INDEX 01 times must strictly increase within each
// audio file. It never makes network requests and only resolves local files.
package cue

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	xunicode "golang.org/x/text/encoding/unicode"
)

// maxInputSize is the largest CUE sheet the parser accepts (controller ruling
// R4). Anything larger is rejected before parsing to bound work on hostile
// input.
const maxInputSize = 4 << 20 // 4 MiB

// ErrTooLarge reports that a CUE sheet exceeded maxInputSize.
var ErrTooLarge = errors.New("cue sheet exceeds 4 MiB limit")

// Sheet is a parsed CUE sheet. Path is the CUE file path when the sheet was
// loaded with ParseFile.
type Sheet struct {
	Path      string
	Title     string
	Performer string
	Files     []File
}

// File is one audio file referenced by a CUE sheet with the tracks that live
// inside it.
type File struct {
	Path   string // resolved absolute path
	Tracks []Track
}

// Track is one track in a Sheet. Start and End are filled in by Resolve.
type Track struct {
	Number     int
	Title      string
	Performer  string
	Start, End time.Duration
	FileIndex  int
}

// Options controls parsing behavior.
type Options struct {
	// FallbackEncoding is used when the input is neither valid UTF-8 nor
	// UTF-16 with a BOM. It defaults to "windows-1252".
	FallbackEncoding string
}

// DurationFunc reports the duration of an audio file. Implementations are
// expected to use the resolved absolute path stored in File.Path.
type DurationFunc func(path string) (time.Duration, error)

// ParseError is a parse failure tied to a line in the source. Lines are
// 1-based.
type ParseError struct {
	Line int
	Err  error
}

// Error implements error.
func (e *ParseError) Error() string {
	return fmt.Sprintf("cue: line %d: %v", e.Line, e.Err)
}

// Unwrap allows errors.Is and errors.As to inspect the underlying cause.
func (e *ParseError) Unwrap() error { return e.Err }

// Parse parses CUE data. Relative FILE paths are resolved against baseDir.
func Parse(data []byte, baseDir string, opts Options) (*Sheet, error) {
	if len(data) > maxInputSize {
		return nil, &ParseError{
			Line: 1,
			Err:  fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data)),
		}
	}

	fallback, err := fallbackDecoder(opts.FallbackEncoding)
	if err != nil {
		return nil, &ParseError{Line: 1, Err: err}
	}
	text, err := decode(data, fallback)
	if err != nil {
		return nil, &ParseError{Line: 1, Err: err}
	}

	if baseDir == "" {
		baseDir = "."
	}
	p := &parser{
		sheet:   &Sheet{},
		baseDir: baseDir,
	}

	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if err := p.parseLine(line, i+1); err != nil {
			return nil, err
		}
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	return p.sheet, nil
}

// ParseFile reads and parses the CUE sheet at path. Sheet.Path is set to path
// and relative FILE entries are resolved against its directory.
func ParseFile(path string, opts Options) (*Sheet, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > maxInputSize {
		return nil, &ParseError{
			Line: 1,
			Err:  fmt.Errorf("%w: %d bytes", ErrTooLarge, info.Size()),
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cue: read %s: %w", path, err)
	}
	sheet, err := Parse(data, filepath.Dir(path), opts)
	if err != nil {
		return nil, err
	}
	sheet.Path = path
	return sheet, nil
}

// Resolve computes each track's End. Tracks are tiled: a track ends where the
// next track in the same file starts, and the last track in a file ends at the
// file's duration. INDEX 00 pregaps therefore belong to the preceding track.
func (s *Sheet) Resolve(d DurationFunc) error {
	if d == nil {
		return errors.New("cue: nil duration function")
	}
	for fi := range s.Files {
		file := &s.Files[fi]
		for ti := range file.Tracks {
			track := &file.Tracks[ti]
			track.FileIndex = fi
			if ti+1 < len(file.Tracks) {
				track.End = file.Tracks[ti+1].Start
				continue
			}
			duration, err := d(file.Path)
			if err != nil {
				return fmt.Errorf("cue: duration of %s: %w", file.Path, err)
			}
			track.End = duration
		}
	}
	return nil
}

// AllTracks returns every track in file order and then track order.
func (s *Sheet) AllTracks() []Track {
	var tracks []Track
	for _, file := range s.Files {
		tracks = append(tracks, file.Tracks...)
	}
	return tracks
}

// parser carries state while walking the source line by line.
type parser struct {
	sheet   *Sheet
	baseDir string

	curFile   *File
	fileLines []int

	hasTrack   bool
	curTrack   Track
	trackLine  int
	hasIndex01 bool

	prevStart time.Duration
	hasPrev   bool
}

func (p *parser) parseLine(line string, lineNo int) error {
	directive := strings.ToUpper(firstField(line))
	if directive == "" || directive == "REM" {
		// Blank lines and REM contents (including unbalanced quotes) are
		// ignored entirely.
		return nil
	}
	switch directive {
	case "FILE", "TRACK", "INDEX", "TITLE", "PERFORMER", "PREGAP", "POSTGAP", "SONGWRITER", "FLAGS", "CATALOG":
		// Known directive; tokenize the full line below.
	default:
		// Unknown directives are ignored by design, even if malformed.
		return nil
	}

	tokens, err := tokenize(line)
	if err != nil {
		return &ParseError{Line: lineNo, Err: err}
	}
	if len(tokens) == 0 {
		return nil
	}

	switch directive {
	case "FILE":
		return p.parseFile(tokens, lineNo)
	case "TRACK":
		return p.parseTrack(tokens, lineNo)
	case "INDEX":
		return p.parseIndex(tokens, lineNo)
	case "TITLE":
		return p.setText(tokens, lineNo, "TITLE", func(v string) { p.setTitle(v) })
	case "PERFORMER":
		return p.setText(tokens, lineNo, "PERFORMER", func(v string) { p.setPerformer(v) })
	case "PREGAP", "POSTGAP":
		if len(tokens) < 2 {
			return parseErr(lineNo, "%s requires a time", directive)
		}
		if _, err := parseTime(tokens[1]); err != nil {
			return parseErr(lineNo, "%s: %v", directive, err)
		}
		return nil
	case "SONGWRITER", "FLAGS", "CATALOG":
		// Recognized but unused in v1.
		return nil
	default:
		// Unknown directives are ignored by design.
		return nil
	}
}

func (p *parser) parseFile(tokens []string, lineNo int) error {
	if err := p.finishTrack(); err != nil {
		return err
	}
	if len(tokens) < 2 {
		return parseErr(lineNo, "FILE requires a file name")
	}

	path, err := p.resolvePath(tokens[1])
	if err != nil {
		return &ParseError{Line: lineNo, Err: fmt.Errorf("FILE %q: %w", tokens[1], err)}
	}

	p.sheet.Files = append(p.sheet.Files, File{Path: path})
	p.curFile = &p.sheet.Files[len(p.sheet.Files)-1]
	p.fileLines = append(p.fileLines, lineNo)
	p.prevStart = 0
	p.hasPrev = false
	return nil
}

func (p *parser) parseTrack(tokens []string, lineNo int) error {
	if err := p.finishTrack(); err != nil {
		return err
	}
	if p.curFile == nil {
		return parseErr(lineNo, "TRACK before FILE")
	}
	if len(tokens) < 2 {
		return parseErr(lineNo, "TRACK requires a track number")
	}

	number, err := strconv.Atoi(tokens[1])
	if err != nil || number < 1 {
		return parseErr(lineNo, "invalid track number %q", tokens[1])
	}

	p.curTrack = Track{Number: number, FileIndex: len(p.sheet.Files) - 1}
	p.hasTrack = true
	p.trackLine = lineNo
	p.hasIndex01 = false
	return nil
}

func (p *parser) parseIndex(tokens []string, lineNo int) error {
	if !p.hasTrack {
		return parseErr(lineNo, "INDEX before TRACK")
	}
	if len(tokens) < 3 {
		return parseErr(lineNo, "INDEX requires a number and a time")
	}

	index, err := strconv.Atoi(tokens[1])
	if err != nil || index < 0 {
		return parseErr(lineNo, "invalid INDEX number %q", tokens[1])
	}
	at, err := parseTime(tokens[2])
	if err != nil {
		return parseErr(lineNo, "INDEX %s: %v", tokens[1], err)
	}

	// INDEX 00 is the pregap and is intentionally not the track start. It is
	// part of the previous track when tracks are tiled from INDEX 01 onwards.
	if index != 1 {
		return nil
	}
	if p.hasIndex01 {
		return parseErr(lineNo, "duplicate INDEX 01")
	}
	if at < 0 {
		return parseErr(lineNo, "track start must not be negative")
	}
	if p.hasPrev && at <= p.prevStart {
		return parseErr(lineNo, "track start %s is not after previous track start %s; INDEX 01 times must increase within a file", formatTime(at), formatTime(p.prevStart))
	}

	p.curTrack.Start = at
	p.hasIndex01 = true
	return nil
}

func (p *parser) setText(tokens []string, lineNo int, name string, set func(string)) error {
	if len(tokens) < 2 {
		return parseErr(lineNo, "%s requires a value", name)
	}
	set(strings.Join(tokens[1:], " "))
	return nil
}

func (p *parser) setTitle(value string) {
	if p.hasTrack {
		p.curTrack.Title = value
		return
	}
	p.sheet.Title = value
}

func (p *parser) setPerformer(value string) {
	if p.hasTrack {
		p.curTrack.Performer = value
		return
	}
	p.sheet.Performer = value
}

// finishTrack validates and appends the track currently being read.
func (p *parser) finishTrack() error {
	if !p.hasTrack {
		return nil
	}
	if !p.hasIndex01 {
		return parseErr(p.trackLine, "track %d has no INDEX 01", p.curTrack.Number)
	}
	if p.curFile == nil {
		// Unreachable: parseTrack requires a FILE first. Guard anyway so
		// adversarial input can never panic.
		return parseErr(p.trackLine, "track %d has no FILE", p.curTrack.Number)
	}

	track := p.curTrack
	if track.Performer == "" {
		track.Performer = p.sheet.Performer
	}
	p.curFile.Tracks = append(p.curFile.Tracks, track)

	p.prevStart = track.Start
	p.hasPrev = true
	p.hasTrack = false
	p.curTrack = Track{}
	return nil
}

func (p *parser) finish() error {
	if err := p.finishTrack(); err != nil {
		return err
	}
	if len(p.sheet.Files) == 0 {
		return parseErr(1, "no FILE directive")
	}
	for i, file := range p.sheet.Files {
		if len(file.Tracks) == 0 {
			return parseErr(p.fileLines[i], "FILE %q has no tracks", file.Path)
		}
	}
	return nil
}

// resolvePath turns a CUE FILE value into a cleaned absolute local path. If the
// file exists under a different letter case, that existing spelling is
// preferred. Missing files are not an error here; callers that need the audio
// to exist (such as the library scanner) validate it themselves.
func (p *parser) resolvePath(name string) (string, error) {
	local, err := localPath(name, p.baseDir)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(local)
	if err != nil {
		return "", err
	}
	return preferExistingCase(abs), nil
}

// schemePattern matches URL schemes such as "http://" or "file://".
var schemePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.\-]*)://`)

// localPath normalizes a CUE FILE value into a cleaned local path. Only
// file:// URLs are accepted; plain paths and Windows drive paths are local.
func localPath(name, baseDir string) (string, error) {
	if name == "" {
		return "", errors.New("empty file name")
	}

	if match := schemePattern.FindStringSubmatch(name); match != nil {
		scheme := match[1]
		// A single-letter scheme is a Windows drive (for example "C://x"),
		// not a remote URL.
		if len(scheme) > 1 && !strings.EqualFold(scheme, "file") {
			return "", fmt.Errorf("remote URL scheme %q is not supported; only local files are allowed", scheme)
		}
		if strings.EqualFold(scheme, "file") {
			rest := name[len(match[0]):]
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
		}
	}

	if runtime.GOOS != "windows" {
		name = strings.ReplaceAll(name, `\`, "/")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(baseDir, name)
	}
	return filepath.Clean(name), nil
}

// preferExistingCase returns path unchanged when it exists. Otherwise it looks
// for a deterministic case-insensitive match in the same directory, which
// handles CUE sheets written on case-insensitive systems. When nothing
// matches, path is returned unchanged.
func preferExistingCase(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}

	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return path
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(entry.Name(), base) {
			continue
		}
		return filepath.Join(dir, entry.Name())
	}
	return path
}

// firstField returns the first whitespace-delimited word of line, ignoring
// leading spaces and tabs. Directive names are never quoted.
func firstField(line string) string {
	line = strings.TrimLeft(line, " \t")
	if line == "" || line[0] == '"' {
		return ""
	}
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' || line[i] == '\t' {
			return line[:i]
		}
	}
	return line
}

// tokenize splits a CUE line into whitespace-separated tokens, honoring
// double-quoted strings. Backslash escapes inside quotes apply only to '"' and
// '\' so ordinary Windows paths are preserved.
func tokenize(line string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(line); {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}

		if line[i] != '"' {
			start := i
			for i < len(line) && line[i] != ' ' && line[i] != '\t' {
				i++
			}
			tokens = append(tokens, line[start:i])
			continue
		}

		i++ // opening quote
		var value strings.Builder
		closed := false
		for i < len(line) {
			c := line[i]
			if c == '\\' && i+1 < len(line) && (line[i+1] == '"' || line[i+1] == '\\') {
				value.WriteByte(line[i+1])
				i += 2
				continue
			}
			if c == '"' {
				i++
				closed = true
				break
			}
			value.WriteByte(c)
			i++
		}
		if !closed {
			return nil, errors.New("unterminated quoted string")
		}
		tokens = append(tokens, value.String())
	}
	return tokens, nil
}

// parseTime parses a CUE timestamp (mm:ss:ff, where ff is 1/75 second). A
// two-component mm:ss form is accepted for lenient handling of old sheets.
func parseTime(value string) (time.Duration, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("invalid time %q (want mm:ss:ff)", value)
	}

	numbers := make([]int, len(parts))
	for i, part := range parts {
		if part == "" {
			return 0, fmt.Errorf("invalid time %q (want mm:ss:ff)", value)
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("invalid time %q (want mm:ss:ff)", value)
			}
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			return 0, fmt.Errorf("invalid time %q (want mm:ss:ff)", value)
		}
		numbers[i] = number
	}

	minutes, seconds := numbers[0], numbers[1]
	frames := 0
	if len(numbers) == 3 {
		frames = numbers[2]
	}
	if seconds > 59 || frames > 74 {
		return 0, fmt.Errorf("invalid time %q: seconds must be < 60 and frames < 75", value)
	}
	// Bound minutes so the conversion to time.Duration cannot overflow.
	if minutes > 1_000_000 {
		return 0, fmt.Errorf("invalid time %q: out of range", value)
	}

	return time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second +
		time.Duration(frames)*time.Second/75, nil
}

// formatTime renders a duration for error messages without losing frames.
func formatTime(d time.Duration) string {
	minutes := d / time.Minute
	d -= minutes * time.Minute
	seconds := d / time.Second
	d -= seconds * time.Second
	frames := d * 75 / time.Second
	return fmt.Sprintf("%02d:%02d:%02d", minutes, seconds, frames)
}

// decode turns CUE bytes into text. BOMs win over content sniffing; otherwise
// valid UTF-8 is used as-is and invalid UTF-8 falls back to the configured
// legacy encoding.
func decode(data []byte, fallback encoding.Encoding) (string, error) {
	var text string

	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		data = data[3:]
		if !utf8.Valid(data) {
			return "", errors.New("invalid UTF-8 after byte-order mark")
		}
		text = string(data)
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		decoded, err := xunicode.UTF16(xunicode.LittleEndian, xunicode.IgnoreBOM).NewDecoder().Bytes(data[2:])
		if err != nil {
			return "", fmt.Errorf("decode UTF-16 LE: %w", err)
		}
		text = string(decoded)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		decoded, err := xunicode.UTF16(xunicode.BigEndian, xunicode.IgnoreBOM).NewDecoder().Bytes(data[2:])
		if err != nil {
			return "", fmt.Errorf("decode UTF-16 BE: %w", err)
		}
		text = string(decoded)
	case utf8.Valid(data):
		text = string(data)
	case fallback != nil:
		decoded, err := fallback.NewDecoder().Bytes(data)
		if err != nil {
			return "", fmt.Errorf("decode fallback encoding: %w", err)
		}
		text = string(decoded)
	default:
		return "", errors.New("input is not valid UTF-8")
	}

	return strings.TrimPrefix(text, "\uFEFF"), nil
}

// fallbackDecoder maps an encoding name to its decoder. An empty name uses
// windows-1252 (controller ruling R14). A nil encoding means "UTF-8 only".
func fallbackDecoder(name string) (encoding.Encoding, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "windows-1252"
	}
	key := strings.ToLower(name)
	if key == "utf-8" || key == "utf8" {
		return nil, nil
	}
	if enc, ok := legacyEncodings[key]; ok {
		return enc, nil
	}
	return nil, fmt.Errorf("unsupported fallback encoding %q", name)
}

var legacyEncodings = map[string]encoding.Encoding{
	"windows-1250": charmap.Windows1250,
	"cp1250":       charmap.Windows1250,
	"windows-1251": charmap.Windows1251,
	"cp1251":       charmap.Windows1251,
	"windows-1252": charmap.Windows1252,
	"cp1252":       charmap.Windows1252,
	"windows-1253": charmap.Windows1253,
	"cp1253":       charmap.Windows1253,
	"windows-1254": charmap.Windows1254,
	"cp1254":       charmap.Windows1254,
	"windows-1255": charmap.Windows1255,
	"cp1255":       charmap.Windows1255,
	"windows-1256": charmap.Windows1256,
	"cp1256":       charmap.Windows1256,
	"windows-1257": charmap.Windows1257,
	"cp1257":       charmap.Windows1257,
	"windows-1258": charmap.Windows1258,
	"cp1258":       charmap.Windows1258,
	"windows-874":  charmap.Windows874,
	"cp874":        charmap.Windows874,
	"iso-8859-1":   charmap.ISO8859_1,
	"iso8859-1":    charmap.ISO8859_1,
	"latin1":       charmap.ISO8859_1,
	"latin-1":      charmap.ISO8859_1,
	"iso-8859-2":   charmap.ISO8859_2,
	"iso8859-2":    charmap.ISO8859_2,
	"iso-8859-15":  charmap.ISO8859_15,
	"iso8859-15":   charmap.ISO8859_15,
	"koi8-r":       charmap.KOI8R,
	"koi8r":        charmap.KOI8R,
	"koi8-u":       charmap.KOI8U,
	"koi8u":        charmap.KOI8U,
	"macintosh":    charmap.Macintosh,
	"mac":          charmap.Macintosh,
	"cp437":        charmap.CodePage437,
	"ibm437":       charmap.CodePage437,
}

// parseErr builds a ParseError for the given line.
func parseErr(line int, format string, args ...any) *ParseError {
	return &ParseError{Line: line, Err: fmt.Errorf(format, args...)}
}
