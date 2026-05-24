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
                include "intercepts.asm"
                include "reader.asm"
                include "main_loop.asm"
                include "symbols.asm"
                include "local_labels.asm"

                include "print.asm"

; -----------------------------------------------------------------------
; Main program — entry via jp from &8000.
;
; M5 PR-A note on placement: `start:` and `fail:` are intentionally
; defined BEFORE the test_*.asm includes (and therefore live entirely
; within the &8000-&9FFF region) so the production code path is not
; disturbed when self-tests, which sit at &A000+, are overwritten by
; load_enctab.  The self-tests run BEFORE load_enctab; by the time
; load_enctab clobbers &A000-&AFFF the test code has already
; completed.  See the BUILD_TESTS include block below.
; -----------------------------------------------------------------------

start:
                di                     ; disable interrupts (batch program)

; Set up the stack before any call.  SAMDOS's EI in the RST 8 hook
; re-enables interrupts, so DI must be repeated after hook calls.
                ld      sp, &C100

; -- Boot-time self-tests (compiled in only when BUILD_TESTS=1) --------
; Five suites run in fixed order BEFORE load_enctab so they have no
; disk-state dependency.  Any assertion failure does `jp fail` (red
; border + printer-channel "FAIL" banner, ci-m3 reports fail
; immediately).  All five are omitted from a production build (the
; corresponding test_* includes are also skipped — see below).
;
; Ordering note: M4 expr_eval / PC-rel suites MUST run AFTER the
; symbol-table + local-label suites, because the M4 tests destructively
; re-init both tables and need a known starting point.  PASS_PC is
; also clobbered but is re-zeroed by main_assemble's pass_pc_reset
; call, so it doesn't need explicit restoration here.
if defined(BUILD_TESTS)
                call    run_slot_self_tests
                call    run_symbol_table_self_tests
                call    run_local_label_self_tests
                call    run_expr_eval_m4_self_tests
                call    run_pc_rel_self_tests
                call    run_directives_m5_self_tests
                call    run_ror_imm_self_tests
endif

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

; -- Print "OK\n" to printer channel 1 so the wrapper can distinguish
; clean success from any early exit / crashed-and-halted scenario.
; The wrapper greps the printer log for "^OK$"; absence → failure.
                ld      hl, msg_ok
                call    print_status_string

; -- Clean exit ---------------------------------------------------------
; The DI at start: is undone by SAMDOS's EI inside the RST 8 hook window
; (ROM PTDOS does EI before dispatching — per src/stub.asm citation).
; Re-issue DI so HALT with IFF1=0 triggers SimCoupé's -exitonhalt.
                di
                halt


; -----------------------------------------------------------------------
; fail — error indicator: print "FAIL\n" to the printer channel, set
; the border red, then halt cleanly.
; -----------------------------------------------------------------------
; Both paths now do `di; halt` (clean SimCoupé exit), with the status
; conveyed via the parallel printer channel — see print.asm and the
; wrapper logic in tools/run-simcoupe.sh.  The previous design spun
; here and relied on the wrapper's 30 s timeout to detect failure;
; that's been replaced because (a) it cost a full 30 s per failure
; (painful during M4 dev), and (b) it gave no per-fail-site
; diagnostic.  The printer-channel banner drops failure latency to
; ~100 ms and leaves room for per-site diagnostic strings in future.
;
; SimCoupé port &FE bit 0-2 sets SAM border colour; value 2 = red.
; The border colour is retained as a human-visible signal when running
; SimCoupé interactively (the wrapper doesn't depend on it).
; Citation: SAM Coupé Technical Manual §7 (ULA port &FE).
fail:           ld      a, 2
                out     (&fe), a            ; SAM border port — red
                ld      hl, msg_fail
                call    print_status_string
                di
                halt


; -----------------------------------------------------------------------
; Boot-time self-test includes (BUILD_TESTS only).
;
; Placed AFTER `start:` and `fail:` so the production code path lives
; entirely in &8000-&9FFF.  The test code may legitimately spill into
; &A000+ (which becomes ENCTAB_BUF after load_enctab); by the time
; load_enctab overwrites that region the tests have already returned.
; -----------------------------------------------------------------------
if defined(BUILD_TESTS)
                include "test_slots.asm"
                include "test_symbols.asm"
                include "test_local_labels.asm"
                include "test_expr_eval_m4.asm"
                include "test_pc_rel.asm"
                include "test_directives_m5.asm"
                include "test_ror_imm.asm"
endif
