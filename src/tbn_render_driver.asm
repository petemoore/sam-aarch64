; tbn_render_driver.asm — standalone driver for the `.tbn` → source-text
; renderer (i365a slice 1).
;
; org &8000; the harness deposits this image into a section-C page (+ its
; section-D neighbour), build/disasm.bin into physical page DISASM_PAGE, and the
; `.tbn` into a CONTIGUOUS run of physical pages starting at IN_PAGE, then calls
; render_run.  A small fixture occupies one IN page; the full release `.tbn`
; (371 KB) spans IN_PAGE..IN_PAGE+22.
;
; The renderer reuses the shared record reader (src/reader.asm) and the
; disassembler (build/disasm.bin) via paged_call.  This driver supplies the
; multi-page IN window helpers and the handful of reader symbols the standalone
; build needs: the reader's byte primitives renormalise the section-A cursor
; across the run by bumping LMPR (in_normalise_hl), so the walk crosses every
; page of the `.tbn` transparently.  The 417 KB rendered text is NOT buffered —
; it streams out a port the harness captures (render_out_append, tbn_render.asm).
;
; Paging layout (S8 — multi-page IN, the full release `.tbn` spans ~23 pages):
;
;   Two distinct LMPR states rotate during the render:
;
;   (a) an IN-read state, LMPR = IN_POS_PAGE (RAM0 | current `.tbn` page):
;         Section A (&0000-&3FFF) = the current `.tbn` page
;         Section B (&4000-&7FFF) = the NEXT `.tbn` page (record-header straddle)
;       The reader roams the contiguous IN run (pages IN_PAGE..IN_PAGE+22) by
;       bumping LMPR as the cursor crosses &4000 (in_normalise_hl).
;
;   (b) a decode/handler state, LMPR = LMPR_DECODE:
;         Section A (&0000-&3FFF) = a scratch page (unread)
;         Section B (&4000-&7FFF) = PAGED_CALL_PAGE — paged_call body + comm
;       enctab_map_in restores this between record reads so render_decode_literal
;       finds paged_call + the comm buffer in section B.
;
;   Section C/D (HMPR-selected) always hold this driver + its resident tables;
;   paged_call swaps HMPR to DISASM_PAGE for the decode and restores it, so
;   section C/D return to the driver on every disasm return.
;
; paged_call MUST live outside the IN run (else a record read would land on it):
; it sits one page below the run at PAGED_CALL_PAGE, mapped to section B only by
; LMPR_DECODE.  The reader's IN-read state (LMPR = IN page) maps the next IN page
; into section B, never PAGED_CALL_PAGE.

                org     &8000

; Disassembler comm-buffer constants (DISASM_COMM_MNEM/OPS/PC, self-test slot).
                include "disasm_comm.inc"

; Record-kind constants (REC_KIND_INSN_RUN etc.), generated from the Go format.
                include "tbn_constants.inc"

; Disassembler decode target: top physical page (above the IN run), entry &8000.
DISASM_ENTRY:           equ     &8000
DISASM_PAGE:            equ     31

; IN paging: the `.tbn` is staged in a CONTIGUOUS run of physical pages starting
; at IN_PAGE.  IN_FIRST_LMPR maps the first `.tbn` page to section A; the reader
; bumps LMPR as the cursor advances, so section A follows pages IN_PAGE, IN_PAGE+1,
; … up the run (the full 371 KB release `.tbn` spans IN_PAGE..IN_PAGE+22).
IN_PAGE:                equ     8
IN_FIRST_LMPR:          equ     &20 | IN_PAGE   ; RAM0 bit + page 8 = &28

; paged_call body page + the decode/handler LMPR that maps it to section B.
; PAGED_CALL_PAGE is below the IN run; section B = LMPR page + 1, so
; LMPR_DECODE = RAM0 | (PAGED_CALL_PAGE - 1) puts PAGED_CALL_PAGE at section B.
PAGED_CALL_PAGE:        equ     7
LMPR_DECODE:            equ     &20 | (PAGED_CALL_PAGE - 1)     ; = &26

