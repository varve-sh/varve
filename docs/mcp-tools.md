# MCP Tools Reference

Varve exposes eight MCP tools. Your agent calls them directly — no configuration
needed beyond `varve setup`.

Three of them read context (`memory_pack`, `memory_context`, `memory_recall`),
three write it (`memory_save`, `memory_update`, `memory_prompt`), one fetches by
id (`memory_get`), and one disposes (`memory_forget`).

**Governance is not on this surface.** Accepting, rejecting, reverting,
promoting and purging decisions are CLI actions. An agent can propose and it can
request disposal; it cannot make something binding or make it go away.

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

## `memory_context`

Everything relevant to a set of files, volunteered at task start. Combines
direct file-path and scope-glob matching with semantic recall.

```
memory_context(
  file_paths: ["src/auth/middleware.go", "src/auth/handler.go"],
  limit:      10        // optional, default 10, max 50
)
```

Returns file-matched memories first (labeled `[file match]`), then semantically
related ones (`[related]`). Each result is a summary — use `memory_get` for full
content.

**Proposals are never returned as content here.** Because this tool volunteers
context rather than answering a question, a proposal would arrive looking like
law. They appear only as a trailing count with their ids, which you can look up
with `memory_get`.

**Pack or context?** `memory_pack` is budget-governed and rank-ordered and tells
you what it left out — prefer it as the session bootstrap. `memory_context` is
the simpler "what's linked to these files" lookup.

---

## `memory_recall`

Search memories by natural language. Returns summaries — use `memory_get` to
read the full content of any result.

```
memory_recall(
  query: "authentication approach",
  limit: 10,            // optional, default 10, max 50
  type:  "decision"     // optional filter: decision, convention, note
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

Recall **does** return proposals, marked `PROPOSED (not accepted by a human;
does not bind)`. Together with `memory_get` this is the review surface — treat
anything so marked as a pending proposal rather than as law, and say so to the
user.

Scoring is BM25 full-text by default; with an embedder configured, semantic
similarity merges in. See [Semantic Search](embeddings.md).

---

## `memory_save`

Save something worth remembering across sessions.

```
memory_save(
  content:    "We use JWT RS256 — stateless API, no session storage",
  type:       "decision",             // decision | convention | note
  tags:       ["auth", "security"],
  file_paths: ["src/middleware/auth.go"],
  topic_key:  "decision/auth"         // optional — see below
)
```

**Decisions and conventions are governed.** One saved over MCP lands
`proposed`: it is captured, but it does not bind until a human accepts it
(`varve decision accept <id>`), and it is never volunteered as context.

Surface pending proposals to the user instead of assuming the save took effect.

**Parameters**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `content` | yes | The memory text. Wrap sensitive parts in `<private>...</private>` — they are stripped before storage. |
| `type` | no | `decision`, `convention` or `note`. `fact` and `event` are accepted as synonyms for `note`. Default: `note`. |
| `tags` | no | Array of strings for categorization. |
| `file_paths` | no | For a decision these are **scope globs** (`internal/auth/**`); for a note they are exact paths. A decision with no scope can never be matched to a commit. |
| `topic_key` | no | Stable identifier. **Notes:** re-saving with the same key updates in place. **Decisions:** re-saving creates a *new* `proposed` successor that supersedes the current holder once accepted — the call returns a new id and the earlier decision is not mutated. The two namespaces are separate. |

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

## `memory_get`

Retrieve the full content of a memory by ID. Use after `memory_recall`,
`memory_context` or a `memory_pack` footer.

```
memory_get(id: "01KMDX71NT...")
```

Returns proposals, marked `PROPOSED`. This is how you read anything a pack
elided or omitted.

---

## `memory_update`

Patch an existing **note**. Only provided fields change.

```
memory_update(
  id:         "01KMDX71NT...",
  content:    "Updated text",
  tags:       ["auth", "api"],
  file_paths: ["src/auth/middleware.go"],
  confidence: 0.8
)
```

| Parameter | Required | Description |
|-----------|----------|-------------|
| `id` | yes | Full ID. |
| `content` | no | Replaces the body. Re-embeds asynchronously if an embedder is configured. |
| `type` | no | Cannot cross the class boundary — see below. |
| `tags` | no | Replaces the tag list. |
| `file_paths` | no | Replaces the path list. |
| `confidence` | no | 0.0–1.0. |

**Two things this tool will not do**, and both return an error rather than
doing something surprising:

- **It cannot update a decision.** An accepted decision's content is immutable
  and its status changes are lifecycle transitions. Supersede it by saving a new
  decision under the same `topic_key`.
- **It cannot turn a note into a decision.** `varve decision promote <id>` does
  that, so the decision is born with provenance and a quarantine rather than by
  rewriting a column. It is a human action.

---

## `memory_forget`

```
memory_forget(id: "01KMDX71NT...")        // by ID
memory_forget(query: "old jwt approach")  // acts on the top match
```

What actually happens depends on the class:

- **A note is deleted outright.**
- **A decision or convention is not deleted, rejected or reverted.** The call
  records a `decision.disposal_requested` event, transitions nothing, and
  returns the request as pending. "The user wanted this thrown away" is exactly
  as untrustworthy as "the user approved."

Tell the user the request is waiting for them. They confirm it with
`varve decision reject <id>` while the decision is proposed, or
`varve decision revert <id>` once it is binding — or they ignore it, and nothing
happens.

---

## `memory_prompt`

Capture the user's original request at the very start of a session, before any
other memory operation, so future sessions can understand what was attempted and
why.

```
memory_prompt(
  content:    "Refactor auth middleware to support multi-tenant JWT validation",
  file_paths: ["src/auth/middleware.go"]   // optional
)
```

Stored as a note tagged `prompt`.

---

## What the agent is told

`varve setup` writes an instruction block into your agent's rules file
(`CLAUDE.md`, `.cursor/rules/varve.mdc`, `GEMINI.md`, …) describing these tools
*and* what actually happens when they are called — that a saved decision is
proposed and waiting, that forgetting one files a request, that a pack footer is
where elided content is named.

Without it, agents reliably report proposals as adopted. See
[Agent Setup](setup.md).
