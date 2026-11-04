// Command player2 is a filesystem-first terminal music player.
//
// It browses a local directory tree, builds a playback queue, plays files and
// CUE tracks through FFmpeg, and renders the interface with Bubble Tea. All
// media access is local; remote URLs are rejected by the library and playlist
// layers.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"player2/internal/audio"
	"player2/internal/audio/ffmpeg"
	malgoSink "player2/internal/audio/malgo"
	"player2/internal/config"
	"player2/internal/library"
	"player2/internal/meta"
	"player2/internal/player"
	"player2/internal/store"
	"player2/internal/tui"
	"player2/internal/xdg"
)

const version = "0.1.0-dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// cliOptions is the parsed command line.
type cliOptions struct {
	configPath string
	debug      bool
	version    bool
	help       bool
	dir        string
}

// run executes the command and returns a process exit code. It writes normal
// output to stdout and diagnostics to stderr.
func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "player2: %v\n\n", err)
		writeUsage(stderr)
		return 2
	}
	if opts.help {
		writeUsage(stdout)
		return 0
	}
	if opts.version {
		fmt.Fprintf(stdout, "player2 %s\n", version)
		return 0
	}

	logger, closeLog := newLogger(opts.debug, stderr)
	defer closeLog()

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "player2: %v\n", err)
		logger.Printf("config error: %v", err)
		return 1
	}
	logger.Printf("player2 %s starting", version)

	startDir, err := resolveStartDir(opts.dir, cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "player2: %v\n", err)
		logger.Printf("start directory error: %v", err)
		return 1
	}
	logger.Printf("start directory: %s", startDir)

	if err := checkBinary("ffmpeg", cfg.FFmpegBin); err != nil {
		fmt.Fprintf(stderr, "player2: %v\n", err)
		logger.Printf("%v", err)
		return 1
	}
	if err := checkBinary("ffprobe", cfg.FFprobeBin); err != nil {
		fmt.Fprintf(stderr, "player2: %v\n", err)
		logger.Printf("%v", err)
		return 1
	}

	prober := meta.NewFFProbe(cfg.FFprobeBin)

	registry := audio.NewRegistry()
	decoder := ffmpeg.New(cfg.FFmpegBin)
	exts := append(audio.DefaultAudioExtensions(), cfg.ExtraAudioExtensions...)
	registry.Register(exts, decoder)

	scanner := library.New(library.Options{
		ShowHidden:          cfg.ShowHidden,
		FollowSymlinks:      cfg.FollowSymlinks,
		CueFallbackEncoding: cfg.CueFallbackEncoding,
		AudioExtensions:     registry.Extensions(),
	}, prober)

	sink := malgoSink.New()
	engine, err := player.New(registry, sink, player.Options{
		SampleRate:    cfg.SampleRate,
		Channels:      2,
		BufferSeconds: cfg.BufferSeconds,
	})
	if err != nil {
		// player.New closes the sink itself on failure; Close is idempotent.
		_ = sink.Close()
		message := fmt.Errorf(
			"open audio output: %w\n"+
				"hint: check that an audio server/device is available (PulseAudio, PipeWire, ALSA, or JACK; try `aplay -l`)",
			err,
		)
		fmt.Fprintf(stderr, "player2: %v\n", message)
		logger.Printf("%v", message)
		return 1
	}

	// Apply the configured volume immediately; the TUI does this too, but
	// doing it here keeps startup behavior obvious.
	engine.SetVolume(cfg.Volume)

	closed := false
	closeEngine := func() error {
		if closed {
			return nil
		}
		closed = true
		return engine.Close()
	}
	defer func() {
		if err := closeEngine(); err != nil {
			fmt.Fprintf(stderr, "player2: close audio output: %v\n", err)
			logger.Printf("close audio output: %v", err)
		}
	}()

	stateDir, err := xdg.StateDir()
	if err != nil {
		fmt.Fprintf(stderr, "player2: locate state directory: %v\n", err)
		logger.Printf("locate state directory: %v", err)
		return 1
	}
	queueStore := store.New(filepath.Join(stateDir, "queue.json"))

	tuiOpts := tui.Options{
		Config:   cfg,
		StartDir: startDir,
		Scanner:  scanner,
		Player:   engine,
		Store:    queueStore,
		Prober:   prober,
	}
	if err := tui.Validate(tuiOpts); err != nil {
		fmt.Fprintf(stderr, "player2: invalid configuration: %v\n", err)
		logger.Printf("invalid configuration: %v", err)
		return 1
	}

	model := tui.New(tuiOpts)
	if err := model.Err(); err != nil {
		fmt.Fprintf(stderr, "player2: invalid configuration: %v\n", err)
		logger.Printf("invalid configuration: %v", err)
		return 1
	}

	program := tea.NewProgram(model)
	_, runErr := program.Run()

	// Save the latest queue before closing playback. FlushQueue serializes with
	// any asynchronous save still in flight, so exit cannot persist stale state.
	flushErr := model.FlushQueue()
	closeErr := closeEngine()

	exitCode := 0
	if flushErr != nil {
		fmt.Fprintf(stderr, "player2: save queue: %v\n", flushErr)
		logger.Printf("save queue: %v", flushErr)
		exitCode = 1
	}
	if closeErr != nil {
		fmt.Fprintf(stderr, "player2: close audio output: %v\n", closeErr)
		logger.Printf("close audio output: %v", closeErr)
		exitCode = 1
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "player2: %v\n", runErr)
		logger.Printf("program error: %v", runErr)
		exitCode = 1
	}
	if exitCode != 0 {
		return exitCode
	}
	logger.Printf("player2 exiting")
	return 0
}

