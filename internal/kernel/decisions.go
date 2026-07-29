package kernel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/memtrace-dev/memtrace/internal/scope"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// DecisionStore owns the governed half of the schema: decisions, evidence and
// the event log.
//
// Every mutating method here runs in one transaction that contains both the
// state change and the event describing it. ADR-0001 §D7 makes that pairing a
// kernel responsibility and states it explicitly; the only documented
// exception is the v1→v2 migration, which writes a single migration.completed
// event rather than fabricating per-row histories it never observed.
type DecisionStore struct {
	db *sql.DB
	// session is the provenance stamped on events this store writes when the
	// emitter does not set its own (ADR-0004 §D3). The MCP path passes the
	// session on the write itself; CLI governance actions — accept, reject,
	// revise, evidence.added — had no way to carry one and landed with a NULL
	// session_id, which is precisely the set of rows a Tier-3 audit trail is
	// sold on (F25).
	//
	// Guarded by a mutex to match the kernel's own session state. MCP tool
	// handlers run concurrently, and today no race is reachable — Go's &&
	// short-circuits, and every MCP-reachable emitter already carries its own
	// session, so the fields are never read on that path. That is an accident
	// of the current emitter set, not a design, and the packer changes the
	// premise: pack.served/pack.item will emit through this store. Making it
	// deliberate costs a mutex.
	sessionMu    sync.RWMutex
	sessionID    string
	sessionAgent string
	sessionModel string
}

// SetSessionContext sets the provenance for events this store writes; empty
// values clear it. The caller is responsible for having announced the session
// first: emission happens inside a write transaction, and opening a second one
// to write session.started from in there would deadlock the single writer.
func (s *DecisionStore) SetSessionContext(id, agent, model string) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	s.sessionID, s.sessionAgent, s.sessionModel = id, agent, model
}

// emit appends an event, filling in the store's session provenance when the
// emitter did not set its own.
//
// The once-emitters (appendEventOnce: diff.scope_match, decision.expired)
// deliberately bypass this and carry no session stamp. The reason is that they
// are written through a different function, not that they are a different
// category of event — decision.violated and revert.detected are equally system
// observations and are stamped, because they go through emit. §D7 lists no
// session_id for any of the three, so nothing is owed either way; if a
// once-emitted kind ever needs the stamp, it needs an emitOnce, not an
// argument about categories.
func (s *DecisionStore) emit(tx *sql.Tx, in EventInput) (string, error) {
	if in.SessionID == "" {
		s.sessionMu.RLock()
		id, agent, model := s.sessionID, s.sessionAgent, s.sessionModel
		s.sessionMu.RUnlock()
		if id != "" {
			in.SessionID = id
			if in.Agent == "" {
				in.Agent = agent
			}
			if in.Model == "" {
				in.Model = model
			}
		}
	}
	return appendEvent(tx, in)
}

// NewDecisionStore returns a store over an already-migrated database.
func NewDecisionStore(db *sql.DB) *DecisionStore { return &DecisionStore{db: db} }

// DB exposes the underlying handle for callers that need their own transaction
// (the v1→v2 migration does).
func (s *DecisionStore) DB() *sql.DB { return s.db }

const decisionColumns = `id, project_id, kind, title, body, status, scope, confidence,
	source, source_ref, agent, model, session_id, expires_at, topic_key, tags,
	supersedes, superseded_by, embedding, created_at, updated_at, decided_at,
	status_changed_at, accessed_at, access_count, pending_topic_key`

// DecisionInput is a new decision. Lifecycle fields are not settable by the
// caller: birth state is determined by Source (D2).
type DecisionInput struct {
	ProjectID  string
	Kind       types.DecisionKind
	Title      string
	Body       string
	Scope      []string
	Confidence float64
	Source     types.DecisionSource
	SourceRef  string
	Agent      string
	Model      string
	SessionID  string
	ExpiresAt  *time.Time
	TopicKey   string
	Tags       []string
	Supersedes []string
	// Via records the channel on the decision.proposed event
	// ("mcp"|"cli"|"import"|"derived"). Defaults from Source.
	Via string
	// Evidence is attached in the same transaction as the proposal.
	Evidence []EvidenceInput
}

// EvidenceInput is one evidence row. Accepting is never set by the caller: the
// kernel sets it, in the acceptance transaction, on the rows that exist at the
// proposed→active transition (D4).
type EvidenceInput struct {
	Kind    types.EvidenceKind
	Ref     string
	Note    string
	AddedBy types.Actor
}

func defaultVia(src types.DecisionSource) string {
	switch src {
	case types.DecisionSourceAgent:
		return "mcp"
	case types.DecisionSourceUser:
		return "cli"
	case types.DecisionSourceDerived:
		return "derived"
	default:
		return "import"
	}
}

func actorFor(src types.DecisionSource) types.Actor {
	switch src {
	case types.DecisionSourceUser:
		return types.ActorHuman
	case types.DecisionSourceAgent:
		return types.ActorAgent
	default:
		return types.ActorSystem
	}
}

