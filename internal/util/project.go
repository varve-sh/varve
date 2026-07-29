package util

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LegacyStoreDir is the pre-rename store directory. It is still resolved, and
// deliberately not migrated on open.
//
// The rename memtrace → varve moves the store from .memtrace/memtrace.db to
// .varve/varve.db. Every install that predates the rename is the founder's own,
// and orphaning that store — looking only in the new location, finding nothing,
// and reporting an empty corpus — is the exact failure this branch already shipped
// once and caught in review (a migration that silently emptied the store while
// exiting 0). So the legacy path is resolved, used in place, and *announced*:
// nothing moves without the user asking, and nothing is silently invisible.
const LegacyStoreDir = ".memtrace"

// StoreDir is the current store directory.
const StoreDir = ".varve"

// FindProjectRoot walks up from startDir looking for .varve/, then the legacy
// .memtrace/, then .git/. Returns the absolute path of the project root, or ""
// if not found.
func FindProjectRoot(startDir string) string {
	// First pass: an already-initialized store, current location or legacy.
	for _, marker := range []string{StoreDir, LegacyStoreDir} {
		dir := startDir
		for {
			if dirExists(filepath.Join(dir, marker)) {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// Second pass: look for .git/ to infer project root
	dir := startDir
	for {
		if dirExists(filepath.Join(dir, ".git")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// GetProjectDbPath returns the path to the SQLite database for a project root,
// preferring the current location and falling back to the legacy one when only
// that exists. A fresh project always gets the current location.
func GetProjectDbPath(projectRoot string) string {
	path, _ := ResolveStore(projectRoot)
	return path
}

// ResolveStore returns the database path to use and whether it is the legacy
// location. Callers that print anything to a user should announce a legacy
// resolution once — see StoreNotice.
func ResolveStore(projectRoot string) (path string, legacy bool) {
	current := filepath.Join(projectRoot, StoreDir, "varve.db")
	if fileExists(current) {
		return current, false
	}
	old := filepath.Join(projectRoot, LegacyStoreDir, "memtrace.db")
	if fileExists(old) {
		return old, true
	}
	return current, false
}

// StoreNotice is the one-line disclosure for a legacy store. Empty when the
// store is already at the current location.
//
// It names the command rather than describing the situation: a notice a user
// cannot act on is noise, and noise on every invocation trains them to stop
// reading the line that will one day matter.
func StoreNotice(projectRoot string) string {
	if _, legacy := ResolveStore(projectRoot); !legacy {
		return ""
	}
	return "using the pre-rename store at " + LegacyStoreDir +
		"/memtrace.db — move it with `varve store move`"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ProjectEntry represents a single project in the config file.
type ProjectEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// EmbedConfig holds optional embedding API settings persisted in the global config.
// Environment variables always take precedence over these values.
type EmbedConfig struct {
	Key      string `json:"key,omitempty"`
	URL      string `json:"url,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"` // "" or "auto" = auto-detect; "disabled" = off
}

// ProjectConfig represents the contents of the global config.json.
type ProjectConfig struct {
	Projects map[string]ProjectEntry `json:"projects"`
	Embed    EmbedConfig             `json:"embed,omitempty"`
}

// GetProjectConfig reads and returns the global config.
// Returns an empty config (not an error) if the file doesn't exist yet.
// If a legacy config exists at ~/.config/varve/config.json and contains
// projects not present in the primary config, they are merged in and the
// merged result is saved to the primary location.
func GetProjectConfig() *ProjectConfig {
	primary := GetConfigPath()
	cfg := readConfig(primary)

	// Check for a legacy config at ~/.config/varve/config.json (used before
	// os.UserConfigDir() was adopted). Merge any projects that are missing from
	// the primary config, then persist so future reads are self-contained.
	legacy := legacyConfigPath()
	if legacy != "" && legacy != primary {
		if lcfg := readConfig(legacy); len(lcfg.Projects) > 0 {
			merged := false
			for k, v := range lcfg.Projects {
				if _, exists := cfg.Projects[k]; !exists {
					cfg.Projects[k] = v
					merged = true
				}
			}
			if merged {
				_ = os.MkdirAll(GetConfigDir(), 0755)
				_ = SaveProjectConfig(cfg)
			}
		}
	}

	return cfg
}

// readConfig reads and parses a config file. Returns an empty config on any error.
func readConfig(path string) *ProjectConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return &ProjectConfig{Projects: make(map[string]ProjectEntry)}
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &ProjectConfig{Projects: make(map[string]ProjectEntry)}
	}
	if cfg.Projects == nil {
		cfg.Projects = make(map[string]ProjectEntry)
	}
	return &cfg
}

// legacyConfigPath returns the old ~/.config/varve/config.json path that was
// used before os.UserConfigDir() was adopted. Returns "" if it cannot be determined.
func legacyConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "varve", "config.json")
}

// SaveProjectConfig writes the config to disk, creating the directory if needed.
func SaveProjectConfig(cfg *ProjectConfig) error {
	if err := os.MkdirAll(GetConfigDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(GetConfigPath(), data, 0644)
}

// dirExists returns true if the given path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
