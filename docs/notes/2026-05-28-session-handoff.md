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

## Critical read of PR #50 (from the orchestrating session)

The impl subagent's PR #50 output is honest but I (the orchestrating session) disagreed or noticed inaccuracies in a few places. Capturing here so the next agent doesn't have to re-derive these.

### 1. The two architecture-doc spec issues the subagent found ARE both real — but the framing of one is misleading

- **§3.3 trailer SP-save ordering** — real bug in the doc. The subagent's analysis (`mem[caller_SP_at_call - 2..caller_SP_at_call - 1]` vs `mem[SP..SP+1]`) is correct. The HLOAD trampoline gets this right; the design doc's pseudocode didn't mirror that ordering. The subagent fixed it in PR #50's code; the design doc still needs the editorial patch.
- **§4 split-bracket primitives (`paged_data_map_hmpr` / `unmap_hmpr`) — the diagnosis is correct, but the subagent's conclusion ("still useful for future plan-PRs whose callers live in section A or B") is **wrong about the long-term shape**. Pete and I worked through this during the session: the split-bracket pattern is **not prior art** — it was a design-doc invention in §4 that wandered away from the actual COMET/HLOAD/128K precedent. The right answer when salvaging PR #50 is to **DROP the split-bracket primitives entirely** and rely on `paged_call` for all paged-data access (call a target routine in the data page that does the work and returns). That's what the prior art does. Don't keep `paged_data_map_hmpr` / `unmap_hmpr` as "limited-use future primitives" — remove them. The `paged_read_byte` style of self-contained helper (§4.1 of the design doc) is fine as a future addition; the split pattern in §4.2 is the one to delete.

### 2. The subagent shipped MORE than the brief asked for, then commented things out when they didn't fit

- The brief said "code-budget watch: production binary should stay ≤ 12 288 B inside `&8000-&AFFF` (target: leave > 50 B headroom). Test variant should stay ≤ `&C100` (already at `&C10F` on main today — already 15 B over the soft boundary; do NOT push deeper). If your additions push either over, stop and report."
- The subagent pushed prod to `&AFFB` (4 B headroom, 50 B short of target) and pushed test to `&C220` (+117 B deeper than the "do NOT push deeper" rule). It noted these in the PR body but didn't stop and report — it shipped anyway, with the affected self-test commented out.
- When salvaging PR #50, the next agent should: (a) be stricter about the budget, (b) NOT comment things out to make the boot work — instead drop the offending code, OR wait for plan-PR 3 first.

### 3. The stuck doc's "bisection plan" is one hypothesis but probably not the right starting point

The stuck doc suggests bisecting by removing one paged-body source at a time. That's a "shift in the binary's tail bytes" theory.

I think the more likely cause of the test-variant boot hang is **the extended `enctab_trampoline_setup`** — which now LDIRs FOUR helper bodies into section B instead of one. The extension runs in both variants (prod-shared code). Production works, test variant boot-hangs, which is consistent with: the trampoline-setup itself works, but the larger prod-shared binary plus the test-only code together push the boundary differently. The size-budget-cliff explanation is more likely than the bisection-by-body explanation.

Practical recommendation: **don't bisect — just wait for plan-PR 3 to land, then rebase #50 and see what survives**. The bisection-by-body theory in the stuck doc is worth trying if rebasing-after-plan-PR-3 still fails, but it's not the first move.

### 4. The page-13 disk plumbing might want re-thinking

The subagent added a `-p13` flag to `tools/build-m3-disk/main.go` to deposit a separate `.bin` on the disk image, plus a `load_page13_payload` routine in `loader.asm` that HLOADs that file at boot. That works but it's a bespoke per-page mechanism.

A cleaner shape for plan-PR 2 onwards: extend the existing `enctab.enc` pattern (Mac-side `enctab-gen` builds a binary artefact loaded at boot into a known page). The next agent could build `data13.dat` (or similar) via a Mac-side generator that pulls from the Go-side authoritative sources (sysreg DB, mnemonic table, …), instead of a per-page-per-file mechanism. **This isn't urgent for plan-PR 3**, but it's worth keeping in mind as the page-13 (and later page-14, page-15, etc.) plumbing patterns get firmed up.

### 5. The subagent's binary-size table has a small arithmetic slip

`12204 (ends &AFAB)` — actual end is `&8000 + 12204 = &AFCC`, not `&AFAB`. The same kind of error appears in `12284 (ends &AFFB)` — actual `&8000 + 12284 = &B01C`. The off-by-some-small-amount pattern suggests the agent was using slightly wrong arithmetic. Not material to any decision, but the next agent should re-measure binary sizes themselves rather than trust the table in the stuck doc.

### Overall

The impl subagent did honest, careful work and surfaced real issues. Don't read this section as a takedown — read it as orchestrator-level annotation that saves the next agent some re-derivation. PR #50's primitive bodies + boot-time installation + page-13 plumbing are mostly salvageable; the split-bracket primitives should be dropped; the self-test code is fine to keep as long as plan-PR 3 frees the test-variant budget first.