// parseArgs parses flags and the optional starting directory.
func parseArgs(args []string) (cliOptions, error) {
	var opts cliOptions
	fs := flag.NewFlagSet("player2", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.configPath, "config", "", "path to a configuration file")
	fs.BoolVar(&opts.debug, "debug", false, "write debug logs to the state directory")
	fs.BoolVar(&opts.version, "version", false, "print the version and exit")
	fs.BoolVar(&opts.help, "help", false, "print usage and exit")
	fs.BoolVar(&opts.help, "h", false, "print usage and exit")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 1 {
		return opts, fmt.Errorf("expected at most one directory, got %d arguments", fs.NArg())
	}
	if fs.NArg() == 1 {
		opts.dir = fs.Arg(0)
	}
	return opts, nil
}

// writeUsage prints the command synopsis and flags.
func writeUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  player2 [directory]
  player2 --config PATH
  player2 --debug
  player2 --version

Options:
  --config PATH  use PATH instead of $XDG_CONFIG_HOME/player2/config.toml
  --debug        append debug logs to $XDG_STATE_HOME/player2/player2.log
  --version      print the version and exit
  -h, --help     print this help and exit

With no directory, player2 starts in start_dir from the configuration, or in
the current directory when start_dir is missing or invalid.
`)
}

// newLogger opens the debug log when enabled. Without --debug, log output is
// discarded. Log files live under the XDG state directory and never go to
// stdout.
func newLogger(debug bool, stderr io.Writer) (*log.Logger, func()) {
	if !debug {
		return log.New(io.Discard, "", 0), func() {}
	}

	dir, err := xdg.StateDir()
	if err != nil {
		fmt.Fprintf(stderr, "player2: debug log: %v (continuing without a log file)\n", err)
		return log.New(io.Discard, "", 0), func() {}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(stderr, "player2: debug log: create %s: %v (continuing without a log file)\n", dir, err)
		return log.New(io.Discard, "", 0), func() {}
	}

	path := filepath.Join(dir, "player2.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "player2: debug log: open %s: %v (continuing without a log file)\n", path, err)
		return log.New(io.Discard, "", 0), func() {}
	}
	// Enforce the documented mode even when the file already existed with
	// wider permissions.
	if err := file.Chmod(0o600); err != nil {
		fmt.Fprintf(stderr, "player2: debug log: set mode on %s: %v\n", path, err)
	}

	return log.New(file, "", log.LstdFlags), func() { _ = file.Close() }
}

// resolveStartDir chooses the browser root. A CLI directory must exist and be a
// directory; an unusable configured start_dir falls back to the current
// directory.
func resolveStartDir(cliDir string, cfg config.Config, logger *log.Logger) (string, error) {
	if cliDir != "" {
		abs, err := filepath.Abs(cliDir)
		if err != nil {
			return "", fmt.Errorf("resolve start directory %q: %w", cliDir, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("start directory %q: %w", cliDir, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("start directory %q is not a directory", cliDir)
		}
		return abs, nil
	}

	if cfg.StartDir != "" {
		abs, err := filepath.Abs(cfg.StartDir)
		if err == nil {
			if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
				return abs, nil
			}
		}
		logger.Printf("start_dir %q is not an accessible directory; falling back to the current directory", cfg.StartDir)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	return cwd, nil
}

// checkBinary verifies that an FFmpeg tool is executable using the configured
// value (a PATH name or an explicit path).
func checkBinary(name, configured string) error {
	value := configured
	if value == "" {
		value = name
	}
	if _, err := exec.LookPath(value); err != nil {
		return fmt.Errorf(
			"%s executable %q is not usable; install FFmpeg (which provides both ffmpeg and ffprobe) or set %s_bin in the configuration: %w",
			name, value, name, err,
		)
	}
	return nil
}
