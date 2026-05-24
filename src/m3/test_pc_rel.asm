; test_pc_rel.asm — boot-time self-tests for the M4 PC-relative slot
; encoders.
;
; Per docs/plans/2026-05-24-m4-symbols-multipass.md Task 6.
; Per docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.5.
;
; M4 changed the calling convention for the BranchImm26 / BranchImm19 /
; BranchImm14 / AdrpImm slot encoders.  Pre-M4 callers passed a raw
; signed byteOffset (target - pc, already computed).  M4 callers pass
; the resolved ABSOLUTE target address; the encoder subtracts PASS_PC
; (BranchImm*) or (PASS_PC & ~0xFFF) (AdrpImm) internally before the
; existing range-check + bit-pack body.
;
; This file exercises the new subtraction step end-to-end:
;   1. Set PASS_PC to a known value.
;   2. Call the encoder with BCDE = absolute target address.
;   3. Assert DEHL = expected encoded bits.
;
; All assertions live in assert_eq32_de_hl_imm (defined in test_slots.asm).
; On any mismatch: jp fail (red border + printer-channel "FAIL" banner, ci-m3 reports fail immediately).
;
; Coverage:
;   1. BranchImm26 forward            (PC=0x100, target=0x128 → +10 instr).
;   2. BranchImm26 backward           (PC=0x200, target=0x1F0 → -4 instr).
;   3. BranchImm19 forward            (PC=0x100, target=0x108 → +2 instr).
;   4. BranchImm14 forward            (PC=0x010, target=0x014 → +1 instr).
;   5. AdrpImm cross-page             (PC=0x1234, target=0x5678 → +4 pages).
;   6. AdrpImm same-page              (PC=0x1234, target=0x1ABC →  0 pages).
;
; Test 5 expected value derivation (the splitting across immlo/immhi is
; performed by encode_adrp_imm itself, not by aarch64 hardware):
;   target & ~0xFFF = 0x00005000
;   pc     & ~0xFFF = 0x00001000
;   diff            = 0x00004000
;   pageOffset      = diff >> 12 = 4
;   imm21           = 4
;   immlo           = 4 & 3 = 0
;   immhi           = (4 >> 2) & 0x7FFFF = 1
;   encoded         = (immlo << 29) | (immhi << 5) = 0x00000020
;
; Test 6 expected value derivation:
;   target & ~0xFFF = 0x00001000
;   pc     & ~0xFFF = 0x00001000
;   diff = pageOffset = imm21 = immlo = immhi = 0
;   encoded         = 0x00000000
;
; Negative-case error paths (mis-aligned target, out-of-range diff) are
; not exercised here for the same reason test_slots.asm doesn't: every
; failure path is a one-way `jp fail` and we have no way to recover.
; Covered by inspection.


; -----------------------------------------------------------------------
; run_pc_rel_self_tests — entry point called from assembler.asm start:.
;
; Input:  none.
; Output: returns to caller on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_pc_rel_self_tests:

; -- (1) BranchImm26 forward: PC=0x100, target=0x128 -------------------
; byteOffset = 0x28 = 40 → instrOffset = 10 = 0xA.
; Slot BP=0, BW=26 → encoded = 0xA.
                call    set_pass_pc_imm
                defb    &00, &01, &00, &00          ; PASS_PC = 0x00000100 LE

                ld      hl, t_pc_rel_branch26
                ld      bc, &0000
                ld      de, &0128                   ; absolute target = 0x128
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &0a, &00, &00, &00          ; 0x0000000A LE

; -- (2) BranchImm26 backward: PC=0x200, target=0x1F0 ------------------
; byteOffset = -0x10 = -16 → instrOffset = -4 → masked 26 bits = 0x03FFFFFC.
                call    set_pass_pc_imm
                defb    &00, &02, &00, &00          ; PASS_PC = 0x00000200 LE

                ld      hl, t_pc_rel_branch26
                ld      bc, &0000
                ld      de, &01f0                   ; absolute target = 0x1F0
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &fc, &ff, &ff, &03          ; 0x03FFFFFC LE

