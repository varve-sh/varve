package kernel

import (
	"fmt"
	"testing"
	"time"
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

func TestImportIdempotency_CostPerThousandCandidates(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement test")
	}
	k := setupTestKernel(t)

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
	t.Logf("idempotency checking for %d candidates: %v (trigger: %v)", n, elapsed, idempotencyBudget)
	if elapsed > idempotencyBudget {
		t.Errorf("idempotency checking took %v for %d candidates, past the %v trigger — "+
			"ship the pre-written (project_id, source_ref) index as the next migration "+
			"(ADR-0001 Amendment 6)", elapsed, n, idempotencyBudget)
	}
}
