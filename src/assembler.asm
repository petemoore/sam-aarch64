; M3 Z80 assembler — top-level.
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m3-z80-emitter-design.md §2.1.
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
;                  trampoline copy at TRAMPOLINE_DST (&7E00).  emit_byte
;                  brackets LMPR per byte to map the OUT run's current
;                  page here (the OUT emit window — see emit_byte).
;   &8000-&BFFF  section C — assembler code (this file + all includes).
;                  The IN/OUT/ENCTAB buffers live off-axis (below), so
;                  the whole 16 KB section is code budget.
;                  tools/check-code-budget.sh (run at the tail of
;                  make assembler / assembler-prod) enforces code_end < &C000
;                  — the stack page starts there.
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
;   &F000-&F018  MOV-imm encoder scratch (encode_mov_imm_*; added 2026-05-29
;                for the release byte-match encoder fixes)
;   &F01B-&FFFF  free (~4 KB headroom in section D for future use)
;   (Also: ORIGIN_HIGH &C960 (4 B) + SYMTAB_ABS_BITMAP &C964 (64 B),
;    added 2026-05-29 — see their equ definitions below.)
;
;   Physical page 4 (off-axis): ENCTAB body — paged into section A on
;     demand for encoder runtime reads.  See `src/trampoline.asm`.
;   Physical pages 5..12 (off-axis): page-pool pages (src/pagepool.asm)
;     claimed at assemble time by the two buffer consumers:
;       IN  — a contiguous run allocated by load_in_file
;         (pp_alloc_run(PP_IN)), HLOAD'd once at startup and read via
;         per-record LMPR-bracket into section A on each
;         reader_next_kind call.  See docs/specs/paged-in-design.md.
;       OUT — a contiguous run allocated between the passes by
;         reset_out_buffer (pp_alloc_run(PP_OUT), sized from the pass-1
;         total), written per-byte via an LMPR-bracketed section-B
;         window (emit_byte).  HSAVE at end of pass 2 reads the run via
;         section C with UIFA[31] = OUT_RUN_BASE.  See
;         docs/specs/paged-out-design.md and
;         docs/specs/samdos-file-io.md.
;     Expansion RAM (16..PRAMTP on a >256 KB machine) joins the same
;     pool, so large buffers can land in the 16..31 run.
;   Physical page 12 (off-axis, BUILD_TESTS only): test_cluster.bin —
;     the off-axis encoder self-test cluster
;     (src/test_offaxis_cluster.asm), HLOAD'd at boot and invoked once
;     via an LMPR swap.  Time-multiplexed with the IN buffer, which is
;     not HLOAD'd until main_assemble.
;   Physical page 13 (off-axis): two co-resident PRODUCTION payloads,
;     loaded in BOTH variants (loader.asm):
;       &8000-&83B3  sysreg_data.bin — the sysname lookup tables +
;         matcher (src/sysreg_data.asm), via load_page13_payload,
;         reached via paged_call.
;       &8400-&8B7D  zx0.bin — greedy ZX0 compressor (&8400) + turbo
;         decoder (&8B00) (src/zx0_payload.asm), via load_zx0_payload,
;         reached via paged_call; workspace &8B80-&AF9D; the test disk
;         ships zx0-test.bin which adds the &AFA0 boot self-test +
;         baked fixture.  Per docs/specs/comment-storage-design.md §5.
;     Under BUILD_TESTS the page first holds test_mem.bin (the largest
;     self-test suite, HLOAD'd via load_test_mem_off_axis and invoked
;     via LMPR-swap-CALL-restore from `start:`); the two production
;     payloads overwrite it once that suite completes.
;   Physical page 14 (off-axis): zx0 staging area (§5 of the
;     comment-storage design) — &C000-&E0FF in the section-D view under
;     HMPR=13: raw block in at &C000, compressed result out at &D000.
;     Under BUILD_TESTS the page first holds
;     paged_call_test_payload.bin — the paged_call boot self-test stub
;     (see src/trampoline.asm PAGED_CALL_TEST_PAGE), 3 bytes at offset
;     0, fully consumed before the zx0 self-test stages over it.
;   Physical page 15 (off-axis): disasm.bin — the on-SAM disassembler
;     (src/disasm.asm), HLOAD'd at boot in EVERY build via
;     load_page15_payload (loader.asm, DISASM_PAGE) and invoked via
;     paged_call.
;
; Pre-M5 layout placed ENCTAB at &A000-&AFFF in section C, consuming
; 4 KB of the code section.  M5's compound-operand encoders pushed
; code size past the resulting 8 KB code budget; paging ENCTAB out
; recovers that 4 KB.  See docs/specs/samdos-file-io.md
; for the design rationale.  M6 PR 1 extends the off-axis pattern to
; OUT, freeing 2 KB at &B800 and lifting the output ceiling from
; 2 KB to 32 KB.  M6 PR 2 extends it to IN, freeing another 2 KB
; at &B000 and lifting the input ceiling from 2 KB to 64 KB.
;
; Note: pyz80 does not support the END directive. Assembly ends at EOF.
; The org directive sets the load address; the entry point is the
; first byte.

