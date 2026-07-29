package kernel

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// BenchmarkRecall measures the recall path ADR-0001 falsifier 3 names: "recall-
// merge latency measurably regresses vs. v1 on the founder's own DB".
//
// It exists so the number is reproducible by the next reader instead of being
// an unrepeatable claim in a report. Read what it is and is not:
//
//   - It measures *this* schema on a synthetic corpus of the stated shape
//     (400 notes + 100 decisions, embedder disabled, BM25 only).
//
//   - The v1 comparator is produced by `scripts/bench-recall-vs-v1.sh`, which
//     materialises a worktree of the last pre-v2 commit (f135a72, one
//     `memories` table) and generates the v1 benchmark *from this file*, so the
//     two sides cannot drift apart. One command, no hand-copying:
//
//         scripts/bench-recall-vs-v1.sh [benchtime] [count]
//
//     Measured 2026-07-28, Apple M4 Pro, 100x x3: v1 72.0/72.3/72.6 ms/op,
//     this tree 49.7/49.6/50.1 ms/op. No regression on this corpus — v2 is
//     faster, because the recall path's cost here is dominated by the write
//     transactions (access tracking, recall.served), not by the FTS merge.
//     (The reviewer checked the obvious confound: removing the DSN's
//     synchronous(NORMAL), which the v1 tree does not set, moves this side by
//     <1%. Not an fsync artefact.)
//   - It is still NOT the falsifier's test, which names "the founder's own DB".
//     A synthetic corpus cannot stand in for one, so the clause is recorded in
//     planning/decisions-log.md as unfalsified with a reproducible harness,
//     not as passed.
func BenchmarkRecall(b *testing.B) {
	b.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	k := New(filepath.Join(b.TempDir(), "bench.db"), testProject)
	if err := k.Open(); err != nil {
		b.Fatal(err)
	}
	defer k.Close()

	for i := 0; i < 400; i++ {
		if _, _, err := k.Save(types.MemorySaveInput{
			Content: fmt.Sprintf("note %d about the auth service and its session store", i),
			Type:    types.MemoryTypeFact,
			Source:  types.MemorySourceUser,
		}); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		if _, _, err := k.Save(types.MemorySaveInput{
			Content: fmt.Sprintf("Decision %d: handlers validate the auth header before "+
				"touching the session store, and the rationale is recorded here at some "+
				"length so the document lengths are realistic.", i),
			Type:      types.MemoryTypeDecision,
			Source:    types.MemorySourceUser,
			FilePaths: []string{"internal/**"},
		}); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := k.Recall(types.MemoryRecallInput{Query: "auth session", Limit: 10}); err != nil {
			b.Fatal(err)
		}
	}
}
