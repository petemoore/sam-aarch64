; test_sysname.asm — Layer-1 self-tests for OpSysName encoders (M5 PR-D).
;
; Four vectors, one per mnemonic.  Expected bytes verified against
; aarch64-linux-gnu-as 2.40 on 2026-05-27:
;
;   mrs x0, midr_el1   → 0xd5380000   (sysreg lookup, X-form)
;   msr daifset, #2    → 0xd50342df   (PSTATE-immediate form)
;   dc cvac, x0        → 0xd50b7a20   (DC-table lookup + Xt encoding)
;   tlbi vmalle1       → 0xd508871f   (TLBI no-Xt branch)
;
; Each vector wipes OPVAL_ARRAY, sets up the operand records by hand
; (name bytes go into a scratch buffer at test_sysname_name_buf,
; OPVAL[..+2..+5] points at that buffer), then calls the encoder.
;
; To falsify: change one expected byte in any vector; ci-core will fail
; loudly via assert_eq32_de_hl_imm.


run_sysname_self_tests:
; ---- Test 1: mrs x0, midr_el1 → 0xd5380000 ----------------------------
                call    test_sysname_wipe
; OPVAL[0] = OpRegX, Rt=0
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                xor     a
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1), a
; OPVAL[1] = OpSysName, name = "midr_el1" (8 bytes)
                ld      a, OP_KIND_SYS_NAME
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      hl, test_sysname_midr
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), hl
                ld      hl, 8
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 4), hl
; op_count = 2
                ld      a, 2
                ld      (main_op_count), a
                call    encode_mrs_word
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &38, &d5

; ---- Test 2: msr daifset, #2 → 0xd50342df ----------------------------
                call    test_sysname_wipe
; OPVAL[0] = OpSysName, name = "daifset"
                ld      a, OP_KIND_SYS_NAME
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      hl, test_sysname_daifset
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 2), hl
                ld      hl, 7
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 4), hl
; OPVAL[1] = OpImmExpr, value = 2 (LE 8 bytes at +2..+9)
                ld      a, OP_KIND_IMM_EXPR
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 2
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                ld      a, 2
                ld      (main_op_count), a
                call    encode_msr_word
                call    assert_eq32_de_hl_imm
                defb    &df, &42, &03, &d5

; ---- Test 3: dc cvac, x0 → 0xd50b7a20 --------------------------------
                call    test_sysname_wipe
; OPVAL[0] = OpSysName, name = "cvac"
                ld      a, OP_KIND_SYS_NAME
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      hl, test_sysname_cvac
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 2), hl
                ld      hl, 4
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 4), hl
; OPVAL[1] = OpRegX, Rt=0
                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                xor     a
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a
                ld      a, 2
                ld      (main_op_count), a
                call    encode_dc_word
                call    assert_eq32_de_hl_imm
                defb    &20, &7a, &0b, &d5

; ---- Test 4: tlbi vmalle1 → 0xd508831f -------------------------------
                call    test_sysname_wipe
                ld      a, OP_KIND_SYS_NAME
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      hl, test_sysname_vmalle1
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 2), hl
                ld      hl, 7
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 4), hl
                ld      a, 1
                ld      (main_op_count), a
                call    encode_tlbi_word
                call    assert_eq32_de_hl_imm
                defb    &1f, &87, &08, &d5
                ret


test_sysname_wipe:
                ld      hl, OPVAL_ARRAY
                ld      b, 30
                xor     a
test_sysname_wipe_loop:
                ld      (hl), a
                inc     hl
                djnz    test_sysname_wipe_loop
                ret


test_sysname_midr:      defm    "midr_el1"
test_sysname_daifset:   defm    "daifset"
test_sysname_cvac:      defm    "cvac"
test_sysname_vmalle1:   defm    "vmalle1"
