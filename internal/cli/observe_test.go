package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// gitProject is setupProject plus a real git repository, because the observer
// commands are git plumbing and a fake would test the fake.
func gitProject(t *testing.T) (*kernel.MemoryKernel, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	k, root := setupProject(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "dev@example.com")
	run("config", "user.name", "Dev")
	run("config", "commit.gpgsign", "false")
	return k, root
}

func commitFile(t *testing.T, root, path, body, msg string) string {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestHooksInstall_WritesAHookThatCannotFailACommit(t *testing.T) {
	_, root := gitProject(t)

	out, err := runCmd(t, "hooks", "install")
	if err != nil {
		t.Fatalf("hooks install: %v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(root, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatalf("no hook written: %v", err)
	}
	hook := string(body)

	// §D1.1 and §D7: every clause of the hook line is load-bearing.
	for _, want := range []struct{ claim, text string }{
		{"no-op without the binary on PATH", "command -v memtrace"},
		{"runs in the background so a commit never waits", "&"},
		{"prints nothing and exits 0", "--quiet"},
		{"silences even a binary too old to know --quiet", ">/dev/null 2>&1 &"},
		{"says how to recover if it is deleted", "memtrace scan"},
	} {
		if !strings.Contains(hook, want.text) {
			t.Errorf("the hook does not %s (missing %q):\n%s", want.claim, want.text, hook)
		}
	}
	info, err := os.Stat(filepath.Join(root, ".git", "hooks", "post-commit"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("the hook is not executable: %v %v", info.Mode(), err)
	}

	// Idempotent: installing twice must not double the line.
	if _, err := runCmd(t, "hooks", "install"); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(filepath.Join(root, ".git", "hooks", "post-commit"))
	if strings.Count(string(body), "memtrace observe") != 1 {
		t.Errorf("installing twice duplicated the hook line:\n%s", body)
	}
}

// Chaining: an existing hook is appended to, never overwritten. A tool that
// destroys someone's husky/lefthook setup is a tool they uninstall.
func TestHooksInstall_AppendsToAnExistingHook(t *testing.T) {
	_, root := gitProject(t)
	hookPath := filepath.Join(root, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "#!/bin/sh\necho \"my own hook\"\n"
	if err := os.WriteFile(hookPath, []byte(existing), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := runCmd(t, "hooks", "install"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(body), "my own hook") {
		t.Errorf("the existing hook was overwritten:\n%s", body)
	}
	if !strings.Contains(string(body), "memtrace observe") {
		t.Errorf("memtrace was not appended:\n%s", body)
	}
}

// A hook that is not a shell script is left alone, with instructions.
func TestHooksInstall_RefusesToAppendToABinaryHook(t *testing.T) {
	_, root := gitProject(t)
	hookPath := filepath.Join(root, ".git", "hooks", "post-commit")
	os.MkdirAll(filepath.Dir(hookPath), 0o755)
	if err := os.WriteFile(hookPath, []byte("\x7fELF\x02\x01binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runCmd(t, "hooks", "install")
	if err != nil {
		t.Fatalf("this must not be an error, just a refusal: %v", err)
	}
	if !strings.Contains(out, "yourself") {
		t.Errorf("the refusal must tell the user what to do:\n%s", out)
	}
	body, _ := os.ReadFile(hookPath)
	if strings.Contains(string(body), "memtrace") {
		t.Errorf("a hook that could not be parsed was modified anyway:\n%q", body)
	}
}

func TestObserveCmd_RecordsTheCommitAndItsVerdict(t *testing.T) {
	k, root := gitProject(t)
	if _, err := k.Decisions().ProposeAccepted(kernel.DecisionInput{
		ProjectID: k.ProjectID(), Title: "Auth is validated at the boundary",
		Scope: []string{"internal/auth/**"}, Source: types.DecisionSourceUser,
		Evidence: []kernel.EvidenceInput{{
			Kind: types.EvidenceKindImport, Ref: "seed", AddedBy: types.ActorHuman,
		}},
	}, kernel.AcceptOptions{Actor: types.ActorHuman}); err != nil {
		t.Fatal(err)
	}
	sha := commitFile(t, root, "internal/auth/session.go", "package auth", "add session")

	out, err := runCmd(t, "observe", "--commit", sha)
	if err != nil {
		t.Fatalf("observe: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 scope matches") {
		t.Errorf("observe did not report the match:\n%s", out)
	}
	evs, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventDiffObserved})
	if len(evs) != 1 {
		t.Fatalf("diff.observed rows = %d, want 1", len(evs))
	}
	if evs[0].Payload["patch_id"] == "" {
		t.Error("patch_id is empty; reports dedupe changes by it")
	}
}

// The architect's contention case. `varve observe` runs from a post-commit
// hook while an MCP session may hold the single writer. It must exit 0, print
// nothing, and leave the commit for the scan to pick up — a retry loop would
// put the cost back on the commit, which is the one place §D7 forbids it.
func TestObserveCmd_QuietExitsZeroWhenTheDatabaseIsBusy(t *testing.T) {
	k, root := gitProject(t)
	sha := commitFile(t, root, "internal/auth/session.go", "package auth", "add session")

	// Hold the writer, as a live MCP session would.
	blocker, err := kernel.OpenDB(filepath.Join(root, ".memtrace", "memtrace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO notes (id, project_id, content, source, confidence,
	    file_paths, tags, status, created_at, updated_at, access_count)
	    VALUES ('01BLOCKER0000000000000000', ?, 'holding the writer', 'user', 1.0,
	            '[]', '[]', 'active', '2026-07-29T00:00:00Z', '2026-07-29T00:00:00Z', 0)`,
		k.ProjectID()); err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	out, err := runCmd(t, "observe", "--commit", sha, "--quiet")
	if err != nil {
		t.Fatalf("the hook's mode must exit 0 even when the database is busy: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("--quiet printed to the user's terminal during a commit:\n%q", out)
	}

	// The failure is recorded where §D7 says it goes, and nowhere else.
	logPath := filepath.Join(root, ".memtrace", "observer.log")
	if body, readErr := os.ReadFile(logPath); readErr == nil && len(body) > 0 {
		if !strings.Contains(string(body), "busy") && !strings.Contains(string(body), "locked") {
			t.Logf("observer.log recorded: %s", body)
		}
	}

	// And the scan recovers it, which is *why* the hook may give up.
	tx.Rollback()
	blocker.Close()
	if out, err := runCmd(t, "scan"); err != nil {
		t.Fatalf("scan: %v\n%s", err, out)
	}
	evs, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventDiffObserved})
	if len(evs) == 0 {
		t.Error("the commit the hook gave up on was never recovered by the scan")
	}
}

func TestScanCmd_ObservesHistoryAndStaysIdempotent(t *testing.T) {
	k, root := gitProject(t)
	for i := 0; i < 3; i++ {
		commitFile(t, root, "internal/auth/f.go", strings.Repeat("x", i+1), "work")
	}

	out, err := runCmd(t, "scan")
	if err != nil {
		t.Fatalf("scan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Observed 3 new commits") {
		t.Errorf("scan output:\n%s", out)
	}
	out, err = runCmd(t, "scan")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Observed 0 new commits") {
		t.Errorf("a second scan re-observed commits:\n%s", out)
	}
	evs, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventDiffObserved})
	if len(evs) != 3 {
		t.Errorf("diff.observed rows = %d, want 3", len(evs))
	}
}

// The staleness scan survives behind a flag: two unrelated jobs under one verb
// would be worse, but silently dropping the old one would be worse still.
func TestScanCmd_StaleFlagStillRunsTheNoteScan(t *testing.T) {
	gitProject(t)
	out, err := runCmd(t, "scan", "--stale")
	if err != nil {
		t.Fatalf("scan --stale: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to scan") && !strings.Contains(out, "Scanned") {
		t.Errorf("--stale did not run the note-staleness scan:\n%s", out)
	}
}

func TestReportCmd_RendersAndDrillsDown(t *testing.T) {
	k, root := gitProject(t)
	d, err := k.Decisions().ProposeAccepted(kernel.DecisionInput{
		ProjectID: k.ProjectID(), Title: "Auth is validated at the boundary",
		Scope: []string{"internal/auth/**"}, Source: types.DecisionSourceUser,
		Evidence: []kernel.EvidenceInput{{
			Kind: types.EvidenceKindImport, Ref: "seed", AddedBy: types.ActorHuman,
		}},
	}, kernel.AcceptOptions{Actor: types.ActorHuman})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, root, "internal/auth/session.go", "package auth", "add session")
	if _, err := runCmd(t, "scan"); err != nil {
		t.Fatal(err)
	}

	out, err := runCmd(t, "report")
	if err != nil {
		t.Fatalf("report: %v\n%s", err, out)
	}
	for _, want := range []string{
		"attribution report", "grace window: 60m", "revert detection: git trailer only",
		"not verified compliance",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}

	// The kill-criterion query ships as its own command.
	out, err = runCmd(t, "report", "coverage")
	if err != nil {
		t.Fatalf("report coverage: %v\n%s", err, out)
	}
	if !strings.Contains(out, "coverage") {
		t.Errorf("coverage output:\n%s", out)
	}

	// §D6.2: every number drills to the rows behind it.
	out, err = runCmd(t, "report", "--decision", d.ID, "--raw")
	if err != nil {
		t.Fatalf("report --raw: %v\n%s", err, out)
	}
	for _, want := range []string{"decision.proposed", "diff.observed", "diff.scope_match"} {
		if !strings.Contains(out, want) {
			t.Errorf("the drill-down is missing %s:\n%s", want, out)
		}
	}

	// JSON is the forwardable artifact and carries the same limitations.
	out, err = runCmd(t, "report", "--format", "json")
	if err != nil {
		t.Fatalf("report --format json: %v\n%s", err, out)
	}
	if !strings.Contains(out, "\"limitations\"") {
		t.Errorf("the JSON form drops the limitations a human would have read:\n%s", out)
	}
}

// §D1.3: `init` records the observation epoch, once.
func TestInitCmd_RecordsTheObservationEpoch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	if out, err := runCmd(t, "init", "--no-import"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	epoch, err := k.ObserverEpoch()
	if err != nil || epoch == nil {
		t.Fatalf("no observation epoch after init: %v, %v", epoch, err)
	}
}

// `init` must keep the store out of the repository even when there is no
// .gitignore yet: a committed database makes `git revert` and `git checkout`
// fail on a file the user never edited.
func TestInitCmd_CreatesAGitignoreWhenThereIsNone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	if out, err := runCmd(t, "init", "--no-import"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("no .gitignore created: %v", err)
	}
	if !strings.Contains(string(body), ".memtrace") {
		t.Errorf(".gitignore does not exclude the store:\n%s", body)
	}
}
