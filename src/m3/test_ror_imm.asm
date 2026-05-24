; test_ror_imm.asm — minimal Layer-1 self-test for the ror-imm intercept.
;
; Mirrors tools/refenc/pass2.go:1460-1492 (encodeRorImm).
;
; Two vectors cover the two bases and exercise the imm6 shift, Rs / Rd
; packing, and bits 20:16 OR.  Layer 3 (tests/m5/sources/inst_alu_single.s)
; covers the same encoder via the parser end-to-end.
;
;   ror w0, w1, #5  →  0x13811420  (32-bit base 0x13800000)
;   ror x0, x1, #5  →  0x93c11420  (64-bit base 0x93c00000)


run_ror_imm_self_tests:
                call    test_ror_wipe

; -- (1) ror w0, w1, #5  →  0x13811420 ---------------------------------
                ld      a, OP_KIND_REG_W
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a    ; Rs=1
                ld      a, OP_KIND_IMM_EXPR
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                ld      a, 5
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                call    encode_ror_imm_word
                call    assert_eq32_de_hl_imm
                defb    &20, &14, &81, &13

                call    test_ror_wipe

; -- (2) ror x0, x1, #5  →  0x93c11420 ---------------------------------
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_IMM_EXPR
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                ld      a, 5
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                call    encode_ror_imm_word
                call    assert_eq32_de_hl_imm
                defb    &20, &14, &c1, &93
                ret


; -----------------------------------------------------------------------
; test_ror_wipe — zero OPVAL_ARRAY[0..2] (30 bytes).
; -----------------------------------------------------------------------
test_ror_wipe:
                ld      hl, OPVAL_ARRAY
                ld      b, 30
                xor     a
test_ror_wipe_loop:
                ld      (hl), a
                inc     hl
                djnz    test_ror_wipe_loop
                ret
