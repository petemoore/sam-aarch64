; test_overlay_classify.asm — boot-time self-test for overlay_classify /
; literal_word / compact_inst (src/insn_overlay.asm, item i204b /
; i48c-b8e brick 4).
;
; Like run_encode_inst_self_tests, these routines read the form table +
; slot records from ENCTAB, so this suite runs AFTER load_enctab inside
; its own enctab_map_in / form_lookup_init / enctab_map_out bracket, and
; must stay addressable while ENCTAB occupies section A (the off-axis
; clusters page into section A, so they cannot host it).  It lives in
; the BUILD_TESTS_ENCODE boot variant (i234), rides the page-12 suite
; payload (src/test_overlay_suite.asm), executes from section-D RAM at
; OVERLAY_SUITE_RAM, and runs right after run_encode_inst_self_tests.
;
; Every fixture mirrors one case in
; tools/sam-aarch64/assemble/overlay_classify_fixtures_test.go
; ::TestOverlayClassifyFixtures — that Go test is the authority
; (CLAUDE.md §6) and the drift guard: it re-derives each expected slot /
; flag / base word from overlayClassify + compactInst.
;
; Three kinds of assertion:
;   * compact_inst fixtures — assert {is_literal, base word} against the
;     Go-computed values.  All use constant / PC-relative-constant exprs
;     the encoder reproduces without a symbol table.
;   * overlay_classify fixtures — assert {slot, is_litpool, litwidth, rt}.
;     Symbol-bearing exprs are never resolved: overlay_classify's slot
;     selection is structural.
;   * a loud-gap fixture — assert the A=1 no-overlay-slot status.
;
; The fixture tables + operand streams live in the page-11 enc_fix
; payload (src/test_encode_inst_payload.asm, i204b block at the payload
; tail), already bulk-copied to section-D RAM at ENC_FIX_TABLE_RAM by
; run_encode_inst_self_tests before this suite runs; the toc_* labels
; are imported from build/enc_fix_payload.sym at the suite build.
;
; On any mismatch: record the failing row pointer in LAST_FAIL_PC, the
; failing assertion in LAST_FAIL_TAG (the TOC_TAG_* below), and jp fail
; (red border + "FAIL<tag><row>" banner).
; -----------------------------------------------------------------------

TOC_CI_ROW_LEN: equ     12          ; compact_inst rows (layout: payload)
TOC_OC_ROW_LEN: equ     10          ; overlay_classify rows (layout: payload)

; Fail tags: which assertion tripped (LAST_FAIL_PC carries the row).
TOC_TAG_CI_STATUS: equ  &c0        ; compact_inst returned a loud gap
TOC_TAG_CI_ISLIT:  equ  &c1        ; is_literal mismatch
TOC_TAG_CI_BASE:   equ  &c2        ; base word mismatch
TOC_TAG_OC_STATUS: equ  &c3        ; overlay_classify returned a loud gap
TOC_TAG_OC_SLOT:   equ  &c4        ; slot mismatch
TOC_TAG_OC_LP:     equ  &c5        ; is_litpool mismatch
TOC_TAG_OC_WIDTH:  equ  &c6        ; litwidth mismatch
TOC_TAG_OC_RT:     equ  &c7        ; rt mismatch
TOC_TAG_LOUD:      equ  &c8        ; loud-gap fixture did not report A=1

run_overlay_classify_self_tests:
                call    enctab_map_in
                call    form_lookup_init

; =======================================================================
; Part 1 — compact_inst fixtures: assert {is_literal, base word}.
; =======================================================================
                ld      hl, toc_ci_table
toc_ci_loop:
                ld      (toc_row), hl
; Seed PASS_PC = pc_lo16 (+0/+1; high 16 bits := 0).
                ld      a, (hl)
                ld      (PASS_PC + 0), a
                inc     hl
                ld      a, (hl)
                ld      (PASS_PC + 1), a
                xor     a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
; fixture ptr (+2); 0 => end of table.
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = fixture ptr
                ld      a, e
                or      d
                jr      z, toc_ci_done
                inc     hl
                ld      a, (hl)                 ; opcount
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                 ; BC = mnemonic id
                push    de
                pop     hl                      ; HL = fixture ptr
                push    bc
                pop     de                      ; DE = mnemonic id
                call    compact_inst            ; A=0 ok; sets ovl_base/ovl_is_literal
                or      a
                ld      a, TOC_TAG_CI_STATUS
                jp      nz, toc_fail            ; loud gap not expected here
