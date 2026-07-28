package kernel

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

const testProject = "proj-1"

func newDecisionStore(t *testing.T) *DecisionStore {
	t.Helper()
	return NewDecisionStore(freshDB(t))
}

func baseInput() DecisionInput {
	return DecisionInput{
		ProjectID: testProject,
		Title:     "always wrap errors with %w",
		Body:      "so callers can errors.Is",
		Scope:     []string{"internal/**"},
		Source:    types.DecisionSourceAgent,
		SessionID: "sess-1",
		Agent:     "claude-code",
		Model:     "opus-5",
	}
}

func eventKinds(t *testing.T, s *DecisionStore, decisionID string) []types.EventKind {
	t.Helper()
	evs, err := s.Events(EventFilter{DecisionID: decisionID})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]types.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func mustEvent(t *testing.T, s *DecisionStore, decisionID string, kind types.EventKind) types.Event {
	t.Helper()
	evs, err := s.Events(EventFilter{DecisionID: decisionID, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected exactly one %s event for %s, got %d", kind, decisionID, len(evs))
	}
	return evs[0]
}

func addCommitEvidence(t *testing.T, s *DecisionStore, id, sha string) {
	t.Helper()
	if _, err := s.AddEvidence(id, EvidenceInput{
		Kind: types.EvidenceKindCommit, Ref: sha, AddedBy: types.ActorHuman,
	}); err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}
}

// --- birth state ---

