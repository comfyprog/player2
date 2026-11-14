# player2

player2 is a filesystem-first music player for Linux terminals. It browses
local directories in a MOC-like two-pane interface, builds a playback queue,
handles CUE sheets as virtual album folders, and plays audio files whose
extensions are recognized by the scanner through FFmpeg. There is no library
index, database, account, or network access: every view reads the filesystem
and playback uses local files only.

## Install and build

Requirements:

- Linux.
- Go 1.26 or newer.
- cgo enabled (`CGO_ENABLED=1`); the audio output backend uses miniaudio through
  malgo. On Linux the build only needs `-ldl` in addition to the normal cgo
  toolchain.
- `ffmpeg` and `ffprobe` at runtime. They provide decoding and metadata.
- A working audio server or device exposed through PulseAudio, PipeWire, ALSA,
  or JACK.

Build the binary:

```sh
make build            # writes bin/player2
# or
CGO_ENABLED=1 go build -o bin/player2 ./cmd/player2
# or, for a binary named player2 in the current directory
go build ./cmd/player2
```

Run it:

```sh
./bin/player2 [directory]
```

## Command line

```text
player2 [directory]
player2 --config PATH
player2 --debug
player2 --version
player2 -h | --help
```

- `--config PATH` reads exactly `PATH`; a missing explicit file is an error.
  With no flag, player2 reads `$XDG_CONFIG_HOME/player2/config.toml`
  (`~/.config/player2/config.toml` by default) if it exists, otherwise built-in
  defaults are used.
- `--debug` appends diagnostic logging to
  `$XDG_STATE_HOME/player2/player2.log`
  (`~/.local/state/player2/player2.log` by default). The directory is created
  with mode `0700` and the file with mode `0600`. Debug output never goes to
  stdout. Without `--debug`, logs are discarded.
- `--version` prints `player2 <version>` and exits without reading
  configuration or checking FFmpeg or audio devices.
- `-h`/`--help` prints usage and exits.
- Diagnostics always go to stderr. Unknown flags and extra positional
  arguments are usage errors.
- Put flags before the optional directory: flag parsing stops at the first
  positional argument, so `player2 --debug ~/Music` works while
  `player2 ~/Music --debug` is treated as extra arguments and rejected.

The start directory is chosen in this order:

1. The optional command-line directory. It must exist and be a directory.
2. `start_dir` from the configuration. If it is missing or not a directory,
   player2 logs a note (with `--debug`) and falls back to the current directory.
3. The current working directory.

Before opening the interface, player2 checks that the configured `ffmpeg_bin`
and `ffprobe_bin` values are executable. An unusable binary is reported with an
installation hint.

## Keys

All keys are configurable; these are the defaults.

| Action | Keys | Description |
| --- | --- | --- |
| Quit | `q`, `Ctrl+C` | Exit player2 |
| Help | `?` | Show the full key map |
| Switch pane | `Tab` | Move between the browser and playlist |
| Switch playlist tab | `Alt+0`…`Alt+9` | Show playlist tab 0–9 |
| Zoom active pane | `z` | Show only the focused pane |
| Open/play | `Enter` | Open a directory/CUE folder or play the selection |
| Preview track | `Shift+Enter` | Play a track without changing the queue |
| Parent directory | `Backspace` | Go up one directory |
| Move up/down | `Up`/`k`, `Down`/`j` | Move the selection |
| Page up/down | `PgUp`, `PgDn` | Move one screen |
| First/last | `Home`, `End` | Jump to the first or last row |
| Add | `a` | Add the selection to the active tab |
| Add all | `A` | Add the whole directory, recursively |
| Remove | `d` | Remove the selected item from the active tab |
| Move item up/down | `K`, `J` | Reorder the selected item in the active tab |
| Play/pause | `Space` | Pause/resume, or start the active tab's current item |
| Stop | `x` | Stop playback |
| Next/previous | `n`, `b` | Move through the sounding queue |
| Seek back/forward | `Left`, `Right` | Seek by 5 seconds |
| Volume up/down | `+`/`=`, `-`/`_` | Change volume by 5% |
| Mute | `m` | Toggle mute |
| Shuffle | `s` | Toggle shuffle for the active tab |
| Repeat | `r` | Cycle repeat for the active tab |
| Search | `/` | Filter the browser by substring |
| Toggle tags | `t` | Switch the browser between filenames and tags |
| Track info | `i` | Show details for the highlighted audio file |
| Save playlist | `w` | Save the active tab as an M3U file |
| Name tab | `T` | Set or clear the active tab's name |
| Rescan | `R` | Reload the current directory |
| Clear queue | `c` | Clear the active tab (with confirmation) |
| Go to playing | `g` | Jump to the sounding tab's current item |

