# Tabbed Playlists Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the playlist pane ten persistent tabs (`0`–`9`) that each hold an independent queue, while audio keeps following the tab that started it.

**Architecture:** Each tab owns a `queue.Queue`; the model tracks the visible tab and the sounding tab separately. `queue.json` becomes schema v2 with `active_tab` plus an array of queue snapshots, with a v1 read path into tab 0. Transport next/previous/auto-advance target the sounding tab; playlist edits target the visible tab.

**Tech Stack:** Go 1.26, Bubble Tea v2, existing `queue`, `store`, `config`, and `tui` packages. No new dependencies.

**Spec:** No separate spec file; the approved design is inlined in the task descriptions and global constraints below.

---

## Global Constraints

- Linux only, local files only, filesystem-first; do not add a database, daemon, network path, or new dependency.
- Ten fixed tabs, digits `0`–`9`; `alt+0`…`alt+9` switch by default and remain configurable through `[keys]` as `tab_0`…`tab_9`.
- The visible tab is the target for add/remove/move/clear/save/open. The sounding tab is the tab that started the current stream; switching tabs never changes audio.
- Next/previous/auto-advance target the sounding tab while a playback context exists. Explicit stop clears that context.
- Persist all ten tab queues, each tab's current/shuffle/repeat, and the active tab. `queue.json` schema becomes version 2; version 1 files load into tab 0.
- Preserve atomic writes, mode `0600`, state directories `0700`, restore-pending suppression, single-flight/coalescing saves, and exit flush ordering.
- Preserve transport serialization: Play/Seek/Stop never run concurrently from `Update`.
- Player events are matched by item ID against the sounding tab's current item, never the visible tab's.
- Metadata probes must be identified by tab and item ID; IDs restart at 1 in every queue.
- Run `gofmt -l .`, `go test -race ./...`, `go vet ./...`, and `CGO_ENABLED=1 go build -o bin/player2 ./cmd/player2` before claiming completion.
- Do not claim interactive TUI or real audio verification unless a TTY and audio device were used.

## Review Focus

1. A finished/error event from tab A while tab B is visible must advance/report tab A, and an item with the same numeric ID in tab B must not consume it.
2. Startup restore must never overwrite a valid tabbed `queue.json` with empty tabs; a mutation in any tab must invalidate a pending restore.
3. Alt+digit must survive Bubble Tea key parsing and be rebindable; terminals that intercept it must have a documented workaround.
4. Saving must include every tab and the active tab, while loading must prune missing files independently per tab and preserve each tab's current item.
5. Metadata results must apply to the tab that requested them even when another tab is visible, despite equal item IDs across tabs.

---

### Task 1: Store schema v2 and tab-aware persistence API

**Files:**
- Modify: `internal/store/queue.go`
- Test: `internal/store/queue_test.go`

**Interfaces:**
- Consumes: `queue.Snapshot`, `queue.Snapshot.Items`, `queue.Snapshot.Current`.
- Produces:
  - `func (s *Store) LoadTabs() (active int, tabs []queue.Snapshot, skipped int, err error)`
  - `func (s *Store) SaveTabs(active int, tabs []queue.Snapshot) error`
  - Temporary compatibility wrappers `Load()` and `Save(queue.Snapshot)` delegating to the tab API, removed in Task 2.

- [ ] **Step 1: Write failing store tests for v2 round-trip, v1 migration, per-tab pruning, active clamp, and too many tabs**

Add tests named `TestSaveTabsLoadTabsRoundtrip`, `TestLoadTabsMigratesVersion1ToTabZero`, `TestLoadTabsPrunesEachTab`, `TestLoadTabsClampsActiveTab`, and `TestSaveTabsRejectsTooManyTabs`. Assert `"version":2`, `"active_tab"`, and `"tabs"` in the file. Update the invalid-data cases so version 3, not version 2, is the unsupported version.

- [ ] **Step 2: Run the store tests and verify RED**

Run: `go test ./internal/store -run 'TestSaveTabs|TestLoadTabs|TestLoadInvalidData' -v`
Expected: FAIL because `LoadTabs`/`SaveTabs` are undefined or v2 is not read.

