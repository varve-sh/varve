package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/types"
	"github.com/varve-sh/varve/internal/util"
)

// v1.5.4 added re-registration for a project whose config entry is missing, and
// minted a fresh project id to do it. Every read filters on project_id, so that
// leaves the store intact and every row in it invisible — and the branch exists
// precisely for the case where the store has data ("the DB came from another
// machine or the config was lost").
//
// This pins adoption of the existing id, on the shape of a real store: rows
// filed under one project_id that the config does not know about.
func TestInit_ReRegisterAdoptsTheStoresProjectID(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	t.Chdir(root)

	const existing = "01KMGG74J7EEZ32GCDTBHQD5ED"
	dir := filepath.Join(root, util.StoreDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A v2 store holding one decision, filed under an id the config never saw.
	k := kernel.New(filepath.Join(dir, "varve.db"), existing)
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.Save(types.MemorySaveInput{
		Content: "Handlers return errors, never panic.",
		Type:    types.MemoryTypeNote,
	}); err != nil {
		t.Fatal(err)
	}
	k.Close()

	out, err := runCmd(t, "init")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if !strings.Contains(out, existing) {
		t.Errorf("init did not report adopting the store's id:\n%s", out)
	}

	cfg := util.GetProjectConfig()
	entry, ok := cfg.Projects[root]
	if !ok {
		t.Fatal("init did not register the project")
	}
	if entry.ID != existing {
		t.Fatalf("registered id = %s, want the store's own %s — every row in the "+
			"store is now invisible", entry.ID, existing)
	}

	// The row must be readable through the registered id, which is the property
	// that actually matters.
	k2 := kernel.New(filepath.Join(dir, "varve.db"), entry.ID)
	if err := k2.Open(); err != nil {
		t.Fatal(err)
	}
	defer k2.Close()
	got, err := k2.List(types.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("the store's rows are invisible after re-registration")
	}
}

// An empty store has no id to adopt, so minting one is correct.
func TestInit_ReRegisterMintsAnIDOnlyWhenTheStoreIsEmpty(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	t.Chdir(root)

	dir := filepath.Join(root, util.StoreDir)
	os.MkdirAll(dir, 0o755)
	k := kernel.New(filepath.Join(dir, "varve.db"), "01KMGG74J7EEZ32GCDTBHQD5ED")
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	k.Close() // schema only, no rows

	out, err := runCmd(t, "init")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if !strings.Contains(out, "store was empty") {
		t.Errorf("expected init to say the store was empty:\n%s", out)
	}
	if _, ok := util.GetProjectConfig().Projects[root]; !ok {
		t.Error("init did not register the project")
	}
}

// ProjectIDInStore must read a v1 store too: the migration path runs after
// re-registration, so the id has to be adoptable before any schema is applied.
func TestProjectIDInStore_ReadsAV1StoreWithoutApplyingASchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memtrace.db")
	db, err := kernel.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE memories (
		id TEXT PRIMARY KEY, project_id TEXT NOT NULL, content TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories VALUES ('m1','01OLDPROJECTID','x')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	got, err := kernel.ProjectIDInStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "01OLDPROJECTID" {
		t.Errorf("ProjectIDInStore = %q, want the v1 store's id", got)
	}
}

// Guessing which of several projects owns a file would hide the others.
func TestProjectIDInStore_RefusesWhenTheStoreHoldsSeveralProjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "varve.db")
	db, err := kernel.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE memories (id TEXT PRIMARY KEY, project_id TEXT NOT NULL)`)
	db.Exec(`INSERT INTO memories VALUES ('a','p1'),('b','p2')`)
	db.Close()

	if _, err := kernel.ProjectIDInStore(path); err == nil {
		t.Fatal("ProjectIDInStore guessed between two projects instead of refusing")
	}
}
