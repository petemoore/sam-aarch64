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
OPK_IMM_EXPR:   equ     &05
OPK_COND:       equ     &0a

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
                cp      OPK_IMM_EXPR
                jr      nc, enc_fail_unsupported_operand
                or      a
                jr      z, enc_fail_unsupported_operand
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

enc_fail_noform:
                ld      a, ENC_TAG_NOFORM
                jp      fail_with_tag
enc_fail_unsupported_operand:
                ld      a, ENC_TAG_BADOPERAND
                jp      fail_with_tag

; -- Scratch (section-C RAM) -------------------------------------------
enc_mnem:       defw    0
enc_op_count:   defb    0
enc_op_ptr:     defw    0
enc_form_ptr:   defw    0
enc_slot_ptr:   defw    0
enc_cur:        defw    0
enc_remaining:  defb    0
