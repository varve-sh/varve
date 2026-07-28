package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// --- Helpers ---

func setupServer(t *testing.T) (*server.MCPServer, *kernel.MemoryKernel) {
	t.Helper()
	t.Setenv("MEMTRACE_EMBED_PROVIDER", "disabled")
	t.Setenv("MEMTRACE_EMBED_URL", "")
	dbPath := filepath.Join(t.TempDir(), "test.db")
	k := kernel.New(dbPath, "test-project")
	if err := k.Open(); err != nil {
		t.Fatalf("open kernel: %v", err)
	}
	t.Cleanup(func() { k.Close() })

	s := server.NewMCPServer("memtrace", "0.0.0", server.WithToolCapabilities(true))
	registerTools(s, k, newSessionTracker())
	return s, k
}

func callTool(t *testing.T, s *server.MCPServer, name string, args map[string]interface{}) *mcpgo.CallToolResult {
	t.Helper()
	tool := s.GetTool(name)
	if tool == nil {
		t.Fatalf("tool %q not registered", name)
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	result, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("tool %q returned error: %v", name, err)
	}
	return result
}

func resultText(t *testing.T, r *mcpgo.CallToolResult) string {
	t.Helper()
	if r == nil || len(r.Content) == 0 {
		t.Fatal("empty result")
	}
	tc, ok := r.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", r.Content[0])
	}
	return tc.Text
}

// --- extractStringSlice ---

func TestExtractStringSlice_Normal(t *testing.T) {
	args := map[string]interface{}{
		"tags": []interface{}{"auth", "api", "security"},
	}
	got := extractStringSlice(args, "tags")
	if len(got) != 3 || got[0] != "auth" || got[1] != "api" || got[2] != "security" {
		t.Errorf("unexpected result: %v", got)
	}
}

func TestExtractStringSlice_Missing(t *testing.T) {
	got := extractStringSlice(map[string]interface{}{}, "tags")
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestExtractStringSlice_WrongType(t *testing.T) {
	args := map[string]interface{}{"tags": "not-a-slice"}
	got := extractStringSlice(args, "tags")
	if len(got) != 0 {
		t.Errorf("expected empty slice for wrong type, got %v", got)
	}
}

func TestExtractStringSlice_SkipsNonStrings(t *testing.T) {
	args := map[string]interface{}{
		"tags": []interface{}{"a", 42, "b", nil},
	}
	got := extractStringSlice(args, "tags")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("expected [a b], got %v", got)
	}
}

// --- formatAge ---

func TestFormatAge_Minutes(t *testing.T) {
	got := formatAge(time.Now().Add(-30 * time.Minute))
	if !strings.HasSuffix(got, "m ago") {
		t.Errorf("unexpected format: %s", got)
	}
}

func TestFormatAge_Hours(t *testing.T) {
	got := formatAge(time.Now().Add(-5 * time.Hour))
	if !strings.HasSuffix(got, "h ago") {
		t.Errorf("unexpected format: %s", got)
	}
}

func TestFormatAge_Days(t *testing.T) {
	got := formatAge(time.Now().Add(-3 * 24 * time.Hour))
	if !strings.HasSuffix(got, "d ago") {
		t.Errorf("unexpected format: %s", got)
	}
}

func TestFormatAge_Months(t *testing.T) {
	got := formatAge(time.Now().Add(-60 * 24 * time.Hour))
	if !strings.HasSuffix(got, "mo ago") {
		t.Errorf("unexpected format: %s", got)
	}
}

// --- truncateStr ---

func TestTruncateStr_Short(t *testing.T) {
	s := "hello"
	got := truncateStr(s, 20)
	if got != s {
		t.Errorf("short string should not be truncated: got %q", got)
	}
}

func TestTruncateStr_Exact(t *testing.T) {
	s := "hello"
	got := truncateStr(s, 5)
	if got != s {
		t.Errorf("exact length should not be truncated: got %q", got)
	}
}

