package ingestion

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// ImportJSON reads a JSON source (file path or HTTP/HTTPS URL) containing either
// a JSON array of Memory objects (varve export format) or a single Memory object,
// and returns them as MemorySaveInput slice ready for ingestion.
func ImportJSON(source string) ([]types.MemorySaveInput, error) {
	data, err := readSource(source)
	if err != nil {
		return nil, err
	}

	// Try array first, then single object
	var memories []types.Memory
	if err := json.Unmarshal(data, &memories); err != nil {
		var single types.Memory
		if err2 := json.Unmarshal(data, &single); err2 != nil {
			return nil, fmt.Errorf("could not parse JSON as memory array or single memory: %w", err)
		}
		memories = []types.Memory{single}
	}

	inputs := make([]types.MemorySaveInput, 0, len(memories))
	for _, m := range memories {
		if m.Content == "" {
			continue
		}
		inputs = append(inputs, types.MemorySaveInput{
			Content:    m.Content,
			Summary:    m.Summary,
			Type:       m.Type,
			Source:     types.MemorySourceImport,
			Confidence: m.Confidence,
			FilePaths:  m.FilePaths,
			Tags:       m.Tags,
		})
	}
	return inputs, nil
}

func readSource(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(source)
		if err != nil {
			return nil, fmt.Errorf("fetching URL: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, source)
		}
		return io.ReadAll(resp.Body)
	}
	return os.ReadFile(source)
}
