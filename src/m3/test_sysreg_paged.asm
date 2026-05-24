; test_sysreg_paged.asm — boot-time self-test for the page-13 sysreg
; lookup matcher reached via paged_call.  BUILD_TESTS only.
;
; Per docs/plans/2026-05-29-m6-closure-release-bytematch.md PR-2, as
; corrected by the PR-2 implementation spec.
;
; Preconditions (set up in assembler.asm before the call):
;   - enctab_trampoline_setup has run -> paged_call body installed.
;   - load_page13_payload has run -> physical page 13 holds the sysreg
;     lookup data (jump table at &8000, tables, self-contained matcher).
;   - load_enctab has NOT yet been called (HMPR observations unperturbed).
;
; These tests drive the matcher directly through its paged_call entry
; points (SYSREG_*_ENTRY in trampoline.asm), staging the operand name
; into the section-B comm buffer (SYSREG_COMM_NAME / SYSREG_COMM_LEN)
; exactly as sysname_stage_to_comm does on the production path, then
; reading SYSREG_COMM_RESULT + the A return.
;
; Tag-space (fail_with_tag values; existing sites use 01,03,10,11,21,
; 40,62,63,73,74,b0): the &75..&7B block is fresh and groups these
; sysreg-paged assertions together.
;
;   tag &75: comm-buffer sentinel clobbered by a paged lookup — either
;            the matcher wrote past its field count or the paged_call
;            stack reached down into the buffer.  (Proves the buffer is
;            safe from the paged_call stack, per the trampoline.asm
;            comm-buffer safety note.)
;   tag &76: "hcr_el2" lookup did not return A=1 (hit).
;   tag &77: "hcr_el2" fields != (3,4,1,1,0).
;   tag &78: "vbar_el1" (table tail) fields != (3,0,12,0,0) or miss.
;   tag &79: "sctlr_el1" (relocation regression) fields != (3,0,1,0,0).
;   tag &7a: miss "bogus_xx" did not return A=0.
;   tag &7b: HMPR not bit-identical across a paged lookup.