func TestTruncateStr_Long(t *testing.T) {
	s := "hello world"
	got := truncateStr(s, 8)
	if got != "hello..." {
		t.Errorf("want 'hello...', got %q", got)
	}
}

func TestTruncateStr_Unicode(t *testing.T) {
	s := "héllo wörld"
	got := truncateStr(s, 7)
	// Should truncate on rune boundary
	if len([]rune(got)) > 7 {
		t.Errorf("truncated too long: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected ellipsis suffix: %q", got)
	}
}

// --- memory_save tool ---

func TestMemorySaveTool_Basic(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_save", map[string]interface{}{
		"content": "We use PostgreSQL for persistence",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "Saved memory") {
		t.Errorf("unexpected output: %s", text)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", text)
	}
}

func TestMemorySaveTool_WithTypeAndTags(t *testing.T) {
	s, k := setupServer(t)

	callTool(t, s, "memory_save", map[string]interface{}{
		"content": "Auth uses JWT with RS256",
		"type":    "decision",
		"tags":    []interface{}{"auth", "security"},
	})

	results, err := k.Recall(types.MemoryRecallInput{Query: "auth JWT", Limit: 5})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected saved memory to be recalled")
	}
	m := results[0].Memory
	if m.Type != types.MemoryTypeDecision {
		t.Errorf("type: want decision, got %s", m.Type)
	}
	if m.Source != types.MemorySourceAgent {
		t.Errorf("source: want agent, got %s", m.Source)
	}
}

func TestMemorySaveTool_EmptyContent(t *testing.T) {
	s, _ := setupServer(t)

	// Empty content is saved without error (kernel does not validate)
	result := callTool(t, s, "memory_save", map[string]interface{}{
		"content": "",
	})
	if result.IsError {
		t.Errorf("unexpected error for empty content: %s", resultText(t, result))
	}
}

// --- memory_recall tool ---

func TestMemoryRecallTool_ReturnsResults(t *testing.T) {
	s, k := setupServer(t)

	k.Save(types.MemorySaveInput{
		Content: "We use Redis for caching session data",
		Type:    types.MemoryTypeDecision,
		Tags:    []string{"cache", "redis"},
	})

	result := callTool(t, s, "memory_recall", map[string]interface{}{
		"query": "caching Redis",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "Redis") {
		t.Errorf("expected Redis in results, got: %s", text)
	}
}

func TestMemoryRecallTool_NoResults(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_recall", map[string]interface{}{
		"query": "obscure topic xyz",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "No relevant memories") {
		t.Errorf("expected no-results message, got: %s", text)
	}
}

func TestMemoryRecallTool_LimitRespected(t *testing.T) {
	s, k := setupServer(t)

	for i := 0; i < 10; i++ {
		k.Save(types.MemorySaveInput{Content: "database connection pooling tip"})
	}

	result := callTool(t, s, "memory_recall", map[string]interface{}{
		"query": "database",
		"limit": float64(3),
	})
	text := resultText(t, result)
	// Each result has exactly one "confidence:" line
	if got := strings.Count(text, "confidence:"); got != 3 {
		t.Errorf("limit=3 should return exactly 3 results, got %d; text: %s", got, text)
	}
}

func TestMemoryRecallTool_TypeFilter(t *testing.T) {
	s, k := setupServer(t)

	k.Save(types.MemorySaveInput{Content: "deploy to Kubernetes", Type: types.MemoryTypeEvent})
	k.Save(types.MemorySaveInput{Content: "Kubernetes naming conventions", Type: types.MemoryTypeConvention})

	result := callTool(t, s, "memory_recall", map[string]interface{}{
		"query": "Kubernetes",
		"type":  "convention",
	})
	text := resultText(t, result)
	if strings.Contains(text, "deploy to Kubernetes") {
		t.Errorf("type filter should exclude event memory; got: %s", text)
	}
}

// --- memory_forget tool ---

