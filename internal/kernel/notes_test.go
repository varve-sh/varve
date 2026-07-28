package kernel

import (
	"errors"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/types"
)

func newNoteStore(t *testing.T) *NoteStore {
	t.Helper()
	return NewNoteStore(freshDB(t))
}

func TestNoteStore_InsertAndGet(t *testing.T) {
	s := newNoteStore(t)
	n, err := s.Insert(NoteInput{
		ProjectID: testProject,
		Content:   "the CI runner is arm64",
		Summary:   "CI is arm64",
		Tags:      []string{"ci"},
		FilePaths: []string{".github/workflows/ci.yml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != types.MemoryStatusActive || n.Confidence != 1.0 {
		t.Errorf("defaults not applied: %+v", n)
	}

	got, err := s.Get(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != n.Content || len(got.Tags) != 1 || len(got.FilePaths) != 1 {
		t.Errorf("round trip lost data: %+v", got)
	}

	if _, err := s.Get("nope"); !errors.Is(err, types.ErrMemoryNotFound) {
		t.Errorf("err = %v, want ErrMemoryNotFound", err)
	}
}

// Notes are deliberately ungoverned: nothing they do writes to the event log
// (ADR-0001 D1).
func TestNoteStore_WritesNoEvents(t *testing.T) {
	db := freshDB(t)
	ns := NewNoteStore(db)
	ds := NewDecisionStore(db)

	n, err := ns.Insert(NoteInput{ProjectID: testProject, Content: "a note"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.SetStatus(n.ID, types.MemoryStatusStale); err != nil {
		t.Fatal(err)
	}
	evs, err := ds.Events(EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Errorf("notes must not write to the event log, got %d events", len(evs))
	}
}

// ADR-0001 Amendment 1, same-class audit item 3: idx_notes_topic_key is unique
// among *active* notes, so a stale note's key can be taken while it is asleep.
// Reactivating it must fail with a holder-naming error, not a raw constraint
// abort — the mirror of ErrTopicKeyHeld.
func TestNoteStore_ReactivationIsRejectedWhenTheKeyIsHeld(t *testing.T) {
	s := newNoteStore(t)

	first, err := s.Insert(NoteInput{
		ProjectID: testProject, Content: "the original", TopicKey: "deploy-target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(first.ID, types.MemoryStatusStale); err != nil {
		t.Fatal(err)
	}

	// With the first note stale, the key is free.
	second, err := s.Insert(NoteInput{
		ProjectID: testProject, Content: "the replacement", TopicKey: "deploy-target",
	})
	if err != nil {
		t.Fatalf("a stale note must not block a new active note: %v", err)
	}

	err = s.SetStatus(first.ID, types.MemoryStatusActive)
	if !errors.Is(err, types.ErrNoteTopicKeyHeld) {
		t.Fatalf("err = %v, want ErrNoteTopicKeyHeld", err)
	}
	var held *types.NoteTopicKeyHeldError
	if !errors.As(err, &held) {
		t.Fatalf("error must carry the holder: %v", err)
	}
	if held.HolderID != second.ID || held.TopicKey != "deploy-target" {
		t.Errorf("holder detail = %+v, want %s", held, second.ID)
	}

	// Nothing changed.
	got, _ := s.Get(first.ID)
	if got.Status != types.MemoryStatusStale {
		t.Errorf("status = %s, want stale", got.Status)
	}

	// Archiving the holder frees the key and reactivation then succeeds.
	if err := s.SetStatus(second.ID, types.MemoryStatusArchived); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(first.ID, types.MemoryStatusActive); err != nil {
		t.Fatalf("reactivation must work once the key is free: %v", err)
	}
}

func TestNoteStore_SetStatusValidatesAndIsANoOpOnNoChange(t *testing.T) {
	s := newNoteStore(t)
	n, _ := s.Insert(NoteInput{ProjectID: testProject, Content: "x"})

	if err := s.SetStatus(n.ID, "deleted"); !errors.Is(err, types.ErrValidation) {
		t.Errorf("err = %v, want a validation error", err)
	}
	if err := s.SetStatus(n.ID, types.MemoryStatusActive); err != nil {
		t.Errorf("same-status update should be a no-op, got %v", err)
	}
	if err := s.SetStatus("nope", types.MemoryStatusStale); !errors.Is(err, types.ErrMemoryNotFound) {
		t.Errorf("err = %v, want ErrMemoryNotFound", err)
	}
}

func TestNoteStore_ListAndCount(t *testing.T) {
	s := newNoteStore(t)
	for i, status := range []types.MemoryStatus{
		types.MemoryStatusActive, types.MemoryStatusActive, types.MemoryStatusStale,
	} {
		if _, err := s.Insert(NoteInput{
			ProjectID: testProject, Content: string(rune('a'+i)) + " note", Status: status,
		}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.List(NoteFilter{ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("listed %d notes, want 3", len(all))
	}
	active, err := s.Count(NoteFilter{ProjectID: testProject, Status: types.MemoryStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Errorf("counted %d active notes, want 2", active)
	}
}