func (in *DecisionInput) applyDefaults() {
	if in.Kind == "" {
		in.Kind = types.DecisionKindDecision
	}
	if in.Source == "" {
		in.Source = types.DecisionSourceUser
	}
	if in.Confidence == 0 {
		in.Confidence = 1.0
	}
	if in.Scope == nil {
		in.Scope = []string{}
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	if in.Supersedes == nil {
		in.Supersedes = []string{}
	}
	if in.Via == "" {
		in.Via = defaultVia(in.Source)
	}
}

func (in *DecisionInput) validate() error {
	if in.ProjectID == "" {
		return &types.ValidationError{Field: "project_id", Message: "must not be empty"}
	}
	if l := len([]rune(in.Title)); l < 1 || l > 200 {
		return &types.ValidationError{Field: "title", Message: "must be 1–200 characters"}
	}
	switch in.Kind {
	case types.DecisionKindDecision, types.DecisionKindConvention:
	default:
		return &types.ValidationError{Field: "kind", Message: "must be decision or convention"}
	}
	switch in.Source {
	case types.DecisionSourceUser, types.DecisionSourceAgent, types.DecisionSourceGit,
		types.DecisionSourceImport, types.DecisionSourceDerived:
	default:
		return &types.ValidationError{Field: "source", Message: "unknown source"}
	}
	if in.Confidence < 0 || in.Confidence > 1 {
		return &types.ValidationError{Field: "confidence", Message: "must be between 0.0 and 1.0"}
	}
	// D4: the MCP path always carries provenance; a save claiming to come from
	// an agent without a session cannot be attributed and is rejected.
	if in.Source == types.DecisionSourceAgent && in.SessionID == "" {
		return &types.ValidationError{Field: "session_id", Message: "required for agent-sourced decisions"}
	}
	if err := scope.Validate(in.Scope); err != nil {
		return err
	}
	for _, e := range in.Evidence {
		if err := e.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (e *EvidenceInput) validate() error {
	switch e.Kind {
	case types.EvidenceKindCommit, types.EvidenceKindPR, types.EvidenceKindTest,
		types.EvidenceKindFile, types.EvidenceKindURL, types.EvidenceKindImport:
	default:
		return &types.ValidationError{Field: "evidence.kind", Message: "unknown evidence kind"}
	}
	if e.Ref == "" {
		return &types.ValidationError{Field: "evidence.ref", Message: "must not be empty"}
	}
	switch e.AddedBy {
	case types.ActorHuman, types.ActorAgent, types.ActorSystem:
	default:
		return &types.ValidationError{Field: "evidence.added_by", Message: "must be human, agent or system"}
	}
	return nil
}

// Propose creates a decision in `proposed`, the mandatory entry state (D2),
// and emits decision.proposed in the same transaction.
//
// A decision is *always* born proposed, including one whose source is `user`.
// D2 permits a human-sourced decision to be born active because "the human is
// the confirmation"; that is implemented as ProposeAccepted, which runs the
// real proposed→active transition inside one transaction. Modelling it as a
// transition rather than as a second birth state keeps the state machine and
// the event catalogue exactly as specified: acceptance emits
// decision.accepted, which is D4's audit witness for forced (unevidenced)
// acceptances, and no active row exists without one.
func (s *DecisionStore) Propose(in DecisionInput) (*types.Decision, error) {
	in.applyDefaults()
	if err := in.validate(); err != nil {
		return nil, err
	}

	var out *types.Decision
	err := s.withTx(func(tx *sql.Tx) error {
		d, err := s.proposeTx(tx, &in)
		out = d
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *DecisionStore) proposeTx(tx *sql.Tx, in *DecisionInput) (*types.Decision, error) {
	now := time.Now().UTC()

	// D4: saving under an existing topic_key does not upsert in place. It
	// creates a proposed successor with `supersedes` pre-linked to the current
	// non-terminal holder; acceptance completes the supersession (D5).
	//
	// The successor cannot carry the key at birth: idx_decisions_topic_key is
	// unique across `proposed`, `active` and `violated`, and the predecessor is
	// still holding it. It is born with `pending_topic_key` instead, and the
	// key transfers in the acceptance transaction after the predecessors have
	// gone terminal and freed it (ADR-0001 Amendment 1, D5).
	//
	// The carrier is a column, not the decision.proposed event payload: the
	// payload made an append-only log row load-bearing *current state* — the
	// snapshot/log inversion rejected alternative C was rejected for — and it
	// contradicted D3's "while proposed, everything is editable in place",
	// since append-only triggers would have frozen it. The payload key remains
	// as an audit record of what was claimed, and nothing reads it.
	pendingTopicKey := ""
	if in.TopicKey != "" {
		var holder string
		err := tx.QueryRow(`
			SELECT id FROM decisions
			 WHERE project_id = ? AND topic_key = ?
			   AND status IN ('proposed','active','violated')`,
			in.ProjectID, in.TopicKey).Scan(&holder)
		switch {
		case err == nil:
			if !containsString(in.Supersedes, holder) {
				in.Supersedes = append(in.Supersedes, holder)
			}
			pendingTopicKey = in.TopicKey
			in.TopicKey = ""
		case errors.Is(err, sql.ErrNoRows):
		default:
			return nil, fmt.Errorf("topic_key lookup: %w", err)
		}
	}

	for _, id := range in.Supersedes {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM decisions WHERE id = ?`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("%w: supersedes references %s", types.ErrDecisionNotFound, id)
		}
	}

	d := &types.Decision{
		ID:              util.GenerateID(),
		ProjectID:       in.ProjectID,
		Kind:            in.Kind,
		Title:           in.Title,
		Body:            in.Body,
		Status:          types.StatusProposed,
		Scope:           in.Scope,
		Confidence:      in.Confidence,
		Source:          in.Source,
		SourceRef:       in.SourceRef,
		Agent:           in.Agent,
		Model:           in.Model,
		SessionID:       in.SessionID,
		ExpiresAt:       in.ExpiresAt,
		TopicKey:        in.TopicKey,
		PendingTopicKey: pendingTopicKey,
		Tags:            in.Tags,
		Supersedes:      in.Supersedes,
		CreatedAt:       now,
		UpdatedAt:       now,
		StatusChangedAt: now,
	}

	if _, err := tx.Exec(`
		INSERT INTO decisions (`+decisionColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ProjectID, string(d.Kind), d.Title, d.Body, string(d.Status),
		mustJSON(d.Scope), d.Confidence, string(d.Source), nullableString(d.SourceRef),
		nullableString(d.Agent), nullableString(d.Model), nullableString(d.SessionID),
		nullableTime(d.ExpiresAt), nullableString(d.TopicKey), mustJSON(d.Tags),
		mustJSON(d.Supersedes), nil, nil,
		fmtTime(d.CreatedAt), fmtTime(d.UpdatedAt), nil, fmtTime(d.StatusChangedAt), nil, 0,
		nullableString(d.PendingTopicKey),
	); err != nil {
		return nil, fmt.Errorf("inserting decision: %w", err)
	}

	for _, e := range in.Evidence {
		if _, err := insertEvidence(tx, d.ID, e, false, now); err != nil {
			return nil, err
		}
	}

	payload := map[string]any{"via": in.Via}
	switch {
	case d.TopicKey != "":
		payload["topic_key"] = d.TopicKey
	case pendingTopicKey != "":
		payload["topic_key"] = pendingTopicKey
	}
	if _, err := s.emit(tx, EventInput{
		ProjectID:  d.ProjectID,
		Kind:       types.EventDecisionProposed,
		Actor:      actorFor(d.Source),
		DecisionID: d.ID,
		SessionID:  d.SessionID,
		Agent:      d.Agent,
		Model:      d.Model,
		Payload:    payload,
	}); err != nil {
		return nil, err
	}
	return d, nil
}

// promotedNotePrefix marks a decision born from a note (A2.3). It is carried
// on source_ref, which is the durable link between the two rows.
const promotedNotePrefix = "note:"

// AcceptOptions controls a proposed→active transition.
type AcceptOptions struct {
	// Force bypasses the evidence requirement. The bypass is recorded in the
	// decision.accepted payload as "forced": true — the audit trail shows
	// exactly which decisions are unevidenced (D4).
	Force bool
	// Actor defaults to human. Acceptance is a human action by design: an
	// agent asserting "the user approved" is the assertion the quarantine
	// exists to distrust (D2, open question 3).
	Actor types.Actor
}

// ProposeAccepted creates and immediately accepts a decision in one
// transaction. This is D2's "human-sourced decisions may be born active".
func (s *DecisionStore) ProposeAccepted(in DecisionInput, opts AcceptOptions) (*types.Decision, error) {
	in.applyDefaults()
	if err := in.validate(); err != nil {
		return nil, err
	}
	if !in.Source.MayBeBornActive() {
		return nil, &types.ValidationError{
			Field:   "source",
			Message: "only user-sourced decisions may be born active; everything else is quarantined as proposed",
		}
	}

	var out *types.Decision
	err := s.withTx(func(tx *sql.Tx) error {
		d, err := s.proposeTx(tx, &in)
		if err != nil {
			return err
		}
		if err := s.acceptTx(tx, d, opts); err != nil {
			return err
		}
		out = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Accept performs proposed→active: it requires at least one evidence row
// (unless forced), marks every evidence row present at this moment as
// accepting, supersedes the rows named in `supersedes`, and emits
// decision.accepted plus one decision.superseded per predecessor — all in one
// transaction (D4, D5, D7).
func (s *DecisionStore) Accept(id string, opts AcceptOptions) (*types.Decision, error) {
	var out *types.Decision
	err := s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		if err := s.acceptTx(tx, d, opts); err != nil {
			return err
		}
		out = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *DecisionStore) acceptTx(tx *sql.Tx, d *types.Decision, opts AcceptOptions) error {
	// Acceptance is the proposed→active edge and nothing else. The general
	// transition guard is not enough here for two reasons, both reachable:
	//
	//   - CanTransition(x, x) is true (a same-status call is a legal no-op for
	//     `transition`), so a second Accept on an *active* decision would re-run
	//     this whole transaction — including the accepting-evidence UPDATE. That
	//     retroactively promotes evidence attached later via evidence.added,
	//     which §D4 forbids in as many words ("rows attached later stay 0 and
	//     are immutable in this respect — no retroactive promotion; the
	//     accepting set is a fact about one moment"), and it re-arms exactly the
	//     fragility the founder's item-6 ruling narrowed §D6 to remove: a revert
	//     of a later conforming commit would then terminate the decision.
	//   - CanTransition(violated, active) is true, because that edge exists for
	//     dismissal and counter-revert. Reaching it through Accept would emit
	//     decision.accepted instead of decision.reinstated and leave every
	//     violation episode unresolved (A2.2). Reinstatement has its own API.
	if d.Status != types.StatusProposed {
		return &types.IllegalTransitionError{From: d.Status, To: types.StatusActive}
	}
	if opts.Actor == "" {
		opts.Actor = types.ActorHuman
	}

	var evidenceCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM evidence WHERE decision_id = ?`, d.ID).
		Scan(&evidenceCount); err != nil {
		return err
	}
	if evidenceCount == 0 && !opts.Force {
		return types.ErrNoEvidence
	}

	// The accepting set is a fact about one moment: rows attached later stay 0
	// and are never retroactively promoted (D4).
	if _, err := tx.Exec(`UPDATE evidence SET accepting = 1 WHERE decision_id = ?`, d.ID); err != nil {
		return fmt.Errorf("marking accepting evidence: %w", err)
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(`
		UPDATE decisions
		   SET status = 'active', decided_at = COALESCE(decided_at, ?),
		       status_changed_at = ?, updated_at = ?
		 WHERE id = ?`, fmtTime(now), fmtTime(now), fmtTime(now), d.ID); err != nil {
		return fmt.Errorf("accepting decision: %w", err)
	}
	d.Status = types.StatusActive
	d.DecidedAt = &now
	d.StatusChangedAt = now
	d.UpdatedAt = now

	if _, err := s.emit(tx, EventInput{
		ProjectID:  d.ProjectID,
		Kind:       types.EventDecisionAccepted,
		Actor:      opts.Actor,
		DecisionID: d.ID,
		Payload:    map[string]any{"evidence_count": evidenceCount, "forced": opts.Force},
	}); err != nil {
		return err
	}

	// D5: predecessors that are still non-terminal become superseded now.
	// Already-terminal rows named in `supersedes` are left untouched — the
	// link is informational.
	for _, predID := range d.Supersedes {
		pred, err := loadDecisionTx(tx, predID)
		if err != nil {
			return err
		}
		if pred.Status.IsTerminal() {
			continue
		}
		if _, err := tx.Exec(`
			UPDATE decisions
			   SET status = 'superseded', superseded_by = ?, status_changed_at = ?, updated_at = ?
			 WHERE id = ?`, d.ID, fmtTime(now), fmtTime(now), predID); err != nil {
			return fmt.Errorf("superseding %s: %w", predID, err)
		}
		if _, err := s.emit(tx, EventInput{
			ProjectID:  pred.ProjectID,
			Kind:       types.EventDecisionSuperseded,
			Actor:      types.ActorSystem,
			DecisionID: predID,
			Payload:    map[string]any{"successor_id": d.ID},
		}); err != nil {
			return err
		}
	}

	// A2.3: a promoted note is archived *in the acceptance transaction*, not at
	// promotion time. While the promotion sits proposed the note stays active,
	// so nothing stops surfacing for the days or weeks a proposal may wait; a
	// proposal never packs, so there is no duplication window either. A
	// rejected promotion leaves the note untouched. The decision's source_ref
	// is the durable link — notes have no event log and need none here.
	if noteID, ok := strings.CutPrefix(d.SourceRef, promotedNotePrefix); ok && noteID != "" {
		if _, err := tx.Exec(
			`UPDATE notes SET status = 'archived', updated_at = ? WHERE id = ? AND status <> 'archived'`,
			fmtTime(now), noteID); err != nil {
			return fmt.Errorf("archiving promoted note %s: %w", noteID, err)
		}
	}

	return applyPendingTopicKeyTx(tx, d)
}

// applyPendingTopicKeyTx is the final step of the acceptance transaction
// (ADR-0001 Amendment 1, D5). The predecessors have just been superseded, so
// the key they held is free; the successor claims it now.
//
// If some *other* non-terminal row still holds the key — a predecessor was
// edited out of `supersedes` while proposed, a third row acquired the key in
// the interim, or a competing pending successor was accepted first — the whole
// acceptance fails with types.ErrTopicKeyHeld naming the holder. Nothing is
// silently dropped: accepting the row without its claimed key would change
// what the human saved. The partial unique index remains the backstop for any
// writer that bypasses this check.
func applyPendingTopicKeyTx(tx *sql.Tx, d *types.Decision) error {
	if d.PendingTopicKey == "" {
		return nil
	}
	key := d.PendingTopicKey

	var holderID, holderStatus string
	err := tx.QueryRow(`
		SELECT id, status FROM decisions
		 WHERE project_id = ? AND topic_key = ? AND id <> ?
		   AND status IN ('proposed','active','violated')`,
		d.ProjectID, key, d.ID).Scan(&holderID, &holderStatus)
	switch {
	case err == nil:
		return &types.TopicKeyHeldError{
			TopicKey:     key,
			HolderID:     holderID,
			HolderStatus: types.DecisionStatus(holderStatus),
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return fmt.Errorf("checking topic_key %q: %w", key, err)
	}

	// topic_key and pending_topic_key are mutually exclusive; the swap is one
	// statement so the pair is never both-set, not even mid-transaction.
	if _, err := tx.Exec(
		`UPDATE decisions SET topic_key = ?, pending_topic_key = NULL WHERE id = ?`,
		key, d.ID); err != nil {
		return fmt.Errorf("transferring topic_key %q: %w", key, err)
	}
	d.TopicKey = key
	d.PendingTopicKey = ""
	return nil
}

// ClearPendingTopicKey drops a proposal's claimed topic_key. It is one of the
// three recoveries from ErrTopicKeyHeld (the others: supersede the named
// holder, or reject the proposal). Legal only while proposed — a pending key
// is meaningless in any other state, and D3 makes everything editable in place
// while proposed.
func (s *DecisionStore) ClearPendingTopicKey(id string, actor types.Actor) error {
	if actor == "" {
		actor = types.ActorHuman
	}
	return s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		if d.Status != types.StatusProposed {
			return types.ErrDecisionImmutable
		}
		if d.PendingTopicKey == "" {
			return nil
		}
		if _, err := tx.Exec(
			`UPDATE decisions SET pending_topic_key = NULL, updated_at = ? WHERE id = ?`,
			fmtTime(time.Now().UTC()), d.ID); err != nil {
			return err
		}
		// pending_topic_key is in A2.1's normative set, so clearing it is a
		// revision, not an advisory patch.
		_, err = s.emit(tx, EventInput{
			ProjectID:  d.ProjectID,
			Kind:       types.EventDecisionRevised,
			Actor:      actor,
			DecisionID: d.ID,
			Payload:    map[string]any{"fields": []string{"pending_topic_key"}},
		})
		return err
	})
}

// Reject performs proposed→rejected. The audit record survives; nothing is
// deleted (D2).
//
// The actor is a parameter and not a constant. §D3 says entering `rejected` is
// a human-confirmed action, and that is a policy question about *what may
// trigger this transition*; it is not a licence to record every rejection as a
// human's. An agent-initiated disposal (memory_forget over MCP on its own
// proposal) previously produced `decision.rejected actor=human agent=mcp` — a
// row asserting that a human did something no human did, in an append-only log
// whose whole value is that it is traceable. Whoever triggers it is recorded
// as having triggered it. An empty actor defaults to human, which is the
// CLI/TUI case.
func (s *DecisionStore) Reject(id, reason string, actor types.Actor) error {
	if actor == "" {
		actor = types.ActorHuman
	}
	return s.transition(id, types.StatusRejected, types.EventDecisionRejected, actor,
		func(d *types.Decision) map[string]any {
			p := map[string]any{}
			if reason != "" {
				p["reason"] = reason
			}
			return p
		}, "")
}

// RevertOptions describes why a decision was undone.
type RevertOptions struct {
	// Via is "revert_detected" (the accepting evidence commit was reverted) or
	// "human" (an explicit repeal).
	Via string
	// RevertedEvidenceRef is the accepting evidence ref that was reverted.
	RevertedEvidenceRef string
	// CommitSHA is the reverting commit, set when Via is revert_detected.
	CommitSHA string
	Actor     types.Actor
}

// Revert moves an active or violated decision to the terminal `reverted`
// state. D6 narrows the automatic rule to *accepting* evidence: a revert of a
// non-accepting evidence commit produces a violation, not a revert.
func (s *DecisionStore) Revert(id string, opts RevertOptions) error {
	if opts.Via == "" {
		opts.Via = "human"
	}
	actor := opts.Actor
	if actor == "" {
		if opts.Via == "revert_detected" {
			actor = types.ActorSystem
		} else {
			actor = types.ActorHuman
		}
	}
	return s.transition(id, types.StatusReverted, types.EventDecisionReverted, actor,
		func(d *types.Decision) map[string]any {
			p := map[string]any{"via": opts.Via}
			if opts.RevertedEvidenceRef != "" {
				p["reverted_evidence_ref"] = opts.RevertedEvidenceRef
			}
			return p
		}, opts.CommitSHA)
}

// ViolationOptions describes a detected violation (D6). Detection itself is
// ADR-0004's; this records its verdict.
type ViolationOptions struct {
	CommitSHA    string
	RevertedSHA  string
	Files        []string
	MatchedGlobs []string
}

// MarkViolated records one violation episode: a `violate` verdict on a
// distinct violating commit (ADR-0001 Amendment 2, A2.2).
//
// Every new episode emits its own decision.violated event, including when the
// decision is *already* violated; the state transition happens only on the
// first. Before the amendment a repeat violation was a documented no-op, so a
// decision violated fifty times counted once — falsifier 2 read low by
// construction, and ADR-0002 §P8's "VIOLATED (n unresolved)" marker could
// never exceed 1.
//
// Idempotency comes from the schema, not from a status check: the episode's
// diff.scope_match row is inserted in the same transaction, and
// idx_events_scopematch_once makes a rescan of the same (decision, commit) a
// no-op for both rows. That leaves the Amendment 1 verdict-freeze disposition
// intact — freezing governs rescans of the same pair, never a new commit.
//
// Reports whether a new episode was recorded.
func (s *DecisionStore) MarkViolated(id string, opts ViolationOptions) (bool, error) {
	// An episode *is* a verdict on a distinct violating commit (A2.2), so the
	// commit is what gives it identity, and without one two invariants break at
	// once: idx_events_scopematch_once is UNIQUE(decision_id, commit_sha) and
	// SQLite treats NULLs as distinct in unique indexes, so the schema-level
	// idempotency this method's whole argument rests on would not fire and
	// every call would create another episode; and unresolvedViolationsSQL's
	// revert clause needs a sha, so none of those episodes could ever be
	// resolved by anything but a dismissal naming its exact event id. §P8's
	// marker would climb without bound.
	if opts.CommitSHA == "" {
		return false, &types.ValidationError{
			Field:   "commit_sha",
			Message: "a violation episode is a verdict on a distinct violating commit; without one it has no identity and can never be resolved",
		}
	}

	var recorded bool
	err := s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		// A proposal is not binding and cannot be violated; a terminal row makes
		// no further transitions. Checked against the matrix even when the
		// decision is already violated, so the illegal cases stay illegal.
		if d.Status != types.StatusViolated {
			if err := types.CheckTransition(d.Status, types.StatusViolated); err != nil {
				return err
			}
		}

		// The observation. Its unique index is the episode's identity.
		_, inserted, err := appendEventOnce(tx, EventInput{
			ProjectID:  d.ProjectID,
			Kind:       types.EventDiffScopeMatch,
			Actor:      types.ActorSystem,
			DecisionID: d.ID,
			CommitSHA:  opts.CommitSHA,
			Payload: map[string]any{
				"files":         nonNilStrings(opts.Files),
				"matched_globs": nonNilStrings(opts.MatchedGlobs),
				"verdict":       "violate",
			},
		})
		if err != nil {
			return err
		}
		if !inserted {
			return nil // already observed: rescans are no-ops
		}
		recorded = true

		payload := map[string]any{
			"files":         nonNilStrings(opts.Files),
			"matched_globs": nonNilStrings(opts.MatchedGlobs),
		}
		// §D7 shows reverted_sha as a SHA. An empty string is a claim about a
		// commit that does not exist, so the key is omitted when the verdict came
		// from a scope match rather than a revert.
		if opts.RevertedSHA != "" {
			payload["reverted_sha"] = opts.RevertedSHA
		}

		if d.Status == types.StatusViolated {
			// A further episode on an already-violated decision: the event, and
			// no state change.
			_, err := s.emit(tx, EventInput{
				ProjectID:  d.ProjectID,
				Kind:       types.EventDecisionViolated,
				Actor:      types.ActorSystem,
				DecisionID: d.ID,
				CommitSHA:  opts.CommitSHA,
				Payload:    payload,
			})
			return err
		}
		return s.applyTransitionTx(tx, d, types.StatusViolated,
			types.EventDecisionViolated, types.ActorSystem, payload, opts.CommitSHA)
	})
	return recorded, err
}

// UnresolvedViolations counts the decision's violation episodes that have
// neither been dismissed nor had their violating commit reverted (A2.2).
//
// This is the *n* in ADR-0002 §P8's "VIOLATED (n unresolved)" and the
// denominator falsifier 2 reads, so it is computed from the event log rather
// than cached anywhere.
func (s *DecisionStore) UnresolvedViolations(decisionID string) (int, error) {
	var n int
	err := s.db.QueryRow(unresolvedViolationsSQL, decisionID).Scan(&n)
	return n, err
}

// unresolvedViolationsSQL counts episodes with no resolution. An episode is
// resolved by a dismissal naming its event id, or by a revert.detected whose
// target is the episode's violating commit.
const unresolvedViolationsSQL = `
SELECT COUNT(*) FROM events v
 WHERE v.decision_id = ? AND v.kind = 'decision.violated'
   AND NOT EXISTS (
        SELECT 1 FROM events x
         WHERE x.kind = 'decision.violation_dismissed'
           AND x.decision_id = v.decision_id
           AND json_extract(x.payload, '$.violation_event_id') = v.id)
   AND NOT EXISTS (
        SELECT 1 FROM events r
         WHERE r.kind = 'revert.detected'
           AND v.commit_sha IS NOT NULL
           AND json_extract(r.payload, '$.reverts_sha') = v.commit_sha)`

func unresolvedViolationsTx(tx *sql.Tx, decisionID string) (int, error) {
	var n int
	err := tx.QueryRow(unresolvedViolationsSQL, decisionID).Scan(&n)
	return n, err
}

// ReinstateOptions identifies the episode a counter-revert resolves.
type ReinstateOptions struct {
	// ViolatingSHA is the violating commit that has itself been reverted —
	// this is what names the episode.
	ViolatingSHA string
	// CommitSHA is the reverting commit.
	CommitSHA string
}

// Reinstate records that a violating commit was itself reverted, resolving
// *that episode only*, and returns the decision to `active` at the
// zero-crossing — when no unresolved episode remains (A2.2).
//
// The resolution record is the revert.detected event (§D7), written here in
// the same transaction as any transition. The observer, when it lands, calls
// this rather than emitting revert.detected on its own, so a counter-revert
// produces exactly one record; a repeat call for the same pair writes nothing.
func (s *DecisionStore) Reinstate(id string, opts ReinstateOptions) error {
	return s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}

		if opts.ViolatingSHA != "" {
			var existing int
			if err := tx.QueryRow(`
				SELECT COUNT(*) FROM events
				 WHERE kind = 'revert.detected'
				   AND json_extract(payload, '$.reverts_sha') = ?`,
				opts.ViolatingSHA).Scan(&existing); err != nil {
				return err
			}
			if existing == 0 {
				if _, err := s.emit(tx, EventInput{
					ProjectID: d.ProjectID,
					Kind:      types.EventRevertDetected,
					Actor:     types.ActorSystem,
					CommitSHA: opts.CommitSHA,
					Payload: map[string]any{
						"reverts_sha": opts.ViolatingSHA,
						"method":      "trailer",
					},
				}); err != nil {
					return err
				}
			}
		}

		// The revert.detected row is global by sha, and so is the resolution it
		// provides: reverting X resolves *every* decision's episode on X. One
		// commit violating two decisions is the ordinary case — a commit matches
		// by glob and scopes overlap by design — so the zero-crossing has to be
		// evaluated for every decision the revert touched, not only for the one
		// the caller named. Otherwise the others sit `violated` forever with
		// UnresolvedViolations == 0 and no reinstatement event, and §P8 renders
		// "VIOLATED (0 unresolved)".
		affected := []string{d.ID}
		if opts.ViolatingSHA != "" {
			rows, err := tx.Query(`
				SELECT DISTINCT decision_id FROM events
				 WHERE kind = 'decision.violated' AND commit_sha = ?
				   AND decision_id IS NOT NULL AND decision_id <> ?`,
				opts.ViolatingSHA, d.ID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var other string
				if err := rows.Scan(&other); err != nil {
					rows.Close()
					return err
				}
				affected = append(affected, other)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}

		for _, decisionID := range affected {
			target := d
			if decisionID != d.ID {
				target, err = loadDecisionTx(tx, decisionID)
				if err != nil {
					return err
				}
			}
			if target.Status != types.StatusViolated {
				continue
			}
			unresolved, err := unresolvedViolationsTx(tx, target.ID)
			if err != nil {
				return err
			}
			if unresolved > 0 {
				// Other episodes are still open: the decision stays violated and
				// no reinstatement event is emitted. §P8's marker keeps counting.
				continue
			}
			if err := s.applyTransitionTx(tx, target, types.StatusActive,
				types.EventDecisionReinstated, types.ActorSystem,
				map[string]any{"via": "counter_revert"}, opts.CommitSHA); err != nil {
				return err
			}
		}
		return nil
	})
}

// DismissViolation records a human dismissing one violation episode
// ("false_positive" or "accepted_exception") and reinstates the decision if
// that was the last unresolved episode (A2.2).
//
// violationEventID is validated: it must name a decision.violated event of
// *this* decision that is currently unresolved. Dismissing a foreign or
// already-resolved episode is a typed error, not a silent reinstatement — the
// unresolved arithmetic §P8 and falsifier 2 read must not be corruptible by a
// dangling or duplicate dismissal. (The shipped version reinstated
// unconditionally and validated nothing, which was harmless only while at most
// one violation event could exist.)
func (s *DecisionStore) DismissViolation(id, violationEventID, reason string) error {
	return s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}

		var episodeDecision string
		var episodeSHA sql.NullString
		err = tx.QueryRow(`
			SELECT decision_id, commit_sha FROM events
			 WHERE id = ? AND kind = 'decision.violated'`, violationEventID).
			Scan(&episodeDecision, &episodeSHA)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: %s", types.ErrUnknownViolationEpisode, violationEventID)
		case err != nil:
			return err
		}
		if episodeDecision != d.ID {
			return fmt.Errorf("%w: %s belongs to decision %s",
				types.ErrUnknownViolationEpisode, violationEventID, episodeDecision)
		}

		resolved, err := episodeResolvedTx(tx, violationEventID, episodeSHA)
		if err != nil {
			return err
		}
		if resolved {
			return fmt.Errorf("%w: %s", types.ErrViolationAlreadyResolved, violationEventID)
		}

		if _, err := s.emit(tx, EventInput{
			ProjectID:  d.ProjectID,
			Kind:       types.EventDecisionViolationDismissed,
			Actor:      types.ActorHuman,
			DecisionID: d.ID,
			Payload: map[string]any{
				"violation_event_id": violationEventID,
				"reason":             reason,
			},
		}); err != nil {
			return err
		}

		if d.Status != types.StatusViolated {
			return nil // nothing to reinstate
		}
		unresolved, err := unresolvedViolationsTx(tx, d.ID)
		if err != nil {
			return err
		}
		if unresolved > 0 {
			return nil // other episodes remain open
		}
		return s.applyTransitionTx(tx, d, types.StatusActive, types.EventDecisionReinstated,
			types.ActorHuman, map[string]any{"via": "dismissal"}, "")
	})
}

