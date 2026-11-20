# Browser Preview and Playlist Folders Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make browser `Enter` preview without touching the queue, turn M3U/M3U8 files into enterable virtual folders, and let preview advance through the current listing with playlist-style highlighting and shuffle/repeat behavior.

**Architecture:** The scanner keeps `KindPlaylist` but treats it as a container whose path is the `.m3u` file. The TUI replaces the `browserCue bool` with a container kind, loads playlist folders through the existing M3U loader, and removes browser queue replacement. Preview becomes a private `queue.Queue` seeded from the visible browser listing; it advances on `finished` and never mutates or saves the real queue.

**Tech Stack:** Go 1.26, Bubble Tea v2, existing `library`, `playlist`, `queue`, `meta`, `player`, and `tui` packages. No new dependencies.

**Spec:** No separate spec file; the approved design is inlined in the tasks and global constraints below.

---

## Global Constraints

- Linux only, local files only, filesystem-first; do not add a database, daemon, network path, or dependency.
- `Enter` on a playable browser row never changes the real queue, its current item, shuffle/repeat, or `queue.json`.
- `a` and `A` remain the only browser actions that append to the real queue.
- Playlist folders and CUE folders are virtual containers; referenced audio stays visible beside playlists, while valid CUE sheets keep hiding their backing audio.
- Keep FFmpeg/ffprobe invocation shell-free and keep `-protocol_whitelist file`; playlist loading continues to reject remote schemes and remote `file://` hosts through `internal/playlist`.
- Playback-changing work stays in `tea.Cmd`s; Play/Seek/Stop remain serialized through `requestTransport`.
- Do not change queue JSON shape, version 3 store schema, restore-pending behavior, save coalescing, or exit flush ordering.
- Preview item IDs must stay in the high `uint64` range so they cannot collide with real queue IDs.
- Run `gofmt -l .`, `go test -race ./...`, `go vet ./...`, and `CGO_ENABLED=1 go build -o bin/player2 ./cmd/player2` before claiming completion.
- Do not claim interactive TUI or real audio playback was verified unless a TTY and audio device were actually used.

## Review Focus

1. Pressing `Enter` on an audio file, CUE track, playlist folder, or invalid CUE must never mutate queue items/current or trigger a queue save.
2. A playlist folder must list only resolvable playable entries while leaving every referenced audio file visible in the parent directory.
3. Playlist navigation must preserve the container kind through back, rescan, bookmark return, and save-directory prompts.
4. Preview auto-advance must ignore queue events, use fresh high-range item IDs per track, and stop cleanly after the last item with repeat off.
5. Preview highlighting must follow the preview item, not the browser cursor, and disappear when the browser leaves the listing that started the preview.

---

### Task 1: Playlist virtual folders in the scanner

**Files:**
- Modify: `internal/library/entry.go`
- Modify: `internal/library/scanner.go`
- Test: `internal/library/scanner_test.go`

**Interfaces:**
- Consumes: existing `ReadDir`, `Walk`, `sortEntries`, `sortGroup`.
- Produces: `.m3u`/`.m3u8` entries with `Kind: KindPlaylist`, `Name: "<filename>/"`, and `Path` equal to the absolute playlist path. `KindPlaylist` sorts in the container group. `Walk` still omits `KindPlaylist`.

- [ ] **Step 1: Write failing scanner tests**

Update `TestReadDirBasic`, `TestReadDirNoAudioExtensions`, and `TestReadDirNaturalSort` so playlist names include a trailing slash and playlists sort with containers. Add:

```go
func TestReadDirPlaylistDoesNotHideAudio(t *testing.T) {
    // Directory contains song.flac and list.m3u referencing song.flac.
    // Assert both appear and the playlist entry is:
    // Kind == KindPlaylist, Name == "list.m3u/", Path == absolute list.m3u.
}
```

Add `TestWalkSkipsPlaylistFolders` asserting `Walk` over a directory containing `song.flac` and a playlist that references it returns only `song.flac` (no playlist entry and no duplicate).

- [ ] **Step 2: Run the library tests and verify RED**

