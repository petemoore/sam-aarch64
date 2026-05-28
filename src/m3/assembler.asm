; M3 Z80 assembler — top-level.
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.1.
;
; Boot via M0's BASIC autorun pattern:
;   CLEAR&7FFF: LOAD CODE "assembler" 32768: CALL 32768
;
; Memory layout (M6 PR 2 — paged IN + paged OUT):
;   &0000-&3FFF  section A — ROM0 (default LMPR_DEFAULT)
;                  OR ENCTAB (physical page 4) when LMPR = LMPR_ENCTAB
;                  OR IN page N (LMPR = LMPR_IN_BASE + N) inside the
;                    reader_next_kind bracket — see reader.asm.
;   &4000-&7FFF  section B — page 1 (BASIC sys area, mostly unused by us);
;                  trampoline copy at TRAMPOLINE_DST (&7E00).  Under
;                  LMPR_ENCTAB section B = page 5 = OUT-low (used as
;                  the OUT emit window — see emit_byte).
;   &8000-&AFFF  assembler code (12 KB; this file + all M3/M4/M5/M6 includes)
;   &B000-&BFFF  reserved (4 KB freed by M6 — was IN_BUF + OUT_BUF
;                  pre-M6.  Both are now paged out of section C;
;                  available for future use.  M6 PR 1 freed &B800-&BFFF
;                  (OUT); M6 PR 2 freed &B000-&B7FF (IN).
;   &C000-&C0FF  stack (SP = &C100, grows down into section D RAM)
;   &C100-&D4FF  scratch (OPVAL arrays, SYMTAB, litpool TABLE+counters) —
;                  section D RAM.  See per-file headers and the detailed
;                  block below for sub-allocations.
;   &D500-&D8FF  STAGING_BUF — paged-IN per-record staging area (M6 PR 2)
;   &D900-&E0FF  LITPOOL_EXPR_BUF — cross-pass expr bytecode pool (M6 PR 2)
;   &E100-&E27F  LITPOOL_PC_MAP (64 × 6 = 384 B; moved here 2026-05-28)
;   &E280-&E77C  LOCAL_LABEL_TABLE (2 + 255 × 5 = 1277 B; moved here 2026-05-28)
;   &E77D-&E7FF  free (131 B between local-labels and symtab-overflow)
;   &E800-&EFFF  SYMTAB_OVERFLOW (256 × 8 = 2 KB; moved here 2026-05-28)
;   &F000-&FFFF  free (4 KB headroom in section D for future use)
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
;   Physical pages 7..12 (off-axis): IN .tbn buffer — 6 contiguous
;     pages = 96 KB ceiling (bumped from 4 pages / 64 KB on 2026-05-28
;     to fit spectrum4's 88 KB stripped release.tbn).  HLOAD'd once at
;     startup via load_in_file_paged; read via per-record LMPR-bracket
;     into section A on each reader_next_kind call.  See
;     docs/specs/2026-05-27-m6-paged-in-design.md.
;   Physical page 13 (off-axis, BUILD_TESTS only): test_mem.bin — the
;     largest BUILD_TESTS-only self-test suite, ported off-axis to
;     free section-C budget.  HLOAD'd at boot via
;     load_test_mem_off_axis (loader.asm); invoked via LMPR-swap-
;     CALL-restore from `start:` in this file.  See plan-PR 3 of
;     docs/notes/2026-05-28-paged-call-architecture.md and the brief
;     at docs/plans/2026-05-28-plan-pr3-test-corpus-off-axis.md.
;
; Pre-M5 layout placed ENCTAB at &A000-&AFFF in section C, consuming
; 4 KB of the code section.  M5's compound-operand encoders pushed
; code size past the resulting 8 KB code budget; paging ENCTAB out
; recovers that 4 KB.  See docs/specs/2026-05-27-samdos-load-idiom.md
; for the design rationale.  M6 PR 1 extends the off-axis pattern to
; OUT, freeing 2 KB at &B800 and lifting the output ceiling from
; 2 KB to 32 KB.  M6 PR 2 extends it to IN, freeing another 2 KB
; at &B000 and lifting the input ceiling from 2 KB to 64 KB.
;
; Note: pyz80 does not support the END directive. Assembly ends at EOF.
; The org directive sets the load address; the entry point is the
; first byte.

; IN buffer paging — see docs/specs/2026-05-27-m6-paged-in-design.md.
;   pages 7..12  ── IN .tbn (HLOAD destination); 96 KB ceiling
;   STAGING_BUF  ── per-record staging window in section D
;   LITPOOL_EXPR_BUF ── cross-pass copy of litpool expr bytecode

STAGING_BUF:           equ     &D500          ; 1 KB record staging area
STAGING_BUF_END:       equ     &D900
LITPOOL_EXPR_BUF:      equ     &D900          ; 2 KB cross-pass expr pool
LITPOOL_EXPR_BUF_END:  equ     &E100

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
;   &C960-&CFFF  free (1696 B; old SYMTAB_OVERFLOW + old LOCAL_LABEL_TABLE
;                regions, freed when both were moved to &E100+ on
;                2026-05-28 to absorb the release.tbn census peaks).
; SYMTAB_OVERFLOW is now at &E800 (256 entries, 2 KB) and
; LOCAL_LABEL_TABLE at &E280 (255 entries, 1277 B); see the high-level
; map block above for the full &E100+ layout.

; M5 PR-C scratch: OpMem encoder's 8-byte LE offset value.
; Only one OpMem operand exists per instruction (Rt, [mem] for non-pair;
; Rt1, Rt2, [mem] for ldp/stp) so a single 8-byte slot suffices.
OPMEM_OFF:      equ     &D100          ; 8 bytes — OpMem offset (s64 LE)

; M5 PR-E scratch: literal-pool data structures (see src/m3/litpool.asm).
;   &D200..&D3BF  LITPOOL_TABLE       (32 slots × 14 bytes = 448 B)
;   &D3C0         LITPOOL_COUNT       (1 byte)
;   &D3C1         LITPOOL_PCM_COUNT   (1 byte)
;   &D3C2         LITPOOL_SEGMENT_ALLOC (1 byte)
;   &D3C3         LITPOOL_SEGMENT_FLUSH (1 byte)
;   &D3C4..&D3C7  LITPOOL_SAVED_PC    (4 bytes)
;   &D3C8..&D4FF  free (312 B between counters and STAGING_BUF at &D500)
;   &E100..&E27F  LITPOOL_PC_MAP      (64 entries × 6 bytes = 384 B;
;                                     moved to &E100+ on 2026-05-28 to
;                                     fit the bumped 64-entry cap —
;                                     release.tbn census peaks at 44).


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

; -- Install the section-B HLOAD trampoline.  Must happen BEFORE
; load_enctab (which uses it).  Per plan-PR 3 it also runs BEFORE
; the BUILD_TESTS self-tests because the test variant HLOADs the
; off-axis test_mem payload via this trampoline before invoking
; run_mem_self_tests below.  The pre-load on-axis self-tests don't
; touch section B, so installing the trampoline early is safe.
                call    enctab_trampoline_setup

; -- BUILD_TESTS only: HLOAD the off-axis test_mem.bin into page 13.
; The Mac-side build pipeline assembles src/m3/test_mem_offaxis.asm
; against the main-binary symbol export, producing a small standalone
; binary that lives at section-A &0000-onward when LMPR = LMPR_TEST_MEM.
; The actual run_mem_self_tests invocation below swaps LMPR briefly,
; calls into the off-axis entry, then restores.
;
; See docs/plans/2026-05-28-plan-pr3-test-corpus-off-axis.md.
if defined(BUILD_TESTS)
                call    load_test_mem_off_axis

; -- BUILD_TESTS only: HLOAD the off-axis "M5 + misc encoder" cluster
; (pc_rel / directives_m5 / ror_imm / shifted_reg / extended_reg /
; litpool) into physical page 12.  Run later, after the inline
; symbol/local-label suites, via the LMPR-swap call site.  Loaded here
; (alongside test_mem) so all off-axis HLOADs happen up front.  Page 12
; is free at boot-self-test time (IN doesn't occupy pages 7..12 until
; main_assemble).  M6 budget-relief PR (2026-05-29).
                call    load_offaxis_cluster

; -- BUILD_TESTS only: HLOAD the paged_call self-test payload into
; physical page 14, then run the self-test.  Must happen AFTER
; enctab_trampoline_setup (paged_call body needs installing in
; section B) and BEFORE load_enctab (which mutates HMPR via the
; HLOAD trampoline and would perturb the test's bit-identity
; assertion).
;
; The payload is 3 bytes (`ld a, &42; ret`); the test calls
; paged_call into it and asserts A=&42 + HMPR-bit-identity on
; return.  See plan-PR 1 of
; docs/notes/2026-05-28-paged-call-architecture.md.
                call    load_page14_payload
                call    run_paged_call_self_tests
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
                call    run_symbol_table_self_tests
                call    run_local_label_self_tests
                ; run_expr_eval_m4_self_tests moved off-axis into the page-12
                ; cluster (PR-3c, 2026-05-29) to reclaim ~449 B of section-C/D
                ; budget for the MUL/DIV evaluator code.  It runs first in
                ; cluster_dispatch, preserving its prior relative order (after
                ; symbol/local, before slots).  The suite is LMPR-swap-safe:
                ; verified call-graph-free of paged_call / section-B / LMPR
                ; routines (only eval_expr_const, symbol_*, local_* — all
                ; HMPR-stable section-C/D), and its inline `defb` literals read
                ; via section A under the swap, like the slots suite already
                ; relies on.

; -- The slot / pc_rel / directives_m5 / ror_imm / shifted_reg /
; extended_reg / litpool suites live off-axis on physical page 12 (M6
; budget-relief PR,
; 2026-05-29).  Same LMPR-swap-call-restore mechanism as the test_mem
; off-axis call below: 9 bytes here vs 6×3 for the inline calls they
; replace, net section-C/D saving ~1.2 KB.  HMPR is unchanged, so the
; off-axis cluster's calls to production routines (encode_*, litpool_*,
; symbol_*, compute_directive_size, pass_pc_reset, assert_eq32_de_hl_imm,
; fail) resolve to their section-C/D addresses.  Verified by call-graph
; analysis that none of those routines reach paged_call / the section-B
; trampoline, which the LMPR swap would relocate.  Stack (section D,
; HMPR) unaffected.  See src/m3/test_offaxis_cluster.asm for the full
; safety argument.  Must run AFTER the inline symbol/local-label suites
; above (pc_rel re-uses the local-label table they leave initialised).
                ld      a, LMPR_TEST_CLUSTER
                out     (250), a
                call    &0000                       ; off-axis cluster_dispatch
                ld      a, (LMPR_DEFAULT_RUNTIME)
                out     (250), a

; -- run_mem_self_tests lives off-axis on page 13 (plan-PR 3).
; LMPR-swap-call-restore sequence: 9 bytes vs 3 for a plain `call`,
; net section-C saving ~770 B over the moved test_mem.asm body.
; HMPR is unchanged, so the off-axis code's calls to encode_mem_word,
; assert_eq32_de_hl_imm, and fail all resolve to their section-C
; addresses and execute correctly.  Stack (section D, HMPR) likewise
; unaffected.  Interrupts already DI at this point (set at start:).
                ld      a, LMPR_TEST_MEM
                out     (250), a
                call    &0000                       ; off-axis run_mem_self_tests
                ld      a, (LMPR_DEFAULT_RUNTIME)
                out     (250), a

; -- Load the sysreg lookup data into page 13 (PR-2).  In the test
; variant this must happen AFTER the test_mem off-axis self-tests above
; (which used page 13 as the test_mem payload) and BEFORE
; run_sysname_self_tests / run_sysreg_paged_self_tests below (which read
; the tables via paged_call).  Page 13 is overwritten: test_mem is fully
; consumed by run_mem_self_tests above, so the overwrite is safe.  See
; loader.asm::load_page13_payload header for the ordering contract.
                call    load_page13_payload
                call    run_sysreg_paged_self_tests

                call    run_sysname_self_tests
                ; run_litpool_self_tests now runs off-axis in the page-12
                ; cluster above (M6 budget-relief PR).  It is self-contained
                ; (litpool_init re-initialises its own state) so its earlier
                ; position in the cluster is behaviour-neutral.
                call    run_emit_paged_self_tests
endif

; -- Load the sysreg lookup data into page 13 (PRODUCTION path, and a
; redundant-but-harmless reload in the test variant).  In a production
; build the BUILD_TESTS block above is #ifdef'd out entirely, so this
; unconditional call is the ONLY place page 13 gets the sysreg data.
; In the test variant the block above already loaded it before the
; sysreg self-tests; reloading the same bytes here is idempotent.  We
; keep this unconditional (rather than `if defined(BUILD_TESTS)==0`,
; which pyz80 doesn't parse) for simplicity — the extra HLOAD costs a
; few ms at boot only.  Must run before main_assemble's first lookup.
                call    load_page13_payload

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
                ; run_reader_paged_self_tests was DISABLED across PRs #42→#45
                ; pending root-cause investigation of a deterministic boot
                ; FAIL on the test variant.  Re-enabled here after the PR-6
                ; M6-closure investigation (the first adversarial use of the
                ; Go harness, tools/z80-test-harness-go/).
                ;
                ; Root cause (now resolved upstream by PR #52, not by this
                ; PR): the original failure was the stack-vs-own-code
                ; collision documented in
                ; docs/notes/2026-05-28-reader-paged-self-test-investigation.md
                ; — on the pre-PR-#52 layout the test function spilled above
                ; &C000 into the SP=&C100 stack page, so its own opcodes were
                ; overwritten by stack pushes.  PR #52 ported test_mem.asm
                ; off-axis to physical page 13, shrinking the test variant
                ; from &C1AB to ~&BF70; run_reader_paged_self_tests now lives
                ; entirely below &C000 (≈&BE5B), so the stack at &C100 never
                ; collides with it.  No SP change is needed — PR #42's
                ; SP=&FFFE "fix" was both unnecessary AND wrong (the top of
                ; section D is HMPR-controlled and would move under paging).
                ; SP stays at &C100 (set at start:).
                ;
                ; Verified: harness PASS + SimCoupé ci-m{3,4,5,6} all green
                ; with this call live.  See
                ; docs/notes/2026-05-28-reader-paged-self-test-investigation.md
                ; (PR-6 resolution section at the foot).
                call    run_reader_paged_self_tests
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
; fail_with_tag — entry point for fail sites that want to record a tag
; byte.  Sites do `ld a, <tag>; jp fail_with_tag` (5 bytes total — saves
; 3 bytes per site versus the inline `ld (LAST_FAIL_TAG), a; jp fail`
; pattern).  Falls through into the main fail routine.
fail_with_tag:  ld      (LAST_FAIL_TAG), a

fail:           ld      a, 2
                out     (&fe), a            ; SAM border port — red
                ld      hl, msg_fail
                call    print_status_string
; Diagnostic: emit the LAST_FAIL_TAG byte as two ASCII hex digits.
; Untagged callers leave LAST_FAIL_TAG at its boot value (0x00); tagged
; callers (via fail_with_tag) recorded their site code in it just before
; jumping here.  The hex pair appears between FAIL and the newline so
; the existing `grep '^FAIL'` status check still works.
                ld      a, (LAST_FAIL_TAG)
                call    print_hex_byte
                ld      a, 10
                call    print_status_char
                di
                halt

; print_hex_byte — write A as two ASCII hex digits to printer channel 1.
; Clobbers: A, F (preserves BC, DE, HL via print_status_char).
print_hex_byte:
                push    af
                rra
                rra
                rra
                rra
                call    print_hex_nibble
                pop     af
                ; fall through to print the low nibble
print_hex_nibble:
                and     &0f
                add     a, &30                  ; '0'
                cp      &3a                     ; '0' + 10
                jr      c, print_hex_emit
                add     a, &27                  ; 'a' - '0' - 10 = 0x61 - 0x30 - 10 = 0x27
print_hex_emit:
                call    print_status_char
                ret

LAST_FAIL_TAG:  defb    0


; -----------------------------------------------------------------------
; Boot-time self-test includes (BUILD_TESTS only).
;
; Placed AFTER `start:` and `fail:`.  Post-budget-lever, the test code
; can legitimately spill into the full &8000-&AFFF code window
; (ENCTAB no longer occupies section C); the production binary's code
; budget is now 12 KB total instead of 8 KB.
; -----------------------------------------------------------------------
if defined(BUILD_TESTS)
                ; Shared inline-literal assertion helper, resident in the
                ; main binary so both inline and off-axis suites resolve it
                ; (the off-axis cluster + test_mem reach it via importfile).
                include "test_assert_eq32.asm"
                include "test_symbols.asm"
                include "test_local_labels.asm"
                ; test_expr_eval_m4 moved off-axis into the page-12 cluster
                ; (PR-3c, 2026-05-29) — see the call-site comment above.
                ; test_slots / test_pc_rel / test_directives_m5 / test_ror_imm /
                ; test_shifted_reg / test_extended_reg / test_litpool now
                ; live off-axis on physical page 12 (the "M5 + misc encoder"
                ; cluster), assembled separately into build/test_cluster.bin
                ; and HLOADed at boot via load_offaxis_cluster.  Relocated
                ; in the M6 budget-relief PR (2026-05-29) to drop the test
                ; variant back below &C000.  See
                ; src/m3/test_offaxis_cluster.asm and
                ; docs/notes/2026-05-29-test-variant-budget-relief.md.
                ;
                ; test_mem.asm likewise lives off-axis (physical page 13);
                ; see load_test_mem_off_axis / plan-PR 3 (PR #52).
                include "test_sysname.asm"
                include "test_litpool.asm"
                include "test_trampoline.asm"
                include "test_emit_paged.asm"
                include "test_reader_paged.asm"
                include "test_paged_call.asm"
                include "test_sysreg_paged.asm"
endif

; -- paged_call body source ---------------------------------------------
; Included LAST so the body source bytes appear at the end of the
; binary, NOT near the BUILD_TESTS test-data labels (boot_hmpr,
; lmpr_save_test, reader_paged_*) which live alongside the
; test_*.asm includes above.  The body is NEVER executed from its
; section-C source location — enctab_trampoline_setup LDIRs the
; bytes into section B at boot; this include just emits the
; source-side bytes for the LDIR to read from.
;
; Source-position rationale: paged_bodies.asm has no `org` directive
; (it inherits pyz80's current PC), so its absolute address shifts
; with every byte added or removed above it.  The bytes baked into
; the body, however, are all absolute references to section-B
; addresses (PAGED_CALL_HMPR_SAVE / PAGED_CALL_SP_SAVE / TRAMP_SAFE_SP
; / paged_call_trailer_dst) — all defined as EQU expressions in
; trampoline.asm — so the body's contents are position-independent.
                include "paged_bodies.asm"
