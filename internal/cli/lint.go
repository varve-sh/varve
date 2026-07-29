package cli

import (
	"encoding/json"
	"time"

	"github.com/spf13/cobra"
	"github.com/varve-sh/varve/internal/lint"
)

func newLintCmd() *cobra.Command {
	var format string
	var raw bool

	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Health-check the memory corpus",
		Long: `Run the ten structural checks over this project's memory and print the report.

Every finding is deterministic — SQL over your own store plus local git plumbing.
No model runs, nothing leaves the machine, and the report says what the checks
cannot see: paraphrase duplicates and semantic contradictions are not detected.

The corpus-health score covers your existing memory (dead references, duplicates,
contradiction candidates, staleness, hygiene). Adoption facts — proposals awaiting
review, packing history, curated evidence — are listed but never scored: on a
fresh import they are all "bad" by construction.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			k, root, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			if raw {
				// §D5 / ADR-0004 §D6.2: the rows behind every line, so a
				// reader can check a finding by hand rather than trust it.
				opts := lint.Options{
					ProjectID: k.ProjectID(), RepoRoot: root, Now: time.Now().UTC(),
					CommitExists: lint.GitCommitExists(root),
				}
				res, err := lint.Run(k.Decisions().DB(), opts)
				if err != nil {
					return err
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(res.Checks)
			}
			return printReport(cmd, k, root, nil, format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "Output format: md, json (default: terminal text)")
	cmd.Flags().BoolVar(&raw, "raw", false, "Print the raw rows behind every finding")
	return cmd
}