Run: `go test ./internal/library -run 'TestReadDir|TestWalk' -v`
Expected: FAIL because playlists still have no trailing slash, sort as files, and the new test names are not satisfied.

- [ ] **Step 3: Implement playlist containers**

In `ReadDir`, replace the playlist branch with:

```go
case ext == ".m3u" || ext == ".m3u8":
    entries = append(entries, Entry{Kind: KindPlaylist, Name: name + "/", Path: path})
```

In `sortGroup`, return `0` for `KindPlaylist` as well as `KindDir` and `KindCueDir`. Update `entry.go` comments so `KindPlaylist` is documented as a virtual container, and update the `ReadDir`/`Walk` comments to state that playlist folders do not hide referenced audio and `Walk` does not expand them.

- [ ] **Step 4: Run the library tests and verify GREEN**

Run: `go test ./internal/library -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/library/entry.go internal/library/scanner.go internal/library/scanner_test.go
git commit -m "feat(library): present playlists as virtual folders"
```

---

### Task 2: Browser container state and playlist folder navigation

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/browser.go`
- Modify: `internal/tui/bookmarks.go`
- Modify: `internal/tui/prompt.go`
- Modify: `internal/tui/browser_test.go`
- Modify: `internal/tui/bookmarks_test.go`
- Modify: `internal/tui/prompt_test.go`

**Interfaces:**
- Consumes: Task 1's `KindPlaylist` container entries, `playlist.Load`, `Scanner.ResolveCueTrack`, `queueItemFromEntry`.
- Produces:
  - `Model.browserContainer library.Kind` (zero value `KindDir`)
  - `browserLocation.Container library.Kind`
  - `browserLoadedMsg.container library.Kind` and `browserLoadedMsg.skipped int`
  - `func (m *Model) loadBrowser(path string, container library.Kind, selected, offset int, focus string) tea.Cmd`
  - `func loadPlaylistEntries(ctx context.Context, scanner Scanner, path string) ([]library.Entry, int, error)`
  - `func libraryEntryFromQueueItem(item queue.Item) library.Entry`

- [ ] **Step 1: Write failing browser navigation and add tests**

Add `TestBrowserPlaylistFolderNavigation`: create a temp directory with `one.flac` and `list.m3u` containing `#EXTM3U\n#EXTINF:60,Artist - One\none.flac\n`; build the model with `sc := &testScanner{dirs: map[string][]library.Entry{dir: {{Kind: library.KindPlaylist, Name: "list.m3u/", Path: playlistPath}}}}`, set `m.browserPath = dir`, `m.browserEntriesPath = dir`, and `m.browserEntries` to that row; take `queueBefore := m.queue.Snapshot()`, press `Enter` on the playlist row, apply the command, then assert:

```go
m.browserContainer == library.KindPlaylist
m.browserPath == playlistPath
len(m.browserEntries) == 1
m.browserEntries[0].Kind == library.KindAudio
m.browserEntries[0].Name == "One — Artist"
snapshotsEqual(queueBefore, m.queue.Snapshot())
m.store.(*fakeStore).saveCount() == 0
```

Then press `Backspace`, apply the command, and assert the parent `KindDir` listing is restored with the playlist row selected.

Add `TestBrowserPlaylistFolderStateSurvivesRescanAndBookmarks`: while inside a playlist folder, run `R` and assert the container stays `KindPlaylist` and the entries reload; open `F` bookmarks and close with `Backspace`, then assert the playlist container and entries return. Add `TestSavePromptUsesPlaylistParentDirectory`: inside a playlist folder, `saveDirectory()` equals `filepath.Dir(playlistPath)`.

Add `TestBrowserPlaylistFolderSkipsMissingEntries` with one present and one missing path; assert one entry remains and `m.statusMsg` contains `"skipped 1"`.

Add `TestBrowserPlaylistFolderResolvesCueTrack` using a `testScanner.resolve` that returns a `KindCueTrack`; assert the folder entry has the resolved path, `CuePath`, `TrackNo`, `Start`, `Duration`, title, and artist.

