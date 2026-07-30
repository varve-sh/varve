package lint

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varve-sh/varve/internal/kernel"
)

// The sentinels below are strings that exist nowhere in this codebase and only
// in the fixture store — one per kind of content the aggregate promises not to
// carry. Asserting on sentinels rather than on a list of struct fields is
// deliberate: a content-bearing field added to Category or Adoption later would
// slip past a field-list assertion, and past this one it cannot, because the
// check is over the rendered bytes.
//
// leaked are the sentinels that demonstrably reach `lint --format json` — the
// artifact a pilot would otherwise be asked to send. Their presence there is
// asserted first, so their absence from the aggregate is evidence rather than
// coincidence.
var leaked = map[string]string{
	"decision title":  "ZZTITLEQ7",
	"note content":    "ZZNOTEQ7",
	"scope glob":      "ZZSCOPEQ7",
	"source ref":      "ZZSOURCEQ7",
	"evidence ref":    "ZZEVIDENCEQ7",
	"repo directory":  "ZZREPOQ7",
	"import batch id": "ZZBATCHQ7",
}

// alsoForbidden are content fields that no finding happens to carry today, so
// there is no leak to prove — decision bodies are never rendered, and a
// topic_key reaches output only through the corrupt tier, which a partial
// unique index makes unreachable in a healthy store. They are asserted anyway:
// the promise is about the store's content, not about which fields currently
// have a path to the page, and a future check that starts quoting bodies should
// fail this test rather than ship a summary that quotes them.
var alsoForbidden = map[string]string{
	"decision body": "ZZBODYQ7",
	"topic key":     "ZZTOPICQ7",
}

func allSentinels() map[string]string {
	all := map[string]string{}
	for k, v := range leaked {
		all[k] = v
	}
	for k, v := range alsoForbidden {
		all[k] = v
	}
	return all
}

// sentinelStore builds a corpus where every content field carries a sentinel
// and every scored check has something to find.
func sentinelStore(t *testing.T) (*sql.DB, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), leaked["repo directory"])
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root)

	k := kernel.New(filepath.Join(t.TempDir(), "agg.db"), project)
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	db := k.Decisions().DB()

	now := time.Now().UTC()
	fresh, old := ts(now), ts(now.AddDate(0, 0, -200))
	dec := func(id, title, body, scope, topicKey, touched string) {
		t.Helper()
		var tk any
		if topicKey != "" {
			tk = topicKey
		}
		if _, err := db.Exec(`INSERT INTO decisions
			(id, project_id, kind, title, body, status, scope, topic_key, source, source_ref,
			 created_at, updated_at, decided_at, status_changed_at)
			VALUES (?,?, 'decision', ?,?, 'active', ?,?, 'import', ?,?,?,?,?)`,
			id, project, title, body, scope, tk,
			"CLAUDE.md#"+leaked["source ref"], touched, touched, touched, touched); err != nil {
			t.Fatal(err)
		}
	}
	title, body := leaked["decision title"], alsoForbidden["decision body"]
	glob := `["internal/` + leaked["scope glob"] + `/*.go"]`

	// L5: two identical rows. L6: same title, different body, plus a shared
	// glob pair. L7: one untouched for 200 days. L10: a repo-wide decision.
	dec("a1", title+" one", body, `[]`, alsoForbidden["topic key"]+"-1", fresh)
	dec("a2", title+" one", body, `[]`, alsoForbidden["topic key"]+"-2", fresh)
	dec("a3", title+" two", body+" alpha", glob, "", fresh)
	dec("a4", title+" two", body+" beta", glob, "", fresh)
	dec("a5", title+" old", body, `[]`, "", old)
	dec("a6", title+" wide", body, `["**"]`, "", fresh)
	// L3: a dead file reference.
	if _, err := db.Exec(`INSERT INTO evidence (id, decision_id, kind, ref, added_by, created_at)
		VALUES ('ev1', 'a6', 'file', ?, 'human', ?)`,
		leaked["evidence ref"]+"/gone.go", fresh); err != nil {
		t.Fatal(err)
	}
	// A duplicated note, so note content has a path into the findings: L5 uses
	// the first 60 characters of a note as its title. Without this the note
	// sentinel would be unreachable and its absence would prove nothing.
	for _, id := range []string{"adup1", "adup2"} {
		if _, err := db.Exec(`INSERT INTO notes
			(id, project_id, content, source, source_ref, status, created_at, updated_at)
			VALUES (?,?,?, 'import', ?, 'active', ?, ?)`,
			id, project, leaked["note content"]+" repeated verbatim",
			"claude-mem#"+leaked["source ref"], fresh, fresh); err != nil {
			t.Fatal(err)
		}
	}
	// Padding, so the corpus clears the n=10 floor and the score is real.
	for i := 0; i < 12; i++ {
		if _, err := db.Exec(`INSERT INTO notes
			(id, project_id, content, source, source_ref, status, created_at, updated_at)
			VALUES (?,?,?, 'import', ?, 'active', ?, ?)`,
			"an"+itoa(i), project, leaked["note content"]+" "+itoa(i),
			"claude-mem#"+leaked["source ref"], fresh, fresh); err != nil {
			t.Fatal(err)
		}
	}
	return db, root
}

