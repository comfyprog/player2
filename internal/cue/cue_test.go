package cue

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// writeFile creates a dummy audio file at dir/parts... and returns its path.
func writeFile(t *testing.T, dir string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{dir}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("not really audio"), 0o644); err != nil {
		t.Fatalf("write dummy audio file: %v", err)
	}
	return path
}

// resolveSheet resolves s with a fixed duration for every file.
func resolveSheet(t *testing.T, s *Sheet, duration time.Duration) {
	t.Helper()
	if err := s.Resolve(func(string) (time.Duration, error) { return duration, nil }); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestParseBasicAlbum(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "album.wav")

	src := `REM GENRE "Rock"
PERFORMER "The Artist"
TITLE "The Album"
CATALOG 1234567890123

FILE "album.wav" WAVE
  TRACK 01 AUDIO
    TITLE "Opening"
    PERFORMER "Guest"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Middle"
    INDEX 00 02:58:00
    INDEX 01 03:00:00
  TRACK 03 AUDIO
    TITLE "Finale"
    INDEX 01 05:00:00
`

	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if s.Path != "" {
		t.Errorf("Parse Path = %q, want empty", s.Path)
	}
	if s.Title != "The Album" {
		t.Errorf("Title = %q, want %q", s.Title, "The Album")
	}
	if s.Performer != "The Artist" {
		t.Errorf("Performer = %q, want %q", s.Performer, "The Artist")
	}
	if len(s.Files) != 1 {
		t.Fatalf("len(Files) = %d, want 1", len(s.Files))
	}
	if s.Files[0].Path != audio {
		t.Errorf("Files[0].Path = %q, want %q", s.Files[0].Path, audio)
	}

	tracks := s.Files[0].Tracks
	if len(tracks) != 3 {
		t.Fatalf("len(Tracks) = %d, want 3", len(tracks))
	}

	want := []Track{
		{Number: 1, Title: "Opening", Performer: "Guest", Start: 0, FileIndex: 0},
		{Number: 2, Title: "Middle", Performer: "The Artist", Start: 3 * time.Minute, FileIndex: 0},
		{Number: 3, Title: "Finale", Performer: "The Artist", Start: 5 * time.Minute, FileIndex: 0},
	}
	for i, w := range want {
		got := tracks[i]
		if got.Number != w.Number || got.Title != w.Title || got.Performer != w.Performer || got.Start != w.Start || got.FileIndex != w.FileIndex {
			t.Errorf("track %d = %+v, want %+v", i, got, w)
		}
		if got.End != 0 {
			t.Errorf("track %d End = %v before Resolve, want 0", i, got.End)
		}
	}

	resolveSheet(t, s, 10*time.Minute)
	wantEnds := []time.Duration{3 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	for i, end := range wantEnds {
		if got := s.Files[0].Tracks[i].End; got != end {
			t.Errorf("track %d End = %v, want %v", i, got, end)
		}
	}

	all := s.AllTracks()
	if len(all) != 3 {
		t.Fatalf("len(AllTracks()) = %d, want 3", len(all))
	}
	for i := range all {
		if all[i].Number != i+1 {
			t.Errorf("AllTracks()[%d].Number = %d, want %d", i, all[i].Number, i+1)
		}
	}
}

func TestPregapIndex00DoesNotSetTrackStart(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")

	src := `FILE "album.wav" WAVE
  TRACK 01 AUDIO
    INDEX 00 00:00:00
    INDEX 01 02:00:00
  TRACK 02 AUDIO
    INDEX 00 04:58:00
    INDEX 01 05:00:00
`
	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tracks := s.Files[0].Tracks
	if len(tracks) != 2 {
		t.Fatalf("len(Tracks) = %d, want 2", len(tracks))
	}
	if got := tracks[0].Start; got != 2*time.Minute {
		t.Errorf("track 1 Start = %v, want 2m (INDEX 01, not INDEX 00)", got)
	}
	if got := tracks[1].Start; got != 5*time.Minute {
		t.Errorf("track 2 Start = %v, want 5m (INDEX 01, not INDEX 00)", got)
	}

	resolveSheet(t, s, 10*time.Minute)
	if got := tracks[0].End; got != 5*time.Minute {
		t.Errorf("track 1 End = %v, want 5m (includes pregap)", got)
	}
}

func TestParseMultiFile(t *testing.T) {
	dir := t.TempDir()
	one := writeFile(t, dir, "one.wav")
	two := writeFile(t, dir, "two.wav")

	src := `TITLE "Double Album"
PERFORMER "Band"
FILE "one.wav" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    INDEX 01 01:00:00
FILE "two.wav" WAVE
  TRACK 03 AUDIO
    INDEX 01 00:00:00
  TRACK 04 AUDIO
    INDEX 01 00:30:00
`
	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(s.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(s.Files))
	}
	if s.Files[0].Path != one || s.Files[1].Path != two {
		t.Errorf("paths = %q, %q; want %q, %q", s.Files[0].Path, s.Files[1].Path, one, two)
	}
	if len(s.Files[0].Tracks) != 2 || len(s.Files[1].Tracks) != 2 {
		t.Fatalf("track counts = %d, %d; want 2, 2", len(s.Files[0].Tracks), len(s.Files[1].Tracks))
	}

	durations := map[string]time.Duration{one: 2 * time.Minute, two: 1 * time.Minute}
	if err := s.Resolve(func(path string) (time.Duration, error) { return durations[path], nil }); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	wantEnds := [][2]time.Duration{
		{1 * time.Minute, 2 * time.Minute},
		{30 * time.Second, 1 * time.Minute},
	}
	for fi, ends := range wantEnds {
		for ti, end := range ends {
			tr := s.Files[fi].Tracks[ti]
			if tr.FileIndex != fi {
				t.Errorf("file %d track %d FileIndex = %d, want %d", fi, ti, tr.FileIndex, fi)
			}
			if tr.End != end {
				t.Errorf("file %d track %d End = %v, want %v", fi, ti, tr.End, end)
			}
		}
	}

	all := s.AllTracks()
	if len(all) != 4 {
		t.Fatalf("len(AllTracks()) = %d, want 4", len(all))
	}
	for i, want := range []int{1, 2, 3, 4} {
		if all[i].Number != want {
			t.Errorf("AllTracks()[%d].Number = %d, want %d", i, all[i].Number, want)
		}
	}
}

