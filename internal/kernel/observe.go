package kernel

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/memtrace-dev/memtrace/internal/scope"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// ObservedCommit is one commit as the diff observer saw it (ADR-0004 §D1.4).
//
// Deliberately free of git: everything here comes out of `git show` and
// `git patch-id` in internal/observer, and the kernel half — which decisions
// this commit touched, what the verdict is, what the verdict does to the
// lifecycle — is testable without a repository.
type ObservedCommit struct {
	SHA         string
	Author      string
	Subject     string
	CommittedAt time.Time
	Files       []string
	// PatchID is `git patch-id --stable`. LOAD-BEARING (§D7): reports count
	// distinct changes by patch_id, not by SHA — without it one rebase of a
	// feature branch doubles every conform count.
	PatchID string
	// Branch is advisory context only. Branch membership is not a stable fact
	// about a commit (branches are deleted, commits are rebased), so §D7 says
	// never to join on it.
	Branch string
	// RevertsSHA is the target of a `This reverts commit <sha>` trailer, or "".
	// Trailer-only detection is §D2's 90-day rule.
	RevertsSHA string
	// Backfill marks an explicit pre-epoch observation (§D1.3).
	Backfill bool
}

// ObservationResult reports what one observation did, for the scan's summary
// and for `varve observe`'s output.
type ObservationResult struct {
	// AlreadyObserved is true when the commit had a diff.observed row already;
	// the whole observation is then a no-op. The scan's cursor is exactly this.
	AlreadyObserved bool
	Matched         int // diff.scope_match rows written
	Conformed       int
	Violated        int
	RevertDetected  bool
	// DecisionsReverted and DecisionsReinstated name §D6's state effects.
	DecisionsReverted   []string
	DecisionsReinstated []string
}

