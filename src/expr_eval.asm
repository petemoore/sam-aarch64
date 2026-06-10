; expr_eval.asm — constant-only expression bytecode evaluator.
;
; Z80 port of tools/sam-aarch64-format/expr.go::EvalConst and
; tools/aarch64enc/expr.go::Eval for the full M4 opcode set.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m3-z80-emitter-design.md §2.5 and
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.5:
;   • M3 subset (constant-only): PUSH_IMM8/16/32/64, ADD, SUB, AND, OR,
;     XOR, SHL, SHR, NEG, NOT.
;   • M4 additions: PUSH_SYM, PUSH_LOCAL, PUSH_PC, REL_LO12, REL_HI12,
;     REL_ABS_G0..G3 (and their _NC variants — same eval semantics, the
;     suffix only affects linker-relocation tracking which doesn't change
;     emitted bytes).
;   • M6 closure (PR-3c) additions: MUL (0x12), DIV (0x13).  These reach
;     the SAM evaluator whenever an operand is symbolic (text2bin only
;     constant-folds all-literal expressions), e.g. release-stripped's
;     `PUSH_SYM .. SUB PUSH_IMM8 MUL` streams.  Semantics match the Go
;     evaluators (expr.go::applyBinary / aarch64enc/expr.go): signed
;     64-bit `a*b` (low 64 bits) and signed `a/b` truncating toward
;     zero, div-by-zero → 0.  Implemented via ml_mul / ml_div.
;
; Opcode values (tools/sam-aarch64-format/expr.go lines 12-66; full
; normative table in docs/specs/tbn-binary-format-reference.md §5):
;
;   0x01 PUSH_IMM8        [s8]
;   0x02 PUSH_IMM16       [s16 LE]
;   0x03 PUSH_IMM32       [s32 LE]
;   0x04 PUSH_IMM64       [s64 LE]
;   0x05 PUSH_SYM         [u16 symbol_id]
;   0x06 PUSH_LOCAL       [u8 digit, u8 dir]     ; dir=0 fwd, dir=1 back
;   0x07 PUSH_PC
;
;   0x10 ADD              (binary)
;   0x11 SUB              (binary)
;   0x12 MUL              (binary; signed 64-bit, low 64 bits)
;   0x13 DIV              (binary; signed truncating, div-by-zero → 0)
;   0x14 AND              (binary)
;   0x15 OR               (binary)
;   0x16 XOR              (binary)
;   0x17 SHL              (binary: a << b)
;   0x18 SHR              (binary: a >> b, arithmetic)
;
;   0x20 NEG              (unary)
;   0x21 NOT              (unary)
;
;   0x30 REL_LO12         top  =  top         & 0xFFF
;   0x31 REL_HI12         top  = (top >> 12)  & 0xFFF
;   0x32 REL_ABS_G0       top  =  top         & 0xFFFF
;   0x33 REL_ABS_G0_NC    (alias of REL_ABS_G0)
;   0x34 REL_ABS_G1       top  = (top >> 16)  & 0xFFFF
;   0x35 REL_ABS_G1_NC    (alias of REL_ABS_G1)
;   0x36 REL_ABS_G2       top  = (top >> 32)  & 0xFFFF
;   0x37 REL_ABS_G2_NC    (alias of REL_ABS_G2)
;   0x38 REL_ABS_G3       top  = (top >> 48)  & 0xFFFF
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
                jp      z, eval_done

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
                cp      &05
                jp      z, eval_push_sym
                cp      &06
                jp      z, eval_push_local
                cp      &07
                jp      z, eval_push_pc
                cp      &10
                jp      z, eval_add
                cp      &11
                jp      z, eval_sub
                cp      &12
                jp      z, eval_mul
                cp      &13
                jp      z, eval_div
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
                cp      &30
                jp      z, eval_rel_lo12
                cp      &31
                jp      z, eval_rel_hi12
                cp      &32
                jp      z, eval_rel_abs_g0
                cp      &33
                jp      z, eval_rel_abs_g0      ; _NC alias
                cp      &34
                jp      z, eval_rel_abs_g1
                cp      &35
                jp      z, eval_rel_abs_g1      ; _NC alias
                cp      &36
                jp      z, eval_rel_abs_g2
                cp      &37
                jp      z, eval_rel_abs_g2      ; _NC alias
                cp      &38
                jp      z, eval_rel_abs_g3
