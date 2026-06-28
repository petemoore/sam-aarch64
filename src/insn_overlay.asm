; insn_overlay.asm — overlay_classify + literal_word + compact_inst:
; the encoder-dependent half of assemble.Compact (item i204b / i48c-b8e
; brick 4, the final brick).
;
; Z80 port of:
;   tools/sam-aarch64/assemble/overlay.go::overlayClassify  (82-163),
;       classifyMem (177-220), classifyMovImm (230-254),
;       foldSlotPCDependent (167-174), exprContextDep (260-278),
;       encodeInstOverlay (20-49)
;   tools/sam-aarch64/assemble/compact.go::literalWord (354-370),
;       compactInst (252-261)
;   tools/sam-aarch64-format/litinsts.go::IsFullyLiteral (16-46)
;   tools/aarch64enc/overlay.go::ZeroSlot (180-212), FoldSlotForKind
;       (150-176)
;
; For text->.tbn assembly ON the SAM, the compactor turns each symbolic
; INST record into a compact `.tbn` v2 INSN_RUN element: a fully-literal
; PC-invariant instruction as a bare 4-byte base word, a symbol/PC-bearing
; one as a base word (relocated field zeroed) plus a {slot, expression}
; overlay patch.  overlay_classify mirrors encode_inst's dispatch to pick
; which overlay slot the relocated field uses and which operand expression
; drives it; literal_word probes encode_inst at two PCs to decide whether
; an instruction is PC-invariant; compact_inst is the glue producing the
; base word + (0 or 1) patches.
;
; These REUSE the encode_inst dispatcher + its helpers (enc_build_kinds,
; enc_nth_operand, enc_eval_at, enc_rd_width, enc_mov_scan,
; form_lookup_find_first, form_lookup_match) and the proven insn_fold
; slot folders.  Like encode_inst they need ENCTAB mapped into section A
; (the form table + slot records live there), so they run inside an
; enctab_map_in bracket — the i204b boot self-test opens that same window.
;
; The FoldSlot byte values these routines emit are the FSID_* wire
; contract already declared in src/insn_run.asm (identical to the Go
; FoldSlot constants, tools/aarch64enc/overlay.go:16-38) — used directly,
; not redeclared.
;
; SCOPE: i204b wires these ONLY as a boot self-test (see
; src/test_overlay_classify.asm).  The production compactor skeleton
; (src/test_compact_ir.asm) still emits placeholder base words; rewriting
; it to call compact_inst is a separate follow-up.  This file is therefore
; gated under BUILD_TESTS_ENCODE alongside insn_encode.asm — the
; production assembler-prod binary is unaffected.
;
; Known divergences from the Go authority (deliberate, same net result):
;   * literalWord catches a probe-PC encode error and keeps the
;     instruction symbolic; the Z80 encode_inst fails hard (jp
;     fail_with_tag) on a real encode error.  The IsFullyLiteral gate
;     runs FIRST (matching Go's ordering), so a curated boot-self-test
;     fixture never reaches a post-gate encode failure unless it is a
;     genuine bug — for which the hard fail is the correct, loud
;     behaviour.
;   * classifyMovImm's "no movz/orr encoding" error is likewise a hard
;     insn_fold fail here rather than a returned error — same loud-gap
;     outcome for the self-test.
; -----------------------------------------------------------------------

; -----------------------------------------------------------------------
; expr_has_context_dep — scan an expression bytecode stream for any opcode
; whose value depends on the symbol table, a local label, the PC, or a
; relocation.  Port of overlay.go::exprContextDep (260-278) /
; litinsts.go::exprHasContextDep (98-116).  Anything the Go ExprReader
; would reject — an unknown opcode or a truncated inline operand — is
; conservatively context-dependent, exactly as the reader's error path
; classifies it.
;
; Opcode map (tools/sam-aarch64-format/expr.go:10-41):
;   &01..&04  PUSH_IMM8/16/32/64  — pure; skip 1/2/4/8 inline bytes
;   &05..&07  PUSH_SYM/LOCAL/PC   — context-dependent
;   &10..&18  ADD..SHR            — pure, no inline bytes
;   &20..&21  NEG/NOT             — pure, no inline bytes
;   &30..&38  REL_*               — context-dependent
;   anything else                 — unknown -> context-dependent
;
; Input:  HL = bytecode ptr, BC = byte count.
; Output: CF=1 if context-dependent, CF=0 if pure.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
expr_has_context_dep:
ehcd_loop:
                ld      a, b
                or      c
                jr      z, ehcd_pure                ; consumed all -> pure
                ld      a, (hl)                     ; opcode
                inc     hl
                dec     bc
                cp      &01
                jr      c, ehcd_dep                 ; &00: unknown
                cp      &05
                jr      c, ehcd_imm                 ; &01..&04: PUSH_IMMn
                cp      &08
                jr      c, ehcd_dep                 ; &05..&07: SYM/LOCAL/PC
                cp      &10
                jr      c, ehcd_dep                 ; &08..&0f: unknown
                cp      &19
                jr      c, ehcd_loop                ; &10..&18: binary ops
                cp      &20
                jr      c, ehcd_dep                 ; &19..&1f: unknown
                cp      &22
                jr      c, ehcd_loop                ; &20..&21: NEG/NOT
                jr      ehcd_dep                    ; &22.. (incl. &30-&38 REL_*)
ehcd_imm:
; A = 1..4 -> inline operand width 1/2/4/8 = 1 << (A-1).
                ld      de, 1
                dec     a
                jr      z, ehcd_skip
ehcd_width:
                sla     e
                dec     a
                jr      nz, ehcd_width
ehcd_skip:
                add     hl, de
                ld      a, c
                sub     e
                ld      c, a
                ld      a, b
                sbc     a, d
                ld      b, a
                jr      c, ehcd_dep                 ; truncated operand -> dependent
                jr      ehcd_loop
ehcd_pure:
                or      a                           ; CF = 0
                ret
ehcd_dep:
                scf
                ret

; -----------------------------------------------------------------------
; ovl_expr_ctx_dep_at — HL -> an IMM_EXPR-style operand body, i.e.
; [len_lo][len_hi][expr...].  Scans its expr for context dependence.
; Output: CF=1 if context-dependent.  Clobbers A, BC, DE, HL.
; -----------------------------------------------------------------------
ovl_expr_ctx_dep_at:
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = expr len
                inc     hl                          ; HL -> expr bytes
                jp      expr_has_context_dep

; -----------------------------------------------------------------------
; enc_is_fully_literal — port of IsFullyLiteral (litinsts.go:16-46).
; Walk the operand stream; an instruction is fully literal iff no operand
; references a symbol/local/PC/reloc and none is a litpool ref.
;
; Input:  enc_op_count, enc_op_ptr already set (compact_inst /
;         overlay_classify seed them from their args).
; Output: CF=1 if fully literal, CF=0 otherwise.
; Clobbers: A, BC, DE, HL.
;
; Kind handling vs litinsts.go:25-44: litpool -> never literal; IMM_EXPR /
; MEM / SHIFTED_REG / EXT_REG -> literal iff their expr (if any) is
; context-independent; the register run (&01..&04), cond (&0a) and
; sys-name (&0b) are neutral; anything else -> not literal.  Go also
; treats OpString (&09) as neutral, but a KindInst never carries one and
; the Z80 encode path cannot even skip it (enc_skip_operand fails hard) —
; Go's literalWord would classify such a record literal and then keep it
; symbolic when the probe encode errors, so classifying it not-literal
; here reaches the same keep-symbolic outcome without the hard fail.
; -----------------------------------------------------------------------
enc_is_fully_literal:
                ld      a, (enc_op_count)
                or      a
                jr      z, eifl_literal             ; 0 operands -> literal
                ld      b, a                        ; B = remaining operands
                ld      hl, (enc_op_ptr)
eifl_loop:
                push    bc
                ld      a, (hl)                     ; operand kind
                cp      OPK_LITPOOL
                jr      z, eifl_notliteral_pop      ; litpool -> never literal
                cp      OPK_IMM_EXPR
                jr      z, eifl_imm
                cp      OPK_MEM
                jr      z, eifl_mem
                cp      OPK_SHIFTED_REG
                jr      z, eifl_shext
                cp      OPK_EXT_REG
                jr      z, eifl_shext
                cp      OPK_REG_LO
                jr      c, eifl_notliteral_pop      ; &00: unknown kind
                cp      OPK_REG_HI + 1
                jr      c, eifl_next                ; &01..&04 reg: neutral
                cp      OPK_COND
                jr      z, eifl_next
                cp      OPK_SYS_NAME
                jr      z, eifl_next
                jr      eifl_notliteral_pop         ; unknown kind (see header)

eifl_imm:
; IMM_EXPR (&05): body is [len][expr]; HL -> kind, so +1.
                push    hl
                inc     hl                          ; -> len_lo
                call    ovl_expr_ctx_dep_at         ; CF=1 if context dep
                pop     hl
                jr      c, eifl_notliteral_pop
                jr      eifl_next

eifl_mem:
; OpMem (&08): only shapes 1/2/3 carry an offset expr; the other shapes
; (constant index regs) are context-independent.
                push    hl
                call    ovl_mem_expr_ptr            ; CF=1 + HL -> body if expr
                jr      nc, eifl_mem_pure
                call    ovl_expr_ctx_dep_at
                pop     hl
                jr      c, eifl_notliteral_pop
                jr      eifl_next
eifl_mem_pure:
                pop     hl
                jr      eifl_next

eifl_shext:
; OpShiftedReg (&06) / OpExtendedReg (&07): the amount expr is at
; [kind][width][Rm][shift/extend][len][expr].  HL -> kind.
                push    hl
                inc     hl
                inc     hl
                inc     hl
                inc     hl                          ; -> len_lo
                call    ovl_expr_ctx_dep_at
                pop     hl
                jr      c, eifl_notliteral_pop
                ; fall through to eifl_next
eifl_next:
; Advance BEFORE restoring the loop counter: enc_skip_operand clobbers BC.
                call    enc_skip_operand
                pop     bc
                djnz    eifl_loop
eifl_literal:
                scf
                ret
eifl_notliteral_pop:
                pop     bc
                or      a                           ; CF = 0
                ret

; -----------------------------------------------------------------------
; literal_word — port of compact.go::literalWord (354-370).  Returns the
; assembled word for a fully-literal, PC-invariant instruction.
;
; IsFullyLiteral gate -> encode_inst at pcProbeA (0) -> at pcProbeB
; (0x40000) -> compare.  Equal => bare literal = wA.  Different (a
; constant-target PC-relative form like `b 0x1000`) => keep symbolic.
;
; Input:  enc_op_count, enc_op_ptr, enc_mnem set by the caller
;         (compact_inst seeds them).
; Output: CF=1 + DEHL = the bare literal word on success;
;         CF=0 (keep symbolic) otherwise.
; Clobbers: everything (incl. PASS_PC — compact_inst restores it).
; -----------------------------------------------------------------------
PC_PROBE_B_BYTE2:  equ  &04          ; pcProbeB 0x40000 = byte2 &04, rest 0

literal_word:
                call    enc_is_fully_literal
                jr      nc, lw_keep_symbolic
; encode at pcProbeA = 0 (PASS_PC all zero).
                xor     a
                ld      (PASS_PC + 0), a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                call    lw_call_encode
; Save wA (DEHL) into ovl_wa.
                ld      (ovl_wa + 0), hl
                ld      (ovl_wa + 2), de
; encode at pcProbeB = 0x40000 (only byte2 differs from probe A).
                ld      a, PC_PROBE_B_BYTE2
                ld      (PASS_PC + 2), a
                call    lw_call_encode
; Compare wB (DEHL) to wA (ovl_wa).
                ld      bc, (ovl_wa + 0)
                or      a
                sbc     hl, bc
                jr      nz, lw_keep_symbolic
                ld      hl, (ovl_wa + 2)
                or      a
                sbc     hl, de
                jr      nz, lw_keep_symbolic
; wA == wB: bare literal = wA.  Reload DEHL = wA.
                ld      hl, (ovl_wa + 0)
                ld      de, (ovl_wa + 2)
                scf
                ret
lw_keep_symbolic:
                or      a                           ; CF = 0
                ret

; lw_call_encode — invoke encode_inst with the saved {mnem, opcount, ptr}.
lw_call_encode:
                ld      hl, (enc_op_ptr)
                ld      a, (enc_op_count)
                ld      de, (enc_mnem)
                jp      encode_inst                 ; tail-call; returns DEHL

; -----------------------------------------------------------------------
; overlay_classify — port of overlay.go::overlayClassify (82-163).  For a
; symbol/PC-bearing instruction, determine the overlay {slot, expr} and
; the litpool flags.  Mirrors encode_inst's dispatch order EXACTLY.
;
; Input:
;   HL = operand stream ptr, A = operand count, DE = mnemonic id.
;   PASS_PC = the instruction's PC (consulted by classifyMovImm's eval).
; Output (on success, A=0):
;   OVL_SLOT       = FoldSlot byte (FSID_* wire values, insn_run.asm)
;   OVL_EXPR_PTR   = ptr to the driving expression's [len_lo][len_hi][expr]
;                    body (set in every case that carries an expr)
;   OVL_EXPR_LEN   = expr byte length (u16)
;   OVL_IS_LITPOOL = 1 if litpool, else 0
;   OVL_LITWIDTH   = litpool element width (8 or 4) when litpool
;   OVL_RT         = destination reg (litpool only)
; Output (loud gap, A=1): a symbolic operand shape with no overlay slot.
; Clobbers: everything.
; -----------------------------------------------------------------------
overlay_classify:
                ld      (enc_mnem), de
                ld      (enc_op_count), a
                ld      (enc_op_ptr), hl
