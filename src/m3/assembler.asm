; M3 Z80 assembler — top-level.
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.1.
;
; Boot via M0's BASIC autorun pattern:
;   CLEAR&7FFF: LOAD CODE "assembler" 32768: CALL 32768
;
; Memory layout (M6 PR 1 — paged OUT):
;   &0000-&3FFF  section A — ROM0 (default LMPR_DEFAULT)
;                  OR ENCTAB (physical page 4) when LMPR = LMPR_ENCTAB
;   &4000-&7FFF  section B — page 1 (BASIC sys area, mostly unused by us);
;                  trampoline copy at TRAMPOLINE_DST (&7E00).  Under
;                  LMPR_ENCTAB section B = page 5 = OUT-low (used as
;                  the OUT emit window — see emit_byte).
;   &8000-&AFFF  assembler code (12 KB; this file + all M3/M4/M5 includes)
;   &B000-&B7FF  IN .tbn buffer (2 KB; IN_BUF below)
;   &B800-&BFFF  reserved (was OUT_BUF pre-M6; freed by paging OUT out
;                  of section C — available for future use, currently
;                  unused).
;   &C000-&C0FF  stack (SP = &C100, grows down into section D RAM)
;   &C100-&FFFF  scratch (OPVAL arrays, SYMTAB, etc.) — section D RAM
;
;   Physical page 4 (off-axis): ENCTAB body — paged into section A on
;     demand for encoder runtime reads.  See `src/m3/trampoline.asm`.
;   Physical pages 5..6 (off-axis): OUT buffer — page 5 reached via
;     section B with LMPR_ENCTAB for free (low zone, bytes 0..16383);
;     page 6 reached via LMPR=LMPR_OUT_HIGH bracket per emit
;     (high zone, bytes 16384..32767).  HSAVE at end of pass 2 reads
;     the buffer via section C with UIFA[31] = OUT_BASE_PAGE.  See
;     docs/specs/2026-05-27-m6-paged-out-design.md and
;     docs/specs/2026-05-27-samdos-save-idiom.md.
;
; Pre-M5 layout placed ENCTAB at &A000-&AFFF in section C, consuming
; 4 KB of the code section.  M5's compound-operand encoders pushed
; code size past the resulting 8 KB code budget; paging ENCTAB out
; recovers that 4 KB.  See docs/specs/2026-05-27-samdos-load-idiom.md
; for the design rationale.  M6 PR 1 extends the off-axis pattern to
; OUT, freeing 2 KB at &B800 and lifting the output ceiling from
; 2 KB to 32 KB.
;
; Note: pyz80 does not support the END directive. Assembly ends at EOF.
; The org directive sets the load address; the entry point is the
; first byte.

IN_BUF:         equ     &B000          ; .tbn source buffer (section C; HLOAD dest)
IN_BUF_END:     equ     &B800          ; one past end of IN buffer (2 KB)

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
;   &CD60-&D0E5  LOCAL_LABEL_TABLE   (count + 180 × 5 bytes = 902 bytes;
;                                    capped below OPMEM_OFF at &D100)

; M5 PR-C scratch: OpMem encoder's 8-byte LE offset value.
; Only one OpMem operand exists per instruction (Rt, [mem] for non-pair;
; Rt1, Rt2, [mem] for ldp/stp) so a single 8-byte slot suffices.
OPMEM_OFF:      equ     &D100          ; 8 bytes — OpMem offset (s64 LE)

; M5 PR-E scratch: literal-pool data structures (see src/m3/litpool.asm).
;   &D200..&D3BF  LITPOOL_TABLE       (32 slots × 14 bytes = 448 B)
;   &D3C0..&D47F  LITPOOL_PC_MAP      (32 entries × 6 bytes = 192 B)
;   &D480         LITPOOL_COUNT       (1 byte)
;   &D481         LITPOOL_PCM_COUNT   (1 byte)
;   &D482..&D485  LITPOOL_SAVED_PC    (4 bytes)
; Total: 646 bytes (&D200..&D485 inclusive).  Remaining &D486..&FFFF
; (~11 KB) free for expr-eval stack and future use.


                org     &8000

; Jump table at the entry point: CALL 32768 lands on the first byte (&8000).
; Pattern mirrors M0's src/stub.asm.
                jp      start

                include "io.asm"
                include "trampoline.asm"
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
                include "slots/shifted_reg.asm"
                include "slots/extended_reg.asm"
                include "slots/mem.asm"
                include "ml.asm"
                include "expr_eval.asm"
                include "form_lookup.asm"
                include "encoder.asm"
                include "intercepts.asm"
                include "sysname.asm"
                include "reader.asm"
                include "main_loop.asm"
                include "symbols.asm"
                include "local_labels.asm"
                include "litpool.asm"

                include "print.asm"

