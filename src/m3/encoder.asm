; encoder.asm — top-level instruction encoder for M3.
;
; Walks a matched form's slot list and dispatches to the appropriate
; per-slot encoder, OR-ing each result into a 32-bit accumulator
; initialised to form.Pattern.
;
; Z80 port of tools/aarch64enc/encode.go::Encode + encodeSlot.
;
; Operand-value extraction model:
;
; M3 only handles a small subset of operand kinds (M3 spec §1):
;   OpRegX (0x01), OpRegW (0x02), OpRegXSP (0x03), OpRegWSP (0x04),
;   OpImmExpr (0x05), OpCond (0x0A)
;
; Compound shapes (OpShiftedReg, OpExtendedReg, OpMem, OpString, OpSysName,
; OpLitPool) are M4 territory and the encoder rejects them.
;
; ---------------------------------------------------------------------
; encode_inst — encode one INST record.
;
; Input:
;   HL = pointer to the matched form's header (the 11-byte block:
;        mnemonic_id u16, operand_count u8, pattern u32, mask u32,
;        followed by N*4 slot records).
;   DE = pointer to the START of the parsed operand value array
;        (each entry: [kind u8][8 bytes value-storage][...] — see
;        the per-operand layout in `parse_operands` below).
;   A  = number of operand values.
;
; Output:
;   The 32-bit instruction word is placed (little-endian) into the
;   output buffer via emit_word_at_pc.  PC is advanced by 4.
;
; Errors:  jp fail.
;
; Clobbers: A, BC, DE, HL.  Other slot encoders also clobber AF/BC/DE/HL.
; ---------------------------------------------------------------------

; ---------------------------------------------------------------------
; Operand value record layout (one entry per parsed operand).
;
;   offset 0   kind byte (matches format.OperandKind)
;   offset 1   reg byte (if reg-kind)
;   offset 2-9 8-byte LE evaluator result (if OpImmExpr) OR cond byte
;              padded out.  We always reserve 10 bytes per operand so
;              array indexing is trivial.
;
; The encoder reads only the fields it needs based on the kind byte.
; ---------------------------------------------------------------------
OPVAL_STRIDE:           equ     10

encode_inst:
                ld      (encoder_form_ptr), hl
                ld      (encoder_op_array), de
                ld      (encoder_op_count), a

; -- Initialise accumulator with form.Pattern (HL+3..HL+6) --------------
                push    hl
                inc     hl
                inc     hl
                inc     hl                  ; HL → pattern bytes
                ld      a, (hl)
                ld      (encoder_acc + 0), a
                inc     hl
                ld      a, (hl)
                ld      (encoder_acc + 1), a
                inc     hl
                ld      a, (hl)
                ld      (encoder_acc + 2), a
                inc     hl
                ld      a, (hl)
                ld      (encoder_acc + 3), a
                pop     hl

; -- Pre-compute is64 flag from accumulator's bit 31 (sf) ---------------
                ld      a, (encoder_acc + 3)
                and     &80
                ld      (encoder_is64), a   ; 0 = 32-bit, 0x80 = 64-bit

; -- Walk slots, dispatching by kind -----------------------------------
                ld      a, (encoder_op_count)
                or      a
                jp      z, encode_inst_emit       ; zero operands → emit pattern as-is

                ld      b, a                ; B = remaining slots/operands
                ld      (encoder_slot_idx), a

                push    hl
                ld      de, 11
                add     hl, de              ; HL → first slot record
                ld      (encoder_slot_ptr), hl
                pop     hl

                ld      hl, (encoder_op_array)
                ld      (encoder_opval_ptr), hl

encode_inst_loop:
                ld      a, (encoder_slot_idx)
                or      a
                jp      z, encode_inst_emit

; -- Fetch this slot's slot_kind byte ----------------------------------
                ld      hl, (encoder_slot_ptr)
                ld      a, (hl)             ; A = slot_kind

