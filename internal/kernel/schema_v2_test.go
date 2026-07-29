package kernel

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/varve-sh/varve/internal/types"
	"github.com/varve-sh/varve/internal/util"
)

// insertDecisionRaw writes a decision directly, bypassing the kernel, so the
// DDL can be tested on its own. The status guard is a BEFORE UPDATE trigger,
// so any starting status may be inserted.
func insertDecisionRaw(t *testing.T, db *sql.DB, status types.DecisionStatus) string {
	t.Helper()
	id := util.GenerateID()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var decidedAt any
	if status == types.StatusActive || status == types.StatusViolated {
		decidedAt = now
	}
	var supersededBy any
	if status == types.StatusSuperseded {
		supersededBy = insertDecisionRaw(t, db, types.StatusProposed)
	}

	_, err := db.Exec(`
		INSERT INTO decisions
		    (id, project_id, kind, title, body, status, created_at, updated_at,
		     status_changed_at, decided_at, superseded_by)
		VALUES (?, 'p1', 'decision', 'a title', '', ?, ?, ?, ?, ?, ?)`,
		id, string(status), now, now, now, decidedAt, supersededBy)
	if err != nil {
		t.Fatalf("insert %s decision: %v", status, err)
	}
	return id
}

func setStatus(db *sql.DB, id string, to types.DecisionStatus) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var decidedAt any
	if to == types.StatusActive || to == types.StatusViolated {
		decidedAt = now
	}
	var supersededBy any
	if to == types.StatusSuperseded {
		// Any existing row satisfies the NOT NULL check; self-reference keeps
		// the DDL test independent of supersession semantics.
		supersededBy = id
	}
	_, err := db.Exec(`
		UPDATE decisions
		   SET status = ?, status_changed_at = ?,
		       decided_at = COALESCE(decided_at, ?),
		       superseded_by = COALESCE(superseded_by, ?)
		 WHERE id = ?`, string(to), now, decidedAt, supersededBy, id)
	return err
}

// adrD3Matrix is ADR-0001 §D3's table, transcribed here directly from the ADR
// and deliberately NOT derived from types.CanTransition.
//
// The oracle used to be types.CanTransition, which is the Go twin of the
// trigger — that test could only prove the two agreed, never that either
// matched the spec, and a transcription error copied from one file to the
// other would pass. Each entry below cites its §D3 row.
var adrD3Matrix = map[types.DecisionStatus]map[types.DecisionStatus]bool{
	// §D3 row "proposed": active LEGAL (human accepts), superseded LEGAL
	// (successor names it), rejected LEGAL (human declines); violated and
	// reverted illegal.
	types.StatusProposed: {
		types.StatusActive: true, types.StatusViolated: false,
		types.StatusSuperseded: true, types.StatusReverted: false,
		types.StatusRejected: true,
	},
	// §D3 row "active": violated, superseded, reverted LEGAL; proposed illegal
	// (no demotion); rejected illegal (that state is for proposals).
	types.StatusActive: {
		types.StatusProposed: false, types.StatusViolated: true,
		types.StatusSuperseded: true, types.StatusReverted: true,
		types.StatusRejected: false,
	},
	// §D3 row "violated": active, superseded, reverted LEGAL; proposed and
	// rejected illegal.
	types.StatusViolated: {
		types.StatusProposed: false, types.StatusActive: true,
		types.StatusSuperseded: true, types.StatusReverted: true,
		types.StatusRejected: false,
	},
	// §D3 rows "superseded", "reverted", "rejected": every cell illegal.
	types.StatusSuperseded: {
		types.StatusProposed: false, types.StatusActive: false,
		types.StatusViolated: false, types.StatusReverted: false,
		types.StatusRejected: false,
	},
	types.StatusReverted: {
		types.StatusProposed: false, types.StatusActive: false,
		types.StatusViolated: false, types.StatusSuperseded: false,
		types.StatusRejected: false,
	},
	types.StatusRejected: {
		types.StatusProposed: false, types.StatusActive: false,
		types.StatusViolated: false, types.StatusSuperseded: false,
		types.StatusReverted: false,
	},
}

// The transition matrix of ADR-0001 §D3, enforced at the DDL level. Illegal
// transitions must be impossible, not merely discouraged in Go.
func TestDDL_StatusGuardEnforcesTransitionMatrix(t *testing.T) {
	db := freshDB(t)

	pairs := 0
	for _, from := range types.AllDecisionStatuses {
		for _, to := range types.AllDecisionStatuses {
			if from == to {
				continue
			}
			pairs++
			t.Run(fmt.Sprintf("%s_to_%s", from, to), func(t *testing.T) {
				id := insertDecisionRaw(t, db, from)
				err := setStatus(db, id, to)
				legal := adrD3Matrix[from][to]
				switch {
				case legal && err != nil:
					t.Fatalf("%s -> %s should be legal, got: %v", from, to, err)
				case !legal && err == nil:
					t.Fatalf("%s -> %s should be rejected by the DDL, but succeeded", from, to)
				case !legal && !strings.Contains(err.Error(), "illegal decision status transition"):
					t.Fatalf("%s -> %s rejected for the wrong reason: %v", from, to, err)
				}
			})
		}
	}
	if pairs != 30 {
		t.Fatalf("exercised %d ordered pairs, want 30 (6x6 minus the diagonal)", pairs)
	}
}