func TestMemoryForgetTool_ByID(t *testing.T) {
	s, k := setupServer(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "temporary memory to delete"})

	result := callTool(t, s, "memory_forget", map[string]interface{}{
		"id": mem.ID,
	})
	text := resultText(t, result)
	if !strings.Contains(text, "Deleted") {
		t.Errorf("expected deletion confirmation, got: %s", text)
	}

	got, _ := k.Get(mem.ID)
	if got != nil {
		t.Error("memory should be deleted after memory_forget")
	}
}

func TestMemoryForgetTool_ByQuery(t *testing.T) {
	s, k := setupServer(t)

	k.Save(types.MemorySaveInput{Content: "old auth approach using session cookies"})

	result := callTool(t, s, "memory_forget", map[string]interface{}{
		"query": "old auth session cookies",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "Deleted") {
		t.Errorf("expected deletion confirmation, got: %s", text)
	}
}

func TestMemoryForgetTool_ByIDNotFound(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_forget", map[string]interface{}{
		"id": "01NONEXISTENTID00000000000",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "not found") {
		t.Errorf("expected not-found message, got: %s", text)
	}
}

// --- memory_update tool ---

func TestMemoryUpdateTool_Content(t *testing.T) {
	s, k := setupServer(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "original content"})

	result := callTool(t, s, "memory_update", map[string]interface{}{
		"id":      mem.ID,
		"content": "updated content",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "Updated memory") {
		t.Errorf("expected update confirmation, got: %s", text)
	}

	got, _ := k.Get(mem.ID)
	if got.Content != "updated content" {
		t.Errorf("want 'updated content', got %q", got.Content)
	}
}

func TestMemoryUpdateTool_Tags(t *testing.T) {
	s, k := setupServer(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "some fact", Type: types.MemoryTypeFact})

	callTool(t, s, "memory_update", map[string]interface{}{
		"id":   mem.ID,
		"tags": []interface{}{"auth", "security"},
	})

	got, _ := k.Get(mem.ID)
	if len(got.Tags) != 2 || got.Tags[0] != "auth" {
		t.Errorf("unexpected tags: %v", got.Tags)
	}
}

// A note cannot be promoted into a decision by an update: they live in
// different tables with different semantics, and a decision has to be born
// through the lifecycle so its provenance and events exist (ADR-0001 D1, D2).
func TestMemoryUpdateTool_CannotPromoteANoteToADecision(t *testing.T) {
	s, k := setupServer(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "some fact", Type: types.MemoryTypeFact})

	result := callTool(t, s, "memory_update", map[string]interface{}{
		"id":   mem.ID,
		"type": "decision",
	})
	if !result.IsError {
		t.Errorf("promoting a note to a decision must be refused, got: %v", resultText(t, result))
	}

	got, _ := k.Get(mem.ID)
	if got.Type != types.MemoryTypeNote {
		t.Errorf("the note must be unchanged, got type %s", got.Type)
	}
}

func TestMemoryUpdateTool_NotFound(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_update", map[string]interface{}{
		"id":      "01NONEXISTENTID0000000000X",
		"content": "new content",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "not found") {
		t.Errorf("expected not-found message, got: %s", text)
	}
}

func TestMemoryUpdateTool_MissingID(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_update", map[string]interface{}{
		"content": "something",
	})
	if !result.IsError {
		t.Error("expected error when id is missing")
	}
}

func TestMemoryForgetTool_NoArgs(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_forget", map[string]interface{}{})
	text := resultText(t, result)
	if !strings.Contains(text, "Provide either") {
		t.Errorf("expected usage hint, got: %s", text)
	}
}

// --- memory_context ---

func TestMemoryContextTool_NoFilePaths(t *testing.T) {
	s, _ := setupServer(t)
	result := callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{},
	})
	text := resultText(t, result)
	if !strings.Contains(text, "No file paths") {
		t.Errorf("expected no-file-paths message, got: %s", text)
	}
}

