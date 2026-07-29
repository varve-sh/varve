package retrieval

import (
	"testing"
	"time"

	"github.com/varve-sh/varve/internal/types"
)

func TestScoreCandidates_Empty(t *testing.T) {
	results := scoreCandidates(nil, time.Now(), nil)
	if results != nil {
		t.Error("expected nil for empty candidates")
	}
}

func TestScoreCandidates_ScoreRange(t *testing.T) {
	now := time.Now()
	candidates := []candidate{
		{
			memory: types.Memory{
				ID:          "01",
				Confidence:  1.0,
				AccessCount: 0,
				CreatedAt:   now.Add(-24 * time.Hour),
			},
			bm25Rank: -5.0,
		},
		{
			memory: types.Memory{
				ID:          "02",
				Confidence:  0.5,
				AccessCount: 10,
				CreatedAt:   now.Add(-7 * 24 * time.Hour),
			},
			bm25Rank: -2.0,
		},
	}

	results := scoreCandidates(candidates, now, nil)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Score < 0 || r.Score > 1 {
			t.Errorf("score out of range [0,1]: %f", r.Score)
		}
	}
}

func TestScoreCandidates_BetterBM25Wins(t *testing.T) {
	now := time.Now()
	candidates := []candidate{
		{memory: types.Memory{ID: "high", Confidence: 1.0, CreatedAt: now}, bm25Rank: -10.0},
		{memory: types.Memory{ID: "low", Confidence: 1.0, CreatedAt: now}, bm25Rank: -1.0},
	}

	results := scoreCandidates(candidates, now, nil)
	if results[0].Memory.ID != "high" {
		t.Errorf("expected high-rank match first, got %s", results[0].Memory.ID)
	}
}

func TestScoreCandidates_RecencyDecay(t *testing.T) {
	now := time.Now()
	candidates := []candidate{
		{memory: types.Memory{ID: "recent", Confidence: 1.0, CreatedAt: now}, bm25Rank: -5.0},
		{memory: types.Memory{ID: "old", Confidence: 1.0, CreatedAt: now.Add(-365 * 24 * time.Hour)}, bm25Rank: -5.0},
	}

	results := scoreCandidates(candidates, now, nil)

	var recentScore, oldScore float64
	for _, r := range results {
		if r.Memory.ID == "recent" {
			recentScore = r.ScoreBreakdown.Recency
		} else {
			oldScore = r.ScoreBreakdown.Recency
		}
	}
	if recentScore <= oldScore {
		t.Errorf("recent should have higher recency score: recent=%f, old=%f", recentScore, oldScore)
	}
}

func TestScoreCandidates_AccessFrequency(t *testing.T) {
	now := time.Now()
	results := scoreCandidates([]candidate{
		{memory: types.Memory{ID: "accessed", Confidence: 1.0, CreatedAt: now, AccessCount: 100}, bm25Rank: -5.0},
	}, now, nil)

	if results[0].ScoreBreakdown.AccessFrequency == 0 {
		t.Error("expected non-zero access frequency for count=100")
	}
	if results[0].ScoreBreakdown.AccessFrequency > 1.0 {
		t.Error("access frequency must not exceed 1.0")
	}
}

func TestScoreCandidates_HybridMode(t *testing.T) {
	now := time.Now()
	candidates := []candidate{
		{memory: types.Memory{ID: "sem-winner", Confidence: 1.0, CreatedAt: now}, bm25Rank: -1.0},
		{memory: types.Memory{ID: "bm25-winner", Confidence: 1.0, CreatedAt: now}, bm25Rank: -10.0},
	}
	semanticScores := map[string]float64{
		"sem-winner":  0.99,
		"bm25-winner": 0.01,
	}

	results := scoreCandidates(candidates, now, semanticScores)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if results[0].Memory.ID != "sem-winner" {
		t.Errorf("expected sem-winner first in hybrid mode, got %s", results[0].Memory.ID)
	}
}

func TestScoreBreakdown_WeightsSumToOne(t *testing.T) {
	sum := weightText + weightRecency + weightConfidence + weightAccess
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("BM25-only weights must sum to 1.0, got %f", sum)
	}
}

func TestScoreBreakdown_HybridWeightsSumToOne(t *testing.T) {
	sum := weightBM25 + weightSemantic + weightRecency + weightConfidence + weightAccess
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("hybrid weights must sum to 1.0, got %f", sum)
	}
}

// --- effectiveConfidence ---

func TestEffectiveConfidence_Fresh(t *testing.T) {
	now := time.Now()
	m := &types.Memory{Confidence: 1.0, UpdatedAt: now}
	got := effectiveConfidence(m, now)
	if got < 0.99 {
		t.Errorf("fresh memory should have near-full confidence, got %f", got)
	}
}

func TestEffectiveConfidence_HalfLife(t *testing.T) {
	now := time.Now()
	// Memory last updated exactly one half-life ago.
	halfLife := time.Duration(confDecayHalfLifeMs) * time.Millisecond
	m := &types.Memory{Confidence: 1.0, UpdatedAt: now.Add(-halfLife)}
	got := effectiveConfidence(m, now)
	if got < 0.49 || got > 0.51 {
		t.Errorf("at half-life, confidence should be ~0.5, got %f", got)
	}
}

func TestEffectiveConfidence_Floor(t *testing.T) {
	now := time.Now()
	// Memory last updated 10 years ago — should hit the floor.
	m := &types.Memory{Confidence: 1.0, UpdatedAt: now.Add(-10 * 365 * 24 * time.Hour)}
	got := effectiveConfidence(m, now)
	if got != confDecayFloor {
		t.Errorf("very old memory should be at floor %f, got %f", confDecayFloor, got)
	}
}

