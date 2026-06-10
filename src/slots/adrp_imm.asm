; slots/adrp_imm.asm — AdrpImm constant-offset encoder.
;
; Z80 port of tools/aarch64enc/slots_adrp.go::encodeAdrpImm
; (lines 5-24):
;
;     func encodeAdrpImm(slot OperandSlot, byteOffset int64)
;             (uint32, error) {
;         if byteOffset%4096 != 0 {
;             return 0, fmt.Errorf("AdrpImm: offset %d not page-aligned", ...)
;         }
;         pageOffset := byteOffset / 4096
;         half := int64(1) << 20
;         if pageOffset >= half || pageOffset < -half {
;             return 0, fmt.Errorf("AdrpImm: page offset out of range")
;         }
;         imm21 := uint32(pageOffset) & ((1 << 21) - 1)
;         immlo := imm21 & 0x3
;         immhi := (imm21 >> 2) & ((1 << 19) - 1)
;         return (immlo << 29) | (immhi << 5), nil
;     }
;
; The (immlo:2, immhi:19) split is HARD-CODED at bits 29..30 and 5..23:
; per the Go source comment "the slot's BitPosition/BitWidth are not
; consulted (the layout is fixed by the adrp encoding)".  We mirror
; that here — bp/bw are not read from the slot record.
;
; SlotKind handled here:
;   AdrpImm = 0x23
;
; M4 caller contract: BCDE is the ABSOLUTE target address (the resolved
; value of a label or PC-relative expression).  The adrp instruction
; computes target_page - pc_page, where each "page" is the 4 KB-aligned
; address (low 12 bits zero).  Per
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.5:
;
;   if slot.SlotKind == AdrpImm:
;       value = (value & ~0xFFF) - (current_pc & ~0xFFF)
;
; Both operands are masked to their page bases BEFORE the subtract.
; This is materially different from "subtract then mask", because if
; current_pc has any low-12 bits set, the page-base difference will
; differ from the masked raw difference.
;
; The mask + subtraction also subsumes the page-alignment check that
; used to exist here: after masking both operands to page boundaries,
; the difference is page-aligned by construction, so the low-12-bit
; alignment check below is a no-op for any well-formed M4 input.  We
; keep the check anyway as a defensive invariant.
;
; M3 fixtures never exercise adrp, so the existing M3 corpus continues
; to byte-match GNU regardless of the new subtraction step.  M4
; fixtures (PR 3) exercise it end-to-end.
;
; The Z80 reachable range is ±(1<<31) bytes (±2GB), comfortably more
; than any practical SAM-resident assembler input.
;
; -----------------------------------------------------------------------
; Calling convention — wide signed single-operand
; -----------------------------------------------------------------------
;
;   HL    = pointer to 4-byte slot record (consumed — overwritten when
;           we build the output word; the slot's bp/bw aren't read so
;           HL is effectively ignored, but the convention requires HL
;           to be the slot pointer).
;   BCDE  = signed 32-bit byteOffset, big-endian register packing:
;             B = bits 24..31  (hi byte, sign bit at B bit 7)
;             C = bits 16..23
;             D = bits  8..15
;             E = bits  0.. 7  (lo byte)
;
; Output:
;   DEHL  = 32-bit encoded value (DE = bits 16..31, HL = bits 0..15).
;
; Error path:
;   byteOffset % 4096 ≠ 0                        → jp fail
;   pageOffset = byteOffset / 4096 ∉ [-2^20, 2^20) → jp fail
;
; Clobbers: A, BC, DE, HL.
;
; Local-label prefix: encode_adrp_imm_*
;
; -----------------------------------------------------------------------
; Implementation outline
; -----------------------------------------------------------------------
;
;   1. Page-alignment: low 12 bits of byteOffset must be zero.  Check
;      E == 0 and (D & 0x0F) == 0.
;
;   2. pageOffset = byteOffset >> 12 (arithmetic).  Z80 has no fast
;      multi-bit shift; we exploit the fact that 12 bits = 1.5 bytes:
;        new B = sign-ext of old B
;        new C = sign-ext of old B's low 4 bits | (B << 4)?  No —
;      cleaner: shift right by 8 bits (one byte right) then 4 more.
;
;      Byte-right by 8 (sign-extended):
;        new E = old D
;        new D = old C
;        new C = old B
;        new B = old B's sign bit replicated to 0xFF or 0x00
;
;      Then a 4-bit ASR (BCDE → BCDE):
;        sra b; rr c; rr d; rr e         (four passes)
;
;   3. Range check: pageOffset ∈ [-2^20, 2^20).  After step 2 BCDE
;      holds pageOffset sign-extended to 32 bits.  Valid range means
;      bits [31:20] all equal bit 20 (the sign of pageOffset within
;      its 21-bit field).  Equivalent test: ASR BCDE 20 more times;
;      the result must be 0x00000000 (positive in-range) or
;      0xFFFFFFFF (negative in-range).
;
;      We use the same in-memory copy + memory-byte counter pattern
;      as encode_branch_imm.
;
;   4. imm21 = pageOffset & ((1<<21) - 1).
;      In BCDE terms: keep low 21 bits = E (all 8), D (all 8), C bits
;      [4:0].  Zero B, mask C with 0x1F.
;
;   5. immlo = imm21 & 3 = low 2 bits of E = E bits [1:0].
;      immhi = (imm21 >> 2) & ((1<<19) - 1)
;            = bits [20:2] of imm21
;            = (imm21 >> 2) masked to 19 bits.
;
;   6. Final word:
;        bits 29..30: immlo
;        bits  5..23: immhi
;        all other bits: zero
;
;      Build directly in DEHL:
;        - shift imm21 right by 2 then left by 5 (net left by 3),
;          mask to bits 5..23 (the immhi field) → that's the
;          immhi<<5 part.
;        - shift original (E & 3) left by 29 → immlo<<29 part.
;        - OR them together.
;
;      Easier on Z80: a single right-shift of 2 (giving bits
;      [22..2] in target position [20..0]), then a left-shift by 5
;      (giving target bits [25..5]).  Mask the 19-bit field to keep
;      only bits 5..23 (the slot for immhi).
;
;   In practice we build the two pieces separately and OR-merge into
;   DEHL, matching the imm12_shifted / imm16_shifted pattern.
;
; -----------------------------------------------------------------------
encode_adrp_imm:
; -- Full 64-bit page-difference, then mask33 + sign-extend bit 32 ------
; Faithful port of tools/refenc/pass2.go:363-369 (the AdrpImm case of
; operandsToValues):
;
;     diff := (v & ^int64(0xFFF)) - (pc & ^int64(0xFFF))
;     const mask33 = int64(1<<33 - 1)
;     diff &= mask33
;     if diff&(int64(1)<<32) != 0 {
;         diff |= ^mask33
;     }
;     v = diff
;
; followed by encodeAdrpImm (slots_adrp.go:11-24): pageOffset = diff/4096,
; range-check ±2^20, imm21 = pageOffset & 0x1FFFFF.
;
; The 32-bit-only predecessor mis-signed page deltas whose low 32 bits
; have bit 31 set but whose true (33-bit) value is positive — e.g.
; delta 0xff841000 under origin 0xfffffff0_00000000.  The diff therefore
; MUST be computed at full width and sign-determined by bit 32, not 31.
;
; CALLING CONVENTION (unchanged from M4): BCDE = low 32 bits of the
; ABSOLUTE target, big-endian register packing (B=hi .. E=lo).  An adrp
; target is ALWAYS an origin-relative address (a label / PC-relative
; expression), so its full-64-bit high word equals ORIGIN_HIGH — exactly
; the same high word as PASS_PC's VMA.  We therefore reconstruct the
; full 64-bit target as (BCDE low) ++ (ORIGIN_HIGH high), which makes the
; high words cancel in the page-difference subtraction below.  This both
; preserves the M4 self-test convention (which sets BCDE directly with
; PASS_PC == 0 and ORIGIN_HIGH == 0, so the high words are 0) AND fixes
; the high-origin path (delta 0xff841000 is +ve in 33-bit space).
; Faithful to Go where target v and pc both carry the full OriginVMA
; (tools/refenc/pass2.go:363 diff = (v&~0xFFF) - (pc&~0xFFF)).
;
; pc = PASS_PC (low 32, LE) ++ ORIGIN_HIGH (high 32, LE).

