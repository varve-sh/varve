package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/types"
)

func writeRules(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const rulesBody = `# Style

Use tabs, not spaces.

# Testing

Tests before features on anything touching the data model.

# Commits

Small commits, one logical change each.
`

func TestImportRules_EndToEnd(t *testing.T) {
	k, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)

	out, err := runCmd(t, "import", "rules", "--yes")
	if err != nil {
		t.Fatalf("import rules: %v\n%s", err, out)
	}
	if !strings.Contains(out, "3 decision candidates") {
		t.Errorf("expected three convention candidates:\n%s", out)
	}
	// The quarantine has to be visible where a user reads it, not just true in
	// the database.
	if !strings.Contains(out, "all PROPOSED") {
		t.Errorf("report does not say imported rows are proposed:\n%s", out)
	}
	if !strings.Contains(out, "undo anytime: varve import undo") {
		t.Errorf("report does not offer the undo path:\n%s", out)
	}

	ds, err := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 {
		t.Fatalf("want 3 decisions, got %d", len(ds))
	}
	for _, d := range ds {
		if string(d.Status) != "proposed" || string(d.Kind) != "convention" {
			t.Fatalf("%s is %s/%s, want proposed/convention", d.ID, d.Status, d.Kind)
		}
	}

	// Re-run: falsifier 3 — zero new rows from an unchanged source.
	out, err = runCmd(t, "import", "rules", "--yes")
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "3 skipped") {
		t.Errorf("re-import should skip all three:\n%s", out)
	}
	ds, _ = k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if len(ds) != 3 {
		t.Fatalf("re-import created rows: %d decisions", len(ds))
	}

	// Undo, then confirm the rows no longer influence anything.
	out, err = runCmd(t, "import", "undo")
	if err != nil {
		t.Fatalf("undo: %v\n%s", err, out)
	}
	if !strings.Contains(out, "3 proposals rejected") {
		t.Errorf("undo output = %s", out)
	}
	ds, _ = k.Decisions().ListDecisions(kernel.DecisionFilter{})
	for _, d := range ds {
		if string(d.Status) != "rejected" {
			t.Fatalf("%s survived undo as %s", d.ID, d.Status)
		}
	}
}

func TestImportRules_AsNotesAndDryRun(t *testing.T) {
	k, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)

	out, err := runCmd(t, "import", "rules", "--as-notes", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would import: 3 notes") {
		t.Errorf("dry run should preview three notes:\n%s", out)
	}
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	notes, _ := k.List(types.ListOptions{})
	if len(ds) != 0 || len(notes) != 0 {
		t.Fatalf("dry run wrote %d decisions and %d notes", len(ds), len(notes))
	}
}

func TestImportRules_ConfirmationIsRequired(t *testing.T) {
	k, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)

	out, err := runCmdStdin(t, "n\n", "import", "rules")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Cancelled") {
		t.Errorf("declining the prompt should cancel:\n%s", out)
	}
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if len(ds) != 0 {
		t.Fatalf("cancelled import still wrote %d rows", len(ds))
	}
}

func TestLintCmd_PrintsScoreAndRecordsEvent(t *testing.T) {
	k, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)
	if out, err := runCmd(t, "import", "rules", "--yes"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}

	out, err := runCmd(t, "lint")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	if !strings.Contains(out, "corpus health:") {
		t.Errorf("lint printed no score line:\n%s", out)
	}
	// Three blocks is below the n=10 floor: findings only, no number.
	if !strings.Contains(out, "corpus too small to score") {
		t.Errorf("a 3-entry corpus must not be scored:\n%s", out)
	}
	if !strings.Contains(out, "adoption (not scored") {
		t.Errorf("lint dropped the adoption section:\n%s", out)
	}

	var lints int
	if err := k.Decisions().DB().QueryRow(
		`SELECT COUNT(*) FROM events WHERE kind = 'lint.completed'`).Scan(&lints); err != nil {
		t.Fatal(err)
	}
	// One from the import's own report, one from `lint`.
	if lints != 2 {
		t.Errorf("lint.completed events = %d, want 2", lints)
	}
	_ = root
}

func TestLintCmd_RawEmitsRowsForEveryFinding(t *testing.T) {
	_, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)
	if out, err := runCmd(t, "import", "rules", "--yes"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	out, err := runCmd(t, "lint", "--raw")
	if err != nil {
		t.Fatalf("lint --raw: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"id": "L1"`) || !strings.Contains(out, `"checked"`) {
		t.Errorf("--raw did not emit the per-check rows:\n%s", out)
	}
}

func TestImportBare_ListsSourcesWithoutImporting(t *testing.T) {
	k, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)

	out, err := runCmd(t, "import")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "CLAUDE.md") || !strings.Contains(out, "3 blocks") {
		t.Errorf("probe listing = %s", out)
	}
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if len(ds) != 0 {
		t.Fatalf("probing imported %d rows — listing must never write", len(ds))
	}
}

// F47, guarded against drift: the constant `init` actually writes must be the
// one the importer skips. A test that hard-codes its own copy of the snippet
// would keep passing after someone edits setup.go.
func TestImportRules_SkipsVarvesOwnInstructionBlock(t *testing.T) {
	k, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody+claudeMdSnippet)

	out, err := runCmd(t, "import", "rules", "--yes")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	ds, err := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 {
		t.Fatalf("want the 3 project blocks, got %d", len(ds))
	}
	for _, d := range ds {
		if strings.Contains(d.Body, "memory_recall") {
			t.Fatalf("varve's own instruction block was proposed as a convention: %s", d.Title)
		}
	}
}

func TestImportUndo_UnknownBatchIsRefused(t *testing.T) {
	k, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)
	if out, err := runCmd(t, "import", "rules", "--yes"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	out, err := runCmd(t, "import", "undo", "01FAKEBATCH0000000000000000")
	if err == nil {
		t.Fatalf("undo of an unknown batch succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no import batch with that id") {
		t.Errorf("error = %v", err)
	}
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	for _, d := range ds {
		if string(d.Status) != "proposed" {
			t.Fatalf("the refused undo still moved %s to %s", d.ID, d.Status)
		}
	}
}

// Amendment 1: the empty-batch skip is announced.
func TestImportUndo_AnnouncesTheSkippedBatch(t *testing.T) {
	_, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)
	if out, err := runCmd(t, "import", "rules", "--yes"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if out, err := runCmd(t, "import", "rules", "--yes"); err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	out, err := runCmd(t, "import", "undo")
	if err != nil {
		t.Fatalf("undo: %v\n%s", err, out)
	}
	if !strings.Contains(out, "created nothing (all rows already present); undoing") {
		t.Errorf("the redirect was silent:\n%s", out)
	}
}

// F49: with no terminal attached the prompt must not block, and must name the
// flag that means yes.
func TestImport_NonInteractiveDoesNotBlock(t *testing.T) {
	k, root := setupProject(t)
	writeRules(t, root, "CLAUDE.md", rulesBody)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close() // an *os.File that is not a terminal, like a CI job's stdin
	defer r.Close()

	done := make(chan struct{})
	var out string
	go func() {
		defer close(done)
		out, _ = runCmdWith(t, r, "import", "rules")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("import blocked on stdin with no terminal attached")
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("the non-interactive path does not name --yes:\n%s", out)
	}
	ds, _ := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if len(ds) != 0 {
		t.Fatalf("a declined import wrote %d rows", len(ds))
	}
}
