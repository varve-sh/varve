package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	var memType string
	var tags string
	var files string
	var content string
	var confidence float64

	cmd := &cobra.Command{
		Use:   "update <id|prefix>",
		Short: "Update an existing memory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			id := resolveID(k, args[0])
			if id == "" {
				fmt.Printf("Memory %s not found\n", args[0])
				return nil
			}

			input := types.MemoryUpdateInput{}

			if cmd.Flags().Changed("content") {
				input.Content = &content
			}
			if cmd.Flags().Changed("type") {
				t := types.MemoryType(memType)
				input.Type = &t
			}
			if cmd.Flags().Changed("tags") {
				var parsed []string
				for _, t := range strings.Split(tags, ",") {
					if t = strings.TrimSpace(t); t != "" {
						parsed = append(parsed, t)
					}
				}
				input.Tags = &parsed
			}
			if cmd.Flags().Changed("files") {
				var parsed []string
				for _, f := range strings.Split(files, ",") {
					if f = strings.TrimSpace(f); f != "" {
						parsed = append(parsed, f)
					}
				}
				input.FilePaths = &parsed
			}
			if cmd.Flags().Changed("confidence") {
				input.Confidence = &confidence
			}

			mem, err := k.Update(id, input)
			if errors.Is(err, types.ErrMemoryNotFound) {
				fmt.Printf("Memory %s not found\n", id)
				return nil
			}
			if err != nil {
				return err
			}
			if mem == nil {
				fmt.Printf("Memory %s not found\n", id)
				return nil
			}
			fmt.Printf("Updated %s (%s): %s\n", mem.ID, mem.Type, mem.Summary)
			return nil
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "New content")
	cmd.Flags().StringVar(&memType, "type", "", "New type: note (decisions cannot be reached by update)")
	cmd.Flags().StringVar(&tags, "tags", "", "New comma-separated tags (replaces existing)")
	cmd.Flags().StringVar(&files, "files", "", "New comma-separated file paths (replaces existing)")
	cmd.Flags().Float64Var(&confidence, "confidence", 0, "New confidence score 0.0-1.0")
	return cmd
}
