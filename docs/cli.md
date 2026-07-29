# CLI Reference

```
memtrace init    [--name <name>] [--no-import]
memtrace setup   [claude-code|cursor|vscode|opencode|windsurf|gemini] [--global]
memtrace save    <content> [--type decision|convention|note] [--tags auth,api] [--files src/auth.go] [--confidence 0.9]
memtrace update  <id|prefix> [--content "..."] [--type ...] [--tags ...] [--files ...] [--confidence 0.9]
memtrace edit    <id|prefix>
memtrace search  <query> [--limit 10] [--type decision] [--json]
memtrace list    [--limit 20] [--type convention] [--status proposed] [--json]
memtrace rm      <id|prefix>
memtrace export  [--output memories.json] [--format json|markdown] [--type decision] [--status active]
memtrace decision pending
memtrace decision accept <id|prefix> [--evidence commit:<sha>] [--force]
memtrace decision reject <id|prefix> [--reason "..."]
memtrace decision revert <id|prefix>
memtrace decision promote <note-id|prefix> [--title "..."] [--kind convention] [--scope "src/**"]
memtrace import  <file|url> [--format json|markdown] [--type decision] [--dry-run]
memtrace browse
memtrace serve   [--dir <path>]
memtrace status  [--json]
memtrace reindex
memtrace scan
memtrace migrate --from-v1
memtrace doctor
memtrace config  get
memtrace config  set <key> <value>
memtrace config  unset <key>
memtrace stats   [--days 7] [--json]
```

---

## `memtrace init`

Initializes memtrace in the current project. Creates `.memtrace/memtrace.db`, adds `.memtrace/` to `.gitignore`, and appends memtrace instructions to `CLAUDE.md`.

Auto-imports from three sources unless `--no-import` is passed:
- **Claude Code memories** — `~/.claude/projects/<project>/memory/*.md`
- **Cursor rules** — `.cursorrules` in the project root
- **Git history** — recent commits containing decisions, migrations, or refactor keywords

```bash
memtrace init
memtrace init --name "my-api"   # override the project name
memtrace init --no-import       # skip auto-import
```

---

## `memtrace setup`

Writes the MCP server entry into your agent's config file. Idempotent — safe to run again.

```bash
memtrace setup              # auto-detect from .claude/, .cursor/, .vscode/, opencode.json, .gemini/
memtrace setup claude-code  # .claude/mcp.json
memtrace setup cursor       # .cursor/mcp.json
memtrace setup vscode       # .vscode/mcp.json
memtrace setup opencode     # opencode.json
memtrace setup windsurf     # ~/.codeium/windsurf/mcp_config.json
memtrace setup gemini       # .gemini/settings.json
memtrace setup --global     # ~/.claude/mcp.json (Claude Code user scope)
```

→ See [Agent Setup](setup.md) for details on each agent.

---

## `memtrace save`

Save a memory from the command line.

```bash
memtrace save "We use Postgres 16 with pgvector for embeddings" \
  --type decision \
  --tags database,postgres \
  --files src/db/client.go
```

---

## `memtrace search`

Search memories by natural language query.

```bash
memtrace search "auth approach"
memtrace search "database" --type decision --limit 5
memtrace search "error handling" --json
```

---

## `memtrace list`

List memories with optional filters.

```bash
memtrace list
memtrace list --type convention
memtrace list --status proposed   # decisions awaiting confirmation
memtrace list --status stale
memtrace list --limit 50 --json
```

---

## `memtrace decision`

Decisions and conventions carry a lifecycle. One saved by an agent, an importer
or the v1 migration lands **`proposed`**: it is captured, but it does not bind
and it is never packed into context until a human accepts it. Acceptance and
rejection are CLI/TUI actions only — an agent asserting "the user approved" is
exactly the assertion the quarantine exists to distrust.

```bash
memtrace decision pending                                   # the confirmation queue, incl. agent disposal requests
memtrace decision accept 01KMDX71NT --evidence commit:9f2c1ab
memtrace decision accept 01KMDX71NT --force                 # no evidence; recorded in the audit trail
memtrace decision reject 01KMDX71NT --reason "duplicate"
memtrace decision revert 01KMDX71NT                         # repeal a binding decision (terminal)
```

