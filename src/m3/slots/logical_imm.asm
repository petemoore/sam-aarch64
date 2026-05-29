; slots/logical_imm.asm — LogicalImm slot encoder.
;
; Z80 port of tools/aarch64enc/slots_logical.go::encodeLogicalImm
; (the corrected version added by commit 2eb3762, which fixed the
; immr inverse-rotation calculation — that commit is part of PR #15
; on the Mac side, currently awaiting merge).
;
; Implements ARM's bitmask-immediate encoding (N:1, immr:6, imms:6),
; following LLVM's processLogicalImmediate.  Returns the 13-bit packed
; (N|immr|imms) at slot.BitPosition.
;
; SlotKind handled here:
;   LogicalImm = 0x24
;
; -----------------------------------------------------------------------
; Algorithm (mirrors slots_logical.go::encodeLogicalImm exactly)
; -----------------------------------------------------------------------
;
;   1. u = imm (64-bit).
;      If !is64: u = (u & 0xFFFFFFFF) | ((u & 0xFFFFFFFF) << 32).
;
;   2. Reject u == 0 or u == 0xFFFFFFFFFFFFFFFF.
;
;   3. Find smallest replicating element size.  Start at 64, halve
;      while low half == high half.  Stop at 2 or first mismatch.
;
;   4. If size < 64, verify u truly replicates element across all 64
;      bits (defence-in-depth check, mirrored from the Go side).
;
;   5. Count ones in element.  Reject ones == 0 or ones == size.
;
;   6. Find rotation r ∈ [0, size) such that ROR(element, r) within
;      `size` bits == (1<<ones) - 1.  If no such r exists, reject.
;
;   7. immr = (rotation == 0) ? 0 : size - rotation.
;
;   8. N and imms:
;        size == 64 → N=1, imms = ones - 1.
;        else       → N=0, sizeLog2 = log2(size),
;                     nimmsTop = ~((1<<(sizeLog2+1))-1) & 0x3F,
;                     imms = nimmsTop | (ones - 1).
;
;   9. combined = (N<<12) | (immr<<6) | imms   (13 bits).
;  10. Output = combined << slot.BitPosition.
;
; -----------------------------------------------------------------------
; Calling convention — 64-bit single-operand
; -----------------------------------------------------------------------
; LogicalImm is the first encoder whose operand exceeds 32 bits, so it
; establishes a new convention:
;
;   HL = pointer to 4-byte slot record (consumed → low word of DEHL).
;   DE = pointer to 8-byte little-endian buffer containing imm.
;          Caller fills 8 bytes, low byte first: byte 0 is bits 0..7,
;          byte 7 is bits 56..63.  For 32-bit inputs the high 4 bytes
;          MAY be garbage (the encoder masks-and-replicates internally
;          per the Go reference).
;   C  = is64 flag (0 → 32-bit; non-zero → 64-bit).
;          C is chosen over A so the slot-record fetch can use A
;          freely without needing to stash the flag first.
;
; Output:
;   DEHL = 32-bit encoded value (DE = bits 16..31, HL = bits 0..15).
;
; Error path:
;   any reject condition above  →  jp fail.
;
; Clobbers: A, BC, DE, HL.
;
; Local-label prefix: encode_logical_imm_*
;
; -----------------------------------------------------------------------
encode_logical_imm:
; -- Stash is64 flag (C) into scratch -----------------------------------
                ld      a, c
                ld      (encode_logical_imm_is64), a

; -- Stash slot's bit_position ------------------------------------------
                inc     hl
                inc     hl
                ld      a, (hl)
                ld      (encode_logical_imm_bp), a

; -- Copy 8 bytes from (DE) to `u` --------------------------------------
                ex      de, hl             ; HL = caller's buffer
                ld      de, encode_logical_imm_u
                ld      bc, 8
                ldir

; -- Save original u to `u_orig` (for step-4 recheck) -------------------
                ld      hl, encode_logical_imm_u
                ld      de, encode_logical_imm_u_orig
                ld      bc, 8
                ldir

