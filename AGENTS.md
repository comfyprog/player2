# AGENTS.md

Instructions for AI coding agents working on player2.

## What this project is

player2 is a Linux terminal music player written in Go. It has a filesystem-first
MOC-style browser/queue UI, exposes valid CUE sheets as virtual album folders, and
uses FFmpeg for decoding and metadata. There is no library index, database, daemon,
or network access.

Start with [README.md](README.md) for user-facing behavior and configuration.
Read [ARCHITECTURE.md](ARCHITECTURE.md) before changing playback, CUE handling,
queue persistence, or the TUI event/command model.

## Environment and commands

Requirements: Linux, Go 1.26+, cgo enabled, `ffmpeg` and `ffprobe` on `PATH`.

```sh
make build                         # CGO_ENABLED=1, writes bin/player2
make test                          # go test ./...
make race                          # go test -race ./...
make vet                           # go vet ./...
make test-integration              # real-FFmpeg E2E, fake audio sink
gofmt -l .                         # must print nothing
```

Focused checks while iterating:

```sh
go test -race ./internal/player ./internal/tui ./internal/integration
go test ./internal/cue -run TestName -v
```

The malgo device test only runs with `PLAYER2_TEST_AUDIO=1`. Do not make the normal
test suite require a real audio device or terminal.

## Non-negotiable product constraints

- Linux only. The TUI is keyboard-driven.
- Filesystem-first: do not add a library database/index, daemon, or client/server
  split.
- Local files only. Do not add network playback, HTTP streaming, remote scanning,
  or remote control. FFmpeg must keep `-protocol_whitelist file`; CUE/M3U loaders
  and the audio registry must reject remote URL schemes (including remote
  `file://host` forms).
- Supported files are discovered by extension, then decoded by FFmpeg. Adding a
  format means adding/registering an extension, not probing every file.
- A valid CUE sheet appears as an enterable folder (`Album.cue/`), its referenced
  same-directory audio is hidden, and its tracks have independent start/end
  offsets and seeking. An invalid CUE must never hide the underlying audio.
- Queue persistence is a JSON snapshot of the current queue, not a tag library.
- Gapless playback is not promised in v1. FFmpeg process startup can create short
  gaps between items and same-file CUE tracks.

## Repository map

| Path | Responsibility |
|---|---|
| `cmd/player2` | CLI flags, startup checks, dependency wiring, exit flush/close |
| `internal/xdg` | XDG config/data/state path resolution |
| `internal/config` | Config defaults, strict TOML loading, key action vocabulary |
| `internal/cue` | CUE parsing, encodings, path resolution, track ranges |
| `internal/meta` | `ffprobe` wrapper and metadata cache |
| `internal/library` | Directory scanner, CUE virtual folders, recursive walk |
| `internal/queue` | Queue items, stable IDs, current index, shuffle/repeat, snapshots |
| `internal/playlist` | M3U/M3U8 read/write, CUE-track extension |
| `internal/store` | Versioned queue and bookmark JSON, atomic writes, queue pruning |
| `internal/audio` | Decoder/stream/sink interfaces, extension registry, PCM ring |
| `internal/audio/ffmpeg` | FFmpeg subprocess decoder |
| `internal/audio/malgo` | malgo/miniaudio output sink |
| `internal/player` | Playback engine, events, seek/pause/finish lifecycle |
| `internal/tui` | Bubble Tea model, browser/playlist panes, prompts, keymap, styles |
| `internal/integration` | End-to-end tests using real FFmpeg and a fake sink |

Dependency direction is deliberately one-way toward lower-level packages. The TUI
may import queue/player/library/meta/config; the player may import queue/audio;
`internal/audio` must not import the FFmpeg or malgo subpackages. Avoid cycles,
especially config → TUI.

## Engineering rules

### TUI and concurrency

- `Update` runs on Bubble Tea's single UI goroutine. Never do blocking file,
  process, or audio work there.
- Play, seek, playlist load, queue restore, metadata probing, and playlist writing
  belong in `tea.Cmd`s and return messages.
- Transport-changing operations (Play/Seek/Stop) are serialized through the TUI's
  in-flight + pending transport chain. Do not call `Player.Play` directly from
  `Update` or from a second concurrent command.
