// Package report is ADR-0004's reporting surface: the queries of §D5, the
// metrics of §D4, and the honesty controls of §D6.
//
// Everything here is a query over the append-only event log. No number in this
// package is computed from mutable state, none involves a model, and every one
// of them can be drilled down to the raw rows behind it (§D6.2) — that is an
// implementation invariant, not a style preference: a figure that cannot be
// traced to events may not be rendered.
package report

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// DefaultGraceMinutes is §D3's attribution grace. The dominant real flow is
// agent writes code → session ends → human reviews and commits minutes later,
// so a zero-grace window would miss most human-committed agent work; an hour
// covers review-and-commit without claiming next-morning commits. It is
// printed on every report, because a tunable that silently changes the numbers
// is an auditor's finding.
const DefaultGraceMinutes = 60

// windowEndExpr renders a session window's upper bound back into the RFC3339
// UTC shape the event log stores.
//
// §D5.1's query is normative "verbatim" and uses `datetime(w_end, '+60
// minutes')` for the window's upper bound. That is a defect: SQLite's
// `datetime()` emits `YYYY-MM-DD HH:MM:SS` — a space where the stored
// timestamps have a `T`, and no trailing `Z` — while every comparison in the
// query is a lexicographic string BETWEEN against `2026-07-28T14:02:33Z`
// values. `'T' > ' '`, so the upper bound always sorts below the commits it is
// meant to admit and the query returns **zero attributed sessions on every
// store**. Verified against SQLite: the verbatim BETWEEN yields 0 for a commit
// demonstrably inside the window; with strftime it yields 1.
//
// Shipping it verbatim would mean shipping a kill-criterion query (strategy
// §9.3) that reports 0% coverage for a product working perfectly — the same
// shape as Amendment 4's audit item 1, where a migrated backlog would have
// fired falsifier 1 on migration day. The correction is mechanical and
// intent-preserving; it is logged in planning/decisions-log.md for
// ratification rather than made silently.
func windowEndExpr(column string, graceMinutes int) string {
	return `strftime('%Y-%m-%dT%H:%M:%SZ', ` + column + `, '+` +
		strconv.Itoa(graceMinutes) + ` minutes')`
}

// The hot joins read `committed_at`, `verdict` and `backfill` from **columns**
// (ADR-0001 Amendment 5, migration 5), not from JSON. §D7 pre-registered the
// promotion and falsifier 6 fired: at ADR-0001's own projected volume of
// 10–15k events/month the JSON probes took 1.19s for the coverage query, an
// order of magnitude past the ~100ms line. `patch_id` and `reverts_sha` stay in
// JSON deliberately — they are projections on rows the joins have already
// matched, not join keys.
//
// Under ADR-0004 A1.2 this is an implementation update to §D0/§D5.1's
// reference SQL: same answers, faster plan, proven by the fixture tests and by
// a plan assertion that `idx_events_committed` serves the window probe.

// Options bounds a report.
type Options struct {
	From         time.Time
	To           time.Time
	GraceMinutes int
	// RepoRoot enables §D4.4's observer-completeness line, which needs git.
	RepoRoot string
	// DecisionID narrows the drill-down to one decision.
	DecisionID string
}

func (o *Options) applyDefaults() {
	if o.GraceMinutes <= 0 {
		o.GraceMinutes = DefaultGraceMinutes
	}
	if o.To.IsZero() {
		o.To = time.Now().UTC()
	}
	if o.From.IsZero() {
		o.From = o.To.AddDate(0, 0, -30)
	}
	o.From = o.From.UTC()
	o.To = o.To.UTC()
}

func (o Options) fromStr() string { return o.From.Format(time.RFC3339Nano) }
func (o Options) toStr() string   { return o.To.Format(time.RFC3339Nano) }

// Coverage is §D5.1's result: the kill-criterion metric (strategy §9.3).
type Coverage struct {
	AttributedSessions int
	TotalSessions      int
	// ViaPackOnly and ViaRecallOnly split the numerator so Phase 0 ruling 3's
	// pack-vs-recall comparison falls out of the same table (§D5.4).
	ViaPack   int
	ViaRecall int
}

