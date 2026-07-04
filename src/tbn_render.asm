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
; Output: canonical GNU-as text is streamed byte-by-byte out RENDER_SINK_PORT
; (render_out_append), which the harness captures.  The output (~417 KB for the
; release corpus) is never buffered resident; render_out_len keeps a 32-bit
; running byte count for diagnostics only.


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
; Output: the source text streamed out RENDER_SINK_PORT; render_out_len holds the
;         byte count.
; -----------------------------------------------------------------------
render_emit:
                ld      hl, 0
                ld      (render_out_len), hl
                ld      (render_out_len + 2), hl
                xor     a
                ld      (render_prev_stmt), a

                call    render_reset_state      ; header-def cursors + PC
                call    reader_init             ; seeds header tables via the
                                                ;   store callbacks, bounds IN_END
                call    render_read_editor      ; name table + .global flags
                call    render_sort_labels      ; order ties by name (newHeaderDefs)
                call    render_sort_locals      ; order ties by digit
                call    render_globals          ; emit "  .global NAME\n" lines
                call    render_walk

; Final: `flush()` then `if prevWasStatement { out.WriteByte('\n') }`
; (emit.go:226,235).  The trailing flush places an end-of-stream label and drains
; any end-of-stream comment / blank run.
                call    render_flush

; Safety: every sidecar row must have been placed by the PC walk (emit.go:231-234).
; With the streamed sidecar that means no rows remain undecoded AND no decoded
; row is still pending — a stranded row is a PC-accounting bug or corrupt sidecar,
; and a loud HALT here beats silently dropping a comment.
                ld      hl, (render_sc_remaining)
                ld      a, h
                or      l
                jp      nz, fail                ; undecoded rows remain
                ld      a, (render_sc_pending)
                or      a
                jp      nz, fail                ; a decoded row was never placed

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
; render driver does not include insn_run.asm.
FSID_BRANCH26:          equ     1
FSID_BRANCH19:          equ     2
FSID_BRANCH14:          equ     3
FSID_ADR:               equ     4
FSID_ADRP:              equ     5
FSID_ADDSUB_IMM12:      equ     6
FSID_MEM_IMM12:         equ     7
FSID_MEM_IMM9:          equ     8       ; deferred (absent from release corpus)
FSID_MOVK_IMM16:        equ     9
FSID_LOGICAL:           equ     10      ; deferred (absent from release corpus)
FSID_PAIR_IMM7:         equ     11
FSID_LITPOOL19:         equ     12
FSID_MOVZ_AUTO:         equ     13


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

; spliceOverlay dispatch (overlay.go:103-159).  The target-text families
; (branch26/19/14, adr, adrp) replace the LAST operand with the rendered target
; text (overlay.go:113-122 — replaceOperand(mnem, ops, lastIndex(ops), sym));
; the S6b immediate/memory families splice the patch immediate into the folded
; field (per-family surgery below).
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
; S6b immediate/memory slot families (overlay.go:105-158).
                cp      FSID_ADDSUB_IMM12
                jp      z, render_ovl_addsub
                cp      FSID_MEM_IMM12
                jp      z, render_ovl_mem_insert
                cp      FSID_MOVK_IMM16
                jp      z, render_ovl_movk
                cp      FSID_PAIR_IMM7
                jp      z, render_ovl_mem_insert
                cp      FSID_LITPOOL19
                jp      z, render_ovl_litpool
                cp      FSID_MOVZ_AUTO
                jp      z, render_ovl_movz
; Deferred: FoldMemImm9 (slot 8) and FoldLogical (slot 10) are absent from the
; release corpus, so they are unimplemented.  A full-release render hitting
; either fails loudly here (correct behaviour): FoldLogical additionally needs
; the decodeWithSentinel base-decode path (overlay.go:80-91,219-225), not yet
; ported.
                jp      fail                    ; unimplemented slots 8, 10

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


; =======================================================================
; SLICE 6b — the immediate / memory slot families of spliceOverlay
; (overlay.go:105-158): add/sub imm12, ldr/str/ldp/stp offset, explicit
; movz/movk imm16, ldr-literal, and mov auto-movz.  Each renders
; `mnem TAB <ops with the folded operand spliced in>` then tail-jumps to
; render_m1_after (pc += 4, next element), reusing the helpers renderImmText
; (render_ovl_emit_imm), replaceImmOperand (render_ovl_imm_replace) and
; insertMemOffset (render_ovl_mem_insert).
;
; Deferred (absent from the release corpus): FoldMemImm9 (slot 8) and
; FoldLogical (slot 10) — dispatched to `fail` in render_m1_overlay.
; =======================================================================

; -- FoldAddSubImm12 (slot 6): replaceImmOperand, decoded mnemonic ------------
; overlay.go:148-152.  The zeroed imm12 disassembles as `#0x0`; swap it for the
; patch immediate, preserving a sibling `lsl #12` shift operand.
render_ovl_addsub:
                ld      hl, DISASM_COMM_MNEM
                ld      (render_ovl_mnem_ptr), hl
                jp      render_ovl_imm_replace

; -- FoldMovkImm16 (slot 9): recover movz/movk, then replaceImmOperand --------
; overlay.go:135-146.  aarch64dec aliases a zeroed-imm movz/movk to `mov`, so
; recover the real mnemonic from the move-wide opc field — base bits 30:29:
; 0b11 → movk, else movz ((base>>29)&3 == 3) — then replace the zeroed
; immediate, keeping any explicit `lsl #N`.
render_ovl_movk:
                ld      a, (render_ovl_base + 3) ; base bits 31:24
                and     &60                     ; bits 6,5 = base bits 30,29
                cp      &60                     ; both set → (base>>29)&3 == 3
                ld      hl, render_str_movz
                jr      nz, romk_have
                ld      hl, render_str_movk
romk_have:
                ld      (render_ovl_mnem_ptr), hl
                ; fall through into render_ovl_imm_replace

; -- render_ovl_imm_replace — replaceImmOperand (overlay.go:193-202) ----------
; Emits <mnem> TAB + ops[:i] + <sym> + ops[i:], where [i,i+len) is the sole
; `#0x0`/`#0` operand token and sym is the patch expr in instruction-immediate
; context (renderPatchOperand default → renderImmText isDirective=false).  The
; mnemonic pointer is render_ovl_mnem_ptr (decoded for add/sub, recovered for
; movz/movk).
render_ovl_imm_replace:
                ld      hl, (render_ovl_mnem_ptr)
                call    render_copy_cstr
                ld      a, 9                    ; TAB
                call    render_out_append
                call    render_find_zero_imm    ; HL = token start, DE = token end
                ld      (render_ovl_suffix), de ; ops[i:] (from the token end)
