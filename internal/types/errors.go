package types

import "errors"

var (
	ErrNotInitialized = errors.New("memtrace is not initialized in this directory — run 'memtrace init' first")
	ErrMemoryNotFound = errors.New("memory not found")
	ErrValidation     = errors.New("validation error")

	// ErrDecisionNotFound is returned when a decision id does not resolve.
	ErrDecisionNotFound = errors.New("decision not found")
	// ErrIllegalTransition wraps a rejected lifecycle state change (ADR-0001 D3).
	ErrIllegalTransition = errors.New("illegal decision status transition")
	// ErrNoEvidence gates proposed→active: acceptance requires at least one
	// evidence row unless the caller passes --force (ADR-0001 D4).
	ErrNoEvidence = errors.New("acceptance requires at least one evidence row (use --force to bypass, which is recorded in the audit trail)")
	// ErrDecisionImmutable is returned when normative content (title, body,
	// scope, kind) is edited after acceptance — supersede instead (ADR-0001 D3).
	ErrDecisionImmutable = errors.New("accepted decisions are immutable; supersede instead")
	// ErrLegacyDatabase is returned by Open() on a v1 database. v1 databases are
	// never auto-migrated (ADR-0001 D9).
	ErrLegacyDatabase = errors.New("this is a v1 database — run 'memtrace migrate --from-v1' to convert it")
	// ErrUnknownEventKind rejects an event kind outside the ADR-0001 §D7 catalogue.
	ErrUnknownEventKind = errors.New("unknown event kind")
	// ErrMigrationNotReady blocks the v1→v2 conversion while the v2 read paths
	// (ADR-0001 §D10) are not wired up. Converting before then would move every
	// row somewhere nothing reads.
	ErrMigrationNotReady = errors.New("the v1 to v2 conversion is not ready to run yet")
)

// ErrTopicKeyHeld is returned when a proposal's claimed topic_key is held by a
// non-terminal decision the proposal does not supersede (ADR-0001 Amendment 1,
// D5). The whole acceptance fails rather than silently dropping the key:
// accepting the row without its claimed key would change what the human saved.
var ErrTopicKeyHeld = errors.New("topic_key is held by another decision")

// TopicKeyHeldError names the current holder so the caller can act on it.
// Recovery is one explicit step: add the holder to `supersedes` and re-accept,
// clear the pending key, or reject the proposal.
type TopicKeyHeldError struct {
	TopicKey string
	HolderID string
	// HolderStatus is the holder's lifecycle state, so the message can say
	// whether it is a competing proposal or a live decision.
	HolderStatus DecisionStatus
}

func (e *TopicKeyHeldError) Error() string {
	return "topic_key " + e.TopicKey + " is already held by decision " + e.HolderID +
		" (" + string(e.HolderStatus) + "); supersede it, clear the pending key, or reject this proposal"
}

func (e *TopicKeyHeldError) Unwrap() error { return ErrTopicKeyHeld }

// ErrNoteTopicKeyHeld mirrors ErrTopicKeyHeld for notes: reactivating a
// stale/archived note whose topic_key an active note has taken is rejected
// (ADR-0001 Amendment 1, same-class audit item 3).
var ErrNoteTopicKeyHeld = errors.New("note topic_key is held by an active note")

// NoteTopicKeyHeldError names the active holder.
type NoteTopicKeyHeldError struct {
	TopicKey string
	HolderID string
}

func (e *NoteTopicKeyHeldError) Error() string {
	return "cannot reactivate: topic_key " + e.TopicKey +
		" is held by active note " + e.HolderID
}

func (e *NoteTopicKeyHeldError) Unwrap() error { return ErrNoteTopicKeyHeld }

// ValidationError wraps ErrValidation with field-level detail.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

func (e *ValidationError) Unwrap() error {
	return ErrValidation
}
