package kernel

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// The FTS candidate query has a 1000× planning cliff, and it is invisible on a
// small store: SQLite will happily plan `SEARCH decisions USING
// idx_decisions_project_status` as the outer loop and re-run the MATCH once per
// row. Measured on 5,000 decisions with a query matching every row — 5.3 s with
// a plain JOIN, 5.5 ms with CROSS JOIN — which is ADR-0002 falsifier 5 firing
// by a factor of 35 on the packer, and the same cost on every recall.
//
// The plan is asserted rather than the wall clock: the plan is deterministic
// and the clock is not. The latency guard below is deliberately loose — it
// exists to catch the 1000× regression, not to police milliseconds.
func TestSearchFTS_PlanIsDrivenByTheFTSTable(t *testing.T) {
	db := setupTestDB(t)

	rows, err := db.Query("EXPLAIN QUERY PLAN "+searchFTSQuery,
		"session", "proj1", 30, "session", "proj1", 30)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var steps []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, detail)
	}
	if len(steps) == 0 {
		t.Fatal("no query plan returned")
	}

	// Each arm must scan its FTS table first and look the row table up by
	// rowid. A plan that searches `decisions`/`notes` by index *before*
	// reaching the virtual table is the pathological shape.
	for i, step := range steps {
		if !strings.Contains(step, "VIRTUAL TABLE") {
			continue
		}
		for j := 0; j < i; j++ {
			if strings.Contains(steps[j], "SEARCH d USING INDEX") ||
				strings.Contains(steps[j], "SEARCH n USING INDEX") {
				t.Errorf("the row table drives the join, so MATCH re-runs per row:\n  %s",
					strings.Join(steps, "\n  "))
			}
		}
	}
	if !strings.Contains(strings.Join(steps, " "), "VIRTUAL TABLE") {
		t.Errorf("the FTS tables are not being scanned at all:\n  %s", strings.Join(steps, "\n  "))
	}
}

func TestSearchFTS_StaysFastWhenEverythingMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("populates 2,000 decisions")
	}
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	k := New(filepath.Join(t.TempDir(), "fts.db"), testProject)
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	for i := 0; i < 2000; i++ {
		if _, err := k.Decisions().ProposeAccepted(DecisionInput{
			ProjectID: testProject,
			Title:     fmt.Sprintf("Decision %04d about session handling", i),
			Body:      "the session store must not be reachable without a validated header",
			Scope:     []string{"internal/auth/**"},
			Source:    types.DecisionSourceUser,
			Evidence: []EvidenceInput{{
				Kind: types.EvidenceKindCommit, Ref: fmt.Sprintf("s%04d", i), AddedBy: types.ActorHuman,
			}},
		}, AcceptOptions{Actor: types.ActorHuman}); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	res, err := k.store.SearchFTS("session store header", testProject, 66)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 66 {
		t.Fatalf("pool = %d rows, want the full 66 — the query matched everything", len(res))
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("a query matching every row took %v; the join order has regressed "+
			"(this was 5.3s before the CROSS JOIN, on 5,000 rows)", elapsed)
	}
}
