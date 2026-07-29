package retrieval

import (
	"math"
	"time"

	"github.com/varve-sh/varve/internal/types"
)

// Scoring weights — each set must sum to 1.0.
const (
	// BM25-only mode
	weightText       = 0.50
	weightRecency    = 0.25
	weightConfidence = 0.15
	weightAccess     = 0.10

	// Hybrid mode: BM25 + semantic (text weight split equally between the two)
	weightBM25     = 0.25
	weightSemantic = 0.25
	// weightRecency, weightConfidence, weightAccess reused — total = 0.25+0.25+0.25+0.15+0.10 = 1.0

	// Semantic-only mode (mirrors weightText)
	weightSemanticOnly = 0.50
	// weightRecency, weightConfidence, weightAccess reused — total = 0.50+0.25+0.15+0.10 = 1.0
)

// recencyHalfLifeMs is the half-life for recency decay: 30 days.
const recencyHalfLifeMs = float64(30 * 24 * 60 * 60 * 1000)

// confDecayHalfLifeMs is the half-life for confidence decay: 90 days.
// A memory not accessed or updated for 90 days loses half its confidence weight.
const confDecayHalfLifeMs = float64(90 * 24 * 60 * 60 * 1000)

// confDecayFloor is the minimum effective confidence — memories never become
// completely irrelevant just from age.
const confDecayFloor = 0.1

// effectiveConfidence returns the time-decayed confidence for scoring.
// It does NOT modify the stored confidence value.
//
// The last-signal time is max(updated_at, accessed_at): either the user
// confirmed/edited the memory, or an agent recently recalled it.
// Confidence halves every 90 days without such a signal, down to confDecayFloor.
func effectiveConfidence(m *types.Memory, now time.Time) float64 {
	return EffectiveConfidence(m.Confidence, m.UpdatedAt, m.AccessedAt, now)
}

// EffectiveConfidence is the decay in primitive form, so callers holding a
// types.Decision or types.Note (which are not types.Memory) compute the same
// number from the same code. ADR-0002 §P3 requires the packer to reuse this
// verbatim: two implementations of a decay curve is two behaviours.
func EffectiveConfidence(confidence float64, updatedAt time.Time, accessedAt *time.Time, now time.Time) float64 {
	signal := updatedAt
	if accessedAt != nil && accessedAt.After(signal) {
		signal = *accessedAt
	}
	ageMs := float64(now.Sub(signal).Milliseconds())
	if ageMs < 0 {
		ageMs = 0
	}
	decayed := confidence * math.Pow(0.5, ageMs/confDecayHalfLifeMs)
	if decayed < confDecayFloor {
		decayed = confDecayFloor
	}
	return decayed
}

// Recency is the 30-day half-life decay on a creation time (ADR-0002 §P3's
// recency_score, and recall's own recency term).
func Recency(createdAt, now time.Time) float64 {
	ageMs := float64(now.Sub(createdAt).Milliseconds())
	if ageMs < 0 {
		ageMs = 0
	}
	return math.Pow(0.5, ageMs/recencyHalfLifeMs)
}

// NormalizedBM25 maps a raw FTS5 rank (negative; more negative is better) onto
// 0–1 by the largest magnitude in the same candidate pool. Pools from different
// FTS tables have different corpus statistics, so this is only meaningful
// within one pool — see the §P11 confound entry in the decisions log.
func NormalizedBM25(rank, maxMagnitude float64) float64 {
	if maxMagnitude <= 0 {
		return 0
	}
	return math.Abs(rank) / maxMagnitude
}

// HybridText combines normalized BM25 with a cosine similarity exactly as the
// recall scorer does: the mean, with the cosine clamped at 0.
func HybridText(bm25Norm, semantic float64) float64 {
	if semantic < 0 {
		semantic = 0
	}
	return (bm25Norm + semantic) / 2.0
}

// candidate holds a memory and its raw BM25 rank for scoring.
type candidate struct {
	memory   types.Memory
	bm25Rank float64 // negative — more negative = better match
}

// scoreCandidates computes a combined relevance score for each candidate.
// semanticScores is an optional map of memory ID → cosine similarity (0–1).
// When nil, BM25-only weights are used.
func scoreCandidates(candidates []candidate, now time.Time, semanticScores map[string]float64) []types.ScoredMemory {
	if len(candidates) == 0 {
		return nil
	}

	hybrid := len(semanticScores) > 0

	// Find the largest BM25 magnitude for normalization.
	maxRankMag := 0.0
	for _, c := range candidates {
		mag := math.Abs(c.bm25Rank)
		if mag > maxRankMag {
			maxRankMag = mag
		}
	}

	results := make([]types.ScoredMemory, 0, len(candidates))
	for _, c := range candidates {
		// Text relevance: normalize BM25 to 0–1
		bm25Norm := NormalizedBM25(c.bm25Rank, maxRankMag)

		// Recency: exponential decay from creation time
		recency := Recency(c.memory.CreatedAt, now)

		// Confidence: decayed by time since last access or update
		confidence := effectiveConfidence(&c.memory, now)

		// Access frequency: logarithmic scaling, capped at 1.0
		accessFreq := math.Min(1.0, math.Log2(float64(c.memory.AccessCount)+1)/10.0)

		var score, textRelevance float64
		if hybrid {
			semScore := semanticScores[c.memory.ID] // 0 if not present
			if semScore < 0 {
				semScore = 0 // clamp: cosine can be negative for dissimilar vectors
			}
			textRelevance = HybridText(bm25Norm, semScore)
			score = weightBM25*bm25Norm + weightSemantic*semScore +
				weightRecency*recency + weightConfidence*confidence + weightAccess*accessFreq
		} else {
			textRelevance = bm25Norm
			score = weightText*bm25Norm +
				weightRecency*recency + weightConfidence*confidence + weightAccess*accessFreq
		}

		results = append(results, types.ScoredMemory{
			Memory: c.memory,
			Score:  score,
			ScoreBreakdown: types.ScoreBreakdown{
				TextRelevance:   textRelevance,
				Recency:         recency,
				Confidence:      confidence,
				AccessFrequency: accessFreq,
			},
		})
	}
	return results
}
