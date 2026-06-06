; mem.asm — encoder for OpMem (operand kind 0x08).
;
; Mac-side reference:
;   tools/refenc/pass2.go:721-861  (encodeMemInst, the scaled / pre /
;                                   post / register / extended paths)
;   tools/refenc/pass2.go:874-914  (encodeUnscaledMemInst, STUR/LDUR)
;   tools/refenc/pass2.go:919-987  (encodePairInst, LDP/STP)
;   tools/refenc/pass2.go:668-716  (memInstSize / memInstOpc helpers)
;
; OpMem is operand-kind-driven (not slot-driven); the form table has no
; MEM-slot entries.  Dispatch happens via try_mnemonic_intercept
; (src/intercepts.asm) for the eleven memory mnemonics that share
; this encoding family:
;   ldr=5    str=6    ldp=7    stp=8
;   ldrb=54  strb=55  ldrh=56  strh=57
;   stur=74  ldur=75
;   ldrsb=85 ldrsh=86 ldrsw=87
;
; OPVAL_ARRAY layout for an OpMem operand (10 bytes):
;   +0 kind   = 0x08
;   +1 shape  (MemBase=0, MemBaseOff=1, MemBaseOffPre=2, MemBaseOffPost=3,
;              MemBaseIdx=4, MemBaseIdxShifted=5, MemBaseIdxExtended=6)
;   +2 base   register (Rn)
;   +3 idx    register (Rm)        [used for shapes 4/5/6]
;   +4 idx_width  0=W, 1=X         [used for shapes 4/5/6]
;   +5 extend code                 [used for shape 6 only]
;   +6 shift_amt                   [used for shapes 5/6]
;   +7..+9 unused (zero)
;
; The 8-byte LE signed offset for shapes 1/2/3 lives in OPMEM_OFF (a
; shared 8-byte scratch in section D RAM).  Stored separately from
; OPVAL_ARRAY because the 10-byte stride can't accommodate a full 8-byte
; value plus the per-shape metadata.
;
; AArch64 encoding (LDR/STR family) cheat sheet:
;   bits 31:30 = size (00=byte 01=hword 10=word 11=dword)
;   bits 29:27 = 111
;   bits 26    = 0  (V — vector form; we always emit GPR)
;   bits 25:24 = mode  (00=unscaled/pre/post/reg-offset, 01=unsigned-offset)
;   bits 23:22 = opc   (00=store, 01=load, 10=signed-load→Xt, 11=signed-load→Wt)
;   bits 21:10 = imm12 OR (1|Rm|option|S|10|imm9|01/11)
;   bits  9:5  = Rn
;   bits  4:0  = Rt


; -----------------------------------------------------------------------
; encode_mem_word — top-level OpMem → 32-bit word encoder.
;
; Input:  A = mnemonic_id low byte (one of the 11 above; pair handled
;             via a separate entry, see encode_pair_word).
;         OPVAL_ARRAY[0]  = Rt operand (OpRegX/OpRegW)
;         OPVAL_ARRAY[1]  = OpMem operand (kind 0x08, shape + base + ...)
;         OPMEM_OFF[0..7] = 8-byte LE signed offset (shapes 1/2/3)
; Output: DE:HL = encoded 32-bit word (HL = bits 0..15, DE = bits 16..31).
; Errors: jp fail on out-of-range offset, unaligned scaled offset,
;         malformed shape, or Rt-width / mnemonic combination errors.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
encode_mem_word:
                ld      (encode_mem_mnem), a

; -- Compute (sizeBits, scale, opc) from mnemonic + Rt-kind.  Covers
;    stur/ldur too (they fall through to the default ldr/str branch on
;    the Mac side via Rt-kind selection).  Sets encode_mem_size /
;    encode_mem_scale / encode_mem_opc.
                call    mem_size_scale_opc

; -- Branch on unscaled mnemonics (stur=74/ldur=75/sturh=94/sturb=95/ldurh=96/ldurb=97) --
; Range-check pairs: {74,75} and {94..97} all route to encode_mem_unscaled.
                ld      a, (encode_mem_mnem)
                cp      74
                jr      c, enc_mw_skip74        ; A < 74 → not unscaled
                cp      76
                jp      c, encode_mem_unscaled  ; 74 ≤ A < 76 → unscaled
enc_mw_skip74:
                cp      94
                jr      c, enc_mw_skip94        ; A < 94 → not unscaled
                cp      98
                jp      c, encode_mem_unscaled  ; 94 ≤ A < 98 → unscaled
enc_mw_skip94:

; -- Branch on shape ---------------------------------------------------
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                cp      0                   ; MemBase
                jr      z, encode_mem_scaled
                cp      1                   ; MemBaseOff
                jr      z, encode_mem_scaled
                cp      2                   ; MemBaseOffPre
                jp      z, encode_mem_preindex
                cp      3                   ; MemBaseOffPost
                jp      z, encode_mem_postindex
                cp      4                   ; MemBaseIdx
                jp      z, encode_mem_regoff
                cp      5                   ; MemBaseIdxShifted
                jp      z, encode_mem_regoff
                cp      6                   ; MemBaseIdxExtended
                jp      z, encode_mem_extoff
                jp      fail


; ---------------------------------------------------------------------
; encode_mem_scaled — emit the unsigned-offset (scaled) form.  Falls
; back to STUR/LDUR when the byte offset doesn't fit (negative, or
; mis-aligned, or > 4095*scale) but is within ±256 — mirroring GNU as
; (tools/refenc/pass2.go:777-779).
;
; On entry encode_mem_size / encode_mem_scale / encode_mem_opc are set.
; ---------------------------------------------------------------------
encode_mem_scaled:
; If shape == 0 (MemBase) then byteOffset is implicitly 0; treat the
; OPMEM_OFF buffer as if it were all-zero by skipping the read.
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                or      a                   ; A=0 → MemBase
                jr      z, encode_mem_scaled_zero

