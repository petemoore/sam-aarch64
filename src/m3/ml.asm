; ml.asm — 64-bit multi-byte arithmetic helpers for the expression
; evaluator and slot encoders.
;
; All routines operate on little-endian 8-byte buffers in memory.  No
; helper here trashes IY/SP, but all return clobbering AF, BC, DE, HL.
;
; Calling convention (every routine in this file):
;   HL = pointer to dest 8-byte LE buffer (also the "lhs" — read first,
;        then overwritten with the result).
;   DE = pointer to source 8-byte LE buffer ("rhs", read-only).
;   Result is written to (HL); (DE) is preserved.
;
; Shift routines take HL = dest buffer, A = shift amount (0..63).
; Negative shift amounts are pre-clamped by the caller.
;
; -----------------------------------------------------------------------
; ml_add — (*HL) += (*DE).  64-bit signed/unsigned add (same op).
; -----------------------------------------------------------------------
ml_add:
                push    hl
                push    de
                ld      b, 8                ; 8 bytes
                or      a                   ; CY = 0
ml_add_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    ml_add_loop
                pop     de
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_sub — (*HL) -= (*DE).  64-bit signed/unsigned sub.
;
; Z80 has no `sbc a,(de)` — only `sbc a,(hl)`.  We swap roles
; temporarily inside the loop by EX DE,HL twice per iteration, or by
; loading (de) into a temp register first.  Cheaper here: load (de)
; into the C register, then sbc a,c with carry preserved.
; (`ld c,(de)` does NOT touch flags, and `inc hl/inc de` likewise do
; not touch CY.)
; -----------------------------------------------------------------------
ml_sub:
                push    hl
                push    de
                push    bc
                ld      b, 8
                or      a                   ; CY = 0
ml_sub_loop:
                ld      a, (de)
                ld      c, a                ; C = rhs byte
                ld      a, (hl)
                sbc     a, c                ; A = lhs - rhs - CY
                ld      (hl), a
                inc     hl
                inc     de
                djnz    ml_sub_loop
                pop     bc
                pop     de
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_neg — (*HL) = -(*HL).  Two's-complement negation: invert bits,
; then propagate +1.
; -----------------------------------------------------------------------
ml_neg:
                push    hl
                push    bc
; Phase 1: invert all 8 bytes.
                ld      b, 8
ml_neg_invert:
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                djnz    ml_neg_invert
                pop     bc
                pop     hl
                push    hl
                push    bc
; Phase 2: add 1 with carry propagation.  Seed CY=1 by `scf` and then
; use adc 0 — the first iteration adds 1, subsequent ones propagate.
                ld      b, 8
                scf                         ; CY = 1
ml_neg_carry:
                ld      a, (hl)
                adc     a, 0
                ld      (hl), a
                inc     hl
                djnz    ml_neg_carry
                pop     bc
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_not — (*HL) = ~(*HL).  Bitwise NOT of each byte.
; -----------------------------------------------------------------------
ml_not:
                push    hl
                ld      b, 8
ml_not_loop:
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                djnz    ml_not_loop
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_and / ml_or / ml_xor — bitwise (*HL) op= (*DE).
; -----------------------------------------------------------------------
ml_and:
                push    hl
                push    de
                ld      b, 8
