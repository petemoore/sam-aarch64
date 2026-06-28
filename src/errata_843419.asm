; errata_843419.asm — Cortex-A53 erratum 843419 ADRP→ADR post-layout fix (i27c).
;
; Authority: tools/sam-aarch64/assemble/errata.go.  Every predicate here
; mirrors the Go code verbatim; the errata.go line number of the ported
; function is cited in each comment below.
;
; Background: on the Cortex-A53 an ADRP whose final virtual address sits in
; the last two instruction slots of a 4 KiB page (PC & 0xfff == 0xff8 or
; 0xffc), followed by a ld/st, then an unsigned-immediate ld/st that uses the
; ADRP's Rd as its base, can produce a wrong address.  Fix: rewrite the ADRP
; to an ADR in place (errata.go:193 rewriteADRPtoADR).  Our images are at
; most ~32 KB so always within ADR's ±1 MiB reach.
;
; Toggle: FIX_843419_ENABLED mirrors FIX_835769_ENABLED.  Default 0 (off).
; Setting it to 1 enables the post-layout scan for A53/Pi3 targeting.
;
; Integration: errata_843419_scan is called from main_assemble (main_loop.asm)
; after pass 2 completes (walk_records + litpool_flush) and before
; enctab_map_out.  The scan walks the finalised OUT buffer in-place, so no
; re-layout is needed.
;
; State variables — placed in the free region &D3D6..&D4FF (following the
; 835769 state which ends at &D3D5).
;   &D3D6  FIX_843419_ENABLED  1 byte: 0=off (default), 1=on
;   &D3D7  E843_SCAN_PAGE      1 byte: 0-based page index in the OUT run
;   &D3D8  E843_SCAN_PTR       2 bytes: section-B ptr (&4000..&7FFF)
;   &D3DA  E843_BYTES_LEFT     3 bytes: bytes remaining in scan (u24 LE)
;   &D3DD  E843_SCAN_PC        2 bytes: low 16 bits of current insn virtual PC
;   &D3DF  E843_INSN_BUF       16 bytes: 4-word lookahead scratch

FIX_843419_ENABLED:   equ     &D3D6
E843_SCAN_PAGE:       equ     &D3D7
E843_SCAN_PTR:        equ     &D3D8
E843_BYTES_LEFT:      equ     &D3DA
E843_SCAN_PC:         equ     &D3DD
E843_INSN_BUF:        equ     &D3DF


; -----------------------------------------------------------------------
; errata_843419_scan — post-layout scan.
;
; Implements errata.go:156-188 (fixErratum843419).  Called from
; main_assemble after pass-2 litpool_flush, before enctab_map_out.
;
; The OUT buffer is walked one 4-byte instruction at a time.  For each word
; that passes the ADRP predicate and the page-offset gate, the lookahead
; words are checked for the 3-instruction (Case A) or 4-instruction (Case B)
; sequence.  A matching ADRP is rewritten to ADR in place.
;
; Input:  OUT buffer populated; OUT_LEN = bytes emitted.
;         PASS_PC = link origin low-16 + OUT_LEN (set by pass-2 emit path).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
errata_843419_scan:
; Toggle gate (errata.go:67-80 opts.Fix843419 check).
                ld      a, (FIX_843419_ENABLED)
                or      a
                ret     z

; Compute the virtual PC of the first byte of the OUT buffer.
; After pass 2: PASS_PC = (link origin low 16) + OUT_LEN.
; start_pc_low16 = PASS_PC_low16 - OUT_LEN_low16.
                ld      hl, (PASS_PC)
                ld      de, (OUT_LEN)
                or      a                       ; clear carry
                sbc     hl, de
                ld      (E843_SCAN_PC), hl

; Initialise the scan cursor to the first byte of the OUT run.
                xor     a
                ld      (E843_SCAN_PAGE), a
                ld      hl, &4000
                ld      (E843_SCAN_PTR), hl