`Shift+Enter` (`preview` in `[keys]`) auditions the highlighted audio file or CUE
track without touching the queue, its current item, or the saved queue file.
The preview stops when the track ends; `n`/`b` are ignored while it is active,
and pause, seek, volume, and `x` still work. Preview relies on the terminal
reporting `Shift+Enter` distinctly from `Enter`; if yours does not, bind
`preview` to another key in `[keys]`. For tmux, see [tmux](#tmux) below.

### tmux

tmux does not forward modified keys by default, so `Shift+Enter` arrives as plain
`Enter` and player2 opens the track instead of previewing it. Enable extended
keys in `~/.tmux.conf`:

```tmux
set -s extended-keys on
set -s extended-keys-format csi-u
```

Then reload the configuration with `tmux source-file ~/.tmux.conf` or restart
tmux. Confirm the active server settings with:

```sh
tmux show-options -sv extended-keys
tmux show-options -sv extended-keys-format
```

`extended-keys on` alone is sufficient; `csi-u` selects the modern
Kitty-compatible encoding. If your tmux does not recognize these options,
update tmux first.

The browser starts in filename mode. Pressing `t` (`toggle_tags` in `[keys]`)
shows embedded tags instead: track number, title, album, and artist, with long
rows truncated using `…`. Files without usable tags keep their filename. Tags
are read lazily with `ffprobe`, so browsing filenames does not probe every file;
the status line shows `[FILENAME]` or `[TAG]` to indicate the active view.

Pressing `i` (`info` in `[keys]`) opens a read-only window for the highlighted
plain audio file in either pane. It shows the filename, full path, duration,
container and codec details, and embedded tags. Long content scrolls with the
arrow, page, Home, and End keys; Esc or `i` closes it. CUE tracks are not
included yet.

Pressing `z` (`toggle_zoom` in `[keys]`) hides the inactive pane and shows the
focused one at full width. Pressing `z` again restores both panes. `Tab` also
leaves zoom and moves focus to the other pane. Zoom is not persisted across
runs.

## Playlist tabs

The playlist pane has ten fixed tabs, numbered `0`–`9`. All ten are always
visible: empty tabs are dimmed, the visible tab is bracketed (`[3]`), and the
tab that owns the current sound is marked with `▶`.

- `Alt+0`…`Alt+9` switch the visible tab. There are no modifier-free tab
  shortcuts by default; bind `tab_0`…`tab_9` in `[keys]` to change them.
- Every tab has its own queue, current item, selection, shuffle, and repeat.
  Browser add/open, remove, move, clear, save, and go-to-playing act on the
  visible tab.
- Switching tabs does not interrupt playback. While audio is playing or paused,
  next/previous and automatic advance follow the tab that started the stream.
  The header keeps showing that item and adds `[tab N]` when another tab is
  visible. The header's `R:`/`S:` modes always describe the visible tab; when
  another tab is sounding they carry a `(tab N)` label. Explicit stop clears
  the sounding context; after that, transport falls back to the visible tab.
- `g` jumps to the sounding tab when it is not visible, selects its current
  item, and leaves playback alone.
- `T` (`name_tab` in `[keys]`) opens a prompt for the active tab. The current
  name is prefilled; Enter saves it and an empty input clears it. A named active
  tab shows its name on a line directly below the numbered strip, and that line
  takes one row from the queue view. Inactive tab names are never shown.
- All ten tab queues, their names, shuffle/repeat settings, and the visible tab
  are saved in the state JSON and restored at startup. `restore_queue = false`
  starts every tab empty and unnamed.
- Some terminal emulators reserve `Alt+digit` for their own tab switching. If
  your terminal does not deliver the key to player2, rebind `tab_0`…`tab_9` in
  `[keys]`.

Tab queues and names are session state, not playlist files. `w` writes only the
visible tab's queue to an `.m3u`; the tab name is not included, and loading an
`.m3u` replaces the visible tab's queue without changing its name.

## Configuration

The default location is `~/.config/player2/config.toml` (respecting
`XDG_CONFIG_HOME`). Every key is optional; omitted keys keep the defaults shown
below. A complete copyable example with every option and default binding is in
[`config.example.toml`](config.example.toml).

```toml
start_dir = "~/Music"
show_hidden = false
follow_symlinks = false
restore_queue = true
volume = 70
sample_rate = 48000
buffer_seconds = 2

ffmpeg_bin = "ffmpeg"
ffprobe_bin = "ffprobe"
cue_fallback_encoding = "windows-1252"
extra_audio_extensions = []

[colors]
accent = "39"
dir = "33"
cue = "141"
playing = "46"
error = "196"

[keys]
# Values are arrays of key names understood by Bubble Tea, for example:
quit = ["q", "ctrl+c"]
volume_up = ["+", "="]
```

Notes:

- `start_dir` supports a leading `~` and environment variables such as
  `$HOME`.
- `volume` is `0`–`100`; `sample_rate` and `buffer_seconds` must be positive.
- `ffmpeg_bin` and `ffprobe_bin` may be names on `PATH` or explicit paths.
- `cue_fallback_encoding` is used when a CUE sheet is neither UTF-8 nor UTF-16
  with a byte-order mark. `windows-1252` is the default; `"utf-8"` (or
  `"utf8"`) disables the legacy fallback, and the common ISO-8859/Windows/KOI8/Mac
  encodings are accepted.
- `extra_audio_extensions` adds extensions (for example `[".m4b"]`) that the
  scanner should treat as audio. Files are still decoded by FFmpeg.
- `[colors]` values are ANSI 256-color indexes.
- `restore_queue` restores all ten playlist tabs, their names, and the last
  visible tab. With it disabled, every tab starts empty and unnamed, and the next
  save replaces the state file.
- `[keys]` action names are the lowercase names shown in the key table; each
  value replaces that action's default binding. The ten tab actions are named
  `tab_0` through `tab_9`.

Unknown configuration keys and unknown `[keys]` action names are startup errors
rather than silently ignored.

## Playlists

player2 reads and writes `.m3u` and `.m3u8` playlists. Paths are local; relative
paths are resolved against the playlist's directory. Remote URLs and remote
`file://` hosts are rejected.

A playlist entry can point at one track inside a CUE sheet. Put the CUE
reference on the line before the audio file it belongs to:

```m3u
#EXTM3U
#EXTINF:185,Artist - Third Movement
#EXT-X-PLAYER2-CUE:cue=Album.cue;track=3
Album.flac
```

The `cue=` value is a path relative to the playlist (URL-escaped when needed)
and `track=` is the CUE `TRACK` number. The following path line identifies the
whole audio file. If the CUE or track cannot be resolved, playback falls back to
the whole-file entry. Saving with `w` writes this extension automatically for
CUE track queue items.

## CUE sheets

A CUE sheet that parses, references existing regular audio files, and has valid
track boundaries appears in the browser as a virtual folder named after the
sheet, for example `Album.cue/`. The audio files referenced by that valid sheet
are hidden from the surrounding directory listing so the album is not shown
twice.

Opening the folder lists one entry per track with its title, performer, album,
start offset, and duration. Entering a track queues it and starts playback at
the correct offset; seeking stays within the track.

An invalid CUE (parse error, missing `FILE`, missing audio, bad `INDEX`, or a
track ending before it starts) is shown as an error entry and hides nothing, so
the underlying audio files remain visible and playable.

Boundary rules:

- `INDEX 01` is the track start.
- An `INDEX 00` pregap belongs to the previous track.
- The last track of a file ends at the audio file's duration.
- Multi-file sheets are supported; each `FILE` has its own track ranges.
- Backslash separators, case-insensitive file names, UTF-16 byte-order marks,
  and the configured fallback encoding are handled.

## Supported formats

Discovering audio files depends on their extension, not on probing every file:
the scanner only shows files with the built-in extensions or those added with
`extra_audio_extensions`. The built-in list is:

```text
.aac .ac3 .aif .aifc .aiff .alac .ape .caf .dff .dsf .dts .flac .m4a .m4b
.mka .mp2 .mp3 .mpc .oga .ogg .opus .spx .tta .w64 .wav .wma .wv
```

If FFmpeg on your system can decode another container or codec, add its
extension to `extra_audio_extensions` (for example `[".mp4", ".webm"]`) so the
scanner discovers those files. Decoding is always delegated to FFmpeg, so the
installed build must include the relevant demuxer and codec.

## Local files only

player2 never makes network requests. FFmpeg is always invoked with
`-protocol_whitelist file`, and CUE/M3U loaders reject remote URL schemes and
remote `file://` hosts. Only local files are scanned, queued, probed, or played.

## Limitations

- FFmpeg is started per item, so short gaps between tracks are expected.
- Playback is not gapless across queue items or between same-file CUE tracks.
- There is no library index, tag database, lyrics, cover-art rendering, MPRIS
  integration, or mouse support.
- Linux only. Audio output uses malgo/miniaudio; there is no fallback to a
  headless or file output device.
- Playlist tabs are persisted as JSON under the state directory; they are not a
  portable library format. The whole tab set is rewritten on each queue change.
- Some terminal emulators intercept `Alt+digit`; use the rebindable `tab_0`…
  `tab_9` actions when that happens.

## Verification checklist

The end-to-end test generates real media with FFmpeg (FLAC, MP3, Opus, ALAC in
M4A, plus a CUE album) and plays a CUE track through a fake sink, so it needs no
audio device:

```sh
make test
make race
make test-integration
```

Manual checks, when a real terminal and audio device are available:

- Play an APE, ALAC, and Opus file.
- Open a single-file FLAC/APE album with a `.cue` sheet and play each track.
- Pause, seek backward/forward, and confirm the position stays within the track.
- Save the active tab with `w`, load the `.m3u` from the browser, and confirm entries.
- Start playback in tab 0, switch to tab 2 with `Alt+2`, and confirm audio and
  the header stay on tab 0.
- Name a tab with `T`, restart with `restore_queue = true`, and confirm the tab
  names and visible tab return.
- Resize the terminal and confirm the layout stays usable.
- Open a malformed CUE and confirm it is displayed as an error while the audio
  file remains visible.
- Rename a referenced audio file and confirm the CUE becomes invalid instead of
  hiding the remaining file.
- Quit and confirm no `ffmpeg` process is left running.

## License

player2 is released under the MIT License. See [LICENSE](LICENSE) for the full
text, including the copyright notice.
