package pack

import (
	"fmt"
	"strings"

	"github.com/memtrace-dev/memtrace/internal/embedding"
)

// --- P5: deduplication ---

type dedupedItem struct {
	id     string
	class  Class
	reason string
}

// dedup applies §P5's four rules to the ranked candidate set, in order. It runs
// before selection so budget is never spent twice on the same thing.
//
// Rule 2 (topic identity) is *asserted*, not enforced: ADR-0001's partial
// unique index permits at most one non-terminal decision per (project,
// topic_key) and only non-terminal decisions pack, so two packed decisions
// sharing a topic_key is store corruption, not a packing case.
func dedup(cands []*candidate, vectors map[string][]float64) ([]*candidate, []dedupedItem) {
	var kept []*candidate
	var dropped []dedupedItem

	seenID := make(map[string]bool, len(cands))
	decisionTopics := make(map[string]string, len(cands)) // topic_key -> decision id
	textKeys := make(map[string]string, len(cands))       // normalized text -> kept id

	for _, c := range cands {
		id := c.id()
		// 1. Row identity. A row reachable via both the scope and text routes is
		// one candidate carrying both signals, not two candidates.
		if seenID[id] {
			continue
		}
		seenID[id] = true

		if c.class == ClassDecision {
			if tk := c.topicKey(); tk != "" {
				decisionTopics[tk] = id
			}
		} else if tk := c.topicKey(); tk != "" {
			// 3. Legacy echo: a note on the same topic as a packed decision is
			// the v1 migration's shadow of that decision.
			if owner, ok := decisionTopics[tk]; ok {
				dropped = append(dropped, dedupedItem{
					id: id, class: c.class, reason: "topic echo of " + owner,
				})
				continue
			}
		}

		// 4. Near-duplicate text. With embeddings on both rows, cosine ≥ 0.95;
		// without, exact identity of whitespace-normalized title+body. The
		// narrow net is deliberate: the packer only refuses to spend budget
		// twice on what it can *prove* is the same thing (paraphrase detection
		// belongs to the linter, with human review).
		if dupID, reason := nearDuplicate(c, kept, vectors, textKeys); dupID != "" {
			dropped = append(dropped, dedupedItem{
				id: id, class: c.class, reason: reason + " " + dupID,
			})
			continue
		}
		textKeys[normalizedText(c)] = id
		kept = append(kept, c)
	}
	return kept, dropped
}

const nearDuplicateCosine = 0.95

func nearDuplicate(c *candidate, kept []*candidate, vectors map[string][]float64, textKeys map[string]string) (string, string) {
	if id, ok := textKeys[normalizedText(c)]; ok {
		return id, "duplicate text of"
	}
	vec := vectors[c.id()]
	if len(vec) == 0 {
		return "", ""
	}
	for _, k := range kept {
		other := vectors[k.id()]
		if len(other) == 0 {
			continue
		}
		if embedding.CosineSimilarity(vec, other) >= nearDuplicateCosine {
			return k.id(), "deduped against"
		}
	}
	return "", ""
}

func normalizedText(c *candidate) string {
	var raw string
	if c.class == ClassDecision {
		raw = c.decision.Title + " " + c.decision.Body
	} else {
		raw = c.note.Summary + " " + c.note.Content
	}
	return strings.Join(strings.Fields(strings.ToLower(raw)), " ")
}

// --- P4: selection under the budget ---

type omittedItem struct {
	id     string
	class  Class
	rank   int
	tokens int
}

type selection struct {
	text    string
	served  []ServedItem
	omitted []omittedItem
	stubs   int
}

