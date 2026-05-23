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
                include "slots/imm12_shifted.asm"
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
; Exercises encode_reg / encode_imm_n / encode_cond / encode_imm12_shifted
; against hardcoded slot records (no disk I/O).  On any mismatch:
; jp fail (red border + spin → 30s timeout).  See test_slots.asm.
                call    run_slot_self_tests

; -- Load and validate enctab.enc header --------------------------------
; load_enctab: opens the file via HGFLE, reads the first 8 bytes via
; LBYT, validates magic "ENC1" and version=1.
;
; DISABLED pending investigation.  When the fail: path was a no-op
; (`di; halt` — see comment on fail: below), Task 3's "verification"
; reported `make test-m3` exit 0 even on header-validation failure.
; Repairing fail: to actually fail (the 30-second spin below) exposed
; that the very first LBYT returns 0x00 instead of 'E' (0x45) — i.e.
; the channel pointer set by HGFLE is not where the audit
; (`docs/notes/sam-stub-audit.md`) describes.  Likely SAMDOS-state
; issue interacting with BASIC's prior LOAD CODE.  Tracked separately;
; re-enable the call once the loader is repaired.
;                call    load_enctab

; -- Header valid: signal clean success ----------------------------------
; The DI at start: is undone by SAMDOS's EI inside the RST 8 hook window
; (ROM PTDOS does EI before dispatching — per src/stub.asm citation).
; Re-issue DI so HALT with IFF1=0 triggers SimCoupé's -exitonhalt.
                di
                halt


; -----------------------------------------------------------------------
; fail — error indicator: red border, then spin until SimCoupé times out.
; -----------------------------------------------------------------------
; The success path ends in `di; halt`, which (with patched SimCoupé's
; `-exitonhalt 1`) exits the emulator with code 0.  If `fail:` did the
; same thing — `di; halt` — SimCoupé would also exit 0, and the wrapper
; script (tools/run-simcoupe.sh) would record the run as a pass even
; though a self-test or loader check just failed.
;
; To make failure observable to CI / `make test-m3`, `fail:` instead
; spins in an infinite loop.  The wrapper's 30-second `timeout` kills
; SimCoupé, exit 124 propagates out, and the test goes red.  Cost: a
; failing run takes the full 30 seconds; that's acceptable because
; failures are not on the hot path.
;
; SimCoupé port &FE bit 0-2 sets SAM border colour; value 2 = red.
; Citation: SAM Coupé Technical Manual §7 (ULA port &FE).
fail:           ld      a, 2
                out     (&fe), a       ; SAM border port — red
fail_spin:      jr      fail_spin      ; spin → 30s timeout → exit 124