// episodeResolvedTx reports whether one episode already has a resolution.
func episodeResolvedTx(tx *sql.Tx, eventID string, commitSHA sql.NullString) (bool, error) {
	var n int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM events
		 WHERE kind = 'decision.violation_dismissed'
		   AND json_extract(payload, '$.violation_event_id') = ?`, eventID).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if !commitSHA.Valid || commitSHA.String == "" {
		return false, nil
	}
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM events
		 WHERE kind = 'revert.detected'
		   AND json_extract(payload, '$.reverts_sha') = ?`, commitSHA.String).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// MarkExpired emits decision.expired the first time any component observes a
// decision to be expired. Expiry is a derived predicate and changes no state
// (D2). Reports whether the event was new.
//
// Two rules, both from ADR-0001 Amendment 1's same-class audit (item 1):
//
//   - The event marks *first* expiry only. `expires_at` can be extended and the
//     decision can expire again, but idx_events_expired_once blocks a second
//     event forever. Every consumer — packer, linter, reports — must therefore
//     read current expiry from the predicate `expires_at < now`, never from
//     this event.
//   - Emission is INSERT OR IGNORE: two components observing the same expiry
//     is normal, and the second write is a no-op rather than an error.
//
// The predicate is checked here rather than trusted from the caller: the event
// is append-only and index-protected, so an event emitted for a decision that
// has not expired yet can never be corrected.
func (s *DecisionStore) MarkExpired(id string) (bool, error) {
	var inserted bool
	err := s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		if !d.IsExpired(time.Now().UTC()) {
			return nil
		}
		_, ins, err := appendEventOnce(tx, EventInput{
			ProjectID:  d.ProjectID,
			Kind:       types.EventDecisionExpired,
			Actor:      types.ActorSystem,
			DecisionID: d.ID,
			Payload:    map[string]any{"expires_at": d.ExpiresAt.UTC().Format(time.RFC3339)},
		})
		inserted = ins
		return err
	})
	return inserted, err
}