; Everything else (MUL/DIV) — unsupported in the constant evaluator.
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
                jp      c, fail
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

; MUL/DIV — opcodes 0x12/0x13.  After eval_top2_ptrs HL=lhs (new top),
; DE=rhs (popped).  ml_mul/ml_div take (HL=dest/lhs, DE=src/rhs) and
; leave the 64-bit result in (HL).  Reference: ml.asm headers +
; tools/sam-aarch64-format/expr.go::applyBinary.
eval_mul:
                call    eval_top2_ptrs
                call    ml_mul
                jp      eval_loop

eval_div:
                call    eval_top2_ptrs
                call    ml_div
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
; eval_push_sym — opcode 0x05.  Resolve a u16 symbol_id (read inline from
; the bytecode stream) via symbol_lookup; push the 32-bit address as a
; zero-extended s64 onto the eval stack.
;
; On miss (symbol not in table) → jp fail.  This matches the
; aarch64enc/expr.go Eval reference's "undefined symbol" error.  text2bin
; only emits PUSH_SYM for identifiers that are known to be defined; an
; emitted PUSH_SYM whose target is missing at pass 2 indicates either a
; mid-assembly symbol-table corruption or a text2bin bug.  Failing here
; is preferable to producing wrong bytes silently.
;
; Bytecode operand:
;   [0..1]  symbol_id u16 LE
;
; eval_take_bytes copies the operand into the freshly-allocated top slot
; (which we zero-fill via eval_alloc_top, so the 6 bytes above the id
; are zero — exactly the right shape for symbol_lookup's `HL = id`
; calling convention to pick up the id from the slot's low 2 bytes).
;
; Reference: tools/aarch64enc/expr.go::Eval (case format.OpPushSym).
; -----------------------------------------------------------------------
eval_push_sym:
; Read the 2-byte symbol id into a scratch slot first; doing the read
; before allocating the eval-stack slot keeps register juggling minimal
; (symbol_lookup clobbers HL/BC/DE/A, so any slot pointer we held would
; need to be saved across the call anyway).
                ld      hl, eval_sym_operand
                ld      b, 2
                call    eval_take_bytes

                ld      hl, (eval_sym_operand)      ; HL = id (LE u16 at scratch)
                call    symbol_lookup
                jp      c, fail
; Hit: symbol_value_buf[0..3] holds the 32-bit address LE.  Allocate the
; eval-stack slot now and write address into slot[0..3], zero slot[4..7].
                call    eval_alloc_top              ; HL = new top slot
                ld      a, (symbol_value_buf + 0)
                ld      (hl), a
                inc     hl
                ld      a, (symbol_value_buf + 1)
                ld      (hl), a
                inc     hl
                ld      a, (symbol_value_buf + 2)
                ld      (hl), a
                inc     hl
                ld      a, (symbol_value_buf + 3)
                ld      (hl), a
                inc     hl                          ; HL → slot[4]
; Reconstruct the high word.  Origin-relative symbols (labels, PC
; snapshots, label-derived .set) get ORIGIN_HIGH re-applied so e.g.
; `.quad <label>` emits the full 64-bit address; ABSOLUTE symbols (a
; `.set`/`.equ` constant such as RAM_DISK_SIZE) get high word 0 — applying
; ORIGIN_HIGH to those broke `mov x9, RAM_DISK_SIZE` (0xfffffff0_10000000
; vs 0x10000000).  The per-id flag is set in main_dir_equ_pass1 by
; comparing the evaluated high word against ORIGIN_HIGH.  See
; assembler.asm SYMTAB_ABS_BITMAP.  Faithful to Go (tools/refenc:
; Symbols[name] holds the full value, eval adds no origin — pass1.go:154,
; pass1.go:296; pass2.go:150).
                push    hl                          ; save slot[4] ptr
                ld      hl, (eval_sym_operand)      ; HL = id
                call    symbol_is_absolute          ; Z=1 origin-rel, NZ absolute
                pop     hl                          ; HL = slot[4] ptr
                jr      nz, eval_push_sym_zero_high
                call    eval_store_origin_high      ; slot[4..7] := ORIGIN_HIGH
                jp      eval_loop
eval_push_sym_zero_high:
                xor     a                           ; absolute → slot[4..7] := 0
                ld      (hl), a
                inc     hl
                ld      (hl), a
                inc     hl
                ld      (hl), a
                inc     hl
                ld      (hl), a
                jp      eval_loop


; -----------------------------------------------------------------------
; eval_push_local — opcode 0x06.  Resolve a per-digit local label via
; local_find_forward (dir=0) or local_find_backward (dir=1), using the
; current PASS_PC as the reference PC.  Push the resolved 32-bit PC as
; zero-extended s64.
;
; Bytecode operand:
;   [0]  digit  u8  (1..9)
;   [1]  dir    u8  (0 = forward, 1 = backward)
;
; Reference: tools/aarch64enc/expr.go::Eval (case format.OpPushLocal).
; The ref_pc semantics for f/b are per the local-label spec:
;   * Nf : smallest PC strictly greater than ref_pc
;   * Nb : largest  PC less than or equal to ref_pc
; ref_pc = PASS_PC (PC of the instruction currently being assembled).
; -----------------------------------------------------------------------
eval_push_local:
; Read the 2-byte operand (digit, dir) directly into a scratch pair so
; we don't have to juggle the eval-stack slot pointer through the find
; call (find clobbers HL/BC/DE/A).
                ld      hl, eval_local_operand
                ld      b, 2
                call    eval_take_bytes

; Seed local_label_pc_buf with PASS_PC (4-byte LE).
                ld      a, (PASS_PC + 0)
                ld      (local_label_pc_buf + 0), a
                ld      a, (PASS_PC + 1)
                ld      (local_label_pc_buf + 1), a
                ld      a, (PASS_PC + 2)
                ld      (local_label_pc_buf + 2), a
                ld      a, (PASS_PC + 3)
                ld      (local_label_pc_buf + 3), a

; Dispatch on dir.
                ld      a, (eval_local_operand + 1) ; A = dir
                or      a
                jr      z, eval_push_local_forward
                cp      1
                jr      z, eval_push_local_backward
                jp      fail


eval_push_local_forward:
                ld      a, (eval_local_operand + 0) ; A = digit
                call    local_find_forward
                jr      eval_push_local_check

eval_push_local_backward:
                ld      a, (eval_local_operand + 0) ; A = digit
                call    local_find_backward

eval_push_local_check:
                jp      c, fail
; Allocate a stack slot AFTER the find call (which clobbered HL/DE) and
; write resolved PC into slot[0..3], zero slot[4..7].
                call    eval_alloc_top              ; HL = new top slot
                ld      a, (local_label_pc_buf + 0)
                ld      (hl), a
                inc     hl
                ld      a, (local_label_pc_buf + 1)
                ld      (hl), a
                inc     hl
                ld      a, (local_label_pc_buf + 2)
                ld      (hl), a
                inc     hl
                ld      a, (local_label_pc_buf + 3)
                ld      (hl), a
                inc     hl                          ; HL → slot[4]
; Local-label values are origin-relative PCs; apply ORIGIN_HIGH like the
; named-symbol path so 64-bit materialisations carry the full VMA.
                call    eval_store_origin_high      ; slot[4..7] := ORIGIN_HIGH
                jp      eval_loop


; -----------------------------------------------------------------------
; eval_push_pc — opcode 0x07.  Push PASS_PC (4-byte LE u32) as a
; zero-extended s64 onto the eval stack.  No inline operand.
;
; PASS_PC is the pass-2 PC counter maintained by walk_records; it equals
; the PC of the instruction currently being assembled (NOT the address
; after this instruction).  Reference: tools/aarch64enc/expr.go::Eval
; (case format.OpPushPC).
; -----------------------------------------------------------------------
eval_push_pc:
                call    eval_alloc_top              ; HL = new top slot
                ld      a, (PASS_PC + 0)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 1)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 2)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 3)
                ld      (hl), a
                inc     hl                          ; HL → slot[4]
                call    eval_store_origin_high      ; slot[4..7] := ORIGIN_HIGH
                jp      eval_loop