; Reset outputs.
                xor     a
                ld      (OVL_IS_LITPOOL), a
                ld      (OVL_LITWIDTH), a
                ld      (OVL_RT), a
; Build kinds[] (the compound scan + form match below read them).
                call    enc_build_kinds

; -- Compound-operand forms (overlay.go:96-109): scan kinds[] -----------
; OpMem -> ovl_classify_mem; OpShiftedReg/OpExtendedReg -> loud gap;
; OpLitPool -> FoldLitpool19 with the litpool operand's expr/width/Rt.
                ld      a, (enc_op_count)
                or      a
                jr      z, ovl_no_compound
                ld      b, a
                ld      hl, OPVAL_KINDS
ovl_cscan:
                ld      a, (hl)
                cp      OPK_MEM
                jp      z, ovl_classify_mem
                cp      OPK_SHIFTED_REG
                jp      z, ovl_loud_gap
                cp      OPK_EXT_REG
                jp      z, ovl_loud_gap
                cp      OPK_LITPOOL
                jp      z, ovl_litpool
                inc     hl
                djnz    ovl_cscan
ovl_no_compound:

; -- mov-imm (overlay.go:111-116): mnem==mov(3), 2 ops, op0 RegX/W, op1 IMM
                ld      a, (enc_mnem + 1)
                or      a
                jp      nz, ovl_generic             ; id >= 256: none of these
                ld      a, (enc_mnem)
                cp      3
                jr      nz, ovl_try_ldrlit
                ld      a, (enc_op_count)
                cp      2
                jr      nz, ovl_try_ldrlit
                ld      a, (OPVAL_KINDS + 0)
                call    ovl_is_regxw
                jr      nz, ovl_try_ldrlit
                ld      a, (OPVAL_KINDS + 1)
                cp      OPK_IMM_EXPR
                jr      nz, ovl_try_ldrlit
                jp      ovl_classify_movimm

