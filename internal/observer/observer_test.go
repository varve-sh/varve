package observer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/types"
)

// These drive a real git repository, because the half of the observer that
// lives here *is* the git plumbing: a fake would test the fake.

type repo struct {
	t    *testing.T
	root string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &repo{t: t, root: root}
	r.git("init", "--initial-branch=main")
	r.git("config", "user.email", "dev@example.com")
	r.git("config", "user.name", "Dev")
	r.git("config", "commit.gpgsign", "false")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files and commits them, optionally at a specific committer
// date (which may carry a non-UTC offset — see the timezone test).
func (r *repo) commit(msg string, date string, files map[string]string) string {
	r.t.Helper()
	for name, body := range files {
		full := filepath.Join(r.root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = r.root
	if date != "" {
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git commit: %v\n%s", err, out)
	}
	return r.git("rev-parse", "HEAD")
}

func testKernel(t *testing.T, root string) *kernel.MemoryKernel {
	t.Helper()
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")
	k := kernel.New(filepath.Join(t.TempDir(), "obs.db"), "proj-obs")
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	if err := k.RecordObserverEnabled(time.Now().UTC().Add(-24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	return k
}

func acceptDecision(t *testing.T, k *kernel.MemoryKernel, title string, scope []string, evidence ...string) *types.Decision {
	t.Helper()
	in := kernel.DecisionInput{
		ProjectID: "proj-obs", Title: title, Scope: scope,
		Source: types.DecisionSourceUser,
	}
	for _, sha := range evidence {
		in.Evidence = append(in.Evidence, kernel.EvidenceInput{
			Kind: types.EvidenceKindCommit, Ref: sha, AddedBy: types.ActorHuman,
		})
	}
	if len(in.Evidence) == 0 {
		in.Evidence = append(in.Evidence, kernel.EvidenceInput{
			Kind: types.EvidenceKindImport, Ref: "seed", AddedBy: types.ActorHuman,
		})
	}
	d, err := k.Decisions().ProposeAccepted(in, kernel.AcceptOptions{Actor: types.ActorHuman})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestShow_ReadsTheFieldsTheObserverNeeds(t *testing.T) {
	r := newRepo(t)
	r.commit("initial", "", map[string]string{"README.md": "hi"})
	sha := r.commit("add auth", "", map[string]string{
		"internal/auth/session.go": "package auth",
		"internal/auth/token.go":   "package auth",
	})

	c, err := Show(r.root, sha)
	if err != nil {
		t.Fatal(err)
	}
	if c.SHA != sha {
		t.Errorf("sha = %s, want %s", c.SHA, sha)
	}
	if c.Subject != "add auth" || c.Author != "Dev" {
		t.Errorf("meta = (%q, %q)", c.Subject, c.Author)
	}
	if len(c.Files) != 2 {
		t.Errorf("files = %v, want both changed paths", c.Files)
	}
	if c.PatchID == "" {
		t.Error("patch_id is empty; §D1.5 makes it load-bearing for dedup")
	}
	if c.RevertsSHA != "" {
		t.Errorf("reverts = %q, want none", c.RevertsSHA)
	}
}

// The architect's finding, pinned: `%cI` emits the committer's *local* offset,
// while every event timestamp and every window comparison in §D0/§D5.1 is a
// lexicographic BETWEEN over RFC3339 **UTC** strings. Mixed offsets are not
// chronologically ordered as strings, so a commit made at 14:02+02:00 would
// sort after a session that ended at 13:00Z and silently fall outside a window
// it actually falls inside.
func TestShow_NormalisesCommitterTimeToUTC(t *testing.T) {
	r := newRepo(t)
	sha := r.commit("offset commit", "2026-07-28T14:02:33+02:00",
		map[string]string{"a.go": "package a"})

	c, err := Show(r.root, sha)
	if err != nil {
		t.Fatal(err)
	}
	if c.CommittedAt.Location() != time.UTC {
		t.Errorf("committed_at location = %v, want UTC", c.CommittedAt.Location())
	}
	want := time.Date(2026, 7, 28, 12, 2, 33, 0, time.UTC)
	if !c.CommittedAt.Equal(want) {
		t.Errorf("committed_at = %s, want %s", c.CommittedAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	// And the recorded payload is the Z form, so it compares chronologically
	// against the session windows.
	k := testKernel(t, r.root)
	if _, err := k.ObserveCommit(c); err != nil {
		t.Fatal(err)
	}
	evs, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventDiffObserved})
	if len(evs) != 1 {
		t.Fatalf("observations = %d", len(evs))
	}
	got, _ := evs[0].Payload["committed_at"].(string)
	if !strings.HasSuffix(got, "Z") {
		t.Errorf("committed_at payload = %q, want a UTC (Z) timestamp — a local "+
			"offset makes the window join's string comparison non-chronological", got)
	}
	if got != "2026-07-28T12:02:33Z" {
		t.Errorf("committed_at payload = %q, want the UTC instant", got)
	}
}

func TestShow_ParsesTheRevertTrailer(t *testing.T) {
	r := newRepo(t)
	first := r.commit("add feature", "", map[string]string{"a.go": "package a"})
	r.git("revert", "--no-edit", first)
	head := r.git("rev-parse", "HEAD")

	c, err := Show(r.root, head)
	if err != nil {
		t.Fatal(err)
	}
	if c.RevertsSHA != strings.ToLower(first) {
		t.Errorf("reverts = %q, want %q — §D2 is trailer-only, and this is the trailer",
			c.RevertsSHA, first)
	}
}

// §D1.5: patch-id survives a rebase, which is what lets reports count distinct
// changes instead of distinct SHAs. Without it one rebase doubles every
// conform count.
func TestPatchID_SurvivesARebase(t *testing.T) {
	r := newRepo(t)
	r.commit("base", "", map[string]string{"base.go": "package base"})
	r.git("checkout", "-b", "feature")
	feature := r.commit("feature work", "", map[string]string{"feat.go": "package feat"})
	before, err := Show(r.root, feature)
	if err != nil {
		t.Fatal(err)
	}

	r.git("checkout", "main")
	r.commit("unrelated main work", "", map[string]string{"main.go": "package main"})
	r.git("checkout", "feature")
	r.git("rebase", "main")
	rebased := r.git("rev-parse", "HEAD")

	after, err := Show(r.root, rebased)
	if err != nil {
		t.Fatal(err)
	}
	if rebased == feature {
		t.Fatal("the rebase did not rewrite the sha; the fixture proves nothing")
	}
	if after.PatchID != before.PatchID {
		t.Errorf("patch_id changed across a rebase: %q -> %q", before.PatchID, after.PatchID)
	}
}

// The scan is the half that makes the record complete: commits made before
// varve was installed, pulled from a teammate, or missed by a busy database.
func TestScan_ObservesUnobservedCommitsAndIsIdempotent(t *testing.T) {
	r := newRepo(t)
	k := testKernel(t, r.root)
	acceptDecision(t, k, "Auth is validated at the boundary", []string{"internal/auth/**"})

	for i := 0; i < 5; i++ {
		r.commit(fmt.Sprintf("work %d", i), "", map[string]string{
			fmt.Sprintf("internal/auth/f%d.go", i): "package auth",
		})
	}

	first, err := Scan(k, ScanOptions{RepoRoot: r.root})
	if err != nil {
		t.Fatal(err)
	}
	if first.Observed != 5 {
		t.Fatalf("observed %d commits, want 5: %+v", first.Observed, first)
	}
	if first.Matched != 5 {
		t.Errorf("matched %d, want one per commit", first.Matched)
	}

	second, err := Scan(k, ScanOptions{RepoRoot: r.root})
	if err != nil {
		t.Fatal(err)
	}
	if second.Observed != 0 {
		t.Errorf("a second scan observed %d commits again", second.Observed)
	}
	obs, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventDiffObserved})
	if len(obs) != 5 {
		t.Errorf("diff.observed rows = %d, want 5", len(obs))
	}
}

// §D1.2's stop rule is 20 consecutive, not 1: after a merge, histories
// interleave, and a stop-at-first walk strands unobserved commits behind
// observed ones forever.
func TestScan_WalksPastAlreadyObservedCommits(t *testing.T) {
	r := newRepo(t)
	k := testKernel(t, r.root)
	acceptDecision(t, k, "Everything under internal", []string{"internal/**"})

	var shas []string
	for i := 0; i < 6; i++ {
		shas = append(shas, r.commit(fmt.Sprintf("c%d", i), "", map[string]string{
			fmt.Sprintf("internal/f%d.go", i): "package internal",
		}))
	}
	// Observe the newest commit only, as the hook would have.
	if _, err := ObserveOne(k, r.root, shas[len(shas)-1]); err != nil {
		t.Fatal(err)
	}

	res, err := Scan(k, ScanOptions{RepoRoot: r.root})
	if err != nil {
		t.Fatal(err)
	}
	if res.Observed != 5 {
		t.Errorf("observed %d commits behind the one the hook caught, want 5: %+v",
			res.Observed, res)
	}
	obs, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventDiffObserved})
	if len(obs) != 6 {
		t.Errorf("diff.observed rows = %d, want every commit", len(obs))
	}
}

// §D1.3: the scan does not walk past the observation epoch, and an explicit
// backfill marks everything it produces.
func TestScan_StopsAtTheEpochUnlessBackfilling(t *testing.T) {
	r := newRepo(t)
	r.commit("ancient", "2020-01-01T00:00:00Z", map[string]string{
		"internal/old.go": "package internal",
	})
	k := testKernel(t, r.root) // epoch = 24h ago
	// A *migrated* decision: §D9 carries v1's created_at into decided_at, so it
	// is the only population whose decisions predate pre-epoch commits — and
	// therefore the only one for which a backfill produces verdicts at all.
	// (Fresh decisions cannot match pre-epoch commits: §D1.3 requires
	// decided_at <= committed_at.)
	seedOldDecision(t, k, "Everything under internal", "internal/**", "2019-01-01T00:00:00Z")
	r.commit("recent", "", map[string]string{"internal/new.go": "package internal"})

	res, err := Scan(k, ScanOptions{RepoRoot: r.root})
	if err != nil {
		t.Fatal(err)
	}
	if res.Observed != 1 || res.SkippedPreEpoch != 1 {
		t.Fatalf("scan = %+v, want one observed and one skipped as pre-epoch", res)
	}

	back, err := Scan(k, ScanOptions{RepoRoot: r.root, Backfill: true})
	if err != nil {
		t.Fatal(err)
	}
	if back.Observed != 1 {
		t.Fatalf("backfill scan = %+v, want the pre-epoch commit", back)
	}
	matches, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventDiffScopeMatch})
	backfilled, live := 0, 0
	for _, m := range matches {
		if m.Payload["backfill"] == true {
			backfilled++
		} else {
			live++
		}
	}
	if backfilled != 1 || live != 1 {
		t.Errorf("matches = %d backfilled, %d live; want exactly one of each so the "+
			"report can exclude archaeology and keep attribution", backfilled, live)
	}
}

