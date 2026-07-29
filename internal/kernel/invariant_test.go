package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/varve-sh/varve/internal/types"
)

// Invariant I1 (ADR-0001 §D7, made normative by Amendment 4): every
// non-migration creation path writes the decision's birth event in the creating
// transaction.
//
// I1 is what makes "has no birth event" mean *exactly* "migration-born", and
// three rules key on that predicate: purge's hard-delete arm, and falsifiers 1
// and 4. F31 is what happens when an emptiness test's premise is false for a
// population nobody enumerated, so this is enforced two ways — structurally
// (only sanctioned code may insert the row) and behaviourally (every path that
// creates a decision is exercised and asserted).

// The structural half. A new creation path that inserts directly would satisfy
// every behavioural test written today and still rot the predicate, so the
// insert itself is fenced: exactly two non-test sites may write the row — the
// birth-event pairing, and the migration, which is §D7's documented exception.
func TestI1_OnlyTheSanctionedPathsInsertDecisions(t *testing.T) {
	sanctioned := map[string]string{
		"decisions.go":  "insertDecisionWithBirthEventTx — the row and its birth event together",
		"migrate_v1.go": "the reimport; §D7's documented migration exception, and the population MigrationBorn identifies",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "INSERT INTO decisions ") &&
			!strings.Contains(string(src), "INSERT INTO decisions(") {
			continue
		}
		if _, ok := sanctioned[name]; !ok {
			t.Errorf("%s inserts into `decisions` outside the sanctioned paths. "+
				"I1 requires the row and its birth event to be written together — "+
				"use insertDecisionWithBirthEventTx, or extend this list with a reason "+
				"and say what the predicate means for the rows it creates", name)
		}
	}
	// The fence is only meaningful if the sanctioned site actually exists.
	src, err := os.ReadFile("decisions.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "func (s *DecisionStore) insertDecisionWithBirthEventTx") {
		t.Fatal("the sanctioned creation path is gone; I1 has no home")
	}
}

// The behavioural half: every path a decision can be created through, asserted
// to leave a birth event behind — and the migration's rows asserted to leave
// none, because that absence is the predicate.
func TestI1_EveryCreationPathWritesItsBirthEvent(t *testing.T) {
	k := packKernel(t)

	create := map[string]func(t *testing.T) string{
		"Propose": func(t *testing.T) string {
			d, err := k.Decisions().Propose(DecisionInput{
				ProjectID: testProject, Title: "Proposed directly",
				Source: types.DecisionSourceUser,
			})
			if err != nil {
				t.Fatal(err)
			}
			return d.ID
		},
		"ProposeAccepted": func(t *testing.T) string {
			d, err := k.Decisions().ProposeAccepted(DecisionInput{
				ProjectID: testProject, Title: "Born active",
				Source: types.DecisionSourceUser,
			}, AcceptOptions{Force: true, Actor: types.ActorHuman})
			if err != nil {
				t.Fatal(err)
			}
			return d.ID
		},
		"kernel.Save (user)": func(t *testing.T) string {
			m, _, err := k.Save(types.MemorySaveInput{
				Content: "A human's decision.", Type: types.MemoryTypeDecision,
				Source: types.MemorySourceUser,
			})
			if err != nil {
				t.Fatal(err)
			}
			return m.ID
		},
		"kernel.Save (agent)": func(t *testing.T) string {
			m, _, err := k.Save(types.MemorySaveInput{
				Content: "An agent's proposal.", Type: types.MemoryTypeDecision,
				Source: types.MemorySourceAgent, SessionID: "s1",
			})
			if err != nil {
				t.Fatal(err)
			}
			return m.ID
		},
		"PromoteNote": func(t *testing.T) string {
			n, _, err := k.Save(types.MemorySaveInput{
				Content: "A note worth promoting.", Type: types.MemoryTypeFact,
				Source: types.MemorySourceUser,
			})
			if err != nil {
				t.Fatal(err)
			}
			d, err := k.PromoteNote(n.ID, PromoteOverrides{})
			if err != nil {
				t.Fatal(err)
			}
			return d.ID
		},
		"topic_key successor": func(t *testing.T) string {
			if _, _, err := k.Save(types.MemorySaveInput{
				Content: "First holder.", Type: types.MemoryTypeDecision,
				Source: types.MemorySourceUser, TopicKey: "i1-topic",
			}); err != nil {
				t.Fatal(err)
			}
			m, _, err := k.Save(types.MemorySaveInput{
				Content: "Successor.", Type: types.MemoryTypeDecision,
				Source: types.MemorySourceUser, TopicKey: "i1-topic",
			})
			if err != nil {
				t.Fatal(err)
			}
			return m.ID
		},
	}

	for name, fn := range create {
		id := fn(t)
		evs, err := k.Decisions().Events(EventFilter{
			DecisionID: id, Kind: types.EventDecisionProposed,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 {
			t.Errorf("%s: %d decision.proposed events, want 1 — without it the row "+
				"reads as migration-born and becomes hard-deletable (I1)", name, len(evs))
		}
		born, err := k.Decisions().MigrationBorn(id)
		if err != nil {
			t.Fatal(err)
		}
		if born {
			t.Errorf("%s: MigrationBorn = true for a row this process created", name)
		}
	}
}

// The other side of the predicate: a row written the way the migration writes
// one *is* migration-born, and says so.
func TestI1_MigrationBornRowsAreIdentifiable(t *testing.T) {
	k := packKernel(t)
	id := seedMigrationBornDecision(t, k, "Sessions are server-side only")

	born, err := k.Decisions().MigrationBorn(id)
	if err != nil {
		t.Fatal(err)
	}
	if !born {
		t.Error("a row with no birth event must read as migration-born")
	}
}
