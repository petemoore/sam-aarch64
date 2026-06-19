# Registry as structured source — design

## Why

The `iN` item registry and `qN` question registry are the project's work-tracking
backbone. They are hand-edited markdown tables (`| id | description | status |
refs |`). Over hundreds of agent edits this format has accumulated structural
corruption that a markdown table cannot defend against:

- **Unescaped `|` inside cells** (`OUT (251)`, bitwise `a|b`, regex alternations)
  — several current rows are not even valid GitHub table rows; `awk -F'|'` sees
  the `i48c` row as 11 columns, not 6.
- **Lexicographic id slips** (`i4, i41, i5` instead of numeric order).
- **Wall-of-text status cells** — the `i48c` row is a single 5,856-character line
  tracking ~16 bricks across PRs #393–#435, exactly the anti-pattern the
  atomic-item rule forbids.
- **Status leakage** — closed items have appeared in the open-items file.

Every agent edit carries a small probability of malformation that *compounds*
toward eventual corruption. A markdown table alone is at its reliability limit.
The fix is to make the registry a **generated artifact** of a validated
structured source, with a **required CI gate** that rejects a malformed edit at
the iteration that introduces it — converting compounding decay into a
self-correcting system. This keeps tracking **git-native** (PR-coupled,
§3-reviewable, history-as-archive, greppable) while making it decay-proof.

This is the exact pattern the repo already runs for code-generated tables
(`tools/tables-gen` → committed `.inc` files → `tables-sync-check` CI gate). The
registry becomes the analogue.

## Model

**One structured source of truth → generated markdown views.**

```
registry/items.yaml      ──gen──▶  docs/notes/item-registry-open.md
                                   docs/notes/item-registry-closed.md
registry/questions.yaml  ──gen──▶  docs/notes/question-registry-open.md
                                   (no closed view — questions are transient)
```

An item's `status` field alone decides which generated view it lands in
(`OPEN`/`IN_PROGRESS` → open; `DONE`/`WONTFIX` → closed). A closed item therefore
*cannot* appear in the open view — the leak class is eliminated by construction.
**There is no stored `BLOCKED` status:** "blocked" is a *derived* property of the
dependency graph — an item with a `depends_on` edge to a node that is not yet
resolved — never a state an agent hand-sets (see "Dependencies"). **Questions are
transient:** a question lives only until it is answered, at which point its answer
is curated into the items that depend on it and the question is *deleted*, so the
question id-space has an open view only (see "Questions — transient by design").

The three generated `.md` files (two item views + the single open question view)
are **output**: never hand-edited, each carries a "GENERATED — do not edit" banner,
and CI fails if they drift from a fresh regen of the source.

## Source format — YAML, one file per id-space

YAML, a list of records, one file per id-space (`items.yaml`, `questions.yaml`),
under a new top-level `registry/` directory. YAML is chosen for git mergeability
(records are separated blocks, so two agents adding different ids touch disjoint
line ranges → no conflict) and for **block scalars**: a bounded multi-line
`description` needs no `|`-escaping in the source — escaping happens only in the
generator, in one place, which is what fixes the unescaped-pipe corruption.

The source file is stored **canonically sorted** (true-numeric order, fixed key
order, two-space indent); the CLI's writer is the single canonical serializer and
`make registry` re-canonicalizes in place, so a mis-sorted hand-edit is corrected
and shows in the diff.

### Schema (per item record)

```yaml
- id: i48c              # required; items ^i[0-9]+[a-z]?(-b[0-9]+[a-z]?)?$ ; questions ^q[0-9]+[a-z]?$
  title: "..."          # required; <= 120 chars, single line
  description: |        # block scalar; bounded: <= 600 chars, <= 6 lines
    ...
  status: OPEN          # enum: OPEN | IN_PROGRESS | DONE | WONTFIX (no BLOCKED — derived)
  depends_on: []        # [ids] this item is gated on (items or questions). "Blocked"
                        # is DERIVED from an unsatisfied edge, never a stored status.
  kind: leaf            # leaf (default) | umbrella
  owner: agent          # required; "agent" | "pete" | a name
  prs: []               # [{num, role}]; a DONE leaf has exactly one role:completing
  parent: ""            # optional; the umbrella id this leaf rolls up to
  refs: []              # cross-links (ids) + pointers (paths, §, URLs)
```

