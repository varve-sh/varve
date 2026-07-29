package kernel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varve-sh/varve/internal/types"
)

func setupTestKernel(t *testing.T) *MemoryKernel {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	k := New(dbPath, "test-project")
	if err := k.Open(); err != nil {
		t.Fatalf("open kernel: %v", err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

func TestKernel_Save_Defaults(t *testing.T) {
	k := setupTestKernel(t)

	mem, _, err := k.Save(types.MemorySaveInput{
		Content: "We use PostgreSQL as the main database",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	if mem.ID == "" {
		t.Error("expected non-empty ID")
	}
	if mem.Type != types.MemoryTypeNote {
		t.Errorf("type: want note, got %s", mem.Type)
	}
	if mem.Source != types.MemorySourceUser {
		t.Errorf("source: want user, got %s", mem.Source)
	}
	if mem.Confidence != 1.0 {
		t.Errorf("confidence: want 1.0, got %f", mem.Confidence)
	}
	if mem.Summary == "" {
		t.Error("expected auto-generated summary")
	}
	if mem.ProjectID != "test-project" {
		t.Errorf("project_id: want test-project, got %s", mem.ProjectID)
	}
	if mem.Status != types.MemoryStatusActive {
		t.Errorf("status: want active, got %s", mem.Status)
	}
}

func TestKernel_Save_ExplicitType(t *testing.T) {
	k := setupTestKernel(t)

	mem, _, err := k.Save(types.MemorySaveInput{
		Content: "We chose JWT over sessions",
		Type:    types.MemoryTypeDecision,
		Tags:    []string{"auth"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if mem.Type != types.MemoryTypeDecision {
		t.Errorf("type: want decision, got %s", mem.Type)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "auth" {
		t.Errorf("tags: want [auth], got %v", mem.Tags)
	}
}

func TestKernel_Get(t *testing.T) {
	k := setupTestKernel(t)

	saved, _, _ := k.Save(types.MemorySaveInput{Content: "test memory"})

	got, err := k.Get(saved.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected memory, got nil")
	}
	if got.ID != saved.ID {
		t.Errorf("ID mismatch: want %s, got %s", saved.ID, got.ID)
	}
}

func TestKernel_Get_NotFound(t *testing.T) {
	k := setupTestKernel(t)
	got, err := k.Get("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for missing ID")
	}
}

func TestKernel_Delete(t *testing.T) {
	k := setupTestKernel(t)
	saved, _, _ := k.Save(types.MemorySaveInput{Content: "to be deleted"})

	outcome, err := k.Forget(saved.ID, types.ActorHuman)
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if outcome != DisposalDeleted {
		t.Errorf("outcome = %v, want DisposalDeleted", outcome)
	}

	got, _ := k.Get(saved.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestKernel_Update_Status(t *testing.T) {
	k := setupTestKernel(t)
	saved, _, _ := k.Save(types.MemorySaveInput{Content: "active memory"})

	archived := types.MemoryStatusArchived
	updated, err := k.Update(saved.ID, types.MemoryUpdateInput{Status: &archived})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != types.MemoryStatusArchived {
		t.Errorf("status: want archived, got %s", updated.Status)
	}
}

func TestKernel_List(t *testing.T) {
	k := setupTestKernel(t)
	k.Save(types.MemorySaveInput{Content: "memory 1", Type: types.MemoryTypeDecision})
	k.Save(types.MemorySaveInput{Content: "memory 2", Type: types.MemoryTypeFact})
	k.Save(types.MemorySaveInput{Content: "memory 3", Type: types.MemoryTypeDecision})

	all, err := k.List(types.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("want 3, got %d", len(all))
	}

	decisions, err := k.List(types.ListOptions{Limit: 10, Type: types.MemoryTypeDecision})
	if err != nil {
		t.Fatalf("list decisions: %v", err)
	}
	if len(decisions) != 2 {
		t.Errorf("want 2 decisions, got %d", len(decisions))
	}
}

func TestKernel_Recall(t *testing.T) {
	k := setupTestKernel(t)

	k.Save(types.MemorySaveInput{
		Content: "We use PostgreSQL as the primary database",
		Type:    types.MemoryTypeDecision,
		Tags:    []string{"database"},
	})
	k.Save(types.MemorySaveInput{
		Content: "All API routes use kebab-case naming convention",
		Type:    types.MemoryTypeConvention,
		Tags:    []string{"api"},
	})

	results, err := k.Recall(types.MemoryRecallInput{
		Query: "database",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].Memory.Content != "We use PostgreSQL as the primary database" {
		t.Errorf("top result mismatch: %s", results[0].Memory.Content)
	}
}

func TestKernel_Recall_Empty(t *testing.T) {
	k := setupTestKernel(t)

	results, err := k.Recall(types.MemoryRecallInput{Query: "anything"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results on empty store, got %d", len(results))
	}
}

func TestKernel_Recall_LimitEnforced(t *testing.T) {
	k := setupTestKernel(t)
	for i := 0; i < 20; i++ {
		k.Save(types.MemorySaveInput{Content: "database memory repeated"})
	}

	results, err := k.Recall(types.MemoryRecallInput{Query: "database", Limit: 5})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(results) > 5 {
		t.Errorf("want ≤5 results, got %d", len(results))
	}
}

func TestKernel_Recall_UpdatesAccessCount(t *testing.T) {
	k := setupTestKernel(t)
	saved, _, _ := k.Save(types.MemorySaveInput{Content: "access tracking test memory"})

	k.Recall(types.MemoryRecallInput{Query: "access tracking", Limit: 5})

	got, _ := k.Get(saved.ID)
	if got.AccessCount == 0 {
		t.Error("expected access_count > 0 after recall")
	}
}

func TestKernel_HasEmbedder_False(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("VARVE_EMBED_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VARVE_EMBED_URL", "")
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")
	k := setupTestKernel(t)
	if k.HasEmbedder() {
		t.Error("expected HasEmbedder=false when provider is disabled")
	}
}

func TestKernel_HasEmbedder_True(t *testing.T) {
	t.Setenv("VARVE_EMBED_PROVIDER", "")
	t.Setenv("VARVE_EMBED_KEY", "test-key")
	k := setupTestKernel(t)
	if !k.HasEmbedder() {
		t.Error("expected HasEmbedder=true when key is set")
	}
}

func TestKernel_HasEmbedder_LocalURL(t *testing.T) {
	// A local URL without a key should still enable embeddings.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type resp struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		json.NewEncoder(w).Encode(resp{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{Embedding: []float64{0.1, 0.2}}}})
	}))
	defer srv.Close()

	t.Setenv("VARVE_EMBED_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VARVE_EMBED_URL", srv.URL)
	t.Setenv("VARVE_EMBED_PROVIDER", "")
	k := setupTestKernel(t)
	if !k.HasEmbedder() {
		t.Error("expected HasEmbedder=true when local URL is set without key")
	}
	provider, _ := k.EmbedInfo()
	if provider == "disabled" {
		t.Errorf("expected non-disabled provider, got %s", provider)
	}
}

func TestKernel_Reindex_NoEmbedder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("VARVE_EMBED_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VARVE_EMBED_URL", "")
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")
	k := setupTestKernel(t)
	k.Save(types.MemorySaveInput{Content: "no embedder memory"})

	res, err := k.Reindex(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Succeeded != 0 {
		t.Errorf("want 0 when no embedder, got %d", res.Succeeded)
	}
}

// fakeEmbedServer returns an httptest.Server that always responds with a
// fixed 3-dimensional embedding vector.
func fakeEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type resp struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		json.NewEncoder(w).Encode(resp{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{Embedding: []float64{0.1, 0.2, 0.3}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestKernel_Reindex_WithEmbedder(t *testing.T) {
	srv := fakeEmbedServer(t)
	t.Setenv("VARVE_EMBED_PROVIDER", "")
	t.Setenv("VARVE_EMBED_KEY", "test-key")
	t.Setenv("VARVE_EMBED_URL", srv.URL)

	k := setupTestKernel(t)

	// Insert memories directly via the store to avoid the async embedding
	// goroutine that Save() spawns — guarantees embedding IS NULL.
	k.store.Insert(makeMemory("01REINDX01", "test-project", types.MemoryTypeFact))
	k.store.Insert(makeMemory("01REINDX02", "test-project", types.MemoryTypeFact))

	res, err := k.Reindex(nil)
	if err != nil {
		t.Fatalf("reindex error: %v", err)
	}
	if res.Succeeded != 2 {
		t.Errorf("want 2 reindexed, got %d (firstErr: %v)", res.Succeeded, res.FirstErr)
	}

	rows, err := k.store.FindEmbeddings("test-project")
	if err != nil {
		t.Fatalf("find embeddings: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 stored embeddings, got %d", len(rows))
	}
}

func TestKernel_Reindex_SkipsAlreadyEmbedded(t *testing.T) {
	srv := fakeEmbedServer(t)
	t.Setenv("VARVE_EMBED_PROVIDER", "")
	t.Setenv("VARVE_EMBED_KEY", "test-key")
	t.Setenv("VARVE_EMBED_URL", srv.URL)

	k := setupTestKernel(t)
	m := makeMemory("01RESKIP01", "test-project", types.MemoryTypeFact)
	k.store.Insert(m)
	k.store.StoreEmbedding(m.ID, []float64{9, 9, 9})

	res, err := k.Reindex(nil)
	if err != nil {
		t.Fatalf("reindex error: %v", err)
	}
	if res.Succeeded != 0 {
		t.Errorf("want 0 (already embedded), got %d", res.Succeeded)
	}
}

func TestKernel_Count(t *testing.T) {
	k := setupTestKernel(t)
	k.Save(types.MemorySaveInput{Content: "d1", Type: types.MemoryTypeDecision})
	k.Save(types.MemorySaveInput{Content: "d2", Type: types.MemoryTypeDecision})
	k.Save(types.MemorySaveInput{Content: "f1", Type: types.MemoryTypeFact})

	n, err := k.Count(types.MemoryTypeDecision, "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2, got %d", n)
	}
}

// --- ScanStaleness ---

func TestScanStaleness_NoFilePaths(t *testing.T) {
	k := setupTestKernel(t)
	k.Save(types.MemorySaveInput{Content: "no file paths"})

	res, err := k.ScanStaleness(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Checked != 0 {
		t.Errorf("want Checked=0 (no file_paths), got %d", res.Checked)
	}
	if res.Marked != 0 {
		t.Errorf("want Marked=0, got %d", res.Marked)
	}
}

func TestScanStaleness_FileDeleted(t *testing.T) {
	dir := t.TempDir()
	k := setupTestKernel(t)

	// Save a memory referencing a file that doesn't exist.
	k.Save(types.MemorySaveInput{
		Content:   "auth uses RS256",
		FilePaths: []string{"src/auth.go"},
	})

	res, err := k.ScanStaleness(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Checked != 1 {
		t.Errorf("want Checked=1, got %d", res.Checked)
	}
	if res.Marked != 1 {
		t.Errorf("want Marked=1 (file missing), got %d", res.Marked)
	}
	if len(res.Details) == 0 || res.Details[0].Reason != "file deleted: src/auth.go" {
		t.Errorf("unexpected detail: %+v", res.Details)
	}

	// Memory should now be stale in the DB.
	mem, _ := k.Get(res.Details[0].MemoryID)
	if mem.Status != types.MemoryStatusStale {
		t.Errorf("expected status=stale, got %s", mem.Status)
	}
}

func TestScanStaleness_FileModified(t *testing.T) {
	dir := t.TempDir()
	k := setupTestKernel(t)

	// Create the file first, then save the memory (memory.UpdatedAt > file mtime).
	filePath := filepath.Join(dir, "src", "db.go")
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("// original"), 0644); err != nil {
		t.Fatal(err)
	}

	mem, _, _ := k.Save(types.MemorySaveInput{
		Content:   "database uses postgres",
		FilePaths: []string{"src/db.go"},
	})

	// Touch the file after saving the memory.
	futureTime := mem.UpdatedAt.Add(2 * time.Second)
	if err := os.Chtimes(filePath, futureTime, futureTime); err != nil {
		t.Fatal(err)
	}

	res, err := k.ScanStaleness(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Marked != 1 {
		t.Errorf("want Marked=1 (file modified after memory saved), got %d", res.Marked)
	}
}

func TestScanStaleness_FileUnchanged(t *testing.T) {
	dir := t.TempDir()
	k := setupTestKernel(t)

	// Write file, then save memory — memory.UpdatedAt is after file mtime.
	filePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(filePath, []byte("# readme"), 0644); err != nil {
		t.Fatal(err)
	}

	// Small sleep so memory timestamp is clearly after file write.
	time.Sleep(5 * time.Millisecond)
	k.Save(types.MemorySaveInput{
		Content:   "see README for setup",
		FilePaths: []string{"README.md"},
	})

	res, err := k.ScanStaleness(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Marked != 0 {
		t.Errorf("want Marked=0 (file unchanged), got %d", res.Marked)
	}
}

// --- filePathsToQuery ---

func TestFilePathsToQuery(t *testing.T) {
	cases := []struct {
		paths []string
		want  []string // terms that must appear
	}{
		{[]string{"src/auth/middleware.go"}, []string{"auth", "middleware"}},
		{[]string{"internal/db/store.go"}, []string{"internal", "db", "store"}},
		{[]string{"README.md"}, []string{"README"}},
		{[]string{"pkg/my_service/handler.go"}, []string{"my", "service", "handler"}},
	}
	for _, tc := range cases {
		got := filePathsToQuery(tc.paths)
		for _, term := range tc.want {
			found := false
			for _, word := range strings.Fields(got) {
				if word == term {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("filePathsToQuery(%v) = %q, missing term %q", tc.paths, got, term)
			}
		}
	}
}

// --- ContextForFiles ---

func TestContextForFiles_Empty(t *testing.T) {
	k := setupTestKernel(t)
	ctxRes, err := k.ContextForFiles(nil, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	results := ctxRes.Items
	if len(results) != 0 {
		t.Errorf("expected empty results for nil paths, got %d", len(results))
	}
}

func TestContextForFiles_DirectMatch(t *testing.T) {
	k := setupTestKernel(t)
	k.Save(types.MemorySaveInput{
		Content:   "Auth uses RS256 JWT",
		FilePaths: []string{"src/auth/middleware.go"},
	})
	k.Save(types.MemorySaveInput{
		Content:   "DB uses Postgres",
		FilePaths: []string{"internal/db/store.go"},
	})

	ctxRes, err := k.ContextForFiles([]string{"src/auth/middleware.go"}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	results := ctxRes.Items
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	// First result should be the direct file match with score=1.0
	if results[0].Score != 1.0 {
		t.Errorf("expected file-match score 1.0, got %f", results[0].Score)
	}
	if results[0].Memory.Content != "Auth uses RS256 JWT" {
		t.Errorf("expected matched memory, got: %s", results[0].Memory.Content)
	}
}

func TestContextForFiles_DeduplicatesAcrossSources(t *testing.T) {
	k := setupTestKernel(t)
	// This memory matches both file path AND keyword "auth"
	k.Save(types.MemorySaveInput{
		Content:   "Auth validates JWT on every request",
		FilePaths: []string{"src/auth/middleware.go"},
		Tags:      []string{"auth"},
	})

	ctxRes, err := k.ContextForFiles([]string{"src/auth/middleware.go"}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	results := ctxRes.Items
	// Should appear exactly once despite matching both signals
	count := 0
	for _, r := range results {
		if r.Memory.Content == "Auth validates JWT on every request" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected memory to appear exactly once, appeared %d times", count)
	}
}

// --- topic_key upsert ---

func TestKernel_Save_TopicKey_CreatesNew(t *testing.T) {
	k := setupTestKernel(t)

	mem, upserted, err := k.Save(types.MemorySaveInput{
		Content:  "We use PostgreSQL",
		TopicKey: "decision/database",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if upserted {
		t.Error("expected upserted=false for new memory")
	}
	if mem.TopicKey != "decision/database" {
		t.Errorf("expected topic_key persisted, got %q", mem.TopicKey)
	}
}

func TestKernel_Save_TopicKey_UpsertUpdates(t *testing.T) {
	k := setupTestKernel(t)

	first, _, _ := k.Save(types.MemorySaveInput{
		Content:  "We use PostgreSQL",
		TopicKey: "decision/database",
	})

	second, upserted, err := k.Save(types.MemorySaveInput{
		Content:  "We switched to MySQL",
		TopicKey: "decision/database",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !upserted {
		t.Error("expected upserted=true for existing topic_key")
	}
	if second.ID != first.ID {
		t.Errorf("upsert should return same ID: want %s, got %s", first.ID, second.ID)
	}

	got, _ := k.Get(first.ID)
	if got.Content != "We switched to MySQL" {
		t.Errorf("content not updated: %q", got.Content)
	}
}

func TestKernel_Save_TopicKey_NoDuplicates(t *testing.T) {
	k := setupTestKernel(t)

	for i := 0; i < 5; i++ {
		k.Save(types.MemorySaveInput{
			Content:  "repeated save",
			TopicKey: "fact/repeated",
		})
	}

	all, _ := k.List(types.ListOptions{Limit: 100})
	count := 0
	for _, m := range all {
		if m.TopicKey == "fact/repeated" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 memory with topic_key, got %d", count)
	}
}

func TestKernel_Save_NoTopicKey_AlwaysCreates(t *testing.T) {
	k := setupTestKernel(t)

	for i := 0; i < 3; i++ {
		k.Save(types.MemorySaveInput{Content: "no key memory"})
	}

	all, _ := k.List(types.ListOptions{Limit: 100})
	if len(all) != 3 {
		t.Errorf("without topic_key, should create 3 separate memories, got %d", len(all))
	}
}
