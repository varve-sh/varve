package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/observer"
	"github.com/varve-sh/varve/internal/pack"
	"github.com/varve-sh/varve/internal/types"
	"github.com/varve-sh/varve/internal/util"
)

// workingDir is where the server was started, which is the repository the
// agent is working in.
func workingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// mcpAgentName is the provenance stamped on writes arriving over MCP.
const mcpAgentName = "mcp"

// Serve starts the MCP server over stdio. Blocks until the connection closes,
// then auto-saves a session summary if any memories were saved.
func Serve(k *kernel.MemoryKernel) error {
	s := server.NewMCPServer(
		"varve",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	// One MCP connection is one session, and the connection opening *is* its
	// start (ADR-0004 §D3): session.started is written here, not lazily, so a
	// connection that never calls a tool still has a window ADR-0002 §P11 can
	// join against.
	tracker := newSessionTracker(k.BeginSession(mcpAgentName, ""))
	registerTools(s, k, tracker)

	// ADR-0004 §D1.2: the catch-up scan runs as a background goroutine at
	// session start, never on a timer and never blocking. Tools serve
	// immediately; the scan closes whatever gap the hook left — commits pulled
	// from a teammate, made before install, or lost to a busy database.
	//
	// Errors are swallowed by design (§D7): the observer's own failures belong
	// in .varve/observer.log and in `varve report`'s completeness line,
	// never in an agent's tool output.
	go func() {
		if root := util.FindProjectRoot(workingDir()); root != "" {
			_, _ = observer.Scan(k, observer.ScanOptions{RepoRoot: root})
		}
	}()

	err := server.ServeStdio(s)

	// Auto-save session summary (best-effort, never blocks the shutdown).
	if text := tracker.summary(); text != "" {
		_, _, _ = k.Save(types.MemorySaveInput{
			Content:   text,
			Type:      types.MemoryTypeNote,
			Source:    types.MemorySourceAgent,
			Tags:      []string{"session"},
			SessionID: tracker.sessionID(),
			Agent:     mcpAgentName,
		})
	}

	// session.ended closes the window. A crash writes no end row and none is
	// ever repaired: §D3 synthesizes the end at query time as MAX(ts), because
	// a repair row would be a fabricated observation.
	k.EndSession()

	return err
}

func registerTools(s *server.MCPServer, k *kernel.MemoryKernel, tracker *sessionTracker) {
	registerPackTool(s, k, tracker)

	// Tool 1: memory_save
	s.AddTool(
		mcp.NewTool("memory_save",
			mcp.WithDescription("Save a memory (decision, convention or note) to the local memory store. Decisions and conventions are governed: one you save is recorded as proposed and does not bind until a human accepts it with `varve decision accept`; it is never served as binding context, and when it comes back from memory_recall or memory_context it is marked PROPOSED — treat anything so marked as a pending proposal rather than as law, and tell the user it is waiting rather than assuming it took effect. `fact` and `event` are synonyms for `note`: retrievable, ungoverned. Use this when you learn something important about the project that should persist across sessions. topic_key behaviour differs by type: for a note, re-saving with the same key updates it in place; for a decision or convention, it creates a *new* proposed decision that supersedes the current holder once accepted, and returns a new id."),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("The memory content to save. Be specific and self-contained. Wrap sensitive details in <private>...</private> to prevent them from being stored."),
			),
			mcp.WithString("type",
				mcp.Description("Memory type: decision (governed, needs human confirmation before it binds), convention, or note. Default: note."),
			),
			mcp.WithArray("tags",
				mcp.Description(`Tags for categorization, e.g. ["auth", "database"]`),
			),
			mcp.WithArray("file_paths",
				mcp.Description(`Related file paths relative to project root, e.g. ["src/auth/middleware.go"]`),
			),
			mcp.WithString("topic_key",
				mcp.Description(`Stable identifier for this memory, e.g. "convention/error-handling" or "decision/database". For a note, re-saving with the same key updates it in place. For a decision or convention, it creates a new proposed successor that supersedes the current holder when a human accepts it — the save returns a new id and does not mutate the earlier one.`),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			content, _ := args["content"].(string)
			memType, _ := args["type"].(string)
			tags := extractStringSlice(args, "tags")
			filePaths := extractStringSlice(args, "file_paths")
			topicKey, _ := args["topic_key"].(string)

			mem, upserted, err := k.Save(types.MemorySaveInput{
				Content:   content,
				Type:      types.MemoryType(memType),
				Source:    types.MemorySourceAgent,
				Tags:      tags,
				FilePaths: filePaths,
				TopicKey:  topicKey,
				SessionID: tracker.sessionID(),
				Agent:     mcpAgentName,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			tracker.recordSave(mem.ID, mem.Summary, mem.Type)
			verb := "Saved"
			if upserted {
				verb = "Updated"
			}
			text := fmt.Sprintf("%s memory %s (%s): %s", verb, mem.ID, mem.Type, mem.Summary)
			return mcp.NewToolResultText(text), nil
		},
	)

	// Tool 1b: memory_prompt
	s.AddTool(
		mcp.NewTool("memory_prompt",
			mcp.WithDescription("Capture the user's original request or goal for this session. Call this at the very start of a task, before any other memory operations, so future sessions can understand what was attempted and why."),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("The user's original request or goal, verbatim or closely paraphrased."),
			),
			mcp.WithArray("file_paths",
				mcp.Description(`Files the request relates to, e.g. ["src/auth/middleware.go"]`),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			content, _ := args["content"].(string)
			filePaths := extractStringSlice(args, "file_paths")

			mem, _, err := k.Save(types.MemorySaveInput{
				Content:   content,
				Type:      types.MemoryTypeNote,
				Source:    types.MemorySourceAgent,
				Tags:      []string{"prompt"},
				FilePaths: filePaths,
				SessionID: tracker.sessionID(),
				Agent:     mcpAgentName,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			tracker.recordSave(mem.ID, mem.Summary, mem.Type)
			return mcp.NewToolResultText(fmt.Sprintf("Captured prompt %s: %s", mem.ID, mem.Summary)), nil
		},
	)

	// Tool 2: memory_recall
	s.AddTool(
		mcp.NewTool("memory_recall",
			mcp.WithDescription("Search the memory store for relevant past memories. Returns summaries — call memory_get(id) to read the full content of any result. Use this at the start of tasks and when you need project context, conventions, or past decisions."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description(`Natural language search query, e.g. "authentication approach" or "database conventions"`),
			),
			mcp.WithNumber("limit",
				mcp.Description("Max results to return. Default: 10, max: 50"),
			),
			mcp.WithString("type",
				mcp.Description("Filter by memory type: decision, convention, note"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			query, _ := args["query"].(string)
			limit := 10
			if l, ok := args["limit"].(float64); ok {
				limit = int(l)
			}
			memType, _ := args["type"].(string)

			results, err := k.Recall(types.MemoryRecallInput{
				Query:     query,
				Limit:     limit,
				Type:      types.MemoryType(memType),
				SessionID: tracker.sessionID(),
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			tracker.recordRecall()
			if len(results) == 0 {
				return mcp.NewToolResultText("No relevant memories found."), nil
			}

			var buf strings.Builder
			fmt.Fprintf(&buf, "Found %d memories:\n\n", len(results))
			for i, r := range results {
				m := r.Memory
				fmt.Fprintf(&buf, "[%s] %s%s · %s · confidence: %.1f\n%s",
					m.ID, m.Type, statusLabel(m), formatAge(m.CreatedAt), m.Confidence, m.Summary)
				if len(m.Tags) > 0 {
					fmt.Fprintf(&buf, "\ntags: %s", strings.Join(m.Tags, ", "))
				}
				if len(m.FilePaths) > 0 {
					fmt.Fprintf(&buf, "\nfiles: %s", strings.Join(m.FilePaths, ", "))
				}
				if i < len(results)-1 {
					buf.WriteString("\n\n")
				}
			}
			buf.WriteString("\n\nCall memory_get with an ID to read the full content.")
			return mcp.NewToolResultText(buf.String()), nil
		},
	)

	// Tool 2b: memory_get
	s.AddTool(
		mcp.NewTool("memory_get",
			mcp.WithDescription("Retrieve the full content of a memory by ID. Use this after memory_recall or memory_context to read the complete text of a specific memory."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("The memory ID to retrieve"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, _ := args["id"].(string)
			if id == "" {
				return mcp.NewToolResultError("id is required"), nil
			}

			mem, err := k.Get(id)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if mem == nil {
				return mcp.NewToolResultText(fmt.Sprintf("Memory %s not found.", id)), nil
			}

			var buf strings.Builder
			fmt.Fprintf(&buf, "[%s] %s%s · %s · confidence: %.1f\n",
				mem.ID, mem.Type, statusLabel(*mem), formatAge(mem.CreatedAt), mem.Confidence)
			if len(mem.Tags) > 0 {
				fmt.Fprintf(&buf, "tags: %s\n", strings.Join(mem.Tags, ", "))
			}
			if len(mem.FilePaths) > 0 {
				fmt.Fprintf(&buf, "files: %s\n", strings.Join(mem.FilePaths, ", "))
			}
			buf.WriteString("\n")
			buf.WriteString(mem.Content)
			return mcp.NewToolResultText(buf.String()), nil
		},
	)

	// Tool 3: memory_forget
	s.AddTool(
		mcp.NewTool("memory_forget",
			mcp.WithDescription("Forget a memory: delete a note by ID, or act on the top memory matching a query. Notes are deleted outright. A decision or convention is NOT deleted, rejected or reverted by this call — disposal of a governed memory is a human action, so the call records a disposal request and returns it as pending; tell the user it awaits their confirmation with `varve decision reject <id>` or `varve decision revert <id>`."),
			mcp.WithString("id",
				mcp.Description("Specific memory ID to delete"),
			),
			mcp.WithString("query",
				mcp.Description("Search query — archives the top match"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, _ := args["id"].(string)
			query, _ := args["query"].(string)

			if id != "" {
				// The MCP channel disposes of nothing governed: an agent's forget
				// of a decision records a request and transitions nothing
				// (ADR-0001 Amendment 3, A3.1). The actor is the agent, which is
				// F28's other half.
				outcome, err := k.Forget(id, types.ActorAgent)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(forgetResult(outcome, id, "")), nil
			}

			if query != "" {
				results, err := k.Recall(types.MemoryRecallInput{Query: query, Limit: 1})
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				if len(results) == 0 {
					return mcp.NewToolResultText("No matching memory found."), nil
				}
				m := results[0].Memory
				outcome, err := k.Forget(m.ID, types.ActorAgent)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(
					forgetResult(outcome, m.ID, truncateStr(m.Content, 100)),
				), nil
			}

			return mcp.NewToolResultText("Provide either id or query."), nil
		},
	)

	// Tool 4: memory_update
	s.AddTool(
		mcp.NewTool("memory_update",
			mcp.WithDescription("Update an existing memory by ID. Only provided fields are changed."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("The memory ID to update"),
			),
			mcp.WithString("content",
				mcp.Description("New content"),
			),
			mcp.WithString("type",
				mcp.Description("New type. Notes cannot be promoted into decisions — save a decision instead."),
			),
			mcp.WithArray("tags",
				mcp.Description(`New tags (replaces existing), e.g. ["auth", "api"]`),
			),
			mcp.WithArray("file_paths",
				mcp.Description(`New file paths (replaces existing), e.g. ["src/auth.go"]`),
			),
			mcp.WithNumber("confidence",
				mcp.Description("New confidence score 0.0–1.0"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, _ := args["id"].(string)
			if id == "" {
				return mcp.NewToolResultError("id is required"), nil
			}

			input := types.MemoryUpdateInput{}

			if v, ok := args["content"].(string); ok && v != "" {
				input.Content = &v
			}
			if v, ok := args["type"].(string); ok && v != "" {
				t := types.MemoryType(v)
				input.Type = &t
			}
			if _, ok := args["tags"]; ok {
				tags := extractStringSlice(args, "tags")
				input.Tags = &tags
			}
			if _, ok := args["file_paths"]; ok {
				fps := extractStringSlice(args, "file_paths")
				input.FilePaths = &fps
			}
			if v, ok := args["confidence"].(float64); ok {
				input.Confidence = &v
			}

			mem, err := k.Update(id, input)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if mem == nil {
				return mcp.NewToolResultText(fmt.Sprintf("Memory %s not found", id)), nil
			}
			return mcp.NewToolResultText(
				fmt.Sprintf("Updated memory %s (%s): %s", mem.ID, mem.Type, mem.Summary),
			), nil
		},
	)

	// Tool 5: memory_context
	s.AddTool(
		mcp.NewTool("memory_context",
			mcp.WithDescription("Get all memories relevant to a set of files you are about to read or edit. Call this at the start of any task that touches specific files to surface conventions, decisions, and notes linked to those files. Everything returned here is binding or ungoverned context: proposed decisions are never included as content — they are reported only as a trailing count with their ids, and a human accepts them before they apply. Use memory_recall or memory_get to read a proposal."),
			mcp.WithArray("file_paths",
				mcp.Required(),
				mcp.Description(`Files you are about to work with, relative to project root, e.g. ["src/auth/middleware.go", "src/auth/handler.go"]`),
			),
			mcp.WithNumber("limit",
				mcp.Description("Max results to return. Default: 10, max: 50"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			filePaths := extractStringSlice(args, "file_paths")
			if len(filePaths) == 0 {
				return mcp.NewToolResultText("No file paths provided."), nil
			}
			limit := 10
			if l, ok := args["limit"].(float64); ok {
				limit = int(l)
			}

			contextRes, err := k.ContextForFiles(filePaths, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(contextRes.Items) == 0 && len(contextRes.ProposedIDs) == 0 {
				return mcp.NewToolResultText("No relevant memories found for these files."), nil
			}

			var buf strings.Builder
			fmt.Fprintf(&buf, "Context for %d file(s):\n", len(filePaths))
			for _, p := range filePaths {
				fmt.Fprintf(&buf, "  %s\n", p)
			}
			buf.WriteString("\n")

			// memory_context *volunteers* context at task start, so it gives
			// §P2's structural guarantee rather than the advisory marker
			// memory_recall and memory_get give (ADR-0002 Amendment 2): a
			// proposed decision — unconfirmed text, often the agent's own — never
			// appears as content here, only as a footer count. An inline caveat
			// inside content the agent was just handed is the precise laundering
			// §P2's rationale names.
			//
			// The partition happens in the kernel, before the limit: filtering
			// here let proposals take binding decisions' slots and then vanish
			// (F32), and made the footer count report what survived a retrieval
			// limit rather than what matched.
			results := contextRes.Items

			for i, r := range results {
				m := r.Memory
				label := "related"
				if r.Score >= 1.0 {
					label = "file match"
				}
				fmt.Fprintf(&buf, "[%s] %s · %s%s · %s · confidence: %.1f\n%s",
					m.ID, label, m.Type, statusLabel(m), formatAge(m.CreatedAt), m.Confidence, m.Summary)
				if len(m.Tags) > 0 {
					fmt.Fprintf(&buf, "\ntags: %s", strings.Join(m.Tags, ", "))
				}
				if len(m.FilePaths) > 0 {
					fmt.Fprintf(&buf, "\nfiles: %s", strings.Join(m.FilePaths, ", "))
				}
				if i < len(results)-1 {
					buf.WriteString("\n\n")
				}
			}
			// The count is always the true number of scope-matching proposals;
			// only the id list is capped (§P8: ids may be dropped, counts never).
			if footer := pack.ProposedFooter(contextRes.ProposedIDs, contextFooterIDs); footer != "" {
				if len(results) > 0 {
					buf.WriteString("\n")
				}
				buf.WriteString("\n" + footer)
			}
			// No pack events are emitted here: ADR-0002 OQ5's instrumentation
			// gap is accepted and adopting §P2's footer must not quietly grow
			// §P10 emission.
			buf.WriteString("\n\nCall memory_get with an ID to read the full content.")
			return mcp.NewToolResultText(buf.String()), nil
		},
	)
}

// statusLabel renders a decision's lifecycle state for the agent-facing tools.
//
// §D10: "Decisions surface with their status." Without it the two read paths
// that exist today render a `proposed` decision — an agent's own unreviewed
// write, quarantined by design — identically to an accepted, binding one, and
// the agent applies it as law. That is ADR-0002 §P2's "laundering the agent's
// own unreviewed writes back into its prompt", on `memory_recall` and
// `memory_context` rather than on the packer.
//
// It also keeps the instructions honest: the templates shipped by A2.4 tell
// the agent a proposal does not bind until a human accepts it, and ADR-0001
// falsifier 1's 30-day clock runs on that copy. An active decision and a note
// carry no marker — the unremarkable case stays unremarked.
func statusLabel(m types.Memory) string {
	switch m.Status {
	case types.MemoryStatus(types.StatusProposed):
		return " · PROPOSED (not accepted by a human; does not bind)"
	case types.MemoryStatus(types.StatusViolated):
		return " · VIOLATED (still binding)"
	case types.MemoryStatusStale:
		return " · stale"
	case types.MemoryStatusActive:
		return ""
	default:
		return " · " + string(m.Status)
	}
}

// registerPackTool adds `memory_pack` (ADR-0002 §P1). It is the session
// bootstrap tool: "I am about to touch these files; give me everything binding
// on them, once, deduplicated, inside this budget." memory_recall stays exactly
// as it was — the two coexist by Phase 0 ruling 3, and merging them would
// destroy the only cheap comparison between them (§P11).
func registerPackTool(s *server.MCPServer, k *kernel.MemoryKernel, tracker *sessionTracker) {
	s.AddTool(
		mcp.NewTool("memory_pack",
			mcp.WithDescription("Get everything binding on the files you are about to work on, packed into a token budget. Call this ONCE at the start of a task, before reading the files — it is the session bootstrap, where memory_recall is for exploring a question mid-task. Returns binding decisions and conventions in rank order (violated ones flagged), then ungoverned notes if budget remains, then a footer naming everything left out and how to fetch it. Proposed decisions are never included as content; they are counted in the footer, because a proposal is not law until a human accepts it."),
			mcp.WithArray("file_paths",
				mcp.Description(`Repo-relative paths you are about to read or edit, e.g. ["internal/auth/middleware.go"]. Absolute paths and ".." are rejected. Either this or task is required.`),
			),
			mcp.WithString("task",
				mcp.Description("What you are about to do, in a sentence. Used for text relevance. Either this or file_paths is required."),
			),
			mcp.WithNumber("budget_tokens",
				mcp.Description("Hard ceiling on the estimated size of the returned text (default 2000, min 500, max 100000). The estimate is deliberately conservative, so the real token count is at or below this."),
			),
			mcp.WithBoolean("include_notes",
				mcp.Description("Include ungoverned notes after the decisions (default true). Notes never displace a decision."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			task, _ := args["task"].(string)
			in := kernelPackRequest(args, task)

			res, err := k.Pack(in)
			if err != nil {
				// Errors are returned as MCP tool errors, never as partial
				// packs, and an errored call emits no pack.served (§P1).
				return mcp.NewToolResultError(err.Error()), nil
			}
			tracker.recordPack()
			return mcp.NewToolResultText(res.Text), nil
		},
	)
}

func kernelPackRequest(args map[string]interface{}, task string) pack.Request {
	in := pack.Request{
		FilePaths: extractStringSlice(args, "file_paths"),
		Task:      task,
	}
	if v, ok := args["budget_tokens"]; ok {
		switch n := v.(type) {
		case float64:
			in.BudgetTokens = int(n)
		case int:
			in.BudgetTokens = n
		}
	}
	if v, ok := args["include_notes"].(bool); ok {
		in.ExcludeNotes = !v
	}
	return in
}

// contextFooterIDs caps how many proposal ids memory_context names. There is
// no budget on this path to derive a reserve from, so the cap is fixed: enough
// to act on, bounded so a large backlog cannot crowd out the content the tool
// exists to deliver.
const contextFooterIDs = 10

// forgetResult says what actually happened, in the words the agent should
// repeat to the user.
//
// The disposal-request case is a success with an explanation, not an error:
// the user's in-chat "forget that" did reach the store, and a refusal agents
// hit constantly is a refusal they learn to ignore (ADR-0001 Amendment 3,
// rejected alternative (i)). What it must not do is claim a deletion that did
// not happen.
func forgetResult(outcome kernel.DisposalOutcome, id, preview string) string {
	suffix := ""
	if preview != "" {
		suffix = ": " + preview
	}
	switch outcome {
	case kernel.DisposalDeleted:
		return fmt.Sprintf("Deleted memory %s%s", id, suffix)
	case kernel.DisposalRequested:
		return fmt.Sprintf(
			"Disposal request recorded for decision %s%s. Nothing was deleted, rejected or "+
				"reverted: disposing of a governed memory is a human action. Tell the user the "+
				"request is pending their confirmation — `varve decision reject %s` while it "+
				"is proposed, or `varve decision revert %s` once it is binding.",
			id, suffix, id, id)
	default:
		return fmt.Sprintf("Memory %s not found, or already disposed of", id)
	}
}

func extractStringSlice(args map[string]interface{}, key string) []string {
	raw, ok := args[key].([]interface{})
	if !ok {
		return []string{}
	}
	result := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	}
}

func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
