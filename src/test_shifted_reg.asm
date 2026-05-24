; test_shifted_reg.asm — minimal Layer-1 self-test for the OpShiftedReg
; encoder (src/slots/shifted_reg.asm).  Two vectors chosen to
; exercise the main code paths:
;
;   1. add x0, x1, x2, lsl #4   → 0x8b021020  (arithmetic 3-op)
;   2. tst x0, x1, asr #3       → 0xea810c1f  (logical 2-op w/ Rd=xzr)
;
; Layer 3 (tests/m5/sources/inst_shifted.s and inst_ands.s) covers the
; remaining mnemonics and shift kinds end-to-end against GNU.
;
; Mirrors tools/refenc/pass2.go:1005-1067 (encodeShiftedRegInst).


run_shifted_reg_self_tests:
                call    shifted_t_wipe

; -- (1) add x0, x1, x2, lsl #4   →  0x8b021020 -----------------------
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      (OPVAL_KINDS + 0), a
                ld      (OPVAL_KINDS + 1), a
                ; Rd=0, Rn=1: leave the +1 bytes zero already; bump Rn.
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_SHIFTED_REG
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                ld      (OPVAL_KINDS + 2), a
                ld      a, 1                ; width = X
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 1), a
                ld      a, 2                ; Rm = 2
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                ; shift_kind = LSL = 0 (already zero)
                ld      a, 4                ; imm6 = 4
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 4), a
                ld      a, 1                ; mnem = add
                call    encode_shifted_reg_word
                call    assert_eq32_de_hl_imm
                defb    &20, &10, &02, &8b

                call    shifted_t_wipe

; -- (2) tst x0, x1, asr #3        →  0xea810c1f -----------------------
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      (OPVAL_KINDS + 0), a
                ; OPVAL[0].reg = 0 (Rn=0) already zero
                ld      a, OP_KIND_SHIFTED_REG
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      (OPVAL_KINDS + 1), a
                ld      a, 1                ; width = X
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 1                ; Rm = 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 2                ; shift_kind = ASR
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3), a
                ld      a, 3                ; imm6 = 3
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 4), a
                ld      a, 46               ; mnem = tst
                call    encode_shifted_reg_word
                call    assert_eq32_de_hl_imm
                defb    &1f, &0c, &81, &ea
                ret

; -----------------------------------------------------------------------
; shifted_t_wipe — zero OPVAL_ARRAY[0..2] (30 bytes) + OPVAL_KINDS[0..2].
; -----------------------------------------------------------------------
shifted_t_wipe:
                ld      hl, OPVAL_ARRAY
                ld      b, 30
                xor     a
shifted_t_wipe_a:
                ld      (hl), a
                inc     hl
                djnz    shifted_t_wipe_a
                ld      hl, OPVAL_KINDS
                ld      b, 3
shifted_t_wipe_k:
                ld      (hl), a
                inc     hl
                djnz    shifted_t_wipe_k
                ret
