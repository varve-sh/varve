package ingestion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

func makeMemory(content string, memType types.MemoryType, tags []string, files []string, confidence float64) types.Memory {
	return types.Memory{
		ID:         "01HX000000000000000000000",
		Type:       memType,
		Content:    content,
		Tags:       tags,
		FilePaths:  files,
		Confidence: confidence,
		CreatedAt:  time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC),
	}
}

func TestExportMarkdown_Empty(t *testing.T) {
	out := ExportMarkdown(nil)
	if !strings.HasPrefix(out, "# Varve Export") {
		t.Errorf("missing header, got: %s", out)
	}
	if strings.Contains(out, "## [") {
		t.Errorf("expected no memory sections for empty input")
	}
}

func TestExportMarkdown_SingleMemory(t *testing.T) {
	mem := makeMemory("We use PostgreSQL for all persistence", types.MemoryTypeDecision, []string{"db"}, []string{"internal/db/db.go"}, 0.9)
	out := ExportMarkdown([]types.Memory{mem})

	if !strings.Contains(out, "## [decision] We use PostgreSQL for all persistence") {
		t.Errorf("missing heading, got:\n%s", out)
	}
	if !strings.Contains(out, "- Tags: db") {
		t.Errorf("missing tags line")
	}
	if !strings.Contains(out, "- Confidence: 0.90") {
		t.Errorf("missing confidence line")
	}
	if !strings.Contains(out, "- Files: internal/db/db.go") {
		t.Errorf("missing files line")
	}
	if !strings.Contains(out, "We use PostgreSQL for all persistence") {
		t.Errorf("missing content")
	}
}

func TestExportMarkdown_NoTagsNoFiles(t *testing.T) {
	mem := makeMemory("A plain fact", types.MemoryTypeFact, nil, nil, 1.0)
	out := ExportMarkdown([]types.Memory{mem})

	if strings.Contains(out, "- Tags:") {
		t.Errorf("should not emit Tags line when empty")
	}
	if strings.Contains(out, "- Files:") {
		t.Errorf("should not emit Files line when empty")
	}
}

func TestExportMarkdown_LongContentTruncatedInHeading(t *testing.T) {
	content := strings.Repeat("x", 100)
	mem := makeMemory(content, types.MemoryTypeFact, nil, nil, 1.0)
	out := ExportMarkdown([]types.Memory{mem})

	if !strings.Contains(out, "...") {
		t.Errorf("expected truncation marker in heading")
	}
	// Full content must still be present
	if !strings.Contains(out, content) {
		t.Errorf("full content missing from export")
	}
}

func TestExportMarkdown_MultilineContentFirstLineInHeading(t *testing.T) {
	mem := makeMemory("First line\nSecond line\nThird line", types.MemoryTypeFact, nil, nil, 1.0)
	out := ExportMarkdown([]types.Memory{mem})

	if !strings.Contains(out, "## [fact] First line") {
		t.Errorf("heading should use first line only, got:\n%s", out)
	}
	if !strings.Contains(out, "Second line") {
		t.Errorf("full content including second line should be present")
	}
}

// Round-trip tests

func TestRoundTrip_Basic(t *testing.T) {
	memories := []types.Memory{
		makeMemory("We use Go for all backend services", types.MemoryTypeDecision, []string{"go", "backend"}, []string{"cmd/main.go"}, 0.95),
		makeMemory("Always wrap errors with fmt.Errorf", types.MemoryTypeConvention, []string{"errors"}, nil, 1.0),
		makeMemory("The project was started in 2026", types.MemoryTypeEvent, nil, nil, 0.8),
	}

	doc := ExportMarkdown(memories)
	inputs, err := parseMarkdown(doc)
	if err != nil {
		t.Fatalf("parseMarkdown error: %v", err)
	}
	if len(inputs) != 3 {
		t.Fatalf("want 3 inputs, got %d\ndoc:\n%s", len(inputs), doc)
	}

	if inputs[0].Content != memories[0].Content {
		t.Errorf("[0] content: want %q, got %q", memories[0].Content, inputs[0].Content)
	}
	if inputs[0].Type != types.MemoryTypeDecision {
		t.Errorf("[0] type: want decision, got %s", inputs[0].Type)
	}
	if len(inputs[0].Tags) != 2 || inputs[0].Tags[0] != "go" {
		t.Errorf("[0] tags: want [go backend], got %v", inputs[0].Tags)
	}
	if len(inputs[0].FilePaths) != 1 || inputs[0].FilePaths[0] != "cmd/main.go" {
		t.Errorf("[0] files: want [cmd/main.go], got %v", inputs[0].FilePaths)
	}
	if inputs[0].Confidence != 0.95 {
		t.Errorf("[0] confidence: want 0.95, got %f", inputs[0].Confidence)
	}

	if inputs[1].Type != types.MemoryTypeConvention {
		t.Errorf("[1] type: want convention, got %s", inputs[1].Type)
	}
	if inputs[2].Type != types.MemoryTypeEvent {
		t.Errorf("[2] type: want event, got %s", inputs[2].Type)
	}
}

