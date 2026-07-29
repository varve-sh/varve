package kernel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/memtrace-dev/memtrace/internal/retrieval"
	"github.com/memtrace-dev/memtrace/internal/scope"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// MemoryStore is the v1-shaped read/write facade over the v2 tables
// (ADR-0001 §D10). It has no business logic — that belongs in the kernel.
//
// The v2 schema splits one table into two with different semantics
// (§D1: governed `decisions`, ungoverned `notes`). Rather than teach every
// caller — recall, list, context, export, stats, the TUI, the MCP tools — to
// query both, the split is bridged in exactly one place: memoryProjection
// below. Every read path is then a single query against that projection, so
// none of the retrieval plumbing is written twice. Writes are *routed* by
// class, which is dispatch, not duplication: a decision write has to go
// through DecisionStore to pick up the lifecycle and its events.
type MemoryStore struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *MemoryStore {
	return &MemoryStore{db: db}
}

// memoryProjection maps `decisions` and `notes` onto the v1 read shape. It is
// the only place the two-table split is bridged.
//
// Column mapping, and why:
//   - decisions: content := body (falling back to title when the body is
//     empty, so a title-only decision is never a blank row), summary := title.
//     That is the inverse of §D9's row mapping, which set title from the v1
//     summary and body from the v1 content.
//   - decisions: file_paths := scope. An exact path is a glob that matches
//     exactly itself, which is what made §D9's verbatim carry-over sound.
//   - decisions surface with their own lifecycle status (§D10), so `status`
//     here can be any of the six.
//   - notes have no `type`: the v2 schema has one note class (§D1), and the
//     fact/event distinction survives as tags (§D9).
const memoryProjection = `
SELECT id, kind AS type,
       CASE WHEN body = '' THEN title ELSE body END AS content,
       title AS summary,
       source, source_ref, confidence, project_id,
       scope AS file_paths, tags, status, superseded_by,
       created_at, updated_at, accessed_at, access_count, embedding, topic_key
  FROM decisions
 UNION ALL
SELECT id, 'note' AS type,
       content,
       COALESCE(summary, '') AS summary,
       source, source_ref, confidence, project_id,
       file_paths, tags, status, NULL AS superseded_by,
       created_at, updated_at, accessed_at, access_count, embedding, topic_key
  FROM notes`

// liveDecisionStatuses is §D10's default visibility for decisions: everything
// non-terminal. `violated` is included because a violated decision is still
// binding — a violation is a fact about the codebase, not a repeal (§D2).
const liveDecisionStatuses = `('proposed','active','violated')`

// statusFilter renders the WHERE fragment for a status filter over the
// projection. An empty filter means "everything live" — the parallel of v1's
// default `status = 'active'`, widened to the decision lifecycle (§D10).
func statusFilter(status types.MemoryStatus, alias string) (string, []any) {
	col := alias + "status"
	typ := alias + "type"
	if status == "" {
		return fmt.Sprintf(
			"((%s = 'note' AND %s = 'active') OR (%s <> 'note' AND %s IN %s))",
			typ, col, typ, col, liveDecisionStatuses), nil
	}
	return col + " = ?", []any{string(status)}
}

