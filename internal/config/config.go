// Package config loads and validates player2's TOML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"player2/internal/xdg"
)

// Config is the complete player2 configuration. TOML keys use the snake_case
// names in their struct tags.
type Config struct {
	StartDir             string              `toml:"start_dir"`
	ShowHidden           bool                `toml:"show_hidden"`
	FollowSymlinks       bool                `toml:"follow_symlinks"`
	RestoreQueue         bool                `toml:"restore_queue"`
	Volume               int                 `toml:"volume"`
	SampleRate           int                 `toml:"sample_rate"`
	BufferSeconds        int                 `toml:"buffer_seconds"`
	FFmpegBin            string              `toml:"ffmpeg_bin"`
	FFprobeBin           string              `toml:"ffprobe_bin"`
	CueFallbackEncoding  string              `toml:"cue_fallback_encoding"`
	ExtraAudioExtensions []string            `toml:"extra_audio_extensions"`
	Colors               Colors              `toml:"colors"`
	Keys                 map[string][]string `toml:"keys"`
}

// Colors holds the configurable UI colors. Values are ANSI 256-color indexes.
type Colors struct {
	Accent  string `toml:"accent"`
	Dir     string `toml:"dir"`
	Cue     string `toml:"cue"`
	Playing string `toml:"playing"`
	Error   string `toml:"error"`
}

// keyActions is the closed vocabulary of action names accepted in [keys].
var keyActions = map[string]struct{}{
	"quit":          {},
	"help":          {},
	"switch_pane":   {},
	"open":          {},
	"parent":        {},
	"up":            {},
	"down":          {},
	"page_up":       {},
	"page_down":     {},
	"first":         {},
	"last":          {},
	"add":           {},
	"add_all":       {},
	"remove":        {},
	"move_up":       {},
	"move_down":     {},
	"pause":         {},
	"stop":          {},
	"next":          {},
	"previous":      {},
	"seek_back":     {},
	"seek_forward":  {},
	"volume_up":     {},
	"volume_down":   {},
	"mute":          {},
	"shuffle":       {},
	"repeat":        {},
	"search":        {},
	"save_playlist": {},
	"rescan":        {},
	"clear":         {},
	"goto_playing":  {},
}

// Default returns the built-in configuration. StartDir is returned unexpanded;
// Load expands it once a configuration has been loaded.
func Default() Config {
	return Config{
		StartDir:             "~/Music",
		ShowHidden:           false,
		FollowSymlinks:       false,
		RestoreQueue:         true,
		Volume:               70,
		SampleRate:           48000,
		BufferSeconds:        2,
		FFmpegBin:            "ffmpeg",
		FFprobeBin:           "ffprobe",
		CueFallbackEncoding:  "windows-1252",
		ExtraAudioExtensions: []string{},
		Colors: Colors{
			Accent:  "39",
			Dir:     "33",
			Cue:     "141",
			Playing: "46",
			Error:   "196",
		},
		Keys: defaultKeys(),
	}
}

// defaultKeys returns a fresh map so callers cannot mutate shared state.
func defaultKeys() map[string][]string {
	return map[string][]string{
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
}

// Load reads the configuration at path and overlays it on Default.
//
// An empty path loads the default location,
// <ConfigDir>/config.toml; if that file does not exist, defaults are returned
// without an error. A non-empty path that does not exist is an error.
func Load(path string) (Config, error) {
	explicit := path != ""
	if !explicit {
		dir, err := xdg.ConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("locate default config path: %w", err)
		}
		path = filepath.Join(dir, "config.toml")
	}

	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return finish(cfg)
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	// Decode keys separately so unspecified actions keep their defaults.
	cfg.Keys = nil
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return Config{}, fmt.Errorf("parse config %s: %s", path, unknownKeys(strict))
		}
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	mergedKeys, err := mergeKeys(cfg.Keys)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.Keys = mergedKeys

	return finish(cfg)
}

// finish expands StartDir and validates the configuration.
func finish(cfg Config) (Config, error) {
	expanded, err := expandPath(cfg.StartDir)
	if err != nil {
		return Config{}, fmt.Errorf("expand start_dir: %w", err)
	}
	cfg.StartDir = expanded

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// unknownKeys formats the keys reported by go-toml's strict-mode error.
func unknownKeys(err *toml.StrictMissingError) string {
	names := make([]string, 0, len(err.Errors))
	for i := range err.Errors {
		names = append(names, fmt.Sprintf("%q", strings.Join(err.Errors[i].Key(), ".")))
	}
	sort.Strings(names)
	return "unknown config key(s): " + strings.Join(names, ", ")
}

// mergeKeys overlays user-supplied bindings on the default bindings and
// rejects unknown action names.
func mergeKeys(user map[string][]string) (map[string][]string, error) {
	merged := defaultKeys()

	var unknown []string
	for action, keys := range user {
		if _, ok := keyActions[action]; !ok {
			unknown = append(unknown, action)
			continue
		}
		merged[action] = append([]string(nil), keys...)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		quoted := make([]string, len(unknown))
		for i, action := range unknown {
			quoted[i] = fmt.Sprintf("%q", action)
		}
		return nil, fmt.Errorf("unknown key action(s) %s in [keys]", strings.Join(quoted, ", "))
	}
	return merged, nil
}

// expandPath expands leading ~ and environment variables in path.
func expandPath(path string) (string, error) {
	expanded := os.ExpandEnv(path)
	if expanded != "~" && !strings.HasPrefix(expanded, "~/") {
		return expanded, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if expanded == "~" {
		return home, nil
	}
	return filepath.Join(home, expanded[2:]), nil
}

func (c Config) validate() error {
	if c.Volume < 0 || c.Volume > 100 {
		return fmt.Errorf("invalid volume %d: must be between 0 and 100", c.Volume)
	}
	if c.SampleRate <= 0 {
		return fmt.Errorf("invalid sample_rate %d: must be greater than 0", c.SampleRate)
	}
	if c.BufferSeconds <= 0 {
		return fmt.Errorf("invalid buffer_seconds %d: must be greater than 0", c.BufferSeconds)
	}
	return nil
}
