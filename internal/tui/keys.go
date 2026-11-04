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
	ActionQuit         Action = "quit"
	ActionHelp         Action = "help"
	ActionSwitchPane   Action = "switch_pane"
	ActionOpen         Action = "open"
	ActionParent       Action = "parent"
	ActionUp           Action = "up"
	ActionDown         Action = "down"
	ActionPageUp       Action = "page_up"
	ActionPageDown     Action = "page_down"
	ActionFirst        Action = "first"
	ActionLast         Action = "last"
	ActionAdd          Action = "add"
	ActionAddAll       Action = "add_all"
	ActionRemove       Action = "remove"
	ActionMoveUp       Action = "move_up"
	ActionMoveDown     Action = "move_down"
	ActionPause        Action = "pause"
	ActionStop         Action = "stop"
	ActionNext         Action = "next"
	ActionPrevious     Action = "previous"
	ActionSeekBack     Action = "seek_back"
	ActionSeekForward  Action = "seek_forward"
	ActionVolumeUp     Action = "volume_up"
	ActionVolumeDown   Action = "volume_down"
	ActionMute         Action = "mute"
	ActionShuffle      Action = "shuffle"
	ActionRepeat       Action = "repeat"
	ActionSearch       Action = "search"
	ActionSavePlaylist Action = "save_playlist"
	ActionRescan       Action = "rescan"
	ActionClear        Action = "clear"
	ActionGotoPlaying  Action = "goto_playing"
)

// actionOrder is the stable order used by help and hint rendering.
var actionOrder = []Action{
	ActionQuit,
	ActionHelp,
	ActionSwitchPane,
	ActionOpen,
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
	ActionSavePlaylist,
	ActionRescan,
	ActionClear,
	ActionGotoPlaying,
}

// actionDescriptions maps actions to the labels shown in help.
var actionDescriptions = map[Action]string{
	ActionQuit:         "Quit",
	ActionHelp:         "Help",
	ActionSwitchPane:   "Switch pane",
	ActionOpen:         "Open/play",
	ActionParent:       "Parent directory",
	ActionUp:           "Move up",
	ActionDown:         "Move down",
	ActionPageUp:       "Page up",
	ActionPageDown:     "Page down",
	ActionFirst:        "First",
	ActionLast:         "Last",
	ActionAdd:          "Add",
	ActionAddAll:       "Add all",
	ActionRemove:       "Remove",
	ActionMoveUp:       "Move item up",
	ActionMoveDown:     "Move item down",
	ActionPause:        "Play/pause",
	ActionStop:         "Stop",
	ActionNext:         "Next",
	ActionPrevious:     "Previous",
	ActionSeekBack:     "Seek back 5s",
	ActionSeekForward:  "Seek forward 5s",
	ActionVolumeUp:     "Volume up",
	ActionVolumeDown:   "Volume down",
	ActionMute:         "Mute",
	ActionShuffle:      "Shuffle",
	ActionRepeat:       "Repeat",
	ActionSearch:       "Search",
	ActionSavePlaylist: "Save playlist",
	ActionRescan:       "Rescan",
	ActionClear:        "Clear queue",
	ActionGotoPlaying:  "Go to playing",
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