; -- Dispatch on slot_kind ---------------------------------------------
; All dispatch targets are required to leave DEHL = 32-bit encoded bits.
; The slot encoder also consumes HL (slot record ptr), so we save it
; via encoder_slot_ptr before the call so we can advance after.
                cp      &01
                jp      z, encode_slot_xreg
                cp      &02
                jp      z, encode_slot_wreg
                cp      &03
                jp      z, encode_slot_xreg          ; XregOrSp same body
                cp      &04
                jp      z, encode_slot_wreg          ; WregOrSp same body
                cp      &05
                jp      z, encode_slot_imm5
                cp      &06
                jp      z, encode_slot_imm6
                cp      &07
                jp      z, encode_slot_cond
                cp      &10
                jp      z, encode_slot_imm12shifted
                cp      &11
                jp      z, encode_slot_imm16shifted
                cp      &12
                jp      z, encode_slot_shamt
                cp      &13
                jp      z, encode_slot_extendop
                cp      &20
                jp      z, encode_slot_branch
                cp      &21
                jp      z, encode_slot_branch
                cp      &22
                jp      z, encode_slot_branch
                cp      &23
                jp      z, encode_slot_adrp
                cp      &24
                jp      z, encode_slot_logimm
                cp      &25
                jp      z, encode_slot_bitfield      ; rejected in M3 by reader
                cp      &26
                jp      z, encode_slot_adr
                jp      fail


; -- Per-slot encoders dispatch tail-call back into the loop -----------
; Each handler:
;   1. Reads the parsed operand value via encoder_opval_ptr.
;   2. Calls the relevant encode_* routine (DEHL ← bits).
;   3. ORs DEHL into encoder_acc.
;   4. Advances encoder_slot_ptr and encoder_opval_ptr.
;   5. Decrements encoder_slot_idx and jp encode_inst_loop.

; Common tail after DEHL has the encoded bits.
;
; CAREFUL: DEHL holds the result; `ld hl, encoder_acc+N` would clobber H/L.
; Save D/H separately first, then OR L+E into acc[0]+acc[2] before
; overwriting HL.
encode_slot_or_and_next:
                push    de                  ; preserve D:E (acc[2..3])
                push    hl                  ; preserve H:L (acc[0..1])

                ld      a, l
                ld      hl, encoder_acc + 0
                or      (hl)
                ld      (hl), a

                pop     bc                  ; BC = saved HL (B=H, C=L)
                ld      a, b                ; A = H (acc[1] contributor)
                ld      hl, encoder_acc + 1
                or      (hl)
                ld      (hl), a

                pop     bc                  ; BC = saved DE (B=D, C=E)
                ld      a, c                ; A = E (acc[2] contributor)
                ld      hl, encoder_acc + 2
                or      (hl)
                ld      (hl), a

                ld      a, b                ; A = D (acc[3] contributor)
                ld      hl, encoder_acc + 3
                or      (hl)
                ld      (hl), a

; Advance slot ptr by 4.
                ld      hl, (encoder_slot_ptr)
                ld      bc, 4
                add     hl, bc
                ld      (encoder_slot_ptr), hl

; Advance opval ptr by OPVAL_STRIDE.
                ld      hl, (encoder_opval_ptr)
                ld      bc, OPVAL_STRIDE
                add     hl, bc
                ld      (encoder_opval_ptr), hl

                ld      a, (encoder_slot_idx)
                dec     a
                ld      (encoder_slot_idx), a
                jp      encode_inst_loop


; -- Emit accumulator as 4 LE bytes, advance PC ------------------------
encode_inst_emit:
                ld      a, (encoder_acc + 0)
                call    emit_byte
                ld      a, (encoder_acc + 1)
                call    emit_byte
                ld      a, (encoder_acc + 2)
                call    emit_byte
                ld      a, (encoder_acc + 3)
                call    emit_byte
                ret


; ---------------------------------------------------------------------
; Per-slot dispatch handlers.
;
; All handlers read the relevant fields from the parsed operand value
; record at (encoder_opval_ptr) and invoke the slot encoder with the
; right calling convention.
; ---------------------------------------------------------------------

