package kernel

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/pack"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// The packer against the real store and the real event log. The unit tests in
// internal/pack drive the algorithm; these assert that what the store hands it
// is what ADR-0002 says, and that the instrumentation ADR-0004 joins on is
// actually written.

func packKernel(t *testing.T) *MemoryKernel {
	t.Helper()
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	k := New(filepath.Join(t.TempDir(), "pack.db"), testProject)
	if err := k.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

// acceptedDecision creates a binding decision the way a human would.
func acceptedDecision(t *testing.T, k *MemoryKernel, title string, scope []string, opts ...func(*DecisionInput)) *types.Decision {
	t.Helper()
	in := DecisionInput{
		ProjectID: testProject, Title: title, Body: "Rationale for " + title,
		Scope: scope, Source: types.DecisionSourceUser,
		Evidence: []EvidenceInput{{
			Kind: types.EvidenceKindCommit, Ref: "sha-" + title, AddedBy: types.ActorHuman,
		}},
	}
	for _, o := range opts {
		o(&in)
	}
	d, err := k.Decisions().ProposeAccepted(in, AcceptOptions{Actor: types.ActorHuman})
	if err != nil {
		t.Fatalf("accept %q: %v", title, err)
	}
	return d
}

// seedMigrationBornDecision inserts a decision exactly as `migrate --from-v1`
// does: the row directly, and no per-decision events (§D7's documented
// exception). Under invariant I1 that absence is what makes it identifiable as
// migration-born.
func seedMigrationBornDecision(t *testing.T, k *MemoryKernel, title string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := util.GenerateID()
	if _, err := k.Decisions().DB().Exec(`
		INSERT INTO decisions (id, project_id, kind, title, body, status, scope, confidence,
		    source, tags, supersedes, created_at, updated_at, decided_at, status_changed_at,
		    access_count)
		VALUES (?, ?, 'decision', ?, '', 'active', '[]', 1.0,
		    'import', '[]', '[]', ?, ?, ?, ?, 0)`,
		id, testProject, title, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if evs, _ := k.Decisions().Events(EventFilter{DecisionID: id}); len(evs) != 0 {
		t.Fatalf("fixture has %d events; the point is that it has none", len(evs))
	}
	return id
}

func TestPack_ServesBindingDecisionsAndRecordsThem(t *testing.T) {
	k := packKernel(t)
	k.BeginSession("mcp", "claude-opus")

	inScope := acceptedDecision(t, k, "Handlers validate the auth header", []string{"internal/auth/**"})
	elsewhere := acceptedDecision(t, k, "Docs are markdown", []string{"docs/**"})
	// An agent's proposal: never content, always counted.
	proposal, _, err := k.Save(types.MemorySaveInput{
		Content: "Everything should use gRPC.", Type: types.MemoryTypeDecision,
		Source: types.MemorySourceAgent, SessionID: "s1", FilePaths: []string{"internal/auth/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	noteMem, _, err := k.Save(types.MemorySaveInput{
		Content: "The auth package was split in March.", Type: types.MemoryTypeFact,
		Source: types.MemorySourceUser, FilePaths: []string{"internal/auth/session.go"},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := k.Pack(pack.Request{FilePaths: []string{"internal/auth/session.go"}})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}

	// A pack that serves nothing is a passing test and a broken product.
	if res.ItemCount == 0 {
		t.Fatalf("the pack served nothing:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, inScope.ID) {
		t.Errorf("the in-scope decision is missing:\n%s", res.Text)
	}
	if strings.Contains(res.Text, elsewhere.ID) {
		t.Errorf("a decision scoped docs/** was packed for an auth file:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "Everything should use gRPC.") {
		t.Errorf("proposed text was laundered into the pack:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "proposed decisions touching these files: 1") ||
		!strings.Contains(res.Text, proposal.ID) {
		t.Errorf("the proposal must be counted and named in the footer:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, noteMem.ID) {
		t.Errorf("an exact-matched note should have fit in a 2000-token budget:\n%s", res.Text)
	}

	// §P10: one pack.served, and one pack.item per served item.
	served, err := k.Decisions().Events(EventFilter{Kind: types.EventPackServed})
	if err != nil || len(served) != 1 {
		t.Fatalf("pack.served events = %d (%v), want 1", len(served), err)
	}
	p := served[0].Payload
	if p["item_count"].(float64) != float64(res.ItemCount) {
		t.Errorf("item_count = %v, want %d", p["item_count"], res.ItemCount)
	}
	if p["estimator"] != pack.EstimatorVersion {
		t.Errorf("estimator = %v, want %q", p["estimator"], pack.EstimatorVersion)
	}
	if p["proposed_matched"].(float64) != 1 {
		t.Errorf("proposed_matched = %v, want 1", p["proposed_matched"])
	}
	if _, present := p["arm"]; present {
		t.Error("`arm` is reserved for ADR-0004 §D8's deferred A/B and must not be emitted")
	}
	if served[0].SessionID == "" {
		t.Error("pack.served carries no session; the whole attribution chain hangs off it")
	}

	items, err := k.Decisions().Events(EventFilter{Kind: types.EventPackItem})
	if err != nil || len(items) != res.ItemCount {
		t.Fatalf("pack.item events = %d (%v), want one per served item (%d)",
			len(items), err, res.ItemCount)
	}
	// The join ADR-0004 walks: "decision D was in session S's context".
	found := false
	for _, ev := range items {
		if ev.DecisionID == inScope.ID && ev.SessionID == served[0].SessionID {
			found = true
			if ev.Payload["form"] != string(pack.FormFull) {
				t.Errorf("form = %v, want full", ev.Payload["form"])
			}
			if ev.Payload["class"] != string(pack.ClassDecision) {
				t.Errorf("class = %v, want decision", ev.Payload["class"])
			}
		}
		if ev.Payload["class"] == string(pack.ClassNote) && ev.DecisionID != "" {
			t.Error("a note's pack.item must not set decision_id — it would forge an attribution row")
		}
	}
	if !found {
		t.Error("no pack.item joins the served decision to the session")
	}
}

// §P10's hard requirement: per-item emission survives budget pressure. Batching
// or eliding these under truncation kills ADR-0004's chain silently.
func TestPack_EmitsPerItemEventsUnderTruncation(t *testing.T) {
	k := packKernel(t)
	k.BeginSession("mcp", "")

	// A spread of body sizes, so the ladder reaches *both* rungs: uniformly
	// huge bodies only ever produce omissions, and the point of this test is
	// that a stubbed item still emits its pack.item.
	for i := 0; i < 12; i++ {
		acceptedDecision(t, k, fmt.Sprintf("Decision %02d", i), []string{"internal/**"},
			func(in *DecisionInput) {
				in.Body = strings.Repeat("A long rationale that will not fit. ", 2+i*8)
			})
	}

	res, err := k.Pack(pack.Request{
		FilePaths: []string{"internal/auth/session.go"}, BudgetTokens: 900,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatalf("expected a truncated pack at this budget:\n%s", res.Text)
	}
	if res.StubCount == 0 || res.OmittedCount == 0 {
		t.Fatalf("the fixture must reach both rungs of §P9's ladder: %d stubs, %d omitted",
			res.StubCount, res.OmittedCount)
	}
	items, _ := k.Decisions().Events(EventFilter{Kind: types.EventPackItem})
	if len(items) != res.ItemCount {
		t.Fatalf("pack.item events = %d, want %d — one per served item, including stubs",
			len(items), res.ItemCount)
	}
	stubs := 0
	for _, ev := range items {
		if ev.Payload["form"] == string(pack.FormStub) {
			stubs++
		}
	}
	if stubs != res.StubCount {
		t.Errorf("%d stub events for %d stubbed items; ADR-0004 must be able to tell "+
			"'saw the rule' from 'saw only its title'", stubs, res.StubCount)
	}
	if got := pack.Estimate(res.Text); got > 900 {
		t.Errorf("pack is %d est-tokens over a 900 budget — the budget is a ceiling", got)
	}
}

// §P12: the pack must not touch accessed_at/access_count, or packing would
// refresh its own decay signal forever and the store would never cool.
func TestPack_DoesNotTouchAccessTracking(t *testing.T) {
	k := packKernel(t)
	d := acceptedDecision(t, k, "Untouched by packing", []string{"internal/**"})

	before, err := k.Decisions().GetDecision(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := k.Pack(pack.Request{FilePaths: []string{"internal/auth/x.go"}}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := k.Decisions().GetDecision(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AccessCount != before.AccessCount {
		t.Errorf("access_count %d -> %d: packing must not feed its own ranking (§P3/§P12)",
			before.AccessCount, after.AccessCount)
	}
	if (after.AccessedAt == nil) != (before.AccessedAt == nil) {
		t.Errorf("accessed_at changed: %v -> %v", before.AccessedAt, after.AccessedAt)
	}
}

// §P11 depends on the two read paths staying distinguishable: a pack must not
// emit recall.served, or it would inflate the arm it is being compared against.
func TestPack_DoesNotEmitRecallEvents(t *testing.T) {
	k := packKernel(t)
	acceptedDecision(t, k, "Something", []string{"internal/**"})

	if _, err := k.Pack(pack.Request{
		FilePaths: []string{"internal/auth/x.go"}, Task: "auth",
	}); err != nil {
		t.Fatal(err)
	}
	recalls, _ := k.Decisions().Events(EventFilter{Kind: types.EventRecallServed})
	if len(recalls) != 0 {
		t.Errorf("%d recall.served events from a pack — §P11's comparison is now polluted",
			len(recalls))
	}
}

// §P2: the packer is usually the first component to observe an expiry, and the
// observation is recorded once, idempotently.
func TestPack_ObservesExpiryOnce(t *testing.T) {
	k := packKernel(t)
	past := time.Now().UTC().Add(-time.Hour)
	d := acceptedDecision(t, k, "Expired rule", []string{"internal/**"}, func(in *DecisionInput) {
		in.ExpiresAt = &past
	})

	for i := 0; i < 3; i++ {
		res, err := k.Pack(pack.Request{FilePaths: []string{"internal/auth/x.go"}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Text, d.ID) {
			t.Fatalf("an expired decision was served as binding:\n%s", res.Text)
		}
	}
	evs, _ := k.Decisions().Events(EventFilter{DecisionID: d.ID, Kind: types.EventDecisionExpired})
	if len(evs) != 1 {
		t.Errorf("decision.expired events = %d, want exactly 1 (idempotent)", len(evs))
	}
}

// session.ended's `packs` had no incrementer and would have reported 0 forever.
func TestPack_CountsTowardsTheSessionSummary(t *testing.T) {
	k := packKernel(t)
	k.BeginSession("mcp", "")
	acceptedDecision(t, k, "Something", []string{"internal/**"})

	for i := 0; i < 2; i++ {
		if _, err := k.Pack(pack.Request{FilePaths: []string{"internal/auth/x.go"}}); err != nil {
			t.Fatal(err)
		}
	}
	k.EndSession()

	evs, _ := k.Decisions().Events(EventFilter{Kind: types.EventSessionEnded})
	if len(evs) != 1 {
		t.Fatalf("session.ended events = %d, want 1", len(evs))
	}
	if got, _ := evs[0].Payload["packs"].(float64); got != 2 {
		t.Errorf("session.ended packs = %v, want 2", evs[0].Payload["packs"])
	}
}

// An errored call records nothing (§P1).
func TestPack_ErrorsRecordNothing(t *testing.T) {
	k := packKernel(t)
	if _, err := k.Pack(pack.Request{}); err == nil {
		t.Fatal("a pack with no anchor must error")
	}
	if evs, _ := k.Decisions().Events(EventFilter{Kind: types.EventPackServed}); len(evs) != 0 {
		t.Errorf("%d pack.served events from an errored call, want 0", len(evs))
	}
}

// ADR-0002 §P13 / falsifier 5: p95 < 150 ms without an embedder on a store of
// ≤5,000 decisions. The distribution is measured, not the mean — a mean hides
// exactly the tail the envelope is about.
func TestPack_LatencyEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("populates 5,000 decisions")
	}
	if raceEnabled {
		// The race detector costs 10–30x, so the wall clock here measures the
		// instrumentation rather than the packer. The envelope is a production
		// property; the correctness tests above run under -race and matter.
		t.Skip("latency envelope is not meaningful under the race detector")
	}
	k := packKernel(t)
	scopes := []string{"internal/auth/**", "internal/kernel/**", "docs/**"}
	for i := 0; i < 5000; i++ {
		acceptedDecision(t, k, fmt.Sprintf("Decision %04d about auth and session handling", i),
			[]string{scopes[i%len(scopes)]}, func(in *DecisionInput) {
				in.Body = "The session store must not be reachable without a validated " +
					"header, and the reasoning is recorded here at realistic length."
			})
	}

	req := pack.Request{
		FilePaths: []string{"internal/auth/session.go", "internal/auth/middleware.go"},
		Task:      "add refresh token rotation",
	}
	const runs = 40
	samples := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		res, err := k.Pack(req)
		samples = append(samples, time.Since(start))
		if err != nil {
			t.Fatal(err)
		}
		if res.ItemCount == 0 {
			t.Fatal("empty pack: this would be measuring nothing")
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p50 := samples[len(samples)/2]
	p95 := samples[int(float64(len(samples))*0.95)-1]
	t.Logf("pack latency over %d runs at 5,000 decisions: p50 %v, p95 %v, max %v",
		runs, p50, p95, samples[len(samples)-1])

	if p95 > 150*time.Millisecond {
		t.Errorf("p95 = %v, over §P13's 150 ms envelope — falsifier 5", p95)
	}
}

// F31. The zero-event hard delete sat above both the channel check and the
// terminal check, and "no history to protect" is false for exactly the
// population §D9 creates deliberately: §D7's migration exception writes one
// migration.completed row and no per-decision events, so after a migration
// every decision has zero events. An agent could destroy any of them, with no
// transition, no request, nothing in the triage queue, and no FK backstop —
// there are no events to reference.
func TestForget_AMigratedDecisionIsNotAgentDeletable(t *testing.T) {
	k := packKernel(t)

	id := seedMigrationBornDecision(t, k, "Sessions are server-side only")

	outcome, err := k.Forget(id, types.ActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == DisposalDeleted {
		t.Fatal("an agent hard-deleted a migrated decision")
	}
	if outcome != DisposalRequested {
		t.Errorf("outcome = %v, want a recorded disposal request", outcome)
	}
	got, err := k.Decisions().GetDecision(id)
	if err != nil {
		t.Fatalf("the row must survive: %v", err)
	}
	if got.Status != types.StatusActive {
		t.Errorf("status = %s, want active — an agent may not dispose of it", got.Status)
	}
	reqs, _ := k.Decisions().Events(EventFilter{
		DecisionID: id, Kind: types.EventDecisionDisposalRequested,
	})
	if len(reqs) != 1 {
		t.Errorf("disposal requests = %d, want 1", len(reqs))
	}
	// And it is visible to the human who has to rule on it.
	pending, _ := k.Decisions().PendingDisposals(testProject)
	if len(pending) != 1 || pending[0].Decision.ID != id {
		t.Errorf("the request is not in the triage queue: %+v", pending)
	}
}

// The human channel transitions it instead of destroying it: `decision revert`
// promises "kept as a reverted audit record", and that has to be true for a
// migrated row too.
func TestForget_AMigratedDecisionRevertsRatherThanVanishing(t *testing.T) {
	k := packKernel(t)

	id := seedMigrationBornDecision(t, k, "Sessions are server-side only")

	outcome, err := k.Forget(id, types.ActorHuman)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != DisposalReverted {
		t.Fatalf("outcome = %v, want DisposalReverted", outcome)
	}
	got, err := k.Decisions().GetDecision(id)
	if err != nil {
		t.Fatalf("the audit record must survive: %v", err)
	}
	if got.Status != types.StatusReverted {
		t.Errorf("status = %s, want reverted", got.Status)
	}
	if evs, _ := k.Decisions().Events(EventFilter{
		DecisionID: id, Kind: types.EventDecisionReverted,
	}); len(evs) != 1 {
		t.Errorf("decision.reverted events = %d, want 1", len(evs))
	}
}
