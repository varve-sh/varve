package kernel

import (
	"fmt"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// ADR-0004 §D1.4 and ADR-0001 §D6, asserted without a git repository: the
// kernel half of the observer decides which decisions a commit touched, what
// the verdict is, and what the verdict does to the lifecycle.
//
// Attribution has an unusually large space of interesting states, so the
// fixtures here are deliberately not minimal: multi-decision commits, repeat
// observation, commits older than the decision, reverts of accepting and of
// non-accepting evidence, and violations resolved one at a time.

func observedCommit(sha string, at time.Time, files ...string) ObservedCommit {
	return ObservedCommit{
		SHA: sha, Author: "dev", Subject: "a commit", CommittedAt: at,
		Files: files, PatchID: "patch-" + sha, Branch: "HEAD",
	}
}

func TestObserveCommit_ScopeMatchAndConformVerdict(t *testing.T) {
	k := packKernel(t)
	d := acceptedDecision(t, k, "Handlers validate the auth header", []string{"internal/auth/**"})
	elsewhere := acceptedDecision(t, k, "Docs are markdown", []string{"docs/**"})

	res, err := k.ObserveCommit(observedCommit("aaa111", time.Now().UTC(),
		"internal/auth/session.go", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 1 || res.Conformed != 1 || res.Violated != 0 {
		t.Fatalf("result = %+v, want one conforming match", res)
	}

	// The observation itself, with its load-bearing payload.
	obs, _ := k.Decisions().Events(EventFilter{Kind: types.EventDiffObserved, CommitSHA: "aaa111"})
	if len(obs) != 1 {
		t.Fatalf("diff.observed events = %d, want 1", len(obs))
	}
	if obs[0].Payload["patch_id"] != "patch-aaa111" {
		t.Errorf("patch_id = %v; reports count distinct changes by it, so a rebase "+
			"would otherwise double every conform count", obs[0].Payload["patch_id"])
	}
	files, _ := obs[0].Payload["files"].([]any)
	if len(files) != 2 {
		t.Errorf("files = %v, want both changed paths", obs[0].Payload["files"])
	}

	matches, _ := k.Decisions().Events(EventFilter{Kind: types.EventDiffScopeMatch})
	if len(matches) != 1 || matches[0].DecisionID != d.ID {
		t.Fatalf("scope matches = %+v, want one against the auth decision", matches)
	}
	if matches[0].Payload["verdict"] != "conform" {
		t.Errorf("verdict = %v, want conform", matches[0].Payload["verdict"])
	}
	if _, present := matches[0].Payload["backfill"]; present {
		t.Error("a live observation must not be marked backfill")
	}
	// The decision that does not claim these files is untouched.
	if got, _ := k.Decisions().GetDecision(elsewhere.ID); got.Status != types.StatusActive {
		t.Errorf("out-of-scope decision status = %s", got.Status)
	}
}

// The cursor is the existence of the diff.observed row, so re-observing is a
// no-op — and §D1.7 needs it to be, because a rebase makes the scan meet the
// same commits again.
func TestObserveCommit_IsIdempotent(t *testing.T) {
	k := packKernel(t)
	acceptedDecision(t, k, "Auth rule", []string{"internal/auth/**"})
	c := observedCommit("bbb222", time.Now().UTC(), "internal/auth/x.go")

	first, err := k.ObserveCommit(c)
	if err != nil || first.AlreadyObserved {
		t.Fatalf("first observation = %+v (%v)", first, err)
	}
	for i := 0; i < 3; i++ {
		again, err := k.ObserveCommit(c)
		if err != nil {
			t.Fatal(err)
		}
		if !again.AlreadyObserved {
			t.Fatalf("re-observation %d was not recognised as one: %+v", i, again)
		}
	}
	obs, _ := k.Decisions().Events(EventFilter{Kind: types.EventDiffObserved})
	matches, _ := k.Decisions().Events(EventFilter{Kind: types.EventDiffScopeMatch})
	if len(obs) != 1 || len(matches) != 1 {
		t.Errorf("%d observations and %d matches after 4 observations of one commit",
			len(obs), len(matches))
	}
}

// §D1.3: a decision cannot be conformed to or violated by a commit that
// predates its acceptance.
func TestObserveCommit_IgnoresCommitsOlderThanTheDecision(t *testing.T) {
	k := packKernel(t)
	acceptedDecision(t, k, "Auth rule", []string{"internal/auth/**"})

	res, err := k.ObserveCommit(observedCommit("ccc333",
		time.Now().UTC().Add(-72*time.Hour), "internal/auth/x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 0 {
		t.Errorf("matched %d decisions with a commit older than the decision", res.Matched)
	}
	if obs, _ := k.Decisions().Events(EventFilter{Kind: types.EventDiffObserved}); len(obs) != 1 {
		t.Error("the commit itself must still be observed — only the verdict is withheld")
	}
}

// A proposal is not binding, so it can be neither conformed to nor violated.
func TestObserveCommit_IgnoresProposals(t *testing.T) {
	k := packKernel(t)
	if _, _, err := k.Save(types.MemorySaveInput{
		Content: "An agent's idea.", Type: types.MemoryTypeDecision,
		Source: types.MemorySourceAgent, SessionID: "s1", FilePaths: []string{"internal/**"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := k.ObserveCommit(observedCommit("ddd444", time.Now().UTC(), "internal/auth/x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 0 {
		t.Errorf("a proposal was scope-matched: %+v", res)
	}
}

// One commit, several decisions — the ordinary case, and the one no test on
// this branch covered until F21.
func TestObserveCommit_MatchesEveryDecisionInScope(t *testing.T) {
	k := packKernel(t)
	var ids []string
	for i, sc := range [][]string{
		{"internal/auth/**"}, {"internal/auth/session.go"}, {"internal/**"},
	} {
		ids = append(ids, acceptedDecision(t, k,
			fmt.Sprintf("Rule %d", i), sc).ID)
	}
	res, err := k.ObserveCommit(observedCommit("eee555", time.Now().UTC(),
		"internal/auth/session.go"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 3 || res.Conformed != 3 {
		t.Fatalf("result = %+v, want three conforming matches", res)
	}
	for _, id := range ids {
		evs, _ := k.Decisions().Events(EventFilter{
			DecisionID: id, Kind: types.EventDiffScopeMatch,
		})
		if len(evs) != 1 {
			t.Errorf("decision %s has %d scope matches, want 1", id, len(evs))
		}
	}
}

// §D6's verdict rule: violate iff the commit reverts a commit that is evidence
// of the decision, or a commit that previously conformed to it.
func TestObserveCommit_VerdictIsViolateWhenItRevertsEvidence(t *testing.T) {
	k := packKernel(t)
	d, err := k.Decisions().ProposeAccepted(DecisionInput{
		ProjectID: testProject, Title: "Sessions are server-side only",
		Scope: []string{"internal/auth/**"}, Source: types.DecisionSourceUser,
		Evidence: []EvidenceInput{{
			Kind: types.EvidenceKindCommit, Ref: "accepting111", AddedBy: types.ActorHuman,
		}},
	}, AcceptOptions{Actor: types.ActorHuman})
	if err != nil {
		t.Fatal(err)
	}

	c := observedCommit("revert999", time.Now().UTC(), "internal/auth/session.go")
	c.RevertsSHA = "accepting111"
	c.Subject = "Revert \"add server-side sessions\""
	res, err := k.ObserveCommit(c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Violated != 1 {
		t.Fatalf("result = %+v, want a violation", res)
	}
	if !res.RevertDetected {
		t.Error("the revert trailer must be recorded as revert.detected")
	}
	// §D6, as narrowed by the founder-delegated item-6 ruling: reverting the
	// *accepting* evidence is what makes a decision terminal.
	if len(res.DecisionsReverted) != 1 || res.DecisionsReverted[0] != d.ID {
		t.Fatalf("decisions reverted = %v, want the decision whose accepting evidence went",
			res.DecisionsReverted)
	}
	got, _ := k.Decisions().GetDecision(d.ID)
	if got.Status != types.StatusReverted {
		t.Errorf("status = %s, want reverted", got.Status)
	}
	reverts, _ := k.Decisions().Events(EventFilter{Kind: types.EventRevertDetected})
	if len(reverts) != 1 || reverts[0].Payload["method"] != "trailer" {
		t.Errorf("revert.detected = %+v, want one trailer-detected revert", reverts)
	}
}

// The other half of the item-6 narrowing: reverting a *later* conforming
// commit is a violation, not a revert. The more a decision was followed, the
// more such commits exist — treating each as fatal would make a decision more
// fragile the better it was working.
func TestObserveCommit_RevertingAConformingCommitViolatesRatherThanReverts(t *testing.T) {
	k := packKernel(t)
	d := acceptedDecision(t, k, "Auth rule", []string{"internal/auth/**"})

	base := time.Now().UTC()
	if _, err := k.ObserveCommit(observedCommit("conform1", base, "internal/auth/x.go")); err != nil {
		t.Fatal(err)
	}
	undo := observedCommit("undo1", base.Add(time.Minute), "internal/auth/x.go")
	undo.RevertsSHA = "conform1"
	res, err := k.ObserveCommit(undo)
	if err != nil {
		t.Fatal(err)
	}
	if res.Violated != 1 {
		t.Fatalf("result = %+v, want a violation", res)
	}
	if len(res.DecisionsReverted) != 0 {
		t.Errorf("the decision was terminated by a revert of a non-accepting commit: %v",
			res.DecisionsReverted)
	}
	got, _ := k.Decisions().GetDecision(d.ID)
	if got.Status != types.StatusViolated {
		t.Errorf("status = %s, want violated (recoverable)", got.Status)
	}
	if n, _ := k.Decisions().UnresolvedViolations(d.ID); n != 1 {
		t.Errorf("unresolved episodes = %d, want 1", n)
	}
}

// A counter-revert resolves the episode, and reinstatement happens only at the
// zero-crossing — for every decision the revert touched (F21).
func TestObserveCommit_CounterRevertReinstatesAtTheZeroCrossing(t *testing.T) {
	k := packKernel(t)
	// Scoped so the second episode belongs to d2 alone: d1 claims one file,
	// d2 the whole package. Both are violated by the first bad commit; only d2
	// is violated by the second.
	d1 := acceptedDecision(t, k, "No JWT session state", []string{"internal/auth/x.go"})
	d2 := acceptedDecision(t, k, "Auth errors are wrapped", []string{"internal/auth/**"})

	base := time.Now().UTC()
	if _, err := k.ObserveCommit(observedCommit("good1", base, "internal/auth/x.go")); err != nil {
		t.Fatal(err)
	}
	bad := observedCommit("bad1", base.Add(time.Minute), "internal/auth/x.go")
	bad.RevertsSHA = "good1"
	if _, err := k.ObserveCommit(bad); err != nil {
		t.Fatal(err)
	}
	// A second, independent episode on d2 only.
	if _, err := k.ObserveCommit(observedCommit("good2", base.Add(2*time.Minute),
		"internal/auth/y.go")); err != nil {
		t.Fatal(err)
	}
	bad2 := observedCommit("bad2", base.Add(3*time.Minute), "internal/auth/y.go")
	bad2.RevertsSHA = "good2"
	if _, err := k.ObserveCommit(bad2); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{d1.ID, d2.ID} {
		if got, _ := k.Decisions().GetDecision(id); got.Status != types.StatusViolated {
			t.Fatalf("decision %s status = %s, want violated", id, got.Status)
		}
	}

	// Counter-revert of the first bad commit: d1 crosses zero, d2 does not.
	counter := observedCommit("counter1", base.Add(4*time.Minute), "internal/auth/x.go")
	counter.RevertsSHA = "bad1"
	res, err := k.ObserveCommit(counter)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DecisionsReinstated) != 1 || res.DecisionsReinstated[0] != d1.ID {
		t.Fatalf("reinstated = %v, want only the decision whose last episode closed",
			res.DecisionsReinstated)
	}
	if got, _ := k.Decisions().GetDecision(d1.ID); got.Status != types.StatusActive {
		t.Errorf("d1 status = %s, want active", got.Status)
	}
	if got, _ := k.Decisions().GetDecision(d2.ID); got.Status != types.StatusViolated {
		t.Errorf("d2 status = %s, want violated — its second episode is still open", got.Status)
	}
	if n, _ := k.Decisions().UnresolvedViolations(d2.ID); n != 1 {
		t.Errorf("d2 unresolved = %d, want 1", n)
	}
}

// §D1.4's atomicity: the scope-match rows, the episode events and the state
// change commit together with the diff.observed row that justifies them. A
// number in the report that drills down to nothing is what this prevents.
func TestObserveCommit_IsOneTransaction(t *testing.T) {
	k := packKernel(t)
	d := acceptedDecision(t, k, "Auth rule", []string{"internal/auth/**"})

	c := observedCommit("atomic1", time.Now().UTC(), "internal/auth/x.go")
	if _, err := k.ObserveCommit(c); err != nil {
		t.Fatal(err)
	}
	// Every row the observation produced shares one instant and one order:
	// they were written by one transaction, so the match cannot exist without
	// its observation.
	obs, _ := k.Decisions().Events(EventFilter{Kind: types.EventDiffObserved, CommitSHA: "atomic1"})
	matches, _ := k.Decisions().Events(EventFilter{
		Kind: types.EventDiffScopeMatch, DecisionID: d.ID,
	})
	if len(obs) != 1 || len(matches) != 1 {
		t.Fatalf("observations=%d matches=%d", len(obs), len(matches))
	}
	if matches[0].Seq < obs[0].Seq {
		t.Error("the verdict was recorded before the observation that justifies it")
	}
}

// §D1.3: backfilled matches carry the flag that keeps them out of every
// reported metric.
func TestObserveCommit_BackfillIsMarked(t *testing.T) {
	k := packKernel(t)
	acceptedDecision(t, k, "Auth rule", []string{"internal/auth/**"})

	c := observedCommit("old1", time.Now().UTC(), "internal/auth/x.go")
	c.Backfill = true
	if _, err := k.ObserveCommit(c); err != nil {
		t.Fatal(err)
	}
	matches, _ := k.Decisions().Events(EventFilter{Kind: types.EventDiffScopeMatch})
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].Payload["backfill"] != true {
		t.Errorf("backfill flag = %v; a verdict about a pre-epoch commit is archaeology "+
			"and must be excluded from the report", matches[0].Payload["backfill"])
	}
}

// The epoch is written once and never moves.
func TestObserverEpoch_IsWrittenOnceAndReadBack(t *testing.T) {
	k := packKernel(t)
	if got, err := k.ObserverEpoch(); err != nil || got != nil {
		t.Fatalf("a store with no epoch must report none: %v, %v", got, err)
	}
	epoch := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		if err := k.RecordObserverEnabled(epoch.Add(time.Duration(i) * time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := k.ObserverEpoch()
	if err != nil || got == nil {
		t.Fatalf("epoch = %v, %v", got, err)
	}
	if !got.Equal(epoch) {
		t.Errorf("epoch = %s, want the first one written (%s)", got, epoch)
	}
	if evs, _ := k.Decisions().Events(EventFilter{Kind: types.EventObserverEnabled}); len(evs) != 1 {
		t.Errorf("observer.enabled events = %d, want 1", len(evs))
	}
}
