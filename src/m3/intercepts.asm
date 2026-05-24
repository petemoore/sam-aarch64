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

; 3-operand mnemonics: op_count=3 required.  Then operand 2 selects the
; encoder: ShiftedReg → shifted-reg encoder; ExtendedReg (add/sub only)
; → extended-reg encoder; 3 plain GPRs → coerce to LSL #0 then encode.
                ld      a, (main_op_count)
                cp      3
                jp      nz, try_intercept_post_shift
                ld      a, (OPVAL_KINDS + 2)
                cp      OP_KIND_SHIFTED_REG
                jp      z, try_intercept_shifted_dispatch
                cp      OP_KIND_EXTENDED_REG
                jp      z, try_intercept_extended_dispatch
; 3-plain-GPR coerce?
                call    operands012_all_plain_gpr
                jp      nz, try_intercept_post_shift
                ld      a, 2
                call    coerce_op_to_lsl0
                jp      try_intercept_shifted_dispatch

try_intercept_extended_dispatch:
; ExtendedReg only valid for add (1) / sub (2).  encode_extended_reg_word
; already enforces this, but reject up-front anything else so we fall
; through to form lookup (which will then fail cleanly).
                ld      a, (try_intercept_mnem)
                cp      1
                jr      z, try_intercept_ext_call
                cp      2
                jp      nz, try_intercept_post_shift
try_intercept_ext_call:
                call    encode_extended_reg_word
                call    intercept_emit_dehl
                xor     a
                ret

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

; -- Memory mnemonics (M5 PR-C Tasks 7-10) ----------------------------
; Eleven mnemonics share the OpMem encoder family:
;   ldr=5    str=6    ldp=7    stp=8
;   ldrb=54  strb=55  ldrh=56  strh=57
;   stur=74  ldur=75
;   ldrsb=85 ldrsh=86 ldrsw=87
;
; Dispatch model:
;   - LDP / STP (ID 7 / 8) have 3 operands; operand 2 is OpMem.
;     Routed to encode_pair_word (M5 PR-C Task 9).
;   - All others have 2 operands; operand 1 is OpMem.  Routed to
;     encode_mem_word.
                ld      a, (try_intercept_mnem)
                call    is_mem_mnemonic_id
                jp      nz, try_intercept_post_mem

; Pair (ldp=7, stp=8): require 3 operands, kind[2] == OpMem.
                cp      7
                jp      z, try_intercept_pair_check
                cp      8
                jp      z, try_intercept_pair_check

; Non-pair: require >= 2 operands and kind[1] == OpMem.  If the
; mnemonic IS a mem-family ID but the operand-1 kind isn't OpMem,
; fall through to the post-mem stage (M5 PR-E adds the OpLitPool
; intercept there for `ldr Xn|Wn, =<expr>`).
                ld      a, (main_op_count)
                cp      2
                jp      c, try_intercept_post_mem
                ld      a, (OPVAL_KINDS + 1)
                cp      OP_KIND_MEM
                jp      nz, try_intercept_post_mem
                ld      a, (try_intercept_mnem)
                call    encode_mem_word
                call    intercept_emit_dehl
                xor     a
                ret

try_intercept_pair_check:
                ld      a, (main_op_count)
                cp      3
                jp      nz, try_intercept_no_match
                ld      a, (OPVAL_KINDS + 2)
                cp      OP_KIND_MEM
                jp      nz, try_intercept_no_match
                ld      a, (try_intercept_mnem)
                call    encode_pair_word
                call    intercept_emit_dehl
                xor     a
                ret

try_intercept_post_mem:

; -- ldr Xn|Wn, =<expr> (M5 PR-E Task 13) -----------------------------
; mnemonic 5 (ldr) with operand 1 = OpLitPool routes to the LDR-literal
; encoder.  The Mac-side dispatch is also handled inline in
; encodeInst (refenc/pass2.go:283-289 → encodeLdrLitPoolInst).
;
; Pass 1 already registered the pool slot keyed by PASS_PC; the encoder
; looks up the slot's entry_pc to compute imm19.  No form-table entry
; exists for OpLitPool, so falling through here would always fail.
                ld      a, (try_intercept_mnem)
                cp      5
                jp      nz, try_intercept_post_litpool
                ld      a, (main_op_count)
                cp      2
                jp      nz, try_intercept_post_litpool
                ld      a, (OPVAL_KINDS + 1)
                cp      OP_KIND_LIT_POOL
                jp      nz, try_intercept_post_litpool
                call    litpool_encode_ldr_word
                call    intercept_emit_dehl
                xor     a
                ret