; OpRegX (0x01) → expected kind OpRegX (0x01), reg byte at +1.
encode_slot_xreg:
                ld      hl, (encoder_opval_ptr)
                inc     hl                  ; HL → reg byte
                ld      a, (hl)             ; A = reg
                ld      hl, (encoder_slot_ptr)
                call    encode_reg
                jp      encode_slot_or_and_next

encode_slot_wreg:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                ld      a, (hl)
                ld      hl, (encoder_slot_ptr)
                call    encode_reg
                jp      encode_slot_or_and_next


; OpImmExpr (0x05) → expected kind 0x05.  Value byte at +2..+9 is the
; evaluator's 8-byte LE result.  For Imm5/Imm6/Shamt the slot encoder
; takes A=value (low byte; range-check enforces fit).
encode_slot_imm5:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                inc     hl
                ld      a, (hl)             ; A = bits 0..7 of the imm
                ld      hl, (encoder_slot_ptr)
                call    encode_imm_n
                jp      encode_slot_or_and_next

encode_slot_imm6:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                inc     hl
                ld      a, (hl)
                ld      hl, (encoder_slot_ptr)
                call    encode_imm_n
                jp      encode_slot_or_and_next

encode_slot_shamt:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                inc     hl
                ld      a, (hl)
                ld      hl, (encoder_slot_ptr)
                call    encode_imm_n
                jp      encode_slot_or_and_next


; OpCond (0x0A) → cond byte stored at +2.
encode_slot_cond:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                inc     hl
                ld      a, (hl)
                ld      hl, (encoder_slot_ptr)
                call    encode_cond
                jp      encode_slot_or_and_next


; Imm12Shifted — wide BCDE big-endian.  Repack bytes +2..+5 (LE) into BCDE.
encode_slot_imm12shifted:
                call    encoder_load_imm_bcde
                ld      hl, (encoder_slot_ptr)
                call    encode_imm12_shifted
                jp      encode_slot_or_and_next


; Imm16Shifted — DE=imm16, A=hw.  We feed imm low 16 bits as DE;
; hw is extracted from bits [17:16] of the imm (matching encode.go's
; "MOV/MOVK/MOVZ/MOVN: hw lives in bits [17:16] of v.Imm").
encode_slot_imm16shifted:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                inc     hl                  ; HL → byte 0 of LE value
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = low 16 bits = imm
                inc     hl
                ld      a, (hl)             ; A = byte 2: contains bits 16..23
                and     &03                 ; A = (v >> 16) & 0x3 = hw
                ld      hl, (encoder_slot_ptr)
                call    encode_imm16_shifted
                jp      encode_slot_or_and_next


; ExtendOp — A=ext, C=shift.  M3 fixtures don't exercise this slot
; (parser surfaces ExtendOp via OpExtendedReg which we reject), but
; we wire the dispatch entry for completeness.  Operand-value record
; for ExtendOp under our scheme would be:
;   +0 = kind, +1 = ext, +2 = shift.  No M3 fixture goes through this.
encode_slot_extendop:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                ld      a, (hl)             ; A = ext
                inc     hl
                ld      c, (hl)             ; C = shift
                ld      hl, (encoder_slot_ptr)
                call    encode_extend_op
                jp      encode_slot_or_and_next


; BranchImm26/19/14 — wide BCDE big-endian signed.
; M4 semantics: BCDE is the ABSOLUTE target address (resolved by the
; expression evaluator from a label / PC-relative expression).
; encode_branch_imm subtracts PASS_PC internally before applying its
; range-check + bit-pack body.  M3 fixtures don't reach this dispatch
; entry; M4 fixtures (PR 3) exercise it end-to-end.
encode_slot_branch:
                call    encoder_load_imm_bcde
                ld      hl, (encoder_slot_ptr)
                call    encode_branch_imm
                jp      encode_slot_or_and_next


; AdrpImm — wide BCDE big-endian signed.
; M4 semantics: BCDE is the ABSOLUTE target address.  encode_adrp_imm
; masks both target and PASS_PC to their 4 KB page bases and subtracts
; before applying its range-check + bit-pack body.
encode_slot_adrp:
                call    encoder_load_imm_bcde
                ld      hl, (encoder_slot_ptr)
                call    encode_adrp_imm
                jp      encode_slot_or_and_next


