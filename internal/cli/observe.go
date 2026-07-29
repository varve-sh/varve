package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/memtrace-dev/memtrace/internal/observer"
	"github.com/spf13/cobra"
)

// newObserveCmd is ADR-0004 §D1's hook path: observe one commit.
//
// It is the only varve command designed to be run by a machine rather than a
// person, and its posture is unusual on purpose (§D7): it never blocks a
// commit, never prints to the user's terminal under --quiet, and never exits
// non-zero. A commit is not a moment at which to tell someone their memory
// tool has an opinion.
func newObserveCmd() *cobra.Command {
	var commit string
	var quiet bool

	cmd := &cobra.Command{
		Use:   "observe",
		Short: "Record one commit and judge it against binding decisions",
		Long: "Reads a commit's files, message and patch-id and records what it did to " +
			"the decisions whose scope it touched (ADR-0004 §D1).\n\n" +
			"This is what the post-commit hook runs. It exits 0 whatever happens: " +
			"losing one observation is acceptable because `memtrace scan` picks it up, " +
			"and a hook that fails or prints is a hook a user removes.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			k, projectRoot, err := openKernel()
			if err != nil {
				return observeFailure(quiet, projectRoot, err)
			}
			defer k.Close()

			res, err := observer.ObserveOne(k, projectRoot, commit)
			if err != nil {
				return observeFailure(quiet, projectRoot, err)
			}
			if quiet {
				return nil
			}
			switch {
			case res.AlreadyObserved:
				fmt.Printf("%s was already observed\n", commit)
			default:
				fmt.Printf("Observed %s — %d scope matches (%d violations)\n",
					commit, res.Matched, res.Violated)
				for _, id := range res.DecisionsReverted {
					color.New(color.FgRed).Printf("  reverted %s (its accepting evidence was reverted)\n", id)
				}
				for _, id := range res.DecisionsReinstated {
					color.New(color.FgGreen).Printf("  reinstated %s (its last open violation was reverted)\n", id)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&commit, "commit", "HEAD", "Commit to observe")
	cmd.Flags().BoolVar(&quiet, "quiet", false,
		"Print nothing and exit 0 whatever happens (the hook's mode)")
	return cmd
}

// observeFailure is §D7's failure posture: the observer's own errors go to
// .memtrace/observer.log and never to the user's terminal.
//
// A missed observation is recoverable — `scan`'s cursor is the absence of the
// diff.observed row, so the next scan picks the commit up — and that is
// precisely why retrying here would be the wrong shape: a busy database (an
// MCP session holding the writer) must cost the commit nothing.
func observeFailure(quiet bool, projectRoot string, err error) error {
	logObserverError(projectRoot, err)
	if quiet {
		return nil // exit 0: the hook must never fail a commit
	}
	return err
}

func logObserverError(projectRoot string, err error) {
	if projectRoot == "" || err == nil {
		return
	}
	path := filepath.Join(projectRoot, ".memtrace", "observer.log")
	f, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if openErr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\n", nowRFC3339(), strings.ReplaceAll(err.Error(), "\n", " "))
}