`prs` entries: `{num: 435, role: completing}` or `{num: 480, role: followup}`.
`completing` is the single PR that closed the leaf; `followup` (unlimited) covers
a later fix PR after completion. The validator counts only `completing` for the
one-PR rule.

### Example records

```yaml
- id: i48c
  title: "Z80 text→overlay encoder (editor input path)"
  description: |
    SAM-side mirror of the host front-end (tools/sam-aarch64/frontend); Go i48b
    is the authority. Large multi-PR port, decomposed into bricks as sub-items.
  status: OPEN
  kind: umbrella                 # umbrella => carries NO completing PR of its own
  owner: agent
  refs: [i48, i39c, i109, "src/asmlex.asm", "src/asmparse.asm"]

- id: i48c-b5a                   # a leaf brick; one deliverable, one completing PR
  title: "B5a — movk/movz/movn special-form parse"
  description: "#imm16 [, lsl #N]; hw=N/16 folded into bits[17:16]; range-checked."
  status: DONE
  kind: leaf
  owner: agent
  parent: i48c
  prs: [{num: 435, role: completing}]
  refs: ["src/asmparse.asm", i48c]

- id: i41e
  title: "Editor edit-model — i48-IR record payload + document symbol table"
  description: |
    Replace i41a raw-text line payload with the i48 symbolic IR. Genuinely-open
    design, not a mechanical port.
  status: OPEN                     # NOT "BLOCKED" — it just has an unsatisfied edge
  depends_on: [q24]                # q24 = serialize an invalid/partial editor doc;
                                   # derived-blocked ⇒ excluded from `ready` until q24 resolves
  kind: leaf
  owner: agent
  parent: i41
  refs: ["docs/specs/editor-edit-model-design.md §8", i41a, i48c, q24]
```

## Status enum

`OPEN` · `IN_PROGRESS` · `DONE` · `WONTFIX`. There is **no `BLOCKED` token** —
blocked-ness is a *derived* property of the dependency graph (see "Dependencies"),
never a state an agent hand-sets.

- `IN_PROGRESS` is a **first-class status**, not an unstructured 🔨 decoration —
  so "what is being worked right now" is a real, machine-readable query
  (`registry list --status IN_PROGRESS` / filter the open view by the status
  column) rather than prose buried in a cell. It is especially load-bearing on
  **umbrellas** (an umbrella like `i48c` is legitimately IN_PROGRESS for weeks
  while its bricks land); a leaf flips OPEN → IN_PROGRESS when picked up → DONE
  when its PR merges.
- Payload lives in structured fields, never packed into the token: a gate → a
  `depends_on` edge; `DONE` → the `prs` completing entry; `WONTFIX` → reason in
  `description`.
- Open vs closed half is **computed**: `{OPEN, IN_PROGRESS}` → open view;
  `{DONE, WONTFIX}` → closed view. An item with unsatisfied `depends_on` edges is
  **still `OPEN`** — "blocked" is not a separate state, it is simply being absent
  from the `ready` set until its gates resolve.

## One-PR / umbrella semantics

- **`kind: leaf`** (default) = one deliverable = one completing PR. A `DONE` leaf
  must have exactly one `prs` entry with `role: completing`; `followup` entries
  are unlimited.
- **`kind: umbrella`** = a non-completing grouping. It carries **no** `prs` and
  never completes by itself; it is `DONE` only when all its child leaves are
  `DONE`/`WONTFIX`. The `i48c` umbrella "spans 16 PRs" by holding zero — the 16
  PRs live on its 16 leaf bricks, one each.
- Validator flags: a `DONE` leaf with ≠1 completing PR; a non-umbrella item with
  >1 `completing` PR ("split into sub-items"); an umbrella carrying any `prs`; an
  umbrella `DONE` with a non-DONE/WONTFIX child.
