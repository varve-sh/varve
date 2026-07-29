package lint

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ImportSummary is the import half of the report header (§D5). Nil when the
// report is a bare `varve lint`.
type ImportSummary struct {
	Repo      string            `json:"repo"`
	Sources   map[string]string `json:"sources"`
	Decisions int               `json:"decisions"`
	Notes     int               `json:"notes"`
	Skipped   int               `json:"skipped"`
	Batch     string            `json:"batch"`
	Warnings  []string          `json:"warnings,omitempty"`
	DryRun    bool              `json:"dry_run"`
}

// Report is the printable artifact.
type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Repo        string           `json:"repo"`
	Import      *ImportSummary   `json:"import,omitempty"`
	Lint        *Result          `json:"lint"`
	Backlog     *ProposedBacklog `json:"backlog"`
	Adoption    Adoption         `json:"adoption"`
}

// Adoption is §D4's never-scored section: facts about how varve is being used,
// as opposed to facts about the user's memory.
type Adoption struct {
	Proposed     int `json:"proposed"`
	Accepted     int `json:"accepted"`
	WithEvidence int `json:"with_evidence"`
	Packed       int `json:"packed"`
}

// BannedVocabulary is ADR-0004 §D6's list, extended by §D5 with the
// editorializing this report specifically must not do. Enforced by a test over
// the rendered output, not by review.
var BannedVocabulary = []string{
	"caused", "saved", "roi", "return on investment", "$",
	"your memory is broken", "productivity",
}

// Text renders the terminal report (§D5's layout).
func (r *Report) Text() string {
	var b strings.Builder
	stamp := r.GeneratedAt.UTC().Format("2006-01-02T15:04Z")
	if r.Import != nil {
		fmt.Fprintf(&b, "varve import report — %s — %s\n", r.Repo, stamp)
		if len(r.Import.Sources) > 0 {
			fmt.Fprintf(&b, "sources: %s\n", joinSources(r.Import.Sources))
		}
		verb := "imported"
		if r.Import.DryRun {
			verb = "would import"
		}
		fmt.Fprintf(&b, "%s: %d notes · %d decision candidates (all PROPOSED — nothing is\n"+
			"          binding until you accept it: varve decision accept) · %d skipped (already imported)\n",
			verb, r.Import.Notes, r.Import.Decisions, r.Import.Skipped)
		if r.Import.Batch != "" && !r.Import.DryRun {
			fmt.Fprintf(&b, "batch: %s · undo anytime: varve import undo %s\n",
				r.Import.Batch, r.Import.Batch)
		}
		for _, w := range r.Import.Warnings {
			fmt.Fprintf(&b, "warning: %s\n", w)
		}
		// F48, disclosed rather than hidden: rejection is remembered per rule
		// heading, so an edit to a rejected rule's body keeps it out, but
		// renaming its heading offers it again.
		if r.Import.Skipped > 0 {
			b.WriteString("skipped entries were already imported, or are rules you rejected; rejection is\n" +
				"          remembered per rule heading, so renaming a rejected rule offers it again\n")
		}
		b.WriteString("\n")
	} else {
		fmt.Fprintf(&b, "varve lint — %s — %s\n\n", r.Repo, stamp)
	}

	b.WriteString(r.scoreBlock())
	b.WriteString("\n")
	b.WriteString(r.adoptionBlock())
	b.WriteString("\nfindings detail: varve lint --format md   raw rows: varve lint --raw\n")
	// The limitation footer is on the report itself, not in docs (§D5,
	// inheriting ADR-0004 §D6): a caveat a reader has to go looking for is a
	// caveat that does not exist.
	b.WriteString("semantic contradictions and paraphrase duplicates are NOT detected by these\n")
	b.WriteString("checks; findings above are structural and each one is traceable to its rows.\n")
	return b.String()
}

