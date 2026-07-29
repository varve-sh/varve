package kernel

import (
	"errors"

	"github.com/varve-sh/varve/internal/pack"
	"strings"
	"testing"

	"github.com/varve-sh/varve/internal/types"
)

// ADR-0001 Amendment 4. Purge is the only destructive verb in the product, so
// each of its properties is asserted rather than assumed: who may run it, what
// it does to each population, and what it leaves behind.

func TestPurge_RefusesAnAgent(t *testing.T) {
	k := packKernel(t)
	d := acceptedDecision(t, k, "A binding rule", []string{"internal/**"})

	if _, err := k.Purge(d.ID, "secret", types.ActorAgent); !errors.Is(err, types.ErrPurgeNotPermitted) {
		t.Fatalf("err = %v, want ErrPurgeNotPermitted", err)
	}
	got, err := k.Decisions().GetDecision(d.ID)
	if err != nil {
		t.Fatalf("the row must survive a refused purge: %v", err)
	}
	if got.Title != "A binding rule" {
		t.Errorf("title = %q; nothing may have happened", got.Title)
	}
	if evs, _ := k.Decisions().Events(EventFilter{Kind: types.EventDecisionPurged}); len(evs) != 0 {
		t.Errorf("%d purge events from a refused purge, want 0", len(evs))
	}
}

// The hard-delete arm: a migration-born row with no history. The row goes; the
// fact that it went does not.
func TestPurge_MigrationBornRowIsDeletedWithATombstone(t *testing.T) {
	k := packKernel(t)
	id := seedMigrationBornDecision(t, k, "An old v1 decision with a secret")

	res, err := k.Purge(id, "secret", types.ActorHuman)
	if err != nil {
		t.Fatal(err)
	}
	if res.Arm != PurgeDeleted {
		t.Fatalf("arm = %s, want %s", res.Arm, PurgeDeleted)
	}
	if _, err := k.Decisions().GetDecision(id); !errors.Is(err, types.ErrDecisionNotFound) {
		t.Errorf("the row should be gone, got %v", err)
	}

	evs, err := k.Decisions().Events(EventFilter{Kind: types.EventDecisionPurged})
	if err != nil || len(evs) != 1 {
		t.Fatalf("decision.purged events = %d (%v), want 1", len(evs), err)
	}
	ev := evs[0]
	if ev.DecisionID != "" {
		t.Errorf("decision_id = %q, want NULL — the row it would reference is gone", ev.DecisionID)
	}
	if ev.Payload["purged_id"] != id {
		t.Errorf("purged_id = %v, want %s", ev.Payload["purged_id"], id)
	}
	if ev.Payload["hard_deleted"] != true {
		t.Errorf("hard_deleted = %v, want true", ev.Payload["hard_deleted"])
	}
	if ev.Actor != types.ActorHuman {
		t.Errorf("actor = %s, want human", ev.Actor)
	}
	// The content must not survive in the search index either.
	if hits, err := k.Store().SearchFTS("secret", testProject, 10); err != nil || len(hits) != 0 {
		t.Errorf("FTS still returns %d rows for the purged content (%v)", len(hits), err)
	}
}