; -- !is64 fixup: zero u[4..7], then copy u[0..3] to u[4..7] ------------
                ld      a, (encode_logical_imm_is64)
                or      a
                jr      nz, encode_logical_imm_no_widen
                xor     a
                ld      (encode_logical_imm_u+4), a
                ld      (encode_logical_imm_u+5), a
                ld      (encode_logical_imm_u+6), a
                ld      (encode_logical_imm_u+7), a
                ld      hl, encode_logical_imm_u
                ld      de, encode_logical_imm_u+4
                ld      bc, 4
                ldir
                xor     a
                ld      (encode_logical_imm_u_orig+4), a
                ld      (encode_logical_imm_u_orig+5), a
                ld      (encode_logical_imm_u_orig+6), a
                ld      (encode_logical_imm_u_orig+7), a
                ld      hl, encode_logical_imm_u_orig
                ld      de, encode_logical_imm_u_orig+4
                ld      bc, 4
                ldir
encode_logical_imm_no_widen:

; -- Reject u == 0 ------------------------------------------------------
                ld      hl, encode_logical_imm_u
                ld      b, 8
                xor     a
encode_logical_imm_zero_test:
                or      (hl)
                inc     hl
                djnz    encode_logical_imm_zero_test
                or      a
                jp      z, encode_logical_imm_reject

; -- Reject u == 0xFFFFFFFFFFFFFFFF -------------------------------------
                ld      hl, encode_logical_imm_u
                ld      b, 8
                ld      a, &ff
encode_logical_imm_ones_test:
                and     (hl)
                inc     hl
                djnz    encode_logical_imm_ones_test
                cp      &ff
                jp      z, encode_logical_imm_reject

; -- Find smallest replicating element size -----------------------------
                ld      hl, encode_logical_imm_u
                ld      de, encode_logical_imm_u+4
                ld      b, 4
                call    encode_logical_imm_cmp_bytes
                jr      nz, encode_logical_imm_size_is_64

                ld      hl, encode_logical_imm_u
                ld      de, encode_logical_imm_u+2
                ld      b, 2
                call    encode_logical_imm_cmp_bytes
                jr      nz, encode_logical_imm_size_is_32

                ld      a, (encode_logical_imm_u)
                ld      hl, encode_logical_imm_u+1
                cp      (hl)
                jr      nz, encode_logical_imm_size_is_16

                ld      a, (encode_logical_imm_u)
                ld      b, a
                and     &0f
                ld      c, a
                ld      a, b
                rrca
                rrca
                rrca
                rrca
                and     &0f
                cp      c
                jr      nz, encode_logical_imm_size_is_8

                ld      a, (encode_logical_imm_u)
                ld      b, a
                and     &03
                ld      c, a
                ld      a, b
                rrca
                rrca
                and     &03
                cp      c
                jr      nz, encode_logical_imm_size_is_4

                ld      a, 2
                jr      encode_logical_imm_size_done

encode_logical_imm_size_is_64:
                ld      a, 64
                jr      encode_logical_imm_size_done
encode_logical_imm_size_is_32:
                ld      a, 32
                jr      encode_logical_imm_size_done
encode_logical_imm_size_is_16:
                ld      a, 16
                jr      encode_logical_imm_size_done
encode_logical_imm_size_is_8:
                ld      a, 8
                jr      encode_logical_imm_size_done
encode_logical_imm_size_is_4:
                ld      a, 4

encode_logical_imm_size_done:
                ld      (encode_logical_imm_size), a

; -- Extract element (low `size` bits of u) -----------------------------
                ld      hl, encode_logical_imm_u
                ld      de, encode_logical_imm_element
                ld      bc, 8
                ldir
                ld      a, (encode_logical_imm_size)
                cp      64
                jr      z, encode_logical_imm_elem_done
                cp      32
                jr      nz, encode_logical_imm_elem_not32
                xor     a
                ld      (encode_logical_imm_element+4), a
                ld      (encode_logical_imm_element+5), a
                ld      (encode_logical_imm_element+6), a
                ld      (encode_logical_imm_element+7), a
                jr      encode_logical_imm_elem_done
encode_logical_imm_elem_not32:
                cp      16
                jr      nz, encode_logical_imm_elem_not16
                xor     a
                ld      hl, encode_logical_imm_element+2
                ld      b, 6
encode_logical_imm_elem16_clear:
                ld      (hl), a
                inc     hl
                djnz    encode_logical_imm_elem16_clear
                jr      encode_logical_imm_elem_done
