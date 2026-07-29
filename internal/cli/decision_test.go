package cli

import (
	"strings"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// proposedProject sets up a project holding one agent-saved decision, which
// ADR-0001 D2 quarantines as `proposed`, plus one note.
func proposedProject(t *testing.T) (*kernel.MemoryKernel, string) {
	t.Helper()
	k, root := setupProject(t,
		types.MemorySaveInput{
			Content:   "All handlers must validate the Authorization header",
			Type:      types.MemoryTypeDecision,
			Source:    types.MemorySourceAgent,
			SessionID: "sess-1",
			Agent:     "claude",
		},
		types.MemorySaveInput{Content: "CI runs on arm64", Type: types.MemoryTypeFact},
	)
	return k, root
}

// The queue has to be visible through the surfaces a user actually runs.
// F1 and F12 are the same bug: rows that exist but cannot be seen.
func TestListCmd_ShowsProposedDecisionsByDefault(t *testing.T) {
	proposedProject(t)

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "All handlers must validate") {
		t.Errorf("`list` hides the proposed decision:\n%s", out)
	}
	if !strings.Contains(out, "CI runs on arm64") {
		t.Errorf("`list` lost the note:\n%s", out)
	}
	if !strings.Contains(out, "[proposed]") {
		t.Errorf("`list` does not mark the proposal as pending:\n%s", out)
	}
	jsonOut, err := runCmd(t, "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, jsonOut)
	}
	if !strings.Contains(jsonOut, `"status":"proposed"`) {
		t.Errorf("`list --json` does not surface the proposal's status:\n%s", jsonOut)
	}
}

func TestExportCmd_IncludesProposedDecisions(t *testing.T) {
	proposedProject(t)

	out, err := runCmd(t, "export")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if !strings.Contains(out, "All handlers must validate") {
		t.Errorf("export — the backup path and D9's migration mechanism — omitted "+
			"the proposed decision:\n%s", out)
	}
}

// A 12-char prefix printed by `list` must resolve for rm/edit/update.
func TestResolveID_ResolvesAProposedDecisionPrefix(t *testing.T) {
	k, _ := proposedProject(t)

	ds, err := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if err != nil || len(ds) != 1 {
		t.Fatalf("expected one decision, got %d (%v)", len(ds), err)
	}
	id := ds[0].ID
	if got := resolveID(k, id[:12]); got != id {
		t.Errorf("resolveID(%q) = %q, want %q", id[:12], got, id)
	}
}

func TestDecisionPendingCmd_ListsTheQueue(t *testing.T) {
	proposedProject(t)

	out, err := runCmd(t, "decision", "pending")
	if err != nil {
		t.Fatalf("decision pending: %v\n%s", err, out)
	}
	if !strings.Contains(out, "All handlers must validate") {
		t.Errorf("the queue is empty:\n%s", out)
	}
	if strings.Contains(out, "CI runs on arm64") {
		t.Errorf("notes are not part of the decision queue:\n%s", out)
	}
}

func TestDecisionAcceptCmd_MakesTheDecisionBinding(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	id := ds[0].ID

	// An agent-saved decision carries no evidence, so acceptance must refuse
	// until the human forces it or attaches some (D4).
	if _, err := runCmd(t, "decision", "accept", id[:12]); err == nil {
		t.Fatal("accepting an unevidenced decision must refuse without --force")
	}

	out, err := runCmd(t, "decision", "accept", id[:12], "--evidence", "commit:9f2c1ab")
	if err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Accepted") {
		t.Errorf("no confirmation printed:\n%s", out)
	}

	d, err := k.Decisions().GetDecision(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != types.StatusActive {
		t.Errorf("status = %s, want active", d.Status)
	}
	// The evidence attached at the CLI is accepting evidence: it was present at
	// the proposed→active transition (D4).
	ev, err := k.Decisions().Evidence(id)
	if err != nil || len(ev) != 1 {
		t.Fatalf("evidence = %d rows (%v), want 1", len(ev), err)
	}
	if !ev[0].Accepting {
		t.Error("evidence present at acceptance must be marked accepting")
	}
	// The acceptance is on the record, unforced.
	events, err := k.Decisions().Events(kernel.EventFilter{
		DecisionID: id, Kind: types.EventDecisionAccepted,
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("decision.accepted events = %d (%v), want 1", len(events), err)
	}
	if events[0].Payload["forced"] != false {
		t.Errorf("forced = %v, want false — the flag must mean unevidenced and nothing else",
			events[0].Payload["forced"])
	}
}

func TestDecisionAcceptCmd_ForceIsRecorded(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	id := ds[0].ID

	if out, err := runCmd(t, "decision", "accept", id[:12], "--force"); err != nil {
		t.Fatalf("accept --force: %v\n%s", err, out)
	}
	events, err := k.Decisions().Events(kernel.EventFilter{
		DecisionID: id, Kind: types.EventDecisionAccepted,
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("decision.accepted events = %d (%v), want 1", len(events), err)
	}
	if events[0].Payload["forced"] != true {
		t.Errorf("forced = %v, want true", events[0].Payload["forced"])
	}
}

func TestDecisionRejectCmd_IsTerminalAndKeepsTheRecord(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	id := ds[0].ID

	if out, err := runCmd(t, "decision", "reject", id[:12], "--reason", "duplicate"); err != nil {
		t.Fatalf("reject: %v\n%s", err, out)
	}
	d, err := k.Decisions().GetDecision(id)
	if err != nil {
		t.Fatalf("a rejected decision must survive as an audit record: %v", err)
	}
	if d.Status != types.StatusRejected {
		t.Errorf("status = %s, want rejected", d.Status)
	}
	events, err := k.Decisions().Events(kernel.EventFilter{
		DecisionID: id, Kind: types.EventDecisionRejected,
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("decision.rejected events = %d (%v), want 1", len(events), err)
	}
	if events[0].Payload["reason"] != "duplicate" {
		t.Errorf("reason = %v, want duplicate", events[0].Payload["reason"])
	}
}

func TestDecisionCmds_UnknownIDIsActionable(t *testing.T) {
	proposedProject(t)
	for _, sub := range []string{"accept", "reject"} {
		_, err := runCmd(t, "decision", sub, "01ZZZZZZ")
		if err == nil {
			t.Errorf("decision %s on an unknown prefix must fail", sub)
			continue
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("decision %s error = %v, want a not-found message", sub, err)
		}
	}
}

// F14: the human `status` output must list the class that actually exists and
// account for every row it counts. It iterated the v1 type set, so the note
// count was computed and thrown away, and the status line named three of the
// nine buckets `total` sums.
func TestStatusCmd_HumanOutputAccountsForEveryRow(t *testing.T) {
	proposedProject(t)

	out, err := runCmd(t, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "note:") {
		t.Errorf("`status` does not list the note class:\n%s", out)
	}
	if !strings.Contains(out, "Memories:  2 total") {
		t.Errorf("total is wrong:\n%s", out)
	}
	// One proposed decision plus one active note — both must be named.
	if !strings.Contains(out, "1 proposed") || !strings.Contains(out, "1 active") {
		t.Errorf("the status line does not account for its own total:\n%s", out)
	}
}
