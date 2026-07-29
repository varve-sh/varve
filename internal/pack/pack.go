package pack

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/varve-sh/varve/internal/embedding"
	"github.com/varve-sh/varve/internal/retrieval"
	"github.com/varve-sh/varve/internal/scope"
	"github.com/varve-sh/varve/internal/types"
)

// --- P1: the tool contract ---

// Request is `memory_pack`'s validated input (ADR-0002 §P1).
type Request struct {
	FilePaths []string
	Task      string
	// BudgetTokens is a hard ceiling on the estimated cost of the entire
	// returned text, envelope included. Zero means the default.
	BudgetTokens int
	// ExcludeNotes inverts §P1's `include_notes`, whose default is true. The
	// field is stored inverted so the zero value *is* the documented default:
	// an `IncludeNotes bool` left unset would silently mean "decisions only",
	// which is a different product.
	ExcludeNotes bool
	// Now is the scoring clock. Build truncates it to the UTC hour (§P12), so
	// repeated calls within an hour are byte-identical.
	Now time.Time
}

// Budget bounds (§P1). The floor is what makes §P4's "manifest plus the top
// decision's stub always fits" true; the ceiling stops a caller asking for a
// pack no context window could hold.
const (
	DefaultBudget = 2000
	MinBudget     = 500
	MaxBudget     = 100000
)

// Error codes (§P1). The code is the first token of the message so an agent
// can branch on it without parsing prose.
var (
	ErrBadBudget = errors.New("E1_BAD_BUDGET")
	ErrBadPath   = errors.New("E2_BAD_PATH")
	ErrNoAnchor  = errors.New("E3_NO_ANCHOR")
	ErrStore     = errors.New("E4_STORE")
)

// Validate normalizes and checks the request. An errored call emits no
// pack.served event, so validation happens before anything is recorded.
func (r *Request) Validate() error {
	if r.BudgetTokens == 0 {
		r.BudgetTokens = DefaultBudget
	}
	if r.BudgetTokens < MinBudget || r.BudgetTokens > MaxBudget {
		return fmt.Errorf("%w: budget_tokens must be between %d and %d, got %d",
			ErrBadBudget, MinBudget, MaxBudget, r.BudgetTokens)
	}

	seen := make(map[string]bool, len(r.FilePaths))
	paths := make([]string, 0, len(r.FilePaths))
	for _, p := range r.FilePaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// An absolute path or a `..` segment is a request about a file outside
		// the repo the scopes are written against; matching it would be
		// meaningless at best.
		if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || hasVolumeName(p) {
			return fmt.Errorf("%w: %s is absolute; file_paths must be repo-relative", ErrBadPath, p)
		}
		clean := scope.Normalize(p)
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("%w: %s escapes the repo root", ErrBadPath, p)
		}
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}
	// Order-insensitive by contract, and sorted so the same set of files always
	// produces the same bytes (§P12).
	sort.Strings(paths)
	r.FilePaths = paths

	r.Task = strings.TrimSpace(r.Task)
	if len([]rune(r.Task)) > 2000 {
		r.Task = string([]rune(r.Task)[:2000])
	}

	if len(r.FilePaths) == 0 && r.Task == "" {
		return fmt.Errorf("%w: provide file_paths, task, or both", ErrNoAnchor)
	}
	if r.Now.IsZero() {
		r.Now = time.Now()
	}
	// §P12: all time-dependent terms use `now` truncated to the UTC hour.
	r.Now = r.Now.UTC().Truncate(time.Hour)
	return nil
}

func hasVolumeName(p string) bool {
	return len(p) >= 2 && p[1] == ':'
}

// --- the store the packer reads ---