func sentinelReport(t *testing.T) *Report {
	t.Helper()
	db, root := sentinelStore(t)
	now := time.Now().UTC()
	opts := Options{ProjectID: project, RepoRoot: root, Now: now,
		CommitExists: GitCommitExists(root)}
	res, err := Run(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	backlog, err := QueryBacklog(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	adoption, err := QueryAdoption(db, opts)
	if err != nil {
		t.Fatal(err)
	}
	return &Report{
		GeneratedAt: now, Repo: filepath.Base(root),
		Import: &ImportSummary{Repo: filepath.Base(root),
			Batch:   leaked["import batch id"],
			Sources: map[string]string{"claude-mem": "12 notes"}},
		Lint: res, Backlog: backlog, Adoption: adoption,
	}
}

// The promise the flag makes is negative — "contains no content from your
// store" — so the test has to be negative too, and it has to be able to fail.
// A fixture whose rows were bland would pass this against an aggregate that
// leaked every title.
func TestAggregate_CarriesNoContentFromTheStore(t *testing.T) {
	rep := sentinelReport(t)

	// Vacuity guard: if the fixture stopped producing findings, the sentinels
	// would have nothing to leak through and this test would pass by finding
	// nothing — the failure mode F51 named in the L6 fixtures.
	found := 0
	for _, c := range rep.Lint.Checks {
		found += len(c.Findings) + len(c.Candidates) + len(c.Hubs)
	}
	if found == 0 {
		t.Fatal("the fixture produced no findings, so the leak assertions cannot fail")
	}
	// And the sentinels must actually be reachable: prove they leak through the
	// artifact this flag exists to replace, or "absent from the aggregate" says
	// nothing about the aggregate.
	full, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range leaked {
		if !strings.Contains(full, s) {
			t.Errorf("the %s sentinel does not appear in `lint --format json` either, "+
				"so its absence from the aggregate proves nothing", name)
		}
	}

	got, err := NewAggregate(rep, "v2.0.0", 7).JSON()
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range allSentinels() {
		if strings.Contains(got, s) {
			t.Errorf("the aggregate carries the %s (%q) — it is meant to be safe to "+
				"send, and this is content from a private store:\n%s", name, s, got)
		}
	}

	// The honesty control has to survive the summarizing, or the number travels
	// without its method (§D4).
	if !strings.Contains(got, "calibration pending") {
		t.Errorf("the aggregate drops the method-line disclosure:\n%s", got)
	}
}

// The aggregate must be a view of the report, not a second computation. Two
// implementations of one formula agree until the day they do not, and the day
// they do not is the day a pilot's number stops matching what they were shown.
func TestAggregate_AgreesWithTheReportItSummarizes(t *testing.T) {
	rep := sentinelReport(t)
	a := NewAggregate(rep, "v2.0.0", 7)
	s := rep.Lint.Score

	if s.Suppressed {
		t.Fatalf("fixture fell below the n=%d floor, so the score comparisons are vacuous",
			MinScorableEntries)
	}
	if a.Score != s.Value || a.Band != s.Band {
		t.Errorf("aggregate score = %d/%q, report = %d/%q", a.Score, a.Band, s.Value, s.Band)
	}
	if a.Entries != rep.Lint.Entries {
		t.Errorf("aggregate entries = %d, report = %d", a.Entries, rep.Lint.Entries)
	}
	if a.MethodLine != rep.methodLine() {
		t.Errorf("aggregate method line = %q, report = %q", a.MethodLine, rep.methodLine())
	}
	if a.Adoption != rep.Adoption {
		t.Errorf("aggregate adoption = %+v, report = %+v", a.Adoption, rep.Adoption)
	}
	if len(a.Categories) != len(s.Categories) {
		t.Fatalf("aggregate has %d categories, report has %d", len(a.Categories), len(s.Categories))
	}
	for i, c := range a.Categories {
		want := s.Categories[i]
		if c.Key != want.Key || c.Rate != want.Rate || c.Affected != want.Affected ||
			c.Denominator != want.Denominator || c.Deduction != want.Deduction ||
			c.NA != want.NA {
			t.Errorf("category %s: aggregate %+v, report %+v", c.Key, c, want)
		}
	}
	// L6's unscored tier survives as counts — the evidence Amendment 2's
	// re-entry condition needs, without the globs that describe someone's repo.
	l6 := rep.Lint.Check("L6")
	if a.Unscored.Hubs != len(l6.Hubs) || a.Unscored.CandidatePairs != len(l6.Candidates) {
		t.Errorf("unscored counts = %+v, report has %d hubs and %d candidates",
			a.Unscored, len(l6.Hubs), len(l6.Candidates))
	}
	if a.Unscored.CandidatePairs == 0 {
		t.Error("the fixture produced no unscored candidates, so the count assertion is vacuous")
	}
}
