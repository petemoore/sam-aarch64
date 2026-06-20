; test_encode_inst.asm — boot-time self-test for encode_inst (the
; standalone aarch64 instruction encoder, the form-table path).
; Item i199 / i48c-b8e brick 1.
;
; Unlike the per-slot tests in test_slots.asm (which run BEFORE
; load_enctab against inline literal slot records), encode_inst reads
; the form table + slot records from ENCTAB, so this suite must run
; AFTER load_enctab and inside its own enctab_map_in / form_lookup_init
; / enctab_map_out bracket — the same window main_assemble opens.  It
; runs INLINE in section C (NOT off-axis): the off-axis cluster pages
; its test code into section A, which is exactly where ENCTAB lives, so
; the two cannot coexist.
;
; Every fixture mirrors one case in
; tools/sam-aarch64/assemble/encoder_skeleton_fixtures_test.go
; ::TestEncoderSkeletonFixtures — that Go test is the authority
; (CLAUDE.md §6) and the drift guard: it re-derives each expected word
; from the Go encoder.  The operand byte streams below are the exact
; .tbn operand bytes the Go OperandWriter emits for each fixture; the
; expected words are encodeInst's output.  All exprs are PUSH_IMM
; constants, so no symbol table is needed — only PASS_PC (seeded per
; fixture for the PC-relative slots).
;
; Assertion helper: assert_eq32_de_hl_imm (test_assert_eq32.asm) — it
; compares DEHL against the 4 inline LE bytes following the call.
;
; Any mismatch -> jp fail (red border + "FAIL" banner); on all-pass the
; entry point RETs.
; -----------------------------------------------------------------------

; The fixtures are driven from a table (enc_fix_table) rather than unrolled
; per case: every case is the same {seed PASS_PC, encode_inst, compare DEHL
; to the expected word} shape, differing only in pc / fixture ptr / opcount /
; mnemonic id / expected word.  Each 11-byte row is:
;   +0 pc_lo16 (LE)    PASS_PC low 16 bits; high 16 are always 0 here
;   +2 fixture ptr     -> operand byte stream
;   +4 opcount
;   +5 mnemonic id (LE)
;   +7 expected word (4 LE bytes)
; On mismatch the row pointer is recorded in LAST_FAIL_PC (as the existing
; inline-literal asserts do) and we jp fail.
ENC_FIX_ROW_LEN: equ    11

run_encode_inst_self_tests:
                call    enctab_map_in
                call    form_lookup_init

                ld      hl, enc_fix_table
enc_fix_loop:
                ld      (enc_fix_row), hl
; Seed PASS_PC = pc_lo16 (row+0/+1; high 16 bits := 0).  HL walks forward.
                ld      a, (hl)
                ld      (PASS_PC + 0), a
                inc     hl
                ld      a, (hl)
                ld      (PASS_PC + 1), a
                xor     a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
; row+2/+3 = fixture ptr.  A zero fixture ptr is the end-of-table sentinel.
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = fixture ptr
                ld      a, e
                or      d
                jr      z, enc_fix_done
; Load remaining encode_inst args: A = opcount, BC = mnemonic id.
                inc     hl
                ld      a, (hl)                 ; A  = opcount
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                 ; BC = mnemonic id
                push    de
                pop     hl                      ; HL = fixture ptr
                push    bc
                pop     de                      ; DE = mnemonic id
                call    encode_inst

; Compare DEHL (encoded word) against the row's expected bytes (+7..+10).
                push    de
                push    hl
                ld      hl, (enc_fix_row)
                ld      de, 7
                add     hl, de
                pop     de                      ; DE = encoded low word (was HL)
                ld      a, (hl)
                cp      e
                jr      nz, enc_fix_fail
                inc     hl
                ld      a, (hl)
                cp      d
                jr      nz, enc_fix_fail
                inc     hl
                pop     de                      ; DE = encoded high word
                ld      a, (hl)
                cp      e
                jr      nz, enc_fix_fail
                inc     hl
                ld      a, (hl)
                cp      d
                jr      nz, enc_fix_fail

; Advance to the next row.
                ld      hl, (enc_fix_row)
                ld      de, ENC_FIX_ROW_LEN
                add     hl, de
                jr      enc_fix_loop

enc_fix_done:
                call    enctab_map_out
                ret