; -----------------------------------------------------------------------
; Main program — entry via jp from &8000.
;
; M5 budget-lever update: ENCTAB no longer occupies section C, so the
; &A000-&AFFF region is now AVAILABLE for code.  The historical
; `start:` / `fail:` placement constraint (must precede the test_*.asm
; includes so production code stays under &9FFF) is no longer
; strictly necessary — but we keep it because the ordering still
; matches "self-tests run BEFORE load_enctab, which is the only real
; ordering requirement".
; -----------------------------------------------------------------------

start:
                di                     ; disable interrupts (batch program)

; Set up the stack before any call.  SAMDOS's EI in the RST 8 hook
; re-enables interrupts, so DI must be repeated after hook calls.
                ld      sp, &C100

; Capture the boot LMPR value (as left by BASIC's CALL 32768) into
; LMPR_DEFAULT_RUNTIME so enctab_map_out restores the *real* default,
; not a synthetic "ROM in A / page 1 in B" guess.  BASIC's default
; LMPR is typically &1F (ROM0 in A; section B = LMPR_low+1 = page 0
; = BASIC sys page).  enctab_map_out must restore THIS value so the
; subsequent RST 8 calls see the same section B they saw at boot
; (UIFA at &4B00, sys vars at &5xxx, etc. all live in section B).
;
; This must run BEFORE any LMPR-changing call (i.e. before
; enctab_map_in / load_enctab).
                in      a, (250)
                ld      (LMPR_DEFAULT_RUNTIME), a

; Capture the boot HMPR value too, so the trampoline self-test can
; verify that load_enctab's trampoline call restored it correctly.
; Only used by the BUILD_TESTS path; production builds zero this
; storage (no callers).
if defined(BUILD_TESTS)
                in      a, (251)
                ld      (boot_hmpr), a
endif

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
                call    run_shifted_reg_self_tests
                call    run_extended_reg_self_tests
                call    run_mem_self_tests
                call    run_sysname_self_tests
                call    run_litpool_self_tests
                call    run_emit_paged_self_tests
endif

; -- Install the section-B HLOAD trampoline.  Must happen BEFORE
; load_enctab (which uses it) but AFTER the self-tests (which may
; reuse section B's address range for their own scratch).
                call    enctab_trampoline_setup

; -- Load and validate enctab.enc header --------------------------------
; load_enctab uses the trampoline to land the file in physical page 4
; (outside section C, freeing &A000-&AFFF for code).  Validates the
; magic + version inline via a temporary enctab_map_in/map_out window,
; and leaves LMPR back at the boot-captured LMPR_DEFAULT_RUNTIME on
; return.
                call    load_enctab

; -- Post-load trampoline / LMPR-swap self-tests ----------------------
; These verify HMPR/LMPR are correctly restored and that ENCTAB is
; readable via the map_in/map_out wrapper.  Must run AFTER load_enctab
; (it needs ENCTAB to be loaded) and BEFORE main_assemble (so any
; failure is reported before we waste time on the assemble loop).
if defined(BUILD_TESTS)
                call    run_trampoline_self_tests
endif

; -- Run the assemble: pass 1 (table build) + pass 2 (emit) -----------
; main_assemble owns the two-pass dance AND the ENCTAB-window
; bracketing: it loads IN (LMPR=DEFAULT for SAMDOS hooks), then
; map_in's ENCTAB into section A, calls form_lookup_init, runs the
; passes, then map_out's before returning.  Callers see LMPR back at
; LMPR_DEFAULT.  See main_loop.asm.
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
; Placed AFTER `start:` and `fail:`.  Post-budget-lever, the test code
; can legitimately spill into the full &8000-&AFFF code window
; (ENCTAB no longer occupies section C); the production binary's code
; budget is now 12 KB total instead of 8 KB.
; -----------------------------------------------------------------------
if defined(BUILD_TESTS)
                include "test_slots.asm"
                include "test_symbols.asm"
                include "test_local_labels.asm"
                include "test_expr_eval_m4.asm"
                include "test_pc_rel.asm"
                include "test_directives_m5.asm"
                include "test_ror_imm.asm"
                include "test_shifted_reg.asm"
                include "test_extended_reg.asm"
                include "test_mem.asm"
                include "test_sysname.asm"
                include "test_litpool.asm"
                include "test_trampoline.asm"
                include "test_emit_paged.asm"
endif
