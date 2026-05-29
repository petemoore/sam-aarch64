; extended_reg.asm — encoder for OpExtendedReg (operand kind 0x07).
;
; Mac-side reference: tools/refenc/pass2.go:1131-1179 (encodeExtendedRegInst).
; Only add (mnem 1) and sub (mnem 2) use this form today.
;
; Encoding: sf|opc|01011|001|Rm|option(3)|imm3|Rn|Rd
;   bit 31    = sf            (from operand 0 / Rd width)
;   bits 30:29 = opc          (add=00, sub=10)
;   bits 28:24 = 01011        (same as shifted, distinguished by bit 21)
;   bits 23:22 = 00
;   bit  21    = 1            (distinguishes extended from shifted)
;   bits 20:16 = Rm
;   bits 15:13 = option (3)
;   bits 12:10 = imm3         (0..4 valid)
;   bits 9:5   = Rn
;   bits 4:0   = Rd
;
; The OPVAL_ARRAY entry for OpExtendedReg is populated by
; main_parse_extended_reg in main_loop.asm:
;   +0 kind=0x07    +1 width    +2 Rm    +3 extend
;   +4..+7 imm3/amt low-32 LE   +8..+9 padding
;
; The +1 width slot here records Rm's surface width (W or X) — sf in
; the encoding comes from operand 0 (Rd), not from this byte.
;
; ---------------------------------------------------------------------
; encode_extended_reg_word — pure 32-bit word computation.
;
; Input:  A = mnemonic_id low byte (1 = add, 2 = sub).
; Reads:  OPVAL_ARRAY (operands 0, 1, 2).
; Output: DE:HL = encoded 32-bit word.  Errors: jp fail.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------
encode_extended_reg_word:
                cp      1
                jr      z, encode_extended_opc_add
                cp      2
                jp      nz, fail
                ld      a, 2                ; sub opc=10
                jr      encode_extended_have_opc
encode_extended_opc_add:
                xor     a                   ; add opc=00
encode_extended_have_opc:
                ld      (encode_extended_opc), a

; Width for sf comes from operand 0 (Rd).  Mac side returns sf=1 for
; both add/sub since fixtures only use the 64-bit form; we read the
; actual Rd width to support both surface forms.
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                ld      a, 1
                jr      z, encode_extended_have_sf
                xor     a
encode_extended_have_sf:
                ld      (encode_extended_sf), a

                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2)
                and     &1f
                ld      (encode_extended_rm), a
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 3)
                and     &07
                ld      (encode_extended_opt), a
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 4)
                cp      5
                jp      nc, fail            ; imm3 must be 0..4
                ld      (encode_extended_imm3), a
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 5)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 6)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 7)
                or      a
                jp      nz, fail

                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_extended_rd), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_extended_rn), a

; -- Build word.  Base: bits 28:24 = 01011, bit 21 = 1 → 0x0B200000.
                ld      hl, &0000
                ld      d, &0b
                ld      e, &20

; sf << 31 → set D bit 7 if 64-bit.
                ld      a, (encode_extended_sf)
                or      a
                jr      z, encode_extended_no_sf
                ld      a, d
                or      &80
                ld      d, a
encode_extended_no_sf:

; opc << 29 → D bits 6..5.
                ld      a, (encode_extended_opc)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; opc << 5
                or      d
                ld      d, a

; Rm at bits 20:16 → E bits 4:0.
                ld      a, (encode_extended_rm)
                or      e
                ld      e, a

; option << 13 → bits 15:13 = H bits 7:5.
                ld      a, (encode_extended_opt)
                rrca
                rrca
                rrca                        ; A bits 5..7 = opt
                and     &e0
                or      h
                ld      h, a

; imm3 << 10 → bits 12:10 = H bits 4:2.
                ld      a, (encode_extended_imm3)
                add     a, a
                add     a, a                ; A = imm3 << 2
                or      h
                ld      h, a

; Rn << 5 → L bits 7..5 (Rn & 7) | H bits 1..0 (Rn >> 3).
                ld      a, (encode_extended_rn)
                rrca
                rrca
                rrca
                ld      b, a
                and     &e0
                or      l
                ld      l, a
                ld      a, b
                and     &03
                or      h
                ld      h, a

; Rd at bits 4:0 → L bits 4..0.
                ld      a, (encode_extended_rd)
                or      l
                ld      l, a
                ret


; -----------------------------------------------------------------------
; Scratch state.
; -----------------------------------------------------------------------
encode_extended_sf:     defb    0
encode_extended_opc:    defb    0
encode_extended_rd:     defb    0
encode_extended_rn:     defb    0
encode_extended_rm:     defb    0
encode_extended_opt:    defb    0
encode_extended_imm3:   defb    0