; Read OPMEM_OFF.  Validate byteOffset >= 0 (high bytes all zero).
; Bytes +4..+7 must be 0; byte +3 high bit must be 0; otherwise it's a
; negative value → auto-promote to STUR/LDUR (or fail if out of ±256).
                ld      a, (OPMEM_OFF + 4)
                or      a
                jp      nz, encode_mem_try_promote
                ld      a, (OPMEM_OFF + 5)
                or      a
                jp      nz, encode_mem_try_promote
                ld      a, (OPMEM_OFF + 6)
                or      a
                jp      nz, encode_mem_try_promote
                ld      a, (OPMEM_OFF + 7)
                or      a
                jp      nz, encode_mem_try_promote
                ld      a, (OPMEM_OFF + 3)
                bit     7, a
                jp      nz, encode_mem_try_promote

; Non-negative byteOffset.  Verify alignment (offset mod scale == 0)
; and range (offset / scale < 4096).
                jp      encode_mem_scaled_check_align

encode_mem_scaled_zero:
; Treat as offset 0 — bypass the OPMEM_OFF read; set encode_mem_imm12 = 0.
                xor     a
                ld      (encode_mem_imm12 + 0), a
                ld      (encode_mem_imm12 + 1), a
                jp      encode_mem_scaled_pack

encode_mem_scaled_check_align:
; Compute imm12 := byteOffset / scale.  Then verify imm12 < 4096.
;
; The Z80 has no divide, but `scale` is always 1/2/4/8 (a power of two).
; Verify alignment by checking (byteOffset & (scale-1)) == 0, then right-
; shift the byte offset by log2(scale) bits.
                ld      a, (encode_mem_scale)
                cp      1
                jr      z, encode_mem_scaled_div1
                cp      2
                jr      z, encode_mem_scaled_div2
                cp      4
                jr      z, encode_mem_scaled_div4
                cp      8
                jr      z, encode_mem_scaled_div8
                jp      fail

encode_mem_scaled_div1:
; No shift / no alignment check needed.  imm12 = byteOffset[0..1].
                ld      a, (OPMEM_OFF + 2)
                or      a
                jp      nz, encode_mem_range_fail
                ld      a, (OPMEM_OFF + 3)
                or      a
                jp      nz, encode_mem_range_fail
                ld      a, (OPMEM_OFF + 1)
                cp      &10                 ; imm12 < 4096 → high byte < 0x10
                jp      nc, encode_mem_range_fail
                ld      b, a                ; B = high byte
                ld      a, (OPMEM_OFF + 0)
                ld      c, a                ; C = low byte
                jp      encode_mem_scaled_have_imm12

encode_mem_scaled_div2:
                ld      a, (OPMEM_OFF + 0)
                and     &01
                jp      nz, encode_mem_align_fail
                ld      a, (OPMEM_OFF + 2)
                or      a
                jp      nz, encode_mem_range_fail
                ld      a, (OPMEM_OFF + 3)
                or      a
                jp      nz, encode_mem_range_fail
                ld      a, (OPMEM_OFF + 1)
                cp      &20                 ; (imm12<<1) < 8192 → byte1 < 0x20
                jp      nc, encode_mem_range_fail
                ld      b, a                ; B = byte1
                ld      a, (OPMEM_OFF + 0)
                ld      c, a
; SRL B / RR C — imm12 = (byte1 byte0) >> 1.
                srl     b
                rr      c
                jp      encode_mem_scaled_have_imm12

encode_mem_scaled_div4:
                ld      a, (OPMEM_OFF + 0)
                and     &03
                jp      nz, encode_mem_align_fail
                ld      a, (OPMEM_OFF + 2)
                or      a
                jp      nz, encode_mem_range_fail
                ld      a, (OPMEM_OFF + 3)
                or      a
                jp      nz, encode_mem_range_fail
                ld      a, (OPMEM_OFF + 1)
                cp      &40
                jp      nc, encode_mem_range_fail
                ld      b, a
                ld      a, (OPMEM_OFF + 0)
                ld      c, a
                srl     b
                rr      c
                srl     b
                rr      c
                jp      encode_mem_scaled_have_imm12

encode_mem_scaled_div8:
                ld      a, (OPMEM_OFF + 0)
                and     &07
                jp      nz, encode_mem_align_fail
                ld      a, (OPMEM_OFF + 2)
                or      a
                jp      nz, encode_mem_range_fail
                ld      a, (OPMEM_OFF + 3)
                or      a
                jp      nz, encode_mem_range_fail
                ld      a, (OPMEM_OFF + 1)
                cp      &80
                jp      nc, encode_mem_range_fail
                ld      b, a
                ld      a, (OPMEM_OFF + 0)
                ld      c, a
                srl     b
                rr      c
                srl     b
                rr      c
                srl     b
                rr      c
                jp      encode_mem_scaled_have_imm12

encode_mem_scaled_have_imm12:
; B:C = imm12 (12 bits, so B[3:0] is meaningful, B[7:4] = 0).
                ld      a, c
                ld      (encode_mem_imm12 + 0), a
                ld      a, b
                ld      (encode_mem_imm12 + 1), a

encode_mem_scaled_pack:
; -- Build the 32-bit unsigned-offset word ----------------------------
; base = (size<<30) | (0b111<<27) | (1<<24) | (opc<<22)
;      = (size<<30) | 0x39000000 | (opc<<22)
;
; HL = low 16 (bits 0..15); DE = high 16 (bits 16..31).
;
;   byte 0 (HL low)  = (Rn & 7) << 5 | Rt
;   byte 1 (HL high) = (Rn >> 3) | (imm12_lo << 2 lower 6 bits)
;                      i.e. bits 9:8 = Rn[4:3]; bits 15:10 = imm12[5:0]
;   byte 2 (DE low)  = imm12[11:6] | (opc << 6)
;   byte 3 (DE high) = (size << 6) | 0x39
;
                ld      hl, &0000
                ld      d, &39
                ld      a, (encode_mem_size)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; size << 6
                or      d
                ld      d, a
                ld      a, (encode_mem_opc)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; opc << 6 → byte 2 (E)
                ld      e, a