Add `TestBrowserAddAllInsidePlaylistFolder`: set `m.browserContainer = library.KindPlaylist`, `m.browserPath` to a temp playlist, press `A`, apply, and assert its playable entries were appended to the queue.

Retarget the existing `TestBrowserPlaylistCueResolutionAndMissingFiles` subtests from `Enter` to `a`, because `Enter` now navigates and `a` owns queue-adding.

- [ ] **Step 2: Run the focused TUI tests and verify RED**

Run: `go test ./internal/tui -run 'TestBrowserPlaylistFolder|TestBrowserAddAllInsidePlaylistFolder|TestBrowserPlaylistCueResolution' -v`
Expected: FAIL because playlist rows still open/replace the queue and there is no playlist container state.

- [ ] **Step 3: Implement container-aware browser state and playlist loading**

Replace `browserCue` with `browserContainer` everywhere. In `loadBrowser`, branch by container:

```go
switch container {
case library.KindCueDir:
    entries, err = scanner.ReadCue(ctx, path)
case library.KindPlaylist:
    entries, skipped, err = loadPlaylistEntries(ctx, scanner, path)
default:
    entries, err = scanner.ReadDir(ctx, path)
}
```

Implement `loadPlaylistEntries` by calling `playlist.Load`, then `playlistQueueItem` per entry, skipping missing entries and counting them. Convert each surviving queue item with `libraryEntryFromQueueItem`: plain entries get `KindAudio` and `Name = displayTitle(item)`; CUE entries get `KindCueTrack` and a `"%02d. "` track-number prefix. Copy `Path`, `CuePath`, `TrackNo`, `Start`, `Duration`, and `Meta` fields. Rewrite `loadPlaylistItems` to call `loadPlaylistEntries` and convert back with `queueItemFromEntry` so folder display and add share one loader.

Update `descend` so `KindPlaylist` descends like a CUE folder and pushes `Container` on the stack. Update `browserBack`, `rescanBrowser`, `showBookmarks`, `closeBookmarks`, `openBookmark`, `saveDirectory`, `addBookmark`, and `Model.Init` to carry `KindDir`/`KindCueDir`/`KindPlaylist`. Reject bookmarks for any container other than `KindDir`.

In `applyBrowserLoaded`, install `msg.container` and, when `msg.skipped > 0`, set status `fmt.Sprintf("playlist: %d item(s), skipped %d", len(msg.entries), msg.skipped)`.

Change `browserOpen` so `KindDir`, `KindCueDir`, and `KindPlaylist` all call `descend`; remove the old playlist stop/load path.

Change `addAll` to branch: `KindCueDir` uses `readCueAdd`, `KindPlaylist` uses `playlistAdd`, otherwise `walkAdd`.

- [ ] **Step 4: Update existing direct field/load calls**

Replace test and production uses of `browserCue` with `browserContainer`, and `loadBrowser(path, false, ...)` with `loadBrowser(path, library.KindDir, ...)`. Delete the obsolete prompt test `TestBrowserEnterPlaylistStopsBeforeLoading`; the new `TestBrowserPlaylistFolderNavigation` owns that behavior. Update bookmark tests that set `m.browserCue = true` to set `m.browserContainer = library.KindCueDir`.

- [ ] **Step 5: Run the TUI tests and verify GREEN**

Run: `go test ./internal/tui -run 'TestBrowser|TestSavePrompt|TestBookmark' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui
git commit -m "feat(tui): browse playlists as virtual folders"
```

---

### Task 3: Make Enter preview and remove browser queue replacement

**Files:**
- Modify: `internal/tui/browser.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/keys.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/browser_test.go`
- Modify: `internal/tui/preview_test.go`
- Modify: `internal/tui/view_test.go`

**Interfaces:**
- Consumes: `browserPreview`, `startPreview`, `requestTransport`.
- Produces: `browserOpen` returns `browserPreview()` for `KindAudio` and `KindCueTrack`; `ActionOpen` is described as "Open/preview"; the hint label is "open".

