; test_ror_imm.asm — Layer-1 self-tests for the ror-imm intercept.
;
; Mirrors tools/refenc/pass2.go:1460-1492 (encodeRorImm).
;
; Builds a synthetic OPVAL_ARRAY for each test case and calls
; encode_ror_imm_word (the pure word-computation half of
; intercept_ror_imm — no emit, no PC advance).  DEHL holds the encoded
; word on return; assert_eq32_de_hl_imm compares it against the GNU as
; reference bytes.
;
; Test vectors verified against `aarch64-elf-as` + `objcopy -O binary`:
;   ror w0, w1, #5  →  0x13811420  (LE: 20 14 81 13)
;   ror x0, x1, #5  →  0x93c11420  (LE: 20 14 c1 93)
;   ror w7, w7, #31 →  0x13877cE7  (LE: e7 7c 87 13)   (max imm6 for W)
;   ror x3, x2, #63 →  0x93c2fc43  (LE: 43 fc c2 93)   (max imm6 for X)
;
; Hand-calc for the last two:
;   `ror w7, w7, #31`: base 0x13800000, Rs=7 (bits 20:16 = 0x70000),
;     imm6=31 (bits 15:10 = 0x7C00), Rs=7 (bits 9:5 = 0xE0), Rd=7.
;     0x13800000 | 0x70000 | 0x7C00 | 0xE0 | 0x07 = 0x13877CE7.
;   `ror x3, x2, #63`: base 0x93c00000, Rs=2 (bits 20:16 = 0x20000),
;     imm6=63 (bits 15:10 = 0xFC00), Rs=2 (bits 9:5 = 0x40), Rd=3.
;     0x93c00000 | 0x20000 | 0xFC00 | 0x40 | 0x03 = 0x93c2fc43.


; -----------------------------------------------------------------------
; run_ror_imm_self_tests — entry from assembler.asm start:.
;
; Input:  none.  Output: returns on success; jp fail on any mismatch.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_ror_imm_self_tests:

; -- (1) ror w0, w1, #5  →  0x13811420 ---------------------------------
                call    test_ror_clear_opval
                ld      a, OP_KIND_REG_W
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, 0
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1), a    ; Rd=0
                ld      a, OP_KIND_REG_W
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a    ; Rs=1
                ld      a, OP_KIND_IMM_EXPR
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                ld      a, 5
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a    ; shift=5
                call    encode_ror_imm_word
                call    assert_eq32_de_hl_imm
                defb    &20, &14, &81, &13     ; 0x13811420 LE

; -- (2) ror x0, x1, #5  →  0x93c11420 ---------------------------------
                call    test_ror_clear_opval
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, 0
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_IMM_EXPR
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                ld      a, 5
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                call    encode_ror_imm_word
                call    assert_eq32_de_hl_imm
                defb    &20, &14, &c1, &93     ; 0x93c11420 LE

; -- (3) ror w7, w7, #31  →  0x13877CE7 --------------------------------
                call    test_ror_clear_opval
                ld      a, OP_KIND_REG_W
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, 7
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_REG_W
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 7
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_IMM_EXPR
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                ld      a, 31
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                call    encode_ror_imm_word
                call    assert_eq32_de_hl_imm
                defb    &e7, &7c, &87, &13     ; 0x13877CE7 LE

; -- (4) ror x3, x2, #63  →  0x93C2FC43 --------------------------------
                call    test_ror_clear_opval
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, 3
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 2
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, OP_KIND_IMM_EXPR
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                ld      a, 63
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                call    encode_ror_imm_word
                call    assert_eq32_de_hl_imm
                defb    &43, &fc, &c2, &93     ; 0x93C2FC43 LE

                ret


; -----------------------------------------------------------------------
; test_ror_clear_opval — zero OPVAL_ARRAY entries [0..2] (30 bytes).
; -----------------------------------------------------------------------
test_ror_clear_opval:
                ld      hl, OPVAL_ARRAY
                ld      b, 30
                xor     a
test_ror_clear_opval_loop:
                ld      (hl), a
                inc     hl
                djnz    test_ror_clear_opval_loop
                ret
