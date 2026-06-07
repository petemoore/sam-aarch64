; test_paged_call.asm — boot-time self-test for the section-B paged_call
; helper.  BUILD_TESTS only.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md plan-PR 1 of §6.
; Exercises the mechanism described in §3 (paged_call) only — the §4
; split-bracket primitives (paged_data_map_hmpr / unmap) were removed
; during the PR-#50 salvage (see the §4 editorial patch); the
; design's recommended shape for paged-data access from section-C/D
; callers is paged_call to a target routine in the data page that
; does its work and returns.
;
; Preconditions (set up in assembler.asm `start:` before the call to
; run_paged_call_self_tests):
;   - enctab_trampoline_setup has run -> paged_call body installed in
;     section B at PAGED_CALL_DST.
;   - load_page14_payload has run -> physical page 14 holds the
;     `ld a, &42; ret` stub at section-C offset &8000 when HMPR maps
;     page 14 in.
;   - load_enctab has NOT yet been called (so HMPR observations are
;     not perturbed by the ENCTAB load, which also touches HMPR).
;
; Assertions (each gets its own fail_with_tag value so a regression
; points at the broken step):
;
;   tag &73: paged_call returned a value other than &42 in A — the
;            trivial target either didn't execute, ran from the
;            wrong page, or the trailer clobbered A on the way out.
;   tag &74: HMPR observed AFTER paged_call differed from HMPR
;            observed BEFORE — the trailer should restore the FULL
;            byte (CLUT bits 5-6 and ext-mem bit 7 included).
;
; Tag-space rationale: existing fail_with_tag callers use values
; 01, 03, 10, 11, 21, 40, 62, 63, b0 (per grep of src/*.asm).
; The &70..&74 block is a fresh contiguous range that doesn't collide
; with any existing site and groups the paged-call assertions
; together for diagnostic recognisability.


; -----------------------------------------------------------------------
; run_paged_call_self_tests — entry point.  Asserts the section-B
; paged_call helper round-trips correctly against the page-14 stub.
;
; Input:  none.  Output: returns on success.  On any mismatch:
;         `jp fail_with_tag` with one of the tag values listed above.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_paged_call_self_tests:

; Capture HMPR pre-call so we can confirm bit-identity post-call.
                in      a, (251)
                ld      (paged_call_test_hmpr_pre), a

; ----- paged_call to the trivial target at page 14 / &8000 ------------
;
; The target stub is `ld a, &42; ret` placed by
; paged_call_test_payload.asm at offset 0 within page 14 (= section-C
; &8000 when HMPR maps page 14 in).  After paged_call returns we
; assert A=&42, and that HMPR is again bit-identical to its pre-call
; value.
                call    paged_call
                defw    &8000                           ; target addr in section-C form
                defb    PAGED_CALL_TEST_PAGE            ; target page (low 5 bits;
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
; Per-test storage.  In section C (D actually, around &Bxxx in the
; BUILD_TESTS binary tail) alongside the rest of the test module;
; reads/writes happen with HMPR at its boot value, so no paging
; concerns here.
; -----------------------------------------------------------------------
paged_call_test_hmpr_pre:       defb    0
