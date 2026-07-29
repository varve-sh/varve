package kernel

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// The §D10 read-path port, asserted end to end through the kernel facade —
// the surface the MCP tools, the CLI and the TUI actually call.
//
// Every assertion here is on rows surfaced. A recall that returns nothing is a
// passing test and a broken product: that is exactly how F1 shipped green.

func readPathKernel(t *testing.T) *MemoryKernel {
	t.Helper()
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	k := New(filepath.Join(t.TempDir(), "test.db"), testProject)
	if err := k.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

func recalledIDs(t *testing.T, k *MemoryKernel, query string) map[string]types.Memory {
	t.Helper()
	results, err := k.Recall(types.MemoryRecallInput{Query: query, Limit: 25})
	if err != nil {
		t.Fatalf("recall %q: %v", query, err)
	}
	out := make(map[string]types.Memory, len(results))
	for _, r := range results {
		out[r.Memory.ID] = r.Memory
	}
	return out
}

// §D10: memory_recall searches decisions_fts AND notes_fts and merges them
// through the existing scorer.
func TestRecall_SurfacesBothDecisionsAndNotes(t *testing.T) {
	k := readPathKernel(t)

	dec, _, err := k.Save(types.MemorySaveInput{
		Content: "Postgres is the primary datastore; we rejected MySQL.",
		Type:    types.MemoryTypeDecision,
		Source:  types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, _, err := k.Save(types.MemorySaveInput{
		Content: "Always wrap Postgres errors with %w.",
		Type:    types.MemoryTypeConvention,
		Source:  types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	note, _, err := k.Save(types.MemorySaveInput{
		Content: "The Postgres connection pool is capped at 20.",
		Type:    types.MemoryTypeFact,
		Source:  types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := recalledIDs(t, k, "Postgres")
	if len(got) != 3 {
		t.Fatalf("recall surfaced %d of 3 memories — the merge is dropping a table", len(got))
	}
	for _, want := range []struct {
		id    string
		class string
	}{{dec.ID, "decision"}, {conv.ID, "convention"}, {note.ID, "note"}} {
		m, ok := got[want.id]
		if !ok {
			t.Errorf("recall did not surface the %s", want.class)
			continue
		}
		if string(m.Type) != want.class {
			t.Errorf("%s surfaced as type %q", want.class, m.Type)
		}
	}
}

// §D10: terminal decisions are excluded by default; proposed and violated are
// not, because a violated decision is still binding.
func TestRecall_ExcludesTerminalDecisionsButKeepsViolatedAndProposed(t *testing.T) {
	k := readPathKernel(t)
	ds := k.Decisions()

	mk := func(title string) *types.Decision {
		d, err := ds.Propose(DecisionInput{
			ProjectID: testProject, Title: title, Body: "kafka " + title,
			Source: types.DecisionSourceAgent, SessionID: "sess-1",
		})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	proposed := mk("proposed rule")
	violated := mk("violated rule")
	ds.Accept(violated.ID, AcceptOptions{Force: true})
	ds.MarkViolated(violated.ID, ViolationOptions{CommitSHA: "sha-bad"})
	rejected := mk("rejected rule")
	ds.Reject(rejected.ID, "no")

	got := recalledIDs(t, k, "kafka")
	if _, ok := got[proposed.ID]; !ok {
		t.Error("a proposed decision must be recallable — it is visible, just not binding")
	}
	if _, ok := got[violated.ID]; !ok {
		t.Error("a violated decision must stay recallable: violation is a fact, not a repeal")
	}
	if _, ok := got[rejected.ID]; ok {
		t.Error("a rejected decision must be excluded by default (D10)")
	}
}

// §D7: recall.served, so the packer-vs-recall comparison has both read paths
// instrumented from the same day (ADR-0002 P11).
func TestRecall_EmitsRecallServed(t *testing.T) {
	k := readPathKernel(t)

	m, _, err := k.Save(types.MemorySaveInput{
		Content: "Redis is the cache", Type: types.MemoryTypeFact, Source: types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}

	results, err := k.Recall(types.MemoryRecallInput{
		Query: "Redis", Limit: 7, SessionID: "sess-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("nothing recalled — the event assertion below would be vacuous")
	}

	evs, err := k.Decisions().Events(EventFilter{Kind: types.EventRecallServed})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("%d recall.served events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.SessionID != "sess-42" {
		t.Errorf("session_id = %q, want sess-42", ev.SessionID)
	}
	if ev.Payload["query"] != "Redis" {
		t.Errorf("query = %v", ev.Payload["query"])
	}
	if ev.Payload["limit"].(float64) != 7 {
		t.Errorf("limit = %v, want 7", ev.Payload["limit"])
	}
	served, _ := ev.Payload["ids"].([]any)
	if len(served) != len(results) || served[0] != m.ID {
		t.Errorf("ids = %v, want the served result ids", ev.Payload["ids"])
	}
}

// A recall that finds nothing must still emit, with an empty id list — the
// zero-result case is exactly the one the comparison needs to see.
func TestRecall_EmitsRecallServedEvenWithNoResults(t *testing.T) {
	k := readPathKernel(t)
	if _, err := k.Recall(types.MemoryRecallInput{Query: "nothingmatchesthis"}); err != nil {
		t.Fatal(err)
	}
	evs, _ := k.Decisions().Events(EventFilter{Kind: types.EventRecallServed})
	if len(evs) != 1 {
		t.Fatalf("%d recall.served events, want 1", len(evs))
	}
	if ids, _ := evs[0].Payload["ids"].([]any); len(ids) != 0 {
		t.Errorf("ids = %v, want empty", evs[0].Payload["ids"])
	}
}

// §D10: memory_context glob-matches decision scopes. Under v1 this was exact
// string equality, which silently missed every scoped decision.
func TestContextForFiles_GlobMatchesDecisionScopes(t *testing.T) {
	k := readPathKernel(t)

	scoped, _, err := k.Save(types.MemorySaveInput{
		Content:   "Store code returns wrapped errors.",
		Type:      types.MemoryTypeConvention,
		Source:    types.MemorySourceUser,
		FilePaths: []string{"internal/kernel/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, _, err := k.Save(types.MemorySaveInput{
		Content:   "Docs are written in Markdown.",
		Type:      types.MemoryTypeConvention,
		Source:    types.MemorySourceUser,
		FilePaths: []string{"docs/**"},
	})
	if err != nil {
		t.Fatal(err)
	}

	results, err := k.ContextForFiles([]string{"internal/kernel/store.go"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, r := range results {
		found[r.Memory.ID] = true
	}
	if !found[scoped.ID] {
		t.Error("a decision scoped internal/kernel/** must match internal/kernel/store.go; " +
			"exact string equality is the bug this port fixes")
	}
	if found[elsewhere.ID] {
		t.Error("docs/** must not match internal/kernel/store.go")
	}
}

// ADR-0001's stated behaviour change: an agent's decision no longer binds on
// save. A human's does, because the human is the confirmation (D2).
func TestSave_BirthStateFollowsTheSource(t *testing.T) {
	k := readPathKernel(t)

	agentMem, _, err := k.Save(types.MemorySaveInput{
		Content:   "Agents should use the repository pattern.",
		Type:      types.MemoryTypeDecision,
		Source:    types.MemorySourceAgent,
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agentMem.Status != types.MemoryStatus(types.StatusProposed) {
		t.Errorf("an agent-sourced decision must be born proposed, got %q", agentMem.Status)
	}

	humanMem, _, err := k.Save(types.MemorySaveInput{
		Content: "We deploy on Fly.io.",
		Type:    types.MemoryTypeDecision,
		Source:  types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if humanMem.Status != types.MemoryStatus(types.StatusActive) {
		t.Errorf("a human-sourced decision may be born active, got %q", humanMem.Status)
	}

	// The acceptance is real: it has the event D4 makes the audit witness, and
	// it is honestly marked as unevidenced.
	ev, err := k.Decisions().Events(EventFilter{
		DecisionID: humanMem.ID, Kind: types.EventDecisionAccepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 1 {
		t.Fatalf("%d decision.accepted events, want 1", len(ev))
	}
	if ev[0].Payload["forced"] != true {
		t.Errorf("an unevidenced acceptance must be recorded as forced: %v", ev[0].Payload)
	}
}

// D4: the MCP path always carries provenance, and the kernel rejects an
// agent-sourced decision without a session — it would be invisible to
// attribution.
func TestSave_AgentDecisionRequiresASession(t *testing.T) {
	k := readPathKernel(t)
	_, _, err := k.Save(types.MemorySaveInput{
		Content: "Something the agent decided.",
		Type:    types.MemoryTypeDecision,
		Source:  types.MemorySourceAgent,
	})
	if !errors.Is(err, types.ErrValidation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
}

// A source_ref becomes evidence, which keeps an evidenced save off the
// `forced` list (D4).
func TestSave_SourceRefBecomesAcceptingEvidence(t *testing.T) {
	k := readPathKernel(t)
	m, _, err := k.Save(types.MemorySaveInput{
		Content:   "We adopted the outbox pattern.",
		Type:      types.MemoryTypeDecision,
		Source:    types.MemorySourceUser,
		SourceRef: "git:abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := k.Decisions().Evidence(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 1 || ev[0].Kind != types.EvidenceKindCommit || ev[0].Ref != "abc123" {
		t.Fatalf("evidence = %+v, want one commit row for abc123", ev)
	}
	if !ev[0].Accepting {
		t.Error("evidence present at acceptance must be marked accepting")
	}
	accepted, _ := k.Decisions().Events(EventFilter{
		DecisionID: m.ID, Kind: types.EventDecisionAccepted,
	})
	if accepted[0].Payload["forced"] != false {
		t.Errorf("an evidenced acceptance must not be marked forced: %v", accepted[0].Payload)
	}
}

// D4: a decision saved under a live topic_key becomes a proposed successor,
// never an in-place update — the note upsert path must not capture it.
func TestSave_DecisionTopicKeyCreatesASuccessorNotAnUpsert(t *testing.T) {
	k := readPathKernel(t)

	first, upserted, err := k.Save(types.MemorySaveInput{
		Content: "Sessions live in Redis.", Type: types.MemoryTypeDecision,
		Source: types.MemorySourceUser, TopicKey: "session-store",
	})
	if err != nil || upserted {
		t.Fatalf("first save = (%v, upserted=%v)", err, upserted)
	}

	second, upserted, err := k.Save(types.MemorySaveInput{
		Content: "Sessions live in Postgres.", Type: types.MemoryTypeDecision,
		Source: types.MemorySourceUser, TopicKey: "session-store",
	})
	if err != nil {
		t.Fatal(err)
	}
	if upserted {
		t.Error("a decision under an existing topic_key is a successor, not an upsert")
	}
	if second.ID == first.ID {
		t.Fatal("the successor must be a new row")
	}

	// Both rows exist and the predecessor was superseded by the acceptance.
	prev, err := k.Decisions().GetDecision(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.Status != types.StatusSuperseded {
		t.Errorf("predecessor status = %s, want superseded", prev.Status)
	}
	succ, _ := k.Decisions().GetDecision(second.ID)
	if succ.TopicKey != "session-store" {
		t.Errorf("successor topic_key = %q, want session-store", succ.TopicKey)
	}
}

// A note keeps v1's in-place topic_key upsert.
func TestSave_NoteTopicKeyStillUpsertsInPlace(t *testing.T) {
	k := readPathKernel(t)

	first, _, err := k.Save(types.MemorySaveInput{
		Content: "The staging URL is old.example", Type: types.MemoryTypeFact,
		Source: types.MemorySourceUser, TopicKey: "staging-url",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, upserted, err := k.Save(types.MemorySaveInput{
		Content: "The staging URL is new.example", Type: types.MemoryTypeFact,
		Source: types.MemorySourceUser, TopicKey: "staging-url",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upserted || second.ID != first.ID {
		t.Fatalf("a note under an existing topic_key must upsert in place (upserted=%v)", upserted)
	}
	if !strings.Contains(second.Content, "new.example") {
		t.Errorf("content = %q, want the update applied", second.Content)
	}
}

// D3: "forget" on a decision with history is a lifecycle transition, not a
// hard delete — the audit record survives.
func TestDelete_MapsDecisionsOntoTheLifecycle(t *testing.T) {
	k := readPathKernel(t)

	active, _, err := k.Save(types.MemorySaveInput{
		Content: "We use gRPC internally.", Type: types.MemoryTypeDecision,
		Source: types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := k.Delete(active.ID)
	if err != nil || !ok {
		t.Fatalf("delete = (%v, %v)", ok, err)
	}
	d, err := k.Decisions().GetDecision(active.ID)
	if err != nil {
		t.Fatalf("the row must survive as an audit record: %v", err)
	}
	if d.Status != types.StatusReverted {
		t.Errorf("status = %s, want reverted", d.Status)
	}

	proposed, _, err := k.Save(types.MemorySaveInput{
		Content: "An agent idea.", Type: types.MemoryTypeDecision,
		Source: types.MemorySourceAgent, SessionID: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Delete(proposed.ID); err != nil {
		t.Fatal(err)
	}
	d, _ = k.Decisions().GetDecision(proposed.ID)
	if d.Status != types.StatusRejected {
		t.Errorf("forgetting a proposal = %s, want rejected", d.Status)
	}

	// Notes keep v1 hard-delete semantics.
	note, _, _ := k.Save(types.MemorySaveInput{
		Content: "disposable", Type: types.MemoryTypeFact, Source: types.MemorySourceUser,
	})
	if _, err := k.Delete(note.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := k.Get(note.ID); m != nil {
		t.Error("a note must be hard-deleted")
	}
}

// Staleness is a note heuristic. Decisions have expiry, which is a derived
// predicate and changes no state — an mtime is not evidence that a decision
// stopped being true.
func TestScanStaleness_LeavesDecisionsAlone(t *testing.T) {
	k := readPathKernel(t)
	root := t.TempDir()

	if _, _, err := k.Save(types.MemorySaveInput{
		Content: "Auth rules.", Type: types.MemoryTypeDecision, Source: types.MemorySourceUser,
		FilePaths: []string{"missing/**"},
	}); err != nil {
		t.Fatal(err)
	}
	note, _, err := k.Save(types.MemorySaveInput{
		Content: "A note about a file.", Type: types.MemoryTypeFact,
		Source: types.MemorySourceUser, FilePaths: []string{"missing/gone.go"},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := k.ScanStaleness(root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Marked != 1 || len(res.Details) != 1 || res.Details[0].MemoryID != note.ID {
		t.Fatalf("scan marked %d rows (%+v), want just the note", res.Marked, res.Details)
	}
	got, _ := k.Get(note.ID)
	if got.Status != types.MemoryStatusStale {
		t.Errorf("note status = %q, want stale", got.Status)
	}
}

// Stats identifies sessions by tag, not by type: the v2 schema has one note
// class and D9 preserves the tag exactly so this keeps working.
func TestStats_CountsSessionsByTag(t *testing.T) {
	k := readPathKernel(t)

	for i := 0; i < 2; i++ {
		if _, _, err := k.Save(types.MemorySaveInput{
			Content: "Session summary", Type: types.MemoryTypeNote,
			Source: types.MemorySourceAgent, Tags: []string{"session"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := k.Save(types.MemorySaveInput{
		Content: "not a session", Type: types.MemoryTypeFact, Source: types.MemorySourceUser,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := k.Stats(24 * 60 * 60 * 1e9)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SessionsThisWeek != 2 {
		t.Errorf("sessions = %d, want 2", stats.SessionsThisWeek)
	}
	if stats.TotalLive != 3 {
		t.Errorf("total live = %d, want 3", stats.TotalLive)
	}
}
