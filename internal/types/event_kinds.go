package types

// EventKind is an event's kind. The DDL deliberately does not CHECK this
// column — new kinds must be addable without a schema migration — so the
// catalogue below is the contract and the kernel validates against it at
// write time (ADR-0001 D8, notes on the DDL).
//
// ADR-0001 §D7 is the single authoritative catalogue. ADR-0002 emits these
// shapes and ADR-0004 consumes them; if any other document disagrees with
// this list, ADR-0001 §D7 wins.
type EventKind string

// Lifecycle events. decision_id is set on all of these.
const (
	EventDecisionProposed EventKind = "decision.proposed"
	EventDecisionAccepted EventKind = "decision.accepted"
	EventDecisionRejected EventKind = "decision.rejected"
	EventDecisionUpdated  EventKind = "decision.updated"
	// EventDecisionRevised is a normative edit while `proposed` — title, body,
	// scope, kind, supersedes or pending_topic_key (ADR-0001 Amendment 2,
	// A2.1). It exists as its own kind, rather than as a field list inside
	// decision.updated, because `kind` is the promoted indexed discriminator
	// the catalogue is built on: a consumer filtering on decision.updated must
	// be able to trust that no normative change happened, and scope is what the
	// whole attribution chain joins on.
	EventDecisionRevised            EventKind = "decision.revised"
	EventDecisionSuperseded         EventKind = "decision.superseded"
	EventDecisionViolated           EventKind = "decision.violated"
	EventDecisionViolationDismissed EventKind = "decision.violation_dismissed"
	EventDecisionReinstated         EventKind = "decision.reinstated"
	EventDecisionReverted           EventKind = "decision.reverted"
	EventDecisionExpired            EventKind = "decision.expired"
	// EventDecisionDisposalRequested records an MCP-channel forget of a
	// decision (ADR-0001 Amendment 3, A3.1). It transitions nothing: "the user
	// wanted this thrown away" is exactly as untrustworthy as "the user
	// approved" (OQ3), and it applies with more force to a *binding* decision,
	// where the old mapping let an agent launder a repeal into
	// active → reverted. The terminal call stays human. Repeats are legal facts
	// — no dedup index, deliberately.
	EventDecisionDisposalRequested EventKind = "decision.disposal_requested"
	// EventDecisionPurged records an irreversible removal (ADR-0001
	// Amendment 4). Two shapes: the redaction arm sets decision_id and lists
	// the redacted field *names* (never their content); the hard-delete arm
	// leaves decision_id NULL — the row is gone, so the FK cannot reference it
	// — and carries the purged id in the payload. Human channel only; never
	// part of any attribution join.
	EventDecisionPurged EventKind = "decision.purged"
	EventEvidenceAdded  EventKind = "evidence.added"
)

// Attribution-substrate events. Emitted by the packer (ADR-0002) and the
// diff observer (ADR-0004); the kinds are registered here so those
// components need no schema or catalogue change when they land.
const (
	EventSessionStarted  EventKind = "session.started"
	EventSessionEnded    EventKind = "session.ended"
	EventPackServed      EventKind = "pack.served"
	EventPackItem        EventKind = "pack.item"
	EventRecallServed    EventKind = "recall.served"
	EventObserverEnabled EventKind = "observer.enabled"
	EventDiffObserved    EventKind = "diff.observed"
	EventDiffScopeMatch  EventKind = "diff.scope_match"
	EventRevertDetected  EventKind = "revert.detected"
	EventMigrationDone   EventKind = "migration.completed"
	// The importer and linter kinds (ADR-0005 §D6, folded into §D7 by
	// ADR-0001 Amendment 6). Additive: §D8 has no kind CHECK, so no DDL.
	EventImportCompleted EventKind = "import.completed"
	EventImportUndone    EventKind = "import.undone"
	EventLintCompleted   EventKind = "lint.completed"
)

// knownEventKinds is the validation set for the kernel's write path.
var knownEventKinds = map[EventKind]bool{
	EventDecisionProposed:           true,
	EventDecisionAccepted:           true,
	EventDecisionRejected:           true,
	EventDecisionUpdated:            true,
	EventDecisionRevised:            true,
	EventDecisionSuperseded:         true,
	EventDecisionViolated:           true,
	EventDecisionViolationDismissed: true,
	EventDecisionReinstated:         true,
	EventDecisionReverted:           true,
	EventDecisionExpired:            true,
	EventDecisionDisposalRequested:  true,
	EventDecisionPurged:             true,
	EventEvidenceAdded:              true,
	EventSessionStarted:             true,
	EventSessionEnded:               true,
	EventPackServed:                 true,
	EventPackItem:                   true,
	EventRecallServed:               true,
	EventObserverEnabled:            true,
	EventDiffObserved:               true,
	EventDiffScopeMatch:             true,
	EventRevertDetected:             true,
	EventMigrationDone:              true,
	EventImportCompleted:            true,
	EventImportUndone:               true,
	EventLintCompleted:              true,
}

// NormativeDecisionFields is A2.1's normative-field set: an edit touching any
// of these while `proposed` emits decision.revised, never decision.updated.
// It lives here, once, because a distributed copy of this set is a contract
// that rots — it has already drifted once (Amendment 1 added
// pending_topic_key).
var NormativeDecisionFields = map[string]bool{
	"title":             true,
	"body":              true,
	"scope":             true,
	"kind":              true,
	"supersedes":        true,
	"pending_topic_key": true,
}

// AdvisoryDecisionFields is the other half of A2.1's contract: fields a
// `decision.updated` may name. Keeping both sets published makes the contract
// checkable in both directions — a `decision.revised` naming an advisory field
// is as much a breach as the reverse, and only one of the two was watched.
var AdvisoryDecisionFields = map[string]bool{
	"tags":       true,
	"confidence": true,
	"expires_at": true,
}

// IsNormativeDecisionField reports whether a field name is normative (A2.1).
//
// Note on `supersedes`: it is normative and today no code path edits it. §D5
// describes "the predecessor was removed from `supersedes` while proposed" as
// a real route to ErrTopicKeyHeld, so the set describes the contract that
// editing API will have to meet, not one that exists yet.
func IsNormativeDecisionField(field string) bool { return NormativeDecisionFields[field] }

// IsAdvisoryDecisionField reports whether a field name is advisory (A2.1).
func IsAdvisoryDecisionField(field string) bool { return AdvisoryDecisionFields[field] }

// IsKnownEventKind reports whether k is in the ADR-0001 §D7 catalogue.
func IsKnownEventKind(k EventKind) bool { return knownEventKinds[k] }