; E843_BYTES_LEFT := OUT_LEN (u24 LE).
                ld      a, (OUT_LEN + 0)
                ld      (E843_BYTES_LEFT + 0), a
                ld      a, (OUT_LEN + 1)
                ld      (E843_BYTES_LEFT + 1), a
                ld      a, (OUT_LEN + 2)
                ld      (E843_BYTES_LEFT + 2), a


e843_loop:
; Check bytes_left >= 4.  u24 >= small-N: byte2 or byte1 nonzero → yes;
; else compare byte0.
                ld      a, (E843_BYTES_LEFT + 2)
                or      a
                jr      nz, e843_have4
                ld      a, (E843_BYTES_LEFT + 1)
                or      a
                jr      nz, e843_have4
                ld      a, (E843_BYTES_LEFT + 0)
                cp      4
                ret     c               ; < 4 bytes left: scan complete

e843_have4:
; Read current word into E843_INSN_BUF+0..3.
                call    e843_read4_cur

; ADRP check (errata.go:371 aarch64ADRP):
;   (insn & 0x9f000000) == 0x90000000  →  byte3 & 0x9f == 0x90.
                ld      a, (E843_INSN_BUF + 3)
                and     &9f
                cp      &90
                jr      nz, e843_next

; Page-offset gate (errata.go:164-166 _bfd_aarch64_erratum_843419_p):
;   (pc & 0xfff) == 0xff8 or 0xffc.
; scan_pc[15:8] = H; scan_pc[7:0] = L.
; Condition: (H & 0x0f) == 0x0f  AND  (L & ~0x04) == 0xf8.
;   L == 0xf8 → offset 0xff8; L == 0xfc → offset 0xffc.
;   Both satisfy (L & 0xfb) == 0xf8.
                ld      hl, (E843_SCAN_PC)
                ld      a, h
                and     &0f
                cp      &0f
                jr      nz, e843_next
                ld      a, l
                and     &fb
                cp      &f8
                jr      nz, e843_next

; ADRP at a vulnerable page offset.
; --- Case A (errata.go:169-175): need bytes_left >= 12. ---
                ld      a, (E843_BYTES_LEFT + 2)
                or      a
                jr      nz, e843_caseA
                ld      a, (E843_BYTES_LEFT + 1)
                or      a
                jr      nz, e843_caseA
                ld      a, (E843_BYTES_LEFT + 0)
                cp      12
                jr      c, e843_next    ; fewer than 12 bytes: skip both cases

e843_caseA:
; Read insn2 (+4) and insn3 (+8).
                call    e843_read4_plus4
                call    e843_read4_plus8

; e843_seq_p checks insn1=BUF+0, insn2=BUF+4, insn3=BUF+8.
                call    e843_seq_p
                jr      z, e843_caseB   ; not Case A; try Case B

; Case A match.
                call    e843_rewrite
                jr      e843_next

e843_caseB:
; --- Case B (errata.go:177-185): ADRP, ld/st, any, unsigned-imm-ld/st. ---
; Need bytes_left >= 16.
                ld      a, (E843_BYTES_LEFT + 2)
                or      a
                jr      nz, e843_caseB_check
                ld      a, (E843_BYTES_LEFT + 1)
                or      a
                jr      nz, e843_caseB_check
                ld      a, (E843_BYTES_LEFT + 0)
                cp      16
                jr      c, e843_next    ; fewer than 16 bytes: skip Case B

e843_caseB_check:
; Read insn4 (+12).
                call    e843_read4_plus12

; For Case B the Go code calls sequence_p(insn1, insn2, insn4).
; seq_p reads insn3 from BUF+8; copy BUF+12 → BUF+8.
                ld      hl, E843_INSN_BUF + 12
                ld      de, E843_INSN_BUF + 8
                ld      bc, 4
                ldir
                call    e843_seq_p
                jr      z, e843_next    ; Case B also fails

; Case B match.
                call    e843_rewrite

e843_next:
; Advance cursor and scan PC by 4; decrement bytes_left by 4.
                call    e843_advance4
                ld      hl, (E843_SCAN_PC)
                ld      de, 4
                add     hl, de
                ld      (E843_SCAN_PC), hl
                jp      e843_loop


