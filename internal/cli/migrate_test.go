package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// setupV1Project builds a project whose database is still v1, with n rows.
func setupV1Project(t *testing.T) (root string, ids []string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".memtrace"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := util.GetProjectConfig()
	cfg.Projects[root] = util.ProjectEntry{
		ID: util.GenerateID(), Name: filepath.Base(root),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := util.SaveProjectConfig(cfg); err != nil {
		t.Fatal(err)
	}

	db, err := kernel.OpenDB(util.GetProjectDbPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(kernel.BaselineV1SQLForTest()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, r := range []struct{ kind, content, summary string }{
		{"decision", "Use ULIDs everywhere.", "Use ULIDs everywhere"},
		{"convention", "Wrap errors with %w.", "Wrap errors"},
		{"fact", "CI runs on arm64.", "CI is arm64"},
	} {
		id := util.GenerateID()
		ids = append(ids, id)
		if _, err := db.Exec(`
			INSERT INTO memories (id, type, content, summary, source, confidence, project_id,
			    file_paths, tags, status, created_at, updated_at, access_count)
			VALUES (?, ?, ?, ?, 'user', 1.0, ?, '[]', '[]', 'active', ?, ?, 0)`,
			id, r.kind, r.content, r.summary, cfg.Projects[root].ID, now, now); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	orig, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	return root, ids
}

// While the conversion is gated, a v1 database must keep working. Refusing to
// open it *and* refusing to convert it would leave the user with no path at
// all — worse than the state before this branch.
func TestCommands_OnAV1DatabaseStillServeTheirRows(t *testing.T) {
	setupV1Project(t)

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("a v1 database must keep working while the conversion is gated: %v\n%s", err, out)
	}
	for _, want := range []string{"Use ULIDs everywhere.", "Wrap errors with %w.", "CI runs on arm64."} {
		if !strings.Contains(out, want) {
			t.Errorf("v1 memory %q is not visible after opening; got:\n%s", want, out)
		}
	}
}

// The gate itself: the command refuses, and nothing is moved or created.
func TestMigrateCmd_IsGatedUntilTheReadPathsLand(t *testing.T) {
	root, _ := setupV1Project(t)

	out, err := runCmd(t, "migrate", "--from-v1")
	if err == nil {
		t.Fatalf("expected the gated conversion to refuse, got: %s", out)
	}
	if !strings.Contains(err.Error(), "invisible") {
		t.Errorf("the refusal must say why, got: %v", err)
	}

	for _, name := range []string{"memtrace.v1.bak.db", "migration-v1-export.json"} {
		if _, statErr := os.Stat(filepath.Join(root, ".memtrace", name)); statErr == nil {
			t.Errorf("a refused conversion must not create %s", name)
		}
	}
	if out, err := runCmd(t, "list"); err != nil || !strings.Contains(out, "Use ULIDs everywhere.") {
		t.Errorf("the database must be untouched after a refusal: %v\n%s", err, out)
	}
}

func TestMigrateCmd_FromV1(t *testing.T) {
	defer kernel.SetV2ReadPathsReady(true)()
	root, ids := setupV1Project(t)

	out, err := runCmd(t, "migrate", "--from-v1")
	if err != nil {
		t.Fatalf("migrate --from-v1: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"exported   3", "decisions  2", "notes      1"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q, got:\n%s", want, out)
		}
	}

	// The v1 backup and the export are both kept.
	backup := filepath.Join(root, ".memtrace", "memtrace.v1.bak.db")
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("v1 backup missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".memtrace", "migration-v1-export.json")); err != nil {
		t.Errorf("export missing: %v", err)
	}

	db, err := kernel.OpenDB(util.GetProjectDbPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := kernel.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	ds := kernel.NewDecisionStore(db)
	for _, id := range ids[:2] {
		if _, err := ds.GetDecision(id); err != nil {
			t.Errorf("decision %s did not survive: %v", id, err)
		}
	}
	n, err := kernel.NewNoteStore(db).Get(ids[2])
	if err != nil {
		t.Fatalf("note did not survive: %v", err)
	}
	if n.Status != types.MemoryStatusActive {
		t.Errorf("note status = %s, want active", n.Status)
	}
}

func TestMigrateCmd_RequiresTheFlag(t *testing.T) {
	setupV1Project(t)
	if _, err := runCmd(t, "migrate"); err == nil {
		t.Error("bare `migrate` should explain that --from-v1 is needed")
	}
}

// The canary for the hole the gate exists for. Today a converted database is
// invisible to every product read path, because they all still query
// `memories` while the rows are in `decisions`/`notes` (ADR-0001 §D10 is not
// implemented yet). This asserts that gap out loud, so it is visible in the
// suite instead of hidden behind an exit code.
//
// WHEN THE §D10 READ-PATH PORT LANDS this test will fail. That is its job:
// delete it, flip kernel's v2ReadPathsReady to true, and replace it with the
// assertion that `list` shows every migrated row.
func TestMigrateFromV1_ReadPathHoleIsWhyTheGateExists(t *testing.T) {
	defer kernel.SetV2ReadPathsReady(true)()
	setupV1Project(t)

	if out, err := runCmd(t, "migrate", "--from-v1"); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("list after migration: %v\n%s", err, out)
	}
	for _, gone := range []string{"Use ULIDs everywhere.", "Wrap errors with %w.", "CI runs on arm64."} {
		if strings.Contains(out, gone) {
			t.Fatalf("the v2 read paths appear to be wired up (%q is visible after migration).\n"+
				"Delete this canary, flip kernel's v2ReadPathsReady to true, and assert "+
				"visibility instead.", gone)
		}
	}
}
