package cli

import (
	"encoding/json"
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

// F24: the *type* axis has to account for the same population as the total and
// the status axis. It counted live rows only while the total summed every
// bucket, so one terminal decision made the breakdown quietly short — the same
// defect as F14, on the other axis. The F14 test could not see it because its
// store had no terminal rows.
func TestStatusCmd_BothAxesCountTheSameRows(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if err := k.Decisions().Reject(ds[0].ID, "duplicate", types.ActorHuman); err != nil {
		t.Fatal(err)
	}
	// Store now holds: 1 rejected decision, 1 active note.

	out, err := runCmd(t, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Memories:  2 total") {
		t.Errorf("total is wrong:\n%s", out)
	}
	if !strings.Contains(out, "decision:    1") {
		t.Errorf("the rejected decision is missing from the type breakdown:\n%s", out)
	}
	if !strings.Contains(out, "note:        1") {
		t.Errorf("the note is missing from the type breakdown:\n%s", out)
	}
	if !strings.Contains(out, "1 rejected") {
		t.Errorf("the status line does not name the terminal row:\n%s", out)
	}

	// And the JSON path, where a consumer can actually add them up.
	jsonOut, err := runCmd(t, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, jsonOut)
	}
	var got struct {
		Total    int            `json:"total"`
		ByType   map[string]int `json:"by_type"`
		ByStatus map[string]int `json:"by_status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	sum := func(m map[string]int) int {
		n := 0
		for _, v := range m {
			n += v
		}
		return n
	}
	if sum(got.ByType) != got.Total || sum(got.ByStatus) != got.Total {
		t.Errorf("axes disagree: total=%d by_type=%d by_status=%d (%v / %v)",
			got.Total, sum(got.ByType), sum(got.ByStatus), got.ByType, got.ByStatus)
	}
}

// A2.3: `decision promote` is the sanctioned note→decision motion. Today's
// answer without it is "retype it by hand", and a retype-in-place would be a
// decision with no birth event and no quarantine.
func TestDecisionPromoteCmd_ProposesFromTheNoteAndKeepsItLive(t *testing.T) {
	k, _ := setupProject(t, types.MemorySaveInput{
		Content:   "We only ever migrate forward.",
		Type:      types.MemoryTypeFact,
		Source:    types.MemorySourceUser,
		FilePaths: []string{"internal/kernel/migrate.go"},
	})
	notes, err := k.List(types.ListOptions{Type: types.MemoryTypeNote, Limit: 10})
	if err != nil || len(notes) != 1 {
		t.Fatalf("expected one note, got %d (%v)", len(notes), err)
	}
	noteID := notes[0].ID

	out, err := runCmd(t, "decision", "promote", noteID[:12])
	if err != nil {
		t.Fatalf("promote: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Proposed") {
		t.Errorf("no confirmation printed:\n%s", out)
	}

	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if len(ds) != 1 || ds[0].Status != types.StatusProposed {
		t.Fatalf("decisions = %+v, want one proposed", ds)
	}
	if ds[0].SourceRef != "note:"+noteID {
		t.Errorf("source_ref = %q, want the link back to the note", ds[0].SourceRef)
	}
	if n, err := k.Notes().Get(noteID); err != nil || n.Status != types.MemoryStatusActive {
		t.Errorf("note status = %v (%v), want active while the promotion is pending", n.Status, err)
	}

	// promote → accept is one motion: the auto-attached import evidence means
	// no --force is needed.
	if out, err := runCmd(t, "decision", "accept", ds[0].ID[:12]); err != nil {
		t.Fatalf("accept after promote must not need --force: %v\n%s", err, out)
	}
	if n, err := k.Notes().Get(noteID); err != nil || n.Status != types.MemoryStatusArchived {
		t.Errorf("note status = %v (%v), want archived by the acceptance", n.Status, err)
	}
}

// F19 through the surface that reaches it: `decision accept` reads as
// idempotent and printed "Accepted" a second time, which promoted evidence
// attached after the acceptance.
func TestDecisionAcceptCmd_RefusesToAcceptTwice(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	id := ds[0].ID

	if out, err := runCmd(t, "decision", "accept", id[:12], "--evidence", "commit:9f2c1ab"); err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	// A conforming commit attached weeks later.
	if _, err := k.Decisions().AddEvidence(id, kernel.EvidenceInput{
		Kind: types.EvidenceKindCommit, Ref: "deadbee", AddedBy: types.ActorHuman,
	}); err != nil {
		t.Fatal(err)
	}

	out, err := runCmd(t, "decision", "accept", id[:12])
	if err == nil {
		t.Fatalf("a second accept must refuse, got: %s", out)
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Errorf("error = %v, want it to say the decision is already active", err)
	}

	ev, _ := k.Decisions().Evidence(id)
	for _, e := range ev {
		if e.Ref == "deadbee" && e.Accepting {
			t.Fatal("the later commit became accepting evidence; a revert of it " +
				"would now terminate the decision (§D6, item-6 ruling)")
		}
	}
	accepted, _ := k.Decisions().Events(kernel.EventFilter{
		DecisionID: id, Kind: types.EventDecisionAccepted,
	})
	if len(accepted) != 1 {
		t.Errorf("%d decision.accepted events, want 1", len(accepted))
	}
}

// F23. Evidence was attached in its own transaction before the acceptance
// could succeed, so a repeat leaked `UNIQUE constraint failed: ...(2067)` —
// the raw-constraint-abort class the ADRs legislate against by name — and a
// failed acceptance left rows the user did not get the decision for.
func TestDecisionAcceptCmd_EvidenceIsCheckedBeforeItIsWritten(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	id := ds[0].ID

	// Duplicate evidence is benign: the row is already there, so the acceptance
	// proceeds and no constraint abort reaches the user.
	if _, err := k.Decisions().AddEvidence(id, kernel.EvidenceInput{
		Kind: types.EvidenceKindCommit, Ref: "9f2c1ab", AddedBy: types.ActorHuman,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := runCmd(t, "decision", "accept", id[:12], "--evidence", "commit:9f2c1ab")
	if err != nil {
		t.Fatalf("a duplicate --evidence must not fail the acceptance: %v\n%s", err, out)
	}
	if strings.Contains(out, "UNIQUE constraint") {
		t.Errorf("a raw constraint abort reached the user:\n%s", out)
	}
	if !strings.Contains(out, "already attached") {
		t.Errorf("the duplicate was not reported:\n%s", out)
	}
	if ev, _ := k.Decisions().Evidence(id); len(ev) != 1 {
		t.Errorf("evidence = %d rows, want 1", len(ev))
	}

	// A decision that cannot be accepted must not collect evidence on the way
	// to being told so.
	out, err = runCmd(t, "decision", "accept", id[:12], "--evidence", "commit:deadbee")
	if err == nil {
		t.Fatalf("accepting an active decision must refuse, got: %s", out)
	}
	ev, _ := k.Decisions().Evidence(id)
	for _, e := range ev {
		if e.Ref == "deadbee" {
			t.Error("evidence was attached to a decision the command then refused to accept")
		}
	}
}

// F25: the synthetic CLI session was minted and then used by nothing but
// recall.served. A human governance action — accept, reject, the evidence it
// attaches — is exactly the row a Tier-3 audit trail is sold on, and it landed
// with a NULL session_id.
func TestDecisionCmds_GovernanceActionsCarryTheCLISession(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	id := ds[0].ID

	if out, err := runCmd(t, "decision", "accept", id[:12], "--evidence", "commit:9f2c1ab"); err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}

	for _, kind := range []types.EventKind{
		types.EventDecisionAccepted, types.EventEvidenceAdded,
	} {
		evs, err := k.Decisions().Events(kernel.EventFilter{DecisionID: id, Kind: kind})
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s events = %d (%v), want 1", kind, len(evs), err)
		}
		if evs[0].SessionID == "" {
			t.Errorf("%s carries a NULL session_id — the governance action is unattributable", kind)
		}
		if evs[0].Agent != kernel.SessionAgentCLI {
			t.Errorf("%s agent = %q, want %q so §D3 can exclude it from coverage denominators",
				kind, evs[0].Agent, kernel.SessionAgentCLI)
		}
		// And the window it names exists.
		started, err := k.Decisions().Events(kernel.EventFilter{
			SessionID: evs[0].SessionID, Kind: types.EventSessionStarted,
		})
		if err != nil || len(started) != 1 {
			t.Errorf("session %s has %d session.started rows (%v), want 1 — nothing to join to",
				evs[0].SessionID, len(started), err)
		}
	}
}

// ADR-0001 Amendment 3 (A3.1), the human end. An agent's disposal request is
// only "one keystroke from confirmed" if a human can see it where proposals are
// triaged — including for binding decisions, which never appear in the
// proposals list at all.
func TestDecisionPendingCmd_ShowsDisposalRequests(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	proposal := ds[0].ID

	// A binding decision the agent also wants gone.
	binding, _, err := k.Save(types.MemorySaveInput{
		Content: "Sessions are server-side only.",
		Type:    types.MemoryTypeDecision,
		Source:  types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{proposal, binding.ID} {
		if _, err := k.Forget(id, types.ActorAgent); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing transitioned on either.
	if d, _ := k.Decisions().GetDecision(proposal); d.Status != types.StatusProposed {
		t.Errorf("proposal status = %s, want proposed", d.Status)
	}
	if d, _ := k.Decisions().GetDecision(binding.ID); d.Status != types.StatusActive {
		t.Errorf("binding status = %s, want active — an agent may not repeal it", d.Status)
	}

	out, err := runCmd(t, "decision", "pending")
	if err != nil {
		t.Fatalf("decision pending: %v\n%s", err, out)
	}
	if !strings.Contains(out, "All handlers must validate") {
		t.Errorf("the proposal is missing from the queue:\n%s", out)
	}
	if !strings.Contains(out, "Sessions are server-side only.") {
		t.Errorf("a binding decision with a disposal request is invisible to triage:\n%s", out)
	}
	if strings.Count(out, "disposal requested by an agent") != 2 {
		t.Errorf("both requests must be shown as requests:\n%s", out)
	}
	if !strings.Contains(out, "decision revert") {
		t.Errorf("the queue does not say how to confirm a disposal:\n%s", out)
	}

	// The human confirmation is the transition.
	if out, err := runCmd(t, "decision", "revert", binding.ID[:12]); err != nil {
		t.Fatalf("decision revert: %v\n%s", err, out)
	}
	d, err := k.Decisions().GetDecision(binding.ID)
	if err != nil {
		t.Fatalf("the row must survive as an audit record: %v", err)
	}
	if d.Status != types.StatusReverted {
		t.Errorf("status = %s after human confirmation, want reverted", d.Status)
	}
	// And it leaves the triage queue.
	out, _ = runCmd(t, "decision", "pending")
	if strings.Contains(out, "Sessions are server-side only.") {
		t.Errorf("a confirmed disposal is still in the queue:\n%s", out)
	}
}

// The CLI is the human channel, so `rm` still transitions — and says which
// transition it made rather than claiming a deletion.
func TestRmCmd_NamesTheTransitionItMade(t *testing.T) {
	k, _ := proposedProject(t)
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})

	out, err := runCmd(t, "rm", ds[0].ID[:12])
	if err != nil {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Rejected") {
		t.Errorf("`rm` on a proposal reports %q; it rejects, it does not delete", out)
	}
	if d, _ := k.Decisions().GetDecision(ds[0].ID); d.Status != types.StatusRejected {
		t.Errorf("status = %s, want rejected", d.Status)
	}
}
