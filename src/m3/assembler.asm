; M3 Z80 assembler — top-level.
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.1.
;
; Boot via M0's BASIC autorun pattern:
;   CLEAR&7FFF: LOAD CODE "assembler" 32768: CALL 32768
;
; Memory layout (M3 post-Task-16):
;   &8000-&9FFF  assembler code (8 KB; this file + all M3 includes)
;   &A000-&AFFF  enctab.enc buffer (4 KB; ENCTAB_BUF in loader.asm)
;   &B000-&B7FF  IN .tbn buffer (2 KB; IN_BUF below)
;   &B800-&BFFF  OUT buffer (2 KB; OUT_BUF below)
;   &C000-&C0FF  stack (SP = &C100, grows down into section D RAM)
;   &C100-&FFFF  scratch (OPVAL arrays, etc.) — section D RAM
;
; Note: pyz80 does not support the END directive. Assembly ends at EOF.
; The org directive sets the load address; the entry point is the first byte.

IN_BUF:         equ     &B000          ; .tbn source buffer (section C; HLOAD dest)
IN_BUF_END:     equ     &B800          ; one past end of IN buffer (2 KB)
OUT_BUF:        equ     &B800          ; output buffer (section C; HSAVE source)
OUT_BUF_END:    equ     &C000          ; one past end of OUT buffer (2 KB)

OPVAL_ARRAY:    equ     &C100          ; 7 * 10 = 70 bytes — operand value array
OPVAL_KINDS:    equ     &C150          ; 7 bytes — kinds[] for form_lookup_match


                org     &8000

; Jump table at the entry point: CALL 32768 lands on the first byte (&8000).
; Pattern mirrors M0's src/stub.asm.
                jp      start

                include "io.asm"
                include "loader.asm"
                include "slots/xreg.asm"
                include "slots/imm_small.asm"
                include "slots/imm12_shifted.asm"
                include "slots/imm16_shifted.asm"
                include "slots/extend_op.asm"
                include "slots/branch_imm.asm"
                include "slots/adrp_imm.asm"
                include "slots/logical_imm.asm"
                include "slots/bitfield_imm.asm"
                include "ml.asm"
                include "expr_eval.asm"
                include "form_lookup.asm"
                include "encoder.asm"
                include "reader.asm"
                include "main_loop.asm"
                include "test_slots.asm"

; -----------------------------------------------------------------------
; Main program — entry via jp from &8000.
; -----------------------------------------------------------------------

start:
                di                     ; disable interrupts (batch program)

; Set up the stack before any call.  SAMDOS's EI in the RST 8 hook
; re-enables interrupts, so DI must be repeated after hook calls.
                ld      sp, &C100

; -- Per-slot encoder self-tests ----------------------------------------
; Exercises encode_reg / encode_imm_n / encode_cond /
; encode_imm12_shifted / encode_imm16_shifted against hardcoded slot
; records (no disk I/O).  On any mismatch: jp fail (red border +
; spin → 30s timeout).  See test_slots.asm.
                call    run_slot_self_tests

; -- Load and validate enctab.enc header --------------------------------
                call    load_enctab

; -- Initialise form-lookup pointers (form table base + index base) -----
                call    form_lookup_init

; -- Run the assemble pass: load IN, walk records, build OUT -----------
                call    main_assemble

; -- Write OUT to disk via HSAVE ----------------------------------------
                call    save_out_file

; -- Clean exit ---------------------------------------------------------
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
