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
DIR_EQU:                equ     8   ; .equ
DIR_SET:                equ     9   ; .set
DIR_GLOBAL:             equ     10  ; .global
DIR_BALIGN:             equ     11  ; .balign
DIR_ORG:                equ     12  ; .org
DIR_SKIP:               equ     13  ; .skip
DIR_SPACE:              equ     14  ; .space
DIR_INST:               equ     15  ; .inst
DIR_ALIGN:              equ     16  ; .align (2^N)
DIR_LTORG:              equ     17  ; .ltorg
DIR_SECTION:            equ     18  ; .section
DIR_ARCH:               equ     19  ; .arch
DIR_CPU:                equ     20  ; .cpu
DIR_HWORD:              equ     21  ; .hword (synonym of .short)


; -----------------------------------------------------------------------
; main_assemble — orchestrate the two-pass assemble + the ENCTAB-in-
; section-A window bracketing.
;
; Called after load_enctab (which leaves LMPR = LMPR_DEFAULT).
;
; Sequence:
;   1. load_in_file (RST 8 — needs LMPR = LMPR_DEFAULT for ROM access)
;   2. enctab_map_in — page ENCTAB into section A for encoder reads
;   3. form_lookup_init — first call that READS ENCTAB; must be inside
;      the map_in window
;   4. Pass 1 — table build (reads ENCTAB heavily via form_lookup)
;   5. Pass 2 — emit (reads ENCTAB + writes OUT)
;   6. enctab_map_out — restore LMPR = LMPR_DEFAULT before returning so
;      the caller can safely call save_out_file (RST 8)
;
; Input: none.
; Output: OUT_BUF populated, OUT_LEN set; ready for HSAVE.
;         LMPR = LMPR_DEFAULT on return.
; On any error: jp fail.
; -----------------------------------------------------------------------
main_assemble:
; -- Load IN via HGTHD + HLOAD into IN_BUF (LMPR = boot default) ------
                call    load_in_file

; -- Bracket open: ENCTAB into section A for encoder reads -----------
                call    enctab_map_in

; -- First ENCTAB-reading call: compute form/index pointers ----------
                call    form_lookup_init

; ----- Pass 1: table build -------------------------------------------
                ld      a, PASS_PASS1
                ld      (PASS_MODE), a
                call    symbol_table_init
                call    local_label_table_init
                call    litpool_init
                call    pass_pc_reset
                call    reset_reader_to_in_buf
                call    walk_records
                call    litpool_flush               ; implicit end-of-source

; ----- Pass 2: emit --------------------------------------------------
                ld      a, PASS_PASS2
                ld      (PASS_MODE), a
                call    pass_pc_reset
                call    reset_out_buffer
                call    reset_reader_to_in_buf
; Pass 2 re-uses the LITPOOL_TABLE / LITPOOL_PC_MAP populated by pass 1.
; Reset the per-slot `pending` flag (cleared by pass-1 flush) so the
; pass-2 flush emits each slot exactly once.  The slot count and the
; pc-map (which pass 2 only reads) are untouched.
                call    litpool_reset_pending
                call    walk_records
                call    litpool_flush               ; implicit end-of-source

; -- Bracket close: restore LMPR so save_out_file's RST 8 finds ROM
; in section A.
                call    enctab_map_out
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
; Per docs/specs/2026-05-27-m6-paged-out-design.md the OUT buffer
; lives in physical pages 5+6, reached via section B (&4000..&7FFF).
; OUT_PC starts at the section-B base; OUT_ZONE starts at 0 (= low
; zone, section B = page 5 for free under LMPR_ENCTAB).
;
; Input:  none.
; Output: OUT_PC = &4000; OUT_ZONE = 0; OUT_LEN = 0.
; Clobbers: A, HL.
; -----------------------------------------------------------------------
reset_out_buffer:
                ld      hl, &4000           ; section-B base
                ld      (OUT_PC), hl
                xor     a
                ld      (OUT_ZONE), a       ; start in low zone (page 5)
                ld      hl, 0
                ld      (OUT_LEN), hl
                ret


; -----------------------------------------------------------------------
; Paged-IN cursor helpers.
;
; Per docs/specs/2026-05-27-m6-paged-in-design.md.  Three primitives:
;   in_map_current   — write IN_POS_PAGE to LMPR, mapping the current
;                      IN page into section A (&0000..&3FFF).
;   in_persist_hl    — write HL back to IN_POS_OFFSET and snapshot the
;                      current LMPR into IN_POS_PAGE.  Used at the end
;                      of a reader bracket to commit the new cursor.
;   in_normalise_hl  — COMET-style adjustpo.  While H >= &40, subtract
;                      &40 from H and bump LMPR's low 5 bits.  The
;                      RAM0 bit at &20 is preserved across the inc
;                      because page numbers can't go above 31, so the
;                      low 5 bits never overflow into bit 5.
;
; All three use port 250 (LMPR).  Port 251 is HMPR (sections C+D)
; and is untouched here.  See trampoline.asm and the SAM Coupé Tech
; Manual §6.10 for the port assignments.
; -----------------------------------------------------------------------

; in_map_current — LMPR := IN_POS_PAGE; section A = current IN page.
;
; Input:  none.  Output: LMPR programmed.  Clobbers: A.
in_map_current:
                ld      a, (IN_POS_PAGE)
                out     (250), a            ; port 250 = LMPR
                ret


; in_persist_hl — IN_POS_OFFSET := HL; IN_POS_PAGE := current LMPR.
;
; Called at the end of a reader bracket so a later in_map_current
; restores the cursor's new position.
;
; Input:  HL = section-A offset (in [&0000, &4000)).
; Output: IN_POS_OFFSET / IN_POS_PAGE updated.  Clobbers: A.
in_persist_hl:
                ld      (IN_POS_OFFSET), hl
                in      a, (250)            ; A = current LMPR
                ld      (IN_POS_PAGE), a
                ret