; -----------------------------------------------------------------------
; eval_store_origin_high — write the 4-byte ORIGIN_HIGH (high word of the
; link OriginVMA) into the eval-stack slot's bytes [4..7], sign/origin-
; extending the just-pushed origin-relative 32-bit value to the full
; 64-bit VMA.
;
; ORIGIN_HIGH is 0 unless a leading `.org` set a >4 GB origin (the
; spectrum4 release links at 0xfffffff0_00000000), so on the `-Ttext=0`
; fixture corpus this is identical to the previous zero-fill.
;
; Used by the three origin-relative pushes — symbol (eval_push_sym), PC
; (eval_push_pc), and local-label (eval_push_local) — mirroring the Go
; reference where every such value carries the full 64-bit OriginVMA
; (tools/refenc/pass2.go:148-152 makeCtx Symbol/PC/LocalLabel).
;
; Input:  HL → slot[4].  Output: slot[4..7] = ORIGIN_HIGH.  HL advanced
;         to slot[8].  Clobbers: A, B, DE.
eval_store_origin_high:
                ld      de, ORIGIN_HIGH
                ld      b, 4
eval_store_origin_high_loop:
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                djnz    eval_store_origin_high_loop
                ret


; -----------------------------------------------------------------------
; REL_* opcodes — mask/shift the top-of-stack value in place.  The _NC
; variants share an implementation with their non-NC counterparts
; (different opcodes route to the same handler in the dispatch above);
; the suffix only affects linker overflow tracking, not the emitted
; bytes.  Reference: tools/aarch64enc/expr.go::Eval lines 76-87.
;
; Byte-shift trick: shifts by 16, 32, 48 are exact byte-aligned moves —
; we copy the requested byte pair into slot[0..1] and zero slot[2..7].
; Shifts by 12 (REL_LO12, REL_HI12) need bit work but operate on only
; the low 24 bits of the source.
; -----------------------------------------------------------------------

