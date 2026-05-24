; slots/xreg.asm — Xreg / Wreg / XregOrSp / WregOrSp slot encoder.
;
; Z80 port of tools/aarch64enc/slots_trivial.go::encodeReg (lines 9-14).
; The Go body is shared by all four register slot kinds (Xreg=0x01,
; Wreg=0x02, XregOrSp=0x03, WregOrSp=0x04): the SP-vs-ZR distinction
; is the parser's job via the slot's ExpectedKind, and at encode time
; value 31 is the same five bits.
;
; Slot-record byte layout (4 bytes, per
; docs/specs/2026-05-24-m2-encoder-tables-design.md §2 lines 71-75):
;   offset 0  slot_kind
;   offset 1  expected_kind
;   offset 2  bit_position
;   offset 3  bit_width
;
; Local-label naming convention:
;   Local labels within a per-slot encoder file MUST be prefixed with
;   the routine name (e.g. encode_reg_shift, encode_reg_done) — pyz80's
;   flat symbol space means a bare `loop:` in two encoder files would
;   collide at link time.  Future encoders (imm_small, etc.) must
;   follow the same prefix rule.
;
; -----------------------------------------------------------------------
; encode_reg — pack a 5-bit register index at slot.BitPosition.
;
; Input:
;   HL = pointer to 4-byte slot record (caller's responsibility to point
;        at a record whose slot_kind is one of Xreg/Wreg/XregOrSp/WregOrSp).
;   A  = register index (0..31).
;
; Output:
;   DEHL = 32-bit encoded value, zero-extended, with D:E the high 16
;          bits and H:L the low 16 bits.  This partitioning mirrors the
;          Z80 convention HL=low / DE=high for 32-bit values and lets a
;          future instruction-word accumulator OR multiple slot results
;          into a working DE:HL pair without re-shuffling.
;
;   HL (slot pointer) is consumed by the routine — on return it forms
;   the low 16 bits of the DEHL result.  Callers that still need the
;   slot pointer must save it themselves.
;
; Error path:
;   A > 31  →  jp fail  (one-way; red border + halt).
;   The Go-side parser enforces A ≤ 31, but we mirror the validation
;   (slots_trivial.go:10) so a corrupt enctab.enc cannot silently emit
;   garbage.
;
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
encode_reg:
; -- Range check: A must fit in 5 bits ----------------------------------
                cp      32
                jp      nc, fail       ; A >= 32  ⇒  jp fail (CY clear after cp 32 means A>=32)

; -- Fetch bit_position (slot record offset 2) into B -------------------
; HL is consumed: per the calling convention above, it becomes the low
; word of the DEHL result, so we don't need to preserve the slot ptr.
                inc     hl
                inc     hl             ; hl -> bit_position byte
                ld      b, (hl)        ; B = shift count

; -- Initialise DEHL = 0x000000AA where AA = the register byte ---------
                ld      l, a
                ld      h, 0
                ld      d, 0
                ld      e, 0

; -- Shift DEHL left by B bits ------------------------------------------
; If B == 0 (bit_position 0) we must skip the loop entirely; djnz would
; otherwise wrap and shift 256 times.  Use `inc b / dec b / jr z` to
; test for zero without disturbing the result word.
                inc     b
                dec     b
                jr      z, encode_reg_done
encode_reg_shift:
                add     hl, hl         ; HL <<= 1; bit 15 -> CY
                rl      e              ; E = (E<<1) | CY ; new bit 15 of low32 -> CY
                rl      d              ; D = (D<<1) | CY
                djnz    encode_reg_shift
encode_reg_done:
                ret
