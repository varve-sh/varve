package util

import (
	"os"
	"path/filepath"
	"testing"
)

// Guards the isolation contract itself: on any platform, sandboxing a test's
// config must actually move os.UserConfigDir(). This is the assertion whose
// absence let the whole cli suite share one real config file on Linux.
func TestConfigIsolation_MovesUserConfigDirOnEveryPlatform(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.HasPrefix(dir, home) {
		t.Fatalf("os.UserConfigDir() = %s, which is outside the sandbox %s — "+
			"setting HOME alone does not isolate config on this platform", dir, home)
	}
	if !filepath.HasPrefix(GetConfigDir(), home) {
		t.Fatalf("GetConfigDir() = %s escapes the sandbox", GetConfigDir())
	}
}
