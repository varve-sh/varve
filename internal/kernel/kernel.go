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
	decisions *DecisionStore
	notes     *NoteStore
	pipeline  *retrieval.Pipeline
	embedder  embedding.Embedder // nil when embeddings are not configured
	// session carries the attribution session this kernel's events belong to
	// (ADR-0004 §D3). Zero value = no session; events are then written with a
	// NULL session_id, as an uninstrumented caller's are.
	session session
}

// OpenDB opens a SQLite database with the per-connection PRAGMAs of ADR-0001
// §D8 baked into the DSN. database/sql pools connections, so PRAGMAs issued as
// statements only bind the one connection that ran them — foreign_keys in
// particular has to come from the DSN or the events→decisions FK that blocks
// hard-deleting a decision with history is silently unenforced.
func OpenDB(path string) (*sql.DB, error) {
	dsn := "file:" + escapeDSNPath(path) + "?" + url.Values{
		"_pragma": {"foreign_keys(1)", "busy_timeout(5000)", "synchronous(NORMAL)"},
		// BEGIN IMMEDIATE, not BEGIN DEFERRED. Our write transactions read
		// before they write (topic_key holder lookup, evidence count), and a
		// deferred transaction that upgrades read→write is failed by SQLite
		// with SQLITE_BUSY_SNAPSHOT *without* invoking the busy handler — so
		// §D8's busy_timeout bought nothing on exactly the path that needed
		// it. Taking the write lock up front puts the contention where
		// busy_timeout applies. Measured: 6 writers x 40 propose+accept cycles
		// went from ~25 failures in 480 operations to zero.
		"_txlock": {"immediate"},
	}.Encode()
	return sql.Open("sqlite", dsn)
}

