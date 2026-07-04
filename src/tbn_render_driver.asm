; tbn_render_driver.asm — standalone driver for the `.tbn` → source-text
; renderer (i365a slice 1).
;
; org &8000; the harness deposits this image into a section-C page (+ its
; section-D neighbour), build/disasm.bin into physical page DISASM_PAGE, and
; the fixture `.tbn` into physical page IN_PAGE, then calls render_run.
;
; The renderer reuses the shared record reader (src/reader.asm) and the
; disassembler (build/disasm.bin) via paged_call.  This driver supplies the
; IN-paging window helpers and the handful of reader symbols the standalone
; build needs, tailored to a single-page IN buffer (the fixture `.tbn` fits
; comfortably in one 16 KB page).
;
; Paging layout while rendering (LMPR = LMPR_RENDER):
;   Section A (&0000-&3FFF) = physical page IN_PAGE   — the staged `.tbn`
;   Section B (&4000-&7FFF) = physical page IN_PAGE+1 — paged_call body + comm
;   Section C (&8000-&BFFF) = this driver (HMPR-selected)
;   Section D (&C000-&FFFF) = driver's section-D page — STAGING_BUF + stack
; paged_call temporarily swaps HMPR to DISASM_PAGE for the decode and restores
; it, so section C/D return to the driver on every disasm return.

                org     &8000

; Disassembler comm-buffer constants (DISASM_COMM_MNEM/OPS/PC, self-test slot).
                include "disasm_comm.inc"

; Record-kind constants (REC_KIND_INSN_RUN etc.), generated from the Go format.
                include "tbn_constants.inc"

; Disassembler decode target: page 15, entry &8000 (mirrors trampoline.asm).
DISASM_ENTRY:           equ     &8000
DISASM_PAGE:            equ     15

; IN paging: the `.tbn` is staged in physical page IN_PAGE.  LMPR_RENDER maps
; that page to section A and page IN_PAGE+1 (paged_call body) to section B.
IN_PAGE:                equ     8
LMPR_RENDER:            equ     &20 | IN_PAGE   ; RAM0 bit + page 8 = &28

; STAGING_BUF — the reader's 1 KB record staging area (section D, HMPR-stable).
STAGING_BUF:            equ     &D500
STAGING_BUF_END:        equ     &D900

; paged_call installation slots (mirror chain_paged_driver.asm / trampoline.asm).
TRAMPOLINE_DST:         equ     &7E00
PAGED_CALL_DST:         equ     TRAMPOLINE_DST + &40    ; = &7E40 (section B)
PAGED_CALL_HMPR_SAVE:   equ     TRAMPOLINE_DST + &D0    ; = &7ED0
PAGED_CALL_SP_SAVE:     equ     TRAMPOLINE_DST + &D1    ; = &7ED1
TRAMP_SAFE_SP:          equ     TRAMPOLINE_DST + 256    ; = &7F00
paged_call:             equ     PAGED_CALL_DST

; Reader header-table pass gate.  The fixture's header tables are empty, so the
; seed path (symbol_insert / local_def_append) is never reached; PASS_MODE is
; held at a non-PASS_PASS1 value so the reader parses-only.
PASS_PASS1:             equ     1


; ===========================================================================
; render_run — set up the render window, install paged_call, point the reader
; at the staged `.tbn`, and render it.
;
; Entry: the fixture `.tbn` is resident at offset 0 of physical page IN_PAGE.
; Exit:  rendered text in render_out, length in render_out_len.  Boot LMPR/SP
;        restored so the harness RET lands on its planted HALT trap.
;
; The stack is relocated to section D (HMPR-stable) for the duration, because
; the render toggles LMPR (moving section B out from under the boot stack) —
; the same save/relocate/restore discipline as chain_paged_driver.asm.
; ===========================================================================
render_run:
                ; Phase 0: save boot LMPR + SP, relocate the stack to section D.
                in      a, (250)
                ld      (render_saved_lmpr), a
                ld      hl, 0
                add     hl, sp
                ld      (render_saved_sp), hl
                ld      sp, &C0FE               ; section D (HMPR-stable) stack

                ; Phase 1: map the render window (section A = IN page, section B
                ; = paged_call page) and install the paged_call body there.
                ld      a, LMPR_RENDER
                out     (250), a
                ld      hl, paged_call_body
                ld      de, PAGED_CALL_DST
                ld      bc, paged_call_body_end - paged_call_body
                ldir

                ; Phase 2: point the reader cursor at the staged `.tbn`.
                ld      a, LMPR_RENDER
                ld      (IN_BASE_LMPR), a
                ld      (IN_POS_PAGE), a
                ld      hl, 0
                ld      (IN_POS_OFFSET), hl

                ; Phase 3: render.
                call    render_emit

                ; Phase 4: restore boot LMPR + SP.
                ld      a, (render_saved_lmpr)
                out     (250), a
                ld      hl, (render_saved_sp)
                ld      sp, hl
                ret

render_saved_lmpr:      defb    0
render_saved_sp:        defw    0


; ===========================================================================
; IN-paging window helpers (single-page IN, section A).  Mirror the shared
; implementations in main_loop.asm, specialised for this standalone driver.
; ===========================================================================

; in_map_current — LMPR := IN_POS_PAGE; section A = the current IN page.
in_map_current:
                ld      a, (IN_POS_PAGE)
                out     (250), a
                ret

; in_persist_hl — IN_POS_OFFSET := HL; IN_POS_PAGE := current LMPR.
in_persist_hl:
                ld      (IN_POS_OFFSET), hl
                in      a, (250)
                ld      (IN_POS_PAGE), a
                ret

; in_normalise_hl — renormalise a section-A offset that ran past &3FFF into
; (page+1, offset-&4000).  The fixture `.tbn` never crosses a page, so this is
; a no-op in practice, but the reader calls it and it must be correct.
in_normalise_hl:
in_normalise_loop:
                ld      a, h
                cp      &40
                ret     c
                sub     &40
                ld      h, a
                in      a, (250)
                inc     a
                out     (250), a
                jr      in_normalise_loop

; enctab_map_in — restore section A to the IN page.  The reader tail-calls
; this after a record read to re-map the IN window.  The render path has no
; ENCTAB, so it simply maps the IN page (LMPR_RENDER), which also keeps the
; paged_call body mapped into section B.
enctab_map_in:
                di
                ld      a, LMPR_RENDER
                out     (250), a
                ret

; fail — the reader's error trap.  No valid render input reaches it; a HALT
; here (not the harness RET trap) makes an unexpected reader error visible.
fail:
                di
                halt


; ===========================================================================
; Reader symbols that the standalone build must resolve.  The fixture's header
; label/local tables are empty, so the seed calls below are never executed —
; they resolve to a fail trap should a future fixture reach them unexpectedly.
; ===========================================================================
symbol_insert:          jp      fail
local_def_append:       jp      fail

PASS_MODE:              defb    0       ; != PASS_PASS1 → reader parses-only
symbol_value_buf:       defs    4       ; label-offset accumulator (zeroed by reader)
local_label_pc_buf:     defs    4       ; local-offset accumulator (zeroed by reader)

; Reader IN cursor state (mirrors main_loop.asm).
IN_BASE_LMPR:           defb    0
IN_POS_PAGE:            defb    0
IN_POS_OFFSET:          defw    0
IN_END_PAGE:            defb    0
IN_END_OFFSET:          defw    0


; ===========================================================================
; Included modules.
; ===========================================================================
                include "reader.asm"
                include "tbn_render.asm"
                include "paged_bodies.asm"
