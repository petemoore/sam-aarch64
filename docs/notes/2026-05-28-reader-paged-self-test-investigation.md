# reader_paged boot self-test — root cause + fix

Date: 2026-05-28
Status: **SUPERSEDED — verification table below was on a stale tree.**

> **2026-05-28 update:** the verification CI table in this document
> ("All CI suites pass with the test re-enabled: ci-m3 9/9 …") was
> generated on a tree that did NOT include the table-relocation work
> from PR #41.  Once PR #41 landed and the SP=&FFFE fix was applied
> on top, test-variant CI started failing across m{3..6} with rc=124
> boot self-test hangs.  PR #43 was authored to revert PR #42 but the
> revert was incomplete — the SP=&FFFE change remained live in main
> until cleanup PR (this commit) restored SP=&C100 and re-disabled
> the call.
>
> The root-cause *analysis* below (stack-vs-code collision at &C100
> when BUILD_TESTS code spills past &C000) may still be accurate; the
> *fix* needs re-evaluation on cleaned main.  A follow-up
> investigation will re-apply SP=&FFFE on cleaned main, run the dev-
> container CI, and either re-land the fix or document why it doesn't
> work in the post-#41 layout.  See
> `docs/notes/2026-05-28-test-variant-ci-regression.md` for the
> regression report.

## Verdict

**Root cause:** The boot-time stack at SP=&C100 grew DOWN into addresses
occupied by the assembler binary's own machine code.  Stack pushes
corrupted the binary's code bytes at &C0F0..&C0FF, and any function
whose body extended into that range executed garbage after the
corruption.

`run_reader_paged_self_tests` (test variant) sits at the very end of
the binary because it's the last `include` in `src/m3/assembler.asm`.
Post-PR-#35 the function lives at &C068..&C111 — overlapping the
stack page from &C068 upward.  As soon as a nested CALL or PUSH
descended past the function's later instructions, those instructions
turned into the return-address/AF bytes of whoever was on the stack.

The earlier guess in `d38cede` was correct in flavour ("285 B spillover
into section D = stack page") but missed the mechanism: it isn't
data getting overwritten, it's the function's own opcodes.

## Why the original PR #37 branch passed but rebased branch failed

| State                              | binary end | run_reader_paged_self_tests at |
|------------------------------------|------------|--------------------------------|
| PR #37 original (cdc6181, no #35)  | &C08B      | &BFCB                          |
| current main + PR #37 (with #35)   | &C16E      | &C068                          |

PR #35 (multi-digit local labels) grew the binary by ~146 bytes,
pushing the test function's start address from &BFCB up to &C068.
On the original branch the function fit just BELOW the stack push
range (&C0F0..&C0FF was uninitialised RAM beyond the binary, so
stack pushes didn't corrupt anything important).  On the rebased
branch the function extends INTO the stack region, so pushes
corrupt the function's own bytes.

This is the SAME family of bug as the HLOAD 16K limit
(`docs/notes/2026-05-28-hload-16k-limit-investigation.md`): both stem
from the stack being in section D, where it can collide with code or
data that also lives in section D.

## Evidence

Inline `print_byte_inline` probes (printer channel) at each step of
`run_reader_paged_self_tests` showed:

  - Step 1 (page-cross helper) PASSES: H=&3F, L=&FE, LMPR=&28 — all
    expected values.  No actual logic bug in `in_normalise_hl` or
    its call site.
  - Step 2 fails partway through.  Adding probes shifts the binary
    layout enough that the failure point moves around — classic
    symptom of "execution diverges into clobbered code".

I also instrumented the binary with a custom helper at &C156+ that
was IMMEDIATELY corrupted by `symbol_table_init` writes to SYMTAB
(&C160+).  This proved the principle: any code placed above &C100
is vulnerable to scratch-data writes; any code at &C000..&C0FF is
vulnerable to stack pushes.

## Fix

Move SP from &C100 to &FFFE so the stack lives in the
&E100..&FFFF "free" region of section D (per the memory map in
`src/m3/assembler.asm` comments).  The stack still grows downward,
just in a region that contains neither code nor scratch data.

```asm
                ld      sp, &FFFE
```

This:
  - Lets `run_reader_paged_self_tests` (and any future test code
    that spills past &C000) execute without being self-corrupted.
  - Doesn't break the trampoline, which already SP-switches to
    &7F00 in section B around RST 8 (PR #39).
  - Doesn't touch any scratch region (OPVAL_ARRAY at &C100,
    SYMTAB at &C160, STAGING_BUF at &D500, LITPOOL_EXPR_BUF at
    &D900 — all below &E100).

Verified by running all CI variants with `run_reader_paged_self_tests`
re-enabled:

| suite        | result    |
|--------------|-----------|
| ci-m3        | 9/9       |
| ci-m4        | 4/4       |
| ci-m5        | 20/20     |
| ci-m6        | 2/2       |
| ci-m3-prod   | 9/9       |
| ci-m4-prod   | 4/4       |
| ci-m5-prod   | 20/20    |
| ci-m6-prod   | 2/2       |

## Notes for follow-up

1. **The memory map comment in assembler.asm needs updating.** Lines
   21-25 still say "stack &C000-&C0FF; scratch &C100-..." — that's
   now stale.  The new map is roughly: scratch &C000-&E0FF; stack
   grows down from &FFFE into the &E100-&FFFE free zone.

2. **Comments in loader.asm + trampoline.asm referencing SP=&C100**
   are also now stale (e.g. `STACK_TOP equ &C100` is informational
   only — not referenced in code — but should probably be updated).
   The trampoline.asm comment block "Why we switch SP" still computes
   "SP = &C100 ... push lands at page 8 offset &00F8 / &00F9" as the
   pre-PR-#39 failure mode; that arithmetic stays as a historical
   citation of WHY the SP-switch was needed, but the actual SP
   value has moved.

3. **Existing latent bug not exposed by long-source fixture.** The
   M6 long-source fixture only exercises the reader's main-pass
   path, where reader_next_kind's stack pushes don't collide with
   the assembler's main-loop code (which is below &C000).  The
   reader_paged boot test caught a DIFFERENT failure mode: stack-vs-
   own-code corruption when the BUILD_TESTS variant's binary spills
   above &C000.  This is a latent bug in the BUILD_TESTS layout,
   not a reader correctness issue.  No change to reader.asm or
   main_loop.asm is needed; the fix is purely in the boot SP setup.

4. **Prior-art commit d38cede** ("disable boot reader self-test pending
   root-cause investigation") explicitly listed the SP-vs-section-D
   spillover as a "plausible suspect".  Confirmed: that WAS the bug.
   The same commit's other suspects (PR #35 local-label test
   interaction, JR-offset shifts) are NOT the cause.

## Files touched (uncommitted)

  - `src/m3/assembler.asm`: SP=&FFFE; re-enable
    `call run_reader_paged_self_tests`.

## Recommended action

  - Ship the SP fix.
  - Update the memory-map comments in assembler.asm and the stale
    SP=&C100 references in loader.asm / trampoline.asm in the same PR.
  - Keep `run_reader_paged_self_tests` enabled.