; imm12 packing.  imm12_lo at bits 15:10 spans byte 1 bits 7:2 and
; imm12_hi at bits 17:16 / 21:16 spans byte 2 bits 5:0.  Practically:
;   byte 1 bits 7:2 = imm12[5:0]
;   byte 2 bits 5:0 = imm12[11:6]
                ld      a, (encode_mem_imm12 + 0)   ; low byte (8 bits)
                ld      b, a                        ; preserve
                and     &3f                         ; imm12[5:0]
                add     a, a
                add     a, a                        ; << 2 → bits 7:2 of byte 1
                ld      h, a
; imm12 bits 7:6 → byte 2 bits 1:0
                ld      a, b
                and     &c0
                rlca
                rlca                                ; A bits 1:0 = imm12[7:6]
                or      e
                ld      e, a
; imm12 bits 11:8 = imm12_hi[3:0] → byte 2 bits 5:2
                ld      a, (encode_mem_imm12 + 1)
                and     &0f
                add     a, a
                add     a, a                        ; << 2 → bits 5:2 of byte 2
                or      e
                ld      e, a

; Rn at bits 9:5: low 3 bits → byte 1 bits 7:5 (collide with imm12 bits
; 5:3?  No: imm12[5:0] is at byte 1 bits 7:2; Rn is at bits 9:5 = byte 1
; bits 1:0 (Rn high 2) + byte 0 bits 7:5 (Rn low 3)).  Already computed
; that imm12[5:0] is in H bits 7:2.  Rn bits 4:3 → H bits 1:0; Rn bits
; 2:0 → L bits 7:5.
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                and     &1f                         ; Rn (5 bits)
                ld      b, a
                and     &07
                rrca
                rrca
                rrca                                ; bits 7:5 of A = Rn[2:0]
                or      l
                ld      l, a
                ld      a, b
                and     &18                         ; Rn[4:3]
                rrca
                rrca
                rrca                                ; bits 1:0 of A = Rn[4:3]
                or      h
                ld      h, a

; Rt at bits 4:0 → byte 0 bits 4:0.
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                or      l
                ld      l, a
                ret


encode_mem_align_fail:
encode_mem_range_fail:
; Range / alignment failure on the scaled path: try the STUR/LDUR
; auto-promote.  This is the GNU-as behaviour for ldr/str with negative
; or unaligned scaled offsets (refenc/pass2.go:777-779).  Only ldr=5 /
; str=6 / their byte / halfword variants get promoted — but those byte/
; halfword cases never trigger here because their scale is 1 (byte) /
; 2 (halfword), and the range check matches.  Effectively only ldr / str
; have scale > 1 with offsets large enough to hit this path.

encode_mem_try_promote:
                ld      a, (encode_mem_mnem)
                cp      5
                jr      z, encode_mem_promote_ok
                cp      6
                jr      z, encode_mem_promote_ok
                cp      54
                jr      z, encode_mem_promote_ok
                cp      55
                jr      z, encode_mem_promote_ok
                cp      56
                jr      z, encode_mem_promote_ok
                cp      57
                jr      z, encode_mem_promote_ok
                cp      85
                jr      z, encode_mem_promote_ok
                cp      86
                jr      z, encode_mem_promote_ok
                cp      87
                jr      z, encode_mem_promote_ok
                jp      fail
encode_mem_promote_ok:
; Verify byteOffset in [-256, +255].  For non-negative we already know
; it overflowed the scaled range, so to land here it must have been
; either >= 4096*scale or unaligned.  STUR/LDUR also require the offset
; to be representable in 9 bits signed → -256..+255.  For positive
; values, bytes 1..7 must be zero AND byte 0 must be < 0x100 (always
; true: it's a byte).  Wait — we need byte 0 in range plus byte 1 high
; bit checked.  Simpler: the imm9 packer below already validates.  Just
; jump.
                jp      encode_mem_unscaled_from_promote


; ---------------------------------------------------------------------
; encode_mem_unscaled — STUR / LDUR direct path (mnem = 74 or 75) and
; the auto-promote fall-through landing pad.
;
; Reads:  encode_mem_mnem (if from promote; else stur/ldur direct)
;         OPVAL_ARRAY[0/1], OPMEM_OFF[0..7].
; ---------------------------------------------------------------------
encode_mem_unscaled:
encode_mem_unscaled_from_promote:
; mode_bits = 00 for unscaled (STUR/LDUR + auto-promote).  Pre/post-
; index use the same encoding template but with bits 11:10 = 11 / 01.
                xor     a
                ld      (encode_mem_idx_mode), a
                jr      encode_mem_unscaled_body

; ---------------------------------------------------------------------
; encode_mem_preindex — pre-index shape (MemBaseOffPre, shape 2).
;   bits 11:10 = 11.
; ---------------------------------------------------------------------
encode_mem_preindex:
                ld      a, &03
                ld      (encode_mem_idx_mode), a
                jr      encode_mem_unscaled_body

; ---------------------------------------------------------------------
; encode_mem_postindex — post-index shape (MemBaseOffPost, shape 3).
;   bits 11:10 = 01.
; ---------------------------------------------------------------------
encode_mem_postindex:
                ld      a, &01
                ld      (encode_mem_idx_mode), a
                ; fall through

encode_mem_unscaled_body:
; encode_mem_size and encode_mem_opc are pre-computed by
; mem_size_scale_opc (called from encode_mem_word).  The opc field
; carries the right value: 01 for loads (incl. STUR/LDUR variants and
; auto-promote source mnemonics), 00 for stores, and 10/11 for the
; signed-extend loads (ldrsb/ldrsh/ldrsw at Task 10 — those reach this
; body via the auto-promote of negative scaled offsets).

; -- Read OPMEM_OFF into a 16-bit working buffer (low 2 bytes) and check
; the high bytes for sign-extension consistency.  imm9 must be in
; [-256, +255]: high 7 bytes must be 0 (non-negative) OR (low 2 bytes
; have bit 8 = 1 AND high 6 bytes are all 0xff and byte 1 high bits
; sign-match).  Simpler: byte 0 = imm9[7:0]; byte 1 of value either 0
; (positive value 0..255) or 0xff (negative value -256..-1) AND the
; ninth bit (byte 0 bit 7) must match — but ARM's imm9 is just the
; bottom 9 bits of a sign-extended value, so check the upper 7 bytes
; are uniform sign-extension of byte 1 bit 7.
                ld      a, (OPMEM_OFF + 1)
                ld      b, a                ; B = sign-extension byte (expected for +2..+7)
                or      a
                jr      z, encode_mem_unscaled_pos
                cp      &ff
                jp      nz, fail
; Negative: validate byte 0 has bit 7 set (so the 9-bit value is < -255?).
; Actually -256 is byte0=0, byte1=0xff (with bit 8 of imm9 set).  -1 is
; byte0=0xff, byte1=0xff.  Both legitimate.  Verify bytes +2..+7 = 0xff.
                ld      a, (OPMEM_OFF + 2)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 3)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 4)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 5)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 6)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 7)
                cp      &ff
                jp      nz, fail
                jr      encode_mem_unscaled_have_off