; -----------------------------------------------------------------------
; e843_seq_p — port of errata.go:377-385 (aarch64Erratum843419SequenceP).
;
; Tests whether the triple (BUF+0=insn1, BUF+4=insn2, BUF+8=insn3) forms
; the erratum-843419 sequence:
;   insn2 is a ld/st (not a load-pair)
;   insn3 is an unsigned-imm ld/st with Rn == ADRP Rd
;
; Output: NZ = true (sequence matches), Z = false.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
e843_seq_p:
; aarch64MemOp(insn2): copy BUF+4 to ERRATA_INSN1 so errata_mem_op_classify
; can read it (it always reads from ERRATA_INSN1).
                ld      hl, E843_INSN_BUF + 4
                ld      de, ERRATA_INSN1
                ld      bc, 4
                ldir
                call    errata_mem_op_classify  ; A=ok, B=pair, C=load

                or      a               ; ok == 0 → not a memory op
                ret     z               ; Z = false

; Reject load-pair: !(pair && load) (errata.go:382).
                ld      a, b            ; pair flag
                or      a
                jr      z, e843_seq_ok_pair
                ld      a, c            ; load flag
                or      a
                jr      z, e843_seq_ok_pair
; pair AND load: reject.
                xor     a
                ret                     ; Z = false

e843_seq_ok_pair:
; aarch64LDSTUIMM(insn3): byte3 & 0x3b == 0x39 (errata.go:245, 383).
                ld      a, (E843_INSN_BUF + 8 + 3)
                and     &3b
                cp      &39
                jr      nz, e843_seq_false

; aarch64RN(insn3) == aarch64RD(insn1) (errata.go:384).
; RN = insn bits[9:5]:
;   bits[2:0] of RN = insn byte0 bits[7:5]
;   bits[4:3] of RN = insn byte1 bits[1:0]
                ld      a, (E843_INSN_BUF + 8 + 0)
                rlca
                rlca
                rlca                    ; byte0[7:5] rotated to bits[2:0]
                and     &07
                ld      l, a
                ld      a, (E843_INSN_BUF + 8 + 1)
                and     &03             ; byte1[1:0]
                rlca
                rlca
                rlca                    ; → bits[4:3]
                or      l               ; A = Rn(insn3)

; RD(insn1) = bits[4:0] = byte0 & 0x1f (errata.go:222).
                ld      l, a            ; L = Rn(insn3)
                ld      a, (E843_INSN_BUF + 0)
                and     &1f             ; A = Rd(insn1)
                cp      l
                jr      nz, e843_seq_false

; True.  Force NZ (A = 0 if Rd=x0, but must still signal NZ).
                ld      a, 1
                or      a
                ret

e843_seq_false:
                xor     a
                ret


; -----------------------------------------------------------------------
; e843_rewrite — rewrite the ADRP at the current cursor position to ADR.
;
; Port of errata.go:193-206 (rewriteADRPtoADR).
;
; Computation:
;   imm21 = aarch64DecodeADRPImm(insn)          (errata.go:399)
;   adr_imm = SignExtend(imm21 << 12, 33) - (place & 0xfff)
;   new_word = aarch64ReencodeADRImm(0x10000000, adr_imm) | Rd(insn)
;                                                 (errata.go:203-204)
;
; Byte-level breakdown (instruction bytes b0=LSB, b3=MSB):
;   imm21[1:0]  = b3[6:5]  (immlo)
;   imm21[4:2]  = b0[7:5]  (immhi[2:0])
;   imm21[12:5] = b1[7:0]  (immhi[10:3])
;   imm21[20:13]= b2[7:0]  (bit 20 = sign; immhi[18:11])
;
; ADR output word bytes (errata.go:404-407 aarch64ReencodeADRImm):
;   out_b0 = Rd[4:0] | (r[4:2] << 5)          bits[7:0]
;   out_b1 = (r[7:5] >> 5) | (r[12:8] << 3)   bits[15:8]
;   out_b2 = (r[15:13] >> 5) | (r[20:16] << 3) bits[23:16]  (approximate)
;   out_b3 = 0x10 | (r[1:0] << 5)              bits[31:24]
; where r = adr_imm = e843_r_b0..b3.
; -----------------------------------------------------------------------
e843_rewrite:
; Cache raw instruction bytes.
                ld      a, (E843_INSN_BUF + 0)
                ld      (e843_wr_b0), a
                ld      a, (E843_INSN_BUF + 1)
                ld      (e843_wr_b1), a
                ld      a, (E843_INSN_BUF + 2)
                ld      (e843_wr_b2), a
                ld      a, (E843_INSN_BUF + 3)
                ld      (e843_wr_b3), a