; Build (v & ~0xFFF) in encode_adrp_imm_t8[0..7] (LE, 8 bytes).
;   t8[0..3] = BCDE (low 32), then mask low 12 bits.
                xor     a
                ld      (encode_adrp_imm_t8 + 0), a    ; clear low 8 bits
                ld      a, d
                and     &f0                            ; clear bits 8..11
                ld      (encode_adrp_imm_t8 + 1), a
                ld      a, c
                ld      (encode_adrp_imm_t8 + 2), a
                ld      a, b
                ld      (encode_adrp_imm_t8 + 3), a
;   t8[4..7] = ORIGIN_HIGH (target high word == origin high word).
                ld      hl, ORIGIN_HIGH
                ld      de, encode_adrp_imm_t8 + 4
                ld      bc, 4
                ldir

; Build (pc & ~0xFFF) in encode_adrp_imm_pc8[0..7] (LE, 8 bytes).
                xor     a
                ld      (encode_adrp_imm_pc8 + 0), a   ; low byte := 0
                ld      a, (PASS_PC + 1)
                and     &f0
                ld      (encode_adrp_imm_pc8 + 1), a
                ld      a, (PASS_PC + 2)
                ld      (encode_adrp_imm_pc8 + 2), a
                ld      a, (PASS_PC + 3)
                ld      (encode_adrp_imm_pc8 + 3), a
                ld      hl, ORIGIN_HIGH
                ld      de, encode_adrp_imm_pc8 + 4
                ld      bc, 4
                ldir

