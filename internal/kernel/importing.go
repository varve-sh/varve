package kernel

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// ImportBatchTagPrefix marks every row an import created (ADR-0005 §D2.3).
//
// Tags are the one advisory, editable-post-acceptance field (ADR-0001 §D3), so
// batch membership survives acceptance without touching frozen content — which
// is what makes `import undo` possible on rows a human has since accepted, and
// what makes the batch reconstructible three ways (tag, event payload,
// source_ref prefix).
const ImportBatchTagPrefix = "varve-import:"

// ImportCandidate is one row an importer proposes to create.
//
// It is deliberately not a types.Decision or types.Note: an importer describes
// what a source said, and the kernel decides what that becomes. Nothing here
// can ask for `active`.
type ImportCandidate struct {
	// SourceRef is the idempotency key (§D2.2), written deterministically per
	// source. Re-running an import against unchanged sources creates zero rows
	// because of this field alone.
	SourceRef string
	// AsDecision requests a *proposed* decision rather than a note. Only an
	// explicit signal in the source may set it (§D1) — never a guess, never a
	// model.
	AsDecision bool
	Kind       types.DecisionKind
	Title      string
	Content    string
	Scope      []string
	FilePaths  []string
	Tags       []string
	Summary    string
	// Evidence is attached at birth for decision candidates: `import`-kind for
	// most sources, `commit`-kind for the git miner (§D2.3). It makes
	// proposed→active satisfiable without --force friction, and records where
	// the text came from.
	Evidence *EvidenceInput
}

// ImportBatchResult is what one source's import did.
type ImportBatchResult struct {
	BatchID   string
	Source    string
	Decisions int
	Notes     int
	Skipped   []string // source_refs already present
	Errors    []string
	DryRun    bool
}

// ImportBatch writes one source's candidates as a single, reversible batch
// (ADR-0005 §D2).
//
// Three properties are load-bearing and each is tested:
//
//   - **Nothing lands `active`.** Decision candidates go through `Propose`, so
//     they are `proposed` with a `decision.proposed` birth event in the
//     creating transaction. An import of 400 notes must not become 400 binding
//     decisions.
//   - **Invariant I1 holds for imported rows** (ADR-0001 §D7/Amendment 4):
//     they have birth events, so they are *not* migration-born, and they are
//     not excluded from falsifier 1's population. §D2.3 makes that a decision
//     rather than an accident: an untriaged import backlog is genuine
//     review-queue debt, because the user ran the import and the templates say
//     proposals need review.
//   - **Timestamps are import time**, never carried from old-looking source
//     material. The same interaction fired twice before (falsifier 1 against
//     migrated rows, falsifier 4 against migrated scopes), and both times the
//     defect was a timestamp that described the source rather than the event.
func (k *MemoryKernel) ImportBatch(source string, candidates []ImportCandidate, dryRun bool) (*ImportBatchResult, error) {
	return k.ImportBatchInto("", source, candidates, dryRun)
}

// ImportBatchInto is ImportBatch with a caller-supplied batch id, so one
// `varve import` run over several sources is one undoable batch (§D2.3) while
// still emitting one `import.completed` per source (§D6).
func (k *MemoryKernel) ImportBatchInto(batchID, source string, candidates []ImportCandidate, dryRun bool) (*ImportBatchResult, error) {
	if batchID == "" {
		batchID = util.GenerateID()
	}
	res := &ImportBatchResult{BatchID: batchID, Source: source, DryRun: dryRun}
	batchTag := ImportBatchTagPrefix + res.BatchID

	for _, c := range candidates {
		if strings.TrimSpace(c.Content) == "" && strings.TrimSpace(c.Title) == "" {
			res.Errors = append(res.Errors, "skipped an entry with no content")
			continue
		}
		taken, err := k.sourceRefTaken(c.SourceRef)
		if err != nil {
			return nil, err
		}
		if taken {
			res.Skipped = append(res.Skipped, c.SourceRef)
			continue
		}
		if dryRun {
			if c.AsDecision {
				res.Decisions++
			} else {
				res.Notes++
			}
			continue
		}

		if c.AsDecision {
			if err := k.importDecision(c, batchTag); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", c.SourceRef, err))
				continue
			}
			res.Decisions++
			continue
		}
		if err := k.importNote(c, batchTag); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", c.SourceRef, err))
			continue
		}
		res.Notes++
	}

	if dryRun {
		return res, nil
	}
	if err := k.recordImportCompleted(res); err != nil {
		return nil, err
	}
	return res, nil
}

