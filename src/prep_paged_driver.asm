; prep_paged_driver.asm — main-image driver for the paged prep harness (i370).
;
; Assembled standalone at org &8000 (section C via default HMPR) with
; --importfile=build/asmprep_paged.sym so prep_run, PREP_ERR, PREP_OUT,
; PREP_SRC, PREP_FILES, PREP_PATH, etc. resolve to their addresses in the
; preprocessor window image.
;
; The prep window image (build/asmprep_paged.bin) lives in physical pages 8
; and 9. LMPR=&28 maps those to sections A and B:
;   Section A (&0000-&3FFF) = physical page 8: PREP_OUT(&0000), PREP_SRC(&2000),
;                                              PREP_FILES(&3000)
;   Section B (&4000-&7FFF) = physical page 9: prep code + the small buffers
;                                              (SET_TAB.. + PREP_PATH/PREP_INCDIRS)
;
; The harness (koron-go/z80) plants SP + a HALT trap in the BOOT-mapped section B
; (page 2 at the flat LMPR). An LMPR switch replaces page 2 with page 9 at
; section B, hiding the boot stack + trap. So the driver, exactly like
; parse_paged_driver.asm:
;   1. saves the boot LMPR + SP,
;   2. moves SP to section D (&C0FE — HMPR-stable, unaffected by LMPR),
;   3. switches LMPR, runs prep_run, switches LMPR back,
;   4. restores SP before RET so the RET pops the harness's return address.
;
; This proves prep runs paged with the DEFAULT memory reader (prep_mem_reader,
; reading PREP_FILES in section A); section C/D stay free — the room i371's real
; reader needs for its trampoline-HLOAD.

                org     &8000

; ===========================================================================
; prep_paged_run — drive prep_run via LMPR=&28 from the main image.
;
; Entry: BC = source byte length. The source is pre-planted by the test into
;        physical page 8 at PREP_SRC (&2000); PREP_FILES/PREP_NFILES/
;        PREP_INCDIRS/PREP_NINCDIRS/PREP_PATH are pre-planted for the memory
;        reader (page 8 / page 9 at their sym addresses).
; Exit:  PPD_ERR = PREP_ERR copy; PPD_OUTLEN = expanded byte count (BC from
;        prep_run). Expanded text is in PREP_OUT (physical page 8, &0000), read
;        by the test. LMPR and SP restored to their entry values.
; ===========================================================================
prep_paged_run:
                ; --- Phase 0: save boot state, relocate stack ---
                in      a, (&fa)            ; read LMPR (port &FA)
                ld      (PPD_SAVED_LMPR), a
                ld      hl, 0
                add     hl, sp              ; HL = SP
                ld      (PPD_SAVED_SP), hl
                ld      sp, &C0FE           ; section D (HMPR-stable) stack

                ; --- Phase 1: switch to the prep window, run prep_run ---
                ld      a, &28              ; LMPR=&28: secA=page8, secB=page9
                out     (&fa), a            ; BC still = source byte length
                call    prep_run            ; BC in = src len; BC out = out bytes

                ; --- Phase 2: capture results while LMPR=&28 ---
                ld      (PPD_OUTLEN), bc    ; expanded byte count
                ld      a, (PREP_ERR)       ; page-9 (section B) — mapped now
                ld      (PPD_ERR), a

                ; --- Phase 3: restore boot state ---
                ld      a, (PPD_SAVED_LMPR)
                out     (&fa), a
                ld      hl, (PPD_SAVED_SP)
                ld      sp, hl
                ret                         ; pops the harness HALT trap

; ===========================================================================
; Driver state cells (section C, HMPR-stable across the LMPR window).
; ===========================================================================
PPD_SAVED_LMPR: defs 1
PPD_SAVED_SP:   defs 2
PPD_ERR:        defs 1          ; copy of PREP_ERR captured inside the window
PPD_OUTLEN:     defs 2          ; expanded byte count returned by prep_run (BC)
