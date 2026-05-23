; M3 Z80 assembler — top-level.
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.1.
;
; Boot via M0's BASIC autorun pattern:
;   CLEAR&7FFF: LOAD CODE "assembler" 32768: CALL 32768
;
; Layout (per spec §2.1):
;   &8000-&BFFF  assembler code (this file + includes)
;   &C000-&C0FF  stack (grows down from &C100)
;   &C100+       enctab.enc buffer
;
; Note: pyz80 does not support the END directive. Assembly ends at EOF.
; The org directive sets the load address; the entry point is the first byte.

                org     &8000

; Jump table at the entry point: CALL 32768 lands on the first byte (&8000).
; Pattern mirrors M0's src/stub.asm.
                jp      start

                include "io.asm"
                include "loader.asm"
                include "slots/xreg.asm"
                include "slots/imm_small.asm"
                include "test_slots.asm"

; -----------------------------------------------------------------------
; Main program — entry via jp from &8000.
; -----------------------------------------------------------------------

start:
                di                     ; disable interrupts (batch program)

; Set up the stack before any call.  SAMDOS's EI in the RST 8 hook
; re-enables interrupts, so DI must be repeated after hook calls.
; SP must be valid before any CALL; loader.asm also sets it, but we
; want it correct for the call itself.
; Stack lives at &C000-&C0FF (section D, HMPR+1 page — always writable).
                ld      sp, &C100

; -- Per-slot encoder self-tests ----------------------------------------
; Exercises encode_reg against hardcoded slot records (no disk I/O).
; On any mismatch: jp fail (red border + halt).  See test_slots.asm.
                call    run_slot_self_tests

; -- Load and validate enctab.enc header --------------------------------
; load_enctab: opens the file, reads the first 8 bytes, validates magic
; "ENC1" and version=1.  On mismatch it jp fail (red border + halt).
; On success, returns with HL = ENCTAB_BUF.
                call    load_enctab

; -- Header valid: signal clean success ----------------------------------
; The DI at start: is undone by SAMDOS's EI inside the RST 8 hook window
; (ROM PTDOS does EI before dispatching — per src/stub.asm citation).
; Re-issue DI so HALT with IFF1=0 triggers SimCoupé's -exitonhalt.
                di
                halt


; -----------------------------------------------------------------------
; fail — error indicator: red border, then halt.
; -----------------------------------------------------------------------
; SimCoupé port &FE bit 0-2 sets SAM border colour.
; Value 2 = red (SAM palette colour 2 = red border).
; Citation: SAM Coupé Technical Manual §7 (ULA port &FE).
fail:           ld      a, 2
                out     (&fe), a       ; SAM border port — red
                di
                halt
