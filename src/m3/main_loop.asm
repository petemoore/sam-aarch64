; main_loop.asm — top-level M3 driver.
;
; 1. Load IN .tbn into IN_BUF via HGTHD+HLOAD.
; 2. Initialise reader, form_lookup pointers, and output state.
; 3. Walk records:
;      KindInst       (0x01)  parse operands, find form, encode → emit.
;      KindLabelDef   (0x02)  jp fail.  (M4)
;      KindLocalDef   (0x03)  jp fail.  (M4)
;      KindDirective  (0x04)  dispatch on directive_id:
;                              .byte / .short / .word / .quad / .ascii / .asciz
;                              else jp fail.
;      KindComment    (0x05)  skip (advance covered by reader_next_kind).
; 4. After the last record, HSAVE OUT_BUF[0..OUT_LEN] as "OUT".
;
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.6 / §2.5 / §3.
; Operand kind / directive id values come from
; tools/sam-aarch64-format/{operands,directives,kinds}.go.


; -----------------------------------------------------------------------
; OperandKind constants (mirror of tools/sam-aarch64-format/operands.go).
; -----------------------------------------------------------------------
OP_KIND_REG_X:          equ     &01
OP_KIND_REG_W:          equ     &02
OP_KIND_REG_XSP:        equ     &03
OP_KIND_REG_WSP:        equ     &04
OP_KIND_IMM_EXPR:       equ     &05
OP_KIND_SHIFTED_REG:    equ     &06
OP_KIND_EXTENDED_REG:   equ     &07
OP_KIND_MEM:            equ     &08
OP_KIND_STRING:         equ     &09
OP_KIND_COND:           equ     &0A
OP_KIND_SYS_NAME:       equ     &0B
OP_KIND_LIT_POOL:       equ     &0C

; -----------------------------------------------------------------------
; RecordKind constants (mirror of tools/sam-aarch64-format/kinds.go).
; -----------------------------------------------------------------------
REC_KIND_INST:          equ     &01
REC_KIND_LABEL_DEF:     equ     &02
REC_KIND_LOCAL_DEF:     equ     &03
REC_KIND_DIRECTIVE:     equ     &04
REC_KIND_COMMENT:       equ     &05

; -----------------------------------------------------------------------
; Directive ID constants (tools/sam-aarch64-format/directives.go).
; -----------------------------------------------------------------------
DIR_TEXT:               equ     0   ; .text
DIR_DATA:               equ     1   ; .data
DIR_BYTE:               equ     2   ; .byte
DIR_SHORT:              equ     3   ; .short
DIR_WORD:               equ     4   ; .word
DIR_QUAD:               equ     5   ; .quad
DIR_ASCII:              equ     6   ; .ascii
DIR_ASCIZ:              equ     7   ; .asciz
DIR_HWORD:              equ     21  ; .hword (synonym of .short)


; -----------------------------------------------------------------------
; main_assemble — orchestrate the assemble pass.
;
; Called after load_enctab + form_lookup_init.
;
; Input: none.
; Output: OUT_BUF populated, OUT_LEN set; ready for HSAVE.
; On any error: jp fail.
; -----------------------------------------------------------------------
main_assemble:
; -- Initialise output buffer state -------------------------------------
                ld      hl, OUT_BUF
                ld      (OUT_BASE), hl
                ld      (OUT_PC), hl
                ld      hl, 0
                ld      (OUT_LEN), hl

; -- Load IN via HGTHD + HLOAD into IN_BUF -----------------------------
                call    load_in_file

; -- Initialise reader (validate magic, skip name table) ----------------
                ld      hl, IN_BUF
                ld      (IN_POS), hl
                ld      hl, (IN_END)        ; set by load_in_file
                ld      (IN_END), hl        ; (no-op write, but documents intent)
                call    reader_init

