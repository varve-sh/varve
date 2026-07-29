<p align="left">
  <img src="logo.svg" alt="varve" width="180" />
</p>

# varve
[varve.sh](https://varve.sh) · Decision memory for AI coding agents — local-first, MCP-native, one binary.

Varve gives Claude Code, Cursor, Windsurf and any MCP-compatible agent a memory that survives every session — and a record of what happened after. Decisions carry evidence, scope, provenance and an expiry. The ones relevant to the files you're touching are packed into each session within a token budget. Commits are linked back to the decisions that were in scope when they landed, including the ones that were later reverted.

A decision an agent saves arrives **proposed**: it doesn't bind, and it isn't packed as context, until a human accepts it.

> **What varve does not claim.** Attribution is a traceable record, not a causal
> one. Varve shows the chain on an individual decision — packed into this
> session, this commit touched its scope, that commit reverted it — and does not
> estimate what would have happened otherwise. "Conformed" means no violation
> signal was detected, not that compliance was verified.

---

## Install

```bash
brew install varve-sh/tap/varve
```

Or: `go install github.com/memtrace-dev/memtrace/cmd/varve@latest` · [prebuilt binaries](https://github.com/memtrace-dev/memtrace/releases/latest)

---

## Quickstart

```bash
# 1. Initialize in your project
cd your-project
varve init

# 2. Wire up your agent
varve setup          # auto-detects Claude Code, Cursor, Windsurf, VS Code, OpenCode, Gemini CLI

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

All data lives in `.varve/varve.db` — SQLite, local only, no account required.

---

## Why varve

- **Hybrid search** — BM25 full-text + vector semantic search. Finds memories even when you use different words.
- **File-aware context** — `memory_context(file_paths)` surfaces conventions and decisions linked to the files you're editing.
- **Confidence decay** — memories age gracefully. Recalled memories stay fresh; stale ones fade.
- **Staleness detection** — `varve scan` flags memories whose referenced files have changed.
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
| [Agent Setup](docs/setup.md) | Wire varve into Claude Code, Cursor, Windsurf, VS Code, OpenCode, Gemini CLI |
| [Semantic Search](docs/embeddings.md) | Ollama, OpenAI, custom endpoints, env vars |
| [Import & Export](docs/import-export.md) | JSON and Markdown, round-trip, dry run |
| [Concepts](docs/concepts.md) | Memory types, confidence decay, staleness, private content |

---

## Development

```bash
make build      # build binary to bin/varve
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

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

The license covers the source. It does not grant rights to the **varve** name or
logo (License §6); trademark status for the name is unresolved. Releases up to
v1.5.3 were published under MIT and remain available under those terms.
