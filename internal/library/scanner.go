package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"player2/internal/cue"
	"player2/internal/meta"
)

// Scanner reads directories and CUE sheets for the browser and queue loading.
//
// Plain audio entries are not probed here; the TUI fills their metadata
// asynchronously later. The prober is used only to resolve CUE track ranges.
type Scanner struct {
	opts      Options
	audioExts map[string]bool
	prober    meta.Prober
}

// New returns a scanner. AudioExtensions are normalized once and may be given
// with or without a leading dot, in any case.
func New(opts Options, prober meta.Prober) *Scanner {
	return &Scanner{
		opts:      opts,
		audioExts: normalizeExtensions(opts.AudioExtensions),
		prober:    prober,
	}
}

// ReadDir lists one directory.
//
// Hidden entries are skipped unless ShowHidden is set. Symlinked directories
// are skipped unless FollowSymlinks is set; symlinked regular files are
// included when their target resolves. A valid CUE becomes a KindCueDir
// virtual folder and the same-directory audio files it references are hidden.
// An invalid CUE becomes KindInvalidCue and hides nothing. Individual
// unreadable or unparsable CUEs never fail the whole directory; only an
// unreadable directory itself returns an error.
func (s *Scanner) ReadDir(ctx context.Context, dir string) ([]Entry, error) {
	ctx = ensureContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("library: resolve directory %q: %w", dir, err)
	}

	dirents, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("library: read directory %s: %w", abs, err)
	}

	entries := make([]Entry, 0, len(dirents))
	audio := make([]Entry, 0, len(dirents))
	referenced := make(map[string]struct{})

	for _, dirent := range dirents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		name := dirent.Name()
		if !s.opts.ShowHidden && strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(abs, name)

		isDir := dirent.IsDir()
		if dirent.Type()&os.ModeSymlink != 0 {
			target, err := os.Stat(path)
			if err != nil {
				// Broken or unreadable symlink: nothing to show.
				continue
			}
			switch {
			case target.IsDir():
				if !s.opts.FollowSymlinks {
					continue
				}
				isDir = true
			case target.Mode().IsRegular():
				isDir = false
			default:
				continue
			}
		} else if !isDir {
			info, err := dirent.Info()
			if err != nil {
				// The entry disappeared between ReadDir and classify.
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
		}

		if isDir {
			entries = append(entries, Entry{Kind: KindDir, Name: name, Path: path})
			continue
		}

		switch ext := strings.ToLower(filepath.Ext(name)); {
		case s.audioExts[ext]:
			audio = append(audio, Entry{Kind: KindAudio, Name: name, Path: path})
		case ext == ".m3u" || ext == ".m3u8":
			entries = append(entries, Entry{Kind: KindPlaylist, Name: name, Path: path})
		case ext == ".cue":
			sheet, err := s.validateCue(ctx, path)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				entries = append(entries, Entry{
					Kind: KindInvalidCue,
					Name: name,
					Path: path,
					Err:  err,
				})
				continue
			}
			entries = append(entries, Entry{
				Kind:    KindCueDir,
				Name:    name + "/",
				Path:    path,
				CuePath: path,
			})
			for _, file := range sheet.Files {
				referenced[file.Path] = struct{}{}
			}
		}
	}

	for _, entry := range audio {
		if _, hidden := referenced[entry.Path]; hidden {
			continue
		}
		entries = append(entries, entry)
	}

	sortEntries(entries)
	return entries, nil
}

