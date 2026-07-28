package kernel

import (
	"database/sql"
	"errors"
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