; AdrImm (0x26) — same calling convention as AdrpImm but routes to
; encode_adr_imm (slots/adrp_imm.asm).  ADR uses a raw PC-relative byte
; offset (no page masking, no >>12), range ±1 MB.  Needed by the
; spectrum4 release source (e.g. `adr x1, mailbox_base`).
encode_slot_adr:
                call    encoder_load_imm_bcde
                ld      hl, (encoder_slot_ptr)
                call    encode_adr_imm
                jp      encode_slot_or_and_next


; LogicalImm — DE=ptr to 8-byte LE buffer, C=is64.
encode_slot_logimm:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                inc     hl                  ; HL → byte 0 of LE 8-byte value
                ex      de, hl              ; DE = pointer to 8-byte LE buffer
                ld      a, (encoder_is64)
                ld      c, 0
                or      a
                jr      z, encode_slot_logimm_call
                ld      c, 1
encode_slot_logimm_call:
                ld      hl, (encoder_slot_ptr)
                call    encode_logical_imm
                jp      encode_slot_or_and_next


; BitfieldImm — two slots, two values.  M3 fixtures don't exercise
; arbitrary bitfield instructions; bfi/ubfx in refenc are handled by a
; mnemonic-id intercept.  For now, hard-error.
encode_slot_bitfield:
                jp      fail


; ---------------------------------------------------------------------
; Helper: encoder_load_imm_bcde — read the 8-byte LE result at
; (encoder_opval_ptr + 2) and pack the LOW 32 bits into BCDE big-endian
; (B = byte 3 high, C = byte 2, D = byte 1, E = byte 0).
;
; Bits 32..63 are ignored (the slot encoder will only consume 32 bits;
; if the M3 constant is out-of-range, the slot encoder's range check
; will catch it).
; ---------------------------------------------------------------------
encoder_load_imm_bcde:
                ld      hl, (encoder_opval_ptr)
                inc     hl
                inc     hl                  ; HL → byte 0 of LE value
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                ret


; ---------------------------------------------------------------------
; emit_byte — append one byte to the output buffer at PC, advance PC.
;
; Per docs/specs/2026-05-27-m6-paged-out-design.md.  OUT lives in
; physical pages 5 (low zone) + 6 (high zone), reached via section B
; (&4000-&7FFF):
;
;   Low zone  (OUT_ZONE = 0; OUT bytes 0..16383):
;       LMPR is already LMPR_ENCTAB during the encoder window, so
;       section B maps to page 5 (= LMPR_ENCTAB + 1) for free.  Write
;       lands directly with no LMPR change.
;   High zone (OUT_ZONE = 1; OUT bytes 16384..32767):
;       Bracket the write with `in a,(250)` to snapshot the current
;       LMPR (= LMPR_ENCTAB at the call site), `out (250), LMPR_OUT_HIGH`
;       to put page 6 in section B, write, then restore the snapshot.
;       Reading LMPR live (rather than hard-coding LMPR_ENCTAB on the
;       restore) keeps us correct against the boot-time top-bits in
;       LMPR_DEFAULT_RUNTIME (see assembler.asm:142, trampoline.asm).
;       (Port 250 = LMPR, port 251 = HMPR per SAM Coupé Tech Manual
;       §6.10 and the existing trampoline / enctab_map_in usage.)
;
; OUT_PC walks &4000..&7FFF; when it hits &8000 we flip OUT_ZONE to 1
; and wrap OUT_PC back to &4000.  A second wrap (i.e. OUT_LEN reaching
; 32768) is an error — jp fail.
;
; Input:    A = byte to emit.
; Output:   byte stored; OUT_PC advanced; OUT_LEN incremented; on a
;           zone boundary, OUT_ZONE flipped and OUT_PC wrapped.
; Clobbers: A, HL.  BC, DE preserved.
; ---------------------------------------------------------------------
emit_byte:
                push    af                  ; preserve byte across LMPR/store
                push    bc                  ; high-zone path clobbers HL only,
                push    de                  ; but BC/DE must survive per ABI
                ld      hl, (OUT_PC)
                ld      a, (OUT_ZONE)
                or      a
                jr      nz, emit_byte_high