; -- Main loop: walk records ------------------------------------------
main_assemble_loop:
                call    reader_at_end
                ret     z

                call    reader_next_kind    ; A = kind, HL = payload, BC = len

                cp      REC_KIND_INST
                jp      z, main_handle_inst
                cp      REC_KIND_LABEL_DEF
                jp      z, main_assemble_loop   ; M3: labels emit no bytes,
                                                ; and our fixture corpus has
                                                ; no PC-relative refs that
                                                ; need resolution.  M4 will
                                                ; build a symbol table here.
                cp      REC_KIND_LOCAL_DEF
                jp      z, main_assemble_loop   ; M3: ditto.
                cp      REC_KIND_DIRECTIVE
                jp      z, main_handle_directive
                cp      REC_KIND_COMMENT
                jp      z, main_assemble_loop   ; skip
                jp      fail


; -----------------------------------------------------------------------
; main_handle_inst — parse one INST record into OPVAL_ARRAY, look up
; its form, dispatch to encode_inst.
;
; Input: HL = payload ptr, BC = payload len.
; Output: 4 bytes appended to OUT.
; Errors: jp fail.
; -----------------------------------------------------------------------
main_handle_inst:
                ld      (main_payload_ptr), hl
                ld      (main_payload_len), bc

; -- Read mnemonic_id (u16 LE) -----------------------------------------
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (main_mnemonic_id), de

; -- Read operand_count (u8) -------------------------------------------
                ld      a, (hl)
                inc     hl
                ld      (main_op_count), a
                ld      (main_op_count_remaining), a

; -- Parse each operand into OPVAL_ARRAY -------------------------------
                or      a
                jp      z, main_handle_inst_no_ops

                ld      de, OPVAL_ARRAY
                ld      (main_opval_dest), de
                ld      (main_opval_src), hl

main_handle_inst_parse_loop:
                ld      hl, (main_opval_src)
                ld      a, (hl)             ; A = operand kind
                inc     hl

                ld      de, (main_opval_dest)
                push    af                  ; save kind
                ld      (de), a             ; store kind at +0
                inc     de
                pop     af

; Dispatch on operand kind.
                cp      OP_KIND_REG_X
                jr      z, main_parse_reg
                cp      OP_KIND_REG_W
                jr      z, main_parse_reg
                cp      OP_KIND_REG_XSP
                jr      z, main_parse_reg
                cp      OP_KIND_REG_WSP
                jr      z, main_parse_reg
                cp      OP_KIND_IMM_EXPR
                jp      z, main_parse_imm
                cp      OP_KIND_COND
                jr      z, main_parse_cond
; Anything else (SHIFTED_REG, EXTENDED_REG, MEM, STRING, SYS_NAME, LIT_POOL)
; is M4 territory.
                jp      fail


; ---- Parse reg: read 1 byte, store at OPVAL[+1] ----------------------
main_parse_reg:
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; store reg at +1
; Zero pad bytes +2..+9
                inc     de
                push    bc
                ld      b, 8
main_parse_reg_pad:
                xor     a
                ld      (de), a
                inc     de
                djnz    main_parse_reg_pad
                pop     bc
                jp      main_handle_inst_advance

; ---- Parse cond: read 1 byte, store at OPVAL[+2] (matches dispatch) --
main_parse_cond:
; First zero +1 (reg slot unused for cond).
                xor     a
                ld      (de), a
                inc     de
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; +2 = cond byte
                inc     de
                push    bc
                ld      b, 7
main_parse_cond_pad:
                xor     a
                ld      (de), a
                inc     de
                djnz    main_parse_cond_pad
                pop     bc
                jp      main_handle_inst_advance

; ---- Parse imm_expr: read len u16, evaluate, store 8-byte result -----
main_parse_imm:
; HL = ptr to expr_len bytes.  Read u16 LE.
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                ld      (main_opval_src), hl
; HL = first byte of bytecode; BC = bytecode length.
; Evaluate: pass HL=bytecode, BC=len → expr_result.
                push    de
                push    bc
                call    eval_expr_const
                pop     bc
                pop     de
; Advance HL by BC.
                ld      hl, (main_opval_src)
                add     hl, bc
                ld      (main_opval_src), hl

; Store 8-byte result starting at DE.  We're at DE = OPVAL+1; the encoder
; expects the 8-byte LE at OPVAL+2.  First zero OPVAL+1 (reg byte).
                xor     a
                ld      (de), a
                inc     de
