package pack

// EstimatorVersion labels the token estimate everywhere it surfaces
// (`pack.served.payload.estimator`). ADR-0002 §P7: counts are estimates,
// comparable within an estimator version, never ground truth — so the version
// travels with the number and a recalibration is a version bump, not an API
// change.
const EstimatorVersion = "bytes/3 v1"

// bytesPerToken is §P7's divisor. Modern BPE vocabularies average ~3.5–4.5
// bytes/token on English prose and ~3–3.5 on code and dense identifiers, so
// dividing by 3 overestimates by roughly 0–35% on the content this tool emits.
// That is the correct direction: the estimate is the guard, so estimator error
// eats into utilization and never through the ceiling.
const bytesPerToken = 3

// Measured against real tokenizers on 2026-07-28 (cl100k_base and o200k_base
// via tiktoken-go, run out of tree — see TestDumpPacks): the estimate is
// 1.12x the true count on id/glob-heavy packs, 1.29x on code-heavy ones and
// 1.43x on prose. Never below 1.0, which is the property that matters: no pack
// can exceed its budget under either tokenizer. §P7's stated 1.0-1.35x band is
// optimistic at the prose end; the error lands on utilization, exactly where
// the ADR's Consequences section put it ("~70-80% of what exact counting could
// achieve").
//
// Estimate returns E(s) = ceil(utf8_byte_length(s) / 3).
//
// Deliberately not a real tokenizer (§P7, rejected alternative D): exact counts
// need the model's own vocab, varve cannot know which model is connected, MCP
// does not expose it, and vocabularies version faster than this product will.
// Precision against the wrong vocab is precision theater; a conservative
// versioned estimate buys the one property that matters — the ceiling is never
// breached.
func Estimate(s string) int {
	n := len(s) // len() on a string is its UTF-8 byte length, which is what §P7 says
	return (n + bytesPerToken - 1) / bytesPerToken
}
