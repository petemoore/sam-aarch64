; parse_paged_driver.asm — Main-image driver for the b8d brick-1 paged-parser
; smoke test (i48c-b8i).
;
; Assembled standalone at org &8000 (section C via default HMPR) with
; --importfile=build/asmparse_paged.sym so that parse_run, PARSE_RECPTR, and
; PARSE_ERR resolve to their addresses in the parser window image.
;
; The parser window image (build/asmparse_paged.bin) lives in physical pages 8
; and 9.  LMPR=&28 maps those to sections A and B:
;   Section A (&0000-&3FFF) = physical page 8: PARSE_RECS(&0000), LEX_SRC(&0800), SYM_NAMES(&1000)
;   Section B (&4000-&7FFF) = physical page 9: code + LEX_TOKS + LEX_STRPOOL
;
; The harness (koron-go/z80, harness.go) plants SP=&6FFE and a HALT trap at
; &7000 in the BOOT-mapped pages.  With FlatLMPR=&21, those land in section B
; = physical page 2.  An LMPR switch replaces page 2 with page 9 at section B,
; hiding the boot stack and halt trap.  The driver MUST therefore:
;   1. Save the current SP (which points into the boot-mapped section B).
;   2. Move SP to section D (&C0FE — HMPR-stable, unaffected by LMPR changes).
;   3. Switch LMPR, do the paged work, switch LMPR back.
;   4. Restore SP before the final RET so the RET pops the harness's return
;      address (&7000 in the restored boot-mapped section B) correctly.

                org     &8000

; ===========================================================================
; b8i_parse_paged — drive parse_run via LMPR=&28 from the main image.
;
; Entry: BC = source byte length (source already written to physical page 8
;             at offset LEX_SRC-&0000 = &0800 by the Go test).
; Exit:  B8I_PARSE_ERR = PARSE_ERR copy; B8I_RECCOUNT = record count (BC from
;        parse_run); B8I_RECLENGTH = PARSE_RECPTR value (= record stream byte
;        length, since PARSE_RECS = &0000 so length = PARSE_RECPTR - 0).
;        LMPR and SP restored to their entry values.
; ===========================================================================
b8i_parse_paged:
                ; --- Phase 0: save boot state, relocate stack ---
                in      a, (&fa)            ; read LMPR (port &FA)
                ld      (B8I_SAVED_LMPR), a ; save boot LMPR

                ld      hl, 0
                add     hl, sp              ; HL = SP (Z80 idiom for reading SP)
                ld      (B8I_SAVED_SP), hl  ; save boot SP (&6FFE from CallEntry)

                ld      sp, &C0FE           ; move stack to section D (HMPR-stable)
                                            ; page 4 (FlatHMPR=3 -> secD=page4)

                ; --- Phase 1: switch to parser window ---
                ld      a, &28              ; LMPR=&28: RAM0 bit + page8
                out     (&fa), a            ; secA=page8 (&0000), secB=page9 (&4000)
                                            ; BC is still = source byte length

                call    parse_run           ; BC in = src len; BC out = record count

                ; --- Phase 2: capture results while LMPR=&28 ---
                ; PARSE_ERR is in page9 (secB window), accessible at its map address.
                ld      a, (PARSE_ERR)
                ld      (B8I_PARSE_ERR), a  ; snapshot into section-C (HMPR-stable) cell

                ld      (B8I_RECCOUNT), bc  ; BC = record count from parse_run

                ; Record stream length = PARSE_RECPTR - PARSE_RECS.
                ; PARSE_RECS = equ &0000, so length = PARSE_RECPTR value directly.
                ld      hl, (PARSE_RECPTR)  ; HL = write-cursor end address
                ld      de, PARSE_RECS      ; DE = &0000 (equ from importfile)
                or      a                   ; clear CF
                sbc     hl, de              ; HL = length = PARSE_RECPTR - &0000
                ld      (B8I_RECLENGTH), hl

                ; --- Phase 3: restore boot state ---
                ld      a, (B8I_SAVED_LMPR)
                out     (&fa), a            ; restore LMPR (secA/B back to boot pages)

                ld      hl, (B8I_SAVED_SP)
                ld      sp, hl              ; restore SP (&6FFE) so RET pops haltTrap

                ret                         ; pops &7000 -> HALT (harness trap)

; ===========================================================================
; Driver state cells (section C, HMPR-stable across the LMPR window).
; ===========================================================================
B8I_SAVED_LMPR: defs 1          ; boot LMPR saved across the window
B8I_SAVED_SP:   defs 2          ; boot SP saved across the window
B8I_PARSE_ERR:  defs 1          ; copy of PARSE_ERR captured inside the window
B8I_RECCOUNT:   defs 2          ; record count returned by parse_run (BC)
B8I_RECLENGTH:  defs 2          ; record stream byte length (PARSE_RECPTR - PARSE_RECS)