; Copy 8 bytes from expr_result to (DE).
                push    bc
                ld      hl, expr_result
                ld      b, 8
main_parse_imm_copy:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    main_parse_imm_copy
                pop     bc
                jp      main_handle_inst_advance_imm


; main_handle_inst_advance — common tail after a non-imm operand parse:
;   - opval_dest += OPVAL_STRIDE
;   - opval_src is already advanced by the parser branch (HL).
;   - decrement remaining operand count.
main_handle_inst_advance:
                ld      (main_opval_src), hl
main_handle_inst_advance_imm:
                ld      hl, (main_opval_dest)
                ld      bc, OPVAL_STRIDE
                add     hl, bc
                ld      (main_opval_dest), hl
                ld      a, (main_op_count_remaining)
                dec     a
                ld      (main_op_count_remaining), a
                jr      nz, main_handle_inst_parse_loop_re
                jp      main_handle_inst_done

main_handle_inst_parse_loop_re:
                jp      main_handle_inst_parse_loop


main_handle_inst_no_ops:
                xor     a
                ld      (main_op_count_remaining), a
                jp      main_handle_inst_done


; -----------------------------------------------------------------------
; Look up form, dispatch encoder.
; -----------------------------------------------------------------------
main_handle_inst_done:
; Build a temporary kinds[] array at OPVAL_KINDS by walking OPVAL_ARRAY
; one operand at a time and copying the kind byte.
                ld      a, (main_op_count)
                ld      (main_kinds_n), a
                or      a
                jr      z, main_kinds_built

                ld      b, a
                ld      hl, OPVAL_ARRAY
                ld      de, OPVAL_KINDS
main_kinds_loop:
                ld      a, (hl)
                ld      (de), a
                push    de
                ld      de, OPVAL_STRIDE
                add     hl, de
                pop     de
                inc     de
                djnz    main_kinds_loop

main_kinds_built:
; Find first form for this mnemonic.
                ld      de, (main_mnemonic_id)
                call    form_lookup_find_first
                jp      nz, fail            ; mnemonic unknown

; Match operand-kind tuple.
                push    hl                  ; save first-form ptr (HL set by find_first)
                push    bc                  ; save form_count
                ld      de, OPVAL_KINDS
                ld      a, (main_kinds_n)
                pop     bc
                pop     hl
                call    form_lookup_match
                jp      nz, fail            ; no matching form

; HL now = matched form header pointer.  Call encoder.
                ld      de, OPVAL_ARRAY
                ld      a, (main_op_count)
                call    encode_inst

                jp      main_assemble_loop


; -----------------------------------------------------------------------
; Save count of operands BEFORE the parse loop starts decrementing it.
; -----------------------------------------------------------------------
; Pre-loop hook: when we enter main_handle_inst_parse_loop, we should
; have main_op_count_remaining = main_op_count.  Initialise inline.
; (Inserted here as a 0-cycle reminder; actual initialisation is below.)

; Patch: insert this between the operand-count read and the first
; iteration of the parse loop.  We achieve it by re-reading main_op_count
; into main_op_count_remaining via a wrapper.  Easiest fix: set it just
; before pushing into the loop.  See main_handle_inst above.


; -----------------------------------------------------------------------
; Directives: .byte / .short / .word / .quad / .ascii / .asciz / .hword.
; -----------------------------------------------------------------------
main_handle_directive:
                ld      (main_payload_ptr), hl
                ld      (main_payload_len), bc

; First byte: directive_id (u8).
                ld      a, (hl)
                inc     hl
                ld      (main_dir_id), a

; Second byte: operand_count (u8).
                ld      a, (hl)
                inc     hl
                ld      (main_op_count), a
                ld      (main_op_count_remaining), a
                ld      (main_opval_src), hl

                ld      a, (main_dir_id)
                cp      DIR_BYTE
                jp      z, main_dir_byte
                cp      DIR_SHORT
                jp      z, main_dir_short
                cp      DIR_HWORD
                jp      z, main_dir_short
                cp      DIR_WORD
                jp      z, main_dir_word
                cp      DIR_QUAD
                jp      z, main_dir_quad
                cp      DIR_ASCII
                jp      z, main_dir_ascii
                cp      DIR_ASCIZ
                jp      z, main_dir_asciz
                cp      DIR_TEXT
                jp      z, main_assemble_loop      ; .text — no-op for M3
                cp      DIR_DATA
                jp      z, main_assemble_loop      ; .data — no-op
                jp      fail


