; test_erratum835769.asm — boot-time self-tests for Cortex-A53 erratum
; 835769 NOP-insertion predicates (i27b).
;
; Authority: tools/sam-aarch64/assemble/errata.go (errata.go:337-358).
; Instruction encodings sourced from errata_test.go (TestMlxlP,
; TestErratumSequence835769) — verified byte-for-byte below.
;
; Design: all tests manipulate the ERRATA_INSN1/INSN2 scratch words and
; ERRATA_PREV_* state directly and call errata_is_hazard (the predicate
; core) and errata_check_and_handle (the full gate including PC adjacency
; and toggle).  Pass-2 emit paths are NOT exercised here to avoid needing
; emit_byte infrastructure in the test context; pass-1 PC-advancement is
; verified instead.
;
; Failure tag byte scheme (all via fail_with_tag):
;   &e1: test 1 fail (toggle off, expected NO hazard for ldrX4→maddX0)
;   &e2: test 2a fail (toggle on, expected hazard for ldrX4→maddX0)
;   &e3: test 2b fail (PASS_PC not advanced by 4 for NOP in pass 1)
;   &e4: test 3 fail (RAW carve-out: ldrX4→maddReadsX4 should NOT hazard)
;   &e5: test 4 fail (non-MAC insn2 should NOT hazard)
;   &e6: test 5 fail (strX4→maddX0: store should hazard)
;
; Encoding derivations:
;   ldrX4 = 0xf94000a4: ldr x4,[x5] — byte3=0xf9, byte2=0x40, byte1=0x00,
;     byte0=0xa4.  RT=byte0&0x1f=4; opc=(byte2 rlca×2)&3=1 → load.
;   maddX0 = 0x9b020c20: madd x0,x1,x2,x3 (Rd=x0) — byte3=0x9b (MAC),
;     op31=(byte2 rlca×3)&7=0 ∈ {0,1,5}; Ra=(byte1 rrca×2)&0x1f=3≠0x1f.
;   maddReadsX4 = 0x9b021020: madd x0,x1,x2,x4 (Ra=x4) — Ra=(byte1 rrca×2)
;     &0x1f=4=x4; ldrX4 loads x4 → RAW dep.
;   strX4 = 0xf90000a4: str x4,[x5] — byte2=0x00, opc=0 → load=false.
;   addX0 = 0x8b020020: add x0,x1,x2 — byte3=0x8b ≠ 0x9b → not MAC.


; -----------------------------------------------------------------------
; run_erratum835769_self_tests — entry point called from cluster_dispatch.
;
; Input:  none.
; Output: returns to caller on all-pass.  Any mismatch: jp fail_with_tag.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_erratum835769_self_tests:

; Save toggle + PASS_MODE (restored before return).
                ld      a, (FIX_835769_ENABLED)
                push    af
                ld      a, (PASS_MODE)
                push    af

; ========================================================================
; Test 1 — Toggle OFF: ldrX4 → maddX0 should NOT report a hazard.
; errata.go:338: first check is aarch64MlxlP(insn2) but the Gate 1
; (toggle check) in errata_check_and_handle suppresses the whole thing.
; We test errata_is_hazard directly (bypass Gate 1) then confirm toggle
; suppression separately via errata_check_and_handle.
; ========================================================================

; Set ERRATA_INSN1 = ldrX4 (LE: a4 00 40 f9)
                ld      hl, e835_ldr_x4
                ld      de, ERRATA_INSN1
                ld      bc, 4
                ldir

; Set ERRATA_INSN2 = maddX0 (LE: 20 0c 02 9b)
                ld      hl, e835_madd_x0
                ld      de, ERRATA_INSN2
                ld      bc, 4
                ldir

; errata_is_hazard should return 1 (predicate level — toggle not consulted)
; This also validates that ldrX4 IS recognised as a mem op and maddX0 IS
; recognised as mlxl_p.
                call    errata_is_hazard
                dec     a               ; A = 0 if was 1 (hazard detected)
                jr      z, e835_t1_pair_ok
                ld      a, &e1
                jp      fail_with_tag   ; predicates did not detect hazard
e835_t1_pair_ok:

; Now confirm that with toggle OFF, errata_check_and_handle skips the NOP.
                xor     a
                ld      (FIX_835769_ENABLED), a         ; toggle OFF

; Set up state for the gate check (pass 1 mode, adjacent PCs)
                ld      a, PASS_PASS1
                ld      (PASS_MODE), a
                call    set_pass_pc_imm
                defb    &04, &00, &00, &00              ; PASS_PC = 0x00000104

                ld      a, 1
                ld      (ERRATA_PREV_VALID), a
                ld      a, &04                          ; ERRATA_PREV_PC = 0x00000104
                ld      (ERRATA_PREV_PC + 0), a
                xor     a
                ld      (ERRATA_PREV_PC + 1), a
                ld      (ERRATA_PREV_PC + 2), a
                ld      (ERRATA_PREV_PC + 3), a

; errata_check_and_handle should do nothing (toggle off → return immediately)
                call    errata_check_and_handle

; PASS_PC must still be 0x00000104 (no NOP advance)
                call    e835_assert_pass_pc_imm
                defb    &04, &00, &00, &00
                ; mismatch → jp fail (tagged by e835_assert_pass_pc_imm)

; ========================================================================
; Test 2 — Toggle ON: ldrX4 → maddX0 hazard detected, NOP accounts for
;           4 bytes in pass 1 (PASS_PC advances from 0x104 to 0x108).
; ========================================================================

                ld      a, 1
                ld      (FIX_835769_ENABLED), a         ; toggle ON