; emit ops[:i] = DISASM_COMM_OPS .. token start.
                ld      de, DISASM_COMM_OPS
                or      a
                sbc     hl, de                  ; HL = prefix length
                ld      b, h
                ld      c, l
                ld      hl, DISASM_COMM_OPS
                call    render_out_put_bytes
; emit the spliced immediate (instruction context), then the suffix.
                xor     a                       ; isDirective = 0
                call    render_ovl_emit_imm
                ld      hl, (render_ovl_suffix)
                call    render_copy_cstr
                jp      render_m1_after

; -- FoldMemImm12 (7) / FoldPairImm7 (11): insertMemOffset (overlay.go:207-213)
; The zeroed offset disassembles as `[base]` (offset omitted); insert
; ", <sym>" before the closing `]`.  This is BRACKET-relative — the memory
; operand carries its own internal `", "`, so it is not a last-operand replace.
render_ovl_mem_insert:
                ld      hl, DISASM_COMM_MNEM
                call    render_copy_cstr
                ld      a, 9                    ; TAB
                call    render_out_append
; find the last `]` in DISASM_COMM_OPS → DE.
                ld      hl, DISASM_COMM_OPS
                ld      de, 0                   ; DE = last `]` ptr (0 = none)
romi_scan:
                ld      a, (hl)
                or      a
                jr      z, romi_scan_done
                cp      "]"
                jr      nz, romi_scan_next
                ld      d, h
                ld      e, l
romi_scan_next:
                inc     hl
                jr      romi_scan
romi_scan_done:
                ld      a, d
                or      e
                jp      z, fail                 ; no `]` → Go errors (overlay.go:209-211)
                ld      (render_ovl_suffix), de ; ops[i:] (from the `]`)
; emit ops[:i] = DISASM_COMM_OPS .. `]`.
                ex      de, hl                  ; HL = `]` ptr
                ld      de, DISASM_COMM_OPS
                or      a
                sbc     hl, de                  ; HL = prefix length
                ld      b, h
                ld      c, l
                ld      hl, DISASM_COMM_OPS
                call    render_out_put_bytes
; emit ", " then the spliced offset (instruction context), then the `]` tail.
                ld      a, ","
                call    render_out_append
                ld      a, " "
                call    render_out_append
                xor     a                       ; isDirective = 0
                call    render_ovl_emit_imm
                ld      hl, (render_ovl_suffix)
                call    render_copy_cstr
                jp      render_m1_after

; -- FoldLitpool19 (slot 12): ldr Rt, =expr (overlay.go:105-111) --------------
; Keep the first operand (Rt), discard the decoded literal target, and render
; `=<sym>` where sym is the patch expr in directive context (renderPatchOperand
; → renderImmText isDirective=true → no leading '#').
render_ovl_litpool:
                ld      hl, DISASM_COMM_MNEM
                call    render_copy_cstr
                ld      a, 9                    ; TAB
                call    render_out_append
                call    render_ovl_emit_first_op ; parts[0] = Rt
                ld      a, ","
                call    render_out_append
                ld      a, " "
                call    render_out_append
                ld      a, "="
                call    render_out_append
                ld      a, 1                    ; isDirective = 1 (=expr, no '#')
                call    render_ovl_emit_imm
                jp      render_m1_after

; -- FoldMovzAuto (slot 13): mov Rd, <sym> (overlay.go:124-133) ---------------
; The base decodes (aliased) as `mov Rd, #0`; render the original 2-operand
; `mov` form with the full-value expression (instruction context) — re-assembly
; re-derives the movz hw shift.  The mnemonic is the literal `mov`.
render_ovl_movz:
                ld      hl, render_str_mov
                call    render_copy_cstr
                ld      a, 9                    ; TAB
                call    render_out_append
                call    render_ovl_emit_first_op ; parts[0] = Rd
                ld      a, ","
                call    render_out_append
                ld      a, " "
                call    render_out_append
                xor     a                       ; isDirective = 0
                call    render_ovl_emit_imm
                jp      render_m1_after


; -----------------------------------------------------------------------
; render_ovl_emit_imm — renderImmText (overlay.go:246-252) →
; emitExprAsImmediateWithContext (emit.go:491-526): render the current patch
; expression (render_ovl_expr_ptr / render_ovl_expr_len) as an instruction or
; directive immediate to render_out.  A constant renders `#dec`/`#%#x`
; (instruction) or `dec`/`%#x` (directive, no '#'); a simple symbol reference
; renders its text; anything else as a parenthesised infix expression.
;
; Input:  A = isDirective flag (0 = instruction, 1 = directive/`=`).
; -----------------------------------------------------------------------
render_ovl_emit_imm:
                ld      (render_ovl_is_dir), a
                ld      hl, (render_ovl_expr_ptr)
                ld      bc, (render_ovl_expr_len)
                call    render_eval_const       ; Z=1 → const, render_num32 = v
                jr      nz, roei_notconst
                ld      hl, render_out_append
                ld      (render_sink_addr), hl
                ld      a, (render_ovl_is_dir)
                or      a
                jr      nz, roei_const_go       ; directive → no leading '#'
                ld      a, "#"
                call    render_out_append
roei_const_go:
                jp      render_emit_const_directive
roei_notconst:
                ld      hl, (render_ovl_expr_ptr)
                ld      bc, (render_ovl_expr_len)
                call    render_simple_symref    ; Z=1 → matched (name/:lo12:/Nf/Nb)
                ret     z
                ld      a, "("
                call    render_out_append
                ld      hl, (render_ovl_expr_ptr)
                ld      bc, (render_ovl_expr_len)
                call    render_print_expr
                ld      a, ")"
                jp      render_out_append


; -----------------------------------------------------------------------
; render_ovl_emit_first_op — emit parts[0] of DISASM_COMM_OPS: everything up to
; (not including) the first ", " separator, or the whole string if single.
; splitOps(ops)[0] (overlay.go:163-167) for the litpool/movz families, which
; keep only the leading (register) operand.  An empty ops string is a Go error
; (len(parts)==0) → fail.
; -----------------------------------------------------------------------
render_ovl_emit_first_op:
                ld      hl, DISASM_COMM_OPS
                ld      a, (hl)
                or      a
                jp      z, fail                 ; len(parts)==0 (overlay.go:108-110,130-132)