; IN buffer paging — see docs/specs/paged-in-design.md.
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
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.1.  Pass 1
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

; ORIGIN_HIGH — the high 32 bits of the link origin (s64 OriginVMA).
; PASS_PC only tracks the low 32 bits of the VMA (the output is < 4 GB so
; the low word fully captures every label's offset within the image), but
; the spectrum4 release links at origin 0xfffffff0_00000000 — the high
; word 0xfffffff0 must be re-applied whenever an origin-relative value
; (a label/PC/local-label reference) is materialised as a 64-bit operand,
; e.g. a `.quad <label>`.  Constants (`.quad 0x93`) carry high=0.
;
; Set by `.org`'s set-pc tail from expr_result[4..7] (the evaluated
; origin's high word); reset to 0 by pass_pc_reset so the `-Ttext=0`
; fixture corpus (origin 0) is unaffected.  Mirrors the Go reference
; where every symbol/PC value is `pc` starting at OriginVMA, so it carries
; the full 64-bit origin (tools/refenc/pass1.go:154 res.Symbols[name]=pc
; with pc seeded from OriginVMA; tools/refenc/pass2.go:18,148-152).
ORIGIN_HIGH:    equ     &C960          ; 4 bytes — high word of OriginVMA (u32 LE)

; SYMTAB_ABS_BITMAP — 1 bit per symbol id: 1 = the symbol is ABSOLUTE (its
; full 64-bit value's high word is NOT the link origin's high word, i.e.
; a `.set`/`.equ` constant such as RAM_DISK_SIZE=0x10000000); 0 = the
; symbol is ORIGIN-RELATIVE (a label / PC snapshot / label-derived
; expression whose high word is ORIGIN_HIGH).
;
; Why this exists: the SYMTAB entry stores only the LOW 32 bits of a
; symbol's value.  eval_push_sym reconstructs the high word at use time.
; For origin-relative symbols that word is ORIGIN_HIGH; for absolute
; constants it is 0 (none of the release's .set/.equ constants exceed 32
; bits — verified against spectrum4/src/spectrum4).  Blanket-applying
; ORIGIN_HIGH (the Class-1 fix) was wrong for absolute constants:
; e.g. `mov x9, RAM_DISK_SIZE` saw 0xfffffff0_10000000 instead of
; 0x10000000, breaking the MOV single-chunk decomposition.
;
; The flag is DERIVED at definition from the evaluated high word: if a
; symbol's expr_result[4..7] equals ORIGIN_HIGH it is origin-relative
; (bit 0), else absolute (bit 1) — see main_dir_equ_pass1 / the label-def
; path.  Faithful to the Go reference where Symbols[name] holds the FULL
; value (tools/refenc/pass1.go:154 label=pc-from-OriginVMA;
; pass1.go:296 .set/.equ = raw evaluated value) and eval adds nothing.
;
; 64 bytes cover ids 0..511 (release peak id 474).  RAM-only (no binary
; cost).  Cleared by symbol_table_init.  Bit (id) lives at
; SYMTAB_ABS_BITMAP + (id>>3), mask 1<<(id&7).
SYMTAB_ABS_BITMAP: equ  &C964          ; 64 bytes — per-id absolute flag

; M4 scratch reservation (allocated by symbols.asm + local_labels.asm):
;   &C160-&C95F  SYMTAB              (256 buckets × 8 bytes = 2 KB)
;   &C960-&C963  ORIGIN_HIGH         (4 bytes — high word of OriginVMA)
;   &C964-&C9A3  SYMTAB_ABS_BITMAP   (64 bytes — per-id absolute flag)
;   &C9A4-&CFFF  free (1628 B; old SYMTAB_OVERFLOW + old LOCAL_LABEL_TABLE
;                regions, freed when both were moved to &E100+ on
;                2026-05-28 to absorb the release.tbn census peaks).
; SYMTAB_OVERFLOW is now at &E800 (256 entries, 2 KB) and
; LOCAL_LABEL_TABLE at &E280 (255 entries, 1277 B); see the high-level
; map block above for the full &E100+ layout.

; M5 PR-C scratch: OpMem encoder's 8-byte LE offset value.
; Only one OpMem operand exists per instruction (Rt, [mem] for non-pair;
; Rt1, Rt2, [mem] for ldp/stp) so a single 8-byte slot suffices.
OPMEM_OFF:      equ     &D100          ; 8 bytes — OpMem offset (s64 LE)

; M5 PR-E scratch: literal-pool data structures (see src/litpool.asm).
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
; Pattern mirrors M0's stub (src/stub.asm, retired in M7 — see git history).
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
                ; intercepts.asm (encode_ror_imm_word) and sysname.asm
                ; (mrs/msr/dc/tlbi encoders) are the retired symbolic-encoder
                ; path: production pre-folds every instruction in the v2 overlay
                ; (insn_run.asm) and reaches sysregs via sysreg_data.asm, so the
                ; only callers left are the boot self-tests (test_ror_imm.asm,
                ; test_sysname.asm) and encode_inst (insn_encode.asm's
                ; enc_sysname reuses the sysname.asm encoders). Include them
                ; only in the variants that carry those callers — they cost
                ; production nothing (i73-L9 / q20).
                if defined(BUILD_TESTS)
                include "intercepts.asm"
                endif
                if defined(BUILD_TESTS) | defined(BUILD_TESTS_ENCODE)
                include "sysname.asm"
                endif
                include "reader.asm"
                include "main_loop.asm"
                include "insn_run.asm"
                include "symbols.asm"
                include "local_labels.asm"
                include "litpool.asm"

                include "print.asm"

                ; i2b — the IDE page allocator (src/pagepool.asm) + its boot
                ; survey (src/pool_boot.asm). PP_TABLE_BASE places the resident
                ; page_owner[] table in section D's free region (&C9A4-&CFFF,
                ; documented above), so it stays out of the section-C code
                ; stream. pool_boot_init runs at boot below; run_pool_self_tests
                ; runs in the BUILD_TESTS self-test block.
