package cli

import (
	"fmt"

	"github.com/memtrace-dev/memtrace/internal/types"

	"github.com/spf13/cobra"
)

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id|prefix>",
		Short: "Delete a memory by ID or short prefix",
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

			deleted, err := k.Delete(id, types.ActorHuman)
			if err != nil {
				return err
			}
			if !deleted {
				fmt.Printf("Memory %s not found\n", id)
				return nil
			}
			fmt.Printf("Deleted %s\n", id)
			return nil
		},
	}
}
