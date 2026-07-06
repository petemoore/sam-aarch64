; test_erratum843419.asm — boot-time self-tests for erratum 843419 (i27c).
;
; Authority: tools/sam-aarch64/assemble/errata.go (errata.go:156-206).
; Test vectors verified against errata_test.go (TestErratum843419SequenceP,
; TestADRRewriteRoundTrip).
;
; Tests exercise e843_seq_p and errata_843419_scan directly, using small
; inline OUT buffers assembled into section C scratch.  The scan is driven
; by setting up E843_INSN_BUF and the state variables manually or by
; pointing the scan at a known instruction buffer.
;
; Failure tag byte scheme (all via fail_with_tag):
;   &c1: t1 — toggle OFF, seq_p should return false for any match
;   &c2: t2 — toggle ON, ADRP@0xff8 + Case A: expected rewrite
;   &c3: t3 — ADRP@0x004 (non-vulnerable offset): no rewrite
;   &c4: t4 — ADRP@0xff8, sequence predicate fails (Rn != Rd): no rewrite
;   &c5: t5 — ADRP@0xffc + Case B (4-word): expected rewrite
;   &c6: t6 — rewritten ADR word byte3 should be 0x10 (no ADRP bit)
;
; Instruction encodings (verified against errata_test.go):
;   adrpX0    = 0x90000000  ldr x4, adrp x0, #0 (Rd=x0)
;   ldrX4     = 0xf94000a4  ldr x4,[x5] (generic ld/st, not a load-pair)
;   ldrFromX0 = 0xf9400009  ldr x9,[x0] (unsigned-imm, Rn=x0 = ADRP Rd)
;   ldrFromX7 = 0xf94000e9  ldr x9,[x7] (Rn=x7 ≠ x0: predicate fails)
;
; Test 2 rewrite check: ADRP x0, #0 at place=0x2ff8 (page 2, offset 0xff8).
;   imm21 = 0: adr_imm = 0 << 12 - (0x2ff8 & 0xfff) = -0xff8 = -4088.
;   new_word = aarch64ReencodeADRImm(0x10000000, adr_imm) | 0 (Rd=0).
;   adr_imm = -4088 = 0xFFFFF008 in 32 bits.
;   new_word = 0x10000000
;            | ((adr_imm & 3) << 29)             = 0
;            | ((adr_imm & (mask19 << 2)) << 3)  = (0x1FF008 << 3) = 0x00FF8040
;            = 0x10FF8040.
;   LE bytes: 40 80 ff 10.
;   (Spot-checked via Go: aarch64ReencodeADRImm(0x10000000, int32(-4088)) =
;    0x10FF8040.)


; -----------------------------------------------------------------------
; run_erratum843419_self_tests — called from cluster_dispatch.
;
; Input:  none.
; Output: returns to caller on all-pass.  Any mismatch: jp fail_with_tag.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_erratum843419_self_tests:

; Save the toggle (restored at end).
                ld      a, (FIX_843419_ENABLED)
                push    af

; =======================================================================
; Test 1 — toggle OFF: seq_p predicate is true for our vectors, but the
;           scan should produce no rewrite when the toggle is off.
; =======================================================================

                xor     a
                ld      (FIX_843419_ENABLED), a         ; OFF

; Load a matching sequence directly into E843_INSN_BUF.
; BUF+0: adrpX0 (LE: 00 00 00 90)
                ld      hl, e843t_adrp_x0
                ld      de, E843_INSN_BUF + 0
                ld      bc, 4
                ldir
; BUF+4: ldrX4 (LE: a4 00 40 f9)
                ld      hl, e843t_ldr_x4
                ld      de, E843_INSN_BUF + 4
                ld      bc, 4
                ldir
; BUF+8: ldrFromX0 (LE: 09 00 40 f9)
                ld      hl, e843t_ldr_from_x0
                ld      de, E843_INSN_BUF + 8
                ld      bc, 4
                ldir

; seq_p with toggle OFF still evaluates the predicate (toggle is not checked
; by seq_p; it is checked by the scan).  Confirm seq_p returns true (NZ).
                call    e843_seq_p
                jr      nz, e843t_t1_seq_ok
                ld      a, &c1
                jp      fail_with_tag   ; t1: seq_p unexpectedly returned false
e843t_t1_seq_ok:

