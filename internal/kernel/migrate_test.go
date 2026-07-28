package kernel

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// freshDB returns an empty database brought to the latest schema version.
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	return db
}

func TestApplySchema_FreshDBReachesLatestVersion(t *testing.T) {
	db := freshDB(t)

	got, err := currentVersion(db)
	if err != nil {
		t.Fatal(err)
	}
	if got != LatestSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, LatestSchemaVersion())
	}

	for _, table := range []string{
		"schema_migrations", "memories", "decisions", "evidence", "notes",
		"events", "decisions_fts", "notes_fts",
	} {
		ok, err := hasTable(db, table)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("table %q missing", table)
		}
	}
}

func TestApplySchema_IsIdempotent(t *testing.T) {
	db := freshDB(t)
	for i := 0; i < 3; i++ {
		if err := ApplySchema(db); err != nil {
			t.Fatalf("re-apply %d: %v", i, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(migrations) {
		t.Fatalf("schema_migrations rows = %d, want %d", n, len(migrations))
	}
}

func TestApplySchema_RecordsEveryMigration(t *testing.T) {
	db := freshDB(t)
	rows, err := db.Query(`SELECT version, name, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	i := 0
	for rows.Next() {
		var version int
		var name, appliedAt string
		if err := rows.Scan(&version, &name, &appliedAt); err != nil {
			t.Fatal(err)
		}
		if version != migrations[i].version || name != migrations[i].name {
			t.Errorf("row %d = (%d, %q), want (%d, %q)",
				i, version, name, migrations[i].version, migrations[i].name)
		}
		if appliedAt == "" {
			t.Errorf("row %d has empty applied_at", i)
		}
		i++
	}
	if i != len(migrations) {
		t.Fatalf("read %d rows, want %d", i, len(migrations))
	}
}

func TestMigrationsAreOrderedAndUnique(t *testing.T) {
	seen := map[int]bool{}
	prev := 0
	for _, m := range migrations {
		if m.version <= prev {
			t.Fatalf("migration %d (%s) is out of order after %d", m.version, m.name, prev)
		}
		if seen[m.version] {
			t.Fatalf("duplicate migration version %d", m.version)
		}
		seen[m.version] = true
		prev = m.version
	}
}

// A partially failing migration must leave no schema_migrations row: the
// version is recorded in the same transaction as the DDL.
func TestApplyOne_FailureIsNotRecorded(t *testing.T) {
	db := freshDB(t)
	bad := migration{
		version: 999,
		name:    "deliberately_broken",
		up:      execScript(`CREATE TABLE ok_so_far (x); CREATE TABLE syntax error here;`),
	}
	if err := applyOne(db, bad); err == nil {
		t.Fatal("expected the broken migration to fail")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 999`).Scan(&n)
	if n != 0 {
		t.Errorf("failed migration recorded in schema_migrations")
	}
	ok, _ := hasTable(db, "ok_so_far")
	if ok {
		t.Errorf("partial DDL from a failed migration was committed")
	}
}

// ADR-0001 D9: a v1 database is never auto-migrated on open.
func TestApplySchema_RefusesLegacyV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(baselineV1SQL); err != nil {
		t.Fatalf("building v1 db: %v", err)
	}

	err = ApplySchema(db)
	if !errors.Is(err, types.ErrLegacyDatabase) {
		t.Fatalf("ApplySchema on v1 db = %v, want ErrLegacyDatabase", err)
	}
	if !strings.Contains(err.Error(), "migrate --from-v1") {
		t.Errorf("error should tell the user what to run, got: %v", err)
	}
	ok, _ := hasTable(db, "decisions")
	if ok {
		t.Errorf("v1 database was migrated in place")
	}
}

// The PRAGMAs of §D8 must actually hold on pooled connections, not just on
// whichever connection happened to execute them.
func TestOpenDB_PragmasHoldAcrossPooledConnections(t *testing.T) {
	db := freshDB(t)
	for i := 0; i < 8; i++ {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var fk int
		if err := conn.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != 1 {
			t.Fatalf("conn %d: foreign_keys = %d, want 1", i, fk)
		}
		conn.Close()
	}

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

// The gate seam, exercised closed. It shipped closed while the §D10 read paths
// were missing; it is open now. Keeping both sides tested means the mechanism
// stays trustworthy if a future change needs it again.
func TestApplySchema_GatedLeavesAV1DatabaseUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(baselineV1SQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories (id, type, content, project_id, created_at, updated_at)
		VALUES ('m1', 'fact', 'a v1 memory', 'p1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	defer SetV2ReadPathsReady(false)()
	if err := ApplySchema(db); err != nil {
		t.Fatalf("a gated v1 database must open, got: %v", err)
	}

	// Nothing was migrated: no v2 tables, no schema_migrations, row intact.
	for _, table := range []string{"schema_migrations", "decisions", "notes", "events"} {
		if ok, _ := hasTable(db, table); ok {
			t.Errorf("table %q was created on a v1 database — that is an auto-migration", table)
		}
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM memories WHERE id = 'm1'`).Scan(&content); err != nil {
		t.Fatalf("the v1 row must still be readable: %v", err)
	}
	if content != "a v1 memory" {
		t.Errorf("content = %q", content)
	}

	// With the gate open — the shipping state — D9's specified behaviour holds.
	SetV2ReadPathsReady(true)
	if err := ApplySchema(db); !errors.Is(err, types.ErrLegacyDatabase) {
		t.Fatalf("with the read paths ready, a v1 database must be refused: %v", err)
	}
}

func TestMigrateFromV1_IsGatedOnTheReadPaths(t *testing.T) {
	defer SetV2ReadPathsReady(false)()
	path := filepath.Join(t.TempDir(), "v1.db")
	db, _ := OpenDB(path)
	if _, err := db.Exec(baselineV1SQL); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err := MigrateFromV1(MigrateV1Options{DBPath: path, ProjectID: "p1"})
	if !errors.Is(err, types.ErrMigrationNotReady) {
		t.Fatalf("err = %v, want ErrMigrationNotReady", err)
	}
	if _, statErr := os.Stat(path + ".v1.bak.db"); statErr == nil {
		t.Error("a gated conversion must not touch anything")
	}
}

// Migration 3 (ADR-0001 Amendment 1) must lift interim pending keys out of the
// decision.proposed payloads that carried them before the column existed.
func TestMigration3_BackfillsPendingTopicKeyFromEventPayloads(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "interim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Build a database at version 2 only — the state the interim carrier
	// shipped in.
	if _, err := db.Exec(migrationsTableSQL); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:2] {
		if err := applyOne(db, m); err != nil {
			t.Fatalf("migration %d: %v", m.version, err)
		}
	}
	if ok, _ := columnExists(db, "decisions", "pending_topic_key"); ok {
		t.Fatal("pending_topic_key must arrive in migration 3, not the D8 baseline")
	}

	now := "2026-07-28T00:00:00Z"
	rows := []struct{ id, status, topicKey, payload string }{
		// Proposed, no key of its own, payload claims one -> backfilled.
		{"d1", "proposed", "", `{"via":"mcp","topic_key":"auth"}`},
		// Proposed, already holds its key -> left alone.
		{"d2", "proposed", "billing", `{"via":"cli","topic_key":"billing"}`},
		// Proposed, payload claims nothing -> stays NULL.
		{"d3", "proposed", "", `{"via":"cli"}`},
		// Not proposed -> out of scope for the backfill.
		{"d4", "rejected", "", `{"via":"mcp","topic_key":"stale"}`},
	}
	for _, r := range rows {
		if _, err := db.Exec(`
			INSERT INTO decisions (id, project_id, title, status, topic_key,
			    created_at, updated_at, status_changed_at)
			VALUES (?, 'p1', 'a title', ?, ?, ?, ?, ?)`,
			r.id, r.status, nullableString(r.topicKey), now, now, now); err != nil {
			t.Fatalf("seeding %s: %v", r.id, err)
		}
		if _, err := db.Exec(`
			INSERT INTO events (id, project_id, ts, kind, actor, decision_id, payload)
			VALUES (?, 'p1', ?, 'decision.proposed', 'agent', ?, ?)`,
			"ev-"+r.id, now, r.id, r.payload); err != nil {
			t.Fatalf("seeding event for %s: %v", r.id, err)
		}
	}

	if err := ApplySchema(db); err != nil {
		t.Fatalf("upgrading to migration 3: %v", err)
	}
	if v, _ := currentVersion(db); v != LatestSchemaVersion() {
		t.Fatalf("version = %d, want %d", v, LatestSchemaVersion())
	}

	want := map[string]string{"d1": "auth", "d2": "", "d3": "", "d4": ""}
	for id, wantPending := range want {
		var pending sql.NullString
		if err := db.QueryRow(
			`SELECT pending_topic_key FROM decisions WHERE id = ?`, id).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending.String != wantPending {
			t.Errorf("%s pending_topic_key = %q, want %q", id, pending.String, wantPending)
		}
	}
	// d2 keeps its real key; the backfill must not disturb it.
	var topic sql.NullString
	db.QueryRow(`SELECT topic_key FROM decisions WHERE id = 'd2'`).Scan(&topic)
	if topic.String != "billing" {
		t.Errorf("d2 topic_key = %q, want billing", topic.String)
	}

	// The supporting index exists and is deliberately NOT unique — competing
	// proposals may pend the same key.
	var unique int
	if err := db.QueryRow(`
		SELECT "unique" FROM pragma_index_list('decisions')
		 WHERE name = 'idx_decisions_pending_topic'`).Scan(&unique); err != nil {
		t.Fatalf("idx_decisions_pending_topic missing: %v", err)
	}
	if unique != 0 {
		t.Error("idx_decisions_pending_topic must be non-unique by design")
	}
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n)
	return n > 0, err
}