// The redaction arm: history exists, so the row and its id survive and only
// the content goes.
func TestPurge_EventedRowIsRedactedNotDeleted(t *testing.T) {
	k := packKernel(t)
	d := acceptedDecision(t, k, "Tokens rotate on every use", []string{"internal/auth/**"},
		func(in *DecisionInput) {
			in.Body = "The signing key is sk-live-SECRET-VALUE."
			in.Tags = []string{"auth"}
		})

	res, err := k.Purge(d.ID, "secret", types.ActorHuman)
	if err != nil {
		t.Fatal(err)
	}
	if res.Arm != PurgeRedacted {
		t.Fatalf("arm = %s, want %s", res.Arm, PurgeRedacted)
	}
	if res.Transitioned != types.StatusReverted {
		t.Errorf("transitioned to %q, want reverted — a redacted row must not stay binding",
			res.Transitioned)
	}

	got, err := k.Decisions().GetDecision(d.ID)
	if err != nil {
		t.Fatalf("the row must survive: its events reference it: %v", err)
	}
	if got.Title != "[purged]" || got.Body != "" || len(got.Scope) != 0 || len(got.Tags) != 0 {
		t.Errorf("row not fully redacted: %+v", got)
	}
	if got.Status != types.StatusReverted {
		t.Errorf("status = %s, want reverted", got.Status)
	}

	// The transition and the purge are both on the record.
	if evs, _ := k.Decisions().Events(EventFilter{
		DecisionID: d.ID, Kind: types.EventDecisionReverted,
	}); len(evs) != 1 {
		t.Errorf("decision.reverted events = %d, want 1", len(evs))
	}
	evs, _ := k.Decisions().Events(EventFilter{
		DecisionID: d.ID, Kind: types.EventDecisionPurged,
	})
	if len(evs) != 1 {
		t.Fatalf("decision.purged events = %d, want 1", len(evs))
	}
	// The event names the fields, never their content — the log must not
	// become the copy the purge missed.
	payload := evs[0].Payload
	fields, _ := payload["redacted_fields"].([]any)
	if len(fields) == 0 {
		t.Error("the purge event should name the redacted fields")
	}
	for k, v := range payload {
		if s, ok := v.(string); ok && strings.Contains(s, "SECRET") {
			t.Errorf("payload key %q carries the purged content: %q", k, s)
		}
	}
	// No event of this decision may carry the secret.
	all, _ := k.Decisions().Events(EventFilter{DecisionID: d.ID})
	for _, e := range all {
		for key, v := range e.Payload {
			if s, ok := v.(string); ok && strings.Contains(s, "SECRET") {
				t.Errorf("%s payload key %q carries the purged content", e.Kind, key)
			}
		}
	}

	// And it is out of the index.
	if hits, err := k.Store().SearchFTS("SECRET", testProject, 10); err != nil || len(hits) != 0 {
		t.Errorf("FTS still returns %d rows for the purged body (%v)", len(hits), err)
	}
	// A purged row is terminal, so it cannot be packed or listed live.
	if res, err := k.Pack(pack.Request{FilePaths: []string{"internal/auth/session.go"}}); err != nil {
		t.Fatal(err)
	} else if strings.Contains(res.Text, d.ID) {
		t.Errorf("a purged decision was packed:\n%s", res.Text)
	}
}

// A proposal takes the same arm, via the transition its own status allows.
func TestPurge_ProposalIsRejectedThenRedacted(t *testing.T) {
	k := packKernel(t)
	m, _, err := k.Save(types.MemorySaveInput{
		Content: "An agent pasted sk-live-SECRET here.", Type: types.MemoryTypeDecision,
		Source: types.MemorySourceAgent, SessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := k.Purge(m.ID, "secret", types.ActorHuman)
	if err != nil {
		t.Fatal(err)
	}
	if res.Transitioned != types.StatusRejected {
		t.Errorf("transitioned to %q, want rejected — §D3 has no proposed→reverted edge",
			res.Transitioned)
	}
	got, _ := k.Decisions().GetDecision(m.ID)
	if got.Status != types.StatusRejected || got.Title != "[purged]" {
		t.Errorf("row = (%s, %q), want a rejected tombstone", got.Status, got.Title)
	}
}

// Purging an already-terminal row redacts it without inventing a transition.
func TestPurge_TerminalRowIsRedactedInPlace(t *testing.T) {
	k := packKernel(t)
	d := acceptedDecision(t, k, "Old rule with a secret", []string{"internal/**"})
	if _, err := k.Forget(d.ID, types.ActorHuman); err != nil {
		t.Fatal(err)
	}

	res, err := k.Purge(d.ID, "cleanup", types.ActorHuman)
	if err != nil {
		t.Fatal(err)
	}
	if res.Transitioned != "" {
		t.Errorf("transitioned = %q, want no transition on an already-terminal row",
			res.Transitioned)
	}
	if evs, _ := k.Decisions().Events(EventFilter{
		DecisionID: d.ID, Kind: types.EventDecisionReverted,
	}); len(evs) != 1 {
		t.Errorf("decision.reverted events = %d, want the original one only", len(evs))
	}
	got, _ := k.Decisions().GetDecision(d.ID)
	if got.Title != "[purged]" {
		t.Errorf("title = %q, want the tombstone", got.Title)
	}
}

// The residue warning names real paths, because a purge that claims more than
// it did is the dishonest version of this feature.
func TestPurgeResidue_NamesWhatItCannotReach(t *testing.T) {
	got := PurgeResidue("/repo")
	if len(got) < 2 {
		t.Fatalf("residue = %v, want the backup and the export at least", got)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{"v1.bak.db", "migration-v1-export.json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("residue does not name %s: %v", want, got)
		}
	}
}