- Ignore player events whose `Event.ItemID` no longer matches the current queue
  item. The engine emits `finished`/`error`; the TUI owns auto-advance.
- The playback engine owns one ring, a monitor goroutine, and one decoder
  goroutine per stream. Use its generation counter and wait for the old decoder
  to exit before reusing/resetting the ring (do not Abort+Reset under a live
  writer).
- `Ring.Consumed` counts real PCM bytes read from the ring, not zero-filled
  underrun silence. Playback position is `streamBase + consumed`.
- Keep audio callbacks allocation-free and race-free. A mutex is acceptable in
  the v1 callback; document any change to that tradeoff.

### CUE and library invariants

- Track start is `INDEX 01`. `INDEX 00` is pregap that belongs to the previous
  track. Track end is the next `INDEX 01` in the same file, or file duration for
  the last track.
- Validate the whole sheet before hiding anything: parse success, every `FILE`
  exists as a regular file, positive probed durations, sane ranges.
- Invalid CUEs stay visible as `KindInvalidCue` and hide nothing.
- `ResolveCueTrack` treats the argument as the sheet `TRACK` number.
- Keep `Walk` output playable-only (audio + CUE tracks), deterministic, and
  cycle-safe when following symlinks.

### Queue and persistence

- Item IDs are stable and assigned by the queue. Restore assigns fresh IDs.
- Keep `queue.Snapshot` JSON shape (`items`, `current`, `shuffle`, `repeat`) and
  the store envelope version 1 unless you deliberately bump the schema and add a
  migration/read path.
- Queue changes are saved asynchronously through the coalescing/single-flight
  saver. Do not start concurrent `store.Save` calls.
- Startup restore is guarded by the queue epoch: only a real item/current
  mutation invalidates a pending restore. Suppress saves/flush while a restore is
  pending; do not overwrite an existing queue file with an empty in-memory queue.
- `store.Save` remains atomic (same-directory temp + rename), mode `0600`; state
  directories are `0700`.
- Filesystem bookmarks are a small global path list in
  `<XDG_DATA_HOME>/player2/bookmarks.json`, not a library index. Keep schema
  version 1, retain missing paths (they stay removable), and use the same
  single-flight/coalescing save and exit-flush discipline as the queue.
- M3U relative paths beginning with `#` or carrying significant leading/trailing
  whitespace are written with a `./` prefix. Keep round-trips safe for hostile
  filenames.

### Errors, safety, and style

- Wrap errors with context (`fmt.Errorf("component: action %s: %w", ...)`).
  Actionable startup errors go to stderr; debug logs go to the XDG state log.
- Never invoke FFmpeg/ffprobe through a shell. Pass paths as single argv values.
- Keep local-path validation consistent across CUE, playlist, meta, audio, and
  registry code.
- Prefer the standard library. Do not add a dependency for something small.
- Keep files focused by responsibility. Follow existing naming and table-driven
  test style; run `gofmt`.
- Tests should verify behavior, not implementation mirrors. Use real temp
  directories for filesystem tests and fakes for decoder/sink/prober/player.
  Fakes belong in test files; do not weaken production interfaces for tests.
- Any change to CUE boundaries, persistence, transport ordering, or process
  cleanup needs a focused regression test plus `-race`.

## Before you claim completion

Run at least:

```sh
gofmt -l .
go test -race ./...
go vet ./...
CGO_ENABLED=1 go build -o bin/player2 ./cmd/player2
```

If you changed startup/CLI behavior, also run `./bin/player2 --version`,
`--help`, an explicit missing-config check, and a missing-FFmpeg `PATH` check.
If you changed playback/decoder code, confirm no `ffmpeg` child processes remain.
Do not claim interactive TUI or real audio playback was verified unless a TTY and
audio device were actually used; the E2E test uses a fake sink on purpose.

## Delegation

Subagents should use the model configured in the harness defaults unless the human
explicitly asks for a specific model. Do not assign models to subagents on your own.

## Key references

- [README.md](README.md) — user-facing install, keys, config, playlists, CUE behavior.
- [ARCHITECTURE.md](ARCHITECTURE.md) — package internals, data flow, concurrency,
  persistence, and extension points.
