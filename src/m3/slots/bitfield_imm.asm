; slots/bitfield_imm.asm — BitfieldImm slot encoders (BFI + UBFX).
;
; Z80 port of:
;   tools/aarch64enc/slots_bitfield.go::encodeBitfieldBFI   (lines 5-20)
;   tools/aarch64enc/slots_bitfield.go::encodeBitfieldUBFX  (lines 23-33)
;
; BFI alias rule:
;     immr = (-lsb) & (regsize - 1)
;     imms = width - 1
;
; UBFX rule:
;     immr = lsb
;     imms = lsb + width - 1
;
; Both pack (immr<<immr_slot.BitPosition) | (imms<<imms_slot.BitPosition).
; SlotKind handled here: BitfieldImm = 0x25.
;
; -----------------------------------------------------------------------
; Calling convention — two-slot two-value form
; -----------------------------------------------------------------------
; BitfieldImm is the first encoder whose Go signature takes TWO operand
; slots (immr_slot, imms_slot).  The Z80 form packs both slot records
; consecutively into one 8-byte combined record, with immr_slot at
; offset 0 and imms_slot at offset 4.  HL points at the combined record.
;
;   HL = pointer to 8-byte combined slot record:
;          bytes 0..3 = immr_slot (slot_kind, expected_kind, bp, bw)
;          bytes 4..7 = imms_slot (slot_kind, expected_kind, bp, bw)
;        Both slots' SlotKind/BitWidth fields are not consulted (the
;        Go side reads neither — only BitPosition matters), but the
;        layout convention is preserved for consistency with other
;        encoders.
;   B  = lsb   (0..63 for BFI; 0..63 for UBFX)
;   C  = width (1..64 for BFI;  1..(64-lsb) for UBFX)
;   A  = regsize selector for BFI: 0 → 32, non-zero → 64.
;        Ignored by UBFX (UBFX is always 64-bit; bnumkn comment in Go
;        line 24-28).
;
; Output:
;   DEHL = 32-bit encoded value.
;
; Error path:
;   BFI: lsb >= regsize  → jp fail.
;        width < 1 or width > regsize - lsb  → jp fail.
;   UBFX: lsb >= 64  → jp fail.
;         width < 1 or lsb + width > 64  → jp fail.
;
; Two distinct entry symbols are exposed so the future Task-18
; dispatcher can route by mnemonic (BFI vs UBFX) without inspecting
; a kind byte:
;   encode_bitfield_bfi
;   encode_bitfield_ubfx
;
; Clobbers: A, BC, DE, HL.
;
; Local-label prefix: encode_bitfield_*
;
; -----------------------------------------------------------------------
; encode_bitfield_bfi
;
; Computes (immr=(-lsb)&(regsize-1), imms=width-1) and packs at the
; two slot positions.
; -----------------------------------------------------------------------
encode_bitfield_bfi:
; -- Stash regsize selector before A is clobbered ----------------------
                ld      (encode_bitfield_regsize), a

; -- Range checks (BFI) -----------------------------------------------
; lsb (B) must be < regsize.
; width (C) must be in [1, regsize - lsb].
                or      a                  ; regsize selector test: 0 or non-zero
                jr      z, encode_bitfield_bfi_32
; regsize=64: lsb must be < 64; width in [1, 64-lsb].
                ld      a, b
                cp      64
                jp      nc, fail
                ld      a, 64
                jr      encode_bitfield_bfi_check_width
encode_bitfield_bfi_32:
; regsize=32: lsb must be < 32; width in [1, 32-lsb].
                ld      a, b
                cp      32
                jp      nc, fail
                ld      a, 32
encode_bitfield_bfi_check_width:
; A holds regsize.  Check C (width) in [1, A - B].
                sub     b                  ; A = regsize - lsb
                cp      c                  ; A - C: CY clear ⇒ C <= A
                jp      c, fail            ; width > (regsize-lsb) → fail
                ld      a, c
                or      a
                jp      z, fail            ; width == 0 → fail

; -- Compute immr = (-lsb) & (regsize - 1) -----------------------------
; (-lsb) in two's complement for 6-bit field: regsize - lsb mod regsize.
; Special case lsb=0: (-0)&mask = 0.
                ld      a, b
                or      a
                jr      nz, encode_bitfield_bfi_immr_nonzero
                xor     a                  ; A = 0
                jr      encode_bitfield_bfi_immr_done
encode_bitfield_bfi_immr_nonzero:
; immr = regsize - lsb (for lsb in [1, regsize-1]).
                ld      a, (encode_bitfield_regsize)
                or      a
                ld      a, 32              ; assume regsize=32
                jr      z, encode_bitfield_bfi_immr_sub
                ld      a, 64              ; regsize=64