- [ ] **Step 3: Implement v2 load/save with a v1 read path**

Use a version header decode, `diskStateV1{Version int; Queue queue.Snapshot}` and `diskStateV2{Version int; ActiveTab int; Tabs []queue.Snapshot}`. Extract the existing prune logic into `pruneSnapshot(s queue.Snapshot) (queue.Snapshot, int)` and apply it to every tab. Reject more than ten tabs. Clamp an out-of-range active index to 0. Save always writes version 2 and normalizes nil tabs to an empty slice.

- [ ] **Step 4: Run the store tests and verify GREEN**

Run: `go test ./internal/store -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/queue.go internal/store/queue_test.go
git commit -m "feat(store): persist playlist tabs with schema v2"
```

---

### Task 2: TUI tab model, switching, rendering, and persistence

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/deps.go`
- Modify: `internal/tui/playlist.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/fakes_test.go`
- Modify: `internal/tui/playlist_test.go`
- Modify: `internal/tui/view_test.go`
- Test: create `internal/tui/tabs_test.go`

**Interfaces:**
- Consumes: `store.LoadTabs`, `store.SaveTabs`, `queue.Queue`, `queue.Snapshot`.
- Produces:
  - `const tabCount = 10` in `internal/tui`
  - `type playlistTab struct { queue *queue.Queue; selected, offset int }`
  - `Model.tabs [tabCount]playlistTab`, `Model.activeTab int`, `Model.playbackTab int`
  - `func (m *Model) switchTab(index int) tea.Cmd`
  - `QueueStore.LoadTabs() (int, []queue.Snapshot, int, error)` and `QueueStore.SaveTabs(int, []queue.Snapshot) error`
  - `func (m *Model) stateSnapshot() (int, []queue.Snapshot)`

- [ ] **Step 1: Write failing model tests for ten independent tabs, switching, and tab persistence**

Add tests named `TestNewCreatesTenIndependentTabs`, `TestSwitchTabSwapsQueueAndSelection`, `TestRestoreLoadsAllTabsAndActiveTab`, `TestSavePersistsAllTabsAndActiveTab`, `TestFlushQueueSavesAllTabs`, and `TestTabsRenderTenSlotsWithActiveMarker`. Seed a fake store with at least three non-empty tabs and active tab 2. Assert that switching to tab 3 shows tab 3's items and preserves tab 0's selection, and that the rendered strip contains `0` through `9` with the active tab bracketed.

- [ ] **Step 2: Run the focused TUI tests and verify RED**

Run: `go test ./internal/tui -run 'TestNewCreatesTenIndependentTabs|TestSwitchTab|TestRestoreLoadsAllTabs|TestSavePersistsAllTabs|TestFlushQueueSavesAllTabs|TestTabsRenderTen' -v`
Expected: FAIL because the model has one queue and no tab fields.

- [ ] **Step 3: Implement the tab model, strip rendering, and tab persistence**

Create ten queues in `New`, keep `m.queue` as an alias for the active tab's queue, and sync `playlistSelected`/`playlistOffset` through `switchTab`. Replace the playlist pane title row with a width-safe strip of ten digits: active `[n]`, empty dim, used normal, sounding `n▶` reserved for Task 3. Implement `stateSnapshot`, update `saveQueue`/`FlushQueue`, and change restore to validate every snapshot into fresh queues before installing any of them. Pad missing tabs with empty snapshots and mark `restorePending` false only after all tabs validate. Remove the temporary `store.Load`/`Save` wrappers.

- [ ] **Step 4: Update existing tests and fakes for the tabbed store API**

Update `fakeStore` and `blockingLoadStore` to implement `LoadTabs`/`SaveTabs`, keeping `snapshot` as a convenience for tab 0. Update expectations that looked for `Playlist (N)` to look for the tab strip, and update direct `store.Save` seeds to `SaveTabs(0, []queue.Snapshot{...})`.

- [ ] **Step 5: Run the full TUI and store packages and verify GREEN**

Run: `go test ./internal/tui ./internal/store`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui internal/store
git commit -m "feat(tui): add ten persistent playlist tabs"
```

