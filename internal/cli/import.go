package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/memtrace-dev/memtrace/internal/ingestion"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/spf13/cobra"
)

// listSources prints the probe results for `import` with no arguments.
func listSources(cmd *cobra.Command) error {
	_, root, err := openKernel()
	if err != nil {
		return err
	}
	found := ProbeAll(root)
	out := cmd.OutOrStdout()
	if len(found) == 0 {
		fmt.Fprintln(out, "No known memory sources found.")
		return nil
	}
	fmt.Fprintln(out, "Found:")
	for _, p := range found {
		if p.Refusal != nil {
			fmt.Fprintf(out, "  %-16s %v\n", p.Source, p.Refusal)
			continue
		}
		fmt.Fprintf(out, "  %-16s %s (%s)\n", p.Source, p.Detail, p.Path)
	}
	fmt.Fprintln(out, "\nImport one with: varve import claude-mem | engram | rules")
	fmt.Fprintln(out, "Everything imports as proposed or notes; undo with: varve import undo")
	return nil
}

func newImportCmd() *cobra.Command {
	var memType string
	var format string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "import [file|url]",
		Short: "Import from a memory store, a rules file, or a JSON/Markdown export",
		Long: `Import memories from a file or HTTP/HTTPS URL.

Supported formats:
  JSON     — varve export format (array or single memory object)
  Markdown — varve export format (.md files are auto-detected)

Use --dry-run to preview what would be imported without saving.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				// Bare `import` probes the known sources and says what it found
				// (ADR-0005 §D2.1). It imports nothing on its own: the user
				// picks a source, so nothing is ever ingested silently.
				return listSources(cmd)
			}
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
	cmd.AddCommand(newImportSourceCmds()...)
	return cmd
}
