package tui

import (
	"fmt"

	"player2/internal/config"
)

// Action is one configurable UI action. The vocabulary matches internal/config
// so configuration files and the keymap cannot drift.
type Action string

// Action names accepted in [keys].
const (
	ActionQuit          Action = "quit"
	ActionHelp          Action = "help"
	ActionSwitchPane    Action = "switch_pane"
	ActionTab0          Action = "tab_0"
	ActionTab1          Action = "tab_1"
	ActionTab2          Action = "tab_2"
	ActionTab3          Action = "tab_3"
	ActionTab4          Action = "tab_4"
	ActionTab5          Action = "tab_5"
	ActionTab6          Action = "tab_6"
	ActionTab7          Action = "tab_7"
	ActionTab8          Action = "tab_8"
	ActionTab9          Action = "tab_9"
	ActionToggleZoom    Action = "toggle_zoom"
	ActionOpen          Action = "open"
	ActionPreview       Action = "preview"
	ActionParent        Action = "parent"
	ActionUp            Action = "up"
	ActionDown          Action = "down"
	ActionPageUp        Action = "page_up"
	ActionPageDown      Action = "page_down"
	ActionFirst         Action = "first"
	ActionLast          Action = "last"
	ActionAdd           Action = "add"
	ActionAddAll        Action = "add_all"
	ActionRemove        Action = "remove"
	ActionMoveUp        Action = "move_up"
	ActionMoveDown      Action = "move_down"
	ActionPause         Action = "pause"
	ActionStop          Action = "stop"
	ActionNext          Action = "next"
	ActionPrevious      Action = "previous"
	ActionSeekBack      Action = "seek_back"
	ActionSeekForward   Action = "seek_forward"
	ActionVolumeUp      Action = "volume_up"
	ActionVolumeDown    Action = "volume_down"
	ActionMute          Action = "mute"
	ActionShuffle       Action = "shuffle"
	ActionRepeat        Action = "repeat"
	ActionSearch        Action = "search"
	ActionAddBookmark   Action = "add_bookmark"
	ActionShowBookmarks Action = "show_bookmarks"
	ActionToggleTags    Action = "toggle_tags"
	ActionInfo          Action = "info"
	ActionSavePlaylist  Action = "save_playlist"
	ActionNameTab       Action = "name_tab"
	ActionRescan        Action = "rescan"
	ActionClear         Action = "clear"
	ActionGotoPlaying   Action = "goto_playing"
)

// actionOrder is the stable order used by help and hint rendering.
var actionOrder = []Action{
	ActionQuit,
	ActionHelp,
	ActionSwitchPane,
	ActionTab0,
	ActionTab1,
	ActionTab2,
	ActionTab3,
	ActionTab4,
	ActionTab5,
	ActionTab6,
	ActionTab7,
	ActionTab8,
	ActionTab9,
	ActionToggleZoom,
	ActionOpen,
	ActionPreview,
	ActionParent,
	ActionUp,
	ActionDown,
	ActionPageUp,
	ActionPageDown,
	ActionFirst,
	ActionLast,
	ActionAdd,
	ActionAddAll,
	ActionRemove,
	ActionMoveUp,
	ActionMoveDown,
	ActionPause,
	ActionStop,
	ActionNext,
	ActionPrevious,
	ActionSeekBack,
	ActionSeekForward,
	ActionVolumeUp,
	ActionVolumeDown,
	ActionMute,
	ActionShuffle,
	ActionRepeat,
	ActionSearch,
	ActionAddBookmark,
	ActionShowBookmarks,
	ActionToggleTags,
	ActionInfo,
	ActionSavePlaylist,
	ActionNameTab,
	ActionRescan,
	ActionClear,
	ActionGotoPlaying,
}

