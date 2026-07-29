package lint

import "math"

// MinScorableEntries is §D4's suppression rule: below ten entries there is no
// score at all, findings only.
//
// A percentage over four rows is noise theater — the same reasoning as
// ADR-0004 D6.1's n<5 rule, applied where the number faces a stranger rather
// than the founder. It is one of the three reversible product calls in
// ADR-0005's Status block, so it lives in one named constant.
const MinScorableEntries = 10

// Category is one scored corpus-health dimension with its own denominator.
type Category struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Weight   float64 `json:"weight"`
	Affected int     `json:"affected"`
	// Denominator is printed beside every line (§D4 suppression rule 2): no
	// aggregate without its sample size.
	Denominator int     `json:"denominator"`
	NA          bool    `json:"na"`
	NAReason    string  `json:"na_reason,omitempty"`
	Rate        float64 `json:"rate"`
	// Deduction is the points this category cost, computed from the
	// renormalized weight. Every deduction traces to enumerated findings; the
	// score is arithmetic over the findings list, never an independent
	// judgment.
	Deduction float64 `json:"deduction"`
}

// Score is §D4's corpus-health number.
type Score struct {
	Value      int        `json:"value"`
	Band       string     `json:"band"`
	Suppressed bool       `json:"suppressed"`
	Reason     string     `json:"reason,omitempty"`
	Entries    int        `json:"entries"`
	Categories []Category `json:"categories"`
}

// scoreWeights are §D4's weights, before renormalization.
var scoreWeights = []struct {
	key, label, check string
	weight            float64
}{
	{"dead_refs", "dead references", "L3", .25},
	{"duplicates", "duplicates", "L5", .25},
	{"contradictions", "contradictions", "L6", .20},
	{"staleness", "staleness", "L7", .20},
	{"hygiene", "hygiene", "L10", .10},
}

// computeScore implements §D4's formula:
//
//	rate_c = affected_c / denominator_c
//	score  = round(100 × (1 − Σ w_c × min(1, rate_c))) over APPLICABLE
//	         categories, weights renormalized to sum 1 when a category is N/A
//
// Only corpus-intrinsic checks are scored. L1, L2, L8, L9 and the proposed
// backlog are adoption facts: on a fresh import they are structurally "bad"
// — everything is proposed, nothing is packed, nothing has curated evidence —
// and scoring them would make every fresh import read as failure at exactly
// the moment a stranger is evaluating the tool.
func computeScore(res *Result, _ Options) *Score {
	s := &Score{Entries: res.Entries}
	total := 0.0
	for _, w := range scoreWeights {
		c := res.Check(w.check)
		cat := Category{Key: w.key, Label: w.label, Weight: w.weight}
		switch {
		case c == nil || c.NA:
			cat.NA = true
			if c != nil {
				cat.NAReason = c.NAReason
			}
		case c.Checked == 0:
			cat.NA, cat.NAReason = true, "nothing to check"
		default:
			cat.Affected = len(c.Findings)
			cat.Denominator = c.Checked
			cat.Rate = math.Min(1, float64(cat.Affected)/float64(cat.Denominator))
			total += w.weight
		}
		s.Categories = append(s.Categories, cat)
	}

	if res.Entries < MinScorableEntries {
		s.Suppressed = true
		s.Reason = "corpus too small to score"
		return s
	}
	if total == 0 {
		s.Suppressed = true
		s.Reason = "no scorable categories applied"
		return s
	}

	penalty := 0.0
	for i := range s.Categories {
		cat := &s.Categories[i]
		if cat.NA {
			continue
		}
		// Renormalization: the remaining weights are scaled to sum to 1, so a
		// store with no repo is scored on what could be measured rather than
		// silently credited for the checks that never ran.
		norm := cat.Weight / total
		cat.Deduction = 100 * norm * cat.Rate
		penalty += norm * cat.Rate
	}
	s.Value = int(math.Round(100 * (1 - penalty)))
	s.Band = Band(s.Value)
	return s
}

// Band labels the score. §D4: printed with the band label, never the bare
// number — a naked 74 invites a question the number cannot answer, and the
// band says what it is claiming.
func Band(v int) string {
	switch {
	case v >= 90:
		return "healthy"
	case v >= 70:
		return "needs attention"
	default:
		return "significant rot"
	}
}
