; loader.asm — enctab.enc reader and header validator.
;
; Reads the encoder-table file from disk via HGFLE+LBYT, validates the
; magic "ENC1" and version=1, and stops at validation failure.
;
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.3.
; Format citation: docs/specs/2026-05-24-m2-encoder-tables-design.md §2.
;
; Memory layout used here:
;   &C000-&C0FF  stack (SP = &C100, grows down)
;   &C100+       enctab buffer (holds entire enctab.enc file)
;
; &C000–&FFFF is section D, HMPR+1 page — always writable RAM.
; enctab.enc is ~4 KB (975 bytes in the current M2 table); section D
; gives ~16 KB headroom, sufficient for any M3 table.
;
; UIFA name block convention (from src/sam_io.inc / M0's stub.asm):
;   1 byte   type     (19 = code file, FT_CODE)
;   10 bytes name     space-padded to 10 chars
;   4 bytes  ext      space-padded to 4 chars (unused on SAM)
; samfile v3 addFile() pads the name field the same way
; (samfile.go:948: `name + "          "` truncated to 10 bytes).
;
; IMPORTANT: LBYT calls (RST 8 / &9F) clobber the B register.
; The ROM PTDOS dispatcher (rom-v3.0_annotated-disassembly.txt:12944-12978,
; step 1: "Read prev LMPR into B") sets B to the current LMPR value before
; switching to the SAMDOS stack.  SAMDOS does not restore the caller's BC
; (d.s:284-289 bcr is the normal return path; it does not load from hkbc).
; Citation: docs/notes/sam-stub-audit.md §"The dispatch path".
; Consequence: do NOT use B as a loop counter around call read_byte.


; -----------------------------------------------------------------------
; Constants
; -----------------------------------------------------------------------

ENCTAB_BUF:     equ     &C100          ; load enctab.enc body here
STACK_TOP:      equ     &C100          ; SP before any call (grows down)


; -----------------------------------------------------------------------
; load_enctab — open enctab.enc, read first 8 bytes into ENCTAB_BUF,
;               validate header.
;
; Input:  none
; Output: HL = ENCTAB_BUF (pointer to validated enctab data)
; On mismatch: jp fail (red border + halt)
; Clobbers: A, DE, HL (B is intentionally not used as a loop counter;
;           see IMPORTANT note above).
; -----------------------------------------------------------------------
load_enctab:
; SP must be set by the caller (start: in assembler.asm) before calling here.
; Precondition: SP = STACK_TOP (&C100), set in assembler.asm start:

; -- Open enctab.enc for reading via HGFLE (hook 158) -------------------
                ld      hl, name_enctab
                call    fill_uifa      ; populate UIFA + IX = &4B00
                call    open_input     ; RST 8 / HOOK_HGFLE; longjmps on error

; -- Read first 8 header bytes into ENCTAB_BUF --------------------------
; LBYT reads one byte into A; longjmps ("End of file") on EOF.
; We read exactly 8 bytes (magic[4] + version[2] + flags[2]).
; Do NOT use DJNZ here: each call read_byte clobbers B via PTDOS dispatch.
; (Citation: IMPORTANT note at top of this file.)
                ld      de, ENCTAB_BUF

; magic[0]
                call    read_byte
                ld      (de), a
                inc     de

; magic[1]
                call    read_byte
                ld      (de), a
                inc     de

; magic[2]
                call    read_byte
                ld      (de), a
                inc     de

; magic[3]
                call    read_byte
                ld      (de), a
                inc     de

; version low byte
                call    read_byte
                ld      (de), a
                inc     de

; version high byte
                call    read_byte
                ld      (de), a
                inc     de

; flags low byte (reserved, not validated)
                call    read_byte
                ld      (de), a
                inc     de

; flags high byte (reserved, not validated)
                call    read_byte
                ld      (de), a
                inc     de

; -- Validate magic "ENC1" -----------------------------------------------
; pyz80 character literals use double-quoted single chars: "E" = ord('E') = 69.
; Citation: pyz80 source (pyz80.py:436: char literal substitution via double quotes).
                ld      hl, ENCTAB_BUF
                ld      a, (hl)
                cp      "E"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "N"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "C"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "1"
                jp      nz, fail

; -- Validate version = 1 ------------------------------------------------
; Version is u16 LE at bytes 4-5 of the file (ENCTAB_BUF+4, ENCTAB_BUF+5).
; Expect version_lo = 1, version_hi = 0.
; Citation: docs/specs/2026-05-24-m2-encoder-tables-design.md §2.
                inc     hl             ; hl = ENCTAB_BUF + 4 (version_lo)
                ld      a, (hl)
                cp      1
                jp      nz, fail
                inc     hl             ; hl = ENCTAB_BUF + 5 (version_hi)
                ld      a, (hl)
                or      a              ; expect 0
                jp      nz, fail

; -- Header validated.  Return HL = ENCTAB_BUF. -------------------------
                ld      hl, ENCTAB_BUF
                ret


; -----------------------------------------------------------------------
; UIFA name block for "enctab.enc"
; -----------------------------------------------------------------------
; Type 19 = FT_CODE.  Name is "enctab.enc" — exactly 10 characters so no
; trailing spaces needed.  Extension field is 4 spaces (unused on SAM).
; Byte layout: [type:1][name:10][ext:4] = 15 bytes total, as required by
; fill_uifa in src/sam_io.inc.
name_enctab:    defb    19
                defm    "enctab.enc"   ; 10 chars
                defm    "    "         ; 4-char ext (4 spaces)
