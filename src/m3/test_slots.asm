; test_slots.asm — boot-time self-tests for the per-slot encoders.
;
; These run BEFORE load_enctab (see assembler.asm start:) so they exercise
; the encoder against inline literal slot records and never touch the disk.
; Any assertion failure does `jp fail` — the same red-border-halt path used
; by load_enctab.  On all-pass the entry point RETs to the caller.
;
; Test vectors mirror the SUCCESS cases in
; tools/aarch64enc/slots_trivial_test.go::TestEncodeXreg /
; ::TestEncodeXregOrSpAcceptsThirtyOne / ::TestEncodeImm5 /
; ::TestEncodeImm6 / ::TestEncodeCondCode and
; tools/aarch64enc/slots_imm_test.go::TestEncodeShiftAmount.
; Error cases (reg=32, imm=overflow, cond=overflow, etc.) are
; intentionally skipped: there is no way to test the `jp fail` path
; without halting the program.  (See plan §"Self-test framework".)


; -----------------------------------------------------------------------
; run_slot_self_tests — entry point called from assembler.asm start:.
;
; Input:  none
; Output: returns to caller on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_slot_self_tests:

; -- encode_reg(Xreg{BP=0,BW=5}, 5)  =>  0x00000005 --------------------
                ld      hl, slot_xreg_bp0_bw5
                ld      a, 5
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    5, 0, 0, 0     ; little-endian 0x00000005

; -- encode_reg(Xreg{BP=0,BW=5}, 30) =>  0x0000001e --------------------
                ld      hl, slot_xreg_bp0_bw5
                ld      a, 30
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    30, 0, 0, 0

; -- encode_reg(Xreg{BP=0,BW=5}, 31) =>  0x0000001f --------------------
                ld      hl, slot_xreg_bp0_bw5
                ld      a, 31
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    31, 0, 0, 0

; -- encode_reg(Xreg{BP=5,BW=5}, 5)  =>  0x000000a0 (5 << 5 = 160) -----
                ld      hl, slot_xreg_bp5_bw5
                ld      a, 5
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    160, 0, 0, 0

; -- encode_reg(XregOrSp{BP=0,BW=5}, 31) =>  0x0000001f ----------------
; Confirms the encoder treats XregOrSp the same as Xreg at encode time
; (slots_trivial.go lines 6-8 / slots_trivial_test.go:34-40).
                ld      hl, slot_xregorsp_bp0_bw5
                ld      a, 31
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    31, 0, 0, 0

; -- encode_imm_n(Imm5{BP=10,BW=5}, 17) => 0x00004400 (17 << 10) -------
; Mirrors slots_trivial_test.go::TestEncodeImm5.
                ld      hl, slot_imm5_bp10_bw5
                ld      a, 17
                call    encode_imm_n
                call    assert_eq32_de_hl_imm
                defb    &00, &44, &00, &00  ; 17<<10 = 0x00004400

; -- encode_imm_n(Imm6{BP=16,BW=6}, 63) => 0x003f0000 (63 << 16) -------
; Mirrors slots_trivial_test.go::TestEncodeImm6.
                ld      hl, slot_imm6_bp16_bw6
                ld      a, 63
                call    encode_imm_n
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &3f, &00  ; 63<<16 = 0x003f0000

; -- encode_cond(CondCode{BP=0,BW=4}, 0xB) => 0x0000000b ----------------
; LT condition code; mirrors slots_trivial_test.go::TestEncodeCondCode.
; Exercises the encode_cond entry point (which jp-tails into
; encode_imm_n) — the value 0xB < (1<<4)=16, so range-check passes.
                ld      hl, slot_cond_bp0_bw4
                ld      a, &0b
                call    encode_cond
                call    assert_eq32_de_hl_imm
                defb    &0b, &00, &00, &00

; -- encode_imm_n(ShiftAmount{BP=10,BW=6}, 4) => 0x00001000 (4 << 10) --
; Mirrors slots_imm_test.go::TestEncodeShiftAmount.  ShiftAmount is
; just a thin wrapper around encodeImmN on the Go side (slots_imm.go
; lines 40-42); on the Z80 side the dispatcher will route SlotKind
; 0x12 straight to encode_imm_n.
                ld      hl, slot_shamt_bp10_bw6
                ld      a, 4
                call    encode_imm_n
                call    assert_eq32_de_hl_imm
                defb    &00, &10, &00, &00  ; 4<<10 = 0x00001000

; -- encode_imm12_shifted(Imm12Shifted{BP=10,BW=12}, 0x00000ABC) -------
;    => 0xABC << 10 = 0x002AF000
; Mirrors slots_imm_test.go::TestEncodeImm12Shifted_NoShift.
; Wide-operand calling convention (see slots/imm12_shifted.asm header):
;   HL = slot pointer, BCDE = big-endian 32-bit value.  For 0x00000ABC
;   that means BC=&0000, DE=&0ABC.
                ld      hl, slot_imm12_bp10_bw12
                ld      bc, &0000
                ld      de, &0abc
                call    encode_imm12_shifted
                call    assert_eq32_de_hl_imm
                defb    &00, &f0, &2a, &00  ; 0x002AF000 LE

