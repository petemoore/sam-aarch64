; tbn_render.asm — the `.tbn` → source-text renderer (Z80 port of the Go
; authority tools/sam-aarch64/render, emit.go/overlay.go).
;
; render.Emit is a pure function of the on-disk `.tbn` bytes.  This file ports
; the orchestration; instruction decode is delegated to build/disasm.bin
; (src/disasm.asm) via paged_call — the renderer never re-implements decode.
;
; The record stream is walked with the shared reader (src/reader.asm): each
; record is staged into STAGING_BUF and dispatched on its kind byte.  For the
; on-disk overlay `.tbn` the only record kinds are INSN_RUN (0x09), LIT_DATA
; (0x08) and DIRECTIVE (0x04); INST/COMMENT/LABEL_DEF never appear on disk.
;
; The INSN_RUN mode-0 (bare literal words), LIT_DATA (constant data directive)
; and DIRECTIVE arms are wired, together with the editor-region name table +
; `.global` flags (tbn_render_editor.asm) and the header-table label/local
; flush placed by PC.  The comment sidecar / mode-1 patch overlays are later
; slices — an unexpected record kind falls through to `fail`.
;
; Output: canonical GNU-as text is appended byte-by-byte to render_out; its
; length lands in render_out_len (both section-C, HMPR-stable — the driver's
; harness reads them after render_run returns).


; -----------------------------------------------------------------------
; render_emit — top-level entry.  Mirrors render.EmitFile (emit.go:66).
;
; Resets the output buffer, the prevWasStatement flag and the header-def
; cursors/PC, seeds the header tables (reader_init, via the store callbacks),
; reads the editor region (name table + `.global` flags), emits the `.global`
; lines, walks the record stream, then closes a trailing open statement with a
; final newline.
;
; PC tracking places header-table label/local definitions before the statement
; whose offset-from-origin they name; render_flush drains them at each element
; boundary (emit.go:74-122, overlay.go:36-48).
;
; Input:  the reader cursor (IN_POS_*) points at the staged `.tbn`.
; Output: render_out / render_out_len populated.
; -----------------------------------------------------------------------
render_emit:
                ld      hl, 0
                ld      (render_out_len), hl
                xor     a
                ld      (render_prev_stmt), a

                call    render_reset_state      ; header-def cursors + PC
                call    reader_init             ; seeds header tables via the
                                                ;   store callbacks, bounds IN_END
                call    render_read_editor      ; name table + .global flags
                call    render_globals          ; emit "  .global NAME\n" lines
                call    render_walk

; Final: `flush()` then `if prevWasStatement { out.WriteByte('\n') }`
; (emit.go:226,235).  The trailing flush places an end-of-stream label.
                call    render_flush
                ld      a, (render_prev_stmt)
                or      a
                ret     z
                ld      a, 10                   ; '\n'
                jp      render_out_append       ; tail-call


; -----------------------------------------------------------------------
; render_walk — the record loop.  Mirrors the `for _, rec := range
; f.Records` switch (emit.go:124) reduced to the on-disk kinds.
;
; reader_at_end / reader_next_kind is the same walk pattern as
; walk_records (main_loop.asm:459).
; -----------------------------------------------------------------------
render_walk:
                call    reader_at_end
                ret     z                       ; no records remain

                call    reader_next_kind        ; A = kind, HL = payload, BC = len
                cp      REC_KIND_INSN_RUN
                jr      z, render_walk_insn_run
                cp      REC_KIND_LIT_DATA
                jr      z, render_walk_lit_data
                cp      REC_KIND_DIRECTIVE
                jr      z, render_walk_directive
                jp      fail                     ; other kinds: later slices

render_walk_insn_run:
                call    render_insn_run
                jr      render_walk

render_walk_lit_data:
                call    render_lit_data
                jr      render_walk

render_walk_directive:
                call    render_directive
                jr      render_walk


; -----------------------------------------------------------------------
; render_insn_run — render a KindInsnRun record.  Mirrors emitInsnRun
; (overlay.go:36): one statement per 4-byte element.
;
; Payload: [mode u8][elements].  Mode 0 is bare 4-byte little-endian assembled
; words (no patches), each decoding as a literal.  Mode 1 is the overlay
; encoding: variable-length elements, each a base word + patch_count + packed
; patches (render_insn_run_mode1, SLICE 6a).
;
; Input:  HL = payload ptr (STAGING_BUF), BC = payload length.
; -----------------------------------------------------------------------
render_insn_run:
                ld      a, (hl)                 ; mode byte
                inc     hl                      ; skip the mode byte
                dec     bc                      ; BC = remaining element bytes
                or      a
                jp      nz, render_insn_run_mode1

render_insn_run_next:
                ld      a, b
                or      c
                ret     z                       ; all elements rendered

                ld      (render_rir_src), hl
                ld      (render_rir_rem), bc

; flush any header-table label/local whose offset == the current PC before the
; statement — a label embedded mid-run (emitInsnRun overlay.go:38, flush()).
                call    render_flush

; open the statement line: `if prevWasStatement '\n'` then the two-space
; indent (emitInsnRun overlay.go:39-42).
                call    render_open_stmt

; load the 4-byte little-endian element word into the disasm ABI registers:
;   BC = high 16 bits (bytes 2,3), IX = low 16 bits (bytes 0,1).
                ld      hl, (render_rir_src)
                ld      c, (hl)                 ; byte 0 (low  of low16)
                inc     hl
                ld      b, (hl)                 ; byte 1 (high of low16); BC = low16
                inc     hl
                push    bc                      ; stash low16
                ld      c, (hl)                 ; byte 2 (low  of high16)
                inc     hl
                ld      b, (hl)                 ; byte 3 (high of high16); BC = high16
                pop     ix                      ; IX = low16

                call    render_insn_element

                ld      a, 1
                ld      (render_prev_stmt), a   ; prevWasStatement = true

; advance the PC by one element (4 bytes) — emitInsnRun overlay.go:47.
                call    render_pc_advance4

; advance to the next element: src += 4, rem -= 4.
                ld      hl, (render_rir_src)
                ld      bc, 4
                add     hl, bc
                ld      bc, (render_rir_rem)
                dec     bc
                dec     bc
                dec     bc
                dec     bc
                jr      render_insn_run_next


; -----------------------------------------------------------------------
; render_insn_element — render one INSN_RUN element.  Mirrors
; emitInsnElement (overlay.go:53).
;
; SLICE 1: only the 0-patch arm (a fully-literal word) is wired, so this is
; a straight decode.  A later slice adds the patch-overlay arm ahead of the
; tail-call.
;
; Input:  BC = high16, IX = low16 (the 32-bit word).
; -----------------------------------------------------------------------
render_insn_element:
                jp      render_decode_literal   ; tail-call


; -----------------------------------------------------------------------
; render_decode_literal — decode a fully-assembled word and append its text.
; Mirrors decodeLiteral (overlay.go:71) + dec.Format (disasm.go:137).
;
; Calls build/disasm.bin (resident in physical page DISASM_PAGE) via
; paged_call; reads the null-terminated mnemonic and operand strings from
; the section-B comm buffer.  The join is `mnem` then, only if operands are
; non-empty, a TAB and the operands.
;
; Input:  BC = high16, IX = low16 (the word to decode).
; -----------------------------------------------------------------------
render_decode_literal:
                call    paged_call
                defw    DISASM_ENTRY
                defb    DISASM_PAGE
                ; DISASM_COMM_MNEM / DISASM_COMM_OPS now populated (section B).

                ld      hl, DISASM_COMM_MNEM
                call    render_copy_cstr        ; append the mnemonic

                ld      a, (DISASM_COMM_OPS)
                or      a
                ret     z                       ; empty operands → mnemonic only

                ld      a, 9                    ; TAB (dec.Format separator)
                call    render_out_append
                ld      hl, DISASM_COMM_OPS
                jp      render_copy_cstr        ; tail-call: append the operands


; =======================================================================
; SLICE 6a — INSN_RUN mode-1 (overlay) rendering: the overlay framework +
; the target-text slot families (branch26/19/14, adr, adrp).
;
; Ports the mode-1 path of the Go authority tools/sam-aarch64/render/overlay.go:
;   render_insn_run_mode1     <- emitInsnRun mode-1 element walk (overlay.go:36)
;   render_m1_literal/overlay <- emitInsnElement 0-patch/1-patch (overlay.go:53)
;   render_ovl_target         <- renderOverlay + spliceOverlay target families
;                                (overlay.go:80-97, 113-122)
;   render_ovl_emit_ops_prefix<- replaceOperand(mnem,ops,lastIndex(ops),sym)
;                                leading-parts join (overlay.go:170-187)
;   render_emit_target_text   <- renderPatchOperand → renderTargetText
;                                (overlay.go:231-274)
;   render_ovl_patch_header   <- insn_patch_header (insn_run.asm:225-265)
;
; The deferred immediate/mem families (S6b: MemImm12/9, AddSubImm12, Litpool19,
; MovzAuto, MovkImm16, PairImm7, Logical) fall through to fail.
; =======================================================================

; FoldSlot ids — wire contract with tools/aarch64enc/overlay.go (mirrors the
; FSID_* ids in insn_run.asm:31-44).  Defined here because the standalone
; render driver does not include insn_run.asm; only the S6a target-text
; families are named (S6b adds ids 6-13).
FSID_BRANCH26:          equ     1
FSID_BRANCH19:          equ     2
FSID_BRANCH14:          equ     3
FSID_ADR:               equ     4
FSID_ADRP:              equ     5


; -----------------------------------------------------------------------
; render_insn_run_mode1 — render a mode-1 (overlay) INSN_RUN record.  Each
; element is [base_word u32][patch_count u8][patch_count × packed header +
; expr bytes]; a 0-patch element is a fully-literal word, a 1-patch element
; carries one FoldSlot overlay whose expression replaces the folded operand.
; Mirrors emitInsnRun (overlay.go:36-48) + emitInsnElement (overlay.go:53-67)
; over the reader's mode-1 element layout (reader.go:128-162).
;
; The variable-length element walk (base_word, patch_count, then per-patch
; packed headers) mirrors insn_run.asm's pass-2 element loop
; (insn_run.asm:84-152) and its header parser (insn_run.asm:225-265).
;
; Input:  HL = first element ptr (STAGING_BUF), BC = element bytes.
; -----------------------------------------------------------------------
render_insn_run_mode1:
                ld      (render_ovl_cursor), hl
                ld      (render_ovl_remaining), bc

render_m1_elem_loop:
                ld      bc, (render_ovl_remaining)
                ld      a, b
                or      c
                ret     z                       ; all elements rendered

; flush header-table defs whose offset == PC, then open the statement line
; (emitInsnRun overlay.go:38-42).
                call    render_flush
                call    render_open_stmt

; read base_word (4 bytes) into render_ovl_base; HL → patch_count byte.
                ld      hl, (render_ovl_cursor)
                ld      de, render_ovl_base
                ld      bc, 4
                ldir
                ld      a, (hl)
                ld      (render_ovl_patch_count), a
                inc     hl
                ld      (render_ovl_cursor), hl
; remaining -= 5 (base_word + patch_count).
                ld      hl, (render_ovl_remaining)
                ld      de, 5
                or      a
                sbc     hl, de
                ld      (render_ovl_remaining), hl

; dispatch on patch count: 0 → literal, 1 → overlay, >1 → unsupported (the Go
; authority errors, overlay.go:58-60).
                ld      a, (render_ovl_patch_count)
                or      a
                jr      z, render_m1_literal
                cp      1
                jr      z, render_m1_overlay
                jp      fail

render_m1_after:
                ld      a, 1
                ld      (render_prev_stmt), a   ; prevWasStatement = true
                call    render_pc_advance4      ; pc += 4 (overlay.go:47)
                jr      render_m1_elem_loop

; -- 0-patch element: decode the literal base word (overlay.go:54-56) --------
render_m1_literal:
                ld      hl, render_ovl_base
                call    render_load_word_regs   ; BC = high16, IX = low16
                call    render_decode_literal
                jr      render_m1_after

; -- 1-patch element: decode + splice the patch operand (renderOverlay) -------
render_m1_overlay:
                ld      hl, render_ovl_base
                call    render_load_word_regs   ; BC = high16, IX = low16
                call    paged_call
                defw    DISASM_ENTRY
                defb    DISASM_PAGE
                ; DISASM_COMM_MNEM / DISASM_COMM_OPS populated (section B).

                call    render_ovl_patch_header ; slot + expr_len; HL = expr ptr
                ld      (render_ovl_expr_ptr), hl

