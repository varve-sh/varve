package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/pack"
	"github.com/varve-sh/varve/internal/types"
	"github.com/varve-sh/varve/internal/util"
)

// --- Helpers ---

func setupServer(t *testing.T) (*server.MCPServer, *kernel.MemoryKernel) {
	t.Helper()
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")
	t.Setenv("VARVE_EMBED_URL", "")
	dbPath := filepath.Join(t.TempDir(), "test.db")
	k := kernel.New(dbPath, "test-project")
	if err := k.Open(); err != nil {
		t.Fatalf("open kernel: %v", err)
	}
	t.Cleanup(func() { k.Close() })

	s := server.NewMCPServer("varve", "0.0.0", server.WithToolCapabilities(true))
	// Mirror Serve: one connection is one session, announced when it opens
	// (ADR-0004 §D3), and the tracker takes the kernel's id.
	registerTools(s, k, newSessionTracker(k.BeginSession(mcpAgentName, "")))
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
	tr := newSessionTracker(util.GenerateID())
	if got := tr.summary(); got != "" {
		t.Errorf("expected empty summary for no activity, got: %s", got)
	}
}

func TestSessionTracker_RecallOnlyNoSummary(t *testing.T) {
	tr := newSessionTracker(util.GenerateID())
	tr.recordRecall()
	tr.recordRecall()
	if got := tr.summary(); got != "" {
		t.Errorf("expected empty summary for recall-only session, got: %s", got)
	}
}

func TestSessionTracker_OneSave(t *testing.T) {
	tr := newSessionTracker(util.GenerateID())
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
	tr := newSessionTracker(util.GenerateID())
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

	s := server.NewMCPServer("varve", "0.0.0", server.WithToolCapabilities(true))
	tr := newSessionTracker(util.GenerateID())
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

	s := server.NewMCPServer("varve", "0.0.0", server.WithToolCapabilities(true))
	tr := newSessionTracker(util.GenerateID())
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

// F20 + ADR-0002 Amendment 2. The volunteering/answering boundary, asserted on
// both sides:
//
//   - `memory_recall` and `memory_get` *answer explicit queries*. They are the
//     review surface — §D10 names proposals visible in recall, and a proposal
//     that cannot be found cannot be accepted or disposed of — so they return
//     proposals carrying the advisory PROPOSED marker, permanently.
//   - `memory_context` *volunteers* context at task start, so it gives §P2's
//     structural guarantee instead: a proposed decision never appears as
//     content, only as a footer count. An inline caveat inside content the
//     agent was just handed is the precise laundering §P2's rationale names.
func TestMCPReadPaths_VolunteeringVersusAnswering(t *testing.T) {
	s, k := setupServer(t)

	// An agent-saved decision: proposed, by D2.
	if _, _, err := k.Save(types.MemorySaveInput{
		Content:   "Handlers must not log secrets.",
		Type:      types.MemoryTypeDecision,
		Source:    types.MemorySourceAgent,
		SessionID: "sess-1",
		FilePaths: []string{"internal/api/**"},
	}); err != nil {
		t.Fatal(err)
	}
	// A human-saved one: active, and unremarkable.
	if _, _, err := k.Save(types.MemorySaveInput{
		Content:   "Handlers return wrapped errors.",
		Type:      types.MemoryTypeConvention,
		Source:    types.MemorySourceUser,
		FilePaths: []string{"internal/api/**"},
	}); err != nil {
		t.Fatal(err)
	}
	ds, err := k.Decisions().ListDecisions(kernel.DecisionFilter{
		Statuses: []types.DecisionStatus{types.StatusProposed},
	})
	if err != nil || len(ds) != 1 {
		t.Fatalf("expected one proposal, got %d (%v)", len(ds), err)
	}
	proposalID := ds[0].ID

	// --- answering: the marker, and the content ---
	out := resultText(t, callTool(t, s, "memory_recall", map[string]interface{}{"query": "handlers"}))
	if !strings.Contains(out, "Handlers must not log secrets.") {
		t.Fatalf("memory_recall must remain the review surface:\n%s", out)
	}
	if strings.Count(out, "PROPOSED") != 1 {
		t.Errorf("memory_recall marked %d rows PROPOSED, want exactly the proposal:\n%s",
			strings.Count(out, "PROPOSED"), out)
	}
	out = resultText(t, callTool(t, s, "memory_get", map[string]interface{}{"id": proposalID}))
	if !strings.Contains(out, "PROPOSED") {
		t.Errorf("memory_get drops the status:\n%s", out)
	}

	// --- volunteering: the guarantee, and only a count ---
	out = resultText(t, callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{"internal/api/users.go"},
	}))
	if strings.Contains(out, "Handlers must not log secrets.") {
		t.Errorf("memory_context volunteered unconfirmed text as content:\n%s", out)
	}
	if strings.Contains(out, proposalID) && !strings.Contains(out, "proposed decisions touching") {
		t.Errorf("the proposal id appears outside the footer:\n%s", out)
	}
	if !strings.Contains(out, "proposed decisions touching these files: 1") {
		t.Errorf("memory_context dropped the proposal silently; it must be counted:\n%s", out)
	}
	if !strings.Contains(out, "decision accept") {
		t.Errorf("the footer does not say how to act on it:\n%s", out)
	}
	// The accepted decision is still served in full.
	if !strings.Contains(out, "Handlers return wrapped errors.") {
		t.Errorf("memory_context dropped a binding convention:\n%s", out)
	}
}