// Insert writes a new memory, routing it to the table its class belongs in.
// Decisions and conventions go through DecisionStore so they pick up the
// lifecycle, the evidence rules and their events (§D1, §D7); notes go straight
// to `notes`, which is deliberately ungoverned.
func (s *MemoryStore) Insert(m *types.Memory) error {
	if m.Type.IsDecision() {
		return fmt.Errorf(
			"decisions are not inserted through MemoryStore — they carry a lifecycle " +
				"and must go through the kernel's decision path")
	}
	n := &types.Note{
		ID: m.ID, ProjectID: m.ProjectID, Content: m.Content, Summary: m.Summary,
		Source: m.Source, SourceRef: m.SourceRef, Confidence: m.Confidence,
		FilePaths: m.FilePaths, Tags: m.Tags, Status: m.Status, TopicKey: m.TopicKey,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	if n.Status == "" {
		n.Status = types.MemoryStatusActive
	}
	return NewNoteStore(s.db).insertRow(s.db, n, nil)
}

// FindByID retrieves a memory by its ID. Returns nil, nil if not found.
func (s *MemoryStore) FindByID(id string) (*types.Memory, error) {
	row := s.db.QueryRow(`SELECT * FROM (`+memoryProjection+`) m WHERE m.id = ?`, id)
	m, err := scanMemory(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// classOf reports which table holds id: "decision", "note", or "" if neither.
func (s *MemoryStore) classOf(id string) (string, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM decisions WHERE id = ?`, id).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "decision", nil
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE id = ?`, id).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "note", nil
	}
	return "", nil
}

// Update applies a partial update. Only non-nil fields are changed.
//
// On a note this is v1's semantics unchanged. On a decision it is not
// available: normative content is immutable after acceptance and status
// changes must go through the state machine (§D3), both enforced by DDL
// triggers. The kernel routes decision edits to DecisionStore instead.
func (s *MemoryStore) Update(id string, input types.MemoryUpdateInput) error {
	class, err := s.classOf(id)
	if err != nil {
		return err
	}
	switch class {
	case "":
		return types.ErrMemoryNotFound
	case "decision":
		return fmt.Errorf(
			"decision %s cannot be updated through MemoryStore: content is immutable "+
				"after acceptance and status changes are lifecycle transitions", id)
	}

	setClauses := []string{"updated_at = ?"}
	args := []any{time.Now().UTC().Format(time.RFC3339Nano)}

	// A note cannot become a decision by an UPDATE: they are different tables
	// with different semantics, and a decision has to be born through the
	// lifecycle so its provenance and events exist (§D1, §D2). Promoting a note
	// means saving a decision that cites it, not rewriting a column.
	if input.Type != nil && input.Type.IsDecision() {
		return &types.ValidationError{
			Field: "type",
			Message: "a note cannot be converted into a " + string(*input.Type) +
				" — save it as one, so it is born with a lifecycle and provenance",
		}
	}

	if input.Content != nil {
		setClauses = append(setClauses, "content = ?")
		args = append(args, *input.Content)
	}
	if input.Summary != nil {
		setClauses = append(setClauses, "summary = ?")
		args = append(args, *input.Summary)
	}
	if input.Confidence != nil {
		setClauses = append(setClauses, "confidence = ?")
		args = append(args, *input.Confidence)
	}
	if input.FilePaths != nil {
		b, _ := json.Marshal(*input.FilePaths)
		setClauses = append(setClauses, "file_paths = ?")
		args = append(args, string(b))
	}
	if input.Tags != nil {
		b, _ := json.Marshal(*input.Tags)
		setClauses = append(setClauses, "tags = ?")
		args = append(args, string(b))
	}

	if input.Status != nil {
		// Routed so the note topic_key reactivation rule applies
		// (ADR-0001 Amendment 1, same-class audit item 3).
		if err := NewNoteStore(s.db).SetStatus(id, *input.Status); err != nil {
			return err
		}
	}
	if len(setClauses) == 1 {
		return nil // only updated_at — nothing to do beyond any status change
	}

	args = append(args, id)
	_, err = s.db.Exec("UPDATE notes SET "+strings.Join(setClauses, ", ")+" WHERE id = ?", args...)
	return err
}

// DeleteByID hard-deletes a memory. Returns true if a row was deleted.
//
// A decision with any event history is never hard-deleted (§D3) — the FK from
// `events` blocks it, and the kernel maps "forget" onto a lifecycle transition
// instead. Notes keep v1 delete semantics.
func (s *MemoryStore) DeleteByID(id string) (bool, error) {
	class, err := s.classOf(id)
	if err != nil || class == "" {
		return false, err
	}
	table := "notes"
	if class == "decision" {
		table = "decisions"
	}
	res, err := s.db.Exec("DELETE FROM "+table+" WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// List returns memories matching the given options.
func (s *MemoryStore) List(opts types.ListOptions) ([]types.Memory, error) {
	q := `SELECT * FROM (` + memoryProjection + `) m WHERE 1=1`
	var args []any

	if opts.Type != "" {
		if opts.Type.IsDecision() {
			q += " AND m.type = ?"
			args = append(args, string(opts.Type))
		} else {
			q += " AND m.type = 'note'"
		}
	}

	where, statusArgs := statusFilter(opts.Status, "m.")
	q += " AND " + where
	args = append(args, statusArgs...)

	sortCol := "created_at"
	if opts.Sort == "updated_at" || opts.Sort == "accessed_at" {
		sortCol = opts.Sort
	}
	order := "DESC"
	if strings.ToLower(opts.Order) == "asc" {
		order = "ASC"
	}
	q += " ORDER BY m." + sortCol + " " + order

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	q += " LIMIT ? OFFSET ?"
	args = append(args, limit, opts.Offset)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []types.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		if len(opts.Tags) > 0 && !hasAllTags(m.Tags, opts.Tags) {
			continue
		}
		memories = append(memories, *m)
	}
	return memories, rows.Err()
}

// Count returns the number of memories matching the given filters.
func (s *MemoryStore) Count(memType types.MemoryType, status types.MemoryStatus) (int, error) {
	q := `SELECT COUNT(*) FROM (` + memoryProjection + `) m WHERE 1=1`
	var args []any

	if memType != "" {
		if memType.IsDecision() {
			q += " AND m.type = ?"
			args = append(args, string(memType))
		} else {
			q += " AND m.type = 'note'"
		}
	}
	where, statusArgs := statusFilter(status, "m.")
	q += " AND " + where
	args = append(args, statusArgs...)

	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// CountSince returns the number of live memories whose column (created_at or
// accessed_at) is >= since.
func (s *MemoryStore) CountSince(column string, since time.Time) (int, error) {
	if column != "created_at" && column != "accessed_at" {
		return 0, fmt.Errorf("invalid column: %s", column)
	}
	where, args := statusFilter("", "m.")
	q := `SELECT COUNT(*) FROM (` + memoryProjection + `) m
	       WHERE ` + where + ` AND m.` + column + ` >= ?`
	var n int
	err := s.db.QueryRow(q, append(args, since.Format(time.RFC3339Nano))...).Scan(&n)
	return n, err
}

// TopAccessed returns the top n live memories ordered by access_count desc.
func (s *MemoryStore) TopAccessed(projectID string, n int) ([]types.Memory, error) {
	where, args := statusFilter("", "m.")
	q := `SELECT * FROM (` + memoryProjection + `) m
	       WHERE m.project_id = ? AND ` + where + ` AND m.access_count > 0
	       ORDER BY m.access_count DESC LIMIT ?`
	rows, err := s.db.Query(q, append(append([]any{projectID}, args...), n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []types.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *m)
	}
	return result, rows.Err()
}

// SearchFTS runs a full-text search across both FTS tables and returns matching
// ids with BM25 ranks (§D10: recall searches decisions_fts *and* notes_fts and
// "merges via the existing scorer").
//
// The limit is applied *inside each arm*, never to the merged set. The two
// ranks come from separate FTS5 indexes and are not comparable, and BM25
// favours short documents — notes are short, decision bodies are long — so one
// ORDER BY over the union does not merely order decisions lower, it deletes
// them before the scorer ever runs. Measured on 60 matching notes plus 1
// matching decision, a merged cut of 30 returned 30 notes and no decision at
// all, while the decision was live and listed. A note-heavy store with few
// decisions is the shape of every migrated v1 database.
//
// Each class therefore contributes up to `limit` candidates and the scorer does
// the merging, normalising BM25 within the pool before weighting it against
// recency, confidence and access.
func (s *MemoryStore) SearchFTS(query string, projectID string, limit int) ([]types.FTSResult, error) {
	sanitized := sanitizeFTSQuery(query)
	if sanitized == "" {
		return nil, nil
	}

	rows, err := s.db.Query(`
		SELECT id, rank FROM (
			SELECT * FROM (
				SELECT d.id AS id, fts.rank AS rank
				  FROM decisions_fts fts
				  JOIN decisions d ON d.rowid = fts.rowid
				 WHERE decisions_fts MATCH ?
				   AND d.project_id = ?
				   AND d.status IN `+liveDecisionStatuses+`
				 ORDER BY rank
				 LIMIT ?
			)
			UNION ALL
			SELECT * FROM (
				SELECT n.id AS id, fts.rank AS rank
				  FROM notes_fts fts
				  JOIN notes n ON n.rowid = fts.rowid
				 WHERE notes_fts MATCH ?
				   AND n.project_id = ?
				   AND n.status = 'active'
				 ORDER BY rank
				 LIMIT ?
			)
		)
		ORDER BY rank`,
		sanitized, projectID, limit, sanitized, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.FTSResult
	for rows.Next() {
		var r types.FTSResult
		if err := rows.Scan(&r.ID, &r.Rank); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// TouchAccess updates accessed_at and increments access_count.
func (s *MemoryStore) TouchAccess(id string, now time.Time) error {
	ts := now.Format(time.RFC3339Nano)
	// Applied to both tables unconditionally: ids are ULIDs and live in exactly
	// one of them, so the other statement is a no-op. Cheaper and simpler than
	// a class lookup, and access tracking is advisory (§D4 — attribution uses
	// `events`, never these counters).
	for _, table := range []string{"decisions", "notes"} {
		if _, err := s.db.Exec(
			`UPDATE `+table+` SET accessed_at = ?, access_count = access_count + 1 WHERE id = ?`,
			ts, id); err != nil {
			return err
		}
	}
	return nil
}

// FindByFilePaths returns live memories relevant to the given paths (§D10).
//
// The two classes match differently, by design. A decision's `scope` is a list
// of globs, matched with doublestar — that is the whole point of the scope
// vocabulary (§D4/§D6), and exact string equality would silently miss every
// scoped decision. A note's `file_paths` keeps v1's exact-match semantics:
// notes are ungoverned and their paths were never patterns.
func (s *MemoryStore) FindByFilePaths(projectID string, paths []string) ([]types.Memory, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	var result []types.Memory

	// Decisions: glob match, evaluated in Go. Scope lists are short and the
	// live-decision count is small; doublestar has no SQL equivalent.
	rows, err := s.db.Query(`
		SELECT * FROM (`+memoryProjection+`) m
		 WHERE m.project_id = ? AND m.type <> 'note' AND m.status IN `+liveDecisionStatuses+`
		 ORDER BY m.updated_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		if len(scope.MatchedFiles(m.FilePaths, paths)) > 0 {
			result = append(result, *m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Notes: v1 exact match, in SQL.
	ph := make([]string, len(paths))
	args := make([]any, 0, 1+len(paths))
	args = append(args, projectID)
	for i, p := range paths {
		ph[i] = "?"
		args = append(args, p)
	}
	noteRows, err := s.db.Query(fmt.Sprintf(`
		SELECT DISTINCT n.id FROM notes n, json_each(n.file_paths) je
		 WHERE n.project_id = ? AND n.status = 'active' AND je.value IN (%s)
		 ORDER BY n.updated_at DESC`, strings.Join(ph, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer noteRows.Close()
	for noteRows.Next() {
		var id string
		if err := noteRows.Scan(&id); err != nil {
			return nil, err
		}
		m, err := s.FindByID(id)
		if err != nil || m == nil {
			continue
		}
		result = append(result, *m)
	}
	if err := noteRows.Err(); err != nil {
		return nil, err
	}

	// One ordering across both classes, applied *before* the cap. The previous
	// code concatenated decisions-by-recency with notes-by-recency and capped
	// afterwards, so the preference for decisions was an accident of append
	// order — and with more matching decisions than the caller's limit, no
	// exact-matched note could ever be reached regardless of recency (F16).
	//
	// The preference itself is kept and made deliberate: within a file scope a
	// decision is binding and a note is explicitly ungoverned (§D1), so a
	// glob-matched decision outranks an exact-matched note. The consequence is
	// named rather than incidental — a path matched by many live decisions can
	// fill the caller's limit on its own.
	sort.SliceStable(result, func(i, j int) bool {
		ri, rj := classRank(result[i].Type), classRank(result[j].Type)
		if ri != rj {
			return ri < rj
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})

	if len(result) > 50 {
		result = result[:50]
	}
	return result, nil
}

// classRank orders the two classes for context assembly: governed first.
func classRank(t types.MemoryType) int {
	if t.IsDecision() {
		return 0
	}
	return 1
}

// FindUnembedded returns ids and content for live memories with no embedding.
func (s *MemoryStore) FindUnembedded(projectID string) ([]struct{ ID, Content string }, error) {
	where, args := statusFilter("", "m.")
	rows, err := s.db.Query(`
		SELECT m.id, m.content FROM (`+memoryProjection+`) m
		 WHERE m.project_id = ? AND `+where+` AND m.embedding IS NULL`,
		append([]any{projectID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []struct{ ID, Content string }
	for rows.Next() {
		var r struct{ ID, Content string }
		if err := rows.Scan(&r.ID, &r.Content); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// StoreEmbedding persists a precomputed embedding vector.
func (s *MemoryStore) StoreEmbedding(id string, vec []float64) error {
	b, err := json.Marshal(vec)
	if err != nil {
		return err
	}
	for _, table := range []string{"decisions", "notes"} {
		if _, err := s.db.Exec(
			`UPDATE `+table+` SET embedding = ? WHERE id = ?`, string(b), id); err != nil {
			return err
		}
	}
	return nil
}

// FindEmbeddings returns ids and stored embeddings for live memories, so
// MemoryStore satisfies retrieval.StoreReader.
func (s *MemoryStore) FindEmbeddings(projectID string) ([]retrieval.EmbeddingRow, error) {
	where, args := statusFilter("", "m.")
	rows, err := s.db.Query(`
		SELECT m.id, m.embedding FROM (`+memoryProjection+`) m
		 WHERE m.project_id = ? AND `+where+` AND m.embedding IS NOT NULL`,
		append([]any{projectID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []retrieval.EmbeddingRow
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var vec []float64
		if err := json.Unmarshal([]byte(raw), &vec); err != nil {
			continue
		}
		result = append(result, retrieval.EmbeddingRow{ID: id, Embedding: vec})
	}
	return result, rows.Err()
}

// --- Helpers ---

// scanner is the common interface for *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...interface{}) error
}

func scanMemory(s scanner) (*types.Memory, error) {
	var m types.Memory
	var (
		summary, sourceRef, supersededBy sql.NullString
		accessedAt, embedding, topicKey  sql.NullString
		filePathsJSON, tagsJSON          string
		createdStr, updatedStr           string
		typeStr, sourceStr, statusStr    string
	)

	err := s.Scan(
		&m.ID, &typeStr, &m.Content, &summary,
		&sourceStr, &sourceRef, &m.Confidence,
		&m.ProjectID, &filePathsJSON, &tagsJSON,
		&statusStr, &supersededBy,
		&createdStr, &updatedStr, &accessedAt, &m.AccessCount,
		&embedding, &topicKey,
	)
	if err != nil {
		return nil, err
	}

	m.Type = types.MemoryType(typeStr)
	m.Source = types.MemorySource(sourceStr)
	m.Status = types.MemoryStatus(statusStr)
	m.Summary = summary.String
	m.SourceRef = sourceRef.String
	m.SupersededBy = supersededBy.String
	m.TopicKey = topicKey.String

	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
	if accessedAt.Valid && accessedAt.String != "" {
		t, _ := time.Parse(time.RFC3339Nano, accessedAt.String)
		m.AccessedAt = &t
	}

	if err := json.Unmarshal([]byte(filePathsJSON), &m.FilePaths); err != nil {
		m.FilePaths = []string{}
	}
	if err := json.Unmarshal([]byte(tagsJSON), &m.Tags); err != nil {
		m.Tags = []string{}
	}
	if m.FilePaths == nil {
		m.FilePaths = []string{}
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}

	return &m, nil
}

// sanitizeFTSQuery removes FTS5 special characters that could cause syntax errors.
func sanitizeFTSQuery(query string) string {
	var b strings.Builder
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.TrimSpace(b.String())
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func hasAllTags(memTags []string, required []string) bool {
	tagSet := make(map[string]bool, len(memTags))
	for _, t := range memTags {
		tagSet[t] = true
	}
	for _, r := range required {
		if !tagSet[r] {
			return false
		}
	}
	return true
}
