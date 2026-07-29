package kernel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// importFixture is deliberately not minimal. The interesting states for an
// importer are all in the interactions — re-import, undo, re-import after undo,
// a human-touched row, an individually rejected row — and a two-row fixture
// cannot reach any of them.
func importFixture() []ImportCandidate {
	return []ImportCandidate{
		{SourceRef: "engram:d1", AsDecision: true, Kind: types.DecisionKindDecision,
			Title: "Use sqlc", Content: "Hand-written scanning drifted", Scope: []string{"internal/db/*.go"}},
		{SourceRef: "engram:d2", AsDecision: true, Kind: types.DecisionKindConvention,
			Title: "Tests colocate", Content: "Keep tests beside code"},
		{SourceRef: "claude-mem:1", Content: "Session note about the auth refactor"},
		{SourceRef: "claude-mem:2", Content: "Session note about the deploy"},
	}
}

func TestImportBatch_QuarantinesEverythingAndCarriesBirthEvents(t *testing.T) {
	k := setupTestKernel(t)
	before := time.Now().UTC().Add(-time.Second)

	res, err := k.ImportBatch("engram", importFixture(), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decisions != 2 || res.Notes != 2 || len(res.Errors) != 0 {
		t.Fatalf("counts = %+v", res)
	}

	ds, err := k.decisions.ListDecisions(DecisionFilter{ProjectID: k.projectID})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 {
		t.Fatalf("want 2 decisions, got %d", len(ds))
	}
	for _, d := range ds {
		// Binding input 3: an import of 400 notes must not become 400 binding
		// decisions.
		if d.Status != types.StatusProposed {
			t.Fatalf("%s landed %s, want proposed", d.ID, d.Status)
		}
		if d.Source != types.DecisionSourceImport {
			t.Fatalf("%s source = %s", d.ID, d.Source)
		}
		// §D2.3 / invariant I1: imported rows have birth events, so they are
		// NOT migration-born and DO count toward falsifier 1's population.
		born, err := k.decisions.MigrationBorn(d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if born {
			t.Fatalf("%s has no birth event — imported rows must satisfy I1", d.ID)
		}
		// §D2.3: import time, never a timestamp carried from old-looking source
		// material. This exact interaction has fired twice before.
		if d.StatusChangedAt.Before(before) {
			t.Fatalf("%s status_changed_at = %v, want import time", d.ID, d.StatusChangedAt)
		}
		if d.CreatedAt.Before(before) {
			t.Fatalf("%s created_at = %v, want import time", d.ID, d.CreatedAt)
		}
		// §D2.3: one evidence row at birth, so accept needs no --force.
		ev, err := k.decisions.Evidence(d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(ev) != 1 || ev[0].Kind != types.EvidenceKindImport || ev[0].AddedBy != types.ActorSystem {
			t.Fatalf("%s evidence = %+v", d.ID, ev)
		}
		// §D2.5: never assigns topic_key.
		if d.TopicKey != "" {
			t.Fatalf("%s got a topic_key from the importer", d.ID)
		}
		if !hasTag(d.Tags, ImportBatchTagPrefix+res.BatchID) {
			t.Fatalf("%s tags = %v, want the batch tag", d.ID, d.Tags)
		}
	}

	var payload struct {
		Batch     string `json:"batch"`
		Decisions int    `json:"decisions"`
		Notes     int    `json:"notes"`
		Source    string `json:"source"`
	}
	var raw string
	if err := k.db.QueryRow(`SELECT payload FROM events WHERE kind = 'import.completed'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Batch != res.BatchID || payload.Decisions != 2 || payload.Notes != 2 || payload.Source != "engram" {
		t.Fatalf("import.completed payload = %s", raw)
	}
}

// Falsifier 3: any re-run against unchanged sources that creates >0 rows
// falsifies the key design for that source.
func TestImportBatch_ReRunCreatesZeroRows(t *testing.T) {
	k := setupTestKernel(t)
	if _, err := k.ImportBatch("engram", importFixture(), false); err != nil {
		t.Fatal(err)
	}
	res, err := k.ImportBatch("engram", importFixture(), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decisions != 0 || res.Notes != 0 {
		t.Fatalf("re-run created rows: %+v", res)
	}
	if len(res.Skipped) != 4 {
		t.Fatalf("skipped = %v, want all four listed (never silent)", res.Skipped)
	}
	var n int
	k.db.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&n)
	if n != 2 {
		t.Fatalf("decision count after re-run = %d, want 2", n)
	}
}

func TestImportBatch_DryRunWritesNothing(t *testing.T) {
	k := setupTestKernel(t)
	res, err := k.ImportBatch("engram", importFixture(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decisions != 2 || res.Notes != 2 {
		t.Fatalf("dry run should still count: %+v", res)
	}
	var rows, evs int
	k.db.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&rows)
	k.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&evs)
	if rows != 0 || evs != 0 {
		t.Fatalf("dry run wrote %d decisions and %d events", rows, evs)
	}
}

func TestUndoImport_ReversesInfluenceAndSparesHumanWork(t *testing.T) {
	k := setupTestKernel(t)
	res, err := k.ImportBatch("engram", importFixture(), false)
	if err != nil {
		t.Fatal(err)
	}
	ds, _ := k.decisions.ListDecisions(DecisionFilter{ProjectID: k.projectID})
	var accepted, untouched *types.Decision
	for i := range ds {
		if ds[i].SourceRef == "engram:d1" {
			accepted = &ds[i]
		} else {
			untouched = &ds[i]
		}
	}
	if accepted == nil || untouched == nil {
		t.Fatal("fixture did not produce both decisions")
	}
	if _, err := k.decisions.Accept(accepted.ID, AcceptOptions{Actor: types.ActorHuman}); err != nil {
		t.Fatal(err)
	}

	undo, err := k.UndoImport(res.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if undo.NotesDeleted != 2 {
		t.Fatalf("notes deleted = %d, want 2", undo.NotesDeleted)
	}
	if undo.DecisionsRejected != 1 {
		t.Fatalf("decisions rejected = %d, want 1", undo.DecisionsRejected)
	}
	// Undo never destroys a human's work.
	if len(undo.LeftUntouched) != 1 || undo.LeftUntouched[0] != accepted.ID {
		t.Fatalf("left untouched = %v, want [%s]", undo.LeftUntouched, accepted.ID)
	}
	after, _ := k.decisions.GetDecision(accepted.ID)
	if after.Status != types.StatusActive {
		t.Fatalf("accepted row was moved to %s by undo", after.Status)
	}
	rejected, _ := k.decisions.GetDecision(untouched.ID)
	if rejected.Status != types.StatusRejected {
		t.Fatalf("untouched proposal = %s, want rejected", rejected.Status)
	}
	var reason, batch string
	k.db.QueryRow(`SELECT json_extract(payload,'$.reason'), json_extract(payload,'$.batch')
	                 FROM events WHERE kind='decision.rejected' AND decision_id=?`,
		untouched.ID).Scan(&reason, &batch)
	if reason != "import_undo" || batch != res.BatchID {
		t.Fatalf("rejection payload = %q / %q", reason, batch)
	}
	var undone int
	k.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='import.undone'`).Scan(&undone)
	if undone != 1 {
		t.Fatalf("import.undone events = %d", undone)
	}
}

// §D2.2's exception is what makes import → undo → import work, and its limit is
// what keeps a human's "no" final.
func TestReImportAfterUndo_RecreatesExceptIndividuallyRejected(t *testing.T) {
	k := setupTestKernel(t)
	res, err := k.ImportBatch("engram", importFixture(), false)
	if err != nil {
		t.Fatal(err)
	}
	ds, _ := k.decisions.ListDecisions(DecisionFilter{ProjectID: k.projectID})
	var d2 types.Decision
	for _, d := range ds {
		if d.SourceRef == "engram:d2" {
			d2 = d
		}
	}
	// A human says no to this one specifically.
	if err := k.decisions.Reject(d2.ID, "not our style", types.ActorHuman); err != nil {
		t.Fatal(err)
	}
	if _, err := k.UndoImport(res.BatchID); err != nil {
		t.Fatal(err)
	}

	again, err := k.ImportBatch("engram", importFixture(), false)
	if err != nil {
		t.Fatal(err)
	}
	// d1 (undone) and both notes come back; d2 (human-rejected) does not.
	if again.Decisions != 1 || again.Notes != 2 {
		t.Fatalf("re-import after undo = %+v, want 1 decision and 2 notes", again)
	}
	if len(again.Skipped) != 1 || again.Skipped[0] != "engram:d2" {
		t.Fatalf("skipped = %v, want the individually rejected row only", again.Skipped)
	}
}

func TestUndoImport_DefaultsToLatestBatch(t *testing.T) {
	k := setupTestKernel(t)
	if _, err := k.ImportBatch("claude-mem", []ImportCandidate{
		{SourceRef: "claude-mem:9", Content: "first batch note"}}, false); err != nil {
		t.Fatal(err)
	}
	second, err := k.ImportBatch("engram", importFixture(), false)
	if err != nil {
		t.Fatal(err)
	}
	undo, err := k.UndoImport("")
	if err != nil {
		t.Fatal(err)
	}
	if undo.BatchID != second.BatchID {
		t.Fatalf("undo defaulted to %s, want the most recent batch %s", undo.BatchID, second.BatchID)
	}
	notes, _ := k.notes.List(NoteFilter{ProjectID: k.projectID})
	if len(notes) != 1 || !strings.Contains(notes[0].Content, "first batch") {
		t.Fatalf("undo touched the wrong batch: %+v", notes)
	}
}
