; encoder.asm — output-buffer emit primitives.
;
; The compact `.tbn` v2 overlay (insn_run.asm) assembles each instruction
; by re-folding its base word and emitting the 4 bytes directly, so the
; old form-table dispatcher (encode_inst) and its per-slot handlers are
; gone.  What remains here is the OUT emit path every record handler uses:
; emit_byte (one byte, zone/LMPR-aware) and emit_bytes_n.
;
; The per-slot value encoders the folds reuse (encode_branch_imm,
; encode_imm12_shifted, encode_imm16_shifted, encode_adrp_imm/encode_adr_imm,
; encode_logical_imm) live in src/slots/*.asm.
;
; ---------------------------------------------------------------------
; Operand value record layout (OPVAL_STRIDE) — still consumed by the
; BUILD_TESTS slot self-tests and the directive/sysname paths that stage
; operands before calling a slot encoder.
;
;   offset 0   kind byte (matches format.OperandKind)
;   offset 1   reg byte (if reg-kind)
;   offset 2-9 8-byte LE evaluator result (if OpImmExpr) OR cond byte.
;              10 bytes reserved per operand so array indexing is trivial.
; ---------------------------------------------------------------------
OPVAL_STRIDE:           equ     10

; ---------------------------------------------------------------------
; emit_byte — append one byte to the output buffer at PC, advance PC.
;
; Per docs/specs/paged-out-design.md.  OUT is ONE contiguous run of
; page-pool pages (pp_alloc_run(PP_OUT), sized from the pass-1 total by
; reset_out_buffer).  Every emit brackets LMPR uniformly:
;
;   `in a,(250)` snapshots the live LMPR (whatever the caller runs
;   under — LMPR_ENCTAB during the encoder window), `out (250), A` with
;   OUT_LMPR_CUR maps the cursor's run page into section B
;   (&4000-&7FFF), the byte is stored at OUT_PC, and the snapshot is
;   restored.  Reading LMPR live (rather than hard-coding a constant on
;   the restore) keeps us correct against the boot-time top-bits in
;   LMPR_DEFAULT_RUNTIME (see assembler.asm, trampoline.asm).
;   (Port 250 = LMPR, port 251 = HMPR per SAM Coupé Tech Manual §6.10
;   and the existing trampoline / enctab_map_in usage.)
;
; OUT_PC walks &4000..&7FFF within the current run page.  A write that
; fills the page leaves OUT_PC parked at the &8000 boundary; the NEXT
; emit_byte advances into the following run page (out_advance_page:
; OUT_PAGE_IDX+1, OUT_LMPR_CUR+1, OUT_PC wraps to &4000).  The lazy
; advance makes an exactly-run-filling output legal: the final byte
; parks the cursor and no advance is attempted.  When no next page
; exists the output has exceeded the pass-1-sized run — an internal
; invariant break (pass 1 and pass 2 must advance identically), tag &b0.
;
; Input:    A = byte to emit.
; Output:   byte stored; OUT_PC advanced; 24-bit OUT_LEN incremented;
;           on a page boundary, cursor advanced into the next run page.
; Clobbers: A, HL.  BC, DE preserved.
; ---------------------------------------------------------------------
emit_byte:
                push    af                  ; preserve byte across LMPR/store
                ld      hl, (OUT_PC)
                ld      a, h
                cp      &80
                jr      nz, emit_byte_store
                call    out_advance_page    ; cursor parked at a page boundary
                jr      c, emit_byte_overrun   ; no next page in the run

emit_byte_store:
                in      a, (250)
                ld      (emit_lmpr_save), a
                ld      a, (OUT_LMPR_CUR)
                out     (250), a            ; section B = current run page
                pop     af                  ; A = byte
                ld      (hl), a
                ld      a, (emit_lmpr_save)
                out     (250), a            ; restore the caller's LMPR
                inc     hl
                ld      (OUT_PC), hl        ; may park at &8000 (page full)

; Bump 24-bit OUT_LEN.  HL is free to clobber per the ABI.
                ld      hl, OUT_LEN
                inc     (hl)
                ret     nz
                inc     hl
                inc     (hl)
                ret     nz
                inc     hl
                inc     (hl)
                ret

; The run was sized from the pass-1 total, so an emit past its last
; page means pass 2 produced more bytes than pass 1 counted — an
; internal invariant break, not a user-facing out-of-memory (that is
; reset_out_buffer's tag &b3).
emit_byte_overrun:
                ld      a, &b0
                jp      fail_with_tag       ; tag b0: output exceeded the run


; ---------------------------------------------------------------------
; out_advance_page — move the OUT cursor into the next page of the run.
;
; Split out of emit_byte so the boundary predicate is directly testable
; (the emit self-test drives it against a deliberately undersized run).
;
; Input:    none (reads OUT_PAGE_IDX / OUT_RUN_PAGES / OUT_LMPR_CUR).
; Output:   CF clear on success: OUT_PAGE_IDX+1, OUT_LMPR_CUR+1 (run
;           pages are contiguous, so the section-B mapping just
;           increments), HL = &4000 (the new page's base).
;           CF set when the cursor is already in the run's last page;
;           state unchanged.
; Clobbers: A, HL (and the state bytes on success).
; ---------------------------------------------------------------------
out_advance_page:
                ld      a, (OUT_RUN_PAGES)
                ld      hl, OUT_PAGE_IDX
                inc     (hl)
                cp      (hl)                ; new idx == pages → past the end
                jr      z, out_advance_page_full
                ld      hl, OUT_LMPR_CUR
                inc     (hl)                ; next contiguous page at section B
                ld      hl, &4000
                or      a                   ; CF clear = success
                ret
out_advance_page_full:
                dec     (hl)                ; leave OUT_PAGE_IDX unchanged
                scf
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
; LMPR snapshot used by emit_byte's per-byte bracket.  Live LMPR at
; the call site (= LMPR_ENCTAB during the encoder window) is captured
; here so the restore goes back to the *exact* boot-derived value
; rather than a hard-coded constant.
emit_lmpr_save:         defb    0