; spliceOverlay dispatch (overlay.go:103-159).  SLICE 6a wires the target-text
; families only (branch26/19/14, adr, adrp): each replaces the LAST operand
; with the rendered target text (overlay.go:113-122 —
; replaceOperand(mnem, ops, lastIndex(ops), sym)).  The immediate/mem families
; (S6b) fall through to fail.
                ld      a, (render_ovl_slot)
                cp      FSID_BRANCH26
                jr      z, render_ovl_target
                cp      FSID_BRANCH19
                jr      z, render_ovl_target
                cp      FSID_BRANCH14
                jr      z, render_ovl_target
                cp      FSID_ADR
                jr      z, render_ovl_target
                cp      FSID_ADRP
                jr      z, render_ovl_target
                jp      fail                    ; S6b immediate/mem slots

render_ovl_target:
; joinLine (overlay.go:170-175): mnem + TAB ...
                ld      hl, DISASM_COMM_MNEM
                call    render_copy_cstr
                ld      a, 9                    ; TAB
                call    render_out_append
; ... + the operand prefix (leading parts up to & incl. the last ", ") ...
                call    render_ovl_emit_ops_prefix
; ... + the target text as the replaced last operand (renderTargetText).
                ld      hl, (render_ovl_expr_ptr)
                ld      bc, (render_ovl_expr_len)
                call    render_emit_target_text
                jr      render_m1_after


; -----------------------------------------------------------------------
; render_load_word_regs — load the 4-byte little-endian word at (HL) into the
; disasm ABI registers: BC = high 16 bits (bytes 2,3), IX = low 16 bits
; (bytes 0,1).  Mirrors the inline load in the mode-0 element loop.
; -----------------------------------------------------------------------
render_load_word_regs:
                ld      c, (hl)                 ; byte 0
                inc     hl
                ld      b, (hl)                 ; byte 1; BC = low16
                inc     hl
                push    bc
                ld      c, (hl)                 ; byte 2
                inc     hl
                ld      b, (hl)                 ; byte 3; BC = high16
                pop     ix                      ; IX = low16
                ret


; -----------------------------------------------------------------------
; render_ovl_patch_header — decode the packed patch header at
; (render_ovl_cursor): [slot:4|expr_len:4]; an expr_len nibble of 15 escapes to
; a real u8 length that follows.  Sets render_ovl_slot + render_ovl_expr_len and
; advances render_ovl_cursor / render_ovl_remaining past header+expr.  Output:
; HL = expr bytes ptr.  Faithful port of insn_patch_header (insn_run.asm:232-265)
; with render-local state (the render driver does not include insn_run.asm).
; -----------------------------------------------------------------------
render_ovl_patch_header:
                ld      hl, (render_ovl_cursor)
                ld      a, (hl)
                inc     hl
                ld      c, a                    ; C = packed header byte
                rrca
                rrca
                rrca
                rrca
                and     &0F
                ld      (render_ovl_slot), a    ; slot = high nibble
                ld      de, 1                   ; header size
                ld      a, c
                and     &0F                     ; expr_len nibble
                cp      &0F
                jr      nz, roph_len_done
                ld      a, (hl)                 ; escape: real u8 length
                inc     hl
                inc     e                       ; header size 2
roph_len_done:
                ld      c, a
                ld      b, 0
                ld      (render_ovl_expr_len), bc
                push    hl                      ; expr ptr
                add     hl, bc                  ; HL = past expr
                ld      (render_ovl_cursor), hl
                ld      hl, (render_ovl_remaining)
                or      a
                sbc     hl, bc                  ; remaining -= expr_len
                or      a
                sbc     hl, de                  ; remaining -= header size
                ld      (render_ovl_remaining), hl
                pop     hl                      ; HL = expr ptr
                ret