encode_mem_unscaled_pos:
; Non-negative: bytes +1..+7 must all be 0.  +1 is already 0 (B=0); check
; the rest.  Also constrain byte 1 bit 7 of imm9 (i.e. byte 1 bit 0?
; no — imm9 only spans 9 bits = bytes 0 + bit 0 of byte 1.  So for
; positive in [0, 255], byte 1 must be 0).
                ld      a, (OPMEM_OFF + 2)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 3)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 4)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 5)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 6)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 7)
                or      a
                jp      nz, fail
encode_mem_unscaled_have_off:

; -- Compose imm9 (9 bits) into a 16-bit value: byte 0 → low byte;
; byte 1 bit 0 → bit 8.
                ld      a, (OPMEM_OFF + 0)
                ld      c, a                        ; C = imm9 low 8 bits
                ld      a, (OPMEM_OFF + 1)
                and     &01
                ld      b, a                        ; B = imm9 bit 8 (in bit 0)

; -- Build word.  Base: (size<<30) | (0b111<<27) | (opc<<22).
;   byte 0 (HL low)  = (Rn & 7) << 5 | Rt
;   byte 1 (HL high) = (Rn >> 3) bits 1:0 | imm9[3:0] << 4
;   byte 2 (DE low)  = imm9[8:4] (5 bits) | (opc << 6)
;   byte 3 (DE high) = (size << 6) | 0b00111000 = 0x38 | (size << 6)
                ld      hl, &0000
                ld      d, &38
                ld      a, (encode_mem_size)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; size << 6
                or      d
                ld      d, a
                ld      a, (encode_mem_opc)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; opc << 6
                ld      e, a

; imm9 bits 3:0 → byte 1 bits 7:4 (i.e. bits 15:12 of word, which is
; bits 15:12 of OPMEM imm9 << 12; imm9[3:0] sits there).
                ld      a, c
                and     &0f
                rlca
                rlca
                rlca
                rlca                        ; A = imm9[3:0] << 4
                ld      h, a
; OR in idx_mode (bits 11:10 of word = byte 1 bits 3:2).
                ld      a, (encode_mem_idx_mode)
                and     &03
                add     a, a
                add     a, a                ; A = mode << 2
                or      h
                ld      h, a

; imm9 bits 7:4 → byte 2 bits 3:0 (bits 19:16 of word; imm9 << 12 puts
; imm9[7:4] at word bits 19:16).
                ld      a, c
                and     &f0
                rrca
                rrca
                rrca
                rrca                        ; A = imm9[7:4]
                or      e
                ld      e, a
; imm9 bit 8 → byte 2 bit 4 (word bit 20).
                ld      a, b
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; A = imm9[8] << 4
                or      e
                ld      e, a

; Rn at bits 9:5.  Rn[2:0] → byte 0 bits 7:5; Rn[4:3] → byte 1 bits 1:0.
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                and     &1f
                ld      b, a
                and     &07
                rrca
                rrca
                rrca
                or      l
                ld      l, a
                ld      a, b
                and     &18
                rrca
                rrca
                rrca
                or      h
                ld      h, a

; Rt at bits 4:0.
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                or      l
                ld      l, a
                ret


; ---------------------------------------------------------------------
; mem_size_scale_opc — compute sizeBits, scale, opc for the scaled /
; pre / post / register / extended forms.
;
; Inputs: encode_mem_mnem, OPVAL_ARRAY[0] (Rt kind).
; Outputs: encode_mem_size (2 bits, in low bits), encode_mem_scale
;          (1/2/4/8 byte), encode_mem_opc (2 bits).
;
; Mirrors memInstSize / memInstOpc in refenc/pass2.go:668-716.
; ---------------------------------------------------------------------
mem_size_scale_opc:
                ld      a, (encode_mem_mnem)
                cp      54                  ; ldrb
                jp      z, mem_szop_byte
                cp      55                  ; strb
                jp      z, mem_szop_byte
                cp      85                  ; ldrsb
                jp      z, mem_szop_byte