try_intercept_post_litpool:

; -- ldr Xt|Wt, <label> — LDR (literal) direct-label form -------------
; mnemonic 5 (ldr) with operand 1 = OpImmExpr (NOT OpLitPool, NOT OpMem).
; The label IS the PC-relative target; imm19 = (target - PASS_PC)/4.
; The Mac-side dispatch is refenc/pass2.go:281 (encodeLdrLitDirect).  No
; form-table entry exists for {RegX/RegW, ImmExpr}, so a miss here would
; fall through to form lookup → FAIL40.  Grounded against
; aarch64-none-elf-as + ARM ARM C6.2.131 (LDR literal).
                ld      a, (try_intercept_mnem)
                cp      5
                jp      nz, try_intercept_post_ldrlit
                ld      a, (main_op_count)
                cp      2
                jp      nz, try_intercept_post_ldrlit
                ld      a, (OPVAL_KINDS + 1)
                cp      OP_KIND_IMM_EXPR
                jp      nz, try_intercept_post_ldrlit
                call    encode_ldr_lit_direct_word
                call    intercept_emit_dehl
                xor     a
                ret
try_intercept_post_ldrlit:

; -- lsl / lsr (mnemonics 17 / 18) ------------------------------------
; Both forms (3 operands): operand 2 = OpImmExpr → UBFM-alias immediate
; shift; operand 2 = OpRegX/OpRegW → LSLV/LSRV register shift.  The
; Mac-side dispatch is refenc/pass2.go:300 (case 17,18 → encodeLSLSR),
; ahead of the form table.  No form-table entry exists, so a miss here
; would fall through to FAIL40.  Grounded against aarch64-none-elf-as +
; ARM ARM C6.2.218/.222 (LSL/LSR imm) and C6.2.219/.223 (LSLV/LSRV).
                ld      a, (try_intercept_mnem)
                cp      17
                jr      z, try_intercept_lslsr
                cp      18
                jp      nz, try_intercept_post_lslsr
try_intercept_lslsr:
                ld      a, (main_op_count)
                cp      3
                jp      nz, try_intercept_post_lslsr
                ld      a, (try_intercept_mnem)
                call    encode_lslsr_word
                call    intercept_emit_dehl
                xor     a
                ret
try_intercept_post_lslsr:

; -- Barrier mnemonics: isb=66 / dsb=67 / dmb=68 ----------------------
; text2bin converts the barrier-arg keyword (sy, ish, ishst, ...) into a
; single OpImmExpr carrying the CRm field (bits 11:8); isb with no arg
; defaults to sy (CRm=15).  The Mac-side dispatch is refenc/pass2.go:310
; (case 66,67,68 → encodeBarrierInst).  No form-table entry exists for
; these, so a miss here would always fall through to fail anyway —
; unconditional intercept, like the sysname block below.
                ld      a, (try_intercept_mnem)
                cp      66
                jp      z, try_intercept_barrier
                cp      67
                jp      z, try_intercept_barrier
                cp      68
                jp      z, try_intercept_barrier
                jp      try_intercept_post_barrier
try_intercept_barrier:
                ld      a, (try_intercept_mnem)
                call    encode_barrier_word
                call    intercept_emit_dehl
                xor     a
                ret
try_intercept_post_barrier:

; -- OpSysName mnemonics (M5 PR-D Task 11) ----------------------------
;   mrs  = 76    mrs Xt, <sysreg>
;   msr  = 77    msr <sysreg>, Xt  OR  msr <pstate>, #imm
;   dc   = 78    dc <op>, Xt
;   tlbi = 79    tlbi <op>[, Xt]
;
; Each routes to its own encoder in sysname.asm.  These are unconditional
; intercepts: the four mnemonics have no form-table entries, so a miss
; here would always fall through to form_lookup_match → fail anyway.
                ld      a, (try_intercept_mnem)
                cp      76
                jp      z, try_intercept_mrs
                cp      77
                jp      z, try_intercept_msr
                cp      78
                jp      z, try_intercept_dc
                cp      79
                jp      z, try_intercept_tlbi
                jp      try_intercept_no_match