; diff = t8 - pc8, LSB-first across 8 bytes, result back into t8.
                or      a                      ; clear carry
                ld      hl, encode_adrp_imm_t8
                ld      de, encode_adrp_imm_pc8
                ld      b, 8
encode_adrp_imm_sub8:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                sbc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    encode_adrp_imm_sub8

; mask33 + sign-extend bit 32.  Keep bits 0..32; bit 32 = bit 0 of byte 4.
;   byte4 &= 1
;   if byte4 != 0:  byte4 = 0xFF; byte5..7 = 0xFF
;   else:           byte4..7 = 0x00
                ld      a, (encode_adrp_imm_t8 + 4)
                and     1
                jr      z, encode_adrp_imm_sx_zero
                ld      a, &ff
encode_adrp_imm_sx_zero:
                ; A = 0x00 (bit32 clear) or 0xFF (bit32 set) → the sign byte.
                ld      hl, encode_adrp_imm_t8 + 4
                ld      (hl), a
                inc     hl
                ld      (hl), a
                inc     hl
                ld      (hl), a
                inc     hl
                ld      (hl), a
; byte 4 (encode_adrp_imm_t8+4) is now the 33-bit sign byte (0x00/0xFF),
; used below as phase-A's incoming sign extension.

; Load BCDE = bytes 3..0 of the (sign-correct) diff.
                ld      a, (encode_adrp_imm_t8 + 0)
                ld      e, a
                ld      a, (encode_adrp_imm_t8 + 1)
                ld      d, a
                ld      a, (encode_adrp_imm_t8 + 2)
                ld      c, a
                ld      a, (encode_adrp_imm_t8 + 3)
                ld      b, a

; -- Page-alignment check: low 12 bits must be zero ---------------------
; After the masked subtraction above the low 12 bits are zero by
; construction; this check is retained as a defensive invariant.
                ld      a, e
                or      a
                jp      nz, fail               ; E ≠ 0 → not 4096-aligned
                ld      a, d
                and     &0f
                jp      nz, fail               ; D[3:0] ≠ 0 → not 4096-aligned

