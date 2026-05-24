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

; M4: which assembly pass is currently active.  Pass 1 walks records to
; assign PC and populate the symbol / local-label tables; pass 2 walks
; them again and emits resolved bytes.  See
; docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.1.  Pass 1
; never touches OUT_BUF; pass 2 alone emits.  PASS_MODE is set by
; main_assemble (which now owns both pass-1 and pass-2 setup) and is
; read by every record handler that diverges per pass.
PASS_MODE:      equ     &C158          ; 1 byte — current pass (PASS_PASS1 / PASS_PASS2)
PASS_PASS1:     equ     1
PASS_PASS2:     equ     2

; PASS_PC — 4-byte little-endian assembler PC, reset to 0 at the start
; of each pass and advanced in lockstep by both pass-1 (table build,
; no emit) and pass-2 (emit) handlers.  Tasks 5-6 will read this when
; resolving PC-relative expressions; M3 fixtures have no PC-relative
; refs, so it's observationally inert today.  Helpers live in
; main_loop.asm (pass_pc_reset / pass_pc_advance_*).
PASS_PC:        equ     &C159          ; 4 bytes — current pass PC (u32 LE)

; M4 scratch reservation (allocated by symbols.asm + local_labels.asm):
;   &C160-&C95F  SYMTAB              (256 buckets × 8 bytes = 2 KB)
;   &C960-&CD5F  SYMTAB_OVERFLOW     (overflow chain, ~1 KB)
;   &CD60-&D15F  LOCAL_LABEL_TABLE   (9 digits, ~1 KB)


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
                include "symbols.asm"
                include "local_labels.asm"
                include "test_slots.asm"
                include "test_symbols.asm"
                include "test_local_labels.asm"
                include "test_expr_eval_m4.asm"

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

; -- Symbol-table self-tests --------------------------------------------
; Exercises symbol_table_init / symbol_insert / symbol_lookup against
; hard-coded ids and addresses (no disk I/O).  On any mismatch: jp fail.
; See test_symbols.asm.
                call    run_symbol_table_self_tests

; -- Local-label table self-tests ---------------------------------------
; Exercises local_label_table_init / local_def_append /
; local_find_forward / local_find_backward against a hard-coded
; per-digit PC list (no disk I/O).  On any mismatch: jp fail.
; See test_local_labels.asm.
                call    run_local_label_self_tests

; -- Expression-evaluator M4 self-tests --------------------------------
; Exercises eval_expr_const's new M4 opcodes (PUSH_SYM, PUSH_LOCAL,
; PUSH_PC, REL_LO12/HI12, REL_ABS_G0..G3) against hand-rolled bytecode
; buffers and pre-seeded PASS_PC / symbol-table / local-label-table
; state.  On any mismatch: jp fail.  See test_expr_eval_m4.asm.
;
; This MUST run AFTER run_symbol_table_self_tests + run_local_label_self_tests
; so it can safely re-init both tables (those suites are destructive on
; the tables they exercise, and the M4 tests need a known starting
; point).  PASS_PC is also clobbered by the M4 tests but is re-zeroed
; by main_assemble's pass_pc_reset call, so the test doesn't need to
; restore it.
                call    run_expr_eval_m4_self_tests

; -- Load and validate enctab.enc header --------------------------------
                call    load_enctab

; -- Initialise form-lookup pointers (form table base + index base) -----
                call    form_lookup_init

; -- Run the assemble: pass 1 (table build) + pass 2 (emit) -----------
; main_assemble owns the two-pass dance internally: it sets PASS_MODE
; for each pass, resets PASS_PC + the symbol/local tables before
; pass 1, resets PASS_PC + OUT state before pass 2, and rewinds the
; reader between passes.  See main_loop.asm.
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
