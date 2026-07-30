<p align="left">
  <img src="logo.svg" alt="varve" width="180" />
</p>

# varve
[varve.sh](https://varve.sh) · Decision memory for AI coding agents — local-first, MCP-native, one binary.

Varve gives Claude Code, Cursor, Windsurf and any MCP-compatible agent a memory that survives every session — and a record of what happened after. Decisions carry evidence, scope, provenance and a lifecycle. The ones binding on the files you're touching are packed into each session within a token budget. Commits are linked back to the decisions that were in scope when they landed, including the ones that were later reverted.

A decision an agent saves arrives **proposed**: it doesn't bind, and it isn't packed as context, until a human accepts it.

> **What varve does not claim.** Attribution is a traceable record, not a causal
> one. Varve shows the chain on an individual decision — packed into this
> session, this commit touched its scope, that commit reverted it — and does not
> estimate what would have happened otherwise. "Conformed" means no violation
> signal was detected, not that compliance was verified.

---

## Install

```bash
brew trust varve-sh/tap          # Homebrew 6 requires this for any third-party tap
brew install varve-sh/tap/varve
```

Without the first line Homebrew 6 refuses the formula — `Refusing to load
formula varve-sh/tap/varve from untrusted tap varve-sh/tap`. That is Homebrew
policy for every tap outside homebrew/core, not a problem with this one.

Or: `go install github.com/varve-sh/varve/cmd/varve@latest` · [prebuilt binaries](https://github.com/varve-sh/varve/releases/latest)

> Upgrading from **memtrace**? `brew update && brew upgrade` migrates you — the
> tap maps the old name to `varve`. Your store is untouched; varve reads a
> pre-rename `.memtrace/memtrace.db` in place and tells you so. `varve store
> move` relocates it when you want that, and `varve migrate --from-v1` converts
> a v1 database to the v2 schema.

---

## Quickstart

```bash
# 1. Initialize in your project
cd your-project
varve init           # creates .varve/varve.db, installs the post-commit hook

# 2. Wire up your agent
varve setup          # auto-detects Claude Code, Cursor, Windsurf, VS Code, OpenCode, Gemini CLI

# 3. Start a new session — memory is live

# 4. Review what the agent proposed
varve decision pending
varve decision accept <id> --evidence commit:9f2c1ab
```

Your agent now has eight tools: `memory_pack`, `memory_recall`, `memory_context`, `memory_save`, `memory_get`, `memory_update`, `memory_forget`, and `memory_prompt`.

Step 4 is not optional bookkeeping — it is the whole governance model. Nothing an agent saves as a decision binds anything until you accept it.

---

## How it works

Two classes of memory, and they behave differently.

**Decisions and conventions are governed.** They carry a lifecycle, a scope of
file globs, evidence and provenance, and every transition is written to an
append-only event log.

```
proposed ──accept──▶ active ──▶ violated ──▶ superseded
    │                   │                ╰──▶ reverted
    ╰──reject──▶ rejected
```

**Notes are ungoverned.** Facts, session summaries, prompts — retrievable, no
lifecycle, no evidence, no attribution. `fact` and `event` are accepted as
synonyms for `note`.

A session looks like this:

```
Session 1
  agent → memory_pack(file_paths: ["internal/auth/**"], task: "add refresh rotation")
  ← [1] DECISION 01J9W… · active · scope: internal/auth/**
  ←     Refresh tokens rotate on every use; reuse detection revokes the family.
  ← -- proposed decisions touching these files: 2 — not binding until accepted

  agent → memory_save("Rotation uses a 30-day family window", type: "decision")
  ← Saved 01KMDX… (proposed — waiting for `varve decision accept`)

You
  $ varve decision accept 01KMDX --evidence commit:9f2c1ab

Session 2 (new chat, blank context)
  agent → memory_pack(...)
  ← the accepted decision is now packed, in rank order, inside the budget
```

Meanwhile the post-commit hook records every commit against the decisions whose
scope it touched, so `varve report` can show what actually happened.

All data lives in `.varve/varve.db` — SQLite, local only, no account required.

---

## Why varve

- **Packed, not dumped** — `memory_pack` returns what binds the files you're about to touch, in rank order, inside a token budget. Nothing is dropped silently: what doesn't fit is named in the footer with its rank and cost.
- **A human accepts what binds** — agent-saved decisions are quarantined as `proposed`. An agent can't accept, reject or repeal one; those are CLI actions, by design.
- **Scopes are file globs** — a decision declares what it governs (`internal/auth/**`), so context is selected by relevance to your diff rather than by keyword luck.
- **Attribution, honestly bounded** — commits are linked to the decisions in scope when they landed, with the method and the limits printed on the report every time.
- **Corpus health you can check** — `varve lint` runs ten deterministic structural checks over your own store. No model runs, nothing leaves the machine.
- **Hybrid search** — BM25 full-text by default; add an embedder and semantic scoring merges in.
- **Private content** — wrap sensitive details in `<private>...</private>` and they're stripped before storage.
- **Works everywhere** — one binary, no daemon, no Docker. Sets up in any editor in one command.

---

## MCP Tools

| Tool | What it does |
|------|-------------|
| `memory_pack` | **Session bootstrap.** Everything binding on the files you're about to touch, packed into a token budget, in rank order. Proposals are counted in the footer, never included as content |
| `memory_recall` | Search memories by natural language query. Mid-task exploration, where `memory_pack` is the once-per-session bootstrap. Proposals come back marked `PROPOSED` |
| `memory_context` | Everything binding on a given set of files. Proposals are reported as a trailing count with their ids, not as content |
| `memory_save` | Save a decision, convention or note. A decision or convention is recorded **proposed** and does not bind until a human accepts it |
| `memory_get` | Fetch the full content of a memory by ID |
| `memory_update` | Edit an existing memory by ID. Cannot move a note into the governed class — `varve decision promote <id>` does that, and it's a human action |
| `memory_forget` | Delete a **note** outright. A decision or convention is *not* deleted, rejected or reverted: the call records a disposal request and returns it as pending human confirmation |
| `memory_prompt` | Capture the user's original request at session start |

→ [Full MCP tools reference](docs/mcp-tools.md)

---

## Governing decisions

```bash
varve decision pending                                # the confirmation queue
varve decision accept 01KMDX --evidence commit:9f2c1ab
varve decision reject 01KMDX --reason "duplicate"
varve decision revert 01KMDX                          # repeal a binding decision
varve decision promote 01KMDX                         # note → proposed decision
```

Acceptance requires at least one evidence row unless `--force` is passed, and a
forced acceptance is recorded as such in the audit trail. Rejection and reversal
are terminal states that keep the record — "we considered X and said no" is
exactly what a later session needs.

→ [Decision lifecycle](docs/decisions.md)

---

## Attribution

```bash
varve report                    # what the agents did with your decisions
varve report --decision <id> --raw   # the raw events behind every number
varve report coverage           # the kill-criterion metric
varve scan                      # observe commits the hook missed
```

The post-commit hook (installed by `varve init`) records each commit against the
decisions whose scope it touched. It cannot block or fail a commit and prints
nothing; anything it misses, `varve scan` picks up.

Commits older than the store are excluded unless you ask for them with
`--backfill`, and anything backfill produces is marked and kept out of every
reported metric — a verdict about a commit that predates your decisions is
archaeology, not attribution.

→ [Attribution](docs/attribution.md)

---

## Importing existing memory

```bash
varve import              # list the sources found here; import nothing
varve import rules        # CLAUDE.md, AGENTS.md, .cursorrules, .cursor/rules
varve import claude-mem   # claude-mem's store, as notes
varve import engram       # engram's store
varve import undo         # undo the most recent batch
```

Nothing lands active — decision candidates land `proposed` for you to review.
Re-running an import against unchanged sources creates zero rows.

→ [Import & Export](docs/import-export.md)

---

## Documentation

| | |
|---|---|
| [Concepts](docs/concepts.md) | Decisions vs notes, scopes, confidence, staleness, private content |
| [Decision lifecycle](docs/decisions.md) | Proposal, acceptance, evidence, supersession, repeal, purge |
| [MCP Tools](docs/mcp-tools.md) | All eight tools, parameters, and examples |
| [CLI Reference](docs/cli.md) | Every command with flags |
| [Attribution](docs/attribution.md) | The observer, the hook, `varve report`, and what it does not claim |
| [Corpus health](docs/lint.md) | The ten checks, the score, and `--aggregate` |
| [Agent Setup](docs/setup.md) | Claude Code, Cursor, Windsurf, VS Code, OpenCode, Gemini CLI |
| [Semantic Search](docs/embeddings.md) | Ollama, OpenAI, custom endpoints, env vars |
| [Import & Export](docs/import-export.md) | Importers, JSON and Markdown, round-trip, undo |

---

## Development

```bash
make build      # build binary to bin/varve
make install    # build and install to $GOPATH/bin (by rename — see the Makefile)
make test       # run all tests
make dogfood    # install, then verify the installed binary is this tree
make snapshot   # cross-platform build via goreleaser (no publish)
make release VERSION=2.0.3  # tag + push → triggers GitHub release workflow
```

`make dogfood` exists because exercising varve on a real store exercises the
*installed* binary, and nothing otherwise connects that to your working tree. It
refuses to run when the two disagree, and warns when a bare `varve` on your PATH
resolves somewhere else.

---

## Author

Built by [Sebastian Puchet](https://github.com/SebastianPuchet) — [LinkedIn](https://www.linkedin.com/in/sebastianpuchet/)

---

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

The license covers the source. It does not grant rights to the **varve** name or
logo (License §6); trademark status for the name is unresolved. Releases up to
v1.5.3 were published under MIT and remain available under those terms.
