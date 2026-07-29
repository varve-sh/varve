#!/usr/bin/env sh
# Runs the recall benchmark on this tree and on the last pre-v2 commit, so
# ADR-0001 falsifier 3's latency clause — "recall-merge latency measurably
# regresses vs. v1" — can be re-checked in one command instead of by hand.
#
# The v1 side has no benchmark of its own: that tree predates this one. The
# script materialises a worktree of the pre-v2 commit and generates the same
# benchmark body into it, deriving both from BENCH_BODY below so the two sides
# cannot drift apart silently.
#
# Usage:  scripts/bench-recall-vs-v1.sh [benchtime] [count]
# Note:   this measures a synthetic corpus. The falsifier names the founder's
#         own database; until it is run against one, the clause is unfalsified
#         rather than passed (planning/decisions-log.md, 2026-07-28).
set -eu

BENCHTIME="${1:-100x}"
COUNT="${2:-3}"
V1_REF="${V1_REF:-f135a72}"   # last commit before the v2 schema landed
WORKTREE="${WORKTREE:-/tmp/varve-bench-v1}"

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

echo "=== this tree ($(git rev-parse --short HEAD)) ==="
go test ./internal/kernel/ -run XXX -bench BenchmarkRecall \
    -benchtime "$BENCHTIME" -count "$COUNT" | grep -E 'Benchmark|ns/op'

echo
echo "=== v1 baseline ($V1_REF) ==="
# `rm -rf` drops the directory but not git's admin record, and the trap below
# does not run if a previous run was killed — so without the prune, the next
# run fails with "missing but already registered worktree". This script exists
# precisely so the comparison survives being come back to later.
rm -rf "$WORKTREE"
git worktree prune
git worktree add -q --detach "$WORKTREE" "$V1_REF"
trap 'git worktree remove --force "$WORKTREE" >/dev/null 2>&1 || true' EXIT

# Same corpus, same query, same limit; only the constructor differs (that tree
# has no shared testProject constant).
sed -e 's/^func BenchmarkRecall(/func BenchmarkRecallV1(/' \
    -e 's/testProject/"proj-1"/' \
    internal/kernel/bench_test.go \
    | grep -v '^//' \
    > "$WORKTREE/internal/kernel/bench_v1_test.go"

cd "$WORKTREE"
go test ./internal/kernel/ -run XXX -bench BenchmarkRecallV1 \
    -benchtime "$BENCHTIME" -count "$COUNT" | grep -E 'Benchmark|ns/op'
