# sam-aarch64 — project-local Claude Code instructions

This file overrides Pete's global Claude Code instructions (`~/.claude/CLAUDE.md`) where they conflict, **for this repo only**. The global rules still apply for everything not addressed here.

## Scope discipline (load-bearing)

**Anything Pete says during a sam-aarch64 session is project-scoped.** Never propagate preferences from this repo to `~/.claude/CLAUDE.md` (global) or `~/git/CLAUDE.md` (a sibling-project file that gets picked up via Claude Code's directory walk-up). Pete's work repos have very different workflows, and mixing them would cause real friction in those repos.

If a preference seems "obviously global", **ask** before propagating — the default direction is project-local.

Memory entries written from sam-aarch64 sessions live in `~/.claude/projects/<this-repo's-path-slug>/memory/` (the slug is derived from the checkout path, so it varies by host — Pete works from both macOS and Linux hosts) — they are project-scoped by their path. Use that location for any preference shared in this project.

See also: `feedback_project_scoped_preferences` memory entry.

## PR workflow overrides

These override the global workflow for this single-developer private project.

### 1. Open PRs ready-for-review by default, not draft

The global rule says "always create PRs as draft". For this repo, **default to ready-for-review** (`gh pr create` without `--draft`). The draft flag was about external-team signalling, which doesn't apply when nobody else is looking at the repo.

Pete still reviews before merge — the difference is just one fewer click for him each time. Drafts are still fine if the change genuinely isn't ready (e.g. mid-flight, CI not yet green).

See also: `feedback_pr_drafts_not_required` memory entry.

### 2. Land PRs with merge commits, never `--squash` or `--rebase`

Use `gh pr merge --merge --delete-branch`. Squash-merge rewrites commit hashes and destroys the iteration story on a branch; we got bitten by exactly this when PR #43's broken revert went undetected because the squash hid the in-PR commit sequence (the agent did `git revert` of a squash hash and the operation silently became near-no-op).

The repo settings disallow `--squash` and `--rebase` at the GitHub level — only merge commits are accepted. The branch-protection on `main` also requires the CI status checks defined in `.github/workflows/ci.yml` to pass before merge, and blocks force-push + deletion.

For **building up a branch locally before opening the PR**, squash / amend / rebase are still fine — that's local hygiene, separate from how the PR lands.

See also: `feedback_merge_commits` memory entry; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-test-variant-ci-regression.md for the PR #43 incident.

### 3. Pre-merge review — mandatory before every `gh pr merge`

Before running `gh pr merge` on any PR, spawn a review subagent with this checklist. The agent must explicitly pass each item or flag it as a blocker. Do not merge if any item is RED.

**What to review** (subagent should read `gh pr diff <N>` as its primary source — the *final* diff, not intermediate commits):

1. **Test-wiring completeness.** For every `src/test_*.asm` file touched or added by the PR:
   - Confirm the file's entry-point function is called from one of the three wiring sites: inline in `src/assembler.asm` (inside `ifdef BUILD_TESTS`), the off-axis page-12 cluster (`src/test_offaxis_cluster.asm`), or the off-axis page-13 wrapper (`src/test_mem_offaxis.asm`).
   - Confirm every symbol the test file references (e.g. `DISASM_ENTRY`, `DISASM_PAGE`, `DISASM_COMM_MNEM`) is defined somewhere in the final build — search `src/trampoline.asm`, `src/assembler.asm`, and any loader files. An undefined symbol is a silent test-never-runs bug.
   - Green: all test files are wired and all symbols are defined. Red: any file unwired or any symbol undefined.

2. **Loader / disk-image wiring.** For every new file that gets HLOAD'd at boot (pages 12-15 etc.), confirm: (a) `loader.asm` has a `load_pageN_payload` function, (b) it is called from `assembler.asm` at boot, (c) the corresponding filename appears in the `build-disk` step of `Makefile` / `tools/build-disk/main.go`. Green: all three present. Red: any missing.

3. **PR description accuracy vs final diff.** Read the PR title + body, then compare against the actual diff. The description must describe what `018a8ac`-equivalent "last commit before merge" state looks like — not what any intermediate commit intended. Flag any claim in the description that is NOT present in the final diff as inaccurate. This is the specific failure mode that produced the PR #99 / session #4 inaccuracy (wiring reverted in final commit; handover said "wiring proven"). Green: description matches final diff. Red: any claim in the description is contradicted by the diff.

4. **No new orphaned symbols.** Run `grep -n 'equ\|label\|entry' src/trampoline.asm` and check that any `equ` added by the PR is referenced somewhere in the build. Orphaned equates are harmless but a code-smell early warning. Green: all new equates used. Yellow (non-blocking): unused equates flagged for review.

5. **No skipped/relaxed tests masking a gap (HOLD trigger).** Scan the PR for newly-added `t.Skip`/`Skipf`/`SkipNow`, build-tag-excluded tests, relaxed/ratcheted assertions, allowed-failure thresholds, `inst_allowed`/`.inst`-free relaxations, or fixtures excluded by name. A *principled* skip is fine (prerequisite-missing like "no GNU toolchain on PATH", pure-data fixtures). But a skip/relaxation that exists because a real disagreement, bug, or unimplemented feature was **worked around rather than fixed** is an automatic **HOLD** — do not merge; either implement the fix or keep the work on a feature branch until the test genuinely passes (the "don't weaken a test to keep `main` green" rule). **Why:** PR #114 (i9, 2026-06-08) merged with two real decoder gaps (ccmp/ccmn, base csinv/csneg) pinned as skipped tests; that should have paused for review, not auto-merged. Green: no gap-masking skips. Red: any skip/relaxation that hides an unfixed gap.

6. **Repo hygiene.** The PR leaves no newly-dead artifacts behind: superseded docs and tooling are deleted, the plan is deleted if the PR completes one, no new dated filenames appear under `docs/`, no new tracking homes exist outside the `iN`/`qN` registries, and any durable new information lands in an existing living doc where one fits. Green: clean. Red: any hygiene debt introduced.

The subagent returns a one-line PASS/FAIL verdict per item plus a final MERGE / DO NOT MERGE. Treat any RED as a blocker — fix it, push a new commit, re-run CI, then re-run the review before merging.

**Record the review natively on the PR.** Submit the verdict as a GitHub review with `gh pr review <n> --comment` (the checklist PASS/FAIL per item + the MERGE/HOLD verdict), with inline comments anchored to specific lines for any findings — so the reasoning persists on the PR for posterity instead of living only in the agent/chat transcript. Use `--comment` (not `--approve`): GitHub blocks an author approving their own PR, and our agents commit as Pete, so `--approve` would error; `--comment` carries the same record. This matches the global "Reviewing PRs on GitHub" rules. (Adopted 2026-06-08.)

**Why this rule:** PR #99 (2026-06-07) had integration wiring reverted in its final commit; the handover doc described intermediate-commit state and claimed "paging mechanics proven end-to-end." A pre-merge review with item #3 would have caught this immediately. Item #1 would have caught the orphaned `test_disasm_paged.asm`.

### 4. Never push or commit directly to `main` — everything lands via a PR

All changes reach `main` through a pull request. **Do not push commits directly to `main`**, even when branch protection would allow it (the owner can bypass — don't). This includes "quick" docs/handover updates: branch, PR, merge.

Merging is fine: once a PR's CI is green and the mandatory pre-merge review (§3) passes, **the agent may run `gh pr merge --merge --delete-branch` itself** — Pete does not need to click merge. The constraint is *no direct pushes to main*, not *who merges*. If a change genuinely shouldn't merge yet (mid-flight, design input wanted), leave the PR open (draft) rather than holding the work on an unmerged local branch with stale `main`.

**Why this rule:** earlier sessions made direct commits to `main` (bypassing branch protection) for handover/state updates; Pete wants every change to go through the PR + CI + review gate, with no exceptions for "small" or "docs-only" changes.

### 5. Don't weaken a test to keep `main` green — use a feature branch until it passes

If incomplete work fails a test, **do not relax the test** (a ratchet, an "allowed-failure" threshold, a skip) so that `main` stays green. That hides real failure behind a moving goalpost. Instead keep the incomplete work on a **feature branch** — across as many agent runs as it takes — with the test asserting its true target (e.g. a plain 100%), and merge only when the test genuinely passes. A long-lived feature branch is the right vehicle for a large incremental port (e.g. the strand-B disassembler): `main` never sees the red test because the branch isn't merged until it's green. Dev-only harness tests that aren't CI gates may stay red on the branch throughout — that's the point.

**Why this rule:** during the strand-B Z80 disassembler port a partial increment was merged to `main` and the per-word oracle test was given a ratchet floor ("allowed failure rate") to keep `main`'s `go test` green. The cleaner shape — caught by Pete — is to develop the whole port on one branch with the test asserting 100% (red until done) and merge once.

### 6. If Go already implements it, the Z80 side is a port, not a design

Most of this project is "implement in Go first (the authority), then port to Z80." When the Go side already implements a behaviour (e.g. PC-relative branch-target rendering via `DecodeAt(pc, …)`), porting it to Z80 is a **mechanical task with a known answer** — read the Go function and mirror it. Don't flag such work as "needs a design decision / Pete's input" unless there is a genuinely *new* choice the Go code doesn't already settle. Manufacturing a blocker where the authority already has the answer just stalls autonomous progress.

## Development inner loop for Z80 changes

When iterating on SAM-side Z80 code (the assembler in `src/`, tests, paging trampoline, etc.), use `tools/z80-test-harness-go/` for fast (~1 ms/fixture) feedback during inner-loop development. See its README for usage. Before pushing, run SimCoupé under Docker locally for a real-hardware confirmation; CI runs the SimCoupé matrix as the gate.

The harness is **not** a CI gate — SimCoupé is the only gate. The harness can crash or mislead without blocking progress; skip it that iteration and use SimCoupé directly. When the harness disagrees with SimCoupé, SimCoupé wins. Agents own the harness code and evolve it as part of normal work — no design review needed for improvements that make it more useful.

Design rationale + workflow: https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-test-harness-bakeoff-evaluation.md.

**Don't use CI as the inner loop.** Pushing and waiting for the SimCoupé CI matrix costs *minutes* per round-trip; the harness runs equivalent checks in *seconds*. An agent doesn't feel the wait, but Pete does — and it's the throughput bottleneck. The harness now covers the **full paged boot path**, not just standalone decode: `tools/z80-test-harness-go/TestBootSelfTestsPass` boots the BUILD_TESTS assembler (paging payloads into pages 12-15) and asserts every boot self-test passes in ~30 ms, with `TestBootSelfTestsFailProbe` as a negative control. So verify locally — `pyz80` + `go test ./...` (oracle/decode + the boot test) — and reserve CI for the **final pre-merge gate**, not per-iteration. If the harness lacks a capability you need (a page not served, a payload not loaded), add it — normal harness evolution (see the `d15`/page-15 gap fixed during the disassembler port).

## Where plans and specs go (override the superpowers default)

The superpowers skills (`writing-plans`, `brainstorming`) **explicitly instruct** you to save to `docs/superpowers/plans/` and `docs/superpowers/specs/`. **In this repo, do NOT — override that:**

- **Plans → `docs/plans/`** (committed). **Specs / design docs → `docs/specs/`** (committed).
- **Never write to `docs/superpowers/`** — it is excluded by the global `~/.gitignore_global`, so anything you put there is silently dropped from the repo. (PR #18 made `docs/plans` + `docs/specs` canonical; see `memory/feedback_superpowers_docs_gitignored`.)

If you find files in `docs/superpowers/`, they're a stray slip — migrate them to `docs/plans`/`docs/specs` and delete the originals. `tools/session-handover.sh` warns at session start if any appear.

### Doc lifecycle rules (spec decision 6, codified in PR 3)

- Plans (`docs/plans/`) are **ephemeral** execution artifacts: committed when execution starts, **deleted in the PR that completes the work** (the completing PR's description links the plan's final blob). A directory with no plan files in it is the healthy steady state.
- `docs/specs/` holds only **living** design docs with **evergreen (undated) filenames**. When a design ships, fold its durable rationale into `docs/ARCHITECTURE.md` or a reference doc and delete the design doc in the same PR.
- Milestone status docs are **deleted at milestone close**, after the registry walk (contract rule 3). Git history is the archive.
- **The `iN`/`qN` registries are generated from `registry/items.yaml` and `registry/questions.yaml`** (the YAML is the source of truth; the `docs/notes/*-registry-*.md` files are output — see "Tracking work" below). **Never hand-edit the generated `.md` files** — the `registry-sync` CI job fails on any hand edit or stale generation. **Status enum is `OPEN`/`IN_PROGRESS`/`DONE`/`WONTFIX`** with no `BLOCKED` (blocked-ness is expressed as a `depends_on` edge, shown in the `deps` column). `IN_PROGRESS` = the one item actively being worked on the current branch. **Atomic items:** one row = one deliverable = one status; a row bundling independently-completable deliverables is split into letter sub-ids (`i81a`/`i81b`…). **Umbrellas carry derived status** (`OPEN` if any child is open, `DONE` if all children are `DONE`/`WONTFIX`; never `IN_PROGRESS`). **Partly-done is the trigger to split** — never accrete a list of finished sub-parts in one row's status.
- **Questions are transient** (`registry/questions.yaml`): answering a question means curating every dependent item (apply the decision: redefine/split/WONTFIX/spawn/raise follow-up), then deleting the question. A question cannot be deleted while anything depends on it — the delete-gate is the no-information-loss guarantee. There is no closed-questions view; the decision lives in the items + git history.
- No `YYYY-MM-DD` filename prefixes anywhere under `docs/` — git history carries the dates. This deliberately overrides the superpowers skills' dated-filename convention.
- Name new artifacts (dirs, scripts, make targets, CI jobs) after their **function**, never after the milestone that introduced them.
- **Single source of truth — don't duplicate operational logic across docs.** State a rule/policy/value in **one** home and *point* to it from elsewhere; never *restate* it. Redundant copies are a latent drift bug — harmless until someone updates one and not the others, at which point the repo holds *conflicting* policy (worse than none); the single reference is also the one you actually find when you go to change it. (Earned 2026-06-18: the autonomous-loop wind-down rule briefly lived in the monitor nudge + the ROADMAP + the startup prompt + the loop README at once — collapsed to one operational source (the nudge) + pointers.)
- Superseded code/tooling is deleted in the PR that supersedes it; if a deletion needs Pete's sign-off, raise a `qN` immediately rather than parking the artifact indefinitely.
- Every top-level directory and every Go module carries a ≤30-line README (*what is this, how does it relate, where is the canonical deep doc* — link, never restate). A PR that creates a new directory ships its README in the same PR.
- **READMEs are about the software, not the project.** No milestone numbers, phase plans, status reports, or history narration in any README — that lives in `docs/ROADMAP.md`, the registries, and git history. The root README is the product pitch: what it is, what it does today, how to try it, what's planned — clean, lean, factual, compelling. (Pete, 2026-06-10.)

**Tracking work (the registry).** The item/question registries are **generated**. The source of truth is `registry/items.yaml` and `registry/questions.yaml`; the `docs/notes/*-registry-*.md` files are output and must never be hand-edited (the `registry-sync` CI job fails on hand edits or stale generation). **Run `build/registry` from the repo root** — it locates the live registry by walking up for `registry/items.yaml`. It **never** falls back to the bundled test fixtures: run somewhere it can't find the live registry (and with no `REGISTRY_ITEMS` set) and it errors out (exit 1) rather than risk an accidental testdata read/write. To track work:
- **Pick what to work next:** `build/registry ready` returns the priority-ordered list of unblocked pullable items — the **tip is authoritative**. Do NOT judgment-pick a lower item or read the markdown views to improvise an ordering. If the tip turns out to be blocked by something *not yet tracked* as a `depends_on` edge, that is a **missing-edge bug** (no `BLOCKED` status — all blocking is an edge): make the blocker a tracked item (`build/registry add …`, `--owner pete` if hardware/Pete-gated), then `build/registry dep add --id <tip> --on <blocker>`. The CLI **auto-repairs the priority order** (topological repair) on every mutation, so re-running `ready` surfaces a genuinely workable tip. Repeat until you get one — never skip the tip.
- **New item:** `build/registry add --id $(build/registry next-id) --title "…" --desc "…" --status OPEN --owner agent [--parent iNN] [--dep iMM]… [--ref …]`, then `make registry`. Commit the YAML and the regenerated `.md` together.
- **Resolve / close an item:** `build/registry set-status --id iNN --status DONE --pr <completing-PR>`, then `make registry` — in the completing PR. Never edit the `.md`.
- **Add/remove a dependency:** `build/registry dep add|rm --id iNN --on iMM`, then `make registry`. The priority order is auto-repaired to keep every item after its dependencies.
- **Multi-PR work:** make the parent an umbrella and add one leaf per deliverable, each with exactly one completing PR. A status cell growing a "Brick 1…N" list is the signal to split — use `registry split`.
- **Answer a question:** `build/registry answer --id qNN`, then `make registry` — after curating every dependent item.
- **Before committing:** `make registry-sync-check` must pass.

## Pointers for first-session-on-this-repo

- System overview (the first read): `docs/ARCHITECTURE.md`.
- Project overview + roadmap: `docs/ROADMAP.md`.
- Current state + milestone status: `docs/notes/m9-status.md` (active milestone). **What to work next: `build/registry ready` (the priority-queue tip).** Browse open work in `docs/notes/item-registry-open.md` / `question-registry-open.md` + `backlog.md` (closed items in `item-registry-closed.md`; questions are transient — deleted once answered, no closed view) — but those are read-only views; selection is `ready`, not a grep.
- SAM Coupé paging primer: `docs/notes/sam-paging.md`.
- Z80 dev tool: `tools/z80-test-harness-go/` (see "Development inner loop" above).
- Memory index: `~/.claude/projects/<this-repo's-path-slug>/memory/MEMORY.md` (always auto-loaded; the slug varies by host).
- Pete's prime directive ("correctness over workarounds"): the first entry in the memory index — read it before anything else.

## Development discipline

Adapted from [obra/superpowers](https://github.com/obra/superpowers) (MIT) — its norms kept as native rules, its skill-invocation choreography deliberately dropped. The full keep/adapt/drop rationale is recorded on the i63 PR.

1. **Spec gate.** Non-trivial work gets a short written spec that Pete approves before implementation starts. Trivial/mechanical changes are exempt — judgment, not ceremony.
2. **Plans are written for a cheaper model.** Exact file paths, exact commands, complete content — no placeholders, no "handle appropriately". If a plan needs its author's intelligence to execute, it isn't done.
3. **Subagent discipline.** Fresh subagent per task with curated context (never the session's history); implementer ≠ reviewer; review between tasks. **Never two writers in one checkout**: mutating subagents either serialize or get harness-native isolation (the Agent tool's `isolation: "worktree"`); read-only agents may parallelize freely. The §3 pre-merge gate is the quality stage — no PR merges without it, regardless of who or what wrote the code. **Merge authority stays with the orchestrator: an implementer subagent opens its PR and *stops* — it never merges or reviews its own work.** A prose "do not merge" in a subagent prompt is a *request, not a gate* (PR #415, 2026-06-18: a worktree implementer self-merged *and* self-reviewed despite exactly that instruction); the enforcement is **structural** — delegate so the subagent has nothing left to merge, and the orchestrator runs §3 + the merge itself. (See `feedback_implementer_subagent_self_merge_risk`.)
4. **Receiving review feedback: verify, then implement — or push back.** Review comments are claims, not commands: check each against the code before acting, implement what's right, and disagree with evidence when it's wrong. No performative agreement.
5. **Worktrees.** Feature isolation uses sibling-dir worktrees (`~/git/<repo>-<purpose>`), never nested under a checkout — `git clean -fdx` cannot cross a worktree boundary, so sibling placement is the mechanical guarantee, not just convention. `main` stays checked out in the primary; branch from `origin/main` in worktrees (and expect `gh pr merge --delete-branch` to need the post-merge checkout done in the primary). Isolation between *concurrent controlling agents* is environment-level — launch additional controllers via a worktree-preparing alias rather than sharing a checkout.
6. **Reconcile before correcting.** Before acting on a plan formed earlier — especially after an interruption, a revival, or anything that looks wrong — re-read the live state first: PR timelines, who acted, on what authority. Corrective action against a state you haven't reconciled is how protective instincts cause damage. (Earned: the PR #174 stale-context incident, 2026-06-11 — an orphaned reviewer woke after its parent's merge had been legitimately delegated and approved, and tried to revert it.)
7. **Emulation-first, and the emulator is the contract.** Every code path — **boot wrappers and integration paths, not just leaf routines** — runs in **emulation before hardware**. If a path can't run in the emulator, that is a **defect in the emulator to fix, never a licence to bypass**: model the missing hardware (ROM vs RAM, ROM1 mapped at `&C000` at boot, paging, EEPROM, the Trinity/SD/ENC seams) so it runs. "The emulator wouldn't have caught it" is a **bug report against the emulator, not an explanation**; **inadequate emulation is as bad as none** — a flat-RAM model that loads code into what is really boot-time ROM manufactures false confidence. Corollaries: **one** emulation layer that captures every integration point, used by **every** test (not a fast flat harness living beside the full one); and **no `ifndef …HOSTTEST`-style carve-outs** that exclude a path from emulation and ship it to hardware (that is the bypass in disguise). **Why:** the i82 netboot `client_main` was `NETBOOT_HOSTTEST`-excluded and fell through *every* existing emulator (the flat netboot harness, the full assembler harness, SimCoupé-without-Trinity) → it went straight to hardware → silent failure + a long SD-card-shuffling debug detour for bugs a faithful emulator catches at build time (2026-06-18; items i124/i125/i126; `feedback_emulation_first`).
