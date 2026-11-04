// Package xdg resolves per-application directories following the XDG Base
// Directory Specification.
//
// Each helper returns the player2-specific directory, for example
// $XDG_CONFIG_HOME/player2. Empty or relative XDG values are ignored and the
// documented home-directory fallbacks are used instead.
package xdg

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "player2"

// ConfigDir returns the directory holding player2's configuration
// (e.g. $XDG_CONFIG_HOME/player2).
func ConfigDir() (string, error) {
	return appDir("XDG_CONFIG_HOME", ".config")
}

// DataDir returns the directory holding player2's data files
// (e.g. $XDG_DATA_HOME/player2).
func DataDir() (string, error) {
	return appDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
}

// StateDir returns the directory holding player2's state and log files
// (e.g. $XDG_STATE_HOME/player2).
func StateDir() (string, error) {
	return appDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
}

func appDir(envVar, homeFallback string) (string, error) {
	if base := absoluteEnv(envVar); base != "" {
		return filepath.Join(base, appName), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for %s fallback: %w", envVar, err)
	}
	if home == "" {
		return "", fmt.Errorf("resolve home directory for %s fallback: home directory is empty", envVar)
	}
	return filepath.Join(home, homeFallback, appName), nil
}

// absoluteEnv returns the value of envVar when it is a non-empty absolute
// path, and "" otherwise.
func absoluteEnv(envVar string) string {
	value := os.Getenv(envVar)
	if value == "" || !filepath.IsAbs(value) {
		return ""
	}
	return value
}