- [ ] **Step 1: Replace queue-replacement tests with preview tests**

Replace `TestBrowserEnterAudioQueuesLevelAndPlaysSelection` and `TestBrowserEnterCueTrackQueuesWholeCueFolder` with:

- `TestBrowserEnterAudioPreviewsWithoutChangingQueue`: seed two real queue items, snapshot them, press `Enter` on an audio row, apply commands; assert the snapshot is unchanged, `store.saveCount() == 0`, one Play call for the selected audio item, `m.previewActive`, and `"previewing"` in the status.
- `TestBrowserEnterCueTrackPreviewsCueTrack`: inside a CUE folder, assert the played item has the selected `CueTrack`, `Start`, and `Duration`, and the queue stayed unchanged.

Replace `TestBrowserPlayRunsInCommand` with `TestBrowserPreviewRunsInCommand` using `controlledPlayer`: assert `Play` is not called inside `Update`, then run the returned command and assert it is called once.

Extend `TestBrowserEnterInvalidCueShowsErrorAndDoesNotPlay` with a seeded queue snapshot and `saveCount()` assertion, so an invalid CUE also proves no queue mutation.

Update `view_test.go` expectations for the hint row from `"enter play"` to `"enter open"` and for the key overlay to contain `"Open/preview"`.

Replace `TestNormalPlayClearsPreview` with `TestPlaylistPlayClearsPreview`: start a preview, focus the playlist pane, press `Enter`, run the command, and assert the queue item plays and `previewActive` is false.

Delete `TestAutoAdvanceDoesNotCancelPendingLevelPlay` and `TestStaleLevelPlayDoesNotReplacePreview`, which test the removed level-play path.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/tui -run 'TestBrowserEnter|TestBrowserPreviewRunsInCommand|TestPlaylistPlayClearsPreview' -v`
Expected: FAIL because Enter still replaces the queue.

- [ ] **Step 3: Remove the destructive browser play path**

In `browserOpen`, route `KindAudio` and `KindCueTrack` to `m.browserPreview()`. Keep `KindDir`/`KindCueDir`/`KindPlaylist` as `descend` and keep the invalid-CUE error.

Delete `browserPlayMsg`, `applyBrowserPlay`, `playLevel`, `playPlaylist`, `buildLevelItems`, and `indexOfEntry` if no longer referenced. Remove the `browserPlayMsg` case from `Model.Update`. Update comments in `preview.go` that describe the old level/playlist build race.

Set `ActionOpen: "Open/preview"` in `actionDescriptions` and change the hint label in `hintsLine` from `"play"` to `"open"`.

- [ ] **Step 4: Run the TUI tests and verify GREEN**

Run: `go test ./internal/tui -v`
Expected: PASS, with preview tests covering both `Enter` and `Shift+Enter`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui
git commit -m "feat(tui): preview files on Enter instead of replacing the queue"
```

---

