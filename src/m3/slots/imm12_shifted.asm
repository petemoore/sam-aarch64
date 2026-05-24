; slots/imm12_shifted.asm — Imm12Shifted slot encoder.
;
; Z80 port of tools/aarch64enc/slots_imm.go::encodeImm12Shifted
; (lines 8-22):
;
;     func encodeImm12Shifted(slot OperandSlot, v int64) (uint32, error) {
;         if v >= 0 && v < (1<<12) {
;             return uint32(v) << slot.BitPosition, nil
;         }
;         if v >= (1<<12) && v < (1<<24) && (v&0xFFF) == 0 {
;             shiftBit := uint32(1) << (slot.BitPosition + 12)
;             return uint32(v>>12)<<slot.BitPosition | shiftBit, nil
;         }
;         return 0, fmt.Errorf("Imm12Shifted: %d ...", v)
;     }
;
; Encodes the (sh:1, imm12:12) pair used by ADD/SUB immediate:
;   • Case A: v in [0, 4095]           → imm12=v,        sh=0
;   • Case B: v in [4096, 16777215]
;             with (v & 0xFFF) == 0    → imm12=v>>12,    sh=1
;   • Anything else                    → jp fail
;
; The sh bit lives at slot.BitPosition + 12; imm12 at slot.BitPosition.
;
; -----------------------------------------------------------------------
; Wide-operand calling convention (this file forward)
; -----------------------------------------------------------------------
; This is the FIRST slot encoder whose operand exceeds 8 bits, so it
; establishes the convention reused by future >8-bit slot encoders
; (Imm16Shifted, BranchImm26/19/14, AdrpImm, LogicalImm, BitfieldImm,
; AdrImm).
;
;   HL    = pointer to 4-byte slot record (unchanged from trivial
;           encoders — HL is consumed by the routine and forms the low
;           word of the DEHL result, exactly as in encode_reg /
;           encode_imm_n).
;   BCDE  = 32-bit operand value, big-endian register packing:
;             B = bits 24..31  (hi byte)
;             C = bits 16..23
;             D = bits  8..15
;             E = bits  0.. 7  (lo byte)
;           Rationale: a caller can `ld bc, hi16` then `ld de, lo16`
;           and the value reads naturally in source order
;           (`ld bc, &0000 / ld de, &1000` → BCDE = 0x00001000).
;           DEHL was rejected because HL is reserved for the slot
;           pointer on entry, and reusing DEHL for the input would
;           shadow the output register pair.
;
; Output:
;   DEHL  = 32-bit encoded value (DE = bits 16..31, HL = bits 0..15),
;           matching encode_reg / encode_imm_n.  Suitable for ORing
;           into a future instruction-word accumulator.
;
; Error path:
;   jp fail for any value the Go side would reject (no recovery —
;   one-way red border + halt).
;
; -----------------------------------------------------------------------
; encode_imm12_shifted
; -----------------------------------------------------------------------
;
; Input/output/error: see "Wide-operand calling convention" above.
; Clobbers: A, BC, DE, HL.
;
; Implementation notes:
;
;   • The slot record's bit_position is at offset 2 (per
;     docs/specs/2026-05-24-m2-encoder-tables-design.md §2 lines 71-75).
;     We grab it into A early, before the BCDE-consuming checks, and
;     park it in a scratch slot (encode_imm12_shifted_bp) because the
;     Z80 has no spare register pair once HL is freed.
;
;   • Bit layout of the 12-bit payload across the D/E pair:
;       Case A — payload lives in (D[3:0], E[7:0]).  After validation
;                that B=C=0 and D≤0x0F, the 12-bit value is already
;                positioned correctly in the low 12 bits of D:E.
;                Move into HL (H=D, L=E), zero DE, shift left bp.
;       Case B — payload is v>>12, which equals (C[7:0]<<4) | (D[7:4]).
;                That is: 12-bit value spread across C and high nibble
;                of D.  Shift (C,D) right by 4 to repack into a 12-bit
;                value in the same (D[3:0], E[7:0]) form as Case A,
;                then follow the same shift-and-return path, finally
;                setting the sh bit at position bp+12.
;
;   • Local-label naming convention (established in slots/xreg.asm):
;     all local labels prefix the routine name to keep pyz80's flat
;     symbol space collision-free.
; -----------------------------------------------------------------------
encode_imm12_shifted:
; -- Stash bit_position from the slot record ----------------------------
; HL → slot record.  Walk +2 to bit_position.  We don't need
; bit_width: this encoder hard-codes the 12-bit imm12 layout (Go side
; line 14 ignores slot.BitWidth too — the value is the field width).
                inc     hl
                inc     hl                 ; HL → bit_position byte
                ld      a, (hl)            ; A = bit_position
                ld      (encode_imm12_shifted_bp), a

