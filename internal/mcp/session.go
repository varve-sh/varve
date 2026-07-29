package mcp

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/varve-sh/varve/internal/types"
)

// sessionTracker records MCP tool activity within a single server lifetime.
// All methods are safe for concurrent use.
type sessionTracker struct {
	// id identifies this session for the whole of the server's lifetime. One
	// MCP connection is one session (ADR-0004 D3, closed), and ADR-0001 D4
	// requires every agent-sourced write to carry it — an unattributable
	// decision is invisible to the attribution chain.
	id          string
	mu          sync.Mutex
	startTime   time.Time
	saved       []savedEntry
	recallCount int
	packCount   int
}

type savedEntry struct {
	id      string
	summary string
	memType types.MemoryType
}

// newSessionTracker takes the id the kernel minted for this connection, so the
// prose summary, every write's provenance and the session.started/ended events
// all name the same session (ADR-0004 §D3).
func newSessionTracker(id string) *sessionTracker {
	return &sessionTracker{id: id, startTime: time.Now().UTC()}
}

// sessionID returns the identifier stamped on this session's events.
func (t *sessionTracker) sessionID() string { return t.id }

func (t *sessionTracker) recordSave(id, summary string, memType types.MemoryType) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.saved = append(t.saved, savedEntry{id: id, summary: summary, memType: memType})
}

func (t *sessionTracker) recordRecall() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recallCount++
}

func (t *sessionTracker) recordPack() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.packCount++
}

// summary returns a human-readable session summary, or "" if nothing was saved.
func (t *sessionTracker) summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.saved) == 0 {
		return ""
	}

	duration := time.Since(t.startTime).Round(time.Minute)
	if duration < time.Minute {
		duration = time.Minute
	}

	var parts []string
	for _, e := range t.saved {
		short := truncateStr(e.summary, 60)
		parts = append(parts, fmt.Sprintf("%q [%s]", short, e.memType))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Session %s (%s): saved %d %s — %s.",
		t.startTime.Format("2006-01-02T15:04Z"),
		formatDuration(duration),
		len(t.saved),
		plural(len(t.saved), "memory", "memories"),
		strings.Join(parts, ", "),
	)
	if t.recallCount > 0 {
		fmt.Fprintf(&b, " Recalled %d %s.",
			t.recallCount,
			plural(t.recallCount, "time", "times"),
		)
	}
	return b.String()
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
