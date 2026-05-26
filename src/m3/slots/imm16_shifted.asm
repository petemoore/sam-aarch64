; slots/imm16_shifted.asm — Imm16Shifted slot encoder.
;
; Z80 port of tools/aarch64enc/slots_imm.go::encodeImm16Shifted
; (lines 27-36):
;
;     func encodeImm16Shifted(slot OperandSlot, imm int64, hw byte)
;             (uint32, error) {
;         if imm < 0 || imm >= (1<<16) {
;             return 0, fmt.Errorf("Imm16Shifted: imm %d does not fit
;                                   in 16 bits", imm)
;         }
;         if hw > 3 {
;             return 0, fmt.Errorf("Imm16Shifted: hw %d > 3", hw)
;         }
;         bits := uint32(imm)<<slot.BitPosition |
;                 uint32(hw)<<(slot.BitPosition+16)
;         return bits, nil
;     }
;
; Encodes the (hw:2, imm16:16) pair used by MOVZ/MOVK/MOVN.  hw
; selects the shift: 0 → no shift, 1 → lsl #16, 2 → lsl #32,
; 3 → lsl #48.  hw lives at slot.BitPosition + 16; imm16 at
; slot.BitPosition.
;
; -----------------------------------------------------------------------
; Calling convention — extended for two-operand wide slots
; -----------------------------------------------------------------------
; encode_imm12_shifted (slots/imm12_shifted.asm) established a BCDE
; big-endian convention for slots whose operand exceeds 8 bits.  That
; convention covers SINGLE-VALUE operands up to 32 bits wide.
;
; encode_imm16_shifted is the first encoder whose Go signature takes
; TWO operand values (imm + hw), so a fresh layout is needed.  We
; choose:
;
;   HL    = pointer to 4-byte slot record (unchanged from all encoders
;           — HL is consumed by the routine and forms the low word of
;           the DEHL result).
;   DE    = imm value (16-bit unsigned).  Fits in DE directly: D = high
;           byte, E = low byte.  By construction DE cannot represent
;           values ≥ 2^16, so the Go-side `imm >= (1<<16)` check is
;           structurally satisfied and we do not re-test it.
;   A     = hw selector (0..3).
;
; This is a natural fit for callers: `ld de, imm16; ld a, hw` reads
; directly from the parsed operand pair.
;
; Output:
;   DEHL  = 32-bit encoded value (DE = bits 16..31, HL = bits 0..15),
;           matching encode_reg / encode_imm_n / encode_imm12_shifted.
;
; Error path:
;   A > 3  →  jp fail  (red border + 30 s timeout).
;   imm range: not checked — DE already enforces imm < 2^16.
;
; Clobbers: A, BC, DE, HL.
;
; Local-label prefix:
;   Per the convention established in slots/xreg.asm, all local labels
;   in this file are prefixed `encode_imm16_shifted_*` to avoid
;   collisions in pyz80's flat symbol space.
; -----------------------------------------------------------------------
encode_imm16_shifted:
; -- Range check: hw must fit in 2 bits ---------------------------------
                cp      4
                jp      nc, fail           ; A >= 4 → fail (mirrors Go's hw > 3)

; -- Stash hw and bit_position ------------------------------------------
; HL → slot record.  Walk +2 to bit_position.  We don't read
; bit_width: the imm16 layout is hard-coded (Go side line 34 also
; ignores slot.BitWidth — the value-shaped field width is implied by
; the SlotKind).
;
; HL is consumed (becomes the low word of DEHL later), so walking it
; forward is free.
                ld      (encode_imm16_shifted_hw), a   ; A = hw, park it
                inc     hl
                inc     hl                 ; HL → bit_position byte
                ld      a, (hl)            ; A = bit_position
                ld      (encode_imm16_shifted_bp), a

; -- Phase 1: build imm << bp in DEHL -----------------------------------
; Move imm (currently in DE) into the low half of DEHL:
;   H = D (imm high byte), L = E (imm low byte), D = 0, E = 0
; Then shift DEHL left by bp bits.  Same djnz pattern as
; encode_reg / encode_imm12_shifted.
                ld      h, d
                ld      l, e
                ld      d, 0
                ld      e, 0

                ld      a, (encode_imm16_shifted_bp)
                ld      b, a
                inc     b
                dec     b
                jr      z, encode_imm16_shifted_imm_done
encode_imm16_shifted_imm_shift:
                add     hl, hl             ; HL <<= 1; bit 15 → CY
                rl      e
                rl      d
                djnz    encode_imm16_shifted_imm_shift
encode_imm16_shifted_imm_done:

; -- Phase 2: build hw << (bp+16) in a scratch DEHL and OR in -----------
; The hw bits live at bit position bp+16.  Seed: place hw at bit 16
; (i.e. E = hw, everything else 0), then shift left bp times — the
; same djnz pattern, just starting one byte higher.
;
; Snapshot the running result on the stack while we reuse DE/HL.
                push    de                 ; save running result hi (imm part)
                push    hl                 ; save running result lo

                ld      a, (encode_imm16_shifted_hw)
                ld      e, a               ; E = hw → bit position 16
                ld      d, 0
                ld      h, 0
                ld      l, 0

                ld      a, (encode_imm16_shifted_bp)
                ld      b, a
                inc     b
                dec     b
                jr      z, encode_imm16_shifted_hw_or
encode_imm16_shifted_hw_shift:
                add     hl, hl             ; HL <<= 1; bit 15 → CY
                rl      e
                rl      d
                djnz    encode_imm16_shifted_hw_shift

encode_imm16_shifted_hw_or:
; Mask (hw << (bp+16)) is now in DEHL.  OR with the saved running
; result on the stack.  Same OR-merge pattern as encode_imm12_shifted's
; sh-bit path.
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
; Scratch bytes — bp and hw parked here across the multi-stage encode.
; The Z80 has no spare register pair once HL is consumed (it becomes
; the low word of DEHL) and DE holds the imm value; A flips between
; the hw operand and the bit_position counter.  Parking both in
; section-A code RAM is the cheapest way to keep them live across the
; two shift loops without juggling alternate registers.
; -----------------------------------------------------------------------
encode_imm16_shifted_bp:
                defb    0
encode_imm16_shifted_hw:
                defb    0