; --- Step 1: Decode imm21 (errata.go:399). ---
;
; v_b0 = imm21[7:0]:
;   bits[1:0] = b3[6:5] (immlo)
;   bits[4:2] = b0[7:5] (immhi[2:0])
;   bits[7:5] = b1[2:0] (immhi[5:3])
                ld      a, (e843_wr_b3)
                rlca
                rlca
                rlca                    ; b3[6:5] → bits[1:0]
                and     &03
                ld      l, a

                ld      a, (e843_wr_b0)
                and     &e0
                rrca
                rrca
                rrca                    ; b0[7:5] → bits[4:2]
                and     &1c
                or      l
                ld      l, a

                ld      a, (e843_wr_b1)
                and     &07
                rlca
                rlca
                rlca
                rlca
                rlca                    ; b1[2:0] → bits[7:5]
                or      l
                ld      (e843_v_b0), a

; v_b1 = imm21[15:8]:
;   bits[4:0] = b1[7:3] (immhi[10:6])
;   bits[7:5] = b2[2:0] (immhi[13:11])
                ld      a, (e843_wr_b1)
                rrca
                rrca
                rrca                    ; b1[7:3] → bits[4:0]
                and     &1f
                ld      l, a

                ld      a, (e843_wr_b2)
                and     &07
                rlca
                rlca
                rlca
                rlca
                rlca                    ; b2[2:0] → bits[7:5]
                or      l
                ld      (e843_v_b1), a

; v_b2 = imm21[20:16]:
;   bits[4:0] = b2[7:3] (bit 4 = sign)
                ld      a, (e843_wr_b2)
                rrca
                rrca
                rrca
                and     &1f
                ld      (e843_v_b2), a

; --- Step 2: Sign-extend imm21 to 32 bits. ---
; Sign bit = v_b2 bit 4 (= imm21 bit 20).
                ld      a, (e843_v_b2)
                and     &10
                jr      z, e843_rw_pos

                ld      a, (e843_v_b2)
                or      &e0             ; extend sign into bits[7:5]
                ld      (e843_v_b2), a
                ld      a, &ff
                ld      (e843_v_b3), a
                jr      e843_rw_sext_done

e843_rw_pos:
                xor     a
                ld      (e843_v_b3), a

e843_rw_sext_done:

; --- Step 3: Shift left 12 (multiply by 4096 to get byte offset). ---
; r = v << 12.  r_b0 = 0.
; r_b1 = (v_b0 & 0x0f) << 4
; r_b2 = ((v_b1 & 0x0f) << 4) | (v_b0 >> 4)
; r_b3 = ((v_b2 & 0x0f) << 4) | (v_b1 >> 4)
                xor     a
                ld      (e843_r_b0), a

                ld      a, (e843_v_b0)
                ld      b, a            ; save v_b0
                and     &0f
                rlca
                rlca
                rlca
                rlca                    ; (v_b0 & 0x0f) << 4
                ld      (e843_r_b1), a

                ld      a, (e843_v_b1)
                ld      c, a            ; save v_b1
                and     &0f
                rlca
                rlca
                rlca
                rlca                    ; (v_b1 & 0x0f) << 4
                ld      l, a
                ld      a, b
                rrca
                rrca
                rrca
                rrca
                and     &0f             ; v_b0 >> 4
                or      l
                ld      (e843_r_b2), a

                ld      a, (e843_v_b2)
                and     &0f
                rlca
                rlca
                rlca
                rlca                    ; (v_b2 & 0x0f) << 4
                ld      l, a
                ld      a, c
                rrca
                rrca
                rrca
                rrca
                and     &0f             ; v_b1 >> 4
                or      l
                ld      (e843_r_b3), a

