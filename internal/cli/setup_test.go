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
		"command": "varve",
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
	entry := servers["varve"].(map[string]interface{})
	if entry["command"] != "varve" {
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
		"command": "varve",
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
	if _, ok := servers["varve"]; !ok {
		t.Error("varve entry should be added")
	}
}

func TestWriteMCPEntry_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	entry := map[string]interface{}{"command": "varve", "args": []string{"serve"}}

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
		"command": "varve",
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
		"command": "varve",
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
	entry := mcp["varve"].(map[string]interface{})
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
	if _, ok := servers["varve"]; !ok {
		t.Error("varve entry not found in .gemini/settings.json")
	}
}

// The paths setup wrote for years — .claude/mcp.json and ~/.claude/mcp.json —
// are not read by Claude Code, which loads project MCP servers from .mcp.json
// and user-scoped ones from ~/.claude.json. Setup reported success and the
// agent came up with no memory tools.
func TestSetupAgent_ClaudeCodeWritesTheConfigClaudeCodeReads(t *testing.T) {
	dir := t.TempDir()
	setupNotices = nil
	defer func() { setupNotices = nil }()

	wrote, err := setupAgent("claude-code", dir, false)
	if err != nil || !wrote {
		t.Fatalf("setupAgent = %v, %v", wrote, err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatalf("nothing written to .mcp.json, the file Claude Code loads: %v", err)
	}
	var cfg map[string]map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("invalid JSON written: %v", err)
	}
	if _, ok := cfg["mcpServers"]["varve"]; !ok {
		t.Errorf("varve entry missing from .mcp.json: %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "mcp.json")); err == nil {
		t.Error("setup still writes .claude/mcp.json, which nothing reads")
	}
}

// A config left by an older setup is indistinguishable, to a user debugging
// missing memory tools, from a working one. Re-running setup removes it.
func TestSetupAgent_RemovesTheConfigThatNeverWorked(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, ".claude", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy,
		[]byte(`{"mcpServers":{"varve":{"command":"varve","args":["serve"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setupNotices = nil
	defer func() { setupNotices = nil }()

	if _, err := setupAgent("claude-code", dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Error(".claude/mcp.json survived, still looking like a working config")
	}
	if len(setupNotices) == 0 || !strings.Contains(strings.Join(setupNotices, " "), ".claude/mcp.json") {
		t.Errorf("a file the user owns was changed silently: %v", setupNotices)
	}
}

// Only varve's own entry is this tool's to remove. Another server's entry in
// the same file is the user's, however useless the path.
func TestSetupAgent_LeavesOtherServersInTheLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, ".claude", "mcp.json")
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	os.WriteFile(legacy, []byte(
		`{"mcpServers":{"varve":{"command":"varve"},"other":{"command":"other"}}}`), 0o644)
	setupNotices = nil
	defer func() { setupNotices = nil }()

	if _, err := setupAgent("claude-code", dir, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatalf("a file holding another server's entry was deleted: %v", err)
	}
	var cfg map[string]map[string]any
	json.Unmarshal(data, &cfg)
	if _, ok := cfg["mcpServers"]["other"]; !ok {
		t.Errorf("another server's entry was removed: %s", data)
	}
	if _, ok := cfg["mcpServers"]["varve"]; ok {
		t.Errorf("the dead varve entry survived: %s", data)
	}
}

// A project with .mcp.json but no .claude/ is still a Claude Code project, and
// each agent is set up once however many of its markers are present.
func TestDetectAgents_MCPJSONMeansClaudeCode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte("{}"), 0o644)

	if agents := detectAgents(dir); len(agents) != 1 || agents[0] != "claude-code" {
		t.Errorf("detectAgents = %v, want [claude-code]", agents)
	}

	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	if agents := detectAgents(dir); len(agents) != 1 {
		t.Errorf("detectAgents = %v, want claude-code listed once", agents)
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
			{"how a human makes it binding", "varve decision accept"},
			// (2) fact/event are note synonyms
			{"fact/event are notes", "synonyms for note"},
			// (3) an agent's forget records a request — it does not dispose
			{"forget records a request", "disposal request"},
			// §4b: pack-first teaching, which ships with the packer.
			{"pack is the first call", "memory_pack"},
			{"recall is for exploration", "memory_recall"},
			{"forget changes nothing", "changes no status"},
			{"who confirms a disposal, while proposed", "varve decision reject"},
			{"who confirms a disposal, once binding", "varve decision revert"},
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
		{"how a human makes it binding", "varve decision accept"},
		{"fact/event are notes", "synonyms for note"},
		{"forget records a request", "disposal request"},
		{"forget changes nothing", "changes no status"},
		{"who confirms while proposed", "varve decision reject"},
		{"who confirms once binding", "varve decision revert"},
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

// Two servers registering the same memory_* tools against the same store is
// worse than one: the agent sees duplicates and the stale entry points at a
// binary the user is replacing.
func TestWriteMCPEntry_ReplacesThePreRenameServerEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"memtrace":{"command":"memtrace","args":["serve"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setupNotices = nil
	written, err := writeMCPEntry(path, "mcpServers", map[string]interface{}{
		"command": "varve", "args": []string{"serve"},
	})
	if err != nil || !written {
		t.Fatalf("writeMCPEntry = %v, %v", written, err)
	}
	data, _ := os.ReadFile(path)
	var cfg map[string]map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, stale := cfg["mcpServers"]["memtrace"]; stale {
		t.Errorf("the pre-rename entry survived alongside the new one: %s", data)
	}
	if _, ok := cfg["mcpServers"]["varve"]; !ok {
		t.Errorf("the varve entry was not written: %s", data)
	}
	// Changes to a file the user owns are reported, not discovered later.
	if len(setupNotices) == 0 || !strings.Contains(setupNotices[0], "memtrace") {
		t.Errorf("replacing the legacy entry was not reported: %v", setupNotices)
	}
	setupNotices = nil
}

// A pre-rename CLAUDE.md already carries this tool's instructions. Appending a
// second block leaves the file with two sections that disagree about the tool's
// own governance rules.
func TestAppendInstructions_DoesNotDuplicateAPreRenameBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	existing := "# memtrace\n\nUse memory_recall before every task.\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	appendInstructions(path, "# varve\n\nUse memory_pack at session start.\n")
	got, _ := os.ReadFile(path)
	if string(got) != existing {
		t.Errorf("a second instruction block was appended:\n%s", got)
	}
}

// setup used to write `"command": "varve"` unconditionally. Both documented
// install paths — `go install` and `make install` — land the binary in
// $GOPATH/bin, which is not on PATH by default on macOS, so setup reported
// success and produced an MCP entry that could not launch. The agent simply had
// no memory tools and nothing said why.
func TestServeCommand_IsLaunchableWhenTheBinaryIsNotOnPATH(t *testing.T) {
	// An empty PATH guarantees the bare name does not resolve.
	t.Setenv("PATH", "")

	got := serveCommand()
	if got == binaryName {
		t.Fatalf("serveCommand() = %q with an unresolvable PATH; an agent config "+
			"written with a bare name cannot start the server", got)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("serveCommand() = %q, want an absolute path", got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("serveCommand() = %q, which does not exist: %v", got, err)
	}
}

// Even when the bare name resolves in THIS process, the config gets the absolute
// path: setup runs in the user's shell, the MCP server runs in the agent host's
// environment, and on a stock macOS zsh setup a ~/.zshrc PATH export reaches the
// first and not the second.
func TestServeCommand_UsesAnAbsolutePathEvenWhenPATHResolves(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, binaryName)
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got := serveCommand()
	if got == binaryName {
		t.Fatal("serveCommand() returned the bare name; a host that cannot resolve " +
			"it gets no memory tools and no explanation")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("serveCommand() = %q, want an absolute path", got)
	}
}

// "already configured" must mean present AND correct. An entry whose command has
// gone stale — a bare name from before setup used absolute paths, or a path to a
// binary that has since moved — used to survive every re-run while setup reported
// success, which makes re-running setup useless as a repair.
func TestWriteMCPEntry_UpdatesAStaleCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path,
		[]byte(`{"mcpServers":{"varve":{"command":"varve","args":["serve"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setupNotices = nil

	written, err := writeMCPEntry(path, "mcpServers", map[string]interface{}{
		"command": "/opt/varve/bin/varve", "args": []string{"serve"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Fatal("a stale command was reported as already configured")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "/opt/varve/bin/varve") {
		t.Errorf("the stale command was not replaced: %s", data)
	}
	if len(setupNotices) == 0 {
		t.Error("the update was made silently, in a file the user owns")
	}
	setupNotices = nil
}

// An entry that is already correct must still be a no-op, or every setup run
// rewrites files it did not need to touch.
func TestWriteMCPEntry_LeavesACorrectEntryAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	os.WriteFile(path, []byte(`{"mcpServers":{"varve":{"command":"/opt/varve/bin/varve","args":["serve"]}}}`), 0o644)
	setupNotices = nil

	written, err := writeMCPEntry(path, "mcpServers", map[string]interface{}{
		"command": "/opt/varve/bin/varve", "args": []string{"serve"},
	})
	if err != nil || written {
		t.Fatalf("writeMCPEntry rewrote a correct entry: written=%v err=%v", written, err)
	}
	setupNotices = nil
}

// Homebrew installs varve into a versioned Cellar directory and points a stable
// symlink at it. serveCommand used to record the resolved target, so a config
// written on 2.0.0 named
//
//	/opt/homebrew/Cellar/varve/2.0.0/bin/varve
//
// and the next `brew upgrade` deleted that directory. The agent then comes up
// with no memory tools, silently — the failure this function was twice rewritten
// to prevent, arriving through the package manager instead of through PATH.
//
// The fixture is the real Homebrew shape, and the assertion is the one that
// matters: the recorded command still launches something after an upgrade.
func TestServeCommand_SurvivesAPackageManagerUpgrade(t *testing.T) {
	prefix := t.TempDir()
	cellar := filepath.Join(prefix, "Cellar", binaryName, "2.0.0", "bin")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cellar, binaryName), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(binDir, binaryName)
	if err := os.Symlink(filepath.Join(cellar, binaryName), stable); err != nil {
		t.Fatal(err)
	}

	got := resolveServeCommand(stable)
	if got != stable {
		t.Errorf("recorded %q, want the stable symlink %q — a versioned path is one "+
			"upgrade from dead", got, stable)
	}

	// The upgrade: 2.0.0's directory goes away, 2.0.1 appears, the symlink moves.
	next := filepath.Join(prefix, "Cellar", binaryName, "2.0.1", "bin")
	if err := os.MkdirAll(next, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(next, binaryName), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(prefix, "Cellar", binaryName, "2.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stable); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(next, binaryName), stable); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(got); err != nil {
		t.Errorf("the command written before the upgrade no longer exists (%v) — this "+
			"is an agent with no memory tools and nothing saying why", err)
	}
}

// An absolute path is still right when there is no symlink to prefer: the
// interactive-vs-non-interactive PATH reasoning that put it there has not
// changed.
func TestServeCommand_StillRecordsAnAbsolutePathForAPlainBinary(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, binaryName)
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveServeCommand(plain); got != plain {
		t.Errorf("resolveServeCommand(%q) = %q", plain, got)
	}
}

// A path that is not there cannot be recorded as if it were.
func TestServeCommand_FallsBackWhenTheInvocationPathIsGone(t *testing.T) {
	t.Setenv("PATH", "")
	got := resolveServeCommand(filepath.Join(t.TempDir(), "deleted", binaryName))
	if got != binaryName {
		t.Errorf("resolveServeCommand on a missing path = %q, want the bare name as "+
			"the last resort", got)
	}
}
