package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// setHome points HOME at a fresh temp directory and clears XDG overrides so
// tests exercise the fallback layout.
func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	return home
}

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.StartDir != "~/Music" {
		t.Errorf("StartDir = %q, want %q", cfg.StartDir, "~/Music")
	}
	if cfg.ShowHidden {
		t.Error("ShowHidden = true, want false")
	}
	if cfg.FollowSymlinks {
		t.Error("FollowSymlinks = true, want false")
	}
	if !cfg.RestoreQueue {
		t.Error("RestoreQueue = false, want true")
	}
	if cfg.Volume != 70 {
		t.Errorf("Volume = %d, want 70", cfg.Volume)
	}
	if cfg.SampleRate != 48000 {
		t.Errorf("SampleRate = %d, want 48000", cfg.SampleRate)
	}
	if cfg.BufferSeconds != 2 {
		t.Errorf("BufferSeconds = %d, want 2", cfg.BufferSeconds)
	}
	if cfg.FFmpegBin != "ffmpeg" {
		t.Errorf("FFmpegBin = %q, want %q", cfg.FFmpegBin, "ffmpeg")
	}
	if cfg.FFprobeBin != "ffprobe" {
		t.Errorf("FFprobeBin = %q, want %q", cfg.FFprobeBin, "ffprobe")
	}
	if cfg.CueFallbackEncoding != "windows-1252" {
		t.Errorf("CueFallbackEncoding = %q, want %q", cfg.CueFallbackEncoding, "windows-1252")
	}
	if len(cfg.ExtraAudioExtensions) != 0 {
		t.Errorf("ExtraAudioExtensions = %v, want empty", cfg.ExtraAudioExtensions)
	}

	wantColors := Colors{Accent: "39", Dir: "33", Cue: "141", Playing: "46", Error: "196"}
	if cfg.Colors != wantColors {
		t.Errorf("Colors = %+v, want %+v", cfg.Colors, wantColors)
	}

	wantKeys := map[string][]string{
		"quit":          {"q", "ctrl+c"},
		"help":          {"?"},
		"switch_pane":   {"tab"},
		"open":          {"enter"},
		"parent":        {"backspace"},
		"up":            {"up", "k"},
		"down":          {"down", "j"},
		"page_up":       {"pgup"},
		"page_down":     {"pgdown"},
		"first":         {"home"},
		"last":          {"end"},
		"add":           {"a"},
		"add_all":       {"A"},
		"remove":        {"d"},
		"move_up":       {"K"},
		"move_down":     {"J"},
		"pause":         {"space"},
		"stop":          {"x"},
		"next":          {"n"},
		"previous":      {"b"},
		"seek_back":     {"left"},
		"seek_forward":  {"right"},
		"volume_up":     {"+", "="},
		"volume_down":   {"-", "_"},
		"mute":          {"m"},
		"shuffle":       {"s"},
		"repeat":        {"r"},
		"search":        {"/"},
		"save_playlist": {"w"},
		"rescan":        {"R"},
		"clear":         {"c"},
		"goto_playing":  {"g"},
	}
	if !reflect.DeepEqual(cfg.Keys, wantKeys) {
		t.Errorf("Keys mismatch:\n got %#v\nwant %#v", cfg.Keys, wantKeys)
	}
}

func TestDefaultReturnsIndependentMaps(t *testing.T) {
	first := Default()
	first.Keys["quit"][0] = "changed"

	second := Default()
	if got := second.Keys["quit"][0]; got != "q" {
		t.Fatalf("Default shares key slice state: quit[0] = %q, want %q", got, "q")
	}
}

