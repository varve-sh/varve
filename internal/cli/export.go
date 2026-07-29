package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/memtrace-dev/memtrace/internal/ingestion"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/spf13/cobra"
)

func newExportCmd() *cobra.Command {
	var outputFile string
	var memType string
	var status string
	var format string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export memories to JSON or Markdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch format {
			case "json", "markdown":
			default:
				return fmt.Errorf("--format must be json or markdown")
			}

			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			memories, err := k.List(types.ListOptions{
				Limit:  10000,
				Type:   types.MemoryType(memType),
				Status: types.MemoryStatus(status),
				Sort:   "created_at",
				Order:  "asc",
			})
			if err != nil {
				return err
			}

			var out []byte
			if format == "markdown" {
				out = []byte(ingestion.ExportMarkdown(memories))
			} else {
				out, err = json.MarshalIndent(memories, "", "  ")
				if err != nil {
					return err
				}
			}

			if outputFile != "" {
				if err := os.WriteFile(outputFile, out, 0644); err != nil {
					return fmt.Errorf("writing file: %w", err)
				}
				fmt.Printf("Exported %d memories to %s\n", len(memories), outputFile)
			} else {
				fmt.Println(string(out))
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file (default: stdout)")
	cmd.Flags().StringVar(&memType, "type", "", "Filter by type: decision, convention, note")
	// Export is the user's backup and ADR-0001 §D9's own migration mechanism, so
	// it must not silently omit rows: the default is everything live, not
	// `active` (which excluded every proposed decision).
	cmd.Flags().StringVar(&status, "status", "",
		"Filter by status (default: everything live). "+
			"Notes: active, stale, archived. "+
			"Decisions: proposed, active, violated, superseded, reverted, rejected")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json, markdown")
	return cmd
}
