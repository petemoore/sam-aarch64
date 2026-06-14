# Migration agent briefing — registry markdown→YAML (item i115d)

You migrate ONE batch of work-item rows from the old markdown registries into the
structured YAML source, then commit it locally. You are the SOLE writer in this
checkout. Do NOT push, do NOT open/merge a PR, do NOT review your own work. The
orchestrator gives you the batch's ids and which `.old` file they live in.

## The mechanic

- Source of truth being built: `registry/items.yaml` (+ `registry/questions.yaml`).
- Worklist (what you consume): scratch copies under `registry/_migration/*.md.old`.
  You DELETE each row from the `.old` copy as you migrate it (same commit), so the
  diff shows exactly what left the worklist and entered the YAML.
- The live `docs/notes/*-registry-*.md` stay UNTOUCHED (do not edit them).
- Binary: `build/registry` (already built; `make registry-gen` rebuilds it).

### Env prefix + `--migrating` on every CLI call (run from repo root)
```
REGISTRY_ITEMS=registry/items.yaml REGISTRY_QUESTIONS=registry/questions.yaml \
REGISTRY_DIR=registry REGISTRY_TEMPLATES=tools/registry/templates \
build/registry add --migrating --id … …  >/dev/null
```
`--migrating` defers ONLY invariant 10 (id-shaped ref existence — refs may point at
not-yet-migrated ids). All other invariants stay strict. Redirect mutator stdout to
/dev/null (it reprints the views each call).

## Per-row field extraction (faithful — NO information loss)

A row is `| **id** | <item cell> | <status cell> | <refs cell> |`.

- **--id**: verbatim.
- **--title**: single-line ≤120 chars — the item's bold lead-in; condense if longer
  and push the remainder into --desc.
- **--desc**: ≤600 chars, ≤6 lines. The durable content (WHAT it is + key caveats).
  RELOCATE, never drop: blow-by-blow PR/iteration history → git holds it; long
  rationale → cite the design-doc path instead of restating. If a fact is durable
  (a decision, a caveat, a verified outcome) and isn't captured by `prs`/a cited
  doc, KEEP it in --desc. If the original cell is a wall-of-text spanning many
  deliverables, it is an UMBRELLA — see "Splitting" below.
- **--status**: OPEN | IN_PROGRESS | DONE | WONTFIX, read from the status cell.
  (Old `BLOCKED:<x>` → status OPEN + a `--dep` edge to the gating item/question; see
  Dependencies.)
- **--pr N** (DONE leaves): the completing PR from the status cell. A multi-PR
  DELIVERY (e.g. "PR1 #121 + PR2 #122 + PR3 #124") is ONE deliverable → pass the
  FINAL PR (`--pr 124`) and cite the sequence in --desc. NEVER attach >1 completing
  PR (invariant 7). `--role followup` is for a post-completion fix only, not delivery
  PRs. Two items sharing one PR is fine (per-record rule). If the cell gives NO PR
  number (resolved by an upstream commit, or completed as a side-effect of a sibling)
  use the closest faithful PR and make the real mechanism clear in --desc; flag it in
  your report.
- **--owner**: `agent` unless the row is clearly Pete-owned (a Pete decision/task) →
  `pete`.
- **--parent iN** (sub-items): set it even if the umbrella isn't migrated yet
  (parent-existence isn't validated; --migrating covers any ref). 
- **--kind umbrella**: for a base item that groups sub-ids (i102/i115/i41/i48/…). An
  umbrella carries NO --pr. Its status is **DERIVED from ALL its children**, which you
  determine by scanning the live `docs/notes/*-registry-*.md` (open + closed) for
  every `iN<letter>`/`iN<letter>-bM` under it — NOT just this batch's rows. If any
  child is OPEN/IN_PROGRESS, the umbrella is **IN_PROGRESS** (it lands in the open
  view), even if the OLD registry filed the base row as closed/DONE. Only when every
  child is DONE/WONTFIX is the umbrella DONE. (A child explicitly reparented elsewhere
  in its row — e.g. "i39c folds into i48c" → parent i48c — does not count as this
  umbrella's child.) The validator rejects a DONE umbrella with a non-DONE child, so
  getting this right now avoids a later-batch validate break.
- **--dep iM|qN** (repeatable): a real dependency. Convert old `BLOCKED:<what>` /
  "blocked on iX" / "gated on qY" prose into an edge. A `BLOCKED:Pete` with no
  existing question stays OPEN with `owner: pete` and no edge (note it) — do NOT mint
  a question unless the orchestrator says to.
- **--ref** (repeatable): **preserve EVERY entry from the original refs/pointer
  cell** — every primary source file, doc (use the EXACT path the original cites;
  do NOT substitute a "related" doc), and sibling/umbrella cross-id. Dropping or
  swapping a ref is information loss the reviewer will flag. File paths / URLs / `§`
  pass through (keep the `§N` suffix on a path as part of that ref string). An
  id-shaped ref MUST be a BARE id (`i52`, not "i52 PR 4" — that fails strict
  validate; move the "PR 4" detail to --desc). The ONE exception: NEVER ref a closed
  question (q-ids in question-registry-closed.md retire and won't exist) — fold its
  decision into --desc instead.

## WONTFIX

Reason goes in --desc with NO "WONTFIX —" prefix (the generator adds that). No --pr.

## Splitting a wall-of-text / multi-deliverable row

If a single row tracks many independently-completable deliverables (the i48c
archetype: "Brick 1…N across PRs #393–#435"), reshape it: `add` the base as
`--kind umbrella` (terse --desc), then `add` one leaf per deliverable
(`--id iN-bM` or the already-named sub-id, `--parent iN`, its own status + --pr).
Mint new hyphenated leaf ids only for genuinely-separate deliverables. The
per-deliverable detail (which PR did what) lives on each leaf + git, NOT in a
wall-of-text cell. The orchestrator flags which rows need splitting and the leaf
breakdown.

## After adding the batch

1. DELETE each migrated row line from its `.old` file (the file shrinks by exactly
   the batch size in data rows; leave header + other rows intact). Verify removal
   with Python `re` (shell `grep '\*\*iN\*\*'` is unreliable — BRE quantifies `\*`).
2. `cd tools/registry && go test ./...` (sanity) then back to repo root.
3. `build/registry validate --migrating registry/items.yaml registry/questions.yaml`
   (with the env prefix) → "validate OK".
4. Confirm every batch id is present in items.yaml (or questions.yaml) and gone from
   `.old`.
5. EYEBALL `build/registry gen --migrating registry/items.yaml registry/questions.yaml`
   (no OUTDIR → stdout; `--migrating` needed so forward refs don't abort gen):
   tables render cleanly, pipes escaped, status cells read "DONE — PR #N" /
   "WONTFIX — <reason>", item cell shows title — description.
6. `g add registry/` (use `g`, not `git`) and commit (NO Co-Authored-By):
   `i115d: registry migration batch <N> — <short note>`. Do NOT push.

## Report back

Confirmation (ids migrated, rows removed, validate OK, gen clean); any inferred/
ambiguous mapping (esp. PR inference, splits, WONTFIX, owner); and a FRICTION REPORT
— if `gen`/`validate` misbehaves, suspect the TOOL (unproven on real data) and flag
it rather than silently working around it.
