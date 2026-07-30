# Agent Setup

`varve setup` writes the MCP config entry for your agent, and an instruction
block into its rules file. It is idempotent — running it again is safe, merges
into existing configs without overwriting other entries, and repairs an entry
that has gone stale.

```bash
varve setup              # auto-detect every agent configured here
varve setup claude-code  # or: cursor, vscode, opencode, windsurf, gemini
varve setup --global     # Claude Code at user scope
```

With no agent named, varve detects which agents are configured in the current
directory and sets up all of them, falling back to `claude-code` if none are
detected.

---

## The command it writes is an absolute path

Every example below shows `"command": "varve"` for readability. **What varve
actually writes is the absolute path to the running binary**, for example
`/opt/homebrew/bin/varve` or `/Users/you/go/bin/varve`.

This is deliberate. The agent host spawns the MCP server in a non-interactive
environment where your shell's `PATH` does not apply, so a bare `varve` is a
command the agent cannot launch. When the invocation path is a stable symlink,
that is what gets recorded — not the versioned directory a package manager
deletes on the next upgrade.

Two consequences worth knowing:

- **Re-run `varve setup` after moving or reinstalling the binary.** `varve
  doctor` reports a config entry pointing at a binary that is no longer there.
- **Consider not committing `.mcp.json`.** It is conventionally committed so a
  team is wired on clone, but an absolute path is true on exactly one machine —
  committing it hands every teammate a command only you can run. Add it to
  `.gitignore` and have each clone run `varve setup`. (`varve init` gitignores
  `.varve/` for you; the config file is your call.)

---

## Claude Code

**Project scope** (recommended — memory is scoped to this project):

```bash
varve setup claude-code
```

Writes `.mcp.json` in the project root — the file Claude Code loads
project-scoped MCP servers from:

```json
{
  "mcpServers": {
    "varve": {
      "command": "/absolute/path/to/varve",
      "args": ["serve"]
    }
  }
}
```

**User scope** (all projects):

```bash
varve setup --global
```

Writes `~/.claude.json`, merging into whatever is already there.

> Earlier versions wrote `.claude/mcp.json` and `~/.claude/mcp.json`. Claude
> Code reads neither, so those entries never loaded. Re-running `varve setup`
> writes the correct file and removes the dead entry; `varve doctor` names it if
> it is still around.

---

## Cursor

```bash
varve setup cursor
```

Writes `.cursor/mcp.json` (same shape as Claude Code) and a rule file at
`.cursor/rules/varve.mdc`.

---

## VS Code (Copilot)

```bash
varve setup vscode
```

Writes `.vscode/mcp.json`:

```json
{
  "servers": {
    "varve": {
      "type": "stdio",
      "command": "/absolute/path/to/varve",
      "args": ["serve"]
    }
  }
}
```

---

## OpenCode

```bash
varve setup opencode
```

Writes `opencode.json` in the project root:

```json
{
  "mcp": {
    "varve": {
      "type": "local",
      "command": ["/absolute/path/to/varve", "serve"]
    }
  }
}
```

---

## Windsurf

```bash
varve setup windsurf
```

Writes `~/.codeium/windsurf/mcp_config.json` (global — Windsurf doesn't support
project-scoped MCP config):

```json
{
  "mcpServers": {
    "varve": {
      "command": "/absolute/path/to/varve",
      "args": ["serve"]
    }
  }
}
```

---

## Gemini CLI

```bash
varve setup gemini
```

Writes `.gemini/settings.json` in the project root, in the `mcpServers` shape.

---

## Passing environment variables

To configure the embeddings API key through the MCP client (so you don't need
`varve config set`), add `env` to the config entry manually:

```json
{
  "mcpServers": {
    "varve": {
      "command": "/absolute/path/to/varve",
      "args": ["serve"],
      "env": {
        "VARVE_EMBED_KEY": "sk-..."
      }
    }
  }
}
```

---

## The instruction block

`varve init` and `varve setup` append a varve section to your agent's rules file
— `CLAUDE.md`, `.cursor/rules/varve.mdc`, `.github/copilot-instructions.md`,
`.windsurfrules` or `GEMINI.md`. Existing content is appended to, never
overwritten, and the block is written only once.

It covers three things, and the third is the one that matters:

1. **The tools** — `memory_pack`, `memory_recall`, `memory_save`, `memory_get`,
   `memory_update`, `memory_forget`, `memory_context`, `memory_prompt`.
2. **When to call them** — pack before touching files, recall mid-task, recall
   before committing to check conventions, save when something is learned.
3. **What actually happens** — that a saved decision lands `proposed` and is
   waiting for `varve decision accept`, that forgetting a decision files a
   request rather than deleting anything, that `memory_context` never returns
   proposals as content, and that a pack footer names everything it elided.

Point 3 exists because agents that are not told this reliably report proposals
as adopted — "I've saved that convention" when nothing binds. If you write your
own rules file instead of using the generated block, carry that part across.

If you skipped init or use an unsupported agent, the minimum viable version is:

```
This project has the varve MCP server connected. Use its tools for all memory
operations — never use built-in memory tools.

Before touching files, call memory_pack with the paths you are about to read or
edit. A decision saved with memory_save lands "proposed": it does not bind until
a human runs `varve decision accept <id>`, so say the proposal is waiting rather
than reporting it as adopted. memory_forget on a decision records a disposal
request and changes nothing; a human confirms it.
```

---

## Verifying

```bash
varve doctor
```

```
  [ok]   Database:          .varve/varve.db (3.0 MB, 95 memories)
  [ok]   Pending decisions: none
  [ok]   Commit timestamps: all observed commits are attributable
  [ok]   Stale memories:    none
  [ok]   Embeddings:        disabled (BM25-only search)
  [ok]   MCP config:        found in .mcp.json
  [ok]   CLAUDE.md:         varve instructions present
```

Then start a new agent session — MCP servers are launched at session start, so a
session already running will not see varve until it restarts.