func TestLoadOverlaysFileOnDefaults(t *testing.T) {
	setHome(t)
	path := writeConfig(t, t.TempDir(), `
start_dir = "/music"
show_hidden = true
volume = 42
extra_audio_extensions = [".wma", ".ape"]

[colors]
accent = "99"

[keys]
pause = ["p"]
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StartDir != "/music" {
		t.Errorf("StartDir = %q, want /music", cfg.StartDir)
	}
	if !cfg.ShowHidden {
		t.Error("ShowHidden = false, want true")
	}
	if cfg.Volume != 42 {
		t.Errorf("Volume = %d, want 42", cfg.Volume)
	}
	if cfg.SampleRate != 48000 {
		t.Errorf("SampleRate = %d, want default 48000", cfg.SampleRate)
	}
	if cfg.FFprobeBin != "ffprobe" {
		t.Errorf("FFprobeBin = %q, want default ffprobe", cfg.FFprobeBin)
	}
	if !reflect.DeepEqual(cfg.ExtraAudioExtensions, []string{".wma", ".ape"}) {
		t.Errorf("ExtraAudioExtensions = %v", cfg.ExtraAudioExtensions)
	}
	if cfg.Colors.Accent != "99" {
		t.Errorf("Colors.Accent = %q, want 99", cfg.Colors.Accent)
	}
	if cfg.Colors.Dir != "33" {
		t.Errorf("Colors.Dir = %q, want default 33", cfg.Colors.Dir)
	}
	if !reflect.DeepEqual(cfg.Keys["pause"], []string{"p"}) {
		t.Errorf("Keys[pause] = %v, want [p]", cfg.Keys["pause"])
	}
	if !reflect.DeepEqual(cfg.Keys["quit"], []string{"q", "ctrl+c"}) {
		t.Errorf("Keys[quit] = %v, want defaults", cfg.Keys["quit"])
	}
	if !reflect.DeepEqual(cfg.Keys["next"], []string{"n"}) {
		t.Errorf("Keys[next] = %v, want defaults", cfg.Keys["next"])
	}
}

func TestLoadMissingDefaultFileUsesDefaults(t *testing.T) {
	home := setHome(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	want.StartDir = filepath.Join(home, "Music")
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("missing default config:\n got %#v\nwant %#v", cfg, want)
	}
}

func TestLoadDefaultFileHonorsXDGConfigHome(t *testing.T) {
	setHome(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	writeConfig(t, filepath.Join(configHome, "player2"), "volume = 11\n")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Volume != 11 {
		t.Errorf("Volume = %d, want 11 from %s", cfg.Volume, filepath.Join(configHome, "player2", "config.toml"))
	}
}

func TestLoadDefaultPathFromHomeConfig(t *testing.T) {
	home := setHome(t)
	writeConfig(t, filepath.Join(home, ".config", "player2"), "volume = 12\n")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Volume != 12 {
		t.Errorf("Volume = %d, want 12", cfg.Volume)
	}
}

func TestLoadExplicitMissingFileIsError(t *testing.T) {
	setHome(t)
	path := filepath.Join(t.TempDir(), "does-not-exist.toml")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing explicit config")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error does not wrap os.ErrNotExist: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error does not mention path %q: %v", path, err)
	}
}

func TestLoadUnknownTopLevelKey(t *testing.T) {
	setHome(t)
	path := writeConfig(t, t.TempDir(), "volum = 50\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown top-level key")
	}
	if !strings.Contains(err.Error(), "volum") {
		t.Errorf("error does not name unknown key: %v", err)
	}
}

func TestLoadUnknownColorKey(t *testing.T) {
	setHome(t)
	path := writeConfig(t, t.TempDir(), "[colors]\nbackground = \"1\"\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown color key")
	}
	if !strings.Contains(err.Error(), "background") {
		t.Errorf("error does not name unknown color key: %v", err)
	}
}

func TestLoadUnknownKeyAction(t *testing.T) {
	setHome(t)
	path := writeConfig(t, t.TempDir(), "[keys]\nbogus_action = [\"z\"]\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown key action")
	}
	if !strings.Contains(err.Error(), "bogus_action") {
		t.Errorf("error does not name unknown action: %v", err)
	}
}

func TestLoadExpandsTilde(t *testing.T) {
	home := setHome(t)
	path := writeConfig(t, t.TempDir(), `start_dir = "~/Music"`+"\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home, "Music"); cfg.StartDir != want {
		t.Errorf("StartDir = %q, want %q", cfg.StartDir, want)
	}
}

func TestLoadExpandsEnvironmentVariables(t *testing.T) {
	setHome(t)
	t.Setenv("PLAYER2_TEST_MUSIC", "/srv/music")
	path := writeConfig(t, t.TempDir(), `start_dir = "${PLAYER2_TEST_MUSIC}/Albums"`+"\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "/srv/music/Albums"; cfg.StartDir != want {
		t.Errorf("StartDir = %q, want %q", cfg.StartDir, want)
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"volume negative", "volume = -1\n", "volume"},
		{"volume too high", "volume = 101\n", "volume"},
		{"sample rate zero", "sample_rate = 0\n", "sample_rate"},
		{"sample rate negative", "sample_rate = -48000\n", "sample_rate"},
		{"buffer zero", "buffer_seconds = 0\n", "buffer_seconds"},
		{"buffer negative", "buffer_seconds = -1\n", "buffer_seconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setHome(t)
			path := writeConfig(t, t.TempDir(), tt.content)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for %s", tt.content)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error does not mention %q: %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadAllowsVolumeBoundaries(t *testing.T) {
	for _, volume := range []string{"0", "100"} {
		t.Run("volume "+volume, func(t *testing.T) {
			setHome(t)
			path := writeConfig(t, t.TempDir(), "volume = "+volume+"\n")
			if _, err := Load(path); err != nil {
				t.Fatalf("Load: %v", err)
			}
		})
	}
}

func TestLoadMalformedTOML(t *testing.T) {
	setHome(t)
	path := writeConfig(t, t.TempDir(), "volume = [\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error does not mention config path: %v", err)
	}
}