; STAGING_BUF — the reader's 1 KB record staging area (section D, HMPR-stable).
; Placed high in section D, above the driver's resident tables (which spill into
; section D for the full corpus) and below the relocated stack top (&FFFE).
STAGING_BUF:            equ     &E000
STAGING_BUF_END:        equ     &E400

; paged_call installation slots (mirror chain_paged_driver.asm / trampoline.asm).
TRAMPOLINE_DST:         equ     &7E00
PAGED_CALL_DST:         equ     TRAMPOLINE_DST + &40    ; = &7E40 (section B)
PAGED_CALL_HMPR_SAVE:   equ     TRAMPOLINE_DST + &D0    ; = &7ED0
PAGED_CALL_SP_SAVE:     equ     TRAMPOLINE_DST + &D1    ; = &7ED1
TRAMP_SAFE_SP:          equ     TRAMPOLINE_DST + 256    ; = &7F00
paged_call:             equ     PAGED_CALL_DST

; Reader header-table pass gate.  PASS_MODE = PASS_PASS1 makes reader_init seed
; the header label/local tables via the store callbacks (symbol_insert /
; local_def_append below) so the renderer can place them by PC.
PASS_PASS1:             equ     1


; ===========================================================================
; render_run — set up the render window, install paged_call, point the reader
; at the staged `.tbn`, and render it.
;
; Entry: the `.tbn` is resident from offset 0 of physical page IN_PAGE upward.
; Exit:  the source text streamed out RENDER_SINK_PORT (harness-captured), byte
;        count in render_out_len.  Boot LMPR/SP restored so the harness RET lands
;        on its planted HALT trap.
;
; The stack is relocated to section D (HMPR-stable) for the duration, because
; the render toggles LMPR (moving section B out from under the boot stack) —
; the same save/relocate/restore discipline as chain_paged_driver.asm.
; ===========================================================================
render_run:
                ; Phase 0: save boot LMPR + SP, relocate the stack high in
                ; section D (HMPR-stable, above the resident tables + STAGING_BUF).
                in      a, (250)
                ld      (render_saved_lmpr), a
                ld      hl, 0
                add     hl, sp
                ld      (render_saved_sp), hl
                ld      sp, &FFFE               ; section D (HMPR-stable) stack top

                ; Phase 1: map the decode state (section B = PAGED_CALL_PAGE) and
                ; install the paged_call body into that page.
                ld      a, LMPR_DECODE
                out     (250), a
                ld      hl, paged_call_body
                ld      de, PAGED_CALL_DST
                ld      bc, paged_call_body_end - paged_call_body
                ldir

                ; Phase 2: point the reader cursor at the first staged `.tbn` page.
                ld      a, IN_FIRST_LMPR
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
; (page+1, offset-&4000), bumping LMPR so section A follows the next physical
; page.  The IN run is contiguous, so a plain LMPR increment walks it; this is
; how a multi-page `.tbn` (the full release) reads across page boundaries.
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

; enctab_map_in — restore the decode/handler LMPR.  The reader tail-calls this
; after a record read; the render path has no ENCTAB, so it maps LMPR_DECODE,
; which keeps the paged_call body + comm buffer mapped into section B for the
; record handler's render_decode_literal.  (Section A becomes a scratch page —
; the handlers read the staged record from STAGING_BUF in section D, not the IN
; window, and the next reader_next_kind re-maps the IN page via in_map_current.)
enctab_map_in:
                di
                ld      a, LMPR_DECODE
                out     (250), a
                ret

; fail — the reader's error trap.  No valid render input reaches it; a HALT
; here (not the harness RET trap) makes an unexpected reader error visible.
fail:
                di
                halt


; ===========================================================================
; Reader header-table seed callbacks.  reader_init calls these once per header
; row (on pass 1); the renderer's store routines record {offset, name_id} /
; {offset, digit} for the PC-driven flush (render_flush).
; ===========================================================================
symbol_insert:          jp      render_store_label
local_def_append:       jp      render_store_local

PASS_MODE:              defb    PASS_PASS1      ; seed the header tables
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
                include "tbn_render_editor.asm"
                include "paged_bodies.asm"
