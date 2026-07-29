package importer

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// fixtureClaudeMem builds a claude-mem-shaped store. The fixture is
// deliberately richer than the assertions need — an empty observation, a
// title-less row, out-of-order ids — because a fixture that cannot reach the
// failure cannot fail the test.
func fixtureClaudeMem(t *testing.T, cols string, rows [][]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-mem.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE observations (` + cols + `)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		ph := "?"
		for i := 1; i < len(r); i++ {
			ph += ", ?"
		}
		if _, err := db.Exec(`INSERT INTO observations VALUES (`+ph+`)`, r...); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestImportClaudeMem_NotesOnlyWithStableRefs(t *testing.T) {
	path := fixtureClaudeMem(t, "id TEXT PRIMARY KEY, text TEXT, title TEXT", [][]any{
		{"3", "Refactored the auth middleware during session 3.", "auth session"},
		{"1", "Considered switching to pnpm; did not.", nil},
		{"2", "   ", "empty body must be dropped"},
	})

	got, err := ImportClaudeMem(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 candidates (blank body dropped), got %d", len(got))
	}
	// Ordered by id, so re-runs produce identical output.
	if got[0].SourceRef != "claude-mem:1" || got[1].SourceRef != "claude-mem:3" {
		t.Fatalf("source refs = %q, %q", got[0].SourceRef, got[1].SourceRef)
	}
	for _, c := range got {
		// §D1's honesty rule: claude-mem yields notes only, always.
		if c.AsDecision {
			t.Fatalf("claude-mem candidate %q was promoted to a decision", c.SourceRef)
		}
	}
}

func TestImportClaudeMem_RefusesUnknownSchema(t *testing.T) {
	path := fixtureClaudeMem(t, "id TEXT, blob_payload TEXT", [][]any{{"1", "x"}})
	_, err := ImportClaudeMem(path)
	if err == nil {
		t.Fatal("want a refusal for an unrecognised schema, got a successful import")
	}
	if p := ProbeClaudeMem(path); p.Refusal == nil || !p.Available {
		t.Fatalf("probe should report available-but-refused, got %+v", p)
	}
}

func fixtureEngram(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engram.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE memories (
		id TEXT PRIMARY KEY, what TEXT, why TEXT, "where" TEXT, type TEXT)`); err != nil {
		t.Fatal(err)
	}
	rows := [][]any{
		{"a1", "Use sqlc for queries", "Hand-written scanning drifted from the schema", `["internal/db/*.go", "internal/store/*.go"]`, "decision"},
		{"a2", "Tests colocate with code", "", "internal/**/*_test.go", "convention"},
		{"a3", "The deploy failed on Tuesday", "disk full", "", "observation"},
		{"a4", "", "orphan why with no what", "", "decision"},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO memories VALUES (?, ?, ?, ?, ?)`, r...); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestImportEngram_PromotesOnlyOnSourceType(t *testing.T) {
	got, err := ImportEngram(fixtureEngram(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 candidates (empty `what` dropped), got %d", len(got))
	}
	byRef := map[string]Candidate{}
	for _, c := range got {
		byRef[c.SourceRef] = c
	}
	d := byRef["engram:a1"]
	if !d.AsDecision || d.Kind != "decision" {
		t.Fatalf("a1 should be a proposed decision, got %+v", d)
	}
	if d.Content != "Hand-written scanning drifted from the schema" {
		t.Fatalf("Why should become the body, got %q", d.Content)
	}
	if len(d.Scope) != 2 || d.Scope[0] != "internal/db/*.go" {
		t.Fatalf("Where should become exact-path scope globs, got %v", d.Scope)
	}
	if c := byRef["engram:a2"]; c.Kind != "convention" {
		t.Fatalf("a2 kind = %q, want convention", c.Kind)
	}
	// The source typed a3 as an observation: no decision signal, so a note.
	if c := byRef["engram:a3"]; c.AsDecision {
		t.Fatal("an untyped engram row was promoted — decisions must follow the source's own type")
	}
}

func TestImportEngram_AsNotesDemotesEverything(t *testing.T) {
	got, err := ImportEngram(fixtureEngram(t), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c.AsDecision {
			t.Fatalf("--as-notes left %s promoted", c.SourceRef)
		}
	}
}

func TestImportRulesFile_BlocksAndContentHash(t *testing.T) {
	root := t.TempDir()
	body := "# Style\n\nUse tabs.\n\n# Testing\n\nTests before features.\n" +
		"\n```\n# not a heading, it is inside a fence\n```\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ImportRulesFile(root, "CLAUDE.md", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 blocks (the fenced # is not a heading), got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if !c.AsDecision || c.Kind != "convention" {
			t.Fatalf("rules-file block should be a proposed convention: %+v", c)
		}
		if len(c.Scope) != 0 {
			t.Fatalf("rules files state no globs; scope must stay empty, got %v", c.Scope)
		}
	}

	// Idempotency: the ref is a content hash, so a re-read is byte-identical…
	again, _ := ImportRulesFile(root, "CLAUDE.md", false)
	if again[0].SourceRef != got[0].SourceRef {
		t.Fatal("source ref is not stable across reads")
	}
	// …and editing a block retires the old ref (§D2.2: the text changed).
	edited := "# Style\n\nUse spaces.\n\n# Testing\n\nTests before features.\n" +
		"\n```\n# not a heading, it is inside a fence\n```\n"
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(edited), 0o644)
	after, _ := ImportRulesFile(root, "CLAUDE.md", false)
	if after[0].SourceRef == got[0].SourceRef {
		t.Fatal("an edited block kept its old source_ref")
	}
	if after[1].SourceRef != got[1].SourceRef {
		t.Fatal("an untouched block changed its source_ref")
	}
}

func TestImportRulesFile_PlainTextSplitsOnBlankLines(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("Never use any.\n\nPrefer early returns.\n"), 0o644)
	got, err := ImportRulesFile(root, ".cursorrules", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 paragraphs, got %d", len(got))
	}
}

func TestImportCursorRules_GlobsScopesAndDemotions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".cursor", "rules")
	os.MkdirAll(dir, 0o755)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-scoped.mdc", "---\ndescription: Go style\nglobs: internal/**/*.go, cmd/*.go\n---\nUse errors.Is.\n")
	write("b-always.mdc", "---\ndescription: Repo wide\nalwaysApply: true\nglobs: **\n---\nBe terse.\n")
	write("c-bad.mdc", "---\ndescription: Broken\nglobs: internal/[unclosed\n---\nSomething.\n")

	got, warnings, err := ImportCursorRules(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rules, got %d", len(got))
	}
	if len(got[0].Scope) != 2 || got[0].Scope[1] != "cmd/*.go" {
		t.Fatalf("globs should become scope, got %v", got[0].Scope)
	}
	// alwaysApply maps to scope=[] — the canonical repo-wide form. Mapping it to
	// ["**"] would make L10 flag every rule the source told us was repo-wide.
	if len(got[1].Scope) != 0 {
		t.Fatalf("alwaysApply should give scope=[], got %v", got[1].Scope)
	}
	if len(got[2].Scope) != 0 || len(warnings) != 1 {
		t.Fatalf("an invalid glob should demote to unscoped with one warning; scope=%v warnings=%v",
			got[2].Scope, warnings)
	}
}

func TestProbeRules_CountsWithoutImporting(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# A\n\nx\n\n# B\n\ny\n"), 0o644)
	probes := ProbeRules(root)
	if len(probes) != 1 || probes[0].Count != 2 || probes[0].Detail != "2 blocks" {
		t.Fatalf("probe = %+v", probes)
	}
}
