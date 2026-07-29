package report

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/memtrace-dev/memtrace/internal/observer"
)

// Report is one rendering-ready attribution report (§D6).
type Report struct {
	Repo         string        `json:"repo"`
	From         time.Time     `json:"from"`
	To           time.Time     `json:"to"`
	GraceMinutes int           `json:"grace_minutes"`
	Coverage     Coverage      `json:"coverage"`
	Decisions    []DecisionRow `json:"decisions"`
	Undone       []UndoneCase  `json:"violations_undone"`
	Completeness Completeness  `json:"observer_completeness"`
	// ScopedDecisions is the cold-start signal: with none, the chain cannot
	// start regardless of how much the agent commits.
	ScopedDecisions int `json:"scoped_decisions"`
}

// Completeness is §D4.4 — the honesty metric about our own instrumentation.
//
// Two bounds are deliberate.
//
// The **intersection**: "distinct observed commits" over "default-branch
// reachable commits" can exceed 100%, because the observer sees feature
// branches the denominator excludes, and a completeness metric that reports
// 112% makes itself the auditor's first finding.
//
// The **epoch**: commits made before `varve init` are ones §D1.3 *forbids*
// observing without an explicit backfill, so counting them measures how old
// the repository is, not whether the observer works. Unbounded, a correct
// observer on a repo with any history reported "1 of 13 commits observed (8%)"
// on day one — on the one self-critical number on the artifact a team lead
// forwards, and a number that never improved except by backfilling (F39).
// §D4.4's stated purpose is "if the observer *missed* commits, the report says
// so on its face"; these were not missed.
type Completeness struct {
	Observed  int  `json:"observed"`
	Reachable int  `json:"reachable"`
	Available bool `json:"available"`
	// Since is the effective start of the denominator: the reporting period's
	// start, or the observation epoch if that is later. Rendered, so a reader
	// can see what the number is about.
	Since time.Time `json:"since"`
	// EpochBounded is true when the epoch moved the window's start — i.e. the
	// repository has history the observer was never allowed to see.
	EpochBounded bool `json:"epoch_bounded"`
	// PreEpoch counts the commits excluded by that bound, so the report can
	// name them instead of hiding them.
	PreEpoch int `json:"pre_epoch_commits"`
}

