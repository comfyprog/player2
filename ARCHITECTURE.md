# Architecture

This document describes how player2 is put together: package boundaries, data flow,
concurrency, the playback pipeline, CUE handling, persistence, and the TUI command
model. For user-facing behavior see [README.md](README.md); for agent rules see
[AGENTS.md](AGENTS.md).

## Goals and constraints

player2 is a single-process Linux terminal application with these invariants:

- Filesystem-first navigation. There is no library index, database, daemon, or
  client/server split.
- Local files only. No network calls; FFmpeg is restricted to file input.
- Broad format support comes from FFmpeg. The application discovers files by
  extension and delegates decoding and metadata to `ffmpeg`/`ffprobe`.
- Valid CUE sheets are first-class: they appear as virtual folders whose tracks
  are independently seekable queue items.
- The audio path is isolated behind interfaces so the UI and queue never depend on
  a specific decoder or output backend.
- v1 does not promise gapless playback. A new FFmpeg process is started per queue
  item and per seek.

Explicit non-goals: library indexing, tag editing, lyrics, cover art, MPRIS,
ReplayGain, scrobbling, mouse support, remote control, and Windows/macOS.

## System context

```text
                    keyboard / terminal
                           │
                           ▼
┌────────────────────────────────────────────────────────────┐
│ cmd/player2                                                │
│  config/XDG → startup checks → dependency construction     │
└──────────────────────────────┬─────────────────────────────┘
                               │
                               ▼
┌────────────────────────────────────────────────────────────┐
│ internal/tui (Bubble Tea v2)                               │
│  browser pane ── library.Scanner ── cue + meta             │
│  playlist pane ── queue.Queue                              │
│  prompts/keymap/styles                                     │
│  tea.Cmd: scan, probe, restore/save, playlist I/O, play    │
└───────────────┬──────────────────────────────┬─────────────┘
                │ queue.Item / transport        │ queue.Snapshot
                ▼                               ▼
┌──────────────────────────────┐   ┌─────────────────────────┐
│ internal/player              │   │ internal/store          │
│  engine + events             │   │  versioned JSON, atomic │
└──────────────┬───────────────┘   └─────────────────────────┘
               │ PCM
               ▼
┌──────────────────────────────┐
│ internal/audio               │
│  Decoder → Ring → Sink       │
│  ffmpeg subprocess / malgo   │
└──────────────────────────────┘
```

## Package map and boundaries

| Package | Owns | May import |
|---|---|---|
| `cmd/player2` | CLI, startup, wiring, shutdown | everything below |
| `internal/xdg` | XDG path resolution | stdlib |
| `internal/config` | defaults, TOML, key-action vocabulary | xdg (indirectly via loader) |
| `internal/cue` | CUE parsing/validation/path resolution | stdlib, x/text |
| `internal/meta` | `ffprobe` invocation, tag/duration parsing, cache | stdlib |
| `internal/library` | directory listings, CUE virtual folders, `Walk` | cue, meta |
| `internal/queue` | queue model, IDs, shuffle/repeat, snapshots | stdlib |
| `internal/playlist` | M3U read/write, CUE extension | queue, cue |
| `internal/store` | queue JSON persistence and pruning | queue |
| `internal/audio` | decoder/stream/sink interfaces, registry, PCM ring | stdlib |
| `internal/audio/ffmpeg` | subprocess decoder | audio |
| `internal/audio/malgo` | miniaudio output sink | audio, malgo |
| `internal/player` | playback engine, stream lifecycle, events | audio, queue |
| `internal/tui` | Bubble Tea model, panes, prompts, keymap, styles | config, library, meta, player, playlist, queue |
| `internal/integration` | real-FFmpeg E2E tests with fake sink | all relevant lower packages |

The `internal/audio` package deliberately does not import its FFmpeg or malgo
subpackages. `internal/config` does not import the TUI; the key-action vocabulary
lives in config so both validation and the TUI keymap share one closed set.

## Startup and shutdown

`cmd/player2/main.go` performs:

1. Parse `player2 [directory]`, `--config PATH`, `--debug`, `--version`,
   `-h`/`--help`.
2. `--version` exits before config, FFmpeg, or audio checks.
3. Resolve the debug log path (`<StateDir>/player2.log`, dir `0700`, file `0600`)
   and load config with `config.Load`.
4. Resolve the start directory: CLI directory (must be an existing directory) →
   config `start_dir` (fallback to cwd if missing/invalid, with a debug note) →
   cwd.
