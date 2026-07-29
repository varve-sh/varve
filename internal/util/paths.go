package util

import (
	"os"
	"path/filepath"
)

// GetConfigDir returns the OS-appropriate config directory for varve.
//   - macOS:   $HOME/Library/Application Support/varve
//   - Linux:   $XDG_CONFIG_HOME/varve  (fallback: $HOME/.config/varve)
//   - Windows: %AppData%\varve
func GetConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		// Last-resort fallback if home dir can't be resolved.
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "varve")
}

// GetConfigPath returns the full path to config.json.
func GetConfigPath() string {
	return filepath.Join(GetConfigDir(), "config.json")
}
