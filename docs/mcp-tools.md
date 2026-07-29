# MCP Tools Reference

Memtrace exposes seven MCP tools. Your agent calls them directly — no configuration needed beyond `memtrace setup`.

---

## `memory_save`

Save something worth remembering across sessions.

```
memory_save(
  content:    "We use JWT RS256 — stateless API, no session storage",
  type:       "decision",             // decision | convention | note
  tags:       ["auth", "security"],
  file_paths: ["src/middleware/auth.go"],
  topic_key:  "decision/auth"         // optional — see the topic_key note below
)
```

**Decisions and conventions are governed.** One saved over MCP lands
`proposed`: it is captured, but it does not bind and is never packed into
context until a human accepts it (`memtrace decision accept <id>`). Surface
pending proposals to the user rather than assuming the save took effect.
Acceptance and rejection are CLI/TUI actions — there is no MCP tool for them,
by design.

**Parameters**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `content` | yes | The memory text. Wrap sensitive parts in `<private>...</private>` — they are stripped before storage. |
| `type` | no | `decision`, `convention` or `note`. `fact` and `event` are accepted as synonyms for `note`. Default: `note`. |
| `tags` | no | Array of strings for categorization. |
| `file_paths` | no | Paths relative to project root. Used by `memory_context` to surface this memory when editing related files. |
| `topic_key` | no | Stable identifier (e.g. `"convention/error-handling"`). **Notes:** re-saving with the same key updates the note in place. **Decisions and conventions:** re-saving creates a *new* `proposed` decision that supersedes the current holder once a human accepts it — the call returns a new id and the earlier decision is not mutated. The two namespaces are separate: a note and a decision may hold the same key. |

**Private content**

```
memory_save(
  content: "Auth uses JWT RS256. <private>Signing key: sk-prod-abc123</private> Tokens expire after 1h.",
  type: "convention"
)
// stored as: "Auth uses JWT RS256.  Tokens expire after 1h."
```

Tags are case-insensitive and support multiline blocks.

---

## `memory_recall`

Search memories by natural language. Returns summaries — use `memory_get` to read the full content of any result.

```
memory_recall(
  query: "authentication approach",
  limit: 10,            // optional, default 10, max 50
  type:  "decision"     // optional filter
)
```

**Example output**

```
Found 3 memories:

[01KMDX71NT...] decision · 3d ago · confidence: 0.9
We use JWT RS256 — stateless API, no session storage
tags: auth, security

[01KMDX72AB...] convention · 1h ago · confidence: 1.0
Error handling: always wrap with fmt.Errorf("...: %w", err)
tags: go, errors

Call memory_get with an ID to read the full content.
```

---

## `memory_get`

Retrieve the full content of a memory by ID. Use this after `memory_recall` or `memory_context`.

```
memory_get(id: "01KMDX71NT...")
```

---

## `memory_forget`

Delete a memory by ID or by searching for it.

```
memory_forget(id: "01KMDX71NT...")        // delete by ID
memory_forget(query: "old jwt approach")  // delete top match
```

---

## `memory_update`

Update an existing memory by ID. Only provided fields are changed — everything else is preserved.

```
memory_update(
  id:         "01KMDX71NT...",
  content:    "Updated decision text",
  type:       "decision",
  tags:       ["auth", "api"],
  file_paths: ["src/auth/middleware.go"],
  confidence: 0.8
)
```

---

## `memory_context`

Get all memories relevant to the files you are about to read or edit. Combines direct file-path matching with semantic recall — call this at the start of any task.

```
memory_context(
  file_paths: ["src/auth/middleware.go", "src/auth/handler.go"],
  limit:      10
)
```

Returns file-matched memories first (labeled `[file match]`), followed by semantically related memories (`[related]`). Each result shows a summary — use `memory_get` for full content.

---

## `memory_prompt`

Capture the user's original request at the very start of a session, before any other memory operations. Stored as an `event` tagged `prompt` so future sessions can understand what was attempted and why.

```
memory_prompt(
  content:    "Refactor auth middleware to support multi-tenant JWT validation",
  file_paths: ["src/auth/middleware.go"]   // optional
)
```

---

## Memory types

| Type | Use for |
|------|---------|
| `decision` | Architecture choices, tooling selections, approach rationale |
| `convention` | Naming rules, code style, structural standards |
| `fact` | Durable truths about the codebase |
| `event` | Migrations, incidents, refactors, session summaries |
