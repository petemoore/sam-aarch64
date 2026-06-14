# Plan — registry as structured source (i115)

Execution plan for the design in `docs/specs/registry-source-of-truth-design.md`.
Ephemeral: deleted by the completing PR (`i115e`).

Decisions (locked with Pete; **substantially revised 2026-06-19 — see "Model
revision" below**): enum `OPEN/IN_PROGRESS/DONE/WONTFIX` (**no `BLOCKED`** — it is
derived from the dependency graph); `depends_on` edges (targeting items *or*
questions) are a **core** field; **questions are transient** (markdown body, no
`answer` field, no `answered` status, no closed file — answered → curate dependents
→ delete); sub-ids hyphenated `i48c-b5a` (umbrella + leaves, each leaf one
completing PR); `qN` folded into the same tooling; wall-of-text rows reshaped into
umbrella+leaves; YAML via `gopkg.in/yaml.v3`; `description` bound 600 chars
(provisional, confirmed at the i115f reshape).

Constraints honoured throughout: merge-commit PRs (`gh pr merge --merge
--delete-branch`); the mandatory §3 pre-merge review on every PR; `docs/specs` =
evergreen, `docs/plans` = ephemeral (this file deleted by `i115e`); no dated
filenames; artifacts named by function. **`main` stays green and the existing
hand-edited registry keeps working until the Phase-4 cutover.**

Pattern to mirror at every step: `tools/tables-gen/` + `Makefile` targets
`tables-gen` (build) / `tables` (regen in place) / `tables-sync-check` (drift
gate) + the `sysreg-sync` CI job (`.github/workflows/ci.yml`) +
`tools/tables-gen/regen_survives_test.go` (round-trip proof).

## Model revision (2026-06-19) — dependency DAG, no `BLOCKED`, transient questions

A design session with Pete materially evolved the model (folded into
`docs/specs/registry-source-of-truth-design.md`). The changes — and what they
imply for the phases below:

- **`BLOCKED` is removed from the status enum.** "Blocked" is a *derived* property
  of a `depends_on` edge to an unresolved node. `depends_on` (targeting items *or*
  questions) is a **core** schema field — the dependency DAG is foundational, not
  the later priority-queue phase. New validator invariants: acyclic DAG, every edge
  resolves to an existing node, **no non-WONTFIX item may depend on a WONTFIX node**
  (a coherency error forcing edge cleanup).
