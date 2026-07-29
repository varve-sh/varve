package importer

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultClaudeMemDB is claude-mem's documented store location (§D1).
func DefaultClaudeMemDB() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude-mem", "claude-mem.db")
}

// claudeMemManifest is the pinned table/column set (open question 3).
//
// Column aliases cover the releases we tested against; the fixture DB in
// claudemem_test.go is built from this manifest, so a schema change upstream
// fails the probe loudly instead of importing a subset.
var claudeMemManifest = schemaManifest{
	table:    "observations",
	idCol:    []string{"id", "rowid"},
	textCol:  []string{"text", "content", "body", "observation"},
	titleCol: []string{"title", "subject", "summary"},
	timeCol:  []string{"created_at", "createdAt", "timestamp", "ts"},
}

// ProbeClaudeMem reports what a claude-mem store holds without importing.
func ProbeClaudeMem(path string) Probe {
	p := Probe{Source: "claude-mem", Path: path}
	db, err := openForeignRO(path)
	if err != nil {
		return p
	}
	defer db.Close()
	p.Available = true
	schema, err := resolveSchema(db, claudeMemManifest)
	if err != nil {
		p.Refusal = refuseMsg("claude-mem", path, "claude-mem export", err)
		return p
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM ` + quoteIdent(schema.table)).Scan(&p.Count)
	p.Detail = strconv.Itoa(p.Count) + " observations"
	return p
}

// ImportClaudeMem reads claude-mem observations as **notes only** (§D1).
//
// This is the honesty rule the source table states outright: session
// archaeology is not a rule. claude-mem stores narrative observations with no
// scope, no evidence and no normative flag, so there is no signal that could
// justify a decision candidate, and inventing one would poison the review
// queue at first contact (rejected alternative A). A large claude-mem corpus
// arrives as a searchable note corpus plus a health report — that is the whole
// claim, and it is the true one.
func ImportClaudeMem(path string) ([]Candidate, error) {
	db, err := openForeignRO(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	schema, err := resolveSchema(db, claudeMemManifest)
	if err != nil {
		return nil, refuseMsg("claude-mem", path, "claude-mem export", err)
	}

	cols := []string{schema.id, schema.text}
	if schema.title != "" {
		cols = append(cols, schema.title)
	}
	q := "SELECT " + quoteList(cols) + " FROM " + quoteIdent(schema.table) + " ORDER BY " + quoteIdent(schema.id)
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Candidate
	for rows.Next() {
		var id, text, title sql.NullString
		dest := []any{&id, &text}
		if schema.title != "" {
			dest = append(dest, &title)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		body := strings.TrimSpace(text.String)
		if body == "" {
			continue
		}
		out = append(out, Candidate{
			// §D2.2: `claude-mem:<db-row-id>` — stable across re-runs, which is
			// the entire idempotency guarantee for this source.
			SourceRef: "claude-mem:" + id.String,
			Title:     strings.TrimSpace(title.String),
			Content:   body,
			Tags:      []string{"claude-mem"},
		})
	}
	return out, rows.Err()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func quoteList(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = quoteIdent(c)
	}
	return strings.Join(out, ", ")
}
