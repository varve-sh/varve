# memtrace

This project has the **memtrace MCP server** connected. Use its tools for **all** memory operations — never use built-in memory tools or write to ~/.claude/projects/.

## Memory tools

- `memory_recall` — search memories by natural language query
- `memory_save` — save a decision, convention or note (`fact`/`event` are accepted as synonyms for `note`)
- `memory_get` — fetch full content of a memory by ID
- `memory_update` — patch an existing memory by ID (content, tags, type, confidence)
- `memory_forget` — delete or archive a memory by ID or query
- `memory_context` — get memories relevant to specific file paths
- `memory_prompt` — capture the user's goal at the start of a session

## Rules (always follow)

- **Before every task** — call `memory_recall` with a relevant query, no exceptions. This includes commits, quick fixes, and one-liners.
- **Before committing** — call `memory_recall` to check for commit conventions.
- **Learn something new** — call `memory_save` to persist it.
- **User says forget/delete/remove** — call `memory_forget`.
- **Never** write memory files manually or use built-in memory features.

## How memories are governed

- A **decision** or **convention** you save lands **`proposed`**. It does *not*
  bind and is never packed into context until a human accepts it with
  `memtrace decision accept <id>`. Say that a proposal is pending rather than
  assuming the save took effect. Acceptance and rejection are CLI/TUI actions —
  there is no MCP tool for them, by design.
- **`fact` and `event` are synonyms for `note`**: retrievable, ungoverned, no
  lifecycle. A note cannot be edited into a decision; `memtrace decision
  promote <note-id>` does that, and it is a human action.
- **Forgetting a decision is not a delete.** `memory_forget` maps it onto the
  lifecycle — a proposal is rejected, a binding decision is reverted — and the
  audit record survives either way. Notes keep ordinary delete semantics.
- `topic_key` behaves differently by class: re-saving a **note** under an
  existing key updates it in place; re-saving a **decision** creates a new
  proposed successor that supersedes the current holder once accepted. The two
  namespaces are separate — a note and a decision may hold the same key.
