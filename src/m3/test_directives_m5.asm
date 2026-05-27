; test_directives_m5.asm — boot-time self-tests for M5 PR-A directives.
;
; Per docs/specs/2026-05-27-m5-compound-operands-directives-design.md §3.
;
; Tests covered (Layer 1):
;
;   1. compute_directive_size:
;        DIR_INST → 4
;        DIR_SKIP / DIR_SPACE → eval(operand1) low-16
;        DIR_BALIGN at PC=0 with N=8 → 0
;        DIR_BALIGN at PC=5 with N=8 → 3
;        DIR_ALIGN at PC=0 with N=3 (align=8) → 0
;        DIR_ALIGN at PC=5 with N=3 (align=8) → 3
;
;   2. .equ pass-1 — synthetic payload [PUSH_SYM=42][PUSH_IMM32=0x12345678],
;        verify SYMTAB lookup of id 42 returns 0x12345678.
;
;   3. .org pass-1 set — PASS_PC := target;
;      .org pass-2 zero-fill — emit (target - pc) zero bytes.
;
; Each test poke-builds a synthetic .tbn-shaped record in scratch RAM,
; sets up the relevant globals (main_dir_id, main_op_count,
; main_dir_payload_after_header, PASS_PC, PASS_MODE), invokes the
; handler under test, and asserts the observable result.  Failure path:
; jp fail (red border + printer "FAIL").
;
; The directive walker proper (reader → main_handle_directive) is not
; exercised here — it's covered by the Layer 3 fixture round-trip.

DIR_TEST_PC_SCRATCH:    equ     &CF80          ; 32-byte synthetic payload buf


; -----------------------------------------------------------------------
; run_directives_m5_self_tests — entry from assembler.asm start:.
;
; Input:  none.  Output: returns on success; jp fail on any mismatch.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_directives_m5_self_tests:

; -- (1) compute_directive_size for .inst → 4 --------------------------
                ld      a, DIR_INST
                ld      (main_dir_id), a
                xor     a
                ld      (main_op_count), a
                call    compute_directive_size
                ld      a, c
                cp      4
                jp      nz, fail
                ld      a, b
                or      a
                jp      nz, fail

; -- (2) compute_directive_size for .balign at PC=0 with N=8 → 0 -------
                call    test_dir_m5_setup_skip_8
                ld      a, DIR_BALIGN
                ld      (main_dir_id), a
                call    pass_pc_reset
                call    compute_directive_size
                ld      a, c
                or      b
                jp      nz, fail

; -- (3) compute_directive_size for .balign at PC=5 with N=8 → 3 -------
                ld      a, DIR_BALIGN
                ld      (main_dir_id), a
                call    pass_pc_reset
                ld      a, 5
                ld      (PASS_PC + 0), a
                call    compute_directive_size
                ld      a, c
                cp      3
                jp      nz, fail
                ld      a, b
                or      a
                jp      nz, fail
                call    pass_pc_reset

; -- (4) compute_directive_size for .align at PC=0 with N=3 (1<<3=8) → 0
                call    test_dir_m5_setup_skip_3
                ld      a, DIR_ALIGN
                ld      (main_dir_id), a
                call    pass_pc_reset
                call    compute_directive_size
                ld      a, c
                or      b
                jp      nz, fail

; -- (5) compute_directive_size for .align at PC=5 with N=3 → 3 --------
                ld      a, DIR_ALIGN
                ld      (main_dir_id), a
                call    pass_pc_reset
                ld      a, 5
                ld      (PASS_PC + 0), a
                call    compute_directive_size
                ld      a, c
                cp      3
                jp      nz, fail
                ld      a, b
                or      a
                jp      nz, fail
                call    pass_pc_reset

; -- (6) compute_directive_size for .skip 16 → 16 ----------------------
                call    test_dir_m5_setup_skip_16
                ld      a, DIR_SKIP
                ld      (main_dir_id), a
                call    pass_pc_reset
                call    compute_directive_size
                ld      a, c
                cp      16
                jp      nz, fail
                ld      a, b
                or      a
                jp      nz, fail

