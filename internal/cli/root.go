package cli

import "github.com/spf13/cobra"

// NewRootCmd builds and returns the root cobra command.
func NewRootCmd(version ...string) *cobra.Command {
	v := "dev"
	if len(version) > 0 && version[0] != "" {
		v = version[0]
	}
	root := &cobra.Command{
		Use:     "varve",
		Short:   "Local-first memory engine for AI coding agents",
		Long:    "Varve gives AI coding tools persistent, structured memory across sessions.\nWebsite: https://varve.sh",
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
		newStoreCmd(),
		newReindexCmd(),
		newScanCmd(),
		newObserveCmd(),
		newHooksCmd(),
		newReportCmd(),
		newLintCmd(),
		newMigrateCmd(),
		newDoctorCmd(),
		newConfigCmd(),
		newStatsCmd(),
	)

	return root
}