; {94..97}: range check dispatches by bit 0 (even=hword, odd=byte).
                cp      94
                jr      c, mem_szop_skip_new    ; A < 94 → skip
                cp      98
                jr      nc, mem_szop_skip_new   ; A ≥ 98 → skip
                rrca                            ; bit 0 → carry
                jr      c, mem_szop_byte        ; odd (95=sturb, 97=ldurb) → byte
                jr      mem_szop_hword          ; even (94=sturh, 96=ldurh) → hword
mem_szop_skip_new:
                cp      56                  ; ldrh
                jp      z, mem_szop_hword
                cp      57                  ; strh
                jp      z, mem_szop_hword
                cp      86                  ; ldrsh
                jp      z, mem_szop_hword
                cp      87                  ; ldrsw
                jp      z, mem_szop_ldrsw
; Default: ldr=5 / str=6.  size depends on Rt kind: X → 11, W → 10.
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_W
                jr      z, mem_szop_w_default
                ld      a, &03              ; sizeBits = 11
                ld      (encode_mem_size), a
                ld      a, 8
                ld      (encode_mem_scale), a
                jp      mem_szop_set_opc
mem_szop_w_default:
                ld      a, &02              ; sizeBits = 10
                ld      (encode_mem_size), a
                ld      a, 4
                ld      (encode_mem_scale), a
                jp      mem_szop_set_opc

mem_szop_byte:
                xor     a                   ; sizeBits = 00
                ld      (encode_mem_size), a
                ld      a, 1
                ld      (encode_mem_scale), a
                jp      mem_szop_set_opc

mem_szop_hword:
                ld      a, &01              ; sizeBits = 01
                ld      (encode_mem_size), a
                ld      a, 2
                ld      (encode_mem_scale), a
                jp      mem_szop_set_opc

mem_szop_ldrsw:
                ld      a, &02              ; sizeBits = 10
                ld      (encode_mem_size), a
                ld      a, 4
                ld      (encode_mem_scale), a
                ; fall through to opc-set

mem_szop_set_opc:
; opc selection:
;   ldrsb/ldrsh/ldrsw → opc per Rt width: Xt → 10, Wt → 11 (ldrsw=Xt only).
;   else if load (ldr/ldrb/ldrh/ldur/ldp) → opc = 01.
;   else (store) → opc = 00.
                ld      a, (encode_mem_mnem)
                cp      85
                jr      z, mem_szop_signed
                cp      86
                jr      z, mem_szop_signed
                cp      87
                jr      z, mem_szop_signed
                ld      a, (encode_mem_mnem)
                call    is_mem_load_mnemonic
                jr      nz, mem_szop_store
                ld      a, 1                ; opc = 01
                ld      (encode_mem_opc), a
                ret
mem_szop_store:
                xor     a
                ld      (encode_mem_opc), a
                ret
mem_szop_signed:
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jr      z, mem_szop_signed_xt
                cp      OP_KIND_REG_W
                jp      nz, fail
                ld      a, (encode_mem_mnem)
                cp      87                  ; ldrsw is Xt-only
                jp      z, fail
                ld      a, &03              ; opc = 11 (signed → Wt)
                ld      (encode_mem_opc), a
                ret
mem_szop_signed_xt:
                ld      a, &02              ; opc = 10 (signed → Xt)
                ld      (encode_mem_opc), a
                ret


; ---------------------------------------------------------------------
; is_mem_load_mnemonic — Z=1 if A is a load (ldr=5, ldp=7, ldrb=54,
; ldrh=56, ldur=75, ldrsb=85, ldrsh=86, ldrsw=87, ldurh=96, ldurb=97).
; Mirrors isLoadMnemonic in refenc/pass2.go:642-650.
; Preserves nothing other than A on entry.
; ---------------------------------------------------------------------
is_mem_load_mnemonic:
                cp      5
                ret     z
                cp      7
                ret     z
                cp      54
                ret     z
                cp      56
                ret     z
                cp      75
                ret     z
                cp      85
                ret     z
                cp      86
                ret     z
                cp      87
                ret     z
                cp      96
                ret     z
                cp      97
                ret


; ---------------------------------------------------------------------
; encode_mem_regoff — register-offset shapes (MemBaseIdx = 4 and
; MemBaseIdxShifted = 5).
;
; Mac-side reference: tools/refenc/pass2.go:820-841.
;
; AArch64 encoding (LDR/STR register form):
;   size(2)|111|00|opc(2)|1|Rm|option(3)|S|10|Rn|Rt
;
; option:
;   011 = LSL (X-form Rm)         — shape 4 / 5
;   xxx = UXTW/SXTW/etc (W-form)  — shape 6 (extended)
;
; S = 0 when shift_amt == 0 (no LSL #N).  S = 1 when shift_amt is
; non-zero (LSL #N applied).  ARM ARM allows only N == log2(scale).
; Mac side does no further validation (the parser already ensures it).
;
; Common shape-4 vs shape-5 difference: shape 5 always uses LSL (idx is
; an Xm, option=011) with a non-zero shift amount.  Shape 4 has no
; explicit shift (shift_amt=0, S=0).
; ---------------------------------------------------------------------
encode_mem_regoff:
; option = 011 (LSL, X-form Rm) for shapes 4 / 5.
                ld      a, &03
                ld      (encode_mem_option), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 6)
                or      a
                ld      a, 0
                jr      z, encode_mem_regoff_s
                ld      a, 1
encode_mem_regoff_s:
                ld      (encode_mem_s), a
                jp      encode_mem_regoff_body


; ---------------------------------------------------------------------
; encode_mem_extoff — extended-register shape (MemBaseIdxExtended = 6).
;
; Mac-side reference: tools/refenc/pass2.go:843-856.
;
; Differs from encode_mem_regoff in two ways:
;   - option (bits 15:13) is the ExtendKind code (UXTW=2, SXTW=6, etc.)
;     not LSL=3.
;   - S bit (bit 12) is set iff shift_amt > 0.
; ---------------------------------------------------------------------
encode_mem_extoff:
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 5)
                and     &07
                ld      (encode_mem_option), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 6)
                or      a
                ld      a, 0
                jr      z, encode_mem_extoff_s
                ld      a, 1
