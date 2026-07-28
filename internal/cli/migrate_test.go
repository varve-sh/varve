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

// Every command that opens the database must route a v1 project to the
// migration instead of failing obscurely or migrating silently.
func TestCommands_OnAV1DatabaseRouteToMigrate(t *testing.T) {
	setupV1Project(t)

	out, err := runCmd(t, "list")
	if err == nil {
		t.Fatalf("expected a v1 database to be refused, got: %s", out)
	}
	if !strings.Contains(err.Error(), "migrate --from-v1") {
		t.Errorf("error should name the command to run, got: %v", err)
	}
}

func TestMigrateCmd_FromV1(t *testing.T) {
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

	// The project opens normally afterwards.
	if out, err := runCmd(t, "list"); err != nil {
		t.Fatalf("list after migration: %v\n%s", err, out)
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
