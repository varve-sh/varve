package kernel

import (
	"database/sql"
	"errors"
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

// ImportIdentityTagPrefix carries a candidate's *rule identity* — an identity
// that survives editing its text (F48).
//
// §D2.2's idempotency key is the content hash, which is right for "have I
// imported this text before" and wrong for "did the human already say no to
// this rule": editing one character produces a new source_ref, and §D2.4's
// promise that an individually-rejected row is not resurrected would break on a
// typo fix. The identity is coarser than the content hash and finer than the
// file: for rules files it is the block's heading, so edits to a block's body
// keep the rejection. Sources with stable row ids (claude-mem, engram) reuse
// their source_ref, since editing upstream text there keeps the same row.
//
// The residual hole is disclosed rather than hidden: renaming a rejected rule's
// *heading* does offer it again, and the import report says so.
const ImportIdentityTagPrefix = "varve-rule:"

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
	// IdentityRef is the rule identity used for the never-resurrect rule
	// (F48). Empty means "use SourceRef".
	IdentityRef string
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
		taken, err := k.candidateTaken(c)
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
		Tags:      append(append([]string{}, c.Tags...), batchTag, identityTag(c)),
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

// candidateTaken is the full skip test: §D2.2's source_ref rule, plus the
// never-resurrect rule keyed on rule identity (F48). Either one skips.
func (k *MemoryKernel) candidateTaken(c ImportCandidate) (bool, error) {
	taken, err := k.sourceRefTaken(c.SourceRef)
	if err != nil || taken {
		return taken, err
	}
	if !c.AsDecision {
		return false, nil
	}
	return k.rejectedByHumanIdentity(identityTag(c))
}

func identityTag(c ImportCandidate) string {
	id := c.IdentityRef
	if id == "" {
		id = c.SourceRef
	}
	return ImportIdentityTagPrefix + id
}

// rejectedByHumanIdentity reports whether a human has already rejected a rule
// with this identity. Undo-produced rejections do not count — those are the
// import's own doing, and §D2.2's exception exists so re-import after undo
// works.
func (k *MemoryKernel) rejectedByHumanIdentity(tag string) (bool, error) {
	rows, err := k.db.Query(`
		SELECT id FROM decisions
		 WHERE project_id = ? AND status = 'rejected'
		   AND EXISTS (SELECT 1 FROM json_each(tags) t WHERE t.value = ?)`,
		k.projectID, tag)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return false, err
		}
		undone, err := k.rejectedByUndo(id)
		if err != nil {
			return false, err
		}
		if !undone {
			return true, nil
		}
	}
	return false, rows.Err()
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
	// RedirectedFrom is set when a default (no-argument) undo skipped a batch
	// that created nothing. ADR-0005 Amendment 1: the skip is legal but must be
	// announced — a command that silently acts on a different batch than the
	// one it just named is the kind of helpfulness that costs trust.
	RedirectedFrom string
}

// ErrUnknownImportBatch is returned for a batch id with no `import.completed`
// event. Refusing an unrecognised id is half of F45's fix: an undo that
// accepts an arbitrary string will eventually be handed one.
var ErrUnknownImportBatch = errors.New("no import batch with that id")

// UndoImport reverses a batch's influence without violating append-only
// (ADR-0005 §D2.4).
//
// Notes are hard-deleted (they carry no event FK). Decision candidates still
// `proposed` and untouched by a human are transitioned to `rejected` with
// `{"reason": "import_undo"}` — hard delete is impossible by design, since each
// has a `decision.proposed` event and the FK blocks it. "Reversible" therefore
// means *no longer influences anything*: rejected rows are invisible to the
// packer, to recall defaults and to attribution, with the audit record intact.
//
// Membership in a batch is decided from **import provenance**, never from the
// batch tag alone (F45). The tag is user-writable, `varve export` preserves
// it, and the documented export→import round trip therefore carries foreign
// batch tags into other stores — so a tag-only membership test can be handed
// rows the batch never created. A note is deleted only if it is tagged AND was
// created by an import AND has not been edited since; anything else is spared
// and listed. Notes carry no event log, so a wrong deletion here is
// unrecoverable, which is why the note path gets the strictest test rather than
// the loosest.
func (k *MemoryKernel) UndoImport(batchID string) (*ImportUndoResult, error) {
	res := &ImportUndoResult{}
	if batchID == "" {
		latest, skipped, err := k.latestImportBatch()
		if err != nil {
			return nil, err
		}
		if latest == "" {
			return nil, fmt.Errorf("no import batches to undo")
		}
		batchID, res.RedirectedFrom = latest, skipped
	} else {
		// An explicitly named batch is always the target, even when it created
		// nothing (Amendment 1: a stated no-op, never a redirect).
		known, err := k.importBatchExists(batchID)
		if err != nil {
			return nil, err
		}
		if !known {
			return nil, fmt.Errorf("%w: %s", ErrUnknownImportBatch, batchID)
		}
	}
	res.BatchID = batchID
	tag := ImportBatchTagPrefix + batchID

	notes, err := k.notes.List(NoteFilter{ProjectID: k.projectID})
	if err != nil {
		return nil, err
	}
	for _, n := range notes {
		if !hasTag(n.Tags, tag) {
			continue
		}
		if n.Source != types.MemorySourceImport {
			// Tagged, but not created by an import: someone else's row wearing
			// this batch's label.
			res.LeftUntouched = append(res.LeftUntouched, n.ID)
			continue
		}
		if n.UpdatedAt.After(n.CreatedAt) {
			// The human-action guard the decision path already had. "Undo never
			// destroys a human's work" has to hold for the class that is hard
			// deleted, or it does not hold.
			res.LeftUntouched = append(res.LeftUntouched, n.ID)
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
func (k *MemoryKernel) LatestImportBatch() (string, error) {
	batch, _, err := k.latestImportBatch()
	return batch, err
}

// latestImportBatch also reports the batch it skipped, if any.
//
// The "created rows" filter is what makes `import undo` with no argument mean
// what a user means by it. A re-run against unchanged sources is idempotent —
// it creates nothing and emits an `import.completed` with all-skipped counts —
// so without this filter the sequence the ADR advertises for a stranger
// (import, read the report, undo) would undo an empty batch and leave the rows
// in place. ADR-0005 Amendment 1 makes the skip legal *and* announced: the
// skipped id is returned so the caller can print it, because a command that
// quietly acts on a batch other than the newest one is unauditable.
func (k *MemoryKernel) latestImportBatch() (batch, skipped string, err error) {
	rows, qerr := k.db.Query(`
		SELECT json_extract(payload, '$.batch'),
		       json_extract(payload, '$.decisions') + json_extract(payload, '$.notes')
		  FROM events WHERE kind = 'import.completed' ORDER BY seq DESC`)
	if qerr != nil {
		return "", "", qerr
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var created int
		if err := rows.Scan(&id, &created); err != nil {
			return "", "", err
		}
		if created > 0 {
			return id, skipped, rows.Err()
		}
		if skipped == "" {
			skipped = id
		}
	}
	return "", skipped, rows.Err()
}

// importBatchExists reports whether a batch id was ever recorded.
func (k *MemoryKernel) importBatchExists(batchID string) (bool, error) {
	var n int
	err := k.db.QueryRow(`
		SELECT COUNT(*) FROM events
		 WHERE kind = 'import.completed'
		   AND json_extract(payload, '$.batch') = ?`, batchID).Scan(&n)
	return n > 0, err
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