func TestMemoryContextTool_DirectFileMatch(t *testing.T) {
	s, k := setupServer(t)

	// Save a memory linked to a specific file.
	k.Save(types.MemorySaveInput{
		Content:   "Auth middleware validates JWT tokens",
		FilePaths: []string{"src/auth/middleware.go"},
		Tags:      []string{"auth"},
	})
	// Save an unrelated memory.
	k.Save(types.MemorySaveInput{
		Content:   "Database uses PostgreSQL",
		FilePaths: []string{"internal/db/store.go"},
	})

	result := callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{"src/auth/middleware.go"},
	})
	text := resultText(t, result)

	if !strings.Contains(text, "file match") {
		t.Errorf("expected [file match] label, got: %s", text)
	}
	if !strings.Contains(text, "Auth middleware") {
		t.Errorf("expected matched memory content, got: %s", text)
	}
	if strings.Contains(text, "PostgreSQL") {
		t.Errorf("unrelated memory should not appear, got: %s", text)
	}
}

func TestMemoryContextTool_NoMatchReturnsNoMemories(t *testing.T) {
	s, _ := setupServer(t)
	result := callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{"src/nonexistent/file.go"},
	})
	text := resultText(t, result)
	if !strings.Contains(text, "No relevant") {
		t.Errorf("expected no-memories message, got: %s", text)
	}
}

func TestMemoryContextTool_MultipleFiles(t *testing.T) {
	s, k := setupServer(t)

	k.Save(types.MemorySaveInput{
		Content:   "Handler returns 401 for unauthenticated requests",
		FilePaths: []string{"src/auth/handler.go"},
	})
	k.Save(types.MemorySaveInput{
		Content:   "Middleware chains must call next()",
		FilePaths: []string{"src/auth/middleware.go"},
	})

	result := callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{"src/auth/handler.go", "src/auth/middleware.go"},
	})
	text := resultText(t, result)

	if !strings.Contains(text, "401") {
		t.Errorf("expected first memory, got: %s", text)
	}
	if !strings.Contains(text, "Middleware chains") {
		t.Errorf("expected second memory, got: %s", text)
	}
}

// --- sessionTracker ---

func TestSessionTracker_EmptyNoSummary(t *testing.T) {
	tr := newSessionTracker()
	if got := tr.summary(); got != "" {
		t.Errorf("expected empty summary for no activity, got: %s", got)
	}
}

func TestSessionTracker_RecallOnlyNoSummary(t *testing.T) {
	tr := newSessionTracker()
	tr.recordRecall()
	tr.recordRecall()
	if got := tr.summary(); got != "" {
		t.Errorf("expected empty summary for recall-only session, got: %s", got)
	}
}

func TestSessionTracker_OneSave(t *testing.T) {
	tr := newSessionTracker()
	tr.recordSave("id1", "We use JWT with RS256", types.MemoryTypeDecision)

	got := tr.summary()
	if got == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(got, "saved 1 memory") {
		t.Errorf("expected singular 'memory', got: %s", got)
	}
	if !strings.Contains(got, "JWT with RS256") {
		t.Errorf("expected memory summary in output, got: %s", got)
	}
	if !strings.Contains(got, "[decision]") {
		t.Errorf("expected type label, got: %s", got)
	}
}

func TestSessionTracker_MultipleSavesAndRecalls(t *testing.T) {
	tr := newSessionTracker()
	tr.recordSave("id1", "Auth uses RS256", types.MemoryTypeDecision)
	tr.recordSave("id2", "Error handling convention", types.MemoryTypeConvention)
	tr.recordRecall()
	tr.recordRecall()
	tr.recordRecall()

	got := tr.summary()
	if !strings.Contains(got, "saved 2 memories") {
		t.Errorf("expected plural 'memories', got: %s", got)
	}
	if !strings.Contains(got, "Recalled 3 times") {
		t.Errorf("expected recall count, got: %s", got)
	}
}

func TestSessionTracker_SaveTool_RecordsInTracker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	k := kernel.New(dbPath, "test-project")
	if err := k.Open(); err != nil {
		t.Fatalf("open kernel: %v", err)
	}
	defer k.Close()

	s := server.NewMCPServer("memtrace", "0.0.0", server.WithToolCapabilities(true))
	tr := newSessionTracker()
	registerTools(s, k, tr)

	callTool(t, s, "memory_save", map[string]interface{}{
		"content": "Test decision about architecture",
		"type":    "decision",
	})

	sum := tr.summary()
	if sum == "" {
		t.Fatal("expected summary after save tool call")
	}
	if !strings.Contains(sum, "Test decision") {
		t.Errorf("expected memory content in summary, got: %s", sum)
	}
}

