package pack

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/retrieval"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// A pack that returns zero items is a passing test and a broken product, so
// every test here asserts on what was served, never on an error-free call.

var fixedNow = time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)

// fakeSource is a store stub. The packer's contract is with the columns, not
// with SQLite, so the unit tests drive it directly and the integration tests
// (internal/kernel) drive it through the real store.
type fakeSource struct {
	decisions  []types.Decision
	proposed   []types.Decision
	notes      []types.Note
	text       map[string]float64 // id -> raw BM25 rank (negative)
	embeddings map[string][]float64
	queryVec   []float64
	timeout    bool
	unresolved map[string]int
	evidence   map[string][]types.Evidence

	textCalls int
}

func (f *fakeSource) PackableDecisions(string) ([]types.Decision, error) { return f.decisions, nil }
func (f *fakeSource) ProposedDecisions(string) ([]types.Decision, error) { return f.proposed, nil }
func (f *fakeSource) ActiveNotes(string) ([]types.Note, error)           { return f.notes, nil }

func (f *fakeSource) TextPool(string, string, int) ([]types.FTSResult, error) {
	f.textCalls++
	out := make([]types.FTSResult, 0, len(f.text))
	for id, rank := range f.text {
		out = append(out, types.FTSResult{ID: id, Rank: rank})
	}
	// Deterministic order, so the test's own fixture cannot introduce noise.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ID < out[i].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func (f *fakeSource) Embeddings(string) ([]retrieval.EmbeddingRow, error) {
	var out []retrieval.EmbeddingRow
	for id, v := range f.embeddings {
		out = append(out, retrieval.EmbeddingRow{ID: id, Embedding: v})
	}
	return out, nil
}

func (f *fakeSource) EmbedQuery(string) ([]float64, bool) { return f.queryVec, f.timeout }

func (f *fakeSource) UnresolvedViolations(string) (map[string]int, error) {
	return f.unresolved, nil
}

func (f *fakeSource) Evidence(string) (map[string][]types.Evidence, error) {
	return f.evidence, nil
}

func decision(id, title string, opts ...func(*types.Decision)) types.Decision {
	d := types.Decision{
		ID: id, ProjectID: "p", Kind: types.DecisionKindDecision,
		Title: title, Body: "Because it matters.", Status: types.StatusActive,
		Confidence: 1.0, Source: types.DecisionSourceUser,
		CreatedAt: fixedNow.Add(-24 * time.Hour), UpdatedAt: fixedNow.Add(-24 * time.Hour),
	}
	decided := fixedNow.Add(-24 * time.Hour)
	d.DecidedAt = &decided
	for _, o := range opts {
		o(&d)
	}
	return d
}

func withScope(globs ...string) func(*types.Decision) {
	return func(d *types.Decision) { d.Scope = globs }
}

func note(id, content string, paths ...string) types.Note {
	return types.Note{
		ID: id, ProjectID: "p", Content: content, Status: types.MemoryStatusActive,
		Confidence: 1.0, FilePaths: paths,
		CreatedAt: fixedNow.Add(-24 * time.Hour), UpdatedAt: fixedNow.Add(-24 * time.Hour),
	}
}

func build(t *testing.T, src *fakeSource, req Request) *Result {
	t.Helper()
	if req.Now.IsZero() {
		req.Now = fixedNow
	}
	res, err := Build(src, "p", req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return res
}

// --- P1: the contract ---

func TestValidate_Errors(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want error
	}{
		{"budget too small", Request{FilePaths: []string{"a.go"}, BudgetTokens: 499}, ErrBadBudget},
		{"budget too large", Request{FilePaths: []string{"a.go"}, BudgetTokens: 100001}, ErrBadBudget},
		{"absolute path", Request{FilePaths: []string{"/etc/passwd"}}, ErrBadPath},
		{"escaping path", Request{FilePaths: []string{"../../secrets.go"}}, ErrBadPath},
		{"no anchor", Request{}, ErrNoAnchor},
		{"blank anchor", Request{FilePaths: []string{"  "}, Task: "   "}, ErrNoAnchor},
	}
	for _, c := range cases {
		req := c.req
		err := req.Validate()
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		// The code is the first token, so an agent can branch without parsing.
		if err != nil && !strings.HasPrefix(err.Error(), c.want.Error()) {
			t.Errorf("%s: message must start with the code, got %q", c.name, err)
		}
	}
}

