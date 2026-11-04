package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"player2/internal/config"
	"player2/internal/library"
	"player2/internal/queue"
)

func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

func typeText(t *testing.T, m *Model, text string) *Model {
	t.Helper()
	for _, r := range text {
		m = press(t, m, runeKey(r))
	}
	return m
}

func newSaveModel(t *testing.T, dir string) (*Model, *fakePlayer) {
	t.Helper()
	fp := newFakePlayer()
	m := newTestModel(t, func(opts *Options) {
		opts.Player = fp
		opts.StartDir = dir
	})
	m = resize(t, m, 120, 30)
	return m, fp
}

func queueItemAt(dir, name string) queue.Item {
	return queue.Item{
		Path:     filepath.Join(dir, name),
		Title:    strings.TrimSuffix(name, filepath.Ext(name)),
		Duration: 0,
	}
}

func TestSavePromptWritesDefaultAndSupportsEditing(t *testing.T) {
	dir := t.TempDir()
	m, _ := newSaveModel(t, dir)
	m.queue.Append(queueItemAt(dir, "one.flac"))

	m = press(t, m, runeKey('w'))
	if m.prompt.mode != promptSavePlaylist {
		t.Fatalf("prompt mode = %v after w, want save playlist", m.prompt.mode)
	}
	if got := m.prompt.input.Value(); got != "" {
		t.Fatalf("default input = %q, want empty (default is a placeholder)", got)
	}
	if got := m.prompt.input.Placeholder; got != "playlist.m3u" {
		t.Fatalf("placeholder = %q, want playlist.m3u", got)
	}

	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"Save playlist", dir, "Enter save", "Esc cancel"} {
		if !strings.Contains(plain, want) {
			t.Errorf("save prompt missing %q:\n%s", want, plain)
		}
	}

	m = press(t, m, ctrlKey('u'))
	if got := m.prompt.input.Value(); got != "" {
		t.Fatalf("input after ctrl+u = %q, want empty", got)
	}
	m = typeText(t, m, "custom.m3u")
	if got := m.prompt.input.Value(); got != "custom.m3u" {
		t.Fatalf("typed input = %q, want custom.m3u", got)
	}

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	if m.prompt.mode != promptNone {
		t.Error("prompt remained open after submit")
	}
	if cmd == nil {
		t.Fatal("submit returned no save command")
	}
	m = runCmd(t, m, cmd)

	target := filepath.Join(dir, "custom.m3u")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("custom playlist was not written: %v", err)
	}
	if !strings.Contains(m.statusMsg, "saved 1 item(s) to") {
		t.Errorf("status = %q, want save success", m.statusMsg)
	}
}

func TestSavePromptOpensFromEitherPane(t *testing.T) {
	dir := t.TempDir()
	m, _ := newSaveModel(t, dir)
	m.focus = PanePlaylist

	m = press(t, m, runeKey('w'))
	if m.prompt.mode != promptSavePlaylist {
		t.Errorf("mode = %v from the playlist pane, want save playlist", m.prompt.mode)
	}
}

func TestSaveDirectoryUsesBrowserFileDirectory(t *testing.T) {
	dir := t.TempDir()
	file := writeTestFile(t, dir, "song.flac", "")
	m, _ := newSaveModel(t, dir)

	m.browserPath = file
	m.browserCue = false
	if got := m.saveDirectory(); got != dir {
		t.Errorf("saveDirectory for a file = %q, want %q", got, dir)
	}

	cuePath := filepath.Join(dir, "album.cue")
	m.browserPath = cuePath
	m.browserCue = true
	if got := m.saveDirectory(); got != dir {
		t.Errorf("saveDirectory for a CUE file = %q, want %q", got, dir)
	}
}

func TestSavePromptEmptyInputUsesDefault(t *testing.T) {
	dir := t.TempDir()
	m, _ := newSaveModel(t, dir)

	m = press(t, m, runeKey('w'))
	m = press(t, m, ctrlKey('u'))
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("empty submit returned no save command")
	}
	m = runCmd(t, m, cmd)

	path := filepath.Join(dir, "playlist.m3u")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("default playlist was not written: %v", err)
	}
	if !strings.HasPrefix(string(data), "#EXTM3U") {
		t.Errorf("empty queue playlist = %q, want an #EXTM3U header", string(data))
	}
}

func TestSavePromptEscCancelsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	m, _ := newSaveModel(t, dir)

	m = press(t, m, runeKey('w'))
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEscape))
	if cmd != nil {
		t.Errorf("cancel returned a command")
	}
	if m.prompt.mode != promptNone {
		t.Error("prompt stayed open after Esc")
	}
	if _, err := os.Stat(filepath.Join(dir, "playlist.m3u")); !os.IsNotExist(err) {
		t.Errorf("Esc wrote a playlist (stat err = %v)", err)
	}
}

func TestSavePromptRelativeAndAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	m, _ := newSaveModel(t, dir)
	m.queue.Append(queueItemAt(dir, "one.flac"))

	// Relative paths resolve against the browser directory.
	m = press(t, m, runeKey('w'))
	m = press(t, m, ctrlKey('u'))
	m = typeText(t, m, "nested.m3u")
	_, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("relative save returned no command")
	}
	m = runCmd(t, m, cmd)
	if _, err := os.Stat(filepath.Join(dir, "nested.m3u")); err != nil {
		t.Fatalf("relative playlist missing: %v", err)
	}

	// Absolute paths are used as given.
	other := t.TempDir()
	target := filepath.Join(other, "absolute.m3u")
	m = press(t, m, runeKey('w'))
	m = press(t, m, ctrlKey('u'))
	m = typeText(t, m, target)
	_, cmd = pressForCmd(t, m, specialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("absolute save returned no command")
	}
	m = runCmd(t, m, cmd)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("absolute playlist missing: %v", err)
	}
	if !strings.Contains(m.statusMsg, "saved 1 item(s) to") {
		t.Errorf("status = %q, want save success", m.statusMsg)
	}

	// A write failure becomes an error status.
	m = press(t, m, runeKey('w'))
	m = press(t, m, ctrlKey('u'))
	m = typeText(t, m, filepath.Join("missing", "list.m3u"))
	_, cmd = pressForCmd(t, m, specialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("failing save returned no command")
	}
	m = runCmd(t, m, cmd)
	if !m.statusIsError {
		t.Errorf("status = %q, want an error", m.statusMsg)
	}
}

func TestSavePromptRejectsExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	m, _ := newSaveModel(t, dir)

	m = press(t, m, runeKey('w'))
	m = press(t, m, ctrlKey('u'))
	m = typeText(t, m, ".")
	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))

	if cmd != nil {
		t.Error("directory target returned a write command")
	}
	if m.prompt.mode != promptSavePlaylist {
		t.Error("prompt closed after a directory target")
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "directory") {
		t.Errorf("status = %q (error=%v), want a directory error", m.statusMsg, m.statusIsError)
	}
}

func TestSavePromptOverwriteConfirmation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "playlist.m3u")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("n cancels", func(t *testing.T) {
		m, _ := newSaveModel(t, dir)
		m.queue.Append(queueItemAt(dir, "one.flac"))

		m = press(t, m, runeKey('w'))
		m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
		if cmd != nil {
			t.Fatal("overwrite prompt wrote before confirmation")
		}
		if m.prompt.mode != promptConfirmOverwrite {
			t.Fatalf("mode = %v, want overwrite confirmation", m.prompt.mode)
		}
		if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "Overwrite") {
			t.Errorf("confirmation view missing Overwrite:\n%s", plain)
		}
		m = press(t, m, runeKey('n'))
		if m.prompt.mode != promptNone {
			t.Error("prompt stayed open after n")
		}
		data, _ := os.ReadFile(path)
		if string(data) != "old" {
			t.Errorf("file = %q after n, want old", data)
		}
	})

	t.Run("esc cancels", func(t *testing.T) {
		m, _ := newSaveModel(t, dir)
		m = press(t, m, runeKey('w'))
		m = press(t, m, specialKey(tea.KeyEnter))
		m = press(t, m, specialKey(tea.KeyEscape))
		if m.prompt.mode != promptNone {
			t.Error("prompt stayed open after Esc")
		}
		data, _ := os.ReadFile(path)
		if string(data) != "old" {
			t.Errorf("file = %q after Esc, want old", data)
		}
	})

	t.Run("y overwrites", func(t *testing.T) {
		m, _ := newSaveModel(t, dir)
		m.queue.Append(queueItemAt(dir, "one.flac"))
		m = press(t, m, runeKey('w'))
		m = press(t, m, specialKey(tea.KeyEnter))
		m, cmd := pressForCmd(t, m, runeKey('y'))
		if cmd == nil {
			t.Fatal("confirmation returned no save command")
		}
		m = runCmd(t, m, cmd)
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), "one.flac") {
			t.Errorf("file after y = %q, want the queue item", data)
		}
	})

	t.Run("Enter cancels", func(t *testing.T) {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := newSaveModel(t, dir)
		m.queue.Append(queueItemAt(dir, "two.flac"))
		m = press(t, m, runeKey('w'))
		m = press(t, m, specialKey(tea.KeyEnter))
		m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
		if cmd != nil {
			t.Fatal("Enter confirmed an overwrite; only y/Y should confirm")
		}
		if m.prompt.mode != promptNone {
			t.Error("prompt stayed open after Enter")
		}
		data, _ := os.ReadFile(path)
		if !bytes.Equal(data, before) {
			t.Errorf("file after Enter = %q, want unchanged %q", data, before)
		}
	})
}