// Source is the store surface the packer needs. It is deliberately *not*
// MemoryStore: the v1-shaped projection carries no `expires_at`, so §P2's
// "not expired" is unreachable through it, and its live-status filter includes
// `proposed`, so reusing it would pull quarantined text into the pack as
// content — §P2's headline prohibition. Eligibility is decided against the
// real columns.
type Source interface {
	// PackableDecisions returns non-terminal, non-proposed decisions for the
	// project: `active` and `violated`, expiry not yet applied.
	PackableDecisions(projectID string) ([]types.Decision, error)
	// ProposedDecisions returns proposals, for the footer count only. They are
	// never content (§P2). A pending disposal request does not exclude one:
	// it is still a `proposed` row awaiting a human (ADR-0001 A3.1).
	ProposedDecisions(projectID string) ([]types.Decision, error)
	// ActiveNotes returns `active` notes for the project.
	ActiveNotes(projectID string) ([]types.Note, error)
	// TextPool runs the FTS candidate query for a task hint, returning ids and
	// raw BM25 ranks across both tables.
	TextPool(query, projectID string, limit int) ([]types.FTSResult, error)
	// Embeddings returns stored vectors for the project, for hybrid text
	// scoring and §P5.4 near-duplicate detection.
	Embeddings(projectID string) ([]retrieval.EmbeddingRow, error)
	// EmbedQuery embeds the task hint with the user's own configured embedder.
	// A nil vector means "no embedding available": timedOut distinguishes a
	// configured embedder that exceeded §P13's 500ms cap — a degraded pack,
	// recorded as such — from no embedder at all, which is the ordinary
	// BM25-only configuration.
	EmbedQuery(task string) (vec []float64, timedOut bool)
	// UnresolvedViolations returns §P8's `VIOLATED (n unresolved)` counts for
	// the project's violated decisions, keyed by id. Batched deliberately: it
	// is an unindexed json_extract aggregate over `events` (ADR-0002 open
	// question 2), and running it per candidate inside a pack was measured at
	// the dominant cost.
	UnresolvedViolations(projectID string) (map[string]int, error)
	// Evidence returns evidence rows for the project's packable decisions,
	// keyed by decision id, oldest first. Batched for the same reason.
	Evidence(projectID string) (map[string][]types.Evidence, error)
}

// --- results ---

// Form is how an item was rendered (§P10's `form`).
type Form string

const (
	FormFull    Form = "full"
	FormStub    Form = "stub"
	FormSummary Form = "summary"
)

// Class distinguishes the two packable classes.
type Class string

const (
	ClassDecision Class = "decision"
	ClassNote     Class = "note"
)

// ServedItem is one item that entered the agent's context. One pack.item event
// is emitted per served item — including under truncation, which is a hard
// requirement: ADR-0004's whole chain is "decision D was in session S's
// context", and batching or eliding these kills it silently.
type ServedItem struct {
	ID     string
	Class  Class
	Form   Form
	Rank   int
	Score  float64
	Tokens int
}

// Result is a built pack: the exact bytes to return, plus everything the
// instrumentation needs. Building is pure — the caller writes the events, in
// one transaction, per ADR-0001 §D7.
type Result struct {
	Text   string
	Served []ServedItem

	BudgetTokens int
	UsedTokens   int
	ItemCount    int
	Truncated    bool
	OmittedCount int
	DedupedCount int
	StubCount    int
	// ProposedMatched counts proposals whose scope matches the request. They are
	// never content; the footer names them so the human hears about the backlog.
	ProposedMatched int
	// EmbedderTimedOut records §P13's degraded path so a BM25-only pack is
	// distinguishable from a hybrid one after the fact.
	EmbedderTimedOut bool
	ScoredAt         time.Time
	// ExpiredObserved names decisions filtered out by the expiry predicate.
	// The caller emits decision.expired for them (idempotent, §P2).
	ExpiredObserved []string
}

// --- candidates ---

type candidate struct {
	class    Class
	decision *types.Decision
	note     *types.Note

	scopeScore float64
	textScore  float64
	confScore  float64
	recency    float64
	score      float64

	matchedGlobs []string
}

