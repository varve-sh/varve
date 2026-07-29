package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show project status",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, projectRoot, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			cfg := util.GetProjectConfig()
			entry := cfg.Projects[projectRoot]
			dbPath := util.GetProjectDbPath(projectRoot)

			// Count by type
			counts := map[string]int{}
			// The v2 schema has three classes: two governed (decision,
			// convention) and one note class — `fact` and `event` were
			// collapsed, and what they distinguished now lives in tags
			// (ADR-0001 D1, D9).
			for _, t := range []types.MemoryType{
				types.MemoryTypeDecision,
				types.MemoryTypeConvention,
				types.MemoryTypeNote,
			} {
				n, _ := k.Count(t, "")
				counts[string(t)] = n
			}

			// Count by status
			statusCounts := map[string]int{}
			// Note statuses plus the six decision lifecycle states (D2).
			for _, s := range []types.MemoryStatus{
				types.MemoryStatusActive,
				types.MemoryStatusStale,
				types.MemoryStatusArchived,
				types.MemoryStatus(types.StatusProposed),
				types.MemoryStatus(types.StatusViolated),
				types.MemoryStatus(types.StatusSuperseded),
				types.MemoryStatus(types.StatusReverted),
				types.MemoryStatus(types.StatusRejected),
			} {
				n, _ := k.Count("", s)
				if n == 0 {
					continue
				}
				statusCounts[string(s)] = n
			}
			total := 0
			for _, n := range statusCounts {
				total += n
			}

			// DB size
			dbSize := dbFileSize(dbPath)

			embedProvider, embedModel := k.EmbedInfo()
			if asJSON {
				out := map[string]interface{}{
					"project":        entry.Name,
					"root":           projectRoot,
					"db_path":        dbPath,
					"db_size":        dbSize,
					"total":          total,
					"by_type":        counts,
					"by_status":      statusCounts,
					"embed_provider": embedProvider,
					"embed_model":    embedModel,
				}
				return json.NewEncoder(os.Stdout).Encode(out)
			}

			bold := color.New(color.Bold)
			dim := color.New(color.Faint)

			bold.Printf("Project:   %s\n", entry.Name)
			dim.Printf("Root:      %s\n", projectRoot)
			dim.Printf("Database:  %s (%s)\n", filepath.Join(".memtrace", "memtrace.db"), dbSize)
			fmt.Println()
			bold.Printf("Memories:  %d total\n", total)
			// The same three classes the counts were collected for. Iterating the
			// v1 set here printed 0 for `fact`/`event` and dropped the note count
			// on the floor, so `total` never matched its own breakdown.
			for _, t := range []types.MemoryType{
				types.MemoryTypeDecision, types.MemoryTypeConvention, types.MemoryTypeNote,
			} {
				if n := counts[string(t)]; n > 0 {
					fmt.Printf("  %-12s %d\n", string(t)+":", n)
				}
			}
			fmt.Println()
			// Every non-zero bucket, in lifecycle order. `total` sums all of them,
			// so a status line naming only three of nine could not add up.
			ordered := []string{"proposed", "active", "violated", "stale",
				"superseded", "reverted", "rejected", "archived"}
			var parts []string
			for _, s := range ordered {
				if n := statusCounts[s]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", n, s))
				}
			}
			if len(parts) == 0 {
				parts = append(parts, "0 active")
			}
			line := "Status:    " + strings.Join(parts, ", ") + "\n"
			if statusCounts["stale"] > 0 || statusCounts["proposed"] > 0 {
				color.New(color.FgYellow).Printf("%s", line)
				if statusCounts["stale"] > 0 {
					dim.Printf("           run 'memtrace list --status stale' to review\n")
				}
				if statusCounts["proposed"] > 0 {
					dim.Printf("           run 'memtrace decision pending' to confirm or decline proposals\n")
				}
			} else {
				fmt.Printf("%s", line)
			}

			fmt.Println()
			if embedProvider == "disabled" {
				dim.Printf("Embeddings: disabled\n")
			} else {
				fmt.Printf("Embeddings: %s (%s)\n", embedProvider, embedModel)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func dbFileSize(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "unknown"
	}
	kb := info.Size() / 1024
	if kb < 1024 {
		return fmt.Sprintf("%d KB", kb)
	}
	return fmt.Sprintf("%.1f MB", float64(info.Size())/(1024*1024))
}