func (r *Report) scoreBlock() string {
	var b strings.Builder
	s := r.Lint.Score
	mode := r.Lint.Modes["duplicates"]
	if s.Suppressed {
		fmt.Fprintf(&b, "corpus health: not scored — %s   [n=%d entries · method: %s]\n",
			s.Reason, s.Entries, mode)
	} else {
		fmt.Fprintf(&b, "corpus health: %d — %s   [n=%d entries · method: %s]\n",
			s.Value, s.Band, s.Entries, mode)
	}
	for _, c := range s.Categories {
		if c.NA {
			fmt.Fprintf(&b, "  %-18s n/a — %s\n", c.Label, c.NAReason)
			continue
		}
		fmt.Fprintf(&b, "  %-18s %d of %d   -%.0f\n", c.Label, c.Affected, c.Denominator, c.Deduction)
	}
	for _, f := range r.Lint.Corrupt {
		fmt.Fprintf(&b, "  %s\n", f.Detail)
	}
	// Unscored review candidates are printed inside the score block, directly
	// under the category they did NOT move (ADR-0005 Amendment 2). Putting them
	// anywhere else would let a reader mistake them for a deduction, or miss
	// them entirely — and the whole point of the split is that a prompt and an
	// arithmetic input look different on the page.
	if c := r.Lint.Check("L6"); c != nil && (len(c.Candidates) > 0 || len(c.Hubs) > 0) {
		n := len(c.Candidates) + len(c.Hubs)
		fmt.Fprintf(&b, "  %-18s %d shared-scope review candidate(s), not scored — calibration pending\n",
			"", n)
		for _, f := range c.Hubs {
			fmt.Fprintf(&b, "    %s\n", f.Detail)
		}
		for _, f := range c.Candidates {
			fmt.Fprintf(&b, "    %s  %s\n", f.ID, truncate(f.Detail, 80))
		}
	}
	return b.String()
}

func (r *Report) adoptionBlock() string {
	var b strings.Builder
	b.WriteString("adoption (not scored — these are facts about varve usage, not about your memory):\n")
	fmt.Fprintf(&b, "  %d proposed decisions awaiting review · %d accepted · %d with curated evidence\n",
		r.Adoption.Proposed, r.Adoption.Accepted, r.Adoption.WithEvidence)
	if r.Backlog != nil && len(r.Backlog.DisposalRequested) > 0 {
		// Listed first when non-empty (§D3 L7): each is one keystroke from
		// resolution, because a user already said "throw this away" in-chat.
		fmt.Fprintf(&b, "  %d pending disposal requests — resolve with varve decision reject <id>:\n",
			len(r.Backlog.DisposalRequested))
		for _, f := range r.Backlog.DisposalRequested {
			fmt.Fprintf(&b, "    %s  %s\n", f.ID, truncate(f.Title, 60))
		}
	}
	if r.Backlog != nil && len(r.Backlog.Aging) > 0 {
		fmt.Fprintf(&b, "  %d proposals older than 14 days\n", len(r.Backlog.Aging))
	}
	for _, id := range r.Lint.GatedOut {
		c := r.Lint.Check(id)
		fmt.Fprintf(&b, "  %s (%s): n/a — %s\n", id, c.Name, c.NAReason)
	}
	return b.String()
}

// Markdown renders §D5's forwardable artifact: the findings, itemized. This is
// the output ADR-0005's honest-funnel bet rests on — a user can act on every
// line of it against their own source files and never install varve.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# varve report — %s\n\n_%s_\n\n", r.Repo,
		r.GeneratedAt.UTC().Format("2006-01-02T15:04Z"))
	b.WriteString("```\n" + r.scoreBlock() + "```\n\n")
	for _, c := range r.Lint.Checks {
		fmt.Fprintf(&b, "## %s — %s\n\n", c.ID, c.Name)
		switch {
		case c.NA:
			fmt.Fprintf(&b, "_not applicable: %s_\n\n", c.NAReason)
			continue
		case len(c.Findings) == 0:
			fmt.Fprintf(&b, "_no findings (%d checked)_\n\n", c.Checked)
			continue
		}
		fmt.Fprintf(&b, "%d of %d checked\n\n", len(c.Findings), c.Checked)
		for _, f := range c.Findings {
			line := "- `" + f.ID + "`"
			if f.Title != "" {
				line += " " + truncate(f.Title, 80)
			}
			if f.Detail != "" {
				line += " — " + f.Detail
			}
			if f.SourceRef != "" {
				line += " (" + f.SourceRef + ")"
			}
			b.WriteString(line + "\n")
		}
		if c.Misses != "" {
			fmt.Fprintf(&b, "\n_misses: %s_\n", c.Misses)
		}
		b.WriteString("\n")
	}
	b.WriteString("---\n\nSemantic contradictions and paraphrase duplicates are NOT detected. " +
		"Every finding above names the rows behind it.\n")
	return b.String()
}

// JSON embeds the row IDs backing every line — ADR-0004 §D6.2's invariant, that
// a number which cannot be traced to rows may not be rendered.
func (r *Report) JSON() (string, error) {
	out, err := json.MarshalIndent(r, "", "  ")
	return string(out), err
}

func joinSources(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" ("+m[k]+")")
	}
	return strings.Join(parts, ", ")
}
