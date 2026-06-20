; test_compact_ir.asm — flat-memory harness for the Z80 compact-core walk
; (i48c-b8b).
;
; Z80 port of the NON-ENCODER body of the host first-pass compactor
; tools/sam-aarch64/assemble/compact.go::Compact (compact.go:46-188). It walks
; the same in-memory IR record stream src/asmparse.asm parse_run emits (the wire
; layout test_pass1_ir.asm consumes) PLUS the pass-1 RecordPC info, and produces
; the three editor/compact outputs that need no instruction encoder:
;
;   * COMMENT / BLANK_RUN records relocate to editor-region sidecar rows
;     (format.SidecarRow), anchored to the record's output PC (RecordPC -
;     OriginVMA, clamped >= 0), in source order (compact.go:103-138);
;   * `.global` directives whose operands are clean symbol references become a
;     name_id list (compact.go:139-148, globalNameIDs compact.go:229-247);
;   * same-directive constant-data directives (.byte/.short/.hword/.word/.quad)
;     coalesce into KindLitData records, split on whole-element boundaries at the
;     litDataMaxBytes (1016) cap (compact.go:61-79, ConstDataWidth, encodeDirective
;     -> evalImmsAsBytes);
;   * LABEL_DEF / LOCAL_DEF drop out of the record stream (compact.go:99-102 —
;     captured by pass1, never re-emitted here);
;   * INST records emit a SKELETON KindInsnRun element (placeholder base word, no
;     patches). The base word + overlay patches are i48c-b8e's job; here only the
;     element COUNT and the 4-byte PC accounting matter, so a mode-0 frame of
;     placeholder words is emitted. The run is flushed on the same record kinds
;     host Compact flushes on (flushInst before a data run, flushData before an
;     inst / at a label / on a different-directive data run).
;
; DESIGN — include the b8a pass1-ir harness, add the compact-core walk on top.
;
; test_pass1_ir.asm (i48c-b8a) is `include`d (its org is gated to
; PASS1_IR_STANDALONE, not set here, so this file owns the org). That pulls in
; the reused leaves (ml/expr_eval/symbols/local_labels/litpool), the pass1 walk
; (pass1_ir_walk), the directive sizer, the eval helpers, fail/fail_with_tag, and
; the shared PASS1_IR_BUF/PASS1_IR_LEN input + COMPACT_REC_PC capture array.
;
; compact_ir_walk runs pass1_ir_walk FIRST (with RecordPC capture enabled) to
; populate the symbol/litpool tables and COMPACT_REC_PC[], then walks the IR a
; second time emitting the compact-core outputs. OriginVMA's low 32 bits are not
; tracked separately — the corpus fixtures are origin 0, so the anchor is
; RecordPC[i] (the captured PASS_PC) directly; a leading `.org` that sets a
; non-zero OriginVMA is out of the b8b non-encoder fixture scope (the host test
; skips it).
;
; Outputs are read back by tools/netboot-oracle/z80/compact_ir_test.go and
; byte-compared against assemble.Compact's sidecar / globals / records.
;
; Provenance: tools/sam-aarch64/assemble/compact.go (Compact / globalNameIDs),
; tools/sam-aarch64-format/litinsts.go (ConstDataWidth), pass2.go (evalImmsAsBytes).

                org     &8000

                include "test_pass1_ir.asm"