### Task 4: Preview session with automatic advance and shuffle/repeat

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/preview.go`
- Modify: `internal/tui/preview_test.go`

**Interfaces:**
- Consumes: `queue.New`, `queue.Queue.Append`, `SetCurrent`, `SetShuffle`, `SetRepeat`, `Next(true)`, `queueItemFromEntry`, `browserVisibleEntries`, `browserEntriesPath`.
- Produces:
  - `Model.previewQueue *queue.Queue`
  - `Model.previewSource string`
  - `func (m *Model) browserPreview() tea.Cmd` builds the preview set from playable visible rows and calls `startPreview`.
  - `func (m *Model) startPreview(entries []library.Entry, selected int) tea.Cmd`
  - `func (m *Model) playPreviewCurrent() tea.Cmd`
  - `func (m *Model) advancePreview() tea.Cmd`
  - `func (m *Model) syncPreviewModes()`
  - `clearPreview` also clears `previewQueue` and `previewSource`.

- [ ] **Step 1: Write failing preview-session tests**

Add:

- `TestPreviewAutoAdvancesThroughBrowserListing`: three audio entries, select the first, start preview, send `EventFinished` for the preview ID, run the returned command; assert the second path played, the real queue snapshot is unchanged, `saveCount() == 0`, and the browser selection did not move.
- `TestPreviewStopsAfterLastWithRepeatOff`: select the last entry, finish it; assert no new Play call, `previewActive` is false, and status is `"preview finished"`.
- `TestPreviewRepeatOneReplaysSameTrack`: set the active queue to `RepeatOne`, start on the first of two entries, finish; assert the same path plays again with a different high-range item ID.
- `TestPreviewRepeatAllWraps`: set `RepeatAll`, start on the last of two entries, finish; assert the first path plays.
- `TestPreviewShuffleUsesPreviewSet`: set shuffle and repeat-all on the active queue, use three entries, finish three times, and assert the next three paths are a permutation of the three entries without duplicates.

Replace `TestPreviewFinishStopsWithoutAdvancingQueue`; its old assumption (an active queue `RepeatOne` does not affect preview) is now wrong.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/tui -run 'TestPreviewAutoAdvance|TestPreviewStopsAfterLast|TestPreviewRepeat|TestPreviewShuffle' -v`
Expected: FAIL because preview still ends after one item and clears on the first `EventFinished`.

- [ ] **Step 3: Implement the private preview queue**

Add `previewQueue` and `previewSource` to `Model`. In `browserPreview`, keep the selected playable check, then build an ordered slice of `KindAudio`/`KindCueTrack` rows from `browserVisibleEntries()` and find the selected row's index.

In `startPreview`, append `queueItemFromEntry` values to a fresh `queue.New()`, copy the active queue's shuffle/repeat with `SetShuffle`/`SetRepeat`, set the selected index, store `previewSource = m.browserEntriesPath`, mark preview active, and call `playPreviewCurrent`.

In `playPreviewCurrent`, take `previewQueue.Current()`, assign the next high-range ID (`^uint64(0) - (previewSeq - 1)`), set `previewItem`, clear the sounding tab, call `beginPlaybackIntent`, and issue the existing serialized `transportPlay` with `preview: true` plus `previewProbeCmd`.

In `advancePreview`, call `syncPreviewModes`, then `previewQueue.Next(true)`. When it returns false, clear preview and set status `"preview finished"`; otherwise call `playPreviewCurrent`. `syncPreviewModes` copies the active queue's current shuffle/repeat into the preview queue on every advance so live `s`/`r` changes apply.

Change `handlePreviewEvent`'s `EventFinished` branch to return `m.advancePreview(), true`. Keep the error branch stopping and clearing without advancing.

- [ ] **Step 4: Run the preview tests and verify GREEN**

Run: `go test ./internal/tui -run 'TestPreview|TestBrowserPreview' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/preview.go internal/tui/preview_test.go
git commit -m "feat(tui): advance preview through the browser listing"
```

---

### Task 5: Preview highlighting in the browser pane

**Files:**
- Modify: `internal/tui/browser.go`
- Modify: `internal/tui/preview.go`
- Modify: `internal/tui/browser_test.go`
- Modify: `internal/tui/view_test.go`
- Modify: `internal/tui/preview_test.go`

**Interfaces:**
- Consumes: Task 4's `previewItem`, `previewActive`, `previewSource`, `browserEntriesPath`, `entryKey`.
- Produces:
  - `func (m *Model) previewingEntry(entry library.Entry) bool`
  - `func (m *Model) browserRow(entry library.Entry, width int, previewing bool) string`
  - Browser rows reserve a two-cell gutter; `previewing` rows render `▶ ` and use `Styles.Playing` unless the row is selected, which keeps `Styles.Selection`.

- [ ] **Step 1: Write failing highlight tests**

Add `TestBrowserPreviewMarkerFollowsAdvance`: set `m.browserEntriesPath = "/music"`, use two audio entries, start preview on the first, render `m.browserLines(80, 20)`; assert the first row starts with `▶ ` and the second row starts with two spaces. Finish the first track, apply the command, render again, and assert the marker moved to the second row while `browserSelected` stayed at 0.