- A single PR number may appear on several records (a refactor closing one item
  and a follow-up on another) — the one-PR constraint is per-record, not global.

### Id grammar, nesting depth, and sort order

Ids are **prescribed to a bounded shape — no arbitrary nesting.** An item id is at
most three components deep:

```
i<N>                       base item            e.g. i115
i<N><letter>               sub-item (level 1)   e.g. i48c
i<N><letter>-b<M>          brick (level 2)      e.g. i48c-b5
i<N><letter>-b<M><letter>  brick part           e.g. i48c-b5a
```

Regex: `^i[0-9]+[a-z]?(-b[0-9]+[a-z]?)?$` (questions: `^q[0-9]+[a-z]?$`, no
bricks). `b` denotes "brick" (the i48c term). The hyphenated brick suffix
preserves the names already locked into shipped PR titles/commits/branches
(#393–#435). **Maximum depth is base → sub-item → brick** (plus an optional
brick-part letter); anything deeper or otherwise shaped — `i236-g4a-c3-6-a17`,
multi-letter segments, nested `-b…-b…` — is **rejected by the validator**. If a
brick ever needs further subdivision, that is a signal to restructure (add sibling
bricks, or promote the brick to its own item with a `ref`), not to nest deeper.

**Sort order** is by a typed key, not string comparison: each id parses into
`(N:int, letter:str, brickN:int, brickLetter:str)`; integer fields compare
numerically, letter fields lexically, absent fields sort first. So `i5 < i41 <
i237`, `i48 < i48a < i48c`, and `i48c-b1 < i48c-b5a < i48c-b9 < i48c-b10` (the
brick number is numeric, so b10 follows b9). This canonical order is **enforced in
CI two ways**: `registry validate` (invariant 3) errors if the source is out of
order, and `registry gen` emits in this order so the `registry-sync` drift diff
fails on any mis-sort.

## Invariants (the validator)

`registry validate` enforces, each with a greppable `registry: ERROR <id>: …`
message:

1. **Ids globally unique; never reused** — relative to an append-only
   `registry/.id-ledger.txt` high-water list, so a deleted id cannot be re-minted.
2. **Well-formed ids** — the grammar above (items / questions).
3. **Canonical typed sort** — records in the typed-key order defined under "Id
   grammar" (integer fields numeric, letter fields lexical); sub-items grouped
   under their parent. Enforced by `validate` AND the `gen`/sync-check drift diff.
4. **Status in the closed enum** with the required payload per status.
5. **Item→PR mapping structured** — `prs` is a list of `{num, role}`.
6. **One completing PR per leaf; umbrellas carry none** (semantics above).
7. **Atomic items** — a non-umbrella item with >1 completing PR is flagged
   ("split into sub-items").