; -----------------------------------------------------------------------
; Compact-core RAM map — all runtime state lives in the &F000-&FFFF free block
; (the leaves' scratch tables end at &F000 = top of SYMTAB_OVERFLOW). Defined as
; `equ` (not emitted defw/defs) so none of it lands in the code image: the
; included pass1 harness's 11 KB IR buffer pushes the code image up to ~&C19D,
; so emitted scratch after it would collide with SYMTAB (&C160+). The host
; harness reads the buffers + the count/len cells back after compact_ir_walk;
; their fixed addresses are mirrored in compact_ir_test.go (a drift is a silent
; corruption, like the b8a table-address contract).
;
; COMPACT_REC_PC (&F000, 128*4) is equ'd in the included test_pass1_ir.asm.
; The host test skips any fixture whose sidecar / globals / record stream / data
; run / record-count would overflow these buffers (a fixture-scope limit, like
; the IR-buffer skip — not a compact-core gap), so each buffer is sized for the
; common corpus + the specific hand fixtures, not the worst case.
; -----------------------------------------------------------------------
COMPACT_SIDECAR:        equ     &F200          ; sidecar rows, source order (768 B)
COMPACT_GLOBALS:        equ     &F500          ; global name_ids, append order (128 B)
COMPACT_DATA_RUN:       equ     &F580          ; open-data-run accumulation (1216 B)
COMPACT_RECS:           equ     &FA40          ; compacted record stream (1344 B)
COMPACT_SIDECAR_CAP:    equ     768
COMPACT_GLOBALS_CAP:    equ     128
COMPACT_DATA_RUN_CAP:   equ     1216
COMPACT_RECS_CAP:       equ     1344

; Output count/length cells (host reads these; addresses mirrored in the test).
compact_sidecar_count:  equ     &FF80          ; u16
compact_globals_count:  equ     &FF82          ; u16
compact_recs_len:       equ     &FF84          ; u16

; Internal walk scratch (not read by the host).
compact_cursor:         equ     &FF86          ; u16
compact_remaining:      equ     &FF88          ; u16
compact_rec_index:      equ     &FF8A          ; u16
compact_dir_size:       equ     &FF8C          ; u16
compact_dir_id:         equ     &FF8E          ; u8
compact_op_count:       equ     &FF8F          ; u8
compact_dir_ops:        equ     &FF90          ; u16
compact_this_width:     equ     &FF92          ; u8
compact_cdw_width:      equ     &FF93          ; u8
compact_inst_run_open:  equ     &FF94          ; u8
compact_inst_run_count: equ     &FF95          ; u16
compact_fi_n:           equ     &FF97          ; u16
compact_data_width:     equ     &FF99          ; u8 (0 = no open data run)
compact_data_dir_id:    equ     &FF9A          ; u8
compact_data_ptr:       equ     &FF9B          ; u16
compact_data_remaining: equ     &FF9D          ; u8
compact_data_opsrc:     equ     &FF9E          ; u16
compact_data_max:       equ     &FFA0          ; u16
compact_data_total:     equ     &FFA2          ; u16
compact_data_src:       equ     &FFA4          ; u16
compact_data_n:         equ     &FFA6          ; u16
compact_sidecar_ptr:    equ     &FFA8          ; u16
compact_globals_ptr:    equ     &FFAA          ; u16
compact_recs_ptr:       equ     &FFAC          ; u16


; -----------------------------------------------------------------------
; compact_ir_walk — entry point. Run pass1 (RecordPC captured), then the
; compact-core walk. On return the three output buffers + their counts/lengths
; are populated for host read-back.
;
; Input:  PASS1_IR_BUF populated, PASS1_IR_LEN set (u16).
; Output: COMPACT_SIDECAR (+ compact_sidecar_count), COMPACT_GLOBALS (+
;         compact_globals_count), COMPACT_RECS (+ compact_recs_len).
; Clobbers: everything.
; -----------------------------------------------------------------------
compact_ir_walk:
; Enable RecordPC capture, then run the pass1 walk (populates symbol/litpool
; tables + COMPACT_REC_PC[]).
                ld      a, 1
                ld      (p1ir_capture_recpc), a
                call    pass1_ir_walk

; Reset compact-core output state.
                xor     a
                ld      (compact_sidecar_count), a
                ld      (compact_sidecar_count + 1), a
                ld      (compact_globals_count), a
                ld      (compact_globals_count + 1), a
                ld      (compact_data_width), a         ; 0 = no open data run
                ld      hl, COMPACT_SIDECAR
                ld      (compact_sidecar_ptr), hl
                ld      hl, COMPACT_GLOBALS
                ld      (compact_globals_ptr), hl
                ld      hl, COMPACT_RECS
                ld      (compact_recs_ptr), hl
                ld      hl, COMPACT_DATA_RUN
                ld      (compact_data_ptr), hl
                xor     a
                ld      (compact_inst_run_open), a      ; no open inst run

; Walk cursor + per-record index (for COMPACT_REC_PC lookup).
                ld      hl, PASS1_IR_BUF
                ld      (compact_cursor), hl
                ld      hl, (PASS1_IR_LEN)
                ld      (compact_remaining), hl
                xor     a
                ld      (compact_rec_index), a
                ld      (compact_rec_index + 1), a

compact_loop:
                ld      hl, (compact_remaining)
                ld      a, h
                or      l
                jp      z, compact_done

                ld      hl, (compact_cursor)
                ld      a, (hl)                         ; record kind tag

                cp      REC_KIND_LABEL_DEF
                jp      z, compact_label_local
                cp      REC_KIND_LOCAL_DEF
                jp      z, compact_label_local
                cp      REC_KIND_COMMENT
                jp      z, compact_comment
                cp      REC_KIND_BLANK_RUN
                jp      z, compact_blank
                cp      REC_KIND_INST
                jp      z, compact_inst
                cp      REC_KIND_DIRECTIVE
                jp      z, compact_directive
                jp      fail                            ; unknown record kind

compact_done:
; flushAll() at end of input (compact.go:186).
                call    compact_flush_inst
                call    compact_flush_data

; Record the final compacted-stream length for host read-back.
                ld      hl, (compact_recs_ptr)
                ld      de, COMPACT_RECS
                or      a
                sbc     hl, de
                ld      (compact_recs_len), hl
                ret


; -----------------------------------------------------------------------
; compact_advance — advance cursor + remaining by BC, bump the record index,
; loop. BC = total on-wire record size.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
compact_advance:
                ld      hl, (compact_cursor)
                add     hl, bc
                ld      (compact_cursor), hl
                ld      hl, (compact_remaining)
                or      a
                sbc     hl, bc
                ld      (compact_remaining), hl
                ld      hl, (compact_rec_index)
                inc     hl
                ld      (compact_rec_index), hl
                jp      compact_loop


; -----------------------------------------------------------------------
; compact_label_local — LABEL_DEF / LOCAL_DEF: flushData(), then drop the record
; WITHOUT flushing the inst run (compact.go:99-102). LABEL_DEF is 5 bytes on the
; wire ([kind][len:2=2][sym_id:2]); LOCAL_DEF is 4 ([kind][len:2=1][digit]).
; -----------------------------------------------------------------------
compact_label_local:
                call    compact_flush_data
; size = 3 framing + payload_len (from the len u16 at cursor+1).
                ld      hl, (compact_cursor)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                         ; BC = payload len
                inc     bc
                inc     bc
                inc     bc                              ; + 3 framing
                jp      compact_advance


; -----------------------------------------------------------------------
; compact_comment — COMMENT: append a SidecarComment row anchored to this
; record's output PC (RecordPC - OriginVMA, clamped >= 0), no flush
; (compact.go:103-122). Wire: [kind][len:2][placement:1][body].
; -----------------------------------------------------------------------
compact_comment:
; Row: [kind=0][anchor:4 LE][placement:1][body_len:2 LE][body].
                ld      hl, (compact_sidecar_ptr)
                ld      (hl), 0                         ; SidecarComment
                inc     hl
                call    compact_store_anchor            ; HL += 4 (anchor)

; placement = payload[0]; body starts at payload[1], body_len = payload_len - 1.
                ld      de, (compact_cursor)
                inc     de                              ; → len LSB
                ld      a, (de)
                ld      c, a
                inc     de
                ld      a, (de)
                ld      b, a                            ; BC = payload_len
                inc     de                              ; → placement
                ld      a, (de)
                ld      (hl), a                         ; placement byte
                inc     hl
                inc     de                              ; → body start
; body_len = payload_len - 1.
                dec     bc
                ld      (hl), c
                inc     hl
                ld      (hl), b
                inc     hl                              ; → body dest
; Copy BC body bytes from (DE) to (HL). A textless `//` comment has body_len 0
; (an empty Body); guard the ldir, since Z80 ldir with BC=0 copies 65536 bytes.
                push    bc
                ld      a, b
                or      c
                jr      z, compact_comment_body_done
                ex      de, hl                          ; HL = src, DE = dest
                ldir
                ex      de, hl                          ; HL = dest after body
compact_comment_body_done:
                pop     bc
                ld      (compact_sidecar_ptr), hl
                call    compact_bump_sidecar_count

; Record size = 3 framing + payload_len.
                ld      hl, (compact_cursor)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     bc
                inc     bc
                inc     bc
                jp      compact_advance


; -----------------------------------------------------------------------
; compact_blank — BLANK_RUN: append a SidecarBlank row anchored to this record's
; output PC, no flush (compact.go:128-138). Wire: [kind][len:2=4][run_len:4 LE].
; Row: [kind=1][anchor:4 LE][run_len:4 LE].
; -----------------------------------------------------------------------
compact_blank:
                ld      hl, (compact_sidecar_ptr)
                ld      (hl), 1                         ; SidecarBlank
                inc     hl
                call    compact_store_anchor            ; HL += 4 (anchor)

; run_len at cursor+3..+6.
                ld      de, (compact_cursor)
                inc     de
                inc     de
                inc     de                              ; → run_len LSB
                ld      bc, 4
                ex      de, hl                          ; HL = src run_len, DE = dest
                ldir
                ex      de, hl                          ; HL = dest after run_len
                ld      (compact_sidecar_ptr), hl
                call    compact_bump_sidecar_count

; Record size = 3 framing + 4 payload = 7.
                ld      bc, 7
                jp      compact_advance


; -----------------------------------------------------------------------
; compact_store_anchor — write the current record's anchor (4-byte LE) at (HL),
; advancing HL past it. anchor = COMPACT_REC_PC[rec_index] (the corpus is origin
; 0, so RecordPC - OriginVMA == RecordPC; clamp is a no-op for >= 0 offsets).
; Input:  HL = dest. Output: HL += 4. Clobbers: A, BC, DE.
; -----------------------------------------------------------------------
compact_store_anchor:
                push    hl
                ld      hl, (compact_rec_index)
                add     hl, hl
                add     hl, hl                          ; *4
                ld      de, COMPACT_REC_PC
                add     hl, de                          ; HL = &COMPACT_REC_PC[i]
                ex      de, hl                          ; DE = src
                pop     hl                              ; HL = dest
                ld      bc, 4
                ex      de, hl                          ; HL = src, DE = dest
                ldir
                ex      de, hl                          ; HL = dest after anchor
                ret


; -----------------------------------------------------------------------
; compact_bump_sidecar_count — compact_sidecar_count += 1 (u16).
; -----------------------------------------------------------------------
compact_bump_sidecar_count:
                ld      hl, (compact_sidecar_count)
                inc     hl
                ld      (compact_sidecar_count), hl
                ret


; -----------------------------------------------------------------------
; compact_inst — INST: flushData(), then append a skeleton element to the open
; inst run (compact.go:149-161). Each INST occupies 4 output bytes; the skeleton
; element is a placeholder base word (0). The run is materialised as mode-0
; KindInsnRun frames at compact_flush_inst. Wire:
; [kind][mnem:2][op_count:1][ops_len:2][ops]; size = 6 + ops_len.
; -----------------------------------------------------------------------
compact_inst:
                call    compact_flush_data
; Mark the inst run open and bump its element count.
                ld      a, (compact_inst_run_open)
                or      a
                jr      nz, compact_inst_count
                ld      a, 1
                ld      (compact_inst_run_open), a
                ld      hl, 0
                ld      (compact_inst_run_count), hl
compact_inst_count:
                ld      hl, (compact_inst_run_count)
                inc     hl
                ld      (compact_inst_run_count), hl

; Record size = 6 + ops_len (ops_len at cursor+4..+5).
                ld      hl, (compact_cursor)
                ld      bc, 4
                add     hl, bc
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                         ; BC = ops_len
                inc     bc
                inc     bc
                inc     bc
                inc     bc
                inc     bc
                inc     bc                              ; + 6 header bytes
                jp      compact_advance


; -----------------------------------------------------------------------
; compact_directive — DIRECTIVE. Stage (dir_id, op_count, operands-ptr), then
; mirror compact.go's dispatch order:
;   1. `.global` with clean symbol-ref operands → name_id list (no flush);
;   2. const-data directive → KindLitData run accumulation (flushInst first);
;   3. else flushAll() + pass-through DIRECTIVE record.
; Wire: [kind][len:2][dir_id:1][op_count:1][operands]; size = 3 + payload_len.
; -----------------------------------------------------------------------
compact_directive:
                ld      hl, (compact_cursor)
                inc     hl                              ; → len LSB
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                         ; BC = payload len
                inc     hl                              ; → dir_id
; size = 3 + payload_len.
                push    bc
                inc     bc
                inc     bc
                inc     bc
                ld      (compact_dir_size), bc
                pop     bc

                ld      a, (hl)                         ; dir_id
                ld      (compact_dir_id), a
                inc     hl                              ; → op_count
                ld      a, (hl)
                ld      (compact_op_count), a
                inc     hl                              ; → operands
                ld      (compact_dir_ops), hl

; (1) `.global` with clean symbol refs → globals list.
                ld      a, (compact_dir_id)
                cp      DIR_GLOBAL
                jr      nz, compact_dir_not_global
                call    compact_try_global              ; CF set on success
                jr      nc, compact_dir_not_global
                ld      bc, (compact_dir_size)
                jp      compact_advance

compact_dir_not_global:
; (2) const-data directive → LIT_DATA run.
                call    compact_const_data_width        ; A = width (0 = not const-data)
                or      a
                jp      z, compact_dir_passthrough
                ld      (compact_this_width), a

; flushInst() (compact.go:166).
                call    compact_flush_inst
; If a data run is open with a DIFFERENT directive id, flush it (compact.go:165).
                ld      a, (compact_data_width)
                or      a
                jr      z, compact_dir_data_start
                ld      a, (compact_data_dir_id)
                ld      b, a
                ld      a, (compact_dir_id)
                cp      b
                jr      z, compact_dir_data_append
                call    compact_flush_data
compact_dir_data_start:
                ld      a, (compact_dir_id)
                ld      (compact_data_dir_id), a
                ld      a, (compact_this_width)
                ld      (compact_data_width), a
compact_dir_data_append:
; Encode each constant operand into compact_data_ptr as `width` LE bytes
; (evalImmsAsBytes, pass2.go:1842). All operands are known-const here.
                call    compact_emit_const_data
                ld      bc, (compact_dir_size)
                jp      compact_advance

compact_dir_passthrough:
; (3) flushAll() then re-serialise the DIRECTIVE verbatim (compact.go:175-184).
                call    compact_flush_inst
                call    compact_flush_data
                call    compact_emit_passthrough_directive
                ld      bc, (compact_dir_size)
                jp      compact_advance


; -----------------------------------------------------------------------
; compact_try_global — port of globalNameIDs (compact.go:229-247). Each operand
; must be an IMM_EXPR wrapping exactly [PUSH_SYM, id_lo, id_hi]; append each id
; to COMPACT_GLOBALS. On any non-clean operand (or zero operands) leave the
; globals buffer UNCHANGED and return CF=0 (caller keeps the directive). On
; success append all ids and return CF=1.
;
; Two-phase to honour the all-or-nothing contract: first validate every operand
; (counting ids) without writing, then append. Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
compact_try_global:
                ld      a, (compact_op_count)
                or      a
                jp      z, compact_global_fail          ; zero operands → keep directive

; Phase 1: validate every operand is [IMM_EXPR][len=3][PUSH_SYM,id].
                ld      b, a                            ; B = op_count
                ld      hl, (compact_dir_ops)
compact_global_val_loop:
                ld      a, (hl)
                cp      OP_KIND_IMM_EXPR
                jr      nz, compact_global_fail
                inc     hl                              ; → len LSB
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                         ; DE = expr len
                inc     hl                              ; → bytecode
                ld      a, d
                or      a
                jr      nz, compact_global_fail         ; len != 3
                ld      a, e
                cp      3
                jr      nz, compact_global_fail
                ld      a, (hl)
                cp      EXPR_PUSH_SYM
                jr      nz, compact_global_fail
                inc     hl
                inc     hl
                inc     hl                              ; skip [PUSH_SYM,id_lo,id_hi]
                djnz    compact_global_val_loop

; Phase 2: append every operand's id to COMPACT_GLOBALS.
                ld      a, (compact_op_count)
                ld      b, a
                ld      hl, (compact_dir_ops)
                ld      de, (compact_globals_ptr)
compact_global_app_loop:
                inc     hl                              ; skip IMM_EXPR kind
                inc     hl                              ; skip len LSB
                inc     hl                              ; skip len MSB
                inc     hl                              ; skip PUSH_SYM opcode → id_lo
                ld      a, (hl)
                ld      (de), a                         ; id_lo
                inc     hl
                inc     de
                ld      a, (hl)
                ld      (de), a                         ; id_hi
                inc     hl
                inc     de
                push    hl
                ld      hl, (compact_globals_count)
                inc     hl
                ld      (compact_globals_count), hl
                pop     hl
                djnz    compact_global_app_loop
                ld      (compact_globals_ptr), de
                scf
                ret

compact_global_fail:
                or      a                               ; CF = 0
                ret


; -----------------------------------------------------------------------
; compact_const_data_width — port of ConstDataWidth (litinsts.go:56). Returns
; A = per-element width (1/2/4/8) when compact_dir_id is a numeric data directive
; AND every operand is an IMM_EXPR with a constant expression; A = 0 otherwise.
;
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
compact_const_data_width:
                ld      a, (compact_dir_id)
                cp      DIR_BYTE
                jr      z, compact_cdw_1
                cp      DIR_SHORT
                jr      z, compact_cdw_2
                cp      DIR_HWORD
                jr      z, compact_cdw_2
                cp      DIR_WORD
                jr      z, compact_cdw_4
                cp      DIR_QUAD
                jr      z, compact_cdw_8
                xor     a                               ; not a data directive
                ret
compact_cdw_1:
                ld      a, 1
                jr      compact_cdw_check
compact_cdw_2:
                ld      a, 2
                jr      compact_cdw_check
compact_cdw_4:
                ld      a, 4
                jr      compact_cdw_check
compact_cdw_8:
                ld      a, 8
compact_cdw_check:
                ld      (compact_cdw_width), a
                ld      a, (compact_op_count)
                or      a
                jr      z, compact_cdw_notconst         ; n == 0 → ok=false
                ld      b, a                            ; B = op_count
                ld      hl, (compact_dir_ops)
compact_cdw_loop:
                ld      a, (hl)
                cp      OP_KIND_IMM_EXPR
                jr      nz, compact_cdw_notconst        ; non-IMM_EXPR → not const-data
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                         ; DE = expr len
                inc     hl                              ; → bytecode
                push    bc                              ; save op-count loop counter
                push    de                              ; save expr len (call clobbers DE)
                push    hl
                ld      b, d
                ld      c, e                            ; BC = expr len
                call    compact_expr_is_const           ; CF=1 if constant
                pop     hl
                pop     de                              ; DE = expr len
                pop     bc
                jr      nc, compact_cdw_notconst
; advance HL past the bytecode.
                add     hl, de
                djnz    compact_cdw_loop
                ld      a, (compact_cdw_width)
                ret
compact_cdw_notconst:
                xor     a
                ret


; -----------------------------------------------------------------------
; compact_expr_is_const — port of EvalConst's const test (the inverse of
; exprHasContextDep, litinsts.go:98). Scan the expr bytecode at HL (BC bytes) for
; any context-dependent opcode (PUSH_SYM/PUSH_LOCAL/PUSH_PC/REL_*); CF=1 if the
; expression is pure-constant, CF=0 otherwise. A malformed/truncated stream is
; conservatively non-constant (CF=0).
;
; Opcode operand lengths (expr.go): PUSH_IMM8 +1, IMM16 +2, IMM32 +4, IMM64 +8,
; PUSH_SYM +2, PUSH_LOCAL +2; all others bare.
;
; Input:  HL = bytecode ptr, BC = byte count. Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
compact_expr_is_const:
compact_eic_loop:
                ld      a, b
                or      c
                jr      z, compact_eic_const            ; consumed all → constant
                ld      a, (hl)                         ; opcode
                inc     hl
                dec     bc