; -- encode_imm12_shifted(Imm12Shifted{BP=10,BW=12}, 0x00001000) -------
;    => (1<<10) | (1<<22) = 0x00400400
; Mirrors slots_imm_test.go::TestEncodeImm12Shifted_LSL12.
; v=0x1000 → BC=&0000, DE=&1000.  Case B path (sh=1, imm12=1).
                ld      hl, slot_imm12_bp10_bw12
                ld      bc, &0000
                ld      de, &1000
                call    encode_imm12_shifted
                call    assert_eq32_de_hl_imm
                defb    &00, &04, &40, &00  ; 0x00400400 LE

; -- encode_imm16_shifted(Imm16Shifted{BP=5,BW=16}, imm=0x42, hw=0) ----
;    => 0x42 << 5 = 0x00000840
; Mirrors slots_imm_test.go::TestEncodeImm16Shifted (first sub-case).
; Two-operand wide convention (see slots/imm16_shifted.asm header):
;   HL = slot pointer, DE = imm16, A = hw selector.
                ld      hl, slot_imm16_bp5_bw16
                ld      de, &0042
                ld      a, 0
                call    encode_imm16_shifted
                call    assert_eq32_de_hl_imm
                defb    &40, &08, &00, &00  ; 0x00000840 LE

; -- encode_imm16_shifted(Imm16Shifted{BP=5,BW=16}, imm=0x42, hw=2) ----
;    => (0x42 << 5) | (2 << 21) = 0x00000840 | 0x00400000 = 0x00400840
; Mirrors slots_imm_test.go::TestEncodeImm16Shifted (second sub-case).
                ld      hl, slot_imm16_bp5_bw16
                ld      de, &0042
                ld      a, 2
                call    encode_imm16_shifted
                call    assert_eq32_de_hl_imm
                defb    &40, &08, &40, &00  ; 0x00400840 LE

; -- encode_extend_op(ExtendOp{BP=10,BW=6}, ext=UXTW(2), shift=0) ------
;    => 2 << (10+3) = 2 << 13 = 0x00004000
; Mirrors slots_imm_test.go::TestEncodeExtendOp (first sub-case).
; Two-operand small-value convention (see slots/extend_op.asm header):
;   HL = slot pointer, A = ext, C = shift.
                ld      hl, slot_extop_bp10_bw6
                ld      a, 2               ; ext = UXTW
                ld      c, 0               ; shift = 0
                call    encode_extend_op
                call    assert_eq32_de_hl_imm
                defb    &00, &40, &00, &00  ; 0x00004000 LE

; -- encode_extend_op(ExtendOp{BP=10,BW=6}, ext=SXTW(6), shift=2) ------
;    => 6 << 13 | 2 << 10 = 0xC000 | 0x0800 = 0x0000C800
; Mirrors slots_imm_test.go::TestEncodeExtendOp (second sub-case).
                ld      hl, slot_extop_bp10_bw6
                ld      a, 6               ; ext = SXTW
                ld      c, 2               ; shift = 2
                call    encode_extend_op
                call    assert_eq32_de_hl_imm
                defb    &00, &c8, &00, &00  ; 0x0000C800 LE

; -- encode_branch_imm(BranchImm26{BP=0,BW=26}, 0) => 0x00000000 -------
; Mirrors slots_branch_test.go::TestEncodeBranchImm26 (first sub-case).
; Wide signed-operand convention (see slots/branch_imm.asm header):
;   HL = slot pointer, BCDE = signed 32-bit byteOffset big-endian.
;   byteOffset = 0 → BC=&0000, DE=&0000.
                ld      hl, slot_branch26_bp0_bw26
                ld      bc, &0000
                ld      de, &0000
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &00, &00  ; 0x00000000 LE

; -- encode_branch_imm(BranchImm26{BP=0,BW=26}, +4) => 0x00000001 ------
; byteOffset = +4 → instrOffset = +1 → bits = 1.
                ld      hl, slot_branch26_bp0_bw26
                ld      bc, &0000
                ld      de, &0004
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &01, &00, &00, &00  ; 0x00000001 LE

; -- encode_branch_imm(BranchImm26{BP=0,BW=26}, -4) => 0x03FFFFFF ------
; byteOffset = -4 → instrOffset = -1 → masked to 26 bits = 0x03FFFFFF.
                ld      hl, slot_branch26_bp0_bw26
                ld      bc, &ffff
                ld      de, &fffc
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &ff, &ff, &ff, &03  ; 0x03FFFFFF LE

; -- encode_branch_imm(BranchImm26{BP=0,BW=26}, +0x07FFFFFC=max) -------
;    => instrOffset = 0x01FFFFFF = (1<<25)-1
; byteOffset = 134217724 = 0x07FFFFFC → BC=&07ff, DE=&fffc.
                ld      hl, slot_branch26_bp0_bw26
                ld      bc, &07ff
                ld      de, &fffc
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &ff, &ff, &ff, &01  ; 0x01FFFFFF LE