func (k *MemoryKernel) importDecision(c ImportCandidate, batchTag string) error {
	title := strings.TrimSpace(c.Title)
	if title == "" {
		title = truncate(firstLineOf(c.Content), 120)
	}
	if r := []rune(title); len(r) > 200 {
		title = string(r[:200])
	}
	ev := c.Evidence
	if ev == nil {
		// §D2.3: one evidence row at birth, so proposed→active needs no
		// --force and the audit trail records where the text came from.
		ev = &EvidenceInput{
			Kind: types.EvidenceKindImport, Ref: c.SourceRef, AddedBy: types.ActorSystem,
		}
	}
	kind := c.Kind
	if kind == "" {
		kind = types.DecisionKindDecision
	}
	_, err := k.decisions.Propose(DecisionInput{
		ProjectID: k.projectID,
		Kind:      kind,
		Title:     title,
		Body:      c.Content,
		Scope:     c.Scope,
		Source:    types.DecisionSourceImport,
		SourceRef: c.SourceRef,
		Tags:      append(append([]string{}, c.Tags...), batchTag),
		Via:       "import",
		Evidence:  []EvidenceInput{*ev},
	})
	return err
}

func (k *MemoryKernel) importNote(c ImportCandidate, batchTag string) error {
	summary := c.Summary
	if summary == "" {
		summary = truncate(firstLineOf(c.Content), 120)
	}
	_, err := k.notes.Insert(NoteInput{
		ProjectID: k.projectID,
		Content:   c.Content,
		Summary:   summary,
		Source:    types.MemorySourceImport,
		SourceRef: c.SourceRef,
		FilePaths: c.FilePaths,
		Tags:      append(append([]string{}, c.Tags...), batchTag),
		Status:    types.MemoryStatusActive,
	})
	return err
}

// sourceRefTaken is §D2.2's skip rule: a candidate is skipped iff a row with
// the same (project, source_ref) exists in **any** status — except rows whose
// terminal status came from `import undo`, which are eligible for re-creation.
//
// The exception is what makes import → undo → import work. An individually
// rejected row (a human said no to *that* one) is not resurrected, because its
// rejection carries no undo reason.
func (k *MemoryKernel) sourceRefTaken(sourceRef string) (bool, error) {
	if sourceRef == "" {
		return false, nil
	}
	var n int
	if err := k.db.QueryRow(`
		SELECT COUNT(*) FROM notes WHERE project_id = ? AND source_ref = ?`,
		k.projectID, sourceRef).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	// Decisions: present unless every row with this ref was undone.
	rows, err := k.db.Query(`
		SELECT id, status FROM decisions WHERE project_id = ? AND source_ref = ?`,
		k.projectID, sourceRef)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return false, err
		}
		found = true
		undone, err := k.rejectedByUndo(id)
		if err != nil {
			return false, err
		}
		if !undone {
			return true, nil // a live row, or a human's own rejection: skip
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	_ = found
	return false, nil
}

func (k *MemoryKernel) rejectedByUndo(decisionID string) (bool, error) {
	var n int
	err := k.db.QueryRow(`
		SELECT COUNT(*) FROM events
		 WHERE kind = 'decision.rejected' AND decision_id = ?
		   AND json_extract(payload, '$.reason') = 'import_undo'`, decisionID).Scan(&n)
	return n > 0, err
}

func (k *MemoryKernel) recordImportCompleted(res *ImportBatchResult) error {
	k.governanceStamp()
	return k.decisions.withTx(func(tx *sql.Tx) error {
		_, err := k.decisions.emit(tx, EventInput{
			ProjectID: k.projectID,
			Kind:      types.EventImportCompleted,
			Actor:     types.ActorHuman,
			Payload: map[string]any{
				"source":    res.Source,
				"batch":     res.BatchID,
				"decisions": res.Decisions,
				"notes":     res.Notes,
				"skipped":   len(res.Skipped),
				"errors":    len(res.Errors),
				"dry_run":   res.DryRun,
			},
		})
		return err
	})
}

// ImportUndoResult reports what an undo did (§D2.4).
type ImportUndoResult struct {
	BatchID           string
	NotesDeleted      int
	DecisionsRejected int
	// LeftUntouched names rows a human already acted on. Undo never destroys a
	// human's work — it reverses the import's influence, not their decisions.
	LeftUntouched []string
}

