// Package importer reads foreign memory stores and rules files and turns them
// into varve import candidates (ADR-0005 §D1/§D2).
//
// Nothing in this package writes to a source, calls a model, or decides a
// lifecycle state: an importer says only "the source contained this text, and
// the source itself typed it thus". The kernel decides what that becomes, and
// per ADR-0005's binding input 3 the answer is never `active`.
package importer

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Candidate is a source-shaped row, converted to a kernel ImportCandidate by
// the CLI layer. Keeping this type free of kernel imports keeps the source
// adapters testable without a database.
type Candidate struct {
	SourceRef string
	// IdentityRef is the rule identity for the never-resurrect rule (F48):
	// coarser than SourceRef, so editing a rule's text does not offer a
	// human-rejected rule again. Empty means "same as SourceRef", which is
	// right for sources with stable row ids.
	IdentityRef string
	AsDecision  bool
	Kind        string // "decision" | "convention" — only set when AsDecision
	Title       string
	Content     string
	Scope       []string
	Tags        []string
}

// Probe is what a source reports about itself before anything is imported: it
// backs `varve import`'s "here is what I found" listing and §D2.6's init
// prompt.
type Probe struct {
	Source    string
	Available bool
	Path      string
	Count     int
	Detail    string
	// Refusal is set when the source exists but does not match the schema this
	// importer was tested against. §D1: a partial import from a half-recognised
	// schema is worse than a clean refusal, so this is never downgraded to a
	// best-effort read.
	Refusal error
}

// openForeignRO opens a foreign SQLite database read-only.
//
// One-way by construction (§D1, and the decisions log's one-way ruling): the
// mode=ro DSN means an importer bug cannot corrupt a user's claude-mem or
// engram store. The path is URL-escaped for the same reason kernel.Open does
// it — a repo path with '?' or '#' in it otherwise silently truncates the DSN.
func openForeignRO(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	dsn := "file:" + url.PathEscape(filepath.ToSlash(path)) + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// schemaManifest is the table/column set an importer was tested against
// (§D1's probe-and-refuse policy, open question 3: the exact shapes are the
// implementer's to pin at build time because the formats move).
//
// Aliases exist because both upstreams have renamed columns across releases
// and neither documents a schema version; a small pinned alias set is not
// "guess the schema", it is the tested set. Anything outside it refuses.
type schemaManifest struct {
	table    string
	idCol    []string
	textCol  []string
	titleCol []string
	timeCol  []string
	typeCol  []string
	scopeCol []string
	whyCol   []string
}

type resolvedSchema struct {
	table                                    string
	id, text, title, tstamp, typ, scope, why string
}

func resolveSchema(db *sql.DB, m schemaManifest) (*resolvedSchema, error) {
	cols, err := tableColumns(db, m.table)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %q not found", m.table)
	}
	pick := func(cands []string) string {
		for _, c := range cands {
			if cols[strings.ToLower(c)] {
				return c
			}
		}
		return ""
	}
	r := &resolvedSchema{
		table:  m.table,
		id:     pick(m.idCol),
		text:   pick(m.textCol),
		title:  pick(m.titleCol),
		tstamp: pick(m.timeCol),
		typ:    pick(m.typeCol),
		scope:  pick(m.scopeCol),
		why:    pick(m.whyCol),
	}
	if r.id == "" || r.text == "" {
		return nil, fmt.Errorf("table %q lacks a recognised id/text column set (found: %s)",
			m.table, strings.Join(sortedKeys(cols), ", "))
	}
	return r, nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols[strings.ToLower(n)] = true
	}
	return cols, rows.Err()
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// refuseMsg names the source's own export command as the fallback (§D1).
func refuseMsg(source, path, exportHint string, err error) error {
	return fmt.Errorf("%s at %s does not match the schema this importer was tested against (%v).\n"+
		"Refusing rather than importing part of it. Fallback: %s, then `varve import file <export>`",
		source, path, err, exportHint)
}
