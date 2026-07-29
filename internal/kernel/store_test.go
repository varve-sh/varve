package kernel

import (
	"database/sql"
	"testing"
	"time"

	"github.com/varve-sh/varve/internal/types"
	"github.com/varve-sh/varve/internal/util"
	_ "modernc.org/sqlite"
)

// These tests exercise the §D10 read-path port: MemoryStore's v1-shaped API
// backed by the v2 `decisions` and `notes` tables.
//
// They assert on rows surfaced, never on error-free calls. A recall that
// returns nothing is a passing test and a broken product — that is exactly how
// F1 shipped green.

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return freshDB(t)
}

// seedNote writes a note directly, returning its id.
func seedNote(t *testing.T, db *sql.DB, projectID, content string, opts ...func(*NoteInput)) string {
	t.Helper()
	in := NoteInput{
		ProjectID: projectID,
		Content:   content,
		Summary:   content,
		Tags:      []string{"test"},
	}
	for _, o := range opts {
		o(&in)
	}
	n, err := NewNoteStore(db).Insert(in)
	if err != nil {
		t.Fatalf("seed note: %v", err)
	}
	return n.ID
}

// seedDecision writes an accepted decision, returning its id.
func seedDecision(t *testing.T, db *sql.DB, projectID, title, body string, scope []string) string {
	t.Helper()
	ds := NewDecisionStore(db)
	d, err := ds.ProposeAccepted(DecisionInput{
		ProjectID: projectID, Title: title, Body: body, Scope: scope,
		Source: types.DecisionSourceUser, Tags: []string{"test"},
	}, AcceptOptions{Force: true})
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	return d.ID
}

