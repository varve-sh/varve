package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/memtrace-dev/memtrace/internal/ingestion"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var memType string
	var format string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "import <file|url>",
		Short: "Import memories from a JSON or Markdown file or URL",
		Long: `Import memories from a file or HTTP/HTTPS URL.

Supported formats:
  JSON     — memtrace export format (array or single memory object)
  Markdown — memtrace export format (.md files are auto-detected)

Use --dry-run to preview what would be imported without saving.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := args[0]

			useMarkdown := format == "markdown"
			if !useMarkdown && format == "" {
				ext := strings.ToLower(filepath.Ext(source))
				useMarkdown = ext == ".md" || ext == ".markdown"
			}

			var inputs []types.MemorySaveInput
			var err error
			if useMarkdown {
				inputs, err = ingestion.ImportMarkdown(source)
			} else {
				inputs, err = ingestion.ImportJSON(source)
			}
			if err != nil {
				return err
			}

			// Apply type filter
			if memType != "" {
				filtered := inputs[:0]
				for _, m := range inputs {
					if string(m.Type) == memType {
						filtered = append(filtered, m)
					}
				}
				inputs = filtered
			}

			if len(inputs) == 0 {
				fmt.Println("No memories to import.")
				return nil
			}

			if dryRun {
				fmt.Printf("Would import %d memories (dry run):\n", len(inputs))
				for i, m := range inputs {
					summary := m.Content
					if len(summary) > 80 {
						summary = summary[:77] + "..."
					}
					fmt.Printf("  [%d] (%s) %s\n", i+1, m.Type, summary)
				}
				return nil
			}

			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			saved := 0
			for _, input := range inputs {
				input.Source = types.MemorySourceImport
				if _, _, err := k.Save(input); err == nil {
					saved++
				}
			}
			fmt.Printf("Imported %d of %d memories\n", saved, len(inputs))
			return nil
		},
	}

	cmd.Flags().StringVar(&memType, "type", "", "Only import memories of this type: decision, convention, note")
	cmd.Flags().StringVar(&format, "format", "", "Force format: json, markdown (default: auto-detect by extension)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be imported without saving")
	return cmd
}