func TestResolveLastTrackUsesDuration(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "album.wav")
	src := `FILE "album.wav" WAVE
TRACK 01 AUDIO
INDEX 01 00:00:00
TRACK 02 AUDIO
INDEX 01 02:30:00
`
	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var gotPath string
	if err := s.Resolve(func(path string) (time.Duration, error) {
		gotPath = path
		return 4*time.Minute + 15*time.Second, nil
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotPath != audio {
		t.Errorf("duration func path = %q, want %q", gotPath, audio)
	}
	if got := s.Files[0].Tracks[1].End; got != 4*time.Minute+15*time.Second {
		t.Errorf("last track End = %v, want 4m15s", got)
	}
}

func TestResolvePropagatesDurationError(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "album.wav")
	s, err := Parse([]byte("FILE \"album.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n"), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	boom := errors.New("boom")
	err = s.Resolve(func(string) (time.Duration, error) { return 0, boom })
	if err == nil {
		t.Fatal("Resolve: expected error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("Resolve error does not wrap duration error: %v", err)
	}
	if !strings.Contains(err.Error(), audio) {
		t.Errorf("Resolve error does not mention %q: %v", audio, err)
	}
}

func TestResolveNilDurationFunc(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")
	s, err := Parse([]byte("FILE \"album.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n"), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := s.Resolve(nil); err == nil {
		t.Fatal("Resolve(nil): expected error")
	}
}

func TestDecoding(t *testing.T) {
	const src = "TITLE \"Café\"\nFILE \"a.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n"

	utf16le := func(t *testing.T) []byte {
		t.Helper()
		b, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes([]byte(src))
		if err != nil {
			t.Fatalf("encode UTF-16 LE: %v", err)
		}
		return b
	}
	utf16be := func(t *testing.T) []byte {
		t.Helper()
		b, err := unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder().Bytes([]byte(src))
		if err != nil {
			t.Fatalf("encode UTF-16 BE: %v", err)
		}
		return b
	}
	cp1252 := func(t *testing.T) []byte {
		t.Helper()
		b, err := charmap.Windows1252.NewEncoder().Bytes([]byte(src))
		if err != nil {
			t.Fatalf("encode windows-1252: %v", err)
		}
		if bytes.Equal(b, []byte(src)) {
			t.Fatal("windows-1252 fixture is identical to UTF-8; fixture is not exercising decoding")
		}
		return b
	}
	latin1 := func(t *testing.T) []byte {
		t.Helper()
		b, err := charmap.ISO8859_1.NewEncoder().Bytes([]byte(src))
		if err != nil {
			t.Fatalf("encode ISO-8859-1: %v", err)
		}
		return b
	}

	tests := []struct {
		name string
		data func(*testing.T) []byte
		opts Options
	}{
		{name: "utf8", data: func(*testing.T) []byte { return []byte(src) }},
		{name: "utf8 bom", data: func(*testing.T) []byte { return append([]byte{0xEF, 0xBB, 0xBF}, []byte(src)...) }},
		{name: "utf16 le", data: utf16le},
		{name: "utf16 be", data: utf16be},
		{name: "windows-1252 default", data: cp1252},
		{name: "windows-1252 explicit", data: cp1252, opts: Options{FallbackEncoding: "windows-1252"}},
		{name: "iso-8859-1", data: latin1, opts: Options{FallbackEncoding: "iso-8859-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.wav")

			data := tt.data(t)
			s, err := Parse(data, dir, tt.opts)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if s.Title != "Café" {
				t.Errorf("Title = %q, want %q", s.Title, "Café")
			}
		})
	}
}

func TestUnsupportedFallbackEncoding(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.wav")
	src := "FILE \"a.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n"

	_, err := Parse([]byte(src), dir, Options{FallbackEncoding: "definitely-not-an-encoding"})
	if err == nil {
		t.Fatal("Parse: expected error for unsupported fallback encoding")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error is not *ParseError: %T %v", err, err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "encoding") {
		t.Errorf("error does not mention encoding: %v", err)
	}
}

func TestPathResolution(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string) (ref, want string)
	}{
		{
			name: "relative with subdirectory",
			setup: func(t *testing.T, dir string) (string, string) {
				audio := writeFile(t, dir, "sub", "album.wav")
				return "sub/album.wav", audio
			},
		},
		{
			name: "windows backslashes",
			setup: func(t *testing.T, dir string) (string, string) {
				audio := writeFile(t, dir, "sub", "album.wav")
				return `sub\album.wav`, audio
			},
		},
		{
			name: "absolute",
			setup: func(t *testing.T, dir string) (string, string) {
				audio := writeFile(t, dir, "album.wav")
				return audio, audio
			},
		},
		{
			name: "file url",
			setup: func(t *testing.T, dir string) (string, string) {
				audio := writeFile(t, dir, "album.wav")
				return "file://" + audio, audio
			},
		},
		{
			name: "file url with localhost and escaped spaces",
			setup: func(t *testing.T, dir string) (string, string) {
				audio := writeFile(t, dir, "my album.wav")
				ref := "file://localhost" + strings.ReplaceAll(audio, " ", "%20")
				return ref, audio
			},
		},
		{
			name: "case-insensitive fallback",
			setup: func(t *testing.T, dir string) (string, string) {
				audio := writeFile(t, dir, "Album.WAV")
				return "album.wav", audio
			},
		},
		{
			name: "case-insensitive fallback is deterministic",
			setup: func(t *testing.T, dir string) (string, string) {
				writeFile(t, dir, "Album.Wav")
				// ASCII sort puts "ALBUM.WAV" before "Album.Wav".
				first := writeFile(t, dir, "ALBUM.WAV")
				return "album.wav", first
			},
		},
		{
			name: "windows drive path is local",
			setup: func(t *testing.T, dir string) (string, string) {
				audio := writeFile(t, dir, "C:", "Music", "album.wav")
				return `C:\Music\album.wav`, audio
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			ref, want := tt.setup(t, dir)
			src := fmt.Sprintf("FILE %q WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n", ref)

			s, err := Parse([]byte(src), dir, Options{})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := s.Files[0].Path; got != want {
				t.Errorf("resolved path = %q, want %q", got, want)
			}
			if !filepath.IsAbs(s.Files[0].Path) {
				t.Errorf("resolved path %q is not absolute", s.Files[0].Path)
			}
		})
	}
}

