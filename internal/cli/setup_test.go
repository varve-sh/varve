package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeAgent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude", "claude-code"},
		{"Claude-Code", "claude-code"},
		{"claudecode", "claude-code"},
		{"cursor", "cursor"},
		{"vscode", "vscode"},
		{"vs-code", "vscode"},
		{"code", "vscode"},
		{"opencode", "opencode"},
		{"open-code", "opencode"},
		{"windsurf", "windsurf"},
		{"gemini", "gemini"},
		{"gemini-cli", "gemini"},
		{"unknown", "unknown"},
	}
	for _, c := range cases {
		if got := normalizeAgent(c.in); got != c.want {
			t.Errorf("normalizeAgent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetectAgents_NoneFound(t *testing.T) {
	dir := t.TempDir()
	agents := detectAgents(dir)
	if len(agents) != 1 || agents[0] != "claude-code" {
		t.Errorf("expected fallback to [claude-code], got %v", agents)
	}
}

func TestDetectAgents_FindsExistingDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
	os.MkdirAll(filepath.Join(dir, ".cursor"), 0755)

	agents := detectAgents(dir)
	if len(agents) != 2 {
		t.Fatalf("want 2 agents, got %v", agents)
	}
	found := map[string]bool{}
	for _, a := range agents {
		found[a] = true
	}
	if !found["claude-code"] || !found["cursor"] {
		t.Errorf("expected claude-code and cursor, got %v", agents)
	}
}

func TestDetectAgents_FindsOpencodeAndGemini(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.json"), []byte("{}"), 0644)
	os.MkdirAll(filepath.Join(dir, ".gemini"), 0755)

	agents := detectAgents(dir)
	found := map[string]bool{}
	for _, a := range agents {
		found[a] = true
	}
	if !found["opencode"] {
		t.Errorf("expected opencode to be detected, got %v", agents)
	}
	if !found["gemini"] {
		t.Errorf("expected gemini to be detected, got %v", agents)
	}
}

func TestWriteMCPEntry_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	wrote, err := writeMCPEntry(path, "mcpServers", map[string]interface{}{
		"command": "memtrace",
		"args":    []string{"serve"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true for new file")
	}

	data, _ := os.ReadFile(path)
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid JSON written: %v", err)
	}
	servers := cfg["mcpServers"].(map[string]interface{})
	entry := servers["memtrace"].(map[string]interface{})
	if entry["command"] != "memtrace" {
		t.Errorf("unexpected command: %v", entry["command"])
	}
}

func TestWriteMCPEntry_MergesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	existing := `{
  "mcpServers": {
    "other-tool": {
      "command": "other",
      "args": ["run"]
    }
  }
}`
	os.WriteFile(path, []byte(existing), 0644)

	wrote, err := writeMCPEntry(path, "mcpServers", map[string]interface{}{
		"command": "memtrace",
		"args":    []string{"serve"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true")
	}

	data, _ := os.ReadFile(path)
	var cfg map[string]interface{}
	json.Unmarshal(data, &cfg)
	servers := cfg["mcpServers"].(map[string]interface{})

	if _, ok := servers["other-tool"]; !ok {
		t.Error("existing entry should be preserved")
	}
	if _, ok := servers["memtrace"]; !ok {
		t.Error("memtrace entry should be added")
	}
}

func TestWriteMCPEntry_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	entry := map[string]interface{}{"command": "memtrace", "args": []string{"serve"}}

	wrote, err := writeMCPEntry(path, "mcpServers", entry)
	if err != nil || !wrote {
		t.Fatalf("first write failed: wrote=%v err=%v", wrote, err)
	}

	wrote, err = writeMCPEntry(path, "mcpServers", entry)
	if err != nil {
		t.Fatalf("second write error: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false on second call (already present)")
	}
}

func TestWriteMCPEntry_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "mcp.json") // .claude/ does not exist yet

	wrote, err := writeMCPEntry(path, "mcpServers", map[string]interface{}{
		"command": "memtrace",
		"args":    []string{"serve"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestWriteMCPEntry_VSCodeFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	_, err := writeMCPEntry(path, "servers", map[string]interface{}{
		"type":    "stdio",
		"command": "memtrace",
		"args":    []string{"serve"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(path)
	var cfg map[string]interface{}
	json.Unmarshal(data, &cfg)

	if _, ok := cfg["servers"]; !ok {
		t.Error("expected 'servers' key for vscode format")
	}
	if _, ok := cfg["mcpServers"]; ok {
		t.Error("should not have 'mcpServers' key in vscode format")
	}
}

func TestSetupAgent_Opencode(t *testing.T) {
	dir := t.TempDir()
	wrote, err := setupAgent("opencode", dir, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true")
	}

	data, _ := os.ReadFile(filepath.Join(dir, "opencode.json"))
	var cfg map[string]interface{}
	json.Unmarshal(data, &cfg)
	mcp := cfg["mcp"].(map[string]interface{})
	entry := mcp["memtrace"].(map[string]interface{})
	if entry["type"] != "local" {
		t.Errorf("expected type=local, got %v", entry["type"])
	}
}

func TestSetupAgent_Gemini(t *testing.T) {
	dir := t.TempDir()
	wrote, err := setupAgent("gemini", dir, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true")
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".gemini", "settings.json"))
	var cfg map[string]interface{}
	json.Unmarshal(data, &cfg)
	servers := cfg["mcpServers"].(map[string]interface{})
	if _, ok := servers["memtrace"]; !ok {
		t.Error("memtrace entry not found in .gemini/settings.json")
	}
}

func TestSetupAgent_UnknownAgent(t *testing.T) {
	_, err := setupAgent("notanagent", t.TempDir(), false)
	if err == nil {
		t.Error("expected error for unknown agent")
	}
}

// ADR-0002 Migration §4a, ruled immediate scope by ADR-0001 Amendment 2 (A2.4).
// Every shipped template must state three things, because the behaviour they
// describe changed with the Phase 2 schema port and not with the packer:
// agent-saved decisions land proposed and do not bind; fact/event are note
// synonyms; forget maps onto the lifecycle, never a hard delete.
//
// ADR-0001 falsifier 1's 30-day clock starts when this ships — measuring the
// confirm gate against agents that were never told the rule changed measures
// template staleness, not UX friction.
func TestInstructionTemplates_StateTheQuarantine(t *testing.T) {
	for _, snippet := range []struct{ name, text string }{
		{"CLAUDE.md", claudeMdSnippet},
		{"cursor rules", cursorRulesSnippet},
		{"copilot", copilotInstructionsSnippet},
		{"windsurf", windsurfRulesSnippet},
		{"gemini", geminiMdSnippet},
	} {
		for _, want := range []string{
			"proposed",                 // (1) it does not bind until accepted
			"memtrace decision accept", // and how a human accepts it
			"synonyms for note",        // (2) fact/event are notes
			"not a delete",             // (3) forget maps onto the lifecycle
		} {
			if !strings.Contains(snippet.text, want) {
				t.Errorf("%s template does not mention %q", snippet.name, want)
			}
		}
	}
}