encode_mem_extoff_s:
                ld      (encode_mem_s), a
                jp      encode_mem_regoff_body


; ---------------------------------------------------------------------
; encode_mem_regoff_body — common 32-bit packer for the register-offset
; shapes (4 / 5 / 6).
;
; Inputs (pre-computed):
;   encode_mem_size    sizeBits (2)
;   encode_mem_opc     opc (2)
;   encode_mem_option  option (3)
;   encode_mem_s       S bit (0/1)
;   OPVAL_ARRAY[0]     Rt
;   OPVAL_ARRAY[1]     OpMem: +2 base, +3 idx
; ---------------------------------------------------------------------
encode_mem_regoff_body:
; -- Build word.  Base: (size<<30) | (0b111<<27) | (opc<<22) | (1<<21) | (0b10<<10)
;   = (size<<30) | 0x38200400 | (opc<<22)
;
; Layout:
;   byte 0 = (Rn & 7) << 5 | Rt
;   byte 1 = (Rn >> 3) bits 1:0 | 0b10 << 2 = 0x08 | Rn_hi
;   byte 2 = Rm (5 bits) | option[0] << 5 | option[1] << 6 | option[2] << 7
;            Actually: bits 20:16 = Rm; bits 15:13 = option; S at bit 12.
;            We compose byte 2 (bits 23:16) and byte 1 (bits 15:8).
;
; Easier: H/L bytes (low 16 of word):
;   bits 15:13 = option       (H bits 7:5)
;   bit  12    = S            (H bit  4)
;   bits 11:10 = 10           (H bits 3:2)
;   bits 9:5   = Rn           (H bits 1:0 | L bits 7:5)
;   bits 4:0   = Rt           (L bits 4:0)
;
; D/E bytes (high 16):
;   bits 31:30 = size         (D bits 7:6)
;   bits 29:24 = 0b111000     (D bits 5:0 = 0x38)
;   bits 23:22 = opc          (E bits 7:6)
;   bit  21    = 1            (E bit  5)
;   bits 20:16 = Rm           (E bits 4:0)

                ld      hl, &0000
                ld      d, &38
                ld      a, (encode_mem_size)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; size << 6
                or      d
                ld      d, a
                ld      a, (encode_mem_opc)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; opc << 6
                or      &20                 ; bit 21 = 1
                ld      e, a
; Rm at bits 20:16 → E bits 4:0.
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3)
                and     &1f
                or      e
                ld      e, a

; option << 5 → H bits 7:5.
                ld      a, (encode_mem_option)
                and     &07
                rrca
                rrca
                rrca                        ; A bits 7:5 = option
                ld      h, a
; S << 4 → H bit 4.
                ld      a, (encode_mem_s)
                or      a
                jr      z, encode_mem_regoff_no_s
                ld      a, h
                or      &10
                ld      h, a
encode_mem_regoff_no_s:
; bits 11:10 = 10 → H bits 3:2.
                ld      a, h
                or      &08
                ld      h, a

; Rn at bits 9:5: Rn[2:0] → L bits 7:5; Rn[4:3] → H bits 1:0.
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                and     &1f
                ld      b, a
                and     &07
                rrca
                rrca
                rrca
                or      l
                ld      l, a
                ld      a, b
                and     &18
                rrca
                rrca
                rrca
                or      h
                ld      h, a

; Rt at bits 4:0 → L bits 4:0.
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                or      l
                ld      l, a
                ret


; ---------------------------------------------------------------------
; encode_pair_word — load/store-pair encoder (ldp = 7, stp = 8).
;
; Mac-side reference: tools/refenc/pass2.go:919-988 (encodePairInst).
;
; Operands: Rt1 (OPVAL[0]), Rt2 (OPVAL[1]), Mem (OPVAL[2]).
;
; AArch64 encoding:
;   opc(2)|101|0|mode(2)|L|imm7(7)|Rt2(5)|Rn(5)|Rt1(5)
;
;   opc: 10 if Rt is 64-bit (X), 00 if 32-bit (W)
;   L:   1 for ldp (mnem 7), 0 for stp (mnem 8)
;   mode: MemBase/MemBaseOff → 10 (signed offset)
;         MemBaseOffPre      → 11 (pre-index)
;         MemBaseOffPost     → 01 (post-index)
;
; imm7 = byteOffset / scale; must be in [-64, +63]; scale = 8 (X) or 4 (W).
;
; Input:  A = mnemonic_id (7 or 8).
; Output: DE:HL = encoded 32-bit word.
; Errors: jp fail on bad shape / out-of-range imm7 / misalignment.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------
encode_pair_word:
                ld      (encode_pair_mnem), a

; -- L bit: 1 for ldp (7), 0 for stp (8) ------------------------------
                cp      7
                ld      a, 0
                jr      nz, encode_pair_lset
                ld      a, 1
encode_pair_lset:
                ld      (encode_pair_l), a

; -- opc / scale from Rt1's kind -------------------------------------
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jr      z, encode_pair_x
                cp      OP_KIND_REG_W
                jp      nz, fail
                xor     a                   ; opc = 00 (32-bit)
                ld      (encode_pair_opc), a
                ld      a, 4
                ld      (encode_pair_scale), a
                jr      encode_pair_have_size