; in_normalise_hl — while H >= &40, subtract &40 from H and bump LMPR's
; low 5 bits by 1 (the RAM0 bit at &20 stays set because the low 5 bits
; never exceed 31 in practice — IN spans pages 7..10 max).
;
; Mirrors COMET's adjustpo (reference/comet-decoded/comet.asm:3180-3188)
; — the standard "renormalise (page, offset) after a section-A
; address-arithmetic step" idiom.
;
; Input:  HL = section-A-ish offset (possibly >= &4000 after an add).
; Output: HL normalised into [&0000, &4000).  LMPR bumped per page
;         boundary crossed.  Clobbers: A.
in_normalise_hl:
in_normalise_loop:
                ld      a, h
                cp      &40
                ret     c                   ; H < &40 → done
                sub     &40
                ld      h, a
                in      a, (250)
                inc     a                   ; low 5 bits += 1 (RAM0 bit
                                            ;   preserved — pages 7..10
                                            ;   never overflow into bit 5)
                out     (250), a
                jr      in_normalise_loop


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
                cp      OP_KIND_SHIFTED_REG
                jp      z, main_parse_shifted_reg
                cp      OP_KIND_EXTENDED_REG
                jp      z, main_parse_extended_reg
                cp      OP_KIND_MEM
                jp      z, main_parse_mem
                cp      OP_KIND_SYS_NAME
                jp      z, main_parse_sys_name
; OpString (0x09) as an instruction operand: defensive error path.
;
; No current fixture routes an OpString through this code path.  The
; text2bin parser only emits OpString inside *directive* records
; (.ascii / .asciz / .section flags) — see parser.go:127-131, where
; the operand-parser routes TokString to a directive-only branch.
; But the on-disk format permits any operand kind in any record kind,
; so a corrupted / fuzzed / future-protocol .tbn could legitimately
; place an OpString record here.  Explicit jp fail prevents that from
; silently producing garbage bytes (the previous fall-through to
; `jp fail` worked but lumped STRING in with LIT_POOL; this gives
; STRING its own clearly-named branch for clarity).  M5 PR-D Task 14.
                cp      OP_KIND_STRING
                jp      z, fail
; OpLitPool (0x0C) — `=expr` for `ldr Xn|Wn, =<value>`.  Parse the
; payload (width + expr_len + bytecode) just enough that the dispatch
; can advance over it.  The actual encoder
; (intercepts.asm::try_intercept_litpool / litpool_encode_ldr_word)
; reads only the width from OPVAL_ARRAY[+1] and looks up the pool slot
; via PASS_PC.  See src/m3/litpool.asm for the full design.
;
; Layout written into OPVAL_ARRAY entry (10 bytes total):
;   +0 kind = 0x0C
;   +1 width (4 or 8)
;   +2..+9 zero (no per-instance state needed; pool entries live in
;          LITPOOL_TABLE keyed by PASS_PC).
                cp      OP_KIND_LIT_POOL
                jp      z, main_parse_litpool
                jp      fail


; ---- Parse OpLitPool (0x0C) — `=<expr>` ------------------------------
;
; On entry: kind byte (0x0C) is already at OPVAL[+0]; DE points at +1.
;           HL points at on-disk byte just past the kind byte (start of
;           [width u8][expr_len u16 LE][bytecode...]).
;
; We don't need to evaluate the expression here — pass 1 has already
; registered the pool slot keyed by PASS_PC.  Pass 2's encoder fetches
; the slot via litpool_lookup and uses its entry_pc to compute imm19.
;
; We DO need to consume the bytecode bytes so subsequent operands (if
; any — `ldr X0, =val` has none beyond OpLitPool) parse correctly, and
; advance the dispatch counter.
main_parse_litpool:
                ld      a, (hl)                     ; width
                inc     hl
                ld      (de), a                     ; OPVAL +1 = width
                inc     de
; Zero OPVAL +2..+9 (8 bytes).
                push    bc
                ld      b, 8
                xor     a
main_parse_litpool_zero:
                ld      (de), a
                inc     de
                djnz    main_parse_litpool_zero
                pop     bc
; Read expr_len u16 LE and skip the bytecode bytes.
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                          ; HL → bytecode start
                add     hl, bc                      ; HL past bytecode
                jp      main_handle_inst_advance


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


