package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/util"
)

func newInitCmd() *cobra.Command {
	var name string
	var noImport bool
	var noHooks bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize varve for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			// Find project root (prefer .git/ if no .varve/ yet)
			projectRoot := util.FindProjectRoot(cwd)
			if projectRoot == "" {
				projectRoot = cwd
			}

			memtraceDir := filepath.Join(projectRoot, ".varve")

			// Check if already initialized
			if info, err := os.Stat(memtraceDir); err == nil && info.IsDir() {
				fmt.Printf("varve is already initialized in %s\n", projectRoot)
				return nil
			}

			// Create .varve directory
			if err := os.MkdirAll(memtraceDir, 0755); err != nil {
				return fmt.Errorf("creating .varve directory: %w", err)
			}

			// Determine project name
			projectName := name
			if projectName == "" {
				projectName = filepath.Base(projectRoot)
			}

			// Generate project ID and register
			projectID := util.GenerateID()
			cfg := util.GetProjectConfig()
			cfg.Projects[projectRoot] = util.ProjectEntry{
				ID:        projectID,
				Name:      projectName,
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			}
			if err := util.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			// Open database and apply schema
			dbPath := util.GetProjectDbPath(projectRoot)
			k := kernel.New(dbPath, projectID)
			if err := k.Open(); err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer k.Close()

			// ADR-0004 §D1.3: the observation epoch, written once. The
			// catch-up scan never walks past it, so a fresh install does not
			// silently backfill verdicts about commits that predate the store —
			// archaeology dressed as attribution is the first thing an auditor
			// catches.
			if err := k.RecordObserverEnabled(time.Now().UTC()); err != nil {
				return fmt.Errorf("recording the observation epoch: %w", err)
			}

			// Add .varve/ to .gitignore
			addToGitignore(projectRoot)

			// Add varve instructions to CLAUDE.md (Claude Code only)
			addToClaudeMd(projectRoot)

			fmt.Printf("Initialized varve in %s\n", projectRoot)

			// ADR-0005 §D2.6: init *offers* an import instead of running one.
			// It replaces IngestOnInit's silent bulk save — no dedup key, no
			// batch identity, no report — with the same batch machinery every
			// other entry point uses, so there is exactly one import code path.
			if !noImport {
				found := ProbeAll(projectRoot)
				if len(found) > 0 {
					fmt.Println("\nFound existing memory:")
					for _, p := range found {
						if p.Refusal != nil {
							fmt.Printf("  %-16s %v\n", p.Source, p.Refusal)
							continue
						}
						fmt.Printf("  %-16s %s\n", p.Source, p.Detail)
					}
					fmt.Println("\nImport it with: varve import claude-mem | engram | rules")
					fmt.Println("Everything lands as proposed or notes, and `varve import undo` reverses it.")
				}
			}

			// §D1.1: offer the hook (default yes, declinable). It never blocks,
			// never prints and cannot fail a commit; `varve scan` recovers
			// whatever it misses.
			if !noHooks {
				installPostCommitHook(projectRoot)
			}

			fmt.Println("\nNext: run 'varve setup' to wire the MCP server into your agent.")
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Project name (default: directory name)")
	cmd.Flags().BoolVar(&noImport, "no-import", false, "Skip auto-importing from Claude/Cursor/git")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false,
		"Skip installing the post-commit hook that feeds attribution")
	return cmd
}

// addToGitignore appends .varve/ to .gitignore if the file exists and doesn't already contain it.
func addToGitignore(projectRoot string) {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		// No .gitignore: create one. Skipping used to look harmless and is not
		// — the store lands in the repository, and once committed it makes
		// `git revert` and `git checkout` fail with "local changes would be
		// overwritten" on a file the user never edited. Found by running the
		// observer end to end in a fresh repository.
		if os.IsNotExist(err) {
			_ = os.WriteFile(gitignorePath, []byte(".varve/\n"), 0o644)
		}
		return
	}
	if strings.Contains(string(data), ".varve") {
		return // already present
	}
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	content := string(data)
	prefix := "\n"
	if strings.HasSuffix(content, "\n") {
		prefix = ""
	}
	fmt.Fprintf(f, "%s.varve/\n", prefix)
}
