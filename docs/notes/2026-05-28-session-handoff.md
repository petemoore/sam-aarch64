# Session handoff — 2026-05-28

**Status:** end-of-session handoff. The next session should start by reading this doc fully, then proceed.

## Where things stand

### Landed on `main` today

- **PR #45** — Properly reverted PR #42's SP=`&FFFE` change (PR #43's revert was incomplete). Investigation regression doc at `docs/notes/2026-05-28-test-variant-ci-regression.md`.
- **PR #46** — Project-local `CLAUDE.md` (no drafts, merge commits, project-scoped preferences) + plan-doc `--squash`→`--merge` fix.
- **PR #47** — Stale 12085 figure + STAGING_BUF postscript in the bounds-check audit doc.
- **PR #48** — `text2bin -strip-comments` + `make release-stripped-tbn` (88 644 B vs 407 730 B unstripped — fits the 96 KB IN-buffer ceiling).
- **PR #49** — Three design docs: paged-call architecture, memory-layout brainstorm, music-playback research (+ ROADMAP music link).
- Verification fixes applied to the paged-call doc (HMPR bits 5-7 mask in §3.3; `ttbr1_el1` typo fix in §6 PR 2; new §7 risk #8 for cross-file addressing convention).

### Open / parked

- **PR #50** — plan-PR 1 of the paging architecture. **DRAFT, PARKED.** Production CI passes (m{3,4,5,6}-prod all green); test-variant CI fails (m{3,4,5,6} all fail) because the impl pushed test-variant binary +117 B past the `&C100` boundary into OPVAL_ARRAY scratch. Boot hangs deterministically. Two design-doc spec issues found, documented in `docs/notes/2026-05-28-plan-pr1-stuck.md`:
  1. §3.3 `paged_call` trailer SP-save ordering bug (must save BEFORE pop, not after) — fixed in this PR.
  2. §4 split-bracket primitives (`paged_data_map_hmpr` / `unmap_hmpr`) can't be safely called from code in section C/D. Useful only for callers living in section A or B. Documented; the boot self-test for these was dropped.

## The big-picture insight

**The §6 implementation plan in the paged-call architecture doc is mis-ordered.** Plan-PR 1 (primitives + boot self-test) can't fit cleanly into the BUILD_TESTS variant today because the test variant has *negative* headroom past `&C100` (already 15 B over baseline; PR #50 pushed +117 B). **plan-PR 3 (port BUILD_TESTS fixture corpus off-axis) needs to land first** to create breathing room, then plan-PR 1 fits.

Suggested new ordering: **plan-PR 3 → plan-PR 1 → plan-PR 2 → plan-PR 4.**

When this re-ordering lands, **PR #50's work is salvageable** — the primitive bodies in `src/m3/paged_bodies.asm`, the boot-time installation in extended `enctab_trampoline_setup`, the page-13 disk plumbing, and the test self-test code are all useful. The reason they don't WORK is purely the test-variant size budget; once plan-PR 3 creates room, rebasing #50 should land them cleanly.

## What the next session should do, in order

1. **Read this doc fully + the three docs it links to** (paged-call architecture, memory-layout brainstorm, plan-PR 1 stuck doc). Skim the music research only if relevant.
2. **Read the auto-loaded `MEMORY.md` index in full** — every entry under "Standing rules" applies. The new `feedback_destructive_ops_intent.md` and `feedback_test_variant_fragility.md` (see below) are load-bearing for the work ahead.
3. **Dispatch the three test-harness bake-off subagents** per `docs/notes/2026-05-28-test-harness-spike-briefs.md`. Spike A and Spike B run in parallel (each in its own worktree); Spike C dispatches after both A and B complete. They're bounded 4h each (overnight OK).
4. **Independently of the bake-off**: kick off plan-PR 3 work. The scope is "port some BUILD_TESTS-only code off-axis" to free section-C budget. Concrete target: get test-variant binary back to ≤ `&C100`, ideally well under (say ≤ `&C0E0`). The off-axis pattern is the ENCTAB/OUT/IN trampoline shape from M6 strand A.
5. **After plan-PR 3 merges**: salvage PR #50 by rebasing. The primitive bodies + page-13 plumbing + self-test should fit now. Land plan-PR 1 properly with passing test-variant CI.
6. **After plan-PR 1 merges**: plan-PR 2 (sysreg table migration + the 8 missing entries land for free).
7. **After plan-PR 2 merges**: plan-PR 4 (codegen sysreg + mnemonic tables from Go-side authoritative lists).
8. **Throughout**: re-enable `run_reader_paged_self_tests` as soon as the test variant has room (task #12). This is the visible artefact that says the test variant is no longer fragile.

## Open follow-up items (smaller, not blocking)

- **Patch the paged-call architecture doc** with the two spec fixes the impl subagent found (§3.3 SP-save order, §4 split-bracket usability). Small editorial PR. Could land before or after PR #50 is salvaged.
- **Global CLAUDE.md "destructive intent" refinement** — drafted in chat at end of session; Pete may apply manually. Memory entry already captured at `feedback_destructive_ops_intent.md`.
- **Worktree cleanup**: the impl subagent's worktree at `.claude/worktrees/agent-a86931e7afe34b857` can be removed once PR #50 is salvaged or explicitly abandoned. Pete's own worktrees (`survey-large-file-loading`, `upstream-simcoupe-pr`) should be left alone.

## What's NOT happening (deferred)

- **Disassembler PR-1** (branch `strand-b-1-disassembler`, 5 commits) — explicitly paused per Pete's redirect to release-bytematch milestone. Resume after the release-bytematch milestone closes.
- **FAIL40+ coverage-gap closure** — blocked on plan-PR 2 landing.
- **Spectrum4 Z80 CI gate** — blocked on FAIL40+ closure.

## Pointers

- **Authoritative design**: `docs/notes/2026-05-28-paged-call-architecture.md` (with the §3.3 + §6 + §7 fixes already on main as of PR #49).
- **Memory-layout context**: `docs/notes/2026-05-28-memory-layout-brainstorm.md`.
- **Where today's work failed and why**: `docs/notes/2026-05-28-plan-pr1-stuck.md`.
- **What we know about test-variant fragility**: `docs/notes/2026-05-28-test-variant-ci-regression.md` + the postscripts on `docs/notes/2026-05-28-z80-bounds-check-audit.md`.
- **Memory index**: `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/MEMORY.md` (auto-loaded).
- **Project-local Claude Code instructions**: `~/git/sam-aarch64/CLAUDE.md` (no drafts, merge-commits, project-scoped preferences).
