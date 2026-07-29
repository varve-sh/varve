package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/varve-sh/varve/internal/types"
)

func newEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <id|prefix>",
		Short: "Open a memory in $EDITOR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			id := resolveID(k, args[0])
			if id == "" {
				fmt.Printf("Memory %s not found\n", args[0])
				return nil
			}

			mem, err := k.Get(id)
			if err != nil {
				return err
			}
			if mem == nil {
				fmt.Printf("Memory %s not found\n", id)
				return nil
			}

			// Write content to a temp file
			tmp, err := os.CreateTemp("", "varve-edit-*.txt")
			if err != nil {
				return fmt.Errorf("creating temp file: %w", err)
			}
			defer os.Remove(tmp.Name())

			if _, err := tmp.WriteString(mem.Content); err != nil {
				tmp.Close()
				return fmt.Errorf("writing temp file: %w", err)
			}
			tmp.Close()

			// Open editor
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = os.Getenv("VISUAL")
			}
			if editor == "" {
				editor = "vi"
			}

			editorCmd := exec.Command(editor, tmp.Name())
			editorCmd.Stdin = os.Stdin
			editorCmd.Stdout = os.Stdout
			editorCmd.Stderr = os.Stderr
			if err := editorCmd.Run(); err != nil {
				return fmt.Errorf("editor exited with error: %w", err)
			}

			// Read back edited content
			data, err := os.ReadFile(tmp.Name())
			if err != nil {
				return fmt.Errorf("reading edited file: %w", err)
			}
			newContent := strings.TrimRight(string(data), "\n")

			if newContent == mem.Content {
				fmt.Println("No changes.")
				return nil
			}

			updated, err := k.Update(id, types.MemoryUpdateInput{Content: &newContent})
			if err != nil {
				return err
			}
			fmt.Printf("Updated %s: %s\n", updated.ID, updated.Summary)
			return nil
		},
	}
}

// resolvableStatuses are the pools resolveID searches, in order. The first is
// "" — everything live, i.e. active notes plus non-terminal decisions — and the
// rest are the pools a live query cannot reach. Listing `active` only (the v1
// behaviour) meant a prefix printed by `list --status proposed` did not resolve
// for `rm`, `edit` or `update` (ADR-0001 D2/D10).
var resolvableStatuses = []types.MemoryStatus{
	"",
	types.MemoryStatusStale,
	types.MemoryStatusArchived,
	types.MemoryStatus(types.StatusSuperseded),
	types.MemoryStatus(types.StatusReverted),
	types.MemoryStatus(types.StatusRejected),
}

// resolveID resolves a full ID or short prefix to a full memory ID.
// Returns "" if not found or ambiguous.
func resolveID(k interface {
	List(types.ListOptions) ([]types.Memory, error)
}, prefix string) string {
	if len(prefix) >= 26 {
		return prefix
	}
	upper := strings.ToUpper(prefix)
	for _, status := range resolvableStatuses {
		all, err := k.List(types.ListOptions{Limit: 1000, Status: status})
		if err != nil {
			return ""
		}
		var matches []string
		for _, m := range all {
			if strings.HasPrefix(m.ID, upper) {
				matches = append(matches, m.ID)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
		if len(matches) > 1 {
			return "" // ambiguous
		}
	}
	return ""
}
