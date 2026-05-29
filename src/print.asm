; print.asm — status reporting via the SAM Coupé parallel printer port.
;
; The assembler emits a status banner ("OK\n" on success, "FAIL\n" on
; failure) over printer channel 1.  SimCoupé routes this to a file
; (via `-parallel1 1 -outpath ... -nextfile 0`), and the test wrapper
; reads it to determine pass/fail without waiting on a 30-second
; spin-then-timeout.
;
; SAM Coupé hardware (per SimCoupé Base/SAMIO.h):
;
;   PRINTL1_DATA_PORT  &E8   — byte to print (latched on strobe rise)
;   PRINTL1_STAT_PORT  &E9   — bit 0 is the strobe line:
;                              rising edge (0→1) latches the data byte
;                              into the printer's input buffer.
;
; The Centronics-style handshake we model is a write of the data byte
; followed by a strobe high then strobe low.  SimCoupé's PrintBuffer
; (Base/Parallel.cpp) only records the byte on the rising edge
; (`m_bControl ^ bVal_` test), so a single rise is enough; the
; trailing low is good citizenship for any future real-hardware run.

PRINT_DATA_PORT:        equ     &E8
PRINT_STAT_PORT:        equ     &E9


; -----------------------------------------------------------------------
; print_status_char — write one byte to printer channel 1.
;
; Input:    A = byte to print
; Output:   none
; Clobbers: F (preserves A, BC, DE, HL).
; -----------------------------------------------------------------------
print_status_char:
                push    af
                out     (PRINT_DATA_PORT), a    ; latch data byte
                ld      a, 1
                out     (PRINT_STAT_PORT), a    ; strobe high (rising edge)
                xor     a
                out     (PRINT_STAT_PORT), a    ; strobe low (good hygiene)
                pop     af
                ret


; -----------------------------------------------------------------------
; print_status_string — write a NUL-terminated string to printer ch 1.
;
; Input:    HL = pointer to ASCIZ string
; Output:   HL advanced past the trailing NUL
; Clobbers: A, F.  Preserves BC, DE.
; -----------------------------------------------------------------------
print_status_string:
                ld      a, (hl)
                or      a
                ret     z
                call    print_status_char
                inc     hl
                jr      print_status_string


; -----------------------------------------------------------------------
; Status banners.  Trailing newline lets a `grep -q '^OK$'` test pass
; against the wrapper's captured file regardless of platform line
; endings.
; -----------------------------------------------------------------------
msg_ok:         defm    "OK"
                defb    10, 0
; NB: msg_fail has no trailing newline; the fail routine in assembler.asm
; emits a 2-digit hex tag (LAST_FAIL_TAG) and then the newline so failure
; sites can be distinguished.  grep '^FAIL' still matches.
msg_fail:       defm    "FAIL"
                defb    0