5. Verify the configured `ffmpeg` and `ffprobe` binaries are executable.
6. Construct:
   - `meta.NewFFProbe(cfg.FFprobeBin)`
   - `audio.NewRegistry()`, register `audio.DefaultAudioExtensions()` plus
     `cfg.ExtraAudioExtensions` with `ffmpeg.New(cfg.FFmpegBin)`
   - `library.New(...)` using the registry's extension list
   - `malgo.New()`
   - `player.New(registry, sink, Options{SampleRate, Channels: 2, BufferSeconds})`
   - `store.New(<StateDir>/queue.json)`
7. Call `tui.Validate` (keymap validation), then `tui.New`, then
   `tea.NewProgram(model).Run()`.
8. On exit, flush the latest queue snapshot through the TUI's save mutex, close
   the player (which closes the sink), and return a non-zero status if a flush,
   close, or program error occurred.

`tui.New` applies the configured volume to the player before the first frame.

## Playback pipeline

### Decoder abstraction

`internal/audio` defines:

```go
type Request struct {
    Path       string
    Start      time.Duration
    SampleRate int
    Channels   int
}
type Stream interface { io.ReadCloser }
type Decoder interface { Open(ctx context.Context, req Request) (Stream, error) }
```

`audio.Registry` maps lowercase extensions (with a leading dot) to a `Decoder`.
It implements `Decoder` itself, so the player can use it directly, and also offers
`OpenPath(ctx, path, start)` with default 48000 Hz/stereo. Registration is
case-normalized, last-registration-wins, and `Extensions()` returns a sorted deduped
list.

All path handling passes through `audio.LocalPath`: plain local paths and Windows
drive-like paths are accepted; `file://` is normalized when the host is empty or
`localhost`; other schemes and remote `file://` hosts are rejected before any
process starts.

### FFmpeg subprocess

`internal/audio/ffmpeg.Decoder` runs:

```text
ffmpeg -hide_banner -loglevel error -nostdin -protocol_whitelist file
       -ss <seconds> -i <path> -map 0:a:0 -vn -sn -dn
       -f s16le -acodec pcm_s16le -ar <rate> -ac <channels> pipe:1
```

No shell is involved; the path is one argv element. The decoder:

- Uses `exec.CommandContext` and validates the path before starting.
- Sets `SysProcAttr{Setpgid:true, Pdeathsig:SIGKILL}` and custom context
  cancellation so the whole process group dies, not only the direct child.
- Captures bounded stderr for actionable error messages.
- Wraps stdout in a `Stream` whose `Close` is idempotent, kills the process group,
  waits exactly once, and leaves `ProcessState` populated. A SIGKILL is treated as
  expected only when the stream is being closed or its context is done; an
  external kill is surfaced as a decode error.
- Reports `ctx.Err()` after cancellation/timeout instead of a clean EOF.

### PCM ring

`internal/audio.Ring` is a byte-oriented fixed-capacity circular buffer guarded by
a `sync.Cond`:

- `Write` blocks while full and completes across wrap-around.
- `Read` never blocks, copies available audio, and zero-fills the rest.
- `Abort` wakes blocked writers; `Reset` clears data/counters and the aborted
  state; `Close` makes writes fail and reads safe.
- `Written` counts accepted bytes. `Consumed` counts only bytes actually taken
  from buffered audio, **not** zero-filled underrun silence. Playback position is
  therefore not advanced by underruns or startup silence.

The player creates one ring with capacity `rate × channels × 2 × buffer_seconds`.
Pause is implemented by stopping sink consumption, so `Consumed` and the displayed
position freeze without discarding queued audio.

### Output sink

`internal/audio/malgo.Sink` implements `audio.Sink`:

```go
type Sink interface {
    Start(ring *Ring, rate, channels int) error
    SetPaused(bool)
    SetVolume(float64)
    Close() error
}
```

It configures miniaudio playback with `FormatS16`, the requested rate/channels,
and the default output device. The callback:

- Outputs silence and consumes nothing while paused.
- Reads the ring, applies a short linear gain ramp to avoid clicks, and clamps
  volume to 0..1.
- Uses atomics/mutexes so pause/volume changes from the UI are race-free.
- Is allocation-free in steady state.

`Start` twice returns an error; `Close` is idempotent. Pause/volume set before
start are retained. The expensive device-init path is gated by
`PLAYER2_TEST_AUDIO=1` in tests; the gain/pause/render processor is tested without
a device.

