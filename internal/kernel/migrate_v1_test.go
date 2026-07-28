package kernel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

type v1Row struct {
	id           string
	memType      types.MemoryType
	content      string
	summary      string
	source       types.MemorySource
	sourceRef    string
	status       types.MemoryStatus
	supersededBy string
	filePaths    []string
	tags         []string
	topicKey     string
	confidence   float64
	accessCount  int
	embedding    string
}

// populatedV1DB builds a v1 database that exercises every branch of §D9's row
// mapping, and returns its path plus the rows it holds.
func populatedV1DB(t *testing.T) (string, []v1Row) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "memtrace.db")

	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(baselineV1SQL); err != nil {
		t.Fatalf("building v1 schema: %v", err)
	}

	successor := util.GenerateID()
	rows := []v1Row{
		{id: util.GenerateID(), memType: types.MemoryTypeDecision, content: "Use ULIDs for all ids.",
			summary: "Use ULIDs for all ids", source: types.MemorySourceGit, sourceRef: "git:deadbeef",
			status: types.MemoryStatusActive, filePaths: []string{"internal/util/ulid.go"},
			tags: []string{"ids"}, topicKey: "id-scheme", confidence: 0.9, accessCount: 4,
			embedding: `[0.1,0.2]`},
		{id: util.GenerateID(), memType: types.MemoryTypeDecision, content: "Deploy on fly.io.",
			summary: "Deploy on fly.io", source: types.MemorySourceUser, sourceRef: "",
			status: types.MemoryStatusActive, confidence: 1.0},
		{id: util.GenerateID(), memType: types.MemoryTypeDecision, content: "Old auth scheme.",
			summary: "Old auth scheme", source: types.MemorySourceUser,
			status: types.MemoryStatusStale, filePaths: []string{"internal/auth/**"}, confidence: 0.5},
		{id: util.GenerateID(), memType: types.MemoryTypeDecision, content: "Abandoned idea.",
			summary: "Abandoned idea", source: types.MemorySourceAgent,
			status: types.MemoryStatusArchived, confidence: 1.0},
		// Archived with a successor that is itself a decision -> superseded.
		{id: util.GenerateID(), memType: types.MemoryTypeDecision, content: "Superseded rule.",
			summary: "Superseded rule", source: types.MemorySourceUser,
			status: types.MemoryStatusArchived, supersededBy: successor, confidence: 1.0},
		{id: successor, memType: types.MemoryTypeDecision, content: "The replacement rule.",
			summary: "The replacement rule", source: types.MemorySourceUser,
			status: types.MemoryStatusActive, confidence: 1.0},
		{id: util.GenerateID(), memType: types.MemoryTypeConvention, content: "Return wrapped errors.",
			summary: "Return wrapped errors", source: types.MemorySourceUser, sourceRef: "engram-import",
			status: types.MemoryStatusActive, confidence: 1.0},
		{id: util.GenerateID(), memType: types.MemoryTypeFact, content: "The CI runner is arm64.",
			summary: "CI runner is arm64", source: types.MemorySourceUser,
			status: types.MemoryStatusActive, tags: []string{"ci"}, confidence: 1.0,
			embedding: `[0.3,0.4]`},
		{id: util.GenerateID(), memType: types.MemoryTypeFact, content: "An outdated fact.",
			summary: "An outdated fact", source: types.MemorySourceUser,
			status: types.MemoryStatusStale, confidence: 1.0},
		{id: util.GenerateID(), memType: types.MemoryTypeEvent, content: "Session summary: shipped the packer.",
			summary: "Session summary", source: types.MemorySourceAgent,
			status: types.MemoryStatusArchived, tags: []string{"session"}, confidence: 1.0},
	}

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i, r := range rows {
		created := base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339Nano)
		_, err := db.Exec(`
			INSERT INTO memories (id, type, content, summary, source, source_ref, confidence,
			    project_id, file_paths, tags, status, superseded_by,
			    created_at, updated_at, accessed_at, access_count, embedding, topic_key)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.id, string(r.memType), r.content, r.summary, string(r.source),
			nullableString(r.sourceRef), r.confidence, testProject,
			mustJSON(nonNilStrings(r.filePaths)), mustJSON(nonNilStrings(r.tags)),
			string(r.status), nil,
			created, created, nil, r.accessCount,
			nullableString(r.embedding), nullableString(r.topicKey))
		if err != nil {
			t.Fatalf("seeding v1 row %d: %v", i, err)
		}
	}
	// superseded_by is a self-FK, so it is set once every row exists.
	for i, r := range rows {
		if r.supersededBy == "" {
			continue
		}
		if _, err := db.Exec(`UPDATE memories SET superseded_by = ? WHERE id = ?`,
			r.supersededBy, r.id); err != nil {
			t.Fatalf("linking v1 row %d: %v", i, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, rows
}

// ADR-0001 falsifier 5: any count mismatch between v1 export and v2 reimport
// falsifies the framework.
func TestMigrateFromV1_RoundTripFidelity(t *testing.T) {
	defer SetV2ReadPathsReady(true)()
	path, rows := populatedV1DB(t)

	report, err := MigrateFromV1(MigrateV1Options{DBPath: path, ProjectID: testProject})
	if err != nil {
		t.Fatalf("MigrateFromV1: %v", err)
	}

	if report.Exported != len(rows) {
		t.Fatalf("exported %d rows, want %d", report.Exported, len(rows))
	}
	if len(report.Skipped) != 0 {
		t.Fatalf("skipped rows: %+v", report.Skipped)
	}
	if report.Decisions+report.Notes != report.Exported {
		t.Fatalf("decisions %d + notes %d != exported %d",
			report.Decisions, report.Notes, report.Exported)
	}

	// Type split: 7 decisions/conventions, 3 facts/events.
	if report.Decisions != 7 || report.Notes != 3 {
		t.Errorf("decisions = %d, notes = %d; want 7 and 3", report.Decisions, report.Notes)
	}

	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ApplySchema(db); err != nil {
		t.Fatalf("the migrated database must open as v2: %v", err)
	}
	ds := NewDecisionStore(db)
	ns := NewNoteStore(db)

	// No v1 row is lost, and every id survives in exactly one of the two tables.
	for _, r := range rows {
		_, dErr := ds.GetDecision(r.id)
		_, nErr := ns.Get(r.id)
		inDecisions := dErr == nil
		inNotes := nErr == nil
		if inDecisions == inNotes {
			t.Errorf("row %s (%s) landed in %v/%v (decisions/notes); want exactly one",
				r.id, r.memType, inDecisions, inNotes)
		}
	}

	var decCount, noteCount int
	db.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&decCount)
	db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&noteCount)
	if decCount+noteCount != len(rows) {
		t.Fatalf("v2 holds %d decisions + %d notes = %d, want %d",
			decCount, noteCount, decCount+noteCount, len(rows))
	}

	// The export artefact holds every row, in the v1 JSON shape.
	blob, err := os.ReadFile(report.ExportPath)
	if err != nil {
		t.Fatal(err)
	}
	var exported []types.Memory
	if err := json.Unmarshal(blob, &exported); err != nil {
		t.Fatalf("export is not valid v1 JSON: %v", err)
	}
	if len(exported) != len(rows) {
		t.Errorf("export holds %d rows, want %d", len(exported), len(rows))
	}

	// Exactly one migration.completed event, no fabricated per-row histories.
	evs, err := ds.Events(EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != types.EventMigrationDone {
		t.Fatalf("events = %v, want exactly one migration.completed", evs)
	}
	p := evs[0].Payload
	if p["from"].(float64) != 1 || p["to"].(float64) != 2 {
		t.Errorf("migration payload from/to = %v/%v", p["from"], p["to"])
	}
	if int(p["decisions"].(float64)) != report.Decisions || int(p["notes"].(float64)) != report.Notes {
		t.Errorf("migration payload counts disagree with the report: %v", p)
	}
}

func TestMigrateFromV1_RowMapping(t *testing.T) {
	defer SetV2ReadPathsReady(true)()
	path, rows := populatedV1DB(t)
	if _, err := MigrateFromV1(MigrateV1Options{DBPath: path, ProjectID: testProject}); err != nil {
		t.Fatal(err)
	}
	db, _ := OpenDB(path)
	defer db.Close()
	if err := ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	ds := NewDecisionStore(db)
	ns := NewNoteStore(db)

	get := func(i int) *types.Decision {
		t.Helper()
		d, err := ds.GetDecision(rows[i].id)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		return d
	}

	// 0: active decision, git source_ref -> active + accepting commit evidence.
	d := get(0)
	if d.Status != types.StatusActive {
		t.Errorf("row 0 status = %s, want active", d.Status)
	}
	if d.DecidedAt == nil {
		t.Error("row 0: an active decision needs decided_at")
	}
	if d.Kind != types.DecisionKindDecision {
		t.Errorf("row 0 kind = %s", d.Kind)
	}
	if len(d.Scope) != 1 || d.Scope[0] != "internal/util/ulid.go" {
		t.Errorf("row 0 scope = %v; v1 file_paths carry over verbatim", d.Scope)
	}
	if d.Title != "Use ULIDs for all ids" || d.Body != "Use ULIDs for all ids." {
		t.Errorf("row 0 title/body = %q / %q", d.Title, d.Body)
	}
	if d.Confidence != 0.9 || d.AccessCount != 4 || d.TopicKey != "id-scheme" {
		t.Errorf("row 0 lost carried-over fields: %+v", d)
	}
	ev, err := ds.Evidence(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 1 || ev[0].Kind != types.EvidenceKindCommit || ev[0].Ref != "deadbeef" {
		t.Fatalf("row 0 evidence = %+v; want one commit row with ref deadbeef", ev)
	}
	if !ev[0].Accepting {
		t.Error("row 0: v1 source_ref evidence must be accepting (D9), or the D6 revert rule has nothing to fire on")
	}
	var emb sql.NullString
	db.QueryRow(`SELECT embedding FROM decisions WHERE id = ?`, d.ID).Scan(&emb)
	if emb.String != `[0.1,0.2]` {
		t.Errorf("row 0 embedding = %q; content is unchanged so the vector must carry over", emb.String)
	}

	// 1: active, no source_ref -> grandfathered, zero evidence.
	d = get(1)
	if d.Status != types.StatusActive {
		t.Errorf("row 1 status = %s, want active", d.Status)
	}
	if ev, _ := ds.Evidence(d.ID); len(ev) != 0 {
		t.Errorf("row 1 should be unevidenced, got %+v", ev)
	}

	// 2: stale decision -> proposed, listed for triage.
	d = get(2)
	if d.Status != types.StatusProposed {
		t.Errorf("row 2 status = %s, want proposed", d.Status)
	}

	// 3: archived, no successor -> rejected.
	if d = get(3); d.Status != types.StatusRejected {
		t.Errorf("row 3 status = %s, want rejected", d.Status)
	}

	// 4: archived with a decision successor -> superseded, link intact.
	d = get(4)
	if d.Status != types.StatusSuperseded {
		t.Errorf("row 4 status = %s, want superseded", d.Status)
	}
	if d.SupersededBy != rows[5].id {
		t.Errorf("row 4 superseded_by = %q, want %q", d.SupersededBy, rows[5].id)
	}

	// 6: convention keeps its kind and gets no expiry.
	d = get(6)
	if d.Kind != types.DecisionKindConvention {
		t.Errorf("row 6 kind = %s, want convention", d.Kind)
	}
	if d.ExpiresAt != nil {
		t.Errorf("row 6 expires_at = %v, want nil", d.ExpiresAt)
	}
	if ev, _ := ds.Evidence(d.ID); len(ev) != 1 || ev[0].Kind != types.EvidenceKindImport {
		t.Errorf("row 6: a non-git source_ref becomes import evidence, got %+v", ev)
	}

	// 7–9: facts and events become notes with their v1 status verbatim.
	for i, wantStatus := range map[int]types.MemoryStatus{
		7: types.MemoryStatusActive, 8: types.MemoryStatusStale, 9: types.MemoryStatusArchived,
	} {
		n, err := ns.Get(rows[i].id)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		if n.Status != wantStatus {
			t.Errorf("note %d status = %s, want %s", i, n.Status, wantStatus)
		}
		if n.Content != rows[i].content {
			t.Errorf("note %d content changed", i)
		}
	}
	n, _ := ns.Get(rows[9].id)
	if len(n.Tags) != 1 || n.Tags[0] != "session" {
		t.Errorf("session note lost its tag: %v", n.Tags)
	}
	db.QueryRow(`SELECT embedding FROM notes WHERE id = ?`, rows[7].id).Scan(&emb)
	if emb.String != `[0.3,0.4]` {
		t.Errorf("note embedding = %q, want it carried over", emb.String)
	}
}

// The rollback path. There are no down migrations by design (§D9: roll forward
// or restore the backup), so the recovery path is the backup file — and it has
// to be a complete, openable v1 database.
func TestMigrateFromV1_BackupIsACompleteRestorePoint(t *testing.T) {
	defer SetV2ReadPathsReady(true)()
	path, rows := populatedV1DB(t)
	report, err := MigrateFromV1(MigrateV1Options{DBPath: path, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(report.BackupPath); err != nil {
		t.Fatalf("the v1 backup must exist: %v", err)
	}

	backup, err := OpenDB(report.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()

	var n int
	if err := backup.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&n); err != nil {
		t.Fatalf("the backup must still be a readable v1 database: %v", err)
	}
	if n != len(rows) {
		t.Errorf("backup holds %d rows, want %d", n, len(rows))
	}
	// It is still recognisably v1, so restoring it over the new file returns
	// the user to exactly where they started.
	legacy, err := isLegacyV1(backup)
	if err != nil || !legacy {
		t.Errorf("backup is not a v1 database (legacy=%v, err=%v)", legacy, err)
	}

	// Restoring is a file copy: put it back and the kernel refuses it as v1
	// again, which is the pre-migration state.
	restored := filepath.Join(t.TempDir(), "restored.db")
	blob, err := os.ReadFile(report.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restored, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	rdb, err := OpenDB(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	if err := ApplySchema(rdb); !errors.Is(err, types.ErrLegacyDatabase) {
		t.Errorf("a restored backup must read as v1 again, got %v", err)
	}
}

func TestMigrateFromV1_RefusesANonV1Database(t *testing.T) {
	defer SetV2ReadPathsReady(true)()
	path := filepath.Join(t.TempDir(), "v2.db")
	db, _ := OpenDB(path)
	if err := ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := MigrateFromV1(MigrateV1Options{DBPath: path}); err == nil {
		t.Fatal("migrating an already-v2 database must fail")
	}
	// Nothing was moved.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the database must be left in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "v2.v1.bak.db")); err == nil {
		t.Error("a failed migration must not create a backup")
	}
}

// A v1 row with neither summary nor content cannot become a decision (a title
// is required). It is skipped, named in the report, and counted.
func TestMigrateFromV1_SkippedRowsAreCountedAndNamed(t *testing.T) {
	defer SetV2ReadPathsReady(true)()
	dir := t.TempDir()
	path := filepath.Join(dir, "memtrace.db")
	db, _ := OpenDB(path)
	if _, err := db.Exec(baselineV1SQL); err != nil {
		t.Fatal(err)
	}
	bad := util.GenerateID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO memories (id, type, content, summary, source, confidence, project_id,
		    file_paths, tags, status, created_at, updated_at, access_count)
		VALUES (?, 'decision', '', '', 'user', 1.0, ?, '[]', '[]', 'active', ?, ?, 0)`,
		bad, testProject, now, now); err != nil {
		t.Fatal(err)
	}
	db.Close()

	report, err := MigrateFromV1(MigrateV1Options{DBPath: path, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].ID != bad {
		t.Fatalf("skipped = %+v, want the untitled row", report.Skipped)
	}
	if report.Decisions != 0 || report.Notes != 0 {
		t.Errorf("nothing should have been imported: %+v", report)
	}
	if report.Decisions+report.Notes+len(report.Skipped) != report.Exported {
		t.Error("skipped rows must still balance the count check")
	}
}

