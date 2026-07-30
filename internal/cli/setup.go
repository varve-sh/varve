package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	var global bool

	cmd := &cobra.Command{
		Use:   "setup [agent]",
		Short: "Wire varve into your AI coding agent's MCP config",
		Long: `Adds varve to your agent's MCP configuration so it is available in every session.

Supported agents:
  claude-code   Writes to .mcp.json (or ~/.claude.json with --global)
  cursor        Writes to .cursor/mcp.json
  vscode        Writes to .vscode/mcp.json
  opencode      Writes to opencode.json (project root)
  windsurf      Writes to ~/.codeium/windsurf/mcp_config.json
  gemini        Writes to .gemini/settings.json

If no agent is specified, varve auto-detects which agents are configured in the
current directory and sets up all detected ones. Falls back to claude-code if none
are detected.

The command is idempotent — running it again is safe.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			projectRoot := cwd

			var agents []string
			if len(args) == 1 {
				agents = []string{normalizeAgent(args[0])}
			} else {
				agents = detectAgents(projectRoot)
			}

			any := false
			for _, agent := range agents {
				done, err := setupAgent(agent, projectRoot, global)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", agent, err)
					continue
				}
				if done {
					fmt.Printf("  [ok] %-12s configured\n", agent)
				} else {
					fmt.Printf("  [ok] %-12s already configured\n", agent)
				}
				any = true
			}
			for _, n := range setupNotices {
				fmt.Printf("  [note] %s\n", n)
			}
			setupNotices = nil

			if !any {
				fmt.Println("No agents set up. Specify an agent:")
				fmt.Println("  varve setup claude-code")
				fmt.Println("  varve setup cursor")
				fmt.Println("  varve setup vscode")
				fmt.Println("  varve setup opencode")
				fmt.Println("  varve setup windsurf")
				fmt.Println("  varve setup gemini")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&global, "global", false, "Install at user scope (~/.claude.json) instead of project scope (claude-code only)")
	return cmd
}

func normalizeAgent(s string) string {
	switch strings.ToLower(s) {
	case "claude", "claude-code", "claudecode":
		return "claude-code"
	case "cursor":
		return "cursor"
	case "vscode", "vs-code", "code":
		return "vscode"
	case "opencode", "open-code":
		return "opencode"
	case "windsurf":
		return "windsurf"
	case "gemini", "gemini-cli":
		return "gemini"
	default:
		return s
	}
}

func detectAgents(projectRoot string) []string {
	var found []string
	checks := []struct {
		path  string
		agent string
	}{
		{".claude", "claude-code"},
		// A project already carrying MCP servers Claude Code reads is a Claude
		// Code project even without a .claude/ directory.
		{".mcp.json", "claude-code"},
		{".cursor", "cursor"},
		{".vscode", "vscode"},
		{"opencode.json", "opencode"},
		{".gemini", "gemini"},
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(projectRoot, c.path)); err != nil {
			continue
		}
		if seen[c.agent] {
			continue
		}
		seen[c.agent] = true
		found = append(found, c.agent)
	}
	if len(found) == 0 {
		return []string{"claude-code"}
	}
	return found
}

// setupAgent writes the varve MCP entry into the agent's config file.
// Returns (true, nil) if the entry was written, (false, nil) if it was already present.
func setupAgent(agent, projectRoot string, global bool) (bool, error) {
	switch agent {
	case "claude-code":
		var configPath string
		if global {
			home, err := os.UserHomeDir()
			if err != nil {
				return false, fmt.Errorf("could not find home directory: %w", err)
			}
			configPath = claudeUserConfigPath(home)
		} else {
			configPath = claudeProjectConfigPath(projectRoot)
		}
		dropLegacyClaudeEntry(projectRoot, global)
		written, err := writeMCPEntry(configPath, "mcpServers", map[string]interface{}{
			"command": serveCommand(),
			"args":    []string{"serve"},
		})
		if err != nil {
			return false, err
		}
		if !global {
			addToClaudeMd(projectRoot)
		}
		return written, nil

	case "cursor":
		configPath := filepath.Join(projectRoot, ".cursor", "mcp.json")
		written, err := writeMCPEntry(configPath, "mcpServers", map[string]interface{}{
			"command": serveCommand(),
			"args":    []string{"serve"},
		})
		if err != nil {
			return false, err
		}
		addToCursorRules(projectRoot)
		return written, nil

	case "vscode":
		configPath := filepath.Join(projectRoot, ".vscode", "mcp.json")
		written, err := writeMCPEntry(configPath, "servers", map[string]interface{}{
			"type":    "stdio",
			"command": serveCommand(),
			"args":    []string{"serve"},
		})
		if err != nil {
			return false, err
		}
		addToCopilotInstructions(projectRoot)
		return written, nil

	case "opencode":
		configPath := filepath.Join(projectRoot, "opencode.json")
		return writeMCPEntry(configPath, "mcp", map[string]interface{}{
			"type":    "local",
			"command": []string{serveCommand(), "serve"},
		})

	case "windsurf":
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("could not find home directory: %w", err)
		}
		configPath := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
		written, err := writeMCPEntry(configPath, "mcpServers", map[string]interface{}{
			"command": serveCommand(),
			"args":    []string{"serve"},
		})
		if err != nil {
			return false, err
		}
		addToWindsurfRules(projectRoot)
		return written, nil

	case "gemini":
		configPath := filepath.Join(projectRoot, ".gemini", "settings.json")
		written, err := writeMCPEntry(configPath, "mcpServers", map[string]interface{}{
			"command": serveCommand(),
			"args":    []string{"serve"},
		})
		if err != nil {
			return false, err
		}
		addToGeminiMd(projectRoot)
		return written, nil

	default:
		return false, fmt.Errorf("unknown agent %q — supported: claude-code, cursor, vscode, opencode, windsurf, gemini", agent)
	}
}

// memtraceInstructionsCore is the shared instructions text injected into every
// agent's rules/instructions file. All agent-specific snippets derive from this.
const memtraceInstructionsCore = `This project has the varve MCP server connected. Use its tools for all memory operations — never use built-in memory tools.

Memory tools: memory_pack, memory_recall, memory_save, memory_get, memory_update, memory_forget, memory_context, memory_prompt

Rules:
- Before touching files — call memory_pack with the file paths you are about to read or edit, and a one-line task. It returns everything binding on those files, deduplicated, inside a token budget. This is the first call of a task, not an optional one.
- Mid-task, to explore — call memory_recall. Pack answers "what binds these files"; recall answers "what do we know about X". Use recall when you have a question, not to bootstrap.
- Before committing — call memory_recall to check for commit conventions.
- Learn something new — call memory_save to persist it.
- User says forget/delete/remove — call memory_forget.
- Never write memory files manually or use built-in memory features.

What actually happens when you call these tools:

- memory_save with type=decision or convention creates a row with status
  "proposed". It does not bind, and memory_context will not return it as
  context. A human runs "varve decision accept <id>" to make it binding.
  Say that the proposal is waiting; do not report it as adopted.
- memory_save with type=fact or event creates a note. fact and event are
  synonyms for note: retrievable, ungoverned, no lifecycle. memory_update
  cannot turn a note into a decision — "varve decision promote <id>" does
  that, and it is a human action.
- memory_forget on a note deletes it. memory_forget on a decision or
  convention deletes nothing and changes no status: it records a
  disposal request and returns it as pending. A human confirms with
  "varve decision reject <id>" while the decision is proposed, or
  "varve decision revert <id>" once it is binding. Tell the user the
  request is waiting for them.
- memory_recall and memory_get return proposals, marked PROPOSED. Treat
  anything so marked as a pending proposal, not as law.
- memory_context never returns a proposal as content; it reports proposals as
  a trailing count with their ids. Everything else it returns is binding or
  ungoverned.
- memory_pack is budget-governed: it serves the highest-ranked binding
  decisions in full, elides bodies to "[body elided — memory_get <id>]" when
  the budget runs short, and names everything it left out in the footer. If
  something you need was elided or omitted, memory_get it by id or raise
  budget_tokens. Proposals are never in the body, only in the footer count.`

// claudeMdSnippet wraps the core in a CLAUDE.md section.
const claudeMdSnippet = "\n## varve (memory)\n\n" + memtraceInstructionsCore + "\n"

// cursorRulesSnippet wraps the core in Cursor's MDC format.
const cursorRulesSnippet = "---\ndescription: varve memory instructions\nalwaysApply: true\n---\n\n" + memtraceInstructionsCore + "\n"

// copilotInstructionsSnippet wraps the core for .github/copilot-instructions.md.
const copilotInstructionsSnippet = "\n## varve\n\n" + memtraceInstructionsCore + "\n"

// windsurfRulesSnippet wraps the core for .windsurfrules.
const windsurfRulesSnippet = "\n# varve\n\n" + memtraceInstructionsCore + "\n"

// geminiMdSnippet wraps the core for GEMINI.md.
const geminiMdSnippet = "\n## varve\n\n" + memtraceInstructionsCore + "\n"

// addToClaudeMd appends varve instructions to CLAUDE.md if not already present.
func addToClaudeMd(projectRoot string) {
	appendInstructions(filepath.Join(projectRoot, "CLAUDE.md"), claudeMdSnippet)
}

// addToCursorRules writes a varve rule file to .cursor/rules/varve.mdc.
// It is idempotent — if the file already exists it is left unchanged.
func addToCursorRules(projectRoot string) {
	rulesDir := filepath.Join(projectRoot, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return
	}
	rulePath := filepath.Join(rulesDir, "varve.mdc")
	if _, err := os.Stat(rulePath); err == nil {
		return // already exists
	}
	_ = os.WriteFile(rulePath, []byte(cursorRulesSnippet), 0644)
}

// addToCopilotInstructions appends varve instructions to .github/copilot-instructions.md.
func addToCopilotInstructions(projectRoot string) {
	dir := filepath.Join(projectRoot, ".github")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	appendInstructions(filepath.Join(dir, "copilot-instructions.md"), copilotInstructionsSnippet)
}

// addToWindsurfRules appends varve instructions to .windsurfrules.
func addToWindsurfRules(projectRoot string) {
	appendInstructions(filepath.Join(projectRoot, ".windsurfrules"), windsurfRulesSnippet)
}

// addToGeminiMd appends varve instructions to GEMINI.md.
func addToGeminiMd(projectRoot string) {
	appendInstructions(filepath.Join(projectRoot, "GEMINI.md"), geminiMdSnippet)
}

// appendInstructions appends snippet to path unless this tool's instructions
// are already there — under either name.
//
// Checking only for "varve" would append a second block to every agent file
// written before the rename, leaving the file with two instruction sections
// that disagree about the tool's own governance rules. The pre-rename block is
// left in place rather than rewritten: it is the user's file, and a setup
// command that edits prose it did not write is worse than one that declines.
// `varve setup --force` is the way to replace it.
func appendInstructions(path, snippet string) {
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "varve") || strings.Contains(string(data), "memtrace") {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(snippet)
}

// setupNotices collects changes setup made to files the user owns, so they are
// reported rather than discovered. Drained by the setup command.
var setupNotices []string

// serveCommand is the command an agent config should launch: the absolute path
// to this executable whenever it can be determined.
//
// The obvious implementation prefers the bare name when exec.LookPath finds it,
// on the reasoning that an absolute path baked into a config goes stale. That is
// backwards, and testing it showed why: `varve setup` runs in the user's
// interactive shell, while the MCP server is spawned by the agent host in a
// non-interactive environment. Those are different environments. On a stock
// macOS zsh setup, `export PATH=...` in ~/.zshrc applies to the first and not
// the second — measured: `zsh -i -l -c 'command -v varve'` resolves, `zsh -c`
// does not — so LookPath succeeding at setup time proves nothing about whether
// the host can launch it.
//
// Both failure modes are silent, so pick the recoverable one. A stale absolute
// path breaks only after the binary is moved or removed, and re-running
// `varve setup` fixes it. A bare name that the host cannot resolve breaks
// immediately, on a machine where the user just watched setup report success.
func serveCommand() string {
	if self, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			return resolved
		}
		return self
	}
	// Only if the executable cannot be located at all.
	if _, err := exec.LookPath(binaryName); err == nil {
		return binaryName
	}
	return binaryName
}

const binaryName = "varve"

// Claude Code reads project-scoped MCP servers from .mcp.json at the project
// root, and user-scoped ones from ~/.claude.json. It reads neither
// .claude/mcp.json nor ~/.claude/mcp.json — those paths look plausible next to
// .claude/settings.json, but nothing loads them, so setup reported success and
// the agent started with no memory tools and no explanation. Same failure shape
// as the bare-name command bug: silent, and indistinguishable from varve being
// broken.
func claudeProjectConfigPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".mcp.json")
}

func claudeUserConfigPath(home string) string {
	return filepath.Join(home, ".claude.json")
}

// dropLegacyClaudeEntry removes a varve entry left in one of the paths setup
// used to write, which Claude Code never read. Leaving it costs nothing at
// runtime but it is indistinguishable, to anyone reading the file, from a
// working config — so a user debugging "why are there no memory tools" finds
// exactly what the docs told them to look for. Re-running setup is the
// documented repair, so it removes the decoy.
func dropLegacyClaudeEntry(projectRoot string, global bool) {
	path := filepath.Join(projectRoot, ".claude", "mcp.json")
	label := ".claude/mcp.json"
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		path = filepath.Join(home, ".claude", "mcp.json")
		label = "~/.claude/mcp.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}
	servers, _ := cfg["mcpServers"].(map[string]interface{})
	_, hasVarve := servers["varve"]
	_, hasLegacy := servers["memtrace"]
	if !hasVarve && !hasLegacy {
		return
	}
	delete(servers, "varve")
	delete(servers, "memtrace")

	// A file this tool created for itself, now empty, is removed rather than
	// left as an empty shell the user has to wonder about. A file with anything
	// else in it belongs to the user and is only edited.
	if len(servers) == 0 && len(cfg) == 1 {
		if err := os.Remove(path); err == nil {
			setupNotices = append(setupNotices,
				"removed "+label+" — Claude Code never read it (the entry now lives in the config it does read)")
		}
		return
	}
	cfg["mcpServers"] = servers
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(path, append(out, '\n'), 0644); err == nil {
		setupNotices = append(setupNotices,
			"removed the varve entry from "+label+" — Claude Code never read it")
	}
}

// writeMCPEntry reads (or creates) the JSON config at path, merges the varve
// entry under the given key, and writes it back. Returns false if already present.
func writeMCPEntry(path, serversKey string, entry map[string]interface{}) (bool, error) {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, fmt.Errorf("creating directory: %w", err)
	}

	// Read existing config or start fresh
	var cfg map[string]interface{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return false, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	if cfg == nil {
		cfg = make(map[string]interface{})
	}

	// Ensure the servers key exists as a map
	servers, _ := cfg[serversKey].(map[string]interface{})
	if servers == nil {
		servers = make(map[string]interface{})
	}

	// Present is not the same as correct. This returned early on any existing
	// `varve` key, so an entry whose command had gone stale — a bare name written
	// before setup learned to use an absolute path, or a path to a binary that has
	// since moved — survived every re-run while setup reported "already
	// configured". Re-running setup is the documented way to repair an agent
	// config, so it has to actually repair it.
	if existing, exists := servers["varve"]; exists {
		if cur, ok := existing.(map[string]interface{}); ok &&
			fmt.Sprint(cur["command"]) == fmt.Sprint(entry["command"]) {
			return false, nil
		}
		setupNotices = append(setupNotices,
			"updated the varve server command in "+path+" (it pointed somewhere else)")
	}

	// A pre-rename `memtrace` entry is replaced, not left beside the new one.
	// Two entries would register the same tool names twice against the same
	// store, and the stale one points at a binary the user is replacing — so the
	// agent would see duplicate memory_* tools, one of them broken.
	if _, exists := servers["memtrace"]; exists {
		delete(servers, "memtrace")
		setupNotices = append(setupNotices,
			"replaced the pre-rename `memtrace` server entry in "+path)
	}

	servers["varve"] = entry
	cfg[serversKey] = servers

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}