rofo_loop:
                ld      a, (hl)
                or      a
                ret     z
                cp      ","
                ret     z
                push    hl
                call    render_out_append
                pop     hl
                inc     hl
                jr      rofo_loop


; -----------------------------------------------------------------------
; render_find_zero_imm — locate the sole `#0x0`/`#0` operand token in
; DISASM_COMM_OPS (replaceImmOperand, overlay.go:193-201).  Exactly one field
; is zeroed in an overlay base word, so the zeroed immediate is unambiguous; a
; `lsl #N` shift is a distinct non-zero operand and is skipped.
;
; Output: HL = token start ptr, DE = token end ptr (start + 2 or 4).
;         A token that is never found is a Go error → fail.
; -----------------------------------------------------------------------
render_find_zero_imm:
                ld      hl, DISASM_COMM_OPS
rfzi_loop:
                ld      a, (hl)
                or      a
                jp      z, fail                 ; no zeroed immediate (overlay.go:201)
                ld      de, render_str_zerox    ; "#0x0"
                call    render_try_token
                jr      z, rfzi_found4
                ld      de, render_str_zero     ; "#0"
                call    render_try_token
                jr      z, rfzi_found2
                call    render_skip_part
                jr      rfzi_loop
rfzi_found4:
                ld      c, 4
                jr      rfzi_ret
rfzi_found2:
                ld      c, 2
rfzi_ret:
                ld      b, 0
                push    hl
                add     hl, bc
                ex      de, hl                  ; DE = token end (start + len)
                pop     hl                      ; HL = token start
                ret

; render_try_token — Z=1 iff the operand at HL equals the candidate cstr at DE
; followed by an operand boundary (',' or nul).  Preserves HL.
render_try_token:
                push    hl
rtt_loop:
                ld      a, (de)
                or      a
                jr      z, rtt_check_delim      ; candidate exhausted → check boundary
                cp      (hl)
                jr      nz, rtt_no
                inc     hl
                inc     de
                jr      rtt_loop
rtt_check_delim:
                ld      a, (hl)
                or      a
                jr      z, rtt_yes              ; end of ops → boundary
                cp      ","
                jr      z, rtt_yes              ; ", " separator → boundary
rtt_no:
                pop     hl
                ld      a, 1
                or      a                       ; Z=0 (no match)
                ret
rtt_yes:
                pop     hl
                xor     a                       ; Z=1 (match)
                ret

; render_skip_part — advance HL past the current operand to the next operand
; start: skip to the next ", " (comma+space) and step over it, or stop at nul.
render_skip_part:
rsp_loop:
                ld      a, (hl)
                or      a
                ret     z                       ; nul → leave HL (loop terminates)
                cp      ","
                jr      z, rsp_comma
                inc     hl
                jr      rsp_loop
rsp_comma:
                inc     hl                      ; past ','
                ld      a, (hl)
                cp      " "
                ret     nz                      ; not ", " (defensive) — leave HL
                inc     hl                      ; past ' '
                ret


; S6b mnemonic / token strings.
render_str_mov:         defm    "mov"
                        defb    0
render_str_movz:        defm    "movz"
                        defb    0
render_str_movk:        defm    "movk"
                        defb    0
render_str_zerox:       defm    "#0x0"
                        defb    0
render_str_zero:        defm    "#0"
                        defb    0


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
                ld      a, (render_num32 + 7)
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
; render_out_append — STREAM the byte in A to the harness output sink.
;
; The full-release render (~417 KB) never fits resident, so the output is not
; buffered: each byte is emitted to RENDER_SINK_PORT, which the Go harness
; captures (an attached IODevice accumulates every write to that port, exactly
; as the assembler harness captures the printer channel).  render_out_len keeps
; a 32-bit running byte count for diagnostics; the harness capture is the
; authority the test byte-compares against render.Emit.
;
; An OUT touches no memory and does not disturb the LMPR/HMPR windows, so the
; sink is valid in any paging state the render passes through.
;
; Input:  A = byte.  Output: byte emitted, render_out_len += 1.
; Clobbers: HL, F.  Preserves A, BC, DE.
; -----------------------------------------------------------------------
render_out_append:
                out     (RENDER_SINK_PORT), a   ; stream the byte (A preserved)
                push    af
                ld      hl, (render_out_len)
                inc     hl
                ld      (render_out_len), hl
                ld      a, h
                or      l
                jr      nz, render_oa_done      ; low word did not wrap
                ld      hl, (render_out_len + 2)
                inc     hl
                ld      (render_out_len + 2), hl
render_oa_done:
                pop     af
                ret

