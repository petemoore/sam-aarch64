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
; All routines preserve registers unless documented otherwise.
;
; (Bodies filled in M0 Task 7 once stub.asm needs them.)


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
HOOK_HOFLE:     equ     147            ; open file for writing (creates).
HOOK_SBYT:      equ     148            ; save one byte (buffered).
HOOK_CFSM:      equ     152            ; close output (flushes + dir update).
HOOK_HGFLE:     equ     158            ; open file for reading.
HOOK_LBYT:      equ     159            ; load one byte (buffered).


; -----------------------------------------------------------------------
; open_input — opens file whose name+type are pre-filled in UIFA at &4B00,
;              for reading. After return: the first sector has been read
;              into the DOS buffer, RAM pointer is at start of payload.
;              CY = 0 on success, CY = 1 + error in A on failure (e.g.
;              file not found = error 26 → SAMDOS report 107).
; -----------------------------------------------------------------------
open_input:     ; TODO (Task 7): rst 8 / defb HOOK_HGFLE with IX = UIFA.
                ret

; -----------------------------------------------------------------------
; read_byte — fetch next byte of currently open input file.
;             A = byte read. CY set on EOF / error.
;             Internally: rst 8 / defb HOOK_LBYT (148-1 → 159).
; -----------------------------------------------------------------------
read_byte:      ; TODO (Task 7): rst 8 / defb HOOK_LBYT.
                ret

; -----------------------------------------------------------------------
; close_input — release the input file. Note: SAMDOS has no explicit
;               close-for-read entry point; on real SAMDOS this is a
;               no-op (the buffer is reused on next open). On MasterDOS
;               there is one; for portability we keep the wrapper as a
;               named entry point even though the body may be empty.
; -----------------------------------------------------------------------
close_input:    ; TODO (Task 7): no-op on SAMDOS; preserve wrapper for
                ;                future MasterDOS support.
                ret

; -----------------------------------------------------------------------
; create_output — create file whose name+type are pre-filled in UIFA, for
;                 writing. Allocates a new directory entry, finds free
;                 sectors, writes header. CY on failure (disk full,
;                 directory full, name in use → user prompted).
;                 Internally: rst 8 / defb HOOK_HOFLE.
; -----------------------------------------------------------------------
create_output:  ; TODO (Task 7): rst 8 / defb HOOK_HOFLE with IX = UIFA.
                ret

; -----------------------------------------------------------------------
; write_byte — write A to currently open output file (buffered through
;              the DOS sector buffer; flushed automatically when full or
;              by close_output).
;              CY set on disk-full / I/O error.
;              Internally: rst 8 / defb HOOK_SBYT.
; -----------------------------------------------------------------------
write_byte:     ; TODO (Task 7): rst 8 / defb HOOK_SBYT.
                ret

; -----------------------------------------------------------------------
; close_output — flush remaining buffer to disk, write/finalise the
;                file's sector-address-map directory entry.
;                Must be called or the file's directory entry stays
;                marked "open" and the file is unreadable.
;                Internally: rst 8 / defb HOOK_CFSM.
; -----------------------------------------------------------------------
close_output:   ; TODO (Task 7): rst 8 / defb HOOK_CFSM.
                ret
