# Semantic Search & Embeddings

`memory_recall` and `varve search` use **hybrid BM25 + semantic scoring** when an embedder is configured. The pipeline runs full-text search and vector similarity independently, merges the candidate pools, and reranks — so memories that match on meaning but not exact keywords still surface.

**Embeddings are optional and off unless something is configured.** Out of the box varve is BM25-only, which is fast, fully local, and enough for most stores. Nothing else degrades without them: `memory_pack` selects by scope glob and rank, `memory_context` by file match, and attribution never uses embeddings at all.

Check what is active:

```bash
varve status     # or: varve doctor
# Embeddings: disabled (BM25-only search)
# Embeddings: ollama (nomic-embed-text)
```

---

## Zero-config with Ollama

If [Ollama](https://ollama.com) is running locally, varve detects it automatically — it probes `localhost:11434` at startup with a 500 ms timeout and uses it if it answers. No configuration needed:

```bash
ollama pull nomic-embed-text
# varve picks it up on next start
```

Verify:

```bash
varve status
# Embeddings: ollama (nomic-embed-text)
```

---

## OpenAI (or any compatible API)

Store the API key in varve's config so both the CLI and MCP server pick it up:

```bash
varve config set embed.key sk-...
varve config set embed.model text-embedding-3-small   # optional, this is the default
```

Or pass it via environment variable:

```bash
export VARVE_EMBED_KEY=sk-...
```

---

## Custom local server

Any OpenAI-compatible endpoint works without an API key:

```bash
varve config set embed.url http://localhost:8080/v1
varve config set embed.model my-model
```

Varve sends a placeholder auth header that local servers ignore.

---

## Passing the key via your MCP client

Add `env` to your MCP config entry so the key is available when `varve serve` runs:

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

## Backfilling existing memories

Memories saved before an embedder was configured have no stored vector — they are still found by BM25, just not by meaning. Run `reindex` once to backfill:

```bash
varve reindex
```

It embeds every active memory with a missing embedding, and needs a configured
API key (`VARVE_EMBED_KEY`, or `OPENAI_API_KEY`) unless you are pointing at a
local server that does not require one.

---

## Disabling semantic search

```bash
varve config set embed.provider disabled
# or: VARVE_EMBED_PROVIDER=disabled
```

---

## Environment variables

Environment variables always override config file values.

| Variable | Default | Description |
|---|---|---|
| `VARVE_EMBED_KEY` | — | API key. Falls back to `OPENAI_API_KEY`. |
| `VARVE_EMBED_URL` | `https://api.openai.com/v1` | Base URL of the embeddings API. |
| `VARVE_EMBED_MODEL` | `text-embedding-3-small` | Model name. |
| `VARVE_EMBED_PROVIDER` | `auto` | Set to `disabled` to turn off embeddings entirely. |

---

## How hybrid scoring works

1. **FTS5** — keyword search across content, summary, tags, and file paths. Returns BM25-ranked candidates.
2. **Vector search** — cosine similarity against stored embeddings for all active memories.
3. **Merge** — candidates from both passes are combined. Vector-only matches get a zero BM25 score.
4. **Rerank** — final score combines BM25, semantic similarity, recency, and confidence decay.

This means a memory saved as "use RS256 for tokens" will still surface for a query like "JWT signing algorithm" even if none of those exact words appear in the content.
