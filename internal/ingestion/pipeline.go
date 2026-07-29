package ingestion

import (
	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/types"
)

// Pipeline coordinates all ingestion processors.
type Pipeline struct {
	kernel *kernel.MemoryKernel
}

// New creates a new ingestion pipeline backed by the given kernel.
func New(k *kernel.MemoryKernel) *Pipeline {
	return &Pipeline{kernel: k}
}

// IngestResult reports how many memories were imported and from which sources.
type IngestResult struct {
	Total   int
	Sources map[string]int
}

// IngestOnInit runs all importers against the given project root.
// Individual importer failures are silently skipped — partial results are returned.
func (p *Pipeline) IngestOnInit(projectRoot string) *IngestResult {
	result := &IngestResult{Sources: make(map[string]int)}

	importAndCount := func(source string, inputs []types.MemorySaveInput, err error) {
		if err != nil || len(inputs) == 0 {
			return
		}
		for _, input := range inputs {
			if _, _, saveErr := p.kernel.Save(input); saveErr == nil {
				result.Total++
				result.Sources[source]++
			}
		}
	}

	claudes, err := ImportClaudeMemories(projectRoot)
	importAndCount("claude", claudes, err)

	cursors, err := ImportCursorRules(projectRoot)
	importAndCount("cursor", cursors, err)

	gits, err := ImportGitHistory(projectRoot, 100)
	importAndCount("git", gits, err)

	return result
}
