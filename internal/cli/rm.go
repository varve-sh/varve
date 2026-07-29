package cli

import (
	"fmt"

	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/spf13/cobra"
)

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id|prefix>",
		Short: "Forget a memory by ID or short prefix",
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

			// The CLI is the human channel, so a forget really does transition
			// (ADR-0001 Amendment 3): a proposal is rejected, a binding decision
			// reverted, a note deleted. The message says which — "Deleted" for a
			// decision that is now a terminal audit row would be false.
			outcome, err := k.Forget(id, types.ActorHuman)
			if err != nil {
				return err
			}
			switch outcome {
			case kernel.DisposalDeleted:
				fmt.Printf("Deleted %s\n", id)
			case kernel.DisposalRejected:
				fmt.Printf("Rejected %s — the decision is kept as a rejected audit record\n", id)
			case kernel.DisposalReverted:
				fmt.Printf("Reverted %s — the decision is kept as a reverted audit record\n", id)
			default:
				fmt.Printf("Memory %s not found, or already disposed of\n", id)
			}
			return nil
		},
	}
}