### Player engine

`internal/player.Player` owns one ring, one monitor goroutine for its lifetime, and
one decoder goroutine per stream. Its public surface:

```go
Play(item queue.Item) error
Pause() / Resume() / TogglePause()
Stop()
SeekBy(delta time.Duration) error
SetVolume(percent int)
Status() Status
Events() <-chan Event
Close() error
```

Key semantics:

- `Play` stops the previous stream, resets the ring, opens the decoder at
  `item.Start`, installs a new stream generation, starts the decoder goroutine,
  unpauses the sink, and sets state playing. A Play-time Open error is returned and
  stored in `Status.Err`; it is not also emitted as an event.
- The decoder goroutine only reads and writes the ring; it never calls control
  methods that take the player's control mutex. EOF/error results are recorded and
  its `done` channel is closed.
- The monitor ticks every 10 ms. It derives position from
  `streamBase + bytesToDuration(ring.Consumed())`, emits `finished` when the
  logical duration is consumed or when the decoder is done and the ring is drained,
  and emits `error` on unexpected decoder failure. It never auto-advances a queue.
- The final position is recomputed after confirming the ring is drained, so a tail
  drained between sampling and completion cannot under-report the track end.
- `SeekBy` clamps the target to the known duration, stops the old stream, reopens
  the decoder at `item.Start+target`, preserves pause/playing state, and sets
  `streamBase=target`. A seek to the end finishes through the monitor.
- `Stop` retains the item but resets position and state. `Close` is idempotent and
  closes the event channel only after the monitor and decoder have stopped.
- `Event` carries `Type` (`finished`/`error`), `Err`, and `ItemID`. The TUI uses
  `ItemID` to ignore events from a stream that is no longer current.

Stream lifecycle ordering is important: cancel/close the old stream, wait for its
decoder `done`, then Abort/Reset the ring. This prevents a writer still blocked in
`Write` from injecting previous-track PCM into the next stream.

## Library and CUE model

### CUE parser

`internal/cue` parses `FILE`, `TRACK`, `INDEX`, `TITLE`, `PERFORMER`, `SONGWRITER`,
`PREGAP`, `POSTGAP`, `FLAGS`, `REM`, and `CATALOG`, ignoring unknown directives.

- Encoding: UTF-8, UTF-8 BOM, UTF-16 LE/BE BOM, then the configured fallback
  (default Windows-1252 via `x/text`).
- Paths: relative to the sheet, absolute, Windows backslashes, and case-insensitive
  filename fallback. Remote schemes and remote `file://` hosts are rejected.
- Track start is `INDEX 01`; a track without it is an error. Times within a file
  must be strictly increasing.
- `INDEX 00` is pregap and does not become the start; it effectively belongs to
  the previous track.
- `Resolve(DurationFunc)` computes each track's end: the next track's `INDEX 01` in
  the same file, or the file duration for the last track.
- Multi-file sheets are supported. `AllTracks()` flattens them in order.
- `ParseError` carries a 1-based line number. Input is capped at 4 MiB.

### Scanner and virtual CUE folders

`internal/library.Scanner` reads one directory or walks a tree. It detects audio
extensions (case-insensitive), `.m3u`/`.m3u8`, `.cue`, directories, and symlinks.

A CUE is treated as valid only when:

1. It parses.
2. It has at least one track with `INDEX 01` (parser-enforced).
3. Every referenced `FILE` exists and is a regular file.
4. Every required duration probes successfully and is positive.
5. `Sheet.Resolve` produces sane ranges.

A valid CUE becomes `KindCueDir` named `base(cue).cue/` (for example `Album.cue/`)
with `Path`/`CuePath` pointing at the sheet. Audio files referenced by any valid
CUE in the same directory are hidden from that listing. Multiple valid CUEs may
share one audio file; each folder appears and the audio is hidden once.

An invalid CUE becomes `KindInvalidCue` with the error attached and hides nothing,
so the underlying audio remains visible and playable.

`ReadCue` returns one `KindCueTrack` entry per track: display name
`NN. Title — Performer` with sheet/performer fallbacks, `Start`, `Duration`, and
metadata (title, artist, album, track number). `ResolveCueTrack(ctx, cuePath, n)`
treats `n` as the sheet `TRACK` number.

`Walk` returns playable entries only (audio + CUE tracks), expands CUE folders in
place, follows symlinks only when configured, and uses resolved real paths to skip
cycles and duplicate subtrees.