// seedOldDecision inserts an accepted decision with an old decided_at, the way
// `migrate --from-v1` does (§D9 carries v1's created_at into decided_at).
func seedOldDecision(t *testing.T, k *kernel.MemoryKernel, title, glob, decidedAt string) {
	t.Helper()
	if _, err := k.Decisions().DB().Exec(`
		INSERT INTO decisions (id, project_id, kind, title, body, status, scope, confidence,
		    source, tags, supersedes, created_at, updated_at, decided_at, status_changed_at,
		    access_count)
		VALUES (?, 'proj-obs', 'decision', ?, '', 'active', ?, 1.0,
		    'import', '[]', '[]', ?, ?, ?, ?, 0)`,
		"01OLDDECISION"+strings.Repeat("0", 13), title,
		`["`+glob+`"]`, decidedAt, decidedAt, decidedAt, decidedAt); err != nil {
		t.Fatal(err)
	}
}

// A repository with no varve store and no git is not an error state — the hook
// runs on machines that have neither.
func TestScan_NonRepoIsANoOp(t *testing.T) {
	dir := t.TempDir()
	k := testKernel(t, dir)
	res, err := Scan(k, ScanOptions{RepoRoot: dir})
	if err != nil {
		t.Fatalf("scanning a non-repo must not error: %v", err)
	}
	if res.Walked != 0 || res.Observed != 0 {
		t.Errorf("result = %+v, want an empty no-op", res)
	}
}