// The Go matrix and the DDL trigger must both match §D3 — checked against the
// independently transcribed table above, so a shared transcription error in
// lifecycle.go and schema_v2.go cannot hide.
func TestGoMatrixMatchesTheADRTranscription(t *testing.T) {
	for _, from := range types.AllDecisionStatuses {
		for _, to := range types.AllDecisionStatuses {
			if from == to {
				continue
			}
			want := adrD3Matrix[from][to]
			if got := types.CanTransition(from, to); got != want {
				t.Errorf("types.CanTransition(%s, %s) = %v, but §D3 says %v",
					from, to, got, want)
			}
		}
	}
}

func TestDDL_StatusGuardAllowsNoOpUpdate(t *testing.T) {
	db := freshDB(t)
	id := insertDecisionRaw(t, db, types.StatusActive)
	if err := setStatus(db, id, types.StatusActive); err != nil {
		t.Fatalf("same-status update should not trip the guard: %v", err)
	}
}

func TestDDL_ContentIsFrozenAfterAcceptance(t *testing.T) {
	db := freshDB(t)

	proposed := insertDecisionRaw(t, db, types.StatusProposed)
	if _, err := db.Exec(`UPDATE decisions SET title = 'edited' WHERE id = ?`, proposed); err != nil {
		t.Fatalf("proposed decisions are editable in place: %v", err)
	}

	for _, col := range []string{"title", "body", "scope", "kind"} {
		id := insertDecisionRaw(t, db, types.StatusActive)
		val := "changed"
		if col == "scope" {
			val = `["src/**"]`
		}
		if col == "kind" {
			val = "convention"
		}
		_, err := db.Exec(`UPDATE decisions SET `+col+` = ? WHERE id = ?`, val, id)
		if err == nil {
			t.Errorf("editing %s after acceptance should abort", col)
			continue
		}
		if !strings.Contains(err.Error(), "immutable") {
			t.Errorf("editing %s aborted for the wrong reason: %v", col, err)
		}
	}
}

func TestDDL_AdvisoryMetadataStaysEditableAfterAcceptance(t *testing.T) {
	db := freshDB(t)
	id := insertDecisionRaw(t, db, types.StatusActive)
	_, err := db.Exec(
		`UPDATE decisions SET tags = '["x"]', confidence = 0.5, expires_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		t.Fatalf("advisory metadata must stay editable (D3): %v", err)
	}
}

func TestDDL_EventsAreAppendOnly(t *testing.T) {
	db := freshDB(t)
	id := util.GenerateID()
	_, err := db.Exec(`
		INSERT INTO events (id, project_id, ts, kind, actor, payload)
		VALUES (?, 'p1', ?, 'decision.proposed', 'human', '{}')`,
		id, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`UPDATE events SET actor = 'system' WHERE id = ?`, id); err == nil {
		t.Error("events must not be updatable")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("update rejected for the wrong reason: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM events WHERE id = ?`, id); err == nil {
		t.Error("events must not be deletable")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("delete rejected for the wrong reason: %v", err)
	}
}