## Queue and persistence

### Queue model

`queue.Item` is the unit of playback:

```go
type Item struct {
    ID       uint64
    Path     string
    CuePath  string
    CueTrack int
    Start    time.Duration
    Duration time.Duration
    Title, Artist, Album string
    TrackNo  int
}
```

IDs are assigned by the queue and remain stable across moves/removals/updates.
`queue.Current` is the playback current item; it is distinct from the TUI
playlist selection. `Next(auto)` honors repeat-one only for automatic advance;
manual next/prev ignore it. Repeat-all wraps. Shuffle is a precomputed order of
item IDs with a deterministic injected RNG for tests; append/remove repair it.

`Snapshot` is `{Items, Current, Shuffle, Repeat}` with JSON tags. `Restore`
validates the snapshot, assigns fresh IDs, clamps/validates current, and rebuilds
shuffle order. It leaves the queue unchanged on error.

### Store

`internal/store.Store` reads/writes:

```json
{"version":1,"queue":{"items":[...],"current":0,"shuffle":false,"repeat":0}}
```

- Missing file → empty snapshot (`current:-1`), no error.
- Corrupt JSON/unsupported version → error.
- Load prunes items whose audio or CUE path is missing/not a regular file, returns
  the skipped count, and preserves the current item by ID where possible.
- Save creates the parent (`0700`), writes a same-directory temp file with mode
  `0600`, syncs, and renames over the destination. Failures clean up the temp.

### TUI persistence and restore ordering

- `Model.Init` starts an async `store.Load` when `restore_queue` is true and a store
  is configured.
- `restorePending` is true from construction until a restore succeeds. While true,
  `saveQueue` and `FlushQueue` do not write, so startup cannot overwrite a valid
  queue file with an empty in-memory queue.
- A real item/current mutation bumps a queue epoch and clears `restorePending`;
  a late restore result is dropped with a status message. Shuffle/repeat setting
  changes do not count as queue replacement.
- Saves are single-flight and coalescing: one save in flight at a time, with a
  `saveDirty` flag for a newer snapshot. `saveMu` serializes TUI saves with the
  exit `FlushQueue`.
- Exit calls `FlushQueue` before/alongside `Player.Close`; a pending restore keeps
  the file untouched.

## Playlists

`internal/playlist` reads and writes M3U/M3U8 text:

- UTF-8 BOM, LF/CRLF, blank lines, comments, `#EXTINF`, relative paths.
- Relative paths resolve against the playlist directory. `file://` is normalized;
  remote schemes and remote hosts are rejected.
- `#EXT-X-PLAYER2-CUE:cue=<url-escaped-path>;track=<number>` attaches to the next
  audio path line. The audio path remains the whole-file path for other players.
- On load, a CUE reference is validated with `cue.ParseFile` and the requested
  track must exist. Resolution failure clears the CUE fields and keeps the
  whole-file entry. The TUI additionally verifies media via `ResolveCueTrack` and
  falls back to the whole file if needed.
- On write, CUE tracks emit the extension line and the audio path. Paths are
  relative to the playlist when possible. Relative paths beginning with `#` or
  carrying significant leading/trailing whitespace get a `./` prefix so they do
  not reload as comments and survive whitespace handling.
- Writes are atomic (temp + rename) and reject paths containing newlines.

When a resolved CUE playlist item is queued, the sheet's `Start`/`Duration` are
authoritative; EXTINF is display metadata only. Missing files are skipped and
reported in the status line.

## TUI architecture

### Model and layout

`internal/tui.Model` is a Bubble Tea v2 model:

```go
Init() tea.Cmd
Update(tea.Msg) (tea.Model, tea.Cmd)
View() tea.View
```

`View` sets `AltScreen` and the `player2` window title. The fixed layout is:

- one header row (now-playing, position/duration, volume, repeat/shuffle),
- a two-pane body (browser left, playlist right),
- a two-row footer (status/progress bar and key hints).

Minimum terminal size is 60×10; below it the view renders a clear “terminal too
small” message. Rendering builds plain, width-checked rows with
`ansi.StringWidth`/`ansi.Truncate`, then applies styles, so Unicode and hostile
filenames cannot overflow or inject control sequences. The five configurable
colors (`accent`, `dir`, `cue`, `playing`, `error`) feed `Styles`; other roles use
built-in defaults.

### Browser

