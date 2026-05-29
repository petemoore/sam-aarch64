; test_extended_reg.asm — minimal Layer-1 self-test for the
; OpExtendedReg encoder (src/slots/extended_reg.asm).
;
; Mirrors tools/refenc/pass2.go:1131-1179 (encodeExtendedRegInst).
;
; One vector covers the option-3 / imm3 / X-form path; Layer 3
; (tests/m5/sources/inst_extended.s) covers the sxtw (no #N) shape via
; the parser end-to-end.
;
;   add x0, x1, w2, uxtw #2   →  0x8b224820


run_extended_reg_self_tests:
                ld      hl, OPVAL_ARRAY
                ld      b, 30
                xor     a
test_ext_wipe:
                ld      (hl), a
                inc     hl
                djnz    test_ext_wipe

; OPVAL[0] = X reg, Rd=0
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
; OPVAL[1] = X reg, Rn=1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 1
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
; OPVAL[2] = ExtendedReg, width=0 (Rm is W), Rm=2, extend=UXTW(2), imm3=2.
                ld      a, OP_KIND_EXTENDED_REG
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0), a
                xor     a
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 1), a
                ld      a, 2
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2), a
                ld      a, 2
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 3), a
                ld      a, 2
                ld      (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 4), a

                ld      a, 1                ; mnem = add
                call    encode_extended_reg_word
                call    assert_eq32_de_hl_imm
                defb    &20, &48, &22, &8b
                ret