func TestValidate_NormalizesPaths(t *testing.T) {
	req := Request{FilePaths: []string{"./b.go", "a.go", "b.go", ""}, BudgetTokens: 0}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(req.FilePaths) != 2 || req.FilePaths[0] != "a.go" || req.FilePaths[1] != "b.go" {
		t.Errorf("paths = %v, want deduplicated, ./-stripped, order-insensitive", req.FilePaths)
	}
	if req.BudgetTokens != DefaultBudget {
		t.Errorf("budget = %d, want the default %d", req.BudgetTokens, DefaultBudget)
	}
}

// An empty pack is a fact worth recording, not an error (§P1) — it is the
// cold-start signal.
func TestBuild_EmptyPackIsNotAnError(t *testing.T) {
	res := build(t, &fakeSource{}, Request{FilePaths: []string{"internal/auth/x.go"}})
	if res.ItemCount != 0 {
		t.Fatalf("items = %d, want 0", res.ItemCount)
	}
	if !strings.Contains(res.Text, "No binding decisions for these files.") {
		t.Errorf("an empty pack must still say something:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "items: 0") {
		t.Errorf("manifest must report the empty pack:\n%s", res.Text)
	}
}

// --- P2: eligibility ---

func TestBuild_EligibilityIsDecidedBeforeRanking(t *testing.T) {
	expired := fixedNow.Add(-time.Hour)
	src := &fakeSource{
		decisions: []types.Decision{
			decision("01ACTIVE", "Active and in scope", withScope("internal/**")),
			decision("01VIOLATED", "Violated but still binding", withScope("internal/**"),
				func(d *types.Decision) { d.Status = types.StatusViolated }),
			decision("01EXPIRED", "Expired", withScope("internal/**"),
				func(d *types.Decision) { d.ExpiresAt = &expired }),
			// A terminal row must never pack even if the store hands it over.
			decision("01TERMINAL", "Superseded", withScope("internal/**"),
				func(d *types.Decision) { d.Status = types.StatusSuperseded }),
		},
		proposed: []types.Decision{
			decision("01PROPOSED", "A proposal in scope", withScope("internal/**"),
				func(d *types.Decision) { d.Status = types.StatusProposed }),
		},
		unresolved: map[string]int{"01VIOLATED": 2},
	}

	res := build(t, src, Request{FilePaths: []string{"internal/auth/x.go"}})

	if !strings.Contains(res.Text, "01ACTIVE") {
		t.Errorf("an active in-scope decision must pack:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "VIOLATED (2 unresolved)") {
		t.Errorf("a violated decision packs, flagged with its unresolved count:\n%s", res.Text)
	}
	for _, id := range []string{"01EXPIRED", "01TERMINAL"} {
		if strings.Contains(res.Text, id) {
			t.Errorf("%s must never be packed:\n%s", id, res.Text)
		}
	}
	// The proposal is counted, never served as content.
	if strings.Contains(res.Text, "A proposal in scope") {
		t.Errorf("proposed text was laundered into the pack:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "proposed decisions touching these files: 1") {
		t.Errorf("the proposal must be counted in the footer:\n%s", res.Text)
	}
	if res.ProposedMatched != 1 {
		t.Errorf("proposed_matched = %d, want 1", res.ProposedMatched)
	}
	// The expiry observation is handed back for the caller to record.
	if len(res.ExpiredObserved) != 1 || res.ExpiredObserved[0] != "01EXPIRED" {
		t.Errorf("expired observations = %v, want [01EXPIRED]", res.ExpiredObserved)
	}
}

func TestBuild_NotesAreASecondTierAndNeverDisplaceADecision(t *testing.T) {
	src := &fakeSource{
		decisions: []types.Decision{
			// Deliberately weak: unscoped-by-glob, low confidence, old.
			decision("01DEC", "A weak decision", withScope("internal/**"),
				func(d *types.Decision) {
					d.Confidence = 0.2
					d.CreatedAt = fixedNow.Add(-365 * 24 * time.Hour)
					d.UpdatedAt = d.CreatedAt
				}),
		},
		notes: []types.Note{note("01NOTE", "A very relevant note", "internal/auth/x.go")},
	}
	res := build(t, src, Request{FilePaths: []string{"internal/auth/x.go"}})
	if len(res.Served) != 2 {
		t.Fatalf("served %d items, want 2", len(res.Served))
	}
	if res.Served[0].ID != "01DEC" || res.Served[0].Class != ClassDecision {
		t.Errorf("a note outranked a decision: %+v", res.Served)
	}
	if res.Served[1].Form != FormSummary {
		t.Errorf("notes render in summary form, got %s", res.Served[1].Form)
	}

	// include_notes=false removes them entirely.
	res = build(t, src, Request{FilePaths: []string{"internal/auth/x.go"}, ExcludeNotes: true})
	for _, s := range res.Served {
		if s.Class == ClassNote {
			t.Errorf("include_notes=false still served a note: %+v", s)
		}
	}
}

// --- P3: ranking ---

func TestBuild_ScopeSpecificityOutranksBreadth(t *testing.T) {
	src := &fakeSource{decisions: []types.Decision{
		decision("01BROAD", "Broad", withScope("**")),
		decision("01EXACT", "Exact", withScope("internal/auth/x.go")),
		decision("01GLOB", "Glob", withScope("internal/auth/*.go")),
	}}
	res := build(t, src, Request{FilePaths: []string{"internal/auth/x.go"}})
	want := []string{"01EXACT", "01GLOB", "01BROAD"}
	for i, id := range want {
		if res.Served[i].ID != id {
			t.Fatalf("rank %d = %s, want %s (specificity must order these)",
				i+1, res.Served[i].ID, id)
		}
	}
}

func TestBuild_UnscopedConventionIsAlwaysACandidate(t *testing.T) {
	src := &fakeSource{decisions: []types.Decision{
		decision("01CONV", "Errors are wrapped with %w", func(d *types.Decision) {
			d.Kind = types.DecisionKindConvention
			d.Scope = nil
		}),
	}}
	res := build(t, src, Request{FilePaths: []string{"anything/at/all.go"}})
	if len(res.Served) != 1 {
		t.Fatalf("an unscoped convention is always a candidate; served %d", len(res.Served))
	}
	if !strings.Contains(res.Text, "active (convention, repo-wide)") {
		t.Errorf("the repo-wide marker is missing:\n%s", res.Text)
	}
}

func TestBuild_TieBreakIsTotalAndDeterministic(t *testing.T) {
	// Identical scores, identical confidence, identical decided_at: only the
	// ULID tiebreak can order these, and it must, every time.
	src := &fakeSource{decisions: []types.Decision{
		decision("01BBB", "Second", withScope("a.go")),
		decision("01AAA", "First", withScope("a.go")),
		decision("01CCC", "Third", withScope("a.go")),
	}}
	for i := 0; i < 5; i++ {
		res := build(t, src, Request{FilePaths: []string{"a.go"}})
		got := []string{res.Served[0].ID, res.Served[1].ID, res.Served[2].ID}
		want := []string{"01AAA", "01BBB", "01CCC"}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("run %d order = %v, want %v", i, got, want)
			}
		}
	}
}

func TestBuild_TaskOnlyRenormalizesWeights(t *testing.T) {
	src := &fakeSource{
		decisions: []types.Decision{
			decision("01HIT", "Matches the task", withScope("nowhere/**")),
			decision("01MISS", "Does not", withScope("nowhere/**")),
		},
		text: map[string]float64{"01HIT": -2.0},
	}
	res := build(t, src, Request{Task: "refresh token rotation"})
	if len(res.Served) != 1 || res.Served[0].ID != "01HIT" {
		t.Fatalf("task-only pack served %+v, want just the text match", res.Served)
	}
	if res.Served[0].Score <= 0 {
		t.Errorf("score = %f; with scope's weight dropped the rest must renormalize",
			res.Served[0].Score)
	}
}

// --- P4: the budget is a ceiling ---

func TestBuild_BudgetIsNeverExceeded(t *testing.T) {
	var decisions []types.Decision
	for i := 0; i < 40; i++ {
		d := decision(fmt.Sprintf("01DEC%03d", i), fmt.Sprintf("Decision number %d", i),
			withScope("internal/**"))
		d.Body = strings.Repeat("This is a long rationale that costs real budget. ", 20)
		decisions = append(decisions, d)
	}
	var notes []types.Note
	for i := 0; i < 20; i++ {
		notes = append(notes, note(fmt.Sprintf("01NOTE%03d", i),
			strings.Repeat("note body ", 50), "internal/auth/x.go"))
	}
	src := &fakeSource{decisions: decisions, notes: notes}

	for _, budget := range []int{500, 750, 1000, 2000, 5000, 20000} {
		res := build(t, src, Request{
			FilePaths: []string{"internal/auth/x.go"}, BudgetTokens: budget,
		})
		if got := Estimate(res.Text); got > budget {
			t.Errorf("budget %d: emitted %d est-tokens — the budget is a ceiling, "+
				"and this is falsifier 1 firing", budget, got)
		}
		if res.UsedTokens != Estimate(res.Text) {
			t.Errorf("budget %d: used_tokens %d disagrees with the text it describes (%d)",
				budget, res.UsedTokens, Estimate(res.Text))
		}
		// Nothing is dropped silently: everything eligible is served or named.
		if res.ItemCount+res.OmittedCount+res.DedupedCount != 60 {
			t.Errorf("budget %d: %d served + %d omitted + %d deduped != 60 candidates",
				budget, res.ItemCount, res.OmittedCount, res.DedupedCount)
		}
	}
}

// §P4/§P9: at the 500 floor the pack degrades to an index — it still tells the
// agent that binding decisions exist and which ones.
func TestBuild_AtTheFloorItStillNamesTheTopDecision(t *testing.T) {
	d := decision("01BIG", strings.Repeat("A long but legal title. ", 8), withScope("internal/**"))
	d.Body = strings.Repeat("An extremely long body that cannot fit anywhere. ", 200)
	src := &fakeSource{decisions: []types.Decision{d}}

	res := build(t, src, Request{FilePaths: []string{"internal/auth/x.go"}, BudgetTokens: MinBudget})
	if res.ItemCount == 0 {
		t.Fatalf("a pack at the floor still names the top decision; got nothing:\n%s", res.Text)
	}
	if res.Served[0].Form != FormStub {
		t.Errorf("form = %s, want a stub at the floor", res.Served[0].Form)
	}
	if !strings.Contains(res.Text, "body elided") || !strings.Contains(res.Text, "memory_get 01BIG") {
		t.Errorf("a stub must carry its memory_get pointer:\n%s", res.Text)
	}
	if Estimate(res.Text) > MinBudget {
		t.Errorf("floor pack is %d est-tokens, over the %d ceiling", Estimate(res.Text), MinBudget)
	}
}

// Skip-and-continue, not first-fit-stop (§P4): one huge mid-rank item must not
// starve the smaller ones below it.
func TestBuild_LargeItemDoesNotStarveTheOnesBelow(t *testing.T) {
	huge := decision("01HUGE", "Huge but second", withScope("internal/auth/*.go"))
	huge.Body = strings.Repeat("enormous ", 3000)
	src := &fakeSource{decisions: []types.Decision{
		decision("01TOP", "Top", withScope("internal/auth/x.go")),
		huge,
		decision("01SMALL", "Small and third", withScope("internal/auth/*.go"),
			func(d *types.Decision) { d.Confidence = 0.9 }),
	}}
	res := build(t, src, Request{FilePaths: []string{"internal/auth/x.go"}, BudgetTokens: 700})

	served := map[string]bool{}
	for _, s := range res.Served {
		served[s.ID] = true
	}
	if !served["01TOP"] {
		t.Error("the top-ranked decision must get first claim on the budget")
	}
	if !served["01SMALL"] {
		t.Errorf("a small low-rank item was starved by a huge mid-rank one:\n%s", res.Text)
	}
}

// --- P5: dedup ---

func TestBuild_DedupsTopicEchoesAndIdenticalText(t *testing.T) {
	d := decision("01DEC", "Sessions are server-side", withScope("internal/auth/x.go"))
	d.TopicKey = "session-store"
	echo := note("01ECHO", "Sessions are server-side", "internal/auth/x.go")
	echo.TopicKey = "session-store"
	twin := note("01TWIN", "A distinct note", "internal/auth/x.go")
	twinCopy := note("01TWIN2", "A  DISTINCT   note", "internal/auth/x.go")

	src := &fakeSource{
		decisions: []types.Decision{d},
		notes:     []types.Note{echo, twin, twinCopy},
	}
	res := build(t, src, Request{FilePaths: []string{"internal/auth/x.go"}})

	if res.DedupedCount != 2 {
		t.Fatalf("deduped = %d, want 2 (topic echo + whitespace-identical text)", res.DedupedCount)
	}
	if strings.Contains(res.Text, "01ECHO") && !strings.Contains(res.Text, "topic echo") {
		t.Errorf("the topic echo must be named as deduped:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "-- deduped: 2") {
		t.Errorf("the footer must report the dedup count:\n%s", res.Text)
	}
}

func TestBuild_DedupsNearDuplicateEmbeddings(t *testing.T) {
	src := &fakeSource{
		decisions: []types.Decision{
			decision("01AAA", "Wrap errors", withScope("a.go")),
			decision("01BBB", "Errors get wrapped", withScope("a.go")),
		},
		embeddings: map[string][]float64{
			"01AAA": {1, 0, 0},
			"01BBB": {0.99, 0.01, 0}, // cosine ≈ 0.9999
		},
	}
	res := build(t, src, Request{FilePaths: []string{"a.go"}})
	if res.ItemCount != 1 || res.DedupedCount != 1 {
		t.Fatalf("served %d, deduped %d; want one of each", res.ItemCount, res.DedupedCount)
	}
	if !strings.Contains(res.Text, "deduped against 01AAA") {
		t.Errorf("the dedup must name what it was dropped against:\n%s", res.Text)
	}
}

// --- P12: determinism ---

func TestBuild_IsByteIdenticalAcrossRuns(t *testing.T) {
	src := &fakeSource{
		decisions: []types.Decision{
			decision("01AAA", "First", withScope("internal/**")),
			decision("01BBB", "Second", withScope("internal/auth/x.go")),
		},
		notes:      []types.Note{note("01NNN", "A note", "internal/auth/x.go")},
		unresolved: map[string]int{},
	}
	req := Request{FilePaths: []string{"internal/auth/x.go"}, Task: "rotate tokens"}
	first := build(t, src, req)
	for i := 0; i < 10; i++ {
		// Same hour, different instants: §P12 truncates the clock to the hour.
		r := req
		r.Now = fixedNow.Add(time.Duration(i) * time.Minute)
		got := build(t, src, r)
		if got.Text != first.Text {
			t.Fatalf("run %d differs:\n--- first ---\n%s\n--- got ---\n%s", i, first.Text, got.Text)
		}
	}
	// Path order must not matter either.
	shuffled := Request{FilePaths: []string{"internal/auth/x.go"}, Task: "rotate tokens"}
	if build(t, src, shuffled).Text != first.Text {
		t.Error("path order changed the bytes")
	}
}

// --- P8: the golden pack ---

func goldenSource() *fakeSource {
	d1 := decision("01J9WAAAAAAAAAAAAAAAAAAAAA", "Refresh tokens rotate on every use.",
		withScope("internal/auth/**"))
	d1.Body = "Reuse detection revokes the family."
	d1.Confidence = 0.92

	d2 := decision("01J8QBBBBBBBBBBBBBBBBBBBBB", "Sessions are stored server-side only.",
		withScope("internal/auth/session.go"), func(d *types.Decision) {
			d.Status = types.StatusViolated
			d.Confidence = 0.88
		})
	d2.Body = "No JWT session state."

	conv := decision("01J7XCCCCCCCCCCCCCCCCCCCCC", "Errors are wrapped with %w.",
		func(d *types.Decision) {
			d.Kind = types.DecisionKindConvention
			d.Scope = nil
			d.Confidence = 0.75
		})
	conv.Body = "Never logged at the call site."

	src := &fakeSource{
		decisions:  []types.Decision{d1, d2, conv},
		notes:      []types.Note{note("01J6TDDDDDDDDDDDDDDDDDDDDD", "Session table was denormalized in March.", "internal/auth/session.go")},
		proposed:   []types.Decision{decision("01J2HEEEEEEEEEEEEEEEEEEEEE", "A proposal", withScope("internal/auth/**"), func(d *types.Decision) { d.Status = types.StatusProposed })},
		unresolved: map[string]int{"01J8QBBBBBBBBBBBBBBBBBBBBB": 2},
		evidence: map[string][]types.Evidence{
			"01J9WAAAAAAAAAAAAAAAAAAAAA": {
				{Kind: types.EvidenceKindCommit, Ref: "4f2a91c"},
				{Kind: types.EvidenceKindPR, Ref: "#87"},
			},
		},
	}

	return src
}

func TestBuild_GoldenPack(t *testing.T) {
	src := goldenSource()
	res := build(t, src, Request{
		FilePaths: []string{"internal/auth/middleware.go", "internal/auth/session.go"},
		Task:      "add refresh-token rotation",
	})

	const golden = `VARVE PACK v1
files: internal/auth/middleware.go, internal/auth/session.go
task: add refresh-token rotation
budget: 2000 est-tokens (bytes/3 v1) · used: 351 · items: 4 (3 decisions, 1 note) · omitted: 0

[1] DECISION 01J8QBBBBBBBBBBBBBBBBBBBBB · VIOLATED (2 unresolved) · conf 0.88 · scope: internal/auth/session.go
Sessions are stored server-side only.
No JWT session state.

[2] DECISION 01J9WAAAAAAAAAAAAAAAAAAAAA · active · conf 0.92 · scope: internal/auth/**
Refresh tokens rotate on every use.
Reuse detection revokes the family.
evidence: commit 4f2a91c, pr #87

[3] DECISION 01J7XCCCCCCCCCCCCCCCCCCCCC · active (convention, repo-wide) · conf 0.75
Errors are wrapped with %w.
Never logged at the call site.

[4] NOTE 01J6TDDDDDDDDDDDDDDDDDDDDD · active · files: internal/auth/session.go
Session table was denormalized in March.
(full text: memory_get 01J6TDDDDDDDDDDDDDDDDDDDDD)

-- proposed decisions touching these files: 1 (01J2HEEEEEEEEEEEEEEEEEEEEE) — not binding until accepted; review with ` + "`memtrace decision accept <id>`" + `
`
	if res.Text != golden {
		t.Errorf("pack is not byte-identical to the golden file.\n--- got ---\n%s\n--- want ---\n%s",
			res.Text, golden)
	}
}
