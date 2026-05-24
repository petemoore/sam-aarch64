; slots/branch_imm.asm — BranchImm26 / BranchImm19 / BranchImm14
;                         constant-offset encoder.
;
; Z80 port of tools/aarch64enc/slots_branch.go::encodeBranchImm
; (lines 5-22):
;
;     func encodeBranchImm(slot OperandSlot, byteOffset int64)
;             (uint32, error) {
;         if byteOffset%4 != 0 {
;             return 0, fmt.Errorf("... not 4-byte aligned ...")
;         }
;         instrOffset := byteOffset / 4
;         half := int64(1) << (slot.BitWidth - 1)
;         if instrOffset >= half || instrOffset < -half {
;             return 0, fmt.Errorf("... out of range ±%d", half)
;         }
;         mask := (uint32(1) << slot.BitWidth) - 1
;         bits := uint32(instrOffset) & mask
;         return bits << slot.BitPosition, nil
;     }
;
; All three slot kinds share one body — only bit_width and bit_position
; differ (consumed from the slot record):
;
;   BranchImm26 (B / BL):  bit_width=26, bit_position=0   (SlotKind=0x20)
;   BranchImm19 (B.cond, CBZ, CBNZ): bit_width=19, BP=5   (SlotKind=0x21)
;   BranchImm14 (TBZ, TBNZ):         bit_width=14, BP=5   (SlotKind=0x22)
;
; M4 caller contract: BCDE is the ABSOLUTE target address (the resolved
; value of a label or PC-relative expression).  This routine subtracts
; PASS_PC (the PC of the instruction currently being assembled, u32 LE
; at &C159) from BCDE before applying the existing range-check + bit-
; pack body.  The subtracted result IS the signed byteOffset the older
; M3-style body operated on.
;
; Per docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.5:
;   if slot.SlotKind in {BranchImm26, BranchImm19, BranchImm14}:
;       value = value - current_pc
;
; M3 fixtures never exercise branches/adrp, so the M3 corpus continues
; to byte-match GNU regardless of the new subtraction step (the encoder
; code path isn't hit).  M4 fixtures (PR 3) exercise it end-to-end.
;
; The largest plausible input AFTER subtraction for BW=26 is
; ±(1<<27 - 4) = ±0x07FFFFFC, well within int32; the subtraction itself
; is a full 32-bit two's-complement subtract via SUB/SBC, so any
; underflow at the high byte is absorbed and the subsequent range check
; catches out-of-range offsets cleanly.
;
; -----------------------------------------------------------------------
; Calling convention — wide signed single-operand
; -----------------------------------------------------------------------
; Reuses the BCDE big-endian convention established by
; encode_imm12_shifted, reinterpreted as signed two's-complement:
;
;   HL    = pointer to 4-byte slot record (consumed → low word of DEHL).
;   BCDE  = signed 32-bit ABSOLUTE address, big-endian register packing:
;             B = bits 24..31  (hi byte, includes sign bit at bit 7)
;             C = bits 16..23
;             D = bits  8..15
;             E = bits  0.. 7  (lo byte)
;           For tests that want to exercise the encoder body directly
;           (independent of PASS_PC) set PASS_PC = 0 first — then BCDE
;           effectively IS the byteOffset and the old M3 semantics
;           are recovered.
;
; Output:
;   DEHL  = 32-bit encoded value (DE = bits 16..31, HL = bits 0..15).
;
; Error path:
;   byteOffset % 4 ≠ 0                          → jp fail
;   instrOffset = byteOffset / 4 ∉ [-half, half) → jp fail
;     where half = 1 << (bit_width - 1)
;
; Clobbers: A, BC, DE, HL.
;
; -----------------------------------------------------------------------
; Implementation outline
; -----------------------------------------------------------------------
;
;   1. Alignment check: E[1:0] == 0.
;
;   2. Arithmetic right-shift BCDE by 2 to form instrOffset (signed
;      div by 4).
;
;   3. Range check: instrOffset must satisfy
;        bits [31:BW-1] all equal sign(bit (BW-1)).
;      Implemented by copying BCDE to memory, then arithmetic-shifting
;      the COPY right (BW-1) more times.  In-range ⇔ copy is all-zero
;      (positive) or all-ones (negative).
;
;   4. Mask BCDE (the unmangled instrOffset) to the bottom BW bits.
;
;   5. Repack BCDE → DEHL (H=D, L=E, D=B, E=C) so the low half of the
;      target u32 sits in HL.  This places bit 0 of instrOffset at bit
;      0 of HL.
;
;   6. Shift DEHL left by bit_position.
;
; The Z80's only 4-byte register set (BCDE) holds the live value, and
; djnz wants B as its counter, so we use a memory byte
; (encode_branch_imm_cnt) for the (BW-1)-iteration count.  Loop body:
;
;     ld   a, (cnt)
;     dec  a
;     ld   (cnt), a
;     ...                 ; one SRA pass on the copy
;     jp   nz, loop_top
;
; The copy lives in encode_branch_imm_copy (4 bytes), so we can mutate
; freely without losing instrOffset.
;
; -----------------------------------------------------------------------
encode_branch_imm:
; -- Read bit_position and bit_width into scratch -----------------------
                inc     hl
                inc     hl                 ; HL → bit_position
                ld      a, (hl)
                ld      (encode_branch_imm_bp), a
                inc     hl                 ; HL → bit_width
                ld      a, (hl)
                ld      (encode_branch_imm_bw), a

; -- M4: subtract PASS_PC from BCDE -------------------------------------
; Caller now passes the ABSOLUTE target address in BCDE.  The encoder
; body below operates on the signed byteOffset = target - PC, so we
; convert here before running the alignment check.
;
; Subtraction is LSB-first across 4 bytes:
;   E -= PASS_PC[0]                 (SUB clears carry)
;   D -= PASS_PC[1] + borrow        (SBC)
;   C -= PASS_PC[2] + borrow
;   B -= PASS_PC[3] + borrow
;
; PASS_PC is treated as a u32; full 32-bit two's-complement subtraction
; absorbs any borrow at the high byte (negative results are sign-
; correct in BCDE).  The downstream range check rejects offsets that
; don't fit the slot's BW.
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

; -- Alignment check: byteOffset & 3 == 0 -------------------------------
                ld      a, e
                and     3
                jp      nz, fail

; -- Arithmetic right-shift BCDE by 2 -----------------------------------
; sra b → bit 7 of B replicated (sign-extension); old bit 0 → CY.
; rr c/d/e → CY → bit 7; old bit 0 → CY.
                sra     b
                rr      c
                rr      d
                rr      e
                sra     b
                rr      c
                rr      d
                rr      e

; -- Stash BCDE into the range-check scratch copy -----------------------
; We need the original instrOffset (in BCDE) for the pack/mask phase
; after the range check, so save a copy now.
                ld      a, b
                ld      (encode_branch_imm_copy+0), a
                ld      a, c
                ld      (encode_branch_imm_copy+1), a
                ld      a, d
                ld      (encode_branch_imm_copy+2), a
                ld      a, e
                ld      (encode_branch_imm_copy+3), a

; -- Range check: arithmetic-shift the COPY right (BW-1) times ----------
; The copy lives in memory (4 bytes, big-endian: [0]=B, [1]=C,
; [2]=D, [3]=E).  We can't sra a memory byte directly — load each byte
; into A, sra/rr it, store back.  Doing the inner shift cycle:
;
;     sra (copy+0)         ; via A
;     rr  (copy+1)         ; via A
;     rr  (copy+2)         ; via A
;     rr  (copy+3)         ; via A
;
; Z80 has no `sra (nn)` so we round-trip through A:
;     ld a, (copy+0); sra a; ld (copy+0), a   ; CY = old bit 0
;     ld a, (copy+1); rra;   ld (copy+1), a   ; CY in ↑; CY out via bit 0
;     ld a, (copy+2); rra;   ld (copy+2), a
;     ld a, (copy+3); rra;   ld (copy+3), a
;
; rra rotates A right through carry (9-bit rotation through CY) —
; behaviour identical to rr a but encoded as a single-byte op.
;
; Loop iteration count = BW - 1.  BW is 14, 19, or 26 → count is 13,
; 18, or 25.  We park the counter in a separate memory byte so B/C/D/E
; can stay as the live shifted values (BCDE is needed AFTER the loop
; in two ways — see below — so although BCDE here holds a STALE copy
; of instrOffset, we reload it from the memory copy after the range
; check is done).
;
; Wait, that's confusing.  Re-do the bookkeeping:
;
;   At this point, BCDE = instrOffset.
;   We've saved instrOffset to (copy+0..3).
;   We're about to shift (copy+0..3) in place.  BCDE itself stays as
;   instrOffset throughout — we never touch it during the range check.
;
; (Earlier draft used registers for the shift and lost the value; the
; in-memory shift below is simpler.)
                ld      a, (encode_branch_imm_bw)
                dec     a                  ; cnt = BW - 1
                ld      (encode_branch_imm_cnt), a
                or      a                  ; set Z if cnt == 0
                jr      z, encode_branch_imm_range_done

encode_branch_imm_range_loop:
                ld      a, (encode_branch_imm_copy+0)
                sra     a
                ld      (encode_branch_imm_copy+0), a   ; CY = old bit 0 of B
                ld      a, (encode_branch_imm_copy+1)
                rra
                ld      (encode_branch_imm_copy+1), a   ; CY in: top bit of B's old bit 0; out: C's bit 0
                ld      a, (encode_branch_imm_copy+2)
                rra
                ld      (encode_branch_imm_copy+2), a
                ld      a, (encode_branch_imm_copy+3)
                rra
                ld      (encode_branch_imm_copy+3), a
                ld      a, (encode_branch_imm_cnt)
                dec     a
                ld      (encode_branch_imm_cnt), a
                jp      nz, encode_branch_imm_range_loop
encode_branch_imm_range_done:

; -- Verify the shifted copy is all-zero or all-ones --------------------
; In-range positive instrOffset → copy = 0x00000000.
; In-range negative instrOffset → copy = 0xFFFFFFFF.
; Out-of-range            → mixed.
                ld      a, (encode_branch_imm_copy+0)
                ld      h, a               ; H = byte 0 of copy
                ld      a, (encode_branch_imm_copy+1)
                or      h
                ld      h, a               ; H now = copy[0] | copy[1]
                ld      a, (encode_branch_imm_copy+2)
                or      h
                ld      h, a
                ld      a, (encode_branch_imm_copy+3)
                or      h
                jr      z, encode_branch_imm_in_range     ; all four bytes 0
                ld      a, (encode_branch_imm_copy+0)
                ld      h, a
                ld      a, (encode_branch_imm_copy+1)
                and     h
                ld      h, a
                ld      a, (encode_branch_imm_copy+2)
                and     h
                ld      h, a
                ld      a, (encode_branch_imm_copy+3)
                and     h
                cp      &ff
                jp      nz, fail
encode_branch_imm_in_range:

; -- Mask BCDE to the bottom bit_width bits -----------------------------
; The high (32-BW) bits should be the sign-extension of bit (BW-1); we
; replace them with zero so OR-merge into the final word works.
;
; Strategy: compute byte_index = (BW-1) >> 3 and bit_in_byte = (BW-1) & 7.
; The surviving high byte keeps its low (bit_in_byte+1) bits via mask
; (2 << bit_in_byte) - 1; bytes above it become zero.
                ld      a, (encode_branch_imm_bw)
                dec     a                  ; A = BW - 1
                ld      l, a               ; L = BW-1 (preserved across mask build)

                and     7                  ; A = bit_in_byte
                ld      h, a               ; H scratch: shift count
                inc     h                  ; H = bit_in_byte + 1
                ld      a, 1
encode_branch_imm_mask_build:
                add     a, a
                dec     h
                jr      nz, encode_branch_imm_mask_build
                dec     a                  ; A = (1 << (bit_in_byte+1)) - 1
                ld      h, a               ; H = surviving-byte mask

; Compute byte index = (BW-1) >> 3.  A still holds the mask; recover
; BW-1 from L.
                ld      a, l
                rrca
                rrca
                rrca
                and     &1f                ; A = (BW-1) >> 3, ∈ {0,1,2,3}

                cp      0
                jr      nz, encode_branch_imm_mask_not0
; byte index 0: mask E, zero B/C/D.
                ld      a, e
                and     h
                ld      e, a
                ld      d, 0
                ld      c, 0
                ld      b, 0
                jp      encode_branch_imm_pack
encode_branch_imm_mask_not0:
                cp      1
                jr      nz, encode_branch_imm_mask_not1
; byte index 1: keep E, mask D, zero B/C.
                ld      a, d
                and     h
                ld      d, a
                ld      c, 0
                ld      b, 0
                jp      encode_branch_imm_pack
encode_branch_imm_mask_not1:
                cp      2
                jr      nz, encode_branch_imm_mask_not2
; byte index 2: keep E,D, mask C, zero B.
                ld      a, c
                and     h
                ld      c, a
                ld      b, 0
                jp      encode_branch_imm_pack
encode_branch_imm_mask_not2:
; byte index 3 (BW in 25..32): keep E,D,C, mask B.
                ld      a, b
                and     h
                ld      b, a
                ; fall through

encode_branch_imm_pack:
; -- Repack BCDE → DEHL --------------------------------------------------
; Target layout:
;   HL = bits 0..15  (H = D-byte, L = E-byte)
;   DE = bits 16..31 (D = B-byte, E = C-byte)
                ld      h, d               ; H = D-byte
                ld      l, e               ; L = E-byte
                ld      d, b               ; D = B-byte
                ld      e, c               ; E = C-byte

; -- Shift DEHL left by bit_position ------------------------------------
                ld      a, (encode_branch_imm_bp)
                ld      b, a
                inc     b
                dec     b
                ret     z                  ; bp == 0 → done
encode_branch_imm_shift_loop:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_branch_imm_shift_loop
                ret


; -----------------------------------------------------------------------
; Scratch bytes — bit_position, bit_width, range-check loop counter,
; and a 4-byte copy of the shifted instrOffset.  The copy lives in
; memory because the Z80 has no spare 32-bit register set and we need
; the original BCDE preserved for the pack/mask phase.
; -----------------------------------------------------------------------
encode_branch_imm_bp:
                defb    0
encode_branch_imm_bw:
                defb    0
encode_branch_imm_cnt:
                defb    0
encode_branch_imm_copy:
                defb    0, 0, 0, 0
