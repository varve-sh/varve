package cli

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func newReindexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Backfill embeddings for memories that have none stored",
		Long:  "Computes and stores embeddings for all active memories with a missing embedding.\nRequires VARVE_EMBED_KEY (or OPENAI_API_KEY) to be set.",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			dim := color.New(color.Faint)

			if !k.HasEmbedder() {
				fmt.Fprintln(cmd.ErrOrStderr(), "No embedder configured — set VARVE_EMBED_KEY (or OPENAI_API_KEY) in your shell environment and re-run.")
				return nil
			}

			res, err := k.Reindex(func(done, total int) {
				dim.Printf("\r  %d / %d", done, total)
			})
			if err != nil {
				return err
			}

			fmt.Println() // newline after progress
			switch {
			case res.Total == 0:
				fmt.Println("All memories already have embeddings — nothing to do.")
			case res.Succeeded == res.Total:
				fmt.Printf("Reindexed %d memories.\n", res.Succeeded)
			case res.Succeeded > 0:
				fmt.Printf("Reindexed %d / %d memories. First error: %v\n", res.Succeeded, res.Total, res.FirstErr)
			default:
				return fmt.Errorf("failed to embed any of %d memories: %w", res.Total, res.FirstErr)
			}
			return nil
		},
	}
}
