; main_loop.asm — top-level M3/M4 driver.
;
; M4 two-pass design (see docs/specs/2026-05-24-m4-symbols-multipass-design.md
; §2.1):
;
; 1. Load IN .tbn into IN_BUF via HGTHD+HLOAD (once, before pass 1).
;
; 2. Pass 1 — table build, no emit:
;      Reset PASS_PC = 0; init symbol + local-label tables.
;      Rewind reader to first record.  Walk records:
;        KindInst        PASS_PC += 4
;        KindLabelDef    symbol_insert(symbol_id, PASS_PC)
;        KindLocalDef    local_def_append(digit, PASS_PC)
;        KindDirective   PASS_PC += directive size
;        KindComment     skip
;
; 3. Pass 2 — emit:
;      Reset PASS_PC = 0; reset OUT state.  Rewind reader.  Walk records:
;        KindInst        parse operands → form lookup → encode → emit;
;                        PASS_PC += 4 (kept in lockstep with OUT_LEN).
;        KindLabelDef    skip (already in table)
;        KindLocalDef    skip (already in table)
;        KindDirective   evaluate operands, emit bytes; PASS_PC += size.
;        KindComment     skip
;
; 4. After pass 2, HSAVE OUT_BUF[0..OUT_LEN] as "OUT".
;
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.6 / §2.5 / §3.
; Operand kind / directive id values come from
; tools/sam-aarch64-format/{operands,directives,kinds}.go.
;
; PASS_PC invariant (correctness gate for Tasks 5-6):
;   At any record boundary, PASS_PC = "address of the next record to
;   process".  Equivalently, in pass 2, PASS_PC == OUT_LEN at every
;   record boundary (M3's OUT_BUF starts at PC 0).  Both passes advance
;   by the same per-record amount, so the invariant follows.


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
; main_assemble — orchestrate the two-pass assemble.
;
; Called after load_enctab + form_lookup_init.
;
; Input: none.
; Output: OUT_BUF populated, OUT_LEN set; ready for HSAVE.
; On any error: jp fail.
; -----------------------------------------------------------------------
main_assemble:
; -- Load IN via HGTHD + HLOAD into IN_BUF (once) ---------------------
                call    load_in_file

; ----- Pass 1: table build -------------------------------------------
                ld      a, PASS_PASS1
                ld      (PASS_MODE), a
                call    symbol_table_init
                call    local_label_table_init
                call    pass_pc_reset
                call    reset_reader_to_in_buf
                call    walk_records

; ----- Pass 2: emit --------------------------------------------------
                ld      a, PASS_PASS2
                ld      (PASS_MODE), a
                call    pass_pc_reset
                call    reset_out_buffer
                call    reset_reader_to_in_buf
                call    walk_records
                ret


; -----------------------------------------------------------------------
; reset_reader_to_in_buf — IN_POS := IN_BUF, then reader_init.
;
; reader_init validates the magic, skips the name table, and leaves
; IN_POS at the first record.  It does not write outside IN_POS, so
; calling it twice (once per pass) is safe.
;
; Input:  IN_END must be set (by load_in_file).
; Output: IN_POS positioned at the first record's kind byte.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
reset_reader_to_in_buf:
                ld      hl, IN_BUF
                ld      (IN_POS), hl
                jp      reader_init                 ; tail-call


; -----------------------------------------------------------------------
; reset_out_buffer — reset output cursor + counter to empty.
;
; Called before pass 2 only (pass 1 never emits).
;
; Input:  none.
; Output: OUT_BASE = OUT_PC = OUT_BUF; OUT_LEN = 0.
; Clobbers: HL.
; -----------------------------------------------------------------------
reset_out_buffer:
                ld      hl, OUT_BUF
                ld      (OUT_BASE), hl
                ld      (OUT_PC), hl
                ld      hl, 0
                ld      (OUT_LEN), hl
                ret


; -----------------------------------------------------------------------
; PASS_PC helpers.
; -----------------------------------------------------------------------

; pass_pc_reset — PASS_PC := 0.
;
; Input:  none.  Output: PASS_PC = 0.  Clobbers: A, HL.
pass_pc_reset:
                xor     a
                ld      (PASS_PC + 0), a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                ret


; pass_pc_advance_4 — PASS_PC += 4.
;
; Used by KindInst (4 bytes per aarch64 instruction).
;
; Input:  none.  Output: PASS_PC bumped by 4.  Clobbers: A.
pass_pc_advance_4:
                ld      a, 4
                ; fall through into pass_pc_advance_a


; pass_pc_advance_a — PASS_PC += A (u8, zero-extended to 32 bits).
;
; Used by directive size computation when op_count × bytes-per-op
; fits in a byte.
;
; Input:  A = unsigned byte to add.
; Output: PASS_PC bumped by A.
; Clobbers: A, HL.
pass_pc_advance_a:
                ld      hl, PASS_PC
                add     a, (hl)
                ld      (hl), a
                ret     nc
                ; propagate carry through bytes 1..3
pass_pc_carry_b1:
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret     nz
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret     nz
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret


; pass_pc_advance_de — PASS_PC += DE (u16, zero-extended to 32 bits).
;
; Used when summing string lengths (which can exceed 255).
;
; Input:  DE = unsigned word to add.
; Output: PASS_PC bumped by DE.
; Clobbers: A, HL, DE.
pass_pc_advance_de:
                ld      hl, PASS_PC
; byte 0 := (PASS_PC[0] + E)
                ld      a, (hl)
                add     a, e
                ld      (hl), a
                ld      e, 0
                jr      nc, pass_pc_advance_de_b1
                inc     e                           ; carry into byte 1
pass_pc_advance_de_b1:
                inc     hl
; byte 1 := PASS_PC[1] + D + carry-from-byte-0 (= E or 0).  Add D first,
; then E (carry).
                ld      a, (hl)
                add     a, d
                ld      d, 0
                jr      nc, pass_pc_advance_de_b1_addc
                inc     d
pass_pc_advance_de_b1_addc:
                add     a, e
                ld      (hl), a
                jr      nc, pass_pc_advance_de_b1_done
                inc     d
pass_pc_advance_de_b1_done:
                ld      a, d
                or      a
                ret     z
; Propagate D (final carry into bytes 2..3).
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret     nz
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret


; copy_pass_pc_to_symbol_value_buf — copy PASS_PC (4 bytes) into
; symbol_value_buf.  Used by KindLabelDef before calling symbol_insert.
;
; Input:  none.  Output: symbol_value_buf = PASS_PC.  Clobbers: A.
copy_pass_pc_to_symbol_value_buf:
                ld      a, (PASS_PC + 0)
                ld      (symbol_value_buf + 0), a
                ld      a, (PASS_PC + 1)
                ld      (symbol_value_buf + 1), a
                ld      a, (PASS_PC + 2)
                ld      (symbol_value_buf + 2), a
                ld      a, (PASS_PC + 3)
                ld      (symbol_value_buf + 3), a
                ret


; copy_pass_pc_to_local_label_pc_buf — copy PASS_PC (4 bytes) into
; local_label_pc_buf.  Used by KindLocalDef before calling
; local_def_append.
;
; Input:  none.  Output: local_label_pc_buf = PASS_PC.  Clobbers: A.
copy_pass_pc_to_local_label_pc_buf:
                ld      a, (PASS_PC + 0)
                ld      (local_label_pc_buf + 0), a
                ld      a, (PASS_PC + 1)
                ld      (local_label_pc_buf + 1), a
                ld      a, (PASS_PC + 2)
                ld      (local_label_pc_buf + 2), a
                ld      a, (PASS_PC + 3)
                ld      (local_label_pc_buf + 3), a
                ret


; -----------------------------------------------------------------------
; walk_records — per-pass main loop.
;
; Dispatches on record kind.  Each handler chooses pass-1 / pass-2
; behaviour by reading PASS_MODE.  All handlers tail-jp back here when
; done.
;
; Input:  PASS_MODE set; reader positioned at first record.
; Output: when reader_at_end returns Z, returns to caller.
; -----------------------------------------------------------------------
walk_records:
                call    reader_at_end
                ret     z

                call    reader_next_kind    ; A = kind, HL = payload, BC = len

                cp      REC_KIND_INST
                jp      z, main_handle_inst
                cp      REC_KIND_LABEL_DEF
                jp      z, main_handle_label_def
                cp      REC_KIND_LOCAL_DEF
                jp      z, main_handle_local_def
                cp      REC_KIND_DIRECTIVE
                jp      z, main_handle_directive
                cp      REC_KIND_COMMENT
                jp      z, walk_records             ; skip (both passes)
                jp      fail


; -----------------------------------------------------------------------
; main_handle_label_def — KindLabelDef record handler.
;
; Pass 1: insert (symbol_id, PASS_PC) into the global symbol table.
; Pass 2: no-op (entry already present from pass 1).
;
; Payload layout: [symbol_id u16 LE] (BC = 2).
;
; Input:  HL = payload ptr, BC = payload len (= 2).
; Output: jp walk_records.
; -----------------------------------------------------------------------
main_handle_label_def:
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jp      nz, walk_records            ; pass 2: skip

; Pass 1: stash PC into symbol_value_buf, then call symbol_insert with
; HL = symbol_id.
                push    hl                          ; preserve payload ptr
                call    copy_pass_pc_to_symbol_value_buf
                pop     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl                      ; HL = symbol_id
                call    symbol_insert
                jp      walk_records


; -----------------------------------------------------------------------
; main_handle_local_def — KindLocalDef record handler.
;
; Pass 1: append PASS_PC to the per-digit list for digit A.
; Pass 2: no-op.
;
; Payload layout: [digit u8] (BC = 1).
;
; Input:  HL = payload ptr, BC = payload len (= 1).
; Output: jp walk_records.
; -----------------------------------------------------------------------
main_handle_local_def:
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jp      nz, walk_records            ; pass 2: skip

                push    hl                          ; preserve payload ptr
                call    copy_pass_pc_to_local_label_pc_buf
                pop     hl
                ld      a, (hl)                     ; A = digit
                call    local_def_append
                jp      walk_records


; -----------------------------------------------------------------------
; main_handle_inst — KindInst record handler.
;
; Pass 1: PASS_PC += 4.  No operand parsing, no encoder.
; Pass 2: existing M3 behaviour (parse operands into OPVAL_ARRAY,
;         look up form, encode_inst → emit 4 bytes), then PASS_PC += 4.
;
; Input: HL = payload ptr, BC = payload len.
; Output: jp walk_records (via main_handle_inst_done).
; Errors: jp fail.
; -----------------------------------------------------------------------
main_handle_inst:
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jp      z, main_handle_inst_pass1
                ; fall through into pass-2 body

main_handle_inst_pass2:
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
; Pass-2 INST tail: look up form, dispatch encoder, advance PASS_PC.
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

; Pass 2: advance PASS_PC by 4 (one aarch64 instruction).
                call    pass_pc_advance_4
                jp      walk_records


; -----------------------------------------------------------------------
; Pass-1 INST handler: just advance PASS_PC.  The reader has already
; advanced IN_POS past this record's payload (see reader.asm:147), so
; there is no operand walking to do.
; -----------------------------------------------------------------------
main_handle_inst_pass1:
                call    pass_pc_advance_4
                jp      walk_records


; -----------------------------------------------------------------------
; Directives: .byte / .short / .word / .quad / .ascii / .asciz / .hword.
;
; Both passes parse the directive header (dir_id + op_count), then:
;   * Pass 1: compute size, advance PASS_PC, return.
;   * Pass 2: dispatch to the per-directive emit routine (which
;             evaluates operands and emits bytes), then advance PASS_PC
;             by the same size.
;
; Sizes (per docs/specs/2026-05-24-m1-binary-tokenised-format-design.md
; §3 and aarch64 ELF as-rules):
;
;   DIR_TEXT  / DIR_DATA      0  (no-op for M3/M4)
;   DIR_BYTE                  op_count × 1
;   DIR_SHORT / DIR_HWORD     op_count × 2
;   DIR_WORD                  op_count × 4
;   DIR_QUAD                  op_count × 8
;   DIR_ASCII                 sum of string lengths
;   DIR_ASCIZ                 sum of string lengths + op_count (NUL/str)
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
                ld      (main_dir_payload_after_header), hl

; Branch on PASS_MODE: pass 1 → size-only; pass 2 → emit then advance.
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jp      z, main_handle_directive_pass1
                jp      main_handle_directive_pass2


; ----- Pass 1: compute directive size, advance PASS_PC, return ---------
main_handle_directive_pass1:
                call    compute_directive_size      ; result → BC (size, 16-bit)
                ld      d, b
                ld      e, c
                call    pass_pc_advance_de
                jp      walk_records


; ----- Pass 2: emit, then advance PASS_PC by directive size ------------
main_handle_directive_pass2:
                ld      a, (main_dir_id)
                cp      DIR_BYTE
                jp      z, main_dir_byte_emit
                cp      DIR_SHORT
                jp      z, main_dir_short_emit
                cp      DIR_HWORD
                jp      z, main_dir_short_emit
                cp      DIR_WORD
                jp      z, main_dir_word_emit
                cp      DIR_QUAD
                jp      z, main_dir_quad_emit
                cp      DIR_ASCII
                jp      z, main_dir_ascii_emit
                cp      DIR_ASCIZ
                jp      z, main_dir_asciz_emit
                cp      DIR_TEXT
                jp      z, walk_records             ; .text — no-op
                cp      DIR_DATA
                jp      z, walk_records             ; .data — no-op
                jp      fail


; -----------------------------------------------------------------------
; compute_directive_size — work out how many bytes a directive emits.
;
; Reads (main_dir_id), (main_op_count), (main_dir_payload_after_header).
; Does NOT disturb main_opval_src (saved/restored when walking strings).
;
; Output: BC = size in bytes (u16).  For M3/M4 directives, sizes fit in
;         a u16 well within OUT_BUF (2 KB).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
compute_directive_size:
                ld      a, (main_dir_id)
                cp      DIR_BYTE
                jp      z, compute_dir_size_x1
                cp      DIR_SHORT
                jp      z, compute_dir_size_x2
                cp      DIR_HWORD
                jp      z, compute_dir_size_x2
                cp      DIR_WORD
                jp      z, compute_dir_size_x4
                cp      DIR_QUAD
                jp      z, compute_dir_size_x8
                cp      DIR_ASCII
                jp      z, compute_dir_size_ascii
                cp      DIR_ASCIZ
                jp      z, compute_dir_size_asciz
                cp      DIR_TEXT
                jr      z, compute_dir_size_zero
                cp      DIR_DATA
                jr      z, compute_dir_size_zero
                jp      fail

compute_dir_size_zero:
                ld      bc, 0
                ret

; size = op_count * 1
compute_dir_size_x1:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                ret

; size = op_count * 2
compute_dir_size_x2:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                sla     c
                rl      b
                ret

; size = op_count * 4
compute_dir_size_x4:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                sla     c
                rl      b
                sla     c
                rl      b
                ret

; size = op_count * 8
compute_dir_size_x8:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                sla     c
                rl      b
                sla     c
                rl      b
                sla     c
                rl      b
                ret

; size = sum of STRING operand lengths
compute_dir_size_ascii:
                ld      hl, 0
                ld      (compute_dir_size_acc), hl
                call    walk_strings_sum_lengths
                ld      bc, (compute_dir_size_acc)
                ret

; size = sum of STRING lengths + op_count (one NUL per string)
compute_dir_size_asciz:
                ld      hl, 0
                ld      (compute_dir_size_acc), hl
                call    walk_strings_sum_lengths
                ld      hl, (compute_dir_size_acc)
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                add     hl, bc
                ld      b, h
                ld      c, l
                ret


; walk_strings_sum_lengths — walk the STRING operand list starting at
; (main_dir_payload_after_header) and add each string's len u16 into
; compute_dir_size_acc.  Does NOT modify main_opval_src.
;
; Per-string layout: [kind=0x09][len u16 LE][bytes...].
;
; Input:  (main_dir_payload_after_header) = first operand byte.
;         (main_op_count) = number of STRING operands.
; Output: (compute_dir_size_acc) += sum of all string lengths.
; Clobbers: A, BC, DE, HL.
walk_strings_sum_lengths:
                ld      a, (main_op_count)
                or      a
                ret     z
                ld      b, a                        ; B = strings remaining
                ld      hl, (main_dir_payload_after_header)

walk_strings_loop:
                ld      a, (hl)                     ; kind byte
                cp      OP_KIND_STRING
                jp      nz, fail
                inc     hl
                ld      e, (hl)                     ; len LSB
                inc     hl
                ld      d, (hl)                     ; len MSB
                inc     hl                          ; HL → string bytes

; acc += DE
                push    bc                          ; preserve counter
                push    hl                          ; preserve cursor
                ld      hl, (compute_dir_size_acc)
                add     hl, de
                ld      (compute_dir_size_acc), hl
                pop     hl
                pop     bc

; Skip the string bytes: HL += DE.
                add     hl, de
                djnz    walk_strings_loop
                ret


; -----------------------------------------------------------------------
; Pass-2 directive emit routines.
;
; Each routine evaluates operands and emits bytes (existing M3 logic).
; On completion it jumps to dir_emit_done, which advances PASS_PC by
; the directive size (recomputed via compute_directive_size to keep the
; pass-1 / pass-2 sizing in lockstep).
; -----------------------------------------------------------------------
main_dir_byte_emit:
                call    main_eval_each_imm_init
main_dir_byte_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                call    emit_byte
                jp      main_dir_byte_loop

main_dir_short_emit:
                call    main_eval_each_imm_init
main_dir_short_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                call    emit_byte
                ld      a, (expr_result + 1)
                call    emit_byte
                jp      main_dir_short_loop

main_dir_word_emit:
                call    main_eval_each_imm_init
main_dir_word_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
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

main_dir_quad_emit:
                call    main_eval_each_imm_init
main_dir_quad_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                call    main_eval_next_imm
                ld      b, 8
                ld      hl, expr_result
main_dir_quad_inner:
                ld      a, (hl)
                push    bc
                push    hl
                call    emit_byte
                pop     hl
                pop     bc
                inc     hl
                djnz    main_dir_quad_inner
                jp      main_dir_quad_loop


; .ascii / .asciz — operands are STRING records.  Each STRING:
;   kind=0x09, len u16, bytes…
main_dir_ascii_emit:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
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
                jp      main_dir_ascii_emit

main_dir_asciz_emit:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
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
                jp      main_dir_asciz_emit


; dir_emit_done — common pass-2 directive tail: recompute size, advance
; PASS_PC, return to walk_records.
;
; Recomputing is safer than threading the size through the emit loops
; (which clobber lots of state).  compute_directive_size reads only
; main_dir_id / main_op_count / main_dir_payload_after_header — all of
; which are untouched by the emit loops.
dir_emit_done:
                call    compute_directive_size
                ld      d, b
                ld      e, c
                call    pass_pc_advance_de
                jp      walk_records


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
main_payload_ptr:               defw    0
main_payload_len:               defw    0
main_mnemonic_id:               defw    0
main_op_count:                  defb    0
main_op_count_remaining:        defb    0
main_opval_src:                 defw    0
main_opval_dest:                defw    0
main_dir_id:                    defb    0
main_kinds_n:                   defb    0
in_file_len:                    defw    0
in_file_pages:                  defb    0

; main_dir_payload_after_header — start of the first operand of a
; directive (i.e. payload + 2, skipping dir_id + op_count).  Captured
; on entry to main_handle_directive so compute_directive_size can walk
; STRING operands without disturbing main_opval_src (which the pass-2
; emit loops advance).
main_dir_payload_after_header:  defw    0

; compute_dir_size_acc — 16-bit accumulator used by
; walk_strings_sum_lengths.  Reset to 0 before each pass at the
; directive's size-compute step.
compute_dir_size_acc:           defw    0

; -----------------------------------------------------------------------
; Globals shared between reader / encoder / main_loop.
; -----------------------------------------------------------------------
IN_POS:                 defw    0           ; current read pointer into IN_BUF
IN_END:                 defw    0           ; one past the last valid byte

OUT_BASE:               defw    0           ; base of output buffer (= OUT_BUF)
OUT_PC:                 defw    0           ; next emit position
OUT_LEN:                defw    0           ; bytes emitted so far
