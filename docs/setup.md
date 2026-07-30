# Agent Setup

`varve setup` writes the MCP config entry for your agent. It is idempotent — running it again is safe and merges into existing configs without overwriting other entries.

```bash
varve setup   # auto-detect from .claude/, .mcp.json, .cursor/, .vscode/, opencode.json, .gemini/
```

---

## Claude Code

**Project scope** (recommended — memory is scoped to this project):

```bash
varve setup claude-code
```

Writes to `.mcp.json` in the project root — the file Claude Code loads project-scoped MCP servers from:

```json
{
  "mcpServers": {
    "varve": {
      "command": "varve",
      "args": ["serve"]
    }
  }
}
```

**User scope** (all projects):

```bash
varve setup --global
```

Writes to `~/.claude.json`, merging into whatever is already there.

> Earlier versions wrote `.claude/mcp.json` and `~/.claude/mcp.json`. Claude Code reads neither, so those entries never loaded. Re-running `varve setup` writes the correct file and removes the dead entry; `varve doctor` names it if it is still around.

---

## Cursor

```bash
varve setup cursor
```

Writes to `.cursor/mcp.json` (same format as Claude Code).

---

## VS Code (Copilot)

```bash
varve setup vscode
```

Writes to `.vscode/mcp.json`:

```json
{
  "servers": {
    "varve": {
      "type": "stdio",
      "command": "varve",
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

Writes to `opencode.json` in the project root:

```json
{
  "mcp": {
    "varve": {
      "type": "local",
      "command": ["varve", "serve"]
    }
  }
}
```

---

## Windsurf

```bash
varve setup windsurf
```

Writes to `~/.codeium/windsurf/mcp_config.json` (global — Windsurf doesn't support project-scoped MCP config):

```json
{
  "mcpServers": {
    "varve": {
      "command": "varve",
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

Writes to `.gemini/settings.json` in the project root:

```json
{
  "mcpServers": {
    "varve": {
      "command": "varve",
      "args": ["serve"]
    }
  }
}
```

---

## Passing environment variables

To configure the embeddings API key through the MCP client (so you don't need `varve config set`), add `env` to the config entry manually:

```json
{
  "mcpServers": {
    "varve": {
      "command": "varve",
      "args": ["serve"],
      "env": {
        "VARVE_EMBED_KEY": "sk-..."
      }
    }
  }
}
```

---

## CLAUDE.md instructions

`varve init` automatically appends instructions to `CLAUDE.md` directing Claude to use varve tools instead of its built-in memory. If you skip init or use a different agent, add this to your project's system prompt or rules file:

```
This project has the varve MCP server connected. Use memory_save, memory_recall,
memory_get, memory_forget, memory_update, memory_context, and memory_prompt for all
memory operations — do not use built-in memory tools.
```
