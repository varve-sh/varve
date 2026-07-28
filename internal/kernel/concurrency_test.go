package kernel

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// Multiple agents writing to one database file is the product's normal
// operating condition, not an edge case: the target segment runs several
// coding agents in parallel, each its own process with its own *sql.DB.
//
// withTx used a deferred BEGIN, and proposeTx/acceptTx both read before they
// write (topic_key holder lookup, evidence count). A deferred transaction that
// upgrades read→write gets SQLITE_BUSY_SNAPSHOT immediately — SQLite does not
// invoke the busy handler for that upgrade, so §D8's busy_timeout bought
// nothing on the hot path. OpenDB now opens with _txlock=immediate, which
// takes the write lock up front where busy_timeout does apply.
func TestConcurrentWriters_NoBusyErrors(t *testing.T) {
	const writers = 6
	const cycles = 40

	path := filepath.Join(t.TempDir(), "concurrent.db")
	setup, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(setup); err != nil {
		t.Fatal(err)
	}
	setup.Close()

	var mu sync.Mutex
	var failures []string

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()

			// One *sql.DB per writer — the real MCP/CLI shape, one process
			// per agent.
			db, err := OpenDB(path)
			if err != nil {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("writer %d open: %v", w, err))
				mu.Unlock()
				return
			}
			defer db.Close()
			if err := ApplySchema(db); err != nil {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("writer %d schema: %v", w, err))
				mu.Unlock()
				return
			}
			s := NewDecisionStore(db)

			for c := 0; c < cycles; c++ {
				in := DecisionInput{
					ProjectID: testProject,
					Title:     fmt.Sprintf("writer %d decision %d", w, c),
					Scope:     []string{"internal/**"},
					Source:    types.DecisionSourceAgent,
					SessionID: fmt.Sprintf("sess-%d", w),
					Evidence: []EvidenceInput{{
						Kind: types.EvidenceKindCommit,
						Ref:  fmt.Sprintf("sha-%d-%d", w, c),
						// AddedBy is required.
						AddedBy: types.ActorSystem,
					}},
				}
				d, err := s.Propose(in)
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("writer %d propose %d: %v", w, c, err))
					mu.Unlock()
					continue
				}
				if _, err := s.Accept(d.ID, AcceptOptions{}); err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("writer %d accept %d: %v", w, c, err))
					mu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()

	if len(failures) > 0 {
		locked := 0
		for _, f := range failures {
			if strings.Contains(f, "database is locked") || strings.Contains(f, "SQLITE_BUSY") {
				locked++
			}
		}
		t.Fatalf("%d of %d operations failed (%d with a lock error); first five:\n%s",
			len(failures), writers*cycles*2, locked,
			strings.Join(failures[:min(5, len(failures))], "\n"))
	}

	// Everything actually landed.
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM decisions WHERE status = 'active'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != writers*cycles {
		t.Errorf("%d active decisions, want %d", n, writers*cycles)
	}
}

// Concurrent readers must not be starved by the write-lock-up-front change.
func TestConcurrentReadersAndWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	s := NewDecisionStore(db)

	seed, err := s.Propose(DecisionInput{
		ProjectID: testProject, Title: "seed", Source: types.DecisionSourceAgent,
		SessionID: "s",
	})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var failures []string
	var wg sync.WaitGroup

	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rdb, err := OpenDB(path)
			if err != nil {
				return
			}
			defer rdb.Close()
			rs := NewDecisionStore(rdb)
			for i := 0; i < 30; i++ {
				if _, err := rs.GetDecision(seed.ID); err != nil {
					mu.Lock()
					failures = append(failures, "read: "+err.Error())
					mu.Unlock()
				}
			}
		}()
	}
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			wdb, err := OpenDB(path)
			if err != nil {
				return
			}
			defer wdb.Close()
			ws := NewDecisionStore(wdb)
			for i := 0; i < 30; i++ {
				if _, err := ws.Propose(DecisionInput{
					ProjectID: testProject,
					Title:     fmt.Sprintf("mixed %d %d", w, i),
					Source:    types.DecisionSourceAgent, SessionID: "s",
				}); err != nil {
					mu.Lock()
					failures = append(failures, "write: "+err.Error())
					mu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d failures under mixed load; first five:\n%s",
			len(failures), strings.Join(failures[:min(5, len(failures))], "\n"))
	}
}

// The DSN must survive a project path containing URL-significant characters.
// It carries foreign_keys, and a mangled query string silently reverts it to
// off — which is exactly the bug the DSN change fixed in the first place.
func TestOpenDB_HandlesAwkwardPaths(t *testing.T) {
	for _, name := range []string{
		"plain.db", "with space.db", "question?mark.db", "hash#tag.db",
		"amp&ersand.db", "percent%20.db", "plus+sign.db",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			db, err := OpenDB(path)
			if err != nil {
				t.Fatalf("OpenDB(%q): %v", path, err)
			}
			defer db.Close()
			if err := ApplySchema(db); err != nil {
				t.Fatalf("ApplySchema(%q): %v", path, err)
			}

			var fk int
			if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
				t.Fatal(err)
			}
			if fk != 1 {
				t.Errorf("foreign_keys = %d for path %q — the DSN was mangled", fk, path)
			}
			assertFileExists(t, path)
		})
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+escapeDSNPath(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'decisions'`).Scan(&n); err != nil {
		t.Fatalf("the database was not created where we asked: %v", err)
	}
	if n != 1 {
		t.Errorf("decisions table missing from %s", path)
	}
}