// MetadataUpdate patches advisory metadata only. Normative content (title,
// body, scope, kind) is immutable after acceptance — changing what a decision
// says or where it applies is a supersession, not an update (D3). Use
// EditProposed while the decision is still proposed.
type MetadataUpdate struct {
	Tags       *[]string
	Confidence *float64
	ExpiresAt  **time.Time // outer nil = leave alone; inner nil = clear
}

// UpdateMetadata applies an advisory patch and emits decision.updated with the
// field list. Post-Amendment 2 that kind is truthfully advisory-only: nothing
// in A2.1's normative set is reachable from here.
func (s *DecisionStore) UpdateMetadata(id string, up MetadataUpdate, actor types.Actor) error {
	if actor == "" {
		actor = types.ActorHuman
	}
	return s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		var sets []string
		var args []any
		var fields []string
		if up.Tags != nil {
			sets = append(sets, "tags = ?")
			args = append(args, mustJSON(*up.Tags))
			fields = append(fields, "tags")
		}
		if up.Confidence != nil {
			if *up.Confidence < 0 || *up.Confidence > 1 {
				return &types.ValidationError{Field: "confidence", Message: "must be between 0.0 and 1.0"}
			}
			sets = append(sets, "confidence = ?")
			args = append(args, *up.Confidence)
			fields = append(fields, "confidence")
		}
		if up.ExpiresAt != nil {
			sets = append(sets, "expires_at = ?")
			args = append(args, nullableTime(*up.ExpiresAt))
			fields = append(fields, "expires_at")
		}
		if len(sets) == 0 {
			return nil
		}
		now := time.Now().UTC()
		sets = append(sets, "updated_at = ?")
		args = append(args, fmtTime(now), d.ID)

		if _, err := tx.Exec(`UPDATE decisions SET `+joinComma(sets)+` WHERE id = ?`, args...); err != nil {
			return fmt.Errorf("updating decision metadata: %w", err)
		}
		_, err = s.emit(tx, EventInput{
			ProjectID:  d.ProjectID,
			Kind:       types.EventDecisionUpdated,
			Actor:      actor,
			DecisionID: d.ID,
			Payload:    map[string]any{"fields": fields},
		})
		return err
	})
}