; ---- Parse OpShiftedReg (0x06) — `Rm, <shift> #amt` -------------------
;
; On entry: kind byte (0x06) is already at OPVAL[+0]; DE points at +1.
;           HL points at on-disk byte just past the kind byte.
;
; Payload (per tools/sam-aarch64-format/operands.go:155-160, WriteShiftedReg):
;   [width u8][reg u8][shift_kind u8][amt_expr_len u16][amt_expr bytes...]
;
; In-memory layout written into OPVAL_ARRAY entry (10 bytes total):
;   +0 kind=0x06    +1 width    +2 reg    +3 shift_kind
;   +4..+7 amt low-32 LE (evaluator's low 4 bytes)
;   +8..+9 padding (zero)
;
; The encoder consumes only the low byte for the imm6 shift amount; we
; keep 4 bytes so the range check can reject negative / wide values
; cleanly (encode_shifted_reg_word in src/m3/slots/shifted_reg.asm).
main_parse_shifted_reg:
                ld      a, (hl)             ; width
                inc     hl
                ld      (de), a             ; +1 = width
                inc     de
                ld      a, (hl)             ; reg
                inc     hl
                ld      (de), a             ; +2 = reg
                inc     de
                ld      a, (hl)             ; shift_kind
                inc     hl
                ld      (de), a             ; +3 = shift_kind
                inc     de
                jp      main_parse_amt_expr_tail


; ---- Parse OpExtendedReg (0x07) — `Rm, <extend> #amt` ------------------
;
; On entry: kind byte (0x07) is already at OPVAL[+0]; DE points at +1.
;
; Payload (per tools/sam-aarch64-format/operands.go:164-168,
; WriteExtendedReg):
;   [width u8][reg u8][extend u8][amt_expr_len u16][amt_expr bytes...]
;
; amt_expr may be empty (no `#N` suffix); main_parse_amt_expr_tail
; handles len=0 by storing amt=0.
;
; In-memory layout (10 bytes):
;   +0 kind=0x07    +1 width    +2 reg    +3 extend
;   +4..+7 amt low-32 LE        +8..+9 padding
;
; The encoder (src/m3/slots/extended_reg.asm) consumes only the low
; byte for the imm3 shift amount (0..4 valid per ARM ARM).
main_parse_extended_reg:
                ld      a, (hl)             ; width
                inc     hl
                ld      (de), a             ; +1
                inc     de
                ld      a, (hl)             ; reg
                inc     hl
                ld      (de), a             ; +2
                inc     de
                ld      a, (hl)             ; extend
                inc     hl
                ld      (de), a             ; +3
                inc     de
                ; fall through into main_parse_amt_expr_tail (shared with
                ; OpShiftedReg).


; ---- main_parse_amt_expr_tail — shared amt_expr evaluator ----------------
;
; OpShiftedReg (M5 PR-B Task 5) and OpExtendedReg (Task 6) both trail
; their fixed prefix with `[amt_expr_len u16][bytes...]`.  Read the
; length; if 0 store amt = 0, otherwise evaluate via eval_expr_const
; and copy the low 4 bytes into OPVAL +4..+7.  Then zero +8..+9 and
; advance.
;
; Input:  HL = on-disk pointer at amt_expr_len's first byte.
;         DE = OPVAL_ARRAY entry's +4 slot.
; Output: OPVAL +4..+9 populated; HL bumped past the amt_expr.
; Clobbers: A, BC, DE, HL.
main_parse_amt_expr_tail:
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → bytecode start; BC = len
                ld      (main_opval_src), hl

                ld      a, b
                or      c
                jr      nz, main_parse_amt_eval
; Zero-length amt expression: amt = 0.  Zero expr_result so the copy
; below propagates that.
                push    de
                ld      hl, expr_result
                ld      b, 8
                xor     a
main_parse_amt_zero_loop:
                ld      (hl), a
                inc     hl
                djnz    main_parse_amt_zero_loop
                pop     de
                jp      main_parse_amt_copy

main_parse_amt_eval:
                push    de
                push    bc
                call    eval_expr_const     ; HL=bytecode, BC=len → expr_result
                pop     bc
                pop     de
                ld      hl, (main_opval_src)
                add     hl, bc              ; HL past amt_expr bytecode
                ld      (main_opval_src), hl

main_parse_amt_copy:
; Copy expr_result[0..3] → OPVAL[+4..+7].  Zero OPVAL[+8..+9].
                ld      hl, expr_result
                ld      b, 4
main_parse_amt_copy_loop:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    main_parse_amt_copy_loop
                xor     a
                ld      (de), a
                inc     de
                ld      (de), a
                jp      main_handle_inst_advance_imm


; ---- Parse OpMem (0x08) — addressing-mode operand ---------------------
;
; On entry: kind byte (0x08) is already at OPVAL[+0]; DE points at +1.
;           HL points at on-disk byte just past the kind byte.
;
; The seven shapes (see tools/sam-aarch64-format/operands.go:64-72):
;   MemBase (0)             : [shape][base]                       — 2 bytes
;   MemBaseOff (1)          : [shape][base][off_len u16][off...]   — 4+ bytes
;   MemBaseOffPre (2)       : same shape as 1                      — 4+ bytes
;   MemBaseOffPost (3)      : same shape as 1                      — 4+ bytes
;   MemBaseIdx (4)          : [shape][base][idx][idxw]             — 4 bytes
;   MemBaseIdxShifted (5)   : [shape][base][idx][idxw][shAmt]      — 5 bytes
;   MemBaseIdxExtended (6)  : [shape][base][idx][idxw][ext][shAmt] — 6 bytes
;
; In-memory layout written into OPVAL_ARRAY (10 bytes):
;   +0 kind = 0x08
;   +1 shape
;   +2 base
;   +3 idx
;   +4 idx_width
;   +5 extend
;   +6 shift_amt
;   +7..+9 zero
;
; The signed-64-bit offset for shapes 1/2/3 is evaluated via
; eval_expr_const and the 8-byte LE result is copied to the shared
; OPMEM_OFF scratch (only one OpMem operand per instruction).
main_parse_mem:
                ld      a, (hl)             ; shape
                inc     hl
                ld      (de), a             ; OPVAL +1 = shape
                inc     de
                ld      b, a                ; B = shape (for dispatch below)
                ld      a, (hl)             ; base reg
                inc     hl
                ld      (de), a             ; OPVAL +2 = base
                inc     de

; Branch on shape — first decide if the remaining payload is an
; off_expr (shapes 1/2/3) or one of the register-offset shapes (4/5/6)
; or empty (shape 0).
                ld      a, b
                or      a
                jp      z, main_parse_mem_finish        ; shape 0: MemBase
                cp      1
                jp      z, main_parse_mem_off
                cp      2
                jp      z, main_parse_mem_off
                cp      3
                jp      z, main_parse_mem_off
                cp      4
                jp      z, main_parse_mem_idx
                cp      5
                jp      z, main_parse_mem_idx_shifted
                cp      6
                jp      z, main_parse_mem_idx_extended
                jp      fail

; ---- MemBase (shape 0): nothing more on-disk; zero the remaining 7
;      OPVAL bytes + OPMEM_OFF (no offset for this shape) and advance.
main_parse_mem_finish:
                push    bc
                ld      b, 7
                xor     a
main_parse_mem_finish_zero:
                ld      (de), a
                inc     de
                djnz    main_parse_mem_finish_zero
                pop     bc
; Zero OPMEM_OFF — encoder reads this unconditionally for shapes that
; may carry an offset; force-clearing it here prevents a previous
; instruction's offset leaking into the current one.
                push    hl
                push    de
                ld      hl, OPMEM_OFF
                ld      b, 8
                xor     a
main_parse_mem_finish_zoff:
                ld      (hl), a
                inc     hl
                djnz    main_parse_mem_finish_zoff
                pop     de
                pop     hl
                jp      main_handle_inst_advance

; ---- MemBaseOff/Pre/Post (shapes 1/2/3): read off_expr_len u16 +
;      bytecode, evaluate, copy 8-byte LE result to OPMEM_OFF.  Then
;      zero OPVAL +3..+9 and advance.
main_parse_mem_off:
                push    de                  ; save OPVAL +3 dest
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → bytecode start; BC = len
                ld      (main_opval_src), hl
                ld      a, b
                or      c
                jr      nz, main_parse_mem_off_eval
; Zero-length expr: store 0.
                ld      hl, expr_result
                ld      b, 8
                xor     a
main_parse_mem_off_zero_loop:
                ld      (hl), a
                inc     hl
                djnz    main_parse_mem_off_zero_loop
                jp      main_parse_mem_off_copy
main_parse_mem_off_eval:
                push    bc
                call    eval_expr_const
                pop     bc
                ld      hl, (main_opval_src)
                add     hl, bc
                ld      (main_opval_src), hl
main_parse_mem_off_copy:
; Copy expr_result[0..7] → OPMEM_OFF[0..7].
                ld      hl, expr_result
                ld      de, OPMEM_OFF
                ld      b, 8
main_parse_mem_off_cpy:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    main_parse_mem_off_cpy
                pop     de                  ; restore OPVAL +3 dest
; Zero OPVAL +3..+9 (7 bytes).
                push    bc
                ld      b, 7
                xor     a
main_parse_mem_off_zerorest:
                ld      (de), a
                inc     de
                djnz    main_parse_mem_off_zerorest
                pop     bc
                ld      hl, (main_opval_src)
                jp      main_handle_inst_advance

; ---- MemBaseIdx (shape 4): [idx][idxw]; remaining OPVAL bytes:
;      +3 idx, +4 idx_width, +5..+9 zero.
main_parse_mem_idx:
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; OPVAL +3 = idx
                inc     de
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; OPVAL +4 = idx_width
                inc     de
                push    bc
                ld      b, 5
                xor     a
main_parse_mem_idx_zero:
                ld      (de), a
                inc     de
                djnz    main_parse_mem_idx_zero
                pop     bc
                jp      main_handle_inst_advance

; ---- MemBaseIdxShifted (shape 5): [idx][idxw][shAmt]
main_parse_mem_idx_shifted:
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; +3 idx
                inc     de
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; +4 idx_width
                inc     de
                inc     de                  ; skip +5 (extend) for shape 5
                ; Write +5 = 0 (no extend for shape 5) — done by zero loop.
                dec     de
                xor     a
                ld      (de), a             ; +5 = 0
                inc     de
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; +6 shift_amt
                inc     de
                push    bc
                ld      b, 3
                xor     a
main_parse_mem_idxs_zero:
                ld      (de), a
                inc     de
                djnz    main_parse_mem_idxs_zero
                pop     bc
                jp      main_handle_inst_advance

; ---- MemBaseIdxExtended (shape 6): [idx][idxw][ext][shAmt]
main_parse_mem_idx_extended:
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; +3 idx
                inc     de
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; +4 idx_width
                inc     de
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; +5 extend
                inc     de
                ld      a, (hl)
                inc     hl
                ld      (de), a             ; +6 shift_amt
                inc     de
                push    bc
                ld      b, 3
                xor     a
main_parse_mem_idxe_zero:
                ld      (de), a
                inc     de
                djnz    main_parse_mem_idxe_zero
                pop     bc
                jp      main_handle_inst_advance


; ---- Parse OpSysName (0x0B) — sysreg / PSTATE / DC-op / TLBI-op name --
;
; On entry: kind byte (0x0B) is already at OPVAL[+0]; DE points at +1.
;           HL points at on-disk byte just past the kind byte (start of
;           len u16).
;
; On-disk payload (operands.go:220-224, WriteSysName):
;   [len u16 LE][bytes...]
;
; In-memory layout written into OPVAL_ARRAY entry (10 bytes total):
;   +0 kind=0x0B
;   +1 zero (reserved)
;   +2..+3 name_ptr (16-bit pointer into IN_BUF)
;   +4..+5 name_len (16-bit length)
;   +6..+9 zero
;
; The encoder (sysname.asm) consumes (ptr,len) to look up the name in
; one of the four tables: sysregs, PSTATE fields, DC ops, TLBI ops.
main_parse_sys_name:
                xor     a
                ld      (de), a             ; +1 = 0 (reserved)
                inc     de
                ld      c, (hl)             ; len LSB
                inc     hl
                ld      b, (hl)             ; len MSB
                inc     hl                  ; HL → name bytes
                ld      a, l
                ld      (de), a             ; +2 = name_ptr LSB
                inc     de
                ld      a, h
                ld      (de), a             ; +3 = name_ptr MSB
                inc     de
                ld      a, c
                ld      (de), a             ; +4 = name_len LSB
                inc     de
                ld      a, b
                ld      (de), a             ; +5 = name_len MSB
                inc     de
; Advance HL by BC (the name bytes).
                add     hl, bc
; Zero +6..+9 (4 bytes of padding).
                push    bc
                ld      b, 4
                xor     a
main_parse_sys_name_zero:
                ld      (de), a
                inc     de
                djnz    main_parse_sys_name_zero
                pop     bc
                jp      main_handle_inst_advance


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
; Mnemonic-ID intercepts (M5 PR-B): ror-imm, OpShiftedReg,
; OpExtendedReg.  See src/m3/intercepts.asm.  Z=1 → handled (skip form
; lookup; PASS_PC already advanced); Z=0 → fall through to form table.
                call    try_mnemonic_intercept
                jp      z, walk_records

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
; Pass-1 INST handler.  Two responsibilities:
;
;   1. PASS_PC += 4 (one aarch64 instruction).
;   2. If the instruction is `ldr Xn|Wn, =<expr>` (mnemonic 5 with an
;      OpLitPool operand), register a literal-pool slot via
;      litpool_scan_inst_record.  All other mnemonics fall straight
;      through — the scanner short-circuits on mnemonic_id != 5.
;
; The reader has already advanced IN_POS past this record's payload
; (reader.asm:147), so we can scan HL→payload non-destructively.
;
; Note: litpool_scan_inst_record reads PASS_PC for the inst_pc field of
; the pc-map.  We MUST scan BEFORE advancing PASS_PC.
; -----------------------------------------------------------------------
main_handle_inst_pass1:
                push    hl
                call    litpool_scan_inst_record
                pop     hl
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
;
; Special-case the directives whose pass-1 effect is something other than
; "PASS_PC += size":
;
;   .equ / .set      — pass-1 inserts (symbol_id, value) into SYMTAB;
;                      size is 0.  Mac-side: refenc/pass1.go:166-170,
;                      :241-271 (resolveEquDirective).
;
;   .org             — pass-1 sets PASS_PC := target directly (not +=).
;                      Backward .org → fail.  Mac-side: pass1.go:173-203.
;                      OriginVMA (at-start .org with target != 0 from
;                      PASS_PC == 0) is punted here per M5 PR-A scope —
;                      no current M1 fixture exercises it.
;
; Every other directive falls through to compute_directive_size +
; pass_pc_advance_de.
main_handle_directive_pass1:
                ld      a, (main_dir_id)
                cp      DIR_EQU
                jp      z, main_dir_equ_pass1
                cp      DIR_SET
                jp      z, main_dir_equ_pass1
                cp      DIR_ORG
                jp      z, main_dir_org_pass1
                cp      DIR_LTORG
                jp      z, main_dir_ltorg

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
; ---- M5 PR A directives -------------------------------------------------
                cp      DIR_EQU
                jp      z, walk_records             ; symbol already inserted in pass 1
                cp      DIR_SET
                jp      z, walk_records             ; .set is an .equ synonym
                cp      DIR_GLOBAL
                jp      z, walk_records             ; no-op (refenc/pass2.go:1719)
                cp      DIR_SECTION
                jp      z, walk_records             ; no-op (refenc/pass2.go:1721-1724)
                cp      DIR_ARCH
                jp      z, walk_records             ; no-op (refenc/pass2.go:1725-1728)
                cp      DIR_CPU
                jp      z, walk_records             ; no-op
                cp      DIR_SKIP
                jp      z, main_dir_skip_emit
                cp      DIR_SPACE
                jp      z, main_dir_skip_emit       ; same handler
                cp      DIR_INST
                jp      z, main_dir_inst_emit
                cp      DIR_BALIGN
                jp      z, main_dir_balign_emit
                cp      DIR_ALIGN
                jp      z, main_dir_align_emit
                cp      DIR_ORG
                jp      z, main_dir_org_pass2
                cp      DIR_LTORG
                jp      z, main_dir_ltorg
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
; ---- M5 PR A directives -------------------------------------------------
                cp      DIR_EQU
                jr      z, compute_dir_size_zero
                cp      DIR_SET
                jr      z, compute_dir_size_zero
                cp      DIR_GLOBAL
                jr      z, compute_dir_size_zero
                cp      DIR_SECTION
                jr      z, compute_dir_size_zero
                cp      DIR_ARCH
                jr      z, compute_dir_size_zero
                cp      DIR_CPU
                jr      z, compute_dir_size_zero
                cp      DIR_ORG
                jr      z, compute_dir_size_zero    ; .org handled in pass-1
                cp      DIR_INST
                jp      z, compute_dir_size_inst
                cp      DIR_SKIP
                jp      z, compute_dir_size_skip
                cp      DIR_SPACE
                jp      z, compute_dir_size_skip
                cp      DIR_BALIGN
                jp      z, compute_dir_size_balign
                cp      DIR_ALIGN
                jp      z, compute_dir_size_align
                cp      DIR_LTORG
                jp      z, compute_dir_size_zero    ; flush is PC-side; size 0
                jp      fail

compute_dir_size_zero:
                ld      bc, 0
                ret

; .inst — fixed 4 bytes (refenc/pass1.go:346-347).
compute_dir_size_inst:
                ld      bc, 4
                ret

; .skip / .space — size = eval(operand1).  We use the low 16 bits;
; M5 PR-A fixtures stay well under 64 KB.  Mac-side: pass1.go:337-345.
compute_dir_size_skip:
                call    main_eval_first_imm
                ld      a, (expr_result + 0)
                ld      c, a
                ld      a, (expr_result + 1)
                ld      b, a
                ret

; .balign N — size = (N - PC%N) % N.
; Mirrors refenc/pass1.go:365-381 with unsigned modulus.  N is read from
; the operand; PASS_PC is the current pc at directive entry.
;
; Both passes call this routine: in pass 1 PASS_PC is the pre-directive
; PC (advanced afterwards via pass_pc_advance_de); in pass 2 the emit
; handler runs first but doesn't advance PASS_PC, then dir_emit_done
; re-calls compute_directive_size — at which point PASS_PC is still the
; pre-directive PC, so the recomputed pad matches what was emitted.
compute_dir_size_balign:
                call    main_eval_first_imm         ; expr_result = N (s64)
                jp      compute_balign_pad_from_n


; .align N — Mac convention `align to 2^N`.  Same as .balign with
; align = 1 << N.  N = 0 is a no-op (pad = 0).  M5 PR-A only sees
; small N (corpus values are 2..4), so we just shift in place.
compute_dir_size_align:
                call    main_eval_first_imm         ; expr_result = N (s64)
; align = 1 << N.  Take low byte of N as the shift count.
                ld      a, (expr_result + 0)
                or      a
                jr      z, compute_dir_size_align_pad0
                cp      32
                jp      nc, fail                    ; alignment too large for u32
; HL = 1 << A (build a 16-bit alignment for now; PR-A fixtures stay <= 16).
                ld      hl, 1
compute_dir_size_align_shift:
                add     hl, hl
                dec     a
                jr      nz, compute_dir_size_align_shift
; Stash HL as N in expr_result+0..1 so compute_balign_pad_from_n can pick
; it up.  Top bytes are already zero from main_eval_first_imm (expr is
; small positive); but zero them explicitly to be safe.
                ld      a, l
                ld      (expr_result + 0), a
                ld      a, h
                ld      (expr_result + 1), a
                xor     a
                ld      (expr_result + 2), a
                ld      (expr_result + 3), a
                jp      compute_balign_pad_from_n

compute_dir_size_align_pad0:
                ld      bc, 0
                ret


; compute_balign_pad_from_n — given N in (expr_result + 0..1) (16-bit
; unsigned), compute pad = (N - PASS_PC%N) % N.  Returns BC = pad.
;
; PASS_PC is a 32-bit value.  For all M5 PR-A fixtures N is a small
; power of two (≤ 16), so PASS_PC % N == (PASS_PC low byte) & (N-1).
; Use the low-byte mask form when N is a power of two for simplicity;
; fall back to long division otherwise (no current fixture needs it).
compute_balign_pad_from_n:
                ld      a, (expr_result + 1)
                or      a
                jp      nz, compute_balign_pad_large_n
                ld      a, (expr_result + 0)
                cp      2
                jp      c, compute_balign_pad_zero  ; N < 2 → pad = 0
; Check power-of-two: N & (N-1) == 0.
                ld      b, a                        ; B = N
                dec     a                           ; A = N-1
                ld      c, a                        ; C = N-1
                and     b
                jp      nz, fail                    ; not power-of-two (PR-A)
; PASS_PC low byte AND (N-1) gives remainder for power-of-two N.
                ld      a, (PASS_PC + 0)
                and     c                           ; A = PASS_PC % N
                jp      z, compute_balign_pad_zero  ; already aligned
                ld      d, a                        ; D = remainder
                ld      a, b                        ; A = N
                sub     d                           ; A = N - remainder
                ld      c, a
                ld      b, 0
                ret

compute_balign_pad_zero:
                ld      bc, 0
                ret

compute_balign_pad_large_n:
; N > 255 — no current PR-A fixture; surface as fail until needed.
                jp      fail

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


; -----------------------------------------------------------------------
; M5 PR-A directive handlers (Tasks 1-3).
; -----------------------------------------------------------------------

; ---- .equ / .set (pass 1) — insert (symbol_id, value) into SYMTAB ----
;
; Both operands are IMM_EXPR records (see emitEqu in
; tools/text2bin/internal/translate/flatten.go).  Operand 1's bytecode
; is exactly [PUSH_SYM=0x05][id_lo][id_hi] (3 bytes) — we extract the
; id by inspection rather than evaluating (the symbol may not be in
; SYMTAB yet, but we only want its id).  Operand 2 is the value
; expression — eval it via main_eval_next_imm.
;
; Pass-2 .equ / .set are no-ops (the symbol was inserted in pass 1).
;
; Mac-side reference: refenc/pass1.go:241-271 (resolveEquDirective);
; format/operands writer: text2bin/internal/translate/flatten.go:283-296
; (emitEqu).
main_dir_equ_pass1:
; Position main_opval_src at operand 1.
                ld      hl, (main_dir_payload_after_header)

; Operand 1 layout: [kind=0x05 IMM_EXPR][len u16][bytecode...].
                ld      a, (hl)
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail
                inc     hl                          ; HL → len LSB
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                          ; HL → bytecode start

; Validate bytecode shape: must be exactly 3 bytes [PUSH_SYM, id_lo, id_hi].
                ld      a, d
                or      a
                jp      nz, fail
                ld      a, e
                cp      3
                jp      nz, fail
                ld      a, (hl)
                cp      &05                         ; PUSH_SYM opcode
                jp      nz, fail
                inc     hl                          ; HL → id_lo
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = symbol_id (C=lo, B=hi)
                inc     hl                          ; HL past operand-1 bytecode

; Stash symbol id; HL still on the kind byte of operand 2.
                ld      (main_dir_equ_pending_id), bc
                ld      (main_opval_src), hl
                ld      a, 1
                ld      (main_op_count_remaining), a    ; only operand 2 left

; Evaluate operand 2 → expr_result (8 bytes LE).
                call    main_eval_next_imm

; Copy low 4 bytes of expr_result into symbol_value_buf.  The address
; field in SYMTAB is u32 — same width as PASS_PC.  M4's evaluator
; already returns 8-byte LE; we drop the high 4 bytes here.
                ld      a, (expr_result + 0)
                ld      (symbol_value_buf + 0), a
                ld      a, (expr_result + 1)
                ld      (symbol_value_buf + 1), a
                ld      a, (expr_result + 2)
                ld      (symbol_value_buf + 2), a
                ld      a, (expr_result + 3)
                ld      (symbol_value_buf + 3), a

; Insert (id, value) — duplicate id → symbol_insert does jp fail.
                ld      hl, (main_dir_equ_pending_id)
                call    symbol_insert
                jp      walk_records


; ---- .org (pass 1) — PASS_PC := target -------------------------------
;
; Per M5 PR-A scope, OriginVMA semantics (at-start .org with target > 0
; from PASS_PC == 0) are punted — no PR-A fixture exercises it.
; Backward .org is an error.  Pass-2 mirror is below.
;
; Mac-side reference: refenc/pass1.go:194-203.
main_dir_org_pass1:
                call    main_eval_first_imm         ; expr_result = target
                jp      main_dir_org_set_pc

; ---- .org (pass 2) — zero-fill from current PC to target ------------
;
; PR-A scope: 16-bit delta only (max 64 KB pad).  M5's IN/OUT buffers
; are 2 KB each, so anything larger would be unusable anyway.  Larger
; deltas are out-of-scope until a fixture needs them.
;
; Mirrors refenc/pass2.go:102-128: emit (target - pc) zero bytes and
; set PASS_PC = target.
main_dir_org_pass2:
                call    main_eval_first_imm         ; expr_result = target

; Validate target's upper 16 bits match PASS_PC (else delta > 64 K).
                ld      a, (expr_result + 2)
                ld      hl, PASS_PC + 2
                cp      (hl)
                jp      nz, fail
                ld      a, (expr_result + 3)
                inc     hl
                cp      (hl)
                jp      nz, fail

; BC = delta = (target_low16 - PASS_PC_low16).  Backward → fail.
                ld      a, (expr_result + 0)
                ld      hl, PASS_PC
                sub     (hl)
                ld      c, a
                ld      a, (expr_result + 1)
                inc     hl
                sbc     a, (hl)
                ld      b, a
                jp      c, fail                     ; target < PASS_PC

main_dir_org_emit_loop:
                ld      a, b
                or      c
                jr      z, main_dir_org_set_pc
                push    bc
                xor     a
                call    emit_byte
                pop     bc
                dec     bc
                jr      main_dir_org_emit_loop

; ---- .ltorg — flush the literal pool (both passes) -------------------
;
; Pass 1: advance PASS_PC by sum(pending pool entry sizes), recording
;         each slot's entry_pc.  No emit.
; Pass 2: same alignment / PC accounting + emit pool bytes.
;
; The litpool_flush helper checks PASS_MODE internally and does the
; right thing.  Both passes follow `walk_records` so a single shared
; handler suffices.
;
; Mac-side reference: refenc/pass1.go:171-172 + refenc/pass2.go:96-100.
main_dir_ltorg:
                call    litpool_flush
                jp      walk_records


; PASS_PC := expr_result.  Shared by pass-1 and pass-2 .org tails.
main_dir_org_set_pc:
                ld      a, (expr_result + 0)
                ld      (PASS_PC + 0), a
                ld      a, (expr_result + 1)
                ld      (PASS_PC + 1), a
                ld      a, (expr_result + 2)
                ld      (PASS_PC + 2), a
                ld      a, (expr_result + 3)
                ld      (PASS_PC + 3), a
                jp      walk_records


; ---- .skip / .space (pass 2 emit) — N zero bytes --------------------
;
; N is computed by compute_dir_size_skip and returned in BC.  We loop
; emit_byte N times.  After emit, jp dir_emit_done re-runs the size
; compute (same value) and advances PASS_PC.
;
; Mac-side reference: refenc/pass2.go:1763-1770.
main_dir_skip_emit:
                call    compute_directive_size      ; BC = N (size, 16-bit)
                ld      a, b
                or      c
                jp      z, dir_emit_done
main_dir_skip_emit_loop:
                push    bc
                xor     a
                call    emit_byte
                pop     bc
                dec     bc
                ld      a, b
                or      c
                jr      nz, main_dir_skip_emit_loop
                jp      dir_emit_done


; ---- .inst (pass 2 emit) — one 32-bit LE word ------------------------
;
; Mac-side reference: refenc/pass1.go:346-347 (size = 4).  Pass-2 emit
; is by-inspection: evaluate the operand expression and emit four bytes
; little-endian.
main_dir_inst_emit:
                call    main_eval_first_imm         ; expr_result = u32 (low 4 bytes)
                ld      a, (expr_result + 0)
                call    emit_byte
                ld      a, (expr_result + 1)
                call    emit_byte
                ld      a, (expr_result + 2)
                call    emit_byte
                ld      a, (expr_result + 3)
                call    emit_byte
                jp      dir_emit_done


; ---- .balign / .align (pass 2 emit) — pad bytes ---------------------
;
; compute_directive_size returns the pad count in BC.  We mirror
; refenc's alignPadBytes (tools/refenc/pass2.go:403-429): zero-fill
; leading bytes until PC is 4-aligned, then NOPs (0xd503201f LE) while
; >= 4 bytes remain, then trailing zeros.
;
; D tracks the running emit-PC offset since the directive started; we
; recompute (PASS_PC + D) % 4 = (PASS_PC + D) & 3 each iteration to
; decide between zero and NOP.  PASS_PC itself is left alone so
; dir_emit_done's compute_directive_size re-evaluates to the same pad.
;
; PR-A fixtures all have pad = 0 (the fixtures' `.balign` directives
; hit at PC=0, where every alignment is a no-op).  The handler is
; general so future promotions don't surface byte-mismatch bugs.
main_dir_balign_emit:
main_dir_align_emit:
                call    compute_directive_size      ; BC = pad
                ld      a, b
                or      c
                jp      z, dir_emit_done
                ld      d, 0                        ; D = bytes emitted so far

main_dir_pad_loop:
                ld      a, b
                or      c
                jp      z, dir_emit_done

; (cur_pc & 3) = (PASS_PC[0] + D) & 3.
                ld      a, (PASS_PC + 0)
                add     a, d
                and     3
                jr      nz, main_dir_pad_zero       ; not 4-aligned → zero

; 4-aligned.  Emit NOP if at least 4 bytes remain; else fall through.
                ld      a, c
                cp      4
                jr      nc, main_dir_pad_nop
                ld      a, b
                or      a
                jr      z, main_dir_pad_zero        ; < 4 left → trailing zero

main_dir_pad_nop:
                push    bc
                push    de
                ld      a, &1f
                call    emit_byte
                ld      a, &20
                call    emit_byte
                ld      a, &03
                call    emit_byte
                ld      a, &d5
                call    emit_byte
                pop     de
                pop     bc
                dec     bc
                dec     bc
                dec     bc
                dec     bc
                ld      a, d
                add     a, 4
                ld      d, a
                jr      main_dir_pad_loop

main_dir_pad_zero:
                push    bc
                push    de
                xor     a
                call    emit_byte
                pop     de
                pop     bc
                dec     bc
                inc     d
                jr      main_dir_pad_loop


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
; main_eval_first_imm — reset main_opval_src to the start of the
; directive's operand list, then evaluate the first IMM_EXPR.  Used by
; compute_directive_size for PC-dependent / size-from-operand directives
; (.skip, .space, .inst, .balign, .align, .org) so they can be
; re-evaluated in dir_emit_done after the emit loop has consumed
; main_opval_src.  M5 PR-A.
;
; Side-effect: decrements main_op_count_remaining (harmless — pass-1's
; main_handle_directive_pass1 doesn't read it afterwards, and pass-2
; emit handlers manage their own counter explicitly).
;
; Input:  none (reads main_dir_payload_after_header).
; Output: expr_result = 8-byte LE eval of operand 1.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
main_eval_first_imm:
                ld      hl, (main_dir_payload_after_header)
                ld      (main_opval_src), hl
                jp      main_eval_next_imm          ; tail-call


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
; save_out_file — HSAVE the paged OUT buffer as file "OUT".
;
; Per docs/specs/2026-05-27-samdos-save-idiom.md.  HSAVE manages its
; own HMPR (saves at entry, restores at exit) and auto-pages across
; &C000 inside its save loop (`samdos/src/c.s:354-369 ctas`).  So the
; caller only has to populate UIFA[31..36]:
;
;   byte 31    : start page → HSAVE sets HMPR low 5 bits from this
;                (= OUT_BASE_PAGE = 5; page 5 then page 6 via auto-page).
;   bytes 32-33: source offset in section-C form (= &8000).
;   byte 34    : pages count = OUT_LEN >> 14.
;   bytes 35-36: remainder length = OUT_LEN & 0x3FFF.
;
; LMPR is unchanged across HSAVE (ROM PTDOS save/restores).  After
; this call: OUT is on disk; assembler-side LMPR/HMPR back to
; pre-call values; IX clobbered to dchan (we don't read it).
; -----------------------------------------------------------------------
save_out_file:
                ld      hl, name_OUT
                call    fill_uifa

                ld      a, OUT_BASE_PAGE
                ld      (UIFA + 31), a              ; HSAVE: HMPR low5 = page

                ld      hl, &8000                   ; section-C source offset
                ld      (UIFA + 32), hl             ; HSAVE: HL = (hd0d1) = UIFA[32-33]

                ld      hl, (OUT_LEN)               ; total bytes (0..32767)
                ld      a, h
                rlca
                rlca
                and     3
                ld      (UIFA + 34), a              ; pages = OUT_LEN >> 14

                ld      a, h
                and     &3f
                ld      h, a
                ld      (UIFA + 35), hl             ; remainder = OUT_LEN & 0x3FFF

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

; M5 PR-A scratch — .equ helper.
;
; main_dir_equ_pending_id: symbol id extracted from operand 1 (held
;                          across the operand-2 evaluation).
main_dir_equ_pending_id:        defw    0

; -----------------------------------------------------------------------
; Globals shared between reader / encoder / main_loop.
; -----------------------------------------------------------------------

; Paged IN cursor — see docs/specs/2026-05-27-m6-paged-in-design.md.
;
; IN lives in physical pages 7..10 (off-axis).  The 24-bit cursor is
; stored as a (page, offset) pair: IN_POS_PAGE holds the LMPR low5+RAM0
; byte (i.e. a full LMPR value, &27..&2A for pages 7..10), IN_POS_OFFSET
; the section-A offset (&0000..&3FFF).  in_normalise_hl re-normalises
; into [&0000, &4000) after a page-crossing add, bumping LMPR's low 5
; bits.
;
; IN_END_PAGE / IN_END_OFFSET together point one past the last valid
; byte; they're set by load_in_file_paged from the .tbn's DIFA bytes.
IN_POS_PAGE:            defb    0           ; current LMPR low5+RAM0 for IN
IN_POS_OFFSET:          defw    0           ; current offset in that page
                                            ;   (&0000..&3FFF)
IN_END_PAGE:            defb    0           ; last byte's LMPR low5+RAM0
IN_END_OFFSET:          defw    0           ; last byte's offset in that page

; Paged OUT cursor state — see docs/specs/2026-05-27-m6-paged-out-design.md.
; OUT_PC walks section B (&4000..&7FFF); OUT_ZONE flips 0 → 1 at the
; first byte 16384 to switch from page 5 (under LMPR_ENCTAB) to page 6
; (under LMPR_OUT_HIGH).  OUT_LEN is the total emitted byte count
; (16-bit; cap is 32 KB given the two-page allocation).
OUT_PC:                 defw    0           ; next emit position (section B)
OUT_LEN:                defw    0           ; bytes emitted so far
OUT_ZONE:               defb    0           ; 0 = low zone (section B, page 5);
                                            ; 1 = high zone (LMPR=&25, page 6)
