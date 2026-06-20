; test_pass1_ir.asm — flat-memory harness for the Z80 Pass1-over-IR walk
; (i48c-b8a).
;
; Z80 port of the host first pass tools/sam-aarch64/assemble/pass1.go::Pass1,
; walking the in-memory IR record stream that src/asmparse.asm parse_run emits
; (PARSE_RECS) and computing, for the record stream:
;
;   * each LABEL_DEF's name->PC (via symbol_insert);
;   * each LOCAL_DEF's digit->PC (via local_def_append);
;   * each `.equ`/`.set` symbol's value (via eval_expr_const + symbol_insert);
;   * the `.org` origin / PC advance;
;   * literal-pool slot placement (via litpool_register / litpool_flush over
;     the LDR-litpool operands of each INST record).
;
; DESIGN — leaf reuse, thin new dispatch (i48c-b8a Option A).
;
; The genuinely flat-safe pass-1 leaves are reused unchanged: the expression
; evaluator (expr_eval.asm eval_expr_const), the symbol table (symbols.asm),
; the local-label table (local_labels.asm), the literal pool (litpool.asm) and
; the 64-bit arithmetic helpers (ml.asm). The thin parts new here are the IR
; record decoder + per-kind dispatch (pass1_ir_walk) and a faithful port of
; pass1.go's PC accounting and directive sizing.
;
; The compact-stream walk (main_loop.asm walk_records) and its directive
; handlers (main_handle_directive_pass1 / compute_directive_size) are NOT
; included: main_loop.asm references the encoder / OUT emitter / paged-IN reader
; / ENCTAB form-lookup (emit_byte, reader_next_kind, enctab_map_in, …), so
; pulling it into a flat harness drags in the whole paged assembler, and its
; record-staging buffers collide with the pass-1 tables in the &C100-&EFFF
; scratch region. The directive PC accounting reimplemented here (pass1_dir_*)
; is a direct port of pass1.go's directiveSizeAtPC + the `.equ`/`.org`/`.ltorg`
; cases of Pass1, so it is the same authority the main_loop.asm handlers port.
;
; This binary contains ONLY the pass-1 machinery; the IR record buffer is
; produced by the Go harness (frontend.Translate serialised into the parse_run
; wire layout) and written to PASS1_IR_BUF before the run. The host oracle is
; assemble.Pass1 over the same records, byte-compared in
; tools/netboot-oracle/z80/pass1_ir_test.go.
;
; Provenance: tools/sam-aarch64/assemble/pass1.go (Pass1 / directiveSizeAtPC /
; litPoolOperand / resolveEquDirective). Verified host-side in pass1_ir_test.go.

; The org + leaf includes belong to the standalone pass1-ir harness. When this
; file is `include`d by another harness (test_compact_ir.asm, i48c-b8b) the
; includer owns the org and the leaf includes, so they are gated out here to
; avoid a double-org / double-definition (mirrors asmparse.asm's
; ASMPARSE_STANDALONE guard). PASS1_IR_STANDALONE is set by the pass1-ir-z80
; Makefile target.
                if defined(PASS1_IR_STANDALONE)
                org     &8000
                endif

                include "tbn_constants.inc"


; -----------------------------------------------------------------------
; Memory map — the &C100-&EFFF scratch addresses the reused leaves assume.
; These mirror src/assembler.asm exactly (the leaves equ these from there in
; the integrated build); a flat harness must reserve the identical addresses
; so symbols.asm / local_labels.asm / litpool.asm / expr_eval.asm see the
; tables where they expect them. A drift here is a silent corruption.
; -----------------------------------------------------------------------
OPVAL_ARRAY:    equ     &C100          ; 70 bytes — operand value array
OPVAL_KINDS:    equ     &C150          ; 7 bytes — kinds[]
PASS_MODE:      equ     &C158          ; 1 byte — current pass (PASS_PASS1)
PASS_PC:        equ     &C159          ; 4 bytes — current pass PC (u32 LE)
ORIGIN_HIGH:    equ     &C960          ; 4 bytes — high word of OriginVMA (u32 LE)
SYMTAB_ABS_BITMAP: equ  &C964          ; 64 bytes — per-id absolute flag
OPMEM_OFF:      equ     &D100          ; 8 bytes — OpMem offset (s64 LE)
STAGING_BUF:    equ     &D500          ; 1 KB record staging area
STAGING_BUF_END: equ    &D900
LITPOOL_EXPR_BUF: equ   &D900          ; 2 KB cross-pass expr pool
LITPOOL_EXPR_BUF_END: equ &E100

; COMPACT_REC_PC — per-record RecordPC array for the optional i48c-b8b capture
; (4-byte LE PASS_PC per record, 128-record cap). Sits in the &F000-page tail
; above SYMTAB_OVERFLOW (&E800-&F000); free in both this standalone harness and
; the b8b compact harness that includes this file. b8a never reads it (capture
; flag stays 0); the symbol exists so the link resolves.
COMPACT_REC_PC: equ     &F000          ; 128 * 4 = 512 B → &F200

