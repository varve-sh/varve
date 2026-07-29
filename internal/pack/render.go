package pack

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// The normative serializer (ADR-0002 §P8). UTF-8, LF line endings, no trailing
// whitespace, lists comma-separated in served order. Byte-identical output for
// identical inputs and store state (§P12) — which is what makes the golden
// tests possible and "why didn't the agent see decision X" answerable.
//
// One deliberate departure from §P8's illustrative example: ids are rendered in
// full, not elided to `01J9W...`. The example abbreviates for readability, but
// the same ids appear in `memory_get <id>` pointers the agent is meant to call,
// and an elided id cannot be called.

const packHeader = "VARVE PACK v1"

// noteSummaryBytes caps a note's rendered form (§P2: notes render in summary
// form only, `summary` or the first 400 bytes of `content`).
const noteSummaryBytes = 400

// footerReserve is §P4's floor for the footer, in estimated tokens.
const footerReserve = 120

// renderContext holds the per-decision lookups the serializer needs, fetched
// once per pack rather than once per candidate.
type renderContext struct {
	evidence   map[string][]types.Evidence
	unresolved map[string]int
}

type renderedItem struct {
	full    string
	stub    string // empty for notes: a note either fits in summary form or is omitted
	class   Class
	id      string
	candIdx int // 1-based rank in the ranked candidate list, for the omitted list
}

// renderItem produces both renderings of a candidate up front, so selection
// costs exactly what it serves (§P4: "all costs via the P7 estimator, over the
// exact bytes that would be emitted").
func renderItem(rc renderContext, c *candidate, candIdx int) renderedItem {
	if c.class == ClassNote {
		return renderedItem{
			full:    renderNote(c.note),
			class:   ClassNote,
			id:      c.note.ID,
			candIdx: candIdx,
		}
	}
	d := c.decision
	head := decisionHeader(rc, c)
	var full strings.Builder
	full.WriteString(head)
	full.WriteString("\n")
	full.WriteString(d.Title)
	if body := strings.TrimRight(d.Body, "\n"); body != "" {
		full.WriteString("\n")
		full.WriteString(body)
	}
	if ev := evidenceLine(rc, d.ID); ev != "" {
		full.WriteString("\n")
		full.WriteString(ev)
	}

	bodyTokens := Estimate(d.Body)
	stub := head + "\n" + d.Title + "\n" +
		fmt.Sprintf("[body elided — %d est. tokens; memory_get %s]", bodyTokens, d.ID)

	return renderedItem{full: full.String(), stub: stub, class: ClassDecision, id: d.ID, candIdx: candIdx}
}

// decisionHeader is §P8's item header line, minus the rank prefix (which is
// only known once selection has finished).
func decisionHeader(rc renderContext, c *candidate) string {
	d := c.decision
	var b strings.Builder
	b.WriteString("DECISION ")
	b.WriteString(d.ID)
	b.WriteString(" · ")

	switch {
	case d.Status == types.StatusViolated:
		// The marker always carries the unresolved count: the agent has to know
		// the codebase currently contradicts a rule that still binds (§P8,
		// episode arithmetic per ADR-0002 Amendment 1).
		n := rc.unresolved[d.ID]
		if n < 0 {
			n = 0
		}
		fmt.Fprintf(&b, "VIOLATED (%d unresolved)", n)
	case d.Kind == types.DecisionKindConvention && len(d.Scope) == 0:
		b.WriteString("active (convention, repo-wide)")
	default:
		b.WriteString(string(d.Status))
	}

	fmt.Fprintf(&b, " · conf %.2f", d.Confidence)
	if len(d.Scope) > 0 {
		b.WriteString(" · scope: ")
		b.WriteString(strings.Join(d.Scope, ", "))
	}
	return b.String()
}

func evidenceLine(rc renderContext, decisionID string) string {
	rows := rc.evidence[decisionID]
	if len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for _, e := range rows {
		parts = append(parts, string(e.Kind)+" "+e.Ref)
	}
	return "evidence: " + strings.Join(parts, ", ")
}