- Navigation stack stores path, selected index, offset, and whether the location is
  a CUE folder. Backspace restores the prior selection/offset.
- Directory/CUE loads are async with a generation counter. Stale results are
  ignored; the previous listing is cleared when the destination changes so actions
  cannot target stale rows. Loading blocks open/add actions.
- `/` enters an incremental, case-insensitive substring filter. Printable keys edit
  the query; Up/Down/PgUp/PgDn/Home/End move within filtered rows; Enter accepts and
  keeps the filter; Esc clears it. Navigation clears the filter; rescan preserves
  it.
- `a` adds the selection: directory/CUE recursively, playlist expanded, single
  audio/CUE track appended.
- `A` adds the whole current location recursively.
- `R` rescans, trying to keep the selected entry by identity.
- Enter on a directory/CUE folder descends. Enter on audio/CUE track/playlist
  replaces the queue with the playable entries at the current level and starts at
  the selection. CUE folders expand in place; playlists are not expanded into the
  level queue (only when selected directly). Playlist loading stops playback,
  starts an async expansion, replaces the queue, plays the first item, and reports
  skipped missing files.
- Browser CUE-track rows show `NN. Title — Performer` and the sheet duration,
  right-aligned and width-safe.
- Invalid CUEs show their error in the status line and never play.

### Playlist and transport

- Playlist rendering shows the current marker, selection, title/artist, CUE track
  number, right-aligned duration, and scroll offset.
- Queue replacement/restore selects the current item. Manual and automatic advance
  move the playlist selection only when it was already on the previous current
  item.
- Transport is global: pause/resume (or play current when stopped), stop,
  next/prev, seek ±5s, volume ±5, mute, shuffle, repeat. Playlist-focused actions:
  Enter plays selection, `d` removes (stopping playback when removing current),
  `K`/`J` move, `c` clears (with confirmation), `g` jumps to current, and
  Up/Down/Page/Home/End move selection.
- Play/Seek/Stop are serialized through an in-flight + pending transport chain.
  This prevents concurrent Bubble Tea commands from acquiring the player mutex in
  the wrong order. Queue/current changes remain immediate; the next operation is
  dispatched only after the current one returns.
- Finished/error events are consumed through a `tea.Cmd` waiting on the player's
  event channel. Auto-advance happens only for a `finished` event whose `ItemID`
  still matches the current queue item. Repeat-one replays the item, repeat-all
  wraps, repeat-off at the end stops advancing. Errors set a status error.
- Plain audio metadata is probed asynchronously for items lacking duration/title;
  CUE tracks are not probed. Probes are deduplicated by ID/path and bounded to four
  concurrent `ffprobe` processes per model.

### Prompts and keymap

- `?` opens a two-column help overlay built from the configured keymap, with
  scrolling when the full map does not fit; Esc closes it. The overlay is modal.
- `w` opens a text-input save prompt (Bubbles v2 `textinput`). The default
  `playlist.m3u` is a placeholder so typing replaces it; empty submit uses the
  default. Relative paths resolve against the browser directory (the CUE file's
  directory when inside a CUE folder). Existing targets require overwrite
  confirmation. The write itself runs in a `tea.Cmd`.
- Clear queue and overwrite use modal `[y/N]` confirmations. Only `y`/`Y` confirm;
  Enter, `n`, and Esc cancel.
- Keymap actions are a closed vocabulary defined in `internal/config`.
  `NewKeyMap` overlays partial config on defaults, rejects unknown actions,
  duplicate keystrokes, and empty keys, and is validated before program startup.

## Configuration and XDG

- Config path: `<ConfigDir>/config.toml`, default `~/.config/player2/config.toml`.
- XDG variables must be absolute; otherwise fallbacks are `~/.config`,
  `~/.local/share`, `~/.local/state`. Helpers return per-app directories.
- Missing default config is fine; an explicit missing `--config` is an error.
- Unknown TOML keys, unknown `[keys]` actions, invalid volume/sample rate/buffer
  values are startup errors.
- `start_dir` expands `~` and environment variables.
- `ffmpeg_bin`/`ffprobe_bin` must resolve to executables at startup.
- `extra_audio_extensions` is appended to the built-in scanner/registry list.
- State: queue JSON and debug log live under `<StateDir>` (`~/.local/state/player2`).

## Security and failure handling

