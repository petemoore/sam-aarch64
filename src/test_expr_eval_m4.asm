; test_expr_eval_m4.asm — boot-time self-tests for the M4 additions to
; the expression-bytecode evaluator (expr_eval.asm).
;
; Per docs/plans/2026-05-24-m4-symbols-multipass.md Task 5.
;
; Mirrors test_symbols.asm / test_local_labels.asm: runs from start:
; BEFORE load_enctab, hard-coded bytecode buffers + table state, no
; disk I/O.  Any assertion failure does `jp fail` (red border +
; printer-channel "FAIL" banner, ci-m3 reports fail immediately).
;
; Coverage:
;   1. PUSH_PC               (op 0x07).
;   2. PUSH_SYM hit          (op 0x05, id in pre-populated symbol table).
;   3. PUSH_LOCAL forward    (op 0x06, dir=0).
;   4. PUSH_LOCAL backward   (op 0x06, dir=1).
;   5. REL_LO12              (op 0x30 on 0x12345678  → 0x678).
;   6. REL_HI12              (op 0x31 on 0x12345678  → 0x345).
;   7. REL_ABS_G0            (op 0x32 on 0x12345678  → 0x5678).
;   8. REL_ABS_G1            (op 0x34 on 0x12345678  → 0x1234).
;   9. REL_ABS_G2            (op 0x36 on 0xAABBCCDDEEFF1122 → 0xCCDD).
;  10. REL_ABS_G3            (op 0x38 on 0xAABBCCDDEEFF1122 → 0xAABB).
;
; Not exercised (one-way jp fail paths; same caveat as test_slots /
; test_symbols / test_local_labels):
;   - PUSH_SYM   miss (undefined symbol).
;   - PUSH_LOCAL miss (no matching label).
;   - PUSH_LOCAL invalid dir.
;   Covered by inspection.
;
; The M3 evaluator subset (PUSH_IMM*, ADD/SUB/AND/OR/XOR/SHL/SHR,
; NEG/NOT) is exercised end-to-end by the fixture corpus; we don't
; duplicate that coverage here.


; -----------------------------------------------------------------------
; run_expr_eval_m4_self_tests — entry point called from assembler.asm
; start: (between run_local_label_self_tests and load_enctab).
;
; Input:  none.
; Output: returns to caller on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
;
; Each sub-test follows the pattern:
;   1. Set up any prerequisite state (PASS_PC, symbol_table, local_label
;      table) so the opcode under test has something to resolve against.
;   2. Call eval_expr_const with HL = pointer to the bytecode buffer and
;      BC = length.
;   3. Assert expr_result matches the expected 8 LE bytes.
;
; We never reset PASS_PC / the tables between sub-tests; each sub-test
; that needs a specific value sets it explicitly.  The symbol and
; local-label tables ARE re-initialised at the top because the prior
; suites (test_symbols, test_local_labels) populated them.
; -----------------------------------------------------------------------
run_expr_eval_m4_self_tests:

; -- (1) PUSH_PC --------------------------------------------------------
; PASS_PC = 0x12345678; bytecode = [0x07]; expect 78 56 34 12 00 00 00 00.
                ld      a, &78
                ld      (PASS_PC + 0), a
                ld      a, &56
                ld      (PASS_PC + 1), a
                ld      a, &34
                ld      (PASS_PC + 2), a
                ld      a, &12
                ld      (PASS_PC + 3), a

                ld      hl, t_expr_push_pc
                ld      bc, t_expr_push_pc_end - t_expr_push_pc
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &78, &56, &34, &12, &00, &00, &00, &00

; -- (2) PUSH_SYM hit ---------------------------------------------------
; Re-init the symbol table (test_symbols left it populated), insert one
; entry, eval PUSH_SYM, expect the address zero-extended to 64 bits.
                call    symbol_table_init

                call    set_value_buf_imm
                defb    &44, &33, &22, &11           ; address 0x11223344
                ld      hl, &0042
                call    symbol_insert

                ld      hl, t_expr_push_sym_42
                ld      bc, t_expr_push_sym_42_end - t_expr_push_sym_42
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &44, &33, &22, &11, &00, &00, &00, &00

; -- (3) PUSH_LOCAL forward --------------------------------------------
; Re-init the local-label table, build digit-1 = [4, 12, 24, 100].
; PASS_PC = 8 → forward lookup must return 12.
                call    local_label_table_init

                call    set_local_pc_buf_imm
                defb    &04, &00, &00, &00
                ld      a, 1
                call    local_def_append

                call    set_local_pc_buf_imm
                defb    &0C, &00, &00, &00           ; 12
                ld      a, 1
                call    local_def_append

                call    set_local_pc_buf_imm
                defb    &18, &00, &00, &00           ; 24
                ld      a, 1
                call    local_def_append

                call    set_local_pc_buf_imm
                defb    &64, &00, &00, &00           ; 100
                ld      a, 1
                call    local_def_append

                ld      a, 8
                ld      (PASS_PC + 0), a
                xor     a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a

                ld      hl, t_expr_push_local_fwd
                ld      bc, t_expr_push_local_fwd_end - t_expr_push_local_fwd
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &0C, &00, &00, &00, &00, &00, &00, &00

; -- (4) PUSH_LOCAL backward -------------------------------------------
; Same digit-1 list, PASS_PC = 50 → backward lookup returns 24.
                ld      a, 50
                ld      (PASS_PC + 0), a
                xor     a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a

                ld      hl, t_expr_push_local_bwd
                ld      bc, t_expr_push_local_bwd_end - t_expr_push_local_bwd
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &18, &00, &00, &00, &00, &00, &00, &00