encode_pair_x:
                ld      a, &02              ; opc = 10 (64-bit)
                ld      (encode_pair_opc), a
                ld      a, 8
                ld      (encode_pair_scale), a
encode_pair_have_size:

; -- Read OpMem shape from OPVAL[2] +1; derive mode bits + whether to
;    read an offset.  Pair only supports MemBase (signed offset 0),
;    MemBaseOff, MemBaseOffPre, MemBaseOffPost.
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 1)
                cp      0
                jr      z, encode_pair_mode_off
                cp      1
                jr      z, encode_pair_mode_off
                cp      2
                jr      z, encode_pair_mode_pre
                cp      3
                jp      z, encode_pair_mode_post
                jp      fail
encode_pair_mode_off:
                ld      a, &02              ; mode = signed offset (10)
                jr      encode_pair_have_mode
encode_pair_mode_pre:
                ld      a, &03              ; mode = pre-index (11)
                jr      encode_pair_have_mode
encode_pair_mode_post:
                ld      a, &01              ; mode = post-index (01)
encode_pair_have_mode:
                ld      (encode_pair_mode), a

; -- imm7 derivation: OPMEM_OFF holds byteOffset (sign-extended s64).
;    For MemBase (shape 0) main_parse_mem already zeroed OPMEM_OFF.
;    For shapes 1/2/3 the parser ran the expression evaluator.
;
;    Sanity-check the 8-byte sign extension: bytes +2..+7 must all match
;    the sign of byte +1 (high bit propagated).  Then divide by scale
;    (1, 2, 4, or 8 — here always 4 or 8).
                ld      a, (OPMEM_OFF + 1)
                ld      b, a                ; B = sign byte
                or      a
                jr      z, encode_pair_imm_pos
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 2)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 3)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 4)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 5)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 6)
                cp      &ff
                jp      nz, fail
                ld      a, (OPMEM_OFF + 7)
                cp      &ff
                jp      nz, fail
                jr      encode_pair_have_off
encode_pair_imm_pos:
                ld      a, (OPMEM_OFF + 2)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 3)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 4)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 5)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 6)
                or      a
                jp      nz, fail
                ld      a, (OPMEM_OFF + 7)
                or      a
                jp      nz, fail
encode_pair_have_off:

; -- Verify alignment + divide by scale.  scale ∈ {4, 8}.  We work on
;    the low 2 bytes only after the high-byte sign check above.
                ld      a, (OPMEM_OFF + 0)
                ld      c, a                ; C = byte 0
                ld      a, (OPMEM_OFF + 1)
                ld      b, a                ; B = byte 1
                ld      a, (encode_pair_scale)
                cp      4
                jr      z, encode_pair_div4
                cp      8
                jp      nz, fail
; div by 8: low 3 bits must be 0; arithmetic shift right by 3.
                ld      a, c
                and     &07
                jp      nz, fail
                sra     b
                rr      c
                sra     b
                rr      c
                sra     b
                rr      c
                jr      encode_pair_imm7_check
encode_pair_div4:
                ld      a, c
                and     &03
                jp      nz, fail
                sra     b
                rr      c
                sra     b
                rr      c

encode_pair_imm7_check:
; B:C now holds the scaled (signed) imm7 in 16-bit two's-complement.
; Range check: must be in [-64, +63].  Equivalent: bits 15..7 must all
; be the same as bit 6 of C (sign-extension of a 7-bit value).
                ld      a, c
                bit     6, a
                jr      nz, encode_pair_imm7_neg
; positive: top 9 bits (B and C bit 7) must be zero.
                or      a               ; A = C
                bit     7, a
                jp      nz, fail
                ld      a, b
                or      a
                jp      nz, fail
                jr      encode_pair_pack
encode_pair_imm7_neg:
; negative: top 9 bits must be all ones.
                bit     7, a
                jp      z, fail
                ld      a, b
                cp      &ff
                jp      nz, fail

encode_pair_pack:
; imm7 = C & 0x7F.
                ld      a, c
                and     &7f
                ld      (encode_pair_imm7), a

; -- Build the 32-bit word --------------------------------------------
; D byte (bits 31:24):
;   bits 31:30 = opc
;   bits 29:27 = 101
;   bit  26    = 0
;   bits 25:24 = bits 9:8 of (mode(2)|L|imm7(7)) =
;                effectively the high 2 bits of (modeBits<<8 | L<<7 | imm7),
;                i.e. modeBits.
                ld      a, (encode_pair_opc)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; opc << 6
                or      &28                 ; bits 29:27 = 101 → 0x28
                ld      d, a
                ld      a, (encode_pair_mode)
                or      d
                ld      d, a

; E byte (bits 23:16):
;   bit 23 = bit 7 of (L|imm7) = L (since imm7 is 7 bits below)... wait,
;   let me redo this.  The full layout is opc(2)|101|0|mode(2)|L|imm7(7)
;   spanning bits 31..15.  Below bit 15 is Rt2(5) at 14:10, Rn(5) at 9:5,
;   Rt1(5) at 4:0.
;
;   So bit positions:
;     31:30 opc
;     29:27 101
;     26    0
;     25:24 mode (modeBits[1:0])
;     23    L
;     22:16 imm7 (7 bits)
;     15:10 Rt2 — no, that's 5 bits at 14:10
;     14:10 Rt2
;      9:5  Rn
;      4:0  Rt1
;
; D byte breakdown:
;     bit 7    = opc[1]
;     bit 6    = opc[0]
;     bits 5..3 = 101
;     bit  2   = 0
;     bits 1..0 = mode[1:0]
; (Already done above.)
;
; E byte:
;     bit 7    = L
;     bits 6..0 = imm7
                ld      a, (encode_pair_l)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                ; L << 7
                ld      e, a
                ld      a, (encode_pair_imm7)
                or      e
                ld      e, a