// escapeDSNPath percent-encodes the characters that would otherwise be read as
// DSN or URI syntax rather than as part of the filename.
//
// The driver finds the query string with the *first* '?' in the DSN, and hands
// the whole string to SQLite with SQLITE_OPEN_URI, which parses '?' and '#'
// itself and percent-decodes what remains. A project path containing any of
// them therefore truncates the query string, and the PRAGMAs — foreign_keys
// among them — silently do not apply. That is the exact failure this DSN was
// introduced to fix, so it must not be reachable through a directory name.
func escapeDSNPath(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); i++ {
		switch c := path[i]; c {
		case '%', '?', '#':
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
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
	k.decisions = NewDecisionStore(db)
	k.notes = NewNoteStore(db)
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

// Close ends any open session and closes the underlying database connection.
func (k *MemoryKernel) Close() error {
	if k.db != nil {
		k.EndSession()
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
		input.Type = types.MemoryTypeNote
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

	// Decisions and conventions are governed: they carry a lifecycle, they are
	// born quarantined unless the human saved them directly, and every write
	// emits its event (ADR-0001 D1, D2, D7). They never take the note path,
	// including the topic_key branch below — a decision saved under an existing
	// key becomes a proposed successor, not an in-place update (D4).
	if input.Type.IsDecision() {
		return k.saveDecision(input)
	}

	// Topic key upsert: if a key is provided and an active *note* already has
	// it, update that note instead of creating a duplicate.
	//
	// The lookup is per table, because §D8's topic_key indexes are (F13): a note
	// and a decision may hold the same key, and a note save that resolved a
	// decision's key routed into MemoryStore.Update, which refuses decisions —
	// so the note was silently never written and the caller got an internal
	// error string. proposeTx has always looked only at `decisions`; that
	// asymmetry is what proved this a bug rather than a policy.
	if input.TopicKey != "" {
		existing, err := k.notes.FindByTopicKey(k.projectID, input.TopicKey)
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
			k.countSessionSave()
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
	// The declared subtype is discarded, not stored and not kept as a tag: the
	// v2 schema has one note class and `fact`/`event` are input synonyms
	// (ADR-0001 D1, confirmed in Amendment 2 A2.3). The v1 subtype was "a
	// label, not a behaviour" and no read path ever branched on it; the
	// accepted cost, recorded there, is that an export round-trip loses the
	// label. D9's tag preservation is about migrated rows' existing tags
	// (`session`, `prompt`), which are unaffected.
	mem.Type = types.MemoryTypeNote

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

	k.countSessionSave()
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

// Delete removes a memory.
//
// Notes keep v1 hard-delete semantics. A decision that has any event history is
// never hard-deleted (ADR-0001 D3): "forget" maps onto a lifecycle transition —
// `rejected` while proposed, `reverted` once binding — so the audit record
// survives. Only an event-free decision can actually be dropped, and the FK
// from `events` is the backstop.
func (k *MemoryKernel) Delete(id string) (bool, error) {
	class, err := k.store.classOf(id)
	if err != nil || class == "" {
		return false, err
	}
	if class == "note" {
		return k.store.DeleteByID(id)
	}

	d, err := k.decisions.GetDecision(id)
	if err != nil {
		return false, err
	}
	events, err := k.decisions.Events(EventFilter{DecisionID: id, Limit: 1})
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return k.store.DeleteByID(id)
	}
	switch d.Status {
	case types.StatusProposed:
		return true, k.decisions.Reject(id, "forgotten")
	case types.StatusActive, types.StatusViolated:
		return true, k.decisions.Revert(id, RevertOptions{Via: "human", Actor: types.ActorHuman})
	default:
		return false, nil // already terminal — nothing to do
	}
}

// Decisions exposes the governed store for callers that need the lifecycle.
func (k *MemoryKernel) Decisions() *DecisionStore { return k.decisions }

// Notes exposes the ungoverned store.
func (k *MemoryKernel) Notes() *NoteStore { return k.notes }

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
	// No status default: empty means "everything live" — non-terminal decisions
	// plus active notes (ADR-0001 D10). Defaulting to `active` here would hide
	// every `proposed` and `violated` decision, and a violated decision is
	// still binding.

	results, err := k.pipeline.Recall(input)
	if err != nil {
		return nil, fmt.Errorf("recall: %w", err)
	}

	// Update access tracking for returned memories
	now := time.Now().UTC()
	ids := make([]string, 0, len(results))
	for _, r := range results {
		_ = k.store.TouchAccess(r.Memory.ID, now)
		ids = append(ids, r.Memory.ID)
	}

	// recall.served (ADR-0001 D7). The legacy read path is instrumented from
	// the same day as the packer so the packer-vs-recall comparison (Phase 0
	// ruling 3, ADR-0002 P11) is available from event one. Emission never fails
	// a recall: a read path that errors because its telemetry failed would be a
	// worse bug than missing telemetry.
	//
	// The session stamp is what makes it joinable. A recall with a NULL
	// session_id is neither attributable nor identifiable as CLI; §P11's join
	// walks recall.served → session window → diff.scope_match, and both ends of
	// that walk need the id (ADR-0004 §D3).
	sessionID, agent, model := k.sessionStamp()
	if input.SessionID != "" {
		sessionID = input.SessionID
	}
	k.countSessionRecall()
	_ = k.decisions.RecordRecall(k.projectID, input, ids, sessionID, agent, model)

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
	// Notes only. Staleness is v1's mtime heuristic and it still serves notes
	// (ADR-0001 open question 8). Decisions do not go stale: they have expiry,
	// which is a derived predicate and changes no state (D2), and mtime is not
	// evidence that a decision stopped being true.
	memories, err := k.store.List(types.ListOptions{
		Type:   types.MemoryTypeNote,
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
	// TotalLive counts everything still live: active notes plus non-terminal
	// decisions (proposed, active, violated). Counting `active` only left every
	// proposed decision out of every total the product prints (ADR-0001 D10).
	TotalLive        int
	SavedThisWeek    int
	RecallsThisWeek  int
	SessionsThisWeek int
	TopAccessed      []types.Memory
}

// Stats returns usage metrics over the last window duration.
func (k *MemoryKernel) Stats(window time.Duration) (StatsResult, error) {
	since := time.Now().UTC().Add(-window)

	total, err := k.store.Count("", "")
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

	// Sessions are notes tagged "session". The tag is the discriminator, not the
	// type: the v2 schema has one note class and D9 preserves the tag exactly so
	// this keeps working.
	sessions, err := k.store.List(types.ListOptions{
		Type:   types.MemoryTypeNote,
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
		TotalLive:        total,
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

// saveDecision is Save's governed branch (ADR-0001 D1, D2, D4).
//
// Birth state follows the source, not the caller's wishes: a `user` save is the
// human's own CLI/TUI action, so it is proposed and accepted in one transaction
// (D2's "the human is the confirmation", implemented as a real transition so
// the decision.accepted event exists). Everything else — agent, git, import,
// derived — is quarantined as `proposed` until a human accepts it. This is the
// behaviour change ADR-0001's Consequences section calls out: an agent's
// decisions no longer bind on save.
func (k *MemoryKernel) saveDecision(input types.MemorySaveInput) (*types.Memory, bool, error) {
	title := input.Summary
	if title == "" {
		title = truncate(input.Content, 120)
	}
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	if r := []rune(title); len(r) > 200 {
		title = string(r[:200])
	}
	if title == "" {
		return nil, false, &types.ValidationError{
			Field: "content", Message: "a decision needs a title — provide content or a summary",
		}
	}

	in := DecisionInput{
		ProjectID:  k.projectID,
		Kind:       types.DecisionKind(input.Type),
		Title:      title,
		Body:       input.Content,
		Scope:      input.FilePaths,
		Confidence: input.Confidence,
		Source:     types.DecisionSource(input.Source),
		SourceRef:  input.SourceRef,
		Agent:      input.Agent,
		Model:      input.Model,
		SessionID:  input.SessionID,
		TopicKey:   input.TopicKey,
		Tags:       input.Tags,
	}
	// A source_ref is the closest thing a save carries to evidence, and
	// attaching it here is what keeps CLI-saved decisions off the `forced` list
	// (D4) — the same rule the v1→v2 migration applies.
	if ref := strings.TrimSpace(input.SourceRef); ref != "" {
		ev := EvidenceInput{Kind: types.EvidenceKindImport, Ref: ref, AddedBy: types.ActorSystem}
		if sha, ok := strings.CutPrefix(ref, "git:"); ok && sha != "" {
			ev = EvidenceInput{Kind: types.EvidenceKindCommit, Ref: sha, AddedBy: types.ActorSystem}
		}
		in.Evidence = append(in.Evidence, ev)
	}

	var d *types.Decision
	var err error
	if in.Source.MayBeBornActive() {
		d, err = k.decisions.ProposeAccepted(in, AcceptOptions{
			// Forced only when there is genuinely no evidence. The flag is the
			// audit trail's record of which decisions are unevidenced, so it
			// has to mean that and nothing else.
			Force: len(in.Evidence) == 0,
			Actor: types.ActorHuman,
		})
	} else {
		d, err = k.decisions.Propose(in)
	}
	if err != nil {
		return nil, false, fmt.Errorf("saving decision: %w", err)
	}

	if k.embedder != nil {
		go func(id, text string) {
			vec, embedErr := k.embedder.Embed(text)
			if embedErr == nil {
				_ = k.store.StoreEmbedding(id, vec)
			}
		}(d.ID, input.Content)
	}

	mem, err := k.store.FindByID(d.ID)
	if err != nil {
		return nil, false, err
	}
	k.countSessionSave()
	// A topic_key collision creates a successor, never an in-place update (D4),
	// so this path never reports an upsert.
	return mem, false, nil
}

// PromoteOverrides lets the caller override fields derived from the note when
// promoting it (A2.3). Everything else is EditProposed's job afterwards.
type PromoteOverrides struct {
	Title string
	Kind  types.DecisionKind
	Scope []string
}

// PromoteNote creates a `proposed` decision from a note (ADR-0001 Amendment 2,
// A2.3). CLI and TUI only — the human-confirmation channel; an agent that
// wants a note promoted proposes a decision citing it.
//
// "I wrote this as a note and now realise it is a decision" is a natural
// motion, and the sanctioned answer is not a retype: a decision born by
// column-flip would have no decision.proposed event, no provenance and no
// quarantine, and an agent-reachable retype would be a quarantine bypass (D1,
// rejected alternative (i)).
//
// The note stays `active` while the promotion is proposed and is archived by
// the acceptance transaction; a rejected promotion leaves it untouched.
func (k *MemoryKernel) PromoteNote(noteID string, over PromoteOverrides) (*types.Decision, error) {
	n, err := k.notes.Get(noteID)
	if err != nil {
		return nil, err
	}
	if n.Status == types.MemoryStatusArchived {
		return nil, &types.ValidationError{
			Field:   "note",
			Message: "an archived note has nothing to promote — reactivate it first",
		}
	}

	title := over.Title
	if title == "" {
		title = n.Summary
	}
	if title == "" {
		title = truncate(n.Content, 120)
	}
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	if r := []rune(title); len(r) > 200 {
		title = string(r[:200])
	}
	if title == "" {
		return nil, &types.ValidationError{
			Field: "title", Message: "the note is empty — nothing to promote",
		}
	}

	kind := over.Kind
	if kind == "" {
		kind = types.DecisionKindDecision
	}
	scope := over.Scope
	if scope == nil {
		// Verbatim, as in §D9's migration: an exact path is a glob that matches
		// exactly itself.
		scope = n.FilePaths
	}

	in := DecisionInput{
		ProjectID:  k.projectID,
		Kind:       kind,
		Title:      title,
		Body:       n.Content,
		Scope:      scope,
		Confidence: n.Confidence,
		Source:     types.DecisionSourceUser,
		SourceRef:  promotedNotePrefix + n.ID,
		TopicKey:   n.TopicKey,
		Tags:       n.Tags,
		Via:        "cli",
		// Attached at promotion so promote→accept is one motion and needs no
		// --force. The linter may still flag it as weak evidence.
		Evidence: []EvidenceInput{{
			Kind:    types.EvidenceKindImport,
			Ref:     promotedNotePrefix + n.ID,
			AddedBy: types.ActorHuman,
		}},
	}
	// Proposed, never born active: promotion goes through the ordinary
	// lifecycle, which is the entire point.
	return k.decisions.Propose(in)
}
