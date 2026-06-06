; test_disasm_paged.asm — boot-time self-test for the page-15 disasm
; stub reached via paged_call.  BUILD_TESTS only.
;
; Per strand-B PR-3 (docs/notes/2026-06-07-disassembler-page-placement.md).
;
; Preconditions (set up in assembler.asm before the call):
;   - enctab_trampoline_setup has run -> paged_call body installed.
;   - load_page15_payload has run -> physical page 15 holds disasm.bin
;     (entry at DISASM_ENTRY = &8000 when HMPR maps page 15 in).
;
; Two cases are exercised:
;
;   (a) NOP (0xD503201F): DISASM_COMM_MNEM[0] = 'n' (spot check).
;   (b) .inst (0xD2800000): DISASM_COMM_MNEM[0] = '.' (spot check).
;
; Spot checks only to keep the inline budget small.  Full string
; assertions are left to SimCoupé end-to-end verification.
;
; Tag-space (fail_with_tag; existing sites use 01,03,10,11,21,40,62,
; 63,73..7B,b0): &7C..&7E is fresh.
;
;   tag &7C: NOP call — HMPR not bit-identical after paged_call.
;   tag &7D: NOP call — DISASM_COMM_MNEM[0] != 'n'.
;   tag &7E: .inst call — DISASM_COMM_MNEM[0] != '.'.


; -----------------------------------------------------------------------
; run_disasm_paged_self_tests — entry point.
; Input:  none.  Output: returns on success; jp fail_with_tag on error.
; Clobbers: A, BC, DE, HL, IX.
; -----------------------------------------------------------------------
run_disasm_paged_self_tests:

; ----- (a) NOP case: BC=&D503, IX=&201F ----------------------------------
                in      a, (251)
                ld      (disasm_test_hmpr_pre), a

                ld      bc, &D503
                ld      ix, &201F
                call    paged_call
                defw    DISASM_ENTRY
                defb    DISASM_PAGE

; HMPR must be bit-identical.
                in      a, (251)
                ld      hl, disasm_test_hmpr_pre
                cp      (hl)
                jr      nz, disasm_test_fail_7c

; Spot check: DISASM_COMM_MNEM[0] == 'n'.
                ld      a, (DISASM_COMM_MNEM)
                cp      "n"
                jr      nz, disasm_test_fail_7d

; ----- (b) .inst case: BC=&D280, IX=&0000 --------------------------------
                ld      bc, &D280
                ld      ix, &0000
                call    paged_call
                defw    DISASM_ENTRY
                defb    DISASM_PAGE

; Spot check: DISASM_COMM_MNEM[0] == '.'.
                ld      a, (DISASM_COMM_MNEM)
                cp      "."
                jr      nz, disasm_test_fail_7e

                ret


; -----------------------------------------------------------------------
; Failure shims.
; -----------------------------------------------------------------------
disasm_test_fail_7c:
                ld      a, &7c
                jp      fail_with_tag
disasm_test_fail_7d:
                ld      a, &7d
                jp      fail_with_tag
disasm_test_fail_7e:
                ld      a, &7e
                jp      fail_with_tag


; -----------------------------------------------------------------------
; Per-test storage.
; -----------------------------------------------------------------------
disasm_test_hmpr_pre:   defb    0
