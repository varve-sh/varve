package kernel

import (
	"database/sql"
	"sync"

	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// session is the kernel's view of the current attribution session
// (ADR-0001 §D7's session.started/session.ended, ADR-0004 §D3).
//
// Two shapes exist and both are specified:
//
//   - One MCP connection is one session. `session.started` is emitted when the
//     server begins serving and `session.ended` at close. `agent` is the
//     constant "mcp" and `model` is empty: the events are written when the
//     connection opens, before any client handshake info is available at this
//     layer, so neither is read from the client today.
//   - A CLI invocation gets a synthetic per-invocation session carrying
//     `agent = 'cli'`, which every coverage denominator excludes (§D3
//     amendment 3). It is registered lazily — the events are written the first
//     time the invocation emits something session-scoped — so `varve list`
//     does not write two rows to say nothing happened.
//
// Without these rows ADR-0002 §P11's window join has nothing to join to: the
// chain is pack.item/recall.served → session window → diff.scope_match, and a
// session with no start row contributes nothing however well instrumented the
// read path is.
type session struct {
	mu      sync.Mutex
	id      string
	agent   string
	model   string
	started bool
	saves   int
	recalls int
	packs   int
}

// SessionAgentCLI marks synthetic per-invocation CLI sessions. ADR-0004 §D3
// excludes them from every coverage and attribution denominator: the kill
// criterion is about agent sessions, and a hundred one-second `list`
// invocations would sandbag coverage in the denominator and inflate session
// counts in the numerator.
const SessionAgentCLI = "cli"

// SetSession registers a session without announcing it. The session.started
// event is written on the first session-scoped emission (see ensureSession).
func (k *MemoryKernel) SetSession(id, agent, model string) {
	k.session.mu.Lock()
	defer k.session.mu.Unlock()
	k.session.id, k.session.agent, k.session.model = id, agent, model
	k.session.started = false
	k.session.saves, k.session.recalls, k.session.packs = 0, 0, 0
	// The store's copy is set by governanceStamp, once the session has been
	// announced; registering a new session must not leave the previous one's
	// provenance behind.
	k.decisions.SetSessionContext("", "", "")
}

// BeginSession registers a session and emits session.started immediately. This
// is the MCP shape: the connection opening *is* the session's start, whether or
// not a tool is ever called.
func (k *MemoryKernel) BeginSession(agent, model string) string {
	id := util.GenerateID()
	k.SetSession(id, agent, model)
	k.ensureSession()
	return id
}

// governanceStamp announces the session (if it has not been announced) and
// hands its provenance to the decision store, so the lifecycle events a human
// governance action writes — decision.accepted, decision.rejected,
// decision.revised, evidence.added — carry a session_id instead of NULL. Those
// are exactly the rows a Tier-3 audit trail is sold on (F25).
//
// It runs *before* the governance transaction opens: announcing inside one
// would need a second write transaction against a single-writer database.
func (k *MemoryKernel) governanceStamp() {
	id, agent, model := k.sessionStamp()
	k.decisions.SetSessionContext(id, agent, model)
}

// AcceptDecision, RejectDecision and AddDecisionEvidence are the session-aware
// entry points for human governance actions. They are what the CLI and TUI
// call; DecisionStore's methods stay available for callers that carry their
// own provenance (the MCP path) or none (the migration).
func (k *MemoryKernel) AcceptDecision(id string, opts AcceptOptions) (*types.Decision, error) {
	k.governanceStamp()
	return k.decisions.Accept(id, opts)
}

// RejectDecision declines a proposal, recording the reason (D2). It is the
// CLI/TUI entry point, so the actor is human.
func (k *MemoryKernel) RejectDecision(id, reason string) error {
	k.governanceStamp()
	return k.decisions.Reject(id, reason, types.ActorHuman)
}

// AddDecisionEvidence attaches an evidence row (D4).
func (k *MemoryKernel) AddDecisionEvidence(id string, in EvidenceInput) (*types.Evidence, error) {
	k.governanceStamp()
	return k.decisions.AddEvidence(id, in)
}

// SessionID returns the current session id, or "" when none is registered.
func (k *MemoryKernel) SessionID() string {
	k.session.mu.Lock()
	defer k.session.mu.Unlock()
	return k.session.id
}

// sessionStamp returns the id/agent/model to put on a session-scoped event,
// announcing the session first if it has not been announced yet.
func (k *MemoryKernel) sessionStamp() (id, agent, model string) {
	k.ensureSession()
	k.session.mu.Lock()
	defer k.session.mu.Unlock()
	return k.session.id, k.session.agent, k.session.model
}

// ensureSession emits session.started exactly once per registered session.
// Emission never fails the caller: telemetry that can break a read path is a
// worse bug than missing telemetry.
func (k *MemoryKernel) ensureSession() {
	k.session.mu.Lock()
	if k.session.id == "" || k.session.started {
		k.session.mu.Unlock()
		return
	}
	k.session.started = true
	id, agent, model := k.session.id, k.session.agent, k.session.model
	k.session.mu.Unlock()

	_ = k.decisions.withTx(func(tx *sql.Tx) error {
		_, err := appendEvent(tx, EventInput{
			ProjectID: k.projectID,
			Kind:      types.EventSessionStarted,
			Actor:     types.ActorSystem,
			SessionID: id,
			Agent:     agent,
			Model:     model,
			Payload:   map[string]any{},
		})
		return err
	})
}

// countSessionSave and countSessionRecall accumulate the session.ended payload.
func (k *MemoryKernel) countSessionSave() {
	k.session.mu.Lock()
	defer k.session.mu.Unlock()
	k.session.saves++
}

func (k *MemoryKernel) countSessionRecall() {
	k.session.mu.Lock()
	defer k.session.mu.Unlock()
	k.session.recalls++
}

// countSessionPack feeds session.ended's `packs`. Without it the payload
// reports 0 forever, and a report nobody can distinguish from "the packer was
// never used" is worse than no number.
func (k *MemoryKernel) countSessionPack() {
	k.session.mu.Lock()
	defer k.session.mu.Unlock()
	k.session.packs++
}

// EndSession emits session.ended for an announced session and clears it.
//
// A session that was registered but never announced (a CLI invocation that
// emitted nothing) writes no rows at all. A crash writes no end row either, and
// none is ever repaired afterwards: ADR-0004 §D3 synthesizes the window end at
// query time as MAX(ts), because a repair row would be a fabricated
// observation.
func (k *MemoryKernel) EndSession() {
	k.session.mu.Lock()
	if !k.session.started || k.session.id == "" {
		k.session.id, k.session.started = "", false
		k.session.mu.Unlock()
		return
	}
	id, agent, model := k.session.id, k.session.agent, k.session.model
	payload := map[string]any{
		"saves":   k.session.saves,
		"recalls": k.session.recalls,
		"packs":   k.session.packs,
	}
	k.session.id, k.session.started = "", false
	k.session.mu.Unlock()

	// The decision store holds its own copy of the provenance (F25). Clearing
	// the kernel's and not the store's would leave later events stamped with a
	// session that has already ended — a window the packer would walk straight
	// into.
	k.decisions.SetSessionContext("", "", "")

	_ = k.decisions.withTx(func(tx *sql.Tx) error {
		_, err := appendEvent(tx, EventInput{
			ProjectID: k.projectID,
			Kind:      types.EventSessionEnded,
			Actor:     types.ActorSystem,
			SessionID: id,
			Agent:     agent,
			Model:     model,
			Payload:   payload,
		})
		return err
	})
}