// EditProposed rewrites normative content while the decision is still
// proposed, where everything is editable in place (D3). It emits
// decision.revised with the fields that actually changed (Amendment 2, A2.1);
// decision.updated stays advisory-only and is UpdateMetadata's.
func (s *DecisionStore) EditProposed(id string, in DecisionInput, actor types.Actor) error {
	if actor == "" {
		actor = types.ActorHuman
	}
	return s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		if d.Status != types.StatusProposed {
			return types.ErrDecisionImmutable
		}
		if l := len([]rune(in.Title)); l < 1 || l > 200 {
			return &types.ValidationError{Field: "title", Message: "must be 1–200 characters"}
		}
		if err := scope.Validate(in.Scope); err != nil {
			return err
		}
		kind := in.Kind
		if kind == "" {
			kind = d.Kind
		}
		newScope := mustJSON(nonNilStrings(in.Scope))

		// Only the fields that actually changed are reported. A consumer that
		// reads the field list gets the truth; one that reads a fixed
		// four-field list learns nothing.
		var fields []string
		if in.Title != d.Title {
			fields = append(fields, "title")
		}
		if in.Body != d.Body {
			fields = append(fields, "body")
		}
		if newScope != mustJSON(nonNilStrings(d.Scope)) {
			fields = append(fields, "scope")
		}
		if kind != d.Kind {
			fields = append(fields, "kind")
		}
		if len(fields) == 0 {
			return nil
		}

		now := time.Now().UTC()
		if _, err := tx.Exec(`
			UPDATE decisions SET title = ?, body = ?, scope = ?, kind = ?, updated_at = ?
			 WHERE id = ?`,
			in.Title, in.Body, newScope, string(kind), fmtTime(now), d.ID,
		); err != nil {
			return fmt.Errorf("editing proposal: %w", err)
		}
		// decision.revised, not decision.updated: every field this method can
		// touch is normative (ADR-0001 Amendment 2, A2.1). The split is what
		// lets a consumer filtering on decision.updated trust that no normative
		// change happened — scope is what the whole attribution chain joins on,
		// so a scope change read as advisory noise would silently mis-read the
		// audit trail. Pre-amendment decision.updated rows carrying normative
		// field names are grandfathered: events are append-only and are not
		// rewritten.
		_, err = s.emit(tx, EventInput{
			ProjectID:  d.ProjectID,
			Kind:       types.EventDecisionRevised,
			Actor:      actor,
			DecisionID: d.ID,
			Payload:    map[string]any{"fields": fields},
		})
		return err
	})
}

