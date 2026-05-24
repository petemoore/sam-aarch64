; slots/imm_small.asm — Imm5 / Imm6 / CondCode / ShiftAmount slot
; encoders.
;
; Z80 port of tools/aarch64enc/slots_trivial.go::encodeImmN (lines
; 18-24) and ::encodeCond (lines 27-32), plus the trivial
; ::encodeShiftAmount wrapper in tools/aarch64enc/slots_imm.go
; (lines 40-42) which simply delegates to encodeImmN.
;
; All three Go functions share one body on the Z80 side: a plain
; unsigned N-bit pack with a range-check derived from slot.BitWidth.
; The Go side keeps encodeCond as a separate function to surface a
; tailored error string and because the future dispatch table
; (M3 Task 18) will index by SlotKind into a per-kind entry-symbol
; table — so we expose encode_cond as its own entry label that
; jp-tails into encode_imm_n.  encodeShiftAmount has no separate
; entry: the dispatch table for SlotKind=ShiftAmount (0x12) will
; route directly to encode_imm_n.
;
; SlotKind values handled here (per tools/aarch64enc/types.go
; lines 20-22 and 26):
;   Imm5        = 0x05  (bit_width = 5)
;   Imm6        = 0x06  (bit_width = 6)
;   CondCode    = 0x07  (bit_width = 4)
;   ShiftAmount = 0x12  (bit_width = 6, typical)
;
; Slot-record byte layout (4 bytes, per
; docs/specs/2026-05-24-m2-encoder-tables-design.md §2 lines 71-75):
;   offset 0  slot_kind
;   offset 1  expected_kind
;   offset 2  bit_position
;   offset 3  bit_width
;
; Local-label naming convention (established in slots/xreg.asm):
;   Local labels within a per-slot encoder file MUST be prefixed
;   with the routine name — pyz80 has a flat symbol space.
;
; -----------------------------------------------------------------------
; encode_cond — pack a 4-bit condition code at slot.BitPosition.
;
; Identical to encode_imm_n in body: CondCode slots always have
; BitWidth=4, so the bit-width-driven range check (A >= 16 → fail)
; matches Go's hard-coded `cc > 15` test.  Kept as a distinct entry
; symbol so the Task-18 dispatcher can route SlotKind=0x07 directly
; here without inspecting BitWidth.
;
; Input/output/clobbers: see encode_imm_n below.
; -----------------------------------------------------------------------
encode_cond:
                jp      encode_imm_n   ; fall through to the shared body
                                       ; (kept as a 3-byte jp for clarity
                                       ; in the dispatch table; future
                                       ; tuning may inline if hot)

; -----------------------------------------------------------------------
; encode_imm_n — pack an unsigned bit_width-bit value at bit_position.
;
; Shared body for Imm5 (BW=5), Imm6 (BW=6), CondCode (BW=4 — via
; encode_cond above) and ShiftAmount (BW=6 typical).
;
; Input:
;   HL = pointer to 4-byte slot record (caller's responsibility to
;        point at a slot whose slot_kind is one of the four above).
;   A  = unsigned value to encode.  0..255 covers every slot
;        encodable by this routine (max BW handled = 7 → max value
;        127, see "Width limit" below).
;
; Output:
;   DEHL = 32-bit encoded value, zero-extended.  DE = bits 16..31,
;          HL = bits 0..15 (matches encode_reg's convention so a
;          future word accumulator can OR slot results together).
;
;   HL (slot pointer) is consumed: on return it forms the low 16
;   bits of the DEHL result.  Callers that need it must save it.
;
; Error path:
;   A >= (1 << bit_width)  →  jp fail  (red border + halt).
;   Mirrors the Go-side check (slots_trivial.go:20) so a corrupt
;   enctab.enc cannot silently emit garbage.
;
; Width limit:
;   The mask (1 << bit_width) is computed in the 8-bit A register,
;   so this routine handles bit_width up to 7 (mask 128).  The four
;   SlotKinds it serves use widths 4, 5, 6 — well within range.
;   Wider slots (Imm12Shifted, Imm16Shifted, BranchImm*) have their
;   own dedicated encoders and never reach this routine.
;
; Clobbers: A, BC, DE, HL.
;
; Note: B is used as a djnz counter for both the mask-build loop
; and the bit-position shift loop.  Neither loop calls into PTDOS
; (RST 8 dispatch), so the established "don't hold B across RST 8"
; caveat (see src/sam_io.inc) does not apply here.
; -----------------------------------------------------------------------
encode_imm_n:
; -- Save the value before we start clobbering registers ----------------
                push    af             ; preserve value (A) on the stack

; -- Read bit_position and bit_width from the slot record ---------------
; HL is consumed (becomes the low word of DEHL later), so we walk it
; forward two bytes for bit_position, one more for bit_width.
                inc     hl
                inc     hl             ; hl -> bit_position byte
                ld      c, (hl)        ; C = bit_position (saved for the shift loop)
                inc     hl             ; hl -> bit_width byte
                ld      b, (hl)        ; B = bit_width  (djnz counter for mask build)

; -- Build mask = (1 << bit_width) in A ---------------------------------
; Starting from A=1, shift left bit_width times.  For bit_width=4
; → A=16; bit_width=5 → A=32; bit_width=6 → A=64.  See "Width limit"
; above for why this fits in 8 bits.
                ld      a, 1
encode_imm_n_mask_loop:
                add     a, a           ; mask <<= 1
                djnz    encode_imm_n_mask_loop
                ld      e, a           ; E = mask (parked while we recover the value)

; -- Range check: value (A) must be strictly less than mask (E) ---------
                pop     af             ; restore value into A
                cp      e              ; A - E:  CY clear  ⇔  A >= E
                jp      nc, fail       ; value >= mask  →  jp fail

; -- Initialise DEHL = 0x000000VV where VV = the value byte -------------
                ld      l, a
                ld      h, 0
                ld      d, 0
                ld      e, 0

; -- Shift DEHL left by C (bit_position) bits ---------------------------
; Skip the loop entirely when bit_position == 0 (the inc/dec/jr-z
; idiom mirrors encode_reg in slots/xreg.asm).
                ld      b, c           ; B = bit_position (djnz counter)
                inc     b
                dec     b
                jr      z, encode_imm_n_done
encode_imm_n_shift:
                add     hl, hl         ; HL <<= 1; bit 15 -> CY
                rl      e              ; E = (E<<1) | CY
                rl      d              ; D = (D<<1) | CY
                djnz    encode_imm_n_shift
encode_imm_n_done:
                ret