encode_logical_imm_elem_not16:
                cp      8
                jr      nz, encode_logical_imm_elem_not8
                xor     a
                ld      hl, encode_logical_imm_element+1
                ld      b, 7
encode_logical_imm_elem8_clear:
                ld      (hl), a
                inc     hl
                djnz    encode_logical_imm_elem8_clear
                jr      encode_logical_imm_elem_done
encode_logical_imm_elem_not8:
                cp      4
                jr      nz, encode_logical_imm_elem_not4
                ld      a, (encode_logical_imm_element)
                and     &0f
                ld      (encode_logical_imm_element), a
                xor     a
                ld      hl, encode_logical_imm_element+1
                ld      b, 7
encode_logical_imm_elem4_clear:
                ld      (hl), a
                inc     hl
                djnz    encode_logical_imm_elem4_clear
                jr      encode_logical_imm_elem_done
encode_logical_imm_elem_not4:
                ld      a, (encode_logical_imm_element)
                and     &03
                ld      (encode_logical_imm_element), a
                xor     a
                ld      hl, encode_logical_imm_element+1
                ld      b, 7
encode_logical_imm_elem2_clear:
                ld      (hl), a
                inc     hl
                djnz    encode_logical_imm_elem2_clear
encode_logical_imm_elem_done:

; -- Step 4: replication recheck (size < 64) ----------------------------
                ld      a, (encode_logical_imm_size)
                cp      64
                jp      z, encode_logical_imm_replicate_ok

                cp      32
                jr      nz, encode_logical_imm_repl_not32
                ld      hl, encode_logical_imm_element
                ld      de, encode_logical_imm_u_check
                ld      bc, 4
                ldir
                ld      hl, encode_logical_imm_element
                ld      de, encode_logical_imm_u_check+4
                ld      bc, 4
                ldir
                jp      encode_logical_imm_replicate_check
encode_logical_imm_repl_not32:
                cp      16
                jr      nz, encode_logical_imm_repl_not16
                ld      hl, encode_logical_imm_element
                ld      de, encode_logical_imm_u_check
                ld      bc, 2
                ldir
                ld      hl, encode_logical_imm_element
                ld      de, encode_logical_imm_u_check+2
                ld      bc, 2
                ldir
                ld      hl, encode_logical_imm_element
                ld      de, encode_logical_imm_u_check+4
                ld      bc, 2
                ldir
                ld      hl, encode_logical_imm_element
                ld      de, encode_logical_imm_u_check+6
                ld      bc, 2
                ldir
                jp      encode_logical_imm_replicate_check
encode_logical_imm_repl_not16:
                cp      8
                jr      nz, encode_logical_imm_repl_not8
                ld      a, (encode_logical_imm_element)
                ld      (encode_logical_imm_u_check+0), a
                ld      (encode_logical_imm_u_check+1), a
                ld      (encode_logical_imm_u_check+2), a
                ld      (encode_logical_imm_u_check+3), a
                ld      (encode_logical_imm_u_check+4), a
                ld      (encode_logical_imm_u_check+5), a
                ld      (encode_logical_imm_u_check+6), a
                ld      (encode_logical_imm_u_check+7), a
                jp      encode_logical_imm_replicate_check
encode_logical_imm_repl_not8:
                cp      4
                jr      nz, encode_logical_imm_repl_not4
; tile 4-bit element to 8 bytes: byte = (e<<4)|e.
                ld      a, (encode_logical_imm_element)
                and     &0f
                ld      c, a
                rlca
                rlca
                rlca
                rlca
                or      c
                ld      (encode_logical_imm_u_check+0), a
                ld      (encode_logical_imm_u_check+1), a
                ld      (encode_logical_imm_u_check+2), a
                ld      (encode_logical_imm_u_check+3), a
                ld      (encode_logical_imm_u_check+4), a
                ld      (encode_logical_imm_u_check+5), a
                ld      (encode_logical_imm_u_check+6), a
                ld      (encode_logical_imm_u_check+7), a
                jp      encode_logical_imm_replicate_check