// UndoImport reverses a batch's influence without violating append-only
// (ADR-0005 §D2.4).
//
// Notes are hard-deleted (they carry no event FK). Decision candidates still
// `proposed` and untouched by a human are transitioned to `rejected` with
// `{"reason": "import_undo"}` — hard delete is impossible by design, since each
// has a `decision.proposed` event and the FK blocks it. "Reversible" therefore
// means *no longer influences anything*: rejected rows are invisible to the
// packer, to recall defaults and to attribution, with the audit record intact.
func (k *MemoryKernel) UndoImport(batchID string) (*ImportUndoResult, error) {
	if batchID == "" {
		latest, err := k.LatestImportBatch()
		if err != nil {
			return nil, err
		}
		if latest == "" {
			return nil, fmt.Errorf("no import batches to undo")
		}
		batchID = latest
	}
	res := &ImportUndoResult{BatchID: batchID}
	tag := ImportBatchTagPrefix + batchID

	notes, err := k.notes.List(NoteFilter{ProjectID: k.projectID})
	if err != nil {
		return nil, err
	}
	for _, n := range notes {
		if !hasTag(n.Tags, tag) {
			continue
		}
		if _, err := k.store.DeleteByID(n.ID); err != nil {
			return nil, err
		}
		res.NotesDeleted++
	}

	decisions, err := k.decisions.ListDecisions(DecisionFilter{ProjectID: k.projectID})
	if err != nil {
		return nil, err
	}
	k.governanceStamp()
	for i := range decisions {
		d := &decisions[i]
		if !hasTag(d.Tags, tag) {
			continue
		}
		if d.Status != types.StatusProposed {
			// Accepted, rejected or otherwise moved on by a human.
			res.LeftUntouched = append(res.LeftUntouched, d.ID)
			continue
		}
		touched, err := k.humanTouched(d.ID)
		if err != nil {
			return nil, err
		}
		if touched {
			res.LeftUntouched = append(res.LeftUntouched, d.ID)
			continue
		}
		if err := k.decisions.rejectForImportUndo(d.ID, batchID); err != nil {
			return nil, err
		}
		res.DecisionsRejected++
	}

	if err := k.recordImportUndone(res); err != nil {
		return nil, err
	}
	return res, nil
}

// humanTouched reports whether anything but the import itself has happened to
// this decision — an edit, an evidence attachment, a disposal request.
func (k *MemoryKernel) humanTouched(decisionID string) (bool, error) {
	var n int
	err := k.db.QueryRow(`
		SELECT COUNT(*) FROM events
		 WHERE decision_id = ?
		   AND kind NOT IN ('decision.proposed')`, decisionID).Scan(&n)
	return n > 0, err
}

func (s *DecisionStore) rejectForImportUndo(id, batchID string) error {
	return s.transition(id, types.StatusRejected, types.EventDecisionRejected,
		types.ActorHuman, func(d *types.Decision) map[string]any {
			return map[string]any{"reason": "import_undo", "batch": batchID}
		}, "")
}

func (k *MemoryKernel) recordImportUndone(res *ImportUndoResult) error {
	return k.decisions.withTx(func(tx *sql.Tx) error {
		_, err := k.decisions.emit(tx, EventInput{
			ProjectID: k.projectID,
			Kind:      types.EventImportUndone,
			Actor:     types.ActorHuman,
			Payload: map[string]any{
				"batch":              res.BatchID,
				"notes_deleted":      res.NotesDeleted,
				"decisions_rejected": res.DecisionsRejected,
				"left_untouched":     nonNilStrings(res.LeftUntouched),
			},
		})
		return err
	})
}

// LatestImportBatch returns the most recent batch that actually created rows,
// or "" when there are none.
//
// The "created rows" filter is what makes `import undo` with no argument mean
// what a user means by it. A re-run against unchanged sources is idempotent —
// it creates nothing and emits an `import.completed` with all-skipped counts —
// so without this filter the sequence the ADR advertises for a stranger
// (import, read the report, undo) would undo an empty batch and leave the rows
// in place. An empty batch has nothing to undo; the next one back does.
func (k *MemoryKernel) LatestImportBatch() (string, error) {
	var batch string
	err := k.db.QueryRow(`
		SELECT json_extract(payload, '$.batch') FROM events
		 WHERE kind = 'import.completed'
		   AND (json_extract(payload, '$.decisions') > 0
		     OR json_extract(payload, '$.notes') > 0)
		 ORDER BY seq DESC LIMIT 1`).Scan(&batch)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return batch, err
}

// RecordLintCompleted writes the linter's own event (§D6). It gives the funnel
// its own measurement for free — score distribution and re-run frequency are a
// query over these rows, all of it local to the user's database.
func (k *MemoryKernel) RecordLintCompleted(payload map[string]any) error {
	k.governanceStamp()
	return k.decisions.withTx(func(tx *sql.Tx) error {
		_, err := k.decisions.emit(tx, EventInput{
			ProjectID: k.projectID,
			Kind:      types.EventLintCompleted,
			Actor:     types.ActorHuman,
			Payload:   payload,
		})
		return err
	})
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func firstLineOf(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

var _ = time.Now
