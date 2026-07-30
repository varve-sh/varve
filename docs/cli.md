# CLI Reference

```
varve init    [--name <name>] [--no-import]
varve setup   [claude-code|cursor|vscode|opencode|windsurf|gemini] [--global]
varve save    <content> [--type decision|convention|note] [--tags auth,api] [--files src/auth.go] [--confidence 0.9]
varve update  <id|prefix> [--content "..."] [--type ...] [--tags ...] [--files ...] [--confidence 0.9]
varve edit    <id|prefix>
varve search  <query> [--limit 10] [--type decision] [--json]
varve list    [--limit 20] [--type convention] [--status proposed] [--json]
varve rm      <id|prefix>
varve export  [--output memories.json] [--format json|markdown] [--type decision] [--status active]
varve decision pending
varve decision accept <id|prefix> [--evidence commit:<sha>] [--force]
varve decision reject <id|prefix> [--reason "..."]
varve decision revert <id|prefix>
varve decision promote <note-id|prefix> [--title "..."] [--kind convention] [--scope "src/**"]
varve decision purge <id> [--reason secret|cleanup] [--yes]
varve import  <file|url> [--format json|markdown] [--type decision] [--dry-run]
varve browse
varve serve   [--dir <path>]
varve status  [--json]
varve reindex
varve scan    [--backfill] [--limit 500] [--stale]
varve observe [--commit HEAD] [--quiet]
varve hooks   install
varve report  [--days 30] [--format text|md|json] [--decision <id>] [--raw] [--grace 60]
varve report  coverage [--days 30]
varve migrate --from-v1
varve doctor
varve config  get
varve config  set <key> <value>
varve config  unset <key>
varve stats   [--days 7] [--json]
```

---

## `varve init`

Initializes varve in the current project. Creates `.varve/varve.db`, adds `.varve/` to `.gitignore`, and appends varve instructions to `CLAUDE.md`.

Auto-imports from three sources unless `--no-import` is passed:
- **Claude Code memories** — `~/.claude/projects/<project>/memory/*.md`
- **Cursor rules** — `.cursorrules` in the project root
- **Git history** — recent commits containing decisions, migrations, or refactor keywords

```bash
varve init
varve init --name "my-api"   # override the project name
varve init --no-import       # skip auto-import
```

---

## `varve setup`

Writes the MCP server entry into your agent's config file. Idempotent — safe to run again.

```bash
varve setup              # auto-detect from .claude/, .mcp.json, .cursor/, .vscode/, opencode.json, .gemini/
varve setup claude-code  # .mcp.json
varve setup cursor       # .cursor/mcp.json
varve setup vscode       # .vscode/mcp.json
varve setup opencode     # opencode.json
varve setup windsurf     # ~/.codeium/windsurf/mcp_config.json
varve setup gemini       # .gemini/settings.json
varve setup --global     # ~/.claude.json (Claude Code user scope)
```

→ See [Agent Setup](setup.md) for details on each agent.

---

## `varve save`

Save a memory from the command line.

```bash
varve save "We use Postgres 16 with pgvector for embeddings" \
  --type decision \
  --tags database,postgres \
  --files src/db/client.go
```

---

## `varve search`

Search memories by natural language query.

```bash
varve search "auth approach"
varve search "database" --type decision --limit 5
varve search "error handling" --json
```

---

## `varve list`

List memories with optional filters.

```bash
varve list
varve list --type convention
varve list --status proposed   # decisions awaiting confirmation
varve list --status stale
varve list --limit 50 --json
```

---

## `varve decision`

Decisions and conventions carry a lifecycle. One saved by an agent, an importer
or the v1 migration lands **`proposed`**: it is captured, but it does not bind
and it is never packed into context until a human accepts it. Acceptance and
rejection are CLI/TUI actions only — an agent asserting "the user approved" is
exactly the assertion the quarantine exists to distrust.

```bash
varve decision pending                                   # the confirmation queue, incl. agent disposal requests
varve decision accept 01KMDX71NT --evidence commit:9f2c1ab
varve decision accept 01KMDX71NT --force                 # no evidence; recorded in the audit trail
varve decision reject 01KMDX71NT --reason "duplicate"
varve decision revert 01KMDX71NT                         # repeal a binding decision (terminal)
```

`varve decision purge` is the one irreversible verb in the product, and it
is not a forget. `rm`, `decision reject` and `decision revert` all keep the
decision as an audit record; purge destroys its content. It exists for one real
case — a secret pasted into a decision body — and it asks you to type the id
back before it runs.

