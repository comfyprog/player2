package tui

import (
	"reflect"
	"strings"
	"testing"

	"player2/internal/config"
)

// defaultKeyTable is the plan's default key table, restated independently of
// the implementation so a drift in either direction fails the test.
var defaultKeyTable = map[Action][]string{
	ActionQuit:         {"q", "ctrl+c"},
	ActionHelp:         {"?"},
	ActionSwitchPane:   {"tab"},
	ActionTab0:         {"alt+0"},
	ActionTab1:         {"alt+1"},
	ActionTab2:         {"alt+2"},
	ActionTab3:         {"alt+3"},
	ActionTab4:         {"alt+4"},
	ActionTab5:         {"alt+5"},
	ActionTab6:         {"alt+6"},
	ActionTab7:         {"alt+7"},
	ActionTab8:         {"alt+8"},
	ActionTab9:         {"alt+9"},
	ActionToggleZoom:   {"z"},
	ActionOpen:         {"enter"},
	ActionPreview:      {"shift+enter"},
	ActionParent:       {"backspace"},
	ActionUp:           {"up", "k"},
	ActionDown:         {"down", "j"},
	ActionPageUp:       {"pgup"},
	ActionPageDown:     {"pgdown"},
	ActionFirst:        {"home"},
	ActionLast:         {"end"},
	ActionAdd:          {"a"},
	ActionAddAll:       {"A"},
	ActionRemove:       {"d"},
	ActionMoveUp:       {"K"},
	ActionMoveDown:     {"J"},
	ActionPause:        {"space"},
	ActionStop:         {"x"},
	ActionNext:         {"n"},
	ActionPrevious:     {"b"},
	ActionSeekBack:     {"left"},
	ActionSeekForward:  {"right"},
	ActionVolumeUp:     {"+", "="},
	ActionVolumeDown:   {"-", "_"},
	ActionMute:         {"m"},
	ActionShuffle:      {"s"},
	ActionRepeat:       {"r"},
	ActionSearch:       {"/"},
	ActionToggleTags:   {"t"},
	ActionInfo:         {"i"},
	ActionSavePlaylist: {"w"},
	ActionRescan:       {"R"},
	ActionClear:        {"c"},
	ActionGotoPlaying:  {"g"},
}

var wantActionOrder = []Action{
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
	ActionToggleTags,
	ActionInfo,
	ActionSavePlaylist,
	ActionRescan,
	ActionClear,
	ActionGotoPlaying,
}

func TestDefaultKeyMap(t *testing.T) {
	km, err := NewKeyMap(config.Default())
	if err != nil {
		t.Fatalf("NewKeyMap: %v", err)
	}

	for action, want := range defaultKeyTable {
		if got := km.Keys(action); !reflect.DeepEqual(got, want) {
			t.Errorf("Keys(%q) = %v, want %v", action, got, want)
		}
		for _, key := range want {
			got, ok := km.Action(key)
			if !ok {
				t.Errorf("Action(%q) not bound, want %q", key, action)
				continue
			}
			if got != action {
				t.Errorf("Action(%q) = %q, want %q", key, got, action)
			}
			if !km.Matches(action, key) {
				t.Errorf("Matches(%q, %q) = false, want true", action, key)
			}
		}
	}

	bindings := km.Bindings()
	if len(bindings) != len(wantActionOrder) {
		t.Fatalf("len(Bindings()) = %d, want %d", len(bindings), len(wantActionOrder))
	}
	for i, binding := range bindings {
		if binding.Action != wantActionOrder[i] {
			t.Errorf("Bindings()[%d].Action = %q, want %q", i, binding.Action, wantActionOrder[i])
		}
		if binding.Description == "" {
			t.Errorf("Bindings()[%d] (%q) has no description", i, binding.Action)
		}
	}
}

func TestKeyMapIsCaseSensitive(t *testing.T) {
	km, err := NewKeyMap(config.Default())
	if err != nil {
		t.Fatalf("NewKeyMap: %v", err)
	}
	if got, ok := km.Action("A"); !ok || got != ActionAddAll {
		t.Fatalf(`Action("A") = %q, %v; want %q, true`, got, ok, ActionAddAll)
	}
	if got, ok := km.Action("a"); !ok || got != ActionAdd {
		t.Fatalf(`Action("a") = %q, %v; want %q, true`, got, ok, ActionAdd)
	}
	if km.Matches(ActionAdd, "A") {
		t.Error(`Matches(add, "A") = true, want false`)
	}
}