func TestEffectiveConfidence_AccessedRecentlyResetsDecay(t *testing.T) {
	now := time.Now()
	halfLife := time.Duration(confDecayHalfLifeMs) * time.Millisecond
	accessedAt := now.Add(-time.Hour) // accessed 1 hour ago

	// Memory was created/updated long ago but accessed recently.
	m := &types.Memory{
		Confidence: 1.0,
		UpdatedAt:  now.Add(-halfLife * 3), // very old
		AccessedAt: &accessedAt,
	}
	got := effectiveConfidence(m, now)
	// Signal = accessed_at (1 hour ago) → virtually no decay
	if got < 0.99 {
		t.Errorf("recently accessed memory should have near-full confidence, got %f", got)
	}
}

func TestEffectiveConfidence_LowBaseConfidence(t *testing.T) {
	now := time.Now()
	halfLife := time.Duration(confDecayHalfLifeMs) * time.Millisecond
	m := &types.Memory{Confidence: 0.3, UpdatedAt: now.Add(-halfLife)}
	got := effectiveConfidence(m, now)
	// 0.3 * 0.5 = 0.15 — above floor
	if got < 0.14 || got > 0.16 {
		t.Errorf("want ~0.15 (0.3 * 0.5), got %f", got)
	}
}

func TestEffectiveConfidence_FloorClamp(t *testing.T) {
	now := time.Now()
	halfLife := time.Duration(confDecayHalfLifeMs) * time.Millisecond
	// 0.1 * 0.5 = 0.05 — below floor, should be clamped to 0.1
	m := &types.Memory{Confidence: 0.1, UpdatedAt: now.Add(-halfLife)}
	got := effectiveConfidence(m, now)
	if got != confDecayFloor {
		t.Errorf("want floor %f, got %f", confDecayFloor, got)
	}
}

func TestScoreCandidates_OldMemoryScoresLower(t *testing.T) {
	now := time.Now()
	halfLife := time.Duration(confDecayHalfLifeMs) * time.Millisecond
	candidates := []candidate{
		{memory: types.Memory{ID: "fresh", Confidence: 1.0, CreatedAt: now, UpdatedAt: now}, bm25Rank: -5.0},
		{memory: types.Memory{ID: "old", Confidence: 1.0, CreatedAt: now.Add(-halfLife * 4), UpdatedAt: now.Add(-halfLife * 4)}, bm25Rank: -5.0},
	}
	results := scoreCandidates(candidates, now, nil)
	scores := map[string]float64{}
	for _, r := range results {
		scores[r.Memory.ID] = r.Score
	}
	if scores["fresh"] <= scores["old"] {
		t.Errorf("fresh memory (%.3f) should outscore old memory (%.3f)", scores["fresh"], scores["old"])
	}
}

func TestScoreBreakdown_SemanticOnlyWeightsSumToOne(t *testing.T) {
	sum := weightSemanticOnly + weightRecency + weightConfidence + weightAccess
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("semantic-only weights must sum to 1.0, got %f", sum)
	}
}

func TestScoreCandidates_SemanticOnlyBeatsWeakFTS(t *testing.T) {
	// A semantic-only doc (bm25Rank=0) with high similarity should score higher
	// than a weakly-matched FTS doc once BM25 normalization makes the weak
	// match small.
	// With fts-strong at -10.0 and fts-weak at -0.5, maxRankMag=10.0.
	// fts-weak text component: 0.25*0.05 + 0.25*0.10 ≈ 0.038.
	// sem-only text component: 0.25*0   + 0.25*0.95 ≈ 0.238. sem-only wins.
	now := time.Now()
	candidates := []candidate{
		{memory: types.Memory{ID: "fts-strong", Confidence: 1.0, CreatedAt: now}, bm25Rank: -10.0},
		{memory: types.Memory{ID: "fts-weak", Confidence: 1.0, CreatedAt: now}, bm25Rank: -0.5},
		{memory: types.Memory{ID: "sem-only", Confidence: 1.0, CreatedAt: now}, bm25Rank: 0},
	}
	semanticScores := map[string]float64{
		"fts-strong": 0.10,
		"fts-weak":   0.10,
		"sem-only":   0.95,
	}

	results := scoreCandidates(candidates, now, semanticScores)
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}

	scores := make(map[string]float64, 3)
	for _, r := range results {
		scores[r.Memory.ID] = r.Score
	}
	if scores["sem-only"] <= scores["fts-weak"] {
		t.Errorf("sem-only score (%f) should exceed fts-weak score (%f)", scores["sem-only"], scores["fts-weak"])
	}
	if scores["fts-strong"] <= scores["sem-only"] {
		t.Errorf("fts-strong score (%f) should exceed sem-only score (%f)", scores["fts-strong"], scores["sem-only"])
	}
}

func TestScoreCandidates_MaxScoreWithSemanticOnly(t *testing.T) {
	// A semantic-only candidate with perfect scores should approach 1.0.
	now := time.Now()
	candidates := []candidate{
		{memory: types.Memory{ID: "perfect", Confidence: 1.0, AccessCount: 0, CreatedAt: now, UpdatedAt: now}, bm25Rank: 0},
	}
	semanticScores := map[string]float64{"perfect": 1.0}

	results := scoreCandidates(candidates, now, semanticScores)
	if results[0].Score > 1.0 {
		t.Errorf("score must not exceed 1.0, got %f", results[0].Score)
	}
	// weightBM25*0 + weightSemantic*1 + weightRecency*~1 + weightConfidence*1 + weightAccess*0
	// = 0 + 0.25 + ~0.25 + 0.15 + 0 ≈ 0.65+
	if results[0].Score < 0.60 {
		t.Errorf("perfect semantic doc should score above 0.60, got %f", results[0].Score)
	}
}