func TestRemoteURLsRejected(t *testing.T) {
	for _, remote := range []string{
		"http://example.com/a.wav",
		"https://example.com/a.wav",
		"ftp://example.com/a.wav",
		"s3://bucket/a.wav",
	} {
		t.Run(remote, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.wav")
			src := fmt.Sprintf("FILE %q WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n", remote)

			_, err := Parse([]byte(src), dir, Options{})
			if err == nil {
				t.Fatal("Parse: expected remote URL to be rejected")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error is not *ParseError: %T %v", err, err)
			}
			if pe.Line != 1 {
				t.Errorf("ParseError.Line = %d, want 1", pe.Line)
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "scheme") && !strings.Contains(msg, "local") {
				t.Errorf("error does not explain remote rejection: %v", err)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	const audioCue = "FILE \"a.wav\" WAVE\n"

	tests := []struct {
		name    string
		src     string
		line    int
		wantSub string
	}{
		{
			name:    "missing INDEX 01",
			src:     audioCue + "TRACK 01 AUDIO\nTITLE \"x\"\n",
			line:    2,
			wantSub: "INDEX 01",
		},
		{
			name:    "equal start times",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01 00:01:00\nTRACK 02 AUDIO\nINDEX 01 00:01:00\n",
			line:    5,
			wantSub: "increase",
		},
		{
			name:    "decreasing start times",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01 00:01:00\nTRACK 02 AUDIO\nINDEX 01 00:00:30\n",
			line:    5,
			wantSub: "increase",
		},
		{
			name:    "track before FILE",
			src:     "TRACK 01 AUDIO\nINDEX 01 00:00:00\n",
			line:    1,
			wantSub: "FILE",
		},
		{
			name:    "INDEX before TRACK",
			src:     audioCue + "INDEX 01 00:00:00\n",
			line:    2,
			wantSub: "TRACK",
		},
		{
			name:    "seconds out of range",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01 00:99:00\n",
			line:    3,
			wantSub: "time",
		},
		{
			name:    "frames out of range",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01 00:00:75\n",
			line:    3,
			wantSub: "time",
		},
		{
			name:    "non-numeric time",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01 xx:00:00\n",
			line:    3,
			wantSub: "time",
		},
		{
			name:    "negative time component",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01 -1:00:00\n",
			line:    3,
			wantSub: "time",
		},
		{
			name:    "bad track number",
			src:     audioCue + "TRACK XX AUDIO\n",
			line:    2,
			wantSub: "track number",
		},
		{
			name:    "zero track number",
			src:     audioCue + "TRACK 00 AUDIO\n",
			line:    2,
			wantSub: "track number",
		},
		{
			name:    "FILE without name",
			src:     "FILE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n",
			line:    1,
			wantSub: "FILE",
		},
		{
			name:    "FILE with empty name",
			src:     "FILE \"\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n",
			line:    1,
			wantSub: "empty",
		},
		{
			name:    "duplicate INDEX 01",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01 00:00:00\nINDEX 01 00:01:00\n",
			line:    4,
			wantSub: "INDEX 01",
		},
		{
			name:    "unterminated quote",
			src:     `FILE "a.wav WAVE` + "\n",
			line:    1,
			wantSub: "unterminated",
		},
		{
			name:    "empty input",
			src:     "",
			line:    1,
			wantSub: "FILE",
		},
		{
			name:    "only REM",
			src:     "REM nothing here\n",
			line:    1,
			wantSub: "FILE",
		},
		{
			name:    "malformed PREGAP",
			src:     audioCue + "TRACK 01 AUDIO\nPREGAP nope\nINDEX 01 00:00:00\n",
			line:    3,
			wantSub: "time",
		},
		{
			name:    "malformed POSTGAP",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01 00:00:00\nPOSTGAP 1:2:3:4\n",
			line:    4,
			wantSub: "time",
		},
		{
			name:    "TITLE without value",
			src:     "TITLE\n" + audioCue + "TRACK 01 AUDIO\nINDEX 01 00:00:00\n",
			line:    1,
			wantSub: "TITLE",
		},
		{
			name:    "INDEX without time",
			src:     audioCue + "TRACK 01 AUDIO\nINDEX 01\n",
			line:    3,
			wantSub: "INDEX",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.wav")

			_, err := Parse([]byte(tt.src), dir, Options{})
			if err == nil {
				t.Fatal("Parse: expected error")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error is not *ParseError: %T %v", err, err)
			}
			if pe.Line != tt.line {
				t.Errorf("ParseError.Line = %d, want %d (error: %v)", pe.Line, tt.line, err)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not contain %q", err, tt.wantSub)
			}
		})
	}
}

func TestParseErrorFormatsLineAndUnwraps(t *testing.T) {
	pe := &ParseError{Line: 7, Err: ErrTooLarge}
	msg := pe.Error()
	if !strings.Contains(msg, "line 7") {
		t.Errorf("ParseError.Error() = %q, want line number", msg)
	}
	if !errors.Is(pe, ErrTooLarge) {
		t.Error("ParseError does not unwrap to ErrTooLarge")
	}
}

func TestOversizedInput(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.wav")

	tooLarge := bytes.Repeat([]byte("A"), maxInputSize+1)
	_, err := Parse(tooLarge, dir, Options{})
	if err == nil {
		t.Fatal("Parse: expected size limit error")
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("error does not wrap ErrTooLarge: %v", err)
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error is not *ParseError: %T %v", err, err)
	}
	if pe.Line != 1 {
		t.Errorf("ParseError.Line = %d, want 1", pe.Line)
	}

	// Exactly at the limit must be accepted by the size check (it then fails
	// as a normal parse because it contains no FILE directive).
	atLimit := bytes.Repeat([]byte("\n"), maxInputSize)
	if _, err := Parse(atLimit, dir, Options{}); errors.Is(err, ErrTooLarge) {
		t.Errorf("input exactly at limit was rejected as too large: %v", err)
	}
}

func TestMissingAudioFileIsResolvedNotRejected(t *testing.T) {
	dir := t.TempDir()
	src := "FILE \"missing.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n"

	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := filepath.Join(dir, "missing.wav")
	if got := s.Files[0].Path; got != want {
		t.Errorf("resolved path = %q, want %q", got, want)
	}
}

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")
	cuePath := filepath.Join(dir, "album.cue")
	src := `TITLE "Album"
FILE "album.wav" WAVE
TRACK 01 AUDIO
INDEX 01 00:00:00
`
	if err := os.WriteFile(cuePath, []byte(src), 0o644); err != nil {
		t.Fatalf("write cue: %v", err)
	}

	s, err := ParseFile(cuePath, Options{})
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if s.Path != cuePath {
		t.Errorf("Path = %q, want %q", s.Path, cuePath)
	}
	if len(s.Files) != 1 || s.Files[0].Path != filepath.Join(dir, "album.wav") {
		t.Errorf("Files = %+v, want resolved album.wav", s.Files)
	}
}

func TestParseFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.cue")
	_, err := ParseFile(path, Options{})
	if err == nil {
		t.Fatal("ParseFile: expected error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error does not mention path %q: %v", path, err)
	}
}

func TestParseFileOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.cue")
	if err := os.WriteFile(path, bytes.Repeat([]byte("A"), maxInputSize+1), 0o644); err != nil {
		t.Fatalf("write cue: %v", err)
	}
	_, err := ParseFile(path, Options{})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ParseFile error = %v, want ErrTooLarge", err)
	}
}

func TestUnknownDirectivesIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")

	src := `REM this is ignored, "quotes" and everything
REM "unterminated quote is still ignored
UNKNOWN "unterminated quote on an unknown directive is ignored too
REM
FILE "album.wav" WAVE
  FLAGS DCP
  TRACK 01 AUDIO
    ISRC USABC1234567
    SONGWRITER "Someone"
    TITLE "Only Track"
    INDEX 01 00:00:00
    SOMETHING_NEW "ignored"
`
	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(s.Files[0].Tracks) != 1 {
		t.Fatalf("len(Tracks) = %d, want 1", len(s.Files[0].Tracks))
	}
	if got := s.Files[0].Tracks[0].Title; got != "Only Track" {
		t.Errorf("Title = %q, want %q", got, "Only Track")
	}
}

func TestDirectivesAreCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")

	src := `performer "Lower"
title "Case"
file "album.wav" wave
track 01 audio
title "Track"
index 01 00:30:00
`
	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Performer != "Lower" || s.Title != "Case" {
		t.Errorf("sheet title/performer = %q/%q, want Case/Lower", s.Title, s.Performer)
	}
	if got := s.Files[0].Tracks[0].Start; got != 30*time.Second {
		t.Errorf("Start = %v, want 30s", got)
	}
}

