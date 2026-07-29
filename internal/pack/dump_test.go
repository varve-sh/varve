package pack

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDumpPacks writes representative packs for ADR-0002 falsifier 1's offline
// re-tokenization (open question 6: "a tokenizer in CI, not in the product").
// Skipped unless DUMP_PACKS names a directory, so the corpus is reproducible
// without the product ever gaining a tokenizer dependency.
//
//	mkdir /tmp/packs && DUMP_PACKS=/tmp/packs go test ./internal/pack/ -run TestDumpPacks
//	# then, in a throwaway module with github.com/pkoukk/tiktoken-go:
//	#   actual := len(tk.Encode(string(fileBytes), nil, nil))
//
// Measured 2026-07-28 against cl100k_base and o200k_base — estimate ÷ actual:
//
//	golden (ids, globs, short prose)  1048 B  313 tok  est 350  ratio 1.118
//	code/identifier-heavy             2664 B  688 tok  est 888  ratio 1.291
//	prose-heavy                       2868 B  670 tok  est 956  ratio 1.427
//
// Every ratio is ≥ 1.0: the estimate never undershot, so no pack could exceed
// its budget under either tokenizer, which is what falsifier 1 tests. The prose
// case exceeds §P7's stated 1.0–1.35× band; the direction is the safe one
// (utilization, not the ceiling) and it lands exactly on the Consequences
// section's predicted "~70–80% utilization", but the §P7 band as written is
// optimistic on prose-heavy content.
func TestDumpPacks(t *testing.T) {
	dir := os.Getenv("DUMP_PACKS")
	if dir == "" {
		t.Skip("set DUMP_PACKS=<dir> to write the re-tokenization corpus")
	}

	corpora := map[string]*fakeSource{
		"golden": goldenSource(),
	}
	// A realistic prose-heavy pack.
	prose := &fakeSource{}
	for i := 0; i < 6; i++ {
		d := decision(fmt.Sprintf("01PROSE%019d", i),
			fmt.Sprintf("Refresh tokens rotate on every use, and reuse revokes the family (%d)", i),
			withScope("internal/auth/**"))
		d.Body = "We rotate refresh tokens on every use because a stolen token is otherwise " +
			"valid until expiry. Reuse detection revokes the entire family, which converts " +
			"silent theft into a visible logout. The cost is that a client racing two " +
			"refreshes will occasionally be logged out; we accept that."
		prose.decisions = append(prose.decisions, d)
	}
	corpora["prose"] = prose

	// A code/identifier-heavy pack, which is where bytes/3 is tightest.
	code := &fakeSource{}
	for i := 0; i < 6; i++ {
		d := decision(fmt.Sprintf("01CODE%020d", i),
			fmt.Sprintf("internal/kernel/store.go must not import internal/mcp (%d)", i),
			withScope("internal/kernel/**", "internal/mcp/**"))
		d.Body = "func (s *MemoryStore) SearchFTS(query string, projectID string, limit int) " +
			"([]types.FTSResult, error) — the projection is bridged in exactly one place; " +
			"see memoryProjection, statusFilter, liveDecisionStatuses, idx_decisions_project_status."
		code.decisions = append(code.decisions, d)
	}
	corpora["code"] = code

	for name, src := range corpora {
		res, err := Build(src, "p", Request{
			FilePaths: []string{"internal/auth/session.go", "internal/kernel/store.go"},
			Task:      "add refresh-token rotation",
			Now:       time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		path := dir + "/" + name + ".pack.txt"
		if err := os.WriteFile(path, []byte(res.Text), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes, estimate %d", path, len(res.Text), Estimate(res.Text))
		_ = strings.TrimSpace(res.Text)
	}
}