- A decision **with history** is *redacted*: its content is cleared, it moves
  to a terminal state, and the row survives as a `[purged]` tombstone, because
  its events are append-only and its id is referenced by the attribution trail.
- A decision **with no history at all** (carried over by `migrate --from-v1`
  and untouched since) is deleted outright, leaving a tombstone event.

Purge cannot reach the v1 backup, the migration export, or any copy outside the
store; it prints those paths and expects you to handle them. There is no MCP
equivalent, by design.

**An agent cannot dispose of a decision.** `memory_forget` over MCP records a
`decision.disposal_requested` event and transitions nothing — "the user wanted
this thrown away" is exactly as untrustworthy as "the user approved". The
request shows up in `varve decision pending`; you confirm it with
`decision reject` (while proposed) or `decision revert` (once binding), or
ignore it. Notes are ungoverned and are still deleted outright on any channel.

`varve decision promote` turns a note into a proposed decision, through the
ordinary lifecycle, so it is born with provenance and a quarantine rather than
by retyping a column. The note stays live while the promotion is pending and is
archived when the decision is accepted; rejecting the promotion leaves the note
untouched.

Acceptance requires at least one evidence row unless `--force` is passed, and a
forced acceptance is recorded as `"forced": true` on the `decision.accepted`
event. Rejection is terminal and keeps the record — it is not a delete.

---

## `varve edit`

Open a memory in `$EDITOR`. Saves on exit if content changed. Accepts a full ID or a unique prefix.

```bash
varve edit 01KMDX71NT
```

---

## `varve rm`

Delete a memory. Accepts a full ID or unique prefix.

```bash
varve rm 01KMDX71NT
```

---

## `varve export` / `varve import`

Export to and import from JSON or Markdown. → See [Import & Export](import-export.md).

`varve import` with no arguments lists the memory sources it can find and
imports nothing. Each source has its own subcommand:

```
varve import claude-mem [--db <path>]   # claude-mem's store — imports as notes
varve import engram     [--db <path>]   # engram — notes, plus proposed decisions
                                           #   for rows engram itself typed as such
varve import rules                      # CLAUDE.md, AGENTS.md, .cursorrules,
                                           #   .cursor/rules/*.mdc
varve import undo [<batch-id>]          # default: the most recent batch
```

Flags on every source: `--dry-run` (preview, save nothing), `--yes` (skip the
confirmation), `--as-notes` (demote every decision candidate to a note),
`--format md|json` (report output).

What import does and does not do:

- Nothing lands active. Decision candidates land **proposed** — review them with
  `varve decision pending` and accept the ones you want.
- Re-running an import against unchanged sources creates zero rows; already
  imported entries are counted as skipped, never silently re-added.
- `varve import undo` deletes the notes the batch created and rejects the
  proposals it created. Rows you have already accepted, edited or rejected are
  left alone and listed by ID.
- Foreign stores are opened read-only. If a store's schema is not one this
  importer was tested against, the import refuses rather than importing part of
  it, and points at that tool's own export command.

---

## `varve lint`

Health-checks the corpus and prints the report — the same report every import
run ends with.

```
varve lint [--format md|json] [--raw] [--aggregate]
```

The corpus-health score covers properties of your memory: dead references,
duplicates, contradiction candidates, staleness and scope hygiene. It prints
`x of n` beside every category, states its method, and is suppressed entirely
below ten entries — a percentage over four rows is noise. Adoption facts
(proposals awaiting review, packing history, curated evidence) are listed but
never scored.

Every finding names the rows behind it: `--format md` is the forwardable
version, `--raw` prints the rows themselves. The checks are deterministic SQL
plus local git plumbing — no model runs, and nothing leaves the machine.
Paraphrase duplicates and semantic contradictions are **not** detected, which
the report says on its own footer.

`--aggregate` prints a summary containing no content from your store: the score
and its per-category arithmetic, the method disclosures, adoption counts, how
many unscored review candidates exist, and the varve and schema versions. No
IDs, titles, findings, file paths or scope globs — a glob is a path in your
repository. It exists because whether the score discriminates can only be
answered across many corpora, and the alternative was asking people to send a
partial dump of a private store.

```bash
varve lint --aggregate > varve-health.json   # read it, then send it if you want to
```

Nothing is transmitted. There is no endpoint and no telemetry; this writes a
file you can read in full and choose to share.

---

## `varve browse`

Opens a full-screen terminal UI for browsing and managing memories.

Key bindings: `/` filter · `enter` view full memory · `e` edit in `$EDITOR` · `d` delete (with confirmation) · `esc` back · `q` quit.

