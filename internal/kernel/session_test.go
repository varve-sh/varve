package kernel

import (
	"path/filepath"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// F17: recall.served was §D7-conformant but carried a NULL session_id on the
// CLI path, and session.started/session.ended were registered kinds nothing
// ever wrote. ADR-0002 §P11's join is recall.served → session window →
// diff.scope_match: with no window row it returns nothing, however conformant
// the payload is.

func sessionKernel(t *testing.T) *MemoryKernel {
	t.Helper()
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	k := New(filepath.Join(t.TempDir(), "s.db"), testProject)
	if err := k.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	return k
}

func eventsOfKind(t *testing.T, k *MemoryKernel, kind types.EventKind) []types.Event {
	t.Helper()
	evs, err := k.Decisions().Events(EventFilter{Kind: kind})
	if err != nil {
		t.Fatalf("events %s: %v", kind, err)
	}
	return evs
}

func TestSession_CLIRecallIsAttributableAndMarkedCLI(t *testing.T) {
	k := sessionKernel(t)
	id := util.GenerateID()
	k.SetSession(id, SessionAgentCLI, "")

	if _, _, err := k.Save(types.MemorySaveInput{
		Content: "The auth service runs in eu-west-1.", Type: types.MemoryTypeFact,
		Source: types.MemorySourceUser,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Recall(types.MemoryRecallInput{Query: "auth", Limit: 5}); err != nil {
		t.Fatal(err)
	}

	recalls := eventsOfKind(t, k, types.EventRecallServed)
	if len(recalls) != 1 {
		t.Fatalf("recall.served = %d, want 1", len(recalls))
	}
	if recalls[0].SessionID != id {
		t.Errorf("recall.served session_id = %q, want %q — a NULL id is neither "+
			"attributable nor identifiable as CLI", recalls[0].SessionID, id)
	}
	if recalls[0].Agent != SessionAgentCLI {
		t.Errorf("recall.served agent = %q, want %q (ADR-0004 §D3 excludes these "+
			"from coverage denominators, which requires being able to see them)",
			recalls[0].Agent, SessionAgentCLI)
	}

	// The window join needs a start row, written lazily on the first
	// session-scoped emission.
	started := eventsOfKind(t, k, types.EventSessionStarted)
	if len(started) != 1 || started[0].SessionID != id || started[0].Agent != SessionAgentCLI {
		t.Fatalf("session.started = %+v, want exactly one for this CLI session", started)
	}

	// Close ends the session, with the counts §D7 specifies.
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	k2 := New(k.dbPath, testProject)
	if err := k2.Open(); err != nil {
		t.Fatal(err)
	}
	defer k2.Close()
	ended := eventsOfKind(t, k2, types.EventSessionEnded)
	if len(ended) != 1 || ended[0].SessionID != id {
		t.Fatalf("session.ended = %+v, want exactly one for this CLI session", ended)
	}
	for key, want := range map[string]float64{"saves": 1, "recalls": 1, "packs": 0} {
		if got, _ := ended[0].Payload[key].(float64); got != want {
			t.Errorf("session.ended %s = %v, want %v", key, ended[0].Payload[key], want)
		}
	}
}

// A registered-but-silent invocation (`memtrace list`) writes no session rows:
// two events per invocation to say nothing happened is log noise, and §D3 only
// requires CLI operations to be identifiable when they emit something.
func TestSession_SilentInvocationWritesNoSessionRows(t *testing.T) {
	k := sessionKernel(t)
	k.SetSession(util.GenerateID(), SessionAgentCLI, "")

	if _, err := k.List(types.ListOptions{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}

	k2 := New(k.dbPath, testProject)
	if err := k2.Open(); err != nil {
		t.Fatal(err)
	}
	defer k2.Close()
	if evs := eventsOfKind(t, k2, types.EventSessionStarted); len(evs) != 0 {
		t.Errorf("session.started = %d, want 0", len(evs))
	}
	if evs := eventsOfKind(t, k2, types.EventSessionEnded); len(evs) != 0 {
		t.Errorf("session.ended = %d, want 0", len(evs))
	}
}

// The MCP shape: the connection opening is the session's start, whether or not
// a tool is ever called.
func TestSession_BeginSessionAnnouncesImmediately(t *testing.T) {
	k := sessionKernel(t)
	defer k.Close()

	id := k.BeginSession("mcp", "claude-opus")
	started := eventsOfKind(t, k, types.EventSessionStarted)
	if len(started) != 1 || started[0].SessionID != id {
		t.Fatalf("session.started = %+v, want one for %s", started, id)
	}
	if started[0].Agent != "mcp" || started[0].Model != "claude-opus" {
		t.Errorf("provenance = (%q, %q), want (mcp, claude-opus)",
			started[0].Agent, started[0].Model)
	}
	// Announcing twice is not possible for one session.
	k.ensureSession()
	if got := len(eventsOfKind(t, k, types.EventSessionStarted)); got != 1 {
		t.Errorf("session.started = %d after a second ensure, want 1", got)
	}
}
