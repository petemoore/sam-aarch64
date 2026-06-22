; test_encode_inst_payload.asm — off-axis data payload for the
; encode_inst boot self-test (i69 lever 3).
;
; This file assembles to a standalone binary (org &0000) that is
; HLOADed at boot into physical page 11 (ENC_FIX_PAGE) by
; load_enc_fix_payload in src/loader.asm, then bulk-copied via LDIR
; into section-D RAM starting at ENC_FIX_TABLE_RAM = &E100, at the
; very start of run_encode_inst_self_tests, before enctab_map_in.
;
; Because the payload is org'd at &0000 and the LDIR copies it to
; &E100, every internal pointer (enc_fix_table row's "fixture ptr"
; field, pointing at one of the enc_fix_* operand-stream blocks) is
; a physical-address-within-payload offset.  Adding ENC_FIX_TABLE_RAM
; (&E100) to each pointer gives the correct section-D address after
; the copy.  Equivalently, org'ing at ENC_FIX_TABLE_RAM = &E100
; bakes those section-D addresses directly — so `org &E100` is used
; here.  After the LDIR the existing driver loop (which reads the
; row pointer from enc_fix_row, advances by ENC_FIX_ROW_LEN, and
; dereferences the fixture pointer as a section-D address) works
; with no changes.
;
; This file contains ONLY data — no executable code — and is
; self-contained (every value is a literal byte).  It is assembled
; standalone with --exportfile=enc_fix_payload.sym, which exports
; ENC_FIX_PAYLOAD_LEN for the assembler build to import (the LDIR
; byte count).
;
; The content below is the verbatim fixture table and operand streams
; from src/test_encode_inst.asm, relocated here with org &E100 so
; the row→stream pointers are section-D absolute addresses.
;
; Row layout (11 bytes, ENC_FIX_ROW_LEN):
;   +0  pc_lo16 (LE)    PASS_PC seed (low 16 bits; high 16 always 0)
;   +2  fixture ptr     -> section-D operand byte stream (absolute)
;   +4  opcount
;   +5  mnemonic id (LE)
;   +7  expected word (4 LE bytes)
; A sentinel row with fixture ptr = 0 terminates the table.
;
; ENC_FIX_PAYLOAD_LEN (defined at the foot) is the total byte count;
; the driver reads it to set BC for the LDIR.

ENC_FIX_TABLE_RAM:  equ     &E100          ; section-D copy destination

                org     ENC_FIX_TABLE_RAM

; enc_fix_table — the fixture table.  Fixture ptrs are section-D
; absolute addresses (from the org &E100 above).
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
; i201a compound-operand fixtures (shifted-reg + extended-reg)
                defw    &0000
                defw    enc_fix_sr_add_lsl3             ; add x0,x1,x2,lsl#3
                defb    3
                defw    1
                defb    &20, &0c, &02, &8b
                defw    &0000
                defw    enc_fix_sr_sub_lsr7             ; sub x3,x4,x5,lsr#7
                defb    3
                defw    2
                defb    &83, &1c, &45, &cb
                defw    &0000
                defw    enc_fix_sr_and_asr2             ; and w6,w7,w8,asr#2
                defb    3
                defw    14
                defb    &e6, &08, &88, &0a
                defw    &0000
                defw    enc_fix_sr_orr_ror15            ; orr x9,x10,x11,ror#15
                defb    3
                defw    15
                defb    &49, &3d, &cb, &aa
                defw    &0000
                defw    enc_fix_sr_eor_lsl0             ; eor x12,x13,x14,lsl#0
                defb    3
                defw    16
                defb    &ac, &01, &0e, &ca
                defw    &0000
                defw    enc_fix_sr_bic_lsl0             ; bic x0,x1,x2,lsl#0
                defb    3
                defw    47
                defb    &20, &00, &22, &8a
                defw    &0000
                defw    enc_fix_sr_subs_lsl0            ; subs x3,x4,x5,lsl#0
                defb    3
                defw    45
                defb    &83, &00, &05, &eb
                defw    &0000
                defw    enc_fix_sr_ands_lsl0            ; ands w0,w1,w2,lsl#0
                defb    3
                defw    80
                defb    &20, &00, &02, &6a
                defw    &0000
                defw    enc_fix_coerce_add3             ; add x0,x1,x2 (3-reg coercion)
                defb    3
                defw    1
                defb    &20, &00, &02, &8b
                defw    &0000
                defw    enc_fix_coerce_sub3             ; sub w3,w4,w5 (3-reg coercion W)
                defb    3
                defw    2
                defb    &83, &00, &05, &4b
                defw    &0000
                defw    enc_fix_coerce_tst2x            ; tst x0,x1 (2-reg coercion X)
                defb    2
                defw    46
                defb    &1f, &00, &01, &ea
                defw    &0000
                defw    enc_fix_coerce_tst2w            ; tst w2,w3 (2-reg coercion W)
                defb    2
                defw    46
                defb    &5f, &00, &03, &6a
                defw    &0000
                defw    enc_fix_sr_tst_lsl3             ; tst x0,x1,lsl#3 (explicit ShiftedReg)
                defb    2
                defw    46
                defb    &1f, &0c, &01, &ea
                defw    &0000
                defw    enc_fix_er_add_uxtw2            ; add x0,x1,w2,uxtw#2
                defb    3
                defw    1
                defb    &20, &48, &22, &8b
                defw    &0000
                defw    enc_fix_er_add_sxtx1            ; add x3,x4,x5,sxtx#1
                defb    3
                defw    1
                defb    &83, &e4, &25, &8b
                defw    &0000
                defw    enc_fix_er_sub_uxtb0            ; sub x6,x7,w8,uxtb#0
                defb    3
                defw    2
                defb    &e6, &00, &28, &cb
                defw    &0000
                defw    enc_fix_er_sub_sxth3            ; sub x9,x10,x11,sxth#3
                defb    3
                defw    2
                defb    &49, &ad, &2b, &cb
