package kernel

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// NoteStore owns the ungoverned half of the schema. Notes are demoted v1
// `fact` and `event` rows: retrievable, but explicitly outside the lifecycle
// — no evidence requirement, no state machine, no attribution (ADR-0001 D1).
// Nothing here writes to the event log, by design.
type NoteStore struct {
	db *sql.DB
}

func NewNoteStore(db *sql.DB) *NoteStore { return &NoteStore{db: db} }

const noteColumns = `id, project_id, content, summary, source, source_ref, confidence,
	file_paths, tags, status, topic_key, embedding, created_at, updated_at,
	accessed_at, access_count`

// NoteInput is a new note.
type NoteInput struct {
	ProjectID  string
	Content    string
	Summary    string
	Source     types.MemorySource
	SourceRef  string
	Confidence float64
	FilePaths  []string
	Tags       []string
	Status     types.MemoryStatus
	TopicKey   string
}

// Insert writes a note. Notes keep v1 semantics, including v1's
// active/stale/archived statuses.
func (s *NoteStore) Insert(in NoteInput) (*types.Note, error) {
	if in.ProjectID == "" {
		return nil, &types.ValidationError{Field: "project_id", Message: "must not be empty"}
	}
	if in.Content == "" {
		return nil, &types.ValidationError{Field: "content", Message: "must not be empty"}
	}
	if in.Source == "" {
		in.Source = types.MemorySourceUser
	}
	if in.Status == "" {
		in.Status = types.MemoryStatusActive
	}
	if in.Confidence == 0 {
		in.Confidence = 1.0
	}

	now := time.Now().UTC()
	n := &types.Note{
		ID:         util.GenerateID(),
		ProjectID:  in.ProjectID,
		Content:    in.Content,
		Summary:    in.Summary,
		Source:     in.Source,
		SourceRef:  in.SourceRef,
		Confidence: in.Confidence,
		FilePaths:  nonNilStrings(in.FilePaths),
		Tags:       nonNilStrings(in.Tags),
		Status:     in.Status,
		TopicKey:   in.TopicKey,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.insertRow(s.db, n, nil); err != nil {
		return nil, err
	}
	return n, nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertRow writes a fully-formed note, optionally with a stored embedding.
// The migration uses it to carry v1 rows over with their original ids and
// timestamps intact.
func (s *NoteStore) insertRow(ex execer, n *types.Note, embedding any) error {
	_, err := ex.Exec(`
		INSERT INTO notes (`+noteColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.ProjectID, n.Content, nullableString(n.Summary), string(n.Source),
		nullableString(n.SourceRef), n.Confidence, mustJSON(nonNilStrings(n.FilePaths)),
		mustJSON(nonNilStrings(n.Tags)), string(n.Status), nullableString(n.TopicKey),
		embedding, fmtTime(n.CreatedAt), fmtTime(n.UpdatedAt),
		nullableTime(n.AccessedAt), n.AccessCount)
	if err != nil {
		return fmt.Errorf("inserting note: %w", err)
	}
	return nil
}

// Get returns a note by id, or types.ErrMemoryNotFound.
func (s *NoteStore) Get(id string) (*types.Note, error) {
	row := s.db.QueryRow(`SELECT `+noteColumns+` FROM notes WHERE id = ?`, id)
	n, err := scanNote(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrMemoryNotFound
	}
	return n, err
}

// NoteFilter narrows a list query. Zero values mean "no filter".
type NoteFilter struct {
	ProjectID string
	Status    types.MemoryStatus
	Limit     int
}

// List returns notes oldest first.
func (s *NoteStore) List(f NoteFilter) ([]types.Note, error) {
	q := `SELECT ` + noteColumns + ` FROM notes WHERE 1=1`
	var args []any
	if f.ProjectID != "" {
		q += " AND project_id = ?"
		args = append(args, f.ProjectID)
	}
	if f.Status != "" {
		q += " AND status = ?"
		args = append(args, string(f.Status))
	}
	q += " ORDER BY created_at, id"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing notes: %w", err)
	}
	defer rows.Close()

	var out []types.Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// Count returns the number of notes matching the filter.
func (s *NoteStore) Count(f NoteFilter) (int, error) {
	list, err := s.List(f)
	return len(list), err
}

func scanNote(sc scanner) (*types.Note, error) {
	var n types.Note
	var (
		summary, sourceRef, topicKey, embedding, accessedAt sql.NullString
		source, status                                      string
		filePathsJSON, tagsJSON                             string
		createdAt, updatedAt                                string
	)
	if err := sc.Scan(&n.ID, &n.ProjectID, &n.Content, &summary, &source, &sourceRef,
		&n.Confidence, &filePathsJSON, &tagsJSON, &status, &topicKey, &embedding,
		&createdAt, &updatedAt, &accessedAt, &n.AccessCount); err != nil {
		return nil, err
	}
	n.Summary = summary.String
	n.Source = types.MemorySource(source)
	n.SourceRef = sourceRef.String
	n.Status = types.MemoryStatus(status)
	n.TopicKey = topicKey.String
	n.FilePaths = decodeStrings(filePathsJSON)
	n.Tags = decodeStrings(tagsJSON)
	n.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	n.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	n.AccessedAt = parseNullTime(accessedAt)
	return &n, nil
}