// ReadCue validates a CUE sheet and returns one KindCueTrack entry per track in
// sheet order. It returns an error for an invalid sheet, a missing or
// non-regular FILE, a failing or non-positive duration, or a track whose range
// ends before it starts.
func (s *Scanner) ReadCue(ctx context.Context, cuePath string) ([]Entry, error) {
	ctx = ensureContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(cuePath)
	if err != nil {
		return nil, fmt.Errorf("library: resolve cue %q: %w", cuePath, err)
	}
	sheet, err := s.validateCue(ctx, abs)
	if err != nil {
		return nil, err
	}

	tracks := sheet.AllTracks()
	entries := make([]Entry, 0, len(tracks))
	for _, track := range tracks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry, err := cueTrackEntry(sheet, abs, track)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Walk recursively scans dir and returns playable entries only: KindAudio and
// KindCueTrack. Containers (directories, CUE folders, playlists, invalid CUEs)
// are not returned; valid CUE folders expand into their tracks.
//
// Walk skips unreadable subdirectories so one bad folder cannot abort the whole
// walk, and it skips symlink cycles using resolved real paths. An unreadable or
// missing starting directory is reported as an error. A CUE that was valid
// during the directory listing but fails to expand is reported as an error
// rather than silently dropping its tracks.
func (s *Scanner) Walk(ctx context.Context, dir string) ([]Entry, error) {
	ctx = ensureContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("library: resolve directory %q: %w", dir, err)
	}

	visited := make(map[string]bool)
	var entries []Entry
	if err := s.walk(ctx, abs, true, visited, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Scanner) walk(ctx context.Context, dir string, root bool, visited map[string]bool, out *[]Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if root {
			return fmt.Errorf("library: resolve directory %s: %w", dir, err)
		}
		return nil
	}
	if visited[real] {
		return nil
	}
	visited[real] = true

	entries, err := s.ReadDir(ctx, dir)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if root {
			return err
		}
		return nil
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch entry.Kind {
		case KindAudio:
			*out = append(*out, entry)
		case KindCueDir:
			tracks, err := s.ReadCue(ctx, entry.Path)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				return err
			}
			*out = append(*out, tracks...)
		case KindDir:
			if err := s.walk(ctx, entry.Path, false, visited, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// ResolveCueTrack returns the single CUE track whose TRACK number is n.
func (s *Scanner) ResolveCueTrack(ctx context.Context, cuePath string, n int) (Entry, error) {
	ctx = ensureContext(ctx)
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}

	abs, err := filepath.Abs(cuePath)
	if err != nil {
		return Entry{}, fmt.Errorf("library: resolve cue %q: %w", cuePath, err)
	}
	sheet, err := s.validateCue(ctx, abs)
	if err != nil {
		return Entry{}, err
	}

	for _, track := range sheet.AllTracks() {
		if track.Number == n {
			return cueTrackEntry(sheet, abs, track)
		}
	}
	return Entry{}, fmt.Errorf("library: cue %s: track %d not found", abs, n)
}

// validateCue parses a sheet and verifies that every FILE exists as a regular
// file and that the prober can supply positive durations. It returns the
// resolved sheet, so track ranges are ready for use.
func (s *Scanner) validateCue(ctx context.Context, cuePath string) (*cue.Sheet, error) {
	sheet, err := cue.ParseFile(cuePath, cue.Options{FallbackEncoding: s.opts.CueFallbackEncoding})
	if err != nil {
		return nil, fmt.Errorf("library: parse cue %s: %w", cuePath, err)
	}

	for _, file := range sheet.Files {
		info, err := os.Stat(file.Path)
		if err != nil {
			return nil, fmt.Errorf("library: cue %s: file %s: %w", cuePath, file.Path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("library: cue %s: file %s is not a regular file", cuePath, file.Path)
		}
	}

	if s.prober == nil {
		return nil, fmt.Errorf("library: cue %s: no metadata prober configured", cuePath)
	}

	err = sheet.Resolve(func(path string) (time.Duration, error) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		info, err := s.prober.Probe(ctx, path)
		if err != nil {
			return 0, err
		}
		if info.Duration <= 0 {
			return 0, fmt.Errorf("duration of %s is %s (must be positive)", path, info.Duration)
		}
		return info.Duration, nil
	})
	if err != nil {
		return nil, fmt.Errorf("library: resolve cue %s: %w", cuePath, err)
	}

	for _, file := range sheet.Files {
		for _, track := range file.Tracks {
			if track.End < track.Start {
				return nil, fmt.Errorf("library: cue %s: track %d ends before it starts", cuePath, track.Number)
			}
		}
	}
	return sheet, nil
}

// cueTrackEntry builds the display and metadata entry for one resolved track.
func cueTrackEntry(sheet *cue.Sheet, cuePath string, track cue.Track) (Entry, error) {
	if track.FileIndex < 0 || track.FileIndex >= len(sheet.Files) {
		return Entry{}, fmt.Errorf("library: cue %s: track %d has invalid file index %d", cuePath, track.Number, track.FileIndex)
	}
	if track.End < track.Start {
		return Entry{}, fmt.Errorf("library: cue %s: track %d ends before it starts", cuePath, track.Number)
	}

	title := track.Title
	if strings.TrimSpace(title) == "" {
		title = fmt.Sprintf("Track %02d", track.Number)
	}
	performer := track.Performer
	if strings.TrimSpace(performer) == "" {
		performer = sheet.Performer
	}

	name := fmt.Sprintf("%02d. %s", track.Number, title)
	if strings.TrimSpace(performer) != "" {
		name += " — " + performer
	}

	duration := track.End - track.Start
	return Entry{
		Kind:     KindCueTrack,
		Name:     name,
		Path:     sheet.Files[track.FileIndex].Path,
		CuePath:  cuePath,
		TrackNo:  track.Number,
		Start:    track.Start,
		Duration: duration,
		Meta: meta.Info{
			Title:    title,
			Artist:   performer,
			Album:    albumName(sheet, cuePath),
			TrackNo:  track.Number,
			Duration: duration,
		},
	}, nil
}

// albumName returns the sheet title, falling back to the CUE basename without
// its .cue extension.
func albumName(sheet *cue.Sheet, cuePath string) string {
	if strings.TrimSpace(sheet.Title) != "" {
		return sheet.Title
	}
	base := filepath.Base(cuePath)
	if ext := filepath.Ext(base); strings.EqualFold(ext, ".cue") {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// normalizeExtensions lowercases extensions, trims surrounding space, and adds
// a leading dot when the caller omitted it. An empty input disables audio
// detection entirely.
func normalizeExtensions(exts []string) map[string]bool {
	normalized := make(map[string]bool, len(exts))
	for _, ext := range exts {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" || ext == "." {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		normalized[ext] = true
	}
	return normalized
}

// sortEntries orders CUE folders and directories first, then files, using a
// natural case-insensitive comparison. Path is a deterministic tie-break.
func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		groupA, groupB := sortGroup(a.Kind), sortGroup(b.Kind)
		if groupA != groupB {
			return groupA < groupB
		}
		if cmp := naturalCompare(a.Name, b.Name); cmp != 0 {
			return cmp < 0
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
}

func sortGroup(kind Kind) int {
	if kind == KindDir || kind == KindCueDir {
		return 0
	}
	return 1
}

// naturalCompare compares strings case-insensitively, treating runs of ASCII
// digits as numbers so "track2" sorts before "track10". It returns a negative
// value when a sorts before b, zero when they are equivalent, and a positive
// value otherwise.
func naturalCompare(a, b string) int {
	ar, br := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ar) && j < len(br) {
		if isASCIIDigit(ar[i]) && isASCIIDigit(br[j]) {
			startA, startB := i, j
			for i < len(ar) && isASCIIDigit(ar[i]) {
				i++
			}
			for j < len(br) && isASCIIDigit(br[j]) {
				j++
			}
			digitsA := trimLeadingZeros(ar[startA:i])
			digitsB := trimLeadingZeros(br[startB:j])
			if len(digitsA) != len(digitsB) {
				if len(digitsA) < len(digitsB) {
					return -1
				}
				return 1
			}
			for k := range digitsA {
				if digitsA[k] != digitsB[k] {
					if digitsA[k] < digitsB[k] {
						return -1
					}
					return 1
				}
			}
			continue
		}

		ca, cb := unicode.ToLower(ar[i]), unicode.ToLower(br[j])
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		i++
		j++
	}

	switch {
	case i < len(ar):
		return 1
	case j < len(br):
		return -1
	default:
		return 0
	}
}

func isASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

// trimLeadingZeros drops insignificant leading zeroes, keeping one digit for
// an all-zero run.
func trimLeadingZeros(digits []rune) []rune {
	for len(digits) > 1 && digits[0] == '0' {
		digits = digits[1:]
	}
	return digits
}

func ensureContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