PP_TABLE_BASE:  equ     &C9A4
                include "pagepool.asm"
                include "pool_boot.asm"

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

; Set up the stack before any call.  The DOS's EI in the RST 8 hook
; re-enables interrupts, so DI must be repeated after hook calls.
                ld      sp, &C100

; Zero ORIGIN_HIGH at cold boot.  It is normally set by `.org`'s set-pc
; tail and reset by pass_pc_reset — but both run inside main_assemble,
; AFTER the BUILD_TESTS boot self-tests.  encode_adrp_imm reads ORIGIN_HIGH
; as the target/PC high word, so the adrp slot self-tests (which assume
; origin 0) would otherwise see uninitialised RAM.  Force it to 0 here,
; before any self-test runs.
                xor     a
                ld      (ORIGIN_HIGH + 0), a
                ld      (ORIGIN_HIGH + 1), a
                ld      (ORIGIN_HIGH + 2), a
                ld      (ORIGIN_HIGH + 3), a

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

; -- Install the DOSER (&5BC0) file-I/O error handler (i25b).  Must run
; BEFORE the first file-I/O hook (load_page15_payload, below): once armed,
; the ROM's PTDOS epilogue dispatches a file-I/O error (file-not-found,
; disk error, ...) to doser_handler_body, which converts it into the
; existing diagnosed FAIL instead of a silent no-op / default halt.  &5BC0
; is in section B (the SAMDOS sysvar page, mapped at boot by BASIC's CALL
; 32768), so this write lands before any enctab_map_in swaps a section.
; Gated out of BUILD_TESTS only: the handler is a runtime error-diagnosis
; feature for the shipped assembler (assembler-prod.bin); the BUILD_TESTS
; self-test variant serves a complete disk and never triggers a file-I/O
; error, so it does not need it — and it sits at the &C000 budget cliff
; with no room to spare.  The enc-tests variant (BUILD_TESTS_ENCODE) has
; ample headroom and carries the handler like prod.
                if defined(BUILD_TESTS)==0
                ; Copy the handler body into section B (LMPR-stable, so it
                ; stays mapped under any HMPR — see DOSER_HANDLER_DST in
                ; src/trampoline.asm for why section C won't do), capture the
                ; boot HMPR for the error path's section-C restore, then arm
                ; (&5BC0) to point at the section-B copy.
                ld      hl, doser_handler_body
                ld      de, DOSER_HANDLER_DST
                ld      bc, doser_handler_body_end - doser_handler_body
                ldir
                in      a, (251)                ; A = boot/default HMPR
                ld      (DOSER_DEFAULT_HMPR), a
                ld      hl, DOSER_HANDLER_DST
                ld      (&5BC0), hl
                endif

; -- i2b/i23/i24: size the IDE page pool from PRAMTP and reserve the
; statically-used pages (0..4 and 13..15). The buffer pages 5..12 are left
; FREE for the pool's two consumers: load_in_file allocates a contiguous IN
; run (pp_alloc_run(PP_IN), i23) and reset_out_buffer allocates a contiguous
; OUT run sized from the pass-1 total (pp_alloc_run(PP_OUT), i24) at assemble
; time. pool_boot reads sysvars (PRAMTP in section B) under the boot LMPR, so
; it must run after the LMPR capture above. The BUILD_TESTS self-tests below
; exercise the live pool.
                call    pool_boot_init

; -- PRODUCTION: HLOAD disasm.bin into physical page 15.  The disassembler
; is a production feature (needed by the editor at runtime); deposited for
; both variants.  Must run AFTER enctab_trampoline_setup (which installs
; the HLOAD trampoline + paged_call body) and BEFORE the BUILD_TESTS
; paged_call to DISASM_SELF_TEST_ENTRY (which calls into the loaded page).
                call    load_page15_payload

; -- BUILD_TESTS only: HLOAD the off-axis test_mem.bin into page 13.
; The Mac-side build pipeline assembles src/test_mem_offaxis.asm
; against the main-binary symbol export, producing a small standalone
; binary that lives at section-A &0000-onward when LMPR = LMPR_TEST_MEM.
; The actual run_mem_self_tests invocation below swaps LMPR briefly,
; calls into the off-axis entry, then restores.
;
; See https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-28-plan-pr3-test-corpus-off-axis.md.
if defined(BUILD_TESTS)
                call    load_test_mem_off_axis

; -- BUILD_TESTS only: HLOAD the off-axis "M5 + misc encoder" cluster
; (pc_rel / directives / ror_imm / shifted_reg / extended_reg /
; litpool) into physical page 12.  Run later, after the inline
; symbol/local-label suites, via the LMPR-swap call site.  Loaded here
; (alongside test_mem) so all off-axis HLOADs happen up front.  Page 12
; is free at boot-self-test time (IN doesn't occupy pages 7..12 until
; main_assemble).  M6 budget-relief PR (2026-05-29).
                call    load_offaxis_cluster
endif

; -- BUILD_TESTS_ENCODE only: HLOAD the encode_inst fixture data payload
; into physical page 11 (i69 lever 3).  Loaded here alongside the other
; off-axis payloads so all HLOADs complete before the self-tests run.
; Page 11 is free at boot-self-test time (same reason as page 12 above).
; run_encode_inst_self_tests bulk-copies from page 11 into section-D RAM
; at ENC_FIX_TABLE_RAM (&E100) via LDIR before enctab_map_in, banking
; ~528 B of section-C headroom.  See src/test_encode_inst_payload.asm.
; The encode_inst family lives in its own boot variant
; (assembler-enc-tests.bin, i234) so section-C test memory is
; time-multiplexed across two boot runs.
if defined(BUILD_TESTS_ENCODE)
                call    load_enc_fix_payload
                ; The overlay-suite code payload (i204b) HLOADs into
                ; physical page 12 — free in this variant for the same
                ; reason page 11 is (the BUILD_TESTS cluster is absent;
                ; IN claims pages 7..12 only at main_assemble).  Executed
                ; later from section-D RAM by the boot stub below.
                call    load_overlay_suite
endif

if defined(BUILD_TESTS)
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
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md.
                call    load_page14_payload
                call    run_paged_call_self_tests
endif

; -- Boot-time self-tests (compiled in only when BUILD_TESTS=1) --------
; The off-axis page-12 cluster (test_offaxis_cluster.asm) runs ALL
; suite suites that exercise symbol_*, local_*, encode_*, and related
; HMPR-stable section-C/D production routines.  The cluster call below
; runs them in a single LMPR-swap-call-restore sequence (9 bytes).
; Any assertion failure does `jp fail` (red border + printer-channel
; "FAIL" banner, ci-core reports fail immediately).  This block is
; omitted from a production build (corresponding includes skipped too —
; see below).
;
; Ordering inside the cluster: symbol → local_label → expr_eval →
; slots → pc_rel → …  The pc_rel suite re-uses the local-label table
; state left by the symbol/local suites that precede it; that ordering
; is preserved because symbol + local_label run FIRST in cluster_dispatch.
; PASS_PC is clobbered by expr_eval but re-zeroed by main_assemble's
; pass_pc_reset call, so it does not need explicit restoration here.
if defined(BUILD_TESTS)
; -- All test suites with HMPR-stable dependencies live off-axis on
; physical page 12 (i69 lever 1, i69 lever 0 / PR-3c).  Same
; LMPR-swap-call-restore mechanism as the test_mem off-axis call
; below: 9 bytes here vs N×3 for the equivalent inline calls.
; HMPR is unchanged, so the off-axis cluster's calls to production
; routines (symbol_*, local_*, encode_*, litpool_*,
; compute_directive_size, pass_pc_reset, assert_eq32_de_hl_imm, fail)
; resolve to their section-C/D addresses.  Verified by transitive
; call-graph analysis that none reach paged_call / the section-B
; trampoline, which the LMPR swap would relocate.  Stack (section D,
; HMPR) unaffected.  See src/test_offaxis_cluster.asm for the full
; safety argument.
                ld      a, LMPR_TEST_CLUSTER
                out     (250), a
                call    &0000                       ; off-axis cluster_dispatch

; -- run_mem_self_tests lives off-axis on page 13 (plan-PR 3).
; LMPR-swap-call-restore sequence: HMPR is unchanged, so the off-axis
; code's calls to encode_mem_word, assert_eq32_de_hl_imm, and fail all
; resolve to their section-C addresses and execute correctly.  Stack
; (section D, HMPR) likewise unaffected.  Interrupts already DI at this
; point (set at start:).
;
; This block sets LMPR directly to LMPR_TEST_MEM rather than first
; restoring LMPR_DEFAULT_RUNTIME: the cluster call above leaves LMPR at
; LMPR_TEST_CLUSTER and nothing between the two off-axis calls reads
; LMPR, so a restore here would only be overwritten by the next OUT.
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
;
; The zx0 payload (compressor + decoder, i68) shares page 13 at &8400+
; and follows the SAME sequencing contract — loaded here, after the
; test_mem suite has fully consumed its page-13 payload, and before the
; zx0 self-test paged_call below.  (test_mem.bin is ~780 B at page
; offset 0, so it never reached &8400 — but the ordering keeps the
; time-multiplex discipline explicit and future-proof.)
                call    load_page13_payload
                call    load_zx0_payload
                call    run_sysreg_paged_self_tests

                call    run_sysname_self_tests
                ; run_litpool_self_tests, run_pool_self_tests (i2b), and
                ; run_emit_paged_self_tests (i24) all run off-axis in the
                ; page-12 cluster above, not inline — they stay out of the
                ; section-C test-variant budget.  The emit suite is
                ; cluster-safe because it never touches LMPR itself
                ; (emit_byte / out_run_peek bracket LMPR from section C).

; -- Disasm stub self-test: invoke run_disasm_self_test on page 15 via
; paged_call.  The self-test (in disasm.bin at DISASM_SELF_TEST_ENTRY)
; calls disasm_entry directly — same page, no nested paged_call.  On
; return BC=0 signals success; non-zero BC carries the fail tag in C.
                call    paged_call
                defw    DISASM_SELF_TEST_ENTRY
                defb    DISASM_PAGE
                call    check_paged_test_result

; -- ZX0 self-test: invoke run_zx0_self_test on page 13 via paged_call
; (i68; docs/specs/comment-storage-design.md §7.1).  The driver (in
; zx0-test.bin at ZX0_SELF_TEST_ENTRY) stages a baked 1 KB comment
; block to page 14 (section D under HMPR=13), runs the page-13
; compressor over it and byte-compares against the Go authority's
; compressed bytes, then decodes the baked compressed bytes with the
; turbo decoder and byte-compares against the raw block.  Exact tests
; both ways — greedy is deterministic.  On return BC=0 signals success;
; non-zero BC carries the fail tag in C (&E1/&E2/&E3).
;
; Must run AFTER load_zx0_payload above (page-13 multiplex: test_mem
; consumed → sysreg + zx0 loaded) and after run_paged_call_self_tests
; (the staging copy overwrites the 3-byte p14 stub at page 14 offset 0,
; which that suite has fully consumed by this point).  The compressor
; needs DI (bit-writer state in the shadow registers) — interrupts are
; disabled throughout the boot self-tests.
                call    paged_call
                defw    ZX0_SELF_TEST_ENTRY
                defb    ZX0_PAGE
                call    check_paged_test_result
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
;
; The zx0 payload follows the same pattern: this unconditional call is
; the ONLY load in a production build (the editor's compress/decode
; paths need it resident), and an idempotent reload in the test variant.
                call    load_page13_payload
                call    load_zx0_payload

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
                ; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-reader-paged-self-test-investigation.md
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
                ; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-reader-paged-self-test-investigation.md
                ; (PR-6 resolution section at the foot).
                call    run_reader_paged_self_tests
endif

; -- encode_inst self-test: the standalone instruction encoder (i199 /
; i48c-b8e brick 1).  Runs here (after load_enctab) because encode_inst
; reads the form table from ENCTAB; it opens its own enctab_map_in /
; form_lookup_init / enctab_map_out bracket internally.  Must precede
; main_assemble so any failure is reported before the assemble loop.
; Lives in the second boot variant (BUILD_TESTS_ENCODE,
; assembler-enc-tests.bin, i234): the encode family is ENCTAB-coupled
; and must stay inline in section C, so it gets a boot run of its own
; instead of sharing variant 1's section-C test budget.
if defined(BUILD_TESTS_ENCODE)
                call    run_encode_inst_self_tests
                ; The section-D suite payload: overlay_classify /
                ; literal_word / compact_inst (i204b) plus the compact
                ; encoder adapter + its full-pipeline self-test
                ; (i48c-b8e) — the compactor half that reuses
                ; encode_inst, so it runs in the same variant, right
                ; after the encoder suite.  The suite code is too large
                ; for this variant's section-C budget, so it rides the
                ; page-12 "ovl12" payload (src/test_overlay_suite.asm)
                ; and executes from section-D RAM — see
                ; OVERLAY_SUITE_RAM in src/trampoline.asm.  The payload
                ; is self-describing: [code_len u16 LE][code], with the
                ; code org'd at OVERLAY_SUITE_RAM.  Copy it there and
                ; call its fixed entry (the first copied byte — a stub
                ; running both suites in order).
                ; Must run after run_encode_inst_self_tests: that routine's
                ; LDIR stages the shared page-11 enc_fix payload (incl. the
                ; toc_* / cadapt_* fixture tables) into section D at
                ; ENC_FIX_TABLE_RAM.
                ld      a, (LMPR_DEFAULT_RUNTIME)
                push    af                      ; save current LMPR
                ld      a, LMPR_OVERLAY_SUITE
                out     (250), a                ; section A = page 12
                ld      hl, (&0000)             ; code length (payload header)
                ld      b, h
                ld      c, l                    ; BC = LDIR count
                ld      hl, &0002               ; source: code after the header
                ld      de, OVERLAY_SUITE_RAM   ; dest: free section-D RAM
                ldir
                pop     af
                out     (250), a                ; restore LMPR
                call    OVERLAY_SUITE_RAM       ; overlay + compact-adapter suites
endif

; -- Run the assemble: pass 1 (table build) + pass 2 (emit) -----------
; main_assemble owns the two-pass dance AND the ENCTAB-window
; bracketing: it loads IN (LMPR=DEFAULT for DOS hooks), then
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
; The DI at start: is undone by the DOS's EI inside the RST 8 hook window
; (ROM PTDOS does EI before dispatching — see docs/notes/headless-simcoupe.md
; "Why the stub ends in DI; HALT").
; Re-issue DI so HALT with IFF1=0 triggers SimCoupé's -exitonhalt.
                di
                halt

if defined(BUILD_TESTS)
; check_paged_test_result — shared tail for the paged self-tests (disasm
; on page 15, zx0 on page 13).  Each returns BC=0 on success; a non-zero
; BC carries the fail tag in C.  Return on success; otherwise jp
; fail_with_tag with the tag in A.  Factored from the two identical
; 9-byte result checks to spare the test-variant code budget.  Placed
; after the `halt` above so control never falls into it (it is reached
; only by `call`).
check_paged_test_result:
                ld      a, b
                or      c
                ret     z
                ld      a, c
                jp      fail_with_tag
endif


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
; Diagnostic: emit LAST_FAIL_PC as four ASCII hex digits (high byte first)
; immediately after the tag, so the banner reads FAIL<tag><pc>.  A non-zero
; PC names the call site of a failing inline-literal assert helper (record
; it via fail_at_bc / fail_at_ret, resolve it against build/assembler.sym);
; zero means the failure came from a direct fail / fail_with_tag site, for
; which the tag is the identifier.  Because `fail` halts, only one fail
; fires per run, so a value here is never stale.
                ld      a, (LAST_FAIL_PC+1)
                call    print_hex_byte
                ld      a, (LAST_FAIL_PC)
                call    print_hex_byte
                ld      a, 10
                call    print_status_char
                di
                halt

; -----------------------------------------------------------------------
; doser_handler_body — the SAM-side DOSER (&5BC0) file-I/O error handler
; (i25b).  The ROM's PTDOS epilogue does the equivalent of `jp (DOSER)`
; after every file-I/O hook (HGTHD/HLOAD/HSAVE) with A = the SAMDOS error
; number (0 = success).  Success resumes the caller (so a normal load/save
; flows on unchanged); a real error converts to the existing diagnosed FAIL,
; passing the SAMDOS error number straight through as the fail tag (the
; banner reads FAIL<errno>, e.g. FAIL6b for error 107 = file-not-found).
;
; These bytes are LDIR-copied into section B at DOSER_HANDLER_DST at boot
; (see the install above) — they MUST live in section B because the ROM
; dispatches DOSER with the caller's HMPR still live, which during a
; trampoline'd HLOAD pages section C onto the payload (see DOSER_HANDLER_DST
; in src/trampoline.asm).  The body has no internal relative branches, so it
; is position-independent: `ret z` / `out` / `jp fail_with_tag` /
; `ld a,(DOSER_DEFAULT_HMPR)` all reference absolute targets that resolve
; the same wherever the copy runs.
;
; Port of COMET's `dier` idiom (reference/comet-decoded/comet.asm:1342) —
; alt D (minimal): no per-error-number messages and no recovery-stack unwind
; (alt A, future), and no vector disarm (fail halts, so there is never a
; return to re-arm).  The error path's HMPR restore is the analogue of
; dier's `OUT (250),A` LMPR restore (comet.asm:1361): re-map the assembler's
; code into section C before jumping to the section-C fail handler.  `and a`
; and the push/pop af bracket leave A = the error number for fail_with_tag.
; Gated out of BUILD_TESTS only: keeps that self-test variant, which sits
; at the &C000 budget cliff, byte-for-byte unchanged; prod and the
; enc-tests variant (BUILD_TESTS_ENCODE) both carry it.
; -----------------------------------------------------------------------
                if defined(BUILD_TESTS)==0
