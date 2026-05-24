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

; -- csetm (ID 52) — invert the condition before the form table -------
; csetm Rd, cond is an alias of CSINV Rd, XZR, XZR, invert(cond): the
; condition field in the machine code is cond XOR 1.  The form-table
; entry for ID 52 (manual_forms.go:416-425, CSINV pattern with the
; CondCode slot at bits[15:12]) places the cond operand verbatim, so we
; must XOR the stored cond byte by 1 here, then fall through to the
; generic path which encodes it.  This mirrors the Mac-side dispatch
; tools/refenc/pass2.go:308 (case 52 -> encodeCsetm), whose body is
; `invertedCond := cond ^ 1` (pass2.go:1436).  Grounded against
; aarch64-none-elf-as + ARM ARM C6.2.58 (CSETM).
;
; The OpCond operand is operand 1; its cond byte lives at +2 of the
; operand record (see encoder.asm encode_slot_cond: cond stored at +2).
                ld      a, (try_intercept_mnem)
                cp      52
                jp      nz, try_intercept_post_csetm
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                xor     1                   ; invert: EQ<->NE, CS<->CC, ...
                ld      (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2), a
                jp      try_intercept_no_match  ; Z=0 → generic form encodes it
try_intercept_post_csetm:

; -- mov Rd, #imm (ID 3) — MOV-immediate alias auto-selection ----------
; A bare `mov Rd, #imm` is an alias GNU as resolves to ONE of MOVZ (wide
; immediate), MOVN (inverted wide immediate), or ORR Rd,XZR,#bimm
; (bitmask immediate), choosing whichever single-instruction form can
; encode the value, in that priority order.  The generic form-table path
; only ever reaches the MOVZ pattern and feeds it through
; encode_slot_imm16shifted, which reads (v>>16)&3 as hw and the low 16
; bits as imm — wrong for any chunk not in the low slot, and incapable of
; MOVN / ORR.  Mirror tryEncodeMovImm (tools/refenc/pass2.go:438-502)
; here, ahead of the form table.  encode.go:53-66 is the same chunk
; search.  Only intercept the (Rd, #imm) shape; movz/movk/movn keep their
; own mnemonics, and `mov Rd, Rs` (register move) is a different shape
; the form table still handles.
                ld      a, (try_intercept_mnem)
                cp      3
                jr      nz, try_intercept_post_mov
                ld      a, (main_op_count)
                cp      2
                jr      nz, try_intercept_post_mov
                ld      a, (OPVAL_KINDS + 0)
                cp      OP_KIND_REG_X
                jr      z, try_intercept_mov_op0_ok
                cp      OP_KIND_REG_W
                jr      nz, try_intercept_post_mov
try_intercept_mov_op0_ok:
                ld      a, (OPVAL_KINDS + 1)
                cp      OP_KIND_IMM_EXPR
                jr      nz, try_intercept_post_mov
                call    encode_mov_imm_word
                jr      nz, try_intercept_post_mov  ; NZ → no single-inst fit; fall through
                call    intercept_emit_dehl
                xor     a                   ; Z=1 → handled
                ret
try_intercept_post_mov:

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

; -- Bitfield aliases: bfi=49 bfxil=50 ubfx=51 bfc=83 sbfx=84 ---------
; All BFM/SBFM/UBFM aliases with computed immr/imms, dispatched ahead of
; the form table on the Mac side (refenc/pass2.go:302 -> case 49,50,51,
; 83,84 -> encodeBitfieldInst).  bfc is 3-operand (Rn implicitly XZR);
; the others are 4-operand.  No form-table entry exists, so a miss here
; would fall through to FAIL40.  Grounded against aarch64-none-elf-as +
; ARM ARM C6.2.40/.42/.335/.41/.270.
                ld      a, (try_intercept_mnem)
                cp      49
                jr      z, try_intercept_bitfield
                cp      50
                jr      z, try_intercept_bitfield
                cp      51
                jr      z, try_intercept_bitfield
                cp      83
                jr      z, try_intercept_bitfield
                cp      84
                jp      nz, try_intercept_post_bitfield
try_intercept_bitfield:
                ld      a, (try_intercept_mnem)
                call    encode_bitfield_word
                call    intercept_emit_dehl
                xor     a
                ret
try_intercept_post_bitfield:

; -- tbz=22 / tbnz=23 — test bit and branch ---------------------------
; 3 operands: Rt, #bit, label.  Dispatched ahead of the form table on
; the Mac side (refenc/pass2.go:292 -> encodeTbzTbnz).  No form-table
; entry exists, so a miss here would fall through to FAIL40.  Grounded
; against aarch64-none-elf-as + ARM ARM C6.2.317 (TBZ) / C6.2.318 (TBNZ).
                ld      a, (try_intercept_mnem)
                cp      22
                jr      z, try_intercept_tbz
                cp      23
                jp      nz, try_intercept_post_tbz
try_intercept_tbz:
                ld      a, (main_op_count)
                cp      3
                jp      nz, try_intercept_post_tbz
                ld      a, (try_intercept_mnem)
                call    encode_tbz_word
                call    intercept_emit_dehl
                xor     a
                ret
try_intercept_post_tbz:

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
; encode_mov_imm_word — MOV-immediate alias auto-selection.
;
; Faithful port of tools/refenc/pass2.go:438-502 (tryEncodeMovImm).  A
; bare `mov Rd, #imm` resolves to the first single-instruction form that
; fits, in priority order:
;
;   1. MOVZ  — value is a single 16-bit chunk in some slot (lsl #0/16/32/
;              48; for W only #0/16).         base 0xd2800000 / 0x52800000
;   2. MOVN  — ~value is a single 16-bit chunk in some slot (for W, ~ is
;              masked to 32 bits first).       base 0x92800000 / 0x12800000
;   3. ORR   — value is a valid logical (bitmask) immediate;
;              mov Rd,#imm == orr Rd,XZR,#imm. base 0xb20003e0 / 0x320003e0
;
; encode.go:53-66 is the same chunk search; slots_logical.go (Z80:
; src/m3/slots/logical_imm.asm) is the bitmask encoder reused for step 3.
;
; "chunk<<shift == u" with shift a multiple of 16 is equivalent to: the
; only non-zero bytes of the 8-byte LE value are the two bytes at offset
; 2*hw.  So the chunk search is a pure byte test — no shifting needed.
;
; In:  OPVAL_ARRAY[0] = Rd (RegX/RegW), OPVAL_ARRAY[1] = OpImmExpr whose
;      8-byte LE value lives at OPVAL_ARRAY + 1*STRIDE + 2.
; Out: Z=1 → handled, DE:HL = encoded word (HL=bits0..15, DE=bits16..31).
;      Z=0 (NZ) → no single-instruction fit; caller falls through to the
;      form table (which will then fail cleanly if truly unencodable).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
encode_mov_imm_word:
; -- is64 from Rd kind (RegX → 1, else 0); Rd number --------------------
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                sub     OP_KIND_REG_X       ; A=0 iff RegX
                sub     1                   ; CY iff was RegX (A=0)
                sbc     a, a                ; A = 0xff iff RegX else 0x00
                and     1                   ; A = 1 iff RegX (is64) else 0
                ld      (encode_mov_imm_is64), a

                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_mov_imm_rd), a

; -- u = value, masked to 32 bits if !is64 ------------------------------
                ld      hl, OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2
                ld      de, encode_mov_imm_u
                ld      bc, 8
                ldir
                ld      hl, encode_mov_imm_u
                call    encode_mov_imm_mask32

; -- Step 1: MOVZ — single 16-bit chunk in u ----------------------------
                ld      hl, encode_mov_imm_u
                call    encode_mov_imm_find_chunk   ; A=hw / 0xff
                cp      &ff
                jr      z, encode_mov_imm_try_movn
                ld      b, 0                ; B=0 → movz base offset
                jr      encode_mov_imm_emit_wide

; -- Step 2: MOVN — single 16-bit chunk in ~u ---------------------------
encode_mov_imm_try_movn:
                ld      hl, encode_mov_imm_u
                ld      de, encode_mov_imm_nu
                ld      bc, 8
                ldir
                ld      hl, encode_mov_imm_nu
                call    ml_not
                ld      hl, encode_mov_imm_nu
                call    encode_mov_imm_mask32
                ld      hl, encode_mov_imm_nu
                call    encode_mov_imm_find_chunk
                cp      &ff
                jr      z, encode_mov_imm_try_orr
                ld      b, 1                ; B=1 → movn base offset
encode_mov_imm_emit_wide:
; Base hi16: movz 0x5280 / movn 0x1280, +0x8000 when is64.
;   B=0 → movz, B=1 → movn.  Start DE=0x5280, if movn sub 0x4000,
;   if is64 add 0x8000.
                push    af                  ; save hw
                ld      de, &5280
                bit     0, b
                jr      z, encode_mov_imm_base_z
                ld      de, &1280
encode_mov_imm_base_z:
                ld      a, (encode_mov_imm_is64)
                or      a
                jr      z, encode_mov_imm_base_w
                ld      a, d
                add     a, &80              ; +0x8000 → set sf bit
                ld      d, a
encode_mov_imm_base_w:
                pop     af                  ; A = hw
                jp      encode_mov_imm_pack_wide

; -- Step 3: ORR (bitmask immediate) — reuse encode_logical_imm ---------
; tryEncodeMovImm step 3 (refenc/pass2.go:489-500).  encode_logical_imm
; rejects non-bitmask values via `jp fail`; the soft-fail wrapper turns
; that into CY=1 so we can fall through to the form table instead.
encode_mov_imm_try_orr:
                ld      hl, encode_mov_imm_orr_slot
                ld      de, encode_mov_imm_u
                ld      a, (encode_mov_imm_is64)
                ld      c, a
                call    encode_logical_imm_try
                jr      c, encode_mov_imm_none
; DE:HL = (N|immr|imms)<<10.  OR in ORR base (0xb20003e0 / 0x320003e0)
; and Rd.  Low/high bits (0xe0/0x03) are width-independent; only D's high
; byte differs (0xb2 vs 0x32).
                ld      a, l
                or      &e0
                ld      l, a
                ld      a, h
                or      &03
                ld      h, a
                ld      c, &32              ; w ORR base hi8
                ld      a, (encode_mov_imm_is64)
                or      a
                jr      z, encode_mov_imm_orr_setd
                ld      c, &b2              ; x ORR base hi8
encode_mov_imm_orr_setd:
                ld      a, d
                or      c
                ld      d, a
                ld      a, (encode_mov_imm_rd)
                or      l
                ld      l, a
                xor     a                   ; Z=1 → handled
                ret

encode_mov_imm_none:
                or      &ff                 ; Z=0 → caller falls through
                ret


; -- encode_mov_imm_mask32 — if !is64, zero bytes 4..7 of (HL) ----------
encode_mov_imm_mask32:
                ld      a, (encode_mov_imm_is64)
                or      a
                ret     nz
                ld      bc, 4
                add     hl, bc
                xor     a
                ld      b, 4
encode_mov_imm_mask32_loop:
                ld      (hl), a
                inc     hl
                djnz    encode_mov_imm_mask32_loop
                ret

; -----------------------------------------------------------------------
; encode_mov_imm_pack_wide — build a MOVZ/MOVN word.
;   In:  DE:HL = base (hi16 in DE, lo16 0), A = hw (0..3),
;        chunk in encode_mov_imm_chunk_lo / _hi, Rd in encode_mov_imm_rd.
;   Word layout: bits 22:21 = hw, bits 20:5 = imm16, bits 4:0 = Rd.
;   Out: DE:HL = word, Z=1 (handled).
;
; Build the variable bits in a 4-byte LE accumulator (acc), then OR into
; DE:HL.  acc = (hw<<21) | (imm16<<5) | Rd.  imm16 = chunk_lo|(chunk_hi<<8).
; We compute (imm16<<5) by placing imm16 into acc bytes 0..1 and doing 5
; single-bit left shifts of the 4-byte accumulator, then OR in hw<<21 and
; Rd (Rd has no overlap with the shifted field).
; -----------------------------------------------------------------------
encode_mov_imm_pack_wide:
; DE (base hi16) survives untouched until the fold (the body below uses
; only A, B, F, HL), so no save/restore is needed.  imm16<<5 spans bits
; 5..20, so acc byte 3 is always 0 — we shift and fold only bytes 0..2.
                ld      (encode_mov_imm_hw), a

; acc[0..1] = imm16 (chunk_lo/chunk_hi consecutive); acc[2] = 0.
                ld      hl, (encode_mov_imm_chunk_lo)
                ld      (encode_mov_imm_acc + 0), hl
                xor     a
                ld      (encode_mov_imm_acc + 2), a

; acc <<= 5  (24-bit logical left shift via 5 single-bit passes).
                ld      b, 5
encode_mov_imm_shl5_outer:
                or      a                   ; CY = 0
                ld      hl, encode_mov_imm_acc + 0
                rl      (hl)
                inc     hl
                rl      (hl)
                inc     hl
                rl      (hl)
                djnz    encode_mov_imm_shl5_outer

; OR hw<<21 into acc byte 2 bits 5..6 (bit 21 = byte 2 bit 5).
                ld      a, (encode_mov_imm_hw)
                rrca
                rrca
                rrca
                and     &60
                ld      hl, encode_mov_imm_acc + 2
                or      (hl)
                ld      (hl), a

; OR Rd (bits 0..4) into acc byte 0.
                ld      a, (encode_mov_imm_rd)
                ld      hl, encode_mov_imm_acc + 0
                or      (hl)
                ld      (hl), a

; Fold acc into DE:HL.  HL = acc[0..1] (base lo16 is 0); D keeps its top
; base bits (acc byte 3 is 0), so only OR acc[2] into E.
                ld      a, (encode_mov_imm_acc + 2)
                or      e
                ld      e, a
                ld      hl, (encode_mov_imm_acc + 0)
                xor     a                   ; Z=1 → handled
                ret


; -----------------------------------------------------------------------
; encode_mov_imm_find_chunk — chunk search over the 8-byte LE value at HL.
;
; Returns A = hw (0..3) of the single non-zero 16-bit chunk, and stores
; that chunk's two bytes in encode_mov_imm_chunk_lo/_hi.  Returns A=0xff
; if NO single-chunk decomposition exists (more than one non-zero chunk).
;
; A "chunk" is bytes [2*hw, 2*hw+1]; the decomposition is valid iff all
; OTHER bytes of the 8-byte value are zero (mirrors Go's chunk<<shift==u
; for shift in {0,16,32,48}).  is64 has already masked bytes 4..7 to zero
; for the W case, so the loop over hw=0..3 naturally restricts W to
; hw∈{0,1} (the upper chunks are zero, and an all-zero value matches
; hw=0 with chunk 0 — exactly Go's behaviour for `mov Rd, #0`).
;
; In:  HL = ptr to 8-byte LE value.
; Out: A = hw or 0xff; encode_mov_imm_chunk_lo/_hi set on success.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
; Walk the four 16-bit chunks.  A single-chunk decomposition exists iff at
; most one chunk is non-zero; its index is hw (chunk_lo/_hi = that chunk).
; All-zero → hw=0, chunk=0 (matches Go's `mov Rd, #0`).  >1 non-zero → no
; fit (A=0xff).
encode_mov_imm_find_chunk:
                xor     a
                ld      (encode_mov_imm_chunk_lo), a    ; default chunk = 0
                ld      (encode_mov_imm_chunk_hi), a
                ld      d, 0                ; D = hw of the (single) non-zero chunk
                ld      e, 0                ; E = count of non-zero chunks
                ld      c, 0                ; C = current chunk index
encode_mov_imm_fc_loop:
                ld      a, (hl)             ; A = chunk lo
                inc     hl
                ld      b, (hl)             ; B = chunk hi
                inc     hl
                or      b                   ; (lo|hi)==0 → chunk is zero
                jr      z, encode_mov_imm_fc_step
; Non-zero chunk: record hw + chunk bytes (re-read lo/hi via HL-2).
                ld      d, c                ; hw = this index
                dec     hl
                dec     hl
                ld      a, (hl)
                ld      (encode_mov_imm_chunk_lo), a
                inc     hl
                ld      a, (hl)
                ld      (encode_mov_imm_chunk_hi), a
                inc     hl
                inc     e                   ; count++
encode_mov_imm_fc_step:
                inc     c
                ld      a, c
                cp      4
                jr      c, encode_mov_imm_fc_loop
; E = number of non-zero chunks.
                ld      a, e
                cp      2
                jr      nc, encode_mov_imm_fc_nofit ; >1 → no single-chunk fit
                ld      a, d                ; 0 or 1 non-zero → hw = D (0 if none)
                ret
encode_mov_imm_fc_nofit:
                ld      a, &ff
                ret


; -----------------------------------------------------------------------
; encode_logical_imm_try — soft-fail wrapper around encode_logical_imm.
;
; encode_logical_imm aborts assembly (`jp fail`) on a non-encodable
; value, but the MOV-bitmask path needs a recoverable "not encodable"
; signal so it can fall through to the form table.  logical_imm.asm
; routes all its reject sites through encode_logical_imm_reject, which
; consults the (encode_logical_imm_soft) flag: when armed it RETs with
; CY=1 (recoverable); when clear it `jp fail`s as before (the normal
; and/orr-immediate slot path).  Every reject site is at a
; stack-balanced point (only the encoder's own return address on the
; stack), so the RET lands back here.
;
; In:  HL = slot ptr, DE = 8-byte LE buffer ptr, C = is64.
; Out: CY=0 → DE:HL = encoded word; CY=1 → not encodable.
; Note: the success path of encode_logical_imm leaves CY clear (its final
; shift loop / early RET do not set CY in a way we rely on), so we force
; CY=0 explicitly here.
; -----------------------------------------------------------------------
encode_logical_imm_try:
                ld      a, 1
                ld      (encode_logical_imm_soft), a
                xor     a
                ld      (encode_logical_imm_rejected), a
                call    encode_logical_imm
; On a reject, encode_logical_imm_reject set (encode_logical_imm_rejected)
; to 1 and RETed here.  On success it RETed normally with that flag still
; 0 and DE:HL holding the encoded word.  Disarm, then map the flag to CY.
                xor     a
                ld      (encode_logical_imm_soft), a
                ld      a, (encode_logical_imm_rejected)
                or      a
                ret     z                   ; success → CY=0 (A=0 from `or`)
                scf                         ; reject → CY=1
                ret


; -----------------------------------------------------------------------
; Scratch + constants for encode_mov_imm_word.
;
; Working buffers live in section-D RAM (&F000 free region — see the
; memory map in assembler.asm) so they consume no code-image bytes; the
; test variant runs tight against the &C000 ceiling.  Only the constant
; slot record needs initialised image bytes.
; -----------------------------------------------------------------------
encode_mov_imm_is64:             equ     &F000
encode_mov_imm_rd:               equ     &F001
encode_mov_imm_hw:               equ     &F002
encode_mov_imm_chunk_lo:         equ     &F003
encode_mov_imm_chunk_hi:         equ     &F004
encode_mov_imm_acc:              equ     &F005   ; 4 bytes
encode_mov_imm_u:                equ     &F009   ; 8 bytes
encode_mov_imm_nu:               equ     &F011   ; 8 bytes
; LogicalImm slot record for the ORR-bitmask path: kind=0x24, BitPosition=10,
; BitWidth=13 (mirrors refenc tryEncodeMovImm's enc.OperandSlot{LogicalImm,10,13}).
encode_mov_imm_orr_slot:         defb    &24, 0, 10, 13


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


; -----------------------------------------------------------------------
; encode_bitfield_word — pure word computation for the bitfield aliases.
;
; Port of tools/refenc/pass2.go:encodeBitfieldInst (pass2.go:1277).
;   bfi  (49): immr=(-lsb)&(regsize-1), imms=width-1     base BFM
;   bfxil(50): immr=lsb,                imms=lsb+width-1  base BFM
;   ubfx (51): immr=lsb,                imms=lsb+width-1  base UBFM
;   bfc  (83): Rn=XZR(31); immr=(-lsb)&(regsize-1), imms=width-1 BFM
;   sbfx (84): immr=lsb,                imms=lsb+width-1  base SBFM
;   word = base | (immr<<16) | (imms<<10) | (Rn<<5) | Rd
;
; In:  A = mnemonic_id (49/50/51/83/84).
;      OPVAL_ARRAY[0] = Rd (X=0x01 / W=0x02), reg in +1.
;      bfc (83):  op1 = #lsb (ImmExpr), op2 = #width (ImmExpr); Rn=31.
;      others:    op1 = Rn, op2 = #lsb, op3 = #width.
; Out: DE:HL = encoded 32-bit word.
; Errors: jp fail on width != X/W, lsb >= regsize, width<1 or
;   width>regsize-lsb, or non-u8 lsb/width.
; -----------------------------------------------------------------------
encode_bitfield_word:
                ld      (encode_bf_mnem), a
; -- regsize / is64 from operand 0 kind -------------------------------
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jr      z, encode_bf_x
                cp      OP_KIND_REG_W
                jp      nz, fail
                ld      a, 32
                jr      encode_bf_size_set
encode_bf_x:
                ld      a, 64
encode_bf_size_set:
                ld      (encode_bf_regsize), a

; -- Locate operand slots (bfc shifts lsb/width down by one) ----------
; Rn and lsb/width source offsets differ for bfc (83).
                ld      a, (encode_bf_mnem)
                cp      83
                jr      z, encode_bf_bfc

; 4-operand: Rn = op1 reg; lsb = op2 LE byte 0; width = op3 LE byte 0.
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                ld      (encode_bf_rn), a
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2)
                ld      (encode_bf_lsb), a
; reject non-u8 lsb
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 3 * OPVAL_STRIDE + 2)
                ld      (encode_bf_width), a
                ld      a, (OPVAL_ARRAY + 3 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail
                jr      encode_bf_have_ops

encode_bf_bfc:
; 3-operand bfc: Rn = XZR (31); lsb = op1 LE; width = op2 LE.
                ld      a, 31
                ld      (encode_bf_rn), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                ld      (encode_bf_lsb), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2)
                ld      (encode_bf_width), a
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail

encode_bf_have_ops:
; -- Range checks: lsb < regsize; 1 <= width <= regsize - lsb ---------
                ld      a, (encode_bf_regsize)
                ld      b, a                ; B = regsize
                ld      a, (encode_bf_lsb)
                cp      b
                jp      nc, fail            ; lsb >= regsize
                ld      a, b                ; regsize
                ld      c, a
                ld      a, (encode_bf_lsb)
                ld      b, a
                ld      a, c
                sub     b                   ; A = regsize - lsb
                ld      c, a                ; C = regsize - lsb (>=1)
                ld      a, (encode_bf_width)
                or      a
                jp      z, fail             ; width == 0
                cp      c
                jp      z, encode_bf_width_ok
                jp      nc, fail            ; width > regsize - lsb
encode_bf_width_ok:

; -- Compute immr/imms by mnemonic ------------------------------------
; bfi(49) and bfc(83): immr=(-lsb)&(regsize-1), imms=width-1
; bfxil(50)/ubfx(51)/sbfx(84): immr=lsb, imms=lsb+width-1
                ld      a, (encode_bf_mnem)
                cp      49
                jr      z, encode_bf_immr_neg
                cp      83
                jr      z, encode_bf_immr_neg

; immr = lsb; imms = lsb + width - 1
                ld      a, (encode_bf_lsb)
                and     &3f
                ld      (encode_bf_immr), a
                ld      a, (encode_bf_lsb)
                ld      b, a
                ld      a, (encode_bf_width)
                add     a, b
                dec     a                   ; lsb + width - 1
                and     &3f
                ld      (encode_bf_imms), a
                jr      encode_bf_base

encode_bf_immr_neg:
; immr = (-lsb) & (regsize-1) = (regsize - lsb) & (regsize-1).
;   lsb==0 → immr=0; else regsize - lsb.
                ld      a, (encode_bf_lsb)
                or      a
                jr      nz, encode_bf_immr_neg_nz
                xor     a
                jr      encode_bf_immr_neg_done
encode_bf_immr_neg_nz:
                ld      a, (encode_bf_regsize)
                ld      c, a
                ld      a, (encode_bf_lsb)
                ld      b, a
                ld      a, c
                sub     b                   ; regsize - lsb
                and     &3f
encode_bf_immr_neg_done:
                ld      (encode_bf_immr), a
; imms = width - 1
                ld      a, (encode_bf_width)
                dec     a
                and     &3f
                ld      (encode_bf_imms), a

encode_bf_base:
; base by mnemonic + width.  DE = base high16, HL = 0.
;   bfi/bfxil/bfc : BFM  — X 0xb340 / W 0x3300
;   ubfx          : UBFM — X 0xd340 / W 0x5300
;   sbfx          : SBFM — X 0x9340 / W 0x1300
                ld      a, (encode_bf_mnem)
                cp      51
                jr      z, encode_bf_base_ubfm
                cp      84
                jr      z, encode_bf_base_sbfm
; BFM (bfi/bfxil/bfc)
                ld      a, (encode_bf_regsize)
                cp      64
                jr      z, encode_bf_bfm_x
                ld      de, &3300
                jr      encode_bf_pack
encode_bf_bfm_x:
                ld      de, &b340
                jr      encode_bf_pack
encode_bf_base_ubfm:
                ld      a, (encode_bf_regsize)
                cp      64
                jr      z, encode_bf_ubfm_x
                ld      de, &5300
                jr      encode_bf_pack
encode_bf_ubfm_x:
                ld      de, &d340
                jr      encode_bf_pack
encode_bf_base_sbfm:
                ld      a, (encode_bf_regsize)
                cp      64
                jr      z, encode_bf_sbfm_x
                ld      de, &1300
                jr      encode_bf_pack
encode_bf_sbfm_x:
                ld      de, &9340

encode_bf_pack:
                ld      hl, 0
; Rd → bits 4:0
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                ld      l, a
; Rn → bits 9:5
                ld      a, (encode_bf_rn)
                call    encode_ubfm_or_rn
; imms → bits 15:10
                ld      a, (encode_bf_imms)
                call    encode_ubfm_or_imms
; immr → bits 21:16 (DE low byte bits 0..5)
                ld      a, (encode_bf_immr)
                or      e
                ld      e, a
                ret


encode_bf_mnem:                  defb    0
encode_bf_regsize:               defb    0
encode_bf_rn:                    defb    0
encode_bf_lsb:                   defb    0
encode_bf_width:                 defb    0
encode_bf_immr:                  defb    0
encode_bf_imms:                  defb    0


; -----------------------------------------------------------------------
; encode_tbz_word — pure word computation for tbz / tbnz (mnem 22/23).
;
; Port of tools/refenc/pass2.go:encodeTbzTbnz (pass2.go:546).
;   imm14 = (target - PC)/4   (4-aligned, 14-bit signed range +/-32 KiB)
;   b5  = (bit>>5)&1   b40 = bit&0x1f   op = (mnem==23)?1:0
;   word = (b5<<31) | (0b011011<<25) | (op<<24) | (b40<<19)
;          | ((imm14 & 0x3fff)<<5) | Rt
;
; In:  A = mnemonic_id (22=tbz, 23=tbnz).
;      OPVAL_ARRAY[0] = Rt (X=0x01 / W=0x02), reg in +1.
;      OPVAL_ARRAY[1] = OpImmExpr (#bit, LE result at +2).
;      OPVAL_ARRAY[2] = OpImmExpr (label byte offset, LE at +2).
; Out: DE:HL = encoded 32-bit word.
; Errors: jp fail on width != X/W, bit > 63, bit > 31 with W register,
;   offset not 4-aligned, or imm14 out of 14-bit signed range.
; -----------------------------------------------------------------------
encode_tbz_word:
                ld      (encode_tbz_op), a  ; stash mnem (resolved to op bit below)

; -- bit number (operand 1 LE byte 0), validate range -----------------
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                ld      (encode_tbz_bit), a
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail            ; non-u8 bit
                ld      a, (encode_tbz_bit)
                cp      64
                jp      nc, fail            ; bit > 63
; W register → bit must be <= 31
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_W
                jr      nz, encode_tbz_bit_ok
                ld      a, (encode_tbz_bit)
                cp      32
                jp      nc, fail            ; bit > 31 with W
                jr      encode_tbz_bit_ok
encode_tbz_bit_ok:
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jr      z, encode_tbz_width_ok
                cp      OP_KIND_REG_W
                jp      nz, fail
encode_tbz_width_ok:

; -- off = target(operand 2) - PASS_PC (32-bit LE two's complement) ---
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 2)
                ld      hl, PASS_PC
                sub     (hl)
                ld      (encode_tbz_off + 0), a
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 3)
                inc     hl
                sbc     a, (hl)
                ld      (encode_tbz_off + 1), a
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 4)
                inc     hl
                sbc     a, (hl)
                ld      (encode_tbz_off + 2), a
                ld      a, (OPVAL_ARRAY + 2 * OPVAL_STRIDE + 5)
                inc     hl
                sbc     a, (hl)
                ld      (encode_tbz_off + 3), a

; -- off must be 4-byte aligned ---------------------------------------
                ld      a, (encode_tbz_off + 0)
                and     &03
                jp      nz, fail

; -- imm14 = off >> 2 (arithmetic) ------------------------------------
                ld      b, 2
encode_tbz_asr_loop:
                ld      a, (encode_tbz_off + 3)
                sra     a
                ld      (encode_tbz_off + 3), a
                ld      a, (encode_tbz_off + 2)
                rra
                ld      (encode_tbz_off + 2), a
                ld      a, (encode_tbz_off + 1)
                rra
                ld      (encode_tbz_off + 1), a
                ld      a, (encode_tbz_off + 0)
                rra
                ld      (encode_tbz_off + 0), a
                djnz    encode_tbz_asr_loop

; -- Range check: imm14 must fit in 14-bit signed --------------------
; ASR a copy by 14; result must be all-0 (non-neg) or all-FF (neg).
                ld      a, (encode_tbz_off + 0)
                ld      (encode_tbz_chk + 0), a
                ld      a, (encode_tbz_off + 1)
                ld      (encode_tbz_chk + 1), a
                ld      a, (encode_tbz_off + 2)
                ld      (encode_tbz_chk + 2), a
                ld      a, (encode_tbz_off + 3)
                ld      (encode_tbz_chk + 3), a
                ld      b, 14
encode_tbz_chk_loop:
                ld      a, (encode_tbz_chk + 3)
                sra     a
                ld      (encode_tbz_chk + 3), a
                ld      a, (encode_tbz_chk + 2)
                rra
                ld      (encode_tbz_chk + 2), a
                ld      a, (encode_tbz_chk + 1)
                rra
                ld      (encode_tbz_chk + 1), a
                ld      a, (encode_tbz_chk + 0)
                rra
                ld      (encode_tbz_chk + 0), a
                djnz    encode_tbz_chk_loop
                ld      a, (encode_tbz_chk + 0)
                ld      h, a
                ld      a, (encode_tbz_chk + 1)
                or      h
                ld      h, a
                ld      a, (encode_tbz_chk + 2)
                or      h
                ld      h, a
                ld      a, (encode_tbz_chk + 3)
                or      h
                jr      z, encode_tbz_pack
                ld      a, (encode_tbz_chk + 0)
                ld      h, a
                ld      a, (encode_tbz_chk + 1)
                and     h
                ld      h, a
                ld      a, (encode_tbz_chk + 2)
                and     h
                ld      h, a
                ld      a, (encode_tbz_chk + 3)
                and     h
                cp      &ff
                jp      nz, fail

encode_tbz_pack:
; imm14 (14 bits) lives in encode_tbz_off[0..1] (low 14 bits valid).
; word bytes:
;   byte0 = ((imm14 & 0x07) << 5) | Rt
;   byte1 = (imm14 >> 3) & 0xff             (imm14 bits 3..10)
;   byte2 = (imm14 >> 11) & 0x07            (imm14 bits 11..13, → bits16..18)
;           | (b40 << 3)                    (b40 bits 0..4 → bits 19..23)
;   byte3 = 0x36 | op | (b5 << 7)           (0b011011<<1=0x36; op bit0; b5 bit7)

; -- byte0 (L) = (imm14[0..2] << 5) | Rt ------------------------------
                ld      a, (encode_tbz_off + 0)
                and     &07
                rlca
                rlca
                rlca
                rlca
                rlca                        ; << 5
                ld      c, a
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f
                or      c
                ld      l, a

; -- byte1 (H) = (imm14 >> 3) & 0xff ----------------------------------
; off[0]>>3 OR off[1]<<5.
                ld      a, (encode_tbz_off + 0)
                rrca
                rrca
                rrca
                and     &1f                 ; imm14 bits 3..7
                ld      c, a
                ld      a, (encode_tbz_off + 1)
                and     &07                 ; imm14 bits 8..10
                rlca
                rlca
                rlca
                rlca
                rlca                        ; << 5
                or      c
                ld      h, a

; -- byte2 (E) = (imm14>>11 & 0x07) | (b40 << 3) ----------------------
; imm14 bits 11..13 = off[1] bits 3..5 → (off[1]>>3)&0x07.
                ld      a, (encode_tbz_off + 1)
                rrca
                rrca
                rrca
                and     &07                 ; imm14 bits 11..13 → byte2 bits 0..2
                ld      c, a
                ld      a, (encode_tbz_bit)
                and     &1f                 ; b40 = bit & 0x1f
                rlca
                rlca
                rlca                        ; << 3 → byte2 bits 3..7
                or      c
                ld      e, a

; -- byte3 (D) = 0x36 | op | (b5 << 7) --------------------------------
                ld      a, &36
                ld      c, a
; op bit: tbnz (23) → 1, tbz (22) → 0.
                ld      a, (encode_tbz_op)
                cp      23
                jr      nz, encode_tbz_op_zero
                ld      a, c
                or      &01                 ; op = 1 → bit0
                ld      c, a
encode_tbz_op_zero:
; b5 = (bit >> 5) & 1.
                ld      a, (encode_tbz_bit)
                and     &20                 ; bit 5 of bit number
                jr      z, encode_tbz_b5_zero
                ld      a, c
                or      &80                 ; b5 = 1 → bit7
                ld      c, a
encode_tbz_b5_zero:
                ld      d, c
                ret


encode_tbz_op:                   defb    0
encode_tbz_bit:                  defb    0
encode_tbz_off:                  defb    0, 0, 0, 0
encode_tbz_chk:                  defb    0, 0, 0, 0
