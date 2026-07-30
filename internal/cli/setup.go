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
  claude-code   Writes to .claude/mcp.json (or ~/.claude/mcp.json with --global)
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

	cmd.Flags().BoolVar(&global, "global", false, "Install at user scope (~/.claude/mcp.json) instead of project scope (claude-code only)")
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
		{".cursor", "cursor"},
		{".vscode", "vscode"},
		{"opencode.json", "opencode"},
		{".gemini", "gemini"},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(projectRoot, c.path)); err == nil {
			found = append(found, c.agent)
		}
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
			configPath = filepath.Join(home, ".claude", "mcp.json")
		} else {
			configPath = filepath.Join(projectRoot, ".claude", "mcp.json")
		}
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

// serveCommand is the command an agent config should launch.
//
// It is the bare binary name when that resolves on PATH, and the absolute path
// to this executable when it does not. Writing "varve" unconditionally produced
// an MCP entry that silently failed to start for anyone whose install directory
// is not on PATH — `go install` and `make install` both land in $GOPATH/bin,
// which is not on PATH by default on macOS. setup reported success and the agent
// simply had no memory tools, with nothing anywhere saying why.
func serveCommand() string {
	if _, err := exec.LookPath(binaryName); err == nil {
		return binaryName
	}
	if self, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			return resolved
		}
		return self
	}
	return binaryName
}

const binaryName = "varve"

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

	// Already present — nothing to do
	if _, exists := servers["varve"]; exists {
		return false, nil
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
