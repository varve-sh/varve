package kernel

import (
	"database/sql"
	"fmt"
)

// ProjectIDInStore reports the project_id the rows in an existing store are
// filed under, so a re-registration can adopt it instead of minting a new one.
//
// Every read filters on project_id (`WHERE project_id = ?`). A project whose
// config entry is missing therefore cannot be re-registered with a fresh ULID:
// the rows would still be in the database, correct and complete, and invisible
// to every query — the failure mode this codebase has now produced four times.
// The one scenario re-registration exists for ("the DB came from another machine
// or the config was lost") is exactly the scenario where the store has data.
//
// Returns:
//   - id, nil          the single project_id found; adopt it
//   - "", nil          the store is empty; the caller may mint a new id
//   - "", err          more than one project_id, or the store is unreadable
//
// Works on a v1 store (a `memories` table) and a v2 store (`decisions` and
// `notes`) without applying a schema, so it is safe to call before migration.
func ProjectIDInStore(dbPath string) (string, error) {
	db, err := OpenDB(dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()

	tables := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return "", err
		}
		tables[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}

	var queries []string
	if tables["memories"] {
		queries = append(queries, `SELECT DISTINCT project_id FROM memories`)
	}
	if tables["decisions"] {
		queries = append(queries, `SELECT DISTINCT project_id FROM decisions`)
	}
	if tables["notes"] {
		queries = append(queries, `SELECT DISTINCT project_id FROM notes`)
	}
	if len(queries) == 0 {
		return "", nil // nothing recognisable in this file yet
	}

	found := map[string]bool{}
	for _, q := range queries {
		r, err := db.Query(q)
		if err != nil {
			// A table without the column is not this store's shape; skip rather
			// than fail, so an unrelated SQLite file cannot block `init`.
			continue
		}
		for r.Next() {
			var id sql.NullString
			if err := r.Scan(&id); err != nil {
				r.Close()
				return "", err
			}
			if id.Valid && id.String != "" {
				found[id.String] = true
			}
		}
		r.Close()
	}

	switch len(found) {
	case 0:
		return "", nil
	case 1:
		for id := range found {
			return id, nil
		}
	}

	// Guessing which of several projects owns this file would silently hide the
	// others. Refusing is recoverable; a wrong guess is not obviously wrong.
	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	return "", fmt.Errorf("store holds rows for %d different projects (%v) — "+
		"cannot infer which one this directory is", len(ids), ids)
}