; -----------------------------------------------------------------------
; run_sysreg_paged_self_tests — entry point.
; Input:  none.  Output: returns on success; jp fail_with_tag on failure.
; Clobbers: A, BC, DE, HL, IX, IY (paged_call preserves BC/IX/IY but we
;           don't rely on that here).
; -----------------------------------------------------------------------
run_sysreg_paged_self_tests:

; ----- (f) capture HMPR pre-call for the bit-identity check -----------
                in      a, (251)
                ld      (sysreg_test_hmpr_pre), a

; ----- (a) sentinel: poison the whole comm RESULT region, plus the
; tail of NAME, before any lookup.  A sysreg lookup writes only
; RESULT[0..4]; RESULT[7] (and the NAME tail) must remain &A5 after the
; lookup, proving the matcher respects its field count AND that the
; paged_call stack never descended into the buffer (&7E80..&7E98).
                ld      a, &a5
                ld      (SYSREG_COMM_RESULT + 7), a
                ld      (SYSREG_COMM_NAME + 15), a

; ----- (b)/(c) "hcr_el2" → hit, fields (3,4,1,1,0) -------------------
                ld      hl, sysreg_test_name_hcr
                ld      a, 7
                call    sysreg_test_stage
                call    paged_call
                defw    SYSREG_SYSREG_ENTRY
                defb    SYSREG_DATA_PAGE
                cp      1
                jp      nz, sysreg_test_fail_76     ; not a hit

; Sentinel still intact?
                ld      a, (SYSREG_COMM_RESULT + 7)
                cp      &a5
                jp      nz, sysreg_test_fail_75
                ld      a, (SYSREG_COMM_NAME + 15)
                cp      &a5
                jp      nz, sysreg_test_fail_75

; Fields == 3,4,1,1,0 ?
                ld      hl, SYSREG_COMM_RESULT
                ld      de, sysreg_test_expect_hcr
                ld      b, 5
                call    sysreg_test_cmp
                jp      nz, sysreg_test_fail_77

; HMPR bit-identity after the paged lookup (do this once here — the
; matcher round-trips HMPR via the trailer like any paged_call target).
                in      a, (251)
                ld      hl, sysreg_test_hmpr_pre
                cp      (hl)
                jp      nz, sysreg_test_fail_7b

; ----- "vbar_el1" (table tail) → (3,0,12,0,0) ------------------------
                ld      hl, sysreg_test_name_vbar
                ld      a, 8
                call    sysreg_test_stage
                call    paged_call
                defw    SYSREG_SYSREG_ENTRY
                defb    SYSREG_DATA_PAGE
                cp      1
                jp      nz, sysreg_test_fail_78
                ld      hl, SYSREG_COMM_RESULT
                ld      de, sysreg_test_expect_vbar
                ld      b, 5
                call    sysreg_test_cmp
                jp      nz, sysreg_test_fail_78

; ----- "sctlr_el1" (relocation regression) → (3,0,1,0,0) -------------
                ld      hl, sysreg_test_name_sctlr
                ld      a, 9
                call    sysreg_test_stage
                call    paged_call
                defw    SYSREG_SYSREG_ENTRY
                defb    SYSREG_DATA_PAGE
                cp      1
                jp      nz, sysreg_test_fail_79
                ld      hl, SYSREG_COMM_RESULT
                ld      de, sysreg_test_expect_sctlr
                ld      b, 5
                call    sysreg_test_cmp
                jp      nz, sysreg_test_fail_79

; ----- (e) miss "bogus_xx" → A=0 -------------------------------------
                ld      hl, sysreg_test_name_bogus
                ld      a, 8
                call    sysreg_test_stage
                call    paged_call
                defw    SYSREG_SYSREG_ENTRY
                defb    SYSREG_DATA_PAGE
                or      a
                jp      nz, sysreg_test_fail_7a     ; expected miss (A=0)

                ret


; -----------------------------------------------------------------------
; sysreg_test_stage — stage a literal name into the comm buffer, the
; same way sysname_stage_to_comm does on the production path.
; Input:  HL = pointer to name bytes; A = name length (<= 16).
; Output: SYSREG_COMM_NAME[0..n-1] filled; SYSREG_COMM_LEN = n.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
sysreg_test_stage:
                ld      (SYSREG_COMM_LEN), a
                ld      c, a
                ld      b, 0
                ld      de, SYSREG_COMM_NAME
                ldir
                ret


; -----------------------------------------------------------------------
; sysreg_test_cmp — compare B bytes at (HL) against (DE).
; Output: Z=1 if all equal, Z=0 otherwise.  Clobbers A, BC, DE, HL.
; -----------------------------------------------------------------------
sysreg_test_cmp:
                ld      a, (de)
                cp      (hl)
                ret     nz
                inc     hl
                inc     de
                djnz    sysreg_test_cmp
                xor     a               ; Z=1
                ret


; -----------------------------------------------------------------------
; Failure shims.
; -----------------------------------------------------------------------
sysreg_test_fail_75:    ld      a, &75
                        jp      fail_with_tag
sysreg_test_fail_76:    ld      a, &76
                        jp      fail_with_tag
sysreg_test_fail_77:    ld      a, &77
                        jp      fail_with_tag
sysreg_test_fail_78:    ld      a, &78
                        jp      fail_with_tag
sysreg_test_fail_79:    ld      a, &79
                        jp      fail_with_tag
sysreg_test_fail_7a:    ld      a, &7a
                        jp      fail_with_tag
sysreg_test_fail_7b:    ld      a, &7b
                        jp      fail_with_tag


; -----------------------------------------------------------------------
; Test data (section D in the BUILD_TESTS binary tail; default HMPR).
; -----------------------------------------------------------------------
sysreg_test_hmpr_pre:       defb    0

sysreg_test_name_hcr:       defm    "hcr_el2"
sysreg_test_expect_hcr:     defb    3, 4, 1, 1, 0

sysreg_test_name_vbar:      defm    "vbar_el1"
sysreg_test_expect_vbar:    defb    3, 0, 12, 0, 0

sysreg_test_name_sctlr:     defm    "sctlr_el1"
sysreg_test_expect_sctlr:   defb    3, 0, 1, 0, 0

sysreg_test_name_bogus:     defm    "bogus_xx"