try_intercept_mrs:
                call    encode_mrs_word
                call    intercept_emit_dehl
                xor     a
                ret

try_intercept_msr:
                call    encode_msr_word
                call    intercept_emit_dehl
                xor     a
                ret

try_intercept_dc:
                call    encode_dc_word
                call    intercept_emit_dehl
                xor     a
                ret

try_intercept_tlbi:
                call    encode_tlbi_word
                call    intercept_emit_dehl
                xor     a
                ret


try_intercept_no_match:
                or      &ff                 ; A=0xFF → Z=0
                ret


; -----------------------------------------------------------------------
; is_mem_mnemonic_id — Z=1 if A is one of the eleven memory mnemonics.
;   ldr=5 / str=6 / ldp=7 / stp=8 /
;   ldrb=54 / strb=55 / ldrh=56 / strh=57 /
;   stur=74 / ldur=75 /
;   ldrsb=85 / ldrsh=86 / ldrsw=87.
; Preserves A.
; -----------------------------------------------------------------------
is_mem_mnemonic_id:
                cp      5
                ret     z
                cp      6
                ret     z
                cp      7
                ret     z
                cp      8
                ret     z
                cp      54
                ret     z
                cp      55
                ret     z
                cp      56
                ret     z
                cp      57
                ret     z
                cp      74
                ret     z
                cp      75
                ret     z
                cp      85
                ret     z
                cp      86
                ret     z
                cp      87
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


; -----------------------------------------------------------------------
; encode_barrier_word — pure word computation for isb / dsb / dmb.
;
; Port of tools/refenc/pass2.go:encodeBarrierInst (refenc/pass2.go:1506).
;   word := base | (CRm << 8)
;     isb (66): base 0xd50330df
;     dsb (67): base 0xd503309f
;     dmb (68): base 0xd50330bf
; Grounded against aarch64-none-elf-as + ARM ARM C6.2.99 (ISB) /
; C6.2.74 (DSB) / C6.2.73 (DMB).
;
; In:  A = mnemonic_id (66/67/68).  Single operand at OPVAL_ARRAY[0] is an
;      OpImmExpr; CRm value (0..15) is the low byte of its 8-byte LE
;      result at OPVAL_ARRAY + 2.
; Out: DE:HL = encoded 32-bit word (HL = bits 0..15, DE = bits 16..31).
;      DE is constant 0xd503 for all three; HL = (base_low16) | (CRm<<8).
; Errors: jp fail on CRm > 15 or non-zero high bytes (not a small u8).
; -----------------------------------------------------------------------
encode_barrier_word:
                cp      66
                jp      z, encode_barrier_isb
                cp      67
                jp      z, encode_barrier_dsb
                cp      68
                jp      z, encode_barrier_dmb
                jp      fail                ; unreachable (intercept gates ids)
encode_barrier_isb:
                ld      hl, &30df
                jp      encode_barrier_pack
encode_barrier_dsb:
                ld      hl, &309f
                jp      encode_barrier_pack
encode_barrier_dmb:
                ld      hl, &30bf

encode_barrier_pack:
; HL = base low16.  DE = base high16 (constant 0xd503 for all barriers).
                ld      de, &d503
; -- Read CRm (low byte of the imm result at OPVAL_ARRAY[0] + 2).
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 2)
                cp      &10
                jp      nc, fail            ; CRm > 15 → out of range [0,15]
                ld      b, a                ; B = CRm (0..15)
; -- Reject any non-zero upper byte of the 8-byte LE imm (must be u8 nibble).
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 4)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 5)
                or      a
                jp      nz, fail
; -- OR CRm into bits 11:8 (HL high byte, low nibble).  base bits 11:8
;    are clear (0x30 / 0x30 / 0x30 → low nibble 0), so a plain OR suffices.
                ld      a, b
                or      h
                ld      h, a
                ret


