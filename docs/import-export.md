# Import & Export

Two different jobs share the word "import":

- **`varve import <file>`** loads a varve export back in — JSON or Markdown.
- **`varve import <source>`** pulls memory out of another tool: a rules file,
  claude-mem, engram.

Both end with a [corpus-health report](lint.md), so a fresh import tells you what
it just gave you.

---

## Importing from another tool

```bash
varve import              # list the sources found here; import nothing
varve import rules        # CLAUDE.md, AGENTS.md, .cursorrules, .cursor/rules/*.mdc
varve import claude-mem   # claude-mem's SQLite store
varve import engram       # engram's store
varve import undo         # undo the most recent batch
varve import undo <batch-id>
```

Flags on every source: `--dry-run` (preview, save nothing), `--yes` (skip the
confirmation, required for non-interactive use), `--as-notes` (demote every
decision candidate to a note), `--format md|json` (report output),
`--db <path>` (for the store-backed sources).

### What each source produces, and why

| Source | Lands as | Why |
|---|---|---|
| `rules` | **proposed conventions** | A rules file's contract is normative — it contains nothing but instructions to agents. Every block is a candidate rule. |
| `claude-mem` | **notes** | Narrative session observations with no scope, no evidence and no normative flag. Session archaeology is not a rule, and nothing here is promoted by guesswork. |
| `engram` | **notes**, plus proposed decisions where engram itself typed them | The source made the distinction; varve carries it across rather than inventing one. |

`--as-notes` demotes the lot if you disagree with any of that.

### Guarantees

- **Nothing lands active.** Decision candidates land `proposed` — review them
  with `varve decision pending` and accept the ones you want.
- **Re-running is free.** An import against unchanged sources creates zero rows;
  already-imported entries are counted as skipped, never silently re-added.
- **Undo is precise.** `varve import undo` deletes the notes the batch created
  and rejects the proposals it created. Rows you have already accepted, edited
  or rejected are left alone and listed by ID.
- **Foreign stores are opened read-only.** If a store's schema is not one this
  importer was tested against, the import refuses rather than importing part of
  it, and points at that tool's own export command.

`varve import rules` skips varve's own instruction block — importing the
instructions that tell an agent how to use varve as though they were project
conventions is noise, not memory.

---

## Export

```bash
# JSON (default)
varve export --output memories.json

# Markdown — human-readable, editable by hand
varve export --format markdown --output memories.md

# Filtered
varve export --type decision --output decisions.json
varve export --status proposed --output pending.json
varve export --status stale --output stale.json
```

`--status` takes note statuses (`active`, `stale`, `archived`) or decision
statuses (`proposed`, `active`, `violated`, `superseded`, `reverted`,
`rejected`). The default is everything live.

---

## Import from a varve export

```bash
# Auto-detected by file extension
varve import memories.md
varve import memories.json

# Preview without saving
varve import memories.md --dry-run

# Only decisions
varve import memories.json --type decision

# Force format
varve import backup.txt --format json
```

Imported decisions land `proposed`, exactly as they do from any other source —
including when the file came from your own `varve export`. A decision that was
accepted in the source project is still a proposal in this one, because
acceptance is a statement about *this* codebase.

---

## Markdown format

The Markdown export uses `## [type] first line` headings with a metadata list
block, separated by `---`. It is readable as-is and editable before reimporting.

```markdown
## [decision] We use JWT with RS256 — stateless API, no session storage

- Tags: auth, security
- Confidence: 1.00
- Created: 2026-03-22T10:00:00Z
- Files: src/middleware/auth.go

We use JWT with RS256 for authentication. The API is completely stateless — no session
storage anywhere in the system. Access tokens expire after 1 hour, refresh tokens after 30 days.

---

## [convention] Error handling: always wrap with fmt.Errorf

- Tags: go, errors
- Confidence: 0.95
- Created: 2026-03-20T08:00:00Z

All errors must be wrapped with fmt.Errorf("context: %w", err) so they are inspectable
with errors.Is / errors.As at the call site.
```

For a decision, `Files:` is its **scope globs**. Writing `internal/auth/**` there
by hand is how you give an imported rule a scope it never had — and an
unscoped decision can never be matched to a commit.

---

## Importing from a URL

Both commands accept an HTTP/HTTPS URL in place of a file path:

```bash
varve import https://example.com/memories.json
```

---

## Round-trip example

```bash
# Export from project A
cd project-a
varve export --format markdown --output ../shared-conventions.md

# Import into project B
cd ../project-b
varve import ../shared-conventions.md --dry-run   # preview first
varve import ../shared-conventions.md
varve decision pending                            # then accept what applies here
```