- **Questions are transient.** No `answer` field, no `answered` status, no
  `question-registry-closed.md`. Answering a question = curate every dependent item
  (apply the decision: redefine / split / WONTFIX / spawn items / raise follow-up
  questions, drop the edge), then **delete** the question. The delete-gate (a
  question can't be deleted while anything depends on it) is the structural
  no-information-loss guarantee. The decision lives in the item + git history.
- **Generated views drop to three** — `item-registry-{open,closed}.md` +
  `question-registry-open.md` (no closed-questions view).
- **i115f (the audited content reshape) runs FIRST**, before any other i115 part
  (Pete; agreed earlier but never tracked — exactly the ordering-tracking gap this
  whole effort fixes). It is the semantic reshape (atomic rows, BLOCKED→edges,
  history→git, rationale→design docs), independently audited for information loss,
  so the later phases assume clean, validator-passing rows.

**Rework implied:** `i115a` (skeleton, merged #437) and `i115b` (generator, PR #443
on hold) were built against the OLD schema (`BLOCKED` + `blocker`, two question
views). Their `model.go`/`validate.go`/`gen.go` + the question-view generation need
updating to this model; PR #443's parity test is also superseded (it asserted
parity with today's messy `.md`, which we are explicitly *not* preserving). The
phase content below is updated by this revision where it conflicts.

## Phase tracking

The work is registered as: `i115` (umbrella) with leaves `i115a` (Phase 1),
`i115b` (Phase 2), `i115c` (Phase 3), `i115d` (Phase 4), `i115e` (Phase 5), plus
`i115f` (the reshape experiment, run before the bounded-description invariant is
enforced) and `i115g` (priority queue + dependency graph — a later phase building
on the core). PR-0 (this PR) lands the spec + this plan + the tracking rows; it
completes no leaf.

`i115f` (experiment) runs **before** Phase 4: it reshapes today's wall-of-text
rows into atomic single-status items (each independently audited for information
loss) and so validates the bounded-description invariant empirically. Because the
semantic reshape happens here, the Phase-4 importer becomes a **pure
markdown→YAML format conversion** with no reshaping. `i115g` (priority +
dependencies) lands after the core (i115a-e) — it adds the ordered queue, the
`depends_on` DAG, the topological-order validation, and the `ready`/`prioritize`/
`dep` CLI commands; see the spec's "Priority queue + dependency graph" section.

## Phase 1 — tool skeleton: validate + gen-to-buffer + round-trip test (PR `i115a`)

Dormant: operates on a fixture only; no live registry source, no CI gate.

Lands:
- `tools/registry/{go.mod,main.go,README.md}` — module
  `github.com/petemoore/sam-aarch64/tools/registry`; YAML via `gopkg.in/yaml.v3`.
- `tools/registry/model.go` (record structs), `load.go` (parse `registry/*.yaml`),
  `validate.go` (all invariants 1–10), `gen.go` (render to an `io.Writer`).
- `tools/registry/testdata/items.yaml` + `questions.yaml` — hand-written fixtures
  (~10 records: umbrella, DONE leaf, BLOCKED, sub-items, a question).
- `tools/registry/validate_test.go` — each invariant has a red fixture asserting
  the exact error string. `regen_survives_test.go` — `gen` byte-stable +
  `load → gen → load` fixed point.
- `go.work`: add `./tools/registry`. `Makefile`: add `registry-gen` build target
  (build binary to `build/registry`, modelled on `tables-gen:`).
  `STATICCHECK_MODULES`: append `registry`.

Verify: `cd tools/registry && go test ./...`; `make staticcheck` clean;
`build/registry validate tools/registry/testdata/items.yaml` exits 0; a broken
fixture exits 1 with the expected message. No live registry exists → zero risk.

## Phase 2 — generator + banner + header templates, parity proof (PR `i115b`)

Lands:
- `gen.go` finalized: `|`→`\|`, `\n`→`<br>`, status-cell rendering, header-template
  concat, the GENERATED banner (§ design "Generator").
- `registry/templates/item-registry-open.head.md` + 3 siblings — the hand-authored
  header prose lifted verbatim from today's `.md` headers.
- `Makefile`: `registry-sync-check` target (regen to `build/gen/registry/`,
  validate, `diff -u`) — not yet CI-wired, not yet generating in place.
- `tools/registry/gen_parity_test.go` — a hand-translated ~10-row slice of the real
  registry as YAML; assert the generated rows match the current `.md` rows after
  normalizing legacy unescaped `|` to `\|` (proves the generator output is the
  correct, valid-markdown form of today's content).

Verify: `go test ./...`; `make staticcheck`. CI gate not yet active; `main` green.

## Phase 3 — CLI mutators (PR `i115c`)

Lands:
- `tools/registry/cmd_{add,split,setstatus,setpr,nextid}.go`.
- `registry/.id-ledger.txt` seeded from the current id set (no-reuse from cutover).
- Tests: add-then-validate; split sets umbrella + adds leaf; set-status moves the
  half on regen; next-id consults source ∪ ledger.

Still operates on `testdata/` only. Verify: `go test ./...`; manual
`build/registry add … && build/registry validate` round-trips.

## Phase 4 — migration + cutover (PR `i115d`) — the single switchover

The only PR that touches the live registry; atomic, hard-reviewed.

Lands, in order:
1. `tools/registry/import.go` (also `registry import --from docs/notes`): tolerant
   parser splitting on the outermost `|`, reading status from the status column.
2. Reshape `i48c`, `i41`, `i41d`, `i33`, `i111`, `i115` into umbrella+leaves
   (the `split` operation at import). All other rows import verbatim into bounded
   fields, regexing `PR #N`/`#N` from the status cell into a structured completing
   entry.
3. Write `registry/items.yaml` + `registry/questions.yaml` (canonical).
4. `registry validate` must pass — the reshape is correct iff validate is green.
   `migration_roundtrip_test.go`: `import(current md) → gen → import(gen md)` is a
   fixed point.
5. `make registry` now generates the four `.md` in place (banner + escaped). The
   committed `.md` become generated output; the diff is large but mechanical.
6. `Makefile`: finalize `registry` (in-place) + `registry-sync-check` to operate
   `registry/` → `docs/notes/`.
7. CI: add the `registry-sync` job (after `sysreg-sync`, same shape) and to the
   branch-protection required checks.

Verify: `make registry-sync-check` green locally + CI; `make check-doc-links`
green; the generated `.md` render correctly on the GitHub PR preview (pipes
escaped, no broken tables); §3 review with extra scrutiny on hygiene + a manual
open/closed item-count reconciliation (pre vs post, modulo the reshape leaves).

## Phase 5 — doc / automation rewiring (PR `i115e`)

Lands (every reader/writer enumerated). **All `CLAUDE.md` edits target the in-repo
`/home/pmoore/git/sam-aarch64/CLAUDE.md` ONLY — never the global
`~/.claude/CLAUDE.md` or the `~/git/CLAUDE.md` sibling (repo scope-discipline
rule).**
- `CLAUDE.md` (in-repo): §3 item 6 + the doc-lifecycle rules + the open→closed move rule →
  point at `registry/items.yaml` as the source; the `.md` are generated and must
  never be hand-edited (CI `registry-sync` enforces). Add the explicit
  agent-guidance block (below).
- `docs/ROADMAP.md`: handover-contract rules 1–2 + "How to extend this doc" →
  "edit `registry/items.yaml` (or `registry add …`) + `make registry`, both
  committed in the completing PR".
- `docs/notes/README.md`: note the `*-registry-*.md` are generated; source is
  `registry/`.
- `tools/session-handover.sh`: add a read-only guard that runs `registry validate`
  + warns if the generated `.md` is stale vs `registry/` (surfaces drift at session
  start; always exit 0).
- `tools/autonomous-loop/README.md`: reference the YAML source + `make registry`.
- `.claude/settings.local.json`: drop the fragile `awk 'NR==142'` permission; add
  a permission for the `registry` CLI.
- Confirm no code consumer breaks: nothing in `src/`, no Go module, no test reads
  the registry `.md` programmatically (only docs + the hook + autonomous-loop, all
  prose).

Agent-guidance block (verbatim, into `CLAUDE.md`):

> **Tracking work (the registry).** The item/question registries are
> **generated**. The source of truth is `registry/items.yaml` and
> `registry/questions.yaml`; the `docs/notes/*-registry-*.md` files are output and
> must never be hand-edited (the `registry-sync` CI job fails on hand edits or
> stale generation). To track work:
> - **New item:** `build/registry add --id $(build/registry next-id) --title "…"
>   --desc "…" --status OPEN --owner agent [--parent iNN] [--ref …]`, then
>   `make registry`. Commit the YAML and the regenerated `.md` together.
> - **Resolve / move open→closed:** `build/registry set-status --id iNN --status
>   DONE --pr <completing-PR>`, then `make registry` — in the completing PR. Never
>   edit the `.md`.
> - **Multi-PR work:** make the parent an umbrella and add one leaf per
>   deliverable, each with exactly one completing PR. A status cell growing a
>   "Brick 1…N" list is the signal to split — use `registry split`.
> - **Before committing:** `make registry-sync-check` must pass.

Verify: `make registry-sync-check` green; `make check-doc-links` green; the
SessionStart hook prints the new guard without erroring; §3 review confirms
single-source-of-truth (no policy restated in two homes). Delete this plan file in
this PR (the completing PR for the `i115` umbrella).

## Sequencing (revised 2026-06-19)

The model revision reorders the work:

1. **Model lockdown** — the spec + this plan + the tracking revision (the PR
   carrying this revision).
2. **i115f — the audited content reshape, FIRST.** Reshape the live markdown into
   clean atomic rows (umbrellas→leaves, history→git, rationale→cited design docs,
   `BLOCKED`→explicit dependency notation), **independently audited for information
   loss** (implementer ≠ reviewer). This is the semantic cleanup that makes every
   later phase mechanical. (One open sub-question — whether the reshape lands in the
   markdown first or directly in validated YAML at migration — is raised as a `qN`
   with this PR; it does not block building the tool.)
3. **Tool rework to the new model** (supersedes the dormant `i115a`/`i115b` as
   built): schema (no `BLOCKED`, `depends_on`, transient questions), validator (the
   new DAG + WONTFIX-coherency + delete-gate invariants), generator (three views,
   new status rendering), CLI mutators incl. `dep add|rm` and `answer`. Dormant —
   no live source, no CI gate — so `main` stays green.
4. **Migration + cutover** — mechanical import of the i115f-cleaned content into
   YAML, generate the three `.md` in place, wire the `registry-sync` CI job.
5. **Doc / automation rewiring** — point CLAUDE.md / ROADMAP / the SessionStart
   hook / the autonomous-loop docs at the YAML source; the completing PR deletes
   this plan.
6. **i115g (later) — priority queue + `ready`** on top of the now-core dependency
   DAG.

A dormant tool (step 3) or a content reshape (step 2) cannot break the live
generated registry — it doesn't exist until the step-4 cutover — so `main` stays
green throughout.

## Flagged / provisional

- `gopkg.in/yaml.v3` is the repo's first external Go dep outside the existing
  `koron-go/z80` harness dep — standard and well-maintained; noted for the record.
- `description` bound 600 is provisional — confirmed (and nudged if a legitimate
  atomic row genuinely exceeds it) when the importer runs in Phase 4.
- `IN_PROGRESS` as a first-class status is deferred (currently an `OPEN`
  decoration); revisit if wanted.