func TestMigrateFromV1_MigratedDatabaseIsFullyUsable(t *testing.T) {
	defer SetV2ReadPathsReady(true)()
	path, rows := populatedV1DB(t)
	if _, err := MigrateFromV1(MigrateV1Options{DBPath: path, ProjectID: testProject}); err != nil {
		t.Fatal(err)
	}
	db, _ := OpenDB(path)
	defer db.Close()
	if err := ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	ds := NewDecisionStore(db)

	// A grandfathered active decision still obeys the state machine.
	migrated := rows[1].id
	if err := ds.Revert(migrated, RevertOptions{Via: "human"}); err != nil {
		t.Fatalf("Revert on a migrated decision: %v", err)
	}
	if _, err := ds.Accept(migrated, AcceptOptions{Force: true}); !errors.Is(err, types.ErrIllegalTransition) {
		t.Errorf("a migrated-then-reverted decision must be terminal, got %v", err)
	}

	// And a new decision can be proposed and accepted on top.
	in := baseInput()
	in.TopicKey = "post-migration"
	d, err := ds.Propose(in)
	if err != nil {
		t.Fatal(err)
	}
	addCommitEvidence(t, ds, d.ID, "sha-post")
	if _, err := ds.Accept(d.ID, AcceptOptions{}); err != nil {
		t.Fatal(err)
	}
}
