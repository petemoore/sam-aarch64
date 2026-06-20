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

run_encode_inst_self_tests:
                call    enctab_map_in
                call    form_lookup_init

; -- nop  =>  0xD503201F  (mnem 0, 0 operands) -------------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_nop
                ld      a, 0
                ld      de, 0
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &1f, &20, &03, &d5

; -- ret x30  =>  0xD65F03C0  (mnem 12, [Xreg]) ------------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_ret
                ld      a, 1
                ld      de, 12
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &c0, &03, &5f, &d6

; -- add x0, x1, #5  =>  0x91001420  (mnem 1, [XSP,XSP,Imm12]) ---------
                call    enc_seed_pc0
                ld      hl, enc_fix_add
                ld      a, 3
                ld      de, 1
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &14, &00, &91

; -- sub x2, x3, #0x1000  =>  0xD1400462  (imm12 lsl #12 case) ---------
                call    enc_seed_pc0
                ld      hl, enc_fix_sub
                ld      a, 3
                ld      de, 2
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &62, &04, &40, &d1

; -- movz x0, #0x1234  =>  0xD2824680  (mnem 81, [Xreg,Imm16Shifted]) --
                call    enc_seed_pc0
                ld      hl, enc_fix_movz
                ld      a, 2
                ld      de, 81
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &80, &46, &82, &d2

; -- orr x0, x1, #0xff  =>  0xB2401C20  (mnem 15, [Xreg,Xreg,Logical]) -
                call    enc_seed_pc0
                ld      hl, enc_fix_orr
                ld      a, 3
                ld      de, 15
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &1c, &40, &b2

; -- csel x0, x1, x2, eq  =>  0x9A820020  (mnem 24, [X,X,X,Cond]) ------
                call    enc_seed_pc0
                ld      hl, enc_fix_csel
                ld      a, 4
                ld      de, 24
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &00, &82, &9a

; -- cbz x0, pc+8 @0x1000  =>  0xB4000040  (mnem 20, [Xreg,Branch19]) --
                call    enc_seed_pc_1000
                ld      hl, enc_fix_cbz
                ld      a, 2
                ld      de, 20
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &40, &00, &00, &b4

; -- b pc+16 @0x1000  =>  0x14000004  (mnem 9, [Branch26]) ------------
                call    enc_seed_pc_1000
                ld      hl, enc_fix_b
                ld      a, 1
                ld      de, 9
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &04, &00, &00, &14

; -- adrp x0, 0x3000 @0x1000  =>  0xD0000000  (mnem 13, [Xreg,AdrpImm]) -
                call    enc_seed_pc_1000
                ld      hl, enc_fix_adrp
                ld      a, 2
                ld      de, 13
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &00, &d0

; -- adr x0, pc+4 @0x1000  =>  0x10000020  (mnem 48, [Xreg,AdrImm]) ----
                call    enc_seed_pc_1000
                ld      hl, enc_fix_adr
                ld      a, 2
                ld      de, 48
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &00, &00, &10

; -- i203a special forms: shift / bitfield / ror -----------------------
; -- lsl x0, x1, #4  =>  0xD37CEC20  (mnem 17, UBFM alias) -------------
                call    enc_seed_pc0
                ld      hl, enc_fix_lsl_x
                ld      a, 3
                ld      de, 17
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &ec, &7c, &d3

; -- lsl w0, w1, #4  =>  0x531C6C20  (32-bit UBFM alias) --------------
                call    enc_seed_pc0
                ld      hl, enc_fix_lsl_w
                ld      a, 3
                ld      de, 17
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &6c, &1c, &53

; -- lsr x0, x1, #4  =>  0xD344FC20  (mnem 18, UBFM alias) -------------
                call    enc_seed_pc0
                ld      hl, enc_fix_lsr_x
                ld      a, 3
                ld      de, 18
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &fc, &44, &d3

; -- lsl x0, x1, x2  =>  0x9AC22020  (LSLV reg form) ------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_lslv
                ld      a, 3
                ld      de, 17
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &20, &c2, &9a

; -- lsr x0, x1, x2  =>  0x9AC22420  (LSRV reg form) ------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_lsrv
                ld      a, 3
                ld      de, 18
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &24, &c2, &9a

; -- bfi x0, x1, #8, #4  =>  0xB3780C20  (BFM alias) ------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_bfi
                ld      a, 4
                ld      de, 49
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &0c, &78, &b3

; -- bfxil x0, x1, #8, #4  =>  0xB3482C20  (BFM alias) ----------------
                call    enc_seed_pc0
                ld      hl, enc_fix_bfxil
                ld      a, 4
                ld      de, 50
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &2c, &48, &b3

; -- ubfx w0, w1, #8, #4  =>  0x53082C20  (32-bit UBFM alias) ---------
                call    enc_seed_pc0
                ld      hl, enc_fix_ubfx
                ld      a, 4
                ld      de, 51
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &2c, &08, &53

; -- bfc x0, #8, #4  =>  0xB3780FE0  (BFM alias, Rn=XZR) --------------
                call    enc_seed_pc0
                ld      hl, enc_fix_bfc
                ld      a, 3
                ld      de, 83
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &e0, &0f, &78, &b3

; -- sbfx x0, x1, #8, #4  =>  0x93482C20  (SBFM alias) ----------------
                call    enc_seed_pc0
                ld      hl, enc_fix_sbfx
                ld      a, 4
                ld      de, 84
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &2c, &48, &93

; -- ror x0, x1, #4  =>  0x93C11020  (EXTR alias) --------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_ror_x
                ld      a, 3
                ld      de, 70
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &10, &c1, &93

; -- ror w0, w1, #4  =>  0x13811020  (32-bit EXTR alias) -------------
                call    enc_seed_pc0
                ld      hl, enc_fix_ror_w
                ld      a, 3
                ld      de, 70
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &10, &81, &13