Add `TestBrowserPreviewMarkerClearsOutsideSource`: start a preview, set `m.browserEntriesPath` to another path, render, and assert no line contains `▶`.

Add `TestBrowserPreviewCueRowMarkerKeepsDuration`: preview a CUE track with a positive duration and assert the rendered row contains both the marker and `2:00`.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/tui -run 'TestBrowserPreviewMarker|TestBrowserPreviewCueRowMarker' -v`
Expected: FAIL because browser rows have no preview marker.

- [ ] **Step 3: Implement marker and style**

Implement `previewingEntry` by returning false when preview is inactive or `previewSource != browserEntriesPath`; otherwise compare `entryKey(entry)` with the key built from `previewItem.Path`, `previewItem.CuePath`, and `previewItem.CueTrack`.

In `browserLines`, compute `previewing := m.previewingEntry(entry)`, pass it to `browserRow`, and choose `Styles.Playing` when previewing unless the row is selected. In `browserRow`, reserve a two-cell marker for every non-bookmark row: `"  "` normally, `playingMarker + " "` while previewing. Prefix both the normal label and the CUE-track left side with it before fitting to width, so CUE durations stay right-aligned.

- [ ] **Step 4: Update width-sensitive tests and run the TUI tests**

Update `TestBrowserCueRowsShowDurationsAtSmallAndLargeSizes` and any view assertions that depend on the browser row starting at column zero for the new two-cell gutter. Run: `go test ./internal/tui -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/browser.go internal/tui/preview.go internal/tui/browser_test.go internal/tui/view_test.go internal/tui/preview_test.go
git commit -m "feat(tui): highlight the previewing browser row"
```

---

### Task 6: Documentation and full verification

**Files:**
- Modify: `README.md`
- Modify: `ARCHITECTURE.md`
- Modify: `config.example.toml`

**Interfaces:**
- Consumes: all prior tasks.
- Produces: user and architecture docs matching the new behavior.

- [ ] **Step 1: Update README**

Change the key table to describe `Enter` as open/preview and `Shift+Enter` as a preview alias. Replace the preview paragraph with: preview starts at the selected playable row, advances through the current listing, honors the visible tab's shuffle/repeat, stops after the last item with repeat off, highlights the current preview row with `▶`, and still ignores `n`/`b` while active. Update the tmux note so `Enter` is safe even when `Shift+Enter` collapses.

In the playlist tabs section, remove “Browser add/open” and “loading an `.m3u` replaces the visible tab's queue”; say that adding a playlist folder appends its contents. In the Playlists section, document that `.m3u`/`.m3u8` files appear as `name/` virtual folders whose contents are listed in playlist order, referenced audio stays visible, `a` on the folder adds its contents, and `A` inside it adds all contents. Update the CUE section sentence “Entering a track queues it” to “Enter previews it; `a` queues it.” Update the manual checklist item that says to load an `.m3u` from the browser.

- [ ] **Step 2: Update ARCHITECTURE**

Update the goals, scanner/virtual-folder section, browser section, playlist section, and transport section: `KindPlaylist` is a virtual container; `Walk` skips playlists; browser state stores a container kind; `Enter` previews playable rows and never replaces the queue; `a`/`A` remain queue mutations; preview uses a private queue over the current listing and respects shuffle/repeat; the preview row uses the playlist marker/playing style.

Update `config.example.toml` comments: `open` is "enter a directory/CUE/playlist folder or preview the selection"; `preview` remains the queue-safe audition alias; `add_all` says "add current location recursively (playlists expand only when they are the current location)".

- [ ] **Step 3: Run formatting, race tests, vet, and build**

Run:

```bash
gofmt -l .
go test -race ./...
go vet ./...
CGO_ENABLED=1 go build -o bin/player2 ./cmd/player2
```

Expected: `gofmt -l .` prints nothing; tests, vet, and build exit 0.

- [ ] **Step 4: Commit**

```bash
git add README.md ARCHITECTURE.md config.example.toml
git commit -m "docs: describe preview sessions and playlist folders"
```