func TestRoundTrip_MultilineContent(t *testing.T) {
	content := "Line one\nLine two\nLine three"
	mem := makeMemory(content, types.MemoryTypeFact, nil, nil, 1.0)
	doc := ExportMarkdown([]types.Memory{mem})

	inputs, err := parseMarkdown(doc)
	if err != nil {
		t.Fatalf("parseMarkdown error: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("want 1 input, got %d", len(inputs))
	}
	if inputs[0].Content != content {
		t.Errorf("content mismatch:\nwant: %q\n got: %q", content, inputs[0].Content)
	}
}

func TestRoundTrip_SourceSetToImport(t *testing.T) {
	mem := makeMemory("Some fact", types.MemoryTypeFact, nil, nil, 1.0)
	doc := ExportMarkdown([]types.Memory{mem})
	inputs, _ := parseMarkdown(doc)
	if inputs[0].Source != types.MemorySourceImport {
		t.Errorf("source should be import, got %s", inputs[0].Source)
	}
}

// ImportMarkdown (file I/O path)

func TestImportMarkdown_File(t *testing.T) {
	mem := makeMemory("Auth uses JWT RS256", types.MemoryTypeConvention, []string{"auth"}, nil, 1.0)
	doc := ExportMarkdown([]types.Memory{mem})

	path := filepath.Join(t.TempDir(), "memories.md")
	if err := os.WriteFile(path, []byte(doc), 0644); err != nil {
		t.Fatal(err)
	}

	inputs, err := ImportMarkdown(path)
	if err != nil {
		t.Fatalf("ImportMarkdown error: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("want 1, got %d", len(inputs))
	}
	if inputs[0].Content != "Auth uses JWT RS256" {
		t.Errorf("unexpected content: %s", inputs[0].Content)
	}
}

func TestImportMarkdown_FileNotFound(t *testing.T) {
	_, err := ImportMarkdown("/nonexistent/file.md")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestImportMarkdown_HandwrittenFormat(t *testing.T) {
	doc := `# Varve Export

Exported: 2026-03-24T00:00:00Z | 1 memory

---

## [decision] Use SQLite for storage

- Tags: database, sqlite
- Confidence: 0.95
- Created: 2026-03-22T10:00:00Z

We chose SQLite because it requires no separate server process and supports FTS5.

---
`
	inputs, err := parseMarkdown(doc)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("want 1, got %d", len(inputs))
	}
	if inputs[0].Type != types.MemoryTypeDecision {
		t.Errorf("type: want decision, got %s", inputs[0].Type)
	}
	if inputs[0].Confidence != 0.95 {
		t.Errorf("confidence: want 0.95, got %f", inputs[0].Confidence)
	}
	if len(inputs[0].Tags) != 2 {
		t.Errorf("tags: want 2, got %v", inputs[0].Tags)
	}
	if !strings.Contains(inputs[0].Content, "We chose SQLite") {
		t.Errorf("unexpected content: %s", inputs[0].Content)
	}
}

func TestParseMarkdown_SkipsEmptyContent(t *testing.T) {
	doc := `# Varve Export

Exported: 2026-03-24T00:00:00Z | 0 memories

---

## [fact] valid memory

- Confidence: 1.00
- Created: 2026-03-22T10:00:00Z

has content

---

## [fact] no content section

- Confidence: 1.00
- Created: 2026-03-22T10:00:00Z

---
`
	inputs, err := parseMarkdown(doc)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(inputs) != 1 {
		t.Errorf("want 1 (empty-content memory skipped), got %d", len(inputs))
	}
}

func TestParseMarkdown_UnknownTypeDefaultsToFact(t *testing.T) {
	doc := `# Varve Export

---

## [unknown] some content

- Confidence: 1.00
- Created: 2026-03-22T10:00:00Z

some content

---
`
	inputs, _ := parseMarkdown(doc)
	if len(inputs) != 1 {
		t.Fatalf("want 1, got %d", len(inputs))
	}
	if inputs[0].Type != types.MemoryTypeFact {
		t.Errorf("want fact fallback, got %s", inputs[0].Type)
	}
}