ovl_try_ldrlit:
; -- ldr-lit (overlay.go:118-124): mnem==ldr(5), 2 ops, op0 RegX/W, op1 IMM
                ld      a, (enc_mnem)
                cp      5
                jr      nz, ovl_try_tbz
                ld      a, (enc_op_count)
                cp      2
                jr      nz, ovl_try_tbz
                ld      a, (OPVAL_KINDS + 0)
                call    ovl_is_regxw
                jr      nz, ovl_try_tbz
                ld      a, (OPVAL_KINDS + 1)
                cp      OPK_IMM_EXPR
                jr      nz, ovl_try_tbz
; FoldBranch19, expr = ops[1].Expr.
                ld      a, FSID_BRANCH19
                jp      ovl_slot_a_expr_op1

ovl_try_tbz:
; -- tbz/tbnz (overlay.go:126-131): mnem 22/23, >=3 ops -> FoldBranch14, op2
                ld      a, (enc_mnem)
                cp      22
                jr      z, ovl_tbz_hit
                cp      23
                jr      nz, ovl_special_gap
ovl_tbz_hit:
                ld      a, (enc_op_count)
                cp      3
                jp      c, ovl_loud_gap             ; needs 3 operands
                ld      a, FSID_BRANCH14
                ld      (OVL_SLOT), a
                ld      a, 2
                call    ovl_set_expr_from_op        ; expr <- op2
                jp      ovl_ok

