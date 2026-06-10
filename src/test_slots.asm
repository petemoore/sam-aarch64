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

; -- Seed PASS_PC = 0 ---------------------------------------------------
; M4 made encode_branch_imm and encode_adrp_imm subtract PASS_PC from
; the input value (caller now passes an absolute target address, not a
; raw byteOffset).  At cold boot PASS_PC (&C159 .. &C15C) contains
; whatever the SAM RAM/SAMDOS load left there, which is non-zero.  The
; pre-M4 branch / adrp test vectors below are still written in "raw
; byteOffset" style, so we zero PASS_PC here to recover the old
; semantics for this suite.  PC-aware tests live in test_pc_rel.asm.
                call    pass_pc_reset

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

; -- encode_adrp_imm(AdrpImm{BP=0,BW=21}, +4096) => 0x20000000 --------
; pageOffset = +1 → immlo=1, immhi=0 → 1<<29 = 0x20000000.
; Wide signed-operand convention: HL=slot, BCDE=byteOffset BE.
                ld      hl, slot_adrp_bp0_bw21
                ld      bc, &0000
                ld      de, &1000
                call    encode_adrp_imm
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &00, &20  ; 0x20000000 LE

; -- encode_adrp_imm(AdrpImm{BP=0,BW=21}, +12288) => 0x60000000 --------
; pageOffset = +3 → immlo=3, immhi=0 → 3<<29 = 0x60000000.
                ld      hl, slot_adrp_bp0_bw21
                ld      bc, &0000
                ld      de, &3000
                call    encode_adrp_imm
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &00, &60  ; 0x60000000 LE

; -- encode_adrp_imm(AdrpImm{BP=0,BW=21}, +20480) => 0x20000020 -------
; pageOffset = +5 = 0b101 → immlo=1, immhi=1 → (1<<29)|(1<<5).
                ld      hl, slot_adrp_bp0_bw21
                ld      bc, &0000
                ld      de, &5000
                call    encode_adrp_imm
                call    assert_eq32_de_hl_imm
                defb    &20, &00, &00, &20  ; 0x20000020 LE

; -- encode_adrp_imm(AdrpImm{BP=0,BW=21}, target=0xFFFFF000) => 0x607FFFE0
; The wide operand is the ABSOLUTE TARGET (not a pre-signed byteOffset),
; and the page-difference is computed in full 64-bit / 33-bit space with
; the target's high word == PC's high word == ORIGIN_HIGH (here 0).  So
; target 0xFFFFF000 with PASS_PC 0 gives diff 0xFFFFF000 (POSITIVE in
; 33-bit space, bit 32 clear) → pageOffset 0xFFFFF (1048575, < 2^20, in
; range) → imm21 = 0xFFFFF → immlo=3, immhi=0x3FFFF →
; (3<<29) | (0x3FFFF<<5) = 0x60000000 | 0x007FFFE0 = 0x607FFFE0.
; Verified against refenc: `adrp x0, 0xFFFFF000` (origin 0) → payload
; 0x607FFFE0 (f07fffe0 with the x0/opcode bits).  The prior expectation
; 0x60FFFFE0 assumed a 32-bit-signed interpretation of the operand
; (treating 0xFFFFF000 as -4096), which diverged from GNU/refenc for
; targets >= 2^31 under a zero origin; the full-width computation (M6
; Class-5 fix) corrects it.
                ld      hl, slot_adrp_bp0_bw21
                ld      bc, &ffff
                ld      de, &f000
                call    encode_adrp_imm
                call    assert_eq32_de_hl_imm
                defb    &e0, &ff, &7f, &60  ; 0x607FFFE0 LE

; -- encode_logical_imm(LogicalImm{BP=10,BW=13}, 0xff, is64=true) ------
;    => N=1, immr=0, imms=7 → imm13 = 0x1007 → << 10 = 0x00401C00.
; Mirrors slots_logical_test.go::TestEncodeLogicalImm_AgainstGNUAs[0]
; (`and x0, x0, #0xff` → 0x92401C00).
; 64-bit single-operand convention (see slots/logical_imm.asm header):
;   HL = slot pointer, DE = ptr to 8-byte LE buffer, C = is64 flag.
                ld      hl, slot_logimm_bp10_bw13
                ld      de, logimm_input_ff_64
                ld      c, 1                ; is64 = true
                call    encode_logical_imm
                call    assert_eq32_de_hl_imm
                defb    &00, &1c, &40, &00  ; 0x00401C00 LE

