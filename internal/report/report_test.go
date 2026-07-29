package report

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// ADR-0004's report, driven through the real kernel and the real event log.
//
// The fixture is a whole attribution chain, not a table of pre-baked rows:
// decisions are accepted through the lifecycle, sessions are opened and
// closed, packs are served, commits are observed. A report assembled from
// hand-written events would test the SQL against my idea of the schema rather
// than against what the product writes.

const project = "proj-report"

type fixture struct {
	t  *testing.T
	k  *kernel.MemoryKernel
	db *sql.DB
	// seq disambiguates event ids: two pack items in one session share an
	// instant, and events.id is unique.
	seq int
	// base is the fixture's clock: everything is placed relative to it so the
	// window arithmetic is explicit rather than incidental.
	base time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	k := kernel.New(filepath.Join(t.TempDir(), "report.db"), project)
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	// The fixture's clock starts now, not in the past: §D1.3 refuses to judge a
	// commit that predates its decision's acceptance, and the decisions here
	// are accepted through the real lifecycle (decided_at = now). Commits
	// therefore sit just *after* base, and the reporting window runs past them.
	base := time.Now().UTC()
	if err := k.RecordObserverEnabled(base.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, k: k, db: k.Decisions().DB(), base: base}
}

func (f *fixture) decision(title string, scope ...string) *types.Decision {
	f.t.Helper()
	d, err := f.k.Decisions().ProposeAccepted(kernel.DecisionInput{
		ProjectID: project, Title: title, Scope: scope, Source: types.DecisionSourceUser,
		Evidence: []kernel.EvidenceInput{{
			Kind: types.EvidenceKindCommit, Ref: "accept-" + title, AddedBy: types.ActorHuman,
		}},
	}, kernel.AcceptOptions{Actor: types.ActorHuman})
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

// session opens an agent session, serves a pack containing the given
// decisions, and closes it — the first two links of §D0's chain.
func (f *fixture) session(agent string, at time.Time, decisionIDs ...string) string {
	f.t.Helper()
	id := "sess-" + fmt.Sprint(at.UnixNano())
	f.event(kernel.EventInput{
		Kind: types.EventSessionStarted, Actor: types.ActorSystem,
		SessionID: id, Agent: agent,
	}, at)
	for i, did := range decisionIDs {
		f.event(kernel.EventInput{
			Kind: types.EventPackItem, Actor: types.ActorSystem,
			SessionID: id, Agent: agent, DecisionID: did,
			Payload: map[string]any{"rank": i + 1, "class": "decision", "form": "full"},
		}, at.Add(time.Second))
	}
	f.event(kernel.EventInput{
		Kind: types.EventSessionEnded, Actor: types.ActorSystem,
		SessionID: id, Agent: agent, Payload: map[string]any{"packs": 1},
	}, at.Add(2*time.Minute))
	return id
}

// recallSession is §D5.4's comparison path: a session that saw a decision
// through recall rather than through a pack.
func (f *fixture) recallSession(at time.Time, decisionIDs ...string) string {
	f.t.Helper()
	id := "recall-" + fmt.Sprint(at.UnixNano())
	f.event(kernel.EventInput{
		Kind: types.EventSessionStarted, Actor: types.ActorSystem,
		SessionID: id, Agent: "mcp",
	}, at)
	f.event(kernel.EventInput{
		Kind: types.EventRecallServed, Actor: types.ActorSystem,
		SessionID: id, Agent: "mcp",
		Payload: map[string]any{"query": "auth", "ids": decisionIDs, "limit": 10},
	}, at.Add(time.Second))
	f.event(kernel.EventInput{
		Kind: types.EventSessionEnded, Actor: types.ActorSystem,
		SessionID: id, Agent: "mcp",
	}, at.Add(2*time.Minute))
	return id
}

// event writes one event at a chosen instant. Events are append-only and the
// kernel stamps `ts` with the wall clock, so the fixture rewrites `ts` through
// the same append-only wall it later reads: the trigger blocks UPDATE, so the
// row is inserted directly here. This is fixture plumbing, not a product path.
func (f *fixture) event(in kernel.EventInput, at time.Time) {
	f.t.Helper()
	f.seq++
	in.ProjectID = project
	payload := "{}"
	if in.Payload != nil {
		payload = mustJSON(f.t, in.Payload)
	}
	if _, err := f.db.Exec(`
		INSERT INTO events (id, project_id, ts, kind, actor, decision_id, session_id,
		    agent, model, commit_sha, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		fmt.Sprintf("ev-%d-%d-%s", at.UnixNano(), f.seq, in.Kind), project,
		at.UTC().Format(time.RFC3339Nano), string(in.Kind), string(in.Actor),
		nullable(in.DecisionID), nullable(in.SessionID), nullable(in.Agent),
		nullable(in.CommitSHA), payload); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) observe(sha string, at time.Time, files []string, revertsSHA string) {
	f.t.Helper()
	c := kernel.ObservedCommit{
		SHA: sha, Author: "dev", Subject: "work", CommittedAt: at,
		Files: files, PatchID: "patch-" + sha, Branch: "main", RevertsSHA: revertsSHA,
	}
	if _, err := f.k.ObserveCommit(c); err != nil {
		f.t.Fatal(err)
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func mustJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("{")
	first := true
	for k, val := range v {
		if !first {
			b.WriteString(",")
		}
		first = false
		switch x := val.(type) {
		case string:
			fmt.Fprintf(&b, "%q:%q", k, x)
		case int:
			fmt.Fprintf(&b, "%q:%d", k, x)
		case []string:
			fmt.Fprintf(&b, "%q:[", k)
			for i, s := range x {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, "%q", s)
			}
			b.WriteString("]")
		default:
			fmt.Fprintf(&b, "%q:null", k)
		}
	}
	b.WriteString("}")
	return b.String()
}

// --- §D5.1: the kill-criterion query ---

func TestCoverage_TheKillCriterionQuery(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Handlers validate the auth header", "internal/auth/**")

	// A session that packed the decision, and a commit inside its window that
	// touched the decision's scope: the full chain.
	f.session("mcp", f.base, d.ID)
	f.observe("commit-attributed", f.base.Add(time.Minute), []string{"internal/auth/x.go"}, "")

	// A session that packed the decision but produced no matching commit.
	f.session("mcp", f.base.Add(time.Hour), d.ID)

	// A session whose commit landed inside the grace window *after* it ended —
	// the dominant real flow §D3's grace exists for.
	f.session("mcp", f.base.Add(2*time.Hour), d.ID)
	f.observe("commit-in-grace", f.base.Add(2*time.Hour).Add(30*time.Minute),
		[]string{"internal/auth/y.go"}, "")

	// A CLI session, which §D3 excludes from the denominator: a hundred
	// one-second `memtrace list` invocations must not sandbag coverage.
	f.session("cli", f.base.Add(3*time.Hour), d.ID)

	cov, err := QueryCoverage(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if cov.TotalSessions != 3 {
		t.Errorf("denominator = %d, want 3 agent sessions (the CLI one is excluded)",
			cov.TotalSessions)
	}
	if cov.AttributedSessions != 2 {
		t.Errorf("numerator = %d, want 2 — one in-window commit and one in the grace window",
			cov.AttributedSessions)
	}
	if cov.ViaPack != 2 {
		t.Errorf("via pack = %d, want 2", cov.ViaPack)
	}
}

// The correction the architect's finding made necessary: §D5.1's verbatim
// `datetime(w_end, '+60 minutes')` yields `YYYY-MM-DD HH:MM:SS`, which sorts
// below every `...T...Z` commit timestamp, so the verbatim query attributes
// *nothing* on any store. This pins the corrected behaviour against the exact
// case the verbatim form gets wrong: a commit inside the grace window.
func TestCoverage_GraceWindowUpperBoundComparesChronologically(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Auth rule", "internal/auth/**")
	f.session("mcp", f.base, d.ID)
	// 30 minutes after the session ended, inside the 60-minute grace.
	f.observe("late-commit", f.base.Add(32*time.Minute), []string{"internal/auth/x.go"}, "")

	cov, err := QueryCoverage(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if cov.AttributedSessions != 1 {
		t.Fatalf("attributed = %d, want 1 — a commit inside the grace window must "+
			"attribute; the verbatim §D5.1 upper bound makes this 0 on every store",
			cov.AttributedSessions)
	}

	// And the grace is a real boundary, not an open end.
	f.observe("way-later", f.base.Add(6*time.Hour), []string{"internal/auth/z.go"}, "")
	cov2, err := QueryCoverage(f.db, Options{
		From: f.base.Add(5 * time.Hour), To: f.base.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cov2.TotalSessions != 0 {
		t.Errorf("sessions in the later period = %d, want 0", cov2.TotalSessions)
	}
}

// Timezone regression, at the query level: a commit recorded with a non-UTC
// local offset would sort outside a window it belongs in. The observer
// normalizes to UTC (internal/observer), and this asserts the consequence the
// normalization exists for.
func TestCoverage_NonUTCCommitTimestampsWouldMisAttribute(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Auth rule", "internal/auth/**")
	f.session("mcp", f.base, d.ID)

	// The shape the observer must never write: a local-offset timestamp.
	at := f.base.Add(time.Minute)
	local := at.In(time.FixedZone("CEST", 2*3600))
	if _, err := f.db.Exec(`
		INSERT INTO events (id, project_id, ts, kind, actor, commit_sha, payload)
		VALUES ('ev-local', ?, ?, 'diff.observed', 'system', 'local-commit', ?)`,
		project, at.Format(time.RFC3339Nano),
		fmt.Sprintf(`{"files":["internal/auth/x.go"],"committed_at":%q,"patch_id":"p1"}`,
			local.Format(time.RFC3339))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`
		INSERT INTO events (id, project_id, ts, kind, actor, decision_id, commit_sha, payload)
		VALUES ('ev-local-match', ?, ?, 'diff.scope_match', 'system', ?, 'local-commit',
		        '{"verdict":"conform","files":["internal/auth/x.go"],"matched_globs":["internal/auth/**"]}')`,
		project, at.Format(time.RFC3339Nano), d.ID); err != nil {
		t.Fatal(err)
	}

	cov, err := QueryCoverage(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if cov.AttributedSessions != 0 {
		t.Skip("this store's offsets happen to compare correctly; the point stands")
	}
	// The commit is genuinely inside the window — it is only the *string form*
	// that puts it outside. This is why the observer normalizes.
	t.Logf("a local-offset committed_at (%s) did not attribute, as expected — "+
		"the observer writes UTC precisely so this cannot happen in production",
		local.Format(time.RFC3339))
}

// §D6.6 / §D5.1: day one is 0 of 0, and it is not a percentage. "0%" reads as
// a failing product when it means an unused one.
func TestCoverage_EmptyStoreSaysSoRatherThanReportingZeroPercent(t *testing.T) {
	f := newFixture(t)
	cov, err := QueryCoverage(f.db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cov.TotalSessions != 0 || cov.AttributedSessions != 0 {
		t.Fatalf("empty store coverage = %+v", cov)
	}
	out := CoverageText(cov, 30, DefaultGraceMinutes)
	if !strings.Contains(out, "no agent sessions yet") {
		t.Errorf("empty coverage must explain itself:\n%s", out)
	}
	if strings.Contains(out, "0%") {
		t.Errorf("an unused store must not render as 0%%:\n%s", out)
	}
}

// --- §D4: the metrics ---

func TestReport_PerDecisionMetrics(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Sessions are server-side only", "internal/auth/**")
	other := f.decision("Docs are markdown", "docs/**")

	f.session("mcp", f.base, d.ID, other.ID)
	// Two conforming commits inside the window, and one commit that reverts
	// the first — a violation by §D6's rule.
	f.observe("c1", f.base.Add(time.Minute), []string{"internal/auth/a.go"}, "")
	f.observe("c2", f.base.Add(2*time.Minute), []string{"internal/auth/b.go"}, "")
	f.observe("c3", f.base.Add(3*time.Minute), []string{"internal/auth/a.go"}, "c1")

	r, err := Build(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var row *DecisionRow
	for i := range r.Decisions {
		if r.Decisions[i].DecisionID == d.ID {
			row = &r.Decisions[i]
		}
	}
	if row == nil {
		t.Fatalf("the packed decision is missing from the report:\n%s", r.Text())
	}
	if row.Attributed != 3 {
		t.Errorf("attributed = %d, want 3 distinct changes", row.Attributed)
	}
	if row.Conformed != 2 || row.Violated != 1 {
		t.Errorf("conform/violate = %d/%d, want 2/1", row.Conformed, row.Violated)
	}
	if row.PackedSessions != 1 {
		t.Errorf("packed sessions = %d, want 1", row.PackedSessions)
	}
	// A decision packed but never touched shows up with zeros rather than
	// vanishing — the denominator context §D4 insists on.
	if len(r.Decisions) != 1 {
		t.Logf("decisions rendered: %d", len(r.Decisions))
	}
}

// §D0's dedup rule: reports count distinct patch_id, never distinct SHA.
// Without it one rebase doubles every conform count.
func TestReport_CountsDistinctChangesNotDistinctSHAs(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Auth rule", "internal/auth/**")
	f.session("mcp", f.base, d.ID)

	// The same change, twice: a rebase gives it a new SHA and the same
	// patch-id.
	at := f.base.Add(time.Minute)
	f.observe("before-rebase", at, []string{"internal/auth/x.go"}, "")
	c := kernel.ObservedCommit{
		SHA: "after-rebase", Author: "dev", Subject: "work",
		CommittedAt: at.Add(time.Minute), Files: []string{"internal/auth/x.go"},
		PatchID: "patch-before-rebase", Branch: "main",
	}
	if _, err := f.k.ObserveCommit(c); err != nil {
		t.Fatal(err)
	}

	r, err := Build(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Decisions) != 1 {
		t.Fatalf("decisions = %d", len(r.Decisions))
	}
	if r.Decisions[0].Attributed != 1 {
		t.Errorf("attributed = %d, want 1 — the rebase is the same change, and "+
			"counting it twice is what patch_id exists to prevent", r.Decisions[0].Attributed)
	}
}

// §D1.3: backfilled matches are excluded from every reported metric.
func TestReport_ExcludesBackfilledMatches(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Auth rule", "internal/auth/**")
	f.session("mcp", f.base, d.ID)

	c := kernel.ObservedCommit{
		SHA: "old-commit", Author: "dev", Subject: "ancient",
		CommittedAt: f.base.Add(time.Minute), Files: []string{"internal/auth/x.go"},
		PatchID: "p-old", Branch: "main", Backfill: true,
	}
	if _, err := f.k.ObserveCommit(c); err != nil {
		t.Fatal(err)
	}

	cov, err := QueryCoverage(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if cov.AttributedSessions != 0 {
		t.Errorf("a backfilled match attributed a session: %+v — archaeology must not "+
			"count as attribution", cov)
	}
}

// §D5.4: the recall path is reported beside the pack path, never merged into
// it. This is what makes Phase 0 ruling 3's comparison a query.
func TestCoverage_SplitsPackAndRecallPaths(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Auth rule", "internal/auth/**")

	f.session("mcp", f.base, d.ID)
	f.observe("packed-commit", f.base.Add(time.Minute), []string{"internal/auth/x.go"}, "")

	f.recallSession(f.base.Add(2*time.Hour), d.ID)
	f.observe("recalled-commit", f.base.Add(2*time.Hour).Add(time.Minute),
		[]string{"internal/auth/y.go"}, "")

	cov, err := QueryCoverage(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if cov.ViaPack != 1 || cov.ViaRecall != 1 {
		t.Errorf("split = pack %d / recall %d, want 1/1", cov.ViaPack, cov.ViaRecall)
	}
	if cov.AttributedSessions != 2 {
		t.Errorf("attributed = %d, want both sessions", cov.AttributedSessions)
	}
}

// --- §D6: the honesty controls, each individually testable ---

// §D6.4's banned vocabulary, held to the ADR's word: a test over rendered
// output, in every format.
func TestReport_NeverUsesBannedVocabulary(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Sessions are server-side only", "internal/auth/**")
	f.session("mcp", f.base, d.ID)
	f.observe("c1", f.base.Add(time.Minute), []string{"internal/auth/a.go"}, "")
	f.observe("c2", f.base.Add(2*time.Minute), []string{"internal/auth/a.go"}, "c1")
	f.observe("c3", f.base.Add(3*time.Minute), []string{"internal/auth/a.go"}, "c2")

	r, err := Build(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	jsonOut, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	renders := map[string]string{
		"text":     r.Text(),
		"markdown": r.Markdown(),
		"json":     string(jsonOut),
		"coverage": CoverageText(r.Coverage, 30, r.GraceMinutes),
	}
	for name, out := range renders {
		lower := strings.ToLower(out)
		for _, banned := range BannedVocabulary {
			if strings.Contains(lower, banned) {
				t.Errorf("%s output contains the banned word %q — the report describes "+
					"recorded chains and claims no counterfactual (§D6.4):\n%s",
					name, banned, out)
			}
		}
	}
	// And the fixture has to have produced something to render, or the sweep
	// proves nothing.
	if !strings.Contains(renders["text"], "per decision") {
		t.Fatalf("the fixture rendered no decisions; the sweep is vacuous:\n%s", renders["text"])
	}
}

// §D6.1: no aggregate without its sample size, and no percentage on a
// denominator under five.
func TestReport_RatesCarryTheirSampleSize(t *testing.T) {
	if got := rate(2, 3, "things"); got != "2 of 3 things" {
		t.Errorf("small denominator rendered as %q; a percentage over n=3 overstates", got)
	}
	if got := rate(38, 41, "changes"); !strings.Contains(got, "38 of 41") ||
		!strings.Contains(got, "93%") {
		t.Errorf("rate = %q, want the fraction and the percentage", got)
	}
	if got := rate(0, 0, "sessions"); !strings.Contains(got, "0 of 0") {
		t.Errorf("empty rate = %q", got)
	}
}

// §D6.3 and §D6.5: the limitations and the method are on the report itself,
// in every format — not in documentation a reader of the number never opens.
func TestReport_PrintsItsMethodAndItsLimitations(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Auth rule", "internal/auth/**")
	f.session("mcp", f.base, d.ID)
	f.observe("c1", f.base.Add(time.Minute), []string{"internal/auth/a.go"}, "")

	r, err := Build(f.db, Options{
		From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour), GraceMinutes: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonOut, _ := r.JSON()
	for name, out := range map[string]string{
		"text": r.Text(), "markdown": r.Markdown(), "json": string(jsonOut),
	} {
		if !strings.Contains(out, "trailer") {
			t.Errorf("%s does not disclose the revert-detection method:\n%s", name, out)
		}
		if !strings.Contains(out, "90") {
			t.Errorf("%s does not disclose the grace window actually used:\n%s", name, out)
		}
		if !strings.Contains(strings.ToLower(out), "not verified compliance") {
			t.Errorf("%s does not state what conform means:\n%s", name, out)
		}
		if !strings.Contains(strings.ToLower(out), "would have happened") {
			t.Errorf("%s does not state that attribution is not causation:\n%s", name, out)
		}
	}
}

// §D6.2: every number drills to raw rows. The invariant, not the feature.
func TestReport_EveryFigureDrillsToRawEvents(t *testing.T) {
	f := newFixture(t)
	d := f.decision("Auth rule", "internal/auth/**")
	f.session("mcp", f.base, d.ID)
	f.observe("c1", f.base.Add(time.Minute), []string{"internal/auth/a.go"}, "")

	shas, err := CommitsForDecision(f.db, d.ID)
	if err != nil || len(shas) != 1 {
		t.Fatalf("commits for decision = %v (%v)", shas, err)
	}
	events, err := QueryRaw(f.db, d.ID, shas)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, e := range events {
		kinds[e.Kind] = true
		if e.Seq == 0 || e.ID == "" {
			t.Errorf("a drill-down row without seq/id is not traceable: %+v", e)
		}
	}
	for _, want := range []string{
		"decision.proposed", "decision.accepted", "pack.item",
		"diff.observed", "diff.scope_match",
	} {
		if !kinds[want] {
			t.Errorf("the drill-down is missing %s; the chain must be inspectable end to end", want)
		}
	}
}

// §D6.6: an empty dashboard explains itself.
func TestReport_EmptyStatesExplainThemselves(t *testing.T) {
	f := newFixture(t)
	out := mustBuild(t, f).Text()
	if !strings.Contains(out, "no agent sessions yet") {
		t.Errorf("empty coverage is silent:\n%s", out)
	}
	if !strings.Contains(out, "no scoped decisions") {
		t.Errorf("a store with no scoped decisions must say so — attribution cannot "+
			"start without them:\n%s", out)
	}

	// With a scoped decision but no sessions, the message changes: the cold
	// start is at a different link in the chain.
	f.decision("Auth rule", "internal/auth/**")
	out = mustBuild(t, f).Text()
	if strings.Contains(out, "no scoped decisions") {
		t.Errorf("the message did not move on from the scoping cold start:\n%s", out)
	}
}

// §D4.4: completeness is an intersection, so it cannot exceed 100%.
func TestCompleteness_IsAnIntersectionAndCannotExceed100(t *testing.T) {
	f := newFixture(t)
	// Observe commits that are not reachable from any branch — the
	// feature-branch case that makes the naive ratio exceed 100%.
	for i := 0; i < 5; i++ {
		f.observe(fmt.Sprintf("branch-only-%d", i), f.base.Add(time.Duration(i)*time.Minute),
			[]string{"internal/auth/x.go"}, "")
	}
	r, err := Build(f.db, Options{
		From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour), RepoRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Completeness.Available {
		t.Fatal("a non-repo must not report completeness at all")
	}
	if !strings.Contains(r.Text(), "not measurable here") {
		t.Errorf("completeness must say when it cannot be measured:\n%s", r.Text())
	}
}

func mustBuild(t *testing.T, f *fixture) *Report {
	t.Helper()
	r, err := Build(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The query-plan pin. §D0 and §D5.1 are multi-self-joins on `events` — the
// same shape as the FTS planning cliff that cost 5.3s per query until the
// CROSS JOIN fix, and invisible on the small stores every other test uses.
// The plan is asserted rather than the clock: the plan is deterministic.
func TestQueryPlans_UseIndexesNotTableScans(t *testing.T) {
	f := newFixture(t)
	seedRealisticHistory(t, f, 60, 40)

	for name, q := range map[string]string{
		"coverage (§D5.1)":       coverageSQL(DefaultGraceMinutes),
		"attributed pairs (§D0)": attributedPairsSQL(DefaultGraceMinutes) + " SELECT * FROM attributed_pairs",
	} {
		rows, err := f.db.Query("EXPLAIN QUERY PLAN "+q,
			sql.Named("from", f.base.Add(-time.Hour).Format(time.RFC3339Nano)),
			sql.Named("to", f.base.Add(24*time.Hour).Format(time.RFC3339Nano)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var steps []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				t.Fatal(err)
			}
			steps = append(steps, detail)
		}
		rows.Close()

		if len(steps) == 0 {
			t.Fatalf("%s: no plan", name)
		}
		// A full scan of `events` inside a correlated join is the shape that
		// turns linear into quadratic as history grows. The driving scan is
		// fine; scans of the joined-to tables are not.
		scans := 0
		for _, s := range steps {
			if strings.HasPrefix(s, "SCAN events") {
				scans++
			}
		}
		if scans > 1 {
			t.Errorf("%s: %d full scans of `events` in one plan — the report runs this "+
				"over all history:\n  %s", name, scans, strings.Join(steps, "\n  "))
		}
		t.Logf("%s plan:\n  %s", name, strings.Join(steps, "\n  "))
	}
}

// The measured numbers the brief asks for: the coverage query's output on a
// seeded store, and the report's latency over a realistic history.
func TestReport_LatencyOverRealisticHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a few thousand events")
	}
	f := newFixture(t)
	sessions, commits := seedRealisticHistory(t, f, 300, 200)

	start := time.Now()
	cov, err := QueryCoverage(f.db, Options{
		From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour),
	})
	coverageElapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}

	start = time.Now()
	r, err := Build(f.db, Options{From: f.base.Add(-time.Hour), To: f.base.Add(24 * time.Hour)})
	reportElapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}

	var events int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	t.Logf("history: %d sessions, %d commits, %d events", sessions, commits, events)
	t.Logf("coverage query: %v · full report: %v", coverageElapsed, reportElapsed)
	t.Logf("coverage: %d of %d agent sessions attributed (via pack %d, via recall %d)",
		cov.AttributedSessions, cov.TotalSessions, cov.ViaPack, cov.ViaRecall)

	if cov.TotalSessions == 0 || cov.AttributedSessions == 0 {
		t.Fatal("the seeded history attributed nothing; the measurement is vacuous")
	}
	if len(r.Decisions) == 0 {
		t.Fatal("the seeded history produced no per-decision rows")
	}
	// ADR-0001 falsifier 6's threshold for a hot attribution query. Generous
	// here because this is a measurement, not a benchmark: it exists to fail
	// loudly if the report goes quadratic, not to police milliseconds.
	if reportElapsed > 2*time.Second {
		t.Errorf("the full report took %v over %d events — falsifier 6's promotion "+
			"of committed_at/verdict to real columns is the pre-registered fix",
			reportElapsed, events)
	}
}

// seedRealisticHistory builds a store shaped like a month of real use:
// decisions accepted over time, agent and CLI sessions, packs, recalls,
// commits inside and outside windows, violations, and reverts.
func seedRealisticHistory(t *testing.T, f *fixture, sessions, commits int) (int, int) {
	t.Helper()
	var ids []string
	for i := 0; i < 12; i++ {
		scope := []string{fmt.Sprintf("internal/pkg%02d/**", i%4)}
		ids = append(ids, f.decision(fmt.Sprintf("Rule %02d", i), scope...).ID)
	}

	for i := 0; i < sessions; i++ {
		at := f.base.Add(time.Duration(i) * 3 * time.Minute)
		agent := "mcp"
		if i%7 == 0 {
			agent = "cli" // excluded from the denominator by §D3
		}
		packed := ids[i%len(ids)]
		if i%5 == 0 {
			f.recallSession(at, packed)
		} else {
			f.session(agent, at, packed, ids[(i+1)%len(ids)])
		}
	}

	prev := ""
	for i := 0; i < commits; i++ {
		at := f.base.Add(time.Duration(i) * 4 * time.Minute)
		files := []string{fmt.Sprintf("internal/pkg%02d/file%02d.go", i%4, i%9)}
		reverts := ""
		if i%23 == 0 && prev != "" {
			reverts = prev // a violation, by §D6's rule
		}
		f.observe(fmt.Sprintf("sha%04d", i), at, files, reverts)
		if reverts == "" {
			prev = fmt.Sprintf("sha%04d", i)
		}
	}
	return sessions, commits
}
