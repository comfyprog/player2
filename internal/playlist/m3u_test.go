package playlist

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"player2/internal/queue"
)

func writeText(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustLoad(t *testing.T, path string) []Entry {
	t.Helper()
	entries, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q): %v", path, err)
	}
	return entries
}

func requireEntry(t *testing.T, got Entry, want Entry) {
	t.Helper()
	if got.Path != want.Path {
		t.Errorf("Path = %q, want %q", got.Path, want.Path)
	}
	if got.CuePath != want.CuePath {
		t.Errorf("CuePath = %q, want %q", got.CuePath, want.CuePath)
	}
	if got.CueTrack != want.CueTrack {
		t.Errorf("CueTrack = %d, want %d", got.CueTrack, want.CueTrack)
	}
	if got.Title != want.Title {
		t.Errorf("Title = %q, want %q", got.Title, want.Title)
	}
	if got.Artist != want.Artist {
		t.Errorf("Artist = %q, want %q", got.Artist, want.Artist)
	}
	if got.Duration != want.Duration {
		t.Errorf("Duration = %s, want %s", got.Duration, want.Duration)
	}
}

func TestLoadBasicMetadataAndLineEndings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.m3u")

	// UTF-8 BOM, CRLF line endings, comments, blank lines, a missing final
	// newline, and an indented comment.
	content := "\uFEFF#EXTM3U\r\n" +
		"# a comment\r\n" +
		"   # an indented comment\r\n" +
		"\r\n" +
		"#EXTINF:125,Artist One - Song One\r\n" +
		"one.mp3\r\n" +
		"#EXTINF:2.5,No Separator Song\r\n" +
		"two.mp3\r\n" +
		"#EXTINF:-1,Artist Two - \r\n" +
		"three.mp3\r\n" +
		"#EXTINF:not-a-number,Unknown Artist - Unknown Title\r\n" +
		"four.mp3\r\n" +
		"#EXTINF:10,\r\n" +
		"five.mp3"
	writeText(t, path, content)

	entries := mustLoad(t, path)
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5: %+v", len(entries), entries)
	}

	requireEntry(t, entries[0], Entry{
		Path:     filepath.Join(dir, "one.mp3"),
		Title:    "Song One",
		Artist:   "Artist One",
		Duration: 125 * time.Second,
	})
	requireEntry(t, entries[1], Entry{
		Path:     filepath.Join(dir, "two.mp3"),
		Title:    "No Separator Song",
		Duration: 2500 * time.Millisecond,
	})
	requireEntry(t, entries[2], Entry{
		Path:   filepath.Join(dir, "three.mp3"),
		Artist: "Artist Two",
	})
	requireEntry(t, entries[3], Entry{
		Path:   filepath.Join(dir, "four.mp3"),
		Title:  "Unknown Title",
		Artist: "Unknown Artist",
	})
	requireEntry(t, entries[4], Entry{
		Path:     filepath.Join(dir, "five.mp3"),
		Duration: 10 * time.Second,
	})
}

func TestLoadRelativePlaylistPath(t *testing.T) {
	root := t.TempDir()
	writeText(t, filepath.Join(root, "sub", "list.m3u"), "songs/a.mp3\n")
	t.Chdir(root)

	entries := mustLoad(t, filepath.Join("sub", "list.m3u"))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	want := filepath.Join(root, "sub", "songs", "a.mp3")
	if entries[0].Path != want {
		t.Errorf("Path = %q, want %q", entries[0].Path, want)
	}
}

