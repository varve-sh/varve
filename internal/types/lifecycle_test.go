package types

import (
	"errors"
	"testing"
)

// The full 6x6 matrix of ADR-0001 §D3, transcribed independently of
// legalTransitions so a typo in either is caught.
func TestCanTransition_MatchesADR0001D3(t *testing.T) {
	legal := map[string]bool{
		"proposed->active":     true,
		"proposed->superseded": true,
		"proposed->rejected":   true,
		"active->violated":     true,
		"active->superseded":   true,
		"active->reverted":     true,
		"violated->active":     true,
		"violated->superseded": true,
		"violated->reverted":   true,
	}

	for _, from := range AllDecisionStatuses {
		for _, to := range AllDecisionStatuses {
			if from == to {
				continue
			}
			key := string(from) + "->" + string(to)
			want := legal[key]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestCanTransition_SameStatusIsNotATransition(t *testing.T) {
	for _, s := range AllDecisionStatuses {
		if !CanTransition(s, s) {
			t.Errorf("%s -> %s should be a no-op, not a rejection", s, s)
		}
	}
}

func TestTerminalStatesHaveNoExits(t *testing.T) {
	for _, s := range AllDecisionStatuses {
		if !s.IsTerminal() {
			continue
		}
		for _, to := range AllDecisionStatuses {
			if to == s {
				continue
			}
			if CanTransition(s, to) {
				t.Errorf("%s is terminal but allows %s -> %s", s, s, to)
			}
		}
	}
}

func TestIsTerminal(t *testing.T) {
	want := map[DecisionStatus]bool{
		StatusProposed: false, StatusActive: false, StatusViolated: false,
		StatusSuperseded: true, StatusReverted: true, StatusRejected: true,
	}
	for s, w := range want {
		if s.IsTerminal() != w {
			t.Errorf("%s.IsTerminal() = %v, want %v", s, s.IsTerminal(), w)
		}
	}
}

func TestCheckTransition_WrapsErrIllegalTransition(t *testing.T) {
	err := CheckTransition(StatusRejected, StatusActive)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("error does not wrap ErrIllegalTransition: %v", err)
	}
	var ite *IllegalTransitionError
	if !errors.As(err, &ite) || ite.From != StatusRejected || ite.To != StatusActive {
		t.Errorf("error lost its detail: %v", err)
	}
	if err := CheckTransition(StatusProposed, StatusActive); err != nil {
		t.Errorf("legal transition returned %v", err)
	}
}

// Only a human-sourced decision may be born active; everything else is
// quarantined as proposed (ADR-0001 D2).
func TestBirthStatus(t *testing.T) {
	want := map[DecisionSource]DecisionStatus{
		DecisionSourceUser:    StatusActive,
		DecisionSourceAgent:   StatusProposed,
		DecisionSourceGit:     StatusProposed,
		DecisionSourceImport:  StatusProposed,
		DecisionSourceDerived: StatusProposed,
	}
	for src, w := range want {
		if got := src.BirthStatus(); got != w {
			t.Errorf("%s.BirthStatus() = %s, want %s", src, got, w)
		}
	}
}