enc_fix_fail:
                ld      hl, (enc_fix_row)
                ld      (LAST_FAIL_PC), hl
                jp      fail

; row layout: pc_lo16, fixture, opcount, mnem(LE), expected(4 LE bytes)
enc_fix_table:
                defw    &0000
                defw    enc_fix_nop
                defb    0
                defw    0
                defb    &1f, &20, &03, &d5
                defw    &0000
                defw    enc_fix_ret
                defb    1
                defw    12
                defb    &c0, &03, &5f, &d6
                defw    &0000
                defw    enc_fix_add
                defb    3
                defw    1
                defb    &20, &14, &00, &91
                defw    &0000
                defw    enc_fix_sub
                defb    3
                defw    2
                defb    &62, &04, &40, &d1
                defw    &0000
                defw    enc_fix_movz
                defb    2
                defw    81
                defb    &80, &46, &82, &d2
                defw    &0000
                defw    enc_fix_orr
                defb    3
                defw    15
                defb    &20, &1c, &40, &b2
                defw    &0000
                defw    enc_fix_orr_s32
                defb    3
                defw    15
                defb    &20, &00, &00, &b2
                defw    &0000
                defw    enc_fix_orr_s16
                defb    3
                defw    15
                defb    &20, &80, &00, &b2
                defw    &0000
                defw    enc_fix_orr_s8
                defb    3
                defw    15
                defb    &20, &c0, &00, &b2
                defw    &0000
                defw    enc_fix_orr_s4
                defb    3
                defw    15
                defb    &20, &e0, &00, &b2
                defw    &0000
                defw    enc_fix_orr_s2
                defb    3
                defw    15
                defb    &20, &f0, &00, &b2
                defw    &0000
                defw    enc_fix_csel
                defb    4
                defw    24
                defb    &20, &00, &82, &9a
                defw    &1000
                defw    enc_fix_cbz
                defb    2
                defw    20
                defb    &40, &00, &00, &b4
                defw    &1000
                defw    enc_fix_b
                defb    1
                defw    9
                defb    &04, &00, &00, &14
                defw    &1000
                defw    enc_fix_adrp
                defb    2
                defw    13
                defb    &00, &00, &00, &d0
                defw    &1000
                defw    enc_fix_adr
                defb    2
                defw    48
                defb    &20, &00, &00, &10
                defw    &0000
                defw    enc_fix_lsl_x
                defb    3
                defw    17
                defb    &20, &ec, &7c, &d3
                defw    &0000
                defw    enc_fix_lsl_w
                defb    3
                defw    17
                defb    &20, &6c, &1c, &53
                defw    &0000
                defw    enc_fix_lsr_x
                defb    3
                defw    18
                defb    &20, &fc, &44, &d3
                defw    &0000
                defw    enc_fix_lslv
                defb    3
                defw    17
                defb    &20, &20, &c2, &9a
                defw    &0000
                defw    enc_fix_lsrv
                defb    3
                defw    18
                defb    &20, &24, &c2, &9a
                defw    &0000
                defw    enc_fix_bfi
                defb    4
                defw    49
                defb    &20, &0c, &78, &b3
                defw    &0000
                defw    enc_fix_bfxil
                defb    4
                defw    50
                defb    &20, &2c, &48, &b3
                defw    &0000
                defw    enc_fix_ubfx
                defb    4
                defw    51
                defb    &20, &2c, &08, &53
                defw    &0000
                defw    enc_fix_bfc
                defb    3
                defw    83
                defb    &e0, &0f, &78, &b3
                defw    &0000
                defw    enc_fix_sbfx
                defb    4
                defw    84
                defb    &20, &2c, &48, &93
                defw    &0000
                defw    enc_fix_ror_x
                defb    3
                defw    70
                defb    &20, &10, &c1, &93
                defw    &0000
                defw    enc_fix_ror_w
                defb    3
                defw    70
                defb    &20, &10, &81, &13
                defw    &0000
                defw    enc_fix_bic_x
                defb    3
                defw    47
                defb    &20, &dc, &78, &92
                defw    &0000
                defw    enc_fix_bic_w
                defb    3
                defw    47
                defb    &20, &5c, &18, &12
                defw    &0000
                defw    enc_fix_csetm_x
                defb    2
                defw    52
                defb    &e0, &13, &9f, &da
                defw    &0000
                defw    enc_fix_csetm_w
                defb    2
                defw    52
                defb    &e3, &03, &9f, &5a
                defw    &0000
                defw    enc_fix_isb
                defb    1
                defw    66
                defb    &df, &3f, &03, &d5
                defw    &0000
                defw    enc_fix_dsb
                defb    1
                defw    67
                defb    &9f, &3b, &03, &d5
                defw    &0000
                defw    enc_fix_dmb
                defb    1
                defw    68
                defb    &bf, &3b, &03, &d5
                defw    &0000
                defw    enc_fix_mrs
                defb    2
                defw    76
                defb    &00, &00, &38, &d5
                defw    &0000
                defw    enc_fix_msr
                defb    2
                defw    77
                defb    &df, &42, &03, &d5
                defw    &0000
                defw    enc_fix_dc
                defb    2
                defw    78
                defb    &20, &7a, &0b, &d5
                defw    &0000
                defw    enc_fix_tlbi
                defb    1
                defw    79
                defb    &1f, &87, &08, &d5
