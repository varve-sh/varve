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
	EventDecisionProposed           EventKind = "decision.proposed"
	EventDecisionAccepted           EventKind = "decision.accepted"
	EventDecisionRejected           EventKind = "decision.rejected"
	EventDecisionUpdated            EventKind = "decision.updated"
	EventDecisionSuperseded         EventKind = "decision.superseded"
	EventDecisionViolated           EventKind = "decision.violated"
	EventDecisionViolationDismissed EventKind = "decision.violation_dismissed"
	EventDecisionReinstated         EventKind = "decision.reinstated"
	EventDecisionReverted           EventKind = "decision.reverted"
	EventDecisionExpired            EventKind = "decision.expired"
	EventEvidenceAdded              EventKind = "evidence.added"
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
)

// knownEventKinds is the validation set for the kernel's write path.
var knownEventKinds = map[EventKind]bool{
	EventDecisionProposed:           true,
	EventDecisionAccepted:           true,
	EventDecisionRejected:           true,
	EventDecisionUpdated:            true,
	EventDecisionSuperseded:         true,
	EventDecisionViolated:           true,
	EventDecisionViolationDismissed: true,
	EventDecisionReinstated:         true,
	EventDecisionReverted:           true,
	EventDecisionExpired:            true,
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
}

// IsKnownEventKind reports whether k is in the ADR-0001 §D7 catalogue.
func IsKnownEventKind(k EventKind) bool { return knownEventKinds[k] }