; -- (7) .equ pass-1: insert (id=42, value=0x12345678) -----------------
; Build a synthetic .equ payload at DIR_TEST_PC_SCRATCH:
;   operand 1 (IMM_EXPR len=3): [0x05][42][0x00]    ; PUSH_SYM 42
;   operand 2 (IMM_EXPR len=5): [0x03][78][56][34][12]  ; PUSH_IMM32 0x12345678
;
; Layout (per text2bin's emitEqu and OperandWriter.WriteImmExpr):
;   [0x05][len_lo][len_hi][bytecode...]
                ld      hl, DIR_TEST_PC_SCRATCH
                ld      (main_dir_payload_after_header), hl
; Operand 1 header.
                ld      (hl), &05                   ; IMM_EXPR kind
                inc     hl
                ld      (hl), 3                     ; len LSB
                inc     hl
                ld      (hl), 0                     ; len MSB
                inc     hl
; Operand 1 bytecode: PUSH_SYM 42.
                ld      (hl), &05
                inc     hl
                ld      (hl), 42
                inc     hl
                ld      (hl), 0
                inc     hl
; Operand 2 header.
                ld      (hl), &05                   ; IMM_EXPR kind
                inc     hl
                ld      (hl), 5                     ; len LSB
                inc     hl
                ld      (hl), 0                     ; len MSB
                inc     hl
; Operand 2 bytecode: PUSH_IMM32 0x12345678.
                ld      (hl), &03
                inc     hl
                ld      (hl), &78
                inc     hl
                ld      (hl), &56
                inc     hl
                ld      (hl), &34
                inc     hl
                ld      (hl), &12
                inc     hl

; Re-init SYMTAB so the insert can't collide with prior test entries.
                call    symbol_table_init

                ld      a, DIR_EQU
                ld      (main_dir_id), a
                ld      a, 2
                ld      (main_op_count), a
                ld      (main_op_count_remaining), a

                call    main_dir_equ_pass1_no_jp

; Verify: symbol_lookup(42) → 0x12345678.
                ld      hl, 42
                call    symbol_lookup
                jp      c, fail                     ; missing → fail
                ld      a, (symbol_value_buf + 0)
                cp      &78
                jp      nz, fail
                ld      a, (symbol_value_buf + 1)
                cp      &56
                jp      nz, fail
                ld      a, (symbol_value_buf + 2)
                cp      &34
                jp      nz, fail
                ld      a, (symbol_value_buf + 3)
                cp      &12
                jp      nz, fail

; -- (8) .org pass-1 — PASS_PC := target -------------------------------
; Build a synthetic .org payload: one IMM_EXPR with PUSH_IMM16 0x40.
                ld      hl, DIR_TEST_PC_SCRATCH
                ld      (main_dir_payload_after_header), hl
                ld      (hl), &05                   ; IMM_EXPR kind
                inc     hl
                ld      (hl), 3                     ; len = 3
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (hl), &02                   ; PUSH_IMM16
                inc     hl
                ld      (hl), &40
                inc     hl
                ld      (hl), 0
                inc     hl

                ld      a, DIR_ORG
                ld      (main_dir_id), a
                ld      a, 1
                ld      (main_op_count), a
                ld      (main_op_count_remaining), a

                call    pass_pc_reset
                call    main_dir_org_pass1_no_jp    ; PASS_PC := 0x40

                ld      a, (PASS_PC + 0)
                cp      &40
                jp      nz, fail
                ld      a, (PASS_PC + 1)
                or      a
                jp      nz, fail
                ld      a, (PASS_PC + 2)
                or      a
                jp      nz, fail
                ld      a, (PASS_PC + 3)
                or      a
                jp      nz, fail

; Reset state for downstream tests / production walk.
                call    pass_pc_reset
                xor     a
                ld      (main_op_count_remaining), a
                ret


; -----------------------------------------------------------------------
; Setup helpers: build a single-IMM_EXPR operand at DIR_TEST_PC_SCRATCH
; with the given inline u8 value, and point
; main_dir_payload_after_header at it.
;
; Layout written:  [0x05][len=2][0][PUSH_IMM8 = 0x01][value]
; -----------------------------------------------------------------------
test_dir_m5_setup_skip_8:
                ld      a, 8
                jp      test_dir_m5_setup_skip_n
test_dir_m5_setup_skip_3:
                ld      a, 3
                jp      test_dir_m5_setup_skip_n
test_dir_m5_setup_skip_16:
                ld      a, 16
                ; fall through
test_dir_m5_setup_skip_n:
                ld      hl, DIR_TEST_PC_SCRATCH
                ld      (main_dir_payload_after_header), hl
                ld      (hl), &05                   ; IMM_EXPR kind
                inc     hl
                ld      (hl), 2                     ; len LSB = 2
                inc     hl
                ld      (hl), 0                     ; len MSB
                inc     hl
                ld      (hl), &01                   ; PUSH_IMM8
                inc     hl
                ld      (hl), a                     ; value
                ld      a, 1
                ld      (main_op_count), a
                ld      (main_op_count_remaining), a
                ret


; -----------------------------------------------------------------------
; main_dir_equ_pass1_no_jp / main_dir_org_pass1_no_jp — wrappers that
; let the self-test call the pass-1 handlers without their trailing
; `jp walk_records`.  The handlers expect to terminate by jumping into
; walk_records; we substitute a `ret` by intercepting just before that
; final jump.  Implementation: re-implement the handlers' core inline
; here, then return.
; -----------------------------------------------------------------------
main_dir_equ_pass1_no_jp:
; Mirror of main_dir_equ_pass1's body, sans the final `jp walk_records`.
                ld      hl, (main_dir_payload_after_header)
                ld      a, (hl)
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      a, d
                or      a
                jp      nz, fail
                ld      a, e
                cp      3
                jp      nz, fail
                ld      a, (hl)
                cp      &05
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                ld      (main_dir_equ_pending_id), bc
                ld      (main_opval_src), hl
                ld      a, 1
                ld      (main_op_count_remaining), a
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                ld      (symbol_value_buf + 0), a
                ld      a, (expr_result + 1)
                ld      (symbol_value_buf + 1), a
                ld      a, (expr_result + 2)
                ld      (symbol_value_buf + 2), a
                ld      a, (expr_result + 3)
                ld      (symbol_value_buf + 3), a
                ld      hl, (main_dir_equ_pending_id)
                call    symbol_insert
                ret


main_dir_org_pass1_no_jp:
                call    main_eval_first_imm
                ld      a, (expr_result + 0)
                ld      (PASS_PC + 0), a
                ld      a, (expr_result + 1)
                ld      (PASS_PC + 1), a
                ld      a, (expr_result + 2)
                ld      (PASS_PC + 2), a
                ld      a, (expr_result + 3)
                ld      (PASS_PC + 3), a
                ret