; -- pageOffset = byteOffset >> 12 (signed) -----------------------------
; Phase A: byte-shift right by 1 byte, sign-extending from BIT 32 (the
; 33-bit sign byte we materialised at encode_adrp_imm_t8+4), NOT from
; bit 31 of B.
                ld      e, d                   ; E ← D
                ld      d, c                   ; D ← C
                ld      c, b                   ; C ← B
                ld      a, (encode_adrp_imm_t8 + 4)  ; A = 33-bit sign byte
                ld      b, a                   ; B = sign byte

; Phase B: 4-bit arithmetic right-shift (sra b; rr c; rr d; rr e) ×4.
                sra     b
                rr      c
                rr      d
                rr      e
                sra     b
                rr      c
                rr      d
                rr      e
                sra     b
                rr      c
                rr      d
                rr      e
                sra     b
                rr      c
                rr      d
                rr      e
; BCDE now holds pageOffset (signed 32-bit, sign-extended).

; -- Stash BCDE for range check -----------------------------------------
                ld      a, b
                ld      (encode_adrp_imm_copy+0), a
                ld      a, c
                ld      (encode_adrp_imm_copy+1), a
                ld      a, d
                ld      (encode_adrp_imm_copy+2), a
                ld      a, e
                ld      (encode_adrp_imm_copy+3), a

