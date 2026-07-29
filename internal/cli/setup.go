package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	var global bool

	cmd := &cobra.Command{
		Use:   "setup [agent]",
		Short: "Wire memtrace into your AI coding agent's MCP config",
		Long: `Adds memtrace to your agent's MCP configuration so it is available in every session.

Supported agents:
  claude-code   Writes to .claude/mcp.json (or ~/.claude/mcp.json with --global)
  cursor        Writes to .cursor/mcp.json
  vscode        Writes to .vscode/mcp.json
  opencode      Writes to opencode.json (project root)
  windsurf      Writes to ~/.codeium/windsurf/mcp_config.json
  gemini        Writes to .gemini/settings.json

If no agent is specified, memtrace auto-detects which agents are configured in the
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

			if !any {
				fmt.Println("No agents set up. Specify an agent:")
				fmt.Println("  memtrace setup claude-code")
				fmt.Println("  memtrace setup cursor")
				fmt.Println("  memtrace setup vscode")
				fmt.Println("  memtrace setup opencode")
				fmt.Println("  memtrace setup windsurf")
				fmt.Println("  memtrace setup gemini")
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

// setupAgent writes the memtrace MCP entry into the agent's config file.
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
			"command": "memtrace",
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
			"command": "memtrace",
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
			"command": "memtrace",
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
			"command": []string{"memtrace", "serve"},
		})

	case "windsurf":
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("could not find home directory: %w", err)
		}
		configPath := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
		written, err := writeMCPEntry(configPath, "mcpServers", map[string]interface{}{
			"command": "memtrace",
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
			"command": "memtrace",
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
const memtraceInstructionsCore = `This project has the memtrace MCP server connected. Use its tools for all memory operations — never use built-in memory tools.

Memory tools: memory_recall, memory_save, memory_get, memory_update, memory_forget, memory_context, memory_prompt

Rules:
- Before every task — call memory_recall with a relevant query, no exceptions. This includes commits, quick fixes, and one-liners.
- Before committing — call memory_recall to check for commit conventions.
- Learn something new — call memory_save to persist it.
- User says forget/delete/remove — call memory_forget.
- Never write memory files manually or use built-in memory features.

How memories are governed:
- A decision or convention you save lands "proposed". It does NOT bind until a
  human accepts it with "memtrace decision accept <id>", and it is never served
  as binding context. It can still come back to you from memory_recall or
  memory_context, marked PROPOSED — treat anything so marked as a pending
  proposal, not as law, and tell the user it is waiting instead of assuming
  your save took effect.
- fact and event are synonyms for note: retrievable, ungoverned, no lifecycle.
  A note cannot be edited into a decision — "memtrace decision promote <id>"
  does that, and it is a human action.
- Forgetting a decision is not a delete. memory_forget maps it onto the
  lifecycle: a proposal is rejected, a binding decision is reverted, and the
  audit record survives either way.`

// claudeMdSnippet wraps the core in a CLAUDE.md section.
const claudeMdSnippet = "\n## memtrace (memory)\n\n" + memtraceInstructionsCore + "\n"

// cursorRulesSnippet wraps the core in Cursor's MDC format.
const cursorRulesSnippet = "---\ndescription: memtrace memory instructions\nalwaysApply: true\n---\n\n" + memtraceInstructionsCore + "\n"

// copilotInstructionsSnippet wraps the core for .github/copilot-instructions.md.
const copilotInstructionsSnippet = "\n## memtrace\n\n" + memtraceInstructionsCore + "\n"

// windsurfRulesSnippet wraps the core for .windsurfrules.
const windsurfRulesSnippet = "\n# memtrace\n\n" + memtraceInstructionsCore + "\n"

// geminiMdSnippet wraps the core for GEMINI.md.
const geminiMdSnippet = "\n## memtrace\n\n" + memtraceInstructionsCore + "\n"

// addToClaudeMd appends memtrace instructions to CLAUDE.md if not already present.
func addToClaudeMd(projectRoot string) {
	appendInstructions(filepath.Join(projectRoot, "CLAUDE.md"), claudeMdSnippet)
}

// addToCursorRules writes a memtrace rule file to .cursor/rules/memtrace.mdc.
// It is idempotent — if the file already exists it is left unchanged.
func addToCursorRules(projectRoot string) {
	rulesDir := filepath.Join(projectRoot, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return
	}
	rulePath := filepath.Join(rulesDir, "memtrace.mdc")
	if _, err := os.Stat(rulePath); err == nil {
		return // already exists
	}
	_ = os.WriteFile(rulePath, []byte(cursorRulesSnippet), 0644)
}

// addToCopilotInstructions appends memtrace instructions to .github/copilot-instructions.md.
func addToCopilotInstructions(projectRoot string) {
	dir := filepath.Join(projectRoot, ".github")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	appendInstructions(filepath.Join(dir, "copilot-instructions.md"), copilotInstructionsSnippet)
}

// addToWindsurfRules appends memtrace instructions to .windsurfrules.
func addToWindsurfRules(projectRoot string) {
	appendInstructions(filepath.Join(projectRoot, ".windsurfrules"), windsurfRulesSnippet)
}

// addToGeminiMd appends memtrace instructions to GEMINI.md.
func addToGeminiMd(projectRoot string) {
	appendInstructions(filepath.Join(projectRoot, "GEMINI.md"), geminiMdSnippet)
}

// appendInstructions appends snippet to path if "memtrace" is not already present.
func appendInstructions(path, snippet string) {
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "memtrace") {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(snippet)
}

// writeMCPEntry reads (or creates) the JSON config at path, merges the memtrace
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
	if _, exists := servers["memtrace"]; exists {
		return false, nil
	}

	servers["memtrace"] = entry
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
