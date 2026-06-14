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
                                   docs/notes/question-registry-closed.md
```

An item's `status` field alone decides which generated view it lands in
(`OPEN`/`BLOCKED` → open; `DONE`/`WONTFIX` → closed). A closed item therefore
*cannot* appear in the open view — the leak class is eliminated by construction.

The four `.md` files are **output**: never hand-edited, each carries a "GENERATED
— do not edit" banner, and CI fails if they drift from a fresh regen of the
source.

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
- id: i48c              # required; items ^i[0-9]+([a-z]|-b[0-9]+[a-z]?)?$ ; questions ^q[0-9]+[a-z]?$
  title: "..."          # required; <= 120 chars, single line
  description: |        # block scalar; bounded: <= 600 chars, <= 6 lines
    ...
  status: OPEN          # enum: OPEN | BLOCKED | DONE | WONTFIX
  blocker: ""           # required iff status==BLOCKED; names the prereq/person
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
  status: BLOCKED
  blocker: "design pass + q24 (serialize an invalid/partial editor doc)"
  kind: leaf
  owner: agent
  parent: i41
  refs: ["docs/specs/editor-edit-model-design.md §8", i41a, i48c, q24]
```

## Status enum

`OPEN` · `BLOCKED` · `DONE` · `WONTFIX` (the existing controlled vocabulary).

- Payload lives in structured fields, never packed into the token: `BLOCKED` →
  `blocker:`; `DONE` → the `prs` completing entry; `WONTFIX` → reason in
  `description`.
- Open vs closed half is **computed**: `{OPEN, BLOCKED}` → open view; `{DONE,
  WONTFIX}` → closed view.
- "In progress" is a decoration on `OPEN` (e.g. a 🔨 marker in the generated
  cell), not a distinct status. (A first-class `IN_PROGRESS` can be added later
  if wanted; deferred for now.)

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

### Sub-id grammar

Bricks under a letter sub-item use a hyphenated suffix: `i48c-b1`, `i48c-b1b`, …
`i48c-b5a`. This preserves the brick names already locked into shipped PR titles,
commits, and branches (#393–#435). Id grammar: `^i[0-9]+([a-z]|-b[0-9]+[a-z]?)?$`.

## Invariants (the validator)

`registry validate` enforces, each with a greppable `registry: ERROR <id>: …`
message:

1. **Ids globally unique; never reused** — relative to an append-only
   `registry/.id-ledger.txt` high-water list, so a deleted id cannot be re-minted.
2. **Well-formed ids** — the grammar above (items / questions).
3. **True numeric sort** — ascending `(N, letter, brick)`; sub-items grouped under
   their parent.
4. **Status in the closed enum** with the required payload per status.
5. **Item→PR mapping structured** — `prs` is a list of `{num, role}`.
6. **One completing PR per leaf; umbrellas carry none** (semantics above).
7. **Atomic items** — a non-umbrella item with >1 completing PR is flagged
   ("split into sub-items").
8. **Bounded description/status** — `title` ≤ 120 chars/1 line; `description` ≤
   600 chars/6 lines; `blocker` ≤ 200 chars. Detail belongs in the PR/git log.
   (600 is provisional, confirmed against the corpus at migration.)
9. **Required-fields-per-status** — `DONE` ⇒ closed + (leaf) one completing PR;
   `BLOCKED` ⇒ non-empty `blocker`; `OPEN` ⇒ open + empty `blocker`; `WONTFIX` ⇒
   closed.
10. **Refs well-formed** — every id-shaped ref (`^[iq][0-9]`) exists in the union;
    non-id refs (paths, `§`, URLs) pass through (path-existence is left to the
    existing `check-doc-links` gate, single-source).

## Generator

`registry gen` (and `make registry`) reads the YAML sources and writes the four
`.md` files:

- **Deterministic** — true-numeric sort, no map iteration, identical across
  machines (so the sync-check diff is reliable).
- **Escaping in one place** — every cell value gets `|` → `\|`, newline → `<br>`,
  so a 6-line block scalar renders as one legal table row. This is what makes
  today's broken unescaped-pipe rows become valid markdown.
- **Status cell** rendered from structured fields: `OPEN`, `BLOCKED:<blocker>`,
  `DONE — PR #435`, `WONTFIX — <reason>`.
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
| `gen` | regenerate the four `.md` + re-canonicalize the YAML in place |
| `next-id [--space items\|questions]` | print the next free id (source ∪ ledger) |
| `add --id … --title … --desc … --status … --owner … [--parent …] [--ref …]` | append a canonical record |
| `split --parent iN --child-id iN-bM --title …` | set parent `kind: umbrella`, add a leaf child |
| `set-status --id iN --status … [--pr N] [--blocker …]` | change status (open↔closed move is automatic on regen) |
| `set-pr --id iN --pr N [--role completing\|followup]` | attach a PR ref |

Every mutating subcommand ends by running `validate` then `gen`, leaving the
working tree consistent (source + four regenerated `.md`). This replaces the
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

## Questions (`qN`)

Folded into the same machinery: `registry/questions.yaml` → the two
question-registry `.md` views, same generator/validator/CLI. The marginal cost is
one extra source file; the gain is the same decay-proofing and a single
extractable pattern (cf. i117).

## Migration

A one-shot importer (`registry import`) parses the current four `.md` files with a
tolerant parser that splits on the *outermost* `|` of a row and treats inner `|`
as content (surviving the unescaped-pipe rows), and reads **status from the status
column**, not from which file a row sits in (catching the leaked closed-in-open
rows). The ~6 wall-of-text rows (`i48c`, `i41`, `i41d`, `i33`, `i111`, `i115`) are
**reshaped into umbrella+leaves during import**, so the source passes its own
validator from the first commit; all other rows import verbatim into bounded
fields (lifting `PR #N` from the status cell into a structured completing entry). A
`migration_roundtrip_test.go` asserts `import → gen → import` is a fixed point.

## Rollout discipline

Six serial PRs (a spec/tracking PR-0, then `i115a`–`i115e`). The tool stays
**dormant** — no live source file, no CI gate — through Phases 1–3, so the live
hand-edited registry keeps working and `main` stays green. Phase 4 is the single
atomic cutover (the `.md` files become generated output); Phase 5 rewires the
docs/automation so agents edit the YAML, not the generated `.md`. The phased plan
lives in `docs/plans/registry-structured-source.md` until the completing PR
(`i115e`) deletes it.
