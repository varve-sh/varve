package ingestion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/varve-sh/varve/internal/types"
)

func writeJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "memories.json")
	if err := os.WriteFile(f, data, 0644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestImportJSON_ArrayOfMemories(t *testing.T) {
	memories := []types.Memory{
		{Content: "We use PostgreSQL", Type: types.MemoryTypeDecision, Tags: []string{"db"}, Confidence: 0.9},
		{Content: "Auth uses JWT RS256", Type: types.MemoryTypeDecision, Tags: []string{"auth"}},
	}
	path := writeJSON(t, memories)

	inputs, err := ImportJSON(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("want 2 inputs, got %d", len(inputs))
	}
	if inputs[0].Content != "We use PostgreSQL" {
		t.Errorf("unexpected content: %s", inputs[0].Content)
	}
	if inputs[0].Type != types.MemoryTypeDecision {
		t.Errorf("type: want decision, got %s", inputs[0].Type)
	}
	if inputs[0].Source != types.MemorySourceImport {
		t.Errorf("source: want import, got %s", inputs[0].Source)
	}
	if inputs[0].Confidence != 0.9 {
		t.Errorf("confidence: want 0.9, got %f", inputs[0].Confidence)
	}
}

func TestImportJSON_SingleObject(t *testing.T) {
	mem := types.Memory{Content: "single memory fact", Type: types.MemoryTypeFact}
	path := writeJSON(t, mem)

	inputs, err := ImportJSON(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("want 1 input, got %d", len(inputs))
	}
	if inputs[0].Content != "single memory fact" {
		t.Errorf("unexpected content: %s", inputs[0].Content)
	}
}

func TestImportJSON_SkipsEmptyContent(t *testing.T) {
	memories := []types.Memory{
		{Content: "valid memory"},
		{Content: ""},
		{Content: "another valid"},
	}
	path := writeJSON(t, memories)

	inputs, err := ImportJSON(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inputs) != 2 {
		t.Errorf("want 2 inputs (empty skipped), got %d", len(inputs))
	}
}

func TestImportJSON_PreservesTagsAndFilePaths(t *testing.T) {
	memories := []types.Memory{
		{
			Content:   "auth middleware location",
			FilePaths: []string{"src/middleware/auth.go"},
			Tags:      []string{"auth", "middleware"},
		},
	}
	path := writeJSON(t, memories)

	inputs, _ := ImportJSON(path)
	if len(inputs[0].FilePaths) != 1 || inputs[0].FilePaths[0] != "src/middleware/auth.go" {
		t.Errorf("unexpected file_paths: %v", inputs[0].FilePaths)
	}
	if len(inputs[0].Tags) != 2 {
		t.Errorf("unexpected tags: %v", inputs[0].Tags)
	}
}

func TestImportJSON_FileNotFound(t *testing.T) {
	_, err := ImportJSON("/nonexistent/path/memories.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestImportJSON_InvalidJSON(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(f, []byte("not json at all"), 0644)

	_, err := ImportJSON(f)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestImportJSON_URL(t *testing.T) {
	memories := []types.Memory{
		{Content: "loaded from URL", Type: types.MemoryTypeFact},
	}
	data, _ := json.Marshal(memories)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	inputs, err := ImportJSON(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inputs) != 1 || inputs[0].Content != "loaded from URL" {
		t.Errorf("unexpected result: %+v", inputs)
	}
}

func TestImportJSON_URL_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := ImportJSON(srv.URL)
	if err == nil {
		t.Error("expected error for HTTP 404")
	}
}