doser_handler_body:
                and     a
                ret     z                   ; A==0: success → resume the caller
                push    af                  ; preserve A = SAMDOS error number
                ld      a, (DOSER_DEFAULT_HMPR)
                out     (251), a            ; restore code HMPR → section C = assembler
                pop     af
                jp      fail_with_tag       ; A = SAMDOS error number → diagnosed FAIL
doser_handler_body_end:
                endif

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

; LAST_FAIL_PC — diagnostic call-site PC for a failing inline-literal assert
; helper, recorded on the fail path only (via fail_at_bc / fail_at_ret).
; Resides in section C so off-axis suites (running under an LMPR swap, HMPR
; stable) can store into it.  Printed by `fail` after LAST_FAIL_TAG.
LAST_FAIL_PC:   defw    0


; -----------------------------------------------------------------------
; Boot-time self-test includes (the two self-test variants only —
; BUILD_TESTS carries every suite except the encode_inst family, which
; lives in the BUILD_TESTS_ENCODE variant, i234).
;
; Placed AFTER `start:` and `fail:`.  Post-budget-lever, the test code
; can legitimately spill into the full &8000-&AFFF code window
; (ENCTAB no longer occupies section C); the production binary's code
; budget is now 12 KB total instead of 8 KB.
; -----------------------------------------------------------------------
if defined(BUILD_TESTS) | defined(BUILD_TESTS_ENCODE)
                ; Shared inline-literal assertion helper, resident in the
                ; main binary so inline, off-axis (cluster + test_mem via
                ; importfile), and encode-variant suites all resolve it.
                include "test_assert_eq32.asm"
