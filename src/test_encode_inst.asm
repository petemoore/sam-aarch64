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
; from the Go encoder.  The operand byte streams are in the off-axis
; payload (src/test_encode_inst_payload.asm), bulk-copied into section D
; at ENC_FIX_TABLE_RAM = &E100 before enctab_map_in.  All exprs are
; PUSH_IMM constants, so no symbol table is needed — only PASS_PC (seeded
; per fixture for the PC-relative slots).
;
; Assertion helper: assert_eq32_de_hl_imm (test_assert_eq32.asm) — it
; compares DEHL against the 4 inline LE bytes following the call.
;
; The fixture data (enc_fix_table rows + operand streams) lives in a
; dedicated off-axis payload page (ENC_FIX_PAGE = 11) assembled from
; src/test_encode_inst_payload.asm.  At the start of this routine the
; payload is bulk-copied via LDIR from section-A page 11 into section-D
; RAM at ENC_FIX_TABLE_RAM (&E100).  The LDIR runs before enctab_map_in,
; so section A is still under LMPR control (not ENCTAB) during the copy.
; Because the payload is org'd at ENC_FIX_TABLE_RAM, every row's "fixture
; ptr" field already holds a section-D absolute address — the existing
; loop reads them correctly with no change to the comparison logic.
;
; Section-D safety: ENC_FIX_TABLE_RAM = &E100 = LITPOOL_PC_MAP.  The
; litpool map is only initialised and used during main_assemble (pass 1
; and pass 2).  This suite runs BEFORE main_assemble, so the region is
; free.
;
; Any mismatch -> jp fail (red border + "FAIL" banner); on all-pass the
; entry point RETs.
; -----------------------------------------------------------------------

; The fixtures are driven from a table (enc_fix_table) rather than unrolled
; per case: every case is the same {seed PASS_PC, encode_inst, compare DEHL
; to the expected word} shape, differing only in pc / fixture ptr / opcount /
; mnemonic id / expected word.  Each 11-byte row is:
;   +0 pc_lo16 (LE)    PASS_PC low 16 bits; high 16 are always 0 here
;   +2 fixture ptr     -> section-D operand byte stream
;   +4 opcount
;   +5 mnemonic id (LE)
;   +7 expected word (4 LE bytes)
; On mismatch the row pointer is recorded in LAST_FAIL_PC (as the existing
; inline-literal asserts do) and we jp fail.
ENC_FIX_ROW_LEN: equ    11

run_encode_inst_self_tests:
; -- Bulk-copy the fixture payload from page 11 into section-D RAM -----
; Map page 11 into section A (LMPR = &2B = RAM0 bit + 11), copy the
; entire payload to ENC_FIX_TABLE_RAM (&E100) via LDIR, then restore
; LMPR.  Both source (section A, LMPR-controlled) and destination
; (section D, HMPR-controlled) are simultaneously accessible.
; This copy runs BEFORE enctab_map_in — ENCTAB (page 4) is not in
; section A yet, so the swap is safe.
                ld      a, (LMPR_DEFAULT_RUNTIME)
                push    af                          ; save current LMPR
                ld      a, LMPR_ENC_FIX
                out     (250), a                   ; section A = page 11
                ld      hl, &0000                  ; source: start of page 11
                ld      de, ENC_FIX_TABLE_RAM       ; dest: &E100 in section D
                ld      bc, ENC_FIX_PAYLOAD_LEN
                ldir
                pop     af
                out     (250), a                   ; restore LMPR

                call    enctab_map_in
                call    form_lookup_init

                ld      hl, ENC_FIX_TABLE_RAM
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
; The OpLitPool dispatch case can't ride the table-driven loop above (it
; needs a pass-1-registered pool slot, not just a seeded PASS_PC), so it
; runs as a dedicated case here.  It exercises the i201c wiring:
; encode_inst -> enc_compound_scan -> enc_litpool -> litpool_encode_ldr_word.
                jp      run_encode_litpool_dispatch_test    ; tail-call; RETs

; -----------------------------------------------------------------------
; run_encode_litpool_dispatch_test — verify encode_inst dispatches
; `ldr X0, =expr` (an OpLitPool operand) through enc_litpool to the same
; word litpool_encode_ldr_word produces directly.
;
; Setup mirrors test_litpool.asm concern (6): register one X-form slot
; for the instruction at PC=0, flush at PC=24 so the slot's entry_pc=24.
; Then encode at PC=0:
;   off = 24 - 0 = 24, imm19 = 6, Rt = 0, X-form base 0x58000000
;   word = 0x58000000 | (6 << 5) | 0 = 0x580000c0   (DEHL bytes c0 00 00 58)
; Authority: pass2.go::encodeLdrLitPoolInst (pass2.go:639-672).
; -----------------------------------------------------------------------
run_encode_litpool_dispatch_test:
                call    litpool_init
                ld      a, PASS_PASS1
                ld      (PASS_MODE), a
; PASS_PC := 0 (all 4 bytes) for the instruction-pc register call.
                ld      hl, PASS_PC
                ld      b, 4
                xor     a
enc_litpool_zero_pc:
                ld      (hl), a
                inc     hl
                djnz    enc_litpool_zero_pc
; Register a width-8 (X-form) pool entry for the instruction at PASS_PC=0.
                ld      a, 8
                ld      hl, enc_litpool_disp_expr
                ld      bc, 2
                call    litpool_register
; Flush at PASS_PC=24 (already 8-aligned) -> slot 0 entry_pc = 24; flush
; leaves PASS_PC = 32, so only its low byte differs from 0.
                ld      a, 24
                ld      (PASS_PC + 0), a
                call    litpool_flush
; Encode `ldr X0, =expr` back at PASS_PC = 0 (high 3 bytes still 0).
                xor     a
                ld      (PASS_PC + 0), a
                ld      hl, enc_litpool_disp_stream
                ld      de, 5                       ; mnemonic id 5 = ldr
                ld      a, 2                        ; opcount
                call    encode_inst
                call    assert_eq32_de_hl_imm
                defb    &c0, &00, &00, &58
                ret

; 2-byte expr (PUSH_IMM8 0xaa); the value is unused in pass 1 / by the
; PC-relative encoder — only the slot's entry_pc matters.
enc_litpool_disp_expr:
                defb    &01, &aa
; Operand stream for `ldr X0, =expr`:
;   op0: RegX (&01), reg 0
;   op1: OpLitPool (&0C), width 8, expr_len 2 (LE), expr bytes
enc_litpool_disp_stream:
                defb    &01, &00
                defb    OP_KIND_LIT_POOL, &08, &02, &00, &01, &aa

enc_fix_fail:
                ld      hl, (enc_fix_row)
                ld      (LAST_FAIL_PC), hl
                jp      fail

; enc_fix_row — section-C scratch slot holding the current row pointer.
; Written at the start of each loop iteration; read on mismatch and at
; loop advance.  Stays in section C (the driver is inline here); only the
; table rows and operand stream data moved off-axis.
enc_fix_row:    defw    0