; -- encode_logical_imm(LogicalImm{BP=10,BW=13}, 0xffff, is64=true) ----
;    => N=1, immr=0, imms=15 → imm13 = 0x100F → << 10 = 0x00403C00.
; Mirrors slots_logical_test.go::TestEncodeLogicalImm_AgainstGNUAs[1].
                ld      hl, slot_logimm_bp10_bw13
                ld      de, logimm_input_ffff_64
                ld      c, 1
                call    encode_logical_imm
                call    assert_eq32_de_hl_imm
                defb    &00, &3c, &40, &00  ; 0x00403C00 LE

; -- encode_logical_imm(LogicalImm{BP=10,BW=13},
;                       0x00ff00ff00ff00ff, is64=true)
;    => 16-bit replicating pattern, 8 ones per element.
;    N=0, immr=0, imms=0x27 → imm13 = 0x27 → << 10 = 0x00009C00.
; Mirrors slots_logical_test.go::TestEncodeLogicalImm_AgainstGNUAs[2].
                ld      hl, slot_logimm_bp10_bw13
                ld      de, logimm_input_00ff_x4
                ld      c, 1
                call    encode_logical_imm
                call    assert_eq32_de_hl_imm
                defb    &00, &9c, &00, &00  ; 0x00009C00 LE

; -- encode_logical_imm(LogicalImm{BP=10,BW=13},
;                       0x000f000f, is64=false)
;    => 32-bit input widened to 64-bit = 0x000f000f000f000f.
;    16-bit replicating pattern with 4 ones.
;    N=0, immr=0, sizeLog2=4 → nimmsTop=0x20, imms=0x23
;    → imm13 = 0x23 → << 10 = 0x00008C00.
; Mirrors slots_logical_test.go::TestEncodeLogicalImm_32bit (pins exact
; value computed by the spec; the Go test only checks non-zero).
                ld      hl, slot_logimm_bp10_bw13
                ld      de, logimm_input_000f000f_32
                ld      c, 0                ; is64 = false
                call    encode_logical_imm
                call    assert_eq32_de_hl_imm
                defb    &00, &8c, &00, &00  ; 0x00008C00 LE

; BitfieldImm Layer-1 unit tests (encode_bitfield_bfi / encode_bitfield_ubfx)
; were removed 2026-05-29 (PR-3c) along with the now-dead
; slots/bitfield_imm.asm.  The live bfi/ubfx path is the mnemonic-id
; intercept (intercepts.asm::encode_bitfield_word); it stays covered
; end-to-end by tests/m6/sources/inst_bitfield.s, which byte-matches GNU.

; All assertions passed.
                ret


; assert_eq32_de_hl_imm was extracted to src/test_assert_eq32.asm in
; the M6 budget-relief PR (2026-05-29) so that test_slots.asm can be
; relocated off-axis (page 12) while the shared helper stays resident in
; the main binary for both inline and off-axis callers.  See that file's
; header for the resolution mechanism.


; -----------------------------------------------------------------------
; Static slot-record literals for the self-tests.
;
; Layout (per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m2-encoder-tables-design.md §2):
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
;   AdrpImm      = 0x23
;   LogicalImm   = 0x24
;   BitfieldImm  = 0x25
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
slot_adrp_bp0_bw21:     defb    &23, 0,  0, 21
slot_logimm_bp10_bw13:  defb    &24, 0, 10, 13

; -- LogicalImm input buffers (8-byte LE) -------------------------------
; The encoder takes a pointer to an 8-byte LE buffer (DE on entry).
; For 32-bit inputs the high 4 bytes are ignored (the encoder masks
; and self-replicates internally per the Go reference).
logimm_input_ff_64:         defb &ff, &00, &00, &00, &00, &00, &00, &00
logimm_input_ffff_64:       defb &ff, &ff, &00, &00, &00, &00, &00, &00
logimm_input_00ff_x4:       defb &ff, &00, &ff, &00, &ff, &00, &ff, &00
logimm_input_000f000f_32:   defb &0f, &00, &0f, &00, &00, &00, &00, &00