endif
if defined(BUILD_TESTS)
                ; test_symbols.asm and test_local_labels.asm live off-axis
                ; in the page-12 cluster (test_offaxis_cluster.asm).
                ; test_expr_eval, test_slots, test_pc_rel, test_directives,
                ; test_ror_imm, test_shifted_reg, test_extended_reg, and
                ; test_litpool likewise live off-axis in the same cluster,
                ; assembled separately into build/test_cluster.bin and
                ; HLOADed at boot via load_offaxis_cluster.  See
                ; src/test_offaxis_cluster.asm.
                ;
                ; test_mem.asm lives off-axis on physical page 13;
                ; see load_test_mem_off_axis / plan-PR 3 (PR #52).
                include "test_sysname.asm"
                ; test_litpool.asm is NOT included inline: its
                ; run_litpool_self_tests is dispatched only from the
                ; off-axis page-12 cluster (test_offaxis_cluster.asm),
                ; which compiles its own copy.  The former inline include
                ; here emitted dead code (never called inline since the
                ; PR-3c cluster move, see line ~342) — removed in the
                ; Class-1 64-bit-address-data fix (2026-05-29) to reclaim
                ; test-variant budget for the ORIGIN_HIGH machinery.  No
                ; behavioural change: the cluster still runs the suite.
                include "test_trampoline.asm"
                ; test_emit_paged.asm lives off-axis in the page-12 cluster
                ; (test_offaxis_cluster.asm); its resident support helper
                ; out_run_peek is in test_assert_eq32.asm above.
                include "test_reader_paged.asm"
                include "test_paged_call.asm"
                include "test_sysreg_paged.asm"
endif
if defined(BUILD_TESTS_ENCODE)
                ; encode_inst (the standalone instruction encoder) + its
                ; self-test (i199 / i48c-b8e brick 1).  Guarded under
                ; BUILD_TESTS_ENCODE — the second self-test boot variant
                ; (i234): the ~2.7 KB encode family is ENCTAB-coupled and
                ; inline-only, so it time-multiplexes section-C test memory
                ; with variant 1's suites across two boot runs.  Nothing in
                ; the production assemble path calls encode_inst yet
                ; (Compact wires it in a later b8e brick), so it stays out
                ; of the production binary too.
                include "insn_encode.asm"
                include "test_encode_inst.asm"
endif

; -- paged_call body source ---------------------------------------------
; Included LAST so the body source bytes appear at the end of the
; binary, NOT near the BUILD_TESTS test-data labels (boot_hmpr,
; reader_paged_*) which live alongside the test_*.asm includes
; above.  The body is NEVER executed from its
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