ovl_special_gap:
; -- Special-form loud gaps (overlay.go:137-140): these carry constant or
; sys-name operands, never a relocatable expr.  If one ever does, fail
; loudly.  Set = {17,18,49,50,51,83,84,47,52,66,67,68,70,76,77,78,79}.
                ld      a, (enc_mnem)
                call    ovl_is_special_gap
                jp      z, ovl_loud_gap

; -- Generic form-table path (overlay.go:142-162) -----------------------
ovl_generic:
                ld      de, (enc_mnem)
                call    form_lookup_find_first      ; HL=form, BC=count, Z
                jp      nz, ovl_loud_gap            ; no form -> loud gap
                ld      de, OPVAL_KINDS
                ld      a, (enc_op_count)
                call    form_lookup_match           ; HL=matched form, Z
                jp      nz, ovl_loud_gap
                ld      (enc_form_ptr), hl
; Walk operands in lockstep with the form's slot records (the same walk
; encode_inst's enc_slot_loop does: first slot record at form+11, 4-byte
; stride).  For each OpImmExpr operand whose slot maps to a FoldSlot
; (FoldSlotForKind) and is either context-dependent OR PC-dependent,
; return {fs, expr}.
                ld      a, (enc_op_count)
                or      a
                jp      z, ovl_loud_gap             ; no operands -> no reloc operand
                ld      (enc_remaining), a
                ld      hl, (enc_form_ptr)
                ld      de, 11
                add     hl, de
                ld      (enc_slot_ptr), hl          ; -> first slot record
                ld      hl, (enc_op_ptr)
                ld      (enc_cur), hl
ovl_gen_loop:
                ld      hl, (enc_cur)
                ld      a, (hl)                     ; operand kind
                cp      OPK_IMM_EXPR
                jr      nz, ovl_gen_next            ; only IMM_EXPR operands
; SlotKind -> FoldSlot (FoldSlotForKind).
                ld      hl, (enc_slot_ptr)
                ld      a, (hl)                     ; slot kind
                call    ovl_fold_slot_for_kind      ; CF=1 + A=FoldSlot if mapped
                jr      nc, ovl_gen_next
                ld      (ovl_gen_fs), a             ; stash candidate FoldSlot
; Context-dependent expr OR PC-dependent slot -> return {fs, expr}.
                ld      hl, (enc_cur)
                inc     hl                          ; -> len_lo of IMM_EXPR body
                call    ovl_expr_ctx_dep_at         ; CF=1 if context dep
                jr      c, ovl_gen_hit
                ld      a, (ovl_gen_fs)
                call    ovl_fold_slot_pc_dependent  ; CF=1 if PC-dependent
                jr      nc, ovl_gen_next
ovl_gen_hit:
                ld      a, (ovl_gen_fs)
                ld      (OVL_SLOT), a
; expr <- the current operand's body.
                ld      hl, (enc_cur)
                inc     hl
                call    ovl_store_expr_body         ; OVL_EXPR_PTR/LEN <- (HL)
                jp      ovl_ok
ovl_gen_next:
                ld      hl, (enc_cur)
                call    enc_skip_operand
                ld      (enc_cur), hl
                ld      hl, (enc_slot_ptr)
                ld      de, 4
                add     hl, de
                ld      (enc_slot_ptr), hl
                ld      a, (enc_remaining)
                dec     a
                ld      (enc_remaining), a
                jp      nz, ovl_gen_loop
                jp      ovl_loud_gap                ; no relocated operand found

; -- ovl_litpool — OpLitPool present: FoldLitpool19, expr/width/Rt -------
ovl_litpool:
; overlay.go:102-107: FoldLitpool19 with the litpool operand's expr and
; width; Rt = ops[0].Reg.
                ld      a, FSID_LITPOOL19
                ld      (OVL_SLOT), a
                ld      a, 1
                ld      (OVL_IS_LITPOOL), a
; ops[0].Reg: HL -> op0, reg byte at +1.
                ld      hl, (enc_op_ptr)
                inc     hl
                ld      a, (hl)
                ld      (OVL_RT), a
; Walk operands to the litpool operand; read width + expr.
                ld      a, (enc_op_count)
                ld      b, a
                ld      hl, (enc_op_ptr)
