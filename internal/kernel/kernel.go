package kernel

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memtrace-dev/memtrace/internal/embedding"
	"github.com/memtrace-dev/memtrace/internal/retrieval"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"

	_ "modernc.org/sqlite" // register the "sqlite" driver
)

// MemoryKernel is the single facade for all memory operations.
// All other modules (CLI, MCP, ingestion) must go through the kernel.
type MemoryKernel struct {
	dbPath    string
	projectID string
	db        *sql.DB
	store     *MemoryStore
	pipeline  *retrieval.Pipeline
	embedder  embedding.Embedder // nil when embeddings are not configured
}

// OpenDB opens a SQLite database with the per-connection PRAGMAs of ADR-0001
// §D8 baked into the DSN. database/sql pools connections, so PRAGMAs issued as
// statements only bind the one connection that ran them — foreign_keys in
// particular has to come from the DSN or the events→decisions FK that blocks
// hard-deleting a decision with history is silently unenforced.
func OpenDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?" + url.Values{
		"_pragma": {"foreign_keys(1)", "busy_timeout(5000)", "synchronous(NORMAL)"},
	}.Encode()
	return sql.Open("sqlite", dsn)
}

// New creates a new MemoryKernel. Call Open() before any other method.
func New(dbPath string, projectID string) *MemoryKernel {
	return &MemoryKernel{
		dbPath:    dbPath,
		projectID: projectID,
	}
}

