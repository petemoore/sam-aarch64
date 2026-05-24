; intercepts.asm — mnemonic-ID intercepts ahead of form-table lookup.
;
; Mac-side reference: tools/refenc/pass2.go's encodeInst dispatches a
; handful of mnemonics via per-ID intercepts BEFORE the generic form-table
; ValidateOperandKinds path (pass2.go:296-324 + the shifted/extended
; register coercion at pass2.go:208-252).  M5 PR-B intercepts:
;
;   * Task 4: ror Rd, Rs, #imm    — EXTR alias (pass2.go:1460-1492).
;   * Task 5: OpShiftedReg         — add/sub/and/orr/eor/subs/tst/bic/ands
;                                    (pass2.go:1005-1067) + the 3-reg
;                                    auto-coercion (pass2.go:214-252).
;   * Task 6: OpExtendedReg        — add/sub (pass2.go:1131-1179).
;
; Calling convention:
;   try_mnemonic_intercept is CALLed from main_handle_inst_done before
;   form lookup.  On match it computes 32 bits, emits 4 LE bytes, bumps
;   PASS_PC by 4, then RETurns with Z=1 (handled).  On no-match it
;   RETurns with Z=0 (caller falls through to the generic form-table).
;
; Reads: (main_mnemonic_id) (u16), (main_op_count), OPVAL_KINDS,
;        OPVAL_ARRAY (only kind bytes for dispatch).
;
; ---------------------------------------------------------------------
try_mnemonic_intercept:
                ld      hl, (main_mnemonic_id)
                ld      a, h
                or      a
                jp      nz, try_intercept_no_match
                ld      a, l

; -- ror (ID 70) — EXTR alias when operand 2 is OpImmExpr -------------
                cp      70
                jp      nz, try_intercept_post_ror
                ld      a, (main_op_count)
                cp      3
                jp      nz, try_intercept_no_match
                ld      a, (OPVAL_KINDS + 2)
                cp      OP_KIND_IMM_EXPR
                jp      nz, try_intercept_no_match
                call    encode_ror_imm_word
                call    intercept_emit_dehl
                xor     a                   ; Z=1 → caller skips form lookup
                ret

try_intercept_post_ror:

try_intercept_no_match:
                or      &ff                 ; A=0xFF → Z=0
                ret


; -----------------------------------------------------------------------
; intercept_emit_dehl — emit DEHL as 4 LE bytes, advance PASS_PC by 4.
;
; Input:  HL = bits 0..15, DE = bits 16..31.
; Output: 4 bytes appended to OUT, PASS_PC += 4.
;
; Note: emit_byte clobbers HL; we stash the encoded word on the stack so
; subsequent bytes still come from the right source.
; -----------------------------------------------------------------------
intercept_emit_dehl:
                push    de                  ; save DE on stack
                push    hl                  ; save HL on stack
                ld      a, l
                call    emit_byte
                pop     hl                  ; restore HL
                ld      a, h
                call    emit_byte
                pop     de                  ; restore DE
                push    de
                ld      a, e
                call    emit_byte
                pop     de
                ld      a, d
                call    emit_byte
                jp      pass_pc_advance_4   ; tail-call (returns Z mangled)


; -----------------------------------------------------------------------
; intercept_ror_imm — port of tools/refenc/pass2.go:1460-1492.
;
;   ror Rd, Rs, #shift  (EXTR Rd, Rs, Rs, #shift  =  ROR Rd, Rs, #shift)
;     sf := operand0 is X
;     base := sf ? 0x93c00000 : 0x13800000
;     op := base | (Rs<<16) | (imm6<<10) | (Rs<<5) | Rd
;     range: 0 <= shift < 32 for W, 0 <= shift < 64 for X.
;
; OPVAL_ARRAY[0] = Rd  (X or W)
; OPVAL_ARRAY[1] = Rs  (matching width)
; OPVAL_ARRAY[2] = IMM_EXPR with 8-byte LE result at +2..+9
;
; try_mnemonic_intercept calls encode_ror_imm_word, then
; intercept_emit_dehl, then RETs Z=1.  The pure word-computation lives
; in encode_ror_imm_word so it can be Layer-1 unit-tested without
; touching OUT_PC.
; -----------------------------------------------------------------------

; -----------------------------------------------------------------------
; encode_ror_imm_word — pure word computation for ROR-imm.
;
; Reads OPVAL_ARRAY (operand layout: kind/reg per the M3 parser).
; Output: DE:HL = encoded 32-bit word (HL = bits 0..15, DE = bits 16..31).
; Errors: jp fail on width mismatch / shift overflow / non-u8 shift.
; -----------------------------------------------------------------------
encode_ror_imm_word:
; -- Width from operand 0 kind (X = 0x01, W = 0x02).
                ld      a, (OPVAL_ARRAY + 0)
                cp      OP_KIND_REG_X
                jp      z, encode_ror_imm_x64
                cp      OP_KIND_REG_W
                jp      nz, fail
; 32-bit ROR: base = 0x13800000.  bits 16..31 = 0x1380.
                ld      hl, &0000
                ld      de, &1380
                ld      a, 32               ; max shift bound
                jp      encode_ror_imm_pack

encode_ror_imm_x64:
; 64-bit ROR: base = 0x93c00000.  bits 16..31 = 0x93c0.
                ld      hl, &0000
                ld      de, &93c0
                ld      a, 64

encode_ror_imm_pack:
; A = regsize bound (32 or 64).  HL:DE = base packed (HL=low16, DE=high16).
                ld      c, a
; -- Read shift's low byte from OPVAL_ARRAY[2] at +2 (start of LE result).
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2)
                cp      c
                jp      nc, fail            ; shift >= regsize → out of range
                ld      b, a                ; B = shift (imm6)
; Reject any high byte non-zero — the shift must be a small u8.
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 4)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 5)
                or      a
                jp      nz, fail

; -- Read Rd and Rs.
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                ld      c, a                ; C = Rd (5 bits)
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_ror_imm_rs), a

; -- OR Rd into bits 4:0 (low 5 of HL low byte).
                ld      a, l
                or      c
                ld      l, a

; -- OR Rs into bits 9:5.  Rs (5 bits) << 5 = 0..0x3e0.  Low 3 bits of
;    Rs land in HL low byte bits 5..7; high 2 bits of Rs land in HL
;    high byte bits 0..1.
                ld      a, (encode_ror_imm_rs)
                rrca
                rrca
                rrca                        ; A bits 0..1 = Rs>>3; A bits 5..7 = (Rs&7)
                ld      c, a
                and     &e0                 ; A = (Rs&7) << 5 → bits 5..7
                or      l
                ld      l, a
                ld      a, c
                and     &03                 ; A = Rs >> 3 → bits 8..9 (HL high byte bits 0..1)
                or      h
                ld      h, a

; -- OR imm6 (B, 0..63) into bits 15:10.  imm6 occupies bits 10..15
;    of the word i.e. HL high byte bits 2..7 = imm6 << 2.
                ld      a, b
                add     a, a
                add     a, a                ; A = imm6 << 2 (fits in u8 since imm6 < 64)
                or      h
                ld      h, a

; -- OR Rs into bits 20:16 (DE low byte bits 0..4).
                ld      a, (encode_ror_imm_rs)
                or      e
                ld      e, a
                ret


encode_ror_imm_rs:               defb    0
