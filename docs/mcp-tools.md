# MCP Tools Reference

Varve exposes eight MCP tools. Your agent calls them directly — no configuration needed beyond `varve setup`.

---

## `memory_pack`

**The first call of a task.** "I am about to touch these files — give me
everything binding on them, once, deduplicated, inside a budget I can afford."

```
memory_pack(
  file_paths:    ["internal/auth/middleware.go", "internal/auth/session.go"],
  task:          "add refresh-token rotation",   // optional if file_paths given
  budget_tokens: 2000,                           // optional: default 2000, min 500, max 100000
  include_notes: true                            // optional: default true
)
```

**Parameters**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `file_paths` | one of the two | Repo-relative paths. Absolute paths and `..` are rejected. Matched against decision scope globs and note file paths. |
| `task` | one of the two | What you are about to do. Feeds text relevance. |
| `budget_tokens` | no | Hard ceiling on the estimated size of the returned text. The estimate is deliberately conservative, so the true count is at or below it. |
| `include_notes` | no | Notes are appended only from budget left over after every decision has been considered; they can never displace one. |

**What comes back**

```
VARVE PACK v1
files: internal/auth/middleware.go, internal/auth/session.go
task: add refresh-token rotation
budget: 2000 est-tokens (bytes/3 v1) · used: 1712 · items: 4 (3 decisions, 1 note) · omitted: 2

[1] DECISION 01J9W… · active · conf 0.92 · scope: internal/auth/**
Refresh tokens rotate on every use; reuse detection revokes the family.
…
evidence: commit 4f2a91c, pr #87

[2] DECISION 01J8Q… · VIOLATED (2 unresolved) · conf 0.88 · scope: internal/auth/session.go
…

[3] DECISION 01J7X… · active (convention, repo-wide) · conf 0.75
Errors are wrapped with %w and never logged at the call site.
[body elided — 640 est. tokens; memory_get 01J7X…]

-- omitted (over budget): 2 — DECISION 01J5R… (rank 5, 812 est. tokens), NOTE 01J4M… (rank 6, 96 est. tokens)
-- proposed decisions touching these files: 2 (01J2H…, 01J1G…) — not binding until accepted; review with `varve decision accept <id>`
-- raise budget_tokens or memory_get an ID above for anything elided
```

Rules worth knowing:

- **Nothing is dropped silently.** Anything eligible is either in the body or
  named in the footer with the rank it would have had and what it would cost.
- **`VIOLATED (n unresolved)`** means the rule still binds and the codebase
  currently contradicts it in `n` unresolved places. It is not a repeal.
- **Proposed decisions are never in the body**, only in the footer count — a
  proposal is not law until a human accepts it.
- **Errors** are `E1_BAD_BUDGET`, `E2_BAD_PATH`, `E3_NO_ANCHOR`, `E4_STORE`,
  and the code is the first token of the message. An empty pack is not an
  error: it is a valid pack with zero items.

**Pack or recall?** Pack answers "what binds these files" — call it once, at
the start, before reading the files. Recall answers "what do we know about X" —
call it when a question comes up. They are different questions and both exist
on purpose.

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
`proposed`: it is captured, but it does not bind until a human accepts it
(`varve decision accept <id>`), and it is never volunteered as context.

Which tool you call decides how you see it:

- `memory_recall` and `memory_get` **answer what you asked** and return
  proposals, marked `PROPOSED (not accepted by a human; does not bind)`. This
  is the review surface — treat anything so marked as a pending proposal
  rather than as law.
- `memory_context` **volunteers** context at task start, so it never returns a
  proposal as content. Proposals appear only as a trailing count with their
  ids, which you can look up with `memory_get` if you need them.

Surface pending proposals to the user instead of assuming the save took effect.
Acceptance, rejection and disposal are CLI/TUI actions — there is no MCP tool
for them, by design.

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
