package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/varve-sh/varve/internal/types"
)

func newSearchCmd() *cobra.Command {
	var limit int
	var memType string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search memories",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			results, err := k.Recall(types.MemoryRecallInput{
				Query: args[0],
				Limit: limit,
				Type:  types.MemoryType(memType),
			})
			if err != nil {
				return err
			}

			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(results)
			}

			if len(results) == 0 {
				fmt.Println("No memories found.")
				return nil
			}

			dim := color.New(color.Faint)
			for i, r := range results {
				m := r.Memory
				printMemoryRow(i+1, &m, dim)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 10, "Max results")
	cmd.Flags().StringVar(&memType, "type", "", "Filter by type: decision, convention, note")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func printMemoryRow(idx int, m *types.Memory, dim *color.Color) {
	typeColor := typeColorFor(m.Type)
	shortID := m.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	fmt.Printf("[%d] %s  ", idx, typeColor.Sprint(fmt.Sprintf("%s", m.Type)))
	dim.Printf("%s", shortID)
	// Any status other than `active` is worth a marker. `proposed` in
	// particular is now the birth state of every agent-saved decision, and a
	// queue the user cannot see is a quarantine with no exit (ADR-0001 D2).
	switch m.Status {
	case types.MemoryStatusActive:
	case types.MemoryStatusStale:
		color.New(color.FgYellow).Printf("  [stale]")
	case types.MemoryStatus(types.StatusProposed):
		color.New(color.FgMagenta).Printf("  [proposed]")
	case types.MemoryStatus(types.StatusViolated):
		color.New(color.FgRed).Printf("  [violated]")
	default:
		color.New(color.Faint).Printf("  [%s]", m.Status)
	}
	fmt.Println()
	fmt.Printf("    %s\n", m.Content)
	meta := []string{}
	if len(m.Tags) > 0 {
		meta = append(meta, "tags: "+strings.Join(m.Tags, ", "))
	}
	if len(m.FilePaths) > 0 {
		meta = append(meta, "files: "+strings.Join(m.FilePaths, ", "))
	}
	if len(meta) > 0 {
		dim.Printf("    %s\n", strings.Join(meta, " | "))
	}
	fmt.Println()
}

func typeColorFor(t types.MemoryType) *color.Color {
	switch t {
	case types.MemoryTypeDecision:
		return color.New(color.FgYellow)
	case types.MemoryTypeConvention:
		return color.New(color.FgCyan)
	case types.MemoryTypeNote:
		return color.New(color.FgWhite)
	default:
		return color.New(color.FgWhite)
	}
}