; eval_rel_lo12 — top = top & 0xFFF.  Keep low 12 bits, zero rest.
eval_rel_lo12:
                call    eval_top_ptr                ; HL = top slot
                inc     hl                          ; HL → slot[1]
                ld      a, (hl)
                and     &0F                         ; mask high nibble of byte 1
                ld      (hl), a
                inc     hl                          ; HL → slot[2]
                ld      b, 6
                xor     a
eval_rel_lo12_zero:
                ld      (hl), a
                inc     hl
                djnz    eval_rel_lo12_zero
                jp      eval_loop


; eval_rel_hi12 — top = (top >> 12) & 0xFFF.
;
; The 12-bit result occupies bits 12..23 of the source.  Bytes 1 and 2
; of the source contain those bits:
;   source byte 1 = src[8..15]  → result bits [0..3] come from src[12..15]
;   source byte 2 = src[16..23] → result bits [4..11] come from src[16..23]
; So result byte 0 = (src[1] >> 4) | (src[2] << 4)    (low 8 bits of result)
;    result byte 1 = (src[2] >> 4) & 0x0F             (high 4 bits of result)
; Result bytes 2..7 = 0.
; -----------------------------------------------------------------------
eval_rel_hi12:
                call    eval_top_ptr                ; HL = top slot[0]
                push    hl                          ; preserve slot[0] ptr
                inc     hl                          ; HL → slot[1] (src[1])
                ld      a, (hl)                     ; A = src[1]
                rrca
                rrca
                rrca
                rrca
                and     &0F                         ; A = src[1] >> 4 (low nibble)
                ld      c, a                        ; C = low nibble of result byte 0
                inc     hl                          ; HL → slot[2] (src[2])
                ld      a, (hl)                     ; A = src[2]
                ld      d, a                        ; D = src[2] (save for byte-1 calc)
                rlca
                rlca
                rlca
                rlca                                ; A = src[2] rotated by 4 (nibbles swapped)
                and     &F0                         ; high nibble of result byte 0 (= src[2] low nibble << 4)
                or      c                           ; combine with low nibble from src[1] >> 4
                pop     hl                          ; HL = slot[0]
                ld      (hl), a                     ; slot[0] = result byte 0
                inc     hl                          ; HL → slot[1]
                ld      a, d                        ; A = src[2]
                rrca
                rrca
                rrca
                rrca
                and     &0F                         ; A = src[2] >> 4 = result byte 1
                ld      (hl), a                     ; slot[1] = result byte 1
                inc     hl                          ; HL → slot[2]
                ld      b, 6
                xor     a