; Context-dependent opcodes → not constant.
                cp      EXPR_PUSH_SYM                   ; 0x05
                jr      z, compact_eic_notconst
                cp      &06                             ; PUSH_LOCAL
                jr      z, compact_eic_notconst
                cp      &07                             ; PUSH_PC
                jr      z, compact_eic_notconst
                cp      &30
                jr      c, compact_eic_inline           ; < 0x30 → not a REL op
                cp      &39
                jr      c, compact_eic_notconst         ; 0x30..0x38 → REL_* (context dep)
compact_eic_inline:
; Skip inline operand bytes for the PUSH_IMMn opcodes.
                cp      &01                             ; PUSH_IMM8
                jr      z, compact_eic_skip1
                cp      &02                             ; PUSH_IMM16
                jr      z, compact_eic_skip2
                cp      &03                             ; PUSH_IMM32
                jr      z, compact_eic_skip4
                cp      &04                             ; PUSH_IMM64
                jr      z, compact_eic_skip8
                jr      compact_eic_loop                ; bare opcode
compact_eic_skip1:
                ld      de, 1
                jr      compact_eic_skip
compact_eic_skip2:
                ld      de, 2
                jr      compact_eic_skip
compact_eic_skip4:
                ld      de, 4
                jr      compact_eic_skip