ml_and_loop:
                ld      a, (de)
                and     (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    ml_and_loop
                pop     de
                pop     hl
                ret

ml_or:
                push    hl
                push    de
                ld      b, 8
ml_or_loop:
                ld      a, (de)
                or      (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    ml_or_loop
                pop     de
                pop     hl
                ret

ml_xor:
                push    hl
                push    de
                ld      b, 8
ml_xor_loop:
                ld      a, (de)
                xor     (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    ml_xor_loop
                pop     de
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_shl1 — (*HL) <<= 1.  Single-bit left shift.
; -----------------------------------------------------------------------
ml_shl1:
                push    hl
                ld      b, 8
                or      a                   ; CY = 0
ml_shl1_loop:
                ld      a, (hl)
                rla
                ld      (hl), a
                inc     hl
                djnz    ml_shl1_loop
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_shr1_logical — (*HL) >>= 1.  Single-bit LOGICAL right shift.
; -----------------------------------------------------------------------
ml_shr1_logical:
                push    hl
                ld      bc, 7
                add     hl, bc              ; HL → byte 7 (highest)
                or      a                   ; CY = 0
                ld      b, 8
ml_shr1l_loop:
                ld      a, (hl)
                rra
                ld      (hl), a
                dec     hl
                djnz    ml_shr1l_loop
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_shr1_arith — (*HL) >>= 1.  Single-bit ARITHMETIC right shift.
; Seeds CY with the sign bit before processing the high byte.
; -----------------------------------------------------------------------
ml_shr1_arith:
                push    hl
                ld      bc, 7
                add     hl, bc              ; HL → byte 7
                ld      a, (hl)
                add     a, a                ; CY ← bit 7 (sign)
                ld      a, (hl)
                rra                         ; CY in (sign) → bit 7
                ld      (hl), a
                dec     hl
                ld      b, 7
ml_shr1a_loop:
                ld      a, (hl)
                rra
                ld      (hl), a
                dec     hl
                djnz    ml_shr1a_loop
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_shl — (*HL) <<= A.  Multi-bit logical left shift.
; A in 0..63.  A >= 64 produces zero (caller's responsibility to clamp;
; we shift up to 64 times via the count, but values > 63 in A would
; iterate djnz B with B=A which still works since B is u8).
; -----------------------------------------------------------------------
ml_shl:
                or      a
                ret     z                   ; shift 0 → no-op
                ld      b, a
ml_shl_loop:
                push    bc
                call    ml_shl1
                pop     bc
                djnz    ml_shl_loop
                ret


; -----------------------------------------------------------------------
; ml_shr_arith — (*HL) >>= A.  Multi-bit arithmetic right shift.
; -----------------------------------------------------------------------
ml_shr_arith:
                or      a
                ret     z
                ld      b, a
ml_shr_arith_loop:
                push    bc
                call    ml_shr1_arith
                pop     bc
                djnz    ml_shr_arith_loop
                ret


; -----------------------------------------------------------------------
; ml_zero — fill the 8-byte buffer at HL with zeros.
; -----------------------------------------------------------------------
ml_zero:
                push    hl
                push    de
                push    bc
                ld      (hl), 0
                ld      d, h
                ld      e, l
                inc     de
                ld      bc, 7
                ldir
                pop     bc
                pop     de
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_copy8 — copy 8 bytes from (DE) to (HL).  Both pointers preserved
; (saved on entry, restored before ret).
; Z80's LDIR copies (HL) → (DE) which is the wrong direction for our
; convention; we do a plain byte loop instead.
; -----------------------------------------------------------------------
ml_copy8:
                push    hl
                push    de
                push    bc
                ld      b, 8
ml_copy8_loop:
                ld      a, (de)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    ml_copy8_loop
                pop     bc
                pop     de
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_is_zero — set Z iff the 8-byte LE buffer at HL is all-zero.
; Clobbers A only; HL preserved.
; -----------------------------------------------------------------------
ml_is_zero:
                push    hl
                push    bc
                ld      b, 8
                xor     a
ml_is_zero_loop:
                or      (hl)
                inc     hl
                djnz    ml_is_zero_loop
                or      a                   ; set flags from accumulated OR
                pop     bc
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_mul — (*HL) = (*HL) * (*DE).  64-bit multiply; result is the LOW
; 64 bits of the product.
;
; The low 64 bits of a two's-complement product are identical whether
; the operands are interpreted as signed or unsigned, so a plain
; unsigned shift-and-add over 64 iterations is correct for the signed
; `a * b` the Go evaluators compute (expr.go::applyBinary OpMul →
; `a * b`, which wraps mod 2^64 in int64).  GNU `as` likewise keeps the
; low 64 bits (offsetT multiply).
;
; Algorithm (shift-and-add, LSB-first on the multiplier):
;   A := lhs (multiplicand, will be shifted left each step)
;   B := rhs (multiplier, shifted right each step)
;   result := 0
;   while B != 0:
;       if B bit0: result += A
;       A <<= 1
;       B >>= 1   (logical)
;   *HL := result
;
; Uses scratch buffers ml_mul_a / ml_mul_b / ml_mul_res.
; Clobbers AF, BC, DE, HL.  Result written to (HL); (DE) preserved.
; -----------------------------------------------------------------------
ml_mul:
                push    hl
                push    de
; ml_mul_a := lhs (*HL)
                ld      de, ml_mul_a
                ex      de, hl              ; HL = ml_mul_a, DE = lhs ptr
                call    ml_copy8            ; copies (DE)->(HL): ml_mul_a := lhs
; ml_mul_b := rhs (original *DE) — restore the saved DE first.
                pop     de                  ; DE = rhs ptr (original)
                push    de
                ld      hl, ml_mul_b
                call    ml_copy8            ; ml_mul_b := rhs
; result := 0
                ld      hl, ml_mul_res
                call    ml_zero
; Iterate exactly 64 times.  (No early-out: once the multiplier reaches
; zero, the remaining iterations add nothing and just shift zeros — the
; result is unaffected, and dropping the per-iteration zero test saves
; code-budget bytes.  Runtime cost is bounded and tiny.)
;
; The loop counter lives in a memory byte, NOT a register: every ml_*
; helper documents BC/DE/HL/AF as clobbered (see this file's header),
; and ml_shr1_logical in particular destroys BC via `ld bc,7`.  A
; register counter would be silently corrupted across the calls below.
                ld      a, 64
                ld      (ml_loop_count), a
ml_mul_loop:
; if (ml_mul_b bit0): result += ml_mul_a
                ld      a, (ml_mul_b)       ; low byte of multiplier
                rrca                        ; CY = bit0
                jr      nc, ml_mul_no_add
                ld      hl, ml_mul_res
                ld      de, ml_mul_a
                call    ml_add              ; result += a
ml_mul_no_add:
; a <<= 1
                ld      hl, ml_mul_a
                call    ml_shl1
; b >>= 1 (logical)
                ld      hl, ml_mul_b
                call    ml_shr1_logical
                ld      a, (ml_loop_count)
                dec     a
                ld      (ml_loop_count), a
                jr      nz, ml_mul_loop
ml_mul_done:
; *HL(orig lhs) := result
                pop     de                  ; DE = rhs ptr (discard role)
                pop     hl                  ; HL = original lhs/dest ptr
                push    hl
                ld      de, ml_mul_res
                call    ml_copy8            ; (HL) := result
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_div — (*HL) = (*HL) / (*DE).  Signed 64-bit division, truncating
; toward zero.  Division by zero yields 0.
;
; Grounded in the Go evaluators (expr.go::applyBinary OpDiv):
;   if b == 0 { return 0 }
;   return a / b        ; Go int64 division truncates toward zero
; GNU `as` uses C integer division on 64-bit offsetT, also truncating
; toward zero, so the constants byte-match.
;
; Method:
;   1. if divisor == 0: result := 0, return.
;   2. sign := (sign(a) XOR sign(b)); take |a|, |b|.
;   3. unsigned restoring division: quotient = |a| / |b|, 64 bits.
;   4. if sign negative: quotient := -quotient.
;
; Uses scratch ml_div_n (dividend/|a|), ml_div_d (divisor/|b|),
; ml_div_q (quotient), ml_div_r (remainder), ml_div_sign.
; Clobbers AF, BC, DE, HL.  Result in (HL); (DE) preserved.
; -----------------------------------------------------------------------
ml_div:
                push    hl
                push    de
; ml_div_n := |a| ; ml_div_d := |b| ; compute result sign.
; First copy operands.
                ld      de, ml_div_n
                ex      de, hl              ; HL = ml_div_n, DE = a ptr
                call    ml_copy8            ; ml_div_n := a
                pop     de                  ; DE = b ptr
                push    de
                ld      hl, ml_div_d
                call    ml_copy8            ; ml_div_d := b
; Divide-by-zero check.
                ld      hl, ml_div_d
                call    ml_is_zero
                jp      z, ml_div_byzero    ; jp (not jr): byzero is >127 B away
; Compute sign: bit7 of byte7 of each operand.
                xor     a
                ld      (ml_div_sign), a
                ld      a, (ml_div_n + 7)
                add     a, a                ; CY = sign(a)
                jr      nc, ml_div_a_pos
                ld      a, 1
                ld      (ml_div_sign), a
                ld      hl, ml_div_n
                call    ml_neg              ; |a|
ml_div_a_pos:
                ld      a, (ml_div_d + 7)
                add     a, a                ; CY = sign(b)
                jr      nc, ml_div_b_pos
                ld      a, (ml_div_sign)
                xor     1
                ld      (ml_div_sign), a
                ld      hl, ml_div_d
                call    ml_neg              ; |b|
ml_div_b_pos:
; Unsigned restoring division: |a| / |b| -> quotient (ml_div_q),
; remainder (ml_div_r).
;   q := 0 ; r := 0
;   for i in 63..0:
;       r := (r << 1) | bit_i(n)        ; shift dividend bit into r
;       if r >= d: r -= d ; set bit_i(q)
; We process the dividend MSB-first by shifting n left into CY and r.
                ld      hl, ml_div_q
                call    ml_zero
                ld      hl, ml_div_r
                call    ml_zero
                ld      a, 64               ; loop counter in memory (BC is
                ld      (ml_loop_count), a  ; clobbered by the ml_* helpers)
ml_div_loop:
; q <<= 1 (make room for the new quotient bit at bit0)
                ld      hl, ml_div_q
                call    ml_shl1
; CY := MSB of n ; n <<= 1
                ld      hl, ml_div_n
                call    ml_shl1             ; CY = old bit63 of n
; r := (r << 1) | CY
                ld      hl, ml_div_r
                call    ml_rcl1             ; rotate-left-through-carry, CY in -> bit0
; if r >= d: r -= d ; q bit0 := 1
                ld      hl, ml_div_r
                ld      de, ml_div_d
                call    ml_cmp_ge           ; CY=0 (NC) iff r >= d
                jr      c, ml_div_no_sub
                ld      hl, ml_div_r
                ld      de, ml_div_d
                call    ml_sub              ; r -= d
                ld      hl, ml_div_q
                ld      a, (hl)
                or      1                   ; set quotient bit0
                ld      (hl), a
ml_div_no_sub:
                ld      a, (ml_loop_count)
                dec     a
                ld      (ml_loop_count), a
                jr      nz, ml_div_loop
; Apply sign.
                ld      a, (ml_div_sign)
                or      a
                jr      z, ml_div_store
                ld      hl, ml_div_q
                call    ml_neg
ml_div_store:
                pop     de                  ; DE = b ptr (discard)
                pop     hl                  ; HL = dest (orig a ptr)
                push    hl
                ld      de, ml_div_q
                call    ml_copy8
                pop     hl
                ret
ml_div_byzero:
                pop     de
                pop     hl
                push    hl
                call    ml_zero             ; (HL) := 0
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_rcl1 — rotate the 8-byte LE buffer at HL left by 1 THROUGH carry:
; the incoming CY becomes bit0; the old bit63 is shifted out into CY.
; (Used by ml_div to feed the next dividend bit into the remainder.)
; HL preserved.
; -----------------------------------------------------------------------
ml_rcl1:
                push    hl
                push    bc
                ld      b, 8
ml_rcl1_loop:
                ld      a, (hl)
                rla                         ; CY -> bit0, bit7 -> CY
                ld      (hl), a
                inc     hl
                djnz    ml_rcl1_loop
                pop     bc
                pop     hl
                ret


; -----------------------------------------------------------------------
; ml_cmp_ge — compare two 8-byte UNSIGNED LE values (*HL) and (*DE).
; Returns CY clear (NC) iff (*HL) >= (*DE); CY set iff (*HL) < (*DE).
; Both buffers preserved; clobbers AF, BC.
;
; Unsigned compare by full-width subtraction (*HL) - (*DE), LSB->MSB,
; with results discarded.  The borrow out of the MSB (final CY) is set
; iff (*HL) < (*DE).  This is the standard multi-byte unsigned compare.
; -----------------------------------------------------------------------
ml_cmp_ge:
                push    hl
                push    de
                push    bc
                or      a                   ; CY = 0
                ld      b, 8
ml_cmp_ge_loop:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                sbc     a, c                ; (*HL)byte - (*DE)byte - borrow
                inc     hl
                inc     de
                djnz    ml_cmp_ge_loop
; CY now reflects the final borrow: set iff (*HL) < (*DE).
                pop     bc
                pop     de
                pop     hl
                ret


; -----------------------------------------------------------------------
; Scratch buffers for ml_mul / ml_div (section C code RAM).
;
; ml_mul and ml_div are never active simultaneously (the expression
; evaluator calls exactly one per binary-op step, and neither recurses),
; so the two routines share the same 4×8-byte scratch region to save
; code-budget bytes.  ml_div needs 4 buffers (n, d, q, r); ml_mul needs
; 3 (a, b, res); the div names alias the mul names.
; -----------------------------------------------------------------------
ml_mul_a:                           ; ml_div_n alias
ml_div_n:       defs    8
ml_mul_b:                           ; ml_div_d alias
ml_div_d:       defs    8
ml_mul_res:                         ; ml_div_q alias
ml_div_q:       defs    8
ml_div_r:       defs    8
ml_div_sign:    defb    0
ml_loop_count:  defb    0           ; shared mul/div iteration counter
