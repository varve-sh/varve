package retrieval

import (
	"math"
	"sort"
	"time"

	"github.com/varve-sh/varve/internal/embedding"
	"github.com/varve-sh/varve/internal/types"
)

// StoreReader defines the subset of MemoryStore methods needed by the pipeline.
// Defined here (not in kernel/) to avoid circular imports.
type StoreReader interface {
	SearchFTS(query string, projectID string, limit int) ([]types.FTSResult, error)
	FindByID(id string) (*types.Memory, error)
	FindEmbeddings(projectID string) ([]EmbeddingRow, error)
}

// EmbeddingRow mirrors kernel.EmbeddingRow to avoid circular imports.
type EmbeddingRow struct {
	ID        string
	Embedding []float64
}

// Pipeline executes the retrieval flow: FTS → filter → score → sort → limit.
type Pipeline struct {
	store     StoreReader
	projectID string
	embedder  embedding.Embedder // nil = embeddings disabled
}

// New creates a retrieval pipeline. store must implement StoreReader.
func New(store StoreReader, projectID string) *Pipeline {
	return &Pipeline{store: store, projectID: projectID}
}

// WithEmbedder attaches an embedder to the pipeline, enabling hybrid BM25+semantic search.
func (p *Pipeline) WithEmbedder(e embedding.Embedder) {
	p.embedder = e
}

// Recall runs the full retrieval pipeline for the given input.
func (p *Pipeline) Recall(input types.MemoryRecallInput) ([]types.ScoredMemory, error) {
	// Fetch 3× the desired limit for reranking headroom.
	candidateLimit := input.Limit * 3
	if candidateLimit < 30 {
		candidateLimit = 30
	}

	ftsResults, err := p.store.SearchFTS(input.Query, p.projectID, candidateLimit)
	if err != nil {
		return nil, err
	}

	// When FTS finds nothing but an embedder is configured, fall back to pure
	// semantic search so conceptually related queries still find results.
	if len(ftsResults) == 0 {
		if p.embedder == nil {
			return nil, nil
		}
		return p.semanticOnlySearch(input)
	}

	// Resolve each FTS row to a full memory and apply hard filters.
	candidates := make([]candidate, 0, len(ftsResults))
	ftsIDs := make(map[string]bool, len(ftsResults))
	for _, r := range ftsResults {
		mem, err := p.store.FindByID(r.ID)
		if err != nil || mem == nil {
			continue
		}
		if !passesFilters(mem, input) {
			continue
		}
		ftsIDs[mem.ID] = true
		candidates = append(candidates, candidate{memory: *mem, bm25Rank: r.Rank})
	}

	// In hybrid mode, score all stored embeddings against the query and expand
	// the candidate pool with top-K semantic results not already found by FTS.
	// This ensures semantically relevant memories are found even when they lack
	// the exact keywords the user typed.
	var semanticScores map[string]float64
	if p.embedder != nil {
		semanticScores, candidates = p.hybridExpand(input, candidates, ftsIDs, candidateLimit)
	}

	// Score, sort, and limit.
	scored := scoreCandidates(candidates, time.Now(), semanticScores)
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})
	if len(scored) > input.Limit {
		scored = scored[:input.Limit]
	}

	return scored, nil
}

// hybridExpand embeds the query, scores all stored embeddings, and adds any
// top-K semantic results that were not already found by FTS. Returns the
// updated candidate slice and the full semantic score map.
func (p *Pipeline) hybridExpand(
	input types.MemoryRecallInput,
	candidates []candidate,
	ftsIDs map[string]bool,
	limit int,
) (map[string]float64, []candidate) {
	queryVec, err := p.embedder.Embed(input.Query)
	if err != nil || len(queryVec) == 0 {
		return nil, candidates
	}

	storedEmbeddings, err := p.store.FindEmbeddings(p.projectID)
	if err != nil || len(storedEmbeddings) == 0 {
		return nil, candidates
	}

	// Score every stored embedding against the query.
	type semRow struct {
		id  string
		sim float64
	}
	semRows := make([]semRow, 0, len(storedEmbeddings))
	semScores := make(map[string]float64, len(storedEmbeddings))
	for _, row := range storedEmbeddings {
		sim := embedding.CosineSimilarity(queryVec, row.Embedding)
		if sim > 0 {
			semScores[row.ID] = sim
			semRows = append(semRows, semRow{row.ID, sim})
		}
	}

	// Sort by similarity descending and add top candidates not already in FTS set.
	sort.Slice(semRows, func(i, j int) bool { return semRows[i].sim > semRows[j].sim })
	added := 0
	for _, r := range semRows {
		if added >= limit {
			break
		}
		if ftsIDs[r.id] {
			continue // already in candidate pool
		}
		mem, err := p.store.FindByID(r.id)
		if err != nil || mem == nil {
			continue
		}
		if !passesFilters(mem, input) {
			continue
		}
		// bm25Rank = 0 → normalises to 0 in the scorer, so this doc wins only via semantic signal.
		candidates = append(candidates, candidate{memory: *mem, bm25Rank: 0})
		added++
	}

	return semScores, candidates
}