compact_eic_skip8:
                ld      de, 8
compact_eic_skip:
; HL += DE (advance past inline operand bytes); BC -= DE (count consumed).
                add     hl, de
                ld      a, c
                sub     e
                ld      c, a
                ld      a, b
                sbc     a, d
                ld      b, a
                jr      compact_eic_loop
compact_eic_const:
                scf
                ret
compact_eic_notconst:
                or      a                               ; CF = 0
                ret


; -----------------------------------------------------------------------
; compact_emit_const_data — append each operand's constant value as `width` LE
; bytes to the open data run at compact_data_ptr (evalImmsAsBytes, pass2.go:1842).
; width = (compact_this_width). All operands are known-const (compact_const_data_width
; passed), so eval_expr_const never fails on a symbol.
;
; Clobbers: everything.
; -----------------------------------------------------------------------
compact_emit_const_data:
                ld      a, (compact_op_count)
                or      a
                ret     z
                ld      (compact_data_remaining), a
                ld      hl, (compact_dir_ops)
                ld      (compact_data_opsrc), hl
compact_ecd_loop:
                ld      hl, (compact_data_opsrc)
                ld      a, (hl)
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                         ; BC = expr len
                inc     hl                              ; → bytecode
                push    bc
                call    eval_expr_const                 ; → expr_result (8-byte LE)
                pop     bc