func TestLoadRelativePathsAndFileURLs(t *testing.T) {
	dir := t.TempDir()
	playlistPath := filepath.Join(dir, "playlists", "list.m3u8")
	base := filepath.Dir(playlistPath)

	absolute := filepath.Join(dir, "absolute", "plain.mp3")
	encoded := filepath.Join(dir, "url media", "a b.mp3")
	localhost := filepath.Join(dir, "localhost", "local.mp3")
	windows := `C:\Music\song.mp3`

	fileURL := "file://" + strings.ReplaceAll(encoded, " ", "%20")
	content := "../media/space song.mp3\n" +
		absolute + "\n" +
		fileURL + "\n" +
		"file://localhost" + localhost + "\n" +
		windows + "\n"
	writeText(t, playlistPath, content)

	entries := mustLoad(t, playlistPath)
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5: %+v", len(entries), entries)
	}

	wants := []string{
		filepath.Join(dir, "media", "space song.mp3"),
		absolute,
		encoded,
		localhost,
		filepath.Join(base, "C:/Music/song.mp3"),
	}
	for i, want := range wants {
		clean, err := filepath.Abs(want)
		if err != nil {
			t.Fatalf("Abs(%q): %v", want, err)
		}
		if entries[i].Path != clean {
			t.Errorf("entry %d Path = %q, want %q", i, entries[i].Path, clean)
		}
		if !filepath.IsAbs(entries[i].Path) {
			t.Errorf("entry %d Path = %q, want absolute", i, entries[i].Path)
		}
	}
}

func TestLoadRejectsRemoteURLs(t *testing.T) {
	tests := []string{
		"http://example.com/song.mp3",
		"https://example.com/song.mp3",
		"ftp://example.com/song.mp3",
		"file://server/share/song.mp3",
		"file://example.com/song.mp3",
	}
	for _, line := range tests {
		t.Run(line, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "list.m3u")
			writeText(t, path, "local.mp3\n"+line+"\n")
			if _, err := Load(path); err == nil {
				t.Fatalf("Load accepted remote path %q", line)
			}
		})
	}
}

func TestLoadMissingOrUnreadableFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.m3u")); err == nil {
		t.Fatal("Load accepted a missing playlist")
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load accepted a directory as a playlist")
	}
}

func writeCue(t *testing.T, path string) {
	t.Helper()
	writeText(t, path, `PERFORMER "Album Artist"
TITLE "Album"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Two"
    INDEX 01 03:00:00
  TRACK 03 AUDIO
    TITLE "Three"
    INDEX 01 06:00:00
`)
}

func TestLoadCueExtension(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "album.flac")
	cuePath := filepath.Join(dir, "my album.cue")
	writeText(t, audio, "audio")
	writeCue(t, cuePath)

	path := filepath.Join(dir, "list.m3u")
	writeText(t, path, "#EXTM3U\n"+
		"#EXTINF:180,Album Artist - Two\n"+
		"#EXT-X-PLAYER2-CUE:cue=my%20album.cue;track=2\n"+
		"album.flac\n")

	entries := mustLoad(t, path)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	requireEntry(t, entries[0], Entry{
		Path:     audio,
		CuePath:  cuePath,
		CueTrack: 2,
		Title:    "Two",
		Artist:   "Album Artist",
		Duration: 180 * time.Second,
	})
}

func TestLoadCueFallback(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "album.flac")
	writeText(t, audio, "audio")
	writeCue(t, filepath.Join(dir, "album.cue"))
	writeText(t, filepath.Join(dir, "bad.cue"), "this is not a cue sheet\n")

	tests := []struct {
		name      string
		extension string
	}{
		{"missing cue", "#EXT-X-PLAYER2-CUE:cue=missing.cue;track=1"},
		{"unknown track", "#EXT-X-PLAYER2-CUE:cue=album.cue;track=99"},
		{"invalid track number", "#EXT-X-PLAYER2-CUE:cue=album.cue;track=abc"},
		{"zero track number", "#EXT-X-PLAYER2-CUE:cue=album.cue;track=0"},
		{"missing track key", "#EXT-X-PLAYER2-CUE:cue=album.cue"},
		{"missing cue key", "#EXT-X-PLAYER2-CUE:track=1"},
		{"missing equals", "#EXT-X-PLAYER2-CUE:cue=album.cue;track"},
		{"parse error", "#EXT-X-PLAYER2-CUE:cue=bad.cue;track=1"},
		{"remote cue", "#EXT-X-PLAYER2-CUE:cue=http://example.com/a.cue;track=1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "list.m3u")
			writeText(t, path, "#EXTINF:12,Artist - Title\n"+tc.extension+"\nalbum.flac\n")
			entries := mustLoad(t, path)
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			requireEntry(t, entries[0], Entry{
				Path:     audio,
				Title:    "Title",
				Artist:   "Artist",
				Duration: 12 * time.Second,
			})
		})
	}

	t.Run("valid extension after malformed one", func(t *testing.T) {
		path := filepath.Join(dir, "list2.m3u")
		writeText(t, path, "#EXT-X-PLAYER2-CUE:track=abc\n"+
			"#EXT-X-PLAYER2-CUE:cue=album.cue;track=1\n"+
			"album.flac\n")
		entries := mustLoad(t, path)
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(entries))
		}
		requireEntry(t, entries[0], Entry{
			Path:     audio,
			CuePath:  filepath.Join(dir, "album.cue"),
			CueTrack: 1,
		})
	})
}

