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
	// The fixture has to contain a row for every §D9 mapping that produces
	// something other than an active decision, or the read-path visibility test
	// cannot fail on any of them (F15 — this is the test that should have
	// caught F12). Order matters: ids[0..1] are decisions, ids[2] is the note.
	rows := []struct {
		kind, content, summary, status, sourceRef, filePaths, supersededBy string
	}{
		{"decision", "Use ULIDs everywhere.", "Use ULIDs everywhere", "active",
			"git:9f2c1ab", `["internal/util/ulid.go"]`, ""},
		{"convention", "Wrap errors with %w.", "Wrap errors", "active", "", "[]", ""},
		{"fact", "CI runs on arm64.", "CI is arm64", "active", "", "[]", ""},
		// stale -> proposed: needs re-confirmation, and must be visible to a
		// human who can act on it.
		{"decision", "Cache TTL is five minutes.", "Cache TTL", "stale", "", "[]", ""},
		// archived with no successor -> rejected (the documented widening).
		{"decision", "Payloads are XML.", "XML payloads", "archived", "", "[]", ""},
		// archived with a successor -> superseded. Filled in below, once the
		// successor's id is known.
		{"decision", "ULIDs only in the API.", "ULIDs in the API", "archived", "", "[]", "@0"},
	}
	for i, r := range rows {
		id := util.GenerateID()
		ids = append(ids, id)
		supersededBy := any(nil)
		if strings.HasPrefix(r.supersededBy, "@") {
			supersededBy = ids[int(r.supersededBy[1]-'0')]
		}
		sourceRef := any(nil)
		if r.sourceRef != "" {
			sourceRef = r.sourceRef
		}
		if _, err := db.Exec(`
			INSERT INTO memories (id, type, content, summary, source, source_ref, confidence,
			    project_id, file_paths, tags, status, superseded_by, created_at, updated_at,
			    access_count)
			VALUES (?, ?, ?, ?, 'user', ?, 1.0, ?, ?, '[]', ?, ?, ?, ?, 0)`,
			id, r.kind, r.content, r.summary, sourceRef, cfg.Projects[root].ID,
			r.filePaths, r.status, supersededBy, now, now); err != nil {
			t.Fatalf("seeding row %d: %v", i, err)
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

// With the read paths ported, ADR-0001 §D9's specified behaviour is in force
// again: a v1 database is refused on open, with instructions. It has to be —
// the read paths query v2 tables a v1 file does not have.
func TestCommands_OnAV1DatabaseRouteToMigrate(t *testing.T) {
	setupV1Project(t)

	out, err := runCmd(t, "list")
	if err == nil {
		t.Fatalf("expected a v1 database to be refused, got: %s", out)
	}
	if !strings.Contains(err.Error(), "migrate --from-v1") {
		t.Errorf("the error must name the command to run, got: %v", err)
	}
}

// The gate seam survives the port: closing it must still refuse the conversion
// and leave the database untouched, so the deviation stays reviewable and the
// mechanism is available if a future schema change needs the same shape.
func TestMigrateCmd_GateStillRefusesWhenClosed(t *testing.T) {
	defer kernel.SetV2ReadPathsReady(false)()
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
}

func TestMigrateCmd_FromV1(t *testing.T) {
	root, ids := setupV1Project(t)

	out, err := runCmd(t, "migrate", "--from-v1")
	if err != nil {
		t.Fatalf("migrate --from-v1: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"exported   6", "decisions  5", "notes      1"} {
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

// The port's acceptance test, and the replacement for the canary that used to
// live here. The canary asserted that migrated rows were invisible to every
// product read path; it fired the moment §D10 landed, as designed, and this
// took its place.
//
// F1 shipped green because a test asserted an exit code rather than an
// outcome. This asserts the outcome: every migrated row is visible through the
// commands a user actually runs.
func TestMigrateCmd_MigratedRowsAreVisibleToEveryReadPath(t *testing.T) {
	root, ids := setupV1Project(t)

	if out, err := runCmd(t, "migrate", "--from-v1"); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}

	want := []string{"Use ULIDs everywhere.", "Wrap errors with %w.", "CI runs on arm64."}

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("`list` does not show %q after migration; got:\n%s", w, out)
		}
	}

	// Search reaches both classes: ULIDs is a decision, arm64 a note.
	for _, probe := range []struct{ query, expect string }{
		{"ULIDs", "Use ULIDs everywhere."},
		{"arm64", "CI runs on arm64."},
		{"errors", "Wrap errors with %w."},
	} {
		out, err := runCmd(t, "search", probe.query)
		if err != nil {
			t.Fatalf("search %q: %v\n%s", probe.query, err, out)
		}
		if !strings.Contains(out, probe.expect) {
			t.Errorf("`search %s` did not surface %q; got:\n%s", probe.query, probe.expect, out)
		}
	}

	// Export sees them too, and status counts them.
	out, err = runCmd(t, "export")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("`export` omitted %q", w)
		}
	}

	// The mappings that produce something other than an active decision. A stale
	// v1 decision comes over `proposed` and the migration report tells the user
	// to re-confirm it — so it has to be visible to the commands that do that
	// (F12/F15).
	if out, err := runCmd(t, "list"); err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	} else if !strings.Contains(out, "Cache TTL is five minutes.") {
		t.Errorf("the stale v1 decision is invisible to `list` after migrating:\n%s", out)
	}
	if out, err := runCmd(t, "export"); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	} else if !strings.Contains(out, "Cache TTL is five minutes.") {
		t.Errorf("`export` omitted the proposed decision")
	}
	if out, err := runCmd(t, "decision", "pending"); err != nil {
		t.Fatalf("decision pending: %v\n%s", err, out)
	} else if !strings.Contains(out, "Cache TTL") {
		t.Errorf("the confirmation queue does not hold the re-confirmation the report asked for:\n%s", out)
	}

	// Terminal rows are correctly *not* live, but must be reachable by status.
	for _, probe := range []struct{ status, content string }{
		{"rejected", "Payloads are XML."},
		{"superseded", "ULIDs only in the API."},
	} {
		out, err := runCmd(t, "list", "--status", probe.status)
		if err != nil {
			t.Fatalf("list --status %s: %v\n%s", probe.status, err, out)
		}
		if !strings.Contains(out, probe.content) {
			t.Errorf("`list --status %s` does not show %q; got:\n%s", probe.status, probe.content, out)
		}
		if live, err := runCmd(t, "list"); err == nil && strings.Contains(live, probe.content) {
			t.Errorf("a %s decision must not be live:\n%s", probe.status, live)
		}
	}

	// source_ref -> accepting evidence, file_paths -> scope (D9's row mapping).
	db, err := kernel.OpenDB(util.GetProjectDbPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := kernel.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	ds := kernel.NewDecisionStore(db)
	d, err := ds.GetDecision(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Scope) != 1 || d.Scope[0] != "internal/util/ulid.go" {
		t.Errorf("scope = %v, want the v1 file_paths carried verbatim", d.Scope)
	}
	ev, err := ds.Evidence(ids[0])
	if err != nil || len(ev) != 1 {
		t.Fatalf("evidence = %d rows (%v), want 1 from source_ref", len(ev), err)
	}
	if ev[0].Kind != types.EvidenceKindCommit || ev[0].Ref != "9f2c1ab" || !ev[0].Accepting {
		t.Errorf("evidence = %+v, want an accepting commit row for 9f2c1ab", ev[0])
	}

	out, err = runCmd(t, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, "0 memories") {
		t.Errorf("`status` reports an empty store after migration:\n%s", out)
	}
}