---

### Task 3: Visible tab versus sounding tab

**Files:**
- Modify: `internal/tui/playlist.go`
- Modify: `internal/tui/preview.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/model.go`
- Test: `internal/tui/tabs_test.go`
- Test: `internal/tui/playlist_test.go`

**Interfaces:**
- Consumes: `Model.playbackTab`, tab queues, existing serialized transport.
- Produces:
  - `func (m *Model) playItem(tab int, item queue.Item, status string) tea.Cmd`
  - `func (m *Model) transportTab() int`
  - `func (m *Model) playbackItem() (queue.Item, bool)` using the sounding tab
  - `func (m *Model) tabSelection(tab int) (selected, offset int)` and `setTabSelection`
  - Tab-aware `handlePlayerEvent`, `actionNext`, `actionPrevious`, `playlistRemove`, `playlistClear`, and `ActionGotoPlaying`.

- [ ] **Step 1: Write failing playback-context tests**

Add tests named `TestSwitchTabKeepsAudioOnSoundingTab`, `TestNextFollowsSoundingTabWhileAnotherTabIsVisible`, `TestAutoAdvanceHiddenTabUpdatesThatQueue`, `TestStopClearsSoundingContext`, `TestGotoPlayingSwitchesToSoundingTab`, and `TestClearingNonSoundingTabKeepsPlayback`. Use `fakePlayer.played` and direct `playerEventMsg` delivery. Also assert that a finished event for tab 0 with item ID 1 does not advance tab 1, which also has an item ID 1.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/tui -run 'TestSwitchTabKeepsAudio|TestNextFollowsSounding|TestAutoAdvanceHidden|TestStopClears|TestGotoPlaying|TestClearingNonSounding' -v`
Expected: FAIL because transport still reads `m.queue` and `playbackTab` is not used.

- [ ] **Step 3: Implement sounding-tab transport**

Set `playbackTab` whenever a queue item starts (including auto-advance and browser Enter), clear it on explicit stop and end-of-queue, and make `playItem` take the tab index. Use `transportTab()` for next/previous. Update `playbackItem`, `nowPlaying`, `durationLabel`, progress, and the now-playing header so they follow the sounding tab. `g` switches to the sounding tab when needed, then selects its current item. Clear/remove stop playback only when the visible tab is the sounding tab.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run: `go test ./internal/tui -run 'TestSwitchTabKeepsAudio|TestNextFollowsSounding|TestAutoAdvanceHidden|TestStopClears|TestGotoPlaying|TestClearingNonSounding' -v`
Expected: PASS.

- [ ] **Step 5: Run the TUI package and verify no regressions**

Run: `go test ./internal/tui`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui
git commit -m "feat(tui): keep playback on the sounding tab"
```

---

### Task 4: Configurable alt-number tab switching

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/tui/keys.go`
- Modify: `internal/tui/keys_test.go`
- Modify: `internal/tui/model.go`

**Interfaces:**
- Consumes: `KeyMap.Action`, `KeyMap.Keys`, `actionOrder`, `switchTab`.
- Produces: actions `tab_0`…`tab_9`, default bindings `alt+0`…`alt+9`, and `tabActionIndex(Action) (int, bool)`.

- [ ] **Step 1: Write failing keymap and config tests**

Add tests named `TestDefaultTabBindings`, `TestTabBindingsAreConfigurable`, `TestDuplicateTabBindingRejected`, and `TestAltDigitSwitchesVisibleTab`. Assert that the default config maps `alt+3` to `tab_3`, an override of `tab_3` is accepted, a duplicate with another action is rejected, and a `tea.KeyPressMsg{Code: '3', Mod: tea.ModAlt}` switches to tab 3.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/config ./internal/tui -run 'TestDefaultTabBindings|TestTabBindings|TestDuplicateTabBinding|TestAltDigit' -v`
Expected: FAIL because `tab_0`…`tab_9` are unknown actions.

- [ ] **Step 3: Implement the ten actions and defaults**