func renderNote(n *types.Note) string {
	body := n.Summary
	if strings.TrimSpace(body) == "" {
		body = n.Content
		if len(body) > noteSummaryBytes {
			// Cut on a rune boundary so the output stays valid UTF-8.
			cut := noteSummaryBytes
			for cut > 0 && !utf8Start(body[cut]) {
				cut--
			}
			body = strings.TrimRight(body[:cut], " \t\n") + "…"
		}
	}
	head := "NOTE " + n.ID + " · " + string(n.Status)
	if len(n.FilePaths) > 0 {
		head += " · files: " + strings.Join(n.FilePaths, ", ")
	}
	return head + "\n" + strings.TrimRight(body, "\n") +
		"\n(full text: memory_get " + n.ID + ")"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// manifest is §P8's three-line header. `used` is the estimate of the whole
// emitted text, which includes the manifest itself — resolved by fixpoint in
// assemble, not by guessing.
func manifest(req Request, used, items, decisions, notes, omitted int) string {
	var b strings.Builder
	b.WriteString(packHeader)
	b.WriteString("\n")
	if len(req.FilePaths) > 0 {
		b.WriteString("files: " + strings.Join(req.FilePaths, ", ") + "\n")
	}
	if req.Task != "" {
		b.WriteString("task: " + singleLine(req.Task) + "\n")
	}
	fmt.Fprintf(&b, "budget: %d est-tokens (%s) · used: %d · items: %d (%s, %s) · omitted: %d",
		req.BudgetTokens, EstimatorVersion, used, items,
		plural(decisions, "decision"), plural(notes, "note"), omitted)
	return b.String()
}

// plural keeps §P8's example wording exact ("3 decisions, 1 note"). The format
// is normative, so "1 notes" would be a deviation, not a typo.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// footerLines renders §P8's footer. Every line truncates by dropping ids and
// keeping counts — the count is the part that carries the meaning ("something
// binding-looking exists that you are not being shown"); the ids are only an
// affordance for acting on it.
func footerLines(omitted []omittedItem, deduped []dedupedItem, proposedIDs []string, maxIDs int) []string {
	var lines []string
	if len(omitted) > 0 {
		parts := make([]string, 0, len(omitted))
		shown := len(omitted)
		if maxIDs < 0 {
			shown = 0
		} else if maxIDs > 0 && maxIDs < shown {
			shown = maxIDs
		}
		for _, o := range omitted[:shown] {
			parts = append(parts, fmt.Sprintf("%s %s (rank %d, %d est. tokens)",
				strings.ToUpper(string(o.class)), o.id, o.rank, o.tokens))
		}
		line := "-- omitted (over budget): " + strconv.Itoa(len(omitted))
		if len(parts) > 0 {
			line += " — " + strings.Join(parts, ", ")
			if shown < len(omitted) {
				line += fmt.Sprintf(", +%d more", len(omitted)-shown)
			}
		}
		lines = append(lines, line)
	}
	if len(deduped) > 0 {
		parts := make([]string, 0, len(deduped))
		shown := len(deduped)
		if maxIDs < 0 {
			shown = 0
		} else if maxIDs > 0 && maxIDs < shown {
			shown = maxIDs
		}
		for _, d := range deduped[:shown] {
			parts = append(parts, fmt.Sprintf("%s %s (%s)",
				strings.ToUpper(string(d.class)), d.id, d.reason))
		}
		line := "-- deduped: " + strconv.Itoa(len(deduped))
		if len(parts) > 0 {
			line += " — " + strings.Join(parts, ", ")
			if shown < len(deduped) {
				line += fmt.Sprintf(", +%d more", len(deduped)-shown)
			}
		}
		lines = append(lines, line)
	}
	if line := ProposedFooter(proposedIDs, maxIDs); line != "" {
		lines = append(lines, line)
	}
	if len(omitted) > 0 || len(deduped) > 0 {
		lines = append(lines, "-- raise budget_tokens or memory_get an ID above for anything elided")
	}
	return lines
}