func TestUnquotedTitleJoinsWords(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")

	src := `FILE "album.wav" WAVE
TRACK 01 AUDIO
TITLE Two Words
INDEX 01 00:00:00
`
	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := s.Files[0].Tracks[0].Title; got != "Two Words" {
		t.Errorf("Title = %q, want %q", got, "Two Words")
	}
}

func TestFileTypeAndTrailingTokensIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")

	src := `FILE "album.wav" WAVE
TRACK 01 AUDIO
INDEX 01 00:00:00
INDEX 02 00:00:10
INDEX 03 00:00:20
`
	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := s.Files[0].Tracks[0].Start; got != 0 {
		t.Errorf("Start = %v, want 0", got)
	}
}

func TestSheetTrackValueOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")

	src := `TITLE "Album"
PERFORMER "Album Artist"
FILE "album.wav" WAVE
TRACK 01 AUDIO
INDEX 01 00:00:00
TRACK 02 AUDIO
TITLE "Named"
PERFORMER "Guest"
INDEX 01 01:00:00
`
	s, err := Parse([]byte(src), dir, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	t1, t2 := s.Files[0].Tracks[0], s.Files[0].Tracks[1]
	if t1.Performer != "Album Artist" {
		t.Errorf("track 1 Performer = %q, want album fallback", t1.Performer)
	}
	if t1.Title != "" {
		t.Errorf("track 1 Title = %q, want empty (no album fallback)", t1.Title)
	}
	if t2.Title != "Named" || t2.Performer != "Guest" {
		t.Errorf("track 2 values = %q/%q, want Named/Guest", t2.Title, t2.Performer)
	}
}

func TestNoTracks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "album.wav")
	_, err := Parse([]byte("FILE \"album.wav\" WAVE\n"), dir, Options{})
	if err == nil {
		t.Fatal("Parse: expected error for FILE without tracks")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error is not *ParseError: %T %v", err, err)
	}
}