; --- Step 4: Subtract (place & 0xfff). ---
; place_low12: L = pc[7:0], H & 0x0f = pc[11:8].
                ld      hl, (E843_SCAN_PC)

                ld      a, (e843_r_b0)
                sub     l               ; r_b0 - pc[7:0]; carry = borrow
                ld      (e843_r_b0), a

                ld      a, h
                and     &0f
                ld      d, a            ; D = pc[11:8]
                ld      a, (e843_r_b1)
                sbc     a, d            ; r_b1 - pc[11:8] - borrow
                ld      (e843_r_b1), a

                ld      a, (e843_r_b2)
                sbc     a, 0
                ld      (e843_r_b2), a

                ld      a, (e843_r_b3)
                sbc     a, 0
                ld      (e843_r_b3), a

; --- Step 5: Range check — bits[31:20] must be all 0 or all 1. ---
                ld      a, (e843_r_b3)
                or      a
                jr      z, e843_rw_chk_pos
                cp      &ff
                jr      z, e843_rw_chk_neg
                jr      e843_rw_range_err

e843_rw_chk_pos:
                ld      a, (e843_r_b2)
                and     &f0
                jr      z, e843_rw_in_range
                jr      e843_rw_range_err

e843_rw_chk_neg:
                ld      a, (e843_r_b2)
                and     &f0
                cp      &f0
                jr      z, e843_rw_in_range

e843_rw_range_err:
; Our images are far too small for this to trigger.  Hard error.
                ld      a, &d7
                jp      fail_with_tag

e843_rw_in_range:
; --- Step 6: Build the ADR word (errata.go:404-407). ---
;
; Full 32-bit word layout after encoding:
;   bits[4:0]   = Rd               → out_b0[4:0]
;   bits[7:5]   = r[4:2]           → out_b0[7:5]
;   bits[15:8]  = {r_b1[4:0]<<3 | r_b0[7:5]}
;   bits[23:16] = {r_b2[4:0]<<3 | r_b1[7:5]}
;   bit[28]     = 1 (ADROp)        → out_b3 bit 4
;   bits[30:29] = r[1:0]           → out_b3[6:5]
;   bit[31]     = 0 (ADR, not ADRP)

; out_b0 = Rd | (r_b0[4:2] << 5)
                ld      a, (e843_r_b0)
                and     &1c             ; bits[4:2]
                rlca
                rlca
                rlca                    ; → bits[7:5]
                ld      l, a
                ld      a, (e843_wr_b0)
                and     &1f             ; Rd
                or      l
                ld      (e843_out_b0), a

; out_b1 = (r_b0[7:5] → bits[2:0]) | (r_b1[4:0] << 3)
                ld      a, (e843_r_b0)
                and     &e0
                rrca
                rrca
                rrca
                rrca
                rrca                    ; r_b0[7:5] → bits[2:0]
                ld      l, a
                ld      a, (e843_r_b1)
                and     &1f
                rlca
                rlca
                rlca                    ; r_b1[4:0] → bits[7:3]
                or      l
                ld      (e843_out_b1), a

; out_b2 = (r_b1[7:5] → bits[2:0]) | (r_b2[4:0] << 3)
                ld      a, (e843_r_b1)
                and     &e0
                rrca
                rrca
                rrca
                rrca
                rrca                    ; r_b1[7:5] → bits[2:0]
                ld      l, a
                ld      a, (e843_r_b2)
                and     &1f
                rlca
                rlca
                rlca                    ; r_b2[4:0] → bits[7:3]
                or      l
                ld      (e843_out_b2), a

; out_b3 = 0x10 | (r_b0[1:0] << 5)
                ld      a, (e843_r_b0)
                and     &03             ; imm[1:0]
                rlca
                rlca
                rlca
                rlca
                rlca                    ; → bits[6:5]
                or      &10             ; ADR opcode bit 28
                ld      (e843_out_b3), a