// ADR-0001 Amendment 3 (A3.1). An agent's forget of a decision transitions
// nothing: it records a request and says so. Before F28's attribution fix the
// log read `decision.proposed actor=agent` followed seconds later by
// `decision.rejected actor=human` from the same agent; the policy ruling then
// removed the transition itself, because "the user wanted this thrown away" is
// exactly as untrustworthy as "the user approved" (OQ3) — and worse for a
// binding decision, where the old mapping laundered a repeal into
// active → reverted.
func TestMemoryForget_OnADecisionRecordsARequestAndTransitionsNothing(t *testing.T) {
	s, k := setupServer(t)

	callTool(t, s, "memory_save", map[string]interface{}{
		"content": "Sessions live in Redis.",
		"type":    "decision",
	})
	ds, err := k.Decisions().ListDecisions(kernel.DecisionFilter{
		Statuses: []types.DecisionStatus{types.StatusProposed},
	})
	if err != nil || len(ds) != 1 {
		t.Fatalf("expected one proposal, got %d (%v)", len(ds), err)
	}
	id := ds[0].ID

	out := resultText(t, callTool(t, s, "memory_forget", map[string]interface{}{"id": id}))

	// The tool succeeds — the user's in-chat "forget that" reached the store —
	// but it must not claim a deletion that did not happen.
	if strings.Contains(out, "Deleted") {
		t.Errorf("memory_forget claims a deletion that did not happen:\n%s", out)
	}
	for _, want := range []string{"Disposal request recorded", "pending their confirmation"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result does not explain what happened (%q):\n%s", want, out)
		}
	}

	// Nothing transitioned.
	got, err := k.Decisions().GetDecision(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.StatusProposed {
		t.Errorf("status = %s, want proposed — an agent may not dispose of a decision", got.Status)
	}
	for _, kind := range []types.EventKind{
		types.EventDecisionRejected, types.EventDecisionReverted,
	} {
		if evs, _ := k.Decisions().Events(kernel.EventFilter{
			DecisionID: id, Kind: kind,
		}); len(evs) != 0 {
			t.Errorf("%d %s events, want 0", len(evs), kind)
		}
	}

	// The request is recorded, attributed to the agent, and carries its session.
	reqs, err := k.Decisions().Events(kernel.EventFilter{
		DecisionID: id, Kind: types.EventDecisionDisposalRequested,
	})
	if err != nil || len(reqs) != 1 {
		t.Fatalf("decision.disposal_requested events = %d (%v), want 1", len(reqs), err)
	}
	if reqs[0].Actor != types.ActorAgent {
		t.Errorf("actor = %q, want agent", reqs[0].Actor)
	}
	if reqs[0].SessionID == "" {
		t.Error("the request carries no session; it is unattributable")
	}

	// Repeats are legal facts, not errors — no dedup index, deliberately.
	callTool(t, s, "memory_forget", map[string]interface{}{"id": id})
	reqs, _ = k.Decisions().Events(kernel.EventFilter{
		DecisionID: id, Kind: types.EventDecisionDisposalRequested,
	})
	if len(reqs) != 2 {
		t.Errorf("disposal requests = %d, want 2 — repeats are facts", len(reqs))
	}

	// And a human can still see it in the triage queue.
	pending, err := k.Decisions().PendingDisposals("test-project")
	if err != nil || len(pending) != 1 || pending[0].Decision.ID != id {
		t.Fatalf("PendingDisposals = %+v (%v), want the requested decision", pending, err)
	}
	if pending[0].Count != 2 {
		t.Errorf("request count = %d, want 2", pending[0].Count)
	}
}