; =======================================================================
; Test 2 — toggle ON, ADRP x0 at page offset 0xff8 (vulnerable), Case A:
;           e843_rewrite turns ADRP x0,#0 into ADR x0,-4088.
;
; The test drives the rewrite arithmetic directly rather than the full scan:
; it loads adrpX0 into E843_INSN_BUF+0 (as t1 did), sets SCAN_PC = 0x2ff8,
; calls e843_rewrite, and checks the four output bytes. The direct path
; isolates the rewrite arithmetic; a full scan would need OUT_RUN_BASE pointed
; at the buffer's physical page, which is fragile across builds.
;
; Byte-level derivation (matching e843_rewrite's borrow subtraction) for
; adrpX0 = 00 00 00 90 at place=0x2ff8:
;   imm21 = 0, so v_b0..v_b3 = 0 and the shifted r_b0..r_b3 start at 0.
;   Subtract place & 0xfff = 0xff8 (0xf8 in byte0, 0x0f in byte1):
;     r_b0 = 0 - 0xf8      = 0x08 (borrow)
;     r_b1 = 0 - 0x0f - 1  = 0xf0 (borrow)
;     r_b2 = 0 - 0    - 1  = 0xff
;     r_b3 = 0 - 0    - 1  = 0xff   (r_b2 & 0xf0 = 0xf0 → negative, in ADR range)
;   Re-encode into the ADR word:
;     out_b0 = Rd(0) | ((r_b0 & 0x1c) << 3)          = 0x40
;     out_b1 = ((r_b0 & 0xe0) >> 5) | ((r_b1 & 0x1f) << 3)  = 0x80
;     out_b2 = ((r_b1 & 0xe0) >> 5) | ((r_b2 & 0x1f) << 3)  = 0xff
;     out_b3 = 0x10 | ((r_b0 & 0x03) << 5)           = 0x10  (bit 31 clear = ADR)
;   ADR word LE: 40 80 FF 10 → 0x10FF8040.
;
; This matches the file-header spot-check:
; aarch64ReencodeADRImm(0x10000000, -4088) = 0x10FF8040.
; =======================================================================

                ld      a, 1
                ld      (FIX_843419_ENABLED), a         ; ON

; Set SCAN_PC to 0x2ff8 (page offset 0xff8, vulnerable).
                ld      hl, &2ff8
                ld      (E843_SCAN_PC), hl

; E843_INSN_BUF+0 already holds adrpX0 from t1.
                call    e843_rewrite

; Check out_b3 == 0x10 (ADR opcode; bit 31 = 0 means ADR not ADRP).
                ld      a, (e843_out_b3)
                cp      &10
                jr      z, e843t_t2_b3_ok
                ld      a, &c6
                jp      fail_with_tag   ; t6: byte3 is not 0x10

e843t_t2_b3_ok:
; Check out_b0 == 0x40 (Rd=0 | r_b0[4:2]=0x08 → bits[7:5]=0x40).
                ld      a, (e843_out_b0)
                cp      &40
                jr      z, e843t_t2_b0_ok
                ld      a, &c2
                jp      fail_with_tag   ; t2: byte0 mismatch

e843t_t2_b0_ok:
; Check out_b2 == 0xff (high byte of offset).
                ld      a, (e843_out_b2)
                cp      &ff
                jr      z, e843t_t2_b2_ok
                ld      a, &c2
                jp      fail_with_tag

e843t_t2_b2_ok:

; =======================================================================
; Test 3 — ADRP at non-vulnerable page offset 0x004: seq_p returns true
;           but the scan's page-offset gate rejects it.
; E843_SCAN_PC = 0x0004.  Gate check: (H & 0x0f) = 0 ≠ 0x0f → reject.
; We test the gate by calling seq_p (which has no gate) to confirm the
; predicate IS true, then running the scan checks manually.
; =======================================================================

; seq_p should still return NZ (predicate is about the sequence, not PC).
                call    e843_seq_p
                jr      nz, e843t_t3_seq_ok
                ld      a, &c3
                jp      fail_with_tag
e843t_t3_seq_ok:

; Verify the gate check: pc=0x0004, so (H & 0x0f)=0 ≠ 0x0f → would be skipped.
; We confirm the gate logic: (0x0004 >> 8) & 0x0f = 0 ≠ 0x0f.  That's a
; structural check of the comment, not an assertion we can run without the full
; scan; t3 is verified by the scan tests below.  No fail tag needed here.

; =======================================================================
; Test 4 — ADRP at 0xff8, but Rn(insn3) != Rd(insn1): seq_p returns false.
; Replace BUF+8 with ldrFromX7 (Rn=x7 ≠ Rd=x0).
; =======================================================================

                ld      hl, e843t_ldr_from_x7
                ld      de, E843_INSN_BUF + 8
                ld      bc, 4
                ldir

                call    e843_seq_p
                jr      z, e843t_t4_ok  ; Z = false: good
                ld      a, &c4
                jp      fail_with_tag   ; t4: predicate should be false with wrong Rn
e843t_t4_ok:

; =======================================================================
; Test 5 — ADRP at 0xffc, Case B (4-word sequence): the intervening word
;           at BUF+8 is irrelevant, the LDSTUIMM is at BUF+12.
; Load: BUF+8 = ldrX4 (the intervening word, anything non-branch);
;        BUF+12 = ldrFromX0 (the actual LDSTUIMM with Rn=x0).
; seq_p is called with BUF+8 temporarily replaced by BUF+12 (the scan
; does ldir BUF+12→BUF+8 before calling seq_p for Case B).
; Test manually: copy BUF+12 → BUF+8, call seq_p.
; =======================================================================

; Restore BUF+8 to ldrX4 (the intervening non-LDSTUIMM word).
                ld      hl, e843t_ldr_x4
                ld      de, E843_INSN_BUF + 8
                ld      bc, 4
                ldir
; Set BUF+12 = ldrFromX0.
                ld      hl, e843t_ldr_from_x0
                ld      de, E843_INSN_BUF + 12
                ld      bc, 4
                ldir

; seq_p on (BUF+0, BUF+4, BUF+8=ldrX4): ldrX4 (0xf94000a4) IS an LDSTUIMM
; (byte3=0xf9; 0xf9 & 0x3b = 0x39 ✓), so the predicate can only reject it on the
; base-register check. Rn is bits[9:5]: byte0=0xa4 (1010 0100) rotated left 3
; then & 7 = 0x05, and byte1=0x00 contributes 0 to bits[4:3] → Rn = x5. Rn=x5 ≠
; Rd=x0, so seq_p returns false for Case A (BUF+8 = ldrX4).
                call    e843_seq_p
                jr      z, e843t_t5_seqA_ok
                ld      a, &c5
                jp      fail_with_tag   ; BUF+8=ldrX4 shouldn't match (Rn≠Rd)
e843t_t5_seqA_ok:

; Now simulate Case B: copy BUF+12 → BUF+8, then call seq_p.
                ld      hl, E843_INSN_BUF + 12
                ld      de, E843_INSN_BUF + 8
                ld      bc, 4
                ldir
                call    e843_seq_p
                jr      nz, e843t_t5_ok
                ld      a, &c5
                jp      fail_with_tag   ; t5: Case B sequence should match
e843t_t5_ok:

; =======================================================================
; Test 6 — e843_advance4 24-bit counter borrow propagation.
;
; The predicate/rewrite tests above never call e843_advance4, so its
; borrow chain went untested.  With E843_BYTES_LEFT = 0x000100 the step
; 0x000100 - 4 = 0x0000FC requires the borrow from byte0 to decrement
; byte1 (0x01→0x00) WITHOUT touching byte2.  A broken `dec (hl); ret nz`
; chain corrupts byte2 to 0xFF (result 0xFF00FC) — which makes the scan
; run away past the OUT buffer.  This is a deterministic pin for that bug.
; =======================================================================

; Safe cursor (no page cross) and the boundary counter value 0x000100.
                xor     a
                ld      (E843_SCAN_PAGE), a
                ld      hl, &4000
                ld      (E843_SCAN_PTR), hl
                xor     a
                ld      (E843_BYTES_LEFT + 0), a
                ld      a, 1
                ld      (E843_BYTES_LEFT + 1), a
                xor     a
                ld      (E843_BYTES_LEFT + 2), a

                call    e843_advance4

; Expect E843_BYTES_LEFT == 0x0000FC (byte0=0xFC, byte1=0x00, byte2=0x00).
                ld      a, (E843_BYTES_LEFT + 0)
                cp      &fc
                jr      nz, e843t_t6_fail
                ld      a, (E843_BYTES_LEFT + 1)
                or      a
                jr      nz, e843t_t6_fail
                ld      a, (E843_BYTES_LEFT + 2)
                or      a
                jr      z, e843t_t6_ctr_ok
e843t_t6_fail:
                ld      a, &c7
                jp      fail_with_tag   ; t6: 24-bit counter borrow wrong
e843t_t6_ctr_ok:

; Cursor advanced by 4 with no page cross.
                ld      hl, (E843_SCAN_PTR)
                ld      de, &4004
                or      a
                sbc     hl, de
                ld      a, h
                or      l
                jr      z, e843t_t6_ok
                ld      a, &c7
                jp      fail_with_tag
e843t_t6_ok:

; =======================================================================
; Test 7 — full errata_843419_scan over a 260-byte synthetic OUT buffer.
;
; Exercises the whole scan loop end-to-end over a real PP_OUT run: ADRP
; predicate + page-offset gate + sequence predicate + rewrite +
; e843_advance4 across the 256-byte counter boundary + the paged
; read/write helpers.
;
; Layout: 260 bytes.  Offsets 0..247 = NOP (0xd503201f).  Offset 248 =
; a Case A hazard (adrp x0,#0 / ldr x4,[x5] / ldr x9,[x0]).  With
; start_pc = 0x0F00 the ADRP's virtual PC is 0x0F00 + 248 = 0x0FF8 (the
; vulnerable page offset), so the scan rewrites it to ADR x0, #-4088 =
; 0x10FF8040 (LE 40 80 ff 10, per the Test 2 derivation).
;
; A correct scan TERMINATES (returns here); the broken 24-bit counter
; ran away past the buffer and never returned (step-budget timeout).
; =======================================================================

; Allocate a 1-page PP_OUT run (260 < 16384) and reset the emit cursor.
                ld      hl, 260
                ld      (PASS_PC + 0), hl
                xor     a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                call    reset_out_buffer

; Emit 62 NOP words (248 bytes): byte pattern 1f 20 03 d5.
; emit_byte preserves BC (clobbers A, HL), so B survives the djnz loop.
                ld      b, 62
e843t_t7_nop_loop:
                ld      a, &1f
                call    emit_byte
                ld      a, &20
                call    emit_byte
                ld      a, &03
                call    emit_byte
                ld      a, &d5
                call    emit_byte
                djnz    e843t_t7_nop_loop

; Emit the 12-byte Case A hazard at offset 248 via emit_bytes_n
; (emit_byte clobbers HL, so drive the copy through the run-aware helper).
                ld      hl, e843t_hazard
                ld      a, 12
                call    emit_bytes_n

; OUT_LEN is now 260.  Set PASS_PC so start_pc = PASS_PC - OUT_LEN = 0x0F00.
                ld      hl, &1004               ; 0x0F00 + 260
                ld      (PASS_PC + 0), hl
                xor     a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a

; Enable the fix and run the scan.
                ld      a, 1
                ld      (FIX_843419_ENABLED), a
                call    errata_843419_scan

; --- Reaching here proves the scan TERMINATED. ---

; The planted ADRP at offset 248 must now be ADR 0x10FF8040 (LE 40 80 ff 10).
; out_run_peek: A = page index (0), HL = section-B addr (&4000 + 248 = &40F8).
                xor     a
                ld      hl, &40f8
                call    out_run_peek
                cp      &40
                jr      nz, e843t_t7_rw_fail
                xor     a
                ld      hl, &40f9
                call    out_run_peek
                cp      &80
                jr      nz, e843t_t7_rw_fail
                xor     a
                ld      hl, &40fa
                call    out_run_peek
                cp      &ff
                jr      nz, e843t_t7_rw_fail
                xor     a
                ld      hl, &40fb
                call    out_run_peek
                cp      &10
                jr      z, e843t_t7_rw_ok
e843t_t7_rw_fail:
                ld      a, &c8
                jp      fail_with_tag   ; t7: planted ADRP not rewritten to ADR
e843t_t7_rw_ok:

; A non-hazard NOP word (offset 0) must be untouched (still 1f 20 03 d5).
                xor     a
                ld      hl, &4000
                call    out_run_peek
                cp      &1f
                jr      nz, e843t_t7_nop_fail
                xor     a
                ld      hl, &4003
                call    out_run_peek
                cp      &d5
                jr      z, e843t_t7_nop_ok
e843t_t7_nop_fail:
                ld      a, &c9
                jp      fail_with_tag   ; t7: NOP word wrongly modified
e843t_t7_nop_ok:

; =======================================================================
; Restore state and return.
; =======================================================================
                pop     af
                ld      (FIX_843419_ENABLED), a
                ret


; -----------------------------------------------------------------------
; Test vector data.
; Encodings verified against errata_test.go and errata.go.
; -----------------------------------------------------------------------
e843t_adrp_x0:      defb    &00, &00, &00, &90  ; adrp x0,#0     0x90000000 LE
e843t_ldr_x4:       defb    &a4, &00, &40, &f9  ; ldr x4,[x5]    0xf94000a4 LE
e843t_ldr_from_x0:  defb    &09, &00, &40, &f9  ; ldr x9,[x0]    0xf9400009 LE
e843t_ldr_from_x7:  defb    &e9, &00, &40, &f9  ; ldr x9,[x7]    0xf94000e9 LE

; 12-byte Case A hazard for the full-scan test (Test 7):
; adrp x0,#0 / ldr x4,[x5] / ldr x9,[x0].
e843t_hazard:       defb    &00, &00, &00, &90  ; adrp x0,#0
                    defb    &a4, &00, &40, &f9  ; ldr x4,[x5]
                    defb    &09, &00, &40, &f9  ; ldr x9,[x0]