// Build assembles everything §D6 renders.
func Build(db *sql.DB, opts Options) (*Report, error) {
	opts.applyDefaults()

	cov, err := QueryCoverage(db, opts)
	if err != nil {
		return nil, err
	}
	decisions, err := QueryDecisions(db, opts)
	if err != nil {
		return nil, err
	}
	undone, err := QueryUndone(db, opts)
	if err != nil {
		return nil, err
	}
	scoped, err := ScopedDecisionCount(db)
	if err != nil {
		return nil, err
	}

	r := &Report{
		Repo: opts.RepoRoot, From: opts.From, To: opts.To,
		GraceMinutes: opts.GraceMinutes, Coverage: cov,
		Decisions: decisions, Undone: undone, ScopedDecisions: scoped,
	}
	if opts.DecisionID != "" {
		filtered := r.Decisions[:0]
		for _, d := range r.Decisions {
			if d.DecisionID == opts.DecisionID {
				filtered = append(filtered, d)
			}
		}
		r.Decisions = filtered
	}

	r.Completeness, err = buildCompleteness(db, opts)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func buildCompleteness(db *sql.DB, opts Options) (Completeness, error) {
	var c Completeness
	if opts.RepoRoot == "" || !observer.IsRepo(opts.RepoRoot) {
		return c, nil // no repo: the line is omitted rather than guessed
	}
	ref := observer.DefaultBranch(opts.RepoRoot)
	if ref == "" {
		ref = "HEAD"
	}

	// §D1.3: the observer is not permitted to observe commits older than the
	// epoch, so they do not belong in a metric about whether it observed what
	// it could.
	from := opts.From
	epoch, err := ObserverEpoch(db)
	if err != nil {
		return c, err
	}
	if epoch != nil && epoch.After(from) {
		if pre, err := observer.ReachableCommits(opts.RepoRoot, ref, from, *epoch); err == nil {
			c.PreEpoch = len(pre)
		}
		from = *epoch
		c.EpochBounded = true
	}

	reachable, err := observer.ReachableCommits(opts.RepoRoot, ref, from, opts.To)
	if err != nil {
		return c, nil // an unborn branch is not a report failure
	}
	observed, err := ObservedInPeriod(db, opts)
	if err != nil {
		return c, err
	}
	c.Available = true
	c.Since = from
	c.Reachable = len(reachable)
	for _, sha := range reachable {
		if observed[sha] {
			c.Observed++
		}
	}
	return c, nil
}

// --- §D6's honesty controls ---

// minDenominatorForPercent is §D6.1: a rate whose denominator is under five is
// rendered as the raw fraction, never as a percentage. Three of four is 75%
// and it is also three of four; the percentage is the part that overstates.
const minDenominatorForPercent = 5

// rate renders a fraction under §D6.1: numerator, denominator, and [n=…]
// always; a percentage only when the denominator can carry one.
func rate(num, den int, unit string) string {
	if den == 0 {
		return fmt.Sprintf("0 of 0 %s", unit)
	}
	if den < minDenominatorForPercent {
		return fmt.Sprintf("%d of %d %s", num, den, unit)
	}
	return fmt.Sprintf("%d of %d %s (%.0f%%)", num, den, unit, 100*float64(num)/float64(den))
}

// BannedVocabulary is §D6.4, as a testable list rather than a style guide.
//
// The report describes recorded chains. It does not claim causation, it does
// not price anything, and it never says a decision "saved" or "prevented"
// anything — those words assert a counterfactual this instrumentation
// explicitly does not establish (Known limitation 1). The list is exported so
// the test that sweeps rendered output can be exhaustive.
var BannedVocabulary = []string{
	"caused", "saved", "prevented", "roi", "hours saved",
	"$", "€", "£", "dollars", "cost savings", "productivity gain",
}

// limitationsFooter is §D6.3: printed on the report itself, not in
// documentation. Underselling is company policy (strategy §10.7).
const limitationsFooter = `attribution shows the recorded chain on individual cases; it does not
establish what would have happened without the decision. conform = no
deterministic violation signal detected, not verified compliance. reverts are
detected from git trailers only, so violations are under-reported and never
fabricated. a rebase gives a change a new commit time, so a rebased change can
be attributed to the rebasing session; counts dedupe by patch-id, attribution
does not.`

// Text renders §D6's normative layout.
func (r *Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "varve attribution report — %s — %s..%s\n",
		repoLabel(r.Repo), r.From.Format("2006-01-02"), r.To.Format("2006-01-02"))
	// §D6.5: method disclosure. Numbers whose method is hidden are numbers an
	// auditor rejects.
	fmt.Fprintf(&b, "grace window: %dm · revert detection: git trailer only · backfill: excluded\n\n",
		r.GraceMinutes)

	// §D6.6: no silent empty states.
	if r.Coverage.TotalSessions == 0 {
		b.WriteString("coverage      no agent sessions yet\n")
	} else {
		fmt.Fprintf(&b, "coverage      %s produced an attributable\n",
			rate(r.Coverage.AttributedSessions, r.Coverage.TotalSessions, "agent sessions"))
		fmt.Fprintf(&b, "              decision→diff event          [n=%d sessions]\n",
			r.Coverage.TotalSessions)
		fmt.Fprintf(&b, "              via pack: %d · via recall: %d\n",
			r.Coverage.ViaPack, r.Coverage.ViaRecall)
	}

	conformed, violated, attributed := r.totals()
	switch {
	case attributed == 0 && r.ScopedDecisions == 0:
		b.WriteString("follow-through  no scoped decisions — attribution requires scoped,\n" +
			"              accepted decisions (varve decision accept, with a scope)\n")
	case attributed == 0:
		b.WriteString("follow-through  no attributed changes yet\n")
	default:
		fmt.Fprintf(&b, "follow-through  %s\n",
			rate(conformed, conformed+violated, "attributed changes conformed"))
		fmt.Fprintf(&b, "              [n=%d distinct changes across %d decisions]\n",
			attributed, len(r.Decisions))
	}

	if len(r.Undone) == 0 {
		b.WriteString("violations undone  0\n")
	} else {
		fmt.Fprintf(&b, "violations undone  %d — the violating commits were later reverted\n",
			len(r.Undone))
		for _, u := range r.Undone {
			fmt.Fprintf(&b, "              %s  %s reverted by %s\n",
				shortID(u.DecisionID), shortSHA(u.ViolatingSHA), shortSHA(u.RevertingSHA))
		}
	}

	if r.Completeness.Available {
		fmt.Fprintf(&b, "observer      %s\n",
			rate(r.Completeness.Observed, r.Completeness.Reachable,
				"default-branch commits observed"))
		if r.Completeness.EpochBounded {
			fmt.Fprintf(&b, "              since install (%s); %d earlier commits are outside\n"+
				"              the observer's remit — `memtrace scan --backfill` covers them\n",
				r.Completeness.Since.Format("2006-01-02"), r.Completeness.PreEpoch)
		}
	} else {
		b.WriteString("observer      not measurable here (no git repository)\n")
	}

	if len(r.Decisions) > 0 {
		b.WriteString("\nper decision:\n")
		fmt.Fprintf(&b, "  %-14s %-34s %7s %8s %5s %8s %8s %7s\n",
			"ID", "title", "packed", "matched", "attr", "conform", "violate", "undone")
		for _, d := range r.Decisions {
			fmt.Fprintf(&b, "  %-14s %-34s %7d %8d %5d %8d %8d %7d\n",
				shortID(d.DecisionID), truncate(d.Title, 34), d.PackedSessions,
				d.MatchedChanges, d.Attributed, d.Conformed, d.Violated, d.Undone)
		}
	}

	b.WriteString("\nevery number drills to raw events: memtrace report --decision <id> --raw\n")
	b.WriteString(limitationsFooter)
	b.WriteString("\n")
	return b.String()
}

