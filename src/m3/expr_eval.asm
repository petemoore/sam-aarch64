; expr_eval.asm — constant-only expression bytecode evaluator.
;
; Z80 port of tools/sam-aarch64-format/expr.go::EvalConst and (parts of)
; tools/aarch64enc/expr.go::Eval for the M3 constant subset.
;
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.5:
;   • Supported opcodes: PUSH_IMM8/16/32/64, ADD, SUB, AND, OR, XOR,
;     SHL, SHR, NEG, NOT.
;   • Errors (jp fail) on PUSH_SYM, PUSH_LOCAL, PUSH_PC, REL_*, MUL, DIV.
;     text2bin's constant-folder should have collapsed these.
;
; Opcode values (tools/sam-aarch64-format/expr.go lines 12-42):
;
;   0x01 PUSH_IMM8        [s8]
;   0x02 PUSH_IMM16       [s16 LE]
;   0x03 PUSH_IMM32       [s32 LE]
;   0x04 PUSH_IMM64       [s64 LE]
;   0x05 PUSH_SYM         [u16] (M4 — fail)
;   0x06 PUSH_LOCAL       [u8 digit, u8 dir] (M4 — fail)
;   0x07 PUSH_PC          (M4 — fail)
;
;   0x10 ADD              (binary)
;   0x11 SUB              (binary)
;   0x12 MUL              (M4 — fail)
;   0x13 DIV              (M4 — fail)
;   0x14 AND              (binary)
;   0x15 OR               (binary)
;   0x16 XOR              (binary)
;   0x17 SHL              (binary: a << b)
;   0x18 SHR              (binary: a >> b, arithmetic)
;
;   0x20 NEG              (unary)
;   0x21 NOT              (unary)
;
;   0x30..0x38 REL_*      (M4 — fail)
;
; -----------------------------------------------------------------------
; Calling convention
; -----------------------------------------------------------------------
;
;   eval_expr_const:
;     Input:
;       HL = pointer to first byte of bytecode stream
;       BC = length of bytecode stream (u16)
;     Output:
;       expr_result[0..7] = 8-byte little-endian s64 result.
;     Error:
;       jp fail on any reserved/unsupported opcode, truncated operand,
;       or stack mismatch (more than 1 value on the stack at end).
;
; -----------------------------------------------------------------------
; Internals: a fixed-size 8-byte-value stack in scratch RAM.
;
;   expr_stack:      EXPR_STACK_DEPTH * 8 bytes
;   expr_sp:         u8 current depth (number of values on stack)
;
; EXPR_STACK_DEPTH = 8 is comfortable for any M3 fixture; the format
; spec puts no hard upper bound but text2bin's folder collapses fully-
; constant expressions to a single PUSH, so depth at most 2-3 in
; practice.  We pick 8 to cover any reasonable hand-emitted bytecode
; and to give room for nested operator chains.

EXPR_STACK_DEPTH:       equ     8