encode_logical_imm_repl_not4:
; size==2: byte = e | (e<<2) | (e<<4) | (e<<6)
                ld      a, (encode_logical_imm_element)
                and     &03
                ld      d, a               ; D = e
                sla     a
                sla     a                  ; A = e<<2
                or      d                  ; A = e | (e<<2) (4 bits)
                ld      d, a
                sla     a
                sla     a
                sla     a
                sla     a                  ; A = (e | (e<<2)) << 4
                or      d                  ; A = e | (e<<2) | (e<<4) | (e<<6)
                ld      (encode_logical_imm_u_check+0), a
                ld      (encode_logical_imm_u_check+1), a
                ld      (encode_logical_imm_u_check+2), a
                ld      (encode_logical_imm_u_check+3), a
                ld      (encode_logical_imm_u_check+4), a
                ld      (encode_logical_imm_u_check+5), a
                ld      (encode_logical_imm_u_check+6), a
                ld      (encode_logical_imm_u_check+7), a

encode_logical_imm_replicate_check:
                ld      hl, encode_logical_imm_u_check
                ld      de, encode_logical_imm_u_orig
                ld      b, 8
                call    encode_logical_imm_cmp_bytes
                jp      nz, encode_logical_imm_reject

encode_logical_imm_replicate_ok:

; -- Step 5: popcount element ------------------------------------------
                xor     a
                ld      (encode_logical_imm_ones), a
                ld      hl, encode_logical_imm_element
                ld      b, 8
encode_logical_imm_popcount_bytes:
                push    bc
                ld      c, (hl)
                ld      b, 8
encode_logical_imm_popcount_bits:
                srl     c
                jr      nc, encode_logical_imm_popcount_skip
                ld      a, (encode_logical_imm_ones)
                inc     a
                ld      (encode_logical_imm_ones), a
encode_logical_imm_popcount_skip:
                djnz    encode_logical_imm_popcount_bits
                pop     bc
                inc     hl
                djnz    encode_logical_imm_popcount_bytes

                ld      a, (encode_logical_imm_ones)
                or      a
                jp      z, encode_logical_imm_reject
                ld      hl, encode_logical_imm_size
                cp      (hl)
                jp      z, encode_logical_imm_reject

; -- Step 6: find rotation ---------------------------------------------
                call    encode_logical_imm_build_expected

                ld      hl, encode_logical_imm_element
                ld      de, encode_logical_imm_rotated
                ld      bc, 8
                ldir

                xor     a
                ld      (encode_logical_imm_rotation), a
encode_logical_imm_rotate_loop:
                ld      hl, encode_logical_imm_rotated
                ld      de, encode_logical_imm_expected
                ld      b, 8
                call    encode_logical_imm_cmp_bytes
                jr      z, encode_logical_imm_rotate_found
                ld      a, (encode_logical_imm_rotation)
                inc     a
                ld      (encode_logical_imm_rotation), a
                ld      hl, encode_logical_imm_size
                cp      (hl)
                jp      z, encode_logical_imm_reject
                call    encode_logical_imm_ror1
                jr      encode_logical_imm_rotate_loop
encode_logical_imm_rotate_found:

; -- Step 7: immr = (rotation == 0) ? 0 : size - rotation ---------------
                ld      a, (encode_logical_imm_rotation)
                or      a
                jr      z, encode_logical_imm_immr_zero
                ld      hl, encode_logical_imm_size
                ld      a, (hl)
                ld      hl, encode_logical_imm_rotation
                sub     (hl)
                ld      (encode_logical_imm_immr), a
                jr      encode_logical_imm_immr_done
encode_logical_imm_immr_zero:
                xor     a
                ld      (encode_logical_imm_immr), a
encode_logical_imm_immr_done:

; -- Step 8: compute N and imms ----------------------------------------
                ld      a, (encode_logical_imm_size)
                cp      64
                jr      nz, encode_logical_imm_imms_lt64
                ld      a, 1
                ld      (encode_logical_imm_n), a
                ld      a, (encode_logical_imm_ones)
                dec     a
                ld      (encode_logical_imm_imms), a
                jp      encode_logical_imm_pack
encode_logical_imm_imms_lt64:
                xor     a
                ld      (encode_logical_imm_n), a
                ld      a, (encode_logical_imm_size)
                ld      b, 0
