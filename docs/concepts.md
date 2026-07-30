# Concepts

---

## Two classes of memory

Everything varve stores is either a **decision** or a **note**, and the two
behave differently on purpose.

| | Decisions & conventions | Notes |
|---|---|---|
| Governed by a lifecycle | yes | no |
| Binds agents | once accepted | never |
| Scope of file globs | yes | file paths, exact match |
| Evidence | yes | no |
| Attribution in `varve report` | yes | no |
| Deleted by `memory_forget` | no — records a disposal request | yes |

**Decisions** (`--type decision`) and **conventions** (`--type convention`) are
the governed class. They differ only in what they describe — an architecture
choice versus a standing rule — and are identical mechanically.

**Notes** (`--type note`) are everything else: durable facts, session summaries,
captured prompts. `fact` and `event` are still accepted and both mean `note`;
what they used to distinguish now lives in tags (`session`, `prompt`).

If you don't specify a type, varve saves a note. Making something binding is a
deliberate act, not a default.

→ [Decision lifecycle](decisions.md) for the states and the transitions.

---

## Scopes are file globs

A decision's file paths are **globs**, matched with `doublestar`:

```bash
varve save "Store code returns wrapped errors" \
  --type convention \
  --files 'internal/kernel/**'
```

`memory_pack`, `memory_context` and the commit observer all expand them, so
`internal/kernel/**` matches `internal/kernel/store.go`. An exact path is simply
a glob that matches itself.

Scope is what makes context selection work by relevance to your diff rather than
by keyword luck — and it is what lets the observer decide whether a commit
touched a decision at all. **A decision with no scope can be packed but can
never be matched to a commit**, so it will never appear in attribution. If a
rule really is repo-wide, give it a repo-wide glob rather than no glob.

Note file paths keep exact-match semantics — they were never patterns.

---

## Confidence

Every memory has a confidence score between 0.0 and 1.0. New memories start at 1.0.

**Decay** — confidence decays exponentially with a 90-day half-life using the
most recent signal (last updated or last accessed). The floor is 0.1 — memories
never reach zero. Accessing a memory via recall resets the clock. Recency is
scored separately, with a 30-day half-life.

Stale, never-accessed memories sink in search results over time while frequently
recalled ones stay prominent.

**Manual override:**

```bash
varve update 01KMDX71NT --confidence 0.5
```

```
memory_update(id: "01KMDX71NT...", confidence: 0.5)
```

---

## Staleness detection

Memories that reference source files can go stale when those files change:

```bash
varve scan --stale
```

```
  stale  01ABCDEF1234  "Auth uses RS256 JWT..." — file modified: src/auth/middleware.go
  stale  01EFGH567890  "Schema migration v3..." — file deleted: db/migrations/003.sql

2 memories marked stale (11 unchanged).
```

A memory is marked stale when any of its `file_paths` has been deleted or
modified more recently than the memory was last updated. Review with
`varve list --status stale`, then edit, update or delete them.

> **`--stale` is not optional here.** Bare `varve scan` is the commit observer —
> a different job entirely. See [Attribution](attribution.md).

---

## Private content

Wrap any part of memory content in `<private>...</private>` to prevent it from
being stored. The tags and their contents are stripped before the memory reaches
the database.

```
memory_save(
  content: "Auth uses JWT RS256. <private>Signing key: sk-prod-abc123</private> Tokens expire after 1h.",
  type: "convention"
)
// stored as: "Auth uses JWT RS256.  Tokens expire after 1h."
```

Tags are case-insensitive (`<PRIVATE>`, `<Private>`) and support multiline
blocks. This lets agents include full context in a save call without sensitive
details ever being persisted.

If a secret reaches the store anyway, `varve decision purge` is the one verb
that destroys content rather than keeping an audit record.

---

## Topic keys

`topic_key` is a stable identifier that prevents duplicates accumulating as a
project evolves. **It behaves differently by class**, and the difference matters:

```
// Notes: re-saving with the same key updates in place.
memory_save(content: "Schema is on v6", type: "note", topic_key: "note/schema")
memory_save(content: "Schema is on v7", type: "note", topic_key: "note/schema")
// → one note, updated

// Decisions: re-saving creates a NEW proposed successor.
memory_save(content: "We use Postgres 16", type: "decision", topic_key: "decision/database")
memory_save(content: "We use Postgres 17", type: "decision", topic_key: "decision/database")
// → a second, proposed decision that supersedes the first once a human accepts it
```

An accepted decision's content is immutable — changing what it says means
superseding it, so the history keeps its meaning. The two namespaces are
separate: a note and a decision may hold the same key.

Good topic keys are hierarchical and stable: `decision/auth`,
`convention/error-handling`, `note/db-schema-version`.

---

## Sessions

One MCP connection is one session, and the connection opening *is* its start —
a session that never calls a tool still has a window that attribution can join
against.

When a session ends, varve saves a compact note recording what happened, but
only if at least one memory was written. Read-only sessions produce no noise.

```
Session 2026-03-23T14:32Z (45m): saved 3 memories — "We use JWT with RS256" [decision],
"Error handling convention" [convention], "DB schema on v2" [note]. Recalled 5 times.
```

Review session history with `varve list --type note` (they carry the `session`
tag). A crashed session writes no end row and none is ever repaired: the end is
synthesized at query time, because a repair row would be a fabricated
observation.

---

## Storage

All data lives in `.varve/varve.db` — SQLite with WAL mode, local-only, no
account required. The `.varve/` directory is added to `.gitignore` automatically
on init. The observer writes its own failures to `.varve/observer.log`, never to
an agent's tool output.

Memory IDs are [ULIDs](https://github.com/ulid/spec) — lexicographically
sortable, collision-resistant, and time-ordered. Every command that takes an ID
also takes a unique prefix.

v2 databases upgrade themselves through a versioned migration framework when
they are opened. Only the v1 conversion is manual — see
[`varve migrate`](cli.md#varve-migrate).
