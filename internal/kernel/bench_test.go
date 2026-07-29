package kernel

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/varve-sh/varve/internal/pack"
	"github.com/varve-sh/varve/internal/types"
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
//     scripts/bench-recall-vs-v1.sh [benchtime] [count]
//
//     Measured 2026-07-28, Apple M4 Pro, 100x x3: v1 72.0/72.3/72.6 ms/op,
//     this tree 49.7/49.6/50.1 ms/op. No regression on this corpus — v2 is
//     faster, because the recall path's cost here is dominated by the write
//     transactions (access tracking, recall.served), not by the FTS merge.
//     (The reviewer checked the obvious confound: removing the DSN's
//     synchronous(NORMAL), which the v1 tree does not set, moves this side by
//     <1%. Not an fsync artefact.)
//
//   - It is still NOT the falsifier's test, which names "the founder's own DB".
//     A synthetic corpus cannot stand in for one, so the clause is recorded in
//     planning/decisions-log.md as unfalsified with a reproducible harness,
//     not as passed.
func BenchmarkRecall(b *testing.B) {
	b.Setenv("VARVE_EMBED_PROVIDER", "disabled")
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

// BenchmarkPack measures ADR-0002 §P13's envelope: p95 < 150 ms without an
// embedder on a store of ≤5,000 decisions. Falsifier 5 fires above that.
//
// The corpus is the falsifier's own worst case — 5,000 decisions, all live, a
// third of them scope-matching the request — so the number is the ceiling of
// what a repo-local store can cost, not a favourable sample.
func BenchmarkPack(b *testing.B) {
	b.Setenv("VARVE_EMBED_PROVIDER", "disabled")
	k := New(filepath.Join(b.TempDir(), "packbench.db"), testProject)
	if err := k.Open(); err != nil {
		b.Fatal(err)
	}
	defer k.Close()

	scopes := []string{"internal/auth/**", "internal/kernel/**", "docs/**"}
	for i := 0; i < 5000; i++ {
		if _, err := k.Decisions().ProposeAccepted(DecisionInput{
			ProjectID: testProject,
			Title:     fmt.Sprintf("Decision %04d about the auth and session handling", i),
			Body: fmt.Sprintf("Rationale %04d: %s", i,
				"the session store must not be reachable without a validated header, "+
					"and the reasoning is recorded here at realistic length."),
			Scope:  []string{scopes[i%len(scopes)]},
			Source: types.DecisionSourceUser,
			Evidence: []EvidenceInput{{
				Kind: types.EvidenceKindCommit, Ref: fmt.Sprintf("sha%04d", i), AddedBy: types.ActorHuman,
			}},
		}, AcceptOptions{Actor: types.ActorHuman}); err != nil {
			b.Fatal(err)
		}
	}

	req := pack.Request{
		FilePaths: []string{"internal/auth/session.go", "internal/auth/middleware.go"},
		Task:      "add refresh token rotation",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := k.Pack(req)
		if err != nil {
			b.Fatal(err)
		}
		if res.ItemCount == 0 {
			b.Fatal("empty pack: the benchmark is measuring nothing")
		}
	}
}