// ObserveCommit records one commit and applies ADR-0001 §D6's verdict rule,
// in a single transaction (ADR-0004 §D1.4).
//
// One transaction is the point: the scope-match rows, the episode events and
// the lifecycle transitions commit together with the `diff.observed` row that
// justifies them, or not at all. A partially observed commit would leave a
// verdict with no observation behind it — a number in the report that drills
// down to nothing, which is the one thing §D6's honesty controls cannot
// survive.
//
// Rescans are no-ops: `idx_events_observed_once` makes the first insert the
// cursor, and `idx_events_scopematch_once` freezes each (decision, commit)
// verdict at first observation (ADR-0001 Amendment 1, audit item 2).
func (k *MemoryKernel) ObserveCommit(c ObservedCommit) (*ObservationResult, error) {
	if c.SHA == "" {
		return nil, &types.ValidationError{Field: "sha", Message: "must not be empty"}
	}
	res := &ObservationResult{}
	err := k.decisions.withTx(func(tx *sql.Tx) error {
		return k.observeCommitTx(tx, c, res)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (k *MemoryKernel) observeCommitTx(tx *sql.Tx, c ObservedCommit, res *ObservationResult) error {
	observedPayload := map[string]any{
		"files":        nonNilStrings(c.Files),
		"author":       c.Author,
		"subject":      c.Subject,
		"committed_at": c.CommittedAt.UTC().Format(time.RFC3339),
		"branch":       c.Branch,
		"patch_id":     c.PatchID,
	}
	_, inserted, err := appendEventOnce(tx, EventInput{
		ProjectID: k.projectID,
		Kind:      types.EventDiffObserved,
		Actor:     types.ActorSystem,
		CommitSHA: c.SHA,
		Payload:   observedPayload,
	})
	if err != nil {
		return err
	}
	if !inserted {
		// The cursor is the existence of this row (§D1.2). Everything below
		// already happened for this commit.
		res.AlreadyObserved = true
		return nil
	}

	// §D2: trailer-only revert detection. `method` distinguishes future
	// detectors forever after — patch_inverse_exact is the first acceptable
	// upgrade, and it is out of the 90 days.
	if c.RevertsSHA != "" {
		res.RevertDetected = true
		var existing int
		if err := tx.QueryRow(`
			SELECT COUNT(*) FROM events
			 WHERE kind = 'revert.detected' AND commit_sha = ?
			   AND json_extract(payload, '$.reverts_sha') = ?`,
			c.SHA, c.RevertsSHA).Scan(&existing); err != nil {
			return err
		}
		if existing == 0 {
			if _, err := k.decisions.emit(tx, EventInput{
				ProjectID: k.projectID,
				Kind:      types.EventRevertDetected,
				Actor:     types.ActorSystem,
				CommitSHA: c.SHA,
				Payload: map[string]any{
					"reverts_sha": c.RevertsSHA,
					"method":      "trailer",
				},
			}); err != nil {
				return err
			}
		}
	}

	// §D1.3: a scope match is only evaluated against decisions that are
	// binding *at observation time* and were decided before the commit was
	// made — a decision cannot be conformed to or violated by a commit that
	// predates its acceptance.
	eligible, err := k.eligibleForMatchTx(tx, c.CommittedAt)
	if err != nil {
		return err
	}

	for i := range eligible {
		d := &eligible[i]
		matchedGlobs := scope.MatchedGlobs(d.Scope, c.Files)
		if len(matchedGlobs) == 0 {
			continue
		}
		matchedFiles := scope.MatchedFiles(d.Scope, c.Files)

		violate, err := k.isViolationTx(tx, d.ID, c.RevertsSHA)
		if err != nil {
			return err
		}
		res.Matched++
		if violate {
			recorded, err := k.decisions.markViolatedTx(tx, d, ViolationOptions{
				CommitSHA:    c.SHA,
				RevertedSHA:  c.RevertsSHA,
				Files:        matchedFiles,
				MatchedGlobs: matchedGlobs,
				Backfill:     c.Backfill,
			})
			if err != nil {
				return err
			}
			if recorded {
				res.Violated++
			}
			continue
		}
		if err := k.recordConformTx(tx, d, c, matchedFiles, matchedGlobs); err != nil {
			return err
		}
		res.Conformed++
	}

	// §D6's revert rule, narrowed by the founder-delegated item-6 ruling: a
	// decision goes terminal only when its *accepting* evidence is reverted.
	// Reverting a later-attached (conforming) commit produces a violation
	// above, never a revert here — the more a decision was followed, the more
	// commits would otherwise exist whose revert could kill it.
	if c.RevertsSHA != "" {
		if err := k.applyRevertEffectsTx(tx, c, res); err != nil {
			return err
		}
	}
	return nil
}

// eligibleForMatchTx returns the decisions a commit may be judged against.
func (k *MemoryKernel) eligibleForMatchTx(tx *sql.Tx, committedAt time.Time) ([]types.Decision, error) {
	rows, err := tx.Query(`SELECT `+decisionColumns+`
		  FROM decisions
		 WHERE project_id = ? AND status IN ('active','violated')
		   AND decided_at IS NOT NULL AND decided_at <= ?
		 ORDER BY id`, k.projectID, committedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// isViolationTx is §D6's verdict rule, verbatim: violate iff this commit
// reverts a commit that is evidence of D, or a commit that previously
// conformed to D. Conform otherwise.
//
// "Conform" therefore means *no deterministic violation signal was found* —
// not verified compliance. That is the trade ruling 1 bought: every positive
// is defensible with `git show`, at the cost of recall on negatives, and the
// report says so on its face.
func (k *MemoryKernel) isViolationTx(tx *sql.Tx, decisionID, revertsSHA string) (bool, error) {
	if revertsSHA == "" {
		return false, nil
	}
	var n int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM evidence
		 WHERE decision_id = ? AND kind = 'commit' AND ref = ?`,
		decisionID, revertsSHA).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM events
		 WHERE kind = 'diff.scope_match' AND decision_id = ? AND commit_sha = ?
		   AND json_extract(payload, '$.verdict') = 'conform'`,
		decisionID, revertsSHA).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (k *MemoryKernel) recordConformTx(
	tx *sql.Tx, d *types.Decision, c ObservedCommit, files, globs []string,
) error {
	payload := map[string]any{
		"files":         nonNilStrings(files),
		"matched_globs": nonNilStrings(globs),
		"verdict":       "conform",
	}
	if c.Backfill {
		payload["backfill"] = true
	}
	_, _, err := appendEventOnce(tx, EventInput{
		ProjectID:  k.projectID,
		Kind:       types.EventDiffScopeMatch,
		Actor:      types.ActorSystem,
		DecisionID: d.ID,
		CommitSHA:  c.SHA,
		Payload:    payload,
	})
	return err
}

// applyRevertEffectsTx runs §D6's two revert consequences: a decision whose
// accepting evidence was reverted goes terminal, and a violation episode whose
// violating commit was reverted is resolved — with reinstatement only at the
// zero-crossing (ADR-0001 Amendment 2).
func (k *MemoryKernel) applyRevertEffectsTx(tx *sql.Tx, c ObservedCommit, res *ObservationResult) error {
	accepting, err := k.acceptingEvidenceHoldersTx(tx, c.RevertsSHA)
	if err != nil {
		return err
	}
	for _, id := range accepting {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		if d.Status.IsTerminal() {
			continue
		}
		if err := k.decisions.applyTransitionTx(tx, d, types.StatusReverted,
			types.EventDecisionReverted, types.ActorSystem,
			map[string]any{"via": "revert_detected", "reverted_evidence_ref": c.RevertsSHA},
			c.SHA); err != nil {
			return err
		}
		res.DecisionsReverted = append(res.DecisionsReverted, id)
	}

	// Episodes on the reverted commit are now resolved; every decision that
	// held one crosses zero or does not, individually (F21's lesson: the
	// resolution is global by sha, so the crossing must be evaluated for every
	// decision the revert touched, not only one).
	rows, err := tx.Query(`
		SELECT DISTINCT decision_id FROM events
		 WHERE kind = 'decision.violated' AND commit_sha = ? AND decision_id IS NOT NULL`,
		c.RevertsSHA)
	if err != nil {
		return err
	}
	var touched []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		touched = append(touched, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range touched {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		if d.Status != types.StatusViolated {
			continue
		}
		unresolved, err := unresolvedViolationsTx(tx, id)
		if err != nil {
			return err
		}
		if unresolved > 0 {
			continue
		}
		if err := k.decisions.applyTransitionTx(tx, d, types.StatusActive,
			types.EventDecisionReinstated, types.ActorSystem,
			map[string]any{"via": "counter_revert"}, c.SHA); err != nil {
			return err
		}
		res.DecisionsReinstated = append(res.DecisionsReinstated, id)
	}
	return nil
}

func (k *MemoryKernel) acceptingEvidenceHoldersTx(tx *sql.Tx, sha string) ([]string, error) {
	rows, err := tx.Query(`
		SELECT DISTINCT e.decision_id FROM evidence e
		  JOIN decisions d ON d.id = e.decision_id
		 WHERE e.ref = ? AND e.kind = 'commit' AND e.accepting = 1
		   AND d.project_id = ?`, sha, k.projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- the observation epoch (§D1.3) ---

// RecordObserverEnabled writes the observation epoch, once, at `varve init`.
//
// The epoch is what stops the catch-up scan from silently backfilling history
// that predates the store: verdicts about commits older than the decisions
// themselves are archaeology, and mixing them into the headline numbers is the
// first thing an auditor catches.
func (k *MemoryKernel) RecordObserverEnabled(epoch time.Time) error {
	existing, err := k.ObserverEpoch()
	if err != nil {
		return err
	}
	if existing != nil {
		return nil // already enabled; the epoch is set once and never moves
	}
	return k.decisions.withTx(func(tx *sql.Tx) error {
		_, err := k.decisions.emit(tx, EventInput{
			ProjectID: k.projectID,
			Kind:      types.EventObserverEnabled,
			Actor:     types.ActorSystem,
			Payload:   map[string]any{"epoch": epoch.UTC().Format(time.RFC3339)},
		})
		return err
	})
}

// ObserverEpoch returns the observation epoch, or nil if the observer was
// never enabled (a store created before this shipped).
func (k *MemoryKernel) ObserverEpoch() (*time.Time, error) {
	var raw string
	err := k.db.QueryRow(`
		SELECT json_extract(payload, '$.epoch') FROM events
		 WHERE kind = 'observer.enabled' ORDER BY seq LIMIT 1`).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading observation epoch: %w", err)
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, nil
	}
	return &t, nil
}

// IsObserved reports whether a commit already has its diff.observed row. This
// is the scan's cursor — there is no cursor table, by design (§D1.2).
func (k *MemoryKernel) IsObserved(sha string) (bool, error) {
	var n int
	if err := k.db.QueryRow(
		`SELECT COUNT(*) FROM events WHERE kind = 'diff.observed' AND commit_sha = ?`,
		sha).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