; HL byte 1 (bits 15:8):
;   bits 15:13 = Rt2[4:2]
;   bits 12:10 = Rt2[2:0] — wait, Rt2 is 5 bits at 14:10.
;
; Recompute: byte 1 (bits 15:8) layout:
;     bit 7   = bit 15 = Rt2[4]
;     bit 6   = bit 14 = ... actually Rt2 lives at bits 14:10, but ARM
;       has Rt2 at 14:10 inclusive, so it's 5 bits.
;
;   Byte 1 bits 7..0 → word bits 15..8:
;     7 → 15:  unused (we're going to land Rt2 at 14:10 and Rn at 9:5).
;              Actually bits 15 is unallocated for this encoding.
;
; Wait: the ARM encoding for LDP/STP places Rt2 at bits 14:10 — 5 bits.
; So word bit 15 is NOT used by Rt2.  Looking at the Mac side again
; (refenc/pass2.go:985-986):
;
;   word := (opc << 30) | (0b101 << 27) | (modeBits << 23) | (l << 22) |
;           (imm7 << 15) | (uint32(rt2.Reg) << 10) | (uint32(mem.Base) << 5)
;           | uint32(rt1.Reg)
;
; So Mac side has:
;   bits 31:30 = opc
;   bits 29:27 = 101
;   bit  26    = 0     (implicit gap in the mask)
;   bits 25:24 = unused? — No: modeBits << 23 means bits 24:23.
;   bits 24:23 = modeBits
;   bit  22    = L
;   bits 21:15 = imm7 (7 bits)
;   bits 14:10 = Rt2
;   bits 9:5   = Rn
;   bits 4:0   = Rt1
;
; My earlier byte-D analysis was wrong.  Restart the packing:

; Reset:
                ld      d, &28          ; bits 29:27 = 101
                ld      e, 0

; opc << 30 → D bits 7:6
                ld      a, (encode_pair_opc)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a            ; opc << 6
                or      d
                ld      d, a

; mode << 23 → D bit 0 (mode[1]) + E bit 7 (mode[0]).
                ld      a, (encode_pair_mode)
                bit     1, a
                jr      z, encode_pair_no_mode_hi
                ld      a, d
                or      &01
                ld      d, a
encode_pair_no_mode_hi:
                ld      a, (encode_pair_mode)
                bit     0, a
                jr      z, encode_pair_no_mode_lo
                ld      a, e
                or      &80
                ld      e, a
encode_pair_no_mode_lo:

; L << 22 → E bit 6.
                ld      a, (encode_pair_l)
                or      a
                jr      z, encode_pair_no_l
                ld      a, e
                or      &40
                ld      e, a
encode_pair_no_l:

; imm7 << 15 → byte at bits 21:15.  Split:
;   bit 15      = imm7[0]   → byte 1 bit 7 (HL high bit 7)
;   bits 21:16  = imm7[6:1] → byte 2 bits 5:0
                ld      a, (encode_pair_imm7)
                rrca                        ; A bits 7:6 = imm7[0:1]?
; Reset — clean derivation.
                ld      a, (encode_pair_imm7)
                and     &01
                jr      z, encode_pair_imm7_b15_zero
                ld      hl, &8000           ; bit 15 set
                jr      encode_pair_imm7_b15_set
encode_pair_imm7_b15_zero:
                ld      hl, &0000
encode_pair_imm7_b15_set:

; imm7[6:1] → E bits 5:0.  Shift imm7 right by 1.
                ld      a, (encode_pair_imm7)
                rrca                        ; imm7 >> 1 (bit 7 was 0 since imm7 is 7 bits)
                and     &3f
                or      e
                ld      e, a

; Rt2 << 10 → bits 14:10.  Rt2[4:2] → byte 1 bits 6:4; Rt2[1:0] → byte 1
; bits ... let me redo bit positions for bits 14:10:
;   bit 14 → byte 1 bit 6
;   bit 13 → byte 1 bit 5
;   bit 12 → byte 1 bit 4
;   bit 11 → byte 1 bit 3
;   bit 10 → byte 1 bit 2
; So Rt2[4:0] occupies byte 1 bits 6:2 (5 bits).
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                add     a, a
                add     a, a                ; A = Rt2 << 2 (lands at bits 6:2 of byte 1)
                or      h
                ld      h, a

; Rn at bits 9:5 → byte 1 bits 1:0 (Rn[4:3]) | byte 0 bits 7:5 (Rn[2:0]).
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2)
                and     &1f
                ld      b, a
                and     &07
                rrca
                rrca
                rrca
                or      l
                ld      l, a
                ld      a, b
                and     &18
                rrca
                rrca
                rrca
                or      h
                ld      h, a

; Rt1 at bits 4:0 → byte 0 bits 4:0.
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                or      l
                ld      l, a
                ret


; -----------------------------------------------------------------------
; Scratch state.
; -----------------------------------------------------------------------
encode_mem_mnem:        defb    0
encode_mem_size:        defb    0       ; sizeBits (2 bits, in low bits of byte)
encode_mem_scale:       defb    0       ; 1, 2, 4, or 8
encode_mem_opc:         defb    0       ; opc field (2 bits)
encode_mem_imm12:       defb    0, 0    ; imm12 (B:C = high:low; stored low,high)
encode_mem_idx_mode:    defb    0       ; bits 11:10 (00 unscaled, 01 post, 11 pre)
encode_mem_option:      defb    0       ; option(3) for register-offset shapes
encode_mem_s:           defb    0       ; S bit (0/1) for register-offset shapes

encode_pair_mnem:       defb    0
encode_pair_opc:        defb    0
encode_pair_l:          defb    0
encode_pair_mode:       defb    0
encode_pair_scale:      defb    0
encode_pair_imm7:       defb    0
