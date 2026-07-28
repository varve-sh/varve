package cli

import (
	"fmt"
	"strings"

	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/spf13/cobra"
)

func newSaveCmd() *cobra.Command {
	var memType string
	var tags string
	var files string
	var confidence float64

	cmd := &cobra.Command{
		Use:   "save <content>",
		Short: "Save a memory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			input := types.MemorySaveInput{
				Content:    args[0],
				Type:       types.MemoryType(memType),
				Source:     types.MemorySourceUser,
				Confidence: confidence,
			}
			if tags != "" {
				for _, t := range strings.Split(tags, ",") {
					if t = strings.TrimSpace(t); t != "" {
						input.Tags = append(input.Tags, t)
					}
				}
			}
			if files != "" {
				for _, f := range strings.Split(files, ",") {
					if f = strings.TrimSpace(f); f != "" {
						input.FilePaths = append(input.FilePaths, f)
					}
				}
			}

			mem, _, err := k.Save(input)
			if err != nil {
				return err
			}
			fmt.Printf("Saved memory %s (%s): %s\n", mem.ID, mem.Type, mem.Summary)
			return nil
		},
	}

	cmd.Flags().StringVar(&memType, "type", "fact", "Memory type: decision, convention, note")
	cmd.Flags().StringVar(&tags, "tags", "", "Comma-separated tags")
	cmd.Flags().StringVar(&files, "files", "", "Comma-separated relative file paths")
	cmd.Flags().Float64Var(&confidence, "confidence", 1.0, "Confidence score 0.0-1.0")
	return cmd
}