func TestKeyMapConfigOverlay(t *testing.T) {
	cfg := config.Default()
	cfg.Keys = map[string][]string{"quit": {"Q"}}

	km, err := NewKeyMap(cfg)
	if err != nil {
		t.Fatalf("NewKeyMap: %v", err)
	}
	if got := km.Keys(ActionQuit); !reflect.DeepEqual(got, []string{"Q"}) {
		t.Errorf("Keys(quit) = %v, want [Q]", got)
	}
	if _, ok := km.Action("q"); ok {
		t.Error(`Action("q") still bound after overlay replaced quit`)
	}
	if got, ok := km.Action("Q"); !ok || got != ActionQuit {
		t.Errorf(`Action("Q") = %q, %v; want %q, true`, got, ok, ActionQuit)
	}
	// Actions not mentioned by the partial overlay keep their defaults.
	if got := km.Keys(ActionHelp); !reflect.DeepEqual(got, []string{"?"}) {
		t.Errorf("Keys(help) = %v, want [?]", got)
	}
	if got := km.Keys(ActionNext); !reflect.DeepEqual(got, []string{"n"}) {
		t.Errorf("Keys(next) = %v, want [n]", got)
	}
}

func TestKeyMapEmptyBindingIsAllowed(t *testing.T) {
	cfg := config.Default()
	cfg.Keys = map[string][]string{"quit": {}}

	km, err := NewKeyMap(cfg)
	if err != nil {
		t.Fatalf("NewKeyMap: %v", err)
	}
	if got := km.Keys(ActionQuit); len(got) != 0 {
		t.Errorf("Keys(quit) = %v, want empty", got)
	}
	if _, ok := km.Action("q"); ok {
		t.Error(`Action("q") still bound after quit was emptied`)
	}
}

func TestKeyMapRejectsDuplicateBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Keys = map[string][]string{"help": {"q"}}

	_, err := NewKeyMap(cfg)
	if err == nil {
		t.Fatal("NewKeyMap accepted duplicate binding")
	}
	for _, want := range []string{`"q"`, "quit", "help"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestKeyMapRejectsDuplicateKeyWithinAction(t *testing.T) {
	cfg := config.Default()
	cfg.Keys = map[string][]string{"quit": {"q", "q"}}

	_, err := NewKeyMap(cfg)
	if err == nil {
		t.Fatal("NewKeyMap accepted duplicate key within one action")
	}
	for _, want := range []string{`"q"`, "quit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestKeyMapRejectsEmptyKey(t *testing.T) {
	cfg := config.Default()
	cfg.Keys = map[string][]string{"quit": {"q", ""}}

	_, err := NewKeyMap(cfg)
	if err == nil {
		t.Fatal("NewKeyMap accepted an empty key")
	}
	for _, want := range []string{"empty", "quit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestKeyMapRejectsUnknownAction(t *testing.T) {
	cfg := config.Default()
	cfg.Keys = map[string][]string{"dance": {"z"}}

	_, err := NewKeyMap(cfg)
	if err == nil {
		t.Fatal("NewKeyMap accepted unknown action")
	}
	for _, want := range []string{"dance", `"z"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestTabBindingsAreConfigurable(t *testing.T) {
	cfg := config.Default()
	cfg.Keys["tab_3"] = []string{"f3"}

	km, err := NewKeyMap(cfg)
	if err != nil {
		t.Fatalf("NewKeyMap: %v", err)
	}
	if got := km.Keys(ActionTab3); !reflect.DeepEqual(got, []string{"f3"}) {
		t.Errorf("Keys(ActionTab3) = %v, want [f3]", got)
	}
	if got, ok := km.Action("f3"); !ok || got != ActionTab3 {
		t.Errorf("Action(f3) = %q, %v; want %q, true", got, ok, ActionTab3)
	}
}

func TestDuplicateTabBindingRejected(t *testing.T) {
	cfg := config.Default()
	cfg.Keys["help"] = []string{"alt+3"}

	_, err := NewKeyMap(cfg)
	if err == nil {
		t.Fatal("NewKeyMap accepted a duplicate tab binding")
	}
	if !strings.Contains(err.Error(), "alt+3") {
		t.Errorf("error %q does not mention the duplicate key", err)
	}
}