encode_logical_imm_log2_loop:
                cp      2
                jr      c, encode_logical_imm_log2_done
                srl     a
                inc     b
                jr      encode_logical_imm_log2_loop
encode_logical_imm_log2_done:
                inc     b                  ; B = sizeLog2 + 1
                ld      a, 1
encode_logical_imm_nimms_shl:
                add     a, a
                dec     b
                jr      nz, encode_logical_imm_nimms_shl
                dec     a                  ; A = (1 << (sizeLog2+1)) - 1
                cpl                        ; A = ~A
                and     &3f                ; A = nimmsTop
                ld      c, a
                ld      a, (encode_logical_imm_ones)
                dec     a
                or      c
                ld      (encode_logical_imm_imms), a

encode_logical_imm_pack:
; -- Step 9: build combined = (N<<12) | (immr<<6) | imms ---------------
;   L bits 0..5  = imms
;   L bits 6..7  = immr bits 0..1
;   H bits 0..3  = immr bits 2..5
;   H bit  4     = N
                ld      a, (encode_logical_imm_imms)
                and     &3f
                ld      c, a               ; C = imms (in bits 0..5)
                ld      a, (encode_logical_imm_immr)
                and     &3f
                ld      b, a               ; B = immr

                ld      a, b
                and     3
                rrca
                rrca                       ; A bits 6..7 = (immr & 3)
                or      c
                ld      l, a               ; L done

                ld      a, b
                rrca
                rrca                       ; A bits 0..3 = immr >> 2 (bits 2..5 of immr)
                and     &0f
                ld      c, a
                ld      a, (encode_logical_imm_n)
                and     1
                rlca
                rlca
                rlca
                rlca                       ; A = N << 4
                or      c
                ld      h, a               ; H done

; -- Step 10: DEHL = combined << bit_position --------------------------
                ld      d, 0
                ld      e, 0
                ld      a, (encode_logical_imm_bp)
                ld      b, a
                inc     b
                dec     b
                ret     z
encode_logical_imm_final_shl:
                add     hl, hl
                rl      e
                rl      d
                djnz    encode_logical_imm_final_shl
                ret


; -----------------------------------------------------------------------
; Helper: encode_logical_imm_cmp_bytes
;
; Compare B bytes at HL with B bytes at DE.  On return: Z=match,
; NZ=mismatch.  Clobbers A, B, HL, DE.
; -----------------------------------------------------------------------
encode_logical_imm_cmp_bytes:
                ld      a, (de)
                cp      (hl)
                ret     nz
                inc     hl
                inc     de
                djnz    encode_logical_imm_cmp_bytes
                ret


; -----------------------------------------------------------------------
; Helper: encode_logical_imm_build_expected
;
; Build 8-byte `expected` = (1 << ones) - 1.  ones is read from
; encode_logical_imm_ones.  Strategy: zero all 8 bytes, then set bits
; 0..ones-1 one at a time.
;
; Clobbers A, BC, DE, HL.
; -----------------------------------------------------------------------
encode_logical_imm_build_expected:
                ld      hl, encode_logical_imm_expected
                ld      de, encode_logical_imm_expected+1
                ld      bc, 7
                ld      (hl), 0
                ldir

                ld      a, (encode_logical_imm_ones)
                or      a
                ret     z
                ld      b, a
                ld      hl, encode_logical_imm_expected
                ld      c, &01
encode_logical_imm_expect_set:
                ld      a, (hl)
                or      c
                ld      (hl), a
                sla     c
                jr      nz, encode_logical_imm_expect_no_carry
                ld      c, &01
                inc     hl
encode_logical_imm_expect_no_carry:
                djnz    encode_logical_imm_expect_set
                ret