; errata_is_hazard must confirm the hazard (already confirmed in test 1,
; but we re-check with toggle ON to exercise the full call path).
; ERRATA_INSN1=ldrX4, ERRATA_INSN2=maddX0 still set from above.
                call    errata_is_hazard
                dec     a
                jr      z, e835_t2_hazard_ok
                ld      a, &e2
                jp      fail_with_tag   ; test 2a: expected hazard
e835_t2_hazard_ok:

; Full gate: PASS_PC=0x104, ERRATA_PREV_PC=0x104, PASS_MODE=PASS_PASS1,
; ERRATA_PREV_VALID=1 (all set from test 1).
; errata_check_and_handle should advance PASS_PC by 4 (NOP, pass 1).
; ERRATA_INSN2=maddX0 still in place.
                call    errata_check_and_handle

; PASS_PC must now be 0x00000108 (0x104 + 4 NOP advance)
                call    e835_assert_pass_pc_imm
                defb    &08, &00, &00, &00              ; expected 0x108
                ; mismatch tag: &e3 (see e835_assert_pass_pc_imm)

; ========================================================================
; Test 3 — RAW carve-out: ldrX4 → maddReadsX4 (Ra=x4) must NOT hazard.
; errata.go:353: if load && (rt == Ra), pipeline serialises — safe.
; ========================================================================

; Set ERRATA_INSN2 = maddReadsX4 (LE: 20 10 02 9b; Ra=x4, matches ldrX4 rt)
                ld      hl, e835_madd_reads_x4
                ld      de, ERRATA_INSN2
                ld      bc, 4
                ldir

; ERRATA_INSN1=ldrX4 still set.
                call    errata_is_hazard
                or      a
                jr      z, e835_t3_ok
                ld      a, &e4
                jp      fail_with_tag   ; test 3: RAW dep should prevent hazard
e835_t3_ok:

; ========================================================================
; Test 4 — Non-MAC insn2: addX0 (byte3=0x8b ≠ 0x9b) → NOT a hazard.
; errata.go:338: aarch64MlxlP(insn2) fails immediately.
; ========================================================================

; Set ERRATA_INSN2 = addX0 (LE: 20 00 02 8b)
                ld      hl, e835_add_x0
                ld      de, ERRATA_INSN2
                ld      bc, 4
                ldir

                call    errata_is_hazard
                or      a
                jr      z, e835_t4_ok
                ld      a, &e5
                jp      fail_with_tag   ; test 4: non-MAC insn2 should not hazard
e835_t4_ok:

; ========================================================================
; Test 5 — Store insn1: strX4 → maddX0 IS a hazard (stores lack the RAW
;           carve-out — errata.go:353 carve-out only applies when load).
; ========================================================================

; Set ERRATA_INSN1 = strX4 (LE: a4 00 00 f9; opc=0 → load=false)
                ld      hl, e835_str_x4
                ld      de, ERRATA_INSN1
                ld      bc, 4
                ldir

; Set ERRATA_INSN2 = maddX0 (LE: 20 0c 02 9b)
                ld      hl, e835_madd_x0
                ld      de, ERRATA_INSN2
                ld      bc, 4
                ldir

                call    errata_is_hazard
                dec     a               ; A = 0 if hazard (was 1)
                jr      z, e835_t5_ok
                ld      a, &e6
                jp      fail_with_tag   ; test 5: store → maddX0 should be hazard
e835_t5_ok:

; ========================================================================
; Restore toggle, PASS_MODE, PASS_PC.
; ========================================================================
                pop     af
                ld      (PASS_MODE), a
                pop     af
                ld      (FIX_835769_ENABLED), a
                call    pass_pc_reset           ; erases test's PASS_PC + ERRATA_PREV_VALID

                ret


; -----------------------------------------------------------------------
; e835_assert_pass_pc_imm — compare PASS_PC against a 4-byte inline
; literal.  Caller pattern:
;     call e835_assert_pass_pc_imm
;     defb lo, b1, b2, hi          ; little-endian expected value
; On mismatch: jp fail (fail_at_bc records the literal address as the site
; and halts).  Clobbers: A, BC, HL.
; -----------------------------------------------------------------------
e835_assert_pass_pc_imm:
                pop     hl              ; HL = pointer to inline expected value
                ld      a, (PASS_PC + 0)
                cp      (hl)
                jr      nz, e835_appi_fail
                inc     hl
                ld      a, (PASS_PC + 1)
                cp      (hl)
                jr      nz, e835_appi_fail
                inc     hl
                ld      a, (PASS_PC + 2)
                cp      (hl)
                jr      nz, e835_appi_fail
                inc     hl
                ld      a, (PASS_PC + 3)
                cp      (hl)
                jr      nz, e835_appi_fail
                inc     hl
                push    hl              ; restore return address past the literal
                ret
e835_appi_fail:
                ld      a, &e3          ; tag: PASS_PC mismatch in NOP-advance test
                jp      fail_with_tag


; -----------------------------------------------------------------------
; Test vector data.
; Encodings verified against errata_test.go (TestErratumSequence835769).
; -----------------------------------------------------------------------
e835_ldr_x4:            defb    &a4, &00, &40, &f9  ; ldr x4,[x5]    0xf94000a4 LE
e835_madd_x0:           defb    &20, &0c, &02, &9b  ; madd x0,x1,x2,x3  0x9b020c20 LE
e835_madd_reads_x4:     defb    &20, &10, &02, &9b  ; madd x0,x1,x2,x4  0x9b021020 LE
e835_str_x4:            defb    &a4, &00, &00, &f9  ; str x4,[x5]    0xf90000a4 LE
e835_add_x0:            defb    &20, &00, &02, &8b  ; add x0,x1,x2   0x8b020020 LE
