package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memtrace-dev/memtrace/internal/ingestion"
	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/util"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var name string
	var noImport bool
	var noHooks bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize memtrace for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			// Find project root (prefer .git/ if no .memtrace/ yet)
			projectRoot := util.FindProjectRoot(cwd)
			if projectRoot == "" {
				projectRoot = cwd
			}

			memtraceDir := filepath.Join(projectRoot, ".memtrace")

			// Check if already initialized
			if info, err := os.Stat(memtraceDir); err == nil && info.IsDir() {
				fmt.Printf("memtrace is already initialized in %s\n", projectRoot)
				return nil
			}

			// Create .memtrace directory
			if err := os.MkdirAll(memtraceDir, 0755); err != nil {
				return fmt.Errorf("creating .memtrace directory: %w", err)
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

			// Add .memtrace/ to .gitignore
			addToGitignore(projectRoot)

			// Add memtrace instructions to CLAUDE.md (Claude Code only)
			addToClaudeMd(projectRoot)

			// Run importers unless --no-import
			var result *ingestion.IngestResult
			if !noImport {
				pipeline := ingestion.New(k)
				result = pipeline.IngestOnInit(projectRoot)
			}

			fmt.Printf("Initialized memtrace in %s\n", projectRoot)
			if result != nil && result.Total > 0 {
				parts := []string{}
				for src, n := range result.Sources {
					parts = append(parts, fmt.Sprintf("%s: %d", src, n))
				}
				fmt.Printf("Imported %d memories (%s)\n", result.Total, strings.Join(parts, ", "))
			}

			// §D1.1: offer the hook (default yes, declinable). It never blocks,
			// never prints and cannot fail a commit; `memtrace scan` recovers
			// whatever it misses.
			if !noHooks {
				installPostCommitHook(projectRoot)
			}

			fmt.Println("\nNext: run 'memtrace setup' to wire the MCP server into your agent.")
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Project name (default: directory name)")
	cmd.Flags().BoolVar(&noImport, "no-import", false, "Skip auto-importing from Claude/Cursor/git")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false,
		"Skip installing the post-commit hook that feeds attribution")
	return cmd
}

// addToGitignore appends .memtrace/ to .gitignore if the file exists and doesn't already contain it.
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
			_ = os.WriteFile(gitignorePath, []byte(".memtrace/\n"), 0o644)
		}
		return
	}
	if strings.Contains(string(data), ".memtrace") {
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
	fmt.Fprintf(f, "%s.memtrace/\n", prefix)
}