ovl_lp_scan:
                ld      a, (hl)
                cp      OPK_LITPOOL
                jr      z, ovl_lp_found
                push    bc
                call    enc_skip_operand
                pop     bc
                djnz    ovl_lp_scan
                jp      ovl_loud_gap                ; unreachable (scan said present)
ovl_lp_found:
; Litpool wire: [kind][width][len][expr].  HL -> kind.
                inc     hl                          ; -> width
                ld      a, (hl)
                ld      (OVL_LITWIDTH), a
                inc     hl                          ; -> len_lo (body)
                call    ovl_store_expr_body
                jp      ovl_ok

; -- ovl_classify_mem — port of classifyMem (overlay.go:177-220) --------
ovl_classify_mem:
; ldp/stp pair (mnem 7/8, >=3 ops, ops[2]=OpMem): FoldPairImm7.
                ld      a, (enc_mnem + 1)
                or      a
                jr      nz, ovl_cm_findmem          ; id >= 256: not pair
                ld      a, (enc_mnem)
                cp      7
                jr      z, ovl_cm_pair_check
                cp      8
                jr      nz, ovl_cm_findmem
ovl_cm_pair_check:
                ld      a, (enc_op_count)
                cp      3
                jr      c, ovl_cm_findmem
                ld      a, (OPVAL_KINDS + 2)
                cp      OPK_MEM
                jr      nz, ovl_cm_findmem
; ops[2] is OpMem.  Require a non-empty offset expr (overlay.go:181-183).
                ld      a, 2
                call    enc_nth_operand             ; HL -> op2 (OpMem)
                call    ovl_mem_expr_ptr            ; CF=1 + HL->body if expr present
                jp      nc, ovl_loud_gap            ; pair with no offset expr
                ld      a, FSID_PAIR_IMM7
                ld      (OVL_SLOT), a
                call    ovl_store_expr_body
                jp      ovl_ok

ovl_cm_findmem:
; Find the first OpMem operand; it must carry a non-empty offset expr
; (overlay.go:187-198).
                ld      a, (enc_op_count)
                ld      b, a
                ld      hl, (enc_op_ptr)
ovl_cm_scan:
                ld      a, (hl)
                cp      OPK_MEM
                jr      z, ovl_cm_gotmem
                push    bc
                call    enc_skip_operand
                pop     bc
                djnz    ovl_cm_scan
                jp      ovl_loud_gap                ; no OpMem (unreachable: scan hit one)
ovl_cm_gotmem:
; HL -> the OpMem operand.
                push    hl
                call    ovl_mem_expr_ptr            ; CF=1 + HL->body if expr
                jr      c, ovl_cm_haveexpr
                pop     hl
                jp      ovl_loud_gap                ; mem operand has no symbolic offset
ovl_cm_haveexpr:
; Stash the body ptr now (HL); decide the slot from mnemonic + shape.
                call    ovl_store_expr_body         ; OVL_EXPR_PTR/LEN <- body
                pop     hl                          ; HL -> OpMem operand (kind)
; isUnscaledMemMnemonic (pass2.go:728-737, {74,75,94,95,96,97}) ->
; FoldMemImm9 regardless of shape.
                ld      a, (enc_mnem + 1)
                or      a
                jr      nz, ovl_cm_byshape          ; id >= 256: never unscaled
                ld      a, (enc_mnem)
                call    ovl_is_unscaled_mem
                jr      z, ovl_cm_memimm9
ovl_cm_byshape:
; Shape decides: MemBaseOff(1) -> FoldMemImm12; Pre(2)/Post(3) -> FoldMemImm9.
                inc     hl                          ; -> shape
                ld      a, (hl)
                cp      1
                jr      z, ovl_cm_memimm12
                cp      2
                jr      z, ovl_cm_memimm9
                cp      3
                jr      z, ovl_cm_memimm9
                jp      ovl_loud_gap                ; other shapes: no symbolic enc
ovl_cm_memimm12:
                ld      a, FSID_MEM_IMM12
                ld      (OVL_SLOT), a
                jp      ovl_ok
ovl_cm_memimm9:
                ld      a, FSID_MEM_IMM9
                ld      (OVL_SLOT), a
                jp      ovl_ok

; -- ovl_classify_movimm — port of classifyMovImm (overlay.go:230-254) --
; eval ops[1].Expr -> value; is64 = ops[0]==RegX; movz when the value is
; a single 16-bit chunk (FoldMovzAuto), else orr-imm when it is a valid
; bitmask immediate (FoldLogical); else fail loudly.  Mirrors
; enc_movimm's staging (insn_encode.asm:1335-1380) so the classification
; and the encoder select the same form.
ovl_classify_movimm:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rd)
                call    enc_rd_width                ; enc_regmask := 63 (X) / 31 (W)
                ld      a, 1
                call    enc_nth_operand             ; HL -> op1 (IMM_EXPR)
                call    enc_eval_at                 ; value -> expr_result
                ld      a, (enc_regmask)
                cp      63
                jr      z, ovl_mi_scan              ; X: full 64-bit value
                xor     a                           ; W: u &= 0xffffffff
                ld      (expr_result + 4), a
                ld      (expr_result + 5), a
                ld      (expr_result + 6), a
                ld      (expr_result + 7), a
