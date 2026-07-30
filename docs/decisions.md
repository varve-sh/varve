# Decision Lifecycle

A decision is not a row you edit. It is a governed object with states,
transitions, evidence and an append-only history — because the point of memory
that binds an agent is that you can tell what bound it, and when, and why.

```
proposed ──accept──▶ active ──────────▶ violated ─────┐
    │                   │                             │
    │                   ╰──────────────┬──────────────┴──▶ superseded
    │                                  ╰─────────────────▶ reverted
    ╰──reject──▶ rejected
```

---

## Why proposals exist

Decisions saved by an **agent**, an **importer**, or the **v1 migration** land
`proposed`. A proposal is captured but inert: it does not bind, it is never
packed as context, and `memory_context` will not volunteer it.

A human accepts it. That is the entire quarantine, and it is deliberate — an
agent asserting "the user approved this" is exactly the assertion the quarantine
exists to distrust. There is no MCP tool for acceptance, rejection or repeal, by
design.

A decision **you** save from the CLI is confirmed on the spot: you are the
confirmation.

Which surface you use decides whether you see proposals:

| Tool | Proposals |
|---|---|
| `memory_recall`, `memory_get` | returned, marked `PROPOSED (not accepted by a human; does not bind)` — this is the review surface |
| `memory_context` | never as content; a trailing count with their ids |
| `memory_pack` | never in the body; a footer count |

---

## The queue

```bash
varve decision pending          # everything awaiting human confirmation
varve decision pending --limit 100
```

The queue holds proposals *and* the disposal requests agents have filed against
binding decisions.

---

## Accepting

```bash
varve decision accept 01KMDX71NT --evidence commit:9f2c1ab
varve decision accept 01KMDX71NT --evidence pr:87 --evidence test:auth_test.go
varve decision accept 01KMDX71NT --force
```

Acceptance requires **at least one evidence row** unless `--force` is passed,
and a forced acceptance is recorded as `"forced": true` on the
`decision.accepted` event. Evidence is `kind:ref`, where kind is one of
`commit`, `pr`, `test`, `file`, `url`, `import`.

The requirement is not bureaucracy: a decision with no evidence is a claim, and
`varve lint` counts those separately for exactly that reason.

---

## Rejecting and repealing

```bash
varve decision reject 01KMDX71NT --reason "duplicate of 01KMHF0TTQ"
varve decision revert 01KMDX71NT
```

**Reject** disposes of a *proposal*. **Revert** repeals a *binding* decision.
Both are terminal and neither is a delete — the audit record survives, because
"we considered X and said no" is precisely what a later session needs.

Re-adopting a repealed rule later means a **new decision citing this one**, never
a resurrection. The history has to keep meaning what it said.

---

## Superseding

An accepted decision's content is immutable. To change what a rule says, save a
new decision under the same `topic_key`:

```
memory_save(
  content:   "We use Postgres 17 with pgvector — upgraded 2026-07",
  type:      "decision",
  topic_key: "decision/database"
)
```

That creates a *new proposed successor*. When a human accepts it, it supersedes
the current holder and the old decision moves to `superseded` — still readable,
still attributable, no longer binding.

`memory_update` cannot do this. It patches notes and metadata; it cannot change
a memory's class and cannot rewrite an accepted decision's content.

---

## Violated

`violated` is not a repeal. It means the rule still binds and the codebase
currently contradicts it in *n* unresolved places, as recorded by the commit
observer. `memory_pack` flags it inline:

```
[2] DECISION 01J8Q… · VIOLATED (2 unresolved) · conf 0.88 · scope: internal/auth/session.go
```

A violated decision is still packed, still binding, and still in the report. It
returns to `active` when the violations are resolved.

---

## Promotion — note to decision

"I wrote this as a note and now realise it is a decision":

```bash
varve decision promote 01KMDX71NT
varve decision promote 01KMDX71NT --kind convention --scope 'internal/auth/**' --title "..."
```

Promotion creates a *proposed* decision through the ordinary lifecycle, so it is
born with provenance and a quarantine rather than by retyping a column. Defaults:
kind `decision`, scope taken from the note's file paths verbatim, title from the
note's summary.

The note stays live while the promotion is pending and is archived when the
decision is accepted; rejecting the promotion leaves the note untouched. CLI and
TUI only.

---

## What an agent can and cannot do

An agent **cannot** dispose of a decision. `memory_forget` over MCP records a
`decision.disposal_requested` event and transitions nothing — "the user wanted
this thrown away" is exactly as untrustworthy as "the user approved".

The request appears in `varve decision pending`. You confirm it with
`decision reject` (while proposed) or `decision revert` (once binding), or you
ignore it. Notes are ungoverned and are still deleted outright on any channel.

---

## Purge — the one irreversible verb

```bash
varve decision purge 01KMDX71NT --reason secret
varve decision purge 01KMDX71NT --yes        # scripts; still irreversible
```

`rm`, `reject` and `revert` all keep the decision as an audit record. **Purge
destroys its content.** It exists for one real case — a secret pasted into a
decision body — and it asks you to type the id back before it runs.

- A decision **with history** is *redacted in place*: content cleared, terminal
  state, row surviving as a `[purged]` tombstone, because its events are
  append-only and its id is referenced by the attribution trail.
- A decision **with no history at all** (carried over by `migrate --from-v1` and
  untouched since) is deleted outright, leaving a tombstone event.

Purge cannot reach the v1 backup, the migration export, or any copy outside the
store. It prints those paths and expects you to handle them. There is no MCP
equivalent, by design.

---

## Reading the history

```bash
varve list --status proposed      # or active, violated, superseded, reverted, rejected
varve report --decision 01KMDX71NT --raw
```

Every transition is an event. `--raw` prints the rows themselves — the invariant
behind every number varve renders is that it drills down to events you can read.
