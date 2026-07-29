package lint

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/kernel"
)

const project = "p"

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// fixture builds the §D3/§D4 known-answer corpus.
//
// It is the artifact ADR-0005 §D4 requires before the score may be rendered
// anywhere: a committed corpus whose per-category rates, renormalization and
// final banded score are hand-computed in the assertions below. The score is
// the number strategy Bet 3 puts in front of strangers, so "the formula is in
// the ADR" is not an implementation.
//
// Deliberate contents, each reaching a specific check:
//
//	d1  active,   expired, dead commit evidence, scope matching no files
//	d2  active,   no evidence
//	d3  proposed, kind=decision, scope=[]            (hygiene)
//	d4  active,   kind=decision, scope=["**"]        (hygiene)
//	d5  proposed, convention   ┐ identical title+body (duplicates)
//	d6  proposed, convention   ┘
//	d7  active,   scope internal/api/*.go ┐ shared glob (contradiction candidates)
//	d8  active,   scope internal/api/*.go ┘ + a missing file evidence ref
//	d9  PURGED tombstone (terminal) — must be invisible to every check
//	d10 active,   200 days untouched, scope matching a file newer than it
//	n1,n2 identical notes (duplicates) · n3 200 days old (staleness) · n4,n5 fresh
func fixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	for _, f := range []string{"internal/api/handler.go", "internal/lint/lint.go", "README.md"} {
		path := filepath.Join(root, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A real repo with a real commit: L3's commit tier is N/A without one
	// (Amendment 1), so a fixture that never commits would silently stop
	// measuring the dead-reference category it claims to measure.
	gitInit(t, root)

	k := kernel.New(filepath.Join(t.TempDir(), "lint.db"), project)
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	db := k.Decisions().DB()

	now := time.Now().UTC()
	fresh := ts(now)
	old := ts(now.AddDate(0, 0, -200))
	expired := ts(now.AddDate(0, 0, -3))

	dec := func(id, kind, title, body, status, scope, touched string, expires *string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO decisions
			(id, project_id, kind, title, body, status, scope, expires_at,
			 created_at, updated_at, decided_at, status_changed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, project, kind, title, body, status, scope, expires,
			touched, touched, touched, touched)
		if err != nil {
			t.Fatal(err)
		}
	}
	ev := func(id, decisionID, kind, ref string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO evidence (id, decision_id, kind, ref, added_by, created_at)
			VALUES (?,?,?,?, 'human', ?)`, id, decisionID, kind, ref, fresh); err != nil {
			t.Fatal(err)
		}
	}
	note := func(id, content, updated string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO notes (id, project_id, content, status, created_at, updated_at)
			VALUES (?,?,?, 'active', ?, ?)`, id, project, content, updated, updated); err != nil {
			t.Fatal(err)
		}
	}

	dec("d1", "decision", "Use Postgres", "It scales", "active", `["internal/db/*.go"]`, fresh, &expired)
	dec("d2", "decision", "Deploy on Fridays never", "", "active", `["deploy/*.sh"]`, fresh, nil)
	dec("d3", "decision", "Prefer early returns", "", "proposed", `[]`, fresh, nil)
	dec("d4", "decision", "All code is reviewed", "", "active", `["**"]`, fresh, nil)
	dec("d5", "convention", "Use pnpm", "npm lockfiles drift", "proposed", `[]`, fresh, nil)
	dec("d6", "convention", "Use pnpm", "npm lockfiles drift", "proposed", `[]`, fresh, nil)
	dec("d7", "decision", "Handlers return errors", "no panics", "active", `["internal/api/*.go"]`, fresh, nil)
	dec("d8", "decision", "Handlers are thin", "logic lives in services", "active", `["internal/api/*.go"]`, fresh, nil)
	dec("d9", "decision", "[purged]", "", "reverted", `["**"]`, fresh, &expired)
	dec("d10", "decision", "Lint runs in CI", "", "active", `["internal/lint/*.go"]`, old, nil)

	ev("e1", "d1", "commit", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	ev("e2", "d7", "file", "internal/api/handler.go")
	ev("e3", "d8", "file", "gone/missing.go")
	// The purged row keeps evidence and an expiry that would otherwise trip L1
	// and L3; it is terminal, so no check may see it.
	ev("e4", "d9", "commit", "cafebabecafebabecafebabecafebabecafebabe")

	note("n1", "Always run gofmt before committing", fresh)
	note("n2", "Always  run gofmt   before committing", fresh)
	note("n3", "The old build script lived in tools/", old)
	note("n4", "Sessions are keyed by ULID", fresh)
	note("n5", "The observer uses a post-commit hook", fresh)

	return db, root
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "fixture@example.com"},
		{"config", "user.name", "fixture"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func fixtureOptions(root string) Options {
	return Options{
		ProjectID: project,
		RepoRoot:  root,
		Now:       time.Now().UTC(),
		// Only the fixture's live evidence commit resolves; d1's is GC'd.
		CommitExists: func(sha string) bool { return false },
	}
}

func mustCheck(t *testing.T, res *Result, id string) *Check {
	t.Helper()
	c := res.Check(id)
	if c == nil {
		t.Fatalf("%s did not run", id)
	}
	return c
}

func assertCheck(t *testing.T, res *Result, id string, wantFindings, wantChecked int, wantIDs ...string) {
	t.Helper()
	c := mustCheck(t, res, id)
	if len(c.Findings) != wantFindings || c.Checked != wantChecked {
		t.Errorf("%s = %d findings of %d checked, want %d of %d (%+v)",
			id, len(c.Findings), c.Checked, wantFindings, wantChecked, c.Findings)
		return
	}
	got := map[string]bool{}
	for _, f := range c.Findings {
		got[f.ID] = true
	}
	for _, want := range wantIDs {
		if !got[want] {
			t.Errorf("%s did not flag %s (flagged %v)", id, want, got)
		}
	}
	if got["d9"] {
		t.Errorf("%s flagged the purged tombstone d9 — terminal rows are invisible to every check", id)
	}
}

func TestLint_FixtureCorpus_KnownAnswers(t *testing.T) {
	db, root := fixture(t)
	res, err := Run(db, fixtureOptions(root))
	if err != nil {
		t.Fatal(err)
	}

	if res.Entries != 14 {
		t.Fatalf("entries = %d, want 14 (9 non-terminal decisions + 5 notes)", res.Entries)
	}
	// L1: 1 of 6 active/violated decisions is past its expiry (d9 is terminal).
	assertCheck(t, res, "L1", 1, 6, "d1")
	// L2: d2, d4, d10 are binding with no evidence row.
	assertCheck(t, res, "L2", 3, 6, "d2", "d4", "d10")
	// L3: 3 refs checked on non-terminal rows; d1's commit and d8's file are dead.
	assertCheck(t, res, "L3", 2, 3, "d1", "d8")
	// L4: 6 scoped non-terminal rows; internal/db/*.go and deploy/*.sh match
	// nothing in the tree. Advisory, never scored — a zero-match glob may be
	// deliberate (ADR-0001 D4 allows scoping files that do not exist yet).
	assertCheck(t, res, "L4", 2, 6, "d1", "d2")
	// L5: 14 entries scanned; two exact groups of two — every member is a finding.
	assertCheck(t, res, "L5", 4, 14, "d5", "d6", "n1", "n2")
	// L6: d7 and d8 share internal/api/*.go. d5/d6 share a title *and* a body,
	// so they are duplicates (L5) and must not be double-counted here.
	assertCheck(t, res, "L6", 2, 9, "d7", "d8")
	// L7: 14 entries; d10 (200d, all scoped files newer) and n3 (200d).
	assertCheck(t, res, "L7", 2, 14, "d10", "n3")
	// L10: 7 decision-kind non-terminal rows; d3 unscoped, d4 repo-wide.
	// d5/d6 are conventions with scope=[] — the sanctioned form, never flagged.
	assertCheck(t, res, "L10", 2, 7, "d3", "d4")

	// L8/L9 are gated: this store has never packed anything.
	for _, id := range []string{"L8", "L9"} {
		if c := mustCheck(t, res, id); !c.NA {
			t.Errorf("%s should be N/A on a store with no packing history, got %+v", id, c.Findings)
		}
	}
	if len(res.GatedOut) != 2 {
		t.Errorf("gated_out = %v, want L8 and L9", res.GatedOut)
	}
	if len(res.Corrupt) != 0 {
		t.Errorf("fixture is not corrupt, got %+v", res.Corrupt)
	}
}

// The score, hand-computed to the integer. If this test and the implementation
// disagree, §D4 says both are wrong until they agree.
//
//	dead refs      2 of 3   rate .666667 × .25 = .1666667
//	duplicates     4 of 14  rate .285714 × .25 = .0714286
//	contradictions 2 of 9   rate .222222 × .20 = .0444444
//	staleness      2 of 14  rate .142857 × .20 = .0285714
//	hygiene        2 of 7   rate .285714 × .10 = .0285714
//	                             Σ penalty     = .3396825
//	score = round(100 × (1 − .3396825)) = round(66.03) = 66 → significant rot
func TestScore_FixtureCorpus_HandComputed(t *testing.T) {
	db, root := fixture(t)
	res, err := Run(db, fixtureOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	s := res.Score
	if s.Suppressed {
		t.Fatalf("14 entries is above the n=%d floor; score should not be suppressed", MinScorableEntries)
	}
	if s.Value != 66 || s.Band != "significant rot" {
		t.Fatalf("score = %d (%s), want 66 (significant rot)", s.Value, s.Band)
	}
	want := map[string][2]int{
		"dead_refs": {2, 3}, "duplicates": {4, 14}, "contradictions": {2, 9},
		"staleness": {2, 14}, "hygiene": {2, 7},
	}
	for _, c := range s.Categories {
		w, ok := want[c.Key]
		if !ok {
			t.Fatalf("unexpected category %s", c.Key)
		}
		if c.NA || c.Affected != w[0] || c.Denominator != w[1] {
			t.Errorf("%s = %d of %d (na=%v), want %d of %d",
				c.Key, c.Affected, c.Denominator, c.NA, w[0], w[1])
		}
	}
	// Every deduction is arithmetic over the findings list.
	sum := 0.0
	for _, c := range s.Categories {
		sum += c.Deduction
	}
	if got := 100 - sum; got < 66.0 || got > 66.1 {
		t.Errorf("deductions sum to %.4f, which does not reconstruct the score", sum)
	}
}

// Renormalization with an N/A category: with no repo present, dead references
// cannot be checked, and the remaining weights scale to sum 1 rather than the
// store being silently credited for a check that never ran.
//
//	applicable weights: .25 + .20 + .20 + .10 = .75
//	duplicates     4 of 14  .285714 × (.25/.75 = .333333) = .0952381
//	contradictions 2 of 9   .222222 × (.20/.75 = .266667) = .0592593
//	staleness      1 of 14  .071429 × .266667              = .0190476
//	  (only n3: without a working tree, L7's scoped-files clause cannot be
//	   satisfied for d10, so it is not counted stale)
//	hygiene        2 of 7   .285714 × (.10/.75 = .133333) = .0380952
//	                              Σ penalty              = .2116402
//	score = round(100 × .7883598) = 79 → needs attention
func TestScore_RenormalizesAroundNACategory(t *testing.T) {
	db, _ := fixture(t)
	res, err := Run(db, Options{ProjectID: project, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if c := mustCheck(t, res, "L3"); !c.NA {
		t.Fatalf("dead references should be N/A with no repo present, got %+v", c)
	}
	if res.Score.Value != 79 || res.Score.Band != "needs attention" {
		t.Fatalf("score = %d (%s), want 79 (needs attention)", res.Score.Value, res.Score.Band)
	}
	for _, c := range res.Score.Categories {
		if c.Key == "dead_refs" && (!c.NA || c.Deduction != 0) {
			t.Fatalf("dead_refs contributed %v while N/A", c.Deduction)
		}
	}
}

// §D4's n<10 rule: a percentage over four rows is noise theater.
func TestScore_SuppressedBelowTenEntries(t *testing.T) {
	k := kernel.New(filepath.Join(t.TempDir(), "small.db"), project)
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	db := k.Decisions().DB()
	now := ts(time.Now().UTC())
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		if _, err := db.Exec(`INSERT INTO decisions
			(id, project_id, kind, title, body, status, scope, created_at, updated_at, decided_at, status_changed_at)
			VALUES (?,?, 'decision', 'Same rule', 'same body', 'active', '[]', ?, ?, ?, ?)`,
			id, project, now, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Run(db, Options{ProjectID: project, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries != 4 {
		t.Fatalf("entries = %d", res.Entries)
	}
	// Four identical rows: every scorable rate is at its maximum, so an
	// unsuppressed score here would be a spectacular zero on four rows.
	if !res.Score.Suppressed || res.Score.Value != 0 {
		t.Fatalf("score = %+v, want suppression below n=%d", res.Score, MinScorableEntries)
	}
	if res.Score.Reason != "corpus too small to score" {
		t.Fatalf("reason = %q", res.Score.Reason)
	}
	// Findings still print — the artifact is the itemized list, not the number.
	if c := mustCheck(t, res, "L5"); len(c.Findings) != 4 {
		t.Fatalf("suppressing the score must not suppress findings: %+v", c)
	}
}

func TestL1_EmitsExpiredOnceIdempotently(t *testing.T) {
	db, root := fixture(t)
	opts := fixtureOptions(root)
	var marked []string
	opts.MarkExpired = func(id string) (bool, error) {
		marked = append(marked, id)
		return true, nil
	}
	if _, err := Run(db, opts); err != nil {
		t.Fatal(err)
	}
	if len(marked) != 1 || marked[0] != "d1" {
		t.Fatalf("MarkExpired called for %v, want [d1] — never for the purged row", marked)
	}
}

// L9's corrected semantics (§D3, rewritten 2026-07-29). The two cases the
// first draft got wrong are both here.
func TestL9_EpisodeArithmetic(t *testing.T) {
	k := kernel.New(filepath.Join(t.TempDir(), "l9.db"), project)
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	db := k.Decisions().DB()
	now := ts(time.Now().UTC())

	mkDecision := func(id, status string) {
		if _, err := db.Exec(`INSERT INTO decisions
			(id, project_id, kind, title, body, status, scope, created_at, updated_at, decided_at, status_changed_at)
			VALUES (?,?, 'decision', ?, '', ?, '["**"]', ?,?,?,?)`,
			id, project, "rule "+id, status, now, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	seq := 0
	mkEventAt := func(id, at, kind, decisionID, sha, payload string) {
		seq++
		if _, err := db.Exec(`INSERT INTO events (id, project_id, ts, kind, actor, decision_id, commit_sha, payload)
			VALUES (?,?,?,?, 'system', ?, ?, ?)`,
			id, project, at, kind, nullable(decisionID), nullable(sha), payload); err != nil {
			t.Fatal(err)
		}
	}
	mkEvent := func(id, kind, decisionID, sha, payload string) {
		mkEventAt(id, now, kind, decisionID, sha, payload)
	}

	// v1: three episodes, one dismissed. Still a rule worth re-stating, with
	// unresolved = 2 — a single dismissal must not exempt it forever.
	mkDecision("v1", "active")
	mkEvent("ev1", "decision.violated", "v1", "sha1", `{}`)
	mkEvent("ev2", "decision.violated", "v1", "sha2", `{}`)
	mkEvent("ev3", "decision.violated", "v1", "sha3", `{}`)
	mkEvent("dis1", "decision.violation_dismissed", "v1", "", `{"violation_event_id":"ev1"}`)

	// v2: three episodes, one dismissed and one counter-reverted ⇒ unresolved 1.
	mkDecision("v2", "violated")
	mkEvent("ev4", "decision.violated", "v2", "sha4", `{}`)
	mkEvent("ev5", "decision.violated", "v2", "sha5", `{}`)
	mkEvent("ev6", "decision.violated", "v2", "sha6", `{}`)
	mkEvent("dis2", "decision.violation_dismissed", "v2", "", `{"violation_event_id":"ev4"}`)
	mkEvent("rev1", "revert.detected", "", "", `{"reverts_sha":"sha5"}`)

	// v3: three episodes but the decision was reverted — terminal rows are not
	// "rules the fleet keeps breaking".
	mkDecision("v3", "reverted")
	mkEvent("ev7", "decision.violated", "v3", "sha7", `{}`)
	mkEvent("ev8", "decision.violated", "v3", "sha8", `{}`)
	mkEvent("ev9", "decision.violated", "v3", "sha9", `{}`)

	// v4: two episodes — below the threshold.
	mkDecision("v4", "active")
	mkEvent("ev10", "decision.violated", "v4", "sha10", `{}`)
	mkEvent("ev11", "decision.violated", "v4", "sha11", `{}`)

	// Open the gate: a pack.served event older than 30 days. It is written with
	// its own timestamp because the event log is append-only — an UPDATE here
	// would trip the trigger, which is the schema working.
	mkEventAt("pk1", ts(time.Now().UTC().AddDate(0, 0, -40)), "pack.served", "", "", `{}`)

	res, err := Run(db, Options{ProjectID: project, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	c := mustCheck(t, res, "L9")
	if c.NA {
		t.Fatalf("L9 should run once packing history is older than 30 days: %s", c.NAReason)
	}
	if len(c.Findings) != 2 {
		t.Fatalf("L9 findings = %+v, want v1 and v2", c.Findings)
	}
	if c.Findings[0].ID != "v1" || c.Findings[0].Detail != "3 violation episodes, 2 unresolved" {
		t.Errorf("v1 = %+v, want 3 episodes / 2 unresolved", c.Findings[0])
	}
	if c.Findings[1].ID != "v2" || c.Findings[1].Detail != "3 violation episodes, 1 unresolved" {
		t.Errorf("v2 = %+v, want 3 episodes / 1 unresolved", c.Findings[1])
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func TestL6_ReportsTopicKeyCorruption(t *testing.T) {
	db, root := fixture(t)
	if _, err := db.Exec(`UPDATE decisions SET topic_key = 'db/engine' WHERE id IN ('d7','d8')`); err != nil {
		// The partial unique index should make this impossible; if the schema
		// ever stops enforcing it, the check below is the safety net.
		t.Skipf("schema refused the corrupt state (which is the point): %v", err)
	}
	res, err := Run(db, fixtureOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Corrupt) != 1 {
		t.Fatalf("corrupt = %+v, want one topic_key collision reported as corruption", res.Corrupt)
	}
}

func TestQueryBacklog_DisposalRequestsListedSeparately(t *testing.T) {
	db, root := fixture(t)
	old := ts(time.Now().UTC().AddDate(0, 0, -30))
	if _, err := db.Exec(`UPDATE decisions SET status_changed_at = ? WHERE id = 'd3'`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (id, project_id, ts, kind, actor, decision_id, payload)
		VALUES ('dr1', ?, ?, 'decision.disposal_requested', 'agent', 'd5', '{}')`,
		project, ts(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	b, err := QueryBacklog(db, fixtureOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if b.Total != 3 {
		t.Fatalf("backlog total = %d, want 3 proposed rows", b.Total)
	}
	if len(b.DisposalRequested) != 1 || b.DisposalRequested[0].ID != "d5" {
		t.Fatalf("disposal group = %+v, want d5", b.DisposalRequested)
	}
	// d5 is aging too, but it belongs to the higher-signal group only.
	if len(b.Aging) != 1 || b.Aging[0].ID != "d3" {
		t.Fatalf("aging group = %+v, want d3 only", b.Aging)
	}
}

// Amendment 1: a repo with no commits leaves the commit tier N/A and says so on
// the method line, instead of reporting every commit ref as unreachable.
func TestL3_NoCommitsIsNotAFindingOfDeadCommits(t *testing.T) {
	db, _ := fixture(t)
	empty := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = empty
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	res, err := Run(db, Options{ProjectID: project, RepoRoot: empty, Now: time.Now().UTC(),
		CommitExists: GitCommitExists(empty)})
	if err != nil {
		t.Fatal(err)
	}
	c := mustCheck(t, res, "L3")
	for _, f := range c.Findings {
		if strings.Contains(f.Detail, "unreachable commit") {
			t.Fatalf("a repo with no commits produced a dead-commit finding: %+v", f)
		}
	}
	if res.Modes["dead_refs"] == "" {
		t.Fatal("the commit tier was skipped without disclosing it on the method line")
	}
}
