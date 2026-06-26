---
description: How to invoke the build/registry CLI (the iN/qN work tracker) — exact commands for ready/view/add/split/set-status/set-title/set-desc/dep/answer + make registry. Invoke when adding, inspecting, resolving, re-pointing, or picking work items/questions. The POLICY (when to do these) lives in CLAUDE.md "Tracking work"; this is the command reference.
---

# `build/registry` command reference

The iN/qN registries are **generated**. Source of truth: `registry/items.yaml` +
`registry/questions.yaml`; the `docs/notes/*-registry-*.md` views are output (the
`registry-sync` CI job fails on a hand edit or stale generation). Run `build/registry`
from the repo root — it locates the live registry by walking up for `registry/items.yaml`
and **never** falls back to test fixtures (it errors out if it can't find the live one).

**The decision rules — when to use these, the tip-is-authoritative + missing-edge rules,
the umbrella-vs-leaf dependency rule, IN_PROGRESS / atomic-item / split discipline,
items-vs-PRs — are load-bearing POLICY and live in `CLAUDE.md` ("Tracking work (the
registry)"). This skill is only the *how to invoke* each operation.**

After any mutation: `make registry` (regenerates the views), and `make registry-sync-check`
must pass before committing. Commit the YAML + regenerated `.md` together.

## Pick / inspect

- `build/registry ready` — priority-ordered unblocked pullable items; the **tip is
  authoritative**. `owner:pete` items are auto-included (and listed first) when the
  presence marker `~/.claude/autonomous-loop/pete-present` exists; `--pete-present`
  forces include, `--pete-away` forces exclude (flags override the marker).
- `build/registry in-progress` — all IN_PROGRESS items (must be empty before a merge).
- `build/registry view --id iNN|qNN [--format json]` — the record + computed dependents
  + priority rank. Use this instead of grepping the YAML.
- `build/registry dependents --id iNN` / `build/registry dag` — reverse edges / the DAG.

## Create

- New top-level item: `build/registry add --title "…" --desc "…" --status OPEN --owner agent [--dep iMM]… [--ref …]…`
  (id auto-allocated — no `--id`; `--owner pete` for hardware/Pete-gated work).
- New question: `build/registry add --space questions --owner pete --desc "…"` (id auto-allocated).
- Sub-item: `build/registry split --parent iNN --title "…" [--desc …] [--status …] [--owner …] [--dep …]… [--ref …]…`
  promotes the parent to an umbrella and adds a child whose id is determined from the
  parent (`iNN`→`iNNa`, `iNNx`→`iNNx-b1`). It auto-rewrites the parent's dependents onto
  the children. It WARNS if the parent is DONE (a DONE item becoming a derived-status
  umbrella is unusual).

## Update

- Status: `build/registry set-status --id iNN --status OPEN|IN_PROGRESS|DONE|WONTFIX [--pr <completing-PR>]`.
- Title: `build/registry set-title --id iNN --title "…"` (items only).
- Description / question body: `build/registry set-desc --id iNN|qNN --desc "…"`.
  Use set-title/set-desc — **do not hand-edit `items.yaml`/`questions.yaml`** for these
  (the CLI enforces the ≤200-char title / ≤4096-char desc limits, regenerates the views,
  and runs the dependency guard; a hand-edit bypasses all three).
- Attach a PR: `build/registry set-pr --id iNN --pr N [--role completing|followup]`.

## Dependencies

- `build/registry dep add|rm --id iNN --on iMM|qNN` — the priority order is auto-repaired
  (topological) to keep every item after its dependencies.
- `dep add` / `add` / `split` WARN when the target is a **DONE leaf of an umbrella with
  OPEN siblings** — the i87b trap (depending on the finished part of a multi-part
  deliverable hides its unfinished sibling). Heed it: depend on the umbrella, or the open
  sibling(s), not the done leaf. (Rule: CLAUDE.md "Depend on the umbrella for the whole".)

## Resolve a question

- `build/registry answer --id qNN` — only after curating every dependent item (apply the
  decision: redefine / split / WONTFIX / spawn follow-ups). The delete-gate blocks the
  answer while anything still depends on the question (the no-information-loss guarantee).

## Priority model — how `ready` ordering works

The priority queue (`registry/priority.yaml`) is a **total order** over all pullable
(OPEN/IN_PROGRESS non-umbrella) items. `ready` emits a **filtered subset** of that queue:
(1) only items whose every `depends_on` target is DONE/WONTFIX or absent ("actionable");
(2) by default `owner:pete` items are **excluded** (needs Pete present to execute) — use
`--pete-present` to include them first; (3) `IN_PROGRESS` items are excluded (see
`in-progress`). The topological repair pass auto-adjusts rank so every item appears after
its in-queue dependencies, so `prioritize --to-top` places an item **first among
agent-actionable items only when it has no in-queue prerequisites**; otherwise it is
pulled just after its last prerequisite. After `add`, `prioritize`, and `move`, the CLI
reports the **resulting ready-position + reasons** automatically — trust that output,
never reverse-engineer the source.