; Each numeric directive walks its operand list, evaluating each as
; OP_KIND_IMM_EXPR, then emitting N bytes (1/2/4/8) per value.
main_dir_byte:
                call    main_eval_each_imm_init
main_dir_byte_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, main_assemble_loop
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                call    emit_byte
                jp      main_dir_byte_loop

main_dir_short:
                call    main_eval_each_imm_init
main_dir_short_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, main_assemble_loop
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                call    emit_byte
                ld      a, (expr_result + 1)
                call    emit_byte
                jp      main_dir_short_loop

main_dir_word:
                call    main_eval_each_imm_init
main_dir_word_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, main_assemble_loop
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                call    emit_byte
                ld      a, (expr_result + 1)
                call    emit_byte
                ld      a, (expr_result + 2)
                call    emit_byte
                ld      a, (expr_result + 3)
                call    emit_byte
                jp      main_dir_word_loop

main_dir_quad:
                call    main_eval_each_imm_init
main_dir_quad_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, main_assemble_loop
                call    main_eval_next_imm
                ld      b, 8
                ld      hl, expr_result
main_dir_quad_emit:
                ld      a, (hl)
                push    bc
                push    hl
                call    emit_byte
                pop     hl
                pop     bc
                inc     hl
                djnz    main_dir_quad_emit
                jp      main_dir_quad_loop


; .ascii / .asciz — operands are STRING records.  Each STRING:
;   kind=0x09, len u16, bytes…
main_dir_ascii:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, main_assemble_loop
                ld      hl, (main_opval_src)
                ld      a, (hl)             ; kind byte
                cp      OP_KIND_STRING
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → string bytes; BC = len
                push    bc
                call    main_emit_string_bytes
                pop     bc
                add     hl, bc
                ld      (main_opval_src), hl
                ld      a, (main_op_count_remaining)
                dec     a
                ld      (main_op_count_remaining), a
                jp      main_dir_ascii

main_dir_asciz:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, main_assemble_loop
                ld      hl, (main_opval_src)
                ld      a, (hl)
                cp      OP_KIND_STRING
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                push    bc
                call    main_emit_string_bytes
                pop     bc
                add     hl, bc
                ld      (main_opval_src), hl
; Emit trailing NUL.
                xor     a
                call    emit_byte
                ld      a, (main_op_count_remaining)
                dec     a
                ld      (main_op_count_remaining), a
                jp      main_dir_asciz


main_emit_string_bytes:
; Input: HL = bytes ptr, BC = length.  Emit each byte via emit_byte.
                ld      a, b
                or      c
                ret     z
main_emit_string_loop:
                ld      a, (hl)
                push    hl
                push    bc
                call    emit_byte
                pop     bc
                pop     hl
                inc     hl
                dec     bc
                ld      a, b
                or      c
                jr      nz, main_emit_string_loop
                ret


; -----------------------------------------------------------------------
; main_eval_each_imm_init — set state for walking N IMM_EXPR operands.
; No-op for now (main_opval_src and main_op_count_remaining were set
; on entry to main_handle_directive).
; -----------------------------------------------------------------------
main_eval_each_imm_init:
                ret


; -----------------------------------------------------------------------
; main_eval_next_imm — consume the next IMM_EXPR operand from
; (main_opval_src), evaluate it, leave the 8-byte LE result in
; expr_result.  Decrements main_op_count_remaining.
; -----------------------------------------------------------------------
main_eval_next_imm:
                ld      hl, (main_opval_src)
                ld      a, (hl)             ; kind
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → bytecode start
                push    bc
                call    eval_expr_const     ; HL=ptr, BC=len → expr_result
                pop     bc
                ld      hl, (main_opval_src)
                inc     hl
                inc     hl
                inc     hl                  ; HL → bytecode start
                add     hl, bc
                ld      (main_opval_src), hl
                ld      a, (main_op_count_remaining)
                dec     a
                ld      (main_op_count_remaining), a
                ret


