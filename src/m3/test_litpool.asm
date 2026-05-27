; test_litpool.asm — Layer-1 self-tests for the OpLitPool pass-1 table,
; .ltorg flush, and pass-2 imm19 patch.  M5 PR-E Task 13.
;
; Three concerns:
;
;   1. Dedup: registering the same (value, width) bytecode twice produces
;      one slot and two pc-map entries.  Different widths or different
;      bytecodes get different slots.
;
;   2. Flush alignment: Wn entries pack first (4-byte aligned); Xn
;      entries follow with 8-byte alignment.  PASS_PC advances by sum
;      of entry sizes plus any pad bytes.
;
;   3. Encoder: imm19 = (entry_pc - inst_pc) / 4 ORed onto 0x58000000
;      (X) or 0x18000000 (W) along with Rt at bits 0..4.
;
; PASS_MODE is set to PASS_PASS1 throughout — flush's pass-1 path
; advances PASS_PC without emitting OUT bytes, which is what the test
; verifies.  Pass-2 emit behaviour is covered by the Layer-3 round-trip
; (tests/m5/sources/inst_ldr_litpool*.s).

run_litpool_self_tests:
                call    litpool_init
                ld      a, PASS_PASS1
                ld      (PASS_MODE), a

; -- (1) First register: width=8, expr_a at PC=0 → slot 0 -------------
                xor     a
                ld      (PASS_PC + 0), a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a

                ld      a, 8
                ld      hl, litpool_test_expr_a
                ld      bc, 2
                call    litpool_register

                ld      a, (LITPOOL_COUNT)
                cp      1
                jp      nz, fail

; -- (2) Same key at PC=4: dedup hit (slot 0; PCM grows) --------------
                ld      a, 4
                ld      (PASS_PC + 0), a

                ld      a, 8
                ld      hl, litpool_test_expr_a
                ld      bc, 2
                call    litpool_register

                ld      a, (LITPOOL_COUNT)
                cp      1
                jp      nz, fail
                ld      a, (LITPOOL_PCM_COUNT)
                cp      2
                jp      nz, fail

; -- (3) Different width: width=4, same expr → slot 1 -----------------
                ld      a, 8
                ld      (PASS_PC + 0), a

                ld      a, 4
                ld      hl, litpool_test_expr_a
                ld      bc, 2
                call    litpool_register

                ld      a, (LITPOOL_COUNT)
                cp      2
                jp      nz, fail

; -- (4) Different bytecode: width=8, expr_b → slot 2 -----------------
                ld      a, 12
                ld      (PASS_PC + 0), a

                ld      a, 8
                ld      hl, litpool_test_expr_b
                ld      bc, 2
                call    litpool_register

                ld      a, (LITPOOL_COUNT)
                cp      3
                jp      nz, fail

; -- (5) Flush at PC=16.  Expected layout (pass-1 PC-only path):
;        slot 1 (Wn): entry_pc = 16; PC → 20
;        pad to 8:    +4 bytes;       PC → 24
;        slot 0 (Xn): entry_pc = 24; PC → 32
;        slot 2 (Xn): entry_pc = 32; PC → 40
                ld      a, 16
                ld      (PASS_PC + 0), a

                call    litpool_flush

                ld      a, (PASS_PC + 0)
                cp      40
                jp      nz, fail
                ld      a, (PASS_PC + 1)
                or      a
                jp      nz, fail

; Verify slot 0 entry_pc = 24.
                xor     a
                call    slot_ptr_for_idx
                ld      bc, 9
                add     hl, bc
                ld      a, (hl)
                cp      24
                jp      nz, fail

; Verify slot 1 entry_pc = 16.
                ld      a, 1
                call    slot_ptr_for_idx
                ld      bc, 9
                add     hl, bc
                ld      a, (hl)
                cp      16
                jp      nz, fail

; Verify slot 2 entry_pc = 32.
                ld      a, 2
                call    slot_ptr_for_idx
                ld      bc, 9
                add     hl, bc
                ld      a, (hl)
                cp      32
                jp      nz, fail

; -- (6) imm19 maths: LDR #1 (X-form, PC=0) → slot 0 entry_pc=24 -----
;   off = 24, imm19 = 6, Rt = 0
;   word = 0x58000000 | (6 << 5) | 0 = 0x580000c0
                xor     a
                ld      (PASS_PC + 0), a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a

                ld      a, OP_KIND_REG_X
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                xor     a
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1), a

                ld      a, OP_KIND_LIT_POOL
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 8
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a

                call    litpool_encode_ldr_word
                call    assert_eq32_de_hl_imm
                defb    &c0, &00, &00, &58

; -- (7) W-form encoding: LDR #3 (W-form, PC=8) → slot 1 entry_pc=16
;   off = 8, imm19 = 2, Rt = 5
;   word = 0x18000000 | (2 << 5) | 5 = 0x18000045
                ld      a, 8
                ld      (PASS_PC + 0), a

                ld      a, OP_KIND_REG_W
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0), a
                ld      a, 5
                ld      (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1), a

                ld      a, OP_KIND_LIT_POOL
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0), a
                ld      a, 4
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1), a

                call    litpool_encode_ldr_word
                call    assert_eq32_de_hl_imm
                defb    &45, &00, &00, &18

                ret


; -----------------------------------------------------------------------
; Test bytecode buffers — two distinct PUSH_IMM8 expressions of 2 bytes
; each.  The dedup logic compares bytecode bytes verbatim; the actual
; eval result is unused in pass 1.
; -----------------------------------------------------------------------
litpool_test_expr_a:    defb    &01, &aa
litpool_test_expr_b:    defb    &01, &bb
