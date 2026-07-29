package importer

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultEngramDB is engram's documented store location (§D1).
func DefaultEngramDB() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".engram", "engram.db")
}

// engramManifest pins the What/Why/Where/Learned shape (§D1, open question 3).
var engramManifest = schemaManifest{
	table:    "memories",
	idCol:    []string{"id", "memory_id", "rowid"},
	textCol:  []string{"what", "content", "text"},
	titleCol: []string{"title", "what"},
	timeCol:  []string{"created_at", "createdAt", "timestamp"},
	typeCol:  []string{"type", "kind", "memory_type"},
	scopeCol: []string{"where", "where_", "file_paths", "paths"},
	whyCol:   []string{"why", "rationale", "reason"},
}

// engramDecisionTypes is the *source's own* type vocabulary that counts as a
// decision signal (§D1). Nothing outside this set is promoted: the signal must
// be the source's structure, never our reading of the prose.
var engramDecisionTypes = map[string]string{
	"decision":     "decision",
	"decisions":    "decision",
	"architecture": "decision",
	"adr":          "decision",
	"convention":   "convention",
	"conventions":  "convention",
	"rule":         "convention",
	"pattern":      "convention",
	"preference":   "convention",
}

// ProbeEngram reports what an engram store holds without importing.
func ProbeEngram(path string) Probe {
	p := Probe{Source: "engram", Path: path}
	db, err := openForeignRO(path)
	if err != nil {
		return p
	}
	defer db.Close()
	p.Available = true
	schema, err := resolveSchema(db, engramManifest)
	if err != nil {
		p.Refusal = refuseMsg("engram", path, "engram export", err)
		return p
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM ` + quoteIdent(schema.table)).Scan(&p.Count)
	p.Detail = strconv.Itoa(p.Count) + " entries"
	return p
}

// ImportEngram reads engram entries as notes, promoting to **proposed**
// decisions only where the source itself typed the row decision-like (§D1).
//
// Two conversions are specified and both are mechanical:
//   - `Why` becomes the body (engram's rationale field is the closest thing in
//     the field to a decision's reasoning).
//   - `Where` paths become exact-path scope globs. ADR-0001 §D9 settles that a
//     path is a glob matching itself, so this invents nothing: it is the source's
//     own file list, not an inferred scope (§D2.5 forbids the latter).
func ImportEngram(path string, asNotes bool) ([]Candidate, error) {
	db, err := openForeignRO(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	schema, err := resolveSchema(db, engramManifest)
	if err != nil {
		return nil, refuseMsg("engram", path, "engram export", err)
	}

	cols := []string{schema.id, schema.text}
	for _, c := range []string{schema.typ, schema.scope, schema.why} {
		if c != "" {
			cols = append(cols, c)
		}
	}
	rows, err := db.Query("SELECT " + quoteList(cols) + " FROM " + quoteIdent(schema.table) +
		" ORDER BY " + quoteIdent(schema.id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Candidate
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		dest := make([]any, len(cols))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		byCol := map[string]string{}
		for i, c := range cols {
			byCol[c] = strings.TrimSpace(vals[i].String)
		}
		what := byCol[schema.text]
		if what == "" {
			continue
		}
		why := ""
		if schema.why != "" {
			why = byCol[schema.why]
		}
		c := Candidate{
			SourceRef: "engram:" + byCol[schema.id],
			Title:     what,
			Content:   joinNonEmpty([]string{what, why}, "\n\n"),
			Tags:      []string{"engram"},
		}
		if !asNotes && schema.typ != "" {
			if kind, ok := engramDecisionTypes[strings.ToLower(byCol[schema.typ])]; ok {
				c.AsDecision = true
				c.Kind = kind
				if why != "" {
					c.Content = why
				}
				if schema.scope != "" {
					c.Scope = splitPaths(byCol[schema.scope])
				}
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func joinNonEmpty(parts []string, sep string) string {
	var keep []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}

// splitPaths turns engram's `Where` field into scope globs. The field is a
// JSON array in newer releases and a comma/newline list in older ones.
func splitPaths(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	s = strings.Trim(s, "[]")
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(strings.Trim(strings.TrimSpace(f), `"'`))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
