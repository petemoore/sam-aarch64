; insn_encode.asm — encode_inst: standalone aarch64 instruction encoder
; (the generic form-table path).  Item i199 / i48c-b8e brick 1.
;
; Z80 port of:
;   tools/sam-aarch64/assemble/pass2.go::encodeInst   (form-table tail,
;       lines 364-372) + operandsToValues (lines 375-423)
;   tools/aarch64enc/encode.go::Encode / encodeSlot   (lines 9-84)
;
; This is the SAM's first standalone instruction-encode capability.
; Today the Z80 only FOLDS host-computed base words ({slot,expr} patches
; in insn_run.asm pass2); for text->.tbn assembly ON the SAM the SAM
; must encode from scratch, starting from the form's fixed-bit Pattern.
;
; SCOPE (brick 1): the generic form-table path ONLY.  Compound operands
; (mem / shifted-reg / extended-reg / litpool) are dispatched BEFORE the
; form table in Go (pass2.go:227-290) and the mnemonic-specific special
; forms (lsl/lsr/bitfield/bic-imm/csetm/barriers/ror/sysreg/mov-imm-
; autoselect/ldr-lit/tbz) intercept before it (pass2.go:301-362).  None
; are handled here — encode_inst jp-fails (tagged) if it meets one.
; Later bricks add them: i203 (special forms), i201 (compound forms),
; i204 (overlay/literal).
;
; REUSE.  The per-slot encoders (src/slots/*.asm) and the fold_* helpers
; (insn_run.asm) are already proven by the .tbn fold path:
;   * register / cond / small-imm fields -> encode_reg / encode_cond /
;     encode_imm_n directly (with the form's own slot record).
;   * the expr-bearing relocatable slots (imm12 / imm16 / logical /
;     branch / adrp / adr) -> insn_fold, which already performs the
;     64-bit PC-relative arithmetic and slot encoding that
;     operandsToValues + encodeSlot do on the Go side.
; The 32-bit word is accumulated in insn_base (= Pattern initially),
; OR-ing each slot's field bits via or_dehl_into_base.  Only the low
; bits are touched by the register/cond fields, so the sf bit (bit 31)
; that fold_logical reads out of insn_base+3 stays the Pattern's.
;
; PRECONDITION: ENCTAB mapped into section A (enctab_map_in) and
; form_lookup_init already run — encode_inst reads the form table + slot
; records from ENCTAB.  The operand stream + expr bytes live in section C
; (caller-owned), unaffected by the ENCTAB window.
;
; Input:
;   HL = pointer to the operand stream (IR operand bytes)
;   A  = operand_count
;   DE = mnemonic_id (u16)
;   PASS_PC (4 bytes LE) pre-set to the instruction PC (read only by the
;          PC-relative slots; harmless otherwise).
; Output:
;   DEHL = 32-bit encoded word (L=bits0..7, H=8..15, E=16..23, D=24..31).
; Error:
;   jp fail_with_tag on no-form-match / unsupported operand / unmapped
;   slot kind (tags ENC_TAG_*).
; Clobbers: A, BC, DE, HL (and insn_base, expr_result, OPVAL_KINDS, the
;   enc_* scratch words below).
; -----------------------------------------------------------------------

; SlotKind constants (tools/aarch64enc/types.go:15-37).
SK_XREG:        equ     &01
SK_WREG:        equ     &02
SK_XREGSP:      equ     &03
SK_WREGSP:      equ     &04
SK_IMM5:        equ     &05
SK_IMM6:        equ     &06
SK_COND:        equ     &07
SK_IMM12SH:     equ     &10
SK_IMM16SH:     equ     &11
SK_BR26:        equ     &20
SK_BR19:        equ     &21
SK_BR14:        equ     &22
SK_ADRP:        equ     &23
SK_LOGICAL:     equ     &24
SK_ADR:         equ     &26

; Operand-kind discriminators (tools/sam-aarch64-format/operands.go:9-37,
; mirrored in build/gen/tbn_constants.inc).  Hardcoded here so this file
; is self-contained regardless of include order.
OPK_REG_LO:     equ     &01     ; REG_X
OPK_REG_HI:     equ     &04     ; REG_WSP (reg kinds are the &01..&04 run)
OPK_REG_X:      equ     &01     ; REG_X  (is64 marker)
OPK_REG_W:      equ     &02     ; REG_W  (32-bit marker)
OPK_IMM_EXPR:   equ     &05
OPK_COND:       equ     &0a
OPK_SYS_NAME:   equ     &0b     ; OpSysName (mrs/msr/dc/tlbi name operand)

; Self-test fail tags.
ENC_TAG_NOFORM:    equ  &e5
ENC_TAG_BADOPERAND:equ  &e6
ENC_TAG_BADSLOT:   equ  &e7

; -----------------------------------------------------------------------
encode_inst:
                ld      (enc_mnem), de
                ld      (enc_op_count), a
                ld      (enc_op_ptr), hl

; -- Pass A: build kinds[] in OPVAL_KINDS (for form_lookup_match) -------
                or      a
                jr      z, enc_after_kinds          ; 0 operands
                ld      b, a                        ; B = count
                ld      de, OPVAL_KINDS
enc_kinds_loop:
                ld      a, (hl)                     ; operand kind
                ld      (de), a
                inc     de
                push    bc
                push    de
                call    enc_skip_operand            ; HL -> next operand
                pop     de
                pop     bc
                djnz    enc_kinds_loop
enc_after_kinds:

; -- Special-form intercepts (mirror Go pass2.go:337-353 switch) --------
; lsl/lsr (17/18), bitfield (49/50/51/83/84), csetm (52), the barriers
; isb/dsb/dmb (66/67/68) and the sysreg ops mrs/msr/dc/tlbi (76/77/78/79)
; bypass the form table unconditionally; ror (70) and bic (47) only when
; their 3rd operand is an immediate (the reg forms -> RORV / BIC-shifted-
; reg go through the form table).  All other mnemonics fall through to
; the generic form-table path below.
                ld      a, (enc_mnem + 1)
                or      a
                jp      nz, enc_after_special       ; id >= 256: never special
                ld      a, (enc_mnem)
                cp      17
                jp      z, enc_lslsr
                cp      18
                jp      z, enc_lslsr
                cp      49
                jp      z, enc_bitfield
                cp      50
                jp      z, enc_bitfield
                cp      51
                jp      z, enc_bitfield
                cp      83
                jp      z, enc_bitfield
                cp      84
                jp      z, enc_bitfield
                cp      52
                jp      z, enc_csetm
                cp      66
                jp      z, enc_barrier
                cp      67
                jp      z, enc_barrier
                cp      68
                jp      z, enc_barrier
                cp      76
                jp      z, enc_sysname
                cp      77
                jp      z, enc_sysname
                cp      78
                jp      z, enc_sysname
                cp      79
                jp      z, enc_sysname
                cp      22
                jp      z, enc_tbz
                cp      23
                jp      z, enc_tbz
                cp      3
                jr      z, enc_special_imm1         ; mov: needs imm op[1]
                cp      5
                jr      z, enc_special_imm1         ; ldr Xt,label: imm op[1]
                cp      70
                jr      z, enc_special_imm2         ; ror: needs imm op[2]
                cp      47
                jr      nz, enc_after_special
enc_special_imm2:
; ror(70)/bic(47): only intercept when op[2] is an immediate.
                ld      b, a                        ; B = mnem (47 or 70)
                ld      a, (enc_op_count)
                cp      3
                jr      c, enc_after_special
                ld      a, (OPVAL_KINDS + 2)
                cp      OPK_IMM_EXPR
                jr      nz, enc_after_special
                ld      a, b
                cp      70
                jp      z, enc_ror
                jp      enc_bicimm
enc_special_imm1:
; mov(3)/ldr(5): only intercept when op[1] is an immediate (mov reg->orr
; and ldr [mem]/=litpool go through their own paths / the form table).
                ld      b, a                        ; B = mnem (3 or 5)
                ld      a, (enc_op_count)
                cp      2
                jr      c, enc_after_special
                ld      a, (OPVAL_KINDS + 1)
                cp      OPK_IMM_EXPR
                jr      nz, enc_after_special
                ld      a, b
                cp      3
                jp      z, enc_movimm
                jp      enc_ldrlit
enc_after_special:

; -- Form lookup: find first form for the mnemonic, then match kinds ----
                ld      de, (enc_mnem)
                call    form_lookup_find_first      ; HL=form, BC=count, Z
                jp      nz, enc_fail_noform
                ld      de, OPVAL_KINDS
                ld      a, (enc_op_count)
                call    form_lookup_match           ; HL=matched form, Z
                jp      nz, enc_fail_noform
                ld      (enc_form_ptr), hl

; -- Accumulator insn_base := Pattern (form header offset +3, 4 bytes) --
                ld      de, 3
                add     hl, de
                ld      de, insn_base
                ld      bc, 4
                ldir

; -- Zero operands -> the word IS the Pattern --------------------------
                ld      a, (enc_op_count)
                or      a
                jp      z, enc_emit

; -- Set up the lockstep slot/operand walk -----------------------------
                ld      (enc_remaining), a
                ld      hl, (enc_form_ptr)
                ld      de, 11
                add     hl, de                      ; -> first slot record
                ld      (enc_slot_ptr), hl
                ld      hl, (enc_op_ptr)
                ld      (enc_cur), hl

enc_slot_loop:
                ld      hl, (enc_slot_ptr)
                ld      a, (hl)                     ; A = slot kind
                cp      SK_IMM5
                jr      c, enc_do_reg               ; &01..&04 register slots
                cp      SK_COND
                jr      z, enc_do_cond              ; &07 CondCode
                cp      SK_COND
                jr      c, enc_do_imm_n             ; &05 Imm5 / &06 Imm6
                jp      enc_do_fold                 ; >=&08: expr-fold slots

; -- Register slot: value = operand's register byte (operand[1]) --------
enc_do_reg:
                ld      hl, (enc_cur)
                inc     hl
                ld      a, (hl)                     ; A = reg index
                ld      hl, (enc_slot_ptr)
                call    encode_reg
                jr      enc_field_done

; -- CondCode slot: value = operand's condition byte (operand[1]) -------
enc_do_cond:
                ld      hl, (enc_cur)
                inc     hl
                ld      a, (hl)                     ; A = cond code
                ld      hl, (enc_slot_ptr)
                call    encode_cond
                jr      enc_field_done

; -- Imm5/Imm6 slot: value = low byte of the evaluated expr -------------
enc_do_imm_n:
                call    enc_eval_cur_expr           ; -> expr_result
                ld      a, (expr_result)
                ld      hl, (enc_slot_ptr)
                call    encode_imm_n
                jr      enc_field_done

; -- Expr-bearing relocatable slot: eval, map kind->FSID, fold ----------
enc_do_fold:
                call    enc_eval_cur_expr           ; -> expr_result
                ld      hl, (enc_slot_ptr)
                ld      a, (hl)                     ; slot kind
                call    enc_slotkind_to_fsid        ; A = FSID
                call    insn_fold                   ; DEHL (reads expr_result,
                                                    ; insn_base, PASS_PC)
                ; fall through

enc_field_done:
                call    or_dehl_into_base
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
                jp      nz, enc_slot_loop

enc_emit:
                ld      a, (insn_base + 0)
                ld      l, a
                ld      a, (insn_base + 1)
                ld      h, a
                ld      a, (insn_base + 2)
                ld      e, a
                ld      a, (insn_base + 3)
                ld      d, a
                ret

; -----------------------------------------------------------------------
; enc_eval_cur_expr — evaluate the IMM_EXPR operand at enc_cur into
; expr_result.  Operand layout: [kind=&05][len u16 LE][expr bytes].
; Tail-calls eval_expr_const, whose ret returns to encode_inst's caller
; of enc_eval_cur_expr.
; -----------------------------------------------------------------------
enc_eval_cur_expr:
                ld      hl, (enc_cur)
                inc     hl                          ; -> len_lo
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = expr length
                inc     hl                          ; HL -> expr bytes
                jp      eval_expr_const

; -----------------------------------------------------------------------
; enc_skip_operand — advance HL past the operand it points at.
; Handles the brick-1 operand kinds: register (&01..&04, 1 payload byte),
; cond (&0a, 1 payload byte), imm_expr (&05, u16 len + bytes).  Any other
; kind is out of scope for the form-table path -> fail.
; Input/Output: HL.  Clobbers: A, BC.
; -----------------------------------------------------------------------
enc_skip_operand:
                ld      a, (hl)
                inc     hl                          ; -> payload
                cp      OPK_IMM_EXPR
                jr      z, enc_skip_imm
                cp      OPK_COND
                jr      z, enc_skip_one
                cp      OPK_SYS_NAME                ; len-prefixed, like imm
                jr      z, enc_skip_imm
                cp      OPK_IMM_EXPR
                jp      nc, enc_fail_unsupported_operand
                or      a
                jp      z, enc_fail_unsupported_operand
enc_skip_one:
                inc     hl
                ret
enc_skip_imm:
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                add     hl, bc
                ret

; -----------------------------------------------------------------------
; enc_slotkind_to_fsid — map an expr-bearing SlotKind to its FoldSlot id
; (insn_run.asm FSID_*).  A in = slot kind; A out = FSID.  Unmapped -> fail.
; Imm16Shifted -> FSID_MOVZ_AUTO: the auto-hw-select path matches Go's
; encodeImm16Shifted hw==0 branch (encode.go:53-65).
; -----------------------------------------------------------------------
enc_slotkind_to_fsid:
                cp      SK_IMM12SH
                jr      z, enc_fsid_addsub
                cp      SK_IMM16SH
                jr      z, enc_fsid_movz
                cp      SK_LOGICAL
                jr      z, enc_fsid_logical
                cp      SK_BR26
                jr      z, enc_fsid_br26
                cp      SK_BR19
                jr      z, enc_fsid_br19
                cp      SK_BR14
                jr      z, enc_fsid_br14
                cp      SK_ADRP
                jr      z, enc_fsid_adrp
                cp      SK_ADR
                jr      z, enc_fsid_adr
                ld      a, ENC_TAG_BADSLOT
                jp      fail_with_tag
enc_fsid_addsub: ld     a, FSID_ADDSUB_IMM12
                ret
enc_fsid_movz:  ld      a, FSID_MOVZ_AUTO
                ret
enc_fsid_logical: ld    a, FSID_LOGICAL
                ret
enc_fsid_br26:  ld      a, FSID_BRANCH26
                ret
enc_fsid_br19:  ld      a, FSID_BRANCH19
                ret
enc_fsid_br14:  ld      a, FSID_BRANCH14
                ret
enc_fsid_adrp:  ld      a, FSID_ADRP
                ret
enc_fsid_adr:   ld      a, FSID_ADR
                ret

; =======================================================================
; Special-form encoders (i203a / i48c-b8e-2 first sub-brick).
;
; Port of pass2.go::encodeLSLSR / encodeBitfieldInst / encodeRorImm.
; These bypass the form table: each computes immr/imms (or a register
; field) by pure arithmetic over the evaluated immediates, then places
; the fields into a fixed base word.  All three share enc_build_word,
; which lays out the common UBFM/BFM/SBFM/EXTR field positions:
;
;   word = base | (immr << 16) | (imms << 10) | (Rn << 5) | Rd
;
; (LSLV/LSRV reuse the same shape with immr=Rm, imms=0; ror reuses it
;  with immr=Rn, imms=shift — Rn<<16 and immr occupy the same bits.)
;
; Each handler is reached via the dispatch above with the same register
; contract as encode_inst: returns the 32-bit word in DEHL (L=byte0,
; H=byte1, E=byte2, D=byte3), or jp-fails with a tag.
; =======================================================================

; -- enc_lslsr — lsl(17)/lsr(18): reg-shift -> LSLV/LSRV, imm -> UBFM ----
enc_lslsr:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rd)
                call    enc_rd_width                ; sets enc_regmask, enc_rd
                ld      a, 1
                call    enc_nth_operand             ; HL -> op1 (Rn)
                inc     hl
                ld      a, (hl)
                ld      (enc_rn), a
                ld      a, 2
                call    enc_nth_operand             ; HL -> op2 (imm or reg)
                ld      (enc_op2ptr), hl
                ld      a, (hl)                     ; op2 kind
                cp      OPK_REG_X
                jr      z, enc_lslsr_reg
                cp      OPK_REG_W
                jr      z, enc_lslsr_reg

; imm form: compute immr/imms, base = UBFM.
                ld      hl, (enc_op2ptr)
                call    enc_eval_at                 ; shift -> expr_result
                ld      a, (expr_result)
                ld      (enc_shift), a
                ld      a, (enc_mnem)
                cp      17
                jr      z, enc_lsl_imm
; lsr: immr = shift & regmask ; imms = (regsize-1) & 0x3f
                ld      a, (enc_shift)
                ld      hl, enc_regmask
                and     (hl)
                ld      (enc_immr), a
                ld      a, (enc_regmask)
                and     &3f
                ld      (enc_imms), a
                jr      enc_lslsr_ubfm
enc_lsl_imm:
; lsl: immr = (-shift) & regmask ; imms = (regsize-1-shift) & 0x3f
                xor     a
                ld      hl, enc_shift
                sub     (hl)                        ; A = -shift
                ld      hl, enc_regmask
                and     (hl)
                ld      (enc_immr), a
                ld      a, (enc_regmask)
                ld      hl, enc_shift
                sub     (hl)                        ; regmask - shift
                and     &3f
                ld      (enc_imms), a
enc_lslsr_ubfm:
                ld      hl, enc_base_ubfm64
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_lslsr_imm_emit
                ld      hl, enc_base_ubfm32
enc_lslsr_imm_emit:
                call    enc_copy_base
                jp      enc_build_word

enc_lslsr_reg:
; reg-shift: immr = Rm, imms = 0, base = LSLV/LSRV by mnem+width.
                inc     hl
                ld      a, (hl)
                ld      (enc_immr), a               ; immr := Rm
                xor     a
                ld      (enc_imms), a
                ld      a, (enc_mnem)
                cp      17
                jr      z, enc_lslv
; lsrv
                ld      hl, enc_base_lsrv64
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_lslsr_reg_emit
                ld      hl, enc_base_lsrv32
                jr      enc_lslsr_reg_emit
enc_lslv:
                ld      hl, enc_base_lslv64
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_lslsr_reg_emit
                ld      hl, enc_base_lslv32
enc_lslsr_reg_emit:
                call    enc_copy_base
                jp      enc_build_word

; -- enc_bitfield — bfi/bfxil/ubfx/bfc/sbfx (BFM/UBFM/SBFM aliases) ------
enc_bitfield:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rd)
                call    enc_rd_width                ; enc_regmask, enc_rd
                ld      a, (enc_mnem)
                cp      83
                jr      z, enc_bf_bfc
; bfi/bfxil/ubfx/sbfx: Rn=op1, lsb=op2, width=op3
                ld      a, 1
                call    enc_nth_operand
                inc     hl
                ld      a, (hl)
                ld      (enc_rn), a
                ld      a, 2
                call    enc_nth_operand
                call    enc_eval_at                 ; lsb -> expr_result
                ld      a, (expr_result)
                ld      (enc_lsb), a
                ld      a, 3
                call    enc_nth_operand
                call    enc_eval_at                 ; width -> expr_result
                ld      a, (expr_result)
                ld      (enc_width), a
                jr      enc_bf_compute
enc_bf_bfc:
; bfc: Rn implicitly XZR (31), lsb=op1, width=op2
                ld      a, 31
                ld      (enc_rn), a
                ld      a, 1
                call    enc_nth_operand
                call    enc_eval_at
                ld      a, (expr_result)
                ld      (enc_lsb), a
                ld      a, 2
                call    enc_nth_operand
                call    enc_eval_at
                ld      a, (expr_result)
                ld      (enc_width), a
enc_bf_compute:
                ld      a, (enc_mnem)
                cp      49
                jr      z, enc_bf_fA                ; bfi
                cp      83
                jr      z, enc_bf_fA                ; bfc
; formula B (bfxil/ubfx/sbfx): immr = lsb & 0x3f ; imms = (lsb+width-1) & 0x3f
                ld      a, (enc_lsb)
                and     &3f
                ld      (enc_immr), a
                ld      a, (enc_lsb)
                ld      hl, enc_width
                add     a, (hl)
                dec     a
                and     &3f
                ld      (enc_imms), a
                jr      enc_bf_base
enc_bf_fA:
; formula A (bfi/bfc): immr = (-lsb) & regmask ; imms = (width-1) & 0x3f
                xor     a
                ld      hl, enc_lsb
                sub     (hl)
                ld      hl, enc_regmask
                and     (hl)
                ld      (enc_immr), a
                ld      a, (enc_width)
                dec     a
                and     &3f
                ld      (enc_imms), a
enc_bf_base:
; base: UBFM for ubfx(51), SBFM for sbfx(84), else BFM.
                ld      a, (enc_mnem)
                cp      51
                jr      z, enc_bf_ubfm
                cp      84
                jr      z, enc_bf_sbfm
                ld      hl, enc_base_bfm64
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_bf_emit
                ld      hl, enc_base_bfm32
                jr      enc_bf_emit
enc_bf_ubfm:
                ld      hl, enc_base_ubfm64
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_bf_emit
                ld      hl, enc_base_ubfm32
                jr      enc_bf_emit
enc_bf_sbfm:
                ld      hl, enc_base_sbfm64
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_bf_emit
                ld      hl, enc_base_sbfm32
enc_bf_emit:
                call    enc_copy_base
                jp      enc_build_word

; -- enc_ror — ror imm: EXTR Rd,Rn,Rn,#shift alias ----------------------
enc_ror:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rd)
                call    enc_rd_width
                ld      a, 1
                call    enc_nth_operand             ; HL -> op1 (Rn)
                inc     hl
                ld      a, (hl)
                ld      (enc_rn), a
                ld      (enc_immr), a               ; immr := Rn (Rn<<16)
                ld      a, 2
                call    enc_nth_operand             ; HL -> op2 (shift)
                call    enc_eval_at
                ld      a, (expr_result)
                and     &3f
                ld      (enc_imms), a               ; imms := shift
                ld      hl, enc_base_extr64
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_ror_emit
                ld      hl, enc_base_extr32
enc_ror_emit:
                call    enc_copy_base
                jp      enc_build_word

; -- enc_bicimm — bic Rd,Rn,#imm: AND Rd,Rn,#~imm (logical-imm) ----------
; Port of pass2.go::encodeBicImm.  Evaluate imm, one's-complement all 8
; bytes (Go: negImm = ^imm), seed insn_base with the AND base word (its
; sf bit drives fold_logical's 32/64-bit choice), fold the logical
; immediate via the proven encoder, then OR in Rn<<5 | Rd.
enc_bicimm:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rd)
                call    enc_rd_width                ; enc_regmask, enc_rd
                ld      a, 1
                call    enc_nth_operand             ; HL -> op1 (Rn)
                inc     hl
                ld      a, (hl)
                ld      (enc_rn), a
                ld      a, 2
                call    enc_nth_operand             ; HL -> op2 (imm)
                call    enc_eval_at                 ; imm -> expr_result
; ~imm: one's-complement the 8-byte LE result in place.
                ld      hl, expr_result
                ld      b, 8
enc_bicimm_not:
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                djnz    enc_bicimm_not
; insn_base := AND base (sf from byte3): 64=0x92000000, 32=0x12000000.
                ld      a, (enc_regmask)
                cp      63
                ld      a, &92
                jr      z, enc_bicimm_base
                ld      a, &12
enc_bicimm_base:
                ld      hl, insn_base
                ld      (hl), 0
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (hl), a                     ; byte3 = 0x92 / 0x12
; fold the logical immediate (reads insn_base+3 for sf, expr_result).
                ld      a, FSID_LOGICAL
                call    insn_fold                   ; DEHL = N:immr:imms bits
                call    or_dehl_into_base
; OR in the register field: (Rn << 5) | Rd  (bits 0..9).
                ld      a, (enc_rn)
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; Rn << 5
                ld      a, (enc_rd)
                or      l
                ld      l, a                        ; HL = (Rn << 5) | Rd
                ld      de, 0                        ; byte2/byte3 untouched
                call    or_dehl_into_base
                jp      enc_emit_base

; -- enc_emit_base — load the 4-byte insn_base into DEHL (L=byte0..D=3) --
enc_emit_base:
                ld      a, (insn_base + 0)
                ld      l, a
                ld      a, (insn_base + 1)
                ld      h, a
                ld      a, (insn_base + 2)
                ld      e, a
                ld      a, (insn_base + 3)
                ld      d, a
                ret

; -- enc_ldrlit — ldr Xt/Wt, label: PC-relative imm19 (FSID_BRANCH19) ----
; Port of pass2.go::encodeLdrLitDirect.  base 0x58000000 (X) / 0x18000000
; (W), imm19 = (label-PC)/4 placed at bits 5..23 by the shared branch fold.
enc_ldrlit:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rt)
                call    enc_rd_width                ; enc_regmask, enc_rd
; insn_base = base | Rt  (byte0 = Rt, byte3 = 0x58 / 0x18).
                ld      a, (enc_rd)
                and     &1f
                ld      (insn_base + 0), a
                xor     a
                ld      (insn_base + 1), a
                ld      (insn_base + 2), a
                ld      a, (enc_regmask)
                cp      63
                ld      a, &58
                jr      z, enc_ldrlit_b
                ld      a, &18
enc_ldrlit_b:
                ld      (insn_base + 3), a
                ld      a, 1
                call    enc_nth_operand             ; HL -> op1 (label)
                call    enc_eval_at                 ; target -> expr_result
                ld      a, FSID_BRANCH19
                call    insn_fold                   ; DEHL = imm19 bits @5
                call    or_dehl_into_base
                jp      enc_emit_base

; -- enc_tbz — tbz(22)/tbnz(23) Rt,#bit,label: imm14 (FSID_BRANCH14) -----
; Port of pass2.go::encodeTbzTbnz.  word = (b5<<31)|(0b011011<<25)|
; (op<<24)|(b40<<19)|imm14<<5|Rt; imm14 = (label-PC)/4 via the branch fold.
enc_tbz:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rt)
                call    enc_rd_width                ; enc_rd = Rt
                ld      a, 1
                call    enc_nth_operand             ; HL -> op1 (bit)
                call    enc_eval_at                 ; bit -> expr_result
                ld      a, (expr_result)            ; bit number 0..63
                ld      c, a
; byte0 = Rt; byte1 = 0; byte2 = (b40 << 3); byte3 = (b5<<7)|0x36|op.
                ld      a, (enc_rd)
                and     &1f
                ld      (insn_base + 0), a
                xor     a
                ld      (insn_base + 1), a
                ld      a, c
                and     &1f                         ; b40 = bit & 0x1f
                add     a, a
                add     a, a
                add     a, a                        ; b40 << 3
                ld      (insn_base + 2), a
; b5 = (bit >> 5) & 1  -> byte3 bit7.
                ld      a, c
                and     &20                         ; bit 5 set?
                jr      z, enc_tbz_b5z
                ld      a, &80
enc_tbz_b5z:
                ld      b, a                        ; B = b5<<7
                ld      a, (enc_mnem)
                cp      23                          ; tbnz -> op=1 (bit24 -> byte3 bit0)
                ld      a, &36
                jr      nz, enc_tbz_op0
                or      &01
enc_tbz_op0:
                or      b                           ; | b5<<7
                ld      (insn_base + 3), a
                ld      a, 2
                call    enc_nth_operand             ; HL -> op2 (label)
                call    enc_eval_at                 ; target -> expr_result
                ld      a, FSID_BRANCH14
                call    insn_fold                   ; DEHL = imm14 bits @5
                call    or_dehl_into_base
                jp      enc_emit_base

; -- enc_movimm — mov Rd,#imm autoselect (movz / movn / orr-imm) --------
; Port of pass2.go::tryEncodeMovImm.  Try movz (one 16-bit chunk), then
; movn (~value one chunk), then orr-imm (logical-immediate).  Faithful
; to GNU's single-instruction selection.
enc_movimm:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rd)
                call    enc_rd_width                ; enc_regmask, enc_rd
                ld      a, 1
                call    enc_nth_operand             ; HL -> op1 (imm)
                call    enc_eval_at                 ; imm -> expr_result
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_mov_movz
                xor     a                           ; W: mask to low 32 bits
                ld      (expr_result + 4), a
                ld      (expr_result + 5), a
                ld      (expr_result + 6), a
                ld      (expr_result + 7), a
enc_mov_movz:
                ld      hl, expr_result
                call    enc_mov_scan
                jr      nc, enc_mov_trymovn
                ld      a, (enc_regmask)
                cp      63
                ld      a, &d2                      ; movz X
                jr      z, enc_mov_emit
                ld      a, &52                      ; movz W
                jr      enc_mov_emit
enc_mov_trymovn:
                ld      hl, expr_result             ; nu = ~value into enc_nu
                ld      de, enc_nu
                ld      b, 8
enc_mov_not:
                ld      a, (hl)
                cpl
                ld      (de), a
                inc     hl
                inc     de
                djnz    enc_mov_not
                ld      a, (enc_regmask)
                cp      63
                jr      z, enc_mov_scn
                xor     a
                ld      (enc_nu + 4), a
                ld      (enc_nu + 5), a
                ld      (enc_nu + 6), a
                ld      (enc_nu + 7), a
enc_mov_scn:
                ld      hl, enc_nu
                call    enc_mov_scan
                jr      nc, enc_mov_orr
                ld      a, (enc_regmask)
                cp      63
                ld      a, &92                      ; movn X
                jr      z, enc_mov_emit
                ld      a, &12                      ; movn W
enc_mov_emit:
; A = byte3 base; build word = base | (hw<<21) | (chunk<<5) | Rd.
                ld      (insn_base + 3), a
                ld      a, (enc_mov_chunk + 0)      ; byte0 = Rd | (Clo&7)<<5
                and     &07
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                ld      b, a
                ld      a, (enc_rd)
                and     &1f
                or      b
                ld      (insn_base + 0), a
                ld      a, (enc_mov_chunk + 0)      ; byte1 = (Clo>>3)|((Chi&7)<<5)
                srl     a
                srl     a
                srl     a
                ld      b, a
                ld      a, (enc_mov_chunk + 1)
                and     &07
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                or      b
                ld      (insn_base + 1), a
                ld      a, (enc_mov_chunk + 1)      ; byte2 = 0x80|(hw<<5)|(Chi>>3)
                srl     a
                srl     a
                srl     a
                ld      b, a
                ld      a, (enc_mov_hw)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                or      b
                or      &80
                ld      (insn_base + 2), a
                jp      enc_emit_base
enc_mov_orr:
; orr Rd, ZR, #imm — base 0xb20003e0 (X) / 0x320003e0 (W); fold logical
; imm (expr_result holds the original value) then OR Rd in.
                ld      a, (enc_regmask)
                cp      63
                ld      a, &b2
                jr      z, enc_mov_orr_b
                ld      a, &32
enc_mov_orr_b:
                ld      (insn_base + 3), a
                xor     a
                ld      (insn_base + 1), a
                ld      (insn_base + 2), a
                ld      a, &e0                      ; Rn = XZR (bits 5..9)
                ld      (insn_base + 0), a
                ld      a, FSID_LOGICAL
                call    insn_fold
                call    or_dehl_into_base
                ld      a, (enc_rd)
                and     &1f
                ld      hl, insn_base + 0
                or      (hl)
                ld      (hl), a
                jp      enc_emit_base

; -- enc_mov_scan — HL=8-byte LE value; find a single nonzero 16-bit -----
; chunk.  enc_regmask picks 4 (X) or 2 (W) chunk slots.  On success: carry
; set, enc_mov_hw = chunk index, enc_mov_chunk = the 2 chunk bytes.  On
; failure (>=2 nonzero chunk slots): carry clear.
enc_mov_scan:
                push    hl
                ld      a, (enc_regmask)
                cp      63
                ld      b, 4
                jr      z, enc_ms_n
                ld      b, 2
enc_ms_n:
                ld      c, 0                        ; nonzero-slot count
                ld      d, &ff                      ; candidate hw (none yet)
                ld      e, 0                        ; current index
enc_ms_loop:
                ld      a, (hl)
                inc     hl
                or      (hl)
                inc     hl
                jr      z, enc_ms_next
                inc     c
                ld      d, e
enc_ms_next:
                inc     e
                djnz    enc_ms_loop
                pop     hl                          ; HL = value base
                ld      a, c
                cp      2
                jr      nc, enc_ms_fail
; hw = d, except d==0xff (all-zero value) -> hw 0.
                ld      a, d
                inc     a                           ; 0xff -> 0 (sets Z)
                jr      nz, enc_ms_usehw
                xor     a                           ; hw = 0
                jr      enc_ms_store
enc_ms_usehw:
                ld      a, d
enc_ms_store:
                ld      (enc_mov_hw), a
                add     a, a                        ; 2*hw
                ld      e, a
                ld      d, 0
                add     hl, de                      ; HL -> chunk bytes
                ld      a, (hl)
                ld      (enc_mov_chunk + 0), a
                inc     hl
                ld      a, (hl)
                ld      (enc_mov_chunk + 1), a
                scf
                ret
enc_ms_fail:
                or      a
                ret

; -- enc_csetm — csetm Rd,cond: csinv Rd,xzr,xzr,!cond ------------------
; Port of pass2.go::encodeCsetm.  word = base | (~cond << 12) | Rd, with
; Rn=Rm=xzr baked into the base (0x..9f03e0).
enc_csetm:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (Rd)
                call    enc_rd_width                ; enc_regmask, enc_rd
                ld      a, 1
                call    enc_nth_operand             ; HL -> op1 (cond)
                inc     hl
                ld      a, (hl)                     ; cond code
                xor     &01                         ; invert: cond ^ 1
                rrca
                rrca
                rrca
                rrca                                ; (~cond) << 4 within byte1
                ld      c, a                        ; C = byte1 cond bits
; byte0 = base0(0xe0) | Rd
                ld      a, (enc_rd)
                and     &1f
                or      &e0
                ld      l, a
; byte1 = 0x03 | (~cond << 4)
                ld      a, c
                or      &03
                ld      h, a
; byte2/byte3 by width: 64=0xda9f.., 32=0x5a9f..
                ld      e, &9f
                ld      a, (enc_regmask)
                cp      63
                ld      d, &da
                ret     z
                ld      d, &5a
                ret

; -- enc_barrier — isb/dsb/dmb: base | (CRm << 8) -----------------------
; Port of pass2.go::encodeBarrierInst.  Single immediate operand = CRm
; (bits 11:8).  Bases: isb 0xd50330df, dsb 0xd503309f, dmb 0xd50330bf.
enc_barrier:
                xor     a
                call    enc_nth_operand             ; HL -> op0 (CRm imm)
                call    enc_eval_at                 ; CRm -> expr_result
                ld      a, (enc_mnem)
                cp      66
                ld      l, &df                      ; isb byte0
                jr      z, enc_barrier_crm
                cp      67
                ld      l, &9f                      ; dsb byte0
                jr      z, enc_barrier_crm
                ld      l, &bf                      ; dmb byte0
enc_barrier_crm:
                ld      a, (expr_result)
                and     &0f
                or      &30                         ; byte1 = 0x30 | CRm
                ld      h, a
                ld      e, &03
                ld      d, &d5
                ret

; -- enc_sysname — mrs/msr/dc/tlbi: stage stream -> OPVAL_ARRAY, reuse ---
; the proven sysname encoders (sysname.asm).  Those read OPVAL_ARRAY +
; main_op_count and resolve names through the paged sysreg tables
; (sysname_lookup_* snapshot/restore the live LMPR, so this composes with
; the ENCTAB window encode_inst runs in).  They return DE:HL with the
; same byte order encode_inst yields (L=byte0..D=byte3), so we tail-call.
;
; Port of pass2.go::encodeMrs/encodeMsr/encodeDc/encodeTlbi — the per-name
; field math lives in the existing encoders; this is the stream->OPVAL
; staging the .tbn path's parser does on the other side.
enc_sysname:
                ld      hl, OPVAL_ARRAY             ; wipe the entries we touch
                ld      b, 30
                xor     a
enc_sn_wipe:
                ld      (hl), a
                inc     hl
                djnz    enc_sn_wipe
                ld      a, (enc_op_count)
                ld      (main_op_count), a
                or      a
                jp      z, enc_sn_dispatch
                ld      (enc_remaining), a
                ld      hl, (enc_op_ptr)
                ld      ix, OPVAL_ARRAY
enc_sn_loop:
                ld      a, (hl)                     ; operand kind
                ld      (ix + 0), a
                cp      OPK_SYS_NAME
                jr      z, enc_sn_name
                cp      OPK_IMM_EXPR
                jr      z, enc_sn_imm
; register / cond: single payload byte -> OPVAL[i]+1.
                inc     hl
                ld      a, (hl)
                ld      (ix + 1), a
                inc     hl
                jr      enc_sn_next
enc_sn_name:
; [0b][len_lo][len_hi][name bytes]: store name ptr -> +2/+3, len -> +4/+5.
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = name length
                inc     hl                          ; HL -> name bytes
                ld      (ix + 4), c
                ld      (ix + 5), b
                ld      (ix + 2), l
                ld      (ix + 3), h
                add     hl, bc                      ; skip the name bytes
                jr      enc_sn_next
enc_sn_imm:
; [05][len_lo][len_hi][expr]: eval -> expr_result, copy 8 LE bytes -> +2.
                push    hl                          ; operand start
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = expr length
                inc     hl
                add     hl, bc                      ; HL -> next operand
                ld      (enc_sn_next_ptr), hl
                pop     hl                          ; HL -> operand start
                push    ix
                call    enc_eval_at                 ; -> expr_result
                pop     ix
                push    ix
                pop     de
                inc     de
                inc     de                          ; DE = OPVAL[i]+2
                ld      hl, expr_result
                ld      b, 8
enc_sn_imm_cp:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    enc_sn_imm_cp
                ld      hl, (enc_sn_next_ptr)
enc_sn_next:
                ld      de, OPVAL_STRIDE
                add     ix, de
                ld      a, (enc_remaining)
                dec     a
                ld      (enc_remaining), a
                jp      nz, enc_sn_loop
enc_sn_dispatch:
                ld      a, (enc_mnem)
                cp      76
                jp      z, encode_mrs_word
                cp      77
                jp      z, encode_msr_word
                cp      78
                jp      z, encode_dc_word
                jp      encode_tlbi_word            ; 79

; -- enc_rd_width — HL -> Rd operand; set enc_regmask (63/31) + enc_rd ---
; is64 iff Rd kind != OpRegW.  Preserves nothing; returns with HL past Rd.
enc_rd_width:
                ld      a, (hl)                     ; Rd kind
                cp      OPK_REG_W
                ld      a, 63
                jr      nz, enc_rd_width_store
                ld      a, 31
enc_rd_width_store:
                ld      (enc_regmask), a
                inc     hl
                ld      a, (hl)                     ; Rd reg
                ld      (enc_rd), a
                ret

; -- enc_nth_operand — A = operand index; returns HL -> that operand ----
enc_nth_operand:
                ld      hl, (enc_op_ptr)
                or      a
                ret     z
                ld      b, a
enc_nth_loop:
                push    bc                          ; enc_skip_operand clobbers BC
                call    enc_skip_operand
                pop     bc
                djnz    enc_nth_loop
                ret

; -- enc_eval_at — HL -> IMM_EXPR operand; evaluate into expr_result ----
; Operand layout: [kind=&05][len u16 LE][expr bytes].  Tail-calls
; eval_expr_const (returns to enc_eval_at's caller).
enc_eval_at:
                inc     hl                          ; -> len_lo
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = expr length
                inc     hl                          ; HL -> expr bytes
                jp      eval_expr_const

; -- enc_copy_base — copy 4 base bytes (HL) into enc_base ---------------
enc_copy_base:
                ld      de, enc_base
                ld      bc, 4
                ldir
                ret

; -- enc_build_word — assemble DEHL from enc_base + rd/rn/immr/imms ------
;   byte0 = base0 | (Rd & 0x1f) | ((Rn & 7) << 5)
;   byte1 = base1 | ((Rn >> 3) & 3) | ((imms & 0x3f) << 2)
;   byte2 = base2 | (immr & 0x3f)
;   byte3 = base3
enc_build_word:
                ld      a, (enc_rn)
                and     &07
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                        ; (Rn & 7) << 5
                ld      c, a
                ld      a, (enc_rd)
                and     &1f
                or      c
                ld      hl, enc_base + 0
                or      (hl)
                ld      (enc_word + 0), a           ; byte0

                ld      a, (enc_imms)
                and     &3f
                add     a, a
                add     a, a                        ; (imms & 0x3f) << 2
                ld      c, a
                ld      a, (enc_rn)
                rrca
                rrca
                rrca
                and     &03                         ; (Rn >> 3) & 3
                or      c
                ld      hl, enc_base + 1
                or      (hl)
                ld      (enc_word + 1), a           ; byte1

                ld      a, (enc_immr)
                and     &3f
                ld      hl, enc_base + 2
                or      (hl)
                ld      (enc_word + 2), a           ; byte2

                ld      a, (enc_base + 3)
                ld      (enc_word + 3), a           ; byte3

                ld      a, (enc_word + 0)
                ld      l, a
                ld      a, (enc_word + 1)
                ld      h, a
                ld      a, (enc_word + 2)
                ld      e, a
                ld      a, (enc_word + 3)
                ld      d, a
                ret

enc_fail_noform:
                ld      a, ENC_TAG_NOFORM
                jp      fail_with_tag
enc_fail_unsupported_operand:
                ld      a, ENC_TAG_BADOPERAND
                jp      fail_with_tag

; -- Special-form base words (LE: byte0, byte1, byte2, byte3) -----------
; UBFM/BFM/SBFM aliases (lsl/lsr/bitfield), EXTR (ror), LSLV/LSRV (reg
; shift).  Values from pass2.go::encodeLSLSR/encodeBitfieldInst/
; encodeRorImm — the encoding authority (CLAUDE.md §6).
enc_base_ubfm64: defb   &00, &00, &40, &d3          ; 0xd3400000
enc_base_ubfm32: defb   &00, &00, &00, &53          ; 0x53000000
enc_base_bfm64:  defb   &00, &00, &40, &b3          ; 0xb3400000
enc_base_bfm32:  defb   &00, &00, &00, &33          ; 0x33000000
enc_base_sbfm64: defb   &00, &00, &40, &93          ; 0x93400000
enc_base_sbfm32: defb   &00, &00, &00, &13          ; 0x13000000
enc_base_extr64: defb   &00, &00, &c0, &93          ; 0x93c00000
enc_base_extr32: defb   &00, &00, &80, &13          ; 0x13800000
enc_base_lslv64: defb   &00, &20, &c0, &9a          ; 0x9ac02000
enc_base_lslv32: defb   &00, &20, &c0, &1a          ; 0x1ac02000
enc_base_lsrv64: defb   &00, &24, &c0, &9a          ; 0x9ac02400
enc_base_lsrv32: defb   &00, &24, &c0, &1a          ; 0x1ac02400

; -- Scratch (section-C RAM) -------------------------------------------
enc_mnem:       defw    0
enc_op_count:   defb    0
enc_op_ptr:     defw    0
enc_form_ptr:   defw    0
enc_slot_ptr:   defw    0
enc_cur:        defw    0
enc_remaining:  defb    0
enc_regmask:    defb    0       ; 63 (is64) or 31 (32-bit) = regsize-1
enc_rd:         defb    0
enc_rn:         defb    0
enc_immr:       defb    0
enc_imms:       defb    0
enc_shift:      defb    0
enc_lsb:        defb    0
enc_width:      defb    0
enc_op2ptr:     defw    0
enc_sn_next_ptr: defw   0
enc_mov_hw:     defb    0
enc_mov_chunk:  defb    0, 0
enc_nu:         defb    0, 0, 0, 0, 0, 0, 0, 0
enc_base:       defb    0, 0, 0, 0
enc_word:       defb    0, 0, 0, 0