8. **Bounded description** — `title` ≤ 120 chars/1 line; `description` ≤ 600
   chars/6 lines. Detail belongs in the PR/git log. **The bound is PROVISIONAL** —
   validated empirically by the i115f reshape (which turns today's wall-of-text
   rows into atomic items, independently audited for information loss). The
   dependency model removes the main pressure on it: "what it's blocked on" is now
   an *edge*, not status prose, so the cases the earlier draft feared (the
   `BLOCKED:` reasons, the agent's lean on a Pete-question) become `depends_on`
   edges to items/questions. If some nuance still proves genuinely irreducible the
   bound is raised, rather than forcing loss.
9. **Required-fields-per-status** — `DONE` ⇒ closed + (leaf) exactly one completing
   PR; `OPEN`/`IN_PROGRESS` ⇒ open; `WONTFIX` ⇒ closed + reason in `description`.
10. **Refs well-formed** — every id-shaped ref (`^[iq][0-9]`) exists in the union;
    non-id refs (paths, `§`, URLs) pass through (path-existence is left to the
    existing `check-doc-links` gate, single-source).
11. **Dependencies form a DAG** — every `depends_on` target exists (an item or a
    question) and the graph has **no cycles**. A dangling edge or a cycle is an error.
12. **No non-WONTFIX item depends on a WONTFIX node** — a stale gate is a coherency
    error, forcing the agent to drop the now-moot edge, re-point it, or mark the
    dependent `WONTFIX` too (`WONTFIX`→`WONTFIX` is allowed). A `depends_on` a
    `DONE` item is trivially satisfied.
13. **Question delete-gate** — because a question is *deleted* only after every
    dependent has been re-curated to drop its edge (see "Questions"), invariant 11's
    union-existence check doubles as the gate: a question cannot be deleted while any
    item still `depends_on` it (the edge would dangle and fail validation). This is
    the structural guarantee that an answer is propagated into the items before the
    transient question is discarded.

## Dependencies

`depends_on` is a **core** field (not a later add-on): it is how the registry
expresses that an item is gated, *replacing* the deleted `BLOCKED` status. An item
may declare `depends_on: [ids]` naming the items and/or questions it is gated on.

- **Cross-space edges.** A target may be an **item** (the node defining what must
  be *built*) or a **question** (the node defining what must be *decided*).
  "Blocked on Pete" is just `depends_on: [qN]` for an open question Pete owns.
- **Derived blocked-ness.** An item is *blocked* iff it has an edge to an
  unresolved node — never a stored status. An **item** dependency resolves when
  that item is `DONE`. A **question** dependency resolves by the question being
  *answered and deleted* (which removes the edge — see "Questions"); while the
  question exists the edge is unsatisfied.
- **DAG, no cycles** (invariant 11). The graph is acyclic; the priority order
  (below) must be a topological extension of it.
- **Stale gates are errors** (invariant 12). A non-`WONTFIX` item may not depend on
  a `WONTFIX` node; a `DONE` dependency is trivially satisfied. This forces a
  curator to drop/re-point a moot edge rather than leave a dead gate.
- **Split-rewrites-dependents.** When a depended-on `iN` splits, every item with
  `depends_on: [iN]` is rewritten to depend on `iN`'s new leaves (default: **all**
  children — the conservative "the whole thing must finish" reading), which a
  curator narrows to the specific blocking child. The `split` CLI rewrites;
  curation refines.

Because "what gates what" is now structured, an answered question or a completed
item mechanically unblocks its dependents — which is what makes the `ready` query
(below) trustworthy.

## Priority queue + `ready` (i115g — later phase)

On top of the dependency DAG, the open items are a **curated priority queue**, so
an autonomous agent can pull "the next unblocked, highest-priority work"
deterministically and an interactive session can re-rank without breaking
dependency safety. This **supersedes** the prose "proposed implementation
sequence" in `docs/ROADMAP.md` (single source of truth). The *queue* builds on the
core schema + validator + CLI, so it lands after the core migration; the
`depends_on` edges themselves are core (see "Dependencies").

- **Priority order** — a committed ordered list of open **pullable** item ids
  (highest priority first). "Pullable" = a leaf or a childless item; **umbrellas
  are not queue entries** (they are spanned by their leaves). The validator
  enforces the list is a **permutation of exactly the open pullable items** — each
  once, nothing missing/extra/closed, no umbrellas — *and* a **topological
  extension** of the dependency DAG (nothing sequenced before a node it depends on).
- **Splitting in place** — when `iN` splits into leaves, the queue slot `iN` held
  is replaced **in place** by its new leaves (the parent becomes an umbrella and
  leaves the queue). The `split` CLI does this.
- **Ready set** — `registry ready` returns the open pullable items whose
  dependencies are all resolved, in priority order: the answer to "what can be
  worked next." Drives both the autonomous loop's work-pull and an interactive
  "what should I pick up?" suggestion a human session adjusts.

