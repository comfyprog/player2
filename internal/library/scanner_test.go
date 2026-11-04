package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"player2/internal/meta"
)

// fakeProber reports fixed durations from a path-keyed map. It records the
// paths it was asked about so tests can prove that plain audio is not probed.
type fakeProber struct {
	mu        sync.Mutex
	durations map[string]time.Duration
	errs      map[string]error
	calls     []string
}

func newFakeProber() *fakeProber {
	return &fakeProber{
		durations: make(map[string]time.Duration),
		errs:      make(map[string]error),
	}
}

func (p *fakeProber) set(path string, duration time.Duration) *fakeProber {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.durations[path] = duration
	return p
}

func (p *fakeProber) fail(path string, err error) *fakeProber {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errs[path] = err
	return p
}

func (p *fakeProber) Probe(ctx context.Context, path string) (meta.Info, error) {
	p.mu.Lock()
	p.calls = append(p.calls, path)
	err := p.errs[path]
	duration := p.durations[path]
	p.mu.Unlock()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return meta.Info{}, ctxErr
	}
	if err != nil {
		return meta.Info{}, err
	}
	return meta.Info{Duration: duration}, nil
}

func (p *fakeProber) probed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// writeFile creates a dummy regular file and returns its absolute path.
func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func makeDir(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	return path
}

func writeCue(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write cue %s: %v", name, err)
	}
	return path
}

func entryNames(entries []Entry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}
	return names
}

func assertNames(t *testing.T, entries []Entry, want ...string) {
	t.Helper()
	got := entryNames(entries)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
}

func findEntry(t *testing.T, entries []Entry, name string) Entry {
	t.Helper()
	for _, entry := range entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("entry %q not found in %#v", name, entryNames(entries))
	return Entry{}
}

func TestReadDirBasic(t *testing.T) {
	dir := t.TempDir()
	makeDir(t, dir, "Sub")
	writeFile(t, dir, "Song.FLAC")
	writeFile(t, dir, "other.mp3")
	writeFile(t, dir, "mix.M3U8")
	writeFile(t, dir, "list.m3u")
	writeFile(t, dir, "notes.txt")
	writeFile(t, dir, ".hidden.flac")
	makeDir(t, dir, ".hidden")

	prober := newFakeProber()
	s := New(Options{AudioExtensions: []string{"flac", ".MP3"}}, prober)
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	assertNames(t, entries, "Sub", "list.m3u", "mix.M3U8", "other.mp3", "Song.FLAC")
	if entries[0].Kind != KindDir || entries[0].Path != filepath.Join(dir, "Sub") {
		t.Errorf("Sub entry = %+v, want KindDir with absolute path", entries[0])
	}
	if got := findEntry(t, entries, "Song.FLAC"); got.Kind != KindAudio || got.Path != filepath.Join(dir, "Song.FLAC") {
		t.Errorf("Song.FLAC = %+v, want KindAudio with absolute path", got)
	}
	if got := findEntry(t, entries, "list.m3u"); got.Kind != KindPlaylist {
		t.Errorf("list.m3u kind = %v, want KindPlaylist", got.Kind)
	}
	if got := findEntry(t, entries, "mix.M3U8"); got.Kind != KindPlaylist {
		t.Errorf("mix.M3U8 kind = %v, want KindPlaylist", got.Kind)
	}
	if calls := prober.probed(); len(calls) != 0 {
		t.Errorf("prober called for plain audio: %v", calls)
	}
}

func TestReadDirNoAudioExtensions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "song.flac")
	writeFile(t, dir, "list.m3u")

	s := New(Options{}, newFakeProber())
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	assertNames(t, entries, "list.m3u")
}