func (c *candidate) id() string {
	if c.class == ClassDecision {
		return c.decision.ID
	}
	return c.note.ID
}

func (c *candidate) topicKey() string {
	if c.class == ClassDecision {
		return c.decision.TopicKey
	}
	return c.note.TopicKey
}

func (c *candidate) orderTime() time.Time {
	if c.class == ClassDecision {
		if c.decision.DecidedAt != nil {
			return *c.decision.DecidedAt
		}
		return time.Time{} // NULLs last
	}
	return c.note.UpdatedAt
}

// Build assembles a pack. It performs no writes: everything it observes that
// needs recording is returned on Result for the caller to emit transactionally.
func Build(src Source, projectID string, req Request) (*Result, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	now := req.Now

	res := &Result{
		BudgetTokens: req.BudgetTokens,
		ScoredAt:     now,
	}

	// --- P2: eligibility, decided before ranking ---
	decisions, err := src.PackableDecisions(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	eligible := decisions[:0]
	for i := range decisions {
		d := decisions[i]
		if d.Status != types.StatusActive && d.Status != types.StatusViolated {
			continue // terminal or proposed never packs, whatever the query returned
		}
		if d.IsExpired(now) {
			// The packer is usually the first component to observe an expiry;
			// the caller emits the (idempotent) event.
			res.ExpiredObserved = append(res.ExpiredObserved, d.ID)
			continue
		}
		eligible = append(eligible, d)
	}
	decisions = eligible

	var notes []types.Note
	if !req.ExcludeNotes {
		notes, err = src.ActiveNotes(projectID)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStore, err)
		}
	}

	// --- P3: candidate routes and text pool ---
	textRanks, semantic, timedOut := textSignals(src, projectID, req)
	res.EmbedderTimedOut = timedOut

	var cands []*candidate
	for i := range decisions {
		d := &decisions[i]
		matched := scope.MatchedGlobs(d.Scope, req.FilePaths)
		unscopedConvention := d.Kind == types.DecisionKindConvention && len(d.Scope) == 0
		_, inText := textRanks[d.ID]
		if len(matched) == 0 && !unscopedConvention && !inText {
			continue
		}
		c := &candidate{class: ClassDecision, decision: d, matchedGlobs: matched}
		switch {
		case len(matched) > 0:
			c.scopeScore = scopeScore(matched, req.FilePaths)
		case unscopedConvention:
			// Route 2: always a candidate at the fixed baseline (§P3, closed
			// open question 1 — `kind=convention, scope=[]` is the canonical
			// repo-wide form).
			c.scopeScore = 0.25
		default:
			c.scopeScore = 0 // matched on text only
		}
		c.textScore = textRanks[d.ID]
		c.confScore = retrieval.EffectiveConfidence(d.Confidence, d.UpdatedAt, d.AccessedAt, now)
		c.recency = retrieval.Recency(d.CreatedAt, now)
		cands = append(cands, c)
	}
	for i := range notes {
		n := &notes[i]
		matched := exactMatches(n.FilePaths, req.FilePaths)
		_, inText := textRanks[n.ID]
		if len(matched) == 0 && !inText {
			continue
		}
		c := &candidate{class: ClassNote, note: n, matchedGlobs: matched}
		if len(matched) > 0 {
			// Notes carry exact paths, so specificity is 1.0 by construction.
			c.scopeScore = 1.0 * (0.5 + 0.5*float64(len(matched))/float64(len(req.FilePaths)))
		}
		c.textScore = textRanks[n.ID]
		c.confScore = retrieval.EffectiveConfidence(n.Confidence, n.UpdatedAt, n.AccessedAt, now)
		c.recency = retrieval.Recency(n.CreatedAt, now)
		cands = append(cands, c)
	}

	// Composite score, with the weights renormalized when an input is
	// structurally absent (§P3).
	hasScope := len(req.FilePaths) > 0
	hasText := req.Task != ""
	for _, c := range cands {
		c.score = composite(c, hasScope, hasText)
	}

	// --- P3: two strict tiers, then the deterministic ordering ---
	sort.SliceStable(cands, func(i, j int) bool { return less(cands[i], cands[j]) })

	// --- P5: dedup ---
	// §P5.4 needs stored vectors whether or not a task was given: near-duplicate
	// detection is about the rows, not about the query.
	if semantic == nil {
		semantic = storedVectors(src, projectID)
	}
	cands, deduped := dedup(cands, semantic)
	res.DedupedCount = len(deduped)

	// --- P8 footer input: proposals matching the request ---
	proposedIDs, err := matchingProposals(src, projectID, req)
	if err != nil {
		return nil, err
	}
	res.ProposedMatched = len(proposedIDs)

	// §P5.2 / §P6: at most one non-terminal decision per (project, topic_key) is
	// a structural invariant of the store, not a packing case. The packer
	// asserts it rather than resolving it: if two packable decisions claim the
	// same topic, the store is corrupt and "the pack contains no contradiction
	// the store knows about" is a promise this call cannot keep.
	if err := assertNoContradiction(cands); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}

	// --- P4: greedy selection under the budget ---
	// The two per-decision lookups the renderer needs are fetched once, not per
	// candidate: at 5,000 decisions the N+1 version spent the whole §P13 budget
	// inside SQLite.
	rc := renderContext{}
	if rc.evidence, err = src.Evidence(projectID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	if rc.unresolved, err = src.UnresolvedViolations(projectID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	sel := selectItems(rc, cands, req, proposedIDs, deduped)
	res.Served = sel.served
	res.ItemCount = len(sel.served)
	res.StubCount = sel.stubs
	res.OmittedCount = len(sel.omitted)
	res.Truncated = sel.stubs > 0 || len(sel.omitted) > 0
	res.Text = sel.text
	res.UsedTokens = Estimate(sel.text)
	return res, nil
}

// composite applies §P3's weights, renormalized when an input is absent.
func composite(c *candidate, hasScope, hasText bool) float64 {
	const (
		wScope = 0.45
		wText  = 0.30
		wConf  = 0.15
		wRec   = 0.10
	)
	scopeW, textW, confW, recW := wScope, wText, wConf, wRec
	if !hasText {
		textW = 0
	}
	if !hasScope {
		scopeW = 0
	}
	total := scopeW + textW + confW + recW
	if total == 0 {
		return 0
	}
	return (scopeW*c.scopeScore + textW*c.textScore + confW*c.confScore + recW*c.recency) / total
}

// less is §P3's ordering: decisions strictly above notes, then score, then
// confidence, then decided_at/updated_at (NULLs last), then id.
func less(a, b *candidate) bool {
	if a.class != b.class {
		return a.class == ClassDecision
	}
	if a.score != b.score {
		return a.score > b.score
	}
	if a.confScore != b.confScore {
		return a.confScore > b.confScore
	}
	at, bt := a.orderTime(), b.orderTime()
	if !at.Equal(bt) {
		if at.IsZero() {
			return false
		}
		if bt.IsZero() {
			return true
		}
		return at.After(bt)
	}
	return a.id() < b.id()
}

// scopeScore is §P3's specificity × (0.5 + 0.5·coverage).
func scopeScore(matchedGlobs []string, paths []string) float64 {
	spec := 0.0
	for _, g := range matchedGlobs {
		if s := specificity(g); s > spec {
			spec = s
		}
	}
	if len(paths) == 0 {
		return 0
	}
	covered := len(scope.MatchedFiles(matchedGlobs, paths))
	coverage := float64(covered) / float64(len(paths))
	return spec * (0.5 + 0.5*coverage)
}

// specificity rewards a glob for claiming these files precisely rather than
// broadly: an exact path is a stronger claim than `**`.
func specificity(g string) float64 {
	switch {
	case strings.Contains(g, "**"):
		return 0.4
	case strings.ContainsAny(g, "*?[{"):
		return 0.7
	default:
		return 1.0
	}
}

func exactMatches(notePaths, requestPaths []string) []string {
	if len(notePaths) == 0 {
		return nil
	}
	want := make(map[string]bool, len(notePaths))
	for _, p := range notePaths {
		want[scope.Normalize(p)] = true
	}
	var out []string
	for _, p := range requestPaths {
		if want[p] {
			out = append(out, p)
		}
	}
	return out
}

// textSignals builds the text-relevance map (§P3's text_score), reusing the
// recall scorer's normalization verbatim so the two paths cannot drift.
func textSignals(src Source, projectID string, req Request) (map[string]float64, map[string][]float64, bool) {
	if req.Task == "" {
		return map[string]float64{}, nil, false
	}
	rows, err := src.TextPool(req.Task, projectID, textPoolLimit(req.BudgetTokens))
	if err != nil {
		return map[string]float64{}, nil, false
	}

	maxMag := 0.0
	for _, r := range rows {
		if m := absFloat(r.Rank); m > maxMag {
			maxMag = m
		}
	}

	// Hybrid mode, when the user has an embedder configured (§P13: 500ms cap,
	// enforced by the Source; a timeout degrades to BM25-only rather than
	// failing the pack).
	queryVec, timedOut := src.EmbedQuery(req.Task)
	var vectors map[string][]float64
	sims := map[string]float64{}
	if len(queryVec) > 0 {
		if stored, err := src.Embeddings(projectID); err == nil {
			vectors = make(map[string][]float64, len(stored))
			for _, row := range stored {
				vectors[row.ID] = row.Embedding
				sims[row.ID] = embedding.CosineSimilarity(queryVec, row.Embedding)
			}
		}
	}

	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		norm := retrieval.NormalizedBM25(r.Rank, maxMag)
		if len(queryVec) > 0 {
			out[r.ID] = retrieval.HybridText(norm, sims[r.ID])
		} else {
			out[r.ID] = norm
		}
	}
	// In hybrid mode a row can be semantically relevant without being in the
	// FTS pool at all, exactly as recall's hybridExpand allows.
	if len(queryVec) > 0 {
		for id, sim := range sims {
			if _, ok := out[id]; !ok && sim > 0 {
				out[id] = retrieval.HybridText(0, sim)
			}
		}
	}
	return out, vectors, timedOut
}