; i203d special forms: mov-imm autoselect / ldr-lit / tbz/tbnz
                defw    &0000
                defw    enc_fix_mov_movz                ; mov x0,#0x12340000 -> movz lsl#16
                defb    2
                defw    3
                defb    &80, &46, &a2, &d2
                defw    &0000
                defw    enc_fix_mov_movn                ; mov x0,#-1 -> movn #0
                defb    2
                defw    3
                defb    &00, &00, &80, &92
                defw    &0000
                defw    enc_fix_mov_movnw               ; mov w1,#0xfffffffe -> movn #1 (W)
                defb    2
                defw    3
                defb    &21, &00, &80, &12
                defw    &0000
                defw    enc_fix_mov_orr                 ; mov x2,#0x5555... -> orr-imm
                defb    2
                defw    3
                defb    &e2, &f3, &00, &b2
                defw    &1000
                defw    enc_fix_ldr_x                   ; ldr x0,pc+8 @0x1000 -> imm19
                defb    2
                defw    5
                defb    &40, &00, &00, &58
                defw    &1000
                defw    enc_fix_ldr_w                   ; ldr w1,pc+16 @0x1000 -> imm19 (W)
                defb    2
                defw    5
                defb    &81, &00, &00, &18
                defw    &1000
                defw    enc_fix_tbz                     ; tbz x0,#5,pc+16 @0x1000 -> imm14
                defb    3
                defw    22
                defb    &80, &00, &28, &36
                defw    &1000
                defw    enc_fix_tbnz                    ; tbnz w1,#3,pc+8 @0x1000 -> imm14
                defb    3
                defw    23
                defb    &41, &00, &18, &37
; sentinel: fixture ptr 0 terminates the table
                defw    &0000
                defw    0
                defb    0
                defw    0
                defb    0, 0, 0, 0

enc_fix_row:    defw    0

