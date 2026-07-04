; prep_reader_driver.asm — standalone SAM-fidelity-harness driver for the real
; `.include` reader, prep_sam_reader (i31b-b3b).
;
; org &8000; the harness (tools/z80-test-harness-go) deposits this image into
; section-C page 2 (+ section-D page 3), serves the requested include file via
; HGTHD/HLOAD, and models the DOSER (&5BC0) post-hook dispatch — the exact
; substrate the reader's not-found bracket needs. Boot installs the HLOAD
; trampoline, points the reader at a fixed request name ("MACROS"), calls
; prep_sam_reader, and reports the outcome on the printer channel so the Go test
; reads it from Result.PrinterCapture:
;
;   hit  → "H"; the loaded bytes land in
;          PREP_READER_PAGE (physical page 9), which the test reads directly.
;   miss → "M" (the DOSER bracket converted a file-not-found to CF=0 without a
;          BASIC longjmp).
;
; The reader (src/prep_sam_reader.asm) is includer-agnostic: this driver supplies
; the REQ_NAME_*/READ_CONTENT_* state cells + PREP_READER_PAGE; the real paged
; build (i370) supplies them from asmprep.asm + the PP_PREP pool.

                org     &8000

; The reader HLOADs the include into this physical page. Must avoid the driver's
; own section-C/D pages (2/3) and the harness's IN/enctab pages — page 9 is free
; (the samdos DOSER tests use it as a served-file target).
PREP_READER_PAGE:       equ     9

; ---------------------------------------------------------------------------
; Boot entry (harness enters at &8000).
; ---------------------------------------------------------------------------
prep_reader_boot:
                di
                ld      sp, &6FFE               ; section-B (LMPR-stable) stack,
                                                ; below the trampoline at &7E00

                call    enctab_trampoline_setup ; install HLOAD trampoline @ &7E00

                ; Point the reader at the fixed request name.
                ld      hl, prb_req_name
                ld      (REQ_NAME_PTR), hl
                ld      hl, prb_req_name_end - prb_req_name
                ld      (REQ_NAME_LEN), hl

                call    prep_sam_reader
                jr      c, prb_hit

                ; miss
                ld      a, "M"
                call    prb_putc
                jr      prb_done

prb_hit:
                ld      a, "H"
                call    prb_putc

prb_done:
                di
                halt

; prb_putc — emit A on the harness printer channel (Result.PrinterCapture).
prb_putc:
                out     (&E8), a                ; character
                ld      a, 1
                out     (&E9), a                ; strobe
                ret

; A non-"IN"-prefixed name: the harness treats any "IN…" name as its legacy
; pre-deposited IN file (harness.go HGTHD path), which would mask a real miss.
prb_req_name:   defm    "MACROS"
prb_req_name_end:

; ---------------------------------------------------------------------------
; Includes — the reader's substrate.
; ---------------------------------------------------------------------------
                include "sam_io.inc"            ; fill_uifa, UIFA, HOOK_HGTHD
                include "trampoline.asm"        ; TRAMPOLINE_DST + trampoline_body
                include "paged_bodies.asm"      ; paged_call_body (enctab_trampoline_setup refs it)
                include "prep_sam_reader.asm"   ; prep_sam_reader (uses the cells below)

; ---------------------------------------------------------------------------
; Reader I/O state (asmprep.asm provides these in the real paged build).
; ---------------------------------------------------------------------------
REQ_NAME_PTR:           defw    0
REQ_NAME_LEN:           defw    0
READ_CONTENT_PTR:       defw    0
READ_CONTENT_LEN:       defw    0
