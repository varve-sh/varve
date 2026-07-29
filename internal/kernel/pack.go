package kernel

import (
	"database/sql"
	"time"

	"github.com/memtrace-dev/memtrace/internal/pack"
	"github.com/memtrace-dev/memtrace/internal/retrieval"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// packSource adapts the kernel's stores to ADR-0002's packer.
//
// Deliberately built on DecisionStore and NoteStore, never on MemoryStore: the
// v1-shaped projection carries no `expires_at`, so §P2's "not expired" is
// unreachable through it, and its live-status filter includes `proposed`, so
// reusing it would pull quarantined text into the pack as content — §P2's
// headline prohibition. The one thing borrowed from the v1 facade is the FTS
// candidate query, which is a *candidate source*, not an eligibility oracle:
// everything it returns is re-checked against the real columns.
type packSource struct {
	k *MemoryKernel
}

func (s packSource) PackableDecisions(projectID string) ([]types.Decision, error) {
	return s.k.decisions.ListDecisions(DecisionFilter{
		ProjectID: projectID,
		Statuses:  []types.DecisionStatus{types.StatusActive, types.StatusViolated},
	})
}

func (s packSource) ProposedDecisions(projectID string) ([]types.Decision, error) {
	return s.k.decisions.ListDecisions(DecisionFilter{
		ProjectID: projectID,
		Statuses:  []types.DecisionStatus{types.StatusProposed},
	})
}

func (s packSource) ActiveNotes(projectID string) ([]types.Note, error) {
	return s.k.notes.List(NoteFilter{ProjectID: projectID, Status: types.MemoryStatusActive})
}

func (s packSource) TextPool(query, projectID string, limit int) ([]types.FTSResult, error) {
	return s.k.store.SearchFTS(query, projectID, limit)
}

func (s packSource) Embeddings(projectID string) ([]retrieval.EmbeddingRow, error) {
	return s.k.store.FindEmbeddings(projectID)
}

// embedTimeout is §P13's cap on the one optional model-adjacent step. After it
// the pack proceeds BM25-only and says so, because a slow local embedder must
// degrade the pack, never delay the session.
const embedTimeout = 500 * time.Millisecond

func (s packSource) EmbedQuery(task string) ([]float64, bool) {
	if s.k.embedder == nil || task == "" {
		return nil, false
	}
	type result struct {
		vec []float64
		err error
	}
	ch := make(chan result, 1)
	go func() {
		vec, err := s.k.embedder.Embed(task)
		ch <- result{vec, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, false
		}
		return r.vec, false
	case <-time.After(embedTimeout):
		return nil, true
	}
}

func (s packSource) UnresolvedViolations(projectID string) (map[string]int, error) {
	return s.k.decisions.UnresolvedViolationsByProject(projectID)
}

func (s packSource) Evidence(projectID string) (map[string][]types.Evidence, error) {
	return s.k.decisions.EvidenceByProject(projectID)
}

// Pack builds a context pack and records it (ADR-0002).
//
// The build itself is pure — it reads and returns bytes. Everything it
// observed that has to be recorded is written here, in **one transaction**, per
// ADR-0001 §D7's rule that a fact and the event describing it commit together
// or not at all. An errored call records nothing (§P1).
func (k *MemoryKernel) Pack(req pack.Request) (*pack.Result, error) {
	res, err := pack.Build(packSource{k}, k.projectID, req)
	if err != nil {
		return nil, err
	}

	// The session has to be announced before the pack transaction opens: the
	// announcement is its own short write, and a single-writer database cannot
	// nest them.
	sessionID, agent, model := k.sessionStamp()
	k.countSessionPack()

	if err := k.decisions.recordPack(k.projectID, sessionID, agent, model, req, res); err != nil {
		return nil, err
	}

	// Expiry observations are idempotent and independent of the pack's own
	// record: `decision.expired` marks first expiry only, and every consumer
	// reads current expiry from the predicate (ADR-0001 Amendment 1, audit
	// item 1). Failure here must not fail a pack that was already served.
	for _, id := range res.ExpiredObserved {
		_, _ = k.decisions.MarkExpired(id)
	}
	return res, nil
}

// recordPack writes §P10's events: one pack.served, and one pack.item per
// **served** item.
//
// Per-item emission is a hard requirement, including under budget pressure and
// truncation. ADR-0004's entire chain is the join "decision D was in session
// S's context" ⇔ a pack.item row exists; batching these into a summary or
// eliding them for stubbed items would break attribution silently, which is
// the worst way for it to break. Omitted and deduped items get no pack.item —
// they were not in context, and that is the fact attribution cares about.
func (s *DecisionStore) recordPack(
	projectID, sessionID, agent, model string, req pack.Request, res *pack.Result,
) error {
	return s.withTx(func(tx *sql.Tx) error {
		payload := map[string]any{
			"budget_tokens":    res.BudgetTokens,
			"used_tokens":      res.UsedTokens,
			"item_count":       res.ItemCount,
			"files":            nonNilStrings(req.FilePaths),
			"truncated":        res.Truncated,
			"omitted_count":    res.OmittedCount,
			"deduped_count":    res.DedupedCount,
			"stub_count":       res.StubCount,
			"proposed_matched": res.ProposedMatched,
			"estimator":        pack.EstimatorVersion,
			"scored_at":        res.ScoredAt.UTC().Format(time.RFC3339),
		}
		if req.Task != "" {
			// §D7 names this key `query`; the tool argument is `task` because
			// that is what it is. The mapping is fixed by §P1.
			payload["query"] = req.Task
		}
		if res.EmbedderTimedOut {
			// Present only on timeout, so a degraded pack is distinguishable
			// from a BM25-only configuration (§P13).
			payload["embedder"] = "timeout"
		}
		// `arm` is reserved for ADR-0004 §D8's deferred opt-in A/B and is never
		// emitted in the 90 days.
		if _, err := s.emit(tx, EventInput{
			ProjectID: projectID,
			Kind:      types.EventPackServed,
			Actor:     types.ActorSystem,
			SessionID: sessionID,
			Agent:     agent,
			Model:     model,
			Payload:   payload,
		}); err != nil {
			return err
		}

		for _, item := range res.Served {
			itemPayload := map[string]any{
				"tokens": item.Tokens,
				"rank":   item.Rank,
				"score":  item.Score,
				"class":  string(item.Class),
				"form":   string(item.Form),
			}
			// `agent`/`model` are additive here: §D7 lists only `session_id`
			// and `decision_id` for this kind, and these mirror the session
			// stamp every kernel event carries since F25. They are true when
			// set and **empty for any caller without a session**, so ADR-0004
			// must read them as convenience, never join on them — the session
			// is the join key, and `session.started` is where §D7 puts the
			// authoritative agent/model for a session.
			in := EventInput{
				ProjectID: projectID,
				Kind:      types.EventPackItem,
				Actor:     types.ActorSystem,
				SessionID: sessionID,
				Agent:     agent,
				Model:     model,
				Payload:   itemPayload,
			}
			if item.Class == pack.ClassDecision {
				in.DecisionID = item.ID
				itemPayload["note_id"] = nil
			} else {
				// Notes have no FK on `events`; the id rides in the payload,
				// which is what §D7's shape says.
				itemPayload["note_id"] = item.ID
			}
			if _, err := s.emit(tx, in); err != nil {
				return err
			}
		}
		return nil
	})
}