; -----------------------------------------------------------------------
; encode_ldr_lit_direct_word — LDR (literal) direct-label form.
;
; Port of tools/refenc/pass2.go:encodeLdrLitDirect (pass2.go:508).
;   off   := target - PC                     (raw byte offset)
;   imm19 := off / 4   (off must be 4-aligned, range +/-1 MiB signed)
;   base  := X ? 0x58000000 : 0x18000000
;   word  := base | ((imm19 & 0x7ffff) << 5) | Rt
;
; OPVAL_ARRAY[0] = Rt (X = 0x01 / W = 0x02); reg in +1.
; OPVAL_ARRAY[1] = OpImmExpr; 8-byte LE result (the label's byte offset,
;   same byte-offset space as PASS_PC) starts at +2.
;
; Output: DE:HL = encoded 32-bit word (HL = bits 0..15, DE = bits 16..31).
; Errors: jp fail on width != X/W, off not 4-aligned, imm19 out of
;   19-bit signed range, or target high bytes inconsistent.
; Grounded against aarch64-none-elf-as + ARM ARM C6.2.131 (LDR literal).
; -----------------------------------------------------------------------
encode_ldr_lit_direct_word:
; -- Width / base from operand-0 kind ---------------------------------
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jr      z, encode_ldrlit_x
                cp      OP_KIND_REG_W
                jp      nz, fail
                ld      a, &18              ; W base high byte (0x18000000)
                jr      encode_ldrlit_base_set
encode_ldrlit_x:
                ld      a, &58              ; X base high byte (0x58000000)
encode_ldrlit_base_set:
                ld      (encode_ldrlit_basehi), a

; -- off = target - PASS_PC (32-bit two's complement, LSB-first) ------
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                ld      hl, PASS_PC
                sub     (hl)
                ld      (encode_ldrlit_off + 0), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3)
                inc     hl
                sbc     a, (hl)
                ld      (encode_ldrlit_off + 1), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 4)
                inc     hl
                sbc     a, (hl)
                ld      (encode_ldrlit_off + 2), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 5)
                inc     hl
                sbc     a, (hl)
                ld      (encode_ldrlit_off + 3), a

; -- off must be 4-byte aligned (low 2 bits zero) ---------------------
                ld      a, (encode_ldrlit_off + 0)
                and     &03
                jp      nz, fail

; -- imm19 = off >> 2 (arithmetic, sign-preserving) -------------------
; Two arithmetic right-shifts of the 4-byte signed value.
                ld      b, 2
encode_ldrlit_asr_loop:
                ld      a, (encode_ldrlit_off + 3)
                sra     a
                ld      (encode_ldrlit_off + 3), a
                ld      a, (encode_ldrlit_off + 2)
                rra
                ld      (encode_ldrlit_off + 2), a
                ld      a, (encode_ldrlit_off + 1)
                rra
                ld      (encode_ldrlit_off + 1), a
                ld      a, (encode_ldrlit_off + 0)
                rra
                ld      (encode_ldrlit_off + 0), a
                djnz    encode_ldrlit_asr_loop
; encode_ldrlit_off now = imm19 (sign-extended to 32 bits).

; -- Range check: imm19 must fit in 19-bit signed --------------------
; Bits 19..31 must all equal bit 18 (the sign).  Equivalently, an extra
; ASR-by-19 of a COPY collapses to all-0 (non-neg) or all-FF (neg).
                ld      a, (encode_ldrlit_off + 0)
                ld      (encode_ldrlit_chk + 0), a
                ld      a, (encode_ldrlit_off + 1)
                ld      (encode_ldrlit_chk + 1), a
                ld      a, (encode_ldrlit_off + 2)
                ld      (encode_ldrlit_chk + 2), a
                ld      a, (encode_ldrlit_off + 3)
                ld      (encode_ldrlit_chk + 3), a
                ld      b, 19
