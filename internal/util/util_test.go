package util

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- FindProjectRoot ---

func TestFindProjectRoot_WithMemtraceDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".memtrace"), 0755); err != nil {
		t.Fatal(err)
	}

	// Searching from the root itself
	got := FindProjectRoot(root)
	if got != root {
		t.Errorf("want %s, got %s", root, got)
	}

	// Searching from a subdirectory should walk up
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	got = FindProjectRoot(sub)
	if got != root {
		t.Errorf("from subdir: want %s, got %s", root, got)
	}
}

func TestFindProjectRoot_WithGitDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	got := FindProjectRoot(sub)
	if got != root {
		t.Errorf("want %s, got %s", root, got)
	}
}

func TestFindProjectRoot_MemtracePreferredOverGit(t *testing.T) {
	// .git at root, .memtrace in subdir — should return the subdir level
	outer := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outer, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(filepath.Join(inner, ".memtrace"), 0755); err != nil {
		t.Fatal(err)
	}

	got := FindProjectRoot(inner)
	if got != inner {
		t.Errorf("want %s (memtrace wins), got %s", inner, got)
	}
}

func TestFindProjectRoot_NotFound(t *testing.T) {
	// Isolated temp dir with nothing in it
	dir := t.TempDir()
	got := FindProjectRoot(dir)
	// May or may not find an ancestor .git — only assert it doesn't panic
	_ = got
}

// --- GetProjectDbPath ---

func TestGetProjectDbPath(t *testing.T) {
	root := "/some/project"
	want := "/some/project/.memtrace/memtrace.db"
	got := GetProjectDbPath(root)
	if got != want {
		t.Errorf("want %s, got %s", want, got)
	}
}

// --- GetConfigDir / GetConfigPath ---

func TestGetConfigDir_ReturnsMemtraceSubdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "") // prevent interference on Linux

	got := GetConfigDir()
	// Platform-agnostic: path must be absolute and end in "memtrace".
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path, got: %s", got)
	}
	if !strings.HasSuffix(got, "memtrace") {
		t.Errorf("expected path ending in 'memtrace', got: %s", got)
	}
}

func TestGetConfigPath_UnderConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	want := filepath.Join(GetConfigDir(), "config.json")
	if got := GetConfigPath(); got != want {
		t.Errorf("want %s, got %s", want, got)
	}
}

// --- GetProjectConfig ---

func TestGetProjectConfig_FileAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	cfg := GetProjectConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Projects) != 0 {
		t.Errorf("expected empty projects map, got %d entries", len(cfg.Projects))
	}
}

func TestGetProjectConfig_ValidFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	cfgDir := GetConfigDir()
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	data := `{"projects":{"/my/proj":{"id":"abc","name":"myproj","created_at":"2024-01-01T00:00:00Z"}}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := GetProjectConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	entry, ok := cfg.Projects["/my/proj"]
	if !ok {
		t.Fatal("expected project entry")
	}
	if entry.ID != "abc" {
		t.Errorf("want id=abc, got %s", entry.ID)
	}
	if entry.Name != "myproj" {
		t.Errorf("want name=myproj, got %s", entry.Name)
	}
}

func TestGetProjectConfig_MalformedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	cfgDir := GetConfigDir()
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := GetProjectConfig()
	if cfg == nil {
		t.Fatal("expected non-nil fallback config")
	}
	if len(cfg.Projects) != 0 {
		t.Error("expected empty projects map on parse error")
	}
}

// --- SaveProjectConfig ---

func TestSaveProjectConfig_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	cfg := &ProjectConfig{
		Projects: map[string]ProjectEntry{
			"/proj/a": {ID: "id1", Name: "project-a", CreatedAt: "2024-06-01T00:00:00Z"},
		},
	}

	if err := SaveProjectConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Read the raw file and verify it's valid JSON
	data, err := os.ReadFile(GetConfigPath())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("invalid JSON written: %v", err)
	}

	// Read via helper and verify round-trip
	got := GetProjectConfig()
	entry, ok := got.Projects["/proj/a"]
	if !ok {
		t.Fatal("project entry missing after round-trip")
	}
	if entry.ID != "id1" {
		t.Errorf("want id=id1, got %s", entry.ID)
	}
}

func TestSaveProjectConfig_CreatesDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	// Config dir does not exist yet
	cfg := &ProjectConfig{Projects: make(map[string]ProjectEntry)}
	if err := SaveProjectConfig(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(GetConfigPath()); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

// --- EmbedConfig ---

func TestEmbedConfig_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	cfg := &ProjectConfig{
		Projects: make(map[string]ProjectEntry),
		Embed: EmbedConfig{
			Key:   "sk-test-key",
			URL:   "http://localhost:11434/v1",
			Model: "nomic-embed-text",
		},
	}
	if err := SaveProjectConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := GetProjectConfig()
	if got.Embed.Key != "sk-test-key" {
		t.Errorf("Embed.Key: want sk-test-key, got %q", got.Embed.Key)
	}
	if got.Embed.URL != "http://localhost:11434/v1" {
		t.Errorf("Embed.URL: want http://localhost:11434/v1, got %q", got.Embed.URL)
	}
	if got.Embed.Model != "nomic-embed-text" {
		t.Errorf("Embed.Model: want nomic-embed-text, got %q", got.Embed.Model)
	}
}

// --- GenerateID ---

func TestGenerateID_NonEmpty(t *testing.T) {
	id := GenerateID()
	if id == "" {
		t.Error("expected non-empty ID")
	}
}

func TestGenerateID_Unique(t *testing.T) {
	ids := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := GenerateID()
		if ids[id] {
			t.Fatalf("duplicate ID generated: %s", id)
		}
		ids[id] = true
	}
}

func TestGenerateID_Length(t *testing.T) {
	// ULIDs are always 26 characters
	id := GenerateID()
	if len(id) != 26 {
		t.Errorf("expected ULID length 26, got %d (%s)", len(id), id)
	}
}