; -- (5) REL_LO12 ------------------------------------------------------
; PUSH_IMM64 0x12345678; REL_LO12 → 0x678.
                ld      hl, t_expr_rel_lo12
                ld      bc, t_expr_rel_lo12_end - t_expr_rel_lo12
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &78, &06, &00, &00, &00, &00, &00, &00

; -- (6) REL_HI12 ------------------------------------------------------
; PUSH_IMM64 0x12345678; REL_HI12 → (0x12345678 >> 12) & 0xFFF = 0x345.
                ld      hl, t_expr_rel_hi12
                ld      bc, t_expr_rel_hi12_end - t_expr_rel_hi12
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &45, &03, &00, &00, &00, &00, &00, &00

; -- (7) REL_ABS_G0 ----------------------------------------------------
; PUSH_IMM64 0x12345678; REL_ABS_G0 → 0x5678.
                ld      hl, t_expr_rel_g0
                ld      bc, t_expr_rel_g0_end - t_expr_rel_g0
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &78, &56, &00, &00, &00, &00, &00, &00

; -- (8) REL_ABS_G1 ----------------------------------------------------
; PUSH_IMM64 0x12345678; REL_ABS_G1 → (0x12345678 >> 16) & 0xFFFF = 0x1234.
                ld      hl, t_expr_rel_g1
                ld      bc, t_expr_rel_g1_end - t_expr_rel_g1
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &34, &12, &00, &00, &00, &00, &00, &00

; -- (9) REL_ABS_G2 ----------------------------------------------------
; PUSH_IMM64 0xAABBCCDDEEFF1122; REL_ABS_G2 → bytes 4-5 = 0xCCDD.
                ld      hl, t_expr_rel_g2
                ld      bc, t_expr_rel_g2_end - t_expr_rel_g2
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &DD, &CC, &00, &00, &00, &00, &00, &00

; -- (10) REL_ABS_G3 ---------------------------------------------------
; PUSH_IMM64 0xAABBCCDDEEFF1122; REL_ABS_G3 → bytes 6-7 = 0xAABB.
                ld      hl, t_expr_rel_g3
                ld      bc, t_expr_rel_g3_end - t_expr_rel_g3
                call    eval_expr_const
                call    assert_expr_result_eq_imm
                defb    &BB, &AA, &00, &00, &00, &00, &00, &00

; All assertions passed.
                ret


; -----------------------------------------------------------------------
; Hand-built bytecode buffers used by the sub-tests.
;
; Each follows the .tbn EXPR_BC encoding: a stream of opcodes with
; inline operand bytes (LE).  See tools/sam-aarch64-format/expr.go.
;
; Trailing `_end:` labels let the caller compute length without manual
; bookkeeping.
; -----------------------------------------------------------------------

t_expr_push_pc:
                defb    &07                          ; PUSH_PC
t_expr_push_pc_end:

t_expr_push_sym_42:
                defb    &05, &42, &00                ; PUSH_SYM id=0x0042
t_expr_push_sym_42_end:

t_expr_push_local_fwd:
                defb    &06, &01, &00                ; PUSH_LOCAL digit=1 dir=fwd
t_expr_push_local_fwd_end:

t_expr_push_local_bwd:
                defb    &06, &01, &01                ; PUSH_LOCAL digit=1 dir=bwd
t_expr_push_local_bwd_end:

t_expr_rel_lo12:
                defb    &04, &78, &56, &34, &12, &00, &00, &00, &00   ; PUSH_IMM64 0x12345678
                defb    &30                          ; REL_LO12
t_expr_rel_lo12_end:

t_expr_rel_hi12:
                defb    &04, &78, &56, &34, &12, &00, &00, &00, &00
                defb    &31                          ; REL_HI12
t_expr_rel_hi12_end:

t_expr_rel_g0:
                defb    &04, &78, &56, &34, &12, &00, &00, &00, &00
                defb    &32                          ; REL_ABS_G0
t_expr_rel_g0_end:

t_expr_rel_g1:
                defb    &04, &78, &56, &34, &12, &00, &00, &00, &00
                defb    &34                          ; REL_ABS_G1
t_expr_rel_g1_end:

t_expr_rel_g2:
                defb    &04, &22, &11, &FF, &EE, &DD, &CC, &BB, &AA   ; PUSH_IMM64 0xAABBCCDDEEFF1122
                defb    &36                          ; REL_ABS_G2
t_expr_rel_g2_end:

t_expr_rel_g3:
                defb    &04, &22, &11, &FF, &EE, &DD, &CC, &BB, &AA
                defb    &38                          ; REL_ABS_G3
t_expr_rel_g3_end:


; -----------------------------------------------------------------------
; assert_expr_result_eq_imm — assert expr_result equals an 8-byte
; little-endian literal that immediately follows the call in the
; caller's code stream.
;
; Caller pattern:
;     call assert_expr_result_eq_imm
;     defb b0, b1, b2, b3, b4, b5, b6, b7
;
; Mirrors assert_value_buf_eq_imm (test_symbols.asm) but reads from
; expr_result and checks 8 bytes instead of 4.  On mismatch: jp fail.
;
; Uses an unrolled 8-byte compare (rather than a djnz loop) because the
; loop counter would otherwise clash with BC's role as the inline-
; literal pointer.  Eight inlined compares is also tiny.
;
; Clobbers: A, BC, HL.
; -----------------------------------------------------------------------
assert_expr_result_eq_imm:
                pop     bc                          ; BC = ptr to inline literal
                ld      hl, expr_result

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc                          ; BC = past the 8-byte literal

                push    bc
                ret
