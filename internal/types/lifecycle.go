package types

import "fmt"

// legalTransitions is ADR-0001 §D3's transition matrix, exhaustively. Any
// ordered pair not present here is illegal and is rejected both here and by
// the decisions_status_guard DDL trigger. The trigger is the backstop against
// a writer that bypasses the kernel; this map is the same law in Go, so the
// kernel can produce a useful error instead of a SQLite ABORT.
var legalTransitions = map[DecisionStatus]map[DecisionStatus]bool{
	StatusProposed: {
		StatusActive:     true, // human accepts; requires >=1 evidence row
		StatusSuperseded: true, // an accepted successor names this row
		StatusRejected:   true, // human declines
	},
	StatusActive: {
		StatusViolated:   true, // automatic, deterministic detector (D6)
		StatusSuperseded: true, // automatic on successor acceptance
		StatusReverted:   true, // accepting evidence reverted, or human repeal
	},
	StatusViolated: {
		StatusActive:     true, // counter-revert, or human dismissal
		StatusSuperseded: true,
		StatusReverted:   true,
	},
	// Terminal: superseded, reverted, rejected. No exits, ever.
	StatusSuperseded: {},
	StatusReverted:   {},
	StatusRejected:   {},
}

// ErrIllegalTransition detail.
type IllegalTransitionError struct {
	From DecisionStatus
	To   DecisionStatus
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("illegal decision status transition: %s -> %s", e.From, e.To)
}

func (e *IllegalTransitionError) Unwrap() error { return ErrIllegalTransition }

// CanTransition reports whether from -> to is legal. A no-op transition
// (from == to) is legal and is not a state change.
func CanTransition(from, to DecisionStatus) bool {
	if from == to {
		return true
	}
	return legalTransitions[from][to]
}

// CheckTransition returns an *IllegalTransitionError when from -> to is not
// permitted by the matrix.
func CheckTransition(from, to DecisionStatus) error {
	if !CanTransition(from, to) {
		return &IllegalTransitionError{From: from, To: to}
	}
	return nil
}
