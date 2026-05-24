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
                ld      (try_intercept_mnem), a

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

; -- Shifted-register-capable mnemonics: add/sub/and/orr/eor/subs/tst/
;    bic/ands.  Two routings:
;      (a) operand 2 (or 1 for tst) is OpShiftedReg — direct dispatch.
;      (b) all-plain-GPR — coerce to ShiftedReg with LSL #0 in-place
;          and dispatch.
;    See tools/refenc/pass2.go:208-252.
                ld      a, (try_intercept_mnem)
                call    is_shifted_reg_mnemonic
                jp      nz, try_intercept_post_shift

                ld      a, (try_intercept_mnem)
                cp      46                  ; tst → 2-operand path
                jp      z, try_intercept_tst

; 3-operand mnemonics: must have op_count=3, op2 = ShiftedReg or plain
                ld      a, (main_op_count)
                cp      3
                jp      nz, try_intercept_post_shift
                ld      a, (OPVAL_KINDS + 2)
                cp      OP_KIND_SHIFTED_REG
                jp      z, try_intercept_shifted_dispatch
; 3-plain-GPR coerce?
                call    operands012_all_plain_gpr
                jp      nz, try_intercept_post_shift
                ld      a, 2
                call    coerce_op_to_lsl0
                jp      try_intercept_shifted_dispatch

try_intercept_tst:
                ld      a, (main_op_count)
                cp      2
                jp      nz, try_intercept_post_shift
                ld      a, (OPVAL_KINDS + 1)
                cp      OP_KIND_SHIFTED_REG
                jp      z, try_intercept_shifted_dispatch
; 2-plain-GPR tst coerce?
                ld      a, (OPVAL_KINDS + 0)
                call    is_plain_gpr_kind
                jp      nz, try_intercept_post_shift
                ld      a, (OPVAL_KINDS + 1)
                call    is_plain_gpr_kind
                jp      nz, try_intercept_post_shift
                ld      a, 1
                call    coerce_op_to_lsl0
                ; fall through

try_intercept_shifted_dispatch:
                ld      a, (try_intercept_mnem)
                call    encode_shifted_reg_word
                call    intercept_emit_dehl
                xor     a
                ret

try_intercept_post_shift:

try_intercept_no_match:
                or      &ff                 ; A=0xFF → Z=0
                ret


; -----------------------------------------------------------------------
; is_plain_gpr_kind — A = operand kind byte.
;   Z=1 if kind ∈ {RegX, RegW, RegX-SP, RegW-SP}.
;   Mirrors tools/refenc/pass2.go:1083-1089 (isPlainGPR).
; -----------------------------------------------------------------------
is_plain_gpr_kind:
                cp      OP_KIND_REG_X
                ret     z
                cp      OP_KIND_REG_W
                ret     z
                cp      OP_KIND_REG_XSP
                ret     z
                cp      OP_KIND_REG_WSP
                ret


; -----------------------------------------------------------------------
; operands012_all_plain_gpr — Z=1 if OPVAL_KINDS[0..2] are all plain GPRs.
; -----------------------------------------------------------------------
operands012_all_plain_gpr:
                ld      a, (OPVAL_KINDS + 0)
                call    is_plain_gpr_kind
                ret     nz
                ld      a, (OPVAL_KINDS + 1)
                call    is_plain_gpr_kind
                ret     nz
                ld      a, (OPVAL_KINDS + 2)
                jp      is_plain_gpr_kind


; -----------------------------------------------------------------------
; is_shifted_reg_mnemonic — A = mnemonic_id (low byte).
;   Z=1 if id ∈ {1, 2, 14, 15, 16, 45, 46, 47, 80}.
;   Mirrors tools/refenc/pass2.go:1072-1078.
;   Preserves A.
; -----------------------------------------------------------------------
is_shifted_reg_mnemonic:
                cp      1
                ret     z
                cp      2
                ret     z
                cp      14
                ret     z
                cp      15
                ret     z
                cp      16
                ret     z
                cp      45
                ret     z
                cp      46
                ret     z
                cp      47
                ret     z
                cp      80
                ret


; -----------------------------------------------------------------------
; coerce_op_to_lsl0 — rewrite OPVAL_ARRAY[idx] in-place from a plain
; GPR record into an OpShiftedReg record with LSL #0.  Mirrors the
; synthesis in tools/refenc/pass2.go:217-251.
;
; Input:  A = idx (1 for tst, 2 for 3-op).
;
; Width is taken from the GPR's own kind for the 3-op case, but from
; operand 0 for the tst (idx=1) case (mirrors pass2.go:241).
;
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
coerce_op_to_lsl0:
                ld      (coerce_idx), a

; Compute idx*STRIDE in C.  STRIDE=10 = 8+2; idx ∈ {1,2}.
                add     a, a                ; A = 2*idx
                ld      c, a                ; C = 2*idx
                add     a, a
                add     a, a                ; A = 8*idx
                add     a, c                ; A = 8*idx + 2*idx = 10*idx
                ld      c, a
                ld      b, 0
                ld      hl, OPVAL_ARRAY
                add     hl, bc              ; HL → OPVAL[idx*STRIDE]

; Save the original kind + reg before overwriting.
                ld      a, (hl)
                ld      c, a                ; C = original kind
                inc     hl
                ld      b, (hl)             ; B = reg
                dec     hl

; Width derivation.  For tst (idx==1), width follows OPVAL[0]; for the
; 3-op coerce (idx==2), width follows operand 2's own kind.
                ld      a, (coerce_idx)
                cp      1
                jr      nz, coerce_kind_self
                ld      a, (OPVAL_ARRAY + 0)
                jr      coerce_kind_done
coerce_kind_self:
                ld      a, c
coerce_kind_done:
                cp      OP_KIND_REG_X
                jr      z, coerce_w1
                cp      OP_KIND_REG_XSP
                jr      z, coerce_w1
                xor     a
                jr      coerce_w_done
coerce_w1:      ld      a, 1
coerce_w_done:
                ld      c, a                ; C = width

; Write the new layout: +0=0x06, +1=width, +2=reg, +3..+9=0.
                ld      a, OP_KIND_SHIFTED_REG
                ld      (hl), a
                inc     hl
                ld      (hl), c
                inc     hl
                ld      (hl), b
                inc     hl
                xor     a
                ld      b, 7
coerce_zero_tail:
                ld      (hl), a
                inc     hl
                djnz    coerce_zero_tail

; Update OPVAL_KINDS[idx] = SHIFTED_REG.
                ld      a, (coerce_idx)
                ld      hl, OPVAL_KINDS
                ld      c, a
                ld      b, 0
                add     hl, bc
                ld      (hl), OP_KIND_SHIFTED_REG
                ret


try_intercept_mnem:             defb    0
coerce_idx:                     defb    0


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