// coverageSQL is §D5.1, with two documented corrections and nothing else:
// the window's upper bound is rendered back into RFC3339 (see rfc3339SQL), and
// the grace is a parameter rather than a literal 60 so §D6.5's method
// disclosure can print the value actually used.
//
// The period filter is deliberately §D5.1's, not §D0's: coverage bounds the
// *session start*, so a session started in the period counts as attributed via
// a commit that lands just after the period ends. §D0 bounds the commit
// instead. The two disagree, §D5.1 is the normative one for coverage, and
// nobody should "fix" one to match the other without moving the kill-criterion
// number on purpose.
func coverageSQL(graceMinutes int) string {
	return `
WITH sessions AS (
    SELECT session_id,
           MIN(CASE WHEN kind = 'session.started' THEN ts END) AS w_start,
           COALESCE(MAX(CASE WHEN kind = 'session.ended' THEN ts END),
                    MAX(ts)) AS w_end
    FROM events
    WHERE session_id IS NOT NULL
      AND (agent IS NULL OR agent <> 'cli')
    GROUP BY session_id
    HAVING w_start >= :from AND w_start < :to
),
packed AS (
    SELECT DISTINCT p.session_id
    FROM events p
    JOIN sessions s ON s.session_id = p.session_id
    JOIN events m ON m.kind = 'diff.scope_match'
                 AND m.decision_id = p.decision_id
                 AND m.backfill = 0
    JOIN events d ON d.kind = 'diff.observed'
                 AND d.commit_sha = m.commit_sha
                 AND d.committed_at IS NOT NULL
                 AND d.committed_at
                       BETWEEN s.w_start AND ` + windowEndExpr("s.w_end", graceMinutes) + `
    WHERE p.kind = 'pack.item'
      AND p.decision_id IS NOT NULL
),
recalled AS (
    -- §D5.4's comparison path. The join order is forced: without it SQLite
    -- drives the scope-match table from the kind index, walking every match
    -- row once per recalled id, which was the quadratic term dominating this
    -- query. CROSS JOIN constrains order only, never the result.
    SELECT DISTINCT r.session_id
    FROM events r
    JOIN sessions s ON s.session_id = r.session_id
    CROSS JOIN json_each(r.payload, '$.ids') ids
    CROSS JOIN events m ON m.kind = 'diff.scope_match'
                       AND m.decision_id = ids.value
                       AND m.backfill = 0
    CROSS JOIN events d ON d.kind = 'diff.observed'
                       AND d.commit_sha = m.commit_sha
                       AND d.committed_at IS NOT NULL
                       AND d.committed_at
                             BETWEEN s.w_start AND ` + windowEndExpr("s.w_end", graceMinutes) + `
    WHERE r.kind = 'recall.served'
)
SELECT (SELECT COUNT(*) FROM sessions),
       (SELECT COUNT(*) FROM (SELECT session_id FROM packed
                              UNION SELECT session_id FROM recalled)),
       (SELECT COUNT(*) FROM packed),
       (SELECT COUNT(*) FROM recalled)`
}

// QueryCoverage runs §D5.1.
func QueryCoverage(db *sql.DB, opts Options) (Coverage, error) {
	opts.applyDefaults()
	var c Coverage
	err := db.QueryRow(coverageSQL(opts.GraceMinutes),
		sql.Named("from", opts.fromStr()), sql.Named("to", opts.toStr()),
	).Scan(&c.TotalSessions, &c.AttributedSessions, &c.ViaPack, &c.ViaRecall)
	if err != nil {
		return c, fmt.Errorf("coverage query: %w", err)
	}
	return c, nil
}

// DecisionRow is §D4's per-decision line.
type DecisionRow struct {
	DecisionID     string
	Title          string
	Status         string
	PackedSessions int
	MatchedChanges int
	Attributed     int
	Conformed      int
	Violated       int
	Undone         int
	Reverted       bool
}

// attributedPairsSQL is §D0's canonical join, with the same two corrections as
// coverageSQL. Backfilled matches are excluded here too: a verdict about a
// commit that predates the store is archaeology, not attribution.
func attributedPairsSQL(graceMinutes int) string {
	return `
WITH windows AS (
    SELECT session_id,
           MIN(CASE WHEN kind = 'session.started' THEN ts END) AS w_start,
           COALESCE(MAX(CASE WHEN kind = 'session.ended' THEN ts END),
                    MAX(ts)) AS w_end
    FROM events
    WHERE session_id IS NOT NULL
      AND (agent IS NULL OR agent <> 'cli')
    GROUP BY session_id
),
attributed_pairs AS (
    SELECT DISTINCT p.decision_id                        AS decision_id,
           d.commit_sha                                  AS commit_sha,
           p.session_id                                  AS session_id,
           m.verdict                                     AS verdict,
           COALESCE(NULLIF(json_extract(d.payload, '$.patch_id'), ''),
                    d.commit_sha)                        AS patch_id
    FROM events p
    JOIN windows w ON w.session_id = p.session_id
    JOIN events d  ON d.kind = 'diff.observed'
                  AND d.committed_at IS NOT NULL
                  AND d.committed_at
                        BETWEEN w.w_start AND ` + windowEndExpr("w.w_end", graceMinutes) + `
    JOIN events m  ON m.kind = 'diff.scope_match'
                  AND m.commit_sha  = d.commit_sha
                  AND m.decision_id = p.decision_id
                  AND m.backfill = 0
    WHERE p.kind = 'pack.item'
      AND p.decision_id IS NOT NULL
      AND d.committed_at >= :from
      AND d.committed_at <  :to
)`
}

