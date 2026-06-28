; test_offaxis_cluster.asm — off-axis assembly wrapper for the
; "M5 + misc encoder" boot self-test suites.
;
; M6 budget-relief PR (2026-05-29).  See
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-29-test-variant-budget-relief.md.
;
; This file is its own pyz80 entry point — it is NOT included from
; src/assembler.asm.  The Makefile invokes pyz80 separately on this
; file with `--importfile=build/assembler.sym` so that section-C
; production symbols (encode_*, litpool_*, symbol_*, compute_directive_size,
; main_eval_*, pass_pc_reset, assert_eq32_de_hl_imm, fail, ...) resolve
; to the addresses they hold in the main assembler.bin.  The result is a
; standalone binary (build/test_cluster.bin) that gets loaded at boot
; into physical page 12 via HLOAD and invoked through a single LMPR swap.
;
; Why off-axis: PR #63 pushed the BUILD_TESTS test variant to end at
; &C0B6 — past the &C000 section-D boundary, into the &C100 boot-stack /
; OPVAL_ARRAY region.  Relocating this cluster (1225 B of section-C/D
; code) below &C000 restores ~1 KB of headroom so the remaining FAIL40
; encoder families (ldr-literal, lsl/lsr-imm, bitfield, tbz/tbnz) can
; land without re-crossing.  See feedback_test_variant_fragility and
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-test-variant-ci-regression.md for the cliff
; history; this mirrors the test_mem off-axis precedent (plan-PR 3 /
; PR #52) exactly.
;
; WHICH suites live here (and why they are LMPR-swap-safe):
;   symbols, local_labels, expr_eval, slots, pc_rel, directives,
;   ror_imm, shifted_reg, extended_reg, litpool, pagepool, emit_paged.
;
;   symbols and local_labels are LMPR-swap-safe: their production
;   routines (symbol_table_init, symbol_lookup, symbol_insert,
;   local_label_table_init, local_def_append, local_find_forward,
;   local_find_backward) are pure in-memory hash-table / linked-list
;   operations residing in HMPR-stable section-C/D (src/symbols.asm,
;   src/local_labels.asm).  Transitive call-graph: the only non-trivial
;   helpers they call are symbol_abs_bit_ptr (pure bit arithmetic,
;   section C) and cmp_pc_at_hl_vs_ref (pure comparison, section C) —
;   NONE reach paged_call, the section-B trampoline, or any port I/O.
;   Their inline `defb` literals read via section A under the swap, the
;   same caveat the slots and expr_eval suites already rely on.
;   They run FIRST in cluster_dispatch (symbols → local_labels →
;   expr_eval → …) so expr_eval and pc_rel see a clean, initialised
;   table state, preserving the ordering those suites depend on.
;
;   expr_eval was relocated here to reclaim ~449 B for the MUL/DIV
;   evaluator routines.  Its dependencies — eval_expr_const (incl.
;   ml_mul/ml_div), symbol_*, local_* — are all HMPR-stable section-C/D
;   routines; none reach paged_call / section B.  It re-inits the symbol
;   + local tables itself, so it is order-independent beyond needing the
;   tables present.  Its inline `defb` bytecode + expectation literals
;   read via section A under the swap (same caveat as the slots suite).
;
;   Every production routine these call is HMPR-stable (section C/D) and
;   — verified by transitive call-graph analysis — NONE reach `paged_call`
;   or the section-B trampoline/comm buffer.  That matters because the
;   LMPR swap below relocates BOTH section A and section B to the off-axis
;   page pair; anything that depends on section B holding the installed
;   paged_call body (e.g. the sysreg encoders used by test_sysname) would
;   break.  Those suites stay inline in the main binary.
;
;   `assert_eq32_de_hl_imm` (defined in test_assert_eq32.asm, resident
;   in the main binary) is reached via importfile.  Its inline-literal
;   `pop bc; (bc)` reads land in section A — which IS the off-axis page
;   under the swap — so the `defb` literals following each call read
;   correctly.  This is the identical caveat that the test_mem off-axis
;   path relies on (see test_mem_offaxis.asm:36-43).
;
; Mechanism at boot (in src/assembler.asm and src/loader.asm):
;   1.  enctab_trampoline_setup installs the HLOAD trampoline.
;   2.  load_offaxis_cluster HLOADs this binary into page 12 at
;       section-C offset &8000.  When section A is mapped to page 12
;       (LMPR = LMPR_TEST_CLUSTER), the binary's bytes appear at
;       &0000-onward in section A.
;   3.  Inline at the call site: OUT (250), A=LMPR_TEST_CLUSTER;
;       CALL &0000 (the cluster_dispatch entry); OUT (250),
;       A=LMPR_DEFAULT_RUNTIME.
;   4.  HMPR is unchanged across the LMPR swap, so calls FROM the
;       off-axis code TO section-C production symbols reach the right
;       addresses.  Stack remains in section D (HMPR), also unchanged.
;
; Page 12 is a free page (Tech Manual "PAGE ALLOCATION TABLE": pages
; 4..12 unused) at boot-self-test time — IN is not HLOADed into pages
; 7..12 until main_assemble runs, long after these self-tests complete.
; This is the same time-multiplexing the test_mem (page 13) and IN
; (pages 7..12) payloads already rely on.

                org     &0000

; -----------------------------------------------------------------------
; cluster_dispatch — entry point reached via `call &0000` from the
; LMPR-swap call site in assembler.asm.  Runs each relocated suite in
; the same relative order it had inline, then RETs to the swap-restore
; sequence.  Any suite's assertion failure does `jp fail` (section C,
; HMPR-stable) and halts before returning.
; -----------------------------------------------------------------------
cluster_dispatch:
                call    run_symbol_table_self_tests
                call    run_local_label_self_tests
                call    run_expr_eval_self_tests
                call    run_slot_self_tests
                call    run_pc_rel_self_tests
                call    run_directives_self_tests
                call    run_ror_imm_self_tests
                call    run_shifted_reg_self_tests
                call    run_extended_reg_self_tests
                call    run_litpool_self_tests
                ; i2b: exercise the live, boot-sized page pool (pool_boot_init
                ; ran earlier in the main boot path). pp_* operate on the
                ; section-D page_owner[] table, HMPR-stable under this swap.
                call    run_pool_self_tests
                ; i24: the pool-run emit path. Runs AFTER the pool suite (which
                ; claims + frees every FREE page) so its own PP_OUT run alloc
                ; sees the boot pool state. LMPR-swap-safe because the suite
                ; never touches LMPR itself: emit_byte / out_run_peek bracket
                ; LMPR internally while executing from section C, restoring the
                ; cluster page before returning (see test_emit_paged.asm).
                call    run_emit_paged_self_tests
                ; i27b: Cortex-A53 erratum 835769 predicate self-tests.
                ; Exercises errata_is_hazard and errata_check_and_handle
                ; (port of errata.go aarch64ErratumSequence:337-358).
                call    run_erratum835769_self_tests
                ret

                include "test_symbols.asm"
                include "test_local_labels.asm"
                include "test_expr_eval.asm"
                include "test_slots.asm"
                include "test_pc_rel.asm"
                include "test_directives.asm"
                include "test_ror_imm.asm"
                include "test_shifted_reg.asm"
                include "test_extended_reg.asm"
                include "test_litpool.asm"
                include "test_pagepool.asm"
                include "test_emit_paged.asm"
                include "test_erratum835769.asm"
