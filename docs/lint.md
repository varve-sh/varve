# Corpus Health

```bash
varve lint                    # the report
varve lint --format md        # forwardable
varve lint --raw              # the rows behind every finding
varve lint --aggregate        # content-free summary
```

`varve lint` runs ten structural checks over your own store and prints a
corpus-health score. Every check is deterministic — SQL over your database plus
local git plumbing. **No model runs, and nothing leaves the machine.**

It is also the report every `varve import` run ends with, so a fresh import
tells you what it just gave you.

---

## The ten checks

**Scored** — corpus-intrinsic properties, with their weights:

| | Check | Weight |
|---|---|---|
| L3 | Dead references — commits, files and PRs that no longer exist | .25 |
| L5 | Duplicates | .25 |
| L6 | Contradiction candidates | .20 |
| L7 | Staleness | .20 |
| L10 | Scope hygiene | .10 |

A category that does not apply is marked N/A and the remaining weights are
renormalized to sum to 1, so a store with no git repo is scored on what could
actually be measured rather than penalised for what could not.

**Reported but never scored:**

| | Check | Why unscored |
|---|---|---|
| L1 | Expired but still binding | Adoption fact |
| L2 | Binding decisions with no evidence | Adoption fact — and deliberately includes migration-born rows, which is exactly the population that needs triage |
| L4 | Scopes matching no files | Advisory: a zero-match glob may be deliberate, since scoping files that do not exist yet is allowed |
| L8 | Never-packed decisions | Adoption fact; `n/a` until packing history is 30 days old |
| L9 | Repeatedly violated decisions | Adoption fact; same 30-day rule |

Adoption is excluded from the score deliberately: on a fresh import every one of
those facts is "bad" by construction — everything is proposed, nothing has been
packed, nothing has curated evidence — and scoring them would make every fresh
import read as failure at exactly the moment a stranger is evaluating the tool.

Proposals awaiting review, accepted counts and curated-evidence counts are listed
in the same unscored adoption block.

---

## Reading the score

The score prints `x of n` beside every category and states its method on the
method line. It is **suppressed entirely below ten entries** — a percentage over
four rows is noise.

Two categories are worth understanding before you react to them:

- **L4 (scopes matching no files)** usually means a decision points at paths that
  were moved, deleted, or are ignored by git. A scope that can never match is a
  decision that can never attribute — see [Attribution](attribution.md). It is
  advisory rather than scored, because scoping a file you are about to create is
  legitimate.
- **L6 (contradiction candidates)** currently scores **title collisions only**.
  Decisions that merely share a scope glob are reported as *unscored* review
  candidates, with hub lines for globs shared by four or more decisions. Sharing
  a scope is normal in a healthy corpus; treating it as rot penalised exactly the
  projects doing it right.

The footer states what the checks cannot see: **paraphrase duplicates and
semantic contradictions are not detected**. Structural checks find structural
problems.

---

## `--aggregate`

```bash
varve lint --aggregate > varve-health.json   # read it, then send it if you want to
```

Prints a summary containing **no content from your store**: the score and its
per-category arithmetic, the method disclosures, adoption counts, how many
unscored review candidates exist, and the varve and schema versions.

No ids, no titles, no findings, no file paths, no scope globs — a glob is a path
in your repository.

It exists because whether the score discriminates at all can only be answered
across many corpora, and the alternative was asking people to send a partial dump
of a private decision store. **Nothing is transmitted. There is no endpoint and
no telemetry.** This writes a file you can read in full and then choose to share.

---

## Fixing what it finds

| Finding | Usually fixed by |
|---|---|
| Binding decision with no evidence | `varve decision accept` already passed, so attach context by superseding it, or leave it — it is a listed fact, not an error |
| Dead references | `varve edit <id>` |
| Scope matching no files | `varve edit <id>` to correct the glob |
| Duplicates | `varve decision revert` the weaker one, or `varve rm` for notes |
| Stale notes | `varve list --status stale`, then edit or delete |
| Proposals piling up | `varve decision pending`, then accept or reject |