; RENDER_SINK_PORT — the port the streamed source text is written to.  Mirrors
; the printer data port (print.asm) so the same "emit a byte" shape drives a
; disk/printer sink on real hardware; the harness captures raw writes to it.
RENDER_SINK_PORT:       equ     &E8


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
                ; Overflow guard (i365 #858 YELLOW): render_labels holds
                ; RENDER_MAX_LABELS rows; fail loud rather than overrun into the
                ; adjacent resident tables once an arbitrary user program (not
                ; the release corpus the caps are sized for) supplies more.
                ld      hl, (render_label_count)
                ld      de, RENDER_MAX_LABELS
                or      a
                sbc     hl, de                      ; count - RENDER_MAX_LABELS
                jp      nc, fail                    ; count >= cap: table full
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
                ; Overflow guard (i365 #858 YELLOW): render_locals holds
                ; RENDER_MAX_LOCALS rows; fail loud rather than overrun.
                ld      hl, (render_local_count)
                ld      de, RENDER_MAX_LOCALS
                or      a
                sbc     hl, de                      ; count - RENDER_MAX_LOCALS
                jp      nc, fail                    ; count >= cap: table full
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


; =======================================================================
; Header-def tie ordering (newHeaderDefs, pc.go:44-66).  At a shared offset the
; Go authority sorts labels by NAME and locals by DIGIT (labels always before
; locals — the render_flush labels-then-locals order handles that).  The header
; tables arrive offset-ascending but in the assembler's table order within a
; tie, so render_sort_labels / render_sort_locals bubble-sort each equal-offset
; GROUP into the required order once, before the walk.  Bubbling only swaps
; ADJACENT rows with EQUAL offsets, so the offset ordering is preserved.
; =======================================================================

; render_sort_labels — order render_labels ties by name (ascending).
render_sort_labels:
                ld      hl, (render_label_count)
                ld      de, 2
                or      a
                sbc     hl, de
                ret     c                       ; < 2 rows: nothing to sort
render_sl_pass:
                xor     a
                ld      (render_sl_swapped), a
                ld      hl, 0
                ld      (render_sl_i), hl
render_sl_loop:
                ld      hl, (render_sl_i)
                inc     hl
                ld      de, (render_label_count)
                or      a
                sbc     hl, de
                jr      nc, render_sl_endpass   ; i+1 >= count
                ld      hl, (render_sl_i)
                call    render_label_row_addr   ; HL = &render_labels[i]
                ld      (render_sl_pi), hl
                ld      de, 6
                add     hl, de
                ld      (render_sl_pj), hl      ; &render_labels[i+1]
                call    render_sl_offsets_eq
                jr      nz, render_sl_next      ; different offsets: leave ordered
                call    render_sl_name_gt       ; CF set iff name(i) > name(j)
                jr      nc, render_sl_next
                ld      hl, (render_sl_pi)
                ld      de, (render_sl_pj)
                ld      b, 6
                call    render_swap_bytes
                ld      a, 1
                ld      (render_sl_swapped), a
render_sl_next:
                ld      hl, (render_sl_i)
                inc     hl
                ld      (render_sl_i), hl
                jr      render_sl_loop
render_sl_endpass:
                ld      a, (render_sl_swapped)
                or      a
                jr      nz, render_sl_pass
                ret

; render_sort_locals — order render_locals ties by digit (ascending).
render_sort_locals:
                ld      hl, (render_local_count)
                ld      de, 2
                or      a
                sbc     hl, de
                ret     c
render_slo_pass:
                xor     a
                ld      (render_sl_swapped), a
                ld      hl, 0
                ld      (render_sl_i), hl
render_slo_loop:
                ld      hl, (render_sl_i)
                inc     hl
                ld      de, (render_local_count)
                or      a
                sbc     hl, de
                jr      nc, render_slo_endpass
                ld      hl, (render_sl_i)
                call    render_local_row_addr   ; HL = &render_locals[i]
                ld      (render_sl_pi), hl
                ld      de, 5
                add     hl, de
                ld      (render_sl_pj), hl
                call    render_sl_offsets_eq
                jr      nz, render_slo_next
; digit(i) > digit(j) ? (rows are {offset u32}{digit u8})
                ld      hl, (render_sl_pi)
                ld      de, 4
                add     hl, de
                ld      a, (hl)                 ; digit(i)
                ld      b, a
                ld      hl, (render_sl_pj)
                add     hl, de
                ld      a, (hl)                 ; digit(j)
                ld      c, a
                ld      a, b
                cp      c                       ; digit(i) - digit(j)
                jr      c, render_slo_next      ; i < j → ordered
                jr      z, render_slo_next      ; equal → no swap
                ld      hl, (render_sl_pi)
                ld      de, (render_sl_pj)
                ld      b, 5
                call    render_swap_bytes
                ld      a, 1
                ld      (render_sl_swapped), a
render_slo_next:
                ld      hl, (render_sl_i)
                inc     hl
                ld      (render_sl_i), hl
                jr      render_slo_loop
render_slo_endpass:
                ld      a, (render_sl_swapped)
                or      a
                jr      nz, render_slo_pass
                ret

; render_label_row_addr — HL = index → HL = render_labels + index*6.
render_label_row_addr:
                ld      d, h
                ld      e, l
                add     hl, hl                  ; 2*index
                add     hl, de                  ; 3*index
                add     hl, hl                  ; 6*index
                ld      de, render_labels
                add     hl, de
                ret

; render_local_row_addr — HL = index → HL = render_locals + index*5.
render_local_row_addr:
                ld      d, h
                ld      e, l
                add     hl, hl                  ; 2*index
                add     hl, hl                  ; 4*index
                add     hl, de                  ; 5*index
                ld      de, render_locals
                add     hl, de
                ret

; render_sl_offsets_eq — Z=1 iff the 4-byte offsets at (render_sl_pi) and
; (render_sl_pj) are equal.
render_sl_offsets_eq:
                ld      hl, (render_sl_pi)
                ld      de, (render_sl_pj)
                ld      b, 4
render_sloe_loop:
                ld      a, (de)
                cp      (hl)
                ret     nz
                inc     hl
                inc     de
                djnz    render_sloe_loop
                ret                             ; Z from last CP (equal)

; render_sl_name_gt — CF set iff name(row i) > name(row j) lexicographically.
render_sl_name_gt:
                ld      hl, (render_sl_pi)
                ld      de, 4
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = name_id(i)
                call    render_name_of          ; HL = strA ptr
                ld      (render_sl_strA), hl
                ld      hl, (render_sl_pj)
                ld      de, 4
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = name_id(j)
                call    render_name_of          ; HL = strB ptr
                ex      de, hl                  ; DE = strB
                ld      hl, (render_sl_strA)    ; HL = strA
                ; fall through to render_strcmp_gt

; render_strcmp_gt — CF set iff the C-string at HL > the C-string at DE
; (byte-wise lexicographic; a prefix is smaller — matches Go string `<`).
render_strcmp_gt:
render_scg_loop:
                ld      a, (de)                 ; strB[k]
                ld      b, a
                ld      a, (hl)                 ; strA[k]
                cp      b                       ; strA[k] - strB[k]
                jr      c, render_scg_le        ; strA < strB
                jr      nz, render_scg_gt       ; strA > strB
                or      a                       ; equal bytes: nul terminator?
                jr      z, render_scg_le        ; both ended → equal (not gt)
                inc     hl
                inc     de
                jr      render_scg_loop
render_scg_gt:
                scf
                ret
render_scg_le:
                or      a                       ; clear CF
                ret

; render_swap_bytes — swap B bytes at (HL) and (DE).
render_swap_bytes:
                ld      a, (hl)
                ld      c, a
                ld      a, (de)
                ld      (hl), a
                ld      a, c
                ld      (de), a
                inc     hl
                inc     de
                djnz    render_swap_bytes
                ret


; -----------------------------------------------------------------------
; render_flush — drain the comment/blank-run sidecar at the current PC, then
; emit every header definition whose offset == PC.  Mirrors flush() (emit.go:122)
; = flushComments() then hd.flushAt(pc, emitDef).  At a shared PC the sidecar
; rows emit BEFORE the labels: a trailing comment belongs to the just-rendered
; statement (its line is still open); a standalone comment / blank run precedes
; the next label.  Labels drain before locals at a shared offset (newHeaderDefs's
; stable order, pc.go:55-66); within a kind the reader's ascending-offset order
; is used.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_flush:
                call    render_flush_comments
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
; render_flush_comments — drain sidecar rows whose anchor <= the current PC,
; in stored (source) order.  Mirrors cc.flushAt(pc, emitComment, emitBlank)
; (pc.go:119-129).  The "<= pc" (not "== pc") matches the Go: a row is anchored
; to a statement-boundary PC the walk hits, and draining at-or-before strands
; nothing if PC accounting ever drifts.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
render_flush_comments:
render_fc_loop:
; Ensure a row is decoded into the pending slot.
                ld      a, (render_sc_pending)
                or      a
                jr      nz, render_fc_have_pending
                ld      hl, (render_sc_remaining)
                ld      a, h
                or      l
                ret     z                           ; no rows remain
                call    render_sc_stream_decode     ; decode one row → pending
render_fc_have_pending:
; Stop if the pending row's anchor is still ahead of the PC.
                call    render_sc_pend_anchor_le_pc ; C set iff anchor > pc
                ret     c
                call    render_sc_place_pending
                xor     a
                ld      (render_sc_pending), a
                jr      render_fc_loop


; render_sc_pend_anchor_le_pc — CARRY set iff the pending row's anchor >
; render_pc (drain stops); CARRY clear iff anchor <= pc (place).  pc - anchor as
; an unsigned 32-bit subtract, LSB first; the final borrow is the result (INC
; HL/DE and LD do not disturb the carry chain).
render_sc_pend_anchor_le_pc:
                ld      de, render_pc
                ld      hl, render_sc_pend_anchor
                ld      a, (de)
                sub     (hl)                        ; pc[0] - anchor[0]
                inc     hl
                inc     de
                ld      a, (de)
                sbc     a, (hl)                     ; pc[1] - anchor[1] - borrow
                inc     hl
                inc     de
                ld      a, (de)
                sbc     a, (hl)                     ; pc[2] - anchor[2] - borrow
                inc     hl
                inc     de
                ld      a, (de)
                sbc     a, (hl)                     ; pc[3] - anchor[3] - borrow
                ret                                 ; C = borrow = (pc < anchor)


; render_sc_place_pending — emit the pending sidecar row on its kind.
; emitComment (emit.go:111-113) / emitBlank (emit.go:114-116).
render_sc_place_pending:
                ld      a, (render_sc_pend_kind)
                cp      SIDECAR_BLANK
                jp      z, render_sc_emit_blank
                ; fall through to render_sc_emit_comment


; -----------------------------------------------------------------------
; render_sc_emit_comment — emitCommentBody (emit.go:24-39): render a comment
; body as one-or-more "//"-prefixed lines.  Split the body on '\n'; strip a
; trailing '\r' from each line.  Placement 1 (trailing) appends the FIRST line
; to the open statement as " //line\n"; every other line, and any standalone
; comment, takes its own "//line\n" (closing an open statement with '\n' first).
; prevWasStatement becomes false.
;
; Input:  the pending row (render_sc_pend_place / render_sc_pend_len) with its
;         body already copied into render_body_buf by render_sc_stream_decode.
; -----------------------------------------------------------------------
render_sc_emit_comment:
                ld      a, (render_sc_pend_place)
                ld      (render_cb_placement), a
                ld      hl, render_body_buf
                ld      (render_cb_start), hl       ; body base
                ld      de, (render_sc_pend_len)
                add     hl, de
                ld      (render_cb_end), hl         ; end = body base + length
                ld      a, 1
                ld      (render_cb_first), a

; --- segment loop: split the body on '\n' (strings.Split semantics) ---------
render_cb_seg_loop:
                ld      hl, (render_cb_start)
                ld      (render_cb_scan), hl
render_cb_scan_loop:
                ld      hl, (render_cb_scan)
                ld      de, (render_cb_end)
                or      a
                sbc     hl, de
                jr      nc, render_cb_scan_end      ; scan >= end → no '\n'
                ld      hl, (render_cb_scan)
                ld      a, (hl)
                cp      10                          ; '\n'
                jr      z, render_cb_scan_found
                inc     hl
                ld      (render_cb_scan), hl
                jr      render_cb_scan_loop
render_cb_scan_end:
; final segment = [start, end); emit, then break (no more '\n').
                ld      hl, (render_cb_end)
                ld      (render_cb_seg_end), hl
                jp      render_cb_emit_segment      ; tail (no continuation)
render_cb_scan_found:
; segment = [start, scan); emit, then continue past the '\n'.
                ld      hl, (render_cb_scan)
                ld      (render_cb_seg_end), hl
                call    render_cb_emit_segment
                ld      hl, (render_cb_scan)
                inc     hl                          ; skip the '\n'
                ld      (render_cb_start), hl
                jr      render_cb_seg_loop


; render_cb_emit_segment — emit one split-line: strip a trailing '\r', then the
; placement/first-line logic of emitCommentBody's loop body.
render_cb_emit_segment:
; seg length = seg_end - start.
                ld      hl, (render_cb_seg_end)
                ld      de, (render_cb_start)
                or      a
                sbc     hl, de                      ; HL = raw segment length
                ld      a, h
                or      l
                jr      z, render_cb_len_done       ; empty segment
; strip a trailing '\r' (TrimSuffix line "\r").
                push    hl
                ld      hl, (render_cb_seg_end)
                dec     hl
                ld      a, (hl)
                cp      13                          ; '\r'
                pop     hl
                jr      nz, render_cb_len_done
                dec     hl                          ; drop the '\r'
render_cb_len_done:
                ld      (render_cb_seg_len), hl

; placement 1 + first line + open statement → append trailing " //line\n".
                ld      a, (render_cb_first)
                or      a
                jr      z, render_cb_standalone
                ld      a, (render_cb_placement)
                cp      1
                jr      nz, render_cb_standalone
                ld      a, (render_prev_stmt)
                or      a
                jr      z, render_cb_standalone
                ld      a, " "
                call    render_out_append
                call    render_cb_emit_slashline
                jr      render_cb_seg_done
render_cb_standalone:
; standalone / continuation line: close an open statement, then "//line\n".
                ld      a, (render_prev_stmt)
                or      a
                jr      z, render_cb_no_nl
                ld      a, 10
                call    render_out_append
render_cb_no_nl:
                call    render_cb_emit_slashline
render_cb_seg_done:
                xor     a
                ld      (render_prev_stmt), a       ; prevWasStatement = false
                ld      (render_cb_first), a        ; subsequent lines are not first
                ret


; render_cb_emit_slashline — append "//", the segment bytes
; [render_cb_start, +render_cb_seg_len), then '\n'.
render_cb_emit_slashline:
                ld      a, "/"
                call    render_out_append
                ld      a, "/"
                call    render_out_append
                ld      hl, (render_cb_start)
                ld      bc, (render_cb_seg_len)
                call    render_out_put_bytes
                ld      a, 10
                jp      render_out_append


; -----------------------------------------------------------------------
; render_sc_emit_blank — emitBlankRun (emit.go:45-53): if a statement is open,
; close it with '\n' (that newline is the statement's own line, not a blank),
; then emit render_cb (run length) blank lines.  prevWasStatement becomes false.
;
; Input:  the pending row's run length (render_sc_pend_len).
; -----------------------------------------------------------------------
render_sc_emit_blank:
                ld      de, (render_sc_pend_len)    ; run length (u16)
                push    de
                ld      a, (render_prev_stmt)
                or      a
                jr      z, rseb_no_close
                ld      a, 10
                call    render_out_append
                xor     a
                ld      (render_prev_stmt), a
rseb_no_close:
                pop     de
rseb_loop:
                ld      a, d
                or      e
                ret     z
                ld      a, 10
                push    de
                call    render_out_append
                pop     de
                dec     de
                jr      rseb_loop


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
                ld      a, (hl)                     ; A = local number (0..255)
                ld      hl, render_out_append
                ld      (render_sink_addr), hl
                call    render_emit_byte_dec_sink   ; number (may be multi-digit)
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
; semantics).  Comments are origin-neutral, so `.org` still drains them first
; (flushComments); only the label flush is held (emit.go:169-171).
                ld      a, (render_dir_id)
                cp      DIR_ORG
                jr      z, render_dir_org_flush
                call    render_flush
                jr      render_dir_afterflush
render_dir_org_flush:
                call    render_flush_comments
render_dir_afterflush:
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


; render_emit_const_directive — print render_num32 (signed 64-bit) in directive
; context, no leading '#': decimal for a magnitude in 1..255, else %#x
; (emit.go:491-503: v in (-256, 256) → %d, else %#x).
render_emit_const_directive:
                ld      hl, render_out_append
                ld      (render_sink_addr), hl
                ld      a, (render_num32 + 7)
                bit     7, a
                jr      nz, render_ecd_neg
; v >= 0: decimal iff bytes 1..7 are all zero (v < 256).
                ld      a, (render_num32 + 1)
                ld      hl, render_num32 + 2
                ld      b, 6
render_ecd_pz:
                or      (hl)
                inc     hl
                djnz    render_ecd_pz
                jr      z, render_ecd_dec
                jp      render_emit_hashx_s32
render_ecd_neg:
; v < 0: decimal iff bytes 1..7 are all &FF and byte0 != 0 (v > -256).
                ld      a, (render_num32 + 1)
                ld      hl, render_num32 + 2
                ld      b, 6
render_ecd_nf:
                and     (hl)
                inc     hl
                djnz    render_ecd_nf
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
                ld      hl, render_out_append
                ld      (render_sink_addr), hl
                ld      a, (render_ssr_id)
                call    render_emit_byte_dec_sink    ; number (may be multi-digit)
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
                call    render_emit_byte_dec_sink    ; number (sink = render_pe_putc)
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
                ld      b, 8
render_ev_not_loop:
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                djnz    render_ev_not_loop
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
                ld      bc, 8
                ldir
                call    render_ev_pop_num32
                ld      hl, render_num32
                ld      de, render_ev_a
                ld      bc, 8
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
                ld      hl, (render_ev_a + 0)
                ld      de, (render_ev_b + 0)
                add     hl, de
                ld      (render_num32 + 0), hl
                ld      hl, (render_ev_a + 2)
                ld      de, (render_ev_b + 2)
                adc     hl, de
                ld      (render_num32 + 2), hl
                ld      hl, (render_ev_a + 4)
                ld      de, (render_ev_b + 4)
                adc     hl, de
                ld      (render_num32 + 4), hl
                ld      hl, (render_ev_a + 6)
                ld      de, (render_ev_b + 6)
                adc     hl, de
                ld      (render_num32 + 6), hl
                jp      render_ev_push_num32
render_ev_op_sub:
                ld      hl, (render_ev_a + 0)
                ld      de, (render_ev_b + 0)
                or      a
                sbc     hl, de
                ld      (render_num32 + 0), hl
                ld      hl, (render_ev_a + 2)
                ld      de, (render_ev_b + 2)
                sbc     hl, de
                ld      (render_num32 + 2), hl
                ld      hl, (render_ev_a + 4)
                ld      de, (render_ev_b + 4)
                sbc     hl, de
                ld      (render_num32 + 4), hl
                ld      hl, (render_ev_a + 6)
                ld      de, (render_ev_b + 6)
                sbc     hl, de
                ld      (render_num32 + 6), hl
                jp      render_ev_push_num32
; render_ev_op_and/or/xor — bytewise 64-bit: num32 = a, then num32 op= b.
render_ev_op_and:
                ld      hl, render_ev_a
                ld      de, render_num32
                ld      bc, 8
                ldir
                ld      hl, render_num32
                ld      de, render_ev_b
                ld      b, 8
render_evand_loop:
                ld      a, (de)
                and     (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    render_evand_loop
                jp      render_ev_push_num32
render_ev_op_or:
                ld      hl, render_ev_a
                ld      de, render_num32
                ld      bc, 8
                ldir
                ld      hl, render_num32
                ld      de, render_ev_b
                ld      b, 8
render_evor_loop:
                ld      a, (de)
                or      (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    render_evor_loop
                jp      render_ev_push_num32
render_ev_op_xor:
                ld      hl, render_ev_a
                ld      de, render_num32
                ld      bc, 8
                ldir
                ld      hl, render_num32
                ld      de, render_ev_b
                ld      b, 8
render_evxor_loop:
                ld      a, (de)
                xor     (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    render_evxor_loop
                jp      render_ev_push_num32
render_ev_op_shl:
; render_num32 = a; shift left by b.  A shift count >= 64 (or any high byte of
; b set — Go's a<<uint64(b) with b>=64 or b<0) yields 0.
                ld      hl, render_ev_a
                ld      de, render_num32
                ld      bc, 8
                ldir
                call    render_ev_shift_count        ; A = count, or >=64 sentinel
                cp      64
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
                ld      (render_num32 + 0), hl
                ld      (render_num32 + 2), hl
                ld      (render_num32 + 4), hl
                ld      (render_num32 + 6), hl
                jp      render_ev_push_num32
render_ev_op_shr:
; render_num32 = a; arithmetic shift right by b (count capped at 64 → sign fill).
                ld      hl, render_ev_a
                ld      de, render_num32
                ld      bc, 8
                ldir
                call    render_ev_shift_count        ; A = count, or >=64 sentinel
                cp      64
                jr      c, render_ev_shr_have
                ld      a, 64
render_ev_shr_have:
                or      a
                jp      z, render_ev_push_num32
                ld      b, a
render_ev_shr_loop:
                call    render_num32_sar1
                djnz    render_ev_shr_loop
                jp      render_ev_push_num32

; render_ev_shift_count — A = the shift amount from render_ev_b, or 64 (clamped)
; when any byte above byte 0 is set (b >= 256, always a full shift).
render_ev_shift_count:
                ld      hl, render_ev_b + 1
                ld      b, 7
                ld      a, 0
render_evsc_loop:
                or      (hl)
                inc     hl
                djnz    render_evsc_loop
                jr      z, render_evsc_lowbyte       ; high bytes all zero
                ld      a, 64                        ; clamp to a full shift
                ret
render_evsc_lowbyte:
                ld      a, (render_ev_b)
                ret

; render_ev_push_num32 / _pop_num32 — 64-bit value stack (depth * 8 bytes).
render_ev_push_num32:
                ld      a, (render_ev_depth)
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl
                add     hl, hl                       ; depth * 8
                ld      de, render_ev_stack
                add     hl, de
                ex      de, hl
                ld      hl, render_num32
                ld      bc, 8
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
                add     hl, hl                       ; depth * 8
                ld      de, render_ev_stack
                add     hl, de
                ld      de, render_num32
                ld      bc, 8
                ldir
                ret


; -----------------------------------------------------------------------
; 32-bit signed helpers used by both the const printer and PC sizing.
; -----------------------------------------------------------------------

; render_signfill — fill C bytes at (HL) with A (a sign byte 0/&FF).
render_signfill:
                ld      (hl), a
                inc     hl
                dec     c
                jr      nz, render_signfill
                ret

; render_sx8_to_num32 — A (int8) → render_num32 sign-extended to 64 bits.
render_sx8_to_num32:
                ld      (render_num32), a
                ld      b, 0
                bit     7, a
                jr      z, render_sx8_p
                ld      b, &FF
render_sx8_p:
                ld      a, b
                ld      hl, render_num32 + 1
                ld      c, 7
                jp      render_signfill

; render_sx16_to_num32 — (render_expr_operand) int16 → render_num32 (64-bit).
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
                ld      hl, render_num32 + 2
                ld      c, 6
                jp      render_signfill

; render_sx32_to_num32 — (render_expr_operand) int32 → render_num32 (64-bit).
render_sx32_to_num32:
                ld      hl, (render_expr_operand)
                ld      de, render_num32
                ld      bc, 4
                ldir
                ld      a, (render_num32 + 3)
                ld      b, 0
                bit     7, a
                jr      z, render_sx32_p
                ld      b, &FF
render_sx32_p:
                ld      a, b
                ld      hl, render_num32 + 4
                ld      c, 4
                jp      render_signfill

; render_sx64_to_num32 — (render_expr_operand) int64 → render_num32 (all 8 bytes).
render_sx64_to_num32:
                ld      hl, (render_expr_operand)
                ld      de, render_num32
                ld      bc, 8
                ldir
                ret

; render_neg_num32 — render_num32 = -render_num32 (64-bit two's complement).
render_neg_num32:
                ld      hl, render_num32
                ld      b, 8
render_neg_cpl:
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                djnz    render_neg_cpl
                ld      hl, render_num32
                ld      b, 8
                scf                                  ; the +1 of two's complement
render_neg_inc:
                ld      a, (hl)
                adc     a, 0
                ld      (hl), a
                inc     hl                           ; INC HL / DJNZ preserve carry
                djnz    render_neg_inc
                ret

; render_num32_shl1 — render_num32 <<= 1 (64-bit; LD preserves carry).
render_num32_shl1:
                ld      hl, (render_num32 + 0)
                add     hl, hl
                ld      (render_num32 + 0), hl
                ld      hl, (render_num32 + 2)
                adc     hl, hl
                ld      (render_num32 + 2), hl
                ld      hl, (render_num32 + 4)
                adc     hl, hl
                ld      (render_num32 + 4), hl
                ld      hl, (render_num32 + 6)
                adc     hl, hl
                ld      (render_num32 + 6), hl
                ret

; render_num32_sar1 — render_num32 >>= 1 (64-bit arithmetic; MSB word first).
render_num32_sar1:
                ld      hl, (render_num32 + 6)
                sra     h
                rr      l
                ld      (render_num32 + 6), hl
                ld      hl, (render_num32 + 4)
                rr      h
                rr      l
                ld      (render_num32 + 4), hl
                ld      hl, (render_num32 + 2)
                rr      h
                rr      l
                ld      (render_num32 + 2), hl
                ld      hl, (render_num32 + 0)
                rr      h
                rr      l
                ld      (render_num32 + 0), hl
                ret

; render_num32_is_zero — Z=1 iff render_num32 == 0 (all 8 bytes).
render_num32_is_zero:
                ld      a, (render_num32)
                ld      hl, render_num32 + 1
                ld      b, 7
render_niz_loop:
                or      (hl)
                inc     hl
                djnz    render_niz_loop
                ret

; render_u32_div10 — render_num32 /= 10; A = remainder (64-bit base-256 long div).
render_u32_div10:
                ld      c, 0                         ; running remainder
                ld      b, 8
                ld      hl, render_num32 + 7         ; MSB first
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
; render_emit_dec_s32 — append render_num32 (signed 64-bit) as decimal via
; render_sink.
; -----------------------------------------------------------------------
render_emit_dec_s32:
                ld      a, (render_num32 + 7)
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
; render_emit_byte_dec_sink — append A (an unsigned 0..255) as decimal via
; render_sink (leading zeros suppressed, always at least one digit).  Numeric
; local labels carry a number, not a single 0..9 digit (Go renders `%d%c`, e.g.
; "10f"), so this replaces the `add a,'0'` single-digit path at every local site.
; Clobbers A, BC, DE, HL (render_sink preserves BC/DE/A across each byte).
; -----------------------------------------------------------------------
render_emit_byte_dec_sink:
                ld      e, a                         ; E = remaining value
                ld      d, 0                         ; D = 0 while suppressing leading zeros
                ld      c, 100
                call    render_ebd_place
                ld      c, 10
                call    render_ebd_place
                ld      a, e                         ; units digit (always emitted)
                add     a, "0"
                jp      render_sink                  ; tail-call
render_ebd_place:
                ld      a, e
                ld      b, "0" - 1                   ; B = '0' + quotient (pre-increment)
render_ebd_sub:
                inc     b
                sub     c
                jr      nc, render_ebd_sub
                add     a, c                         ; undo the over-subtract → remainder
                ld      e, a
                ld      a, b
                cp      "0"
                jr      nz, render_ebd_emit          ; nonzero quotient → emit
                ld      a, d
                or      a
                ret     z                            ; leading zero → suppress
                ld      a, "0"
render_ebd_emit:
                ld      d, 1                          ; stop suppressing after a real digit
                jp      render_sink                  ; tail-call (returns to render_ebd_place's caller)


; -----------------------------------------------------------------------
; render_emit_hashx_s32 — append render_num32 (signed 64-bit) as Go's %#x via
; render_sink.
; -----------------------------------------------------------------------
render_emit_hashx_s32:
                ld      a, (render_num32 + 7)
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
                ld      b, 8
                ld      hl, render_num32 + 7
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
                ld      (render_num32 + 0), hl
                ld      (render_num32 + 2), hl
                ld      (render_num32 + 4), hl
                ld      (render_num32 + 6), hl
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
                ld      (render_num32 + 4), hl
                ld      (render_num32 + 6), hl
                ret
render_dbs_align:
                call    render_dir_eval_op0          ; render_num32 = N, Z=const?
                jp      nz, render_dbs_zero
                ld      a, (render_num32 + 7)
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

; render_dbs_set_opcount — render_num32 = op_count (zero-extended to 64 bits).
render_dbs_set_opcount:
                ld      a, (render_dir_opcount)
                ld      (render_num32), a
                xor     a
                ld      (render_num32 + 1), a
                ld      hl, 0
                ld      (render_num32 + 2), hl
                ld      (render_num32 + 4), hl
                ld      (render_num32 + 6), hl
                ret

; render_dir_op0_len — render_num32 = length of operand-0's string (64-bit).
render_dir_op0_len:
                ld      hl, (render_dir_ops_ptr)
                inc     hl                           ; skip kind
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                      ; DE = string length
                ld      (render_num32), de
                ld      hl, 0
                ld      (render_num32 + 2), hl
                ld      (render_num32 + 4), hl
                ld      (render_num32 + 6), hl
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
; Sized for the full release corpus: 282 header-label defs, 172 numeric-local
; defs (both read once by reader_init and flushed by PC during the walk).
RENDER_MAX_LABELS:      equ     320
RENDER_MAX_LOCALS:      equ     192

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

; Header-def tie-sort scratch (render_sort_labels / render_sort_locals).
render_sl_i:            defw    0       ; bubble-sort outer index
render_sl_swapped:      defb    0       ; a swap happened this pass
render_sl_pi:          defw    0       ; &row[i]
render_sl_pj:          defw    0       ; &row[i+1]
render_sl_strA:        defw    0       ; name(row i) string ptr

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

; SLICE 6b — immediate/memory splice scratch.
render_ovl_suffix:      defw    0       ; ops[i:] tail ptr (after the spliced field)
render_ovl_mnem_ptr:    defw    0       ; mnemonic ptr (decoded, or recovered movz/movk)
render_ovl_is_dir:      defb    0       ; renderImmText isDirective flag

render_ld_src:          defw    0       ; LIT_DATA payload ptr ([dir_id][data])
render_ld_len:          defw    0       ; LIT_DATA payload length (1 + nbytes)
render_ld_data:         defw    0       ; LIT_DATA data cursor
render_ld_rem_bytes:    defw    0       ; LIT_DATA data bytes remaining
render_ld_nameptr:      defw    0       ; directive name string ptr
render_ld_idx:          defb    0       ; element index (litDataPerLine wrap)
render_ld_width:        defb    0       ; element width in bytes (1/2/4/8)
render_ld_seen:         defb    0       ; hex leading-zero suppression flag

; SLICE 7 — comment-body (emitCommentBody) split-line scratch.
render_cb_placement:    defb    0       ; current comment placement (0/1)
render_cb_first:        defb    0       ; is this the first split line (i==0)?
render_cb_start:        defw    0       ; current segment start ptr (body buffer)
render_cb_end:          defw    0       ; body end ptr (start + length)
render_cb_scan:         defw    0       ; '\n' scan cursor
render_cb_seg_end:      defw    0       ; current segment end (exclusive)
render_cb_seg_len:      defw    0       ; current segment length (post-'\r'-strip)

render_out_len:         defb    0, 0, 0, 0      ; 32-bit count of bytes streamed out


; -----------------------------------------------------------------------
; SLICE 5 state — directive/operand/expr scratch (section C/D, HMPR-stable).
; -----------------------------------------------------------------------
RENDER_EV_MAX_DEPTH:    equ     16      ; const-eval value-stack depth
RENDER_PE_MAX_DEPTH:    equ     16      ; printExpr text-stack depth
RENDER_PE_ARENA_SIZE:   equ     512     ; printExpr fragment arena

render_num32:           defs    8       ; 64-bit signed accumulator / result

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
render_ev_stack:        defs    RENDER_EV_MAX_DEPTH * 8 ; 64-bit values
render_ev_a:            defs    8       ; binary-op left operand
render_ev_b:            defs    8       ; binary-op right operand

render_align:           defs    4       ; alignment boundary
render_align_n:         defb    0       ; .align exponent (align = 1 << n)
render_align_addr:      defs    4       ; (origin+pc) dividend (consumed by mod)
render_align_rem:       defs    4       ; (origin+pc) mod align

render_decbuf:          defs    24      ; decimal digits (reverse order; 64-bit)
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
