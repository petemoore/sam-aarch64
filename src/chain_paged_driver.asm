; chain_paged_driver.asm — Main-image driver for the b8d brick-2 paged chain
; (i48c-b8j): text → parse_run → pass1_ir_walk → compact_ir_walk (skeleton
; INST arm), entirely on-Z80 over the paged arrangement.
;
; Built at org &8000 with CHAIN_PAGED_DRIVER=1, which adjusts the included
; harnesses for the paged context:
;
;   test_pass1_ir.asm: PASS1_IR_BUF is an equ alias of &0000 (the PARSE_RECS
;   page-8 window address) instead of the flat 10752-byte defs buffer; the IR is
;   read through the live section A window while LMPR=&28.
;
;   test_compact_ir.asm: COMPACT_LABELROWS/COMPACT_LOCALROWS relocate from the
;   flat low-RAM block (&5000/&5400 — inside the page-9 window at &4000-&7FFF
;   when LMPR=&28) into page-8 spare (&1200/&1500 — after PARSE_RECS &0000,
;   LEX_SRC &0800, SYM_NAMES &1000).  The walk writes them via section A;
;   the Go test reads them directly from pager.RAM[8].
;
; The parser image (build/asmparse_paged.bin) lives in physical pages 8 and 9.
; LMPR=&28 maps them to sections A and B:
;   Section A (&0000-&3FFF) = physical page 8: PARSE_RECS(&0000), LEX_SRC(&0800), SYM_NAMES(&1000)
;   Section B (&4000-&7FFF) = physical page 9: lexer+parser code + LEX_TOKS + LEX_STRPOOL
;
; Cross-image symbols (parse_run, PARSE_RECPTR, PARSE_ERR, PARSE_RECS) resolve
; via --importfile=build/asmparse_paged.sym (the same .sym the b8i driver uses).
;
; The pass1 and compact walks run from this main image (section C/D via HMPR)
; with LMPR=&28 kept live throughout both walks, so the IR at &0000 is always
; readable through section A.  Section C (&8000-&BFFF) and section D
; (&C000-&FFFF) are controlled by HMPR and are unaffected by LMPR changes —
; the pass-1 leaf tables at &C100-&EFFF and the compact state at &F000-&FFFF
; are stable for the entire run.
;
; The harness (koron-go/z80) plants SP=&6FFE and a HALT trap at &7000 in the
; boot-mapped section B (page 2 at FlatLMPR=&21).  An LMPR switch replaces
; page 2 with page 9, hiding both; the driver must save the boot SP, relocate
; to the HMPR-stable section D stack at &C0FE, do the paged work, then restore
; SP before RET (mirrors the b8i driver pattern exactly).

                org     &8000

                include "test_compact_ir.asm"   ; includes test_pass1_ir.asm


; ===========================================================================
; b8j_chain_paged — drive parse → pass1 → compact walk via LMPR=&28.
;
; Entry: BC = source byte length (source already written to physical page 8
;             at offset &0800 = LEX_SRC by the Go test).
; Exit:  Compact outputs in COMPACT_SIDECAR / COMPACT_GLOBALS / COMPACT_RECS
;        (section D, HMPR-stable; readable via mac.Read after RET) and
;        COMPACT_LABELROWS / COMPACT_LOCALROWS (physical page 8 at &1200/&1500;
;        readable via pager.RAM[8] after RET — NOT via mac.Read, which maps
;        the restored section A = page 1, not page 8).
;        LMPR and SP restored to their entry values.
; On parse error: hits the fail trap with tag &f1 (PARSE_ERR non-zero).
; ===========================================================================
b8j_chain_paged:
                ; --- Phase 0: save boot state, relocate stack ---
                in      a, (&fa)
                ld      (B8J_SAVED_LMPR), a

                ld      hl, 0
                add     hl, sp
                ld      (B8J_SAVED_SP), hl

                ld      sp, &C0FE               ; move stack to section D (HMPR-stable)

                ; --- Phase 1: switch to parser window, run parse_run ---
                ld      a, &28                  ; LMPR=&28: secA=page8 (&0000), secB=page9 (&4000)
                out     (&fa), a

                call    parse_run               ; BC in = src len; BC out = record count

                ; Check parse error flag (in page9, readable via section B)
                ld      a, (PARSE_ERR)
                or      a
                jr      nz, b8j_parse_err

                ; PASS1_IR_LEN := PARSE_RECPTR - PARSE_RECS
                ; PARSE_RECS = equ &0000 = PASS1_IR_BUF, so length = PARSE_RECPTR directly.
                ld      hl, (PARSE_RECPTR)
                ld      de, PARSE_RECS          ; &0000
                or      a                       ; clear CF
                sbc     hl, de
                ld      (PASS1_IR_LEN), hl      ; write into main-image cell before walk

                ; --- Phase 2: pass1 + compact (LMPR=&28 kept live throughout) ---
                call    compact_ir_walk         ; reads IR from &0000, writes state to &F000+

                ; --- Phase 3: restore boot state ---
b8j_restore:
                ld      a, (B8J_SAVED_LMPR)
                out     (&fa), a                ; restore LMPR (section A back to page 1)

                ld      hl, (B8J_SAVED_SP)
                ld      sp, hl                  ; restore SP (&6FFE) so RET pops haltTrap (&7000)

                ret

b8j_parse_err:
                ld      a, &f1
                jp      fail_with_tag           ; tag &f1: PARSE_ERR non-zero in chain driver


; ===========================================================================
; Driver state cells (section C, HMPR-stable across the LMPR window).
; ===========================================================================
B8J_SAVED_LMPR: defs 1          ; boot LMPR saved across the window
B8J_SAVED_SP:   defs 2          ; boot SP saved across the window
