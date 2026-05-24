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
; docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.5:
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
; -- M4: mask BCDE with ~0xFFF, then subtract (PASS_PC & ~0xFFF) --------
; Caller passes the ABSOLUTE target address in BCDE.  We compute the
; page-difference: (target & ~0xFFF) - (pc & ~0xFFF).  Both operands
; are first masked to their 4 KB page bases by clearing the low 12
; bits — for BCDE that means E := 0, D := D & 0xF0.  PASS_PC is read
; into a 4-byte temp and the same mask applied there.  Then a full
; 32-bit two's-complement subtract LSB-first.
                xor     a
                ld      e, a                   ; clear low 8 bits of target
                ld      a, d
                and     &f0                    ; clear bits 8..11 of target
                ld      d, a
; Build (PASS_PC & ~0xFFF) in encode_adrp_imm_pcpage[0..3] (LE).
                xor     a
                ld      (encode_adrp_imm_pcpage + 0), a    ; low byte := 0
                ld      a, (PASS_PC + 1)
                and     &f0                                ; clear bits 8..11
                ld      (encode_adrp_imm_pcpage + 1), a
                ld      a, (PASS_PC + 2)
                ld      (encode_adrp_imm_pcpage + 2), a
                ld      a, (PASS_PC + 3)
                ld      (encode_adrp_imm_pcpage + 3), a
; BCDE -= (PASS_PC & ~0xFFF), LSB-first across 4 bytes.
                ld      a, e
                ld      hl, encode_adrp_imm_pcpage
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
; Phase A: byte-shift right by 1 byte, sign-extending B.
                ld      e, d                   ; E ← D
                ld      d, c                   ; D ← C
                ld      c, b                   ; C ← B
; New B = sign-extension of old B: 0xFF if B's bit 7 set, else 0x00.
                ld      a, b                   ; A = old B
                add     a, a                   ; CY ← old bit 7 of B
                sbc     a, a                   ; A = 0xFF if CY, else 0x00
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
encode_adrp_imm_pcpage:
                defb    0, 0, 0, 0