; -----------------------------------------------------------------------
; Helper: encode_logical_imm_ror1
;
; Single-bit ROR of `rotated` within `size` bits.  size ∈ {2, 4, 8,
; 16, 32, 64}.  Bytes above size/8 in the buffer are already zero.
;
;   1. Save old bit 0 (LSB of rotated[0]) as new_top.
;   2. LSR the 8-byte rotated value: iterate from byte 7 down to byte
;      0, clearing CY before the first srl, then rr each subsequent
;      byte.  CY flows hi-byte-bit-0 → next-byte-bit-7.
;   3. OR new_top into bit (size - 1) of rotated.
;
; Clobbers A, BC, DE, HL.
; -----------------------------------------------------------------------
encode_logical_imm_ror1:
                ld      a, (encode_logical_imm_rotated)
                and     &01
                ld      c, a               ; C = new_top

                or      a                  ; CY = 0
                ld      hl, encode_logical_imm_rotated+7
                ld      a, (hl)
                srl     a
                ld      (hl), a
                ld      b, 7
encode_logical_imm_ror1_loop:
                dec     hl
                ld      a, (hl)
                rra
                ld      (hl), a
                djnz    encode_logical_imm_ror1_loop

                ld      a, c
                or      a
                ret     z

; Build mask = 1 << ((size-1) & 7); store at offset (size-1) >> 3.
                ld      a, (encode_logical_imm_size)
                dec     a                  ; A = size - 1
                ld      b, a               ; B = size - 1 (preserved)
                and     7                  ; A = bit_in_byte
                ld      c, a               ; C = shift count
                ld      a, 1
                inc     c
                dec     c
                jr      z, encode_logical_imm_ror1_mask_done
encode_logical_imm_ror1_mask_shl:
                add     a, a
                dec     c
                jr      nz, encode_logical_imm_ror1_mask_shl
encode_logical_imm_ror1_mask_done:
                ld      c, a               ; C = mask byte

                ld      a, b               ; A = size - 1
                rrca
                rrca
                rrca
                and     &1f                ; A = (size-1) >> 3 (0..7)
                ld      e, a
                ld      d, 0
                ld      hl, encode_logical_imm_rotated
                add     hl, de
                ld      a, (hl)
                or      c
                ld      (hl), a
                ret


; -----------------------------------------------------------------------
; Scratch storage.
; -----------------------------------------------------------------------
encode_logical_imm_bp:
                defb    0
encode_logical_imm_is64:
                defb    0
encode_logical_imm_size:
                defb    0
encode_logical_imm_ones:
                defb    0
encode_logical_imm_rotation:
                defb    0
encode_logical_imm_n:
                defb    0
encode_logical_imm_immr:
                defb    0
encode_logical_imm_imms:
                defb    0

encode_logical_imm_u:
                defb    0, 0, 0, 0, 0, 0, 0, 0
encode_logical_imm_u_orig:
                defb    0, 0, 0, 0, 0, 0, 0, 0
encode_logical_imm_u_check:
                defb    0, 0, 0, 0, 0, 0, 0, 0
encode_logical_imm_element:
                defb    0, 0, 0, 0, 0, 0, 0, 0
encode_logical_imm_expected:
                defb    0, 0, 0, 0, 0, 0, 0, 0
encode_logical_imm_rotated:
                defb    0, 0, 0, 0, 0, 0, 0, 0


; -----------------------------------------------------------------------
; encode_logical_imm_reject — shared reject target.
;
; All reject sites in encode_logical_imm jump here (replacing their former
; `jp fail`).  The default behaviour is unchanged: `jp fail` aborts
; assembly.  But the MOV-bitmask alias path (intercepts.asm
; encode_logical_imm_try) needs a recoverable "not encodable" signal so it
; can fall through to the next alias form.  When that caller has armed the
; soft flag, this handler instead records the rejection and RETs — landing
; back in encode_logical_imm_try, which maps it to CY=1.
;
; Every reject site is at a stack-balanced point (only the encoder's own
; return address is on the stack), so the RET returns to the correct
; caller.  Mirrors the Go side, where tryEncodeMovImm simply observes
; encodeLogicalImm returning an error and moves on (refenc/pass2.go:492).
; -----------------------------------------------------------------------
encode_logical_imm_reject:
                ld      a, (encode_logical_imm_soft)
                or      a
                jp      z, fail             ; hard fail (normal and/orr-imm path)
                ld      a, 1
                ld      (encode_logical_imm_rejected), a
                ret

; Soft-fail flags live in section-D RAM (&F000 free region) to avoid
; spending code-image bytes (the test variant is tight against &C000).
encode_logical_imm_soft:         equ     &F019
encode_logical_imm_rejected:     equ     &F01A
