// Package lint is ADR-0005 §D3's linter: ten deterministic checks over
// ADR-0001's v2 schema, plus §D4's health score.
//
// Two properties are structural, not stylistic:
//
//   - **No founder-paid inference** (ADR-0005 binding input 1). Every finding
//     here is SQL or git plumbing. No model runs, and the report says out loud
//     what that costs — paraphrase duplicates and semantic contradictions are
//     not detected, and absence of findings is never presented as absence of
//     problems.
//   - **Every finding names its rows.** §D5 inherits ADR-0004 §D6.2: a number
//     that cannot be traced to rows may not be rendered. Findings carry IDs, so
//     `--raw` and `--format json` are projections of the same data the terminal
//     text summarizes, never a separate computation.
//
// Purged decisions (ADR-0001 Amendment 4's `'[purged]'` tombstones) are
// terminal, so every check here excludes them by its status filter rather than
// by naming them. The fixture corpus contains one to keep that true rather
// than assumed.
package lint

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/memtrace-dev/memtrace/internal/importer"
)

// Options configures one lint run.
type Options struct {
	ProjectID string
	// RepoRoot is the working tree. Empty means "no repo present": the checks
	// that need one report N/A rather than guessing, and the score renormalizes
	// its weights over what remained applicable (§D4).
	RepoRoot string
	// Now is bound once per run (§D3) so a lint run is reproducible: two checks
	// compare against it and a drifting clock would make findings unstable
	// between the query and the report.
	Now time.Time
	// MarkExpired is L1's side effect (ADR-0001 D2: first observation emits
	// `decision.expired`, idempotently). Injected so the linter stays a
	// read-mostly package and tests can observe the emission.
	MarkExpired func(decisionID string) (bool, error)
	// CommitExists is L3's git plumbing, injected for testability. nil means
	// commit refs are not checked.
	CommitExists func(sha string) bool
}