; -----------------------------------------------------------------------
; load_in_file — HGTHD + HLOAD "IN" into IN_BUF.
;
; HLOAD's destination must be in section C (&8000-&BFFF) per the
; loader.asm header notes.  IN_BUF lives in section C.
;
; The on-disk IN file size is recorded in the file's body header.
; SAMDOS's HGTHD reads that header, populates internal `difa`, then
; copies difa to &4B50 (= UIFA + 80) via txhed (samdos/src/h.s:txrom).
; So after HGTHD:
;   (&4B50 + 34) = page count (C-only-of-BC for HLOAD)
;   (&4B50 + 35..36) = length-mod-16K
;     with bit 15 set by SAMDOS's `set 7, d` line (h.s:hgthd) —
;     we must `res 7` on the high byte before passing to HLOAD.
;
; Citation: samdos/src/h.s::hgthd lines 59-67 + ::txhed lines that
; transfer difa to &4B50.
; -----------------------------------------------------------------------
load_in_file:
                ld      hl, name_IN
                call    fill_uifa
                rst     8
                defb    HOOK_HGTHD          ; longjmps on file-not-found

; Read length-mod-16K from the SAMDOS-deposited header at &4B50+35.
                ld      hl, (&4B50 + 35)
                ld      a, h
                and     &7F                 ; clear SAMDOS's `set 7, d` marker
                ld      h, a
                ld      (in_file_len), hl

; Read page count from &4B50+34 (low byte only).
                ld      a, (&4B50 + 34)
                ld      (in_file_pages), a

                ld      hl, IN_BUF
                ld      a, (in_file_pages)
                ld      c, a
                ld      b, 0
                ld      de, (in_file_len)
                rst     8
                defb    HOOK_HLOAD

                ld      hl, IN_BUF
                ld      de, (in_file_len)
                add     hl, de
                ld      (IN_END), hl
                ret


; -----------------------------------------------------------------------
; save_out_file — HSAVE OUT_BUF[0..OUT_LEN] as "OUT".
;
; Mirrors M0's stub.asm HSAVE call.  Pre-populates UIFA bytes 31-36
; with current HMPR, source address, and length.
; -----------------------------------------------------------------------
save_out_file:
                ld      hl, name_OUT
                call    fill_uifa
                in      a, (251)
                and     &1f
                ld      (UIFA + 31), a
                ld      hl, (OUT_BASE)
                ld      (UIFA + 32), hl
                xor     a
                ld      (UIFA + 34), a
                ld      hl, (OUT_LEN)
                ld      (UIFA + 35), hl
                rst     8
                defb    HOOK_HSAVE
                ret


; -----------------------------------------------------------------------
; UIFA name blocks for the IN and OUT files.
; -----------------------------------------------------------------------
name_IN:        defb    19                  ; type 19 = code
                defm    "IN        "
                defm    "    "

name_OUT:       defb    19
                defm    "OUT       "
                defm    "    "


; -----------------------------------------------------------------------
; Scratch globals.
; -----------------------------------------------------------------------
main_payload_ptr:       defw    0
main_payload_len:       defw    0
main_mnemonic_id:       defw    0
main_op_count:          defb    0
main_op_count_remaining:defb    0
main_opval_src:         defw    0
main_opval_dest:        defw    0
main_dir_id:            defb    0
main_kinds_n:           defb    0
in_file_len:            defw    0
in_file_pages:          defb    0

; -----------------------------------------------------------------------
; Globals shared between reader / encoder / main_loop.
; -----------------------------------------------------------------------
IN_POS:                 defw    0           ; current read pointer into IN_BUF
IN_END:                 defw    0           ; one past the last valid byte

OUT_BASE:               defw    0           ; base of output buffer (= OUT_BUF)
OUT_PC:                 defw    0           ; next emit position
OUT_LEN:                defw    0           ; bytes emitted so far
