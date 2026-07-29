package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
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
