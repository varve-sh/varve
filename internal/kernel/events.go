package kernel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// EventInput is one row for the append-only log (ADR-0001 D7).
type EventInput struct {
	ProjectID  string
	Kind       types.EventKind
	Actor      types.Actor
	DecisionID string
	SessionID  string
	Agent      string
	Model      string
	CommitSHA  string
	Payload    map[string]any
}

func (in *EventInput) validate() error {
	if in.ProjectID == "" {
		return &types.ValidationError{Field: "project_id", Message: "must not be empty"}
	}
	if !types.IsKnownEventKind(in.Kind) {
		return fmt.Errorf("%w: %q — the ADR-0001 §D7 catalogue is the contract; "+
			"register the kind in internal/types/event_kinds.go before emitting it",
			types.ErrUnknownEventKind, in.Kind)
	}
	switch in.Actor {
	case types.ActorHuman, types.ActorAgent, types.ActorSystem:
	default:
		return &types.ValidationError{Field: "actor", Message: "must be human, agent or system"}
	}
	return nil
}

const insertEventSQL = `
INSERT INTO events
    (id, project_id, ts, kind, actor, decision_id, session_id, agent, model, commit_sha, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// appendEvent writes one event inside the caller's transaction. Event writes
// are never done on their own connection: D7 requires the event and the state
// change it describes to commit together or not at all.
func appendEvent(tx *sql.Tx, in EventInput) (string, error) {
	id, args, err := eventArgs(in)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(insertEventSQL, args...); err != nil {
		return "", fmt.Errorf("appending %s event: %w", in.Kind, err)
	}
	return id, nil
}

// appendEventOnce writes an event whose kind carries a partial unique index
// (decision.expired, diff.observed, diff.scope_match) and reports whether the
// row was new. A duplicate is not an error — it is the idempotency the index
// exists to provide.
//
// Two consequences are deliberate, both dispositioned in ADR-0001 Amendment 1's
// same-class audit, and both must be respected by the components that land on
// top of this (the packer, the observer, the linter):
//
//   - decision.expired marks *first* expiry only. `expires_at` may be extended
//     and the decision may expire again; no second event is possible. Read
//     current expiry from the predicate, never from the event.
//   - diff.scope_match verdicts freeze at first observation. A commit scanned
//     as `conform` can become a `violate` under D6 if the commit it reverts is
//     attached as evidence later, but the (decision_id, commit_sha) index keeps
//     the first verdict. That is the intended trade: rescans stay idempotent
//     and verdicts stay stable, because a verdict that flips retroactively
//     costs more trust than one that is occasionally stale. The linter may
//     flag the discrepancy; it must never rewrite the event.
func appendEventOnce(tx *sql.Tx, in EventInput) (id string, inserted bool, err error) {
	id, args, err := eventArgs(in)
	if err != nil {
		return "", false, err
	}
	res, err := tx.Exec(insertEventSQL+" ON CONFLICT DO NOTHING", args...)
	if err != nil {
		return "", false, fmt.Errorf("appending %s event: %w", in.Kind, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", false, err
	}
	return id, n > 0, nil
}

func eventArgs(in EventInput) (string, []any, error) {
	if err := in.validate(); err != nil {
		return "", nil, err
	}
	payload := in.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("marshalling %s payload: %w", in.Kind, err)
	}
	id := util.GenerateID()
	return id, []any{
		id, in.ProjectID, time.Now().UTC().Format(time.RFC3339Nano),
		string(in.Kind), string(in.Actor),
		nullableString(in.DecisionID), nullableString(in.SessionID),
		nullableString(in.Agent), nullableString(in.Model), nullableString(in.CommitSHA),
		string(blob),
	}, nil
}

// EventFilter narrows an event query. Zero values mean "no filter".
type EventFilter struct {
	DecisionID string
	SessionID  string
	CommitSHA  string
	Kind       types.EventKind
	Limit      int
}

// Events returns matching events in total order (by seq).
func (s *DecisionStore) Events(f EventFilter) ([]types.Event, error) {
	q := `SELECT seq, id, project_id, ts, kind, actor, decision_id, session_id,
	             agent, model, commit_sha, payload
	        FROM events WHERE 1=1`
	var args []any
	if f.DecisionID != "" {
		q += " AND decision_id = ?"
		args = append(args, f.DecisionID)
	}
	if f.SessionID != "" {
		q += " AND session_id = ?"
		args = append(args, f.SessionID)
	}
	if f.CommitSHA != "" {
		q += " AND commit_sha = ?"
		args = append(args, f.CommitSHA)
	}
	if f.Kind != "" {
		q += " AND kind = ?"
		args = append(args, string(f.Kind))
	}
	q += " ORDER BY seq"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying events: %w", err)
	}
	defer rows.Close()

	var out []types.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func scanEvent(s scanner) (*types.Event, error) {
	var e types.Event
	var (
		ts, kind, actor                             string
		decisionID, sessionID, agent, model, commit sql.NullString
		payload                                     string
	)
	if err := s.Scan(&e.Seq, &e.ID, &e.ProjectID, &ts, &kind, &actor,
		&decisionID, &sessionID, &agent, &model, &commit, &payload); err != nil {
		return nil, err
	}
	e.Kind = types.EventKind(kind)
	e.Actor = types.Actor(actor)
	e.DecisionID = decisionID.String
	e.SessionID = sessionID.String
	e.Agent = agent.String
	e.Model = model.String
	e.CommitSHA = commit.String
	e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
		e.Payload = map[string]any{}
	}
	return &e, nil
}
