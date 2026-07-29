package tui

import (
	"strings"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// The TUI's `d` key calls kernel.Delete, which for a decision is not a delete:
// a proposal is rejected and a binding decision is reverted, both terminal and
// irreversible (§D3). Prompting "Delete this memory?" hid an irreversible
// governance action behind a word that described neither — and it meant the
// TUI could reject a proposal but not accept one.
func TestDisposalPrompt_SaysWhatWillActuallyHappen(t *testing.T) {
	cases := []struct {
		name string
		mem  *types.Memory
		want string
	}{
		{"note", &types.Memory{
			Type: types.MemoryTypeNote, Status: types.MemoryStatusActive,
		}, "Delete this note?"},
		{"proposal", &types.Memory{
			Type: types.MemoryTypeDecision, Status: types.MemoryStatus(types.StatusProposed),
		}, "Reject this proposal?"},
		{"active decision", &types.Memory{
			Type: types.MemoryTypeConvention, Status: types.MemoryStatusActive,
		}, "Revert this decision?"},
		{"violated decision", &types.Memory{
			Type: types.MemoryTypeDecision, Status: types.MemoryStatus(types.StatusViolated),
		}, "Revert this decision?"},
		{"terminal decision", &types.Memory{
			Type: types.MemoryTypeDecision, Status: types.MemoryStatus(types.StatusRejected),
		}, "already terminal"},
	}
	for _, c := range cases {
		got := disposalPrompt(c.mem)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s prompt = %q, want it to contain %q", c.name, got, c.want)
		}
		if c.mem.Type.IsDecision() && strings.Contains(got, "Delete") {
			t.Errorf("%s prompt says Delete, but a decision with history is never "+
				"hard-deleted (§D3): %q", c.name, got)
		}
	}
}
