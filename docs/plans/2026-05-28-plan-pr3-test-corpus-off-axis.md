# plan-PR 3 — port BUILD_TESTS fixture corpus off-axis (brief)

**Status:** brief committed at the head of branch `m6/plan-pr-3-test-corpus-off-axis`. Dispatch from a fresh session as an Agent prompt.

## Goal

Free up section-C+D budget in the **BUILD_TESTS variant** of `assembler.bin` so that future plan-PRs (notably plan-PR 1 of the paging architecture) can land without pushing the test variant past the `&C100` boundary into OPVAL_ARRAY scratch (which today causes silent code-corruption + boot hangs).

Today's baseline: test variant ends at `&C10F` — 15 B past `&C100`. Working only by accident (the 15 B happens to be code that's never executed after OPVAL_ARRAY initialisation clobbers it). PR #50 attempted to add primitives + bodies + scaffolding; pushed to `&C220` (+117 B deeper); test variant boot-hangs. See `docs/notes/2026-05-28-plan-pr1-stuck.md` for the full impl-subagent diagnostic.

**Target:** get the test variant binary back to ending at `&C0E0` or earlier (≥ 32 B of comfortable headroom below `&C100`).

## Scope

Pick BUILD_TESTS-only code that:

- Is included via `if defined(BUILD_TESTS)` in `src/m3/assembler.asm` or its includes.
- Can be relocated to an off-axis page (the same pattern as ENCTAB / OUT / IN — load the .bin at boot into a known physical page; call into it via the existing trampoline or via the now-landing `paged_call` once PR #50 is salvaged).
- Is meaningfully large (≥ 100 B per self-test moved).

Candidates (in approximate priority order):

1. `run_slot_self_tests`, `run_symbol_table_self_tests`, `run_local_label_self_tests` — the early-boot test suites in `src/m3/assembler.asm`'s BUILD_TESTS block. Probably the largest aggregate.
2. `test_*.asm` includes that compile into the test-variant binary's tail (these are the ones currently spilling past `&C100`).
3. The boot-time `print_*` helpers used only by the self-tests.

## Mechanism

**The architecture doc's plan-PR 3 description was the original intent**: use `paged_call` to invoke off-axis self-tests. But `paged_call` itself depends on PR #50 landing. Two paths:

**Path A — don't depend on PR #50.** Use a simpler off-axis pattern just for plan-PR 3: HLOAD the self-test code into a designated test page (e.g. physical page 13 reserved by the brainstorm for "disasm aux"; reuse for tests during boot if no disasm aux exists yet), then call into it via `LMPR_TEST_PAGE` swap (`out (250), a` + `call` + restore). This is a one-off pattern just for test code. Concrete and finite.

**Path B — wait for PR #50 to be salvaged.** Plan-PR 1 (PR #50 salvaged) lands `paged_call`. Plan-PR 3 then uses `paged_call` to invoke off-axis self-tests. Cleaner architecturally but blocks on #50.

**Recommendation: Path A.** Plan-PR 3 lands FIRST. Once #50 has room to fit, the test code can migrate to `paged_call` in a small follow-up.

## Concrete steps

1. Survey `src/m3/assembler.asm`'s BUILD_TESTS block + the `test_*.asm` includes. Measure the byte-size of each self-test suite.
2. Pick ONE substantive suite (target: ≥ 200 B) and migrate it off-axis:
   - Move its code into a new `src/m3/test_*_offaxis.asm` source assembled with `org &8000` to produce a `.bin` file.
   - Extend `tools/build-m3-disk` (or take the cleaner enctab.enc generator route — see PR #50 critical-read in the session handoff doc, point #4) to deposit that file at a known disk location.
   - Add a `load_<test>_off_axis` routine in `loader.asm` that HLOADs the file at boot into a chosen physical page.
   - Replace the inline `call run_*_self_tests` with `LMPR_TEST_PAGE` swap + `call &8000` + restore.
3. Verify dev-container CI: m{3..6} test variants all green; binary size measured + reported in PR body.
4. If ONE suite isn't enough to get test variant ≤ `&C0E0`, migrate a second.

## Hard constraints

- Use `g` not `git`.
- Open the PR ready-for-review (not draft).
- `gh pr merge --merge`; Co-Authored-By trailer.
- Dev container only for SimCoupé runs.
- Don't touch `src/m3/paged_bodies.asm`, `src/m3/test_paged_call.asm`, or the page-13 plumbing from PR #50 — those wait for plan-PR 1's salvage.
- **Don't push code into `&AFFF`-then-`&B000-&BFFF` territory carelessly** — the brainstorm doc treats `&B000-&BFFF` as production-code headroom, not test-code spillover. If you need to spill there for test code only (and BUILD_TESTS-not-set production stays clear), that's OK; document it.

## Discipline reminders

- Don't transpose LMPR/HMPR port numbers. LMPR = 250; HMPR = 251.
- The mode-3 CLUT preservation (HMPR bits 5-7) applies to any HMPR write — see the design doc's §3.3 corrected pseudocode for the mask pattern, and the `feedback_test_variant_fragility.md` memory entry for the cliff context.
- Read `docs/notes/2026-05-28-session-handoff.md` first if you're a fresh session — its "Critical read of PR #50" section has orchestrator-level guidance you'd otherwise re-derive.

## What "done" looks like

- One BUILD_TESTS-only suite migrated off-axis.
- Test variant binary ≤ `&C0E0` (32 B+ headroom below `&C100`).
- Dev-container CI all 8 m-prefixed jobs + build-image + m1 + m2 + test = 11 green.
- PR body includes pre/post sizes for both variants and a sentence on how this unblocks PR #50's salvage.
