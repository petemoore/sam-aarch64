; test_offaxis_cluster.asm — off-axis assembly wrapper for the
; "M5 + misc encoder" boot self-test suites.
;
; M6 budget-relief PR (2026-05-29).  See
; docs/notes/2026-05-29-test-variant-budget-relief.md.
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
; docs/notes/2026-05-28-test-variant-ci-regression.md for the cliff
; history; this mirrors the test_mem off-axis precedent (plan-PR 3 /
; PR #52) exactly.
;
; WHICH suites live here (and why they are LMPR-swap-safe):
;   symbols, local_labels, expr_eval_m4, slots, pc_rel, directives_m5,
;   ror_imm, shifted_reg, extended_reg, litpool.
;
;   symbols and local_labels were relocated here in strand-B PR-3
;   (2026-06-07) to reclaim ~775 B of inline section-C budget for the
;   page-15 disassembler loader and self-test.  Both suites call
;   symbol_table_init / local_label_table_init themselves and are
;   therefore order-independent.  Their helper routines (assert_cf_set/
;   assert_cf_clear, set_value_buf_imm, assert_value_buf_eq_imm,
;   set_local_pc_buf_imm, assert_local_pc_buf_eq_imm) move with them;
;   none of these reach paged_call or any section-B routine.  The
;   symbol/local tests run BEFORE expr_eval_m4 (which also re-inits both
;   tables), so the tables are in a known post-init state for each suite.
;
;   expr_eval_m4 was relocated here in PR-3c (2026-05-29) to reclaim
;   ~449 B for the MUL/DIV evaluator routines.  It runs AFTER symbols /
;   local_labels, preserving the prior relative order (symbol/local before
;   expr_eval).  Its dependencies — eval_expr_const (now incl. ml_mul/
;   ml_div), symbol_*, local_* — are all HMPR-stable section-C/D routines;
;   none reach paged_call / section B.  It re-inits the symbol + local
;   tables itself, so it is order-independent.  Its inline `defb` bytecode
;   + expectation literals read via section A under the swap (same caveat
;   as the slots suite).
;
;   Every production routine these call is HMPR-stable (section C/D) and
;   — verified by transitive call-graph analysis — NONE reach `paged_call`
;   or the section-B trampoline/comm buffer.  That matters because the
;   LMPR swap below relocates BOTH section A and section B to the off-axis
;   page pair; anything that depends on section B holding the installed
;   paged_call body (e.g. the sysreg encoders used by test_sysname) would
;   break.  Those suites stay inline in the main binary.
;
;   `assert_eq32_de_hl_imm` (defined in test_slots.asm, which stays in the
;   main binary) is reached via importfile.  Its inline-literal `pop bc;
;   (bc)` reads land in section A — which IS the off-axis page under the
;   swap — so the `defb` literals following each call read correctly.
;   This is the identical caveat that the test_mem off-axis path relies
;   on (see test_mem_offaxis.asm:36-43).
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
                call    run_expr_eval_m4_self_tests
                call    run_slot_self_tests
                call    run_pc_rel_self_tests
                call    run_directives_m5_self_tests
                call    run_ror_imm_self_tests
                call    run_shifted_reg_self_tests
                call    run_extended_reg_self_tests
                call    run_litpool_self_tests
                ret

                include "test_symbols.asm"
                include "test_local_labels.asm"
                include "test_expr_eval_m4.asm"
                include "test_slots.asm"
                include "test_pc_rel.asm"
                include "test_directives_m5.asm"
                include "test_ror_imm.asm"
                include "test_shifted_reg.asm"
                include "test_extended_reg.asm"
                include "test_litpool.asm"