; -- encode_branch_imm(BranchImm19{BP=5,BW=19}, +8) => 0x40 ------------
; instrOffset = 2 → 2 << 5 = 0x40.
                ld      hl, slot_branch19_bp5_bw19
                ld      bc, &0000
                ld      de, &0008
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &40, &00, &00, &00  ; 0x00000040 LE

; -- encode_branch_imm(BranchImm19{BP=5,BW=19}, -4) => 0x00FFFFE0 -----
; instrOffset = -1 → masked to 19 bits = 0x7FFFF, << 5 = 0x00FFFFE0.
                ld      hl, slot_branch19_bp5_bw19
                ld      bc, &ffff
                ld      de, &fffc
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &e0, &ff, &ff, &00  ; 0x00FFFFE0 LE

; -- encode_branch_imm(BranchImm14{BP=5,BW=14}, +4) => 0x20 ------------
; instrOffset = +1 → 1 << 5 = 0x20.
                ld      hl, slot_branch14_bp5_bw14
                ld      bc, &0000
                ld      de, &0004
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &20, &00, &00, &00  ; 0x00000020 LE

; All assertions passed.
                ret


; -----------------------------------------------------------------------
; assert_eq32_de_hl_imm — assert DEHL equals a 32-bit literal that
; immediately follows the call in the caller's code stream.
;
; Caller pattern:
;     call assert_eq32_de_hl_imm
;     defb b0, b1, b2, b3        ; little-endian: b0 = bit 0..7
;
; The return address on the stack points at the first defb byte.  This
; routine compares DEHL against the four bytes (HL low first, then DE
; high), advances the return address past them on match, and RETs.
; On mismatch: jp fail.
;
; Layout reminder: DEHL where HL is bits 0..15 and DE is bits 16..31,
; so the in-memory little-endian byte order is L, H, E, D.
;
; Rationale: inline-literal style reads cleanly at the call site and is
; consistent with the pyz80 convention used for SAMDOS hooks (`rst 8`
; followed by `defb <hook>`).  See src/sam_io.inc lines 87-101.
;
; Clobbers: A, BC.  Preserves DE and HL so the caller's "actual" result
; remains intact (useful when chaining diagnostics).
; -----------------------------------------------------------------------
assert_eq32_de_hl_imm:
                pop     bc             ; BC = pointer to inline literal

                ld      a, (bc)        ; byte 0: low byte of HL
                cp      l
                jp      nz, fail
                inc     bc

                ld      a, (bc)        ; byte 1: high byte of HL
                cp      h
                jp      nz, fail
                inc     bc

                ld      a, (bc)        ; byte 2: low byte of DE
                cp      e
                jp      nz, fail
                inc     bc

                ld      a, (bc)        ; byte 3: high byte of DE
                cp      d
                jp      nz, fail
                inc     bc             ; BC now points just past the literal

                push    bc             ; restore as return address
                ret


; -----------------------------------------------------------------------
; Static slot-record literals for the self-tests.
;
; Layout (per docs/specs/2026-05-24-m2-encoder-tables-design.md §2):
;   defb slot_kind, expected_kind, bit_position, bit_width
;
; slot_kind values (per tools/aarch64enc/types.go lines 16-22, 24-25,
; and 26):
;   Xreg         = 0x01
;   Wreg         = 0x02
;   XregOrSp     = 0x03
;   WregOrSp     = 0x04
;   Imm5         = 0x05
;   Imm6         = 0x06
;   CondCode     = 0x07
;   Imm12Shifted = 0x10
;   Imm16Shifted = 0x11
;   ShiftAmount  = 0x12
;   ExtendOp     = 0x13
;   BranchImm26  = 0x20
;   BranchImm19  = 0x21
;   BranchImm14  = 0x22
;
; expected_kind is set to 0 here: the encoders do not consult it
; (text2bin uses it earlier in the pipeline).
; -----------------------------------------------------------------------
slot_xreg_bp0_bw5:      defb    &01, 0, 0, 5
slot_xreg_bp5_bw5:      defb    &01, 0, 5, 5
slot_xregorsp_bp0_bw5:  defb    &03, 0, 0, 5
slot_imm5_bp10_bw5:     defb    &05, 0, 10, 5
slot_imm6_bp16_bw6:     defb    &06, 0, 16, 6
slot_cond_bp0_bw4:      defb    &07, 0, 0, 4
slot_shamt_bp10_bw6:    defb    &12, 0, 10, 6
slot_imm12_bp10_bw12:   defb    &10, 0, 10, 12
slot_imm16_bp5_bw16:    defb    &11, 0, 5, 16
slot_extop_bp10_bw6:    defb    &13, 0, 10, 6
slot_branch26_bp0_bw26: defb    &20, 0,  0, 26
slot_branch19_bp5_bw19: defb    &21, 0,  5, 19
slot_branch14_bp5_bw14: defb    &22, 0,  5, 14
