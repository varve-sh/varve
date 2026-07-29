# memtrace

This project has the **memtrace MCP server** connected. Use its tools for **all** memory operations — never use built-in memory tools or write to ~/.claude/projects/.

## Memory tools

- `memory_pack` — everything binding on a set of files, inside a token budget. **The first call of a task.**
- `memory_recall` — search memories by natural language query (exploration, mid-task)
- `memory_save` — save a decision, convention or note (`fact`/`event` are accepted as synonyms for `note`)
- `memory_get` — fetch full content of a memory by ID
- `memory_update` — patch an existing note by ID (content, tags, confidence). It cannot change a memory's class: a note cannot become a decision, and an accepted decision's content is immutable.
- `memory_forget` — delete or archive a memory by ID or query
- `memory_context` — get memories relevant to specific file paths
- `memory_prompt` — capture the user's goal at the start of a session

## Rules (always follow)

- **Before touching files** — call `memory_pack` with the paths you are about to read or edit and a one-line task. It returns what binds those files, deduplicated, within a budget. Pack first; recall is for questions that come up afterwards.
- **Before every task** — call `memory_recall` with a relevant query, no exceptions. This includes commits, quick fixes, and one-liners.
- **Before committing** — call `memory_recall` to check for commit conventions.
- **Learn something new** — call `memory_save` to persist it.
- **User says forget/delete/remove** — call `memory_forget`.
- **Never** write memory files manually or use built-in memory features.

## What actually happens when you call these tools

- **`memory_save`** with `type=decision` or `convention` creates a row with
  status **`proposed`**. It does not bind, and `memory_context` will not return
  it as context. A human runs `memtrace decision accept <id>` to make it
  binding. Say the proposal is waiting; do not report it as adopted.
- **`memory_save`** with `type=fact` or `event` creates a **note** — `fact` and
  `event` are synonyms for `note`: retrievable, ungoverned, no lifecycle.
  `memory_update` cannot turn a note into a decision; `memtrace decision
  promote <note-id>` does, and it is a human action.
- **`memory_forget`** on a note deletes it. On a decision or convention it
  deletes nothing and changes no status: it records a **disposal request** and
  returns it as pending. A human confirms with `memtrace decision reject <id>`
  while the decision is proposed, or `memtrace decision revert <id>` once it is
  binding. Tell the user the request is waiting for them.
- **`memory_recall`** and **`memory_get`** return proposals, marked
  `PROPOSED`. They are the review surface — treat anything so marked as a
  pending proposal, not as law.
- **`memory_context`** never returns a proposal as content; it reports them as
  a trailing count with their ids. Everything else it returns is binding or
  ungoverned.
- **`memory_pack`** is budget-governed. It serves the top-ranked binding
  decisions in full, elides bodies (`[body elided — memory_get <id>]`) when the
  budget runs short, and names everything omitted in the footer with its rank
  and cost. Nothing is dropped silently: if it is not in the body it is in the
  footer. Proposals appear only as the footer count.
- **`topic_key`** behaves differently by class: re-saving a **note** under an
  existing key updates it in place; re-saving a **decision** creates a new
  proposed successor that supersedes the current holder once accepted. The two
  namespaces are separate — a note and a decision may hold the same key.
