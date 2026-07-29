package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	mcpserver "github.com/varve-sh/varve/internal/mcp"
)

func newServeCmd() *cobra.Command {
	var dir string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server (stdio transport)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir != "" {
				if err := os.Chdir(dir); err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					os.Exit(1)
				}
			}
			k, _, err := openKernel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			defer k.Close()
			return mcpserver.Serve(k)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "project directory (overrides cwd)")
	return cmd
}