; -----------------------------------------------------------------------
; render_ovl_emit_ops_prefix — append the operand-list prefix that precedes the
; folded (last) operand: everything in DISASM_COMM_OPS up to and including the
; last ", " separator.  A single-operand ops string (no ", ") contributes
; nothing.  This is joinLine over replaceOperand's untouched leading parts
; (overlay.go:180-187): only the last operand is replaced, so the leading text
; is copied verbatim.  The target families' operands carry no bracket group, so
; the last ", " is the true operand boundary (matching Go's splitOps).
; -----------------------------------------------------------------------
render_ovl_emit_ops_prefix:
                ld      hl, DISASM_COMM_OPS
                ld      de, 0                   ; DE = ptr past the last ", " (0 = none)
rovp_scan:
                ld      a, (hl)
                or      a
                jr      z, rovp_done
                cp      ","
                jr      nz, rovp_next
                inc     hl                      ; inspect the char after ','
                ld      a, (hl)
                cp      " "
                jr      nz, rovp_scan           ; ',' not followed by ' '
                inc     hl                      ; HL → char after ", "
                ld      d, h
                ld      e, l                    ; DE = start of the operand after ", "
                jr      rovp_scan
rovp_next:
                inc     hl
                jr      rovp_scan
rovp_done:
                ld      a, d
                or      e
                ret     z                       ; no ", " → empty prefix
                ex      de, hl                  ; HL = end (past last ", ")
                ld      de, DISASM_COMM_OPS
                or      a
                sbc     hl, de                  ; HL = prefix length
                ld      b, h
                ld      c, l                    ; BC = length
                ld      hl, DISASM_COMM_OPS
                jp      render_out_put_bytes


; -----------------------------------------------------------------------
; render_emit_target_text — renderTargetText (overlay.go:257-274): render a
; branch/adr/adrp target expression to render_out.  A constant renders as a
; bare address (0x%x for v>=0, %d for v<0, no leading '#'); a simple symbol
; reference renders as the symbol/local name (reusing S5's render_simple_symref);
; anything else as a parenthesised infix expression.  renderPatchOperand
; (overlay.go:231-242) routes the branch/adr/adrp slots here (target style).
;
; Input:  HL = expr ptr, BC = expr len.
; -----------------------------------------------------------------------
render_emit_target_text:
                ld      (render_ovl_expr_ptr), hl
                ld      (render_ovl_expr_len), bc
                call    render_eval_const       ; Z=1 → const, render_num32 = v
                jr      nz, rett_notconst
; constant target: v<0 → signed decimal (%d); v>=0 → "0x" + hex (0x%x).
                ld      hl, render_out_append
                ld      (render_sink_addr), hl
                ld      a, (render_num32 + 3)
                bit     7, a
                jp      nz, render_emit_dec_s32
                jp      render_emit_hashx_s32
rett_notconst:
                ld      hl, (render_ovl_expr_ptr)
                ld      bc, (render_ovl_expr_len)
                call    render_simple_symref    ; Z=1 → matched (name/Nf/Nb emitted)
                ret     z
; compound target: "(" printExpr ")".
                ld      a, "("
                call    render_out_append
                ld      hl, (render_ovl_expr_ptr)
                ld      bc, (render_ovl_expr_len)
                call    render_print_expr
                ld      a, ")"
                jp      render_out_append


; -----------------------------------------------------------------------
; render_open_stmt — start a new statement line.  Mirrors emitInsnRun's
; `if prevWasStatement '\n'` + `out.WriteString("  ")` (overlay.go:39-42).
;
; Reads render_prev_stmt (the caller sets it true AFTER the element emits,
; matching the Go ordering).  Clobbers A, HL, DE, F.
; -----------------------------------------------------------------------
render_open_stmt:
                ld      a, (render_prev_stmt)
                or      a
                jr      z, render_open_stmt_indent
                ld      a, 10                   ; close the previous line
                call    render_out_append
render_open_stmt_indent:
                ld      a, " "
                call    render_out_append
                ld      a, " "
                jp      render_out_append       ; tail-call (second indent space)


; -----------------------------------------------------------------------
; render_copy_cstr — append a null-terminated string to render_out.
;
; Input:  HL = source string ptr.  Clobbers A, HL, DE, F (BC preserved).
; -----------------------------------------------------------------------
render_copy_cstr:
                ld      a, (hl)
                or      a
                ret     z
                push    hl
                call    render_out_append
                pop     hl
                inc     hl
                jr      render_copy_cstr


; -----------------------------------------------------------------------
; render_out_append — append the byte in A to render_out; bump
; render_out_len.
;
; render_out lives in section C (HMPR-stable), so the append is unaffected
; by the LMPR window the reader toggles for the IN pages.
;
; Input:  A = byte.  Output: byte stored, render_out_len += 1.
; Clobbers: HL, DE, F.  Preserves A, BC.
; -----------------------------------------------------------------------
render_out_append:
                push    af
                ld      hl, (render_out_len)
                ld      de, render_out
                add     hl, de                  ; HL = render_out + len
                pop     af
                ld      (hl), a
                ld      hl, (render_out_len)
                inc     hl
                ld      (render_out_len), hl
                ret


; -----------------------------------------------------------------------
; render_reset_state — zero the header-def cursors/counts and the PC before a
; render (reader_init's seed callbacks then fill render_labels/render_locals).
; Clobbers: A, HL.
; -----------------------------------------------------------------------
render_reset_state:
                ld      hl, 0
                ld      (render_label_count), hl
                ld      (render_label_cursor), hl
                ld      (render_local_count), hl
                ld      (render_local_cursor), hl
                ld      hl, render_labels
                ld      (render_label_next), hl
                ld      hl, render_locals
                ld      (render_local_next), hl
                xor     a
                ld      (render_pc + 0), a
                ld      (render_pc + 1), a
                ld      (render_pc + 2), a
                ld      (render_pc + 3), a
                ld      (render_origin_vma + 0), a      ; leading `.org` sets it
                ld      (render_origin_vma + 1), a
                ld      (render_origin_vma + 2), a
                ld      (render_origin_vma + 3), a
                ret


; -----------------------------------------------------------------------
; render_store_label — reader header-table label seed callback (the driver's
; symbol_insert dispatches here).  Appends a {offset, name_id} row.
;
; Mirrors newHeaderDefs's label ingest (pc.go:49-51): the reader has already
; accumulated the row's byte offset in symbol_value_buf and passes name_id.
;
; Input:  HL = name_id; symbol_value_buf = 4-byte LE offset from origin.
; Output: render_labels[render_label_count++] = {offset, name_id}.
; Clobbers: A, BC, DE, HL (the reader permits symbol_insert to clobber all).
; -----------------------------------------------------------------------
render_store_label:
                ld      (render_tmp_id), hl         ; save name_id
                ld      hl, (render_label_next)
                ex      de, hl                      ; DE = slot
                ld      hl, symbol_value_buf
                ld      bc, 4
                ldir                                ; copy offset; DE = slot+4
                ld      hl, (render_tmp_id)
                ex      de, hl                      ; HL = slot+4, DE = name_id
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl                          ; HL = next slot (slot+6)
                ld      (render_label_next), hl
                ld      hl, (render_label_count)
                inc     hl
                ld      (render_label_count), hl
                ret


; -----------------------------------------------------------------------
; render_store_local — reader header-table local seed callback (the driver's
; local_def_append dispatches here).  Appends a {offset, digit} row.
;
; Mirrors newHeaderDefs's local ingest (pc.go:52-53).
;
; Input:  A = digit; local_label_pc_buf = 4-byte LE offset from origin.
; Output: render_locals[render_local_count++] = {offset, digit}.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_store_local:
                ld      (render_tmp_digit), a
                ld      hl, (render_local_next)
                ex      de, hl                      ; DE = slot
                ld      hl, local_label_pc_buf
                ld      bc, 4
                ldir                                ; copy offset; DE = slot+4
                ld      a, (render_tmp_digit)
                ld      (de), a
                inc     de                          ; DE = next slot (slot+5)
                ld      (render_local_next), de
                ld      hl, (render_local_count)
                inc     hl
                ld      (render_local_count), hl
                ret


; -----------------------------------------------------------------------
; render_pc_advance4 — PC += 4 (32-bit LE).  emitInsnRun overlay.go:47.
; Clobbers: A?, BC, HL.
; -----------------------------------------------------------------------
render_pc_advance4:
                ld      hl, (render_pc)
                ld      bc, 4
                add     hl, bc
                ld      (render_pc), hl
                ret     nc
                ld      hl, (render_pc + 2)
                inc     hl
                ld      (render_pc + 2), hl
                ret


; -----------------------------------------------------------------------
; render_flush — emit every header definition whose offset == the current PC.
; Labels drain before locals at a shared offset (newHeaderDefs's stable order,
; pc.go:55-66); within a kind the reader's ascending-offset order is used.
; Mirrors flush() → hd.flushAt(pc, emitDef) (emit.go:122, pc.go:78-83).  The
; comment sidecar is not consumed this slice, so flushComments is a no-op here.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_flush:
render_flush_labels:
                ld      hl, (render_label_cursor)
                ld      de, (render_label_count)
                or      a
                sbc     hl, de
                jr      nc, render_flush_locals     ; cursor >= count → labels done
                call    render_label_slot_addr      ; HL = &render_labels[cursor]
                call    render_offset_eq_pc         ; Z=1 iff row.offset == PC
                jr      nz, render_flush_locals     ; sorted — stop at first miss
                call    render_emit_label
                ld      hl, (render_label_cursor)
                inc     hl
                ld      (render_label_cursor), hl
                jr      render_flush_labels
render_flush_locals:
                ld      hl, (render_local_cursor)
                ld      de, (render_local_count)
                or      a
                sbc     hl, de
                ret     nc                          ; cursor >= count → done
                call    render_local_slot_addr
                call    render_offset_eq_pc
                ret     nz
                call    render_emit_local
                ld      hl, (render_local_cursor)
                inc     hl
                ld      (render_local_cursor), hl
                jr      render_flush_locals


; render_label_slot_addr — HL = render_labels + render_label_cursor*6.
render_label_slot_addr:
                ld      hl, (render_label_cursor)
                ld      d, h
                ld      e, l                        ; DE = cursor
                add     hl, hl                      ; 2*cursor
                add     hl, de                      ; 3*cursor
                add     hl, hl                      ; 6*cursor
                ld      de, render_labels
                add     hl, de
                ret

; render_local_slot_addr — HL = render_locals + render_local_cursor*5.
render_local_slot_addr:
                ld      hl, (render_local_cursor)
                ld      d, h
                ld      e, l                        ; DE = cursor
                add     hl, hl                      ; 2*cursor
                add     hl, hl                      ; 4*cursor
                add     hl, de                      ; 5*cursor
                ld      de, render_locals
                add     hl, de
                ret

; render_offset_eq_pc — Z=1 iff the 4-byte LE offset at (HL) equals render_pc.
; Preserves HL (the caller reads the row after).  Clobbers: A, B, DE.
render_offset_eq_pc:
                push    hl
                ld      de, render_pc
                ld      b, 4
render_oep_loop:
                ld      a, (de)
                cp      (hl)
                jr      nz, render_oep_ne
                inc     hl
                inc     de
                djnz    render_oep_loop
                pop     hl
                xor     a                           ; Z=1 (equal)
                ret
render_oep_ne:
                pop     hl
                ld      a, 1
                or      a                           ; Z=0 (not equal)
                ret


; -----------------------------------------------------------------------
; render_emit_label / render_emit_local — emitDef (emit.go:97-107): a leading
; newline if a statement is open, then "NAME:" (label) or "N:" (local digit),
; then prevWasStatement = true.
;
; Input:  HL = row slot ({offset u32}{name_id u16} or {offset u32}{digit u8}).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_emit_label:
                push    hl                          ; save slot across the '\n'
                ld      a, (render_prev_stmt)
                or      a
                jr      z, render_el_no_nl
                ld      a, 10
                call    render_out_append
render_el_no_nl:
                pop     hl
                ld      de, 4
                add     hl, de                      ; HL = &name_id
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                     ; DE = name_id
                call    render_name_of              ; HL = name string ptr
                call    render_copy_cstr
                ld      a, ":"
                call    render_out_append
                ld      a, 1
                ld      (render_prev_stmt), a
                ret

render_emit_local:
                push    hl
                ld      a, (render_prev_stmt)
                or      a
                jr      z, render_elo_no_nl
                ld      a, 10
                call    render_out_append
render_elo_no_nl:
                pop     hl
                ld      de, 4
                add     hl, de
                ld      a, (hl)                     ; A = digit (0..9)
                add     a, "0"
                call    render_out_append
                ld      a, ":"
                call    render_out_append
                ld      a, 1
                ld      (render_prev_stmt), a
                ret


; -----------------------------------------------------------------------
; render_lit_data — render a KindLitData record (a run of constant data
; bytes) as source data directives.  Mirrors the KindLitData case of EmitFile
; (emit.go:212-217): flush header defs, emit the directive line(s), then
; advance the PC by the raw data length.
;
; Payload: [dir_id u8][raw little-endian data bytes].
;
; Input:  HL = payload ptr (STAGING_BUF), BC = payload length (= 1 + nbytes).
; -----------------------------------------------------------------------
render_lit_data:
                ld      (render_ld_src), hl
                ld      (render_ld_len), bc

; flush any header-table label/local whose offset == the current PC before the
; data (emit.go:213, flush()).
                call    render_flush

                call    render_emit_lit_data

; advance the PC by the raw data length: nbytes = payload_len - 1 (emit.go:217).
                ld      bc, (render_ld_len)
                dec     bc
                jp      render_pc_add_bc        ; tail-call


; -----------------------------------------------------------------------
; render_emit_lit_data — port of emitLitData (overlay.go:304).  Resolves the
; directive name + element width from the record's directive id, then emits
; the little-endian values as `%#x`, litDataPerLine (16) values per line,
; wrapping onto fresh "  <name> " continuation lines (overlay.go:317-334).
;
; Input:  render_ld_src / render_ld_len (the staged payload).
; Clobbers: A, BC, DE, HL (loop state is held in memory, not registers).
; -----------------------------------------------------------------------
render_emit_lit_data:
; directive id → name pointer + element width (dataWidth, overlay.go:338).
                ld      hl, (render_ld_src)
                ld      a, (hl)                 ; dir_id
                call    render_ld_dir_lookup    ; HL = name ptr, render_ld_width set
                jp      z, fail                 ; width 0 → not a numeric data dir
                ld      (render_ld_nameptr), hl

; data cursor = src + 1; remaining bytes = len - 1; element index = 0.
                ld      hl, (render_ld_src)
                inc     hl
                ld      (render_ld_data), hl
                ld      hl, (render_ld_len)
                dec     hl
                ld      (render_ld_rem_bytes), hl
                xor     a
                ld      (render_ld_idx), a

render_eld_loop:
                ld      hl, (render_ld_rem_bytes)
                ld      a, h
                or      l
                ret     z                       ; nElem elements emitted

; i % litDataPerLine == 0 → open a fresh directive line; else ", " separator.
                ld      a, (render_ld_idx)
                and     &0F                     ; litDataPerLine = 16
                jr      nz, render_eld_sep

                ld      a, (render_prev_stmt)
                or      a
                jr      z, render_eld_no_nl
                ld      a, 10                   ; close the previous line
                call    render_out_append
render_eld_no_nl:
                ld      a, " "
                call    render_out_append
                ld      a, " "
                call    render_out_append
                ld      hl, (render_ld_nameptr)
                call    render_copy_cstr        ; the directive name
                ld      a, " "
                call    render_out_append
                ld      a, 1
                ld      (render_prev_stmt), a   ; prevWasStatement = true
                jr      render_eld_value
render_eld_sep:
                ld      a, ","
                call    render_out_append
                ld      a, " "
                call    render_out_append
render_eld_value:
                call    render_emit_hex_value

; advance: data += width, rem -= width, i += 1.
                ld      a, (render_ld_width)
                ld      c, a
                ld      b, 0
                ld      hl, (render_ld_data)
                add     hl, bc
                ld      (render_ld_data), hl
                ld      hl, (render_ld_rem_bytes)
                or      a
                sbc     hl, bc
                ld      (render_ld_rem_bytes), hl
                ld      a, (render_ld_idx)
                inc     a
                ld      (render_ld_idx), a
                jr      render_eld_loop


; -----------------------------------------------------------------------
; render_ld_dir_lookup — map a data-directive id to its name string + element
; width (dataWidth, overlay.go:338).  Only the numeric data directives
; (.byte/.short/.word/.quad/.hword) appear in a LIT_DATA record.
;
; Input:  A = directive id.
; Output: HL = null-terminated name ptr; render_ld_width = width.
;         Z=1 (and width 0) if the id is not a numeric data directive.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_ld_dir_lookup:
                ld      b, 5                    ; five numeric data directives
                ld      hl, render_ld_dir_table
render_lddl_loop:
                cp      (hl)
                jr      z, render_lddl_found
                inc     hl
                inc     hl
                inc     hl
                inc     hl                      ; next 4-byte {id,width,nameptr}
                djnz    render_lddl_loop
                xor     a                       ; Z=1 → unknown directive id
                ret
render_lddl_found:
                inc     hl
                ld      a, (hl)                 ; width
                ld      (render_ld_width), a
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = name ptr
                ex      de, hl                  ; HL = name ptr
                ld      a, (render_ld_width)
                or      a                       ; width nonzero → Z=0 (found)
                ret


; -----------------------------------------------------------------------
; render_emit_hex_value — append "0x" then the width-byte little-endian value
; at render_ld_data as leading-zero-suppressed lower-case hex, mirroring Go's
; `fmt.Fprintf(out, "%#x", v)` (overlay.go:333).  A zero value renders "0x0".
;
; Input:  render_ld_data = data ptr; render_ld_width = element width (1/2/4/8).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_emit_hex_value:
                ld      a, "0"
                call    render_out_append
                ld      a, "x"
                call    render_out_append
                xor     a
                ld      (render_ld_seen), a     ; no significant digit emitted yet

; HL = &MSB = data + width - 1; walk down to the LSB.
                ld      a, (render_ld_width)
                ld      b, a                    ; B = byte count
                ld      e, a
                ld      d, 0
                ld      hl, (render_ld_data)
                add     hl, de
                dec     hl
render_ehv_loop:
                ld      c, (hl)                 ; C = current byte (kept over emits)
                push    hl
                ld      a, c
                rrca
                rrca
                rrca
                rrca
                and     &0F
                call    render_emit_nibble      ; high nibble (preserves BC)
                ld      a, c
                and     &0F
                call    render_emit_nibble      ; low nibble
                pop     hl
                dec     hl
                djnz    render_ehv_loop

; A value of exactly zero suppressed every nibble; emit a single "0".
                ld      a, (render_ld_seen)
                or      a
                ret     nz
                ld      a, "0"
                jp      render_out_append


; -----------------------------------------------------------------------
; render_emit_nibble — append one hex nibble with leading-zero suppression.
; A leading zero (render_ld_seen still 0) is skipped; a zero after a
; significant digit renders "0".  Preserves BC (the caller's byte/counter).
;
; Input:  A = nibble (0..15).  Clobbers: A, DE, HL, F.
; -----------------------------------------------------------------------
render_emit_nibble:
                or      a
                jr      nz, render_ren_emit
                ld      a, (render_ld_seen)
                or      a
                ret     z                       ; leading zero → skip
                xor     a                       ; zero after a digit → emit "0"
render_ren_emit:
                push    af
                ld      a, 1
                ld      (render_ld_seen), a
                pop     af
                cp      10
                jr      c, render_ren_digit
                add     a, "a" - 10
                jp      render_out_append
render_ren_digit:
                add     a, "0"
                jp      render_out_append


; -----------------------------------------------------------------------
; render_pc_add_bc — render_pc += BC (32-bit LE PC, 16-bit addend).  Used to
; advance the PC over a LIT_DATA record's raw bytes (emit.go:217).
; Clobbers: A?, BC, HL.
; -----------------------------------------------------------------------
render_pc_add_bc:
                ld      hl, (render_pc)
                add     hl, bc
                ld      (render_pc), hl
                ret     nc
                ld      hl, (render_pc + 2)
                inc     hl
                ld      (render_pc + 2), hl
                ret


; render_ld_dir_table — {id u8, width u8, name ptr u16} for each numeric data
; directive (overlay.go:338 dataWidth; ids from tbn_constants.inc / DIR_*).
render_ld_dir_table:
                defb    DIR_BYTE, 1
                defw    render_str_byte
                defb    DIR_SHORT, 2
                defw    render_str_short
                defb    DIR_WORD, 4
                defw    render_str_word
                defb    DIR_QUAD, 8
                defw    render_str_quad
                defb    DIR_HWORD, 2
                defw    render_str_hword

render_str_byte:        defm    ".byte"
                        defb    0
render_str_short:       defm    ".short"
                        defb    0
render_str_word:        defm    ".word"
                        defb    0
render_str_quad:        defm    ".quad"
                        defb    0
render_str_hword:       defm    ".hword"
                        defb    0


; =======================================================================
; SLICE 5 — DIRECTIVE records: the operand/expr printer + PC sizing.
;
; Ports the DIRECTIVE path of the Go authority tools/sam-aarch64/render:
;   render_directive          <- EmitFile KindDirective case (emit.go:156-205)
;   render_dir_emit_operands  <- emitStatement directive path (emit.go:291-307)
;   render_emit_operand       <- emitOperandWithContext (emit.go:343-395)
;   render_emit_imm_expr      <- emitExprAsImmediateWithContext (emit.go:491-526)
;   render_simple_symref      <- simpleSymRef (emit.go:533-567)
;   render_print_expr         <- printExpr + opSym/relName (emit.go:593-714)
;   render_eval_const         <- format.EvalConst (expr.go:223-274)
;   render_emit_escaped       <- writeEscapedString (emit.go:716-737)
;   render_dir_byte_size      <- directiveByteSize + alignPad (pc.go:142-216)
;
; Expression bytecode is walked directly out of the staged record in
; STAGING_BUF (section D, HMPR-stable — no paging), so a plain byte cursor
; suffices.  Constant values are folded into a 32-bit signed accumulator
; (render_num32); this narrows Go's int64 fold — sufficient for every
; DIRECTIVE constant this renderer produces (SAM origins are 16-bit; aligns/
; sets/skips are small), and consistent with the 32-bit render_pc model.
; =======================================================================

; Expression-bytecode opcodes (ExprOp, tools/sam-aarch64-format/expr.go:11-42).
EXPR_PUSH_IMM8:         equ     &01
EXPR_PUSH_IMM16:        equ     &02
EXPR_PUSH_IMM32:        equ     &03
EXPR_PUSH_IMM64:        equ     &04
EXPR_PUSH_SYM:          equ     &05
EXPR_PUSH_LOCAL:        equ     &06
EXPR_PUSH_PC:           equ     &07
EXPR_OP_ADD:            equ     &10
EXPR_OP_SUB:            equ     &11
EXPR_OP_MUL:            equ     &12
EXPR_OP_DIV:            equ     &13
EXPR_OP_AND:            equ     &14
EXPR_OP_OR:             equ     &15
EXPR_OP_XOR:            equ     &16
EXPR_OP_SHL:            equ     &17
EXPR_OP_SHR:            equ     &18
EXPR_OP_NEG:            equ     &20
EXPR_OP_NOT:            equ     &21
EXPR_REL_LO12:          equ     &30
EXPR_REL_HI12:          equ     &31
EXPR_REL_ABS_G0:        equ     &32
EXPR_REL_ABS_G0NC:      equ     &33
EXPR_REL_ABS_G1:        equ     &34
EXPR_REL_ABS_G1NC:      equ     &35
EXPR_REL_ABS_G2:        equ     &36
EXPR_REL_ABS_G2NC:      equ     &37
EXPR_REL_ABS_G3:        equ     &38

LIT_DIR_PER_LINE:       equ     16


; -----------------------------------------------------------------------
; render_directive — render a KindDirective record.  Mirrors the
; KindDirective case of EmitFile (emit.go:156-205): flush header defs (all
; directives except `.org`), emit `"  " + name + " " + operands`, then advance
; the PC by the directive's byte contribution (`.org` re-bases the origin).
;
; Input:  HL = payload ptr (STAGING_BUF) = [dir_id u8][op_count u8][operands],
;         BC = payload length.
; -----------------------------------------------------------------------
render_directive:
                ld      a, (hl)
                ld      (render_dir_id), a
                inc     hl
                ld      a, (hl)
                ld      (render_dir_opcount), a
                inc     hl
                ld      (render_dir_ops_ptr), hl    ; operands cursor start
                ld      h, b
                ld      l, c
                dec     hl
                dec     hl                          ; ops_len = payload_len - 2
                ld      (render_dir_ops_len), hl

; Flush labels/locals before the directive EXCEPT `.org` (emit.go:165-171): a
; label coincident with the leading `.org` must render AFTER it (origin
; semantics).  No comment sidecar this slice, so `.org` simply skips the flush.
                ld      a, (render_dir_id)
                cp      DIR_ORG
                jr      z, render_dir_noflush
                call    render_flush
render_dir_noflush:
                call    render_open_stmt            ; newline-if-prev + "  "

                ld      a, (render_dir_id)
                call    render_dir_name_ptr         ; HL = name string ptr
                call    render_copy_cstr

                ld      a, (render_dir_opcount)
                or      a
                jr      z, render_dir_no_ops
                ld      a, " "
                call    render_out_append
                call    render_dir_emit_operands
render_dir_no_ops:
                ld      a, 1
                ld      (render_prev_stmt), a        ; prevWasStatement = true

; PC advance (emit.go:182-205).  `.org` re-bases; all others add their size.
                ld      a, (render_dir_id)
                cp      DIR_ORG
                jp      z, render_dir_pc_org
                call    render_dir_byte_size         ; render_num32 = byte size
                jp      render_pc_add_num32          ; tail: render_pc += render_num32

render_dir_pc_org:
                call    render_dir_eval_op0          ; render_num32 = target VMA
                call    render_pc_is_zero            ; Z=1 iff render_pc == 0
                jp      nz, render_dir_org_advance
                call    render_origin_is_zero        ; Z=1 iff origin == 0
                jp      nz, render_dir_org_advance
; first `.org`: origin = target, pc stays 0 (emit.go:193-194).
                ld      hl, render_num32
                ld      de, render_origin_vma
                ld      bc, 4
                ldir
                ret
render_dir_org_advance:
; later `.org`: pc = target - origin (emit.go:195-196).
                ld      hl, (render_num32)
                ld      de, (render_origin_vma)
                or      a
                sbc     hl, de
                ld      (render_pc), hl
                ld      hl, (render_num32 + 2)
                ld      de, (render_origin_vma + 2)
                sbc     hl, de
                ld      (render_pc + 2), hl
                ret


; render_dir_name_ptr — A = dir_id → HL = null-terminated directive name.
; format.DirectiveName (directives.go:46), indexed straight into the 22-entry
; pointer table render_dir_names.
render_dir_name_ptr:
                ld      l, a
                ld      h, 0
                add     hl, hl                       ; dir_id * 2
                ld      de, render_dir_names
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl
                ret


; render_dir_emit_operands — walk the operand stream, emitting each in
; directive (immediate) context, joined by ", " (emit.go:291-307).
render_dir_emit_operands:
                ld      hl, (render_dir_ops_ptr)
                ld      (render_op_cur), hl
                ld      de, (render_dir_ops_len)
                add     hl, de
                ld      (render_op_end), hl
                ld      a, 1
                ld      (render_op_first), a
render_deo_loop:
                ld      hl, (render_op_cur)
                ld      de, (render_op_end)
                or      a
                sbc     hl, de
                ret     nc                           ; cur >= end → done
                ld      a, (render_op_first)
                or      a
                jr      nz, render_deo_nosep
                ld      a, ","
                call    render_out_append
                ld      a, " "
                call    render_out_append
render_deo_nosep:
                xor     a
                ld      (render_op_first), a
                call    render_op_next               ; parse one operand
                call    render_emit_operand
                jr      render_deo_loop


; render_op_next — parse one operand from render_op_cur.  IMM_EXPR / STRING /
; SYS_NAME share the shape [kind u8][len u16][bytes] (operands.go:148-223);
; those are the only kinds a directive carries.  Sets render_op_kind /
; render_op_data / render_op_len and advances render_op_cur.
render_op_next:
                ld      hl, (render_op_cur)
                ld      a, (hl)
                ld      (render_op_kind), a
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                           ; DE = len, HL = data ptr
                ld      (render_op_len), de
                ld      (render_op_data), hl
                add     hl, de
                ld      (render_op_cur), hl
                ret


; render_emit_operand — emitOperandWithContext for a directive operand
; (emit.go:343-395).  IMM_EXPR → immediate; STRING → quoted+escaped;
; SYS_NAME → raw bytes.  Register/mem/etc. never appear in a directive.
render_emit_operand:
                ld      a, (render_op_kind)
                cp      OP_KIND_IMM_EXPR
                jp      z, render_emit_imm_expr
                cp      OP_KIND_STRING
                jp      z, render_emit_string_op
                cp      OP_KIND_SYS_NAME
                jp      z, render_emit_sysname_op
                jp      fail

render_emit_string_op:
                ld      a, 34                        ; '"'
                call    render_out_append
                call    render_emit_escaped
                ld      a, 34
                jp      render_out_append

render_emit_sysname_op:
                ld      hl, (render_op_data)
                ld      bc, (render_op_len)
                jp      render_out_put_bytes


; render_emit_imm_expr — emitExprAsImmediateWithContext, directive context
; (emit.go:491-526).  Const → decimal or %#x (no leading '#'); else a simple
; symbol ref; else "(" printExpr ")".
render_emit_imm_expr:
                ld      hl, (render_op_data)
                ld      bc, (render_op_len)
                call    render_eval_const            ; Z=1 → const, render_num32=v
                jr      nz, render_eie_notconst
                jp      render_emit_const_directive
render_eie_notconst:
                ld      hl, (render_op_data)
                ld      bc, (render_op_len)
                call    render_simple_symref         ; Z=1 → matched (emitted)
                ret     z
                ld      a, "("
                call    render_out_append
                ld      hl, (render_op_data)
                ld      bc, (render_op_len)
                call    render_print_expr
                ld      a, ")"
                jp      render_out_append


; render_emit_const_directive — print render_num32 in directive context, no
; leading '#': decimal for a magnitude in 1..255, else %#x (emit.go:491-503).
render_emit_const_directive:
                ld      hl, render_out_append
                ld      (render_sink_addr), hl
                ld      a, (render_num32 + 3)
                bit     7, a
                jr      nz, render_ecd_neg
; v >= 0: decimal iff bytes 1..3 are all zero (v < 256).
                ld      a, (render_num32 + 1)
                ld      hl, render_num32 + 2
                or      (hl)
                inc     hl
                or      (hl)
                jr      z, render_ecd_dec
                jp      render_emit_hashx_s32
render_ecd_neg:
; v < 0: decimal iff bytes 1..3 are all &FF and byte0 != 0 (v > -256).
                ld      a, (render_num32 + 1)
                ld      hl, render_num32 + 2
                and     (hl)
                inc     hl
                and     (hl)
                cp      &FF
                jp      nz, render_emit_hashx_s32
                ld      a, (render_num32)
                or      a
                jp      z, render_emit_hashx_s32
render_ecd_dec:
                jp      render_emit_dec_s32


; -----------------------------------------------------------------------
; render_simple_symref — simpleSymRef (emit.go:533-567).  Recognises a bare
; PUSH_SYM (→ "name"), PUSH_SYM;REL_LO12/HI12 (→ ":lo12:name"/":hi12:name") and
; PUSH_LOCAL (→ "Nf"/"Nb"), writing the text straight to render_out.
;
; Input:  HL = expr ptr, BC = expr len.
; Output: Z=1 if matched (text emitted); Z=0 if not (caller falls to printExpr).
; -----------------------------------------------------------------------
render_simple_symref:
                call    render_expr_init
                call    render_expr_atend
                jp      z, render_ssr_no
                call    render_expr_next             ; A = opcode
                cp      EXPR_PUSH_SYM
                jr      z, render_ssr_sym
                cp      EXPR_PUSH_LOCAL
                jr      z, render_ssr_local
                jp      render_ssr_no

render_ssr_sym:
                ld      hl, (render_expr_operand)
                ld      a, (hl)
                ld      (render_ssr_id), a
                inc     hl
                ld      a, (hl)
                ld      (render_ssr_id + 1), a       ; render_ssr_id = name_id
                call    render_expr_atend
                jr      z, render_ssr_sym_plain
                call    render_expr_next             ; A = op2
                ld      (render_ssr_op2), a
                call    render_expr_atend
                jp      nz, render_ssr_no            ; trailing bytes → not simple
                ld      a, (render_ssr_op2)
                cp      EXPR_REL_LO12
                jr      z, render_ssr_lo12
                cp      EXPR_REL_HI12
                jr      z, render_ssr_hi12
                jp      render_ssr_no
render_ssr_lo12:
                ld      a, ":"
                call    render_out_append
                ld      hl, render_str_lo12
                call    render_copy_cstr
                ld      a, ":"
                call    render_out_append
                jr      render_ssr_emit_name
render_ssr_hi12:
                ld      a, ":"
                call    render_out_append
                ld      hl, render_str_hi12
                call    render_copy_cstr
                ld      a, ":"
                call    render_out_append
                jr      render_ssr_emit_name
render_ssr_sym_plain:
render_ssr_emit_name:
                ld      de, (render_ssr_id)
                call    render_name_of
                call    render_copy_cstr
                xor     a                            ; Z=1 matched
                ret

render_ssr_local:
                ld      hl, (render_expr_operand)
                ld      a, (hl)
                ld      (render_ssr_id), a           ; digit
                inc     hl
                ld      a, (hl)
                ld      (render_ssr_id + 1), a       ; direction (0=f,1=b)
                call    render_expr_atend
                jp      nz, render_ssr_no
                ld      a, (render_ssr_id)
                add     a, "0"
                call    render_out_append
                ld      a, (render_ssr_id + 1)
                or      a
                ld      a, "f"
                jr      z, render_ssr_local_dir
                ld      a, "b"
render_ssr_local_dir:
                call    render_out_append
                xor     a                            ; Z=1 matched
                ret

render_ssr_no:
                ld      a, 1
                or      a                            ; Z=0 not matched
                ret


; -----------------------------------------------------------------------
; render_print_expr — printExpr (emit.go:593-714): render bytecode as infix
; text with an atom-tracking stack, parenthesising bare binary/unary operands.
; Fragments are built in render_pe_arena (append-only); the stack holds
; {ptr u16, len u16, atom u8} rows.  Leaf number pushes go through render_sink
; (retargeted to the arena for the duration).  The top-of-stack text is copied
; to render_out unparenthesised (the caller adds the single enclosing pair).
;
; Input:  HL = expr ptr, BC = expr len.
; -----------------------------------------------------------------------
render_print_expr:
                call    render_expr_init
                ld      hl, (render_sink_addr)
                push    hl                           ; save the live sink
                ld      hl, render_pe_putc
                ld      (render_sink_addr), hl
                ld      hl, render_pe_arena
                ld      (render_pe_next), hl
                xor     a
                ld      (render_pe_depth), a
render_pe_loop:
                call    render_expr_atend
                jp      z, render_pe_finish
                call    render_expr_next             ; A = op, render_cur_op set
                cp      EXPR_PUSH_IMM8
                jp      z, render_pe_imm8
                cp      EXPR_PUSH_IMM16
                jp      z, render_pe_imm16
                cp      EXPR_PUSH_IMM32
                jp      z, render_pe_imm32
                cp      EXPR_PUSH_IMM64
                jp      z, render_pe_imm64
                cp      EXPR_PUSH_SYM
                jp      z, render_pe_sym
                cp      EXPR_PUSH_LOCAL
                jp      z, render_pe_local
                cp      EXPR_PUSH_PC
                jp      z, render_pe_pc
                cp      EXPR_OP_NEG
                jp      z, render_pe_neg
                cp      EXPR_OP_NOT
                jp      z, render_pe_not
                cp      EXPR_OP_ADD
                jp      z, render_pe_binary
                cp      EXPR_OP_SUB
                jp      z, render_pe_binary
                cp      EXPR_OP_MUL
                jp      z, render_pe_binary
                cp      EXPR_OP_DIV
                jp      z, render_pe_binary
                cp      EXPR_OP_AND
                jp      z, render_pe_binary
                cp      EXPR_OP_OR
                jp      z, render_pe_binary
                cp      EXPR_OP_XOR
                jp      z, render_pe_binary
                cp      EXPR_OP_SHL
                jp      z, render_pe_binary
                cp      EXPR_OP_SHR
                jp      z, render_pe_binary
                cp      EXPR_REL_LO12
                jp      z, render_pe_rel
                cp      EXPR_REL_HI12
                jp      z, render_pe_rel
                cp      EXPR_REL_ABS_G0
                jp      z, render_pe_rel
                cp      EXPR_REL_ABS_G0NC
                jp      z, render_pe_rel
                cp      EXPR_REL_ABS_G1
                jp      z, render_pe_rel
                cp      EXPR_REL_ABS_G1NC
                jp      z, render_pe_rel
                cp      EXPR_REL_ABS_G2
                jp      z, render_pe_rel
                cp      EXPR_REL_ABS_G2NC
                jp      z, render_pe_rel
                cp      EXPR_REL_ABS_G3
                jp      z, render_pe_rel
                jp      fail
render_pe_finish:
                pop     hl
                ld      (render_sink_addr), hl       ; restore the sink
                ld      hl, render_pe_stack          ; stack[0].text (top when depth==1)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                      ; DE=ptr, BC=len
                ex      de, hl
                jp      render_out_put_bytes

render_pe_imm8:
                call    render_pe_frag_begin
                ld      hl, (render_expr_operand)
                ld      a, (hl)
                call    render_sx8_to_num32
                call    render_emit_dec_s32
                call    render_pe_push_frag_atom
                jp      render_pe_loop
render_pe_imm16:
                call    render_pe_frag_begin
                call    render_sx16_to_num32
                call    render_emit_dec_s32
                call    render_pe_push_frag_atom
                jp      render_pe_loop
render_pe_imm32:
                call    render_pe_frag_begin
                call    render_sx32_to_num32
                call    render_emit_dec_s32
                call    render_pe_push_frag_atom
                jp      render_pe_loop
render_pe_imm64:
                call    render_pe_frag_begin
                call    render_sx64_to_num32
                call    render_emit_dec_s32
                call    render_pe_push_frag_atom
                jp      render_pe_loop
render_pe_sym:
                call    render_pe_frag_begin
                ld      hl, (render_expr_operand)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                      ; DE = name_id
                call    render_name_of
                call    render_pe_puts
                call    render_pe_push_frag_atom
                jp      render_pe_loop
render_pe_local:
                call    render_pe_frag_begin
                ld      hl, (render_expr_operand)
                ld      a, (hl)
                add     a, "0"
                call    render_pe_putc
                ld      hl, (render_expr_operand)
                inc     hl
                ld      a, (hl)
                or      a
                ld      a, "f"
                jr      z, render_pe_local_dir
                ld      a, "b"
render_pe_local_dir:
                call    render_pe_putc
                call    render_pe_push_frag_atom
                jp      render_pe_loop
render_pe_pc:
                call    render_pe_frag_begin
                ld      a, "."
                call    render_pe_putc
                call    render_pe_push_frag_atom
                jp      render_pe_loop
render_pe_neg:
                call    render_pe_frag_begin
                ld      a, "-"
                call    render_pe_putc
                call    render_pe_entry_top
                call    render_pe_put_operand
                ld      a, (render_pe_depth)
                dec     a
                ld      (render_pe_depth), a
                call    render_pe_push_frag_nonatom
                jp      render_pe_loop
render_pe_not:
                call    render_pe_frag_begin
                ld      a, "~"
                call    render_pe_putc
                call    render_pe_entry_top
                call    render_pe_put_operand
                ld      a, (render_pe_depth)
                dec     a
                ld      (render_pe_depth), a
                call    render_pe_push_frag_nonatom
                jp      render_pe_loop
render_pe_rel:
                call    render_pe_frag_begin
                ld      a, ":"
                call    render_pe_putc
                call    render_pe_put_relname
                ld      a, ":"
                call    render_pe_putc
                call    render_pe_entry_top
                call    render_pe_put_operand
                ld      a, (render_pe_depth)
                dec     a
                ld      (render_pe_depth), a
                call    render_pe_push_frag_atom
                jp      render_pe_loop
render_pe_binary:
                call    render_pe_frag_begin
                call    render_pe_entry_second
                call    render_pe_put_operand        ; asOperand(a)
                ld      a, " "
                call    render_pe_putc
                call    render_pe_put_opsym
                ld      a, " "
                call    render_pe_putc
                call    render_pe_entry_top
                call    render_pe_put_operand        ; asOperand(b)
                ld      a, (render_pe_depth)
                sub     2
                ld      (render_pe_depth), a
                call    render_pe_push_frag_nonatom
                jp      render_pe_loop


; render_pe_frag_begin — mark the arena tip as the next fragment's start.
render_pe_frag_begin:
                ld      hl, (render_pe_next)
                ld      (render_pe_frag_start), hl
                ret

; render_pe_putc — append A to the arena; preserves A, BC, DE.
render_pe_putc:
                push    hl
                ld      hl, (render_pe_next)
                ld      (hl), a
                inc     hl
                ld      (render_pe_next), hl
                pop     hl
                ret

; render_pe_puts — append a null-terminated string (HL) to the arena.
render_pe_puts:
                ld      a, (hl)
                or      a
                ret     z
                push    hl
                call    render_pe_putc
                pop     hl
                inc     hl
                jr      render_pe_puts

; render_pe_copy — append BC bytes from HL to the arena.
render_pe_copy:
                ld      a, b
                or      c
                ret     z
render_pe_copy_loop:
                ld      a, (hl)
                push    hl
                push    bc
                call    render_pe_putc
                pop     bc
                pop     hl
                inc     hl
                dec     bc
                ld      a, b
                or      c
                jr      nz, render_pe_copy_loop
                ret

; render_pe_put_operand — asOperand (emit.go:582-587): append the entry (HL)
; text raw when atomic, else parenthesised.
render_pe_put_operand:
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                           ; DE = frag ptr
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                           ; BC = frag len
                ld      a, (hl)                      ; A  = atom flag
                or      a
                jr      nz, render_ppo_atom
                ld      a, "("
                call    render_pe_putc
                ex      de, hl
                call    render_pe_copy
                ld      a, ")"
                jp      render_pe_putc
render_ppo_atom:
                ex      de, hl
                jp      render_pe_copy

; render_pe_entry_* — HL = &render_pe_stack[index]; each row is 5 bytes.
render_pe_entry_at_depth:
                ld      a, (render_pe_depth)
                jr      render_pe_entry_a
render_pe_entry_top:
                ld      a, (render_pe_depth)
                dec     a
                jr      render_pe_entry_a
render_pe_entry_second:
                ld      a, (render_pe_depth)
                sub     2
render_pe_entry_a:
                ld      l, a
                ld      h, 0
                ld      d, h
                ld      e, l                         ; DE = index
                add     hl, hl
                add     hl, hl                       ; 4*index
                add     hl, de                       ; 5*index
                ld      de, render_pe_stack
                add     hl, de
                ret

; render_pe_push_frag_atom / _nonatom — push {frag_start, len, atom}; depth++.
render_pe_push_frag_atom:
                ld      a, 1
                jr      render_pe_push_frag
render_pe_push_frag_nonatom:
                xor     a
render_pe_push_frag:
                ld      (render_pe_atom_tmp), a
                ld      hl, (render_pe_next)
                ld      de, (render_pe_frag_start)
                or      a
                sbc     hl, de
                ld      (render_pe_len_tmp), hl       ; len = next - start
                call    render_pe_entry_at_depth
                ld      de, (render_pe_frag_start)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      de, (render_pe_len_tmp)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      a, (render_pe_atom_tmp)
                ld      (hl), a
                ld      a, (render_pe_depth)
                inc     a
                ld      (render_pe_depth), a
                ret

; render_pe_put_opsym — opSym (emit.go:668-690): append the operator token.
render_pe_put_opsym:
                ld      a, (render_cur_op)
                cp      EXPR_OP_ADD
                jr      z, render_ops_add
                cp      EXPR_OP_SUB
                jr      z, render_ops_sub
                cp      EXPR_OP_MUL
                jr      z, render_ops_mul
                cp      EXPR_OP_DIV
                jr      z, render_ops_div
                cp      EXPR_OP_AND
                jr      z, render_ops_and
                cp      EXPR_OP_OR
                jr      z, render_ops_or
                cp      EXPR_OP_XOR
                jr      z, render_ops_xor
                cp      EXPR_OP_SHL
                jr      z, render_ops_shl
                cp      EXPR_OP_SHR
                jr      z, render_ops_shr
                ret
render_ops_add:
                ld      a, "+"
                jp      render_pe_putc
render_ops_sub:
                ld      a, "-"
                jp      render_pe_putc
render_ops_mul:
                ld      a, "*"
                jp      render_pe_putc
render_ops_div:
                ld      a, "/"
                jp      render_pe_putc
render_ops_and:
                ld      a, "&"
                jp      render_pe_putc
render_ops_or:
                ld      a, "|"
                jp      render_pe_putc
render_ops_xor:
                ld      a, "^"
                jp      render_pe_putc
render_ops_shl:
                ld      a, "<"
                call    render_pe_putc
                ld      a, "<"
                jp      render_pe_putc
render_ops_shr:
                ld      a, ">"
                call    render_pe_putc
                ld      a, ">"
                jp      render_pe_putc

; render_pe_put_relname — relName (emit.go:692-714): append the reloc keyword.
render_pe_put_relname:
                ld      a, (render_cur_op)
                cp      EXPR_REL_LO12
                jr      z, render_rn_lo12
                cp      EXPR_REL_HI12
                jr      z, render_rn_hi12
                cp      EXPR_REL_ABS_G0
                jr      z, render_rn_g0
                cp      EXPR_REL_ABS_G0NC
                jr      z, render_rn_g0nc
                cp      EXPR_REL_ABS_G1
                jr      z, render_rn_g1
                cp      EXPR_REL_ABS_G1NC
                jr      z, render_rn_g1nc
                cp      EXPR_REL_ABS_G2
                jr      z, render_rn_g2
                cp      EXPR_REL_ABS_G2NC
                jr      z, render_rn_g2nc
                cp      EXPR_REL_ABS_G3
                jr      z, render_rn_g3
                ret
render_rn_lo12:
                ld      hl, render_str_lo12
                jp      render_pe_puts
render_rn_hi12:
                ld      hl, render_str_hi12
                jp      render_pe_puts
render_rn_g0:
                ld      hl, render_str_absg0
                jp      render_pe_puts
render_rn_g0nc:
                ld      hl, render_str_absg0nc
                jp      render_pe_puts
render_rn_g1:
                ld      hl, render_str_absg1
                jp      render_pe_puts
render_rn_g1nc:
                ld      hl, render_str_absg1nc
                jp      render_pe_puts
render_rn_g2:
                ld      hl, render_str_absg2
                jp      render_pe_puts
render_rn_g2nc:
                ld      hl, render_str_absg2nc
                jp      render_pe_puts
render_rn_g3:
                ld      hl, render_str_absg3
                jp      render_pe_puts


; -----------------------------------------------------------------------
; render_eval_const — format.EvalConst (expr.go:223-274).  Fold a bytecode
; stream to a 32-bit signed constant.  Any PUSH_SYM/LOCAL/PC or REL_* opcode
; (or a deferred MUL/DIV, or a malformed stack) makes it non-constant.
;
; Input:  HL = expr ptr, BC = expr len.
; Output: Z=1 → constant, render_num32 = value; Z=0 → not constant.
; -----------------------------------------------------------------------
render_eval_const:
                call    render_expr_init
                xor     a
                ld      (render_ev_depth), a
render_ev_loop:
                call    render_expr_atend
                jp      z, render_ev_end
                call    render_expr_next             ; A = op, render_cur_op set
                cp      EXPR_PUSH_IMM8
                jp      z, render_ev_imm8
                cp      EXPR_PUSH_IMM16
                jp      z, render_ev_imm16
                cp      EXPR_PUSH_IMM32
                jp      z, render_ev_imm32
                cp      EXPR_PUSH_IMM64
                jp      z, render_ev_imm64
                cp      EXPR_OP_ADD
                jp      z, render_ev_binary
                cp      EXPR_OP_SUB
                jp      z, render_ev_binary
                cp      EXPR_OP_AND
                jp      z, render_ev_binary
                cp      EXPR_OP_OR
                jp      z, render_ev_binary
                cp      EXPR_OP_XOR
                jp      z, render_ev_binary
                cp      EXPR_OP_SHL
                jp      z, render_ev_binary
                cp      EXPR_OP_SHR
                jp      z, render_ev_binary
                cp      EXPR_OP_NEG
                jp      z, render_ev_unary_neg
                cp      EXPR_OP_NOT
                jp      z, render_ev_unary_not
                jp      render_ev_notconst           ; sym/local/pc/rel/mul/div
render_ev_imm8:
                ld      hl, (render_expr_operand)
                ld      a, (hl)
                call    render_sx8_to_num32
                call    render_ev_push_num32
                jp      render_ev_loop
render_ev_imm16:
                call    render_sx16_to_num32
                call    render_ev_push_num32
                jp      render_ev_loop
render_ev_imm32:
                call    render_sx32_to_num32
                call    render_ev_push_num32
                jp      render_ev_loop
render_ev_imm64:
                call    render_sx64_to_num32
                call    render_ev_push_num32
                jp      render_ev_loop
render_ev_binary:
                call    render_ev_do_binary
                jp      render_ev_loop
render_ev_unary_neg:
                call    render_ev_pop_num32
                call    render_neg_num32
                call    render_ev_push_num32
                jp      render_ev_loop
render_ev_unary_not:
                call    render_ev_pop_num32
                ld      hl, render_num32
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                ld      a, (hl)
                cpl
                ld      (hl), a
                call    render_ev_push_num32
                jp      render_ev_loop
render_ev_end:
                ld      a, (render_ev_depth)
                cp      1
                jr      nz, render_ev_notconst
                call    render_ev_pop_num32
                xor     a                            ; Z=1 constant
                ret
render_ev_notconst:
                ld      a, 1
                or      a                            ; Z=0 not constant
                ret

; render_ev_do_binary — pop b then a, compute (a op b) → render_num32, push.
render_ev_do_binary:
                call    render_ev_pop_num32
                ld      hl, render_num32
                ld      de, render_ev_b
                ld      bc, 4
                ldir
                call    render_ev_pop_num32
                ld      hl, render_num32
                ld      de, render_ev_a
                ld      bc, 4
                ldir
                ld      a, (render_cur_op)
                cp      EXPR_OP_ADD
                jp      z, render_ev_op_add
                cp      EXPR_OP_SUB
                jp      z, render_ev_op_sub
                cp      EXPR_OP_AND
                jp      z, render_ev_op_and
                cp      EXPR_OP_OR
                jp      z, render_ev_op_or
                cp      EXPR_OP_XOR
                jp      z, render_ev_op_xor
                cp      EXPR_OP_SHL
                jp      z, render_ev_op_shl
                cp      EXPR_OP_SHR
                jp      z, render_ev_op_shr
                jp      fail
render_ev_op_add:
                ld      hl, (render_ev_a)
                ld      de, (render_ev_b)
                add     hl, de
                ld      (render_num32), hl
                ld      hl, (render_ev_a + 2)
                ld      de, (render_ev_b + 2)
                adc     hl, de
                ld      (render_num32 + 2), hl
                jp      render_ev_push_num32
render_ev_op_sub:
                ld      hl, (render_ev_a)
                ld      de, (render_ev_b)
                or      a
                sbc     hl, de
                ld      (render_num32), hl
                ld      hl, (render_ev_a + 2)
                ld      de, (render_ev_b + 2)
                sbc     hl, de
                ld      (render_num32 + 2), hl
                jp      render_ev_push_num32
render_ev_op_and:
                ld      a, (render_ev_a + 0)
                ld      hl, render_ev_b + 0
                and     (hl)
                ld      (render_num32 + 0), a
                ld      a, (render_ev_a + 1)
                ld      hl, render_ev_b + 1
                and     (hl)
                ld      (render_num32 + 1), a
                ld      a, (render_ev_a + 2)
                ld      hl, render_ev_b + 2
                and     (hl)
                ld      (render_num32 + 2), a
                ld      a, (render_ev_a + 3)
                ld      hl, render_ev_b + 3
                and     (hl)
                ld      (render_num32 + 3), a
                jp      render_ev_push_num32
render_ev_op_or:
                ld      a, (render_ev_a + 0)
                ld      hl, render_ev_b + 0
                or      (hl)
                ld      (render_num32 + 0), a
                ld      a, (render_ev_a + 1)
                ld      hl, render_ev_b + 1
                or      (hl)
                ld      (render_num32 + 1), a
                ld      a, (render_ev_a + 2)
                ld      hl, render_ev_b + 2
                or      (hl)
                ld      (render_num32 + 2), a
                ld      a, (render_ev_a + 3)
                ld      hl, render_ev_b + 3
                or      (hl)
                ld      (render_num32 + 3), a
                jp      render_ev_push_num32
render_ev_op_xor:
                ld      a, (render_ev_a + 0)
                ld      hl, render_ev_b + 0
                xor     (hl)
                ld      (render_num32 + 0), a
                ld      a, (render_ev_a + 1)
                ld      hl, render_ev_b + 1
                xor     (hl)
                ld      (render_num32 + 1), a
                ld      a, (render_ev_a + 2)
                ld      hl, render_ev_b + 2
                xor     (hl)
                ld      (render_num32 + 2), a
                ld      a, (render_ev_a + 3)
                ld      hl, render_ev_b + 3
                xor     (hl)
                ld      (render_num32 + 3), a
                jp      render_ev_push_num32
render_ev_op_shl:
; render_num32 = a; shift left by b (b>=32 or any high byte → 0).
                ld      hl, render_ev_a
                ld      de, render_num32
                ld      bc, 4
                ldir
                ld      a, (render_ev_b + 1)
                ld      hl, render_ev_b + 2
                or      (hl)
                inc     hl
                or      (hl)
                jr      nz, render_ev_shl_zero
                ld      a, (render_ev_b)
                cp      32
                jr      nc, render_ev_shl_zero
                or      a
                jp      z, render_ev_push_num32
                ld      b, a
render_ev_shl_loop:
                call    render_num32_shl1
                djnz    render_ev_shl_loop
                jp      render_ev_push_num32
render_ev_shl_zero:
                ld      hl, 0
                ld      (render_num32), hl
                ld      (render_num32 + 2), hl
                jp      render_ev_push_num32
render_ev_op_shr:
; render_num32 = a; arithmetic shift right by b (capped at 32 → sign fill).
                ld      hl, render_ev_a
                ld      de, render_num32
                ld      bc, 4
                ldir
                ld      a, (render_ev_b + 1)
                ld      hl, render_ev_b + 2
                or      (hl)
                inc     hl
                or      (hl)
                jr      nz, render_ev_shr_cap
                ld      a, (render_ev_b)
                cp      33
                jr      c, render_ev_shr_have
render_ev_shr_cap:
                ld      a, 32
render_ev_shr_have:
                or      a
                jp      z, render_ev_push_num32
                ld      b, a
render_ev_shr_loop:
                call    render_num32_sar1
                djnz    render_ev_shr_loop
                jp      render_ev_push_num32

; render_ev_push_num32 / _pop_num32 — 32-bit value stack.
render_ev_push_num32:
                ld      a, (render_ev_depth)
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl                       ; depth * 4
                ld      de, render_ev_stack
                add     hl, de
                ex      de, hl
                ld      hl, render_num32
                ld      bc, 4
                ldir
                ld      a, (render_ev_depth)
                inc     a
                ld      (render_ev_depth), a
                ret
render_ev_pop_num32:
                ld      a, (render_ev_depth)
                dec     a
                ld      (render_ev_depth), a
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl
                ld      de, render_ev_stack
                add     hl, de
                ld      de, render_num32
                ld      bc, 4
                ldir
                ret


; -----------------------------------------------------------------------
; 32-bit signed helpers used by both the const printer and PC sizing.
; -----------------------------------------------------------------------

; render_sx8_to_num32 — A (int8) → render_num32 sign-extended.
render_sx8_to_num32:
                ld      (render_num32), a
                ld      b, 0
                bit     7, a
                jr      z, render_sx8_p
                ld      b, &FF
render_sx8_p:
                ld      a, b
                ld      (render_num32 + 1), a
                ld      (render_num32 + 2), a
                ld      (render_num32 + 3), a
                ret

; render_sx16_to_num32 — (render_expr_operand) int16 → render_num32.
render_sx16_to_num32:
                ld      hl, (render_expr_operand)
                ld      a, (hl)
                ld      (render_num32), a
                inc     hl
                ld      a, (hl)
                ld      (render_num32 + 1), a
                ld      b, 0
                bit     7, a
                jr      z, render_sx16_p
                ld      b, &FF
render_sx16_p:
                ld      a, b
                ld      (render_num32 + 2), a
                ld      (render_num32 + 3), a
                ret

; render_sx32_to_num32 — (render_expr_operand) int32 → render_num32.
render_sx32_to_num32:
                ld      hl, (render_expr_operand)
                ld      de, render_num32
                ld      bc, 4
                ldir
                ret

; render_sx64_to_num32 — (render_expr_operand) low 32 bits → render_num32.
; Narrows Go's int64 push to 32 bits (see slice header note).
render_sx64_to_num32:
                ld      hl, (render_expr_operand)
                ld      de, render_num32
                ld      bc, 4
                ldir
                ret

; render_neg_num32 — render_num32 = -render_num32 (two's complement).
render_neg_num32:
                ld      hl, render_num32
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                ld      a, (hl)
                cpl
                ld      (hl), a
                ld      hl, (render_num32)
                inc     hl
                ld      (render_num32), hl
                ld      a, h
                or      l
                ret     nz
                ld      hl, (render_num32 + 2)
                inc     hl
                ld      (render_num32 + 2), hl
                ret

; render_num32_shl1 — render_num32 <<= 1.
render_num32_shl1:
                ld      hl, (render_num32)
                add     hl, hl
                ld      (render_num32), hl
                ld      hl, (render_num32 + 2)
                adc     hl, hl
                ld      (render_num32 + 2), hl
                ret

; render_num32_sar1 — render_num32 >>= 1 (arithmetic).
render_num32_sar1:
                ld      hl, (render_num32 + 2)
                sra     h
                rr      l
                ld      (render_num32 + 2), hl
                ld      hl, (render_num32)
                rr      h
                rr      l
                ld      (render_num32), hl
                ret

; render_num32_is_zero — Z=1 iff render_num32 == 0.
render_num32_is_zero:
                ld      a, (render_num32)
                ld      hl, render_num32 + 1
                or      (hl)
                inc     hl
                or      (hl)
                inc     hl
                or      (hl)
                ret

; render_u32_div10 — render_num32 /= 10; A = remainder (base-256 long div).
render_u32_div10:
                ld      c, 0                         ; running remainder
                ld      b, 4
                ld      hl, render_num32 + 3         ; MSB first
render_div10_byteloop:
                ld      d, c
                ld      e, (hl)                      ; DE = rem*256 + byte
                ld      c, 0                         ; quotient byte
render_div10_sub:
                ld      a, d
                or      a
                jr      nz, render_div10_do
                ld      a, e
                cp      10
                jr      c, render_div10_qdone
render_div10_do:
                ld      a, e
                sub     10
                ld      e, a
                jr      nc, render_div10_nb
                dec     d
render_div10_nb:
                inc     c
                jr      render_div10_sub
render_div10_qdone:
                ld      (hl), c                      ; store quotient byte
                ld      c, e                         ; remainder → next lower byte
                dec     hl
                djnz    render_div10_byteloop
                ld      a, c
                ret


; -----------------------------------------------------------------------
; render_emit_dec_s32 — append render_num32 as signed decimal via render_sink.
; -----------------------------------------------------------------------
render_emit_dec_s32:
                ld      a, (render_num32 + 3)
                bit     7, a
                jr      z, render_dec_pos
                ld      a, "-"
                call    render_sink
                call    render_neg_num32
render_dec_pos:
                ld      hl, render_decbuf
                ld      (render_decptr), hl
render_dec_digloop:
                call    render_u32_div10             ; A = remainder digit
                add     a, "0"
                ld      hl, (render_decptr)
                ld      (hl), a
                inc     hl
                ld      (render_decptr), hl
                call    render_num32_is_zero
                jr      nz, render_dec_digloop
render_dec_emit_loop:
                ld      hl, (render_decptr)
                ld      de, render_decbuf
                ld      a, h
                cp      d
                jr      nz, render_dec_emit_go
                ld      a, l
                cp      e
                ret     z                            ; reached the buffer base
render_dec_emit_go:
                dec     hl
                ld      a, (hl)
                ld      (render_decptr), hl
                call    render_sink
                jr      render_dec_emit_loop


; -----------------------------------------------------------------------
; render_emit_hashx_s32 — append render_num32 as Go's %#x via render_sink.
; -----------------------------------------------------------------------
render_emit_hashx_s32:
                ld      a, (render_num32 + 3)
                bit     7, a
                jr      z, render_hx_pos
                ld      a, "-"
                call    render_sink
                call    render_neg_num32
render_hx_pos:
                ld      a, "0"
                call    render_sink
                ld      a, "x"
                call    render_sink
                xor     a
                ld      (render_hexseen), a
                ld      b, 4
                ld      hl, render_num32 + 3
render_hx_byteloop:
                ld      c, (hl)
                push    hl
                push    bc
                ld      a, c
                rrca
                rrca
                rrca
                rrca
                and     &0F
                call    render_emit_hexnib_sink
                ld      a, c
                and     &0F
                call    render_emit_hexnib_sink
                pop     bc
                pop     hl
                dec     hl
                djnz    render_hx_byteloop
                ld      a, (render_hexseen)
                or      a
                ret     nz
                ld      a, "0"
                jp      render_sink
render_emit_hexnib_sink:
                or      a
                jr      nz, render_hns_emit
                ld      a, (render_hexseen)
                or      a
                ret     z                            ; leading zero → skip
                xor     a
render_hns_emit:
                push    af
                ld      a, 1
                ld      (render_hexseen), a
                pop     af
                cp      10
                jr      c, render_hns_dig
                add     a, "a" - 10
                jp      render_sink
render_hns_dig:
                add     a, "0"
                jp      render_sink

; render_sink — indirect byte sink (render_out_append or render_pe_putc).
render_sink:
                ld      hl, (render_sink_addr)
                jp      (hl)


; -----------------------------------------------------------------------
; render_emit_escaped — writeEscapedString (emit.go:716-737) over
; render_op_data / render_op_len, writing to render_out.
; -----------------------------------------------------------------------
render_emit_escaped:
                ld      hl, (render_op_data)
                ld      bc, (render_op_len)
render_esc_loop:
                ld      a, b
                or      c
                ret     z
                ld      a, (hl)
                push    hl
                push    bc
                call    render_esc_byte
                pop     bc
                pop     hl
                inc     hl
                dec     bc
                jr      render_esc_loop
render_esc_byte:
                cp      92                           ; '\'
                jr      z, render_esc_backslash
                cp      34                           ; '"'
                jr      z, render_esc_quote
                cp      10                           ; '\n'
                jr      z, render_esc_nl
                cp      9                            ; '\t'
                jr      z, render_esc_tab
                cp      0
                jr      z, render_esc_zero
                cp      &20
                jr      c, render_esc_hex
                cp      &7F
                jr      nc, render_esc_hex
                jp      render_out_append            ; printable → raw
render_esc_backslash:
                ld      a, 92
                call    render_out_append
                ld      a, 92
                jp      render_out_append
render_esc_quote:
                ld      a, 92
                call    render_out_append
                ld      a, 34
                jp      render_out_append
render_esc_nl:
                ld      a, 92
                call    render_out_append
                ld      a, "n"
                jp      render_out_append
render_esc_tab:
                ld      a, 92
                call    render_out_append
                ld      a, "t"
                jp      render_out_append
render_esc_zero:
                ld      a, 92
                call    render_out_append
                ld      a, "0"
                jp      render_out_append
render_esc_hex:
                ld      c, a
                ld      a, 92
                call    render_out_append
                ld      a, "x"
                call    render_out_append
                ld      a, c
                rrca
                rrca
                rrca
                rrca
                and     &0F
                call    render_out_hexnib_raw
                ld      a, c
                and     &0F
                jp      render_out_hexnib_raw
render_out_hexnib_raw:
                cp      10
                jr      c, render_ohr_dig
                add     a, "a" - 10
                jp      render_out_append
render_ohr_dig:
                add     a, "0"
                jp      render_out_append


; -----------------------------------------------------------------------
; render_out_put_bytes — append BC bytes from HL to render_out.
; -----------------------------------------------------------------------
render_out_put_bytes:
                ld      a, b
                or      c
                ret     z
render_opb_loop:
                ld      a, (hl)
                push    hl
                push    bc
                call    render_out_append
                pop     bc
                pop     hl
                inc     hl
                dec     bc
                ld      a, b
                or      c
                jr      nz, render_opb_loop
                ret


; -----------------------------------------------------------------------
; Expression-bytecode cursor (a plain byte walk over the staged operand).
; -----------------------------------------------------------------------
; render_expr_init — cursor = HL, end = HL + BC.
render_expr_init:
                ld      (render_expr_cur), hl
                add     hl, bc
                ld      (render_expr_end), hl
                ret

; render_expr_atend — Z=1 iff cursor >= end.
render_expr_atend:
                ld      hl, (render_expr_cur)
                ld      de, (render_expr_end)
                or      a
                sbc     hl, de
                jr      c, render_expr_notend        ; cur < end
                xor     a                            ; Z=1 at/past end
                ret
render_expr_notend:
                ld      a, 1
                or      a                            ; Z=0
                ret

; render_expr_next — read the opcode at the cursor, advance past its inline
; operand.  A = opcode (also render_cur_op); render_expr_operand = operand ptr.
render_expr_next:
                ld      hl, (render_expr_cur)
                ld      a, (hl)
                ld      (render_cur_op), a
                inc     hl
                ld      (render_expr_operand), hl
                call    render_operand_width         ; A = inline-operand width
                ld      e, a
                ld      d, 0
                ld      hl, (render_expr_operand)
                add     hl, de
                ld      (render_expr_cur), hl
                ld      a, (render_cur_op)
                ret

; render_operand_width — operandWidth (expr.go:191-217) for render_cur_op.
render_operand_width:
                ld      a, (render_cur_op)
                cp      EXPR_PUSH_IMM8
                jr      z, render_ow_1
                cp      EXPR_PUSH_IMM16
                jr      z, render_ow_2
                cp      EXPR_PUSH_IMM32
                jr      z, render_ow_4
                cp      EXPR_PUSH_IMM64
                jr      z, render_ow_8
                cp      EXPR_PUSH_SYM
                jr      z, render_ow_2
                cp      EXPR_PUSH_LOCAL
                jr      z, render_ow_2
                xor     a
                ret
render_ow_1:
                ld      a, 1
                ret
render_ow_2:
                ld      a, 2
                ret
render_ow_4:
                ld      a, 4
                ret
render_ow_8:
                ld      a, 8
                ret


; -----------------------------------------------------------------------
; render_dir_byte_size — directiveByteSize (pc.go:142-206) → render_num32.
; -----------------------------------------------------------------------
render_dir_byte_size:
                ld      a, (render_dir_id)
                cp      DIR_BYTE
                jp      z, render_dbs_x1
                cp      DIR_SHORT
                jp      z, render_dbs_x2
                cp      DIR_HWORD
                jp      z, render_dbs_x2
                cp      DIR_WORD
                jp      z, render_dbs_x4
                cp      DIR_QUAD
                jp      z, render_dbs_x8
                cp      DIR_ASCII
                jp      z, render_dbs_ascii
                cp      DIR_ASCIZ
                jp      z, render_dbs_asciz
                cp      DIR_SKIP
                jp      z, render_dbs_count
                cp      DIR_SPACE
                jp      z, render_dbs_count
                cp      DIR_INST
                jp      z, render_dbs_4
                cp      DIR_ALIGN
                jp      z, render_dbs_align
                cp      DIR_BALIGN
                jp      z, render_dbs_balign
render_dbs_zero:
                ld      hl, 0
                ld      (render_num32), hl
                ld      (render_num32 + 2), hl
                ret
render_dbs_x1:
                jp      render_dbs_set_opcount
render_dbs_x2:
                call    render_dbs_set_opcount
                jp      render_num32_shl1
render_dbs_x4:
                call    render_dbs_set_opcount
                call    render_num32_shl1
                jp      render_num32_shl1
render_dbs_x8:
                call    render_dbs_set_opcount
                call    render_num32_shl1
                call    render_num32_shl1
                jp      render_num32_shl1
render_dbs_ascii:
                jp      render_dir_op0_len
render_dbs_asciz:
                call    render_dir_op0_len
                ld      hl, (render_num32)
                inc     hl
                ld      (render_num32), hl
                ld      a, h
                or      l
                ret     nz
                ld      hl, (render_num32 + 2)
                inc     hl
                ld      (render_num32 + 2), hl
                ret
render_dbs_count:
                jp      render_dir_eval_op0
render_dbs_4:
                ld      hl, 4
                ld      (render_num32), hl
                ld      hl, 0
                ld      (render_num32 + 2), hl
                ret
render_dbs_align:
                call    render_dir_eval_op0          ; render_num32 = N, Z=const?
                jp      nz, render_dbs_zero
                ld      a, (render_num32 + 3)
                bit     7, a
                jp      nz, render_dbs_zero          ; N < 0 → 0
                call    render_num32_is_zero
                jp      z, render_dbs_zero           ; N == 0 → 0
                ld      a, (render_num32)
                ld      (render_align_n), a
                call    render_compute_align_pow2    ; render_align = 1 << N
                jp      render_align_pad
render_dbs_balign:
                call    render_dir_eval_op0          ; render_num32 = align
                jp      nz, render_dbs_zero
                ld      hl, render_num32
                ld      de, render_align
                ld      bc, 4
                ldir
                ld      a, (render_align + 1)
                ld      hl, render_align + 2
                or      (hl)
                inc     hl
                or      (hl)
                jr      nz, render_dbs_balign_go
                ld      a, (render_align)
                cp      2
                jp      c, render_dbs_zero           ; align <= 1 → 0
render_dbs_balign_go:
                jp      render_align_pad

; render_dbs_set_opcount — render_num32 = op_count (zero-extended).
render_dbs_set_opcount:
                ld      a, (render_dir_opcount)
                ld      (render_num32), a
                xor     a
                ld      (render_num32 + 1), a
                ld      (render_num32 + 2), a
                ld      (render_num32 + 3), a
                ret

; render_dir_op0_len — render_num32 = length of operand-0's string.
render_dir_op0_len:
                ld      hl, (render_dir_ops_ptr)
                inc     hl                           ; skip kind
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                      ; DE = string length
                ld      (render_num32), de
                ld      hl, 0
                ld      (render_num32 + 2), hl
                ret

; render_dir_eval_op0 — EvalConst on operand-0's expression → render_num32.
; Z reflects const-ness.
render_dir_eval_op0:
                ld      hl, (render_dir_ops_ptr)
                inc     hl                           ; skip kind
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                           ; DE = expr len, HL = expr ptr
                ld      b, d
                ld      c, e
                jp      render_eval_const

; render_compute_align_pow2 — render_align = 1 << render_align_n.
render_compute_align_pow2:
                ld      hl, 1
                ld      (render_align), hl
                ld      hl, 0
                ld      (render_align + 2), hl
                ld      a, (render_align_n)
                or      a
                ret     z
                ld      b, a
render_cap_loop:
                ld      hl, (render_align)
                add     hl, hl
                ld      (render_align), hl
                ld      hl, (render_align + 2)
                adc     hl, hl
                ld      (render_align + 2), hl
                djnz    render_cap_loop
                ret

; render_align_pad — alignPad (pc.go:211-216): render_num32 = pad bytes to
; align (origin+pc) up to the render_align boundary.
render_align_pad:
                ld      hl, (render_origin_vma)
                ld      de, (render_pc)
                add     hl, de
                ld      (render_align_addr), hl
                ld      hl, (render_origin_vma + 2)
                ld      de, (render_pc + 2)
                adc     hl, de
                ld      (render_align_addr + 2), hl
                call    render_u32_mod               ; render_align_rem = addr % align
                ld      a, (render_align_rem)
                ld      hl, render_align_rem + 1
                or      (hl)
                inc     hl
                or      (hl)
                inc     hl
                or      (hl)
                jr      nz, render_align_pad_sub
                ld      hl, 0
                ld      (render_num32), hl
                ld      (render_num32 + 2), hl
                ret
render_align_pad_sub:
                ld      hl, (render_align)
                ld      de, (render_align_rem)
                or      a
                sbc     hl, de
                ld      (render_num32), hl
                ld      hl, (render_align + 2)
                ld      de, (render_align_rem + 2)
                sbc     hl, de
                ld      (render_num32 + 2), hl
                ret

; render_u32_mod — render_align_rem = render_align_addr % render_align (32-bit
; bitwise long division; consumes render_align_addr).
render_u32_mod:
                ld      hl, 0
                ld      (render_align_rem), hl
                ld      (render_align_rem + 2), hl
                ld      b, 32
render_mod_loop:
                ld      hl, (render_align_addr)
                add     hl, hl
                ld      (render_align_addr), hl
                ld      hl, (render_align_addr + 2)
                adc     hl, hl
                ld      (render_align_addr + 2), hl
                ld      a, 0
                adc     a, 0
                ld      c, a                         ; C = old bit31 of dividend
                ld      hl, (render_align_rem)
                add     hl, hl
                ld      (render_align_rem), hl
                ld      hl, (render_align_rem + 2)
                adc     hl, hl
                ld      (render_align_rem + 2), hl
                ld      a, c
                or      a
                jr      z, render_mod_nobit
                ld      hl, (render_align_rem)
                set     0, l
                ld      (render_align_rem), hl
render_mod_nobit:
                ld      hl, (render_align_rem + 2)
                ld      de, (render_align + 2)
                or      a
                sbc     hl, de
                jr      c, render_mod_next           ; rem_hi < div_hi → rem < div
                jr      nz, render_mod_sub           ; rem_hi > div_hi → rem >= div
                ld      hl, (render_align_rem)
                ld      de, (render_align)
                or      a
                sbc     hl, de
                jr      c, render_mod_next
render_mod_sub:
                ld      hl, (render_align_rem)
                ld      de, (render_align)
                or      a
                sbc     hl, de
                ld      (render_align_rem), hl
                ld      hl, (render_align_rem + 2)
                ld      de, (render_align + 2)
                sbc     hl, de
                ld      (render_align_rem + 2), hl
render_mod_next:
                djnz    render_mod_loop
                ret


; render_pc_add_num32 — render_pc += render_num32 (32-bit).
render_pc_add_num32:
                ld      hl, (render_pc)
                ld      de, (render_num32)
                add     hl, de
                ld      (render_pc), hl
                ld      hl, (render_pc + 2)
                ld      de, (render_num32 + 2)
                adc     hl, de
                ld      (render_pc + 2), hl
                ret

; render_pc_is_zero / render_origin_is_zero — Z=1 iff the u32 is zero.
render_pc_is_zero:
                ld      a, (render_pc)
                ld      hl, render_pc + 1
                or      (hl)
                inc     hl
                or      (hl)
                inc     hl
                or      (hl)
                ret
render_origin_is_zero:
                ld      a, (render_origin_vma)
                ld      hl, render_origin_vma + 1
                or      (hl)
                inc     hl
                or      (hl)
                inc     hl
                or      (hl)
                ret


; -----------------------------------------------------------------------
; Directive-name table (22 entries, indexed by dir_id) + name strings for the
; directives not already spelled by the LIT_DATA numeric-data set above.
; -----------------------------------------------------------------------
render_dir_names:
                defw    render_dn_text          ; 0  .text
                defw    render_dn_data          ; 1  .data
                defw    render_str_byte         ; 2  .byte
                defw    render_str_short        ; 3  .short
                defw    render_str_word         ; 4  .word
                defw    render_str_quad         ; 5  .quad
                defw    render_dn_ascii         ; 6  .ascii
                defw    render_dn_asciz         ; 7  .asciz
                defw    render_dn_equ           ; 8  .equ
                defw    render_dn_set           ; 9  .set
                defw    render_dn_global        ; 10 .global
                defw    render_dn_balign        ; 11 .balign
                defw    render_dn_org           ; 12 .org
                defw    render_dn_skip          ; 13 .skip
                defw    render_dn_space         ; 14 .space
                defw    render_dn_inst          ; 15 .inst
                defw    render_dn_align         ; 16 .align
                defw    render_dn_ltorg         ; 17 .ltorg
                defw    render_dn_section       ; 18 .section
                defw    render_dn_arch          ; 19 .arch
                defw    render_dn_cpu           ; 20 .cpu
                defw    render_str_hword        ; 21 .hword

render_dn_text:         defm    ".text"
                        defb    0
render_dn_data:         defm    ".data"
                        defb    0
render_dn_ascii:        defm    ".ascii"
                        defb    0
render_dn_asciz:        defm    ".asciz"
                        defb    0
render_dn_equ:          defm    ".equ"
                        defb    0
render_dn_set:          defm    ".set"
                        defb    0
render_dn_global:       defm    ".global"
                        defb    0
render_dn_balign:       defm    ".balign"
                        defb    0
render_dn_org:          defm    ".org"
                        defb    0
render_dn_skip:         defm    ".skip"
                        defb    0
render_dn_space:        defm    ".space"
                        defb    0
render_dn_inst:         defm    ".inst"
                        defb    0
render_dn_align:        defm    ".align"
                        defb    0
render_dn_ltorg:        defm    ".ltorg"
                        defb    0
render_dn_section:      defm    ".section"
                        defb    0
render_dn_arch:         defm    ".arch"
                        defb    0
render_dn_cpu:          defm    ".cpu"
                        defb    0

; Reloc keywords (relName, emit.go:692-714; also the simpleSymRef :lo12:/:hi12:).
render_str_lo12:        defm    "lo12"
                        defb    0
render_str_hi12:        defm    "hi12"
                        defb    0
render_str_absg0:       defm    "abs_g0"
                        defb    0
render_str_absg0nc:     defm    "abs_g0_nc"
                        defb    0
render_str_absg1:       defm    "abs_g1"
                        defb    0
render_str_absg1nc:     defm    "abs_g1_nc"
                        defb    0
render_str_absg2:       defm    "abs_g2"
                        defb    0
render_str_absg2nc:     defm    "abs_g2_nc"
                        defb    0
render_str_absg3:       defm    "abs_g3"
                        defb    0


; -----------------------------------------------------------------------
; Render state + output buffer (section C — read by the driver's harness
; after render_run returns).
; -----------------------------------------------------------------------
RENDER_MAX_LABELS:      equ     64
RENDER_MAX_LOCALS:      equ     64

render_label_count:     defw    0       ; header label rows stored
render_label_cursor:    defw    0       ; flush cursor into render_labels
render_label_next:      defw    0       ; append cursor into render_labels
render_local_count:     defw    0
render_local_cursor:    defw    0
render_local_next:      defw    0
render_tmp_id:          defw    0       ; name_id scratch (store callback)
render_tmp_digit:       defb    0       ; digit scratch (store callback)
render_pc:              defb    0, 0, 0, 0      ; running offset from origin (LE)
render_origin_vma:      defb    0, 0, 0, 0      ; origin VMA (set by the leading .org)

render_labels:          defs    RENDER_MAX_LABELS * 6   ; [offset u32][name_id u16]
render_locals:          defs    RENDER_MAX_LOCALS * 5   ; [offset u32][digit u8]

render_prev_stmt:       defb    0       ; prevWasStatement flag (emit.go)
render_rir_src:         defw    0       ; INSN_RUN mode-0 element source cursor
render_rir_rem:         defw    0       ; INSN_RUN mode-0 remaining element bytes

; SLICE 6a — INSN_RUN mode-1 (overlay) element-walk state.
render_ovl_cursor:      defw    0       ; mode-1 element walk cursor (STAGING_BUF)
render_ovl_remaining:   defw    0       ; mode-1 element bytes remaining
render_ovl_base:        defb    0, 0, 0, 0      ; current element base word (LE)
render_ovl_patch_count: defb    0       ; current element patch count
render_ovl_slot:        defb    0       ; current patch FoldSlot id
render_ovl_expr_ptr:    defw    0       ; current patch expr ptr (STAGING_BUF)
render_ovl_expr_len:    defw    0       ; current patch expr length

render_ld_src:          defw    0       ; LIT_DATA payload ptr ([dir_id][data])
render_ld_len:          defw    0       ; LIT_DATA payload length (1 + nbytes)
render_ld_data:         defw    0       ; LIT_DATA data cursor
render_ld_rem_bytes:    defw    0       ; LIT_DATA data bytes remaining
render_ld_nameptr:      defw    0       ; directive name string ptr
render_ld_idx:          defb    0       ; element index (litDataPerLine wrap)
render_ld_width:        defb    0       ; element width in bytes (1/2/4/8)
render_ld_seen:         defb    0       ; hex leading-zero suppression flag

render_out_len:         defw    0       ; bytes written to render_out
render_out:             defs    512     ; rendered source text


; -----------------------------------------------------------------------
; SLICE 5 state — directive/operand/expr scratch (section C/D, HMPR-stable).
; -----------------------------------------------------------------------
RENDER_EV_MAX_DEPTH:    equ     16      ; const-eval value-stack depth
RENDER_PE_MAX_DEPTH:    equ     16      ; printExpr text-stack depth
RENDER_PE_ARENA_SIZE:   equ     512     ; printExpr fragment arena

render_num32:           defs    4       ; 32-bit signed accumulator / result

render_dir_id:          defb    0       ; current directive id
render_dir_opcount:     defb    0       ; operand count
render_dir_ops_ptr:     defw    0       ; operands region start (in STAGING_BUF)
render_dir_ops_len:     defw    0       ; operands region length

render_op_cur:          defw    0       ; operand-walk cursor
render_op_end:          defw    0       ; operand-walk end
render_op_first:        defb    0       ; first-operand flag (", " separator)
render_op_kind:         defb    0       ; current operand kind
render_op_data:         defw    0       ; current operand payload ptr
render_op_len:          defw    0       ; current operand payload length

render_expr_cur:        defw    0       ; expr-bytecode cursor
render_expr_end:        defw    0       ; expr-bytecode end
render_expr_operand:    defw    0       ; ptr to the current opcode's inline operand
render_cur_op:          defb    0       ; current expr opcode

render_ev_depth:        defb    0       ; const-eval value-stack depth
render_ev_stack:        defs    RENDER_EV_MAX_DEPTH * 4
render_ev_a:            defs    4       ; binary-op left operand
render_ev_b:            defs    4       ; binary-op right operand

render_align:           defs    4       ; alignment boundary
render_align_n:         defb    0       ; .align exponent (align = 1 << n)
render_align_addr:      defs    4       ; (origin+pc) dividend (consumed by mod)
render_align_rem:       defs    4       ; (origin+pc) mod align

render_decbuf:          defs    12      ; decimal digits (reverse order)
render_decptr:          defw    0       ; decimal-digit cursor
render_hexseen:         defb    0       ; %#x leading-zero suppression flag
render_sink_addr:       defw    render_out_append   ; active byte sink

render_ssr_id:          defw    0       ; simpleSymRef name_id / {digit,dir}
render_ssr_op2:         defb    0       ; simpleSymRef second opcode

render_pe_next:         defw    0       ; printExpr arena bump pointer
render_pe_depth:        defb    0       ; printExpr text-stack depth
render_pe_frag_start:   defw    0       ; current fragment start
render_pe_len_tmp:      defw    0       ; fragment length scratch
render_pe_atom_tmp:     defb    0       ; fragment atom-flag scratch
render_pe_stack:        defs    RENDER_PE_MAX_DEPTH * 5   ; {ptr u16, len u16, atom u8}
render_pe_arena:        defs    RENDER_PE_ARENA_SIZE