// --- memory_get tool ---

func TestMemoryGetTool_ReturnsFullContent(t *testing.T) {
	s, k := setupServer(t)

	mem, _, _ := k.Save(types.MemorySaveInput{
		Content: "We use JWT with RS256 for authentication. The API is stateless — no session storage.",
		Type:    types.MemoryTypeDecision,
		Tags:    []string{"auth"},
	})

	result := callTool(t, s, "memory_get", map[string]interface{}{
		"id": mem.ID,
	})
	text := resultText(t, result)

	if !strings.Contains(text, mem.ID) {
		t.Errorf("expected ID in output, got: %s", text)
	}
	if !strings.Contains(text, "stateless") {
		t.Errorf("expected full content, got: %s", text)
	}
	if !strings.Contains(text, "auth") {
		t.Errorf("expected tags in output, got: %s", text)
	}
}

func TestMemoryGetTool_NotFound(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_get", map[string]interface{}{
		"id": "01NONEXISTENTID0000000000X",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "not found") {
		t.Errorf("expected not-found message, got: %s", text)
	}
}

func TestMemoryGetTool_MissingID(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_get", map[string]interface{}{})
	if !result.IsError {
		t.Error("expected error when id is missing")
	}
}

// --- memory_recall output format ---

func TestMemoryRecallTool_ShowsIDsForMemoryGet(t *testing.T) {
	s, k := setupServer(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "We use Redis for caching"})

	result := callTool(t, s, "memory_recall", map[string]interface{}{
		"query": "Redis caching",
	})
	text := resultText(t, result)

	if !strings.Contains(text, mem.ID) {
		t.Errorf("expected memory ID %s in recall output for memory_get, got: %s", mem.ID, text)
	}
}

func TestMemoryRecallTool_IncludesMemoryGetHint(t *testing.T) {
	s, k := setupServer(t)

	k.Save(types.MemorySaveInput{Content: "some fact about the project"})

	result := callTool(t, s, "memory_recall", map[string]interface{}{
		"query": "project fact",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "memory_get") {
		t.Errorf("expected memory_get hint in recall output, got: %s", text)
	}
}

func TestMemoryRecallTool_UsesSummaryNotFullContent(t *testing.T) {
	s, k := setupServer(t)

	// Build content where the unique marker is beyond the 120-char summary boundary
	prefix := "Start of memory. " + strings.Repeat("padding ", 15) // >120 chars
	marker := "BEYOND_SUMMARY_MARKER"
	longContent := prefix + marker
	k.Save(types.MemorySaveInput{Content: longContent})

	result := callTool(t, s, "memory_recall", map[string]interface{}{
		"query": "start memory padding",
	})
	text := resultText(t, result)

	if !strings.Contains(text, "Start of memory") {
		t.Errorf("expected summary start in recall, got: %s", text)
	}
	// The marker is beyond position 120 so it must not appear in the recall output
	if strings.Contains(text, marker) {
		t.Errorf("full content beyond summary should not appear in recall, got: %s", text)
	}
}

func TestMemoryContextTool_IncludesMemoryGetHint(t *testing.T) {
	s, k := setupServer(t)

	k.Save(types.MemorySaveInput{
		Content:   "Auth middleware validates JWT",
		FilePaths: []string{"src/auth/middleware.go"},
	})

	result := callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{"src/auth/middleware.go"},
	})
	text := resultText(t, result)
	if !strings.Contains(text, "memory_get") {
		t.Errorf("expected memory_get hint in context output, got: %s", text)
	}
}