func TestClearConfirmation(t *testing.T) {
	t.Run("n leaves the queue", func(t *testing.T) {
		m, fp := newPlaylistModel(t, nil)
		m.queue.Append(playlistItem("One", "", 1, 0), playlistItem("Two", "", 2, 0))

		m = press(t, m, runeKey('c'))
		if m.prompt.mode != promptConfirmClear {
			t.Fatalf("mode = %v, want clear confirmation", m.prompt.mode)
		}
		if got := len(m.queue.Snapshot().Items); got != 2 {
			t.Fatalf("queue length = %d while confirming, want 2", got)
		}
		if fp.stops != 0 {
			t.Errorf("stops = %d while confirming, want 0", fp.stops)
		}
		if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "Clear queue? [y/N]") {
			t.Errorf("confirmation view missing message:\n%s", plain)
		}

		m = press(t, m, runeKey('n'))
		if m.prompt.mode != promptNone {
			t.Error("prompt stayed open after n")
		}
		if got := len(m.queue.Snapshot().Items); got != 2 {
			t.Errorf("queue length = %d after n, want 2", got)
		}
	})

	t.Run("esc leaves the queue", func(t *testing.T) {
		m, _ := newPlaylistModel(t, nil)
		m.queue.Append(playlistItem("One", "", 1, 0))
		m = press(t, m, runeKey('c'))
		m = press(t, m, specialKey(tea.KeyEscape))
		if got := len(m.queue.Snapshot().Items); got != 1 {
			t.Errorf("queue length = %d after Esc, want 1", got)
		}
	})

	t.Run("Enter cancels", func(t *testing.T) {
		m, fp := newPlaylistModel(t, nil)
		m.queue.Append(playlistItem("One", "", 1, 0), playlistItem("Two", "", 2, 0))

		m = press(t, m, runeKey('c'))
		m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
		if cmd != nil {
			t.Fatal("Enter cleared the queue; only y/Y should confirm")
		}
		if m.prompt.mode != promptNone {
			t.Error("prompt stayed open after Enter")
		}
		if got := len(m.queue.Snapshot().Items); got != 2 {
			t.Errorf("queue length = %d after Enter, want 2", got)
		}
		if fp.stops != 0 {
			t.Errorf("stops = %d after Enter, want 0", fp.stops)
		}
	})

	t.Run("y clears, stops, and persists", func(t *testing.T) {
		m, fp := newPlaylistModel(t, nil)
		store := m.store.(*fakeStore)
		m.queue.Append(playlistItem("One", "", 1, 0), playlistItem("Two", "", 2, 0))

		m = press(t, m, runeKey('c'))
		m, cmd := pressForCmd(t, m, runeKey('y'))
		if cmd == nil {
			t.Fatal("clear confirmation returned no command")
		}
		m = runCmd(t, m, cmd)
		if got := len(m.queue.Snapshot().Items); got != 0 {
			t.Errorf("queue length = %d, want 0", got)
		}
		if fp.stops != 1 {
			t.Errorf("stops = %d, want 1", fp.stops)
		}
		if store.saveCount() == 0 {
			t.Error("cleared queue was not persisted")
		}
	})

	t.Run("empty queue has no modal", func(t *testing.T) {
		m, _ := newPlaylistModel(t, nil)
		m = press(t, m, runeKey('c'))
		if m.prompt.mode != promptNone {
			t.Error("empty clear opened a confirmation")
		}
		if !strings.Contains(m.statusMsg, "queue is empty") {
			t.Errorf("status = %q, want queue is empty", m.statusMsg)
		}
	})
}

func TestPromptSwallowsNormalKeysAndQuitsOnCtrlC(t *testing.T) {
	dir := t.TempDir()
	m, fp := newSaveModel(t, dir)

	m = press(t, m, runeKey('w'))
	m = press(t, m, runeKey('x'))
	m = press(t, m, runeKey('?'))
	m = press(t, m, runeKey('q'))

	if m.help {
		t.Error("? opened help while the prompt was active")
	}
	if fp.stops != 0 {
		t.Error("x stopped playback while the prompt was active")
	}
	if got := m.prompt.input.Value(); got != "x?q" {
		t.Errorf("input = %q, want only the keystrokes typed into the prompt", got)
	}

	m, cmd := pressForCmd(t, m, ctrlKey('c'))
	if cmd == nil {
		t.Fatal("ctrl+c in prompt returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c returned %T, want tea.QuitMsg", cmd())
	}
}

func TestConfirmSwallowsOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "playlist.m3u")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := newSaveModel(t, dir)
	m.queue.Append(queueItemAt(dir, "one.flac"))

	m = press(t, m, runeKey('w'))
	m = press(t, m, specialKey(tea.KeyEnter))
	m = press(t, m, runeKey('x'))

	if m.prompt.mode != promptConfirmOverwrite {
		t.Error("unrelated key closed the confirmation")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old" {
		t.Errorf("file = %q after an unrelated key, want old", data)
	}
}

func TestBrowserEnterPlaylistStopsBeforeLoading(t *testing.T) {
	dir := t.TempDir()
	audio := writeTestFile(t, dir, "one.flac", "")
	playlistPath := writeTestFile(t, dir, "list.m3u", "#EXTM3U\n#EXTINF:60,Artist - Song\none.flac\n")
	sc := &testScanner{}
	m, fp := newBrowserModel(t, sc)
	m.browserEntries = []library.Entry{{
		Kind: library.KindPlaylist, Name: "list.m3u", Path: playlistPath,
	}}

	m, cmd := pressForCmd(t, m, specialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("opening a playlist returned no command")
	}
	// Stop is serialized and runs as a command rather than inside Update.
	m = applyBrowserFollow(t, m, cmd)
	if fp.stops != 1 {
		t.Errorf("stops = %d when opening a playlist, want 1", fp.stops)
	}

	items := m.queue.Snapshot().Items
	if len(items) != 1 || items[0].Path != audio {
		t.Fatalf("queue items = %#v, want the playlist item", items)
	}
	if len(fp.played) != 1 || fp.played[0].Path != audio {
		t.Errorf("played = %#v, want the first playlist item", fp.played)
	}
	if !strings.Contains(m.statusMsg, "playing 1 item(s)") {
		t.Errorf("status = %q, want playing status", m.statusMsg)
	}
}

func TestHelpShowsCustomConfiguredBinding(t *testing.T) {
	m := resize(t, newTestModel(t, func(opts *Options) {
		opts.Config.Keys = map[string][]string{"quit": {"Q"}}
	}), 100, 30)

	m = press(t, m, runeKey('?'))
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "Q  Quit") {
		t.Errorf("help missing custom quit binding:\n%s", plain)
	}
	if !m.help {
		t.Fatal("help did not open")
	}

	m = press(t, m, specialKey(tea.KeyEscape))
	if m.help {
		t.Error("Esc did not close help")
	}
	_, cmd := pressForCmd(t, m, runeKey('Q'))
	if cmd == nil {
		t.Fatal("custom quit key returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("custom quit key returned %T, want tea.QuitMsg", cmd())
	}
}

func TestValidateRejectsInvalidKeyConfig(t *testing.T) {
	valid := baseOptions()
	if err := Validate(valid); err != nil {
		t.Fatalf("Validate rejected defaults: %v", err)
	}

	custom := baseOptions()
	custom.Config = config.Default()
	custom.Config.Keys = map[string][]string{"quit": {"Q"}, "pause": {"p"}}
	if err := Validate(custom); err != nil {
		t.Fatalf("Validate rejected a valid custom keymap: %v", err)
	}

	tests := []struct {
		name string
		keys map[string][]string
		want []string
	}{
		{
			name: "duplicate within action",
			keys: map[string][]string{"quit": {"q", "q"}},
			want: []string{"duplicate", `"q"`},
		},
		{
			name: "duplicate across actions",
			keys: map[string][]string{"help": {"q"}},
			want: []string{"duplicate", `"q"`},
		},
		{
			name: "unknown action",
			keys: map[string][]string{"bogus": {"b"}},
			want: []string{"unknown action", `"bogus"`},
		},
		{
			name: "empty key",
			keys: map[string][]string{"quit": {""}},
			want: []string{"empty key", `"quit"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := baseOptions()
			opts.Config = config.Default()
			opts.Config.Keys = tc.keys
			err := Validate(opts)
			if err == nil {
				t.Fatal("Validate accepted invalid key config")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestPromptResizeKeepsInputAndModalWidthSafe(t *testing.T) {
	m, _ := newSaveModel(t, t.TempDir())
	m = press(t, m, runeKey('w'))

	if got, want := m.prompt.input.Width(), m.width-2; got != want {
		t.Errorf("input width = %d, want %d", got, want)
	}

	m = resize(t, m, 60, 10)
	if got, want := m.prompt.input.Width(), m.width-2; got != want {
		t.Errorf("resized input width = %d, want %d", got, want)
	}

	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != 10 {
		t.Fatalf("line count = %d, want 10", len(lines))
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != 60 {
			t.Errorf("line %d width = %d, want 60: %q", i, got, ansi.Strip(line))
		}
	}
}