; Advance opsrc past this operand: 3 (kind+len) + bytecode len.
                ld      hl, (compact_data_opsrc)
                inc     hl
                inc     hl
                inc     hl
                add     hl, bc
                ld      (compact_data_opsrc), hl
; Append the low `width` bytes of expr_result.
                ld      a, (compact_this_width)
                ld      b, a
                ld      hl, expr_result
                ld      de, (compact_data_ptr)
compact_ecd_copy:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    compact_ecd_copy
                ld      (compact_data_ptr), de
                ld      a, (compact_data_remaining)
                dec     a
                ret     z
                ld      (compact_data_remaining), a
                jp      compact_ecd_loop


; -----------------------------------------------------------------------
; compact_flush_inst — materialise the open inst run as mode-0 KindInsnRun
; frames of placeholder base words (compact.go:57-60 flushInst →
; emitInsnRunFrames; with no patches every frame is mode-0, split at maxWords =
; (1016-1)/4 = 253 words). The base words are placeholders (0) — the real words
; are i48c-b8e's job; only the element count + PC accounting matter here.
;
; A KindInsnRun mode-0 record is [REC_KIND_INSN_RUN][len:2 LE][mode=0][word:4]*.
; Clobbers: everything.
; -----------------------------------------------------------------------
COMPACT_INSN_MAX_WORDS: equ     253