; -- i203b special forms: bic-imm / csetm / barrier --------------------
; -- bic x0, x1, #0xff  =>  0x9278DC20  (AND ~imm logical) ------------
                call    enc_seed_pc0
                ld      hl, enc_fix_bic_x
                ld      a, 3
                ld      de, 47
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &dc, &78, &92

; -- bic w0, w1, #0xff  =>  0x12185C20  (32-bit AND ~imm) ------------
                call    enc_seed_pc0
                ld      hl, enc_fix_bic_w
                ld      a, 3
                ld      de, 47
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &5c, &18, &12

; -- csetm x0, eq  =>  0xDA9F13E0  (CSINV inverted cond) -------------
                call    enc_seed_pc0
                ld      hl, enc_fix_csetm_x
                ld      a, 2
                ld      de, 52
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &e0, &13, &9f, &da

; -- csetm w3, ne  =>  0x5A9F03E3  (32-bit CSINV) -------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_csetm_w
                ld      a, 2
                ld      de, 52
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &e3, &03, &9f, &5a

; -- isb #15  =>  0xD5033FDF  (barrier CRm in bits 11:8) ------------
                call    enc_seed_pc0
                ld      hl, enc_fix_isb
                ld      a, 1
                ld      de, 66
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &df, &3f, &03, &d5

; -- dsb #11  =>  0xD5033B9F ----------------------------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_dsb
                ld      a, 1
                ld      de, 67
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &9f, &3b, &03, &d5

; -- dmb #11  =>  0xD5033BBF ----------------------------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_dmb
                ld      a, 1
                ld      de, 68
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &bf, &3b, &03, &d5

; -- i203c special forms: mrs / msr / dc / tlbi (paged sysreg tables) ---
; -- mrs x0, midr_el1  =>  0xD5380000 -------------------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_mrs
                ld      a, 2
                ld      de, 76
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &38, &d5

; -- msr daifset, #2  =>  0xD50342DF  (PSTATE-immediate form) -------
                call    enc_seed_pc0
                ld      hl, enc_fix_msr
                ld      a, 2
                ld      de, 77
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &df, &42, &03, &d5

; -- dc cvac, x0  =>  0xD50B7A20 -----------------------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_dc
                ld      a, 2
                ld      de, 78
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &20, &7a, &0b, &d5

; -- tlbi vmalle1  =>  0xD508871F  (no-Xt branch) ------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_tlbi
                ld      a, 1
                ld      de, 79
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &1f, &87, &08, &d5

; -- i203d special forms: mov-imm autoselect / ldr-lit / tbz/tbnz ------
; -- mov x0, #0x12340000  =>  0xD2A24680  (movz lsl#16) -------------
                call    enc_seed_pc0
                ld      hl, enc_fix_mov_movz
                ld      a, 2
                ld      de, 3
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &80, &46, &a2, &d2

; -- mov x0, #-1  =>  0x92800000  (movn #0) ------------------------
                call    enc_seed_pc0
                ld      hl, enc_fix_mov_movn
                ld      a, 2
                ld      de, 3
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &00, &00, &80, &92

; -- mov w1, #0xfffffffe  =>  0x12800021  (32-bit movn #1) ---------
                call    enc_seed_pc0
                ld      hl, enc_fix_mov_movnw
                ld      a, 2
                ld      de, 3
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &21, &00, &80, &12

; -- mov x2, #0x5555555555555555  =>  0xB200F3E2  (orr-imm) --------
                call    enc_seed_pc0
                ld      hl, enc_fix_mov_orr
                ld      a, 2
                ld      de, 3
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &e2, &f3, &00, &b2

; -- ldr x0, pc+8 @0x1000  =>  0x58000040  (literal imm19) --------
                call    enc_seed_pc_1000
                ld      hl, enc_fix_ldr_x
                ld      a, 2
                ld      de, 5
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &40, &00, &00, &58

; -- ldr w1, pc+16 @0x1000  =>  0x18000081  (32-bit literal) ------
                call    enc_seed_pc_1000
                ld      hl, enc_fix_ldr_w
                ld      a, 2
                ld      de, 5
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &81, &00, &00, &18

; -- tbz x0, #5, pc+16 @0x1000  =>  0x36280080 -------------------
                call    enc_seed_pc_1000
                ld      hl, enc_fix_tbz
                ld      a, 3
                ld      de, 22
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &80, &00, &28, &36

; -- tbnz w1, #3, pc+8 @0x1000  =>  0x37180041 ------------------
                call    enc_seed_pc_1000
                ld      hl, enc_fix_tbnz
                ld      a, 3
                ld      de, 23
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &41, &00, &18, &37

                call    enctab_map_out
                ret

; -- PASS_PC seeders ----------------------------------------------------
enc_seed_pc0:
                xor     a
                ld      (PASS_PC + 0), a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                ret

enc_seed_pc_1000:                                   ; PASS_PC = 0x00001000
                xor     a
                ld      (PASS_PC + 0), a
                ld      a, &10
                ld      (PASS_PC + 1), a
                xor     a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                ret

; -- Fixture operand streams (exact Go OperandWriter bytes) -------------
enc_fix_nop:                                         ; (no operands)
enc_fix_ret:    defb    &01, &1e
enc_fix_add:    defb    &03, &00, &03, &01, &05, &02, &00, &01, &05
enc_fix_sub:    defb    &03, &02, &03, &03, &05, &03, &00, &02, &00, &10
enc_fix_movz:   defb    &01, &00, &05, &03, &00, &02, &34, &12
enc_fix_orr:    defb    &01, &00, &01, &01, &05, &03, &00, &02, &ff, &00
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