; --- Step 7: Write ADR word back to OUT buffer at current cursor. ---
                ld      a, (e843_out_b0)
                call    e843_poke_cur0
                ld      a, (e843_out_b1)
                call    e843_poke_cur1
                ld      a, (e843_out_b2)
                call    e843_poke_cur2
                ld      a, (e843_out_b3)
                call    e843_poke_cur3
                ret


; -----------------------------------------------------------------------
; OUT buffer read/write helpers.
;
; The OUT run occupies a contiguous set of physical pages mapped into
; section B (&4000-&7FFF) via LMPR.  These helpers run from section C
; (HMPR-stable) and bracket LMPR internally — safe even when called from
; the off-axis cluster context because the cluster uses LMPR only while
; executing from section A (physical page 12); the helpers here execute
; from section C and restore LMPR before returning.
;
; LMPR encoding (same as out_run_peek in test_assert_eq32.asm):
;   LMPR = (OUT_RUN_BASE + page_idx - 1) | &20
; This maps physical page (OUT_RUN_BASE + page_idx) into section B.
; -----------------------------------------------------------------------

; e843_read4_cur: read 4 bytes at current cursor into E843_INSN_BUF+0.
e843_read4_cur:
                ld      a, (E843_SCAN_PAGE)
                ld      b, a
                ld      hl, (E843_SCAN_PTR)
                ld      de, E843_INSN_BUF + 0
                jp      e843_read4_bhl_de

; e843_advance4: advance cursor by 4; decrement E843_BYTES_LEFT by 4.
e843_advance4:
                ld      hl, (E843_SCAN_PTR)
                ld      de, 4
                add     hl, de
                ld      a, h
                cp      &80
                jr      c, e843_adv4_ok
                ld      a, (E843_SCAN_PAGE)
                inc     a
                ld      (E843_SCAN_PAGE), a
                ld      de, &4000
                or      a
                sbc     hl, de
e843_adv4_ok:
                ld      (E843_SCAN_PTR), hl
                ld      hl, E843_BYTES_LEFT
                ld      a, (hl)
                sub     4
                ld      (hl), a
                ret     nc
                inc     hl
                dec     (hl)
                ret     nz
                inc     hl
                dec     (hl)
                ret

; e843_read4_plus4: read 4 bytes at cursor+4 into E843_INSN_BUF+4.
e843_read4_plus4:
                ld      a, (E843_SCAN_PAGE)
                ld      b, a
                ld      hl, (E843_SCAN_PTR)
                ld      de, 4
                add     hl, de
                call    e843_norm_bhl
                ld      de, E843_INSN_BUF + 4
                jp      e843_read4_bhl_de

; e843_read4_plus8: read 4 bytes at cursor+8 into E843_INSN_BUF+8.
e843_read4_plus8:
                ld      a, (E843_SCAN_PAGE)
                ld      b, a
                ld      hl, (E843_SCAN_PTR)
                ld      de, 8
                add     hl, de
                call    e843_norm_bhl
                ld      de, E843_INSN_BUF + 8
                jp      e843_read4_bhl_de

; e843_read4_plus12: read 4 bytes at cursor+12 into E843_INSN_BUF+12.
e843_read4_plus12:
                ld      a, (E843_SCAN_PAGE)
                ld      b, a
                ld      hl, (E843_SCAN_PTR)
                ld      de, 12
                add     hl, de
                call    e843_norm_bhl
                ld      de, E843_INSN_BUF + 12
                jp      e843_read4_bhl_de

; e843_norm_bhl: while HL >= &8000: B++, HL -= &4000.
e843_norm_bhl:
                ld      a, h
                cp      &80
                ret     c
                inc     b
                ld      de, &4000
                or      a
                sbc     hl, de
                jr      e843_norm_bhl

; e843_read4_bhl_de: read 4 bytes from (B=page_idx, HL=ptr) into memory at DE.
; Handles single page boundary crossing during the 4-byte window.
e843_read4_bhl_de:
                call    e843_read1_bhl
                ld      (de), a
                inc     hl
                inc     de
                ld      a, h
                cp      &80
                jr      c, e843_r4_b1
                inc     b
                ld      hl, &4000
