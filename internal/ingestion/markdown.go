package ingestion

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// ExportMarkdown renders a slice of memories as a human-readable Markdown document
// that can be round-tripped through ImportMarkdown.
func ExportMarkdown(memories []types.Memory) string {
	var b strings.Builder
	b.WriteString("# Varve Export\n\n")
	b.WriteString(fmt.Sprintf("Exported: %s | %d %s\n",
		time.Now().UTC().Format(time.RFC3339),
		len(memories),
		mdPlural(len(memories), "memory", "memories"),
	))

	for _, m := range memories {
		b.WriteString("\n---\n\n")

		firstLine := mdFirstLine(m.Content, 80)
		b.WriteString(fmt.Sprintf("## [%s] %s\n\n", m.Type, firstLine))

		if len(m.Tags) > 0 {
			b.WriteString(fmt.Sprintf("- Tags: %s\n", strings.Join(m.Tags, ", ")))
		}
		b.WriteString(fmt.Sprintf("- Confidence: %.2f\n", m.Confidence))
		b.WriteString(fmt.Sprintf("- Created: %s\n", m.CreatedAt.UTC().Format(time.RFC3339)))
		if len(m.FilePaths) > 0 {
			b.WriteString(fmt.Sprintf("- Files: %s\n", strings.Join(m.FilePaths, ", ")))
		}

		b.WriteString("\n")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}

	if len(memories) > 0 {
		b.WriteString("\n---\n")
	}

	return b.String()
}

// ImportMarkdown parses a Markdown document produced by ExportMarkdown (or hand-written
// in the same format) and returns a MemorySaveInput slice ready for ingestion.
func ImportMarkdown(source string) ([]types.MemorySaveInput, error) {
	data, err := readSource(source)
	if err != nil {
		return nil, err
	}
	return parseMarkdown(string(data))
}

func parseMarkdown(doc string) ([]types.MemorySaveInput, error) {
	doc = strings.ReplaceAll(doc, "\r\n", "\n")

	// Split on horizontal rules; section[0] is the file header — skip it.
	sections := strings.Split(doc, "\n---\n")

	var inputs []types.MemorySaveInput
	for _, section := range sections[1:] {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}
		input, ok := parseMarkdownSection(section)
		if !ok || input.Content == "" {
			continue
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

// parseMarkdownSection parses one memory block:
//
//	## [type] first line of content
//
//	- Tags: a, b
//	- Confidence: 0.90
//	- Created: 2026-03-22T10:00:00Z
//	- Files: path/to/file.go
//
//	Full content text...
func parseMarkdownSection(section string) (types.MemorySaveInput, bool) {
	lines := strings.Split(section, "\n")
	if len(lines) == 0 {
		return types.MemorySaveInput{}, false
	}

	var input types.MemorySaveInput
	input.Source = types.MemorySourceImport

	// Heading must be "## [type] ..."
	heading := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(heading, "## [") {
		return types.MemorySaveInput{}, false
	}
	end := strings.Index(heading, "]")
	if end < 0 {
		return types.MemorySaveInput{}, false
	}
	memType := types.MemoryType(heading[4:end])
	switch memType {
	case types.MemoryTypeDecision, types.MemoryTypeConvention,
		types.MemoryTypeFact, types.MemoryTypeEvent:
		input.Type = memType
	default:
		input.Type = types.MemoryTypeFact
	}

	// Skip blank line(s) after heading, then parse "- Key: value" metadata.
	i := 1
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			break // blank line ends the metadata block
		}
		if !strings.HasPrefix(line, "- ") {
			break // not a list item — content has started without a blank separator
		}
		kv := strings.TrimPrefix(line, "- ")
		sep := strings.Index(kv, ": ")
		if sep >= 0 {
			key := strings.ToLower(strings.TrimSpace(kv[:sep]))
			val := strings.TrimSpace(kv[sep+2:])
			switch key {
			case "tags":
				for _, t := range strings.Split(val, ",") {
					if t = strings.TrimSpace(t); t != "" {
						input.Tags = append(input.Tags, t)
					}
				}
			case "confidence":
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					input.Confidence = f
				}
			case "files":
				for _, f := range strings.Split(val, ",") {
					if f = strings.TrimSpace(f); f != "" {
						input.FilePaths = append(input.FilePaths, f)
					}
				}
			}
		}
		i++
	}

	input.Content = strings.TrimSpace(strings.Join(lines[i:], "\n"))
	return input, true
}

func mdFirstLine(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

func mdPlural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