// A note is ungoverned on every channel: an agent's forget really does delete
// it (D1, D3's "notes keep v1 delete semantics").
func TestMemoryForget_StillDeletesNotes(t *testing.T) {
	s, k := setupServer(t)

	saved, _, err := k.Save(types.MemorySaveInput{
		Content: "CI runs on arm64.", Type: types.MemoryTypeFact,
		Source: types.MemorySourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(t, callTool(t, s, "memory_forget", map[string]interface{}{"id": saved.ID}))
	if !strings.Contains(out, "Deleted") {
		t.Errorf("a note must still be deleted outright:\n%s", out)
	}
	if m, _ := k.Get(saved.ID); m != nil {
		t.Error("the note is still there")
	}
}

// ADR-0002 §P1: memory_pack is the session bootstrap. The tool test asserts on
// the pack the agent actually receives — a tool that returns a valid empty
// string is a passing test and a broken product.
func TestMemoryPack_ServesBindingContextWithinBudget(t *testing.T) {
	s, k := setupServer(t)

	// A binding convention, an agent's proposal, and a note.
	if _, err := k.Decisions().ProposeAccepted(kernel.DecisionInput{
		ProjectID: "test-project",
		Title:     "Handlers validate the auth header",
		Body:      "Every handler under internal/auth checks the header before touching the session store.",
		Scope:     []string{"internal/auth/**"},
		Source:    types.DecisionSourceUser,
		Evidence: []kernel.EvidenceInput{{
			Kind: types.EvidenceKindCommit, Ref: "9f2c1ab", AddedBy: types.ActorHuman,
		}},
	}, kernel.AcceptOptions{Actor: types.ActorHuman}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.Save(types.MemorySaveInput{
		Content: "Everything should use gRPC.", Type: types.MemoryTypeDecision,
		Source: types.MemorySourceAgent, SessionID: "s1", FilePaths: []string{"internal/auth/**"},
	}); err != nil {
		t.Fatal(err)
	}

	out := resultText(t, callTool(t, s, "memory_pack", map[string]interface{}{
		"file_paths": []interface{}{"internal/auth/session.go"},
		"task":       "add refresh-token rotation",
	}))

	if !strings.Contains(out, "VARVE PACK v1") {
		t.Fatalf("not a pack:\n%s", out)
	}
	if !strings.Contains(out, "Handlers validate the auth header") {
		t.Errorf("the binding decision is missing from the pack:\n%s", out)
	}
	if strings.Contains(out, "Everything should use gRPC.") {
		t.Errorf("a proposal was served as content:\n%s", out)
	}
	if !strings.Contains(out, "proposed decisions touching these files: 1") {
		t.Errorf("the proposal must be counted in the footer:\n%s", out)
	}
	if got := pack.Estimate(out); got > pack.DefaultBudget {
		t.Errorf("pack is %d est-tokens over the %d default budget", got, pack.DefaultBudget)
	}

	// The events the attribution chain needs.
	served, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventPackServed})
	if len(served) != 1 {
		t.Fatalf("pack.served events = %d, want 1", len(served))
	}
	items, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventPackItem})
	if len(items) == 0 {
		t.Fatal("no pack.item events; ADR-0004's chain starts here")
	}
	if items[0].SessionID != served[0].SessionID || items[0].SessionID == "" {
		t.Errorf("pack.item and pack.served must share the session: %q vs %q",
			items[0].SessionID, served[0].SessionID)
	}
}