; -- Case A test: v in [0, 4095] ----------------------------------------
; v < 4096  ⇔  B=0, C=0, D ≤ 0x0F.
                ld      a, b
                or      a
                jp      nz, encode_imm12_shifted_try_b   ; B ≠ 0 → not Case A
                ld      a, c
                or      a
                jp      nz, encode_imm12_shifted_try_b   ; C ≠ 0 → not Case A
                ld      a, d
                and     &f0                ; high nibble of D
                jp      nz, encode_imm12_shifted_try_b   ; D > 0x0F → not Case A

; -- Case A encode: payload (D[3:0], E) → HL, DE=0, shift left bp -------
; D already in [0, 0x0F]; E carries the low byte.  Move into HL.
                ld      h, d
                ld      l, e
                ld      d, 0
                ld      e, 0
                jp      encode_imm12_shifted_shift_bp    ; tail into shared shift

; -- Case B test: v in [4096, 16777215] and low 12 bits all zero --------
; v < (1<<24)            ⇔  B = 0.
; v ≥ (1<<12)            ⇔  C ≠ 0  OR  (D & 0xF0) ≠ 0.  (We've already
;                             established D & 0xF0 == 0 fails Case A, so
;                             if we reach here, either C ≠ 0 or
;                             D ≥ 0x10.  Either way v ≥ 4096.)
;                          → no explicit lower-bound check needed; the
;                            B=0 + (E=0, D&0x0F=0) tests below combined
;                            with "Case A already rejected" imply
;                            v ≥ 4096.  Documented here so a reviewer
;                            doesn't expect a (1<<12) compare.
; (v & 0xFFF) == 0       ⇔  E = 0  AND  (D & 0x0F) = 0.
encode_imm12_shifted_try_b:
                ld      a, b
                or      a
                jp      nz, fail           ; B ≠ 0 → v ≥ (1<<24)
                ld      a, e
                or      a
                jp      nz, fail           ; low byte non-zero → not lsl-12-aligned
                ld      a, d
                and     &0f
                jp      nz, fail           ; low nibble of D non-zero → not lsl-12-aligned

; -- Case B encode: payload = v>>12 = (C<<4) | (D>>4) -------------------
; Equivalent transformation: shift the (C, D) byte-pair right by 4.
; After the shift:
;     new D = C >> 4   (top 4 bits of the 12-bit payload)
;     new E = (C << 4) | (D >> 4)   (bottom 8 bits of the payload)
;
; We perform the right-shift via four srl/rr passes on the (C, D) pair,
; producing the payload in the (D[3:0], E[7:0]) form that matches Case
; A's intermediate layout.  Then we fall through into the shared
; "shift-left-by-bp" tail, and finally set the sh bit.
                ld      b, 4               ; 4 right-shifts of (C, D)
encode_imm12_shifted_b_repack:
                srl     c                  ; C >>= 1; bit 0 → CY
                rr      d                  ; D = (CY << 7) | (D >> 1); old D bit 0 → CY discarded
                djnz    encode_imm12_shifted_b_repack
