package pack

import (
	"strings"
	"testing"
)

func TestProposedFooter(t *testing.T) {
	if got := ProposedFooter(nil, 0); got != "" {
		t.Errorf("no proposals must render nothing, got %q", got)
	}
	got := ProposedFooter([]string{"01J2H", "01J1G"}, 0)
	for _, want := range []string{
		"proposed decisions touching these files: 2", "01J2H", "01J1G", "decision accept",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("footer %q missing %q", got, want)
		}
	}

	// §P8's truncation rule: ids are dropped, the count never is.
	truncated := ProposedFooter([]string{"01AAA", "01BBB", "01CCC", "01DDD"}, 2)
	if !strings.Contains(truncated, ": 4") {
		t.Errorf("the count must survive truncation: %q", truncated)
	}
	if strings.Contains(truncated, "01CCC") || strings.Contains(truncated, "01DDD") {
		t.Errorf("ids beyond maxIDs must be dropped: %q", truncated)
	}
	if !strings.Contains(truncated, "+2 more") {
		t.Errorf("truncation must say how many were dropped: %q", truncated)
	}
}