compact_flush_inst:
                ld      a, (compact_inst_run_open)
                or      a
                ret     z
                xor     a
                ld      (compact_inst_run_open), a
compact_fi_frame:
                ld      hl, (compact_inst_run_count)
                ld      a, h
                or      l
                ret     z                               ; no elements left
; n = min(count, 253).
                ld      de, COMPACT_INSN_MAX_WORDS
                ld      a, h
                or      a
                jr      nz, compact_fi_cap              ; count >= 256 → cap
                ld      a, l
                cp      COMPACT_INSN_MAX_WORDS + 1
                jr      c, compact_fi_n_ok              ; count <= 253
compact_fi_cap:
                ld      hl, COMPACT_INSN_MAX_WORDS
compact_fi_n_ok:
; HL = n (words this frame). count -= n.
                ld      (compact_fi_n), hl
                push    hl
                ld      hl, (compact_inst_run_count)
                ld      de, (compact_fi_n)
                or      a
                sbc     hl, de
                ld      (compact_inst_run_count), hl
                pop     hl                              ; HL = n

; payload_len = 1 (mode) + 4*n.
                add     hl, hl
                add     hl, hl                          ; HL = 4*n
                inc     hl                              ; + mode byte
                ld      de, (compact_recs_ptr)
                ld      a, REC_KIND_INSN_RUN
                ld      (de), a
                inc     de
                ld      a, l
                ld      (de), a
                inc     de
                ld      a, h
                ld      (de), a
                inc     de                              ; → payload
                xor     a
                ld      (de), a                         ; mode = 0
                inc     de
