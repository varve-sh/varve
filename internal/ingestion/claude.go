package ingestion

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/varve-sh/varve/internal/types"
)

// ImportClaudeMemories imports memories from Claude Code's ~/.claude/projects/ directory.
func ImportClaudeMemories(projectRoot string) ([]types.MemorySaveInput, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}

	// Build the Claude project path: replace "/" with "-" in the absolute path.
	projectKey := strings.ReplaceAll(projectRoot, "/", "-")
	claudeMemoryDir := filepath.Join(home, ".claude", "projects", projectKey, "memory")

	entries, err := os.ReadDir(claudeMemoryDir)
	if err != nil {
		return nil, nil // directory doesn't exist — not an error
	}

	var results []types.MemorySaveInput
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(claudeMemoryDir, entry.Name()))
		if err != nil {
			continue
		}
		frontmatter, body := parseFrontmatter(string(data))
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}

		memType := mapFrontmatterType(frontmatter["type"])
		summary := frontmatter["description"]
		if summary == "" {
			summary = truncateStr(body, 120)
		}

		results = append(results, types.MemorySaveInput{
			Content:    body,
			Type:       memType,
			Summary:    summary,
			Source:     types.MemorySourceImport,
			SourceRef:  "claude:" + entry.Name(),
			Confidence: 0.9,
			Tags:       []string{"imported", "claude"},
			FilePaths:  []string{},
		})
	}
	return results, nil
}

// parseFrontmatter splits YAML frontmatter from the rest of the content.
// Returns a map of key→value pairs and the body after the second "---".
func parseFrontmatter(content string) (map[string]string, string) {
	fm := make(map[string]string)
	if !strings.HasPrefix(content, "---") {
		return fm, content
	}
	// Find the closing "---"
	rest := content[3:]
	if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	end := strings.Index(rest, "---")
	if end == -1 {
		return fm, content
	}
	fmBlock := rest[:end]
	body := rest[end+3:]
	if strings.HasPrefix(body, "\n") {
		body = body[1:]
	}

	for _, line := range strings.Split(fmBlock, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx == -1 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		fm[key] = val
	}
	return fm, body
}

func mapFrontmatterType(s string) types.MemoryType {
	switch strings.ToLower(s) {
	case "decision":
		return types.MemoryTypeDecision
	case "convention":
		return types.MemoryTypeConvention
	case "event":
		return types.MemoryTypeEvent
	default:
		return types.MemoryTypeFact
	}
}

func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
