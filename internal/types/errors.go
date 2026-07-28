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
)

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