e843_r4_b1:
                call    e843_read1_bhl
                ld      (de), a
                inc     hl
                inc     de
                ld      a, h
                cp      &80
                jr      c, e843_r4_b2
                inc     b
                ld      hl, &4000
e843_r4_b2:
                call    e843_read1_bhl
                ld      (de), a
                inc     hl
                inc     de
                ld      a, h
                cp      &80
                jr      c, e843_r4_b3
                inc     b
                ld      hl, &4000
e843_r4_b3:
                call    e843_read1_bhl
                ld      (de), a
                ret

; e843_read1_bhl: read 1 byte from (B=page_idx, HL=section-B addr) → A.
e843_read1_bhl:
                push    bc
                push    hl
                ld      c, b            ; C = page index
                ld      a, (OUT_RUN_BASE)
                add     a, c
                dec     a
                or      &20             ; LMPR: RAM0 | (page-1) → section B = page
                ld      c, a
                in      a, (250)
                ld      b, a            ; save caller LMPR
                ld      a, c
                out     (250), a
                ld      a, (hl)
                ld      c, a
                ld      a, b
                out     (250), a        ; restore LMPR
                ld      a, c
                pop     hl
                pop     bc
                ret

; Write helpers: A = byte to write.
e843_poke_cur0:
                ld      a, (E843_SCAN_PAGE)
                ld      b, a
                ld      hl, (E843_SCAN_PTR)
                jp      e843_write1_bhl

e843_poke_cur1:
                ld      a, (E843_SCAN_PAGE)
                ld      b, a
                ld      hl, (E843_SCAN_PTR)
                inc     hl
                call    e843_norm_bhl
                jp      e843_write1_bhl

e843_poke_cur2:
                ld      a, (E843_SCAN_PAGE)
                ld      b, a
                ld      hl, (E843_SCAN_PTR)
                inc     hl
                inc     hl
                call    e843_norm_bhl
                jp      e843_write1_bhl

e843_poke_cur3:
                ld      a, (E843_SCAN_PAGE)
                ld      b, a
                ld      hl, (E843_SCAN_PTR)
                inc     hl
                inc     hl
                inc     hl
                call    e843_norm_bhl
                jp      e843_write1_bhl

; e843_write1_bhl: write A to (B=page_idx, HL=section-B addr).
e843_write1_bhl:
                push    bc
                push    hl
                push    af
                ld      c, b
                ld      a, (OUT_RUN_BASE)
                add     a, c
                dec     a
                or      &20
                ld      c, a
                in      a, (250)
                ld      b, a
                ld      a, c
                out     (250), a
                pop     af
                ld      (hl), a
                ld      a, b
                out     (250), a
                pop     hl
                pop     bc
                ret


; -----------------------------------------------------------------------
; Scratch bytes (section C, statically allocated by the assembler).
; -----------------------------------------------------------------------

e843_wr_b0:     defb    0       ; raw insn byte 0
e843_wr_b1:     defb    0       ; raw insn byte 1
e843_wr_b2:     defb    0       ; raw insn byte 2
e843_wr_b3:     defb    0       ; raw insn byte 3
e843_v_b0:      defb    0       ; imm21 byte 0 (sign-extended to 32 bits)
e843_v_b1:      defb    0       ; imm21 byte 1
e843_v_b2:      defb    0       ; imm21 byte 2
e843_v_b3:      defb    0       ; imm21 byte 3
e843_r_b0:      defb    0       ; adr_imm byte 0 (imm21<<12 - place_low12)
e843_r_b1:      defb    0       ; adr_imm byte 1
e843_r_b2:      defb    0       ; adr_imm byte 2
e843_r_b3:      defb    0       ; adr_imm byte 3
e843_out_b0:    defb    0       ; ADR word byte 0
e843_out_b1:    defb    0       ; ADR word byte 1
e843_out_b2:    defb    0       ; ADR word byte 2
e843_out_b3:    defb    0       ; ADR word byte 3
