; slots/extend_op.asm — ExtendOp slot encoder.
;
; Z80 port of tools/aarch64enc/slots_imm.go::encodeExtendOp
; (lines 47-54):
;
;     func encodeExtendOp(slot OperandSlot, ext format.ExtendKind,
;                         shift byte) (uint32, error) {
;         if shift > 4 {
;             return 0, fmt.Errorf("ExtendOp: shift %d > 4", shift)
;         }
;         option := uint32(ext)
;         bits := option<<(slot.BitPosition+3) |
;                 uint32(shift)<<slot.BitPosition
;         return bits, nil
;     }
;
; Encodes the (option:3, imm3:3) pair used by extended-register forms.
;   • option field at bit_position + 3  (3 bits — the extend kind)
;   • imm3   field at bit_position      (3 bits — the optional shift 0..4)
;
; ExtendKind values (tools/sam-aarch64-format/operands.go:92-99):
;   UXTB=0, UXTH=1, UXTW=2, UXTX=3, SXTB=4, SXTH=5, SXTW=6, SXTX=7.
; The Go side does NOT bounds-check ext — it's declared as
; `format.ExtendKind` (byte), and the parser is the gate.  We mirror
; that here: any A in 0..255 is accepted, although well-formed inputs
; only use 0..7.
;
; -----------------------------------------------------------------------
; Calling convention — two-operand small-value form
; -----------------------------------------------------------------------
; ExtendOp takes TWO byte-sized operands (ext, shift).  Both fit in
; registers without going via memory; we choose:
;
;   HL  = pointer to 4-byte slot record (unchanged — HL is consumed by
;         the routine and forms the low word of the DEHL result, same
;         pattern as every other slot encoder).
;   A   = ext value (the option field, 0..7 in well-formed input).
;   C   = shift  value (0..4 per Go's bounds check).
;
; Rationale: the parser will hand both as bytes; A is the obvious choice
; for the first, and C is a free 8-bit register the caller can load via
; a single `ld c, n` after computing the shift.  B is left untouched so
; the caller could use a future BC-pair convention if extended.
;
; Output:
;   DEHL = 32-bit encoded value (DE = bits 16..31, HL = bits 0..15),
;          matching encode_reg / encode_imm12_shifted / encode_imm16_shifted.
;
; Error path:
;   C > 4  →  jp fail  (mirrors Go's shift > 4 check).
;
; Clobbers: A, BC, DE, HL.
;
; Local-label prefix:
;   Per the convention established in slots/xreg.asm, all local labels
;   in this file are prefixed `encode_extend_op_*` to avoid collisions
;   in pyz80's flat symbol space.
; -----------------------------------------------------------------------
encode_extend_op:
; -- Stash ext FIRST: A holds it on entry, but the upcoming shift
;    range-check clobbers A.  Park ext in the scratch byte.
                ld      (encode_extend_op_ext), a

; -- Range check: shift (C) must be in 0..4 -----------------------------
                ld      a, c
                cp      5
                jp      nc, fail           ; C >= 5 → fail (mirrors Go's shift > 4)

; -- Stash shift too (A currently == C == shift) ------------------------
                ld      (encode_extend_op_shift), a

; -- Read bit_position from slot record ---------------------------------
; HL → slot record.  Walk +2 to bit_position byte.  bit_width is not
; consulted: the field-width split (3+3) is hard-coded in this encoder
; (Go side lines 52-53 also ignore slot.BitWidth — the layout is
; fixed).  HL is consumed (becomes the low word of DEHL later), so
; walking it forward is free.
                inc     hl
                inc     hl                 ; HL → bit_position byte
                ld      a, (hl)            ; A = bit_position
                ld      (encode_extend_op_bp), a

; -- Phase 1: build (shift << bit_position) in DEHL ---------------------
; shift is in [0..4] so always fits in the bottom 3 bits.  Seed L with
; shift, zero the rest, then shift DEHL left by bit_position bits.
                ld      a, (encode_extend_op_shift)
                ld      l, a
                ld      h, 0
                ld      d, 0
                ld      e, 0

                ld      a, (encode_extend_op_bp)
                ld      b, a
                inc     b
                dec     b
                jr      z, encode_extend_op_shift_done
encode_extend_op_shift_loop:
                add     hl, hl             ; HL <<= 1; bit 15 → CY
                rl      e
                rl      d
                djnz    encode_extend_op_shift_loop
encode_extend_op_shift_done:

; -- Phase 2: build (ext << (bit_position + 3)) and OR into DEHL --------
; Snapshot the running result on the stack so we can reuse DE/HL as the
; mask register quad.  Same OR-merge pattern as imm12_shifted /
; imm16_shifted.
                push    de                 ; save running result hi (shift part)
                push    hl                 ; save running result lo

                ld      a, (encode_extend_op_ext)
                ld      l, a
                ld      h, 0
                ld      d, 0
                ld      e, 0

                ld      a, (encode_extend_op_bp)
                add     a, 3               ; bit_position + 3
                ld      b, a
                inc     b
                dec     b
                jr      z, encode_extend_op_ext_or
encode_extend_op_ext_loop:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_extend_op_ext_loop

encode_extend_op_ext_or:
; Mask (ext << (bp+3)) is now in DEHL.  OR with the saved running
; result on the stack.
                pop     bc                 ; BC = saved HL (low 16 of result)
                ld      a, l
                or      c
                ld      l, a
                ld      a, h
                or      b
                ld      h, a
                pop     bc                 ; BC = saved DE (high 16 of result)
                ld      a, e
                or      c
                ld      e, a
                ld      a, d
                or      b
                ld      d, a
                ret


; -----------------------------------------------------------------------
; Scratch bytes — bp, ext and shift parked here across the multi-stage
; encode.  Three bytes of section-A code RAM is the cheapest way to
; keep them live across the two shift loops without juggling alternate
; registers.
; -----------------------------------------------------------------------
encode_extend_op_bp:
                defb    0
encode_extend_op_ext:
                defb    0
encode_extend_op_shift:
                defb    0
