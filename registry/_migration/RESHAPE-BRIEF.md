# Registry reshape brief — finish the IN_PROGRESS split + PR/deps model

Continues the migration on the SAME long-lived branch `i115d-registry-migration`
(draft PR #455). **The branch lives for the entire crossover — DO NOT merge it
part-way.** We split items up as we find them; the deliverable is: *at any moment,
the registry shows at a glance exactly what is done and what is outstanding.*

## Locked model (Pete, 2026-06-20 — supersedes the spec where it conflicts)

1. **`IN_PROGRESS` = the one item being actively worked on THIS branch, now.**
   Rare — usually exactly one (currently `i115d`). NEVER an umbrella; NEVER "started
   long ago, ongoing." Anything not the current branch's active subject is `OPEN`
   or closed (`DONE`/`WONTFIX`).
2. **"Partly done" is not a status — it is the trigger to SPLIT.** Every item =
   ONE deliverable at ONE status. An item bundling done + not-done parts is split
   into dedicated `DONE` item(s) (with their PR(s)) + dedicated `OPEN` item(s), so
   no part's true state is hidden.
3. **Umbrella status is DERIVED**: `OPEN` if any child is open, `DONE` if all
   children are `DONE`/`WONTFIX`. Never `IN_PROGRESS`. If an umbrella's remaining
   work lives in its *description* rather than a dedicated open child, that work
   must become a dedicated `OPEN` child (e.g. i88's "6b").
4. **PRs are a separate first-class column, independent of status.** A list of 0+
   PRs per item, rendered as clickable links `[#N](https://github.com/petemoore/sam-aarch64/pull/N)`.
   Any status carries PRs (IN_PROGRESS shows its in-flight PR; DONE shows its
   delivery PR(s); a PR-less DONE — operational work like i129/i130 — shows none).
   **Dropped invariants:** "exactly one completing PR per DONE leaf" and ">1
   completing PR ⇒ split." The split signal is multiple *deliverables*, not multiple
   PRs.
5. **Dependencies are visible both ways**: a `deps` column (`depends_on`) AND a
   `dependents` column (reverse edges, computed at gen). Plus a CLI to show/query
   the DAG.
6. **There is a curated PRIORITY ORDER — the backlog queue agents pull from.** A
   committed, ordered list of the open *pullable* items (leaves / childless;
   umbrellas excluded), highest-priority first. The id-sorted `.md` are NOT the
   backlog. The order is stored (`registry/priority.yaml` — an ordered id list) and
   validated as a permutation of exactly the open pullable items AND a topological
   extension of the dependency DAG (nothing ranked before a node it depends on). A
   generated **backlog view** (`docs/notes/backlog.md`) lists them in that order;
   `registry ready` returns the *unblocked* tip (deps satisfied) so an agent pulls
   the next real work deterministically. This **supersedes** the prose "proposed
   implementation sequence" in `docs/ROADMAP.md` (single source of truth). Seed the
   order from that ROADMAP sequence + the DAG; Pete curates the ranking.

## The work — drive to completion (branch stays alive throughout)

- **Phase 1 — tool.** validator: PRs are a list (num>0), no required completing PR,
  PR-less DONE allowed, drop the >1-PR-split rule; KEEP DAG/cycle, WONTFIX-coherency,
  dangling-edge, id grammar/sort, bounded fields, umbrella-DONE-coherence. generator:
  a `PR` column of clickable links split out of `status` (status cell becomes just
  the token); add a `dependents` column. Column order:
  `| id | item | status | PR | deps | dependents | refs/links |`. Update the tests.
- **Phase 2 — split every `IN_PROGRESS` item** into dedicated items. The 20 are:
  - 10 umbrellas → derived status (`OPEN`), + carve out any remaining work buried in
    the umbrella description into a dedicated `OPEN` child: i41, i48, i48c, i81, i88,
    i100, i102, i102m, i115, i119.
  - 10 partly-done leaves → split into `DONE`(+PRs) and `OPEN` dedicated items:
    i7, i80, i83, i86, i92, i93, i95, i113, i118, i124.
  - `i115d` stays `IN_PROGRESS`.
  For each, read the migrated record + the relevant PRs (git log) to determine the
  done/not-done boundary. New sub-items use the id grammar (sub-item letters under a
  base, brick `-bN` under a sub-item) and go through the `registry` CLI.
- **Phase 3** — regenerate the `.md`, independent info-loss/correctness re-review,
  strict `registry validate`.
- **Phase 4 — the PRIORITY QUEUE (folded in from i115g; core, not deferred).**
  Build it AFTER the split (so the final open-pullable set is known): add
  `registry/priority.yaml` (ordered id list); validator checks it is a permutation
  of the open pullable items + a topological extension of the DAG; generator emits a
  `backlog.md` view in priority order (+ a rank surfaced in the open view); CLI gains
  `ready` (unblocked tip), `prioritize`/`move` (re-rank), `dag`/`dependents` (show
  the graph). Seed the order from the ROADMAP "proposed sequence" + the DAG; Pete
  curates. The `split` CLI must replace a parent's queue slot with its new leaves.
- **Spec** — update `docs/specs/registry-source-of-truth-design.md` to match (the
  IN_PROGRESS definition, PR-as-separate-dimension, the dependents view); delete the
  "umbrellas are legitimately IN_PROGRESS for weeks" line.

## State when this brief was written
All 211 items + 5 questions migrated; strict validate passes; the 3 `.md` are
generated in `docs/notes/`; a `deps` column already exists. Remaining = this brief.