// selectItems is §P4: greedy in rank order, per-item stub fallback, explicit
// omission. Not knapsack — score sums are not user value, and the top-ranked
// binding decision must get first claim on the budget unconditionally
// (rejected alternative C).
func selectItems(rc renderContext, cands []*candidate, req Request, proposedIDs []string, deduped []dedupedItem) selection {
	rendered := make([]renderedItem, len(cands))
	for i, c := range cands {
		rendered[i] = renderItem(rc, c, i+1)
	}

	// The reserve is a true upper bound on the envelope: the manifest rendered
	// with the largest numbers it could carry, plus §P4's footer floor.
	worstManifest := manifest(req, req.BudgetTokens, len(cands), len(cands), len(cands), len(cands))
	reserve := Estimate(worstManifest) + footerReserve
	remaining := req.BudgetTokens - reserve

	var sel selection
	var blocks []string

	consider := func(i int) {
		c := cands[i]
		r := rendered[i]
		if c.class == ClassNote && req.ExcludeNotes {
			return
		}
		fullCost := Estimate(r.full) + blockSeparatorCost
		if fullCost <= remaining {
			remaining -= fullCost
			form := FormFull
			if c.class == ClassNote {
				form = FormSummary
			}
			rank := len(sel.served) + 1
			blocks = append(blocks, withRank(rank, r.full))
			sel.served = append(sel.served, ServedItem{
				ID: r.id, Class: c.class, Form: form,
				Rank: rank, Score: c.score, Tokens: Estimate(r.full),
			})
			return
		}
		if r.stub != "" {
			stubCost := Estimate(r.stub) + blockSeparatorCost
			if stubCost <= remaining {
				remaining -= stubCost
				rank := len(sel.served) + 1
				blocks = append(blocks, withRank(rank, r.stub))
				sel.stubs++
				sel.served = append(sel.served, ServedItem{
					ID: r.id, Class: c.class, Form: FormStub,
					Rank: rank, Score: c.score, Tokens: Estimate(r.stub),
				})
				return
			}
		}
		// Skip and continue, never first-fit-stop: a large mid-rank item must
		// not starve the smaller items below it. Nothing is dropped silently.
		sel.omitted = append(sel.omitted, omittedItem{
			id: r.id, class: c.class, rank: r.candIdx, tokens: Estimate(r.full),
		})
	}

	// Tier 1: decisions, in rank order. Tier 2: notes, from what is left — a
	// note can never displace a decision (§P3's two strict tiers).
	for i, c := range cands {
		if c.class == ClassDecision {
			consider(i)
		}
	}
	for i, c := range cands {
		if c.class == ClassNote {
			consider(i)
		}
	}

	sel.text = assemble(req, blocks, sel, proposedIDs, deduped)
	return sel
}

// withRank prefixes §P8's 1-based render-order rank. Rank is assigned over the
// *served* items, while an omitted item reports the rank it would have had in
// the candidate order — so "what was #3 and why didn't I see it" is answerable.
func withRank(rank int, block string) string {
	return fmt.Sprintf("[%d] %s", rank, block)
}

// blockSeparatorCost accounts for the blank line between items, so the budget
// covers the bytes actually emitted rather than the items in isolation.
const blockSeparatorCost = 1

// assemble renders the final text and resolves the manifest's `used` field.
//
// `used` is the estimate of the whole text *including the manifest that states
// it*, which is self-referential. It is resolved by fixpoint rather than
// guessed: the number only ever grows by digits, so this converges in one or
// two passes, and the loop is bounded.
func assemble(req Request, blocks []string, sel selection, proposedIDs []string, deduped []dedupedItem) string {
	decisions, notes := 0, 0
	for _, s := range sel.served {
		if s.Class == ClassDecision {
			decisions++
		} else {
			notes++
		}
	}

	body := strings.Join(blocks, "\n\n")
	if len(blocks) == 0 {
		body = "No binding decisions for these files."
	}

	// The footer truncates into §P4's reserve by dropping ids and keeping
	// counts, in one downward ladder: all ids, then 4, 2, 1, then counts only.
	footer := footerLines(sel.omitted, deduped, proposedIDs, 0)
	if Estimate(strings.Join(footer, "\n")) > footerReserve {
		for _, maxIDs := range []int{4, 2, 1, -1} {
			footer = footerLines(sel.omitted, deduped, proposedIDs, maxIDs)
			if Estimate(strings.Join(footer, "\n")) <= footerReserve {
				break
			}
		}
	}

	used := 0
	var text string
	for i := 0; i < 5; i++ {
		head := manifest(req, used, len(sel.served), decisions, notes, len(sel.omitted))
		parts := []string{head, body}
		if len(footer) > 0 {
			parts = append(parts, strings.Join(footer, "\n"))
		}
		text = strings.Join(parts, "\n\n") + "\n"
		next := Estimate(text)
		if next == used {
			break
		}
		used = next
	}
	return text
}

// assertNoContradiction is §P6's structural guarantee, kept as an executable
// statement rather than prose: at most one non-terminal decision per topic_key
// means two packed decisions can never share one. A violation is store
// corruption; the packer reports it rather than papering over it.
func assertNoContradiction(served []*candidate) error {
	seen := map[string]string{}
	for _, c := range served {
		if c.class != ClassDecision {
			continue
		}
		tk := c.topicKey()
		if tk == "" {
			continue
		}
		if other, ok := seen[tk]; ok {
			return fmt.Errorf("store corruption: decisions %s and %s both hold topic_key %q",
				other, c.id(), tk)
		}
		seen[tk] = c.id()
	}
	return nil
}