// Markdown is the forwardable artifact (§D6: "an ROI report a team lead can
// forward" — the artifact, not the phrase, which §D6.4 bans).
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# varve attribution report — %s\n\n", repoLabel(r.Repo))
	fmt.Fprintf(&b, "`%s..%s` · grace window %dm · revert detection: git trailer only · backfill excluded\n\n",
		r.From.Format("2006-01-02"), r.To.Format("2006-01-02"), r.GraceMinutes)

	conformed, violated, attributed := r.totals()
	b.WriteString("| metric | value | n |\n|---|---|---|\n")
	if r.Coverage.TotalSessions == 0 {
		b.WriteString("| coverage | no agent sessions yet | 0 |\n")
	} else {
		fmt.Fprintf(&b, "| coverage | %s | %d sessions |\n",
			rate(r.Coverage.AttributedSessions, r.Coverage.TotalSessions, "agent sessions"),
			r.Coverage.TotalSessions)
	}
	if attributed > 0 {
		fmt.Fprintf(&b, "| follow-through | %s | %d changes |\n",
			rate(conformed, conformed+violated, "attributed changes conformed"), attributed)
	} else {
		b.WriteString("| follow-through | no attributed changes yet | 0 |\n")
	}
	fmt.Fprintf(&b, "| violations undone | %d | exhibits listed below |\n", len(r.Undone))
	if r.Completeness.Available {
		note := ""
		if r.Completeness.EpochBounded {
			note = fmt.Sprintf(" (since install %s; %d earlier commits outside the observer's remit)",
				r.Completeness.Since.Format("2006-01-02"), r.Completeness.PreEpoch)
		}
		fmt.Fprintf(&b, "| observer completeness | %s%s | %d commits |\n",
			rate(r.Completeness.Observed, r.Completeness.Reachable, "commits observed"),
			note, r.Completeness.Reachable)
	}

	if len(r.Decisions) > 0 {
		b.WriteString("\n| decision | title | packed | matched | attributed | conform | violate | undone |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|\n")
		for _, d := range r.Decisions {
			fmt.Fprintf(&b, "| `%s` | %s | %d | %d | %d | %d | %d | %d |\n",
				shortID(d.DecisionID), d.Title, d.PackedSessions, d.MatchedChanges,
				d.Attributed, d.Conformed, d.Violated, d.Undone)
		}
	}
	if len(r.Undone) > 0 {
		b.WriteString("\n**Violations undone**\n\n")
		for _, u := range r.Undone {
			fmt.Fprintf(&b, "- `%s` — %s: `%s` reverted by `%s`\n",
				shortID(u.DecisionID), u.Title, shortSHA(u.ViolatingSHA), shortSHA(u.RevertingSHA))
		}
	}
	b.WriteString("\n---\n\n")
	b.WriteString(limitationsFooter)
	b.WriteString("\n")
	return b.String()
}

