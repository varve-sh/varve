package cli

import (
	"github.com/spf13/cobra"
	"github.com/varve-sh/varve/internal/tui"
)

func newBrowseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "browse",
		Short: "Open the interactive memory browser",
		Long: `Opens a full-screen terminal UI for browsing, searching, and managing memories.

Key bindings:
  /        filter memories
  enter    view full memory
  e        edit selected memory in $EDITOR
  d        delete selected memory (with confirmation)
  esc      go back
  q        quit`,
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			return tui.Browse(k)
		},
	}
}
