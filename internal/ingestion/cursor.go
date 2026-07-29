package ingestion

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/varve-sh/varve/internal/types"
)

// ImportCursorRules imports memories from a .cursorrules file in the project root.
func ImportCursorRules(projectRoot string) ([]types.MemorySaveInput, error) {
	path := filepath.Join(projectRoot, ".cursorrules")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil // file doesn't exist — not an error
	}

	var results []types.MemorySaveInput
	for _, para := range strings.Split(string(data), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		results = append(results, types.MemorySaveInput{
			Content:    para,
			Type:       types.MemoryTypeConvention,
			Summary:    truncateStr(para, 120),
			Source:     types.MemorySourceImport,
			SourceRef:  "cursor:.cursorrules",
			Confidence: 0.9,
			Tags:       []string{"imported", "cursor"},
			FilePaths:  []string{},
		})
	}
	return results, nil
}
