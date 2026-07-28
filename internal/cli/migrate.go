package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
	"github.com/spf13/cobra"
)

func newMigrateCmd() *cobra.Command {
	var fromV1 bool

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Convert this project's database to the current schema version",
		Long: "Converts a v1 database (one `memories` table) to the v2 decision\n" +
			"lifecycle schema. The v1 file is moved aside and kept indefinitely —\n" +
			"nothing deletes it — and the JSON export is kept beside it.\n\n" +
			"v2 databases upgrade themselves through the versioned migration\n" +
			"framework when they are opened; only the v1 conversion is manual.\n\n" +
			"NOT YET AVAILABLE: the v2 read paths are still being built, so a\n" +
			"converted database would read as empty. The command refuses until\n" +
			"they land. Your v1 database keeps working in the meantime.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !fromV1 {
				return fmt.Errorf("nothing to do — pass --from-v1 to convert a v1 database")
			}
			if !kernel.V2ReadPathsReady() {
				return fmt.Errorf(
					"not yet — converting now would make every memory invisible.\n\n" +
						"The v2 schema is in place, but the read paths that query it are still\n" +
						"being built. A converted database would move every row into `decisions`\n" +
						"and `notes`, which no command reads yet: list, search, export, status and\n" +
						"the MCP tools would all return nothing.\n\n" +
						"Nothing has been changed. Your database keeps working as it is")
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			projectRoot := util.FindProjectRoot(cwd)
			if projectRoot == "" {
				return types.ErrNotInitialized
			}
			cfg := util.GetProjectConfig()
			entry, ok := cfg.Projects[projectRoot]
			if !ok {
				return types.ErrNotInitialized
			}

			report, err := kernel.MigrateFromV1(kernel.MigrateV1Options{
				DBPath:    util.GetProjectDbPath(projectRoot),
				ProjectID: entry.ID,
			})
			if err != nil {
				return err
			}
			fmt.Print(report.String())
			return nil
		},
	}

	cmd.Flags().BoolVar(&fromV1, "from-v1", false, "Convert a v1 database to v2")
	return cmd
}

// legacyDatabaseHint turns the kernel's refusal to open a v1 database into a
// message that names the command to run.
func legacyDatabaseHint(err error) error {
	if errors.Is(err, types.ErrLegacyDatabase) {
		return fmt.Errorf(
			"this project still uses the v1 schema.\n\n" +
				"  run:  memtrace migrate --from-v1\n\n" +
				"Your v1 database is moved aside and kept; nothing is deleted")
	}
	return err
}
