; shifted_reg.asm — encoder for OpShiftedReg (operand kind 0x06) and
; the 3-reg auto-coercion driver.
;
; This file lives under slots/ to match the convention; OpShiftedReg is
; an operand-kind direct-emit (not a slot kind drained by the encoder.asm
; slot dispatcher).  The Mac-side equivalent is the top-level dispatch
; in tools/refenc/pass2.go:1005-1067 (encodeShiftedRegInst), called
; ahead of the form-table path.
;
; Encoding: sf|opc|01011|shift|N|Rm|imm6|Rn|Rd
;   bits 31    = sf            (1 = 64-bit, 0 = 32-bit)
;   bits 30:29 = opc           (per-mnemonic; see shifted_reg_table)
;   bits 28:24 = 0b01011 (arith)  or 0b01010 (logical)
;   bits 23:22 = shift_kind    (LSL=00, LSR=01, ASR=10, ROR=11)
;   bit  21    = N             (BIC sets N=1; others N=0)
;   bits 20:16 = Rm
;   bits 15:10 = imm6
;   bits 9:5   = Rn
;   bits 4:0   = Rd
;
; The OPVAL_ARRAY entry for OpShiftedReg is populated by
; main_parse_shifted_reg (main_loop.asm):
;   +0 kind=0x06    +1 width    +2 Rm    +3 shift_kind
;   +4..+7 imm6/amt low-32 LE   +8..+9 padding
;
; For the 3-reg coerce case, intercepts.asm synthesises this layout
; in-place before calling here.

; ---------------------------------------------------------------------
; encode_shifted_reg_word — pure 32-bit word computation.
;
; Input:  A = mnemonic_id low byte.
; Reads:  OPVAL_ARRAY (and the per-mnem table below).
; Output: DE:HL = encoded 32-bit word.  Errors: jp fail.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------
encode_shifted_reg_word:
                ld      (encode_shifted_mnem), a

; Look up per-mnem fields (opc, N, isLogical) in shifted_reg_table.
                call    shifted_reg_lookup
                or      a
                jp      nz, fail

; ShiftedReg operand index: 1 for tst (mnem 46), 2 otherwise.
                ld      a, (encode_shifted_mnem)
                cp      46
                ld      a, 2
                jr      nz, encode_shifted_have_idx
                ld      a, 1
encode_shifted_have_idx:
; HL = OPVAL_ARRAY + idx*10 + 1  (first field of ShiftedReg = width)
                ld      hl, OPVAL_ARRAY + 1
                or      a
                jr      z, encode_shifted_sr_at_0
encode_shifted_add_stride:
                ld      bc, OPVAL_STRIDE
                add     hl, bc
                dec     a
                jr      nz, encode_shifted_add_stride
encode_shifted_sr_at_0:

; Read width (+1), Rm (+2), shift_kind (+3), imm6 low byte (+4),
; high bytes (+5..+7 must be zero).
                ld      a, (hl)             ; width
                ld      (encode_shifted_sf), a
                inc     hl
                ld      a, (hl)             ; Rm
                and     &1f
                ld      (encode_shifted_rm), a
                inc     hl
                ld      a, (hl)             ; shift_kind
                and     &03
                ld      (encode_shifted_shiftk), a
                inc     hl
                ld      a, (hl)             ; imm6 low byte
                cp      64
                jp      nc, fail
                ld      (encode_shifted_imm6), a
                inc     hl
                ld      a, (hl)
                or      a
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                or      a
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                or      a
                jp      nz, fail

; Read Rd (=Rn for tst — baked to 31) and Rn.
                ld      a, (encode_shifted_mnem)
                cp      46
                jr      z, encode_shifted_tst
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_shifted_rd), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_shifted_rn), a
                jr      encode_shifted_pack
encode_shifted_tst:
                ld      a, 31               ; xzr baked into Rd
                ld      (encode_shifted_rd), a
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_shifted_rn), a

encode_shifted_pack:
; -- Build the 32-bit word.  Start with mid << 24 (0x0A logical / 0x0B
;    arithmetic) in D.
                ld      hl, &0000
                ld      a, (encode_shifted_islog)
                or      a
                ld      d, &0b
                jr      z, encode_shifted_arith
                ld      d, &0a
encode_shifted_arith:
                ld      e, 0

