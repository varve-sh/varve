package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/varve-sh/varve/internal/util"
)

// newStoreCmd owns the one operation the rename created: moving a pre-rename
// store from .memtrace/ to .varve/.
//
// It is an explicit command rather than an on-open migration. The move itself is
// trivially reversible — no schema change, no data transformation — but this
// branch already shipped one migration that emptied a store while exiting 0
// (review round 2, F1), and the lesson recorded from it was that a store's
// location is not something to change behind the user's back. So the legacy
// store keeps working untouched until someone asks for this.
func newStoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Inspect and relocate the varve store",
	}
	cmd.AddCommand(newStoreMoveCmd())
	return cmd
}

func newStoreMoveCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "move",
		Short: "Move a pre-rename store from .memtrace/ to .varve/",
		Long: "Moves .memtrace/memtrace.db (and its WAL sidecars, observer log and\n" +
			"config) to .varve/varve.db.\n\n" +
			"Nothing is deleted: the old directory is left in place if anything in it\n" +
			"is not recognised, and the move is skipped entirely if a store already\n" +
			"exists at the new location.",
		RunE: func(cmd *cobra.Command, args []string) error {
			root := util.FindProjectRoot(".")
			if root == "" {
				return fmt.Errorf("no varve project found here — run `varve init` first")
			}
			oldDir := filepath.Join(root, util.LegacyStoreDir)
			newDir := filepath.Join(root, util.StoreDir)

			if _, err := os.Stat(filepath.Join(oldDir, "memtrace.db")); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(),
					"nothing to move: no store at %s/memtrace.db\n", util.LegacyStoreDir)
				return nil
			}
			// Refusing rather than merging: two stores means two histories, and
			// picking one silently is how attribution loses events.
			if _, err := os.Stat(filepath.Join(newDir, "varve.db")); err == nil {
				return fmt.Errorf("a store already exists at %s/varve.db — "+
					"move or remove it first; varve will not merge two stores",
					util.StoreDir)
			}

			// WAL and shm are moved with the database. Leaving them behind would
			// hand SQLite a database whose write-ahead log is missing.
			moves := [][2]string{
				{"memtrace.db", "varve.db"},
				{"memtrace.db-wal", "varve.db-wal"},
				{"memtrace.db-shm", "varve.db-shm"},
				{"observer.log", "observer.log"},
				{"config.json", "config.json"},
			}
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "would move %s/ → %s/:\n", util.LegacyStoreDir, util.StoreDir)
			} else if err := os.MkdirAll(newDir, 0o755); err != nil {
				return err
			}

			moved := 0
			for _, m := range moves {
				src := filepath.Join(oldDir, m[0])
				if _, err := os.Stat(src); err != nil {
					continue
				}
				if dryRun {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s → %s\n", m[0], m[1])
					moved++
					continue
				}
				if err := os.Rename(src, filepath.Join(newDir, m[1])); err != nil {
					return fmt.Errorf("moving %s: %w", m[0], err)
				}
				moved++
			}
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "%d file(s); re-run without --dry-run to apply\n", moved)
				return nil
			}

			// Left in place rather than removed: anything else in the old
			// directory is the user's, and this command has no opinion on it.
			leftovers, _ := os.ReadDir(oldDir)
			fmt.Fprintf(cmd.OutOrStdout(), "moved %d file(s) to %s/\n", moved, util.StoreDir)
			if len(leftovers) == 0 {
				_ = os.Remove(oldDir)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(),
					"%s/ still holds %d unrecognised file(s) and was left in place\n",
					util.LegacyStoreDir, len(leftovers))
			}
			addToGitignore(root)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would move without moving it")
	return cmd
}