encode_ldrlit_chk_loop:
                ld      a, (encode_ldrlit_chk + 3)
                sra     a
                ld      (encode_ldrlit_chk + 3), a
                ld      a, (encode_ldrlit_chk + 2)
                rra
                ld      (encode_ldrlit_chk + 2), a
                ld      a, (encode_ldrlit_chk + 1)
                rra
                ld      (encode_ldrlit_chk + 1), a
                ld      a, (encode_ldrlit_chk + 0)
                rra
                ld      (encode_ldrlit_chk + 0), a
                djnz    encode_ldrlit_chk_loop
; chk must be 0x00000000 (non-neg in range) or 0xFFFFFFFF (neg in range).
                ld      a, (encode_ldrlit_chk + 0)
                ld      h, a
                ld      a, (encode_ldrlit_chk + 1)
                or      h
                ld      h, a
                ld      a, (encode_ldrlit_chk + 2)
                or      h
                ld      h, a
                ld      a, (encode_ldrlit_chk + 3)
                or      h
                jr      z, encode_ldrlit_pack
                ld      a, (encode_ldrlit_chk + 0)
                ld      h, a
                ld      a, (encode_ldrlit_chk + 1)
                and     h
                ld      h, a
                ld      a, (encode_ldrlit_chk + 2)
                and     h
                ld      h, a
                ld      a, (encode_ldrlit_chk + 3)
                and     h
                cp      &ff
                jp      nz, fail

encode_ldrlit_pack:
; -- Pack word = basehi:00 | (imm19[0..18] << 5) | Rt -----------------
; imm19 (19 bits) lives in encode_ldrlit_off[0..2] (low 3 bytes; bit 18
; is the top valid bit).  word bytes:
;   byte0 = ((imm19 & 0x07) << 5) | Rt
;   byte1 = (imm19 >> 3)  & 0xff
;   byte2 = (imm19 >> 11) & 0xff   (8 bits: imm19[11..18])
;   byte3 = basehi
; (imm19 << 5) spans bits 5..23 — disjoint from Rt (bits 0..4) and the
; base high byte (bits 24..31), so plain ORs suffice.

; -- byte2 (DE high) = (imm19 >> 11) & 0xff ---------------------------
; imm19>>11: take off[1] (bits 8..15) >> 3, OR off[2] (bits 16..18) << 5.
                ld      a, (encode_ldrlit_off + 1)
                rrca
                rrca
                rrca
                and     &1f                 ; bits 11..15 of imm19 → low 5 of byte2
                ld      c, a
                ld      a, (encode_ldrlit_off + 2)
                and     &07                 ; imm19 bits 16..18
                rlca
                rlca
                rlca
                rlca
                rlca                        ; << 5 → bits 16..18 land in byte2 bits 5..7
                or      c
                ld      d, a                ; D = byte2 (word bits 16..23)
                ld      a, (encode_ldrlit_basehi)
                ; DE high byte (bit24..31) = basehi; build E later, keep in B
                ld      b, a                ; B = byte3 (basehi)

; -- byte1 (HL high) = (imm19 >> 3) & 0xff ----------------------------
; imm19>>3: off[0] (bits 0..7) >> 3 OR off[1] (bits 8..10) << 5.
                ld      a, (encode_ldrlit_off + 0)
                rrca
                rrca
                rrca
                and     &1f                 ; imm19 bits 3..7 → low 5
                ld      c, a
                ld      a, (encode_ldrlit_off + 1)
                and     &07                 ; imm19 bits 8..10
                rlca
                rlca
                rlca
                rlca
                rlca                        ; << 5 → bits 5..7
                or      c
                ld      h, a                ; H = byte1 (word bits 8..15)

; -- byte0 (HL low) = ((imm19 & 0x07) << 5) | Rt ----------------------
                ld      a, (encode_ldrlit_off + 0)
                and     &07                 ; imm19 bits 0..2
                rlca
                rlca
                rlca
                rlca
                rlca                        ; << 5 → bits 5..7
                ld      c, a
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f                 ; Rt (5 bits) → bits 0..4
                or      c
                ld      l, a                ; L = byte0 (word bits 0..7)

; -- Assemble DE = byte3:byte2 ----------------------------------------
                ld      e, d                ; E = byte2 (bits 16..23)
                ld      d, b                ; D = byte3 (bits 24..31) = basehi
                ret


encode_ldrlit_basehi:            defb    0
encode_ldrlit_off:               defb    0, 0, 0, 0
encode_ldrlit_chk:               defb    0, 0, 0, 0


