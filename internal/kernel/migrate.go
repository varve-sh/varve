package kernel

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// migration is one forward step, compiled into the binary. There are no down
// migrations: roll forward, or restore the backup (ADR-0001 D9).
type migration struct {
	version int
	name    string
	up      func(*sql.Tx) error
}

// migrations is the ordered, append-only list. Never renumber, never edit an
// applied migration's body — add a new one.
//
// Migration 1 is the recorded v1 baseline: the shape a fully-migrated v1
// database has after the old ad-hoc addColumnIfMissing calls. It exists so a
// fresh database is built by the same mechanism as every later change; it is
// NOT applied to databases that already hold v1 data (those are refused on
// open and converted by MigrateFromV1).
//
// Migration 2 is ADR-0001 §D8.
var migrations = []migration{
	{1, "baseline_v1", execScript(baselineV1SQL)},
	{2, "decision_lifecycle_v2", execScript(schemaV2SQL)},
}

// LatestSchemaVersion is the version a freshly created database ends up at.
func LatestSchemaVersion() int { return migrations[len(migrations)-1].version }

func execScript(script string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(script)
		return err
	}
}

const migrationsTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL              -- RFC3339Nano UTC
);`

// ApplySchema brings db up to the latest schema version.
//
// It refuses a v1 database outright (types.ErrLegacyDatabase): a database
// holding v1 `memories` rows with no schema_migrations table is never
// auto-migrated, because the v1→v2 path is an export→reimport that rebuilds
// the file (ADR-0001 D9). Run MigrateFromV1 for those.
func ApplySchema(db *sql.DB) error {
	if err := applyPragmas(db); err != nil {
		return fmt.Errorf("setting pragmas: %w", err)
	}

	legacy, err := isLegacyV1(db)
	if err != nil {
		return err
	}
	if legacy {
		return types.ErrLegacyDatabase
	}

	if _, err := db.Exec(migrationsTableSQL); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	current, err := currentVersion(db)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyOne(db, m); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

// applyOne runs a single migration and records it in the same transaction, so
// a partially applied migration cannot be recorded as complete.
func applyOne(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := m.up(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func currentVersion(db *sql.DB) (int, error) {
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// isLegacyV1 reports whether db is a pre-framework v1 database: it has the v1
// `memories` table but no schema_migrations table.
func isLegacyV1(db *sql.DB) (bool, error) {
	hasMigrations, err := hasTable(db, "schema_migrations")
	if err != nil {
		return false, err
	}
	if hasMigrations {
		return false, nil
	}
	return hasTable(db, "memories")
}

func hasTable(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("inspecting sqlite_master: %w", err)
	}
	return n > 0, nil
}

// applyPragmas sets the connection-level PRAGMAs from ADR-0001 §D8.
//
// journal_mode is a persistent database property, so setting it once is
// enough. foreign_keys and busy_timeout are per-connection, and database/sql
// pools connections — they are therefore also set in the DSN by openDB();
// this call covers callers that hand us a *sql.DB they opened themselves.
func applyPragmas(db *sql.DB) error {
	_, err := db.Exec(`
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;`)
	return err
}