; -- Range check: ASR the COPY 20 more times ----------------------------
; After 20 arithmetic right-shifts of pageOffset, in-range value yields
; 0x00000000 (positive) or 0xFFFFFFFF (negative).  See branch_imm.asm
; for the rationale (this is the same idiom; here count = 20 because
; the slot's effective bit_width is 21 → BW-1 = 20).
                ld      a, 20
                ld      (encode_adrp_imm_cnt), a
encode_adrp_imm_range_loop:
                ld      a, (encode_adrp_imm_copy+0)
                sra     a
                ld      (encode_adrp_imm_copy+0), a
                ld      a, (encode_adrp_imm_copy+1)
                rra
                ld      (encode_adrp_imm_copy+1), a
                ld      a, (encode_adrp_imm_copy+2)
                rra
                ld      (encode_adrp_imm_copy+2), a
                ld      a, (encode_adrp_imm_copy+3)
                rra
                ld      (encode_adrp_imm_copy+3), a
                ld      a, (encode_adrp_imm_cnt)
                dec     a
                ld      (encode_adrp_imm_cnt), a
                jp      nz, encode_adrp_imm_range_loop

; Verify copy is all-0 or all-FF.
                ld      a, (encode_adrp_imm_copy+0)
                ld      h, a
                ld      a, (encode_adrp_imm_copy+1)
                or      h
                ld      h, a
                ld      a, (encode_adrp_imm_copy+2)
                or      h
                ld      h, a
                ld      a, (encode_adrp_imm_copy+3)
                or      h
                jr      z, encode_adrp_imm_in_range
                ld      a, (encode_adrp_imm_copy+0)
                ld      h, a
                ld      a, (encode_adrp_imm_copy+1)
                and     h
                ld      h, a
                ld      a, (encode_adrp_imm_copy+2)
                and     h
                ld      h, a
                ld      a, (encode_adrp_imm_copy+3)
                and     h
                cp      &ff
                jp      nz, fail
encode_adrp_imm_in_range:

; -- Mask BCDE to 21 bits (imm21) ---------------------------------------
; Keep E full, D full, C bits [4:0]; zero B, mask C with 0x1F.
                ld      b, 0
                ld      a, c
                and     &1f
                ld      c, a

; -- Stash immlo = (E & 3) BEFORE we clobber E for the immhi path -------
; immlo lives at u32 bits 29..30 in the final word.  We park it in a
; scratch byte and build immhi first (which consumes BCDE), then
; reconstruct immlo at the end and OR into the running result.
                ld      a, e
                and     3
                ld      (encode_adrp_imm_immlo), a

; -- Build (immhi << 5) in DEHL ----------------------------------------
; immhi = (imm21 >> 2) & ((1<<19) - 1).  Currently BCDE = imm21
; (21-bit value, B=0, C&=0x1F).  Shift BCDE right by 2 logical to get
; (imm21 >> 2); the result has at most 19 significant bits, so masking
; to 19 is a no-op.
;
; Logical right-shift: srl b ; rr c ; rr d ; rr e.
                srl     b
                rr      c
                rr      d
                rr      e
                srl     b
                rr      c
                rr      d
                rr      e
; BCDE now = (imm21 >> 2) zero-extended.  Highest set bit is bit 18.
; Mask to 19 bits (already true: imm21 was 21 bits → shifted right
; 2 = 19 bits in BCDE).  Now shift left 5 to land in target bits 5..23.
;
; Need to copy BCDE into DEHL.  Layout:
;   target HL bits 0..15 = (D-byte<<8) | E-byte
;   target DE bits 16..31 = (B-byte<<8) | C-byte
                ld      h, d
                ld      l, e
                ld      d, b
                ld      e, c

; Shift DEHL left 5 times.
                ld      b, 5
encode_adrp_imm_immhi_shift:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_adrp_imm_immhi_shift

; -- Save the immhi-positioned word on the stack ------------------------
                push    de                 ; running result hi
                push    hl                 ; running result lo

; -- Build (immlo << 29) in a fresh DEHL --------------------------------
; immlo (in [0..3]) sits at u32 bits 29..30.  Build it via
; shift-left-29:
;   start with L = immlo, zero everywhere else
;   shift left 16 by byte-pair swap (DE ← HL, HL ← 0)
;   shift left 13 more via the normal add hl,hl / rl e / rl d loop.
                ld      a, (encode_adrp_imm_immlo)
                ld      l, a
                ld      h, 0
                ld      d, 0
                ld      e, 0
; Step 1: shift left 16 (byte-pair swap).
                ld      d, h
                ld      e, l
                ld      h, 0
                ld      l, 0
; Step 2: shift left 13 more.
                ld      b, 13
encode_adrp_imm_immlo_shift:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_adrp_imm_immlo_shift

; -- OR with the saved immhi-positioned word ----------------------------
                pop     bc                 ; BC = saved HL (low 16 of immhi mask)
                ld      a, l
                or      c
                ld      l, a
                ld      a, h
                or      b
                ld      h, a
                pop     bc                 ; BC = saved DE (high 16 of immhi mask)
                ld      a, e
                or      c
                ld      e, a
                ld      a, d
                or      b
                ld      d, a
                ret


; -----------------------------------------------------------------------
; encode_adr_imm — AdrImm constant-offset encoder (ADR Xd, <label>).
;
; Z80 port of tools/aarch64enc/slots_adrp.go::encodeAdrImm (lines 26-38).
; The (immlo:2, immhi:19) bit layout is IDENTICAL to adrp (bits 29..30
; and 5..23), so we share adrp's imm21-packing tail.  The differences
; from adrp:
;   - the offset is a RAW byte offset (NOT page-aligned, NOT >>12'd):
;     value = target - PASS_PC, no masking of either operand.
;   - range is ±1 MB i.e. the 21-bit signed range [-2^20, 2^20).
;
; M4 caller contract (same as encode_adrp_imm): BCDE = ABSOLUTE target
; address, big-endian register packing (B=hi .. E=lo).  PASS_PC holds
; the current instruction's PC (4-byte LE).  Output DEHL = encoded word.
; Clobbers A, BC, DE, HL.  Errors: jp fail when the byte offset doesn't
; fit in 21-bit signed (±1 MB).
;
; Grounded against aarch64-none-elf-as + ARM ARM C6.2.10 (ADR).
; -----------------------------------------------------------------------
encode_adr_imm:
; -- value = target - PASS_PC (raw 32-bit two's-complement, LSB-first) --
; No page masking (unlike adrp).  PASS_PC is little-endian at PASS_PC+0..3.
                ld      a, e
                ld      hl, PASS_PC
                sub     (hl)
                ld      e, a
                ld      a, d
                inc     hl
                sbc     a, (hl)
                ld      d, a
                ld      a, c
                inc     hl
                sbc     a, (hl)
                ld      c, a
                ld      a, b
                inc     hl
                sbc     a, (hl)
                ld      b, a
; BCDE now = signed byte offset (target - PASS_PC), sign-extended to 32.

; -- Range check: byte offset must fit in 21-bit signed (±1 MB) ---------
; Same ASR-20-times idiom as adrp's pageOffset check, but applied to the
; RAW byte offset.  In-range yields 0x00000000 or 0xFFFFFFFF.
                ld      a, b
                ld      (encode_adrp_imm_copy+0), a
                ld      a, c
                ld      (encode_adrp_imm_copy+1), a
                ld      a, d
                ld      (encode_adrp_imm_copy+2), a
                ld      a, e
                ld      (encode_adrp_imm_copy+3), a
                ld      a, 20
                ld      (encode_adrp_imm_cnt), a
encode_adr_imm_range_loop:
                ld      a, (encode_adrp_imm_copy+0)
                sra     a
                ld      (encode_adrp_imm_copy+0), a
                ld      a, (encode_adrp_imm_copy+1)
                rra
                ld      (encode_adrp_imm_copy+1), a
                ld      a, (encode_adrp_imm_copy+2)
                rra
                ld      (encode_adrp_imm_copy+2), a
                ld      a, (encode_adrp_imm_copy+3)
                rra
                ld      (encode_adrp_imm_copy+3), a
                ld      a, (encode_adrp_imm_cnt)
                dec     a
                ld      (encode_adrp_imm_cnt), a
                jp      nz, encode_adr_imm_range_loop
; Verify copy is all-0 or all-FF.
                ld      a, (encode_adrp_imm_copy+0)
                ld      h, a
                ld      a, (encode_adrp_imm_copy+1)
                or      h
                ld      h, a
                ld      a, (encode_adrp_imm_copy+2)
                or      h
                ld      h, a
                ld      a, (encode_adrp_imm_copy+3)
                or      h
                jr      z, encode_adr_imm_pack
                ld      a, (encode_adrp_imm_copy+0)
                ld      h, a
                ld      a, (encode_adrp_imm_copy+1)
                and     h
                ld      h, a
                ld      a, (encode_adrp_imm_copy+2)
                and     h
                ld      h, a
                ld      a, (encode_adrp_imm_copy+3)
                and     h
                cp      &ff
                jp      nz, fail
encode_adr_imm_pack:
; BCDE = raw byte offset (in range).  The imm21 mask + immlo/immhi pack
; is identical to adrp — reuse it.
                jp      encode_adrp_imm_in_range


; -----------------------------------------------------------------------
; Scratch bytes — range-check loop counter, a 4-byte copy of the
; shifted pageOffset, a 1-byte stash for immlo, and a 4-byte buffer
; for the masked PASS_PC (M4 page-base subtraction).
; -----------------------------------------------------------------------
encode_adrp_imm_cnt:
                defb    0
encode_adrp_imm_copy:
                defb    0, 0, 0, 0
encode_adrp_imm_immlo:
                defb    0
; 64-bit working buffers for the full-width page-difference (Class 5):
;   t8  — page-masked target v, then diff (LSB-first, 8 bytes)
;   pc8 — page-masked pc (PASS_PC low ++ ORIGIN_HIGH high), 8 bytes
encode_adrp_imm_t8:
                defb    0, 0, 0, 0, 0, 0, 0, 0
encode_adrp_imm_pc8:
                defb    0, 0, 0, 0, 0, 0, 0, 0