; Write n placeholder words (4 zero bytes each).
                ld      hl, (compact_fi_n)
                ld      b, l                            ; n <= 253, fits in B
compact_fi_words:
                xor     a
                ld      (de), a
                inc     de
                ld      (de), a
                inc     de
                ld      (de), a
                inc     de
                ld      (de), a
                inc     de
                djnz    compact_fi_words
                ld      (compact_recs_ptr), de
                jr      compact_fi_frame


; -----------------------------------------------------------------------
; compact_flush_data — emit the open data run as KindLitData records, split on
; whole-element boundaries at max = litDataMaxBytes / width * width
; (compact.go:61-79). Each record: [REC_KIND_LIT_DATA][len:2 LE][dir_id][bytes].
;
; Clobbers: everything.
; -----------------------------------------------------------------------
LIT_DATA_MAX_BYTES:     equ     1016

compact_flush_data:
                ld      a, (compact_data_width)
                or      a
                ret     z                               ; no open data run

; max = 1016 / width * width (whole elements per record).
                ld      b, a                            ; B = width
                ld      hl, LIT_DATA_MAX_BYTES
                call    compact_div_hl_by_b             ; HL = 1016 / width
; max = (1016/width) * width.
                ld      a, b
                call    compact_mul_hl_by_a             ; HL = max
                ld      (compact_data_max), hl

; Source = COMPACT_DATA_RUN; total = compact_data_ptr - COMPACT_DATA_RUN.
                ld      hl, (compact_data_ptr)
                ld      de, COMPACT_DATA_RUN
                or      a
                sbc     hl, de
                ld      (compact_data_total), hl
                ld      hl, COMPACT_DATA_RUN
                ld      (compact_data_src), hl