// Finding is one flagged row. Every field that appears in the report is here,
// because the report may not compute anything the JSON output cannot show.
type Finding struct {
	ID        string   `json:"id"`
	Title     string   `json:"title,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	SourceRef string   `json:"source_ref,omitempty"`
	Related   []string `json:"related,omitempty"`
}

// Check is one of L1–L10.
type Check struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Scored   bool      `json:"scored"`
	Findings []Finding `json:"findings"`
	// Checked is the denominator printed beside the finding count. §D4's
	// suppression rule 2: no aggregate without its sample size.
	Checked int `json:"checked"`
	// NA distinguishes "ran and found nothing" from "could not run". Conflating
	// the two is how a fresh import would read as clean when it is unmeasured
	// (and how L8's gate would otherwise deduct points on day one).
	NA       bool   `json:"na"`
	NAReason string `json:"na_reason,omitempty"`
	Misses   string `json:"misses,omitempty"`
	// Candidates are review prompts that deliberately do NOT feed the score
	// (ADR-0005 Amendment 2). A check may surface a structural coincidence
	// worth a human's eye while having no calibrated claim about how often it
	// means something — L6's shared-scope tier is exactly that. Findings are
	// arithmetic; Candidates are prompts.
	Candidates []Finding `json:"candidates,omitempty"`
	// Hubs collapse a coincidence shared by many rows into one line. A glob
	// claimed by four or more decisions describes a file everything touches,
	// not k(k-1)/2 conflicts.
	Hubs []Finding `json:"hubs,omitempty"`
}

// Result is the whole lint run.
type Result struct {
	Checks []Check `json:"checks"`
	// Corrupt holds invariant violations, not findings: two non-terminal
	// decisions sharing (project_id, topic_key) is store corruption (§D3 L6),
	// and reporting it as a lint finding would understate it.
	Corrupt  []Finding         `json:"corrupt,omitempty"`
	Entries  int               `json:"entries"`
	Modes    map[string]string `json:"modes"`
	Score    *Score            `json:"score"`
	GatedOut []string          `json:"gated_out,omitempty"`
}

// Check finds a check by id.
func (r *Result) Check(id string) *Check {
	for i := range r.Checks {
		if r.Checks[i].ID == id {
			return &r.Checks[i]
		}
	}
	return nil
}

// Run executes L1–L10 and computes the score.
func Run(db *sql.DB, opts Options) (*Result, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	opts.Now = opts.Now.UTC()
	res := &Result{Modes: map[string]string{
		// §D4 suppression rule 3: a score whose method is hidden is a score an
		// evaluator rightly rejects.
		"duplicates":     "exact-match only (no embeddings)",
		"contradictions": "title collisions scored; shared-scope candidates listed unscored (calibration pending)",
	}}

	tree := newTreeIndex(opts.RepoRoot)
	entries, err := countEntries(db, opts.ProjectID)
	if err != nil {
		return nil, err
	}
	res.Entries = entries

	gated, gateReason, err := eventsGateOpen(db, opts.Now)
	if err != nil {
		return nil, err
	}

	for _, fn := range []func(*sql.DB, Options, *Result, *treeIndex) (Check, error){
		checkL1, checkL2, checkL3, checkL4, checkL5, checkL6, checkL7, checkL10,
	} {
		c, err := fn(db, opts, res, tree)
		if err != nil {
			return nil, err
		}
		res.Checks = append(res.Checks, c)
	}
	for _, fn := range []func(*sql.DB, Options) (Check, error){checkL8, checkL9} {
		c, err := fn(db, opts)
		if err != nil {
			return nil, err
		}
		if !gated {
			c.NA, c.NAReason, c.Findings = true, gateReason, nil
			res.GatedOut = append(res.GatedOut, c.ID)
		}
		res.Checks = append(res.Checks, c)
	}
	sort.SliceStable(res.Checks, func(i, j int) bool {
		return checkOrder(res.Checks[i].ID) < checkOrder(res.Checks[j].ID)
	})

	corrupt, err := topicKeyCorruption(db, opts.ProjectID)
	if err != nil {
		return nil, err
	}
	res.Corrupt = corrupt
	res.Score = computeScore(res, opts)
	return res, nil
}

func checkOrder(id string) int {
	n := 0
	for _, r := range id[1:] {
		n = n*10 + int(r-'0')
	}
	return n
}

// countEntries is the corpus denominator: non-terminal decisions plus live
// notes. Terminal rows — including purge tombstones — are excluded, so a store
// cannot be made to look healthier by accumulating rejected rows.
func countEntries(db *sql.DB, projectID string) (int, error) {
	var n int
	err := db.QueryRow(`
		SELECT (SELECT COUNT(*) FROM decisions
		         WHERE project_id = ? AND status IN ('proposed','active','violated'))
		     + (SELECT COUNT(*) FROM notes
		         WHERE project_id = ? AND status IN ('active','stale'))`,
		projectID, projectID).Scan(&n)
	return n, err
}

// eventsGateOpen implements L8/L9's shared gate: they run only once the
// store's oldest `pack.served` event is more than 30 days old.
//
// This gate is load-bearing for §D4. On a fresh import there are no pack
// events at all, so "never packed" and "repeatedly violated" are N/A, not
// findings — otherwise every stranger's first run would report their entire
// corpus as dead weight on the strength of varve never having run.
func eventsGateOpen(db *sql.DB, now time.Time) (bool, string, error) {
	var oldest sql.NullString
	err := db.QueryRow(`SELECT MIN(ts) FROM events WHERE kind = 'pack.served'`).Scan(&oldest)
	if err != nil && err != sql.ErrNoRows {
		return false, "", err
	}
	if !oldest.Valid || oldest.String == "" {
		return false, "no packing history yet — varve has not served context in this store", nil
	}
	t, err := time.Parse(time.RFC3339, oldest.String)
	if err != nil {
		return false, "packing history has an unparseable timestamp", nil
	}
	if now.Sub(t) < 30*24*time.Hour {
		return false, "packing history is less than 30 days old", nil
	}
	return true, "", nil
}

// ---------------------------------------------------------------- L1 … L10

func checkL1(db *sql.DB, opts Options, _ *Result, _ *treeIndex) (Check, error) {
	c := Check{ID: "L1", Name: "expired but still binding",
		Misses: "nothing — the expiry predicate is total"}
	if err := db.QueryRow(`SELECT COUNT(*) FROM decisions
		 WHERE project_id = ? AND status IN ('active','violated')`,
		opts.ProjectID).Scan(&c.Checked); err != nil {
		return c, err
	}
	// ADR-0004 A1.3 rule 3: compare in the stored Z-form. A Go-side RFC3339
	// bound is the same discipline as strftime on the SQL side.
	now := opts.Now.Format("2006-01-02T15:04:05Z")
	rows, err := db.Query(`
		SELECT id, title, expires_at FROM decisions
		 WHERE project_id = ? AND status IN ('active','violated')
		   AND expires_at IS NOT NULL AND expires_at < ?`, opts.ProjectID, now)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Finding
		var exp string
		if err := rows.Scan(&f.ID, &f.Title, &exp); err != nil {
			return c, err
		}
		f.Detail = "expired " + exp
		c.Findings = append(c.Findings, f)
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	// The side effect (ADR-0001 D2): first observation emits decision.expired.
	// Expiry changes no state — the event records that someone saw it.
	if opts.MarkExpired != nil {
		for _, f := range c.Findings {
			if _, err := opts.MarkExpired(f.ID); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

func checkL2(db *sql.DB, opts Options, _ *Result, _ *treeIndex) (Check, error) {
	// Deliberately includes the migration-born population (ADR-0001 Amendment
	// 4's audit item 3): surfacing grandfathered rows for triage is the
	// linter's assigned job, and their revert-blindness — no accepting
	// evidence means ADR-0004 D6's automatic revert detection can never fire
	// for them — is exactly why they belong here.
	c := Check{ID: "L2", Name: "binding decisions with no evidence",
		Misses: "evidence that exists but is meaningless (L3 covers liveness for checkable kinds)"}
	if err := db.QueryRow(`SELECT COUNT(*) FROM decisions
		 WHERE project_id = ? AND status IN ('active','violated')`,
		opts.ProjectID).Scan(&c.Checked); err != nil {
		return c, err
	}
	rows, err := db.Query(`
		SELECT d.id, d.title, d.status FROM decisions d
		 WHERE d.project_id = ? AND d.status IN ('active','violated')
		   AND NOT EXISTS (SELECT 1 FROM evidence e WHERE e.decision_id = d.id)`,
		opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Finding
		if err := rows.Scan(&f.ID, &f.Title, &f.Detail); err != nil {
			return c, err
		}
		c.Findings = append(c.Findings, f)
	}
	return c, rows.Err()
}

func checkL3(db *sql.DB, opts Options, res *Result, tree *treeIndex) (Check, error) {
	c := Check{ID: "L3", Name: "dead references", Scored: true,
		Misses: "pr/url/import refs (no network), and commits that exist locally but vanished from all remotes"}
	rows, err := db.Query(`
		SELECT e.id, e.decision_id, e.kind, e.ref
		  FROM evidence e JOIN decisions d ON d.id = e.decision_id
		 WHERE d.project_id = ? AND d.status IN ('proposed','active','violated')
		   AND e.kind IN ('commit','file')`, opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	type ref struct{ id, decisionID, kind, ref string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.decisionID, &r.kind, &r.ref); err != nil {
			return c, err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	// ADR-0005 Amendment 1: the commit tier is N/A when the repo has no commits
	// as well as when there is no repo. A fresh `git init` resolves no object,
	// so every commit ref would read as dead — an unreachable-commit finding
	// that says nothing about the corpus. Disclosed on the method line rather
	// than silently dropped.
	commitsCheckable := opts.CommitExists != nil && opts.RepoRoot != "" && repoHasCommits(opts.RepoRoot)
	if opts.CommitExists != nil && !commitsCheckable {
		res.Modes["dead_refs"] = "file references only (the repo has no commits to check against)"
	}
	for _, r := range refs {
		switch r.kind {
		case "commit":
			if !commitsCheckable {
				continue // unchecked, so not counted in the denominator either
			}
			c.Checked++
			if !opts.CommitExists(r.ref) {
				c.Findings = append(c.Findings, Finding{ID: r.decisionID,
					Detail: "unreachable commit " + shortSHA(r.ref), SourceRef: r.ref})
			}
		case "file":
			if opts.RepoRoot == "" {
				continue
			}
			c.Checked++
			if _, err := os.Stat(filepath.Join(opts.RepoRoot, r.ref)); err != nil {
				c.Findings = append(c.Findings, Finding{ID: r.decisionID,
					Detail: "missing file " + r.ref, SourceRef: r.ref})
			}
		}
	}
	if c.Checked == 0 {
		c.NA = true
		c.NAReason = "no checkable commit or file references"
	}
	_ = tree
	return c, nil
}

func checkL4(db *sql.DB, opts Options, _ *Result, tree *treeIndex) (Check, error) {
	// Advisory, never scored (§D3 L4): a zero-match glob may be deliberate —
	// ADR-0001 D4 explicitly allows scoping files that do not exist yet.
	c := Check{ID: "L4", Name: "scopes matching no files",
		Misses: "badly-drawn but non-empty scopes"}
	if opts.RepoRoot == "" {
		c.NA, c.NAReason = true, "no repo present"
		return c, nil
	}
	rows, err := db.Query(`
		SELECT id, title, scope FROM decisions
		 WHERE project_id = ? AND status IN ('proposed','active','violated')
		   AND scope != '[]'`, opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, scopeJSON string
		if err := rows.Scan(&id, &title, &scopeJSON); err != nil {
			return c, err
		}
		c.Checked++
		var dead []string
		for _, g := range decodeJSONArray(scopeJSON) {
			if !tree.matchesAny(g) {
				dead = append(dead, g)
			}
		}
		if len(dead) > 0 {
			c.Findings = append(c.Findings, Finding{ID: id, Title: title,
				Detail: "no files match " + strings.Join(dead, ", ")})
		}
	}
	return c, rows.Err()
}

// checkL5 is the duplicate check. §D3 L5: the SQL there is the shape; the hash
// is computed at row load in Go, with ADR-0002 P5.4's normalization — the same
// function the rules-file importer hashes with, so "the same text" means one
// thing across the product.
//
// Exact tier only. The near tier needs embeddings from the user's own
// configured embedder; where they are absent the report says so rather than
// letting silence imply cleanliness.
func checkL5(db *sql.DB, opts Options, res *Result, _ *treeIndex) (Check, error) {
	c := Check{ID: "L5", Name: "duplicates", Scored: true,
		Misses: "paraphrase duplicates — not detected without embeddings"}
	groups := map[string][]Finding{}
	add := func(hash string, f Finding) {
		if hash == "" {
			return
		}
		groups[hash] = append(groups[hash], f)
	}

	rows, err := db.Query(`SELECT id, title, body, embedding IS NOT NULL FROM decisions
		 WHERE project_id = ? AND status IN ('proposed','active','violated')`, opts.ProjectID)
	if err != nil {
		return c, err
	}
	unembedded := 0
	for rows.Next() {
		var id, title, body string
		var embedded bool
		if err := rows.Scan(&id, &title, &body, &embedded); err != nil {
			rows.Close()
			return c, err
		}
		if !embedded {
			unembedded++
		}
		c.Checked++
		add(importer.NormalizeText(title+" "+body), Finding{ID: id, Title: title})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}

	nrows, err := db.Query(`SELECT id, content, embedding IS NOT NULL FROM notes
		 WHERE project_id = ? AND status IN ('active','stale')`, opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer nrows.Close()
	for nrows.Next() {
		var id, content string
		var embedded bool
		if err := nrows.Scan(&id, &content, &embedded); err != nil {
			return c, err
		}
		if !embedded {
			unembedded++
		}
		c.Checked++
		add(importer.NormalizeText(content), Finding{ID: id, Title: truncate(content, 60)})
	}
	if err := nrows.Err(); err != nil {
		return c, err
	}

	hashes := make([]string, 0, len(groups))
	for h, g := range groups {
		if len(g) > 1 {
			hashes = append(hashes, h)
		}
	}
	sort.Strings(hashes)
	for _, h := range hashes {
		g := groups[h]
		sort.Slice(g, func(i, j int) bool { return g[i].ID < g[j].ID })
		// Every member is a finding: the score's numerator is "rows affected",
		// and reporting one row per group would understate a 4-way duplicate.
		for i, f := range g {
			f.Detail = "duplicate of " + strings.Join(idsExcept(g, i), ", ")
			f.Related = idsExcept(g, i)
			c.Findings = append(c.Findings, f)
		}
	}
	if unembedded > 0 {
		res.Modes["duplicates"] = "exact-match only (" + itoa(unembedded) + " rows have no embedding)"
	}
	return c, nil
}

// checkL6 is the contradiction-*candidate* check. §D3 L6 and ADR-0002 §P6 both
// insist on the word: these are structural coincidences presented as "review
// candidates — assign a shared topic_key or supersede", never verdicts. A
// linter that told users which of their rules was wrong would be making
// judgments it cannot support.
// Two tiers, and only one of them is arithmetic (ADR-0005 Amendment 2).
//
// The title-collision tier scores: two non-terminal decisions with the same
// normalized title and different bodies is a strong prior on any corpus shape.
//
// The shared-scope tier does NOT score. Measured on a real 80-entry store it
// flagged 17 of 45 decisions, nine of which shared only one glob — a file many
// decisions were *about* rather than rules that governed it. `scope` conflates
// governing a file with concerning it, and on the population this report
// actually runs against, every structural axis that might separate the two is
// assigned by the import machinery rather than by the user: importers set
// `kind` by source contract (so every CLAUDE.md block is a `convention`), D2.5
// forbids them setting `topic_key`, and D9 makes exact-path scopes the norm.
// Choosing an axis today is choosing which corpus shape to be wrong on, so the
// tier reports unscored candidates until ≥3 real corpora say which axis works.
func checkL6(db *sql.DB, opts Options, _ *Result, _ *treeIndex) (Check, error) {
	c := Check{ID: "L6", Name: "contradiction candidates", Scored: true,
		Misses: "semantic contradictions between rows with different titles and scopes — not structurally detectable; " +
			"shared-scope candidates are listed but not scored (calibration pending)"}
	rows, err := db.Query(`
		SELECT id, title, body, scope, kind, COALESCE(topic_key,'') FROM decisions
		 WHERE project_id = ? AND status IN ('proposed','active','violated')
		 ORDER BY id`, opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	type row struct {
		id, title, body, kind, topicKey string
		scope                           []string
	}
	var all []row
	for rows.Next() {
		var r row
		var scopeJSON string
		if err := rows.Scan(&r.id, &r.title, &r.body, &scopeJSON, &r.kind, &r.topicKey); err != nil {
			return c, err
		}
		r.scope = decodeJSONArray(scopeJSON)
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	c.Checked = len(all)

	// Scored tier: identical titles with *differing* bodies. Identical titles
	// with identical bodies are duplicates, and L5 already owns them — filing
	// the same rows under two checks would deduct twice for one problem.
	scored := map[string]*Finding{}
	noteScored := func(a, b row) {
		f, ok := scored[a.id]
		if !ok {
			f = &Finding{ID: a.id, Title: a.title, Detail: "identical title, different body"}
			scored[a.id] = f
		}
		f.Related = append(f.Related, b.id)
	}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			a, b := all[i], all[j]
			if importer.NormalizeText(a.title) == importer.NormalizeText(b.title) &&
				importer.NormalizeText(a.body) != importer.NormalizeText(b.body) {
				noteScored(a, b)
				noteScored(b, a)
			}
		}
	}
	ids := make([]string, 0, len(scored))
	for id := range scored {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c.Findings = append(c.Findings, *scored[id])
	}

	// Unscored tier: shared scope globs, grouped by the glob itself so a hub
	// is one line rather than a combinatorial fan-out.
	byGlob := map[string][]int{}
	for i := range all {
		seen := map[string]bool{}
		for _, g := range all[i].scope {
			if g == "" || seen[g] {
				continue
			}
			seen[g] = true
			byGlob[g] = append(byGlob[g], i)
		}
	}
	globs := make([]string, 0, len(byGlob))
	for g := range byGlob {
		globs = append(globs, g)
	}
	sort.Strings(globs)

	type cand struct {
		f              Finding
		bothConvention bool
		bothUntopiced  bool
	}
	var cands []cand
	for _, g := range globs {
		members := byGlob[g]
		if len(members) < 2 {
			continue
		}
		if len(members) >= hubShareThreshold {
			ids := make([]string, 0, len(members))
			for _, i := range members {
				ids = append(ids, all[i].id)
			}
			c.Hubs = append(c.Hubs, Finding{
				ID: g,
				Detail: itoa(len(members)) + " decisions share scope " + g +
					" — a shared-scope hub, not a conflict; curate with a shared topic_key if any of them compete",
				Related: ids,
			})
			continue
		}
		for x := 0; x < len(members); x++ {
			for y := x + 1; y < len(members); y++ {
				a, b := all[members[x]], all[members[y]]
				cands = append(cands, cand{
					f: Finding{ID: a.id, Title: a.title,
						Detail:  "shares scope glob " + g + " with " + b.id + " — review candidate, not a verdict",
						Related: []string{b.id}},
					bothConvention: a.kind == "convention" && b.kind == "convention",
					bothUntopiced:  a.topicKey == "" && b.topicKey == "",
				})
			}
		}
	}
	// Presentation order only — these signals rank the list a human reads;
	// they deliberately do not gate what enters it, because on imported
	// corpora both are assigned by the importer rather than the user.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].bothConvention != cands[j].bothConvention {
			return cands[i].bothConvention
		}
		if cands[i].bothUntopiced != cands[j].bothUntopiced {
			return cands[i].bothUntopiced
		}
		return false
	})
	for _, cd := range cands {
		c.Candidates = append(c.Candidates, cd.f)
	}
	return c, nil
}

// hubShareThreshold is the point at which sharing a glob stops being a
// coincidence worth pairing up and starts describing the file. Presentation
// only: nothing below it scores either.
const hubShareThreshold = 4

// topicKeyCorruption is L6's guaranteed tier. Two non-terminal decisions
// sharing (project_id, topic_key) cannot happen — a partial unique index
// forbids it — so if it is ever seen the store is corrupt, and calling that a
// "finding" would file a broken invariant next to a duplicate paragraph.
func topicKeyCorruption(db *sql.DB, projectID string) ([]Finding, error) {
	rows, err := db.Query(`
		SELECT topic_key, COUNT(*), group_concat(id) FROM decisions
		 WHERE project_id = ? AND topic_key IS NOT NULL
		   AND status IN ('proposed','active','violated')
		 GROUP BY topic_key HAVING COUNT(*) > 1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		var key, ids string
		var n int
		if err := rows.Scan(&key, &n, &ids); err != nil {
			return nil, err
		}
		out = append(out, Finding{ID: key, Detail: "CORRUPT: " + itoa(n) +
			" non-terminal decisions share topic_key " + key, Related: strings.Split(ids, ",")})
	}
	return out, rows.Err()
}

