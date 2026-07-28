# Concepts

---

## Memory types

| Type | Use for |
|------|---------|
| `decision` | Architecture choices, tooling selections, approach rationale |
| `convention` | Naming rules, code style, structural standards |
| `fact` | Durable truths about the codebase |
| `event` | Migrations, incidents, refactors, session summaries, prompts |

If you don't specify a type, memtrace defaults to `fact`.

---

## Confidence

Every memory has a confidence score between 0.0 and 1.0. New memories start at 1.0.

**Decay** — confidence decays exponentially with a 90-day half-life using the most recent signal (last updated or last accessed). The floor is 0.1 — memories never reach zero. Accessing a memory via recall resets the clock.

This means stale, never-accessed memories naturally sink in search results over time while frequently recalled ones stay prominent.

**Manual override** — you can set confidence explicitly:

```bash
memtrace update 01KMDX71NT --confidence 0.5
```

```
memory_update(id: "01KMDX71NT...", confidence: 0.5)
```

---

## Staleness detection

Memories that reference source files can go stale when those files change. Run `memtrace scan` to check:

```bash
memtrace scan
```

```
  stale  01ABCDEF1234  "Auth uses RS256 JWT..." — file modified: src/auth/middleware.go
  stale  01EFGH567890  "Schema migration v3..." — file deleted: db/migrations/003.sql

2 memories marked stale (11 unchanged).
```

A memory is marked stale when any of its `file_paths` has been deleted or modified more recently than the memory was last updated.

Review with:

```bash
memtrace list --status stale
```

Then edit, update, or delete them.

---

## Private content

Wrap any part of memory content in `<private>...</private>` to prevent it from being stored. The tags and their contents are stripped before the memory reaches the database.

```
memory_save(
  content: "Auth uses JWT RS256. <private>Signing key: sk-prod-abc123</private> Tokens expire after 1h.",
  type: "convention"
)
// stored as: "Auth uses JWT RS256.  Tokens expire after 1h."
```

Tags are case-insensitive (`<PRIVATE>`, `<Private>`) and support multiline blocks. This lets agents include full context in a save call without sensitive details ever being persisted.

---

## Topic keys

`topic_key` is a stable identifier that prevents duplicate memories from accumulating as a project evolves.

```
memory_save(
  content:   "We use Postgres 16 with pgvector",
  type:      "decision",
  topic_key: "decision/database"
)

// Later, when you upgrade:
memory_save(
  content:   "We use Postgres 17 with pgvector — upgraded March 2026",
  type:      "decision",
  topic_key: "decision/database"   // updates the existing memory instead of creating a new one
)
```

Good topic keys are hierarchical and stable: `decision/auth`, `convention/error-handling`, `fact/db-schema-version`.

---

## Session auto-summarization

When an MCP session ends, memtrace automatically saves a compact `event` memory recording what happened — but only if at least one memory was written. Read-only sessions produce no noise.

```
Session 2026-03-23T14:32Z (45m): saved 3 memories — "We use JWT with RS256" [decision],
"Error handling convention" [convention], "DB schema on v2" [fact]. Recalled 5 times.
```

Review session history:

```bash
memtrace list --type event
```

---

## Storage

All data lives in `.memtrace/memtrace.db` — SQLite with WAL mode, local-only, no account required. The `.memtrace/` directory is added to `.gitignore` automatically on init.

Memory IDs are [ULIDs](https://github.com/ulid/spec) — lexicographically sortable, collision-resistant, and time-ordered.

---

## Decisions and notes

Memories are stored in two classes, and they behave differently.

**Decisions** (`--type decision`, `--type convention`) are governed. They carry
a lifecycle — `proposed` → `active` → `violated` → `superseded` / `reverted` /
`rejected` — a scope of file globs, evidence, and provenance. Every change to
one is recorded in an append-only event log.

- A decision you save yourself is confirmed on the spot: you are the
  confirmation.
- A decision an agent saves over MCP arrives as **`proposed`** and does not
  bind anything until a human accepts it. This is deliberate: the model does
  not get to decide what is law.
- Content and scope are immutable once accepted. Changing what a decision says
  means superseding it with a new one, so the history keeps its meaning.
- Forgetting a decision that has history is not a delete — it becomes
  `rejected` (if it was still a proposal) or `reverted`. The record survives.

**Notes** (`--type note`) are everything else: facts, session summaries,
prompts. They are retrievable but ungoverned — no lifecycle, no evidence, no
attribution. `fact` and `event` are still accepted and both mean `note`; what
they used to distinguish now lives in tags (`session`, `prompt`).

### Scopes are file globs

A decision's file paths are **globs**, matched with `doublestar`:

```bash
memtrace save "Store code returns wrapped errors" --type convention --files 'internal/kernel/**'
```

`memory_context` and the file matcher expand them, so `internal/kernel/**`
matches `internal/kernel/store.go`. An exact path is simply a glob that matches
itself. Note file paths keep exact-match semantics — they were never patterns.
