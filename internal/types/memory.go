package types

import "time"

type MemoryType string

const (
	MemoryTypeDecision   MemoryType = "decision"
	MemoryTypeConvention MemoryType = "convention"
	// MemoryTypeNote is what a v2 `notes` row surfaces as. The v2 schema
	// deliberately has one note class (ADR-0001 D1): `fact` and `event` were
	// collapsed, and the distinction they carried survives as tags — D9's row
	// mapping says so explicitly ("session summaries keep their `session` tag,
	// prompts keep `prompt`").
	MemoryTypeNote MemoryType = "note"
	// MemoryTypeFact and MemoryTypeEvent remain accepted on *input*, as
	// synonyms for MemoryTypeNote, so deployed agent-instruction templates and
	// `memtrace save --type fact` keep working. Nothing reads them back.
	MemoryTypeFact  MemoryType = "fact"
	MemoryTypeEvent MemoryType = "event"
)

// IsNote reports whether t names the ungoverned class.
func (t MemoryType) IsNote() bool {
	switch t {
	case MemoryTypeNote, MemoryTypeFact, MemoryTypeEvent, "":
		return true
	}
	return false
}

// IsDecision reports whether t names a governed class (ADR-0001 D1: v1
// `decision` and `convention` both land in `decisions`).
func (t MemoryType) IsDecision() bool {
	return t == MemoryTypeDecision || t == MemoryTypeConvention
}

// Canonical folds the input synonyms onto what the schema actually stores.
func (t MemoryType) Canonical() MemoryType {
	if t.IsDecision() {
		return t
	}
	return MemoryTypeNote
}

type MemorySource string

const (
	MemorySourceUser   MemorySource = "user"
	MemorySourceAgent  MemorySource = "agent"
	MemorySourceGit    MemorySource = "git"
	MemorySourceImport MemorySource = "import"
)

type MemoryStatus string

const (
	MemoryStatusActive   MemoryStatus = "active"
	MemoryStatusStale    MemoryStatus = "stale"
	MemoryStatusArchived MemoryStatus = "archived"
)

// A decision surfaces through the v1 read shape carrying its *own* lifecycle
// status (ADR-0001 D10), so MemoryStatus also holds the six decision states.
// An empty MemoryStatus filter means "everything still live": non-terminal
// decisions (proposed, active, violated — violated is still binding) and
// active notes. That is the parallel of v1's default `status = active`.

// IsTerminalMemoryStatus reports whether a surfaced status is one of the
// decision lifecycle's terminal states.
func IsTerminalMemoryStatus(s MemoryStatus) bool {
	switch DecisionStatus(s) {
	case StatusSuperseded, StatusReverted, StatusRejected:
		return true
	}
	return false
}

// Memory is the core domain object.
type Memory struct {
	ID           string       `json:"id"`
	Type         MemoryType   `json:"type"`
	Content      string       `json:"content"`
	Summary      string       `json:"summary,omitempty"`
	Source       MemorySource `json:"source"`
	SourceRef    string       `json:"source_ref,omitempty"`
	Confidence   float64      `json:"confidence"`
	ProjectID    string       `json:"project_id"`
	FilePaths    []string     `json:"file_paths"`
	Tags         []string     `json:"tags"`
	Status       MemoryStatus `json:"status"`
	SupersededBy string       `json:"superseded_by,omitempty"`
	TopicKey     string       `json:"topic_key,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	AccessedAt   *time.Time   `json:"accessed_at,omitempty"`
	AccessCount  int          `json:"access_count"`
}

// MemorySaveInput is what callers provide to save a new memory.
type MemorySaveInput struct {
	Content    string       // Required, max 10000 chars
	Type       MemoryType   // Default: MemoryTypeFact
	Summary    string       // Auto-generated if empty: first 120 chars of Content
	Source     MemorySource // Default: MemorySourceUser
	SourceRef  string
	Confidence float64  // Default: 1.0, range 0.0-1.0
	FilePaths  []string // Relative paths, max 20
	Tags       []string // Max 20 tags, each max 50 chars
	TopicKey   string   // Optional stable key; if set, re-saving upserts instead of creating a duplicate
	// Provenance. ADR-0001 D4 makes these advisory in the DDL but mandatory on
	// the MCP path: the kernel rejects an agent-sourced decision with no
	// SessionID, because a decision that cannot be attributed to a session is
	// invisible to the whole attribution chain.
	SessionID string
	Agent     string
	Model     string
}

// MemoryRecallInput is what callers provide to search memories.
type MemoryRecallInput struct {
	Query         string       // Required
	Limit         int          // Default: 10, max: 50
	Type          MemoryType   // Empty = no filter
	Status        MemoryStatus // Empty = everything live (see MemoryStatus)
	Tags          []string     // Memory must have ALL of these
	MinConfidence float64      // Default: 0.0
	// SessionID is recorded on the recall.served event (ADR-0001 D7) so the
	// packer-vs-recall comparison (ADR-0002 P11) has both read paths
	// instrumented from the same day. Empty for uninstrumented callers.
	SessionID string
}

// MemoryUpdateInput is a partial update — nil fields are not updated.
type MemoryUpdateInput struct {
	Content    *string
	Summary    *string
	Type       *MemoryType
	Confidence *float64
	FilePaths  *[]string
	Tags       *[]string
	Status     *MemoryStatus
}

// ScoredMemory is a memory with retrieval relevance score.
type ScoredMemory struct {
	Memory         Memory         `json:"memory"`
	Score          float64        `json:"score"`
	ScoreBreakdown ScoreBreakdown `json:"score_breakdown"`
}

// ScoreBreakdown shows how the final score was computed.
type ScoreBreakdown struct {
	TextRelevance   float64 `json:"text_relevance"`
	Recency         float64 `json:"recency"`
	Confidence      float64 `json:"confidence"`
	AccessFrequency float64 `json:"access_frequency"`
}

// ListOptions controls how memories are listed.
type ListOptions struct {
	Limit  int          // Default: 50
	Offset int          // Default: 0
	Type   MemoryType   // Empty = no filter
	Status MemoryStatus // Empty = everything live (see MemoryStatus)
	Tags   []string
	Sort   string // "created_at" | "updated_at" | "accessed_at"
	Order  string // "asc" | "desc"
}

// FTSResult holds a memory id and BM25 rank from a full-text search.
// BM25 rank is negative — more negative means a better match.
//
// It carries the id rather than a SQLite rowid because the search now spans
// two FTS tables (decisions_fts and notes_fts), whose rowid spaces are
// separate and would collide.
type FTSResult struct {
	ID   string
	Rank float64
}

// Project represents a registered memtrace project.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RootPath  string    `json:"root_path"`
	CreatedAt time.Time `json:"created_at"`
}