PASS_PASS1:     equ     1
PASS_PASS2:     equ     2

; PUSH_SYM expression opcode (tools/sam-aarch64-format/expr.go OpPushSym).
; Operand 1 of a `.equ`/`.set` is exactly [PUSH_SYM, id_lo, id_hi]; the id is
; extracted by inspection (the symbol may not be in the table yet).
EXPR_PUSH_SYM:  equ     &05


; -----------------------------------------------------------------------
; IR record buffer + result-readout pointers (host harness reads these back).
; The buffer sits in the &8000-page after the code; PASS1_IR_LEN is the record
; byte count the harness writes before calling pass1_ir_walk.
; -----------------------------------------------------------------------


; -----------------------------------------------------------------------
; pass1_ir_walk — entry point. Walk the IR record stream at PASS1_IR_BUF
; (PASS1_IR_LEN bytes) computing label/local positions, `.equ`/`.set` values,
; and litpool placement. Initialises the symbol/local/litpool tables and
; PASS_PC, walks the records, then flushes the pending literal pool at
; end-of-input (mirrors Pass1's trailing flushPool(pc), pass1.go:311).
;
; Input:  PASS1_IR_BUF populated, PASS1_IR_LEN set (u16).
; Output: SYMTAB / LOCAL_LABEL_TABLE / LITPOOL_TABLE populated.
; Clobbers: everything.
; -----------------------------------------------------------------------
pass1_ir_walk:
                ld      a, PASS_PASS1
                ld      (PASS_MODE), a
                call    symbol_table_init
                call    local_label_table_init
                call    litpool_init
                call    pass_pc_reset

; HL = cursor into the IR buffer; DE = bytes remaining.
                ld      hl, PASS1_IR_BUF
                ld      (p1ir_cursor), hl
                ld      hl, (PASS1_IR_LEN)
                ld      (p1ir_remaining), hl

; Record-index counter for the optional RecordPC capture (i48c-b8b). Reset to 0
; here; incremented per record only when p1ir_capture_recpc != 0 (b8b sets it).
                xor     a
                ld      (p1ir_rec_index), a
                ld      (p1ir_rec_index + 1), a

p1ir_loop:
                ld      hl, (p1ir_remaining)
                ld      a, h
                or      l
                jr      z, p1ir_done                ; remaining == 0 → flush + return

; Optional RecordPC capture (i48c-b8b): host Compact anchors COMMENT/BLANK_RUN
; to p1.RecordPC[i] = the PC at the START of record i (pass1.go:173). When the
; b8b compact walk needs those anchors it sets p1ir_capture_recpc; the pass1
; walk then stores PASS_PC (4-byte LE) into COMPACT_REC_PC[rec_index] before
; processing each record, exactly as Pass1 appends res.RecordPC at the loop top.
                ld      a, (p1ir_capture_recpc)
                or      a
                call    nz, p1ir_store_recpc

                ld      hl, (p1ir_cursor)
                ld      a, (hl)                     ; record kind tag

                cp      REC_KIND_INST
                jp      z, p1ir_inst
                cp      REC_KIND_LABEL_DEF
                jp      z, p1ir_label_def
                cp      REC_KIND_LOCAL_DEF
                jp      z, p1ir_local_def
                cp      REC_KIND_DIRECTIVE
                jp      z, p1ir_directive
                cp      REC_KIND_COMMENT
                jp      z, p1ir_skip_framed
                cp      REC_KIND_BLANK_RUN
                jp      z, p1ir_skip_framed
                jp      fail                        ; unknown record kind

p1ir_done:
; Implicit flush at end of input (pass1.go:311 flushPool(pc)).
                call    litpool_flush
                ret


; -----------------------------------------------------------------------
; p1ir_store_recpc — store PASS_PC (4-byte LE) into COMPACT_REC_PC at the slot
; for the current record index, then increment the index. Faithful to
; pass1.go:173 (res.RecordPC = append(res.RecordPC, pc)) so the b8b compact walk
; reads RecordPC[i] = PC at the start of record i.
;
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
p1ir_store_recpc:
                ld      hl, (p1ir_rec_index)
                add     hl, hl                      ; *2
                add     hl, hl                      ; *4 (4 bytes per slot)
                ld      de, COMPACT_REC_PC
                add     hl, de                      ; HL = &COMPACT_REC_PC[index]
                ex      de, hl                      ; DE = dest slot
                ld      hl, PASS_PC
                ld      bc, 4
                ldir
                ld      hl, (p1ir_rec_index)
                inc     hl
                ld      (p1ir_rec_index), hl
                ret


; -----------------------------------------------------------------------
; p1ir_advance — advance the cursor by BC bytes and decrement remaining by
; the same. BC is the TOTAL on-wire size of the record just consumed.
;
; Input:  BC = record size in bytes.
; Output: p1ir_cursor += BC; p1ir_remaining -= BC. jp p1ir_loop.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
p1ir_advance:
                ld      hl, (p1ir_cursor)
                add     hl, bc
                ld      (p1ir_cursor), hl
                ld      hl, (p1ir_remaining)
                or      a                           ; clear CF
                sbc     hl, bc
                ld      (p1ir_remaining), hl
                jp      p1ir_loop


; -----------------------------------------------------------------------
; p1ir_skip_framed — COMMENT / BLANK_RUN: framed [kind:1][len:2 LE][payload],
; no PC effect. Consume 3 + len bytes (pass1.go has no case for these kinds —
; they contribute zero PC, exactly like the record-walk's default skip).
;
; Input:  (p1ir_cursor) at the kind tag.
; -----------------------------------------------------------------------
p1ir_skip_framed:
                ld      hl, (p1ir_cursor)
                inc     hl                          ; → len LSB
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = payload len
                inc     bc
                inc     bc
                inc     bc                          ; + 3 framing bytes
                jp      p1ir_advance


; -----------------------------------------------------------------------
; p1ir_label_def — LABEL_DEF: framed [kind:1][len:2 LE = 2][sym_id:2 LE].
; Capture sym_id -> current PASS_PC via symbol_insert (pass1.go:176-182:
; res.Symbols[name] = pc; res.LabelDefs[name] = true).
;
; symbol_insert reads the value from symbol_value_buf (4-byte LE) and takes the
; id in HL; PASS_PC is the byte offset from origin, which is exactly what the
; record walk stores (and matches Pass1's per-record pc). A duplicate id is an
; error in symbol_insert (jp fail), matching Pass1 only ever assigning a given
; LABEL_DEF name once in a well-formed stream.
; -----------------------------------------------------------------------
p1ir_label_def:
; symbol_value_buf := PASS_PC (4-byte LE) FIRST (the copy clobbers BC/DE/HL).
                call    p1ir_copy_pc_to_symval

; Read sym_id at cursor+3..+4 into HL, then symbol_insert.
                ld      hl, (p1ir_cursor)
                inc     hl
                inc     hl                          ; skip kind + len LSB
                inc     hl                          ; skip len MSB → sym_id LSB
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = sym_id
                ld      h, b
                ld      l, c                        ; HL = sym_id
                call    symbol_insert

; Record size = 3 (framing) + 2 (sym_id) = 5.
                ld      bc, 5
                jp      p1ir_advance


; -----------------------------------------------------------------------
; p1ir_local_def — LOCAL_DEF: framed [kind:1][len:2 LE = 1][digit:1].
; Append digit -> current PASS_PC (pass1.go:183-185:
; res.LocalDefs[rec.Digit] = append(..., pc)).
;
; local_def_append reads the PC from local_label_pc_buf (4-byte LE) and takes
; the digit in A.
; -----------------------------------------------------------------------
p1ir_local_def:
                ld      hl, (p1ir_cursor)
                inc     hl
                inc     hl
                inc     hl                          ; skip kind + len → digit
                ld      a, (hl)
                ld      (p1ir_saved_digit), a

; local_label_pc_buf := PASS_PC (4-byte LE).
                ld      hl, PASS_PC
                ld      de, local_label_pc_buf
                ld      bc, 4
                ldir

                ld      a, (p1ir_saved_digit)
                call    local_def_append

; Record size = 3 (framing) + 1 (digit) = 4.
                ld      bc, 4
                jp      p1ir_advance


; -----------------------------------------------------------------------
; p1ir_inst — INST: [kind:1][mnem_id:2 LE][op_count:1][ops_len:2 LE][ops].
; The instruction occupies 4 output bytes (pass1.go:214 pc += 4) AND, when it
; is an `ldr Xn|Wn, =expr` form, registers a litpool entry (pass1.go:190-213).
;
; litpool_scan_inst_record does exactly the litpool detection + registration:
; it takes HL = payload start at the mnemonic_id LSB, short-circuits on the ldr
; mnemonic id, scans the operand list for OP_KIND_LIT_POOL, and calls
; litpool_register for each (the same dedup-by-(width,expr) + pc-map record as
; the Go pending/PoolEntries logic). It must run with PASS_PC already at this
; instruction's PC (litpool_register stamps eval_pc/pc-map from PASS_PC), so the
; scan happens BEFORE the +4 advance — matching Pass1 (the entry's EvalPC = pc,
; pass1.go:199, before pc += 4).
; -----------------------------------------------------------------------
p1ir_inst:
; ops_len lives at cursor+4..+5; total record size = 6 + ops_len.
                ld      hl, (p1ir_cursor)
                push    hl                          ; save payload-at-kind ptr
                ld      bc, 4
                add     hl, bc                      ; → ops_len LSB
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = ops_len
                inc     bc
                inc     bc
                inc     bc
                inc     bc
                inc     bc
                inc     bc                          ; + 6 header bytes
                ld      (p1ir_inst_size), bc

; Scan the INST's operands for an LDR-litpool operand (HL → kind tag).
                pop     hl                          ; HL = kind tag
                call    p1ir_litpool_scan_inst

; pc += 4 (the instruction's own 4 bytes).
                call    pass_pc_advance_4

                ld      bc, (p1ir_inst_size)
                jp      p1ir_advance


; -----------------------------------------------------------------------
; p1ir_directive — DIRECTIVE: framed [kind:1][len:2 LE][dir_id:1][op_count:1]
; [operands]. Stage (dir_id, op_count, operands-ptr) into the scratch the
; sizer/eval helpers read, then dispatch on dir_id per pass1.go's directive
; switch (the `.equ`/`.set`/`.ltorg`/`.org` special cases, else
; directiveSizeAtPC + pc += n).
; -----------------------------------------------------------------------
p1ir_directive:
                ld      hl, (p1ir_cursor)
                inc     hl                          ; → len LSB
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = payload len
                inc     hl                          ; → dir_id
; Total record size = 3 (framing) + payload_len.
                push    bc
                inc     bc
                inc     bc
                inc     bc
                ld      (p1ir_dir_size), bc
                pop     bc                          ; BC = payload len (unused below)

                ld      a, (hl)                     ; dir_id
                ld      (main_dir_id), a
                inc     hl                          ; → op_count
                ld      a, (hl)
                ld      (main_op_count), a
                ld      (main_op_count_remaining), a
                inc     hl                          ; → operands start
                ld      (main_dir_payload_after_header), hl

; Special-case directives whose pass-1 effect differs from "pc += size"
; (pass1.go:260-307).
                ld      a, (main_dir_id)
                cp      DIR_EQU
                jp      z, p1ir_dir_equ
                cp      DIR_SET
                jp      z, p1ir_dir_equ
                cp      DIR_LTORG
                jp      z, p1ir_dir_ltorg
                cp      DIR_ORG
                jp      z, p1ir_dir_org

; Default: pc += directiveSizeAtPC (BC = size).
                call    pass1_dir_size
                ld      d, b
                ld      e, c
                call    pass_pc_advance_de
                ld      bc, (p1ir_dir_size)
                jp      p1ir_advance


; ---- .equ / .set — insert (symbol_id, value); size 0 (pass1.go:262-266,
; resolveEquDirective pass1.go:354-387). Operand 1 is a PUSH_SYM expr giving
; the symbol id; operand 2 is the value expression. This is a faithful port of
; main_loop.asm main_dir_equ_pass1 (which ports the same Go function): extract
; the id from operand 1 by inspection, evaluate operand 2, classify
; origin-relative vs absolute against ORIGIN_HIGH, and symbol_insert.
p1ir_dir_equ:
                ld      hl, (main_dir_payload_after_header)

; Operand 1: [OP_KIND_IMM_EXPR][len:2 LE][PUSH_SYM, id_lo, id_hi].
                ld      a, (hl)
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail
                inc     hl                          ; → len LSB
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                          ; → bytecode

; Validate bytecode is exactly 3 bytes [PUSH_SYM, id_lo, id_hi].
                ld      a, d
                or      a
                jp      nz, fail
                ld      a, e
                cp      3
                jp      nz, fail
                ld      a, (hl)
                cp      EXPR_PUSH_SYM
                jp      nz, fail
                inc     hl                          ; → id_lo
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = symbol id
                inc     hl                          ; → operand 2 kind byte

                ld      (p1ir_equ_id), bc
                ld      (main_opval_src), hl
                ld      a, 1
                ld      (main_op_count_remaining), a    ; only operand 2 remains

; Evaluate operand 2 → expr_result (8-byte LE).
                call    main_eval_next_imm

; symbol_value_buf := low 4 bytes of expr_result.
                ld      a, (expr_result + 0)
                ld      (symbol_value_buf + 0), a
                ld      a, (expr_result + 1)
                ld      (symbol_value_buf + 1), a
                ld      a, (expr_result + 2)
                ld      (symbol_value_buf + 2), a
                ld      a, (expr_result + 3)
                ld      (symbol_value_buf + 3), a

; Classify origin-relative vs absolute: high word (expr_result[4..7]) ==
; ORIGIN_HIGH → origin-relative; otherwise absolute (must sign-extend from
; bit 31, else a >32-bit constant would truncate). Faithful to main_dir_equ_*.
                ld      hl, expr_result + 4
                ld      de, ORIGIN_HIGH
                ld      b, 4
p1ir_equ_high_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, p1ir_equ_mark_abs
                inc     hl
                inc     de
                djnz    p1ir_equ_high_cmp
                jr      p1ir_equ_insert             ; high == ORIGIN_HIGH

p1ir_equ_mark_abs:
                ld      a, (expr_result + 3)
                add     a, a                        ; bit 31 → CF
                sbc     a, a                        ; A = 0x00 or 0xFF = expected sign byte
                ld      e, a
                ld      hl, expr_result + 4
                ld      b, 4
p1ir_equ_signext_cmp:
                ld      a, (hl)
                cp      e
                jr      nz, p1ir_equ_trunc_fail
                inc     hl
                djnz    p1ir_equ_signext_cmp
                ld      hl, (p1ir_equ_id)
                call    symbol_mark_absolute
                jr      p1ir_equ_insert
p1ir_equ_trunc_fail:
                ld      a, &64
                jp      fail_with_tag               ; tag 64: .equ/.set value out of 32-bit range

p1ir_equ_insert:
                ld      hl, (p1ir_equ_id)
                call    symbol_insert
                ld      bc, (p1ir_dir_size)
                jp      p1ir_advance


; ---- .ltorg — flush the literal pool (pass1.go:267-269). Size 0; the flush
; advances PASS_PC over the pool bytes. litpool_flush checks PASS_MODE and does
; the pass-1 accounting (pad + per-slot advance, no emit).
p1ir_dir_ltorg:
                call    litpool_flush
                ld      bc, (p1ir_dir_size)
                jp      p1ir_advance


; ---- .org — set PASS_PC := target (pass1.go:270-300). The OriginVMA case
; (first .org before any output, target != 0) is faithfully ported: at PASS_PC
; == 0 with no pool entries and ORIGIN_HIGH still 0, the target seeds both
; PASS_PC and ORIGIN_HIGH. Otherwise the target must be >= current PASS_PC
; (backward .org is an error) and PASS_PC := target.
;
; PASS_PC tracks the low 32 bits; the .org backward / set logic mirrors
; main_dir_org_pass1 + main_dir_org_set_pc.
p1ir_dir_org:
                call    main_eval_first_imm         ; expr_result = target (8-byte LE)

; Backward check: CF = (target_low32 < PASS_PC).
                ld      a, (expr_result + 0)
                ld      hl, PASS_PC
                sub     (hl)
                ld      a, (expr_result + 1)
                inc     hl
                sbc     a, (hl)
                ld      a, (expr_result + 2)
                inc     hl
                sbc     a, (hl)
                ld      a, (expr_result + 3)
                inc     hl
                sbc     a, (hl)
                jp      c, p1ir_org_backward

; PASS_PC := expr_result[0..3]; ORIGIN_HIGH := expr_result[4..7].
                ld      a, (expr_result + 0)
                ld      (PASS_PC + 0), a
                ld      a, (expr_result + 1)
                ld      (PASS_PC + 1), a
                ld      a, (expr_result + 2)
                ld      (PASS_PC + 2), a
                ld      a, (expr_result + 3)
                ld      (PASS_PC + 3), a
                ld      hl, expr_result + 4
                ld      de, ORIGIN_HIGH
                ld      bc, 4
                ldir
                ld      bc, (p1ir_dir_size)
                jp      p1ir_advance

p1ir_org_backward:
                ld      a, &a1
                jp      fail_with_tag               ; tag a1: backward .org


; -----------------------------------------------------------------------
; p1ir_litpool_scan_inst — scan an IR INST record's operands for an LDR-
; litpool operand and register a pool entry for each (pass1.go:190-213). This
; mirrors litpool.asm litpool_scan_inst_record exactly EXCEPT for the record
; header: the IR INST carries an extra ops_len u16 between op_count and the
; operands ([kind][mnem:2][op_count:1][ops_len:2][ops]), whereas the compact
; record litpool_scan_inst_record reads omits ops_len. The operand-skip and the
; litpool_register call are reused unchanged.
;
; Only `ldr` (mnemonic id 5) produces OP_KIND_LIT_POOL, so the scan short-
; circuits on the mnemonic id, matching the Go `rec.MnemonicID == ldrID` guard.
;
; Input:  HL = INST record kind tag.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
p1ir_litpool_scan_inst:
                inc     hl                          ; → mnemonic_id LSB
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                          ; → op_count
                ld      a, d
                or      a
                ret     nz                          ; mnem > 255 → not ldr
                ld      a, e
                cp      5
                ret     nz                          ; not ldr

                ld      a, (hl)                     ; op_count
                inc     hl                          ; → ops_len LSB
                or      a
                ret     z                           ; no operands
                ld      (p1ir_scan_remaining), a
                inc     hl
                inc     hl                          ; skip ops_len u16 → operands

p1ir_scan_loop:
                ld      a, (hl)                     ; operand kind byte
                inc     hl
                cp      OP_KIND_LIT_POOL
                jr      z, p1ir_scan_hit

; Skip a non-litpool operand (reused from litpool.asm).
                call    litpool_skip_operand
                jp      p1ir_scan_after

p1ir_scan_hit:
; OP_KIND_LIT_POOL payload: [width u8][expr_len u16 LE][bytecode].
                ld      a, (hl)                     ; width
                inc     hl
                ld      (p1ir_scan_width), a
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                          ; HL → bytecode; BC = expr_len
                ld      (p1ir_scan_expr_ptr), hl
                ld      (p1ir_scan_expr_len), bc

; Advance HL past the bytecode for any subsequent operands.
                add     hl, bc
                ex      de, hl                      ; DE = post-bytecode ptr
                push    de

                ld      a, (p1ir_scan_width)
                ld      hl, (p1ir_scan_expr_ptr)
                ld      bc, (p1ir_scan_expr_len)
                call    litpool_register            ; reused leaf

                pop     hl                          ; HL = post-bytecode ptr

p1ir_scan_after:
                ld      a, (p1ir_scan_remaining)
                dec     a
                ret     z
                ld      (p1ir_scan_remaining), a
                jp      p1ir_scan_loop


; -----------------------------------------------------------------------
; p1ir_copy_pc_to_symval — symbol_value_buf := PASS_PC (4-byte LE).
; Clobbers: BC, DE, HL.
; -----------------------------------------------------------------------
p1ir_copy_pc_to_symval:
                ld      hl, PASS_PC
                ld      de, symbol_value_buf
                ld      bc, 4
                ldir
                ret


; -----------------------------------------------------------------------
; pass1_dir_size — byte contribution of the directive currently staged in
; (main_dir_id)/(main_op_count)/(main_dir_payload_after_header), returned in BC.
;
; Faithful port of pass1.go directiveSizeAtPC (the non-special directives). PC-
; dependent / operand-derived directives (.skip/.space/.balign/.align) evaluate
; their first operand via main_eval_first_imm (-> eval_expr_const) at the
; current PASS_PC, mirroring the Go directiveSizeAtPC's pass1EvalCtx(pc, …).
;
; Output: BC = size (u16). M1 fixtures stay well within 16 bits.
; Clobbers: A, BC, DE, HL (and expr_result).
; -----------------------------------------------------------------------
pass1_dir_size:
                ld      a, (main_dir_id)
                cp      DIR_BYTE
                jp      z, p1ds_x1
                cp      DIR_SHORT
                jp      z, p1ds_x2
                cp      DIR_HWORD
                jp      z, p1ds_x2
                cp      DIR_WORD
                jp      z, p1ds_x4
                cp      DIR_QUAD
                jp      z, p1ds_x8
                cp      DIR_ASCII
                jp      z, p1ds_ascii
                cp      DIR_ASCIZ
                jp      z, p1ds_asciz
                cp      DIR_INST
                jp      z, p1ds_inst
                cp      DIR_SKIP
                jp      z, p1ds_skip
                cp      DIR_SPACE
                jp      z, p1ds_skip
                cp      DIR_BALIGN
                jp      z, p1ds_balign
                cp      DIR_ALIGN
                jp      z, p1ds_align
; Zero-byte directives (.text/.data/.global/.section/.arch/.cpu/.equ/.set/
; .org/.ltorg). .equ/.set/.org/.ltorg never reach here (handled as special
; cases), but they are listed in directiveSizeAtPC as size-0 too.
                cp      DIR_TEXT
                jp      z, p1ds_zero
                cp      DIR_DATA
                jp      z, p1ds_zero
                cp      DIR_GLOBAL
                jp      z, p1ds_zero
                cp      DIR_SECTION
                jp      z, p1ds_zero
                cp      DIR_ARCH
                jp      z, p1ds_zero
                cp      DIR_CPU
                jp      z, p1ds_zero
                cp      DIR_EQU
                jp      z, p1ds_zero
                cp      DIR_SET
                jp      z, p1ds_zero
                cp      DIR_ORG
                jp      z, p1ds_zero
                cp      DIR_LTORG
                jp      z, p1ds_zero
                jp      fail                        ; unknown directive

p1ds_zero:
                ld      bc, 0
                ret

p1ds_inst:
                ld      bc, 4
                ret

; size = op_count * 1
p1ds_x1:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                ret

; size = op_count * 2
p1ds_x2:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                sla     c
                rl      b
                ret

; size = op_count * 4
p1ds_x4:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                sla     c
                rl      b
                sla     c
                rl      b
                ret

; size = op_count * 8
p1ds_x8:
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
p1ds_ascii:
                ld      hl, 0
                ld      (p1ds_acc), hl
                call    p1ds_sum_string_lengths
                ld      bc, (p1ds_acc)
                ret

; size = sum of STRING lengths + op_count (one trailing NUL per string)
p1ds_asciz:
                ld      hl, 0
                ld      (p1ds_acc), hl
                call    p1ds_sum_string_lengths
                ld      hl, (p1ds_acc)
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                add     hl, bc
                ld      b, h
                ld      c, l
                ret

; .skip / .space — size = eval(operand1). Low 16 bits.
p1ds_skip:
                call    main_eval_first_imm
                ld      a, (expr_result + 0)
                ld      c, a
                ld      a, (expr_result + 1)
                ld      b, a
                ret

; .balign N — pad = (N - PASS_PC%N) % N, N a power of two (M1 fixtures use
; small powers of two). pass1.go:477-493.
p1ds_balign:
                call    main_eval_first_imm         ; expr_result = N
                jp      p1ds_pad_from_n

; .align N — align to 2^N bytes (pass1.go:494-510). N small for the corpus.
p1ds_align:
                call    main_eval_first_imm         ; expr_result = N
                ld      a, (expr_result + 0)
                or      a
                jr      z, p1ds_zero                ; N == 0 → pad 0
                cp      16
                jp      nc, fail                    ; N >= 16 out of flat-harness range
                ld      hl, 1
p1ds_align_shift:
                add     hl, hl
                dec     a
                jr      nz, p1ds_align_shift
                ld      a, l
                ld      (expr_result + 0), a
                ld      a, h
                ld      (expr_result + 1), a
                jp      p1ds_pad_from_n

; p1ds_pad_from_n — N in expr_result[0..1] (16-bit power of two). Compute
; pad = (N - PASS_PC%N) % N via the low-byte/word mask, returning BC = pad.
p1ds_pad_from_n:
                ld      a, (expr_result + 1)
                or      a
                jp      nz, p1ds_pad_large
                ld      a, (expr_result + 0)
                cp      2
                jp      c, p1ds_pad_zero            ; N < 2 → pad 0
                ld      b, a                        ; B = N
                dec     a
                ld      c, a                        ; C = N-1 (mask)
                and     b
                jp      nz, fail                    ; not a power of two
                ld      a, (PASS_PC + 0)
                and     c                           ; A = PASS_PC % N
                jp      z, p1ds_pad_zero            ; already aligned
                ld      d, a
                ld      a, b
                sub     d
                ld      c, a
                ld      b, 0
                ret

p1ds_pad_zero:
                ld      bc, 0
                ret

; p1ds_pad_large — N >= 256 power of two (covers .align 12 = 4096 etc.).
; pad = (N - (PASS_PC & (N-1))) & (N-1).
p1ds_pad_large:
                ld      a, (expr_result + 2)
                or      a
                jp      nz, fail                    ; N >= 2^16 unsupported here
                ld      a, (expr_result + 3)
                or      a
                jp      nz, fail
                ld      hl, (expr_result + 0)       ; HL = N
                ld      d, h
                ld      e, l
                dec     de                          ; DE = N-1 (mask)
; validate power of two: N & (N-1) == 0
                ld      a, l
                and     e
                jp      nz, fail
                ld      a, h
                and     d
                jp      nz, fail
; remainder = PASS_PC low16 & mask
                ld      a, (PASS_PC + 0)
                and     e
                ld      c, a
                ld      a, (PASS_PC + 1)
                and     d
                ld      b, a
                ld      a, b
                or      c
                jp      z, p1ds_pad_zero            ; already aligned
                ld      a, l
                sub     c
                ld      c, a
                ld      a, h
                sbc     a, b
                ld      b, a
                ret


; -----------------------------------------------------------------------
; p1ds_sum_string_lengths — walk the STRING operand list at
; (main_dir_payload_after_header), summing each string's u16 length into
; (p1ds_acc). Each STRING: [OP_KIND_STRING][len:2 LE][bytes]. Mirrors
; main_loop.asm walk_strings_sum_lengths.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
p1ds_sum_string_lengths:
                ld      a, (main_op_count)
                or      a
                ret     z
                ld      b, a
                ld      hl, (main_dir_payload_after_header)
p1ds_sum_loop:
                ld      a, (hl)
                cp      OP_KIND_STRING
                jp      nz, fail
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                          ; HL → string bytes; DE = len
                push    bc
                push    hl
                ld      hl, (p1ds_acc)
                add     hl, de
                ld      (p1ds_acc), hl
                pop     hl
                pop     bc
                add     hl, de                      ; skip string bytes
                djnz    p1ds_sum_loop
                ret


; -----------------------------------------------------------------------
; main_eval_first_imm / main_eval_next_imm — evaluate IMM_EXPR operands from
; (main_opval_src), leaving the 8-byte LE result in expr_result. Faithful copy
; of main_loop.asm's helpers (they are entangled in main_loop.asm with the
; paged pass-2 code and cannot be included flat). eval_expr_const itself is
; reused unchanged from expr_eval.asm.
; -----------------------------------------------------------------------
main_eval_first_imm:
                ld      hl, (main_dir_payload_after_header)
                ld      (main_opval_src), hl
                ; fall through

main_eval_next_imm:
                ld      hl, (main_opval_src)
                ld      a, (hl)                     ; kind
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                          ; HL → bytecode; BC = len
                push    bc
                call    eval_expr_const             ; HL=ptr, BC=len → expr_result
                pop     bc
                ld      hl, (main_opval_src)
                inc     hl
                inc     hl
                inc     hl                          ; HL → bytecode
                add     hl, bc
                ld      (main_opval_src), hl
                ld      a, (main_op_count_remaining)
                dec     a
                ld      (main_op_count_remaining), a
                ret


; -----------------------------------------------------------------------
; PASS_PC helpers — faithful copies of main_loop.asm pass_pc_* (same reason as
; the eval helpers: entangled with the paged walk). PASS_PC is a 4-byte LE u32.
; -----------------------------------------------------------------------
pass_pc_reset:
                xor     a
                ld      (PASS_PC + 0), a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                ld      hl, ORIGIN_HIGH
                ld      b, 4
pass_pc_reset_oh:
                ld      (hl), a
                inc     hl
                djnz    pass_pc_reset_oh
                ret

pass_pc_advance_4:
                ld      a, 4
                ; fall through

; pass_pc_advance_a — PASS_PC += A (u8, zero-extended).
pass_pc_advance_a:
                ld      hl, PASS_PC
                add     a, (hl)
                ld      (hl), a
                ret     nc
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

; pass_pc_advance_de — PASS_PC += DE (u16, zero-extended).
pass_pc_advance_de:
                ld      hl, PASS_PC
                ld      a, (hl)
                add     a, e
                ld      (hl), a
                ld      e, 0
                jr      nc, ppad_b1
                inc     e
ppad_b1:
                inc     hl
                ld      a, (hl)
                add     a, d
                ld      d, 0
                jr      nc, ppad_b1_addc
                inc     d
ppad_b1_addc:
                add     a, e
                ld      (hl), a
                jr      nc, ppad_b1_done
                inc     d
ppad_b1_done:
                ld      a, d
                or      a
                ret     z
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


; -----------------------------------------------------------------------
; fail / fail_with_tag — the reused leaves jp fail on an unrecoverable error
; (symbol/litpool overflow, malformed bytecode). In this flat harness a fail is
; a HALT with the tag (if any) parked at p1ir_fail_tag; the host harness sees a
; non-clean halt PC and reports the tag. No emit / border in the harness.
; -----------------------------------------------------------------------
fail:
                xor     a
                ld      (p1ir_fail_tag), a
fail_halt:
                halt
                jr      fail_halt

fail_with_tag:
                ld      (p1ir_fail_tag), a
                jr      fail_halt


; emit_byte — referenced by litpool.asm's pass-2 flush path. This harness runs
; pass 1 only (PASS_MODE = PASS_PASS1), where litpool_flush never emits, so
; emit_byte is unreachable; the stub exists only to satisfy the link. Reaching
; it signals a pass-mode bug, so it traps.
emit_byte:
                ld      a, &e0
                jp      fail_with_tag               ; tag e0: emit_byte reached in pass-1 harness


; -----------------------------------------------------------------------
; Reused pass-1 leaves (flat-safe; see header). Included in dependency order:
; ml.asm (64-bit arithmetic) before expr_eval.asm (uses ml_*), the tables, and
; litpool.asm. None of these reference the paged reader / encoder / OUT emitter
; in their pass-1 paths.
; -----------------------------------------------------------------------
                include "ml.asm"
                include "expr_eval.asm"
                include "symbols.asm"
                include "local_labels.asm"
                include "litpool.asm"


; -----------------------------------------------------------------------
; Scratch + IR buffer (after the code, in the &8000 page).
; -----------------------------------------------------------------------
p1ir_cursor:                    defw    0
p1ir_remaining:                 defw    0
p1ir_inst_size:                 defw    0
p1ir_dir_size:                  defw    0
p1ir_saved_digit:               defb    0
p1ir_equ_id:                    defw    0
p1ir_fail_tag:                  defb    0
p1ir_scan_remaining:            defb    0
p1ir_scan_width:                defb    0
p1ir_scan_expr_ptr:             defw    0
p1ir_scan_expr_len:             defw    0

; i48c-b8b RecordPC capture state. p1ir_capture_recpc gates the capture (0 in the
; b8a standalone harness; the b8b compact walk sets it to 1 before pass1_ir_walk).
p1ir_capture_recpc:             defb    0
p1ir_rec_index:                 defw    0

; Directive staging — the names main_loop.asm's helpers / sizer use; the eval
; helpers above and pass1_dir_size read these.
main_dir_id:                    defb    0
main_op_count:                  defb    0
main_op_count_remaining:        defb    0
main_opval_src:                 defw    0
main_dir_payload_after_header:  defw    0
p1ds_acc:                       defw    0

PASS1_IR_LEN:                   defw    0
; IR record buffer. Sized so that, in the b8b compact harness (which `include`s
; this file and appends the compact-core code after this buffer), the buffer end
; plus the compact code stays below the &C160 SYMTAB the reused leaves populate —
; otherwise pass1 writing symbols would overwrite the compact code (the i48c-b8b
; SYMTAB-vs-code collision). A comment-heavy fixture whose serialised IR overflows
; this is skipped host-side (mirrored as pass1IRBufSize in the Go harness) rather
; than corrupting the tables. The corpus + hand fixtures fit comfortably.
PASS1_IR_BUF:                   defs    10752       ; IR record buffer (see above)

