package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var limit int
	var memType string
	var status string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List memories",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			memories, err := k.List(types.ListOptions{
				Limit:  limit,
				Type:   types.MemoryType(memType),
				Status: types.MemoryStatus(status),
				Sort:   "created_at",
				Order:  "desc",
			})
			if err != nil {
				return err
			}

			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(memories)
			}

			if len(memories) == 0 {
				fmt.Println("No memories found.")
				return nil
			}

			dim := color.New(color.Faint)
			for i, m := range memories {
				m := m
				printMemoryRow(i+1, &m, dim)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "Max results")
	cmd.Flags().StringVar(&memType, "type", "", "Filter by type: decision, convention, note")
	cmd.Flags().StringVar(&status, "status", "active", "Filter by status: active, stale, archived")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}
