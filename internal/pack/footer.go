// Package pack holds the pieces shared by the tools that *volunteer* context
// at task start — `memory_pack` (ADR-0002) and `memory_context`.
//
// The boundary these tools sit on is ADR-0001 Amendment 3 / ADR-0002
// Amendment 2: a tool that volunteers context gives §P2's **structural**
// guarantee — a proposed decision never appears as content, only as a footer
// count — while a tool that answers an explicit query (`memory_recall`,
// `memory_get`) gives the advisory `PROPOSED` marker, permanently, because it
// is the surface through which proposals are reviewed at all. An advisory
// marker is a request to the reader; structural exclusion is a guarantee.
package pack

import (
	"fmt"
	"strings"
)

// ProposedFooter renders ADR-0002 §P8's proposed-decisions footer line:
//
//	-- proposed decisions touching these files: 2 (01J2H..., 01J1G...) — review with `memtrace decision accept`
//
// It is the shared contract between `memory_pack` and `memory_context`
// (decisions log, 2026-07-28 ruling): one serializer, so the two tools cannot
// drift into describing the same fact two ways.
//
// maxIDs caps how many ids are named: 0 means all of them, and a **negative**
// value means none — the count only. §P8's truncation rule is that a footer
// line drops **ids** when its reserve is exhausted and never drops the
// **count** — the count is the part that carries the meaning ("something
// binding-looking exists that you are not being shown"), and an id list is
// only an affordance for acting on it.
//
// Returns "" when there is nothing to report: a footer line that says "0" is
// noise in every pack that has no proposals, which is most of them.
func ProposedFooter(ids []string, maxIDs int) string {
	if len(ids) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- proposed decisions touching these files: %d", len(ids))
	if maxIDs == 0 || maxIDs > len(ids) {
		maxIDs = len(ids)
	}
	if maxIDs > 0 {
		shown := make([]string, 0, maxIDs)
		for _, id := range ids[:maxIDs] {
			shown = append(shown, id)
		}
		b.WriteString(" (" + strings.Join(shown, ", "))
		if maxIDs < len(ids) {
			fmt.Fprintf(&b, ", +%d more", len(ids)-maxIDs)
		}
		b.WriteString(")")
	}
	b.WriteString(" — not binding until accepted; review with `memtrace decision accept <id>`")
	return b.String()
}
