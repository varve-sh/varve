package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func newStatsCmd() *cobra.Command {
	var asJSON bool
	var days int

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show memory usage statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			window := time.Duration(days) * 24 * time.Hour
			result, err := k.Stats(window)
			if err != nil {
				return err
			}

			if asJSON {
				type topEntry struct {
					ID          string `json:"id"`
					Summary     string `json:"summary"`
					AccessCount int    `json:"access_count"`
				}
				top := make([]topEntry, len(result.TopAccessed))
				for i, m := range result.TopAccessed {
					top[i] = topEntry{
						ID:          m.ID,
						Summary:     truncateSummary(m.Summary, m.Content),
						AccessCount: m.AccessCount,
					}
				}
				out := map[string]interface{}{
					"window_days":       days,
					"total_live":        result.TotalLive,
					"saved_this_period": result.SavedThisWeek,
					"recalls_this_period": result.RecallsThisWeek,
					"sessions_this_period": result.SessionsThisWeek,
					"top_accessed":      top,
				}
				return json.NewEncoder(os.Stdout).Encode(out)
			}

			dim := color.New(color.Faint)

			label := fmt.Sprintf("last %d days", days)
			fmt.Printf("%s\n\n", color.New(color.Bold).Sprint("Memory usage — "+label))

			fmt.Printf("  %-28s %d\n", "Live memories:", result.TotalLive)
			fmt.Printf("  %-28s %d\n", "Saved this period:", result.SavedThisWeek)
			fmt.Printf("  %-28s %d\n", "Recalls this period:", result.RecallsThisWeek)
			fmt.Printf("  %-28s %d\n", "Sessions this period:", result.SessionsThisWeek)

			if len(result.TopAccessed) > 0 {
				fmt.Println()
				fmt.Printf("%s\n", color.New(color.Bold).Sprint("Most recalled memories:"))
				for i, m := range result.TopAccessed {
					summary := truncateSummary(m.Summary, m.Content)
					fmt.Printf("%s(%dx) %s\n", dim.Sprint(fmt.Sprintf("  %d. ", i+1)), m.AccessCount, summary)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&days, "days", 7, "Time window in days")
	return cmd
}

func truncateSummary(summary, content string) string {
	s := summary
	if s == "" {
		s = content
	}
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}