// D3: a decision with history is never hard-deleted. The FK from events has no
// ON DELETE clause, so the delete fails — provided foreign_keys is actually on.
func TestDDL_DecisionWithHistoryCannotBeHardDeleted(t *testing.T) {
	db := freshDB(t)
	id := insertDecisionRaw(t, db, types.StatusProposed)

	if _, err := db.Exec(`DELETE FROM decisions WHERE id = ?`, id); err != nil {
		t.Fatalf("a decision with no events should be deletable: %v", err)
	}

	id = insertDecisionRaw(t, db, types.StatusProposed)
	if _, err := db.Exec(`
		INSERT INTO events (id, project_id, ts, kind, actor, decision_id, payload)
		VALUES (?, 'p1', ?, 'decision.proposed', 'human', ?, '{}')`,
		util.GenerateID(), time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM decisions WHERE id = ?`, id); err == nil {
		t.Fatal("deleting a decision with events must fail")
	}
}

func TestDDL_EvidenceConstraints(t *testing.T) {
	db := freshDB(t)
	id := insertDecisionRaw(t, db, types.StatusProposed)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	insert := func(kind, ref string, accepting int) error {
		_, err := db.Exec(`
			INSERT INTO evidence (id, decision_id, kind, ref, added_by, accepting, created_at)
			VALUES (?, ?, ?, ?, 'human', ?, ?)`,
			util.GenerateID(), id, kind, ref, accepting, now)
		return err
	}

	if err := insert("commit", "abc123", 1); err != nil {
		t.Fatal(err)
	}
	if err := insert("commit", "abc123", 0); err == nil {
		t.Error("idx_evidence_dedupe must reject a duplicate (decision, kind, ref)")
	}
	if err := insert("nonsense", "x", 0); err == nil {
		t.Error("evidence.kind CHECK must reject an unknown kind")
	}
	if err := insert("pr", "#1", 2); err == nil {
		t.Error("evidence.accepting CHECK must reject values outside {0,1}")
	}

	var accepting int
	if err := db.QueryRow(`
		INSERT INTO evidence (id, decision_id, kind, ref, added_by, created_at)
		VALUES (?, ?, 'pr', '#2', 'human', ?) RETURNING accepting`,
		util.GenerateID(), id, now).Scan(&accepting); err != nil {
		t.Fatal(err)
	}
	if accepting != 0 {
		t.Errorf("evidence.accepting default = %d, want 0", accepting)
	}
}

func TestDDL_DecisionChecks(t *testing.T) {
	db := freshDB(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	insert := func(cols, vals string, args ...any) error {
		_, err := db.Exec(`INSERT INTO decisions (id, project_id, title, created_at,
			updated_at, status_changed_at`+cols+`) VALUES (?, 'p1', 'a title', ?, ?, ?`+vals+`)`,
			append([]any{util.GenerateID(), now, now, now}, args...)...)
		return err
	}

	if err := insert(`, status, decided_at`, `, 'active', NULL`); err == nil {
		t.Error("active without decided_at must be rejected")
	}
	if err := insert(`, status, decided_at`, `, 'violated', NULL`); err == nil {
		t.Error("violated without decided_at must be rejected")
	}
	if err := insert(`, status, superseded_by`, `, 'superseded', NULL`); err == nil {
		t.Error("superseded without superseded_by must be rejected")
	}
	if err := insert(`, status`, `, 'nonsense'`); err == nil {
		t.Error("unknown status must be rejected")
	}
	if err := insert(`, confidence`, `, 1.5`); err == nil {
		t.Error("confidence > 1.0 must be rejected")
	}
	if err := insert(`, scope`, `, 'not json'`); err == nil {
		t.Error("non-JSON scope must be rejected")
	}
	if err := insert(`, source`, `, 'telepathy'`); err == nil {
		t.Error("unknown source must be rejected")
	}

	long := strings.Repeat("x", 201)
	if _, err := db.Exec(`INSERT INTO decisions (id, project_id, title, created_at,
		updated_at, status_changed_at) VALUES (?, 'p1', ?, ?, ?, ?)`,
		util.GenerateID(), long, now, now, now); err == nil {
		t.Error("title longer than 200 chars must be rejected")
	}
}

// One non-terminal holder per topic_key per project — the invariant ADR-0002
// §P6's contradiction-freedom guarantee rests on.
func TestDDL_TopicKeyIsUniqueAmongNonTerminalRows(t *testing.T) {
	db := freshDB(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	insert := func(status types.DecisionStatus, topic string) (string, error) {
		id := util.GenerateID()
		var decidedAt any
		if status == types.StatusActive || status == types.StatusViolated {
			decidedAt = now
		}
		_, err := db.Exec(`INSERT INTO decisions (id, project_id, title, status,
			topic_key, decided_at, created_at, updated_at, status_changed_at)
			VALUES (?, 'p1', 'a title', ?, ?, ?, ?, ?, ?)`,
			id, string(status), topic, decidedAt, now, now, now)
		return id, err
	}

	if _, err := insert(types.StatusActive, "auth"); err != nil {
		t.Fatal(err)
	}
	if _, err := insert(types.StatusProposed, "auth"); err == nil {
		t.Error("two non-terminal rows must not share a topic_key")
	}
	// Terminal rows are exempt: history keeps its keys.
	if _, err := insert(types.StatusRejected, "auth"); err != nil {
		t.Errorf("a terminal row may reuse a live topic_key: %v", err)
	}
	if _, err := insert(types.StatusReverted, "auth"); err != nil {
		t.Errorf("terminal rows must not collide with each other: %v", err)
	}
}

func TestDDL_EventIdempotencyIndexes(t *testing.T) {
	db := freshDB(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	d1 := insertDecisionRaw(t, db, types.StatusActive)
	d2 := insertDecisionRaw(t, db, types.StatusActive)

	emit := func(kind types.EventKind, decisionID, commitSHA any) error {
		_, err := db.Exec(`
			INSERT INTO events (id, project_id, ts, kind, actor, decision_id, commit_sha, payload)
			VALUES (?, 'p1', ?, ?, 'system', ?, ?, '{}')`,
			util.GenerateID(), now, string(kind), decisionID, commitSHA)
		return err
	}

	// decision.expired: once per decision.
	if err := emit(types.EventDecisionExpired, d1, nil); err != nil {
		t.Fatal(err)
	}
	if err := emit(types.EventDecisionExpired, d1, nil); err == nil {
		t.Error("decision.expired must be emitted at most once per decision")
	}
	if err := emit(types.EventDecisionExpired, d2, nil); err != nil {
		t.Errorf("a different decision may expire: %v", err)
	}

	// diff.scope_match: once per (decision, commit).
	if err := emit(types.EventDiffScopeMatch, d1, "sha1"); err != nil {
		t.Fatal(err)
	}
	if err := emit(types.EventDiffScopeMatch, d1, "sha1"); err == nil {
		t.Error("diff.scope_match must be idempotent per (decision, commit)")
	}
	if err := emit(types.EventDiffScopeMatch, d1, "sha2"); err != nil {
		t.Errorf("a different commit may match: %v", err)
	}
	if err := emit(types.EventDiffScopeMatch, d2, "sha1"); err != nil {
		t.Errorf("a different decision may match the same commit: %v", err)
	}

	// diff.observed: once per commit.
	if err := emit(types.EventDiffObserved, nil, "sha1"); err != nil {
		t.Fatal(err)
	}
	if err := emit(types.EventDiffObserved, nil, "sha1"); err == nil {
		t.Error("diff.observed must be emitted at most once per commit")
	}
	if err := emit(types.EventDiffObserved, nil, "sha2"); err != nil {
		t.Errorf("a different commit may be observed: %v", err)
	}
}

func TestDDL_EventActorIsConstrained(t *testing.T) {
	db := freshDB(t)
	_, err := db.Exec(`
		INSERT INTO events (id, project_id, ts, kind, actor, payload)
		VALUES (?, 'p1', ?, 'decision.proposed', 'robot', '{}')`,
		util.GenerateID(), time.Now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		t.Error("events.actor CHECK must reject an actor outside {human,agent,system}")
	}
}

// The DDL deliberately does not CHECK events.kind — new kinds must be addable
// without a migration. The kernel is the only guard (D8, notes on the DDL).
func TestDDL_EventKindIsNotConstrainedInDDL(t *testing.T) {
	db := freshDB(t)
	_, err := db.Exec(`
		INSERT INTO events (id, project_id, ts, kind, actor, payload)
		VALUES (?, 'p1', ?, 'some.future.kind', 'system', '{}')`,
		util.GenerateID(), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Errorf("events.kind must stay open in the DDL: %v", err)
	}
	if types.IsKnownEventKind("some.future.kind") {
		t.Error("the kernel-side catalogue should not know an invented kind")
	}
}

func TestDDL_FTSTablesIndexBothEntities(t *testing.T) {
	db := freshDB(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if _, err := db.Exec(`INSERT INTO decisions (id, project_id, title, body,
		created_at, updated_at, status_changed_at)
		VALUES (?, 'p1', 'always wrap errors', 'use %w', ?, ?, ?)`,
		util.GenerateID(), now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notes (id, project_id, content, summary,
		created_at, updated_at) VALUES (?, 'p1', 'the deploy pipeline uses fly.io', 'deploy', ?, ?)`,
		util.GenerateID(), now, now); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decisions_fts WHERE decisions_fts MATCH 'wrap'`).Scan(&n); err != nil {
		t.Fatalf("decisions_fts: %v", err)
	}
	if n != 1 {
		t.Errorf("decisions_fts matched %d rows, want 1", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM notes_fts WHERE notes_fts MATCH 'deploy'`).Scan(&n); err != nil {
		t.Fatalf("notes_fts: %v", err)
	}
	if n != 1 {
		t.Errorf("notes_fts matched %d rows, want 1", n)
	}
}

func TestDDL_RequiredIndexesExist(t *testing.T) {
	db := freshDB(t)
	want := []string{
		"idx_decisions_project_status", "idx_decisions_expires", "idx_decisions_topic_key",
		"idx_evidence_decision", "idx_evidence_dedupe", "idx_evidence_commit",
		"idx_evidence_accepting_commit",
		"idx_notes_project_status", "idx_notes_topic_key",
		"idx_events_decision", "idx_events_kind_ts", "idx_events_session",
		"idx_events_commit", "idx_events_expired_once", "idx_events_scopematch_once",
		"idx_events_observed_once",
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'index'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		have[n] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("index %q missing", w)
		}
	}
}
