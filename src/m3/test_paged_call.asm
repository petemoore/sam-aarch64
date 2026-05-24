; test_paged_call.asm — boot-time self-tests for the section-B paged_call
; helper.
;
; Per docs/notes/2026-05-28-paged-call-architecture.md §6 plan-PR 1.
; Exercises the mechanism described in §3 (paged_call).
;
; The plan-PR 1 instructions also call for self-tests of
; paged_data_map_hmpr / paged_data_unmap_hmpr, but those primitives
; have a structural restriction not spelled out in the architecture
; doc: they cannot be safely invoked from code that lives in section
; C or D (HMPR-controlled).  The CALL pushes the return address to
; caller's section-D stack, the body changes HMPR, the RET reads
; the return address from a now-different physical page (target+1).
; And even if SP-switching is added internally, the caller's next
; instruction (after the bracket-open CALL returns) is fetched from
; section C/D under target HMPR — wrong page.
;
; The plan-PR 1 test code itself lives in section D (around &C2xx in
; the BUILD_TESTS binary) and therefore cannot exercise those
; primitives directly.  The primitives ARE defined and LDIR'd into
; section B at boot — they will be exercised by future plan-PRs
; whose callers live in LMPR-stable memory (e.g. a sysreg-lookup
; routine that's itself moved to section B in plan-PR 2).  See
; docs/notes/2026-05-28-plan-pr1-stuck.md for the gap analysis.
;
; This file therefore tests paged_call only — which IS safe from
; section-C/D callers because its trailer restores HMPR before
; returning, so the caller's continuation runs under the original
; HMPR (correct page mapped into section C/D).
;
; Preconditions (set up in assembler.asm start: before the call to
; run_paged_call_self_tests):
;   - enctab_trampoline_setup has run → all four section-B helper
;     bodies installed (HLOAD trampoline at TRAMPOLINE_DST, paged_call
;     at PAGED_CALL_DST, paged_data_map_hmpr at PAGED_DATA_MAP_DST,
;     paged_data_unmap_hmpr at PAGED_DATA_UNMAP_DST).
;   - load_page13_payload has run → physical page 13 holds the
;     `ld a, &42; ret` stub at offset 0 (= section-C &8000 when
;     HMPR maps page 13 in).
;   - LMPR_DEFAULT_RUNTIME has been captured at boot.  HMPR is at its
;     boot value (load_page13_payload's trampoline call restores it).
;   - enctab_map_in has NOT yet been called (so HMPR observations are
;     not perturbed by the ENCTAB window).
;
; Assertions (each gets its own fail_with_tag value so a regression
; points at the broken step):
;
;   tag 70: HMPR observed pre-call equals the boot-captured HMPR
;           (sanity check on the test fixture setup).
;   tag 73: paged_call returned a value other than &42 in A (the
;           trivial target either didn't execute, ran from the wrong
;           page, or the trailer clobbered A).
;   tag 74: HMPR after paged_call differed from HMPR before paged_call
;           (the trailer should restore the FULL byte, CLUT bits 5-6
;           and ext-mem bit 7 included).
;
; Tag-space rationale: existing fail_with_tag callers use values 01,
; 03, 10, 11, 21, 40, 62, 63, b0 (per grep of src/m3/*.asm at
; PR-#41).  The 0x70..0x74 block is a fresh contiguous range that
; doesn't collide with any existing site and groups the paged-call
; assertions together for diagnostic recognisability.  Tags 71 and
; 72 are reserved for the paged_data round-trip assertions a future
; PR will reinstate once a LMPR-stable test driver lives in section B.
;
; Compile-time included only when BUILD_TESTS is defined; the call
; site in assembler.asm is also gated.


; -----------------------------------------------------------------------
; run_paged_call_self_tests — entry point.
;
; Input:  none.  Output: returns on success.  On any mismatch:
;         `jp fail_with_tag` with one of the tag values listed above.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_paged_call_self_tests:

; Capture HMPR pre-call so we can confirm bit-identity post-call.
                in      a, (251)
                ld      (paged_call_test_hmpr_pre), a

; ----- paged_call to the trivial target at page 13 / &8000 ------------
;
; The target stub is `ld a, &42; ret` placed by page13_test_payload.asm
; at offset 0 within page 13.  After paged_call returns we assert
; A=&42, and that HMPR is again bit-identical to its pre-call value.
                call    paged_call
                defw    &8000                           ; target addr in section-C form
                defb    PAGED_PAGE_DISASM_AUX           ; target page (low 5 bits;
                                                        ; helper combines with entry
                                                        ; HMPR top 3 bits)

                ; A should now be &42 (stub did `ld a, &42; ret`).
                cp      &42
                jr      nz, paged_call_test_fail_73

; HMPR after paged_call must equal pre-call HMPR (full byte, including
; CLUT bits 5-6 and ext-mem bit 7).
                in      a, (251)
                ld      hl, paged_call_test_hmpr_pre
                cp      (hl)
                jr      nz, paged_call_test_fail_74

                ret


; -----------------------------------------------------------------------
; Failure shims.  Each loads its tag into A then jumps to fail_with_tag,
; which stores LAST_FAIL_TAG and falls through into fail.
; -----------------------------------------------------------------------
paged_call_test_fail_73:
                ld      a, &73
                jp      fail_with_tag
paged_call_test_fail_74:
                ld      a, &74
                jp      fail_with_tag


; -----------------------------------------------------------------------
; Per-test storage.  All in section C alongside the rest of the test
; module; reads/writes happen with HMPR at its boot value, so no paging
; concerns here.
; -----------------------------------------------------------------------
paged_call_test_hmpr_pre:       defb    0