// makeMemory builds a note in the v1 read shape. Only notes: decisions carry a
// lifecycle and are not insertable through MemoryStore.
func makeMemory(id, projectID string, memType types.MemoryType) *types.Memory {
	now := time.Now().UTC()
	return &types.Memory{
		ID:         id,
		Type:       memType.Canonical(),
		Content:    "test content for " + id,
		Summary:    "summary " + id,
		Source:     types.MemorySourceUser,
		Confidence: 1.0,
		ProjectID:  projectID,
		FilePaths:  []string{},
		Tags:       []string{"test"},
		Status:     types.MemoryStatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func ids(memories []types.Memory) map[string]types.Memory {
	out := make(map[string]types.Memory, len(memories))
	for _, m := range memories {
		out[m.ID] = m
	}
	return out
}

// The projection must surface both tables through one shape, with the column
// mapping §D10 implies.
func TestStore_ProjectsBothTables(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	decID := seedDecision(t, db, "proj1", "always wrap errors",
		"use %w so callers can errors.Is", []string{"internal/**"})
	noteID := seedNote(t, db, "proj1", "the CI runner is arm64")

	dec, err := store.FindByID(decID)
	if err != nil || dec == nil {
		t.Fatalf("decision not found: %v", err)
	}
	if dec.Type != types.MemoryTypeDecision {
		t.Errorf("decision type = %q, want decision", dec.Type)
	}
	if dec.Summary != "always wrap errors" {
		t.Errorf("summary should project from title, got %q", dec.Summary)
	}
	if dec.Content != "use %w so callers can errors.Is" {
		t.Errorf("content should project from body, got %q", dec.Content)
	}
	if len(dec.FilePaths) != 1 || dec.FilePaths[0] != "internal/**" {
		t.Errorf("file_paths should project from scope, got %v", dec.FilePaths)
	}
	if dec.Status != types.MemoryStatus(types.StatusActive) {
		t.Errorf("decisions surface with their own status (D10), got %q", dec.Status)
	}

	note, err := store.FindByID(noteID)
	if err != nil || note == nil {
		t.Fatalf("note not found: %v", err)
	}
	if note.Type != types.MemoryTypeNote {
		t.Errorf("note type = %q, want note", note.Type)
	}
	if note.Content != "the CI runner is arm64" {
		t.Errorf("note content = %q", note.Content)
	}
}

// A title-only decision must never surface as a blank row.
func TestStore_ProjectionFallsBackToTitleWhenBodyIsEmpty(t *testing.T) {
	db := setupTestDB(t)
	id := seedDecision(t, db, "proj1", "use ULIDs for ids", "", nil)
	m, err := NewStore(db).FindByID(id)
	if err != nil || m == nil {
		t.Fatal(err)
	}
	if m.Content != "use ULIDs for ids" {
		t.Errorf("content = %q, want the title as fallback", m.Content)
	}
}

func TestStore_FindByID_NotFound(t *testing.T) {
	m, err := NewStore(setupTestDB(t)).FindByID("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil, got %+v", m)
	}
}

// Decisions carry a lifecycle, so they must not be insertable through the
// v1-shaped store — that would bypass the state machine and its events.
func TestStore_InsertRefusesDecisions(t *testing.T) {
	store := NewStore(setupTestDB(t))
	err := store.Insert(&types.Memory{
		ID: util.GenerateID(), Type: types.MemoryTypeDecision, Content: "x",
		ProjectID: "proj1", Confidence: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("MemoryStore.Insert must refuse a decision")
	}
}

func TestStore_InsertNote(t *testing.T) {
	store := NewStore(setupTestDB(t))
	now := time.Now().UTC()
	m := &types.Memory{
		ID: util.GenerateID(), Type: types.MemoryTypeNote, Content: "a fact",
		Summary: "a fact", Source: types.MemorySourceUser, Confidence: 1.0,
		ProjectID: "proj1", FilePaths: []string{"a.go"}, Tags: []string{"t"},
		Status: types.MemoryStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Insert(m); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := store.FindByID(m.ID)
	if err != nil || got == nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if got.Content != "a fact" || got.Tags[0] != "t" || got.FilePaths[0] != "a.go" {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestStore_DeleteByID(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	id := seedNote(t, db, "proj1", "disposable")

	deleted, err := store.DeleteByID(id)
	if err != nil || !deleted {
		t.Fatalf("delete = (%v, %v), want (true, nil)", deleted, err)
	}
	if m, _ := store.FindByID(id); m != nil {
		t.Error("note survived deletion")
	}

	deleted, err = store.DeleteByID("nonexistent")
	if err != nil || deleted {
		t.Errorf("deleting a missing row = (%v, %v), want (false, nil)", deleted, err)
	}
}

func TestStore_Update(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	id := seedNote(t, db, "proj1", "original")

	newContent := "updated content"
	newTags := []string{"a", "b"}
	if err := store.Update(id, types.MemoryUpdateInput{
		Content: &newContent, Tags: &newTags,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := store.FindByID(id)
	if got.Content != newContent || len(got.Tags) != 2 {
		t.Errorf("update not applied: %+v", got)
	}

	// A decision's content is immutable after acceptance and its status moves
	// only through the state machine, so the v1-shaped update is refused.
	decID := seedDecision(t, db, "proj1", "a decision", "body", nil)
	if err := store.Update(decID, types.MemoryUpdateInput{Content: &newContent}); err == nil {
		t.Error("MemoryStore.Update must refuse a decision")
	}
}

func TestStore_ListAndCountSpanBothTables(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	decID := seedDecision(t, db, "proj1", "a decision", "body", nil)
	noteID := seedNote(t, db, "proj1", "a note")

	all, err := store.List(types.ListOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byID := ids(all)
	if _, ok := byID[decID]; !ok {
		t.Error("list did not surface the decision")
	}
	if _, ok := byID[noteID]; !ok {
		t.Error("list did not surface the note")
	}

	n, err := store.Count("", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}

	decisions, err := store.List(types.ListOptions{Type: types.MemoryTypeDecision, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].ID != decID {
		t.Errorf("type filter returned %d rows, want just the decision", len(decisions))
	}

	// fact and event are accepted input synonyms for the single note class.
	for _, alias := range []types.MemoryType{
		types.MemoryTypeNote, types.MemoryTypeFact, types.MemoryTypeEvent,
	} {
		notes, err := store.List(types.ListOptions{Type: alias, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(notes) != 1 || notes[0].ID != noteID {
			t.Errorf("type %q returned %d rows, want just the note", alias, len(notes))
		}
	}
}

// §D10: terminal decisions are excluded by default; proposed and violated are
// visible, because a violated decision is still binding.
func TestStore_DefaultVisibilityExcludesTerminalDecisionsOnly(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ds := NewDecisionStore(db)

	mk := func(title string) *types.Decision {
		d, err := ds.Propose(DecisionInput{
			ProjectID: "proj1", Title: title, Source: types.DecisionSourceAgent,
			SessionID: "s",
		})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	proposed := mk("still proposed")
	active := mk("accepted")
	ds.Accept(active.ID, AcceptOptions{Force: true})
	violated := mk("violated")
	ds.Accept(violated.ID, AcceptOptions{Force: true})
	ds.MarkViolated(violated.ID, ViolationOptions{CommitSHA: "sha"})
	rejected := mk("rejected")
	ds.Reject(rejected.ID, "no", types.ActorHuman)
	reverted := mk("reverted")
	ds.Accept(reverted.ID, AcceptOptions{Force: true})
	ds.Revert(reverted.ID, RevertOptions{Via: "human"})

	live, err := store.List(types.ListOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byID := ids(live)
	for _, want := range []struct {
		id, label string
	}{{proposed.ID, "proposed"}, {active.ID, "active"}, {violated.ID, "violated"}} {
		if _, ok := byID[want.id]; !ok {
			t.Errorf("%s decision must be visible by default", want.label)
		}
	}
	for _, gone := range []struct {
		id, label string
	}{{rejected.ID, "rejected"}, {reverted.ID, "reverted"}} {
		if _, ok := byID[gone.id]; ok {
			t.Errorf("%s decision must be excluded by default (D10)", gone.label)
		}
	}

	// An explicit status filter still reaches terminal rows.
	term, err := store.List(types.ListOptions{
		Status: types.MemoryStatus(types.StatusRejected), Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(term) != 1 || term[0].ID != rejected.ID {
		t.Errorf("explicit status filter returned %d rows, want the rejected decision", len(term))
	}
}

// §D10: recall searches decisions_fts AND notes_fts.
func TestStore_SearchFTSSpansBothFTSTables(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	decID := seedDecision(t, db, "proj1", "postgres is the primary datastore",
		"we chose postgres over mysql", nil)
	noteID := seedNote(t, db, "proj1", "the postgres connection pool is 20")
	seedNote(t, db, "proj2", "postgres notes from another project")

	results, err := store.SearchFTS("postgres", "proj1", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, r := range results {
		found[r.ID] = true
	}
	if !found[decID] {
		t.Error("FTS did not reach decisions_fts")
	}
	if !found[noteID] {
		t.Error("FTS did not reach notes_fts")
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2 (the other project must not leak)", len(results))
	}

	empty, err := store.SearchFTS("zzzznomatch", "proj1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("expected no matches, got %d", len(empty))
	}
}

// Terminal decisions must not be reachable by search either.
func TestStore_SearchFTSExcludesTerminalDecisions(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	ds := NewDecisionStore(db)

	d, _ := ds.Propose(DecisionInput{
		ProjectID: "proj1", Title: "kafka is the event bus",
		Source: types.DecisionSourceAgent, SessionID: "s",
	})
	if res, _ := store.SearchFTS("kafka", "proj1", 10); len(res) != 1 {
		t.Fatalf("a proposed decision should be searchable, got %d", len(res))
	}
	ds.Reject(d.ID, "changed our minds", types.ActorHuman)
	res, err := store.SearchFTS("kafka", "proj1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("a rejected decision must not be searchable, got %d results", len(res))
	}
}

// §D10: memory_context glob-matches decision scopes, and keeps v1 exact
// matching for note file_paths.
func TestStore_FindByFilePathsGlobsDecisionsAndExactMatchesNotes(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	globbed := seedDecision(t, db, "proj1", "kernel rules", "", []string{"internal/kernel/**"})
	exactDec := seedDecision(t, db, "proj1", "one file", "", []string{"cmd/main.go"})
	unrelated := seedDecision(t, db, "proj1", "docs rules", "", []string{"docs/*.md"})
	noteExact := seedNote(t, db, "proj1", "note on store.go", func(in *NoteInput) {
		in.FilePaths = []string{"internal/kernel/store.go"}
	})
	noteGlob := seedNote(t, db, "proj1", "note with a glob-looking path", func(in *NoteInput) {
		in.FilePaths = []string{"internal/kernel/**"}
	})

	got, err := store.FindByFilePaths("proj1", []string{"internal/kernel/store.go"})
	if err != nil {
		t.Fatal(err)
	}
	byID := ids(got)

	if _, ok := byID[globbed]; !ok {
		t.Error("a decision scoped internal/kernel/** must match internal/kernel/store.go — " +
			"this is the exact-equality bug the port exists to fix")
	}
	if _, ok := byID[noteExact]; !ok {
		t.Error("a note with the exact path must still match")
	}
	if _, ok := byID[exactDec]; ok {
		t.Error("cmd/main.go must not match internal/kernel/store.go")
	}
	if _, ok := byID[unrelated]; ok {
		t.Error("docs/*.md must not match")
	}
	if _, ok := byID[noteGlob]; ok {
		t.Error("note file_paths keep v1 exact-match semantics; they are not patterns")
	}

	// An exact path in a decision scope still matches itself.
	got, _ = store.FindByFilePaths("proj1", []string{"cmd/main.go"})
	if _, ok := ids(got)[exactDec]; !ok {
		t.Error("an exact path is a glob that matches itself")
	}
}

func TestStore_FindUnembeddedAndEmbeddingsSpanBothTables(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	decID := seedDecision(t, db, "proj1", "a decision", "body", nil)
	noteID := seedNote(t, db, "proj1", "a note")
	seedNote(t, db, "proj2", "another project")

	un, err := store.FindUnembedded("proj1")
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 2 {
		t.Fatalf("unembedded = %d, want 2 (one of each class)", len(un))
	}

	if err := store.StoreEmbedding(decID, []float64{0.1, 0.2}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreEmbedding(noteID, []float64{0.3, 0.4}); err != nil {
		t.Fatal(err)
	}

	un, _ = store.FindUnembedded("proj1")
	if len(un) != 0 {
		t.Errorf("unembedded = %d after storing both, want 0", len(un))
	}

	embs, err := store.FindEmbeddings("proj1")
	if err != nil {
		t.Fatal(err)
	}
	if len(embs) != 2 {
		t.Fatalf("embeddings = %d, want 2", len(embs))
	}
	found := map[string][]float64{}
	for _, e := range embs {
		found[e.ID] = e.Embedding
	}
	if len(found[decID]) != 2 || found[decID][0] != 0.1 {
		t.Errorf("decision embedding = %v", found[decID])
	}
	if len(found[noteID]) != 2 || found[noteID][0] != 0.3 {
		t.Errorf("note embedding = %v", found[noteID])
	}
}

func TestStore_TouchAccessAndTopAccessed(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	decID := seedDecision(t, db, "proj1", "a decision", "body", nil)
	noteID := seedNote(t, db, "proj1", "a note")

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if err := store.TouchAccess(decID, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.TouchAccess(noteID, now); err != nil {
		t.Fatal(err)
	}

	dec, _ := store.FindByID(decID)
	if dec.AccessCount != 3 || dec.AccessedAt == nil {
		t.Errorf("decision access tracking = %d/%v", dec.AccessCount, dec.AccessedAt)
	}
	note, _ := store.FindByID(noteID)
	if note.AccessCount != 1 {
		t.Errorf("note access count = %d, want 1", note.AccessCount)
	}

	top, err := store.TopAccessed("proj1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].ID != decID {
		t.Errorf("top accessed = %d rows, first %v; want the decision first", len(top), top)
	}
}

// §D8 gives `decisions` and `notes` independent topic_key indexes, so the
// namespaces are per table: the same key may be held once in each. The lookup
// that spanned both tables is gone — it unified the namespaces in one
// direction only (a note save saw a decision's key; a decision save never saw
// a note's), which broke the note save outright (F13).
func TestStores_TopicKeyNamespacesArePerTable(t *testing.T) {
	db := setupTestDB(t)

	noteID := seedNote(t, db, "proj1", "a keyed note", func(in *NoteInput) {
		in.TopicKey = "auth"
	})
	ds := NewDecisionStore(db)
	d, err := ds.ProposeAccepted(DecisionInput{
		ProjectID: "proj1", Title: "a keyed decision", Source: types.DecisionSourceUser,
		TopicKey: "auth",
	}, AcceptOptions{Force: true})
	if err != nil {
		t.Fatalf("a decision must be able to hold a key a note also holds: %v", err)
	}

	ns := NewNoteStore(db)
	got, err := ns.FindByTopicKey("proj1", "auth")
	if err != nil || got == nil || got.ID != noteID {
		t.Errorf("note topic lookup = %v, %v; want the note, never the decision", got, err)
	}
	if got, err := ns.FindByTopicKey("proj1", "missing"); err != nil || got != nil {
		t.Errorf("missing topic = %v, %v", got, err)
	}

	held, err := ds.ListDecisions(DecisionFilter{ProjectID: "proj1", TopicKey: "auth"})
	if err != nil || len(held) != 1 || held[0].ID != d.ID {
		t.Errorf("decision topic lookup = %v, %v", held, err)
	}
}

func TestStore_CountSince(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	seedDecision(t, db, "proj1", "a decision", "body", nil)
	seedNote(t, db, "proj1", "a note")

	n, err := store.CountSince("created_at", time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("created since = %d, want 2", n)
	}
	n, _ = store.CountSince("created_at", time.Now().UTC().Add(time.Hour))
	if n != 0 {
		t.Errorf("created in the future = %d, want 0", n)
	}
	if _, err := store.CountSince("bogus", time.Now()); err == nil {
		t.Error("an arbitrary column name must be rejected")
	}
}