---

## `varve serve`

Starts the MCP server over stdio. This is the command your agent calls — you don't run it directly.

```bash
varve serve            # uses current directory
varve serve --dir /path/to/project
```

---

## `varve scan --stale`

Checks memories that reference source files and marks them stale when those files have been deleted or modified more recently than the memory was last updated.

```bash
varve scan --stale
```

```
  stale  01ABCDEF1234  "Auth uses RS256 JWT..." — file modified: src/auth/middleware.go
  stale  01EFGH567890  "Schema migration v3..." — file deleted: db/migrations/003.sql

2 memories marked stale (11 unchanged).
```

Review with `varve list --status stale`. Bare `varve scan` observes
commits — see below.

---

## `varve migrate`

Converts a v1 database (one `memories` table) to the v2 decision-lifecycle schema.

```bash
varve migrate --from-v1
```

The v1 file is moved aside to `.varve/varve.v1.bak.db` and kept indefinitely — nothing deletes it — along with the JSON export at `.varve/migration-v1-export.json`. Decisions and conventions become governed `decisions`; facts and events become `notes`. v2 databases upgrade themselves when opened; only the v1 conversion is manual.

The command does not require a config entry, so it works on an unregistered store: migration is a repair on a database that already exists, and the project id it needs is read out of that database. Registration follows a successful migration rather than gating it.

---

## `varve store`

Inspects and relocates the store itself.

```bash
varve store move --dry-run   # show what would move
varve store move
```

`move` is the memtrace→varve rename repair: it moves `.memtrace/memtrace.db`, its WAL sidecars, the observer log and the config to `.varve/varve.db`. Nothing is deleted — the old directory stays in place if it holds anything unrecognised, and the move is skipped entirely if a store already exists at the new location.

This is separate from `migrate`: `move` changes where the store lives, `migrate` changes what is inside it.

---

## `varve doctor`

Runs health checks and reports issues.

```bash
varve doctor
```

```
  [ok]   Database:        .varve/varve.db (24 KB, 42 memories)
  [ok]   Stale memories:  none
  [ok]   Embeddings:      ollama (nomic-embed-text)
  [ok]   Unembedded:      all memories indexed
 [warn]  MCP config:      varve not found — run 'varve setup'
  [ok]   CLAUDE.md:       varve instructions present
```

---

## `varve reindex`

Backfills embeddings for memories saved before an embedder was configured.

```bash
varve reindex
```

---

## `varve report`

What the agents did with your decisions. Every figure comes from the
append-only event log and drills down to the rows behind it.

```bash
varve report                         # last 30 days
varve report --days 7 --format md    # forwardable
varve report --decision 01KMDX71NT --raw   # the raw events behind the numbers
varve report coverage                # the kill-criterion metric
```

The report prints its own method and its own limits, on the report, every
time: the grace window used, that reverts are detected from git trailers only,
and that **conform means no deterministic violation signal was detected — not
verified compliance**. Attribution shows the recorded chain on individual
cases; it does not establish what would have happened without the decision.

Rates always carry their sample size, and a rate over fewer than five cases is
shown as a raw fraction rather than a percentage.

---

## `varve scan`, `varve observe`, `varve hooks install`

How commits get attributed to decisions.

```bash
varve hooks install      # post-commit hook; `varve init` does this for you
varve scan               # observe commits the hook missed
varve scan --backfill    # also observe commits older than this store
varve scan --stale       # the note-staleness scan (unrelated)
```

The hook runs `varve observe` in the background. It cannot block a commit,
cannot fail one, and prints nothing; if it misses a commit — because another
process held the database, or because it was never installed — `varve scan`
picks it up, and the scan also runs automatically when an agent session starts.

Commits older than the store are skipped unless you ask for them, and anything
`--backfill` produces is marked and excluded from every reported metric: a
verdict about a commit that predates your decisions is archaeology, not
attribution.

---

## `varve status`

Shows the current configuration and database state.

```bash
varve status
varve status --json
```

---

## `varve stats`

Shows memory activity over a rolling window.

```bash
varve stats
varve stats --days 30 --json
```

---

## `varve config`

Reads and writes persistent configuration (embed key, URL, model).

```bash
varve config get
varve config set embed.key sk-...
varve config set embed.model text-embedding-3-small
varve config unset embed.key
```

Settings are stored in `~/Library/Application Support/varve/config.json` (macOS), `~/.config/varve/config.json` (Linux), or `%AppData%\varve\config.json` (Windows). Environment variables always take precedence.