ovl_mi_scan:
                ld      hl, expr_result
                call    enc_mov_scan                ; CF=1 if single 16-bit chunk
                jr      nc, ovl_mi_trylogical
; movz-encodable -> FoldMovzAuto, expr <- op1.
                ld      a, FSID_MOVZ_AUTO
                jr      ovl_slot_a_expr_op1
ovl_mi_trylogical:
; orr-imm fallback: probe encodability via the proven fold_logical path
; (insn_fold reads the sf bit from insn_base+3 and the value from
; expr_result).  insn_fold jp-fails on a non-encodable bitmask — Go
; returns an error there too, so the hard fail IS the loud gap (see the
; file header; the boot self-test never feeds an unencodable mov).
                ld      a, (enc_regmask)
                cp      63
                ld      a, &b2                      ; orr X: sf=1
                jr      z, ovl_mi_logbase
                ld      a, &32                      ; orr W: sf=0
ovl_mi_logbase:
                ld      (insn_base + 3), a
                ld      a, FSID_LOGICAL
                call    insn_fold                   ; jp-fails if not encodable
                ld      a, FSID_LOGICAL
                ; fall through to ovl_slot_a_expr_op1

; -- Shared success tail: FoldSlot A + driving expr = operand 1 ---------
ovl_slot_a_expr_op1:
                ld      (OVL_SLOT), a
                ld      a, 1
                call    ovl_set_expr_from_op        ; expr <- op1
                jp      ovl_ok

; =======================================================================
; Helpers
; =======================================================================

; ovl_is_regxw — A = operand kind; Z if RegX or RegW (overlay.go:113/120).
ovl_is_regxw:
                cp      OPK_REG_X
                ret     z
                cp      OPK_REG_W
                ret

; ovl_set_expr_from_op — A = operand index; set OVL_EXPR_PTR/LEN from that
; IMM_EXPR operand's body.  Clobbers A, BC, HL.
ovl_set_expr_from_op:
                call    enc_nth_operand             ; HL -> opN (kind)
                inc     hl                          ; -> len_lo body
                ; fall through to ovl_store_expr_body
; ovl_store_expr_body — HL -> [len_lo][len_hi][expr]; record OVL_EXPR_PTR
; (= HL, the body ptr) and OVL_EXPR_LEN.  Clobbers A, HL.
ovl_store_expr_body:
                ld      (OVL_EXPR_PTR), hl
                ld      a, (hl)
                inc     hl
                ld      h, (hl)
                ld      l, a                        ; HL = expr len
                ld      (OVL_EXPR_LEN), hl
                ret

; ovl_mem_expr_ptr — HL -> OpMem operand (kind byte).  If its shape is
; 1/2/3 (carries an offset expr) and the expr is non-empty, CF=1 and
; HL -> the [len_lo].. body; else CF=0.  The non-empty check mirrors
; Go's `len(mem.Expr) == 0` guards (overlay.go:181/196).
; Clobbers A, HL.
ovl_mem_expr_ptr:
                inc     hl                          ; -> shape
                ld      a, (hl)
                inc     hl                          ; -> base
                cp      1
                jr      z, ovl_mep_yes
                cp      2
                jr      z, ovl_mep_yes
                cp      3
                jr      z, ovl_mep_yes
                or      a                           ; CF = 0
                ret
ovl_mep_yes:
                inc     hl                          ; -> len_lo (past base)
                ld      a, (hl)
                inc     hl
                or      (hl)                        ; len == 0?
                dec     hl                          ; HL -> len_lo again
                ret     z                           ; empty expr -> CF=0 (Z path: or cleared CF)
                scf
                ret

; ovl_fold_slot_for_kind — A = SlotKind; CF=1 + A=FoldSlot if it maps to a
; relocatable FoldSlot (FoldSlotForKind, tools/aarch64enc/overlay.go:
; 150-176), else CF=0.  Covers the generic-table relocatable slots.
; NOTE: Imm16Shifted maps to FoldMovkImm16 (hw stays in the base word) —
; deliberately different from enc_slotkind_to_fsid's FSID_MOVZ_AUTO (the
; literal encoder's auto-hw path).
ovl_fold_slot_for_kind:
                cp      SK_BR26
                jr      z, ovl_fsk_br26
                cp      SK_BR19
                jr      z, ovl_fsk_br19
                cp      SK_BR14
                jr      z, ovl_fsk_br14
                cp      SK_ADR
                jr      z, ovl_fsk_adr
                cp      SK_ADRP
                jr      z, ovl_fsk_adrp
                cp      SK_IMM12SH
                jr      z, ovl_fsk_imm12
                cp      SK_IMM16SH
                jr      z, ovl_fsk_imm16
                cp      SK_LOGICAL
                jr      z, ovl_fsk_logical
                or      a                           ; CF = 0 (no mapping)
                ret
ovl_fsk_br26:   ld      a, FSID_BRANCH26
                scf
                ret
ovl_fsk_br19:   ld      a, FSID_BRANCH19
                scf
                ret
ovl_fsk_br14:   ld      a, FSID_BRANCH14
                scf
                ret
ovl_fsk_adr:    ld      a, FSID_ADR
                scf
                ret
ovl_fsk_adrp:   ld      a, FSID_ADRP
                scf
                ret
ovl_fsk_imm12:  ld      a, FSID_ADDSUB_IMM12
                scf
                ret
ovl_fsk_imm16:  ld      a, FSID_MOVK_IMM16
                scf
                ret
