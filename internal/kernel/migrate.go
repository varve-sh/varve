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
// Migration 2 is ADR-0001 §D8. Migration 3 is Amendment 1; migration 4 is
// Amendment 4; migration 5 is Amendment 5.
var migrations = []migration{
	{1, "baseline_v1", execScript(baselineV1SQL)},
	{2, "decision_lifecycle_v2", execScript(schemaV2SQL)},
	{3, "pending_topic_key", execScript(schemaV3SQL)},
	{4, "purge_redaction_exemption", execScript(schemaV4SQL)},
	{5, "promote_attribution_columns", execScript(schemaV5SQL)},
}

// LatestSchemaVersion is the version a freshly created database ends up at.
func LatestSchemaVersion() int { return migrations[len(migrations)-1].version }

// v2ReadPathsReady reports whether the product can actually read a v2
// database. It gates the v1→v2 conversion, and it exists because of a real
// hole found in review.
//
// ADR-0001 §D9 writes the v1 rows into `decisions` and `notes`; §D10 specifies
// the matching read paths — recall over `decisions_fts` + `notes_fts`,
// `memory_context` over decision scopes. This branch shipped the write half.
// Until the read half lands, converting a database moves every row somewhere
// nothing reads: `list`, `recall`, `export`, `memory_recall`, `memory_context`,
// `status` and the TUI would all return nothing and exit 0, and subsequent
// saves would land back in the empty v1 table, splitting the store in two. The
// data would survive in `decisions`/`notes` and in the kept backup, but from
// the user's point of view their memory would be gone, silently.
//
// OPEN as of the §D10 read-path port. MemoryStore now reads `decisions` and
// `notes` through one projection, recall searches both FTS tables, and
// memory_context glob-matches decision scopes — so a converted database is
// fully visible to every read path, and the canary that asserted the hole
// fired and was removed.
//
// With the gate open, ADR-0001 §D9's specified behaviour is in force:
// MigrateFromV1 runs, and ApplySchema refuses a v1 database outright with
// instructions to convert it. It has to refuse now — the read paths query v2
// tables that do not exist in a v1 file, so serving one is no longer possible.
//
// The variable stays rather than being deleted: it is the seam that made the
// deviation reviewable, and the tests still exercise both sides of it.
var v2ReadPathsReady = true

// V2ReadPathsReady reports whether the v2 read paths (ADR-0001 §D10) are
// wired up, and therefore whether the v1→v2 conversion may run.
func V2ReadPathsReady() bool { return v2ReadPathsReady }

// SetV2ReadPathsReady flips the gate and returns a function restoring the
// previous value. Tests use it to exercise both sides; the read-path port will
// change the variable's initial value instead.
func SetV2ReadPathsReady(ready bool) func() {
	prev := v2ReadPathsReady
	v2ReadPathsReady = ready
	return func() { v2ReadPathsReady = prev }
}

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
		if V2ReadPathsReady() {
			return types.ErrLegacyDatabase
		}
		// The conversion is gated (see v2ReadPathsReady). Refusing here as well
		// would leave an existing database with no read path *and* no way
		// forward, which is strictly worse than the state before this branch.
		// The database is left exactly as it is — untouched, un-migrated, and
		// still served by the v1 read paths. D9's actual requirement, "a v1
		// database is never auto-migrated on open", is upheld either way.
		return nil
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
// pools connections — they are therefore also set in the DSN by OpenDB();
// this call covers callers that hand us a *sql.DB they opened themselves.
func applyPragmas(db *sql.DB) error {
	_, err := db.Exec(`
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;`)
	return err
}

// BaselineV1SQLForTest exposes the recorded v1 baseline so tests in other
// packages can build a genuine pre-migration database to migrate.
func BaselineV1SQLForTest() string { return baselineV1SQL }