; sentinel: fixture ptr 0 terminates the table
                defw    &0000
                defw    0
                defb    0
                defw    0
                defb    0, 0, 0, 0

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

; i201a compound-operand fixture operand streams (exact Go OperandWriter bytes)
; add x0,x1,x2,lsl#3: Rd=x0, Rn=x1, ShiftedReg(X,Rm=2,LSL,amt=3)
enc_fix_sr_add_lsl3:    defb    &01, &00, &01, &01, &06, &01, &02, &00, &02, &00, &01, &03
; sub x3,x4,x5,lsr#7: Rd=x3, Rn=x4, ShiftedReg(X,Rm=5,LSR,amt=7)
enc_fix_sr_sub_lsr7:    defb    &01, &03, &01, &04, &06, &01, &05, &01, &02, &00, &01, &07
; and w6,w7,w8,asr#2: Rd=w6, Rn=w7, ShiftedReg(W,Rm=8,ASR,amt=2)
enc_fix_sr_and_asr2:    defb    &02, &06, &02, &07, &06, &00, &08, &02, &02, &00, &01, &02
; orr x9,x10,x11,ror#15: Rd=x9, Rn=x10, ShiftedReg(X,Rm=11,ROR,amt=15)
enc_fix_sr_orr_ror15:   defb    &01, &09, &01, &0a, &06, &01, &0b, &03, &02, &00, &01, &0f
; eor x12,x13,x14,lsl#0: Rd=x12, Rn=x13, ShiftedReg(X,Rm=14,LSL,amt=0)
enc_fix_sr_eor_lsl0:    defb    &01, &0c, &01, &0d, &06, &01, &0e, &00, &02, &00, &01, &00
; bic x0,x1,x2,lsl#0: Rd=x0, Rn=x1, ShiftedReg(X,Rm=2,LSL,amt=0) N=1
enc_fix_sr_bic_lsl0:    defb    &01, &00, &01, &01, &06, &01, &02, &00, &02, &00, &01, &00
; subs x3,x4,x5,lsl#0
enc_fix_sr_subs_lsl0:   defb    &01, &03, &01, &04, &06, &01, &05, &00, &02, &00, &01, &00
; ands w0,w1,w2,lsl#0
enc_fix_sr_ands_lsl0:   defb    &02, &00, &02, &01, &06, &00, &02, &00, &02, &00, &01, &00
; add x0,x1,x2 (3-reg coercion -> LSL#0)
enc_fix_coerce_add3:    defb    &01, &00, &01, &01, &01, &02
; sub w3,w4,w5 (3-reg coercion W -> LSL#0)
enc_fix_coerce_sub3:    defb    &02, &03, &02, &04, &02, &05
; tst x0,x1 (2-reg coercion X -> ShiftedReg LSL#0, Rd=xzr)
enc_fix_coerce_tst2x:   defb    &01, &00, &01, &01
; tst w2,w3 (2-reg coercion W -> ShiftedReg LSL#0)
enc_fix_coerce_tst2w:   defb    &02, &02, &02, &03
; tst x0,x1,lsl#3 (explicit ShiftedReg operand on tst)
enc_fix_sr_tst_lsl3:    defb    &01, &00, &06, &01, &01, &00, &02, &00, &01, &03
; add x0,x1,w2,uxtw#2: ExtendedReg(W,Rm=2,UXTW,amt=2)
enc_fix_er_add_uxtw2:   defb    &01, &00, &01, &01, &07, &00, &02, &02, &02, &00, &01, &02
; add x3,x4,x5,sxtx#1: ExtendedReg(X,Rm=5,SXTX,amt=1)
enc_fix_er_add_sxtx1:   defb    &01, &03, &01, &04, &07, &01, &05, &07, &02, &00, &01, &01
; sub x6,x7,w8,uxtb#0: ExtendedReg(W,Rm=8,UXTB,amt=0)
enc_fix_er_sub_uxtb0:   defb    &01, &06, &01, &07, &07, &00, &08, &00, &02, &00, &01, &00
; sub x9,x10,x11,sxth#3: ExtendedReg(X,Rm=11,SXTH,amt=3)
enc_fix_er_sub_sxth3:   defb    &01, &09, &01, &0a, &07, &01, &0b, &05, &02, &00, &01, &03

; Total payload size: the LDIR in run_encode_inst_self_tests uses this
; count as BC.  Computed by the assembler so no manual update is needed
; when fixtures are added.
ENC_FIX_PAYLOAD_LEN: equ    $ - ENC_FIX_TABLE_RAM