// Open opens the database connection, applies the schema, and sets PRAGMAs.
func (k *MemoryKernel) Open() error {
	db, err := OpenDB(k.dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	if err := ApplySchema(db); err != nil {
		db.Close()
		if errors.Is(err, types.ErrLegacyDatabase) {
			return err
		}
		return fmt.Errorf("applying schema: %w", err)
	}
	k.db = db
	k.store = NewStore(db)
	k.pipeline = retrieval.New(k.store, k.projectID) // MemoryStore satisfies retrieval.StoreReader

	// Wire up optional embedder.
	// Priority: env vars > config file > Ollama auto-detect.
	cfg := util.GetProjectConfig()
	providerOverride := firstNonEmpty(os.Getenv("MEMTRACE_EMBED_PROVIDER"), cfg.Embed.Provider)
	if providerOverride != "disabled" {
		key := firstNonEmpty(os.Getenv("MEMTRACE_EMBED_KEY"), os.Getenv("OPENAI_API_KEY"), cfg.Embed.Key)
		url := firstNonEmpty(os.Getenv("MEMTRACE_EMBED_URL"), cfg.Embed.URL)
		model := firstNonEmpty(os.Getenv("MEMTRACE_EMBED_MODEL"), cfg.Embed.Model)

		// Use *Client as intermediate to avoid the interface-wrapping-nil-pointer bug.
		var ec *embedding.Client
		switch {
		case key != "":
			// Authenticated endpoint (OpenAI or custom with key)
			ec = embedding.NewClient(key, "", url, model)
		case url != "":
			// Local endpoint configured without a key (Ollama, llama.cpp, etc.)
			ec = embedding.NewLocalClient(url, model)
		default:
			// Auto-detect: probe Ollama on localhost
			ec = embedding.ProbeOllama()
		}
		if ec != nil {
			k.embedder = ec
			k.pipeline.WithEmbedder(ec)
		}
	}
	return nil
}

// Close closes the underlying database connection.
func (k *MemoryKernel) Close() error {
	if k.db != nil {
		return k.db.Close()
	}
	return nil
}

// Save validates input, generates ID and timestamps, and writes to the store.
// Returns the memory and a boolean indicating whether an existing memory was
// updated via topic_key upsert (true) or a new one was created (false).
func (k *MemoryKernel) Save(input types.MemorySaveInput) (*types.Memory, bool, error) {
	// Apply defaults
	if input.Type == "" {
		input.Type = types.MemoryTypeFact
	}
	if input.Source == "" {
		input.Source = types.MemorySourceUser
	}
	if input.Confidence == 0 {
		input.Confidence = 1.0
	}
	if input.FilePaths == nil {
		input.FilePaths = []string{}
	}
	if input.Tags == nil {
		input.Tags = []string{}
	}
	// Strip <private>...</private> blocks before anything touches the content.
	input.Content = util.StripPrivate(input.Content)

	if input.Summary == "" && input.Content != "" {
		input.Summary = truncate(input.Content, 120)
	}

	// Topic key upsert: if a key is provided and an active memory already has it,
	// update that memory instead of creating a duplicate.
	if input.TopicKey != "" {
		existing, err := k.store.FindByTopicKey(k.projectID, input.TopicKey)
		if err != nil {
			return nil, false, fmt.Errorf("topic key lookup: %w", err)
		}
		if existing != nil {
			updateInput := types.MemoryUpdateInput{
				Content: &input.Content,
				Summary: &input.Summary,
			}
			if input.Type != "" {
				updateInput.Type = &input.Type
			}
			if len(input.Tags) > 0 {
				updateInput.Tags = &input.Tags
			}
			if len(input.FilePaths) > 0 {
				updateInput.FilePaths = &input.FilePaths
			}
			if input.Confidence != 1.0 {
				updateInput.Confidence = &input.Confidence
			}
			mem, err := k.Update(existing.ID, updateInput)
			if err != nil {
				return nil, false, err
			}
			return mem, true, nil
		}
	}

	now := time.Now().UTC()
	mem := &types.Memory{
		ID:         util.GenerateID(),
		Type:       input.Type,
		Content:    input.Content,
		Summary:    input.Summary,
		Source:     input.Source,
		SourceRef:  input.SourceRef,
		Confidence: input.Confidence,
		ProjectID:  k.projectID,
		FilePaths:  input.FilePaths,
		Tags:       input.Tags,
		TopicKey:   input.TopicKey,
		Status:     types.MemoryStatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := k.store.Insert(mem); err != nil {
		return nil, false, fmt.Errorf("saving memory: %w", err)
	}

	// Compute and persist embedding asynchronously so Save() stays fast.
	if k.embedder != nil {
		go func(id, text string) {
			vec, err := k.embedder.Embed(text)
			if err == nil {
				_ = k.store.StoreEmbedding(id, vec)
			}
		}(mem.ID, mem.Content)
	}

	return mem, false, nil
}

// Get retrieves a single memory by ID. Returns nil, nil if not found.
func (k *MemoryKernel) Get(id string) (*types.Memory, error) {
	return k.store.FindByID(id)
}

// Update partially updates a memory. Only non-nil fields in input are changed.
func (k *MemoryKernel) Update(id string, input types.MemoryUpdateInput) (*types.Memory, error) {
	if err := k.store.Update(id, input); err != nil {
		return nil, fmt.Errorf("updating memory: %w", err)
	}
	// Re-embed asynchronously when content changes so the vector stays in sync.
	if input.Content != nil && k.embedder != nil {
		go func(id, text string) {
			vec, err := k.embedder.Embed(text)
			if err == nil {
				_ = k.store.StoreEmbedding(id, vec)
			}
		}(id, *input.Content)
	}
	return k.store.FindByID(id)
}

// Delete hard-deletes a memory by ID. Returns false if not found.
func (k *MemoryKernel) Delete(id string) (bool, error) {
	return k.store.DeleteByID(id)
}

// List returns memories matching the given options.
func (k *MemoryKernel) List(opts types.ListOptions) ([]types.Memory, error) {
	return k.store.List(opts)
}

// Count returns the number of memories matching the given filters.
func (k *MemoryKernel) Count(memType types.MemoryType, status types.MemoryStatus) (int, error) {
	return k.store.Count(memType, status)
}

// Recall searches memories using the retrieval pipeline, then updates access tracking.
func (k *MemoryKernel) Recall(input types.MemoryRecallInput) ([]types.ScoredMemory, error) {
	if input.Limit <= 0 {
		input.Limit = 10
	}
	if input.Limit > 50 {
		input.Limit = 50
	}
	if input.Status == "" {
		input.Status = types.MemoryStatusActive
	}

	results, err := k.pipeline.Recall(input)
	if err != nil {
		return nil, fmt.Errorf("recall: %w", err)
	}

	// Update access tracking for returned memories
	now := time.Now().UTC()
	for _, r := range results {
		_ = k.store.TouchAccess(r.Memory.ID, now)
	}

	return results, nil
}

// UnembeddedCount returns the number of active memories with no stored embedding.
func (k *MemoryKernel) UnembeddedCount() (int, error) {
	rows, err := k.store.FindUnembedded(k.projectID)
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

// HasEmbedder reports whether an embedding client is configured.
func (k *MemoryKernel) HasEmbedder() bool {
	return k.embedder != nil
}

// EmbedInfo returns the provider label and model name for the active embedder.
// Returns ("disabled", "") when no embedder is configured.
func (k *MemoryKernel) EmbedInfo() (provider, model string) {
	if k.embedder == nil {
		return "disabled", ""
	}
	type infoer interface {
		Provider() string
		Model() string
	}
	if i, ok := k.embedder.(infoer); ok {
		return i.Provider(), i.Model()
	}
	return "enabled", ""
}

// ReindexResult holds the outcome of a Reindex run.
type ReindexResult struct {
	Total     int   // memories with no stored embedding
	Succeeded int   // successfully embedded and stored
	FirstErr  error // first embed/store error encountered, if any
}

// Reindex computes and persists embeddings for all active memories that have
// none stored yet. If no embedder is configured it returns a zero result.
func (k *MemoryKernel) Reindex(progress func(done, total int)) (ReindexResult, error) {
	if k.embedder == nil {
		return ReindexResult{}, nil
	}

	rows, err := k.store.FindUnembedded(k.projectID)
	if err != nil {
		return ReindexResult{}, fmt.Errorf("listing unembedded memories: %w", err)
	}

	res := ReindexResult{Total: len(rows)}
	for _, row := range rows {
		vec, err := k.embedder.Embed(row.Content)
		if err != nil {
			if res.FirstErr == nil {
				res.FirstErr = fmt.Errorf("embed %s: %w", row.ID[:8], err)
			}
			continue
		}
		if storeErr := k.store.StoreEmbedding(row.ID, vec); storeErr != nil {
			if res.FirstErr == nil {
				res.FirstErr = fmt.Errorf("store %s: %w", row.ID[:8], storeErr)
			}
			continue
		}
		res.Succeeded++
		if progress != nil {
			progress(res.Succeeded, res.Total)
		}
	}
	return res, nil
}

// Store returns the underlying store (used by the retrieval pipeline).
func (k *MemoryKernel) Store() *MemoryStore {
	return k.store
}

// StaleDetail describes a single memory that was marked stale during a scan.
type StaleDetail struct {
	MemoryID string
	Summary  string
	Reason   string // e.g. "file deleted: src/auth.go" or "file modified: src/auth.go"
}

// ScanResult holds the outcome of a ScanStaleness run.
type ScanResult struct {
	Checked int           // active memories with file_paths that were examined
	Marked  int           // memories newly marked as stale
	Details []StaleDetail // one entry per newly-stale memory
}

// ScanStaleness checks every active memory that references file_paths.
// A memory is marked stale when any of its referenced files has been deleted
// or modified more recently than the memory was last updated.
// projectRoot is the absolute path to the project directory (file_paths are relative to it).
func (k *MemoryKernel) ScanStaleness(projectRoot string) (ScanResult, error) {
	memories, err := k.store.List(types.ListOptions{
		Status: types.MemoryStatusActive,
		Limit:  10000,
	})
	if err != nil {
		return ScanResult{}, fmt.Errorf("listing memories: %w", err)
	}

	var res ScanResult
	for i := range memories {
		m := &memories[i]
		if len(m.FilePaths) == 0 {
			continue
		}
		res.Checked++

		reason := stalenessReason(projectRoot, m)
		if reason == "" {
			continue
		}

		stale := types.MemoryStatusStale
		if err := k.store.Update(m.ID, types.MemoryUpdateInput{Status: &stale}); err != nil {
			return res, fmt.Errorf("marking %s stale: %w", m.ID[:8], err)
		}
		res.Marked++
		res.Details = append(res.Details, StaleDetail{
			MemoryID: m.ID,
			Summary:  truncate(m.Content, 60),
			Reason:   reason,
		})
	}
	return res, nil
}

// ContextForFiles returns memories relevant to the given file paths.
// It combines two signals:
//  1. Direct file match — memories whose file_paths overlap the input
//  2. Inferred keyword recall — file/dir names used as a search query
//
// File-matched memories are returned first (score=1.0), followed by
// keyword-matched memories not already in the file set.
func (k *MemoryKernel) ContextForFiles(filePaths []string, limit int) ([]types.ScoredMemory, error) {
	if len(filePaths) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	// 1. Direct file match
	byFile, err := k.store.FindByFilePaths(k.projectID, filePaths)
	if err != nil {
		return nil, fmt.Errorf("file match: %w", err)
	}

	// 2. Keyword search inferred from file paths
	query := filePathsToQuery(filePaths)
	byQuery, err := k.Recall(types.MemoryRecallInput{
		Query: query,
		Limit: limit * 2,
	})
	if err != nil {
		return nil, fmt.Errorf("keyword recall: %w", err)
	}

	// 3. Merge, deduplicating by ID. File-matched memories get score=1.0.
	seen := make(map[string]bool, len(byFile)+len(byQuery))
	results := make([]types.ScoredMemory, 0, limit)

	for i := range byFile {
		m := &byFile[i]
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		results = append(results, types.ScoredMemory{Memory: *m, Score: 1.0})
	}
	for _, r := range byQuery {
		if seen[r.Memory.ID] {
			continue
		}
		seen[r.Memory.ID] = true
		results = append(results, r)
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// filePathsToQuery extracts meaningful search terms from a list of file paths.
// "src/auth/middleware.go" → "auth middleware"
func filePathsToQuery(paths []string) string {
	seen := make(map[string]bool)
	var terms []string
	for _, p := range paths {
		// Collect dir components (skip "." and common noise)
		dir := filepath.Dir(p)
		if dir != "." {
			for _, seg := range strings.Split(dir, string(filepath.Separator)) {
				if seg == "." || seg == "" {
					continue
				}
				for _, part := range strings.FieldsFunc(seg, func(r rune) bool {
					return r == '_' || r == '-'
				}) {
					if !seen[part] {
						seen[part] = true
						terms = append(terms, part)
					}
				}
			}
		}
		// Collect filename without extension, split on _ - .
		base := filepath.Base(p)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		for _, part := range strings.FieldsFunc(name, func(r rune) bool {
			return r == '_' || r == '-' || r == '.'
		}) {
			if !seen[part] {
				seen[part] = true
				terms = append(terms, part)
			}
		}
	}
	return strings.Join(terms, " ")
}

// StatsResult holds usage metrics for the memory store.
type StatsResult struct {
	TotalActive      int
	SavedThisWeek    int
	RecallsThisWeek  int
	SessionsThisWeek int
	TopAccessed      []types.Memory
}

// Stats returns usage metrics over the last window duration.
func (k *MemoryKernel) Stats(window time.Duration) (StatsResult, error) {
	since := time.Now().UTC().Add(-window)

	total, err := k.store.Count("", types.MemoryStatusActive)
	if err != nil {
		return StatsResult{}, err
	}

	saved, err := k.store.CountSince("created_at", since)
	if err != nil {
		return StatsResult{}, err
	}

	recalls, err := k.store.CountSince("accessed_at", since)
	if err != nil {
		return StatsResult{}, err
	}

	// Sessions are event memories tagged "session"
	sessions, err := k.store.List(types.ListOptions{
		Type:   types.MemoryTypeEvent,
		Status: types.MemoryStatusActive,
		Sort:   "created_at",
		Limit:  1000,
	})
	if err != nil {
		return StatsResult{}, err
	}
	sessionCount := 0
	for _, m := range sessions {
		for _, tag := range m.Tags {
			if tag == "session" && m.CreatedAt.After(since) {
				sessionCount++
				break
			}
		}
	}

	top, err := k.store.TopAccessed(k.projectID, 5)
	if err != nil {
		return StatsResult{}, err
	}

	return StatsResult{
		TotalActive:      total,
		SavedThisWeek:    saved,
		RecallsThisWeek:  recalls,
		SessionsThisWeek: sessionCount,
		TopAccessed:      top,
	}, nil
}

// stalenessReason returns a non-empty string describing why m is stale, or ""
// if all referenced files are present and unmodified since m.UpdatedAt.
func stalenessReason(projectRoot string, m *types.Memory) string {
	for _, rel := range m.FilePaths {
		abs := filepath.Join(projectRoot, rel)
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return "file deleted: " + rel
			}
			continue // stat error — skip rather than false-positive
		}
		if info.ModTime().After(m.UpdatedAt) {
			return "file modified: " + rel
		}
	}
	return ""
}

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
