# sam-aarch64 — project-local Claude Code instructions

This file overrides Pete's global Claude Code instructions (`~/.claude/CLAUDE.md`) where they conflict, **for this repo only**. The global rules still apply for everything not addressed here.

## Scope discipline (load-bearing)

**Anything Pete says during a sam-aarch64 session is project-scoped.** Never propagate preferences from this repo to `~/.claude/CLAUDE.md` (global) or `~/git/CLAUDE.md` (a sibling-project file that gets picked up via Claude Code's directory walk-up). Pete's work repos have very different workflows, and mixing them would cause real friction in those repos.

If a preference seems "obviously global", **ask** before propagating — the default direction is project-local.

Memory entries written from sam-aarch64 sessions live in `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/` — they are project-scoped by their path. Use that location for any preference shared in this project.

See also: `feedback_project_scoped_preferences` memory entry.

## PR workflow overrides

These override the global workflow for this single-developer private project.

### 1. Open PRs ready-for-review by default, not draft

The global rule says "always create PRs as draft". For this repo, **default to ready-for-review** (`gh pr create` without `--draft`). The draft flag was about external-team signalling, which doesn't apply when nobody else is looking at the repo.

Pete still reviews before merge — the difference is just one fewer click for him each time. Drafts are still fine if the change genuinely isn't ready (e.g. mid-flight, CI not yet green).

See also: `feedback_pr_drafts_not_required` memory entry.

### 2. Land PRs with merge commits, never `--squash` or `--rebase`

Use `gh pr merge --merge --delete-branch`. Squash-merge rewrites commit hashes and destroys the iteration story on a branch; we got bitten by exactly this when PR #43's broken revert went undetected because the squash hid the in-PR commit sequence (the agent did `git revert` of a squash hash and the operation silently became near-no-op).

The repo settings disallow `--squash` and `--rebase` at the GitHub level — only merge commits are accepted. The branch-protection on `main` also requires the 11 CI status checks to pass before merge, and blocks force-push + deletion.

For **building up a branch locally before opening the PR**, squash / amend / rebase are still fine — that's local hygiene, separate from how the PR lands.

See also: `feedback_merge_commits` memory entry; `docs/notes/2026-05-28-test-variant-ci-regression.md` for the PR #43 incident.

### 3. Pre-merge review — mandatory before every `gh pr merge`

Before running `gh pr merge` on any PR, spawn a review subagent with this checklist. The agent must explicitly pass each item or flag it as a blocker. Do not merge if any item is RED.

**What to review** (subagent should read `gh pr diff <N>` as its primary source — the *final* diff, not intermediate commits):

1. **Test-wiring completeness.** For every `src/test_*.asm` file touched or added by the PR:
   - Confirm the file's entry-point function is called from one of the three wiring sites: inline in `src/assembler.asm` (inside `ifdef BUILD_TESTS`), the off-axis page-12 cluster (`src/test_offaxis_cluster.asm`), or the off-axis page-13 wrapper (`src/test_mem_offaxis.asm`).
   - Confirm every symbol the test file references (e.g. `DISASM_ENTRY`, `DISASM_PAGE`, `DISASM_COMM_MNEM`) is defined somewhere in the final build — search `src/trampoline.asm`, `src/assembler.asm`, and any loader files. An undefined symbol is a silent test-never-runs bug.
   - Green: all test files are wired and all symbols are defined. Red: any file unwired or any symbol undefined.

2. **Loader / disk-image wiring.** For every new file that gets HLOAD'd at boot (pages 12-15 etc.), confirm: (a) `loader.asm` has a `load_pageN_payload` function, (b) it is called from `assembler.asm` at boot, (c) the corresponding filename appears in the `build-m3-disk` step of `Makefile` / `tools/build-disk.sh`. Green: all three present. Red: any missing.

3. **PR description accuracy vs final diff.** Read the PR title + body, then compare against the actual diff. The description must describe what `018a8ac`-equivalent "last commit before merge" state looks like — not what any intermediate commit intended. Flag any claim in the description that is NOT present in the final diff as inaccurate. This is the specific failure mode that produced the PR #99 / session #4 inaccuracy (wiring reverted in final commit; handover said "wiring proven"). Green: description matches final diff. Red: any claim in the description is contradicted by the diff.

4. **No new orphaned symbols.** Run `grep -n 'equ\|label\|entry' src/trampoline.asm` and check that any `equ` added by the PR is referenced somewhere in the build. Orphaned equates are harmless but a code-smell early warning. Green: all new equates used. Yellow (non-blocking): unused equates flagged for review.

The subagent returns a one-line PASS/FAIL verdict per item plus a final MERGE / DO NOT MERGE. Treat any RED as a blocker — fix it, push a new commit, re-run CI, then re-run the review before merging.

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

Design rationale + workflow: `docs/notes/2026-05-28-test-harness-bakeoff-evaluation.md`.

## Where plans and specs go (override the superpowers default)

The superpowers skills (`writing-plans`, `brainstorming`) **explicitly instruct** you to save to `docs/superpowers/plans/` and `docs/superpowers/specs/`. **In this repo, do NOT — override that:**

- **Plans → `docs/plans/`** (committed). **Specs / design docs → `docs/specs/`** (committed).
- **Never write to `docs/superpowers/`** — it is excluded by the global `~/.gitignore_global`, so anything you put there is silently dropped from the repo. (PR #18 made `docs/plans` + `docs/specs` canonical; see `memory/feedback_superpowers_docs_gitignored`.)

If you find files in `docs/superpowers/`, they're a stray slip — migrate them to `docs/plans`/`docs/specs` and delete the originals. `tools/session-handover.sh` warns at session start if any appear.

## Pointers for first-session-on-this-repo

- Project overview + roadmap: `docs/ROADMAP.md`.
- Current state + milestone status: latest `docs/notes/m{6,5,4,3}-status.md` files.
- SAM Coupé paging primer: `docs/notes/sam-paging.md`.
- Z80 dev tool: `tools/z80-test-harness-go/` (see "Development inner loop" above).
- Memory index: `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/MEMORY.md` (always auto-loaded).
- Pete's prime directive ("correctness over workarounds"): the first entry in the memory index — read it before anything else.