// AddEvidence attaches an evidence row and emits evidence.added. The row is
// not accepting: only rows present at the acceptance transition are (D4).
func (s *DecisionStore) AddEvidence(decisionID string, in EvidenceInput) (*types.Evidence, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	var out *types.Evidence
	err := s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, decisionID)
		if err != nil {
			return err
		}
		// idx_evidence_dedupe would abort with a raw SQLite constraint error,
		// which is what the ADRs' typed-error rule exists to keep away from the
		// caller. Checked here so the refusal names the row (F23).
		var existing int
		if err := tx.QueryRow(`
			SELECT COUNT(*) FROM evidence
			 WHERE decision_id = ? AND kind = ? AND ref = ?`,
			decisionID, string(in.Kind), in.Ref).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return &types.DuplicateEvidenceError{
				DecisionID: decisionID, Kind: in.Kind, Ref: in.Ref,
			}
		}
		ev, err := insertEvidence(tx, decisionID, in, false, time.Now().UTC())
		if err != nil {
			return err
		}
		out = ev
		_, err = s.emit(tx, EventInput{
			ProjectID:  d.ProjectID,
			Kind:       types.EventEvidenceAdded,
			Actor:      in.AddedBy,
			DecisionID: decisionID,
			// commit_sha is deliberately not set: §D7 does not list it for this
			// kind, and populating it puts non-observation rows into
			// idx_events_commit, which ADR-0004's commit joins scan.
			Payload: map[string]any{
				"evidence_id": ev.ID,
				"kind":        string(ev.Kind),
				"ref":         ev.Ref,
			},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func insertEvidence(tx *sql.Tx, decisionID string, in EvidenceInput, accepting bool, now time.Time) (*types.Evidence, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	ev := &types.Evidence{
		ID:         util.GenerateID(),
		DecisionID: decisionID,
		Kind:       in.Kind,
		Ref:        in.Ref,
		Note:       in.Note,
		AddedBy:    in.AddedBy,
		Accepting:  accepting,
		CreatedAt:  now,
	}
	acc := 0
	if accepting {
		acc = 1
	}
	if _, err := tx.Exec(`
		INSERT INTO evidence (id, decision_id, kind, ref, note, added_by, accepting, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		ev.ID, ev.DecisionID, string(ev.Kind), ev.Ref, nullableString(ev.Note),
		string(ev.AddedBy), acc, fmtTime(ev.CreatedAt)); err != nil {
		return nil, fmt.Errorf("inserting evidence: %w", err)
	}
	return ev, nil
}

// Evidence returns a decision's evidence rows, oldest first.
func (s *DecisionStore) Evidence(decisionID string) ([]types.Evidence, error) {
	rows, err := s.db.Query(`
		SELECT id, decision_id, kind, ref, note, added_by, accepting, created_at
		  FROM evidence WHERE decision_id = ? ORDER BY created_at, id`, decisionID)
	if err != nil {
		return nil, fmt.Errorf("querying evidence: %w", err)
	}
	defer rows.Close()

	var out []types.Evidence
	for rows.Next() {
		var e types.Evidence
		var note sql.NullString
		var kind, addedBy, created string
		var accepting int
		if err := rows.Scan(&e.ID, &e.DecisionID, &kind, &e.Ref, &note, &addedBy,
			&accepting, &created); err != nil {
			return nil, err
		}
		e.Kind = types.EvidenceKind(kind)
		e.AddedBy = types.Actor(addedBy)
		e.Note = note.String
		e.Accepting = accepting == 1
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AcceptingCommitEvidence returns the decisions for which sha is *accepting*
// commit evidence — the only rows the automatic revert rule may terminate (D6).
func (s *DecisionStore) AcceptingCommitEvidence(sha string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT decision_id FROM evidence WHERE ref = ? AND kind = 'commit' AND accepting = 1`, sha)
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

// GetDecision returns a decision by id, or types.ErrDecisionNotFound.
func (s *DecisionStore) GetDecision(id string) (*types.Decision, error) {
	row := s.db.QueryRow(`SELECT `+decisionColumns+` FROM decisions WHERE id = ?`, id)
	d, err := scanDecision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrDecisionNotFound
	}
	return d, err
}

// DecisionFilter narrows a list query. Zero values mean "no filter".
type DecisionFilter struct {
	ProjectID string
	Statuses  []types.DecisionStatus
	Kind      types.DecisionKind
	TopicKey  string
	Limit     int
}

// ListDecisions returns decisions oldest first.
func (s *DecisionStore) ListDecisions(f DecisionFilter) ([]types.Decision, error) {
	q := `SELECT ` + decisionColumns + ` FROM decisions WHERE 1=1`
	var args []any
	if f.ProjectID != "" {
		q += " AND project_id = ?"
		args = append(args, f.ProjectID)
	}
	if len(f.Statuses) > 0 {
		q += " AND status IN ("
		for i, st := range f.Statuses {
			if i > 0 {
				q += ","
			}
			q += "?"
			args = append(args, string(st))
		}
		q += ")"
	}
	if f.Kind != "" {
		q += " AND kind = ?"
		args = append(args, string(f.Kind))
	}
	if f.TopicKey != "" {
		q += " AND topic_key = ?"
		args = append(args, f.TopicKey)
	}
	q += " ORDER BY created_at, id"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing decisions: %w", err)
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

// CountDecisions counts rows matching the filter.
func (s *DecisionStore) CountDecisions(f DecisionFilter) (int, error) {
	list, err := s.ListDecisions(f)
	return len(list), err
}

// --- transition plumbing ---

// transition applies a state change and its event in one transaction. A
// same-status call is a no-op that emits nothing.
func (s *DecisionStore) transition(
	id string, to types.DecisionStatus, kind types.EventKind, actor types.Actor,
	payload func(*types.Decision) map[string]any, commitSHA string,
) error {
	return s.withTx(func(tx *sql.Tx) error {
		d, err := loadDecisionTx(tx, id)
		if err != nil {
			return err
		}
		if d.Status == to {
			return nil
		}
		if err := types.CheckTransition(d.Status, to); err != nil {
			return err
		}
		return s.applyTransitionTx(tx, d, to, kind, actor, payload(d), commitSHA)
	})
}

func (s *DecisionStore) applyTransitionTx(
	tx *sql.Tx, d *types.Decision, to types.DecisionStatus,
	kind types.EventKind, actor types.Actor, payload map[string]any, commitSHA string,
) error {
	now := time.Now().UTC()
	if _, err := tx.Exec(`
		UPDATE decisions SET status = ?, status_changed_at = ?, updated_at = ?
		 WHERE id = ?`, string(to), fmtTime(now), fmtTime(now), d.ID); err != nil {
		return fmt.Errorf("transition %s -> %s: %w", d.Status, to, err)
	}
	d.Status = to
	d.StatusChangedAt = now
	d.UpdatedAt = now

	_, err := s.emit(tx, EventInput{
		ProjectID:  d.ProjectID,
		Kind:       kind,
		Actor:      actor,
		DecisionID: d.ID,
		CommitSHA:  commitSHA,
		Payload:    payload,
	})
	return err
}

func loadDecisionTx(tx *sql.Tx, id string) (*types.Decision, error) {
	row := tx.QueryRow(`SELECT `+decisionColumns+` FROM decisions WHERE id = ?`, id)
	d, err := scanDecision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", types.ErrDecisionNotFound, id)
	}
	return d, err
}

func (s *DecisionStore) withTx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func scanDecision(sc scanner) (*types.Decision, error) {
	var d types.Decision
	var (
		kind, status, source                         string
		sourceRef, agent, model, sessionID           sql.NullString
		expiresAt, topicKey, supersededBy, embedding sql.NullString
		decidedAt, accessedAt, pendingTopicKey       sql.NullString
		scopeJSON, tagsJSON, supersedesJSON          string
		createdAt, updatedAt, statusChangedAt        string
	)
	if err := sc.Scan(
		&d.ID, &d.ProjectID, &kind, &d.Title, &d.Body, &status, &scopeJSON, &d.Confidence,
		&source, &sourceRef, &agent, &model, &sessionID, &expiresAt, &topicKey, &tagsJSON,
		&supersedesJSON, &supersededBy, &embedding, &createdAt, &updatedAt, &decidedAt,
		&statusChangedAt, &accessedAt, &d.AccessCount, &pendingTopicKey,
	); err != nil {
		return nil, err
	}

	d.Kind = types.DecisionKind(kind)
	d.Status = types.DecisionStatus(status)
	d.Source = types.DecisionSource(source)
	d.SourceRef = sourceRef.String
	d.Agent = agent.String
	d.Model = model.String
	d.SessionID = sessionID.String
	d.TopicKey = topicKey.String
	d.PendingTopicKey = pendingTopicKey.String
	d.SupersededBy = supersededBy.String

	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	d.StatusChangedAt, _ = time.Parse(time.RFC3339Nano, statusChangedAt)
	d.ExpiresAt = parseNullTime(expiresAt)
	d.DecidedAt = parseNullTime(decidedAt)
	d.AccessedAt = parseNullTime(accessedAt)

	d.Scope = decodeStrings(scopeJSON)
	d.Tags = decodeStrings(tagsJSON)
	d.Supersedes = decodeStrings(supersedesJSON)
	return &d, nil
}

// --- small helpers ---

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeStrings(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseNullTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, s.String); err != nil {
			return nil
		}
	}
	return &t
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
