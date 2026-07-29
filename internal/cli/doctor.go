package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the health of your memtrace setup",
		Long:  "Runs a series of checks and reports any issues with your memtrace configuration.",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, projectRoot, err := openKernel()
			if err != nil {
				printCheck(checkFail, "Database", "not initialized — run 'memtrace init'")
				fmt.Println()
				fmt.Println("1 issue found.")
				return nil
			}
			defer k.Close()

			issues := 0
			ok := func(label, msg string) {
				printCheck(checkOK, label, msg)
			}
			warn := func(label, msg string) {
				printCheck(checkWarn, label, msg)
				issues++
			}
			fail := func(label, msg string) {
				printCheck(checkFail, label, msg)
				issues++
			}

			fmt.Println()

			// 1. Database
			dbPath := util.GetProjectDbPath(projectRoot)
			size := dbFileSize(dbPath)
			// "" is everything live: active notes plus non-terminal decisions.
			// Counting `active` only omitted every proposed decision (ADR-0001 D10).
			live, _ := k.Count("", "")
			staleCount, _ := k.Count("", types.MemoryStatusStale)
			archived, _ := k.Count("", types.MemoryStatusArchived)
			totalAll := live + staleCount + archived
			ok("Database", fmt.Sprintf("%s (%s, %d memories)", filepath.Join(".memtrace", "memtrace.db"), size, totalAll))

			// 1b. The confirmation queue. A proposal that nobody sees is a
			// quarantine with no exit (ADR-0001 D2, open question 3).
			if pending := pendingProposalCount(k); pending > 0 {
				warn("Pending decisions", fmt.Sprintf(
					"%d awaiting confirmation — run 'memtrace decision pending' to review", pending))
			} else {
				ok("Pending decisions", "none")
			}

			// 1c. Attribution's own health (ADR-0001 A5.3): a diff.observed row
			// whose promoted committed_at is NULL carries a timestamp migration
			// 5 could not parse, and is invisible to every window join. Expected
			// population: zero.
			if bad, err := k.UnparseableCommitTimestamps(); err == nil {
				if bad > 0 {
					warn("Commit timestamps", fmt.Sprintf(
						"%d observed commits have an unreadable timestamp and are "+
							"excluded from attribution — re-run 'memtrace scan' after "+
							"upgrading, or report it", bad))
				} else {
					ok("Commit timestamps", "all observed commits are attributable")
				}
			}

			// 2. Stale memories
			if staleCount > 0 {
				warn("Stale memories", fmt.Sprintf("%d — run 'memtrace list --status stale' to review, or 'memtrace scan' to refresh", staleCount))
			} else {
				ok("Stale memories", "none")
			}

			// 3. Embeddings
			embedProvider, embedModel := k.EmbedInfo()
			if embedProvider == "disabled" {
				ok("Embeddings", "disabled (BM25-only search)")
			} else {
				ok("Embeddings", fmt.Sprintf("%s (%s)", embedProvider, embedModel))
				unembedded, err := k.UnembeddedCount()
				if err == nil && unembedded > 0 {
					warn("Unembedded", fmt.Sprintf("%d memories have no vector — run 'memtrace reindex'", unembedded))
				} else if err == nil {
					ok("Unembedded", "all memories indexed")
				}
			}

			// 4. MCP configuration
			checkMCPConfig(projectRoot, ok, warn, fail)

			// 5. CLAUDE.md instructions
			checkClaudeMD(projectRoot, ok, warn)

			fmt.Println()
			switch issues {
			case 0:
				fmt.Printf("%s\n", color.GreenString("Everything looks good."))
			case 1:
				fmt.Printf("%s\n", color.YellowString("1 issue found."))
			default:
				fmt.Printf("%s\n", color.YellowString("%d issues found.", issues))
			}
			return nil
		},
	}
}

const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
)

func printCheck(level, label, msg string) {
	var badge string
	switch level {
	case checkOK:
		badge = color.New(color.FgGreen).Sprint("  [ok]  ")
	case checkWarn:
		badge = color.New(color.FgYellow).Sprint(" [warn] ")
	case checkFail:
		badge = color.New(color.FgRed).Sprint(" [fail] ")
	}
	fmt.Printf("%s %-18s %s\n", badge, label+":", msg)
}

func checkMCPConfig(projectRoot string, ok, warn, fail func(string, string)) {
	// Candidate MCP config files to check.
	candidates := []struct {
		path  string
		label string
	}{
		{filepath.Join(projectRoot, ".claude", "mcp.json"), ".claude/mcp.json"},
		{filepath.Join(projectRoot, ".cursor", "mcp.json"), ".cursor/mcp.json"},
		{filepath.Join(projectRoot, ".vscode", "mcp.json"), ".vscode/mcp.json"},
		{claudeUserMCPConfig(), "~/.claude/mcp.json"},
	}

	for _, c := range candidates {
		data, err := os.ReadFile(c.path)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "memtrace") {
			ok("MCP config", fmt.Sprintf("found in %s", c.label))
			return
		}
	}
	warn("MCP config", "memtrace not found in any MCP config — run 'memtrace setup'")
}

func claudeUserMCPConfig() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "mcp.json")
}

func checkClaudeMD(projectRoot string, ok, warn func(string, string)) {
	path := filepath.Join(projectRoot, "CLAUDE.md")
	data, err := os.ReadFile(path)
	if err != nil {
		warn("CLAUDE.md", "not found — run 'memtrace init' to add memtrace instructions")
		return
	}
	if strings.Contains(string(data), "memtrace") || strings.Contains(string(data), "memory_save") {
		ok("CLAUDE.md", "memtrace instructions present")
	} else {
		warn("CLAUDE.md", "CLAUDE.md exists but has no memtrace instructions")
	}
}