// QueryDecisions is §D5.2.
//
// The population is every decision with *any* activity in the period — packed
// into a session, or scope-matched by a commit — not only those with an
// attributed pair. §D4 is explicit that both columns are shown, and that
// "hiding unattributed matches would overstate how much of the repo's activity
// flows through packed sessions". Driving the table from `attributed_pairs`
// did exactly that: a decision packed 41 times whose scope is only ever
// touched by human commits outside any window has `packed 41, matched 12,
// attributed 0` — the most informative row in the report — and it was omitted
// entirely, taking §D6.2's drill-down with it, because a row that is never
// rendered cannot be drilled (F40). The bias ran in the product's favour,
// which is the direction §D6 can least afford.
//
// Counts are over distinct `patch_id`, never distinct SHA (§D0's
// multi-attribution rule): one rebase of a feature branch would otherwise
// double every conform count, which an auditor finds in minutes.
func QueryDecisions(db *sql.DB, opts Options) ([]DecisionRow, error) {
	opts.applyDefaults()
	q := attributedPairsSQL(opts.GraceMinutes) + `,
population AS (
    SELECT DISTINCT p.decision_id AS decision_id
      FROM events p
     WHERE p.kind = 'pack.item' AND p.decision_id IS NOT NULL
       AND (p.agent IS NULL OR p.agent <> 'cli')
       AND p.ts >= :from AND p.ts < :to
    UNION
    SELECT DISTINCT sm.decision_id
      FROM events sm
      JOIN events od ON od.kind = 'diff.observed' AND od.commit_sha = sm.commit_sha
     WHERE sm.kind = 'diff.scope_match' AND sm.decision_id IS NOT NULL
       AND sm.backfill = 0
       AND od.committed_at >= :from
       AND od.committed_at <  :to
)
SELECT pop.decision_id,
       COALESCE(dec.title, '(purged or missing)'),
       COALESCE(dec.status, '?'),
       (SELECT COUNT(DISTINCT pi.session_id) FROM events pi
         WHERE pi.kind = 'pack.item' AND pi.decision_id = pop.decision_id
           AND (pi.agent IS NULL OR pi.agent <> 'cli')
           AND pi.ts >= :from AND pi.ts < :to),
       (SELECT COUNT(DISTINCT COALESCE(NULLIF(json_extract(od.payload, '$.patch_id'), ''), sm.commit_sha))
          FROM events sm
          JOIN events od ON od.kind = 'diff.observed' AND od.commit_sha = sm.commit_sha
         WHERE sm.kind = 'diff.scope_match' AND sm.decision_id = pop.decision_id
           AND sm.backfill = 0
           AND od.committed_at >= :from
           AND od.committed_at <  :to),
       COUNT(DISTINCT ap.patch_id),
       COUNT(DISTINCT CASE WHEN ap.verdict = 'conform' THEN ap.patch_id END),
       COUNT(DISTINCT CASE WHEN ap.verdict = 'violate' THEN ap.patch_id END),
       (SELECT COUNT(DISTINCT v.commit_sha) FROM events v
         WHERE v.kind = 'decision.violated' AND v.decision_id = pop.decision_id
           AND EXISTS (SELECT 1 FROM events r
                        WHERE r.kind = 'revert.detected'
                          AND json_extract(r.payload, '$.reverts_sha') = v.commit_sha)),
       (SELECT COUNT(*) > 0 FROM events dr
         WHERE dr.kind = 'decision.reverted' AND dr.decision_id = pop.decision_id
           AND dr.ts >= :from AND dr.ts < :to)
  FROM population pop
  LEFT JOIN attributed_pairs ap ON ap.decision_id = pop.decision_id
  LEFT JOIN decisions dec ON dec.id = pop.decision_id
 GROUP BY pop.decision_id
 ORDER BY COUNT(DISTINCT CASE WHEN ap.verdict = 'violate' THEN ap.patch_id END) DESC,
          COUNT(DISTINCT ap.patch_id) DESC, pop.decision_id`

	rows, err := db.Query(q, sql.Named("from", opts.fromStr()), sql.Named("to", opts.toStr()))
	if err != nil {
		return nil, fmt.Errorf("per-decision query: %w", err)
	}
	defer rows.Close()

	var out []DecisionRow
	for rows.Next() {
		var r DecisionRow
		if err := rows.Scan(&r.DecisionID, &r.Title, &r.Status, &r.PackedSessions,
			&r.MatchedChanges, &r.Attributed, &r.Conformed, &r.Violated,
			&r.Undone, &r.Reverted); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UndoneCase is one "violations undone" exhibit: a violating commit that was
// itself later reverted. §D4 keeps this an absolute count with the cases
// listed, never a rate — each case is checkable with two `git show`s, and a
// rate over single digits is noise dressed as statistics.
type UndoneCase struct {
	DecisionID     string
	Title          string
	ViolatingSHA   string
	RevertingSHA   string
	RevertedAtTime string
}

// QueryUndone lists the exhibits behind §D4's "violations undone".
func QueryUndone(db *sql.DB, opts Options) ([]UndoneCase, error) {
	opts.applyDefaults()
	rows, err := db.Query(`
		SELECT v.decision_id, COALESCE(d.title, '(purged or missing)'),
		       v.commit_sha, r.commit_sha, r.ts
		  FROM events v
		  JOIN events r ON r.kind = 'revert.detected'
		               AND json_extract(r.payload, '$.reverts_sha') = v.commit_sha
		  LEFT JOIN decisions d ON d.id = v.decision_id
		 WHERE v.kind = 'decision.violated'
		   AND r.ts >= :from AND r.ts < :to
		 ORDER BY r.ts DESC`,
		sql.Named("from", opts.fromStr()), sql.Named("to", opts.toStr()))
	if err != nil {
		return nil, fmt.Errorf("violations-undone query: %w", err)
	}
	defer rows.Close()
	var out []UndoneCase
	for rows.Next() {
		var c UndoneCase
		if err := rows.Scan(&c.DecisionID, &c.Title, &c.ViolatingSHA,
			&c.RevertingSHA, &c.RevertedAtTime); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ObservedInPeriod returns the SHAs of commits observed in the period, for
// §D4.4's completeness intersection.
func ObservedInPeriod(db *sql.DB, opts Options) (map[string]bool, error) {
	opts.applyDefaults()
	rows, err := db.Query(`
		SELECT commit_sha FROM events
		 WHERE kind = 'diff.observed'
		   AND committed_at >= :from
		   AND committed_at <  :to`,
		sql.Named("from", opts.fromStr()), sql.Named("to", opts.toStr()))
	if err != nil {
		return nil, fmt.Errorf("observed-commits query: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out[sha] = true
	}
	return out, rows.Err()
}

// RawEvent is one row of §D5.3's drill-down.
type RawEvent struct {
	Seq        int64
	ID         string
	Timestamp  string
	Kind       string
	Actor      string
	SessionID  string
	CommitSHA  string
	DecisionID string
	Payload    string
}

// QueryRaw is §D5.3: the raw rows behind a figure, verbatim.
//
// §D6.2 makes this an invariant rather than a feature — if a number cannot be
// traced to event rows, it may not be rendered.
func QueryRaw(db *sql.DB, decisionID string, shas []string) ([]RawEvent, error) {
	q := `SELECT seq, id, ts, kind, actor,
	             COALESCE(session_id, ''), COALESCE(commit_sha, ''), COALESCE(decision_id, ''),
	             payload
	        FROM events WHERE 1=0`
	var args []any
	if decisionID != "" {
		q += ` OR decision_id = ?`
		args = append(args, decisionID)
	}
	for _, sha := range shas {
		q += ` OR commit_sha = ?`
		args = append(args, sha)
	}
	q += ` ORDER BY seq`

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("drill-down query: %w", err)
	}
	defer rows.Close()
	var out []RawEvent
	for rows.Next() {
		var e RawEvent
		if err := rows.Scan(&e.Seq, &e.ID, &e.Timestamp, &e.Kind, &e.Actor,
			&e.SessionID, &e.CommitSHA, &e.DecisionID, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CommitsForDecision returns the commit SHAs a decision was scope-matched
// against, so the drill-down can pull their events too.
func CommitsForDecision(db *sql.DB, decisionID string) ([]string, error) {
	rows, err := db.Query(`
		SELECT DISTINCT commit_sha FROM events
		 WHERE kind = 'diff.scope_match' AND decision_id = ? AND commit_sha IS NOT NULL`,
		decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out = append(out, sha)
	}
	return out, rows.Err()
}

// ScopedDecisionCount reports how many binding decisions carry a scope — the
// cold-start signal behind §D6.6's empty state ("attribution requires scoped,
// accepted decisions").
func ScopedDecisionCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM decisions
		 WHERE status IN ('active','violated') AND json_array_length(scope) > 0`).Scan(&n)
	return n, err
}

// ObserverEpoch reads the observation epoch (§D1.3) from the event log.
//
// The report needs it for §D4.4's denominator: commits older than the epoch
// are ones the observer was never allowed to see, and counting them measures
// the repository's age rather than the observer's health.
func ObserverEpoch(db *sql.DB) (*time.Time, error) {
	var raw string
	err := db.QueryRow(`
		SELECT json_extract(payload, '$.epoch') FROM events
		 WHERE kind = 'observer.enabled' ORDER BY seq LIMIT 1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading observation epoch: %w", err)
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, nil
	}
	return &t, nil
}