compact_fd_rec:
                ld      hl, (compact_data_total)
                ld      a, h
                or      l
                jr      z, compact_fd_done
; n = min(total, max): if total <= max use total, else max.
                ld      de, (compact_data_max)
                or      a
                sbc     hl, de                          ; (total - max); CF set if total < max
                jr      c, compact_fd_n_total           ; total < max → n = total
                ld      hl, (compact_data_max)          ; total >= max → n = max
                jr      compact_fd_have_n
compact_fd_n_total:
                ld      hl, (compact_data_total)
compact_fd_have_n:
                ld      (compact_data_n), hl

; Write the record header: [REC_KIND_LIT_DATA][len:2 = 1+n][dir_id].
                ld      de, (compact_recs_ptr)
                ld      a, REC_KIND_LIT_DATA
                ld      (de), a
                inc     de
                ld      bc, (compact_data_n)
                inc     bc                              ; payload = 1 (dir_id) + n
                ld      a, c
                ld      (de), a
                inc     de
                ld      a, b
                ld      (de), a
                inc     de
                ld      a, (compact_data_dir_id)
                ld      (de), a
                inc     de                              ; → data bytes dest
; Copy n bytes from compact_data_src (HL) to the record dest (DE).
                ld      hl, (compact_data_src)
                ld      bc, (compact_data_n)
                ldir                                    ; (HL)->(DE), BC bytes
                ld      (compact_recs_ptr), de
                ld      (compact_data_src), hl          ; advance src past copied bytes
; total -= n.
                ld      hl, (compact_data_total)
                ld      de, (compact_data_n)
                or      a
                sbc     hl, de
                ld      (compact_data_total), hl
                jr      compact_fd_rec

compact_fd_done:
                xor     a
                ld      (compact_data_width), a         ; close the data run
                ld      hl, COMPACT_DATA_RUN
                ld      (compact_data_ptr), hl          ; reset accumulation ptr
                ret


; -----------------------------------------------------------------------
; compact_emit_passthrough_directive — re-serialise the staged DIRECTIVE record
; verbatim into the compacted stream (compact.go:181 WriteDirective). The on-wire
; payload [dir_id][op_count][operands] is already contiguous in the IR buffer at
; compact_cursor+3, length = payload_len; copy the whole framed record.
;
; Clobbers: everything.
; -----------------------------------------------------------------------
compact_emit_passthrough_directive:
; payload_len at cursor+1..+2; total = 3 + payload_len; copy verbatim.
                ld      hl, (compact_cursor)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                         ; BC = payload len
                inc     bc
                inc     bc
                inc     bc                              ; BC = total framed size
                ld      hl, (compact_cursor)
                ld      de, (compact_recs_ptr)
                ldir
                ld      (compact_recs_ptr), de
                ret


; -----------------------------------------------------------------------
; compact_div_hl_by_b — HL := HL / B (unsigned, B != 0). Simple repeated
; subtraction; B (width) is 1/2/4/8 and HL is 1016, so at most 1016 iterations —
; fine for a one-shot per flush. Clobbers: A, DE.
; -----------------------------------------------------------------------
compact_div_hl_by_b:
                ld      de, 0                           ; DE = quotient
compact_div_loop:
                ld      a, l
                sub     b
                ld      l, a
                ld      a, h
                sbc     a, 0
                ld      h, a
                jr      c, compact_div_done
                inc     de
                jr      compact_div_loop
compact_div_done:
                ex      de, hl                          ; HL = quotient
                ret


; -----------------------------------------------------------------------
; compact_mul_hl_by_a — HL := HL * A (unsigned, A small: 1/2/4/8). Clobbers: A,
; B, DE.
; -----------------------------------------------------------------------
compact_mul_hl_by_a:
                ld      d, h
                ld      e, l                            ; DE = HL (one copy)
                or      a
                jr      z, compact_mul_zero
                dec     a
                jr      z, compact_mul_done             ; A == 1 → HL unchanged
                ld      b, a
compact_mul_loop:
                add     hl, de
                djnz    compact_mul_loop
compact_mul_done:
                ret
compact_mul_zero:
                ld      hl, 0
                ret


; All compact-core scratch + buffers are `equ`-defined in the RAM map near the
; top of this file (the &F000-&FFFF free block), NOT emitted here — see that map
; for why (the included 11 KB IR buffer pushes the code image past &C100, so any
; emitted scratch would collide with the leaves' SYMTAB).