- Network isolation is layered: CUE/M3U/meta/audio path validators reject remote
  schemes and remote `file://` hosts; FFmpeg gets `-protocol_whitelist file`;
  `ffprobe` runs with the same whitelist; no application code performs HTTP.
- FFmpeg/ffprobe are invoked without a shell, with paths as single argv values.
- Decoder children run in their own process group and are killed as a group;
  `Pdeathsig` covers parent death. Close waits exactly once and captures stderr.
- Transport cancellation is distinguished from clean EOF. External SIGKILL is
  reported as an error, not a finished track.
- CUE validation is fail-visible: invalid sheets never hide audio. Missing media
  makes the sheet invalid rather than leaving a phantom folder.
- Queue/store writes are atomic and serialize through the model's save mutex;
  startup restore cannot overwrite a valid file with an empty queue.
- TUI event delivery is lossless during normal operation (the monitor blocks on a
  full buffer but is unblocked by Close).

## Testing strategy

- Package unit tests use real temp directories and fakes for decoder/sink/prober/
  player where process or device I/O would be nondeterministic.
- CUE tests cover pregaps, multi-file sheets, encodings, backslash paths,
  case-insensitive resolution, malformed/oversized input, and a fuzz target.
- Player tests cover pause/seek/stop/finish, unknown durations, decoder errors,
  rapid transport, event-buffer pressure, goroutine leaks, and the final-position
  drain race.
- Decoder tests exercise exact argv, process-group cleanup, cancellation, external
  kills, and real MP3/FLAC/Opus/ALAC decode when FFmpeg is available.
- The integration test generates real media with FFmpeg (FLAC required; MP3/Opus/
  ALAC when encoders exist), builds a CUE album, scans it with the real prober, and
  plays a CUE track through a fake sink. It requires no audio device.
- The malgo device test skips unless `PLAYER2_TEST_AUDIO=1`.
- `go test -race ./...`, `go vet ./...`, `gofmt -l .`, and a cgo build are the
  completion gate.

## Extension points

### Add a format

1. Add the extension to `audio.DefaultAudioExtensions()` (or document use of
   `extra_audio_extensions` for site-specific formats).
2. The main wiring registers every default extension with the FFmpeg decoder, so
   no decoder change is needed if FFmpeg supports it.
3. Add a scanner/registry test and a README entry. Discovery is extension-based;
   the file is not probed just to decide whether to show it.

### Replace or add a decoder backend

Implement `audio.Decoder.Open(ctx, audio.Request) (audio.Stream, error)` and
register it for the relevant extensions. The player only depends on the interface,
so an alternative backend (for example libavcodec or a persistent stream) can be
introduced without changing the queue or TUI. Keep local-path rejection, process/
resource cleanup, and sample format (s16le interleaved at the requested rate/
channels) consistent with the FFmpeg decoder.

### Change persistence

Bump the store schema version and add a read path/migration before changing
`queue.Snapshot` tags. Preserve atomic writes, pruning, mode `0600`, restore-pending
suppression, and exit flush ordering. Add a round-trip test and a corrupt-version
test.

### Change CUE behavior

Keep the boundary model (`INDEX 01` → next `INDEX 01` or file end), the
invalid-CUE-never-hides-audio invariant, and scanner validation before hiding.
Add tests for pregap, multi-file, last-track duration, missing FILE, and
encoding/path edge cases.

## Known limitations and follow-ups

- FFmpeg process startup causes short gaps between items; same-file CUE tracks are
  not gapless. A persistent decoder backend is the intended future path.
- Linux-only. malgo/miniaudio requires cgo; there is no headless output backend.
- Metadata cache is in-memory and unbounded per observed file version. Probe
  goroutines are bounded at four running `ffprobe` processes but one waiting
  goroutine per eligible item is still created.
- Transport status for rapid overlapping actions can briefly show an older
  “playing …” message even though the latest item wins; playback ordering itself is
  serialized.
- Shuffle/repeat toggled before a pending startup restore completes may be
  overwritten by the restored snapshot and are not persisted in that window.
- A failed startup restore leaves saves suppressed until the first real queue
  mutation, preserving the existing file rather than overwriting it.
- Event identity is item-ID-based; replaying the exact same queue item in a narrow
  window can still accept an already-queued finished event.
- M3U path lines with leading whitespace before a URL are treated as local paths
  (and then skipped as missing); there is no network exposure.
- The real malgo device/callback path is only exercised with
  `PLAYER2_TEST_AUDIO=1`; normal CI covers the processor with fakes.