func TestPropose_IsAlwaysQuarantined(t *testing.T) {
	s := newDecisionStore(t)
	d, err := s.Propose(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != types.StatusProposed {
		t.Errorf("status = %s, want proposed", d.Status)
	}
	if got := eventKinds(t, s, d.ID); len(got) != 1 || got[0] != types.EventDecisionProposed {
		t.Errorf("events = %v, want [decision.proposed]", got)
	}
	ev := mustEvent(t, s, d.ID, types.EventDecisionProposed)
	if ev.Actor != types.ActorAgent {
		t.Errorf("actor = %s, want agent", ev.Actor)
	}
	if ev.Payload["via"] != "mcp" {
		t.Errorf("payload via = %v, want mcp", ev.Payload["via"])
	}
	if ev.SessionID != "sess-1" {
		t.Errorf("event lost the session id: %q", ev.SessionID)
	}
}

func TestPropose_AgentSaveWithoutSessionIsRejected(t *testing.T) {
	s := newDecisionStore(t)
	in := baseInput()
	in.SessionID = ""
	if _, err := s.Propose(in); !errors.Is(err, types.ErrValidation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
}

func TestProposeAccepted_OnlyForUserSource(t *testing.T) {
	s := newDecisionStore(t)

	in := baseInput()
	in.Source = types.DecisionSourceUser
	in.SessionID = ""
	in.Evidence = []EvidenceInput{{Kind: types.EvidenceKindCommit, Ref: "abc", AddedBy: types.ActorHuman}}
	d, err := s.ProposeAccepted(in, AcceptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != types.StatusActive {
		t.Errorf("status = %s, want active", d.Status)
	}
	if d.DecidedAt == nil {
		t.Error("decided_at must be set on an active decision")
	}
	// Both the proposal and the acceptance are recorded: an active decision
	// always has a decision.accepted event (D4's audit witness).
	got := eventKinds(t, s, d.ID)
	if len(got) != 2 || got[0] != types.EventDecisionProposed || got[1] != types.EventDecisionAccepted {
		t.Errorf("events = %v, want [decision.proposed decision.accepted]", got)
	}

	agent := baseInput()
	if _, err := s.ProposeAccepted(agent, AcceptOptions{}); err == nil {
		t.Error("an agent-sourced decision must not be born active")
	}
}

// --- acceptance ---

func TestAccept_RequiresEvidence(t *testing.T) {
	s := newDecisionStore(t)
	d, err := s.Propose(baseInput())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Accept(d.ID, AcceptOptions{}); !errors.Is(err, types.ErrNoEvidence) {
		t.Fatalf("err = %v, want ErrNoEvidence", err)
	}
	// The failed acceptance left nothing behind.
	reloaded, _ := s.GetDecision(d.ID)
	if reloaded.Status != types.StatusProposed {
		t.Errorf("status = %s after a failed accept, want proposed", reloaded.Status)
	}
	if got := eventKinds(t, s, d.ID); len(got) != 1 {
		t.Errorf("a failed acceptance emitted events: %v", got)
	}

	addCommitEvidence(t, s, d.ID, "sha-accept")
	if _, err := s.Accept(d.ID, AcceptOptions{}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	ev := mustEvent(t, s, d.ID, types.EventDecisionAccepted)
	if ev.Payload["evidence_count"].(float64) != 1 {
		t.Errorf("evidence_count = %v, want 1", ev.Payload["evidence_count"])
	}
	if ev.Payload["forced"] != false {
		t.Errorf("forced = %v, want false", ev.Payload["forced"])
	}
}

func TestAccept_ForceIsRecordedInTheAuditTrail(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	if _, err := s.Accept(d.ID, AcceptOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	ev := mustEvent(t, s, d.ID, types.EventDecisionAccepted)
	if ev.Payload["forced"] != true {
		t.Errorf(`payload["forced"] = %v, want true`, ev.Payload["forced"])
	}
	if ev.Payload["evidence_count"].(float64) != 0 {
		t.Errorf("evidence_count = %v, want 0", ev.Payload["evidence_count"])
	}
}

// D4: the accepting set is a fact about one moment. Rows present at the
// transition are accepting; rows attached later never are.
func TestAccept_MarksOnlyEvidencePresentAtTheTransition(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	addCommitEvidence(t, s, d.ID, "sha-before-1")
	addCommitEvidence(t, s, d.ID, "sha-before-2")

	if _, err := s.Accept(d.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}
	addCommitEvidence(t, s, d.ID, "sha-after")

	evs, err := s.Evidence(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"sha-before-1": true, "sha-before-2": true, "sha-after": false}
	for _, e := range evs {
		if e.Accepting != want[e.Ref] {
			t.Errorf("evidence %s accepting = %v, want %v", e.Ref, e.Accepting, want[e.Ref])
		}
	}

	accepting, err := s.AcceptingCommitEvidence("sha-after")
	if err != nil {
		t.Fatal(err)
	}
	if len(accepting) != 0 {
		t.Errorf("a later-added commit must not count as accepting evidence: %v", accepting)
	}
	accepting, _ = s.AcceptingCommitEvidence("sha-before-1")
	if len(accepting) != 1 || accepting[0] != d.ID {
		t.Errorf("AcceptingCommitEvidence(sha-before-1) = %v, want [%s]", accepting, d.ID)
	}
}

// --- supersession ---

func TestAccept_SupersedesPredecessorsInOneTransaction(t *testing.T) {
	s := newDecisionStore(t)

	oldD, _ := s.Propose(baseInput())
	addCommitEvidence(t, s, oldD.ID, "sha-old")
	if _, err := s.Accept(oldD.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}

	in := baseInput()
	in.Title = "wrap errors with %w and never with %v"
	in.Supersedes = []string{oldD.ID}
	newD, err := s.Propose(in)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-linking alone changes nothing on the predecessor (D5).
	still, _ := s.GetDecision(oldD.ID)
	if still.Status != types.StatusActive {
		t.Fatalf("predecessor status = %s before acceptance, want active", still.Status)
	}

	addCommitEvidence(t, s, newD.ID, "sha-new")
	if _, err := s.Accept(newD.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}

	pred, _ := s.GetDecision(oldD.ID)
	if pred.Status != types.StatusSuperseded {
		t.Errorf("predecessor status = %s, want superseded", pred.Status)
	}
	if pred.SupersededBy != newD.ID {
		t.Errorf("superseded_by = %q, want %q", pred.SupersededBy, newD.ID)
	}
	ev := mustEvent(t, s, oldD.ID, types.EventDecisionSuperseded)
	if ev.Payload["successor_id"] != newD.ID {
		t.Errorf("successor_id = %v, want %s", ev.Payload["successor_id"], newD.ID)
	}
	if ev.Actor != types.ActorSystem {
		t.Errorf("actor = %s, want system", ev.Actor)
	}
}

func TestAccept_LeavesAlreadyTerminalPredecessorsUntouched(t *testing.T) {
	s := newDecisionStore(t)

	dead, _ := s.Propose(baseInput())
	if err := s.Reject(dead.ID, "not this way"); err != nil {
		t.Fatal(err)
	}

	in := baseInput()
	in.Title = "the revived rule"
	in.Supersedes = []string{dead.ID}
	revival, _ := s.Propose(in)
	addCommitEvidence(t, s, revival.ID, "sha-revival")
	if _, err := s.Accept(revival.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}

	old, _ := s.GetDecision(dead.ID)
	if old.Status != types.StatusRejected {
		t.Errorf("terminal predecessor changed to %s; terminal is terminal", old.Status)
	}
	for _, k := range eventKinds(t, s, dead.ID) {
		if k == types.EventDecisionSuperseded {
			t.Error("a terminal predecessor must not get a decision.superseded event")
		}
	}
}

// The reconciled reading of D4's topic_key rule (see the objection in
// planning/decisions-log.md): the successor is pre-linked at proposal time and
// takes the key at acceptance, once the predecessor is terminal.
func TestPropose_TopicKeyCollisionCreatesAPreLinkedSuccessor(t *testing.T) {
	s := newDecisionStore(t)

	in := baseInput()
	in.TopicKey = "error-handling"
	first, err := s.Propose(in)
	if err != nil {
		t.Fatal(err)
	}
	if first.TopicKey != "error-handling" {
		t.Fatalf("first holder lost its topic_key: %q", first.TopicKey)
	}
	addCommitEvidence(t, s, first.ID, "sha-1")
	if _, err := s.Accept(first.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}

	in2 := baseInput()
	in2.TopicKey = "error-handling"
	in2.Title = "wrap errors, and add context"
	second, err := s.Propose(in2)
	if err != nil {
		t.Fatalf("a topic_key collision must create a successor, not fail: %v", err)
	}
	if len(second.Supersedes) != 1 || second.Supersedes[0] != first.ID {
		t.Errorf("supersedes = %v, want [%s]", second.Supersedes, first.ID)
	}
	ev := mustEvent(t, s, second.ID, types.EventDecisionProposed)
	if ev.Payload["topic_key"] != "error-handling" {
		t.Errorf("the pending topic_key must ride on the proposed event, got %v", ev.Payload["topic_key"])
	}

	addCommitEvidence(t, s, second.ID, "sha-2")
	if _, err := s.Accept(second.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetDecision(second.ID)
	if got.TopicKey != "error-handling" {
		t.Errorf("successor topic_key = %q after acceptance, want error-handling", got.TopicKey)
	}
	pred, _ := s.GetDecision(first.ID)
	if pred.Status != types.StatusSuperseded {
		t.Errorf("predecessor status = %s, want superseded", pred.Status)
	}
	// The invariant ADR-0002 §P6 depends on: one non-terminal holder per topic.
	live, err := s.ListDecisions(DecisionFilter{
		ProjectID: testProject, TopicKey: "error-handling",
		Statuses: []types.DecisionStatus{types.StatusProposed, types.StatusActive, types.StatusViolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Errorf("%d non-terminal rows hold the topic_key, want 1", len(live))
	}
}

// --- violation, reinstatement, revert ---

func TestViolationCycle(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	addCommitEvidence(t, s, d.ID, "sha-accept")
	s.Accept(d.ID, AcceptOptions{})

	err := s.MarkViolated(d.ID, ViolationOptions{
		CommitSHA:    "sha-bad",
		RevertedSHA:  "sha-accept",
		Files:        []string{"internal/kernel/store.go"},
		MatchedGlobs: []string{"internal/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetDecision(d.ID)
	if got.Status != types.StatusViolated {
		t.Fatalf("status = %s, want violated", got.Status)
	}
	ev := mustEvent(t, s, d.ID, types.EventDecisionViolated)
	if ev.CommitSHA != "sha-bad" {
		t.Errorf("commit_sha = %q, want sha-bad", ev.CommitSHA)
	}
	if ev.Payload["reverted_sha"] != "sha-accept" {
		t.Errorf("reverted_sha = %v", ev.Payload["reverted_sha"])
	}
	if ev.Actor != types.ActorSystem {
		t.Errorf("actor = %s, want system", ev.Actor)
	}

	// A violated decision is still binding — no state change on re-detection,
	// and no duplicate event.
	if err := s.MarkViolated(d.ID, ViolationOptions{CommitSHA: "sha-bad-2"}); err != nil {
		t.Fatal(err)
	}
	mustEvent(t, s, d.ID, types.EventDecisionViolated)

	if err := s.Reinstate(d.ID, "sha-counter"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDecision(d.ID)
	if got.Status != types.StatusActive {
		t.Fatalf("status = %s after reinstatement, want active", got.Status)
	}
	ev = mustEvent(t, s, d.ID, types.EventDecisionReinstated)
	if ev.Payload["via"] != "counter_revert" {
		t.Errorf("via = %v, want counter_revert", ev.Payload["via"])
	}
}

func TestDismissViolation_EmitsBothEventsAndReinstates(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	addCommitEvidence(t, s, d.ID, "sha-accept")
	s.Accept(d.ID, AcceptOptions{})
	s.MarkViolated(d.ID, ViolationOptions{CommitSHA: "sha-bad"})

	violation := mustEvent(t, s, d.ID, types.EventDecisionViolated)
	if err := s.DismissViolation(d.ID, violation.ID, "false_positive"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetDecision(d.ID)
	if got.Status != types.StatusActive {
		t.Errorf("status = %s, want active", got.Status)
	}
	dismissed := mustEvent(t, s, d.ID, types.EventDecisionViolationDismissed)
	if dismissed.Payload["violation_event_id"] != violation.ID {
		t.Errorf("violation_event_id = %v, want %s", dismissed.Payload["violation_event_id"], violation.ID)
	}
	if dismissed.Actor != types.ActorHuman {
		t.Errorf("actor = %s, want human", dismissed.Actor)
	}
	reinstated := mustEvent(t, s, d.ID, types.EventDecisionReinstated)
	if reinstated.Payload["via"] != "dismissal" {
		t.Errorf("via = %v, want dismissal", reinstated.Payload["via"])
	}
}

func TestRevert_IsTerminal(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	addCommitEvidence(t, s, d.ID, "sha-accept")
	s.Accept(d.ID, AcceptOptions{})

	if err := s.Revert(d.ID, RevertOptions{
		Via: "revert_detected", RevertedEvidenceRef: "sha-accept", CommitSHA: "sha-revert",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetDecision(d.ID)
	if got.Status != types.StatusReverted {
		t.Fatalf("status = %s, want reverted", got.Status)
	}
	ev := mustEvent(t, s, d.ID, types.EventDecisionReverted)
	if ev.Payload["via"] != "revert_detected" || ev.Payload["reverted_evidence_ref"] != "sha-accept" {
		t.Errorf("payload = %v", ev.Payload)
	}
	if ev.Actor != types.ActorSystem {
		t.Errorf("actor = %s, want system for an automatic revert", ev.Actor)
	}

	// Terminal is terminal — the kernel refuses before the trigger has to.
	if _, err := s.Accept(d.ID, AcceptOptions{Force: true}); !errors.Is(err, types.ErrIllegalTransition) {
		t.Errorf("re-accepting a reverted decision = %v, want ErrIllegalTransition", err)
	}
	if err := s.Reject(d.ID, ""); !errors.Is(err, types.ErrIllegalTransition) {
		t.Errorf("rejecting a reverted decision = %v, want ErrIllegalTransition", err)
	}
}

func TestReject_KeepsTheAuditRecord(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	if err := s.Reject(d.ID, "we do the opposite"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDecision(d.ID)
	if err != nil {
		t.Fatalf("a rejected decision must survive: %v", err)
	}
	if got.Status != types.StatusRejected {
		t.Errorf("status = %s, want rejected", got.Status)
	}
	ev := mustEvent(t, s, d.ID, types.EventDecisionRejected)
	if ev.Payload["reason"] != "we do the opposite" {
		t.Errorf("reason = %v", ev.Payload["reason"])
	}
}

// --- expiry ---

func TestMarkExpired_IsIdempotentAndChangesNoState(t *testing.T) {
	s := newDecisionStore(t)
	past := time.Now().UTC().Add(-time.Hour)
	in := baseInput()
	in.ExpiresAt = &past
	d, _ := s.Propose(in)
	addCommitEvidence(t, s, d.ID, "sha-accept")
	s.Accept(d.ID, AcceptOptions{})

	got, _ := s.GetDecision(d.ID)
	if !got.IsExpired(time.Now().UTC()) {
		t.Fatal("decision should read as expired")
	}

	first, err := s.MarkExpired(d.ID)
	if err != nil || !first {
		t.Fatalf("first MarkExpired = (%v, %v), want (true, nil)", first, err)
	}
	second, err := s.MarkExpired(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Error("decision.expired must be emitted at most once")
	}
	mustEvent(t, s, d.ID, types.EventDecisionExpired)

	after, _ := s.GetDecision(d.ID)
	if after.Status != types.StatusActive {
		t.Errorf("expiry must not change state; status = %s", after.Status)
	}
}

// --- metadata vs. normative content ---

func TestUpdateMetadata_IsAdvisoryOnlyAndEmitsFieldList(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	addCommitEvidence(t, s, d.ID, "sha-accept")
	s.Accept(d.ID, AcceptOptions{})

	conf := 0.4
	tags := []string{"errors", "style"}
	later := time.Now().UTC().Add(72 * time.Hour)
	exp := &later
	if err := s.UpdateMetadata(d.ID, MetadataUpdate{
		Tags: &tags, Confidence: &conf, ExpiresAt: &exp,
	}, types.ActorHuman); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetDecision(d.ID)
	if got.Confidence != 0.4 || len(got.Tags) != 2 || got.ExpiresAt == nil {
		t.Errorf("metadata not applied: %+v", got)
	}
	ev := mustEvent(t, s, d.ID, types.EventDecisionUpdated)
	fields, _ := ev.Payload["fields"].([]any)
	if len(fields) != 3 {
		t.Errorf("fields = %v, want three entries", ev.Payload["fields"])
	}
}

func TestEditProposed_OnlyWhileProposed(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())

	edit := DecisionInput{Title: "rewritten", Body: "new body", Scope: []string{"cmd/**"}}
	if err := s.EditProposed(d.ID, edit, types.ActorHuman); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetDecision(d.ID)
	if got.Title != "rewritten" || got.Scope[0] != "cmd/**" {
		t.Errorf("proposal edit not applied: %+v", got)
	}

	addCommitEvidence(t, s, d.ID, "sha-accept")
	s.Accept(d.ID, AcceptOptions{})
	if err := s.EditProposed(d.ID, edit, types.ActorHuman); !errors.Is(err, types.ErrDecisionImmutable) {
		t.Errorf("err = %v, want ErrDecisionImmutable", err)
	}
}

func TestPropose_RejectsInvalidGlobs(t *testing.T) {
	s := newDecisionStore(t)
	in := baseInput()
	in.Scope = []string{"internal/[unclosed"}
	if _, err := s.Propose(in); !errors.Is(err, types.ErrValidation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
}

// --- event emission is transactional ---

// D7: the event and the state change it describes commit together or not at
// all. A rejected transition must leave neither.
func TestTransition_EventAndStateChangeShareATransaction(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())

	// proposed -> violated is illegal; nothing may be written.
	if err := s.MarkViolated(d.ID, ViolationOptions{CommitSHA: "x"}); !errors.Is(err, types.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
	got, _ := s.GetDecision(d.ID)
	if got.Status != types.StatusProposed {
		t.Errorf("status = %s, want proposed", got.Status)
	}
	if kinds := eventKinds(t, s, d.ID); len(kinds) != 1 {
		t.Errorf("events = %v, want only the proposal", kinds)
	}
}

// The DDL leaves events.kind open so new kinds need no migration; the kernel
// is the only guard, and it validates against the §D7 catalogue.
func TestAppendEvent_RejectsKindsOutsideTheCatalogue(t *testing.T) {
	s := newDecisionStore(t)

	err := s.withTx(func(tx *sql.Tx) error {
		_, err := appendEvent(tx, EventInput{
			ProjectID: testProject, Kind: "decision.invented", Actor: types.ActorSystem,
		})
		return err
	})
	if !errors.Is(err, types.ErrUnknownEventKind) {
		t.Fatalf("err = %v, want ErrUnknownEventKind", err)
	}

	err = s.withTx(func(tx *sql.Tx) error {
		_, err := appendEvent(tx, EventInput{
			ProjectID: testProject, Kind: types.EventSessionStarted, Actor: "robot",
		})
		return err
	})
	if !errors.Is(err, types.ErrValidation) {
		t.Fatalf("err = %v, want a validation error for the actor", err)
	}

	// A catalogued attribution-substrate kind is accepted today, so ADR-0002
	// and ADR-0004 need no catalogue change when they land.
	if err := s.withTx(func(tx *sql.Tx) error {
		_, err := appendEvent(tx, EventInput{
			ProjectID: testProject, Kind: types.EventPackItem, Actor: types.ActorSystem,
			SessionID: "sess-1", Payload: map[string]any{"rank": 1},
		})
		return err
	}); err != nil {
		t.Fatalf("pack.item must be emittable: %v", err)
	}
}

func TestEvidence_DedupeIsSurfacedAsAnError(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	addCommitEvidence(t, s, d.ID, "sha-dup")
	if _, err := s.AddEvidence(d.ID, EvidenceInput{
		Kind: types.EvidenceKindCommit, Ref: "sha-dup", AddedBy: types.ActorHuman,
	}); err == nil {
		t.Error("duplicate (decision, kind, ref) evidence must be rejected")
	}
	evs, _ := s.Evidence(d.ID)
	if len(evs) != 1 {
		t.Errorf("%d evidence rows, want 1", len(evs))
	}
	// The failed insert must not have left an evidence.added event behind.
	added, _ := s.Events(EventFilter{DecisionID: d.ID, Kind: types.EventEvidenceAdded})
	if len(added) != 1 {
		t.Errorf("%d evidence.added events, want 1", len(added))
	}
}

func TestGetDecision_NotFound(t *testing.T) {
	s := newDecisionStore(t)
	if _, err := s.GetDecision("nope"); !errors.Is(err, types.ErrDecisionNotFound) {
		t.Errorf("err = %v, want ErrDecisionNotFound", err)
	}
}

// --- ADR-0001 Amendment 1: pending_topic_key ---

func pendingKeyOf(t *testing.T, s *DecisionStore, id string) (topic, pending string) {
	t.Helper()
	d, err := s.GetDecision(id)
	if err != nil {
		t.Fatal(err)
	}
	return d.TopicKey, d.PendingTopicKey
}

// The carrier is a column. The event payload keeps the claimed key as audit,
// but nothing reads it — an append-only row must never be load-bearing current
// state, and D3 keeps a proposal editable in place.
func TestPendingTopicKey_LivesInAColumnNotTheEventPayload(t *testing.T) {
	s := newDecisionStore(t)

	first := baseInput()
	first.TopicKey = "auth"
	holder, err := s.Propose(first)
	if err != nil {
		t.Fatal(err)
	}
	if holder.TopicKey != "auth" || holder.PendingTopicKey != "" {
		t.Fatalf("a first claim goes straight to topic_key: %+v", holder)
	}
	addCommitEvidence(t, s, holder.ID, "sha-holder")
	if _, err := s.Accept(holder.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}

	second := baseInput()
	second.TopicKey = "auth"
	second.Title = "the successor"
	succ, err := s.Propose(second)
	if err != nil {
		t.Fatal(err)
	}
	topic, pending := pendingKeyOf(t, s, succ.ID)
	if topic != "" || pending != "auth" {
		t.Errorf("successor topic_key=%q pending=%q; want ''/auth", topic, pending)
	}
	if len(succ.Supersedes) != 1 || succ.Supersedes[0] != holder.ID {
		t.Errorf("supersedes = %v, want [%s]", succ.Supersedes, holder.ID)
	}
	// Audit copy still present in the payload.
	ev := mustEvent(t, s, succ.ID, types.EventDecisionProposed)
	if ev.Payload["topic_key"] != "auth" {
		t.Errorf("the proposed event should record the claimed key: %v", ev.Payload)
	}

	addCommitEvidence(t, s, succ.ID, "sha-succ")
	if _, err := s.Accept(succ.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}
	topic, pending = pendingKeyOf(t, s, succ.ID)
	if topic != "auth" || pending != "" {
		t.Errorf("after acceptance topic_key=%q pending=%q; want auth/''", topic, pending)
	}
	if pred, _ := s.GetDecision(holder.ID); pred.Status != types.StatusSuperseded {
		t.Errorf("predecessor status = %s, want superseded", pred.Status)
	}
}

// Amendment 1: two proposals may pend the same key — competing successors are
// legitimate — and the index that guards them is deliberately non-unique.
// First acceptance wins; the second fails with a typed, holder-naming error,
// never a raw constraint abort and never a silently dropped key.
func TestPendingTopicKey_CompetingSuccessorsFirstAcceptanceWins(t *testing.T) {
	s := newDecisionStore(t)

	base := baseInput()
	base.TopicKey = "auth"
	holder, _ := s.Propose(base)
	addCommitEvidence(t, s, holder.ID, "sha-holder")
	if _, err := s.Accept(holder.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}

	mk := func(title string) *types.Decision {
		in := baseInput()
		in.TopicKey = "auth"
		in.Title = title
		d, err := s.Propose(in)
		if err != nil {
			t.Fatalf("two proposals may pend the same key: %v", err)
		}
		return d
	}
	a := mk("successor A")
	b := mk("successor B")

	if _, pending := pendingKeyOf(t, s, a.ID); pending != "auth" {
		t.Errorf("A pending = %q", pending)
	}
	if _, pending := pendingKeyOf(t, s, b.ID); pending != "auth" {
		t.Errorf("B pending = %q", pending)
	}

	addCommitEvidence(t, s, a.ID, "sha-a")
	addCommitEvidence(t, s, b.ID, "sha-b")

	if _, err := s.Accept(a.ID, AcceptOptions{}); err != nil {
		t.Fatalf("the first acceptance must win: %v", err)
	}
	if topic, pending := pendingKeyOf(t, s, a.ID); topic != "auth" || pending != "" {
		t.Errorf("A after acceptance: topic=%q pending=%q", topic, pending)
	}

	_, err := s.Accept(b.ID, AcceptOptions{})
	if !errors.Is(err, types.ErrTopicKeyHeld) {
		t.Fatalf("the losing acceptance must fail with ErrTopicKeyHeld, got: %v", err)
	}
	var held *types.TopicKeyHeldError
	if !errors.As(err, &held) {
		t.Fatalf("error must carry the holder: %v", err)
	}
	if held.HolderID != a.ID || held.TopicKey != "auth" || held.HolderStatus != types.StatusActive {
		t.Errorf("holder detail = %+v, want A=%s active", held, a.ID)
	}
	if !strings.Contains(err.Error(), a.ID) {
		t.Errorf("the message must name the holder: %v", err)
	}

	// The whole acceptance rolled back: B is still a proposal, still pending,
	// with no accepted event.
	bd, _ := s.GetDecision(b.ID)
	if bd.Status != types.StatusProposed {
		t.Errorf("B status = %s, want proposed", bd.Status)
	}
	if bd.PendingTopicKey != "auth" {
		t.Errorf("B pending key must not be dropped, got %q", bd.PendingTopicKey)
	}
	if evs, _ := s.Events(EventFilter{DecisionID: b.ID, Kind: types.EventDecisionAccepted}); len(evs) != 0 {
		t.Errorf("a failed acceptance must emit no decision.accepted event")
	}
	// And the invariant held throughout.
	assertOneLiveHolder(t, s, "auth")
}

// The other interleaving: the predecessor goes terminal by an unrelated route,
// a third row takes the freed key, and the original pending successor can no
// longer have it. It must fail loudly and stay recoverable.
func TestPendingTopicKey_ThirdRowTakesTheFreedKey(t *testing.T) {
	s := newDecisionStore(t)

	base := baseInput()
	base.TopicKey = "db"
	holder, _ := s.Propose(base)
	addCommitEvidence(t, s, holder.ID, "sha-holder")
	s.Accept(holder.ID, AcceptOptions{})

	in := baseInput()
	in.TopicKey = "db"
	in.Title = "the original successor"
	orig, _ := s.Propose(in)
	addCommitEvidence(t, s, orig.ID, "sha-orig")

	// Holder is superseded by something unrelated, freeing the key.
	unrelated := baseInput()
	unrelated.Title = "an unrelated decision"
	unrelated.Supersedes = []string{holder.ID}
	u, _ := s.Propose(unrelated)
	addCommitEvidence(t, s, u.ID, "sha-u")
	if _, err := s.Accept(u.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}

	// A third row now claims the freed key outright.
	third := baseInput()
	third.TopicKey = "db"
	third.Title = "the third claimant"
	t3, err := s.Propose(third)
	if err != nil {
		t.Fatal(err)
	}
	if t3.TopicKey != "db" {
		t.Fatalf("the freed key should be claimable outright, got %q", t3.TopicKey)
	}

	_, err = s.Accept(orig.ID, AcceptOptions{})
	if !errors.Is(err, types.ErrTopicKeyHeld) {
		t.Fatalf("err = %v, want ErrTopicKeyHeld", err)
	}
	var held *types.TopicKeyHeldError
	errors.As(err, &held)
	if held.HolderID != t3.ID {
		t.Errorf("holder = %s, want the third claimant %s", held.HolderID, t3.ID)
	}

	// Recovery 1: clear the pending key, then acceptance succeeds.
	if err := s.ClearPendingTopicKey(orig.ID, types.ActorHuman); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(orig.ID, AcceptOptions{}); err != nil {
		t.Fatalf("after clearing the pending key the proposal must be acceptable: %v", err)
	}
	d, _ := s.GetDecision(orig.ID)
	if d.Status != types.StatusActive || d.TopicKey != "" || d.PendingTopicKey != "" {
		t.Errorf("unexpected final state: %+v", d)
	}
	assertOneLiveHolder(t, s, "db")
}

// Recovery 2 from ErrTopicKeyHeld: supersede the named holder and re-accept.
func TestPendingTopicKey_RecoveryBySupersedingTheHolder(t *testing.T) {
	s := newDecisionStore(t)

	base := baseInput()
	base.TopicKey = "cache"
	holder, _ := s.Propose(base)
	addCommitEvidence(t, s, holder.ID, "sha-h")
	s.Accept(holder.ID, AcceptOptions{})

	a := baseInput()
	a.TopicKey = "cache"
	a.Title = "A"
	da, _ := s.Propose(a)
	b := baseInput()
	b.TopicKey = "cache"
	b.Title = "B"
	dbb, _ := s.Propose(b)
	addCommitEvidence(t, s, da.ID, "sha-a")
	addCommitEvidence(t, s, dbb.ID, "sha-b")

	s.Accept(da.ID, AcceptOptions{})
	if _, err := s.Accept(dbb.ID, AcceptOptions{}); !errors.Is(err, types.ErrTopicKeyHeld) {
		t.Fatalf("err = %v, want ErrTopicKeyHeld", err)
	}

	// Add the named holder to supersedes — the proposal is still editable.
	if _, err := s.DB().Exec(`UPDATE decisions SET supersedes = ? WHERE id = ?`,
		mustJSON([]string{da.ID}), dbb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(dbb.ID, AcceptOptions{}); err != nil {
		t.Fatalf("re-acceptance after superseding the holder must work: %v", err)
	}
	final, _ := s.GetDecision(dbb.ID)
	if final.TopicKey != "cache" || final.PendingTopicKey != "" {
		t.Errorf("B should now hold the key: %+v", final)
	}
	if prev, _ := s.GetDecision(da.ID); prev.Status != types.StatusSuperseded {
		t.Errorf("A status = %s, want superseded", prev.Status)
	}
	assertOneLiveHolder(t, s, "cache")
}

func assertOneLiveHolder(t *testing.T, s *DecisionStore, key string) {
	t.Helper()
	live, err := s.ListDecisions(DecisionFilter{
		ProjectID: testProject, TopicKey: key,
		Statuses: []types.DecisionStatus{types.StatusProposed, types.StatusActive, types.StatusViolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) > 1 {
		t.Errorf("%d non-terminal rows hold topic_key %q; the one-holder invariant broke", len(live), key)
	}
}

func TestClearPendingTopicKey_OnlyWhileProposed(t *testing.T) {
	s := newDecisionStore(t)
	d, _ := s.Propose(baseInput())
	addCommitEvidence(t, s, d.ID, "sha")
	s.Accept(d.ID, AcceptOptions{})
	if err := s.ClearPendingTopicKey(d.ID, types.ActorHuman); !errors.Is(err, types.ErrDecisionImmutable) {
		t.Errorf("err = %v, want ErrDecisionImmutable", err)
	}
}

// Amendment 1, same-class audit item 1: decision.expired marks *first* expiry
// only, and must not be emitted for a decision that has not expired.
func TestMarkExpired_ChecksThePredicate(t *testing.T) {
	s := newDecisionStore(t)
	future := time.Now().UTC().Add(24 * time.Hour)
	in := baseInput()
	in.ExpiresAt = &future
	d, _ := s.Propose(in)
	addCommitEvidence(t, s, d.ID, "sha")
	s.Accept(d.ID, AcceptOptions{})

	emitted, err := s.MarkExpired(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if emitted {
		t.Error("decision.expired must not be emitted before the decision expires — " +
			"the event is append-only and index-protected, so it could never be corrected")
	}
	if evs, _ := s.Events(EventFilter{DecisionID: d.ID, Kind: types.EventDecisionExpired}); len(evs) != 0 {
		t.Errorf("%d expired events, want 0", len(evs))
	}

	// Once expired, it fires exactly once, and extending the expiry cannot
	// produce a second event — consumers must use the predicate.
	past := time.Now().UTC().Add(-time.Hour)
	p := &past
	if err := s.UpdateMetadata(d.ID, MetadataUpdate{ExpiresAt: &p}, types.ActorHuman); err != nil {
		t.Fatal(err)
	}
	if first, _ := s.MarkExpired(d.ID); !first {
		t.Error("an expired decision must emit the event once")
	}
	later := time.Now().UTC().Add(48 * time.Hour)
	l := &later
	s.UpdateMetadata(d.ID, MetadataUpdate{ExpiresAt: &l}, types.ActorHuman)
	past2 := time.Now().UTC().Add(-time.Minute)
	p2 := &past2
	s.UpdateMetadata(d.ID, MetadataUpdate{ExpiresAt: &p2}, types.ActorHuman)
	if again, err := s.MarkExpired(d.ID); err != nil || again {
		t.Errorf("a second expiry must be a silent no-op (got %v, %v); consumers read the predicate", again, err)
	}
}