; -----------------------------------------------------------------------
; encode_lslsr_word — pure word computation for lsl / lsr (mnem 17/18).
;
; Port of tools/refenc/pass2.go:encodeLSLSR (pass2.go:1196).
;
; In:  A = mnemonic_id (17=lsl, 18=lsr).
;      OPVAL_ARRAY[0] = Rd (X=0x01 / W=0x02), reg in +1.
;      OPVAL_ARRAY[1] = Rn (matching width), reg in +1.
;      OPVAL_ARRAY[2] = OpImmExpr (#shift, LE result at +2) OR a register
;        (LSLV/LSRV).  Width comes from operand 0.
; Out: DE:HL = encoded 32-bit word (HL=bits0..15, DE=bits16..31).
; Errors: jp fail on width != X/W, shift out of [0,regsize), or non-u8
;   shift.
; Grounded against aarch64-none-elf-as + ARM ARM C6.2.218/.222/.219/.223.
; -----------------------------------------------------------------------
encode_lslsr_word:
                ld      (encode_lslsr_mnem), a
; -- regsize / is64 from operand 0 kind -------------------------------
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jr      z, encode_lslsr_x
                cp      OP_KIND_REG_W
                jp      nz, fail
                xor     a                   ; is64 = 0 (W)
                jr      encode_lslsr_size_set
encode_lslsr_x:
                ld      a, 1                ; is64 = 1 (X)
encode_lslsr_size_set:
                ld      (encode_lslsr_is64), a

; -- Branch on operand-2 kind: register (LSLV/LSRV) vs immediate ------
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jp      z, encode_lslsr_reg
                cp      OP_KIND_REG_W
                jp      z, encode_lslsr_reg
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail

; =====================================================================
; Immediate form (UBFM alias).
; =====================================================================
; regsize = is64 ? 64 : 32.  Read shift (operand 2 LE byte 0).
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2)
                ld      (encode_lslsr_shift), a
; reject non-u8 shift (upper LE bytes must be zero)
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 4)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 5)
                or      a
                jp      nz, fail
; range: 0 <= shift < regsize
                ld      a, (encode_lslsr_is64)
                or      a
                ld      a, 32
                jr      z, encode_lslsr_imm_rs_set
                ld      a, 64
encode_lslsr_imm_rs_set:
                ld      (encode_lslsr_regsize), a
                ld      b, a                ; B = regsize
                ld      a, (encode_lslsr_shift)
                cp      b
                jp      nc, fail            ; shift >= regsize → fail

; -- Compute immr/imms by mnemonic ------------------------------------
                ld      a, (encode_lslsr_mnem)
                cp      18
                jp      z, encode_lslsr_lsr_imm

; LSL: immr = (-shift) & (regsize-1); imms = regsize-1-shift
;   shift==0 → immr=0; else immr = regsize - shift.
                ld      a, (encode_lslsr_shift)
                or      a
                jr      nz, encode_lslsr_lsl_immr_nz
                xor     a                   ; immr = 0
                jr      encode_lslsr_lsl_immr_done
encode_lslsr_lsl_immr_nz:
; immr = regsize - shift  (shift in [1, regsize-1])
                ld      a, (encode_lslsr_regsize)
                ld      c, a
                ld      a, (encode_lslsr_shift)
                ld      b, a
                ld      a, c
                sub     b                   ; A = regsize - shift
                and     &3f
encode_lslsr_lsl_immr_done:
                ld      (encode_lslsr_immr), a
; imms = regsize - 1 - shift
                ld      a, (encode_lslsr_regsize)
                dec     a                   ; regsize - 1
                ld      c, a
                ld      a, (encode_lslsr_shift)
                ld      b, a
                ld      a, c
                sub     b                   ; A = (regsize-1) - shift
                and     &3f
                ld      (encode_lslsr_imms), a
                jp      encode_lslsr_imm_base

encode_lslsr_lsr_imm:
; LSR: immr = shift; imms = regsize - 1
                ld      a, (encode_lslsr_shift)
                and     &3f
                ld      (encode_lslsr_immr), a
                ld      a, (encode_lslsr_regsize)
                dec     a
                and     &3f
                ld      (encode_lslsr_imms), a