Add `tab_0`…`tab_9` to `config.keyActions`, their defaults to `defaultKeys`, stable `ActionTab0`…`ActionTab9` constants, `actionDescriptions`, and `actionOrder`. Dispatch them in `handleKey` through `switchTab`. Keep the hints line unchanged; the help overlay lists all ten.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run: `go test ./internal/config ./internal/tui -run 'TestDefaultTabBindings|TestTabBindings|TestDuplicateTabBinding|TestAltDigit' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config internal/tui
git commit -m "feat(keys): bind alt-number to playlist tabs"
```

---

### Task 5: Tab-aware metadata probing

**Files:**
- Modify: `internal/tui/playlist.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/browser.go`
- Modify: `internal/tui/fakes_test.go`
- Test: `internal/tui/tabs_test.go`
- Test: `internal/tui/playlist_test.go`

**Interfaces:**
- Consumes: tab queues, `queueMetadataMsg`, `probeItems`.
- Produces:
  - `type probeKey struct { tab int; id uint64 }`
  - `queueMetadataMsg.Tab int`
  - `func (m *Model) probeItems(tab int) tea.Cmd`
  - per-tab probe cleanup on replace/clear.

- [ ] **Step 1: Write failing probe tests**

Add tests named `TestQueueMetadataAppliesToRequestingTab`, `TestEqualItemIDsInOtherTabAreNotUpdated`, and `TestSwitchingTabProbesThatTabsItems`. Seed two tabs with items that have ID 1; deliver metadata for tab 0 and assert only tab 0 changes. Switch to tab 1 and assert the prober is asked for tab 1's path.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/tui -run 'TestQueueMetadataApplies|TestEqualItemIDs|TestSwitchingTabProbes' -v`
Expected: FAIL because probes are keyed only by item ID and always apply to the active queue.

- [ ] **Step 3: Implement tab-aware probe identity and cleanup**

Change `Model.probing` to `map[probeKey]string`, include the tab in `queueMetadataMsg`, have `probeItems(tab)` snapshot that tab's queue, and apply results to `m.tabs[msg.tab].queue` with range checks. Probe the active tab on restore and on switch. Remove only the active tab's probe entries when replacing or clearing it.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run: `go test ./internal/tui -run 'TestQueueMetadataApplies|TestEqualItemIDs|TestSwitchingTabProbes' -v`
Expected: PASS.

- [ ] **Step 5: Run the TUI package and verify no regressions**

Run: `go test ./internal/tui`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui
git commit -m "feat(tui): probe playlist metadata per tab"
```

---

### Task 6: Documentation and full verification

**Files:**
- Modify: `README.md`
- Modify: `config.example.toml`
- Modify: `ARCHITECTURE.md`

**Interfaces:**
- Consumes: the implemented behavior and key names.
- Produces: user-facing docs for ten tabs, `alt+0`…`alt+9`, `[keys] tab_0`…`tab_9`, schema v2, and the visible/sounding tab distinction.

- [ ] **Step 1: Update user documentation**

Add a Tabs section describing the strip, empty tabs, switching while audio continues, next/previous following the sounding tab, and `g`. Add the tab key row to the key table and the ten actions to the `[keys]` example. Document that some terminal emulators intercept `Alt+digit`, so the bindings can be changed. Update the playlist/persistence descriptions to schema v2.

- [ ] **Step 2: Update architecture documentation**

Update the queue/persistence section with `active_tab` plus ten snapshots and the v1 migration. Update the TUI section with visible/sounding tabs, the tab strip, and per-tab probing. Add known limitations for terminal Alt+digit interception and whole-state saves.

- [ ] **Step 3: Run formatting and the full gate**

Run:
```sh
gofmt -l .
go test -race ./...
go vet ./...
CGO_ENABLED=1 go build -o bin/player2 ./cmd/player2
```
Expected: `gofmt -l .` prints nothing; all tests pass; vet and build succeed.

- [ ] **Step 4: Commit**

```bash
git add README.md config.example.toml ARCHITECTURE.md
git commit -m "docs: document tabbed playlists"
```
