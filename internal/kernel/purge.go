package kernel

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/varve-sh/varve/internal/types"
)

// PurgeArm records which of Amendment 4's two behaviours ran. The caller has
// to be able to tell them apart: one destroys a row, the other keeps it as a
// tombstone, and reporting either as the other is the class of lie F31 and F28
// were.
type PurgeArm string

const (
	// PurgeDeleted: the row had no events, so under invariant I1 it was
	// migration-born and never touched since. It is gone; a tombstone event
	// records that it was.
	PurgeDeleted PurgeArm = "deleted"
	// PurgeRedacted: the row had history. Its content is cleared in place and
	// the row, its id and its events survive.
	PurgeRedacted PurgeArm = "redacted"
)

// PurgeResult describes what a purge did, for the caller's message.
type PurgeResult struct {
	Arm PurgeArm
	// Transitioned names the terminal state a non-terminal row was moved to
	// before redaction, or "" if it was already terminal.
	Transitioned types.DecisionStatus
	EvidenceRows int
}

// purgedTitle is the tombstone title. Migration 4's trigger exemption matches
// on this exact string, so the two must never drift apart.
const purgedTitle = "[purged]"

// Purge irreversibly removes a decision's content (ADR-0001 Amendment 4).
//
// It is the only destructive verb in the product, and it is deliberately not
// reachable from `forget`, `rm` or MCP: those carry a contract of "transition
// and keep", and the whole of Amendment 4 is that an irreversible action must
// live behind a name that says so. The kernel refuses a non-human actor rather
// than trusting the caller to have checked.
//
// Two arms, by whether the row has history:
//
//   - **No events** — under I1 exactly "migration-born and untouched since".
//     The row is deleted (evidence cascades) and a `decision.purged` tombstone
//     is written with `decision_id` NULL, because the row it would reference no
//     longer exists. Nothing vanishes silently; no per-row history is invented.
//   - **Has events** — the secret-in-a-body case. The row is *redacted*, never
//     deleted: its events are append-only and its id is load-bearing in
//     attribution joins, so deleting it is barred by the FK and would be wrong
//     regardless. A non-terminal row transitions through the legal matrix first
//     (rejected if proposed, reverted otherwise), then title/body/scope/tags/
//     embedding are cleared. §D7's payloads carry counts, ids and field *names*
//     — never decision content — so clearing the snapshot removes every copy
//     the store controls.
//
// What it cannot reach is named by the caller, not hidden: the v1 backup and
// the migration export (§D9 keeps both indefinitely), and event payloads that
// record query text (open question 10).
func (k *MemoryKernel) Purge(id, reason string, actor types.Actor) (*PurgeResult, error) {
	if actor != types.ActorHuman {
		return nil, fmt.Errorf("%w (decision %s)", types.ErrPurgeNotPermitted, id)
	}
	if reason == "" {
		reason = "secret"
	}

	d, err := k.decisions.GetDecision(id)
	if err != nil {
		return nil, err
	}
	migrationBorn, err := k.decisions.MigrationBorn(id)
	if err != nil {
		return nil, err
	}
	events, err := k.decisions.Events(EventFilter{DecisionID: id, Limit: 1})
	if err != nil {
		return nil, err
	}

	k.governanceStamp()

	if len(events) == 0 {
		// Belt and braces: the hard-delete arm keys on "no events", and I1 says
		// that equals migration-born. If the two ever disagree, the predicate
		// has rotted and destroying a row on it is exactly the wrong response.
		if !migrationBorn {
			return nil, fmt.Errorf(
				"refusing to purge %s: it has no events but is not migration-born, "+
					"so invariant I1 does not hold and the hard-delete arm's premise is unsafe", id)
		}
		res, err := k.decisions.purgeDeleteTx(d, reason)
		if err != nil {
			return nil, err
		}
		k.vacuum()
		return res, nil
	}

	res, err := k.decisions.purgeRedactTx(d, reason)
	if err != nil {
		return nil, err
	}
	k.vacuum()
	return res, nil
}

// vacuum rewrites the database file after a purge, so the freed pages holding
// the purged content are not simply marked reusable. Best-effort: a purge that
// committed is not un-done by a failed VACUUM, and the caller is told which
// copies remain either way.
func (k *MemoryKernel) vacuum() {
	_, _ = k.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	_, _ = k.db.Exec(`VACUUM`)
}

func (s *DecisionStore) purgeDeleteTx(d *types.Decision, reason string) (*PurgeResult, error) {
	res := &PurgeResult{Arm: PurgeDeleted}
	err := s.withTx(func(tx *sql.Tx) error {
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM evidence WHERE decision_id = ?`, d.ID).
			Scan(&res.EvidenceRows); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM decisions WHERE id = ?`, d.ID); err != nil {
			return fmt.Errorf("purging decision %s: %w", d.ID, err)
		}
		// decision_id is deliberately NULL: the row is gone and the FK cannot
		// reference it. The id rides in the payload instead (§D7).
		_, err := s.emit(tx, EventInput{
			ProjectID: d.ProjectID,
			Kind:      types.EventDecisionPurged,
			Actor:     types.ActorHuman,
			Payload: map[string]any{
				"reason":        reason,
				"purged_id":     d.ID,
				"hard_deleted":  true,
				"evidence_rows": res.EvidenceRows,
			},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *DecisionStore) purgeRedactTx(d *types.Decision, reason string) (*PurgeResult, error) {
	res := &PurgeResult{Arm: PurgeRedacted}
	err := s.withTx(func(tx *sql.Tx) error {
		// A live decision is taken out of service through the legal matrix
		// first, with its ordinary event: a redacted row that was still
		// `active` would be a binding rule whose text is '[purged]'.
		if !d.Status.IsTerminal() {
			to := types.StatusReverted
			kind := types.EventDecisionReverted
			payload := map[string]any{"via": "human"}
			if d.Status == types.StatusProposed {
				to, kind, payload = types.StatusRejected, types.EventDecisionRejected,
					map[string]any{"reason": "purged"}
			}
			if err := s.applyTransitionTx(tx, d, to, kind, types.ActorHuman, payload, ""); err != nil {
				return err
			}
			res.Transitioned = to
		}

		// The one shape migration 4's trigger exemption licenses.
		if _, err := tx.Exec(`
			UPDATE decisions
			   SET title = ?, body = '', scope = '[]', tags = '[]',
			       embedding = NULL, updated_at = ?
			 WHERE id = ?`,
			purgedTitle, fmtTime(time.Now().UTC()), d.ID); err != nil {
			return fmt.Errorf("redacting decision %s: %w", d.ID, err)
		}

		_, err := s.emit(tx, EventInput{
			ProjectID:  d.ProjectID,
			Kind:       types.EventDecisionPurged,
			Actor:      types.ActorHuman,
			DecisionID: d.ID,
			Payload: map[string]any{
				"reason": reason,
				// Field names, never their content — the event log must not
				// become the copy the purge missed.
				"redacted_fields": []string{"title", "body", "scope", "tags", "embedding"},
			},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// PurgeResidue names the copies a purge cannot reach. Printed on every purge:
// a purge that silently left four copies while claiming removal would be the
// dishonest version of this feature.
func PurgeResidue(projectRoot string) []string {
	return []string{
		projectRoot + "/.varve/varve.v1.bak.db (the v1 backup, kept indefinitely by design)",
		projectRoot + "/.varve/migration-v1-export.json (the migration export)",
		"any copy outside this store — git history, chat logs, the agent's own context",
	}
}