; Compare is_literal (+7).
                ld      hl, (toc_row)
                ld      de, 7
                add     hl, de
                ld      a, (ovl_is_literal)
                cp      (hl)
                ld      a, TOC_TAG_CI_ISLIT
                jp      nz, toc_fail
; Compare base word (+8..+11) against ovl_base.
                inc     hl                      ; -> +8
                ld      de, ovl_base
                ld      b, 4
toc_ci_cmp:
                ld      a, (de)
                cp      (hl)
                ld      a, TOC_TAG_CI_BASE
                jp      nz, toc_fail
                inc     hl
                inc     de
                djnz    toc_ci_cmp
; Next row.
                ld      hl, (toc_row)
                ld      de, TOC_CI_ROW_LEN
                add     hl, de
                jr      toc_ci_loop
toc_ci_done:

; =======================================================================
; Part 2 — overlay_classify fixtures: assert {slot, is_litpool, litwidth,
; rt}.  (rt: all fixtures use dest reg 0, so OVL_RT==0 is asserted for
; every litpool row.)
; =======================================================================
                ld      hl, toc_oc_table
toc_oc_loop:
                ld      (toc_row), hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = fixture ptr
                ld      a, e
                or      d
                jr      z, toc_oc_done
                inc     hl
                ld      a, (hl)                 ; opcount
                ld      (toc_oc_opcount), a
                inc     hl
                ld      a, (hl)                 ; pc_lo16 lo
                ld      (PASS_PC + 0), a
                inc     hl
                ld      a, (hl)                 ; pc_lo16 hi
                ld      (PASS_PC + 1), a
                xor     a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                 ; BC = mnemonic id
                push    de
                pop     hl                      ; HL = fixture ptr
                push    bc
                pop     de                      ; DE = mnemonic id
                ld      a, (toc_oc_opcount)
                call    overlay_classify        ; A=0 ok / A=1 loud gap
                or      a
                ld      a, TOC_TAG_OC_STATUS
                jp      nz, toc_fail            ; not a loud-gap fixture
; Compare slot (+7).
                ld      hl, (toc_row)
                ld      de, 7
                add     hl, de
                ld      a, (OVL_SLOT)
                cp      (hl)
                ld      a, TOC_TAG_OC_SLOT
                jp      nz, toc_fail
; Compare is_litpool (+8).
                inc     hl
                ld      a, (OVL_IS_LITPOOL)
                cp      (hl)
                ld      a, TOC_TAG_OC_LP
                jp      nz, toc_fail
; Compare litwidth (+9).
                inc     hl
                ld      a, (OVL_LITWIDTH)
                cp      (hl)
                ld      a, TOC_TAG_OC_WIDTH
                jp      nz, toc_fail
; For litpool fixtures, also assert Rt==0 (all fixtures use dest reg 0).
                ld      a, (OVL_IS_LITPOOL)
                or      a
                jr      z, toc_oc_advance
                ld      a, (OVL_RT)
                or      a
                ld      a, TOC_TAG_OC_RT
                jp      nz, toc_fail
toc_oc_advance:
                ld      hl, (toc_row)
                ld      de, TOC_OC_ROW_LEN
                add     hl, de
                jr      toc_oc_loop
toc_oc_done:

; =======================================================================
; Part 3 — loud-gap fixture: lsl x0,x1,#sym must return A=1 (a symbolic
; special-form operand has no overlay slot, overlay.go:137-140).
; =======================================================================
                ld      hl, toc_op_loud_lsl
                ld      (toc_row), hl           ; diagnostic on mismatch
                ld      de, 17                  ; mnemonic id = lsl
                ld      a, 3                    ; opcount
                call    overlay_classify
                cp      1
                ld      a, TOC_TAG_LOUD
                jp      nz, toc_fail

                jp      enctab_map_out          ; tail-call; RETs to caller

; -- Failure exit: A = assertion tag; record the failing row, then fail --
toc_fail:
                ld      hl, (toc_row)
                ld      (LAST_FAIL_PC), hl
                jp      fail_with_tag

; -- Scratch (section-C RAM) ---------------------------------------------
toc_row:        defw    0                       ; current fixture row ptr
toc_oc_opcount: defb    0