func TestWriteRoundtrip(t *testing.T) {
	dir := t.TempDir()
	audio1 := filepath.Join(dir, "media dir", "tune ü 'quoted'.mp3")
	audio2 := filepath.Join(dir, "media dir", "solo.flac")
	audio3 := filepath.Join(dir, "media dir", "no metadata.ogg")
	writeText(t, audio1, "one")
	writeText(t, audio2, "two")
	writeText(t, audio3, "three")

	path := filepath.Join(dir, "out.m3u")
	items := []queue.Item{
		{ID: 1, Path: audio1, Title: "Song\nOne", Artist: "Artist", Duration: 125 * time.Second},
		{ID: 2, Path: audio2, Title: "Solo"},
		{ID: 3, Path: audio3},
	}
	if err := Write(path, items); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "#EXTM3U\n") {
		t.Errorf("playlist does not start with #EXTM3U: %q", content)
	}
	if strings.Count(content, "#EXTINF:") != 2 {
		t.Errorf("got %d EXTINF lines, want 2:\n%s", strings.Count(content, "#EXTINF:"), content)
	}
	if !strings.Contains(content, "#EXTINF:125,Artist - Song One\n") {
		t.Errorf("missing sanitized EXTINF line:\n%s", content)
	}
	if !strings.Contains(content, "#EXTINF:-1,Solo\n") {
		t.Errorf("missing unknown-duration EXTINF line:\n%s", content)
	}
	rel1, _ := filepath.Rel(dir, audio1)
	if !strings.Contains(content, rel1+"\n") {
		t.Errorf("missing relative path %q:\n%s", rel1, content)
	}
	if strings.Contains(content, audio1) {
		t.Errorf("playlist contains absolute audio path %q", audio1)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat playlist: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("playlist mode = %o, want 644", perm)
	}

	entries := mustLoad(t, path)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	requireEntry(t, entries[0], Entry{
		Path:     audio1,
		Title:    "Song One",
		Artist:   "Artist",
		Duration: 125 * time.Second,
	})
	requireEntry(t, entries[1], Entry{Path: audio2, Title: "Solo"})
	requireEntry(t, entries[2], Entry{Path: audio3})

	// Overwriting must replace the whole file, not append to it.
	if err := Write(path, []queue.Item{{Path: audio3, Title: "Only"}}); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}
	content = string(data)
	if strings.Count(content, "#EXTM3U") != 1 {
		t.Errorf("got %d header lines, want 1:\n%s", strings.Count(content, "#EXTM3U"), content)
	}
	if strings.Contains(content, filepath.Base(audio1)) {
		t.Errorf("stale entry remained after overwrite:\n%s", content)
	}
	entries = mustLoad(t, path)
	if len(entries) != 1 || entries[0].Path != audio3 || entries[0].Title != "Only" {
		t.Errorf("load after overwrite = %+v", entries)
	}
}