; (C, D) now hold the right-shifted pair.  Concretely, after 4 srl/rr
; passes, C's original 8 bits have moved into D's high 4 (via CY chain)
; while C's high 4 bits ended up in C[3:0]:
;     final C = old_C >> 4             (in C[3:0]; C[7:4] = 0)
;     final D = (old_C << 4) | (old_D >> 4)
; This matches the formulae above (final_C is the high nibble of the
; 12-bit payload, final_D is the low 8 bits).  Move into (H, L) using
; the same convention as Case A: H = payload_hi_nibble, L = payload_lo_byte.
                ld      h, c               ; H = 12-bit payload [11:8]  (≤ 0x0F)
                ld      l, d               ; L = 12-bit payload  [7:0]
                ld      d, 0
                ld      e, 0

; Fall through to shift, but remember we still need to set sh bit (B path).
; We use B (now 0 after djnz) as a flag is risky; instead jp into a
; distinct continuation that sets the sh bit after the shared shift.
                jp      encode_imm12_shifted_shift_bp_then_sh

; -- Shared tail: DEHL holds the 12-bit payload, shift left by bp -------
; bp is in the scratch byte encode_imm12_shifted_bp.
; bp can be 0..(31-12)=19 for any sane Imm12Shifted slot — within the
; 32-bit width.  We use the same djnz-with-inc/dec-skip idiom as the
; trivial encoders to handle bp=0 safely.
encode_imm12_shifted_shift_bp:
                ld      a, (encode_imm12_shifted_bp)
                ld      b, a
                inc     b
                dec     b
                ret     z                  ; bp == 0 → DEHL already correct
encode_imm12_shifted_shift_loop_a:
                add     hl, hl             ; HL <<= 1; bit 15 → CY
                rl      e
                rl      d
                djnz    encode_imm12_shifted_shift_loop_a
                ret

; -- Case B tail: same shift, then set sh bit at position bp+12 ---------
encode_imm12_shifted_shift_bp_then_sh:
                ld      a, (encode_imm12_shifted_bp)
                ld      b, a
                inc     b
                dec     b
                jr      z, encode_imm12_shifted_set_sh
encode_imm12_shifted_shift_loop_b:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_imm12_shifted_shift_loop_b

encode_imm12_shifted_set_sh:
; Set bit (bp + 12) of DEHL.
; Strategy: build a single-bit mask at position (bp + 12) in a DEHL-
; shaped scratch register pair, then OR into DEHL.
;
; Equivalent (cheaper) strategy used here: position the mask directly
; by walking a 1-bit through DEHL.  Start with DEHL_mask = 0x00001000
; (bit 12) and shift left bp times, then OR into the running result.
;
; To avoid a second scratch quad, we instead do this:
;   • Snapshot DEHL into (HL', DE') via EXX-style push/pop.  Z80 has
;     no quick way to hold two 32-bit values, so we push DE and HL,
;     reuse those registers to build the mask, then pop and OR.
                push    de                 ; save running result hi
                push    hl                 ; save running result lo
                ld      hl, &1000          ; mask seed = 1 << 12
                ld      d, 0
                ld      e, 0
                ld      a, (encode_imm12_shifted_bp)
                ld      b, a
                inc     b
                dec     b
                jr      z, encode_imm12_shifted_sh_or
encode_imm12_shifted_sh_shift:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_imm12_shifted_sh_shift
encode_imm12_shifted_sh_or:
; Mask is now in DEHL.  OR with the saved running result on the stack.
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
; encode_imm12_shifted_bp — 1-byte scratch for bit_position.
;
; Lives in section A code RAM alongside the encoder.  We don't have a
; spare register pair across the multi-stage encode (HL is consumed,
; BCDE holds the 32-bit input, A is the working byte), so the
; bit_position is parked here at routine entry and read three times:
;   (a) by the shared shift-bp tail after Case A,
;   (b) by the Case-B shift tail,
;   (c) by the sh-bit-mask construction.
; -----------------------------------------------------------------------
encode_imm12_shifted_bp:
                defb    0
