<p align="left">
  <img src="logo.svg" alt="memtrace" width="180" />
</p>

# memtrace
[memtrace.sh](https://memtrace.sh) · Persistent, searchable memory for AI coding agents — local, fast, zero-config.

Memtrace gives Claude Code, Cursor, Windsurf, and any MCP-compatible agent a memory that survives every new session. Decisions, conventions, and codebase knowledge are there the next time you open a chat.

---

## Install

```bash
brew install memtrace-dev/tap/memtrace
```

Or: `go install github.com/memtrace-dev/memtrace/cmd/memtrace@latest` · [prebuilt binaries](https://github.com/memtrace-dev/memtrace/releases/latest)

---

## Quickstart

```bash
# 1. Initialize in your project
cd your-project
memtrace init

# 2. Wire up your agent
memtrace setup          # auto-detects Claude Code, Cursor, Windsurf, VS Code, OpenCode, Gemini CLI

# 3. Start a new session — memory is live
```

Your agent now has seven tools: `memory_save`, `memory_recall`, `memory_get`, `memory_forget`, `memory_update`, `memory_context`, and `memory_prompt`.

---

## How it works

```
Session 1
  agent → memory_save("We use JWT RS256 — stateless API, no session storage.")
  agent → memory_save("Auth middleware lives in src/middleware/auth.go")

Session 2 (new chat, blank context)
  agent → memory_recall("auth")
  ← "We use JWT RS256 — stateless API, no session storage."
  ← "Auth middleware lives in src/middleware/auth.go"
```

All data lives in `.memtrace/memtrace.db` — SQLite, local only, no account required.

---

## Why memtrace

- **Hybrid search** — BM25 full-text + vector semantic search. Finds memories even when you use different words.
- **File-aware context** — `memory_context(file_paths)` surfaces conventions and decisions linked to the files you're editing.
- **Confidence decay** — memories age gracefully. Recalled memories stay fresh; stale ones fade.
- **Staleness detection** — `memtrace scan` flags memories whose referenced files have changed.
- **Private content** — wrap sensitive details in `<private>...</private>` and they're stripped before storage.
- **Works everywhere** — one binary, no daemon, no Docker. Sets up in any editor in one command.

---

## MCP Tools

| Tool | What it does |
|------|-------------|
| `memory_save` | Save a decision, convention or note |
| `memory_recall` | Search memories by natural language query |
| `memory_get` | Fetch the full content of a memory by ID |
| `memory_forget` | Delete a memory by ID or query |
| `memory_update` | Edit an existing memory by ID |
| `memory_context` | Get all memories relevant to a set of files |
| `memory_prompt` | Capture the user's original request at session start |

→ [Full MCP tools reference](docs/mcp-tools.md)

---

## Documentation

| | |
|---|---|
| [MCP Tools](docs/mcp-tools.md) | All tools, parameters, and examples |
| [CLI Reference](docs/cli.md) | Every command with flags |
| [Agent Setup](docs/setup.md) | Wire memtrace into Claude Code, Cursor, Windsurf, VS Code, OpenCode, Gemini CLI |
| [Semantic Search](docs/embeddings.md) | Ollama, OpenAI, custom endpoints, env vars |
| [Import & Export](docs/import-export.md) | JSON and Markdown, round-trip, dry run |
| [Concepts](docs/concepts.md) | Memory types, confidence decay, staleness, private content |

---

## Development

```bash
make build      # build binary to bin/memtrace
make install    # build and copy to $GOPATH/bin
make test       # run all tests
make snapshot   # cross-platform build via goreleaser (no publish)
make release VERSION=1.2.3  # tag + push → triggers GitHub release workflow
```

---

## Author

Built by [Sebastian Puchet](https://github.com/SebastianPuchet) — [LinkedIn](https://www.linkedin.com/in/sebastianpuchet/)

---

## License

MIT — see [LICENSE](LICENSE)