// End to end through git: a commit that reverts a decision's accepting
// evidence terminates it, and the trailer is what proves it (§D2/§D6).
func TestObserveOne_RevertOfAcceptingEvidenceEndsTheDecision(t *testing.T) {
	r := newRepo(t)
	k := testKernel(t, r.root)
	accepting := r.commit("implement server-side sessions", "", map[string]string{
		"internal/auth/session.go": "package auth",
	})
	d := acceptDecision(t, k, "Sessions are server-side only",
		[]string{"internal/auth/**"}, accepting)

	r.git("revert", "--no-edit", accepting)
	head := r.git("rev-parse", "HEAD")

	res, err := ObserveOne(k, r.root, head)
	if err != nil {
		t.Fatal(err)
	}
	if !res.RevertDetected {
		t.Fatal("the revert trailer was not detected")
	}
	if len(res.DecisionsReverted) != 1 || res.DecisionsReverted[0] != d.ID {
		t.Fatalf("reverted = %v, want the decision whose accepting evidence went",
			res.DecisionsReverted)
	}
	got, _ := k.Decisions().GetDecision(d.ID)
	if got.Status != types.StatusReverted {
		t.Errorf("status = %s, want reverted", got.Status)
	}
}

// §D4.4's denominator is computed live from git, never stored.
func TestReachableCommits_CountsTheDefaultBranchInPeriod(t *testing.T) {
	r := newRepo(t)
	r.commit("old", "2020-01-01T00:00:00Z", map[string]string{"a.go": "package a"})
	r.commit("new1", "", map[string]string{"b.go": "package b"})
	r.commit("new2", "", map[string]string{"c.go": "package c"})

	got, err := ReachableCommits(r.root, "main",
		time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("reachable in period = %d, want the two recent commits", len(got))
	}
}
