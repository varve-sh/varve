package cli

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/observer"
	"github.com/spf13/cobra"
)

// newScanCmd is ADR-0004 §D1.2's catch-up scan.
//
// The name is the ADR's. It previously meant the note-staleness scan, which
// survives behind --stale: two unrelated jobs under one verb is worse than one
// flag, and the observer's scan is the one that runs on a schedule nobody sets
// (at every MCP session start), so it gets the bare command.
func newScanCmd() *cobra.Command {
	var stale bool
	var backfill bool
	var limit int

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Observe commits that have not been observed yet",
		Long: `Walks HEAD and the default branch, newest first, and records every commit
that has no diff.observed row yet — judging each against the decisions whose
scope it touches (ADR-0004 §D1.2).

This is the half of the observer that makes the record complete: commits made
before varve was installed, pulled from a teammate, made with the hook
bypassed, or missed because the database was busy. It runs automatically in
the background at every MCP session start; this command is the manual door.

--stale runs the unrelated note-staleness scan instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			k, projectRoot, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			if stale {
				return runStalenessScan(k, projectRoot)
			}

			dim := color.New(color.Faint)
			res, err := observer.Scan(k, observer.ScanOptions{
				RepoRoot: projectRoot, Backfill: backfill, Limit: limit,
			})
			if err != nil {
				return err
			}
			if res.Walked == 0 {
				fmt.Println("No git history to observe here.")
				return nil
			}

			fmt.Printf("Observed %d new commits (%d already observed, %d walked).\n",
				res.Observed, res.AlreadyObserved, res.Walked)
			if res.Matched > 0 {
				fmt.Printf("  %d scope matches", res.Matched)
				if res.Violated > 0 {
					color.New(color.FgRed).Printf(" — %d violations", res.Violated)
				}
				fmt.Println()
			}
			for _, id := range res.Reverted {
				color.New(color.FgRed).Printf("  reverted %s (its accepting evidence was reverted)\n", id)
			}
			for _, id := range res.Reinstated {
				color.New(color.FgGreen).Printf("  reinstated %s\n", id)
			}
			if res.SkippedPreEpoch > 0 {
				dim.Printf("  %d commits predate this store and were skipped — "+
					"`varve scan --backfill` observes them, marked and excluded from reports\n",
					res.SkippedPreEpoch)
			}
			for _, e := range res.Errors {
				dim.Printf("  skipped: %s\n", e)
			}
			if backfill {
				dim.Println("Backfilled matches are excluded from every reported metric: " +
					"a verdict about a commit that predates the store is archaeology, not attribution.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&stale, "stale", false,
		"Run the note-staleness scan instead (the pre-observer meaning of this command)")
	cmd.Flags().BoolVar(&backfill, "backfill", false,
		"Also observe commits older than this store, marked and excluded from reports")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max commits to walk per root (default 500)")
	return cmd
}

// runStalenessScan is the pre-observer meaning of `scan`: mtime-based note
// staleness (ADR-0001 open question 8 keeps it for notes only).
func runStalenessScan(k *kernel.MemoryKernel, projectRoot string) error {
	dim := color.New(color.Faint)
	warn := color.New(color.FgYellow)

	res, err := k.ScanStaleness(projectRoot)
	if err != nil {
		return err
	}

	if res.Checked == 0 {
		fmt.Println("No memories with file paths — nothing to scan.")
		return nil
	}

	for _, d := range res.Details {
		shortID := d.MemoryID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		warn.Printf("  stale  ")
		fmt.Printf("%s  %q — %s\n", shortID, d.Summary, d.Reason)
	}

	if res.Marked > 0 {
		fmt.Println()
	}

	unchanged := res.Checked - res.Marked
	switch res.Marked {
	case 0:
		dim.Printf("Scanned %d memories — all up to date.\n", res.Checked)
	case 1:
		fmt.Printf("1 memory marked stale")
		if unchanged > 0 {
			dim.Printf(" (%d unchanged)", unchanged)
		}
		fmt.Println(".")
		fmt.Println("Run 'varve list --status stale' to review.")
	default:
		fmt.Printf("%d memories marked stale", res.Marked)
		if unchanged > 0 {
			dim.Printf(" (%d unchanged)", unchanged)
		}
		fmt.Println(".")
		fmt.Println("Run 'varve list --status stale' to review.")
	}
	return nil
}