eval_rel_hi12_zero:
                ld      (hl), a
                inc     hl
                djnz    eval_rel_hi12_zero
                jp      eval_loop


; eval_rel_abs_g0 — top = top & 0xFFFF.  Keep low 16 bits.
eval_rel_abs_g0:
                call    eval_top_ptr                ; HL = top slot
                ld      bc, 2
                add     hl, bc                      ; HL → slot[2]
                ld      b, 6
                xor     a
eval_rel_abs_g0_zero:
                ld      (hl), a
                inc     hl
                djnz    eval_rel_abs_g0_zero
                jp      eval_loop


; eval_rel_abs_g1 — top = (top >> 16) & 0xFFFF.
; Source bytes 2,3 → dest bytes 0,1; rest = 0.
eval_rel_abs_g1:
                call    eval_top_ptr                ; HL = slot[0]
                push    hl                          ; preserve slot base
                ld      bc, 2
                add     hl, bc                      ; HL → slot[2]
                ld      a, (hl)                     ; A = src[2]
                pop     de                          ; DE = slot[0]
                ld      (de), a
                inc     hl                          ; HL → src[3]
                inc     de                          ; DE → dst[1]
                ld      a, (hl)
                ld      (de), a
                inc     de                          ; DE → dst[2]
                ex      de, hl                      ; HL = dst[2]
                ld      b, 6
                xor     a
eval_rel_abs_g1_zero:
                ld      (hl), a
                inc     hl
                djnz    eval_rel_abs_g1_zero
                jp      eval_loop


; eval_rel_abs_g2 — top = (top >> 32) & 0xFFFF.
; Source bytes 4,5 → dest bytes 0,1; rest = 0.
eval_rel_abs_g2:
                call    eval_top_ptr                ; HL = slot[0]
                push    hl                          ; preserve slot base
                ld      bc, 4
                add     hl, bc                      ; HL → slot[4]
                ld      a, (hl)                     ; A = src[4]
                pop     de                          ; DE = slot[0]
                ld      (de), a
                inc     hl                          ; HL → src[5]
                inc     de                          ; DE → dst[1]
                ld      a, (hl)
                ld      (de), a
                inc     de                          ; DE → dst[2]
                ex      de, hl                      ; HL = dst[2]
                ld      b, 6
                xor     a
eval_rel_abs_g2_zero:
                ld      (hl), a
                inc     hl
                djnz    eval_rel_abs_g2_zero
                jp      eval_loop


; eval_rel_abs_g3 — top = (top >> 48) & 0xFFFF.
; Source bytes 6,7 → dest bytes 0,1; rest = 0.
eval_rel_abs_g3:
                call    eval_top_ptr                ; HL = slot[0]
                push    hl                          ; preserve slot base
                ld      bc, 6
                add     hl, bc                      ; HL → slot[6]
                ld      a, (hl)                     ; A = src[6]
                pop     de                          ; DE = slot[0]
                ld      (de), a
                inc     hl                          ; HL → src[7]
                inc     de                          ; DE → dst[1]
                ld      a, (hl)
                ld      (de), a
                inc     de                          ; DE → dst[2]
                ex      de, hl                      ; HL = dst[2]
                ld      b, 6
                xor     a
eval_rel_abs_g3_zero:
                ld      (hl), a
                inc     hl
                djnz    eval_rel_abs_g3_zero
                jp      eval_loop


; -----------------------------------------------------------------------
; Scratch storage.  Placed in section A code RAM (assembler.bin).
; -----------------------------------------------------------------------
expr_sp:        defb    0                   ; current depth
eval_pos:       defw    0                   ; bytecode read pointer
eval_remaining: defw    0                   ; bytes left to consume
expr_result:    defb    0, 0, 0, 0, 0, 0, 0, 0   ; final 8-byte LE result

; Operand scratch for the M4 push opcodes — separates the bytecode
; operand bytes from the eval-stack slot so we don't have to preserve
; an in-slot pointer across the resolve calls (which clobber HL/BC/DE).
eval_sym_operand:   defw    0               ; PUSH_SYM: symbol_id u16
eval_local_operand: defb    0, 0            ; PUSH_LOCAL: digit u8, dir u8

; The stack: 8 slots × 8 bytes = 64 bytes.
expr_stack:     defs    EXPR_STACK_DEPTH * 8