; ----- Low zone — write to section B with LMPR_ENCTAB live -----------
                pop     de
                pop     bc
                pop     af                  ; A = byte
                ld      (hl), a
                jr      emit_byte_advance

; ----- High zone — bracket the store with LMPR=LMPR_OUT_HIGH ---------
; Port 250 is LMPR (sections A+B); port 251 is HMPR (sections C+D).
; The OUT buffer is reached via section B so we touch LMPR only.
emit_byte_high:
                in      a, (250)
                ld      (emit_lmpr_save), a
                ld      a, LMPR_OUT_HIGH
                out     (250), a
                pop     de
                pop     bc
                pop     af                  ; A = byte
                ld      (hl), a             ; section B = page 6 under &25
                ld      a, (emit_lmpr_save)
                out     (250), a            ; restore LMPR_ENCTAB
                ; fall through; A is dead beyond this point

; ----- Common tail: advance OUT_PC, handle zone boundary, bump LEN ----
emit_byte_advance:
                inc     hl
                ld      a, h
                cp      &80
                jr      nz, emit_byte_no_zone_cross

; Hit &8000 — last byte of the current zone was just written.  Flip
; OUT_ZONE 0 → 1 and wrap OUT_PC back to &4000.  A second crossing
; (OUT_ZONE already 1) means OUT_LEN ≥ 32768 — past the M6 ceiling.
                ld      a, (OUT_ZONE)
                or      a
                jr      z, emit_byte_zone_room_ok
                ld      a, &b0
                jp      fail_with_tag       ; tag b0: OUT > 32 KB
emit_byte_zone_room_ok:
                ld      a, 1
                ld      (OUT_ZONE), a
                ld      hl, &4000

emit_byte_no_zone_cross:
                ld      (OUT_PC), hl

; Bump 16-bit OUT_LEN.  HL is free to clobber per the ABI.
                ld      hl, (OUT_LEN)
                inc     hl
                ld      (OUT_LEN), hl
                ret


; ---------------------------------------------------------------------
; emit_bytes_n — append A bytes from (HL) to the output buffer.
;
; Input:    HL = source, A = number of bytes.
; Output:   HL advanced past the source; OUT_PC / OUT_LEN bumped.
; Clobbers: A, BC, DE, HL.
;
; The pre-M6 implementation open-coded the inner loop as `LD (DE), A`
; using DE = OUT_PC.  That bypassed the paged-emit machinery (the
; section-B / LMPR-bracket dance per emit_byte) and won't work once
; OUT lives off-axis.  We just loop over emit_byte instead; the call
; sites (mainly OpString) aren't on the hot path.
; ---------------------------------------------------------------------
emit_bytes_n:
                or      a
                ret     z
                ld      b, a
emit_bytes_n_loop:
                ld      a, (hl)
                push    hl
                push    bc
                call    emit_byte
                pop     bc
                pop     hl
                inc     hl
                djnz    emit_bytes_n_loop
                ret


; ---------------------------------------------------------------------
; Scratch / state.
; ---------------------------------------------------------------------
encoder_form_ptr:       defw    0
encoder_op_array:       defw    0
encoder_op_count:       defb    0
encoder_acc:            defb    0, 0, 0, 0  ; 32-bit accumulator (LE)
encoder_is64:           defb    0
encoder_slot_idx:       defb    0
encoder_slot_ptr:       defw    0
encoder_opval_ptr:      defw    0

; LMPR snapshot used by emit_byte's high-zone bracket.  Live LMPR at
; the call site (= LMPR_ENCTAB during the encoder window) is captured
; here so the restore goes back to the *exact* boot-derived value
; rather than a hard-coded constant.
emit_lmpr_save:         defb    0
