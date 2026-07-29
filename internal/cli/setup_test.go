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

// ADR-0002 Migration §4a, as revised by ADR-0001 Amendment 3. Every shipped
// template must state the three normative points, and this is the third time
// the copy has moved — so it is pinned to *observable behaviour* rather than
// to intent, and each assertion names a thing a reader can check by running
// the tool.
//
// ADR-0001 falsifier 1's 30-day clock runs on this copy, so a template that
// describes behaviour the product does not have measures template staleness
// rather than UX friction.
func TestInstructionTemplates_DescribeObservableBehaviour(t *testing.T) {
	for _, snippet := range []struct{ name, text string }{
		{"CLAUDE.md snippet", claudeMdSnippet},
		{"cursor rules", cursorRulesSnippet},
		{"copilot", copilotInstructionsSnippet},
		{"windsurf", windsurfRulesSnippet},
		{"gemini", geminiMdSnippet},
	} {
		for _, want := range []struct{ claim, text string }{
			// (1) an agent-saved decision lands proposed and does not bind
			{"the status a governed save lands in", `"proposed"`},
			{"how a human makes it binding", "memtrace decision accept"},
			// (2) fact/event are note synonyms
			{"fact/event are notes", "synonyms for note"},
			// (3) an agent's forget records a request — it does not dispose
			{"forget records a request", "disposal request"},
			// §4b: pack-first teaching, which ships with the packer.
			{"pack is the first call", "memory_pack"},
			{"recall is for exploration", "memory_recall"},
			{"forget changes nothing", "changes no status"},
			{"who confirms a disposal, while proposed", "memtrace decision reject"},
			{"who confirms a disposal, once binding", "memtrace decision revert"},
		} {
			if !strings.Contains(snippet.text, want.text) {
				t.Errorf("%s template does not state %s (missing %q)",
					snippet.name, want.claim, want.text)
			}
		}
		// The superseded claim must be gone: an agent's forget no longer
		// rejects or reverts anything (A2.4 point 3, superseded by A3.1).
		for _, stale := range []string{
			"forget/delete of a decision maps to reject",
			"never packed into context",
		} {
			if strings.Contains(snippet.text, stale) {
				t.Errorf("%s template still carries superseded copy: %q", snippet.name, stale)
			}
		}
	}
}

// A2.4 names this repo's own CLAUDE.md as one of the templates ("this repo's
// own CLAUDE.md is itself one of the stale templates"), and it is the
// instruction set governing the founder's dogfooding — which is where
// falsifier 1's data comes from. The generated snippet was pinned; the
// hand-maintained file was not, and two of the pinned literals do not appear
// in it verbatim because it is markdown (“ `note` “, “ **`proposed`** “).
// So it could drift and no test would notice (F34).
//
// The assertions are markdown-tolerant: they strip the formatting characters
// rather than demanding the plain-text spelling, because the file is meant to
// be read by a human as well as an agent.
func TestRepoClaudeMD_StatesTheSameNormativeContent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("this repo's CLAUDE.md must exist and be readable: %v", err)
	}
	// Strip the characters markdown uses for emphasis and code spans, so
	// **`proposed`** reads as proposed. Underscores are left alone: they are
	// part of every tool name here (memory_pack), not formatting.
	plain := strings.NewReplacer("`", "", "*", "").Replace(string(raw))

	for _, want := range []struct{ claim, text string }{
		{"a governed save lands proposed", "proposed"},
		{"how a human makes it binding", "memtrace decision accept"},
		{"fact/event are notes", "synonyms for note"},
		{"forget records a request", "disposal request"},
		{"forget changes nothing", "changes no status"},
		{"who confirms while proposed", "memtrace decision reject"},
		{"who confirms once binding", "memtrace decision revert"},
		{"pack is the first call", "memory_pack"},
	} {
		if !strings.Contains(plain, want.text) {
			t.Errorf("this repo's CLAUDE.md does not state %s (missing %q)",
				want.claim, want.text)
		}
	}
	for _, stale := range []string{
		"forget/delete of a decision maps to reject",
		"never packed into context",
		"patch an existing memory by ID (content, tags, type, confidence)",
	} {
		if strings.Contains(plain, stale) {
			t.Errorf("this repo's CLAUDE.md still carries superseded copy: %q", stale)
		}
	}
}
