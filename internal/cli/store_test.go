package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/varve-sh/varve/internal/util"
)

// seedLegacyStore writes a pre-rename store: .memtrace/memtrace.db plus the
// sidecars SQLite needs and the files init creates alongside it.
func seedLegacyStore(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, util.LegacyStoreDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memtrace.db", "memtrace.db-wal", "memtrace.db-shm", "observer.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The failure this guards is the one this branch already shipped once: a store
// that exists but is invisible because the code looks only in the new location.
func TestResolveStore_FindsAPreRenameStoreInsteadOfReportingNothing(t *testing.T) {
	root := t.TempDir()
	seedLegacyStore(t, root)

	path, legacy := util.ResolveStore(root)
	if !legacy {
		t.Fatalf("a pre-rename store was not recognised as legacy: %s", path)
	}
	if !strings.HasSuffix(path, filepath.Join(util.LegacyStoreDir, "memtrace.db")) {
		t.Fatalf("resolved to %s, want the legacy store", path)
	}
	if util.FindProjectRoot(root) != root {
		t.Fatalf("FindProjectRoot did not recognise a project holding only %s/", util.LegacyStoreDir)
	}
	// Resolution alone is not enough: an invisible fallback is how a user ends
	// up not knowing which store they are reading.
	if n := util.StoreNotice(root); !strings.Contains(n, "varve store move") {
		t.Errorf("legacy notice does not name the command that fixes it: %q", n)
	}
}

func TestResolveStore_PrefersTheCurrentLocationAndStaysQuiet(t *testing.T) {
	root := t.TempDir()
	seedLegacyStore(t, root)
	if err := os.MkdirAll(filepath.Join(root, util.StoreDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, util.StoreDir, "varve.db"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, legacy := util.ResolveStore(root)
	if legacy || !strings.HasSuffix(path, filepath.Join(util.StoreDir, "varve.db")) {
		t.Fatalf("resolved to %s (legacy=%v), want the current location", path, legacy)
	}
	if n := util.StoreNotice(root); n != "" {
		t.Errorf("notice printed for a store already in the current location: %q", n)
	}
}

// A fresh project must not be told about a legacy store it does not have.
func TestResolveStore_FreshProjectGetsTheCurrentLocation(t *testing.T) {
	root := t.TempDir()
	path, legacy := util.ResolveStore(root)
	if legacy || !strings.HasSuffix(path, filepath.Join(util.StoreDir, "varve.db")) {
		t.Fatalf("fresh project resolved to %s (legacy=%v)", path, legacy)
	}
	if n := util.StoreNotice(root); n != "" {
		t.Errorf("fresh project got a legacy notice: %q", n)
	}
}

func TestStoreMove_MovesTheDatabaseWithItsSidecars(t *testing.T) {
	root := t.TempDir()
	seedLegacyStore(t, root)
	t.Chdir(root)

	out, err := runCmd(t, "store", "move")
	if err != nil {
		t.Fatalf("store move: %v\n%s", err, out)
	}
	for _, name := range []string{"varve.db", "varve.db-wal", "varve.db-shm", "observer.log"} {
		if _, err := os.Stat(filepath.Join(root, util.StoreDir, name)); err != nil {
			t.Errorf("%s did not move: %v", name, err)
		}
	}
	// A WAL left behind hands SQLite a database with a missing write-ahead log.
	if _, err := os.Stat(filepath.Join(root, util.LegacyStoreDir, "memtrace.db-wal")); err == nil {
		t.Error("the WAL was left in the old directory")
	}
	if _, legacy := util.ResolveStore(root); legacy {
		t.Error("resolution still reports legacy after a move")
	}
	if n := util.StoreNotice(root); n != "" {
		t.Errorf("notice survives the move it asked for: %q", n)
	}
}

// Two stores means two histories. Picking one silently is how attribution
// loses events, so the command refuses.
func TestStoreMove_RefusesWhenBothStoresExist(t *testing.T) {
	root := t.TempDir()
	seedLegacyStore(t, root)
	if err := os.MkdirAll(filepath.Join(root, util.StoreDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, util.StoreDir, "varve.db"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	if _, err := runCmd(t, "store", "move"); err == nil {
		t.Fatal("store move overwrote or merged an existing store")
	}
	// The legacy store must still be intact after the refusal.
	if _, err := os.Stat(filepath.Join(root, util.LegacyStoreDir, "memtrace.db")); err != nil {
		t.Errorf("the refused move damaged the legacy store: %v", err)
	}
}

func TestStoreMove_DryRunChangesNothing(t *testing.T) {
	root := t.TempDir()
	seedLegacyStore(t, root)
	t.Chdir(root)

	out, err := runCmd(t, "store", "move", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would move") {
		t.Errorf("dry run did not say it was a dry run: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, util.LegacyStoreDir, "memtrace.db")); err != nil {
		t.Errorf("dry run moved the database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, util.StoreDir, "varve.db")); err == nil {
		t.Error("dry run created the new store")
	}
}