; -- Fixture operand streams (exact Go OperandWriter bytes) -------------
enc_fix_nop:                                         ; (no operands)
enc_fix_ret:    defb    &01, &1e
enc_fix_add:    defb    &03, &00, &03, &01, &05, &02, &00, &01, &05
enc_fix_sub:    defb    &03, &02, &03, &03, &05, &03, &00, &02, &00, &10
enc_fix_movz:   defb    &01, &00, &05, &03, &00, &02, &34, &12
enc_fix_orr:    defb    &01, &00, &01, &01, &05, &03, &00, &02, &ff, &00
; i205 logical-imm replication-size coverage (exact Go OperandWriter bytes)
enc_fix_orr_s32: defb   &01, &00, &01, &01, &05, &09, &00, &04, &01, &00, &00, &00, &01, &00, &00, &00
enc_fix_orr_s16: defb   &01, &00, &01, &01, &05, &09, &00, &04, &01, &00, &01, &00, &01, &00, &01, &00
enc_fix_orr_s8:  defb   &01, &00, &01, &01, &05, &09, &00, &04, &01, &01, &01, &01, &01, &01, &01, &01
enc_fix_orr_s4:  defb   &01, &00, &01, &01, &05, &09, &00, &04, &11, &11, &11, &11, &11, &11, &11, &11
enc_fix_orr_s2:  defb   &01, &00, &01, &01, &05, &09, &00, &04, &55, &55, &55, &55, &55, &55, &55, &55
enc_fix_csel:   defb    &01, &00, &01, &01, &01, &02, &0a, &00
enc_fix_cbz:    defb    &01, &00, &05, &03, &00, &02, &08, &10
enc_fix_b:      defb    &05, &03, &00, &02, &10, &10
enc_fix_adrp:   defb    &01, &00, &05, &03, &00, &02, &00, &30
enc_fix_adr:    defb    &01, &00, &05, &03, &00, &02, &04, &10
; i203a special forms (exact Go OperandWriter bytes)
enc_fix_lsl_x:  defb    &01, &00, &01, &01, &05, &02, &00, &01, &04
enc_fix_lsl_w:  defb    &02, &00, &02, &01, &05, &02, &00, &01, &04
enc_fix_lsr_x:  defb    &01, &00, &01, &01, &05, &02, &00, &01, &04
enc_fix_lslv:   defb    &01, &00, &01, &01, &01, &02
enc_fix_lsrv:   defb    &01, &00, &01, &01, &01, &02
enc_fix_bfi:    defb    &01, &00, &01, &01, &05, &02, &00, &01, &08, &05, &02, &00, &01, &04
enc_fix_bfxil:  defb    &01, &00, &01, &01, &05, &02, &00, &01, &08, &05, &02, &00, &01, &04
enc_fix_ubfx:   defb    &02, &00, &02, &01, &05, &02, &00, &01, &08, &05, &02, &00, &01, &04
enc_fix_bfc:    defb    &01, &00, &05, &02, &00, &01, &08, &05, &02, &00, &01, &04
enc_fix_sbfx:   defb    &01, &00, &01, &01, &05, &02, &00, &01, &08, &05, &02, &00, &01, &04
enc_fix_ror_x:  defb    &01, &00, &01, &01, &05, &02, &00, &01, &04
enc_fix_ror_w:  defb    &02, &00, &02, &01, &05, &02, &00, &01, &04
; i203b special forms (exact Go OperandWriter bytes)
enc_fix_bic_x:  defb    &01, &00, &01, &01, &05, &03, &00, &02, &ff, &00
enc_fix_bic_w:  defb    &02, &00, &02, &01, &05, &03, &00, &02, &ff, &00
enc_fix_csetm_x: defb   &01, &00, &0a, &00
enc_fix_csetm_w: defb   &02, &03, &0a, &01
enc_fix_isb:    defb    &05, &02, &00, &01, &0f
enc_fix_dsb:    defb    &05, &02, &00, &01, &0b
enc_fix_dmb:    defb    &05, &02, &00, &01, &0b
; i203c sysreg forms (exact Go OperandWriter bytes; ASCII names inline:
; "midr_el1" / "daifset" / "cvac" / "vmalle1")
enc_fix_mrs:    defb    &01, &00, &0b, &08, &00, &6d, &69, &64, &72, &5f, &65, &6c, &31
enc_fix_msr:    defb    &0b, &07, &00, &64, &61, &69, &66, &73, &65, &74, &05, &02, &00, &01, &02
enc_fix_dc:     defb    &0b, &04, &00, &63, &76, &61, &63, &01, &00
enc_fix_tlbi:   defb    &0b, &07, &00, &76, &6d, &61, &6c, &6c, &65, &31
; i203d mov-imm / ldr-lit / tbz (exact Go OperandWriter bytes)
enc_fix_mov_movz:  defb &01, &00, &05, &05, &00, &03, &00, &00, &34, &12
enc_fix_mov_movn:  defb &01, &00, &05, &02, &00, &01, &ff
enc_fix_mov_movnw: defb &02, &01, &05, &02, &00, &01, &fe
enc_fix_mov_orr:   defb &01, &02, &05, &09, &00, &04, &55, &55, &55, &55, &55, &55, &55, &55
enc_fix_ldr_x:     defb &01, &00, &05, &03, &00, &02, &08, &10
enc_fix_ldr_w:     defb &02, &01, &05, &03, &00, &02, &10, &10
enc_fix_tbz:       defb &01, &00, &05, &02, &00, &01, &05, &05, &03, &00, &02, &10, &10
enc_fix_tbnz:      defb &02, &01, &05, &02, &00, &01, &03, &05, &03, &00, &02, &08, &10