// JSON is the machine-readable form. It carries the same numbers and the same
// limitations text: a consumer that reads only this must not get a cleaner
// story than a human reading the terminal.
func (r *Report) JSON() ([]byte, error) {
	type payload struct {
		*Report
		Method      map[string]any `json:"method"`
		Limitations string         `json:"limitations"`
	}
	return json.MarshalIndent(payload{
		Report: r,
		Method: map[string]any{
			"grace_minutes":    r.GraceMinutes,
			"revert_detection": "git trailer only",
			"backfill":         "excluded",
		},
		Limitations: limitationsFooter,
	}, "", "  ")
}

func (r *Report) totals() (conformed, violated, attributed int) {
	for _, d := range r.Decisions {
		conformed += d.Conformed
		violated += d.Violated
		attributed += d.Attributed
	}
	return
}

// RawText renders §D5.3's drill-down: the event rows behind a figure.
func RawText(events []RawEvent) string {
	if len(events) == 0 {
		return "no events for that scope.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d raw events (seq, id, ts, kind, actor, session, commit, decision, payload)\n\n",
		len(events))
	for _, e := range events {
		fmt.Fprintf(&b, "%d  %s  %s  %-28s %-6s %-14s %-10s %-14s %s\n",
			e.Seq, e.ID, e.Timestamp, e.Kind, e.Actor,
			shortID(e.SessionID), shortSHA(e.CommitSHA), shortID(e.DecisionID), e.Payload)
	}
	return b.String()
}

func repoLabel(root string) string {
	if root == "" {
		return "(this store)"
	}
	parts := strings.Split(strings.TrimSuffix(root, "/"), "/")
	return parts[len(parts)-1]
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// SortDecisions keeps the rendering deterministic for callers that assemble
// rows themselves (the tests do).
func SortDecisions(rows []DecisionRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].DecisionID < rows[j].DecisionID })
}

// CoverageText renders §D5.1 for `memtrace report coverage`.
//
// Day one's answer is 0 of 0, and it is displayed as "no agent sessions yet",
// never as a percentage: 0% would read as a failing product when it means an
// unused one, and the kill criterion it feeds deserves better than a number
// that cannot tell those apart.
func CoverageText(c Coverage, days, graceMinutes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "attribution coverage — last %d days\n", days)
	fmt.Fprintf(&b, "grace window: %dm · revert detection: git trailer only · backfill: excluded\n\n",
		graceMinutes)
	if c.TotalSessions == 0 {
		b.WriteString("no agent sessions yet — coverage is undefined, not zero\n")
		b.WriteString("(CLI invocations are excluded from this denominator by design)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%s produced an attributable decision→diff event\n",
		rate(c.AttributedSessions, c.TotalSessions, "agent sessions"))
	fmt.Fprintf(&b, "  via pack:   %d\n  via recall: %d\n", c.ViaPack, c.ViaRecall)
	fmt.Fprintf(&b, "\n[n=%d sessions]\n", c.TotalSessions)
	return b.String()
}
