package xdg

import (
	"path/filepath"
	"testing"
)

func TestDirsFallBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	tests := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"config", ConfigDir, filepath.Join(home, ".config", "player2")},
		{"data", DataDir, filepath.Join(home, ".local", "share", "player2")},
		{"state", StateDir, filepath.Join(home, ".local", "state", "player2")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDirsHonorAbsoluteXDGOverrides(t *testing.T) {
	home := t.TempDir()
	configHome := t.TempDir()
	dataHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	tests := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"config", ConfigDir, filepath.Join(configHome, "player2")},
		{"data", DataDir, filepath.Join(dataHome, "player2")},
		{"state", StateDir, filepath.Join(stateHome, "player2")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDirsIgnoreRelativeXDGOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	t.Setenv("XDG_DATA_HOME", "relative/data")
	t.Setenv("XDG_STATE_HOME", "relative/state")

	tests := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"config", ConfigDir, filepath.Join(home, ".config", "player2")},
		{"data", DataDir, filepath.Join(home, ".local", "share", "player2")},
		{"state", StateDir, filepath.Join(home, ".local", "state", "player2")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDirsErrorWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := ConfigDir(); err == nil {
		t.Fatal("expected error when HOME is unset and no XDG override is present")
	}
}