func TestMemoryContextTool_ShowsIDsForMemoryGet(t *testing.T) {
	s, k := setupServer(t)

	mem, _, _ := k.Save(types.MemorySaveInput{
		Content:   "Auth middleware validates JWT tokens",
		FilePaths: []string{"src/auth/middleware.go"},
	})

	result := callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{"src/auth/middleware.go"},
	})
	text := resultText(t, result)
	if !strings.Contains(text, mem.ID) {
		t.Errorf("expected memory ID %s in context output, got: %s", mem.ID, text)
	}
}

func TestSessionTracker_RecallTool_RecordsInTracker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	k := kernel.New(dbPath, "test-project")
	if err := k.Open(); err != nil {
		t.Fatalf("open kernel: %v", err)
	}
	defer k.Close()

	s := server.NewMCPServer("memtrace", "0.0.0", server.WithToolCapabilities(true))
	tr := newSessionTracker()
	registerTools(s, k, tr)

	callTool(t, s, "memory_recall", map[string]interface{}{"query": "anything"})
	callTool(t, s, "memory_recall", map[string]interface{}{"query": "more"})

	tr.mu.Lock()
	count := tr.recallCount
	tr.mu.Unlock()

	if count != 2 {
		t.Errorf("expected recallCount=2, got %d", count)
	}
}

// --- memory_save topic_key ---

func TestMemorySaveTool_TopicKey_CreatesNew(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_save", map[string]interface{}{
		"content":   "We use PostgreSQL",
		"topic_key": "decision/database",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "Saved memory") {
		t.Errorf("expected Saved, got: %s", text)
	}
}

func TestMemorySaveTool_TopicKey_UpsertSaysUpdated(t *testing.T) {
	s, _ := setupServer(t)

	callTool(t, s, "memory_save", map[string]interface{}{
		"content":   "We use PostgreSQL",
		"topic_key": "decision/database",
	})

	result := callTool(t, s, "memory_save", map[string]interface{}{
		"content":   "We switched to MySQL",
		"topic_key": "decision/database",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "Updated memory") {
		t.Errorf("expected Updated for upsert, got: %s", text)
	}
}

func TestMemorySaveTool_TopicKey_NoDuplicates(t *testing.T) {
	s, k := setupServer(t)

	for i := 0; i < 4; i++ {
		callTool(t, s, "memory_save", map[string]interface{}{
			"content":   "repeated fact",
			"topic_key": "fact/repeated",
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
		t.Errorf("expected 1 memory with topic_key, got %d", count)
	}
}

// --- memory_prompt ---

func TestMemoryPromptTool_Basic(t *testing.T) {
	s, k := setupServer(t)

	result := callTool(t, s, "memory_prompt", map[string]interface{}{
		"content": "Refactor auth middleware to support OAuth",
	})
	text := resultText(t, result)
	if !strings.Contains(text, "Captured prompt") {
		t.Errorf("expected Captured prompt, got: %s", text)
	}

	all, _ := k.List(types.ListOptions{Limit: 10})
	if len(all) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(all))
	}
	m := all[0]
	if m.Type != types.MemoryTypeNote {
		t.Errorf("expected note type, got %s", m.Type)
	}
	found := false
	for _, tag := range m.Tags {
		if tag == "prompt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'prompt' tag, got %v", m.Tags)
	}
}

func TestMemoryPromptTool_MissingContent(t *testing.T) {
	s, _ := setupServer(t)

	result := callTool(t, s, "memory_prompt", map[string]interface{}{})
	// Empty content still saves (kernel handles empty content gracefully)
	_ = result
}

func TestMemoryPromptTool_WithFilePaths(t *testing.T) {
	s, k := setupServer(t)

	callTool(t, s, "memory_prompt", map[string]interface{}{
		"content":    "Fix the JWT validation bug",
		"file_paths": []interface{}{"src/auth/middleware.go"},
	})

	all, _ := k.List(types.ListOptions{Limit: 10})
	if len(all) == 0 {
		t.Fatal("no memories saved")
	}
	if len(all[0].FilePaths) != 1 || all[0].FilePaths[0] != "src/auth/middleware.go" {
		t.Errorf("unexpected file_paths: %v", all[0].FilePaths)
	}
}