// actionDescriptions maps actions to the labels shown in help.
var actionDescriptions = map[Action]string{
	ActionQuit:          "Quit",
	ActionHelp:          "Help",
	ActionSwitchPane:    "Switch pane",
	ActionTab0:          "Switch to tab 0",
	ActionTab1:          "Switch to tab 1",
	ActionTab2:          "Switch to tab 2",
	ActionTab3:          "Switch to tab 3",
	ActionTab4:          "Switch to tab 4",
	ActionTab5:          "Switch to tab 5",
	ActionTab6:          "Switch to tab 6",
	ActionTab7:          "Switch to tab 7",
	ActionTab8:          "Switch to tab 8",
	ActionTab9:          "Switch to tab 9",
	ActionToggleZoom:    "Zoom active pane",
	ActionOpen:          "Open/play",
	ActionPreview:       "Preview track",
	ActionParent:        "Parent directory",
	ActionUp:            "Move up",
	ActionDown:          "Move down",
	ActionPageUp:        "Page up",
	ActionPageDown:      "Page down",
	ActionFirst:         "First",
	ActionLast:          "Last",
	ActionAdd:           "Add",
	ActionAddAll:        "Add all",
	ActionRemove:        "Remove",
	ActionMoveUp:        "Move item up",
	ActionMoveDown:      "Move item down",
	ActionPause:         "Play/pause",
	ActionStop:          "Stop",
	ActionNext:          "Next",
	ActionPrevious:      "Previous",
	ActionSeekBack:      "Seek back 5s",
	ActionSeekForward:   "Seek forward 5s",
	ActionVolumeUp:      "Volume up",
	ActionVolumeDown:    "Volume down",
	ActionMute:          "Mute",
	ActionShuffle:       "Shuffle",
	ActionRepeat:        "Repeat",
	ActionSearch:        "Search",
	ActionAddBookmark:   "Bookmark directory",
	ActionShowBookmarks: "Show bookmarks",
	ActionToggleTags:    "Toggle tags",
	ActionInfo:          "Track info",
	ActionSavePlaylist:  "Save playlist",
	ActionNameTab:       "Name tab",
	ActionRescan:        "Rescan",
	ActionClear:         "Clear queue",
	ActionGotoPlaying:   "Go to playing",
}

// tabActions maps tab index to the action that switches to it.
var tabActions = [tabCount]Action{
	ActionTab0,
	ActionTab1,
	ActionTab2,
	ActionTab3,
	ActionTab4,
	ActionTab5,
	ActionTab6,
	ActionTab7,
	ActionTab8,
	ActionTab9,
}

// tabActionIndex reports the tab index bound to action.
func tabActionIndex(action Action) (int, bool) {
	for index, candidate := range tabActions {
		if action == candidate {
			return index, true
		}
	}
	return 0, false
}

// Binding is one action's keys and help label.
type Binding struct {
	Action      Action
	Keys        []string
	Description string
}

// KeyMap maps keystrokes to actions. It is immutable after construction.
type KeyMap struct {
	bindings map[Action][]string
	lookup   map[string]Action
}

// NewKeyMap loads the built-in bindings, overlays any entries in cfg.Keys, and
// validates the result. A partial cfg.Keys overlays only the named actions;
// unmentioned actions keep their defaults. Empty binding lists are allowed.
func NewKeyMap(cfg config.Config) (KeyMap, error) {
	defaults := config.Default().Keys

	merged := make(map[Action][]string, len(actionOrder))
	for action, keys := range defaults {
		merged[Action(action)] = append([]string(nil), keys...)
	}

	for name, keys := range cfg.Keys {
		action := Action(name)
		if _, ok := actionDescriptions[action]; !ok {
			return KeyMap{}, fmt.Errorf("keymap: unknown action %q with keys %q", name, keys)
		}
		merged[action] = append([]string(nil), keys...)
	}

	lookup := make(map[string]Action)
	for _, action := range actionOrder {
		seen := make(map[string]struct{}, len(merged[action]))
		for _, key := range merged[action] {
			if key == "" {
				return KeyMap{}, fmt.Errorf("keymap: empty key for action %q", action)
			}
			if _, exists := seen[key]; exists {
				return KeyMap{}, fmt.Errorf("keymap: duplicate key %q within action %q", key, action)
			}
			seen[key] = struct{}{}
			if previous, exists := lookup[key]; exists && previous != action {
				return KeyMap{}, fmt.Errorf(
					"keymap: duplicate key %q bound to both %q and %q",
					key, previous, action,
				)
			}
			lookup[key] = action
		}
	}

	return KeyMap{bindings: merged, lookup: lookup}, nil
}

// Keys returns a copy of the keys bound to action.
func (k KeyMap) Keys(action Action) []string {
	return append([]string(nil), k.bindings[action]...)
}

// Action returns the action bound to key.
func (k KeyMap) Action(key string) (Action, bool) {
	action, ok := k.lookup[key]
	return action, ok
}

// Matches reports whether key is bound to action.
func (k KeyMap) Matches(action Action, key string) bool {
	got, ok := k.lookup[key]
	return ok && got == action
}

// Bindings returns every action in stable order with its keys and description.
// Actions with no bindings are included so help can show them as unbound.
func (k KeyMap) Bindings() []Binding {
	bindings := make([]Binding, 0, len(actionOrder))
	for _, action := range actionOrder {
		bindings = append(bindings, Binding{
			Action:      action,
			Keys:        append([]string(nil), k.bindings[action]...),
			Description: actionDescriptions[action],
		})
	}
	return bindings
}