// §P1's error codes reach the agent as tool errors, never as partial packs.
func TestMemoryPack_ErrorsAreTypedAndRecordNothing(t *testing.T) {
	s, k := setupServer(t)

	for _, c := range []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{"no anchor", map[string]interface{}{}, "E3_NO_ANCHOR"},
		{"bad budget", map[string]interface{}{
			"task": "x", "budget_tokens": float64(10),
		}, "E1_BAD_BUDGET"},
		{"absolute path", map[string]interface{}{
			"file_paths": []interface{}{"/etc/passwd"},
		}, "E2_BAD_PATH"},
	} {
		res := callTool(t, s, "memory_pack", c.args)
		if !res.IsError {
			t.Errorf("%s: expected a tool error", c.name)
			continue
		}
		if got := resultText(t, res); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: error = %q, want it to start with %s", c.name, got, c.want)
		}
	}
	if evs, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventPackServed}); len(evs) != 0 {
		t.Errorf("%d pack.served events from errored calls, want 0", len(evs))
	}
}

// F32. The structural guarantee held, but the filter ran *after* the limit, so
// proposals consumed result slots and then vanished — and proposals win
// `updated_at DESC` by construction, so the better the quarantine worked the
// less this tool returned. One binding decision behind twelve proposals came
// back as no content at all, to the tool agents are told to call first.
func TestMemoryContext_ProposalsDoNotCrowdOutBindingContent(t *testing.T) {
	s, k := setupServer(t)

	if _, err := k.Decisions().ProposeAccepted(kernel.DecisionInput{
		ProjectID: "test-project",
		Title:     "Handlers validate the auth header",
		Scope:     []string{"internal/api/**"},
		Source:    types.DecisionSourceUser,
		Evidence: []kernel.EvidenceInput{{
			Kind: types.EvidenceKindCommit, Ref: "9f2c1ab", AddedBy: types.ActorHuman,
		}},
	}, kernel.AcceptOptions{Actor: types.ActorHuman}); err != nil {
		t.Fatal(err)
	}
	// The ordinary state of a store under the quarantine: proposals pile up,
	// and they are the newest rows.
	for i := 0; i < 12; i++ {
		if _, _, err := k.Save(types.MemorySaveInput{
			Content:   fmt.Sprintf("Proposal number %d about the API.", i),
			Type:      types.MemoryTypeDecision,
			Source:    types.MemorySourceAgent,
			SessionID: "s1",
			FilePaths: []string{"internal/api/**"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	out := resultText(t, callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{"internal/api/users.go"},
	}))

	if !strings.Contains(out, "Handlers validate the auth header") {
		t.Errorf("the binding decision was crowded out by quarantined rows:\n%s", out)
	}
	if strings.Contains(out, "Proposal number") {
		t.Errorf("a proposal was served as content:\n%s", out)
	}
	// §P8: ids may be dropped, the count never may. Twelve matched.
	if !strings.Contains(out, "proposed decisions touching these files: 12") {
		t.Errorf("the footer count must be the true match count, not what survived "+
			"a retrieval limit:\n%s", out)
	}
}

// F33. memory_context's keyword half is not a recall the user ran. Emitting
// recall.served for it put one row per context call into the arm ADR-0002 §P11
// compares the packer against — with a machine-generated query, and the
// keyword half's ids rather than what the tool served.
func TestMemoryContext_DoesNotEmitRecallEvents(t *testing.T) {
	s, k := setupServer(t)

	if _, _, err := k.Save(types.MemorySaveInput{
		Content: "The API package was split in March.", Type: types.MemoryTypeFact,
		Source: types.MemorySourceUser, FilePaths: []string{"internal/api/users.go"},
	}); err != nil {
		t.Fatal(err)
	}
	out := resultText(t, callTool(t, s, "memory_context", map[string]interface{}{
		"file_paths": []interface{}{"internal/api/users.go"},
	}))
	if !strings.Contains(out, "The API package was split in March.") {
		t.Fatalf("the note should have surfaced; otherwise this proves nothing:\n%s", out)
	}

	recalls, _ := k.Decisions().Events(kernel.EventFilter{Kind: types.EventRecallServed})
	if len(recalls) != 0 {
		t.Errorf("%d recall.served events from a memory_context call — §P11's recall "+
			"arm is contaminated with calls the user never made", len(recalls))
	}

	// An explicit recall still records one: this suppresses the internal
	// caller, not the instrumentation.
	callTool(t, s, "memory_recall", map[string]interface{}{"query": "api"})
	recalls, _ = k.Decisions().Events(kernel.EventFilter{Kind: types.EventRecallServed})
	if len(recalls) != 1 {
		t.Errorf("recall.served events after an explicit recall = %d, want 1", len(recalls))
	}
}

// TestREADME_ListsEveryToolTheServerRegisters pins the public tool list against
// the server.
//
// The README's tool table has now been wrong twice: it claimed seven tools after
// memory_pack shipped, and it described memory_forget as deleting a memory long
// after Amendment 3 made an agent's forget of a governed memory a disposal
// request that deletes nothing. Both survived because nothing compared the
// document to the code. The A2.4 truth pass fixed the same class of drift in
// CLAUDE.md and the setup templates three times; the README was never in its
// scope, and it is the page a stranger reads first.
func TestREADME_ListsEveryToolTheServerRegisters(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("reading README: %v", err)
	}
	text := string(readme)

	// Source of truth: the names the server actually registers.
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading server.go: %v", err)
	}
	names := regexp.MustCompile(`mcp\.NewTool\("([a-z_]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(names) == 0 {
		t.Fatal("found no registered tools in server.go — the extraction pattern is stale")
	}

	seen := map[string]bool{}
	for _, m := range names {
		tool := m[1]
		if seen[tool] {
			continue
		}
		seen[tool] = true
		if !strings.Contains(text, "`"+tool+"`") {
			t.Errorf("the server registers %s and the README never mentions it", tool)
		}
	}
	// The count in prose has to move with the list.
	want := len(seen)
	counts := map[int]string{7: "seven", 8: "eight", 9: "nine", 10: "ten"}
	if word, ok := counts[want]; ok && !strings.Contains(text, word+" tools") {
		t.Errorf("the server registers %d tools; the README does not say %q", want, word+" tools")
	}
}

// The two descriptions that were not merely stale but wrong: an agent reading
// them would expect a delete that does not happen, and a type change the class
// boundary forbids.
func TestREADME_DoesNotPromiseBehaviourTheServerRefuses(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range []string{
		"| `memory_forget` | Delete a memory by ID or query |",
		"| `memory_update` | Edit an existing memory by ID |",
	} {
		if strings.Contains(string(readme), claim) {
			t.Errorf("the README carries a description the server contradicts: %s", claim)
		}
	}
}