**An agent cannot dispose of a decision.** `memory_forget` over MCP records a
`decision.disposal_requested` event and transitions nothing — "the user wanted
this thrown away" is exactly as untrustworthy as "the user approved". The
request shows up in `memtrace decision pending`; you confirm it with
`decision reject` (while proposed) or `decision revert` (once binding), or
ignore it. Notes are ungoverned and are still deleted outright on any channel.

`memtrace decision promote` turns a note into a proposed decision, through the
ordinary lifecycle, so it is born with provenance and a quarantine rather than
by retyping a column. The note stays live while the promotion is pending and is
archived when the decision is accepted; rejecting the promotion leaves the note
untouched.

Acceptance requires at least one evidence row unless `--force` is passed, and a
forced acceptance is recorded as `"forced": true` on the `decision.accepted`
event. Rejection is terminal and keeps the record — it is not a delete.

---

## `memtrace edit`

Open a memory in `$EDITOR`. Saves on exit if content changed. Accepts a full ID or a unique prefix.

```bash
memtrace edit 01KMDX71NT
```

---

## `memtrace rm`

Delete a memory. Accepts a full ID or unique prefix.

```bash
memtrace rm 01KMDX71NT
```

---

## `memtrace export` / `memtrace import`

Export to and import from JSON or Markdown. → See [Import & Export](import-export.md).

---

## `memtrace browse`

Opens a full-screen terminal UI for browsing and managing memories.

Key bindings: `/` filter · `enter` view full memory · `e` edit in `$EDITOR` · `d` delete (with confirmation) · `esc` back · `q` quit.

---

## `memtrace serve`

Starts the MCP server over stdio. This is the command your agent calls — you don't run it directly.

```bash
memtrace serve            # uses current directory
memtrace serve --dir /path/to/project
```

---

## `memtrace scan`

Checks memories that reference source files and marks them stale when those files have been deleted or modified more recently than the memory was last updated.

```bash
memtrace scan
```

```
  stale  01ABCDEF1234  "Auth uses RS256 JWT..." — file modified: src/auth/middleware.go
  stale  01EFGH567890  "Schema migration v3..." — file deleted: db/migrations/003.sql

2 memories marked stale (11 unchanged).
```

Review with `memtrace list --status stale`.

---

## `memtrace migrate`

Converts a v1 database (one `memories` table) to the v2 decision-lifecycle schema.

```bash
memtrace migrate --from-v1
```

**Not available yet.** The v2 read paths are still being built, so a converted database would read as empty — every row would move into `decisions`/`notes`, which no command queries yet. The command refuses until they land, and your v1 database keeps working unchanged in the meantime.

When it does run: the v1 file is moved aside to `.memtrace/memtrace.v1.bak.db` and kept indefinitely — nothing deletes it — along with the JSON export at `.memtrace/migration-v1-export.json`. Decisions and conventions become governed `decisions`; facts and events become `notes`. v2 databases upgrade themselves when opened; only the v1 conversion is manual.

---

## `memtrace doctor`

Runs health checks and reports issues.

```bash
memtrace doctor
```

```
  [ok]   Database:        .memtrace/memtrace.db (24 KB, 42 memories)
  [ok]   Stale memories:  none
  [ok]   Embeddings:      ollama (nomic-embed-text)
  [ok]   Unembedded:      all memories indexed
 [warn]  MCP config:      memtrace not found — run 'memtrace setup'
  [ok]   CLAUDE.md:       memtrace instructions present
```

---

## `memtrace reindex`

Backfills embeddings for memories saved before an embedder was configured.

```bash
memtrace reindex
```

---

## `memtrace status`

Shows the current configuration and database state.

```bash
memtrace status
memtrace status --json
```

---

## `memtrace stats`

Shows memory activity over a rolling window.

```bash
memtrace stats
memtrace stats --days 30 --json
```

---

## `memtrace config`

Reads and writes persistent configuration (embed key, URL, model).

```bash
memtrace config get
memtrace config set embed.key sk-...
memtrace config set embed.model text-embedding-3-small
memtrace config unset embed.key
```

Settings are stored in `~/Library/Application Support/memtrace/config.json` (macOS), `~/.config/memtrace/config.json` (Linux), or `%AppData%\memtrace\config.json` (Windows). Environment variables always take precedence.