// passesFilters returns true if m satisfies all hard filters in input.
func passesFilters(m *types.Memory, input types.MemoryRecallInput) bool {
	if input.Status != "" && m.Status != input.Status {
		return false
	}
	// An empty status filter means "everything live". The store already
	// excludes terminal decisions, but the semantic-only path can reach rows
	// the FTS query never filtered, so it is re-checked here (ADR-0001 D10).
	if input.Status == "" && types.IsTerminalMemoryStatus(m.Status) {
		return false
	}
	if input.Type != "" && m.Type.Canonical() != input.Type.Canonical() {
		return false
	}
	if input.MinConfidence > 0 && m.Confidence < input.MinConfidence {
		return false
	}
	if len(input.Tags) > 0 && !hasAllTags(m.Tags, input.Tags) {
		return false
	}
	return true
}

// semanticOnlySearch is used when FTS returns no candidates. It embeds the query,
// scores all stored embeddings by cosine similarity, applies hard filters, and
// returns the top results. Used as a fallback for conceptual/paraphrased queries.
func (p *Pipeline) semanticOnlySearch(input types.MemoryRecallInput) ([]types.ScoredMemory, error) {
	queryVec, err := p.embedder.Embed(input.Query)
	if err != nil || len(queryVec) == 0 {
		return nil, nil
	}

	rows, err := p.store.FindEmbeddings(p.projectID)
	if err != nil || len(rows) == 0 {
		return nil, nil
	}

	type scoredRow struct {
		id  string
		sim float64
	}
	ranked := make([]scoredRow, 0, len(rows))
	for _, row := range rows {
		sim := embedding.CosineSimilarity(queryVec, row.Embedding)
		if sim > 0 {
			ranked = append(ranked, scoredRow{row.ID, sim})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].sim > ranked[j].sim })

	now := time.Now()
	results := make([]types.ScoredMemory, 0, input.Limit)
	for _, r := range ranked {
		if len(results) >= input.Limit {
			break
		}
		mem, err := p.store.FindByID(r.id)
		if err != nil || mem == nil {
			continue
		}
		if !passesFilters(mem, input) {
			continue
		}
		ageMs := float64(now.Sub(mem.CreatedAt).Milliseconds())
		recency := math.Pow(0.5, ageMs/recencyHalfLifeMs)
		accessFreq := math.Min(1.0, math.Log2(float64(mem.AccessCount)+1)/10.0)
		// Use weightSemanticOnly (0.50) so scores sum to 1.0, matching BM25-only mode.
		conf := effectiveConfidence(mem, now)
		score := weightSemanticOnly*r.sim + weightRecency*recency +
			weightConfidence*conf + weightAccess*accessFreq
		results = append(results, types.ScoredMemory{
			Memory: *mem,
			Score:  score,
			ScoreBreakdown: types.ScoreBreakdown{
				TextRelevance:   r.sim,
				Recency:         recency,
				Confidence:      conf,
				AccessFrequency: accessFreq,
			},
		})
	}
	return results, nil
}

func hasAllTags(memTags, required []string) bool {
	set := make(map[string]bool, len(memTags))
	for _, t := range memTags {
		set[t] = true
	}
	for _, r := range required {
		if !set[r] {
			return false
		}
	}
	return true
}
