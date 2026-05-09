; SAM disk file I/O wrappers.
; Derived from docs/notes/sam-file-io.md (M0 Task 6 spike).
;
; Convention used by SAMDOS / MasterDOS / Pro-DOS: applications enter the
; OS via the Z80 RST 8 instruction followed by an immediate "hook code"
; byte, e.g.
;
;     ld   ix, &4B00          ; UIFA (header buffer) at start of bank 1
;     rst  8
;     defb 158                ; HGFLE — get file (open for reading)
;
; When DOS is loaded into RAM, it intercepts RST 8 and dispatches on the
; following byte. Hook codes < 128 are SAM ROM error reports; codes 128+
; are file-system operations. The hook table is in samdos/src/b.s::samhk
; (line 497+); concrete implementations live in c.s, f.s, h.s. See the
; spike doc for full register conventions and gotchas.
;
; Buffer addresses we use:
;   UIFA     = &4B00  ; 48-byte user-information area: name + type + addr
;                     ;        + length, populated before HOFLE / HGFLE.
;
; SAMDOS error handling: most hooks longjmp to BASIC on error — they do
; NOT return to the caller. CY on error is only set by HOFLE (name
; conflict / disk full). All other errors cause a BASIC error message and
; longjmp. See spike doc for details.
;
; All routines preserve registers unless documented otherwise.


; -----------------------------------------------------------------------
; Constants — UIFA layout and SAMDOS hook codes.
; -----------------------------------------------------------------------

UIFA:           equ     &4B00          ; 48-byte header buffer in bank 1.
                                       ; Caller fills, points IX here, RST 8s.

; UIFA byte offsets (subset; see spike doc for full layout).
UIFA_TYPE:      equ     0              ; file type (19 = code, 16 = BASIC).
UIFA_NAME:      equ     1              ; 10 bytes, space-padded.
UIFA_EXT:       equ     11             ; 4 bytes, default 4 spaces.
UIFA_PAGE:      equ     31             ; start page (mod 16K).
UIFA_LOAD:      equ     32             ; load address LE word.
UIFA_PAGES:     equ     34             ; page count.
UIFA_LENGTH:    equ     35             ; length-mod-16K LE word.

; SAMDOS hook codes (samdos/src/b.s::samhk). Numbered from 128.
HOOK_HERAZ:     equ     166            ; erase file by name.
HOOK_HOFLE:     equ     147            ; open file for writing (creates).
HOOK_SBYT:      equ     148            ; save one byte (buffered).
HOOK_CFSM:      equ     152            ; close output (flushes + dir update).
HOOK_HGFLE:     equ     158            ; open file for reading.
HOOK_LBYT:      equ     159            ; load one byte (buffered).


; -----------------------------------------------------------------------
; fill_uifa — copy 15 bytes from (HL) into UIFA, pad bytes 15-47 with &FF.
;
; Input:  HL = pointer to 15-byte name block: 1 type + 10 name + 4 ext.
; Output: UIFA at &4B00 is populated; IX = &4B00.
; Clobbers: A, BC, DE, HL (via LDIR).
; -----------------------------------------------------------------------
fill_uifa:      ld      de, UIFA
                ld      bc, 15
                ldir                   ; copy type + name (10) + ext (4) = 15 bytes
                ld      a, &FF
                ld      b, 48 - 15     ; fill remaining 33 bytes with &FF
fu1:            ld      (de), a
                inc     de
                djnz    fu1
                ld      ix, UIFA       ; point IX at UIFA ready for RST 8
                ret


; -----------------------------------------------------------------------
; open_input — open existing file for reading (HGFLE, hook 158).
;
; Precondition: fill_uifa has been called with the input file's name block.
; After return: first sector in DOS buffer, read pointer at start of payload.
; On error: SAMDOS longjmps to BASIC — does not return to caller.
; -----------------------------------------------------------------------
open_input:     rst     8
                defb    HOOK_HGFLE     ; 158: HGFLE — open file for reading
                ret


; -----------------------------------------------------------------------
; read_byte — fetch next byte from currently open input file.
;
; Output: A = byte read.
; On EOF or error: SAMDOS longjmps ("End of file" report) — never returns.
; Precondition: open_input has been called.
; -----------------------------------------------------------------------
read_byte:      rst     8
                defb    HOOK_LBYT      ; 159: LBYT — load one byte
                ret


; -----------------------------------------------------------------------
; close_input — release the input file.
;
; SAMDOS 2 has no explicit close-for-read hook (HCLOS stub at code 135
; is a bare RET). This wrapper is a no-op for SAMDOS but is kept as a
; named entry point for future MasterDOS support.
; -----------------------------------------------------------------------
close_input:    ret


; -----------------------------------------------------------------------
; create_output — create a new file for streaming write (HOFLE, hook 147).
;
; Precondition: fill_uifa called with output file's name block.
;               HERAZ should be called first to erase any existing file so
;               that HOFLE does not prompt interactively (see spike doc).
; After return: directory entry reserved, header written, buffer ready.
; On error (disk/dir full): CY set — HOFLE is the only hook that returns
;   CY instead of longjmping (see spike doc, ofsm / cyes).
; -----------------------------------------------------------------------
create_output:  rst     8
                defb    HOOK_HOFLE     ; 147: HOFLE — open file for writing
                ret


; -----------------------------------------------------------------------
; write_byte — write A to currently open output file (buffered).
;
; Input: A = byte to write.
; On disk-full / I/O error: SAMDOS longjmps — does not return.
; -----------------------------------------------------------------------
write_byte:     rst     8
                defb    HOOK_SBYT      ; 148: SBYT — save one byte
                ret


; -----------------------------------------------------------------------
; close_output — flush buffer and finalise directory entry (CFSM, hook 152).
;
; Must be called or the directory entry stays "open" and the file is
; unreadable on the next boot.
; -----------------------------------------------------------------------
close_output:   rst     8
                defb    HOOK_CFSM      ; 152: CFSM — close/flush output file
                ret