func checkL7(db *sql.DB, opts Options, _ *Result, tree *treeIndex) (Check, error) {
	c := Check{ID: "L7", Name: "staleness", Scored: true,
		Misses: "a stale-dated rule that is still correct — staleness is a prompt to re-confirm, never an auto-transition"}
	cutoff := opts.Now.AddDate(0, 0, -180).Format("2006-01-02T15:04:05Z")
	rows, err := db.Query(`
		SELECT id, title, scope,
		       max(updated_at, coalesce(decided_at,''), status_changed_at) AS touched
		  FROM decisions
		 WHERE project_id = ? AND status IN ('proposed','active','violated')`,
		opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, scopeJSON, touched string
		if err := rows.Scan(&id, &title, &scopeJSON, &touched); err != nil {
			return c, err
		}
		c.Checked++
		if touched >= cutoff {
			continue
		}
		// §D3 L7's second clause: *and* whose scoped files have all changed
		// since. Over an empty scope the clause is vacuous, so an unscoped
		// decision is stale on age alone — which is the honest reading, since
		// there are no files whose stillness could argue it is still current.
		scope := decodeJSONArray(scopeJSON)
		if len(scope) > 0 && !tree.allScopedFilesChangedSince(scope, touched) {
			continue
		}
		c.Findings = append(c.Findings, Finding{ID: id, Title: title,
			Detail: "untouched since " + touched})
	}
	if err := rows.Err(); err != nil {
		return c, err
	}

	nrows, err := db.Query(`SELECT id, content, updated_at FROM notes
		 WHERE project_id = ? AND status IN ('active','stale')`, opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer nrows.Close()
	for nrows.Next() {
		var id, content, updated string
		if err := nrows.Scan(&id, &content, &updated); err != nil {
			return c, err
		}
		c.Checked++
		if updated < cutoff {
			c.Findings = append(c.Findings, Finding{ID: id, Title: truncate(content, 60),
				Detail: "untouched since " + updated})
		}
	}
	return c, nrows.Err()
}

// ProposedBacklog is L7's adoption half: proposed rows older than 14 days,
// with pending disposal requests listed first as their own group (§D3 L7).
//
// It is never scored. On a fresh import every row is proposed, and deducting
// points for that would make the import itself the failure the report reports.
type ProposedBacklog struct {
	DisposalRequested []Finding `json:"disposal_requested"`
	Aging             []Finding `json:"aging"`
	Total             int       `json:"total"`
}

// QueryBacklog returns the proposed backlog. The disposal-request group is
// ADR-0001 Amendment 3's promised triage surface: each of those rows is one
// keystroke from resolution because a user already said "throw this away".
func QueryBacklog(db *sql.DB, opts Options) (*ProposedBacklog, error) {
	b := &ProposedBacklog{}
	cutoff := opts.Now.AddDate(0, 0, -14).Format("2006-01-02T15:04:05Z")
	rows, err := db.Query(`
		SELECT d.id, d.title, d.status_changed_at,
		       EXISTS (SELECT 1 FROM events e
		                WHERE e.decision_id = d.id
		                  AND e.kind = 'decision.disposal_requested') AS requested
		  FROM decisions d
		 WHERE d.project_id = ? AND d.status = 'proposed'
		 ORDER BY d.status_changed_at`, opts.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Finding
		var changed string
		var requested bool
		if err := rows.Scan(&f.ID, &f.Title, &changed, &requested); err != nil {
			return nil, err
		}
		b.Total++
		f.Detail = "proposed " + changed
		switch {
		case requested:
			// A pending request survives only while no human terminal
			// transition followed; a rejected/accepted row is no longer
			// `proposed`, so the status filter above already enforces that.
			b.DisposalRequested = append(b.DisposalRequested, f)
		case changed < cutoff:
			b.Aging = append(b.Aging, f)
		}
	}
	return b, rows.Err()
}

// QueryAdoption counts the never-scored section (§D4). These are facts about
// how varve is being used; on a fresh import they are all structurally "bad",
// which is why they are a checklist with counts and no number.
func QueryAdoption(db *sql.DB, opts Options) (Adoption, error) {
	var a Adoption
	err := db.QueryRow(`
		SELECT (SELECT COUNT(*) FROM decisions WHERE project_id = ? AND status = 'proposed'),
		       (SELECT COUNT(*) FROM decisions WHERE project_id = ? AND status IN ('active','violated')),
		       (SELECT COUNT(*) FROM decisions d WHERE d.project_id = ?
		          AND EXISTS (SELECT 1 FROM evidence e
		                       WHERE e.decision_id = d.id AND e.added_by = 'human')),
		       (SELECT COUNT(DISTINCT decision_id) FROM events WHERE kind = 'pack.item')`,
		opts.ProjectID, opts.ProjectID, opts.ProjectID).
		Scan(&a.Proposed, &a.Accepted, &a.WithEvidence, &a.Packed)
	return a, err
}

func checkL8(db *sql.DB, opts Options) (Check, error) {
	c := Check{ID: "L8", Name: "never-packed decisions",
		Misses: "decisions packed before the observer existed"}
	cutoff := opts.Now.AddDate(0, 0, -30).Format("2006-01-02T15:04:05Z")
	rows, err := db.Query(`
		SELECT d.id, d.title, d.decided_at FROM decisions d
		 WHERE d.project_id = ? AND d.status IN ('active','violated')
		   AND d.decided_at < ?
		   AND NOT EXISTS (SELECT 1 FROM events e
		                    WHERE e.kind = 'pack.item' AND e.decision_id = d.id)`,
		opts.ProjectID, cutoff)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Finding
		var decided sql.NullString
		if err := rows.Scan(&f.ID, &f.Title, &decided); err != nil {
			return c, err
		}
		f.Detail = "accepted " + decided.String + ", never packed"
		c.Findings = append(c.Findings, f)
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	return c, db.QueryRow(`SELECT COUNT(*) FROM decisions
		 WHERE project_id = ? AND status IN ('active','violated')`, opts.ProjectID).Scan(&c.Checked)
}

// checkL9 counts unresolved violation *episodes* (§D3 L9, corrected
// 2026-07-29 against ADR-0002 Amendment 1's derivation).
//
// Two things the first draft got wrong and this must not reintroduce:
//
//   - A single dismissal must not exempt a decision forever. Resolution is
//     per-episode: an episode is resolved by a dismissal naming *that
//     episode's event id* or by a counter-revert targeting *that episode's
//     commit*. A decision with three episodes and one dismissal still
//     reports, with unresolved = 2.
//   - Terminal rows — reverted, superseded, purged — are not "rules the fleet
//     keeps breaking". The status filter is part of the semantics.
//
// Both numbers print: total episodes and unresolved. A rule violated five
// times and dismissed five times is still a rule worth re-stating, and the two
// counts say different things.
func checkL9(db *sql.DB, opts Options) (Check, error) {
	c := Check{ID: "L9", Name: "repeatedly violated decisions",
		Misses: "everything trailer-only revert detection misses (ADR-0004 D2's one-directional under-count)"}
	rows, err := db.Query(`
		SELECT v.decision_id, d.title,
		       COUNT(*) AS episodes,
		       SUM(CASE WHEN dis.id IS NULL AND cr.id IS NULL THEN 1 ELSE 0 END) AS unresolved
		  FROM events v
		  JOIN decisions d ON d.id = v.decision_id
		                  AND d.status IN ('active','violated')
		  LEFT JOIN events dis ON dis.kind = 'decision.violation_dismissed'
		                      AND json_extract(dis.payload, '$.violation_event_id') = v.id
		  LEFT JOIN events cr ON cr.kind = 'revert.detected'
		                     AND json_extract(cr.payload, '$.reverts_sha') = v.commit_sha
		 WHERE v.kind = 'decision.violated' AND d.project_id = ?
		 GROUP BY v.decision_id, d.title
		HAVING episodes >= 3
		 ORDER BY v.decision_id`, opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Finding
		var episodes, unresolved int
		if err := rows.Scan(&f.ID, &f.Title, &episodes, &unresolved); err != nil {
			return c, err
		}
		f.Detail = itoa(episodes) + " violation episodes, " + itoa(unresolved) + " unresolved"
		c.Findings = append(c.Findings, f)
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	return c, db.QueryRow(`SELECT COUNT(*) FROM decisions
		 WHERE project_id = ? AND status IN ('active','violated')`, opts.ProjectID).Scan(&c.Checked)
}

// checkL10 is scope hygiene. Two shapes, and the distinction between them is
// the decisions-log item 5 ruling: repo-wide *conventions* via `scope=[]` are
// the sanctioned common case and are not flagged; repo-wide *decisions* are
// almost always under-thought.
func checkL10(db *sql.DB, opts Options, _ *Result, _ *treeIndex) (Check, error) {
	c := Check{ID: "L10", Name: "scope hygiene", Scored: true,
		Misses: "badly-drawn but non-empty scopes"}
	if err := db.QueryRow(`SELECT COUNT(*) FROM decisions
		 WHERE project_id = ? AND kind = 'decision'
		   AND status IN ('proposed','active','violated')`, opts.ProjectID).Scan(&c.Checked); err != nil {
		return c, err
	}
	rows, err := db.Query(`
		SELECT id, title, scope FROM decisions
		 WHERE project_id = ? AND kind = 'decision'
		   AND status IN ('proposed','active','violated')
		   AND scope IN ('["**"]', '[]')
		 ORDER BY id`, opts.ProjectID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Finding
		var scope string
		if err := rows.Scan(&f.ID, &f.Title, &scope); err != nil {
			return c, err
		}
		if scope == `["**"]` {
			f.Detail = `repo-wide decision (scope ["**"]) — use scope=[] for a convention, or narrow it`
		} else {
			f.Detail = "unscoped decision — can never be violated or attributed"
		}
		c.Findings = append(c.Findings, f)
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	if c.Checked == 0 {
		c.NA, c.NAReason = true, "no decision-kind rows"
	}
	return c, nil
}

// ------------------------------------------------------------------ helpers

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func idsExcept(g []Finding, skip int) []string {
	var out []string
	for i, f := range g {
		if i != skip {
			out = append(out, f.ID)
		}
	}
	return out
}

func decodeJSONArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(strings.Trim(s, "[]"), ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// treeIndex is the working tree, walked once per run. L4 and L7 both need it,
// and walking twice on a large repo is the difference between a report that
// runs on save and one nobody waits for.
type treeIndex struct {
	root  string
	files []string
	mtime map[string]time.Time
}

func newTreeIndex(root string) *treeIndex {
	t := &treeIndex{root: root, mtime: map[string]time.Time{}}
	if root == "" {
		return t
	}
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == ".varve" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		t.files = append(t.files, rel)
		if info, err := d.Info(); err == nil {
			t.mtime[rel] = info.ModTime().UTC()
		}
		return nil
	})
	return t
}

func (t *treeIndex) matchesAny(glob string) bool {
	for _, f := range t.files {
		if ok, err := doublestar.Match(glob, f); err == nil && ok {
			return true
		}
	}
	return false
}

// allScopedFilesChangedSince reports whether every file the scope matches has
// been modified since the decision was last touched — L7's second clause, on
// the same mtime logic the v1 staleness scan uses. A scope matching nothing is
// not evidence of staleness (L4 owns that finding), so it answers false.
func (t *treeIndex) allScopedFilesChangedSince(scope []string, touched string) bool {
	cut, err := time.Parse("2006-01-02T15:04:05Z", touched)
	if err != nil {
		if cut, err = time.Parse(time.RFC3339, touched); err != nil {
			return false
		}
	}
	matched := 0
	for _, f := range t.files {
		for _, g := range scope {
			ok, err := doublestar.Match(g, f)
			if err != nil || !ok {
				continue
			}
			matched++
			if !t.mtime[f].After(cut) {
				return false
			}
			break
		}
	}
	return matched > 0
}

// repoHasCommits reports whether the working tree has any commit at all.
func repoHasCommits(root string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = root
	return cmd.Run() == nil
}

// GitCommitExists is L3's git plumbing: a commit is dead iff the object is
// unreachable in the local repo.
func GitCommitExists(root string) func(string) bool {
	return func(sha string) bool {
		cmd := exec.Command("git", "cat-file", "-e", sha+"^{commit}")
		cmd.Dir = root
		return cmd.Run() == nil
	}
}
