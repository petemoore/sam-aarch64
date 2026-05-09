; M0 stub assembler — opens IN, discards its contents, writes 4 NOP bytes to OUT.
;
; The 4 bytes written are the little-endian aarch64 encoding of NOP:
;   0xd503201f → bytes 1f 20 03 d5  (low byte first).
;
; SAMDOS calling convention (from docs/notes/sam-file-io.md, Task 6 spike):
;   - IX must point to UIFA at &4B00 before each RST 8 hook call.
;   - fill_uifa (in sam_io.inc) loads IX and populates the 48-byte buffer.
;   - Most hooks longjmp to BASIC on error; jp c, fail guards are dead code
;     for HGFLE/CFSM but are kept for defensive symmetry.
;   - HOFLE is the only hook that may return with CY set (name-in-use /
;     disk full) rather than longjmping — that CY is checked after create_output.
;   - SAMDOS has no explicit close-for-read; close_input is a no-op wrapper.
;   - LBYT longjmps on EOF; we must not call it past the last byte.
;     For M0 we skip reading/discarding the input: HGFLE opens it and positions
;     the read pointer, then HOFLE opens a fresh output channel — they are
;     independent, so abandoning the input read is safe.
;
; Note: pyz80 does not support the END directive. Assembly ends at EOF.
; The org directive sets the load address; the entry point is the first byte.

                org     &6000

; Jump table at the entry point: CALL 24576 lands on the first byte (&6000).
; (Note: SAMDOS itself lives at &8000+, so user code must avoid that range.)
; The subroutines from sam_io.inc follow immediately; start: is after them.
                jp      start

                include "sam_io.inc"

; -----------------------------------------------------------------------
; Main program — entry via jp from &8000.
; -----------------------------------------------------------------------

start:          di                     ; one-shot batch program; no interrupts needed

; -- open IN for reading -----------------------------------------------
; HERAZ (hook 166) longjmps if the file is absent, so we can't call it
; speculatively to erase a pre-existing OUT.  The test harness builds a
; fresh disk on every run, so OUT never pre-exists; this is safe for M0.
                ld      hl, name_IN
                call    fill_uifa      ; populate UIFA + set IX = &4B00
                call    open_input     ; RST 8 / DEFB 158 — longjmps on error

; -- skip reading IN (LBYT longjmps on EOF; we don't know the file length) --
; HGFLE and HOFLE use a single DOS channel but independently.  HOFLE will
; reset the channel state; abandoning the read is safe for M0.

; -- create OUT for writing -----------------------------------------------
                ld      hl, name_OUT
                call    fill_uifa      ; populate UIFA + set IX = &4B00
                call    create_output  ; RST 8 / DEFB 147 — CY on name conflict
                jp      c, fail        ; disk/dir full → abort with red border

; -- write the 4 NOP bytes to OUT ----------------------------------------
; aarch64 NOP little-endian: 1f 20 03 d5
                ld      hl, payload
                ld      b, 4
emit:           ld      a, (hl)
                call    write_byte     ; RST 8 / DEFB 148
                inc     hl
                djnz    emit

; -- close OUT (flush + finalise directory entry) -------------------------
                call    close_output   ; RST 8 / DEFB 152 — mandatory

; -- magic exit signal -----------------------------------------------------
; The patched SimCoupé's `-exitonhalt 1` flag detects an OUT to port &DEAD
; with value &C0 and quits cleanly. No real SAM hardware decodes port &DEAD,
; so this is unambiguous. HALT remains as a defence-in-depth fallback.
                ld      bc, &dead
                ld      a, &c0
                out     (c), a
                halt                   ; defence in depth

fail:           ld      a, &02         ; red border = error indicator for debug
                out     (&fe), a
                halt

; -----------------------------------------------------------------------
; Data
; -----------------------------------------------------------------------

; 4-byte aarch64 NOP (little-endian): low byte first.
payload:        defb    &1f, &20, &03, &d5

; UIFA name blocks: 1 byte type + 10-char space-padded name + 4-char ext.
; Type 19 = code file (matches samfile add -c used by build-disk.sh).
name_IN:        defb    19
                defm    "IN        "   ; 10 chars (I, N, 8 spaces)
                defm    "    "         ; 4-char ext (4 spaces)

name_OUT:       defb    19
                defm    "OUT       "   ; 10 chars (O, U, T, 7 spaces)
                defm    "    "         ; 4-char ext (4 spaces)
