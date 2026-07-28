package types

import "time"

// DecisionKind distinguishes a scoped decision from a standing convention.
// Both carry the identical lifecycle (ADR-0001 D4); the split preserves the
// user-facing vocabulary and changes empty-scope semantics only.
type DecisionKind string

const (
	DecisionKindDecision   DecisionKind = "decision"
	DecisionKindConvention DecisionKind = "convention"
)

// DecisionStatus is one of the six lifecycle states (ADR-0001 D2).
type DecisionStatus string

const (
	// StatusProposed is the mandatory entry state for agent/git/import/derived
	// sources. Not binding, never packed.
	StatusProposed DecisionStatus = "proposed"
	// StatusActive is a human-confirmed, binding decision.
	StatusActive DecisionStatus = "active"
	// StatusViolated is an active decision with >=1 unresolved violation.
	// Still binding — a violation is a fact about the codebase, not a repeal.
	StatusViolated DecisionStatus = "violated"
	// StatusSuperseded is terminal: replaced by an accepted successor.
	StatusSuperseded DecisionStatus = "superseded"
	// StatusReverted is terminal: the decision itself was undone.
	StatusReverted DecisionStatus = "reverted"
	// StatusRejected is terminal: a proposal declined by a human, or (migration
	// only) a v1 archived decision with no successor.
	StatusRejected DecisionStatus = "rejected"
)

// AllDecisionStatuses lists every state, in lifecycle order.
var AllDecisionStatuses = []DecisionStatus{
	StatusProposed, StatusActive, StatusViolated,
	StatusSuperseded, StatusReverted, StatusRejected,
}

// IsTerminal reports whether s admits no outgoing transitions (ADR-0001 D3).
func (s DecisionStatus) IsTerminal() bool {
	switch s {
	case StatusSuperseded, StatusReverted, StatusRejected:
		return true
	}
	return false
}

// DecisionSource records where a decision came from. It determines the birth
// state: only "user" may be born active (ADR-0001 D2).
type DecisionSource string

const (
	DecisionSourceUser    DecisionSource = "user"
	DecisionSourceAgent   DecisionSource = "agent"
	DecisionSourceGit     DecisionSource = "git"
	DecisionSourceImport  DecisionSource = "import"
	DecisionSourceDerived DecisionSource = "derived"
)

// BornQuarantined reports whether a decision from this source must be born
// `proposed` and stay there until a human accepts it (ADR-0001 D2).
//
// Every source is quarantined, including `user`. D2 lets a human-sourced
// decision be born active because "the human is the confirmation", but the
// kernel implements that as a real proposed→active transition inside one
// transaction (DecisionStore.ProposeAccepted), not as a second birth state —
// so no active row exists without the decision.accepted event that D4 makes
// the audit witness for forced acceptances.
func (s DecisionSource) BornQuarantined() bool { return true }

// MayBeBornActive reports whether ProposeAccepted will accept this source:
// only `user`. An agent asserting "the user approved" is exactly the assertion
// the quarantine exists to distrust.
func (s DecisionSource) MayBeBornActive() bool { return s == DecisionSourceUser }

// Decision is the core governed entity (ADR-0001 D1, D4).
type Decision struct {
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	Kind       DecisionKind   `json:"kind"`
	Title      string         `json:"title"`
	Body       string         `json:"body"`
	Status     DecisionStatus `json:"status"`
	Scope      []string       `json:"scope"`
	Confidence float64        `json:"confidence"`
	Source     DecisionSource `json:"source"`
	SourceRef  string         `json:"source_ref,omitempty"`

	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	SessionID string `json:"session_id,omitempty"`

	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	TopicKey  string     `json:"topic_key,omitempty"`
	// PendingTopicKey is the topic_key a proposed successor claims but cannot
	// hold yet, because a non-terminal predecessor still holds it. It transfers
	// to TopicKey in the acceptance transaction (ADR-0001 Amendment 1). Never
	// set at the same time as TopicKey.
	PendingTopicKey string   `json:"pending_topic_key,omitempty"`
	Tags            []string `json:"tags"`
	Supersedes      []string `json:"supersedes"`
	SupersededBy    string   `json:"superseded_by,omitempty"`

	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	StatusChangedAt time.Time  `json:"status_changed_at"`
	AccessedAt      *time.Time `json:"accessed_at,omitempty"`
	AccessCount     int        `json:"access_count"`
}

// IsExpired reports whether the decision's expiry has passed. Expiry is a
// derived predicate, never a state (ADR-0001 D2).
func (d *Decision) IsExpired(now time.Time) bool {
	return d.ExpiresAt != nil && d.ExpiresAt.Before(now)
}

// EvidenceKind enumerates the accepted evidence reference types.
type EvidenceKind string

const (
	EvidenceKindCommit EvidenceKind = "commit"
	EvidenceKindPR     EvidenceKind = "pr"
	EvidenceKindTest   EvidenceKind = "test"
	EvidenceKindFile   EvidenceKind = "file"
	EvidenceKindURL    EvidenceKind = "url"
	EvidenceKindImport EvidenceKind = "import"
)

// Evidence is a reference supporting a decision. Accepting is 1 iff the row
// existed at the decision's proposed→active transition; the D6 revert rule
// fires only on accepting evidence.
type Evidence struct {
	ID         string       `json:"id"`
	DecisionID string       `json:"decision_id"`
	Kind       EvidenceKind `json:"kind"`
	Ref        string       `json:"ref"`
	Note       string       `json:"note,omitempty"`
	AddedBy    Actor        `json:"added_by"`
	Accepting  bool         `json:"accepting"`
	CreatedAt  time.Time    `json:"created_at"`
}

// Note is a demoted v1 fact/event row: retrievable, deliberately ungoverned.
type Note struct {
	ID          string       `json:"id"`
	ProjectID   string       `json:"project_id"`
	Content     string       `json:"content"`
	Summary     string       `json:"summary,omitempty"`
	Source      MemorySource `json:"source"`
	SourceRef   string       `json:"source_ref,omitempty"`
	Confidence  float64      `json:"confidence"`
	FilePaths   []string     `json:"file_paths"`
	Tags        []string     `json:"tags"`
	Status      MemoryStatus `json:"status"`
	TopicKey    string       `json:"topic_key,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	AccessedAt  *time.Time   `json:"accessed_at,omitempty"`
	AccessCount int          `json:"access_count"`
}

// Actor is who caused an event. Only these three values are legal.
type Actor string

const (
	ActorHuman  Actor = "human"
	ActorAgent  Actor = "agent"
	ActorSystem Actor = "system"
)

// Event is one append-only row of the log (ADR-0001 D7).
type Event struct {
	Seq        int64          `json:"seq"`
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	Timestamp  time.Time      `json:"ts"`
	Kind       EventKind      `json:"kind"`
	Actor      Actor          `json:"actor"`
	DecisionID string         `json:"decision_id,omitempty"`
	SessionID  string         `json:"session_id,omitempty"`
	Agent      string         `json:"agent,omitempty"`
	Model      string         `json:"model,omitempty"`
	CommitSHA  string         `json:"commit_sha,omitempty"`
	Payload    map[string]any `json:"payload"`
}
