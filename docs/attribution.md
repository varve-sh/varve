# Attribution

Memory that binds an agent is only half the product. The other half is the
record of what happened next: which decisions were packed into a session, which
commits touched their scope, and which of those were later undone.

**What this is not.** Attribution is a traceable record, not a causal one. It
shows the chain on an individual decision and does not estimate what would have
happened without it. "Conformed" means no deterministic violation signal was
detected — not that compliance was verified. The report prints these limits on
itself, every time.

---

## The chain

```
memory_pack packs a decision into a session      →  pack.item
a commit lands touching that decision's scope    →  diff.observed + scope_match
the verdict is recorded                          →  conform | violate
a later commit reverts it                        →  undone
```

Every link is an append-only event. Every number in `varve report` drills back
down to those rows with `--raw`.

Two conditions have to hold for a commit to attribute to a decision, and both
are easy to miss:

1. **The decision must have a scope glob.** An unscoped decision can be packed
   but can never be matched to a diff, so it will never attribute.
2. **The commit must touch a path that scope matches.** Files your repo ignores
   never appear in a diff, so a scope pointing at ignored paths can never match.

---

## The observer

```bash
varve hooks install      # `varve init` does this for you
varve observe            # observe HEAD (this is what the hook runs)
varve observe --commit 9f2c1ab
varve scan               # observe every commit with no observation yet
varve scan --limit 1000
```

The post-commit hook runs `varve observe --quiet` in the background. It **cannot
block a commit, cannot fail one, and prints nothing** — a hook that fails or
prints is a hook a user removes. It exits 0 whatever happens, because losing one
observation is acceptable: `varve scan` picks it up.

`varve scan` is the half that makes the record complete — commits made before
varve was installed, pulled from a teammate, made with the hook bypassed, or
missed because the database was busy. It also runs automatically in the
background at every MCP session start. The command is the manual door.

An existing `post-commit` hook is appended to, never overwritten.

---

## The epoch, and backfill

Varve records an observation epoch when the store is created. Commits older than
that epoch are outside the observer's remit: it was not there, and counting them
would measure the age of your repository rather than the health of the observer.

```bash
varve scan --backfill    # also observe commits older than the store
```

Anything `--backfill` produces is **marked and excluded from every reported
metric**. A verdict about a commit that predates your decisions is archaeology,
not attribution. The report says so rather than quietly rewriting its own
denominator:

```
observer      16 of 16 default-branch commits observed (100%)
              since install (2026-07-30); 89 earlier commits are outside
              the observer's remit — `varve scan --backfill` covers them
```

> The observer-completeness line counts commits reachable from the **default
> branch as the remote defines it** (`origin/HEAD`, falling back to `main` or
> `master`). Local commits you haven't pushed are correctly absent from that
> count — the figure lags your working tree by exactly the unpushed commits.

---

## The report

```bash
varve report                             # last 30 days
varve report --days 7
varve report --format md                 # forwardable
varve report --decision 01KMDX71NT --raw # the raw events behind the numbers
varve report --grace 120                 # attribution grace window, in minutes
varve report coverage                    # the kill-criterion metric
```

```
varve attribution report — myproject — 2026-06-30..2026-07-30
grace window: 60m · revert detection: git trailer only · backfill: excluded

coverage      3 of 5 agent sessions (60%) produced an attributable
              decision→diff event          [n=5 sessions]
              via pack: 3 · via recall: 0
follow-through  4 of 5 attributed changes conformed
              [n=5 distinct changes across 2 decisions]
violations undone  1 — the violating commits were later reverted
              01J8QF7RTB  a3f19c2 reverted by 7b40e11
observer      16 of 16 default-branch commits observed (100%)

per decision:
  ID             title                               packed  matched  attr  conform  violate  undone
  01J9WKQ2M4XZ   Refresh tokens rotate on every…          7        4     3        3        0       0
  01J8QF7RTBNC   Errors are wrapped with %w and…          5        2     2        1        1       1
```

**Honesty controls, applied to every figure:**

- Rates always carry their sample size (`[n=…]`).
- A rate over fewer than five cases is printed as a raw fraction, never a
  percentage — three of four is 75% and it is also three of four; the percentage
  is the part that overstates.
- Reverts are detected from **git trailers only**, so violations are
  under-reported and never fabricated.
- A rebase gives a change a new commit time, so a rebased change can attribute to
  the rebasing session. Counts dedupe by patch-id; attribution does not.
- If a figure cannot be traced to events, it is not rendered.

---

## Coverage

```bash
varve report coverage --days 30 --grace 60
```

Coverage is the share of agent sessions that produced an attributable
decision→diff event, split by how the decision reached the session (`via pack`
versus `via recall`). It is the metric the design is willing to be judged on —
if decisions are packed and nothing downstream ever touches them, that is a
result, and the report is built to show it rather than hide it.

A fresh store reads 0%. So does a store whose decisions are all unscoped — and
the report says which case it is rather than printing a bare zero:

```
follow-through  no scoped decisions — attribution requires scoped,
              accepted decisions (varve decision accept, with a scope)
```

The first is a lack of history; the second is a corpus problem `varve lint`
will name.

---

## When the observer fails

Observer errors go to `.varve/observer.log` and into the report's completeness
line — never into an agent's tool output. A memory tool that starts reporting
git plumbing failures is a tool an agent starts working around.

```bash
varve doctor      # includes "Commit timestamps: all observed commits are attributable"
```