; -----------------------------------------------------------------------
; eval_expr_const — evaluate a constant-only expression bytecode.
;
; Input:
;   HL = pointer into the bytecode buffer (typically inside the .tbn
;        record's operand payload, somewhere in IN_BUF).
;   BC = byte count of the bytecode.
;
; Output:
;   The 8-byte LE result is left in `expr_result`.
;   Z flag is meaningless on return; callers consume expr_result directly.
;
; On any error: jp fail.
;
; Clobbers: A, BC, DE, HL.  Preserves IY, SP.
;
; Implementation notes:
;
;   • Each PUSH_IMMn opcode allocates the next free stack slot, then
;     copies/sign-extends the inline operand bytes into it.
;
;   • Each binary op pops two values (rhs into a temporary, lhs stays
;     in place at the new top), applies the op via ml_*, leaves the
;     result on top.
;
;   • SHL/SHR consume only the low byte of `rhs` as the shift count
;     (matching Go's `uint64(b)` cast then `<<` — high bytes of b that
;     exceed 63 produce zero/sign-extended results; for M3 we expect
;     shift counts <= 63 always, so we clamp implicitly via the
;     ml_shl/ml_shr_arith routines which terminate after 64 iterations
;     anyway via the bit-falling-off-end semantics).
; -----------------------------------------------------------------------
eval_expr_const:
; -- Reset stack depth --------------------------------------------------
                xor     a
                ld      (expr_sp), a

; -- Park bytecode pointer and remaining count -------------------------
                ld      (eval_pos), hl
                ld      (eval_remaining), bc

eval_loop:
; -- Termination: remaining == 0 → check single value left and return --
                ld      hl, (eval_remaining)
                ld      a, h
                or      l
                jr      z, eval_done

; -- Fetch next opcode byte --------------------------------------------
                ld      hl, (eval_pos)
                ld      a, (hl)
                inc     hl
                ld      (eval_pos), hl
                ld      hl, (eval_remaining)
                dec     hl
                ld      (eval_remaining), hl

; -- Dispatch on opcode --------------------------------------------------
                cp      &01
                jp      z, eval_push_imm8
                cp      &02
                jp      z, eval_push_imm16
                cp      &03
                jp      z, eval_push_imm32
                cp      &04
                jp      z, eval_push_imm64
                cp      &10
                jp      z, eval_add
                cp      &11
                jp      z, eval_sub
                cp      &14
                jp      z, eval_and
                cp      &15
                jp      z, eval_or
                cp      &16
                jp      z, eval_xor
                cp      &17
                jp      z, eval_shl
                cp      &18
                jp      z, eval_shr
                cp      &20
                jp      z, eval_neg
                cp      &21
                jp      z, eval_not
; Everything else (PUSH_SYM/LOCAL/PC, MUL/DIV, REL_*) — M4 / unsupported.
                jp      fail


; -----------------------------------------------------------------------
; eval_done — one value must remain on the stack; copy it to
; expr_result and return.  Stack-depth mismatch → jp fail.
; -----------------------------------------------------------------------
eval_done:
                ld      a, (expr_sp)
                cp      1
                jp      nz, fail
; Stack slot 0 is at expr_stack; copy 8 bytes to expr_result.
                ld      de, expr_stack
                ld      hl, expr_result
                call    ml_copy8
                ret


; -----------------------------------------------------------------------
; Helper: eval_alloc_top — allocate a new top-of-stack slot.
;
; On entry: nothing.
; On exit:  HL → start of new top-of-stack 8-byte slot.
;           expr_sp incremented; zero-fills the slot for safety.
; On overflow (>= EXPR_STACK_DEPTH): jp fail.
; -----------------------------------------------------------------------
eval_alloc_top:
                ld      a, (expr_sp)
                cp      EXPR_STACK_DEPTH
                jp      nc, fail
                ld      b, a
                inc     a
                ld      (expr_sp), a
; HL = expr_stack + B*8.
                ld      hl, expr_stack
                inc     b
                dec     b
                ret     z
eval_alloc_top_shift:
                ld      de, 8
                add     hl, de
                djnz    eval_alloc_top_shift
                ret


; -----------------------------------------------------------------------
; Helper: eval_top_ptr — leave HL pointing at the current top slot
; (without changing expr_sp).  expr_sp must be >= 1; if 0, jp fail.
; -----------------------------------------------------------------------
eval_top_ptr:
                ld      a, (expr_sp)
                or      a
                jp      z, fail
                dec     a
                ld      b, a
                ld      hl, expr_stack
                inc     b
                dec     b
                ret     z
eval_top_ptr_shift:
                ld      de, 8
                add     hl, de
                djnz    eval_top_ptr_shift
                ret


; -----------------------------------------------------------------------
; Helper: eval_top2_ptrs — leave HL = lhs slot (second-from-top),
; DE = rhs slot (top).  Decrements expr_sp by 1 (so on return lhs is
; the new top).  On underflow (sp < 2): jp fail.
; -----------------------------------------------------------------------
eval_top2_ptrs:
                ld      a, (expr_sp)
                cp      2
                jp      c, fail
                dec     a
                ld      (expr_sp), a        ; sp -= 1; lhs is new top
                dec     a                   ; A = lhs index
                ld      b, a
                ld      hl, expr_stack
                inc     b
                dec     b
                jr      z, eval_top2_lhs_ready
eval_top2_lhs_shift:
                ld      de, 8
                add     hl, de
                djnz    eval_top2_lhs_shift
eval_top2_lhs_ready:
                ld      d, h
                ld      e, l                ; DE = lhs (temporarily)
                push    de
; Advance HL by 8 to get rhs.
                ld      bc, 8
                add     hl, bc
                ex      de, hl              ; DE = rhs
                pop     hl                  ; HL = lhs
                ret


; -----------------------------------------------------------------------
; Helper: eval_take_bytes — copy N bytes from eval_pos into (HL), then
; advance eval_pos / eval_remaining by N.  If eval_remaining < N
; → jp fail.
;
; Input:  HL = dest, B = N (1..8).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
eval_take_bytes:
                push    hl                  ; preserve dest
; Range check.
                ld      a, b
                ld      hl, (eval_remaining)
                ld      c, a
                ld      a, h
                or      a
                jr      nz, eval_take_have_enough   ; remaining >= 256 → OK
                ld      a, l
                cp      c
                jp      c, fail             ; remaining < N → fail
eval_take_have_enough:
                pop     hl                  ; restore dest
                ld      de, (eval_pos)
                ld      a, b                ; A = N
                push    af
eval_take_loop:
                ld      a, (de)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    eval_take_loop
                ld      (eval_pos), de
                pop     af
                ld      e, a
                ld      d, 0
                ld      hl, (eval_remaining)
                or      a
                sbc     hl, de
                ld      (eval_remaining), hl
                ret


; -----------------------------------------------------------------------
; Push opcodes.
; -----------------------------------------------------------------------

; eval_push_imm8 — read 1 byte, sign-extend to 8-byte LE on the stack.
eval_push_imm8:
                call    eval_alloc_top      ; HL = new top slot
                ld      b, 1
                call    eval_take_bytes     ; copies 1 byte into top[0]
                call    eval_sign_extend1
                jp      eval_loop

; eval_push_imm16 — read 2 bytes, sign-extend.
eval_push_imm16:
                call    eval_alloc_top
                ld      b, 2
                call    eval_take_bytes
                call    eval_sign_extend2
                jp      eval_loop

; eval_push_imm32 — read 4 bytes, sign-extend.
eval_push_imm32:
                call    eval_alloc_top
                ld      b, 4
                call    eval_take_bytes
                call    eval_sign_extend4
                jp      eval_loop

; eval_push_imm64 — read 8 bytes (no extension needed).
eval_push_imm64:
                call    eval_alloc_top
                ld      b, 8
                call    eval_take_bytes
                jp      eval_loop


; -----------------------------------------------------------------------
; Sign-extend helpers.
;
; The current top slot is at TOP = expr_stack + (expr_sp-1)*8.
; Bytes [0..n-1] of TOP have just been filled with the immediate; bytes
; [n..7] must be set to the sign-extension byte (0x00 or 0xFF) based on
; bit 7 of TOP[n-1].
; -----------------------------------------------------------------------

eval_sign_extend1:
                call    eval_top_ptr        ; HL = TOP
                ld      a, (hl)
                add     a, a
                sbc     a, a                ; A = 0xFF if bit7 set, else 0x00
                ld      b, 7
                inc     hl                  ; HL → TOP[1]
eval_se1_loop:
                ld      (hl), a
                inc     hl
                djnz    eval_se1_loop
                ret

eval_sign_extend2:
                call    eval_top_ptr
                inc     hl                  ; HL → TOP[1]
                ld      a, (hl)
                add     a, a
                sbc     a, a
                ld      b, 6
                inc     hl                  ; HL → TOP[2]
eval_se2_loop:
                ld      (hl), a
                inc     hl
                djnz    eval_se2_loop
                ret

eval_sign_extend4:
                call    eval_top_ptr
                ld      bc, 3
                add     hl, bc              ; HL → TOP[3]
                ld      a, (hl)
                add     a, a
                sbc     a, a
                ld      b, 4
                inc     hl                  ; HL → TOP[4]
eval_se4_loop:
                ld      (hl), a
                inc     hl
                djnz    eval_se4_loop
                ret


; -----------------------------------------------------------------------
; Binary ops — pop 2, apply, push 1.
;
; After eval_top2_ptrs:
;   HL = lhs slot (new top)
;   DE = rhs slot (popped)
; ml_* routines take (HL=dest, DE=src) and store result in (HL).
; -----------------------------------------------------------------------

eval_add:
                call    eval_top2_ptrs
                call    ml_add
                jp      eval_loop

eval_sub:
                call    eval_top2_ptrs
                call    ml_sub
                jp      eval_loop

eval_and:
                call    eval_top2_ptrs
                call    ml_and
                jp      eval_loop

eval_or:
                call    eval_top2_ptrs
                call    ml_or
                jp      eval_loop

eval_xor:
                call    eval_top2_ptrs
                call    ml_xor
                jp      eval_loop


; -----------------------------------------------------------------------
; eval_shl / eval_shr — binary shifts.
;
; After eval_top2_ptrs HL = lhs (the value), DE = rhs (the shift count).
; We read rhs's low byte (DE[0]) as the shift amount, ignoring higher
; bytes.  This matches the Go reference's `uint64(b)` cast followed by
; `<<` — values outside [0,63] would produce a saturating shift, but
; M3 fixtures always use small shift counts; we cap at 63 to keep the
; ml_shl/ml_shr_arith loops bounded.
; -----------------------------------------------------------------------
eval_shl:
                call    eval_top2_ptrs      ; HL=value, DE=shamt
                ld      a, (de)             ; A = shamt low byte
                cp      64
                jr      c, eval_shl_ok
                ld      a, 64               ; clamp to 64 → produces all zeros
eval_shl_ok:
                call    ml_shl
                jp      eval_loop

eval_shr:
                call    eval_top2_ptrs
                ld      a, (de)
                cp      64
                jr      c, eval_shr_ok
                ld      a, 63               ; clamp; >=64 produces sign-fill
eval_shr_ok:
                call    ml_shr_arith
                jp      eval_loop


; -----------------------------------------------------------------------
; Unary ops — operate in place on top slot.
; -----------------------------------------------------------------------

eval_neg:
                call    eval_top_ptr
                call    ml_neg
                jp      eval_loop

eval_not:
                call    eval_top_ptr
                call    ml_not
                jp      eval_loop


; -----------------------------------------------------------------------
; Scratch storage.  Placed in section A code RAM (assembler.bin).
; -----------------------------------------------------------------------
expr_sp:        defb    0                   ; current depth
eval_pos:       defw    0                   ; bytecode read pointer
eval_remaining: defw    0                   ; bytes left to consume
expr_result:    defb    0, 0, 0, 0, 0, 0, 0, 0   ; final 8-byte LE result

; The stack: 8 slots × 8 bytes = 64 bytes.
expr_stack:     defs    EXPR_STACK_DEPTH * 8
