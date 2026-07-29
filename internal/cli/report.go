package cli

import (
	"fmt"
	"time"

	"github.com/memtrace-dev/memtrace/internal/report"
	"github.com/spf13/cobra"
)

// newReportCmd is ADR-0004 §D6's reporting surface.
//
// Everything it prints obeys §D6's honesty controls: no rate without its
// sample size, no aggregate whose method is hidden, no number that cannot be
// drilled to the event rows behind it, and the limitations printed on the
// report itself rather than in documentation.
func newReportCmd() *cobra.Command {
	var days int
	var format string
	var decisionID string
	var raw bool
	var grace int

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Attribution report: what the agents did with your decisions",
		Long: `Shows, per decision, how often it was packed into an agent session, how many
changes touched its scope, and whether those changes conformed to it or
violated it (ADR-0004).

Every figure is computed from the append-only event log and drills down to the
raw rows: varve report --decision <id> --raw.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			k, projectRoot, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			to := time.Now().UTC()
			opts := report.Options{
				From: to.AddDate(0, 0, -days), To: to,
				GraceMinutes: grace, RepoRoot: projectRoot, DecisionID: decisionID,
			}
			db := k.Decisions().DB()

			if raw {
				// §D5.3 / §D6.2: the raw rows, verbatim. This is the invariant
				// behind every rendered number — if a figure cannot be traced
				// to events, it may not be rendered.
				var shas []string
				if decisionID != "" {
					shas, err = report.CommitsForDecision(db, decisionID)
					if err != nil {
						return err
					}
				}
				events, err := report.QueryRaw(db, decisionID, shas)
				if err != nil {
					return err
				}
				fmt.Print(report.RawText(events))
				return nil
			}

			r, err := report.Build(db, opts)
			if err != nil {
				return err
			}
			switch format {
			case "json":
				out, err := r.JSON()
				if err != nil {
					return err
				}
				fmt.Println(string(out))
			case "md", "markdown":
				fmt.Print(r.Markdown())
			case "text", "":
				fmt.Print(r.Text())
			default:
				return fmt.Errorf("--format must be text, md or json")
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "Reporting window in days")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, md, json")
	cmd.Flags().StringVar(&decisionID, "decision", "", "Restrict to one decision")
	cmd.Flags().BoolVar(&raw, "raw", false,
		"Print the raw event rows behind the figures (§D5.3 drill-down)")
	cmd.Flags().IntVar(&grace, "grace", report.DefaultGraceMinutes,
		"Attribution grace window in minutes, printed on the report")
	cmd.AddCommand(newReportCoverageCmd())
	return cmd
}

// newReportCoverageCmd is §D5.1 as a shipped command: strategy kill criterion
// 3 is "<20% of agent sessions produce an attributable event after 30 days",
// and an unmeasurable kill criterion is not a kill criterion.
func newReportCoverageCmd() *cobra.Command {
	var days int
	var grace int
	cmd := &cobra.Command{
		Use:   "coverage",
		Short: "Attribution coverage — the kill-criterion metric",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			to := time.Now().UTC()
			cov, err := report.QueryCoverage(k.Decisions().DB(), report.Options{
				From: to.AddDate(0, 0, -days), To: to, GraceMinutes: grace,
			})
			if err != nil {
				return err
			}
			fmt.Print(report.CoverageText(cov, days, grace))
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "Window in days")
	cmd.Flags().IntVar(&grace, "grace", report.DefaultGraceMinutes,
		"Attribution grace window in minutes")
	return cmd
}
