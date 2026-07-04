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
; The INSN_RUN mode-0 (bare literal words) arm is wired, together with the
; editor-region name table + `.global` flags (tbn_render_editor.asm) and the
; header-table label/local flush placed by PC.  DIRECTIVE / LIT_DATA / the
; comment sidecar / mode-1 patch overlays are later slices — an unexpected
; record kind falls through to `fail`.
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
                jp      fail                     ; slice 1: only INSN_RUN is wired

render_walk_insn_run:
                call    render_insn_run
                jr      render_walk


; -----------------------------------------------------------------------
; render_insn_run — render a KindInsnRun record.  Mirrors emitInsnRun
; (overlay.go:36): one statement per 4-byte element.
;
; Payload: [mode u8][elements].  SLICE 1 handles mode 0 only (bare 4-byte
; little-endian assembled words, no patches); each element decodes as a
; literal.  mode 1 (base word + patch overlay) is a later slice.
;
; Input:  HL = payload ptr (STAGING_BUF), BC = payload length.
; -----------------------------------------------------------------------
render_insn_run:
                inc     hl                      ; skip the mode byte
                dec     bc                      ; BC = remaining element bytes

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

render_labels:          defs    RENDER_MAX_LABELS * 6   ; [offset u32][name_id u16]
render_locals:          defs    RENDER_MAX_LOCALS * 5   ; [offset u32][digit u8]

render_prev_stmt:       defb    0       ; prevWasStatement flag (emit.go)
render_rir_src:         defw    0       ; INSN_RUN element source cursor
render_rir_rem:         defw    0       ; INSN_RUN remaining element bytes

render_out_len:         defw    0       ; bytes written to render_out
render_out:             defs    256     ; rendered source text
