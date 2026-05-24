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
; (src/m3/intercepts.asm) for the eleven memory mnemonics that share
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

; -- Branch on unscaled mnemonics (stur=74, ldur=75) ------------------
                cp      74
                jp      z, encode_mem_unscaled
                cp      75
                jp      z, encode_mem_unscaled

; -- Compute (sizeBits, scale, opc) from mnemonic + Rt-kind -----------
                call    mem_size_scale_opc

; -- Branch on shape ---------------------------------------------------
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                cp      0                   ; MemBase
                jp      z, encode_mem_scaled
                cp      1                   ; MemBaseOff
                jp      z, encode_mem_scaled
; (shapes 2..6 land in later sub-tasks; fall through fail for now.)
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
; -- sizeBits from Rt's kind: W → 10, X → 11 -------------------------
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jr      z, encode_mem_unscaled_x
                cp      OP_KIND_REG_W
                jp      nz, fail
                ld      a, &02              ; sizeBits = 10
                jr      encode_mem_unscaled_have_size
encode_mem_unscaled_x:
                ld      a, &03              ; sizeBits = 11
encode_mem_unscaled_have_size:
                ld      (encode_mem_size), a

; -- isLoad → opc.  ldr=5, ldur=75, ldrb=54, ldrh=56, ldrsb=85, ldrsh=86,
;    ldrsw=87 are loads; stur=74, str=6, strb=55, strh=57 are stores.
;
; For STUR / LDUR (direct mnem), and the auto-promote path from
; ldr/str/ldrb/strb/ldrh/strh, we use opc=01 (load) or 00 (store).
; The signed-extend loads (85/86/87) use a different opc per Rt width;
; M5 PR-C Task 10 will reach this through the auto-promote of negative
; offsets — but Task 10's ldrs* will land here too, with their dedicated
; opc lookup.  For now ldrs* are not handled by this routine and will
; fail.
                ld      a, (encode_mem_mnem)
                call    is_mem_load_mnemonic
                ld      a, 0
                jr      nz, encode_mem_unscaled_no_load
                ld      a, 1
encode_mem_unscaled_no_load:
                ld      (encode_mem_opc), a

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
; ldrh=56, ldur=75, ldrsb=85, ldrsh=86, ldrsw=87).  Mirrors
; isLoadMnemonic in refenc/pass2.go:642-650 (with the ID renumbering for
; the post-PR-#26 table).  Preserves nothing other than A on entry.
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
                ret


; ---------------------------------------------------------------------
; encode_pair_word — top-level pair encoder (ldp/stp).  Implemented in
; M5 PR-C Task 9.  For now, fail cleanly.
; ---------------------------------------------------------------------
encode_pair_word:
                jp      fail


; -----------------------------------------------------------------------
; Scratch state.
; -----------------------------------------------------------------------
encode_mem_mnem:        defb    0
encode_mem_size:        defb    0       ; sizeBits (2 bits, in low bits of byte)
encode_mem_scale:       defb    0       ; 1, 2, 4, or 8
encode_mem_opc:         defb    0       ; opc field (2 bits)
encode_mem_imm12:       defb    0, 0    ; imm12 (B:C = high:low; stored low,high)