The CLI gains (this phase): `ready` (the unblocked-work query), `prioritize`/`move`
(re-rank), `dep add|rm` (edit edges). Open sub-decision for i115g design time:
order storage (separate `priority.yaml` vs an in-`items.yaml` sequence) and the
split-rewrite default (all-children vs prompt). The earlier open sub-decision
"whether `depends_on` derives blocked-ness vs is advisory" is **resolved** (Pete,
2026-06-19): it derives it, and `BLOCKED` is removed as a status.

## Generator

`registry gen` (and `make registry`) reads the YAML sources and writes the three
generated `.md` files (item open/closed + the single open question view):

- **Deterministic** — true-numeric sort, no map iteration, identical across
  machines (so the sync-check diff is reliable).
- **Escaping in one place** — every cell value gets `|` → `\|`, newline → `<br>`,
  so a 6-line block scalar renders as one legal table row. This is what makes
  today's broken unescaped-pipe rows become valid markdown.
- **Status cell** rendered from structured fields: `OPEN`, `IN_PROGRESS`,
  `DONE — PR #435`, `WONTFIX — <reason>`. A gated item renders `OPEN` with its
  `depends_on` edges shown in the refs/links cell — "blocked" is visible as an edge
  to an unresolved node, not a status token.
- **Header prose stays hand-authored** — the long governance headers (id
  conventions, controlled vocabulary, atomic-item rule) live in committed
  templates `registry/templates/*.head.md`, concatenated above the generated
  table. Only the table is generated.
- **Banner** — each generated file opens with an HTML-comment banner (invisible on
  GitHub, unmissable in an editor), mirroring the `tables-gen` `.inc` convention:

  ```
  <!--
    GENERATED by `registry gen` (tools/registry) — DO NOT EDIT BY HAND.
    Source of truth: registry/items.yaml
    Regenerate:      make registry
    Validated in CI by the `registry-sync` job. Hand edits FAIL CI.
  -->
  ```

## CLI — `tools/registry` (Go)

Module `github.com/petemoore/sam-aarch64/tools/registry`, in `go.work`, headless,
exit-code-clean (0 ok / 1 validation-or-drift / 2 usage). Operates only on
`registry/*.yaml`; never edits the generated `.md`.

| subcommand | action |
|---|---|
| `validate` | run all invariants; print every error; exit 1 on any |
| `gen` | regenerate the three `.md` views + re-canonicalize the YAML in place |
| `next-id [--space items\|questions]` | print the next free id (source ∪ ledger) |
| `add --id … --title … --desc … --status … --owner … [--parent …] [--dep …] [--ref …]` | append a canonical record |
| `split --parent iN --child-id iN-bM --title …` | set parent `kind: umbrella`, add a leaf child; rewrite dependents onto the new leaves |
| `set-status --id iN --status … [--pr N]` | change status (open↔closed move is automatic on regen) |
| `dep add\|rm --id iN --on iM\|qN` | add / remove a `depends_on` edge (validated acyclic, no WONTFIX target) |
| `answer --id qN` | curate dependents off `qN`, then delete it (fails if any item still depends on it) |
| `set-pr --id iN --pr N [--role completing\|followup]` | attach a PR ref |

Every mutating subcommand ends by running `validate` then `gen`, leaving the
working tree consistent (source + the three regenerated `.md`). This replaces the
hand-rolled `awk 'NR==142'` / boundary-marker scripts agents have resorted to.

## CI sync-check + regen

- `make registry` — regen in place (analogue of `make tables`).
- `make registry-sync-check` — build the tool, run `registry validate` (fail on
  schema violation), regen into `build/gen/registry/`, `diff -u` against the
  committed `.md`, fail on drift with a message pointing at `make registry`
  (analogue of `tables-sync-check`).
- CI job `registry-sync` (`.github/workflows/ci.yml`) — pure Go, no container,
  runs `make registry-sync-check`; added to the branch-protection required checks
  (same as `sysreg-sync`).
- A round-trip test (`tools/registry/regen_survives_test.go`) asserts `gen` output
  is byte-stable and `import → gen → import` is a fixed point.

## Questions — transient by design