encode_bitfield_bfi_immr_sub:
                sub     b                  ; A = regsize - lsb
                and     &3f                ; mask to 6 bits (Go: & 0x3F)
encode_bitfield_bfi_immr_done:
                ld      (encode_bitfield_immr), a

; -- Compute imms = width - 1 ------------------------------------------
                ld      a, c
                dec     a
                and     &3f
                ld      (encode_bitfield_imms), a

                jp      encode_bitfield_pack


; -----------------------------------------------------------------------
; encode_bitfield_ubfx
;
; Computes (immr=lsb, imms=lsb+width-1) and packs at the two slot
; positions.
; -----------------------------------------------------------------------
encode_bitfield_ubfx:
; -- Range checks (UBFX) ----------------------------------------------
; lsb (B) must be < 64.  width (C) must satisfy width >= 1 and
; lsb + width <= 64.
                ld      a, b
                cp      64
                jp      nc, fail
                ld      a, c
                or      a
                jp      z, fail            ; width == 0
                add     a, b               ; A = lsb + width
                jp      c, fail            ; overflowed 8-bit → > 256 ≫ 64
                cp      65                 ; in-range ⇔ A <= 64
                jp      nc, fail

; -- Compute immr = lsb -------------------------------------------------
                ld      a, b
                and     &3f
                ld      (encode_bitfield_immr), a

; -- Compute imms = lsb + width - 1 -------------------------------------
                ld      a, b
                add     a, c
                dec     a
                and     &3f
                ld      (encode_bitfield_imms), a

                ; fall through to pack


; -----------------------------------------------------------------------
; Shared packing tail.
;
; Read immr_slot.BitPosition (HL+2), shift immr<<immr_bp into DEHL,
; then read imms_slot.BitPosition (HL+6), shift imms<<imms_bp into a
; scratch DEHL and OR-merge.
;
; HL is consumed (becomes the low word of DEHL, per the encoder
; calling convention).
; -----------------------------------------------------------------------
encode_bitfield_pack:
; -- Read immr_slot.BitPosition -----------------------------------------
                push    hl                 ; save combined-slot pointer
                inc     hl
                inc     hl                 ; HL → immr_slot + 2 (bp byte)
                ld      a, (hl)
                ld      (encode_bitfield_immr_bp), a
                pop     hl                 ; restore HL = combined-slot ptr

; -- Read imms_slot.BitPosition -----------------------------------------
                ld      bc, 6              ; offset 4 + 2 = imms_slot bp byte
                add     hl, bc
                ld      a, (hl)
                ld      (encode_bitfield_imms_bp), a

; -- Build (immr << immr_bp) in DEHL -----------------------------------
                ld      a, (encode_bitfield_immr)
                ld      l, a
                ld      h, 0
                ld      d, 0
                ld      e, 0
                ld      a, (encode_bitfield_immr_bp)
                ld      b, a
                inc     b
                dec     b
                jr      z, encode_bitfield_immr_shift_done
encode_bitfield_immr_shift:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_bitfield_immr_shift
encode_bitfield_immr_shift_done:

; -- Save running result on stack --------------------------------------
                push    de
                push    hl

; -- Build (imms << imms_bp) in DEHL -----------------------------------
                ld      a, (encode_bitfield_imms)
                ld      l, a
                ld      h, 0
                ld      d, 0
                ld      e, 0
                ld      a, (encode_bitfield_imms_bp)
                ld      b, a
                inc     b
                dec     b
                jr      z, encode_bitfield_imms_shift_done
encode_bitfield_imms_shift:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_bitfield_imms_shift
encode_bitfield_imms_shift_done:

; -- OR with the saved (immr<<immr_bp) word ----------------------------
                pop     bc                 ; BC = saved HL (low 16)
                ld      a, l
                or      c
                ld      l, a
                ld      a, h
                or      b
                ld      h, a
                pop     bc                 ; BC = saved DE (high 16)
                ld      a, e
                or      c
                ld      e, a
                ld      a, d
                or      b
                ld      d, a
                ret


; -----------------------------------------------------------------------
; Scratch bytes — regsize, immr, imms, and the two bit-positions parked
; here across the multi-phase encode.
; -----------------------------------------------------------------------
encode_bitfield_regsize:
                defb    0
encode_bitfield_immr:
                defb    0
encode_bitfield_imms:
                defb    0
encode_bitfield_immr_bp:
                defb    0
encode_bitfield_imms_bp:
                defb    0