encode_lslsr_imm_base:
; base = is64 ? 0xd3400000 : 0x53000000.  DE high16, HL=0.
                ld      a, (encode_lslsr_is64)
                or      a
                jr      z, encode_lslsr_imm_w
                ld      de, &d340
                jr      encode_lslsr_imm_pack
encode_lslsr_imm_w:
                ld      de, &5300
encode_lslsr_imm_pack:
                ld      hl, 0
; -- OR Rd into bits 4:0 ---------------------------------------------
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                ld      l, a
; -- OR Rn into bits 9:5 ---------------------------------------------
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                call    encode_ubfm_or_rn      ; HL |= Rn<<5
; -- OR imms into bits 15:10 -----------------------------------------
                ld      a, (encode_lslsr_imms)
                call    encode_ubfm_or_imms    ; HL |= imms<<10
; -- OR immr into bits 21:16 (DE low byte bits 0..5) -----------------
                ld      a, (encode_lslsr_immr)
                or      e
                ld      e, a
                ret

; =====================================================================
; Register form (LSLV / LSRV).
; =====================================================================
encode_lslsr_reg:
; base by mnemonic + width:
;   lsl(17): X 0x9ac02000 / W 0x1ac02000
;   lsr(18): X 0x9ac02400 / W 0x1ac02400
; DE high16: lsl X=0x9ac0 W=0x1ac0; lsr same high16 (0x...c0).
; HL low16:  lsl 0x2000; lsr 0x2400.  Then | (Rm<<16)|(Rn<<5)|Rd.
                ld      a, (encode_lslsr_is64)
                or      a
                jr      z, encode_lslsr_reg_w
                ld      de, &9ac0
                jr      encode_lslsr_reg_lo
encode_lslsr_reg_w:
                ld      de, &1ac0
encode_lslsr_reg_lo:
                ld      a, (encode_lslsr_mnem)
                cp      18
                jr      z, encode_lslsr_reg_lsr
                ld      hl, &2000           ; LSLV low16
                jr      encode_lslsr_reg_pack
encode_lslsr_reg_lsr:
                ld      hl, &2400           ; LSRV low16
encode_lslsr_reg_pack:
; -- OR Rd into bits 4:0 ---------------------------------------------
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                or      l
                ld      l, a
; -- OR Rn into bits 9:5 ---------------------------------------------
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                call    encode_ubfm_or_rn      ; HL |= Rn<<5
; -- OR Rm into bits 20:16 (DE low byte bits 0..4) -------------------
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 1)
                and     &1f
                or      e
                ld      e, a
                ret


; -----------------------------------------------------------------------
; encode_ubfm_or_rn — OR a 5-bit Rn (in A) into HL at bit position 5.
; Rn<<5 spans HL bits 5..9: low 3 bits of Rn → HL low byte bits 5..7;
; high 2 bits of Rn → HL high byte bits 0..1.  Clobbers A, C.
; -----------------------------------------------------------------------
encode_ubfm_or_rn:
                and     &1f
                rrca
                rrca
                rrca                        ; A bits 5..7 = Rn&7 ; A bits 0..1 = Rn>>3
                ld      c, a
                and     &e0                 ; (Rn&7)<<5
                or      l
                ld      l, a
                ld      a, c
                and     &03                 ; Rn>>3
                or      h
                ld      h, a
                ret

; -----------------------------------------------------------------------
; encode_ubfm_or_imms — OR a 6-bit imms (in A) into HL at bit position
; 10.  imms<<10 spans HL bits 10..15 = HL high byte bits 2..7.
; Clobbers A.
; -----------------------------------------------------------------------
encode_ubfm_or_imms:
                and     &3f
                add     a, a
                add     a, a                ; imms<<2 → bits 2..7 of high byte
                or      h
                ld      h, a
                ret


encode_lslsr_mnem:               defb    0
encode_lslsr_is64:               defb    0
encode_lslsr_regsize:            defb    0
encode_lslsr_shift:              defb    0
encode_lslsr_immr:               defb    0
encode_lslsr_imms:               defb    0