Questions ride the same machinery (`registry/questions.yaml` → generator /
validator / CLI), but their **lifecycle differs from items**: a question is a
*transient* node that exists only while it is open, and it has **one generated
view** (`question-registry-open.md`) — no closed file.

- **Fields:** `id` (`qN`), a markdown `body` (the question, possibly multi-part),
  and `owner` (usually `pete`). **No `answer` field and no `answered` status** —
  answers are not persisted on the question (see "why transient").
- **A question is "what gates an under-defined item."** An item not yet fully
  defined declares `depends_on: [qN]`. One question may gate several items — a
  multi-part question whose parts touch different items is fine; each item depends
  on the one question.
- **Answering is a curation step, then a delete.** When Pete answers, an agent
  *curates every dependent item* — applying the decision however it lands: redefine
  the item, split it, mark it `WONTFIX`, spawn new items for work the answer
  created, raise follow-up questions for new unknowns (the dependent re-points at
  those), and drop the `depends_on: [qN]` edge. When the **last** dependent edge is
  gone, the question is **deleted**. This is the same triage a human conversation
  produces — just with explicit edges so nothing is missed.
- **Why transient (no persisted answer).** The durable record is the *item* (it now
  accurately reflects what was decided) plus *git history* (the question's life and
  deletion). A stored answer on a long-lived question invites drift — going stale
  and conflicting with a later decision. Folding the answer into the items and
  deleting the carrier means the registry only ever holds *current* decisions.
- **The delete-gate is the no-information-loss guarantee** (invariant 13): a
  question cannot be deleted while any item still `depends_on` it, so the answer is
  *structurally forced* to propagate into the items before the question disappears.
- **Standalone questions** (nothing depends on them — "should we chase X?", not
  gating a specific item) resolve the same way: the answer either **spawns a new
  item** (the work it created) or the question is simply deleted with the rationale
  in git. "Answer → new item" is the same mechanism as "answer → new question."

## Migration

A one-shot importer (`registry import`) parses the current `.md` files with a
tolerant parser that splits on the *outermost* `|` of a row and treats inner `|`
as content (surviving the unescaped-pipe rows), and reads **status from the status
column**, not from which file a row sits in (catching the leaked closed-in-open
rows). Migration applies the model in this doc:

- **`BLOCKED:<what>` cells become `depends_on` edges.** Each `BLOCKED:Pete` /
  `BLOCKED:<prereq>` becomes an `OPEN` item with a `depends_on` edge to the
  question or item naming the gate — minting a `qN` for a Pete-decision gate where
  one doesn't already exist. No row keeps a `BLOCKED` token.
- **The existing `question-registry-closed.md` is retired.** Its rows are *already
  answered*, so their decisions are folded into the relevant items (or already live
  there) and the file is deleted — git history is the archive. Only open questions
  carry forward, into the single open view.
- **Wall-of-text rows are reshaped to atomic items** by the i115f reshape (its own
  step, independently audited for information loss): umbrellas split into leaves,
  PR-by-PR history moves to git, rationale moves to the cited design docs. All
  other rows import verbatim into bounded fields (lifting `PR #N` from the status
  cell into a structured completing entry).

A `migration_roundtrip_test.go` asserts `import → gen → import` is a fixed point.

## Rollout discipline

The tool stays **dormant** — no live source file, no CI gate — through the
tool-building phases, so the live hand-edited registry keeps working and `main`
stays green; a single atomic cutover turns the `.md` files into generated output,
and a final phase rewires the docs/automation so agents edit the YAML, not the
generated `.md`.

**The dependency model in this doc (no `BLOCKED`; `depends_on` edges; transient
questions) is foundational** — part of the core schema + validator, not the later
priority-queue phase — because the i115f reshape turns every `BLOCKED` row into an
edge and must reshape **once** into the final model. **The i115f content reshape
runs before any other i115 part** (Pete, 2026-06-19; agreed earlier but not
previously tracked): it is what lets the later phases assume clean, atomic,
validator-passing rows. The phased plan + the precise sequencing live in
`docs/plans/registry-structured-source.md` until the completing PR deletes it.
