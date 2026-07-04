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
; SLICE 1 (i365a): only the INSN_RUN mode-0 (bare literal words) arm is wired.
; DIRECTIVE / LIT_DATA / the editor-region sidecar / mode-1 patch overlays are
; later slices — an unexpected kind falls through to `fail`.
;
; Output: canonical GNU-as text is appended byte-by-byte to render_out; its
; length lands in render_out_len (both section-C, HMPR-stable — the driver's
; harness reads them after render_run returns).


; -----------------------------------------------------------------------
; render_emit — top-level entry.  Mirrors render.EmitFile (emit.go:66).
;
; Resets the output buffer and the prevWasStatement flag, positions the
; reader at the first record (reader_init), walks the record stream, then
; closes a trailing open statement with a final newline.
;
; For the on-disk `.tbn` there are no globals / header definitions / sidecar
; rows (they live in the editor region the assembler never reads), so the
; PC-tracking and flush machinery of the Go authority is inert here and is
; not ported for slice 1.
;
; Input:  the reader cursor (IN_POS_*) points at the staged `.tbn`.
; Output: render_out / render_out_len populated.
; -----------------------------------------------------------------------
render_emit:
                ld      hl, 0
                ld      (render_out_len), hl
                xor     a
                ld      (render_prev_stmt), a

                call    reader_init
                call    render_walk

; Final: `if prevWasStatement { out.WriteByte('\n') }` (emit.go:235).
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
; Render state + output buffer (section C — read by the driver's harness
; after render_run returns).
; -----------------------------------------------------------------------
render_prev_stmt:       defb    0       ; prevWasStatement flag (emit.go)
render_rir_src:         defw    0       ; INSN_RUN element source cursor
render_rir_rem:         defw    0       ; INSN_RUN remaining element bytes

render_out_len:         defw    0       ; bytes written to render_out
render_out:             defs    256     ; rendered source text
