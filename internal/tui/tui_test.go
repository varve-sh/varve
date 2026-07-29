package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/memtrace-dev/memtrace/internal/kernel"
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
		if strings.Contains(got, "(n to go back)") {
			t.Errorf("%s prompt offers only n while y is still bound: %q", c.name, got)
		}
		if c.mem.Type.IsDecision() && strings.Contains(got, "Delete") {
			t.Errorf("%s prompt says Delete, but a decision with history is never "+
				"hard-deleted (§D3): %q", c.name, got)
		}
	}
}

// F29: `y` on an already-terminal decision reported a disposal that did not
// happen. kernel.Delete returns (false, nil) for a terminal row and the
// handler discarded the bool, emitting deletedMsg, so the row vanished from
// the list and reappeared on the next reload. The prompt is a pure function
// and was tested; the key path was not.
func TestConfirmDelete_ReportsNothingWhenNothingHappened(t *testing.T) {
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	k := kernel.New(filepath.Join(t.TempDir(), "tui.db"), "proj-tui")
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	// A rejected decision: terminal, so there is nothing left to forget.
	d, err := k.Decisions().Propose(kernel.DecisionInput{
		ProjectID: "proj-tui", Title: "Sessions live in Redis.",
		Source: types.DecisionSourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.RejectDecision(d.ID, "duplicate"); err != nil {
		t.Fatal(err)
	}
	mem, err := k.Get(d.ID)
	if err != nil || mem == nil {
		t.Fatalf("get: %v, %v", mem, err)
	}

	m := newModel(k, []types.Memory{*mem})
	m.selected = mem
	m.state = viewConfirmDelete

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isDeleted := msg.(deletedMsg); isDeleted {
				t.Fatal("the TUI reported a disposal that did not happen")
			}
			if e, isErr := msg.(errMsg); isErr {
				t.Fatalf("unexpected error message: %v", e)
			}
		}
	}
	if next.(model).state != viewDetail {
		t.Errorf("state = %v, want the detail view back", next.(model).state)
	}

	// A note, by contrast, really is deleted and really is reported.
	note, _, err := k.Save(types.MemorySaveInput{
		Content: "CI runs on arm64.", Type: types.MemoryTypeFact,
		Source: types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	m2 := newModel(k, []types.Memory{*note})
	m2.selected = note
	m2.state = viewConfirmDelete
	_, cmd = m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("deleting a note reported nothing")
	}
	if _, ok := cmd().(deletedMsg); !ok {
		t.Error("deleting a note must report the removal")
	}
}
