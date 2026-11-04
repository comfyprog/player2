package cue

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzParse checks that arbitrary CUE input can never panic the parser or the
// resolver. All returned errors must be typed *ParseError.
func FuzzParse(f *testing.F) {
	dir := f.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.wav"), []byte("x"), 0o644); err != nil {
		f.Fatalf("seed audio file: %v", err)
	}

	seeds := []string{
		"",
		"REM empty\n",
		"FILE \"a.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n",
		"TITLE \"T\"\nPERFORMER \"P\"\nFILE \"a.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 00 00:00:00\nINDEX 01 00:00:30\nTRACK 02 AUDIO\nINDEX 01 01:00:00\n",
		"FILE \"a.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:99:00\n",
		"FILE \"a.wav WAVE\n",
		"FILE \"http://example.com/a.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n",
		"FILE \"a.wav\" WAVE\nTRACK 01 AUDIO\nPREGAP 00:00:00\nPOSTGAP 00:00:01\nINDEX 01 00:00:00\n",
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := Parse(data, dir, Options{})
		if err != nil {
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Parse returned non-ParseError: %T %v", err, err)
			}
			if pe.Line < 1 {
				t.Fatalf("ParseError.Line = %d, want >= 1", pe.Line)
			}
			return
		}

		if err := s.Resolve(func(string) (time.Duration, error) {
			return time.Hour, nil
		}); err != nil {
			t.Fatalf("Resolve failed for successfully parsed input: %v", err)
		}
		_ = s.AllTracks()
	})
}