ovl_fsk_logical: ld     a, FSID_LOGICAL
                scf
                ret

; ovl_fold_slot_pc_dependent — A = FoldSlot; CF=1 if PC-dependent
; (foldSlotPCDependent, overlay.go:167-174): Branch26/19/14, Adr, Adrp,
; Litpool19.  Clobbers A.
ovl_fold_slot_pc_dependent:
                cp      FSID_BRANCH26
                jr      z, ovl_pcdep_yes
                cp      FSID_BRANCH19
                jr      z, ovl_pcdep_yes
                cp      FSID_BRANCH14
                jr      z, ovl_pcdep_yes
                cp      FSID_ADR
                jr      z, ovl_pcdep_yes
                cp      FSID_ADRP
                jr      z, ovl_pcdep_yes
                cp      FSID_LITPOOL19
                jr      z, ovl_pcdep_yes
                or      a                           ; CF = 0
                ret
ovl_pcdep_yes:
                scf
                ret

; ovl_is_unscaled_mem — A = mnemonic id low byte; Z if in {74,75,94,95,
; 96,97} (isUnscaledMemMnemonic, pass2.go:728-737).  Clobbers A.
ovl_is_unscaled_mem:
                cp      74
                ret     z
                cp      75
                ret     z
                cp      94
                ret     z
                cp      95
                ret     z
                cp      96
                ret     z
                cp      97
                ret

; ovl_is_special_gap — A = mnemonic id low byte; Z if in the special-form
; loud-gap set (overlay.go:138).  Clobbers A, B, HL.
ovl_is_special_gap:
                ld      b, a
                ld      hl, ovl_special_gap_set
ovl_isg_loop:
                ld      a, (hl)
                or      a
                jr      z, ovl_isg_no               ; 0 sentinel: end of set
                cp      b
                ret     z                           ; found -> Z
                inc     hl
                jr      ovl_isg_loop
ovl_isg_no:
                or      1                           ; NZ
                ret
ovl_special_gap_set:
                defb    17, 18, 49, 50, 51, 83, 84, 47, 52
                defb    66, 67, 68, 70, 76, 77, 78, 79, 0

; -- Status exits --------------------------------------------------------
ovl_ok:
                xor     a                           ; A = 0: success
                ret
ovl_loud_gap:
                ld      a, 1                        ; A = 1: loud gap (no overlay slot)
                ret

; -----------------------------------------------------------------------
; compact_inst — port of compact.go::compactInst (252-261) +
; encodeInstOverlay (20-49).  Turn one INST record into an INSN_RUN
; element: a bare literal base word (0 patches) when fully-literal +
; PC-invariant, else a base word (relocated field zeroed) + one
; {slot, expr} overlay patch.
;
; Input:  HL = operand stream, A = operand count, DE = mnemonic id,
;         PASS_PC = the instruction's true PC.
; Output:
;   ovl_base (4 LE) = base word.
;   ovl_is_literal  = 1 (bare literal: no patch) or 0 (overlay patch).
;   On overlay (ovl_is_literal==0): OVL_SLOT / OVL_EXPR_PTR / OVL_EXPR_LEN
;     / OVL_IS_LITPOOL / OVL_LITWIDTH / OVL_RT hold the single patch.
;   A = 0 on success; A = 1 if overlay_classify hit a loud gap.
; Clobbers: everything (PASS_PC is left at the true PC).
; -----------------------------------------------------------------------
compact_inst:
                ld      (enc_mnem), de
                ld      (enc_op_count), a
                ld      (enc_op_ptr), hl
; Save the instruction's true PC: literal_word's probes overwrite PASS_PC
; (Go passes its probe PCs as encodeInst arguments; the Z80 PC is the
; PASS_PC global, so compact_inst brackets it).
                ld      hl, (PASS_PC + 0)
                ld      (ovl_pc_save + 0), hl
                ld      hl, (PASS_PC + 2)
                ld      (ovl_pc_save + 2), hl
; literal_word first (compact.go:253).
                call    literal_word                ; CF=1 + DEHL = bare word
                jr      nc, ci_overlay
; Fully-literal: store the bare word, no patch.
                ld      (ovl_base + 0), hl
                ld      (ovl_base + 2), de
                ld      a, 1
                ld      (ovl_is_literal), a
                xor     a                           ; success
                ret