func TestReadDirNaturalSort(t *testing.T) {
	dir := t.TempDir()
	makeDir(t, dir, "b")
	makeDir(t, dir, "A")
	writeFile(t, dir, "z.flac")
	writeFile(t, dir, "track10.flac")
	writeFile(t, dir, "Track2.flac")
	writeFile(t, dir, "track01.flac")
	writeFile(t, dir, "List10.m3u")
	writeFile(t, dir, "list2.m3u")
	cuePath := writeCue(t, dir, "Zebra.cue", `TITLE "Zebra"
FILE "z.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
	prober := newFakeProber().set(filepath.Join(dir, "z.flac"), time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	assertNames(t, entries,
		"A", "b", "Zebra.cue/",
		"list2.m3u", "List10.m3u",
		"track01.flac", "Track2.flac", "track10.flac",
	)
	got := findEntry(t, entries, "Zebra.cue/")
	if got.Kind != KindCueDir || got.Path != cuePath || got.CuePath != cuePath {
		t.Errorf("Zebra.cue/ = %+v, want valid KindCueDir", got)
	}
}

func TestReadDirHidden(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "audio.flac")
	writeFile(t, dir, ".hidden.flac")
	makeDir(t, dir, ".hidden")
	writeCue(t, dir, ".secret.cue", `FILE "audio.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
	writeFile(t, dir, "visible.txt")
	prober := newFakeProber().set(audio, time.Minute)

	hiddenOff := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := hiddenOff.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	assertNames(t, entries, "audio.flac")

	hiddenOn := New(Options{ShowHidden: true, AudioExtensions: []string{"flac"}}, prober)
	entries, err = hiddenOn.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir with hidden: %v", err)
	}
	assertNames(t, entries, ".hidden", ".secret.cue/", ".hidden.flac")
	if entries[0].Kind != KindDir {
		t.Errorf(".hidden kind = %v, want KindDir", entries[0].Kind)
	}
	if entries[1].Kind != KindCueDir {
		t.Errorf(".secret.cue/ kind = %v, want KindCueDir", entries[1].Kind)
	}
}

func TestReadDirSymlinks(t *testing.T) {
	dir := t.TempDir()
	realDir := makeDir(t, dir, "real")
	writeFile(t, realDir, "song.flac")
	plain := writeFile(t, dir, "plain.flac")

	if err := os.Symlink(realDir, filepath.Join(dir, "linkdir")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := os.Symlink(plain, filepath.Join(dir, "linkfile.flac")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "broken.flac")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	prober := newFakeProber()
	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	assertNames(t, entries, "real", "linkfile.flac", "plain.flac")
	if got := findEntry(t, entries, "linkfile.flac"); got.Kind != KindAudio || got.Path != filepath.Join(dir, "linkfile.flac") {
		t.Errorf("linkfile.flac = %+v, want KindAudio at the link path", got)
	}

	follow := New(Options{FollowSymlinks: true, AudioExtensions: []string{"flac"}}, prober)
	entries, err = follow.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir following symlinks: %v", err)
	}
	assertNames(t, entries, "linkdir", "real", "linkfile.flac", "plain.flac")
	if got := findEntry(t, entries, "linkdir"); got.Kind != KindDir || got.Path != filepath.Join(dir, "linkdir") {
		t.Errorf("linkdir = %+v, want KindDir at the link path", got)
	}
}

func TestReadDirValidCueHidesAudio(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "album.flac")
	writeFile(t, dir, "unrelated.flac")
	cuePath := writeCue(t, dir, "Album.cue", `TITLE "The Album"
PERFORMER "The Artist"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Two"
    INDEX 01 01:00:00
`)
	prober := newFakeProber().set(audio, 3*time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	assertNames(t, entries, "Album.cue/", "unrelated.flac")

	got := findEntry(t, entries, "Album.cue/")
	if got.Kind != KindCueDir {
		t.Errorf("kind = %v, want KindCueDir", got.Kind)
	}
	if got.Path != cuePath || got.CuePath != cuePath {
		t.Errorf("paths = (%q, %q), want both %q", got.Path, got.CuePath, cuePath)
	}
	if got.Err != nil {
		t.Errorf("Err = %v, want nil", got.Err)
	}
	if calls := prober.probed(); !reflect.DeepEqual(calls, []string{audio}) {
		t.Errorf("prober calls = %v, want only the CUE audio", calls)
	}
}

func TestReadDirInvalidCueKeepsAudio(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "album.flac")
	writeCue(t, dir, "broken.cue", `TITLE "Broken"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
FILE "missing.flac" WAVE
  TRACK 02 AUDIO
    INDEX 01 00:00:00
`)
	prober := newFakeProber().set(audio, 3*time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	assertNames(t, entries, "album.flac", "broken.cue")
	if got := findEntry(t, entries, "broken.cue"); got.Kind != KindInvalidCue || got.Err == nil {
		t.Errorf("broken.cue = %+v, want KindInvalidCue with Err", got)
	}
	if got := findEntry(t, entries, "album.flac"); got.Kind != KindAudio {
		t.Errorf("album.flac = %+v, want audio preserved", got)
	}
}

