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
; i201b memory-operand fixtures (OpMem)
                defw    &0000
                defw    enc_fix_mem_base            ; ldr x0,[x1]  MemBase
                defb    2
                defw    5
                defb    &20, &00, &40, &f9
                defw    &0000
                defw    enc_fix_mem_off8            ; ldr x2,[x3,#8]  MemBaseOff scaled
                defb    2
                defw    5
                defb    &62, &04, &40, &f9
                defw    &0000
                defw    enc_fix_mem_pre0            ; str x4,[x5,#0]!  MemBaseOffPre
                defb    2
                defw    6
                defb    &a4, &0c, &00, &f8
                defw    &0000
                defw    enc_fix_mem_post8           ; str x6,[x7],#8  MemBaseOffPost
                defb    2
                defw    6
                defb    &e6, &84, &00, &f8
                defw    &0000
                defw    enc_fix_mem_idx             ; ldr x8,[x9,x10]  MemBaseIdx
                defb    2
                defw    5
                defb    &28, &69, &6a, &f8
                defw    &0000
                defw    enc_fix_mem_lsl3            ; str x11,[x12,x13,lsl#3]  MemBaseIdxShifted
                defb    2
                defw    6
                defb    &8b, &79, &2d, &f8
                defw    &0000
                defw    enc_fix_mem_uxtw            ; ldr x14,[x15,w16,uxtw]  MemBaseIdxExtended
                defb    2
                defw    5
                defb    &ee, &49, &70, &f8
                defw    &0000
                defw    enc_fix_mem_stur_m8         ; stur x17,[x18,#-8]  unscaled
                defb    2
                defw    74
                defb    &51, &82, &1f, &f8
                defw    &0000
                defw    enc_fix_mem_ldp_off16       ; ldp x19,x20,[x21,#16]  pair MemBaseOff
                defb    3
                defw    7
                defb    &b3, &52, &41, &a9
                defw    &0000
                defw    enc_fix_mem_str_w           ; str w22,[x23,#4]  W-register
                defb    2
                defw    6
                defb    &f6, &06, &00, &b9
; i204 bare Imm16 SlotKind (0x08) coverage: udf #0x1234 -> imm16 @0
                defw    &0000
                defw    enc_fix_udf
                defb    1
                defw    89
                defb    &34, &12, &00, &00
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

; i201b memory-operand fixture operand streams (exact Go OperandWriter bytes)
; ldr x0,[x1]: Rt=x0, MemBase(base=1)
enc_fix_mem_base:       defb    &01, &00, &08, &00, &01
; ldr x2,[x3,#8]: Rt=x2, MemBaseOff(base=3, off=8)
enc_fix_mem_off8:       defb    &01, &02, &08, &01, &03, &02, &00, &01, &08
; str x4,[x5,#0]!: Rt=x4, MemBaseOffPre(base=5, off=0)
enc_fix_mem_pre0:       defb    &01, &04, &08, &02, &05, &02, &00, &01, &00
; str x6,[x7],#8: Rt=x6, MemBaseOffPost(base=7, off=8)
enc_fix_mem_post8:      defb    &01, &06, &08, &03, &07, &02, &00, &01, &08
; ldr x8,[x9,x10]: Rt=x8, MemBaseIdx(base=9,idx=10,idxW=1)
enc_fix_mem_idx:        defb    &01, &08, &08, &04, &09, &0a, &01
; str x11,[x12,x13,lsl#3]: Rt=x11, MemBaseIdxShifted(base=12,idx=13,idxW=1,shiftAmt=3)
enc_fix_mem_lsl3:       defb    &01, &0b, &08, &05, &0c, &0d, &01, &03
; ldr x14,[x15,w16,uxtw]: Rt=x14, MemBaseIdxExtended(base=15,idx=16,idxW=0,extend=2,shiftAmt=0)
enc_fix_mem_uxtw:       defb    &01, &0e, &08, &06, &0f, &10, &00, &02, &00
; stur x17,[x18,#-8]: Rt=x17, MemBaseOff(base=18, off=-8)
enc_fix_mem_stur_m8:    defb    &01, &11, &08, &01, &12, &02, &00, &01, &f8
; ldp x19,x20,[x21,#16]: Rt1=x19, Rt2=x20, MemBaseOff(base=21, off=16)
enc_fix_mem_ldp_off16:  defb    &01, &13, &01, &14, &08, &01, &15, &02, &00, &01, &10
; str w22,[x23,#4]: Rt=w22, MemBaseOff(base=23, off=4)
enc_fix_mem_str_w:      defb    &02, &16, &08, &01, &17, &02, &00, &01, &04

; i204 Imm16 fixture: udf #0x1234 (exact Go OperandWriter bytes)
; op0: IMM_EXPR (&05), len 3, PUSH_IMM16 (&02), 0x1234 LE
enc_fix_udf:            defb    &05, &03, &00, &02, &34, &12

; =======================================================================
; i204b overlay_classify / compact_inst fixtures (test_overlay_classify.asm)
;
; Every row mirrors one case in tools/sam-aarch64/assemble/
; overlay_classify_fixtures_test.go::TestOverlayClassifyFixtures — that Go
; test is the authority (CLAUDE.md §6) and the drift guard: it re-derives
; each expected slot / flag / base word from overlayClassify + compactInst
; and logs them (go test -run TestOverlayClassifyFixtures -v); the values
; below are those logged bytes.
;
; PLACEMENT: this block sits at the payload tail, past offset &280.  The
; first &180 bytes of the section-D copy (&E100-&E27F) are LITPOOL_PC_MAP,
; which run_encode_litpool_dispatch_test (the tail of
; run_encode_inst_self_tests) overwrites via litpool_init BEFORE the
; overlay suite runs — data below offset &280 would be clobbered.
; =======================================================================

; -- compact_inst fixture table (12-byte rows; see TOC_CI_ROW_LEN) -------
;   +0 pc_lo16 (LE)   PASS_PC seed (high 16 bits := 0)
;   +2 fixture ptr    -> operand stream (0 = end of table)
;   +4 opcount
;   +5 mnemonic id (LE)
;   +7 expected ovl_is_literal (1 = bare literal, 0 = overlay)
;   +8 expected base word (4 LE bytes)
toc_ci_table:
; lit_nop: literal
                defw    &0000
                defw    toc_op_nop
                defb    0
                defw    0
                defb    1
                defb    &1f, &20, &03, &d5
; lit_add_x0_x1_5: literal
                defw    &0000
                defw    toc_op_add5
                defb    3
                defw    1
                defb    1
                defb    &20, &14, &00, &91
; sym_b_const (b 0x1010 @0x1000): PC-variant -> overlay, Branch26 zeroed
                defw    &1000
                defw    toc_op_b
                defb    1
                defw    9
                defb    0
                defb    &00, &00, &00, &14
; b_branch26 (same fixture, the generic-slot PC-dependent case)
                defw    &1000
                defw    toc_op_b
                defb    1
                defw    9
                defb    0
                defb    &00, &00, &00, &14
; cbz_branch19 (cbz x0,0x1008 @0x1000)
                defw    &1000
                defw    toc_op_cbz
                defb    2
                defw    20
                defb    0
                defb    &00, &00, &00, &b4
; adrp_slot (adrp x0,0x3000 @0x1000)
                defw    &1000
                defw    toc_op_adrp
                defb    2
                defw    13
                defb    0
                defb    &00, &00, &00, &90
; adr_slot (adr x0,0x1004 @0x1000)
                defw    &1000
                defw    toc_op_adr
                defb    2
                defw    48
                defb    0
                defb    &00, &00, &00, &10
; ldr_lit_branch19 (ldr x0,0x1008 @0x1000)
                defw    &1000
                defw    toc_op_ldrlit
                defb    2
                defw    5
                defb    0
                defb    &00, &00, &00, &58
; sentinel
                defw    &0000
                defw    0
                defb    0
                defw    0
                defb    0
                defb    0, 0, 0, 0

; -- overlay_classify fixture table (10-byte rows; TOC_OC_ROW_LEN) -------
;   +0 fixture ptr    -> operand stream (0 = end of table)
;   +2 opcount
;   +3 pc_lo16 (LE)   PASS_PC seed
;   +5 mnemonic id (LE)
;   +7 expected slot
;   +8 expected is_litpool
;   +9 expected litwidth
toc_oc_table:
; add_lo12: slot 6 (FoldAddSubImm12)
                defw    toc_op_add_lo12
                defb    3
                defw    &0000
                defw    1
                defb    6, 0, 0
; ldr_memimm12: slot 7 (FoldMemImm12)
                defw    toc_op_ldr_mem
                defb    2
                defw    &0000
                defw    5
                defb    7, 0, 0
; stur_memimm9: slot 8 (FoldMemImm9)
                defw    toc_op_stur_mem
                defb    2
                defw    &0000
                defw    74
                defb    8, 0, 0
; ldp_pairimm7: slot 11 (FoldPairImm7)
                defw    toc_op_ldp_mem
                defb    3
                defw    &0000
                defw    7
                defb    11, 0, 0
; mov_movz: slot 13 (FoldMovzAuto)
                defw    toc_op_mov_movz
                defb    2
                defw    &0000
                defw    3
                defb    13, 0, 0
; mov_logical: slot 10 (FoldLogical)
                defw    toc_op_mov_log
                defb    2
                defw    &0000
                defw    3
                defb    10, 0, 0
; litpool: slot 12 (FoldLitpool19), is_litpool=1, width 8, rt 0
                defw    toc_op_litpool
                defb    2
                defw    &0000
                defw    5
                defb    12, 1, 8
; sentinel
                defw    0
                defb    0
                defw    0
                defw    0
                defb    0, 0, 0

; -- i204b operand streams (exact Go OperandWriter bytes) ----------------
toc_op_nop:                                         ; (no operands)
toc_op_add5:    defb    &03, &00, &03, &01, &05, &02, &00, &01, &05
toc_op_b:       defb    &05, &03, &00, &02, &10, &10
toc_op_cbz:     defb    &01, &00, &05, &03, &00, &02, &08, &10
toc_op_adrp:    defb    &01, &00, &05, &03, &00, &02, &00, &30
toc_op_adr:     defb    &01, &00, &05, &03, &00, &02, &04, &10
toc_op_ldrlit:  defb    &01, &00, &05, &03, &00, &02, &08, &10
; add x0,x1,:lo12:sym (PUSH_SYM id 0, REL_LO12)
toc_op_add_lo12: defb   &03, &00, &03, &01, &05, &04, &00, &05, &00, &00, &30
; ldr x0,[x1,#sym]  (MemBaseOff, expr = PUSH_SYM id 0)
toc_op_ldr_mem: defb    &01, &00, &08, &01, &01, &03, &00, &05, &00, &00
; stur x0,[x1,#sym]
toc_op_stur_mem: defb   &01, &00, &08, &01, &01, &03, &00, &05, &00, &00
; ldp x0,x1,[x2,#sym]
toc_op_ldp_mem: defb    &01, &00, &01, &01, &08, &01, &02, &03, &00, &05, &00, &00
; mov x0,#0x12340000  (movz autoselect)
toc_op_mov_movz: defb   &01, &00, &05, &05, &00, &03, &00, &00, &34, &12
; mov x2,#0x5555555555555555  (logical)
toc_op_mov_log: defb    &01, &02, &05, &09, &00, &04, &55, &55, &55, &55, &55, &55, &55, &55
; ldr x0,=sym  (litpool width 8, expr PUSH_SYM id 0)
toc_op_litpool: defb    &01, &00, &0c, &08, &03, &00, &05, &00, &00
; lsl x0,x1,#sym  (loud gap)
toc_op_loud_lsl: defb   &01, &00, &01, &01, &05, &03, &00, &05, &00, &00

; =======================================================================
; i48c-b8e compact-adapter fixture (test_compact_adapter.asm)
;
; cadapt_ir is a serialized IR record stream (the parse_run wire layout);
; cadapt_expected is the compact record stream assemble.Compact produces
; for it.  BOTH byte arrays are derived by — and drift-guarded against —
; tools/netboot-oracle/z80/compact_adapter_fixture_test.go
; ::TestCompactAdapterFixture (the Go authority, CLAUDE.md §6): that test
; bakes the same bytes and fails on any divergence, printing the defb
; re-bake dump.  The record mix and what it exercises are documented on
; buildCompactAdapterFixture there.
;
; PLACEMENT: like the toc_* block above, this sits past the &E100-&E27F
; LITPOOL_PC_MAP clobber zone.  The payload tail now reaches into the
; &E800+ SYMTAB_OVERFLOW region — safe by the same boot-phase ordering:
; nothing writes the symbol tables before main_assemble, and every suite
; reading this copy has completed by then.
; =======================================================================
cadapt_ir_len:  defw    cadapt_ir_end - cadapt_ir
cadapt_ir:
                defb    &04, &08, &00, &0c, &01, &05, &03, &00, &02, &00, &10, &05
                defb    &06, &00, &00, &20, &68, &65, &61, &64, &01, &00, &00, &00
                defb    &00, &00, &01, &01, &00, &03, &09, &00, &03, &00, &03, &01
                defb    &05, &02, &00, &01, &05, &01, &09, &00, &01, &06, &00, &05
                defb    &03, &00, &02, &20, &10, &01, &00, &00, &00, &00, &00, &05
                defb    &05, &00, &01, &20, &6d, &69, &64, &01, &14, &00, &02, &08
                defb    &00, &01, &00, &05, &03, &00, &02, &18, &10, &01, &05, &00
                defb    &02, &0f, &00, &01, &01, &0c, &08, &09, &00, &04, &f0, &de
                defb    &bc, &9a, &78, &56, &34, &12, &01, &00, &00, &00, &00, &00
                defb    &04, &11, &00, &04, &03, &05, &02, &00, &01, &01, &05, &02
                defb    &00, &01, &02, &05, &02, &00, &01, &03, &04, &07, &00, &04
                defb    &01, &05, &02, &00, &01, &04, &05, &06, &00, &00, &20, &64
                defb    &61, &74, &61, &04, &0e, &00, &02, &02, &05, &03, &00, &02
                defb    &aa, &00, &05, &03, &00, &02, &bb, &00, &01, &00, &00, &00
                defb    &00, &00
cadapt_ir_end:

cadapt_expected_len: defw   cadapt_expected_end - cadapt_expected
cadapt_expected:
                defb    &04, &08, &00, &0c, &01, &05, &03, &00, &02, &00, &10, &09
                defb    &09, &00, &00, &1f, &20, &03, &d5, &20, &14, &00, &91, &09
                defb    &2a, &00, &01, &00, &00, &00, &14, &01, &01, &03, &02, &20
                defb    &10, &1f, &20, &03, &d5, &00, &00, &00, &00, &b4, &01, &02
                defb    &03, &02, &18, &10, &01, &00, &00, &58, &01, &0c, &09, &04
                defb    &f0, &de, &bc, &9a, &78, &56, &34, &12, &09, &05, &00, &00
                defb    &1f, &20, &03, &d5, &08, &11, &00, &04, &01, &00, &00, &00
                defb    &02, &00, &00, &00, &03, &00, &00, &00, &04, &00, &00, &00
                defb    &08, &03, &00, &02, &aa, &bb, &09, &05, &00, &00, &1f, &20
                defb    &03, &d5
cadapt_expected_end:

; Total payload size: the LDIR in run_encode_inst_self_tests uses this
; count as BC.  Computed by the assembler so no manual update is needed
; when fixtures are added.
ENC_FIX_PAYLOAD_LEN: equ    $ - ENC_FIX_TABLE_RAM
