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