; -- (3) BranchImm19 forward: PC=0x100, target=0x108 -------------------
; byteOffset = +8 → instrOffset = +2 → BP=5,BW=19 → encoded = 2<<5 = 0x40.
                call    set_pass_pc_imm
                defb    &00, &01, &00, &00          ; PASS_PC = 0x00000100 LE

                ld      hl, t_pc_rel_branch19
                ld      bc, &0000
                ld      de, &0108                   ; absolute target = 0x108
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &40, &00, &00, &00          ; 0x00000040 LE

; -- (4) BranchImm14 forward: PC=0x010, target=0x014 -------------------
; byteOffset = +4 → instrOffset = +1 → BP=5,BW=14 → encoded = 1<<5 = 0x20.
                call    set_pass_pc_imm
                defb    &10, &00, &00, &00          ; PASS_PC = 0x00000010 LE

                ld      hl, t_pc_rel_branch14
                ld      bc, &0000
                ld      de, &0014                   ; absolute target = 0x14
                call    encode_branch_imm
                call    assert_eq32_de_hl_imm
                defb    &20, &00, &00, &00          ; 0x00000020 LE

; -- (5) AdrpImm cross-page: PC=0x1234, target=0x5678 ------------------
; (target & ~0xFFF) - (pc & ~0xFFF) = 0x5000 - 0x1000 = 0x4000.
; pageOffset = 4 → imm21=4 → immlo=0, immhi=1 → encoded = 1<<5 = 0x20.
                call    set_pass_pc_imm
                defb    &34, &12, &00, &00          ; PASS_PC = 0x00001234 LE

                ld      hl, t_pc_rel_adrp
                ld      bc, &0000
                ld      de, &5678                   ; absolute target = 0x5678
                call    encode_adrp_imm
                call    assert_eq32_de_hl_imm
                defb    &20, &00, &00, &00          ; 0x00000020 LE

; -- (6) AdrpImm same-page: PC=0x1234, target=0x1ABC -------------------
; Both round down to 0x1000 → diff = 0 → encoded = 0.
                call    set_pass_pc_imm
                defb    &34, &12, &00, &00          ; PASS_PC = 0x00001234 LE

                ld      hl, t_pc_rel_adrp
                ld      bc, &0000
                ld      de, &1abc                   ; absolute target = 0x1ABC
                call    encode_adrp_imm
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &00, &00          ; 0x00000000 LE

; -- Restore PASS_PC = 0 -----------------------------------------------
; The walker re-zeroes PASS_PC at the start of each pass via
; pass_pc_reset, so this is observationally inert, but tidy up for any
; future suite that runs between us and main_assemble.
                call    pass_pc_reset

                ret


; -----------------------------------------------------------------------
; set_pass_pc_imm — assign PASS_PC from a 4-byte inline literal.
;
; Caller pattern (mirrors assert_eq32_de_hl_imm in test_slots.asm):
;     call set_pass_pc_imm
;     defb b0, b1, b2, b3        ; little-endian: b0 = bits 0..7
;
; The return address on the stack points at b0; we copy the 4 bytes into
; PASS_PC, advance the return address past them, and RET.
;
; Clobbers: A, BC.  Preserves DE and HL (so the caller can build the
; encoder arguments in the same statement group as the PC setup).
; -----------------------------------------------------------------------
set_pass_pc_imm:
                pop     bc                           ; BC = pointer to inline literal

                ld      a, (bc)
                ld      (PASS_PC + 0), a
                inc     bc
                ld      a, (bc)
                ld      (PASS_PC + 1), a
                inc     bc
                ld      a, (bc)
                ld      (PASS_PC + 2), a
                inc     bc
                ld      a, (bc)
                ld      (PASS_PC + 3), a
                inc     bc                           ; BC now points past the literal

                push    bc                           ; restore as return address
                ret


; -----------------------------------------------------------------------
; Static slot-record literals for the PC-relative self-tests.
;
; Layout: defb slot_kind, expected_kind, bit_position, bit_width.
; The encoders consult bit_position and bit_width but not slot_kind /
; expected_kind, matching the practice in test_slots.asm.
; -----------------------------------------------------------------------
t_pc_rel_branch26:      defb    &20, 0,  0, 26
t_pc_rel_branch19:      defb    &21, 0,  5, 19
t_pc_rel_branch14:      defb    &22, 0,  5, 14
t_pc_rel_adrp:          defb    &23, 0,  0, 21