; sf << 31 → set D bit 7 if 64-bit.
                ld      a, (encode_shifted_sf)
                or      a
                jr      z, encode_shifted_no_sf
                ld      a, d
                or      &80
                ld      d, a
encode_shifted_no_sf:

; opc << 29 → D bits 6..5.
                ld      a, (encode_shifted_opc)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; opc << 5
                or      d
                ld      d, a

; shift_kind << 22 → E bits 7..6.
                ld      a, (encode_shifted_shiftk)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; shiftk << 6
                or      e
                ld      e, a

; N << 21 → E bit 5.
                ld      a, (encode_shifted_nbit)
                or      a
                jr      z, encode_shifted_no_n
                ld      a, e
                or      &20
                ld      e, a
encode_shifted_no_n:

; Rm at bits 20:16 → E bits 4:0.
                ld      a, (encode_shifted_rm)
                or      e
                ld      e, a

; imm6 << 10 → H bits 7:2.
                ld      a, (encode_shifted_imm6)
                add     a, a
                add     a, a                ; imm6 << 2
                or      h
                ld      h, a

; Rn << 5 → L bits 7..5 (Rn & 7) | H bits 1..0 (Rn >> 3).
                ld      a, (encode_shifted_rn)
                rrca
                rrca
                rrca                        ; A bits 5..7 = Rn&7; A bits 0..1 = Rn>>3
                ld      b, a
                and     &e0
                or      l
                ld      l, a
                ld      a, b
                and     &03
                or      h
                ld      h, a

; Rd at bits 4:0 → L bits 4..0.
                ld      a, (encode_shifted_rd)
                or      l
                ld      l, a
                ret


; -----------------------------------------------------------------------
; shifted_reg_lookup — A = mnem id.
; Walks shifted_reg_table; populates encode_shifted_{opc,nbit,islog}.
;
; Output: A = 0 on success, &ff on bad mnem.  Clobbers: A, BC, HL.
; Table row: [mnem][opc][nbit][islog] terminated by 0xff.
; -----------------------------------------------------------------------
shifted_reg_lookup:
                ld      c, a
                ld      hl, shifted_reg_table
shifted_reg_lookup_loop:
                ld      a, (hl)
                cp      &ff
                jr      z, shifted_reg_lookup_fail
                cp      c
                jr      z, shifted_reg_lookup_hit
                inc     hl
                inc     hl
                inc     hl
                inc     hl
                jr      shifted_reg_lookup_loop
shifted_reg_lookup_hit:
                inc     hl
                ld      a, (hl)
                ld      (encode_shifted_opc), a
                inc     hl
                ld      a, (hl)
                ld      (encode_shifted_nbit), a
                inc     hl
                ld      a, (hl)
                ld      (encode_shifted_islog), a
                xor     a
                ret
shifted_reg_lookup_fail:
                ld      a, &ff
                ret


; Mirrors tools/refenc/pass2.go:1096-1123 (shiftedRegMnemonicFields).
;          mnem id   opc  nbit  islog
shifted_reg_table:
                defb    1,        0,   0,    0    ; add  — arith
                defb    2,        2,   0,    0    ; sub  — arith
                defb    14,       0,   0,    1    ; and  — logical N=0
                defb    15,       1,   0,    1    ; orr  — logical N=0
                defb    16,       2,   0,    1    ; eor  — logical N=0
                defb    45,       3,   0,    0    ; subs — arith
                defb    46,       3,   0,    1    ; tst  — logical (Rd=xzr)
                defb    47,       0,   1,    1    ; bic  — logical N=1
                defb    80,       3,   0,    1    ; ands — logical
                defb    98,       1,   0,    0    ; adds — arith opc=01
                defb    &ff                       ; sentinel


; -----------------------------------------------------------------------
; Scratch state (one byte per field).
; -----------------------------------------------------------------------
encode_shifted_mnem:    defb    0
encode_shifted_sf:      defb    0
encode_shifted_opc:     defb    0
encode_shifted_nbit:    defb    0
encode_shifted_islog:   defb    0
encode_shifted_rd:      defb    0
encode_shifted_rn:      defb    0
encode_shifted_rm:      defb    0
encode_shifted_shiftk:  defb    0
encode_shifted_imm6:    defb    0