func TestReadDirUnparsableCueIsInvalid(t *testing.T) {
	dir := t.TempDir()
	writeCue(t, dir, "junk.cue", "this is not a cue sheet\n")

	s := New(Options{AudioExtensions: []string{"flac"}}, newFakeProber())
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := findEntry(t, entries, "junk.cue")
	if got.Kind != KindInvalidCue || got.Err == nil {
		t.Fatalf("junk.cue = %+v, want KindInvalidCue with Err", got)
	}
}

func TestReadDirMultipleCuesShareAudio(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "shared.flac")
	writeCue(t, dir, "first.cue", `FILE "shared.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
	writeCue(t, dir, "second.cue", `TITLE "Second"
FILE "shared.flac" WAVE
  TRACK 01 AUDIO
    TITLE "Only"
    INDEX 01 00:00:00
`)
	prober := newFakeProber().set(audio, 2*time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	assertNames(t, entries, "first.cue/", "second.cue/")
	if entries[0].Kind != KindCueDir || entries[1].Kind != KindCueDir {
		t.Errorf("kinds = (%v, %v), want both KindCueDir", entries[0].Kind, entries[1].Kind)
	}
}

func TestReadCueNamesAndMeta(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "album.flac")
	cuePath := writeCue(t, dir, "Album.cue", `TITLE "The Album"
PERFORMER "The Artist"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "Opening"
    PERFORMER "Guest"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE ""
    INDEX 01 01:00:00
  TRACK 03 AUDIO
    TITLE "Finale"
    PERFORMER "Solo"
    INDEX 01 02:00:00
`)
	prober := newFakeProber().set(audio, 5*time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadCue(context.Background(), cuePath)
	if err != nil {
		t.Fatalf("ReadCue: %v", err)
	}
	assertNames(t, entries,
		"01. Opening — Guest",
		"02. Track 02 — The Artist",
		"03. Finale — Solo",
	)

	wantStarts := []time.Duration{0, time.Minute, 2 * time.Minute}
	wantDurations := []time.Duration{time.Minute, time.Minute, 3 * time.Minute}
	wantTitles := []string{"Opening", "Track 02", "Finale"}
	wantArtists := []string{"Guest", "The Artist", "Solo"}
	for i, entry := range entries {
		if entry.Kind != KindCueTrack {
			t.Errorf("entry %d kind = %v, want KindCueTrack", i, entry.Kind)
		}
		if entry.Path != audio {
			t.Errorf("entry %d Path = %q, want %q", i, entry.Path, audio)
		}
		if entry.CuePath != cuePath {
			t.Errorf("entry %d CuePath = %q, want %q", i, entry.CuePath, cuePath)
		}
		if entry.TrackNo != i+1 {
			t.Errorf("entry %d TrackNo = %d, want %d", i, entry.TrackNo, i+1)
		}
		if entry.Start != wantStarts[i] {
			t.Errorf("entry %d Start = %v, want %v", i, entry.Start, wantStarts[i])
		}
		if entry.Duration != wantDurations[i] {
			t.Errorf("entry %d Duration = %v, want %v", i, entry.Duration, wantDurations[i])
		}
		if entry.Meta.Title != wantTitles[i] {
			t.Errorf("entry %d Meta.Title = %q, want %q", i, entry.Meta.Title, wantTitles[i])
		}
		if entry.Meta.Artist != wantArtists[i] {
			t.Errorf("entry %d Meta.Artist = %q, want %q", i, entry.Meta.Artist, wantArtists[i])
		}
		if entry.Meta.Album != "The Album" {
			t.Errorf("entry %d Meta.Album = %q, want %q", i, entry.Meta.Album, "The Album")
		}
		if entry.Meta.TrackNo != i+1 {
			t.Errorf("entry %d Meta.TrackNo = %d, want %d", i, entry.Meta.TrackNo, i+1)
		}
		if entry.Meta.Duration != wantDurations[i] {
			t.Errorf("entry %d Meta.Duration = %v, want %v", i, entry.Meta.Duration, wantDurations[i])
		}
	}
}

func TestReadCuePerformerFallbacks(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "plain.flac")
	cuePath := writeCue(t, dir, "Plain.cue", `FILE "plain.flac" WAVE
  TRACK 01 AUDIO
    TITLE "Solo"
    INDEX 01 00:00:00
`)
	prober := newFakeProber().set(audio, time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadCue(context.Background(), cuePath)
	if err != nil {
		t.Fatalf("ReadCue: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Name != "01. Solo" {
		t.Errorf("Name = %q, want %q", entry.Name, "01. Solo")
	}
	if entry.Meta.Artist != "" {
		t.Errorf("Meta.Artist = %q, want empty", entry.Meta.Artist)
	}
	if entry.Meta.Album != "Plain" {
		t.Errorf("Meta.Album = %q, want %q", entry.Meta.Album, "Plain")
	}
}

func TestReadCueMultiFile(t *testing.T) {
	dir := t.TempDir()
	one := writeFile(t, dir, "one.flac")
	two := writeFile(t, dir, "two.flac")
	cuePath := writeCue(t, dir, "Multi.cue", `TITLE "Multi"
FILE "one.flac" WAVE
  TRACK 01 AUDIO
    TITLE "First"
    INDEX 01 00:00:00
FILE "two.flac" WAVE
  TRACK 02 AUDIO
    TITLE "Second"
    INDEX 01 00:00:00
`)
	prober := newFakeProber().
		set(one, time.Minute).
		set(two, 2*time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadCue(context.Background(), cuePath)
	if err != nil {
		t.Fatalf("ReadCue: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Path != one || entries[0].Duration != time.Minute {
		t.Errorf("entry 0 = %+v, want path %q duration 1m", entries[0], one)
	}
	if entries[1].Path != two || entries[1].Duration != 2*time.Minute {
		t.Errorf("entry 1 = %+v, want path %q duration 2m", entries[1], two)
	}

	// ReadDir must hide both referenced files behind one virtual folder.
	rdEntries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	assertNames(t, rdEntries, "Multi.cue/")
}

func TestReadCueErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string, prober *fakeProber) string
	}{
		{
			name: "missing file",
			setup: func(t *testing.T, dir string, prober *fakeProber) string {
				return writeCue(t, dir, "missing.cue", `FILE "missing.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
			},
		},
		{
			name: "referenced path is a directory",
			setup: func(t *testing.T, dir string, prober *fakeProber) string {
				makeDir(t, dir, "album.flac")
				return writeCue(t, dir, "dir.cue", `FILE "album.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
			},
		},
		{
			name: "zero duration",
			setup: func(t *testing.T, dir string, prober *fakeProber) string {
				writeFile(t, dir, "album.flac")
				return writeCue(t, dir, "zero.cue", `FILE "album.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
			},
		},
		{
			name: "prober error",
			setup: func(t *testing.T, dir string, prober *fakeProber) string {
				audio := writeFile(t, dir, "album.flac")
				prober.fail(audio, errors.New("probe failed"))
				return writeCue(t, dir, "probe.cue", `FILE "album.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
			},
		},
		{
			name: "track ends before it starts",
			setup: func(t *testing.T, dir string, prober *fakeProber) string {
				audio := writeFile(t, dir, "album.flac")
				prober.set(audio, 30*time.Second)
				return writeCue(t, dir, "range.cue", `FILE "album.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 01:00:00
`)
			},
		},
		{
			name: "malformed sheet",
			setup: func(t *testing.T, dir string, prober *fakeProber) string {
				return writeCue(t, dir, "malformed.cue", "TRACK 01 AUDIO\n")
			},
		},
		{
			name: "track without index",
			setup: func(t *testing.T, dir string, prober *fakeProber) string {
				audio := writeFile(t, dir, "album.flac")
				prober.set(audio, time.Minute)
				return writeCue(t, dir, "noindex.cue", `FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "No index"
`)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			prober := newFakeProber()
			cuePath := test.setup(t, dir, prober)
			s := New(Options{AudioExtensions: []string{"flac"}}, prober)
			entries, err := s.ReadCue(context.Background(), cuePath)
			if err == nil {
				t.Fatalf("ReadCue succeeded, want error (entries %#v)", entryNames(entries))
			}
			if len(entries) != 0 {
				t.Errorf("entries = %#v, want none", entries)
			}
		})
	}
}

func TestResolveCueTrackByNumber(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "album.flac")
	cuePath := writeCue(t, dir, "Odd.cue", `PERFORMER "Artist"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    INDEX 01 00:00:00
  TRACK 03 AUDIO
    TITLE "Three"
    INDEX 01 01:00:00
  TRACK 07 AUDIO
    TITLE "Seven"
    INDEX 01 02:00:00
`)
	prober := newFakeProber().set(audio, 4*time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entry, err := s.ResolveCueTrack(context.Background(), cuePath, 3)
	if err != nil {
		t.Fatalf("ResolveCueTrack(3): %v", err)
	}
	if entry.Kind != KindCueTrack || entry.TrackNo != 3 {
		t.Errorf("entry = %+v, want KindCueTrack track 3", entry)
	}
	if entry.Name != "03. Three — Artist" {
		t.Errorf("Name = %q, want %q", entry.Name, "03. Three — Artist")
	}
	if entry.Start != time.Minute || entry.Duration != time.Minute {
		t.Errorf("range = %v..%v, want 1m..2m", entry.Start, entry.Start+entry.Duration)
	}

	if _, err := s.ResolveCueTrack(context.Background(), cuePath, 2); err == nil {
		t.Fatal("ResolveCueTrack(2) succeeded, want not-found error")
	}
}

func TestWalkExpandsCueAndSkipsContainers(t *testing.T) {
	dir := t.TempDir()
	albumAudio := writeFile(t, dir, "album.flac")
	aAudio := writeFile(t, dir, "a.flac")
	badAudio := writeFile(t, dir, "bad.flac")
	writeFile(t, dir, ".hidden.flac")
	writeFile(t, dir, "list.m3u")
	writeFile(t, dir, "notes.txt")
	sub := makeDir(t, dir, "sub")
	bAudio := writeFile(t, sub, "b.flac")
	writeCue(t, dir, "Album.cue", `PERFORMER "Artist"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Two"
    INDEX 01 01:00:00
`)
	writeCue(t, dir, "bad.cue", `FILE "missing.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
	prober := newFakeProber().
		set(albumAudio, 3*time.Minute).
		set(aAudio, time.Minute).
		set(badAudio, time.Minute).
		set(bAudio, time.Minute)

	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.Walk(context.Background(), dir)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	assertNames(t, entries,
		"01. One — Artist",
		"02. Two — Artist",
		"b.flac",
		"a.flac",
		"bad.flac",
	)
	for i, entry := range entries {
		if entry.Kind != KindAudio && entry.Kind != KindCueTrack {
			t.Errorf("entry %d %q has kind %v, want only audio or cue track", i, entry.Name, entry.Kind)
		}
	}
	if got := findEntry(t, entries, "b.flac"); got.Path != bAudio {
		t.Errorf("b.flac Path = %q, want %q", got.Path, bAudio)
	}
	if got := findEntry(t, entries, "a.flac"); got.Path != aAudio {
		t.Errorf("a.flac Path = %q, want %q", got.Path, aAudio)
	}
	// The valid CUE must hide album.flac; the invalid CUE must not hide bad.flac.
	for _, entry := range entries {
		if entry.Kind == KindAudio && entry.Path == albumAudio {
			t.Errorf("Walk yielded hidden whole-file audio %q", albumAudio)
		}
	}
	if got := findEntry(t, entries, "bad.flac"); got.Path != badAudio {
		t.Errorf("bad.flac Path = %q, want %q", got.Path, badAudio)
	}
}

func TestWalkSymlinksAndCycles(t *testing.T) {
	dir := t.TempDir()
	makeDir(t, dir, "sub")
	sub := filepath.Join(dir, "sub")
	inner := writeFile(t, sub, "inner.flac")
	song := writeFile(t, dir, "song.flac")

	if err := os.Symlink(dir, filepath.Join(sub, "loop")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := os.Symlink(sub, filepath.Join(dir, "zzlink")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	prober := newFakeProber()
	plain := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := plain.Walk(context.Background(), dir)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	assertNames(t, entries, "inner.flac", "song.flac")
	if entries[0].Path != inner || entries[1].Path != song {
		t.Errorf("paths = (%q, %q), want (%q, %q)", entries[0].Path, entries[1].Path, inner, song)
	}

	follow := New(Options{FollowSymlinks: true, AudioExtensions: []string{"flac"}}, prober)
	entries, err = follow.Walk(context.Background(), dir)
	if err != nil {
		t.Fatalf("Walk following symlinks: %v", err)
	}
	assertNames(t, entries, "inner.flac", "song.flac")
	if entries[0].Path != inner || entries[1].Path != song {
		t.Errorf("follow paths = (%q, %q), want (%q, %q)", entries[0].Path, entries[1].Path, inner, song)
	}
}

func TestWalkSkipsUnreadableSubdirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}

	dir := t.TempDir()
	good := makeDir(t, dir, "good")
	audio := writeFile(t, good, "a.flac")
	bad := makeDir(t, dir, "bad")
	writeFile(t, bad, "secret.flac")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod unreadable dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o755) })

	s := New(Options{AudioExtensions: []string{"flac"}}, newFakeProber())
	entries, err := s.Walk(context.Background(), dir)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	assertNames(t, entries, "a.flac")
	if entries[0].Path != audio {
		t.Errorf("Path = %q, want %q", entries[0].Path, audio)
	}
}

func TestReadDirUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}

	dir := makeDir(t, t.TempDir(), "locked")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	s := New(Options{AudioExtensions: []string{"flac"}}, newFakeProber())
	if _, err := s.ReadDir(context.Background(), dir); err == nil {
		t.Fatal("ReadDir succeeded on unreadable directory, want error")
	}
}

func TestReadDirUnreadableCueIsInvalid(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}

	dir := t.TempDir()
	audio := writeFile(t, dir, "album.flac")
	cuePath := writeCue(t, dir, "locked.cue", `FILE "album.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
	if err := os.Chmod(cuePath, 0o000); err != nil {
		t.Fatalf("chmod cue: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(cuePath, 0o644) })

	prober := newFakeProber().set(audio, time.Minute)
	s := New(Options{AudioExtensions: []string{"flac"}}, prober)
	entries, err := s.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	assertNames(t, entries, "album.flac", "locked.cue")
	if got := findEntry(t, entries, "locked.cue"); got.Kind != KindInvalidCue || got.Err == nil {
		t.Errorf("locked.cue = %+v, want KindInvalidCue with Err", got)
	}
}

func TestReadDirMissingDirectory(t *testing.T) {
	s := New(Options{}, newFakeProber())
	if _, err := s.ReadDir(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("ReadDir succeeded on a missing directory, want error")
	}
}

func TestContextCancellation(t *testing.T) {
	dir := t.TempDir()
	audio := writeFile(t, dir, "album.flac")
	cuePath := writeCue(t, dir, "Album.cue", `FILE "album.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`)
	prober := newFakeProber().set(audio, time.Minute)
	s := New(Options{AudioExtensions: []string{"flac"}}, prober)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.ReadDir(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadDir error = %v, want context.Canceled", err)
	}
	if _, err := s.ReadCue(ctx, cuePath); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadCue error = %v, want context.Canceled", err)
	}
	if _, err := s.Walk(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("Walk error = %v, want context.Canceled", err)
	}
	if _, err := s.ResolveCueTrack(ctx, cuePath, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("ResolveCueTrack error = %v, want context.Canceled", err)
	}
}

func TestWalkMissingRootReturnsError(t *testing.T) {
	s := New(Options{AudioExtensions: []string{"flac"}}, newFakeProber())
	if _, err := s.Walk(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("Walk succeeded on a missing root, want error")
	}
}

func TestNaturalCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"track2", "track10", -1},
		{"track10", "track2", 1},
		{"Track2", "track2", 0},
		{"track02", "track2", 0},
		{"album", "Album", 0},
		{"a2b", "a10b", -1},
		{"abc", "abcd", -1},
		{"", "a", -1},
	}
	for _, test := range tests {
		got := naturalCompare(test.a, test.b)
		if got < 0 {
			got = -1
		} else if got > 0 {
			got = 1
		}
		if got != test.want {
			t.Errorf("naturalCompare(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}

func TestNormalizeExtensions(t *testing.T) {
	got := normalizeExtensions([]string{"FLAC", ".Mp3", " ogg ", "", "."})
	want := map[string]bool{".flac": true, ".mp3": true, ".ogg": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeExtensions = %#v, want %#v", got, want)
	}
}

// Ensure the fake prober satisfies the interface at compile time.
var _ meta.Prober = (*fakeProber)(nil)