func TestWriteRelativePathOutsidePlaylistDirectory(t *testing.T) {
	root := t.TempDir()
	playlistDir := filepath.Join(root, "lists")
	if err := os.MkdirAll(playlistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(root, "music", "song.mp3")
	writeText(t, audio, "audio")

	path := filepath.Join(playlistDir, "list.m3u")
	if err := Write(path, []queue.Item{{Path: audio}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("..", "music", "song.mp3")
	if !strings.Contains(string(data), want+"\n") {
		t.Errorf("playlist does not contain relative path %q:\n%s", want, data)
	}
}

func TestWriteCueExtension(t *testing.T) {
	root := t.TempDir()
	playlistDir := filepath.Join(root, "lists")
	if err := os.MkdirAll(playlistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(root, "media", "album.flac")
	cuePath := filepath.Join(root, "media", "my album.cue")
	writeText(t, audio, "audio")
	writeCue(t, cuePath)

	path := filepath.Join(playlistDir, "list.m3u")
	items := []queue.Item{{
		Path:     audio,
		CuePath:  cuePath,
		CueTrack: 3,
		Title:    "Three",
		Artist:   "Album Artist",
		Duration: 200 * time.Second,
	}}
	if err := Write(path, items); err != nil {
		t.Fatalf("Write: %v", err)
	}

	relCue, err := filepath.Rel(playlistDir, cuePath)
	if err != nil {
		t.Fatal(err)
	}
	wantLine := "#EXT-X-PLAYER2-CUE:cue=" + url.PathEscape(relCue) + ";track=3"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), wantLine+"\n") {
		t.Errorf("playlist missing CUE line %q:\n%s", wantLine, data)
	}

	entries := mustLoad(t, path)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	requireEntry(t, entries[0], Entry{
		Path:     audio,
		CuePath:  cuePath,
		CueTrack: 3,
		Title:    "Three",
		Artist:   "Album Artist",
		Duration: 200 * time.Second,
	})
}

func TestWriteTruncatesDuration(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.mp3")
	writeText(t, audio, "audio")
	path := filepath.Join(dir, "list.m3u")
	items := []queue.Item{{
		Path:     audio,
		Title:    "Song",
		Duration: 2*time.Second + 900*time.Millisecond,
	}}
	if err := Write(path, items); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "#EXTINF:2,Song\n") {
		t.Errorf("duration was not truncated to whole seconds:\n%s", data)
	}
}

func TestWriteRejectsNewlinePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.m3u")
	original := "#EXTM3U\nold entry\n"
	writeText(t, path, original)

	tests := []queue.Item{
		{Path: "bad\nname.mp3"},
		{Path: "bad\rname.mp3"},
		{Path: "ok.mp3", CuePath: "bad\ncue.cue"},
	}
	for _, item := range tests {
		if err := Write(path, []queue.Item{item}); err == nil {
			t.Fatalf("Write accepted a path with a newline: %+v", item)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != original {
			t.Errorf("playlist changed after failed Write:\n got %q\nwant %q", data, original)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "list.m3u" {
			t.Errorf("temporary files left behind: %v", entryNames(entries))
		}
	}
}

func TestWriteEmptyPlaylist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.m3u")
	if err := Write(path, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#EXTM3U\n" {
		t.Errorf("empty playlist = %q, want %q", data, "#EXTM3U\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("playlist mode = %o, want 644", perm)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

func TestWriteRoundtripPreservesTrickyRelativeNames(t *testing.T) {
	dir := t.TempDir()
	names := []string{"#song.flac", " leading.flac", "trailing.flac "}
	items := make([]queue.Item, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		writeText(t, path, "audio")
		items = append(items, queue.Item{Path: path})
	}

	playlistPath := filepath.Join(dir, "list.m3u")
	if err := Write(playlistPath, items); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		want := "./" + name
		if !strings.Contains(string(data), want+"\n") {
			t.Errorf("playlist missing %q line:\n%s", want, data)
		}
	}

	entries := mustLoad(t, playlistPath)
	if len(entries) != len(names) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(names), entries)
	}
	for i, name := range names {
		want := filepath.Join(dir, name)
		if entries[i].Path != want {
			t.Errorf("entry %d Path = %q, want %q", i, entries[i].Path, want)
		}
	}
}

func TestLoadPreservesPathWhitespaceAndHashNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.m3u")
	writeText(t, path, "#EXTM3U\n./#song.flac\n leading.flac\ntrailing.flac \n")

	entries := mustLoad(t, path)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	wants := []string{
		filepath.Join(dir, "#song.flac"),
		filepath.Join(dir, " leading.flac"),
		filepath.Join(dir, "trailing.flac "),
	}
	for i, want := range wants {
		if entries[i].Path != want {
			t.Errorf("entry %d Path = %q, want %q", i, entries[i].Path, want)
		}
	}
}
