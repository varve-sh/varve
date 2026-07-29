package cli

import "github.com/spf13/cobra"

// NewRootCmd builds and returns the root cobra command.
func NewRootCmd(version ...string) *cobra.Command {
	v := "dev"
	if len(version) > 0 && version[0] != "" {
		v = version[0]
	}
	root := &cobra.Command{
		Use:     "memtrace",
		Short:   "Local-first memory engine for AI coding agents",
		Long:    "Memtrace gives AI coding tools persistent, structured memory across sessions.\nWebsite: https://memtrace.sh",
		Version: v,
	}

	root.AddCommand(
		newInitCmd(),
		newSetupCmd(),
		newSaveCmd(),
		newUpdateCmd(),
		newEditCmd(),
		newSearchCmd(),
		newDecisionCmd(),
		newListCmd(),
		newRmCmd(),
		newExportCmd(),
		newImportCmd(),
		newServeCmd(),
		newBrowseCmd(),
		newStatusCmd(),
		newReindexCmd(),
		newScanCmd(),
		newMigrateCmd(),
		newDoctorCmd(),
		newConfigCmd(),
		newStatsCmd(),
	)

	return root
}
