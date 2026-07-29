package kernel

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varve-sh/varve/internal/util"
)

// idempotencyBudget is ADR-0001 Amendment 6 / ADR-0005 open question 1's
// pre-registered trigger: the partial index on (project_id, source_ref) is
// deliberately deferred, and ships as the next migration iff idempotency
// checking costs more than this for a 1,000-candidate batch.
//
// The trigger is only worth pre-registering if something measures it, which is
// what this test is for. Spending a migration ahead of a measured need is the
// discipline ADR-0001's falsifier 6′ argues against; letting a slow batch pass
// unmeasured is the failure on the other side.
const idempotencyBudget = 5 * time.Second

// storeMultiple is F50's correction: the cost of the SELECT-before-insert path
// is O(candidates × store rows), because `source_ref` is unindexed by design
// (ADR-0001 A6.2 defers the index). A fixture holding store size equal to
// candidate count measures the favourable diagonal and would keep reporting a
// comfortable number while the real shape — a large corpus re-imported in
// batches — crossed the trigger.
//
// 20 × 1,000 candidates ≈ a 20k-row claude-mem store, which is the volume the
// wave-1 importers are aimed at.
const storeMultiple = 20

func TestImportIdempotency_CostPerThousandCandidates(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement test")
	}
	k := setupTestKernel(t)

	// Seed the store an order of magnitude above the candidate count, so the
	// per-lookup scan cost is what the measurement measures.
	// Seeded with raw inserts rather than through ImportBatch: seeding via the
	// import path would itself pay the quadratic cost this test is measuring,
	// and the seed is not the measurement.
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := k.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000*(storeMultiple-1); i++ {
		if _, err := tx.Exec(`INSERT INTO notes
			(id, project_id, content, source, source_ref, status, created_at, updated_at)
			VALUES (?,?,?, 'import', ?, 'active', ?, ?)`,
			util.GenerateID(), k.projectID,
			fmt.Sprintf("Unrelated session observation %d", i),
			fmt.Sprintf("ballast:%d", i), now, now); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	const n = 1000
	candidates := make([]ImportCandidate, 0, n)
	for i := 0; i < n; i++ {
		c := ImportCandidate{
			SourceRef: fmt.Sprintf("claude-mem:%d", i),
			Content:   fmt.Sprintf("Session observation number %d about the auth refactor", i),
		}
		if i%4 == 0 {
			c.AsDecision = true
			c.Title = fmt.Sprintf("Rule %d", i)
		}
		candidates = append(candidates, c)
	}

	if _, err := k.ImportBatch("claude-mem", candidates, false); err != nil {
		t.Fatal(err)
	}

	// The worst case for the SELECT-before-insert path: every candidate is
	// already present, so every one of them pays the full lookup and no insert
	// amortizes it.
	start := time.Now()
	res, err := k.ImportBatch("claude-mem", candidates, false)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if len(res.Skipped) != n {
		t.Fatalf("re-import skipped %d of %d — the measurement is not measuring idempotency",
			len(res.Skipped), n)
	}
	var storeRows int
	if err := k.db.QueryRow(`SELECT (SELECT COUNT(*) FROM notes) + (SELECT COUNT(*) FROM decisions)`).
		Scan(&storeRows); err != nil {
		t.Fatal(err)
	}
	if storeRows < n*storeMultiple/2 {
		t.Fatalf("store holds %d rows for %d candidates — the fixture is back on the diagonal",
			storeRows, n)
	}
	t.Logf("idempotency checking for %d candidates against %d store rows: %v (trigger: %v)",
		n, storeRows, elapsed, idempotencyBudget)
	if elapsed > idempotencyBudget {
		t.Errorf("idempotency checking took %v for %d candidates, past the %v trigger — "+
			"ship the pre-written (project_id, source_ref) index as the next migration "+
			"(ADR-0001 Amendment 6)", elapsed, n, idempotencyBudget)
	}
}

// A7.4 made this normative for every index-adding migration: assert the plan,
// against a realistically-sized store, never an empty one. Migration 6 exists
// only to be chosen by these two queries, and the shape A6.2 specified was not
// chosen at all — so the plan, not the clock, is what proves it works.
func TestMigration6_IndexIsActuallyChosen(t *testing.T) {
	k := New(filepath.Join(t.TempDir(), "plan.db"), "proj")
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	for _, tc := range []struct{ label, query, want string }{
		{"notes source_ref lookup",
			`SELECT COUNT(*) FROM notes WHERE project_id = ? AND source_ref = ?`,
			"idx_notes_source_ref"},
		{"decisions source_ref lookup",
			`SELECT id, status FROM decisions WHERE project_id = ? AND source_ref = ?`,
			"idx_decisions_source_ref"},
	} {
		rows, err := k.db.Query("EXPLAIN QUERY PLAN "+tc.query, "proj", "ref")
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		var plan string
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		rows.Close()
		if !strings.Contains(plan, tc.want) {
			t.Errorf("%s does not use %s — migration 6 is inert:\n%s",
				tc.label, tc.want, plan)
		}
	}
}