// storedVectors loads the project's embeddings for §P5.4. A store with no
// embedder configured simply has none, and the dedup falls back to exact
// normalized-text identity.
func storedVectors(src Source, projectID string) map[string][]float64 {
	rows, err := src.Embeddings(projectID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make(map[string][]float64, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Embedding
	}
	return out
}

// stubCost is §P4's own figure for the cheapest useful item: a title of ≤200
// chars plus its header renders as a stub of ~90 estimated tokens.
const stubCost = 90

// textPoolLimit sizes the FTS candidate pool. §P3 says the text route works
// "exactly as Recall builds its pool today", which is 3× the number of items
// that can be returned, floored at 30. The packer has no `limit` argument by
// design — the budget is the limit — so the item count is derived from it:
// at most budget/stubCost items can be served, and the pool carries Recall's
// same 3× reranking headroom over that.
//
// Sizing it by the *store* instead was measured at ~1.0s of a 1.3s pack on a
// 5,000-decision store: the pool is reranking headroom, not a table scan.
func textPoolLimit(budget int) int {
	limit := 3 * (budget / stubCost)
	if limit < 30 {
		limit = 30
	}
	return limit
}

func matchingProposals(src Source, projectID string, req Request) ([]string, error) {
	proposals, err := src.ProposedDecisions(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	var ids []string
	for i := range proposals {
		p := &proposals[i]
		if len(scope.MatchedGlobs(p.Scope, req.FilePaths)) > 0 {
			ids = append(ids, p.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
