; test_mem_offaxis.asm — off-axis assembly wrapper for test_mem.asm.
;
; plan-PR 3 of the M6-strand-A paging-architecture rollout.  See
; docs/plans/2026-05-28-plan-pr3-test-corpus-off-axis.md.
;
; This file is its own pyz80 entry point — it is NOT included from
; src/m3/assembler.asm.  The Makefile invokes pyz80 separately on this
; file with `--importfile=build/assembler.sym` so that section-C
; symbols (e.g. `encode_mem_word`, `assert_eq32_de_hl_imm`,
; `OPVAL_ARRAY`) resolve to the addresses they hold in the main
; assembler.bin.  The result is a small standalone binary (build/
; test_mem.bin) that gets loaded at boot into physical page 13 via
; HLOAD and invoked through a LMPR swap.
;
; Why off-axis: test_mem.asm is the largest single BUILD_TESTS-only
; self-test suite (~780 bytes of section-C code).  Without it
; off-axis, the test variant's binary tail spills past the &C100
; OPVAL_ARRAY boundary in section D, causing silent code corruption
; at boot.  See feedback_test_variant_fragility.md.
;
; Mechanism at boot (in src/m3/assembler.asm and src/m3/loader.asm):
;   1.  enctab_trampoline_setup installs the HLOAD trampoline.
;   2.  load_test_mem_off_axis HLOADs this binary into page 13 at
;       section-C offset &8000.  When section A is mapped to page 13
;       (LMPR = LMPR_TEST_MEM = &2D), the binary's bytes appear at
;       &0000-onward in section A.
;   3.  Inline at the run_mem_self_tests call site: OUT (250), A=&2D;
;       CALL &0000 (the entry — first byte of run_mem_self_tests);
;       OUT (250), A=LMPR_DEFAULT_RUNTIME.
;   4.  HMPR is unchanged across the LMPR swap, so calls FROM the
;       off-axis code TO section-C production symbols (encode_mem_word,
;       assert_eq32_de_hl_imm, fail) reach the right addresses and
;       continue to execute correctly under the test-page LMPR.
;       Stack remains in section D, also unchanged.
;
; Caveats:
;   - `assert_eq32_de_hl_imm` pops the inline literal pointer from the
;     stack; that pointer holds a section-A address (where the
;     `defb &xx, &xx, &xx, &xx` literals after each `call
;     assert_eq32_de_hl_imm` reside).  Under LMPR_TEST_MEM section A
;     IS the off-axis page, so the literals are read correctly.  The
;     final `push bc; ret` returns to a section-A address still under
;     LMPR_TEST_MEM, so execution continues in the off-axis code.
;   - A `jp nz, fail` from inside the assert helper goes to section-C
;     &AFAC (`fail`), which prints "FAIL\n" + a tag and `halt`s.  LMPR
;     remains LMPR_TEST_MEM at the halt — fine for SimCoupé exit; the
;     status banner has already been emitted.

                org     &0000
                include "test_mem.asm"