ci_overlay:
; Restore the true PC: encodeInstOverlay receives it (overlay.go:20) for
; the classify (classifyMovImm's eval context) and the base-word encode.
                ld      hl, (ovl_pc_save + 0)
                ld      (PASS_PC + 0), hl
                ld      hl, (ovl_pc_save + 2)
                ld      (PASS_PC + 2), hl
                xor     a
                ld      (ovl_is_literal), a
; overlay_classify (encodeInstOverlay:21).
                ld      hl, (enc_op_ptr)
                ld      a, (enc_op_count)
                ld      de, (enc_mnem)
                call    overlay_classify            ; A=0 ok, A=1 loud gap
                or      a
                ret     nz                          ; loud gap -> propagate
; Litpool base (encodeInstOverlay:27-37): base = 0x18000000|rt, or
; 0x58000000|rt when litWidth==8.  No encode_inst call.
                ld      a, (OVL_IS_LITPOOL)
                or      a
                jr      z, ci_overlay_encode
                ld      a, (OVL_RT)
                and     &1f
                ld      (ovl_base + 0), a
                xor     a
                ld      (ovl_base + 1), a
                ld      (ovl_base + 2), a
                ld      a, (OVL_LITWIDTH)
                cp      8
                ld      a, &58                      ; X-form (width 8)
                jr      z, ci_lp_base3
                ld      a, &18                      ; W-form
ci_lp_base3:
                ld      (ovl_base + 3), a
                xor     a                           ; success
                ret
ci_overlay_encode:
; base = ZeroSlot(encode_inst(rec, pc, ...), slot) (encodeInstOverlay:
; 39-43), with PASS_PC = the restored true PC.
                ld      hl, (enc_op_ptr)
                ld      a, (enc_op_count)
                ld      de, (enc_mnem)
                call    encode_inst                 ; DEHL = full word
                ld      (ovl_base + 0), hl
                ld      (ovl_base + 2), de
                ld      a, (OVL_SLOT)
                call    enc_zero_slot               ; clear the relocated field
                xor     a                           ; success
                ret

; -----------------------------------------------------------------------
; enc_zero_slot — clear the bit-range a FoldSlot's fold writes, in place
; on ovl_base (4 LE bytes).  Port of ZeroSlot (tools/aarch64enc/
; overlay.go:180-212).  A = FoldSlot (1..13).  Clobbers A, BC, DE, HL.
;
; The clear is ovl_base[i] &= mask[i], with the 4 LE mask bytes per slot
; in ezs_mask_table (= ~fieldmask) — the bit math lives in data, not code.
; -----------------------------------------------------------------------
enc_zero_slot:
                dec     a                           ; table index = (FoldSlot-1)*4
                add     a, a
                add     a, a
                ld      c, a
                ld      b, 0
                ld      hl, ezs_mask_table
                add     hl, bc                      ; HL -> 4-byte AND-mask
                ld      de, ovl_base
                ld      b, 4
ezs_loop:
                ld      a, (de)
                and     (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    ezs_loop
                ret

; ezs_mask_table — 4-byte AND-masks (LE) that CLEAR each FoldSlot's field
; (i.e. ~fieldmask), indexed by (FoldSlot-1).  Computed from ZeroSlot
; (tools/aarch64enc/overlay.go:180-212):
;   1 Branch26   ~0x03FFFFFF          = 0xFC000000
;   2 Branch19   ~(0x7FFFF<<5)        = 0xFF00001F
;   3 Branch14   ~(0x3FFF<<5)         = 0xFFF8001F
;   4 Adr        ~((3<<29)|(0x7FFFF<<5)) = 0x9F00001F
;   5 Adrp       same as Adr          = 0x9F00001F
;   6 AddSub12   ~(0xFFF<<10)         = 0xFFC003FF   (sh@22 stays in base)
;   7 MemImm12   same as AddSub12     = 0xFFC003FF
;   8 MemImm9    ~(0x1FF<<12)         = 0xFFE00FFF
;   9 MovkImm16  ~(0xFFFF<<5)         = 0xFFE0001F   (hw stays in base)
;  10 Logical    ~(0x1FFF<<10)        = 0xFF8003FF
;  11 PairImm7   ~(0x7F<<15)          = 0xFFC07FFF
;  12 Litpool19  same as Branch19     = 0xFF00001F
;  13 MovzAuto   ~((0xFFFF<<5)|(3<<21)) = 0xFF80001F (hw computed in fold)
ezs_mask_table:
                defb    &00, &00, &00, &fc          ; 1 Branch26
                defb    &1f, &00, &00, &ff          ; 2 Branch19
                defb    &1f, &00, &f8, &ff          ; 3 Branch14
                defb    &1f, &00, &00, &9f          ; 4 Adr
                defb    &1f, &00, &00, &9f          ; 5 Adrp
                defb    &ff, &03, &c0, &ff          ; 6 AddSubImm12
                defb    &ff, &03, &c0, &ff          ; 7 MemImm12
                defb    &ff, &0f, &e0, &ff          ; 8 MemImm9
                defb    &1f, &00, &e0, &ff          ; 9 MovkImm16
                defb    &ff, &03, &80, &ff          ; 10 Logical
                defb    &ff, &7f, &c0, &ff          ; 11 PairImm7
                defb    &1f, &00, &00, &ff          ; 12 Litpool19
                defb    &1f, &00, &80, &ff          ; 13 MovzAuto

; -- Scratch + outputs (section-C RAM) ----------------------------------
ovl_is_literal: defb    0                           ; compact_inst: 1 bare / 0 overlay
ovl_gen_fs:     defb    0                           ; candidate FoldSlot (generic loop)
ovl_wa:         defb    0, 0, 0, 0                  ; literal_word: wA (4 LE bytes)
ovl_pc_save:    defb    0, 0, 0, 0                  ; compact_inst: true-PC bracket
OVL_SLOT:       defb    0                           ; chosen FoldSlot byte
OVL_EXPR_PTR:   defw    0                           ; ptr to driving expr body
OVL_EXPR_LEN:   defw    0                           ; driving expr byte length
OVL_IS_LITPOOL: defb    0                           ; 1 if litpool
OVL_LITWIDTH:   defb    0                           ; litpool element width
OVL_RT:         defb    0                           ; destination reg (litpool)
ovl_base:       defb    0, 0, 0, 0                  ; compact_inst: base word (4 LE)
