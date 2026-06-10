# `src/` — the SAM-side Z80 aarch64 assembler

This directory is the SAM Coupé Z80 program that implements an aarch64 assembler: it reads a binary tokenised `.tbn` source (the compact v2 instruction-overlay format, produced by the host `tools/sam-aarch64` assembler), performs two-pass assembly, and writes the resolved machine-code image — byte-identical to GNU `as` + `ld` + `objcopy` on the supported corpus. It runs on real SAM hardware and under SimCoupé.

> **Naming note:** this code formerly lived in `src/m3/` — "m3" was a *historical milestone* name (Milestone 3, the first SimCoupé byte-match), not a description of contents. It was flattened into `src/` in M7 so the layout reads as a logical component (the SAM-side Z80 assembler) rather than a chronology.

## Entry point and include order

`assembler.asm` is the top-level translation unit (`org &8000`; `CALL 32768` lands on the entry `jp start`). It `include`s every other in-section file, **in this order** (the order is load-bearing — code lands in section C `&8000-&AFFF` in this sequence):

1. `io.asm` — low-level SAM I/O primitives.
2. `trampoline.asm` — the paging trampoline (ENCTAB / OUT / IN window swaps via LMPR).
3. `loader.asm` — boot-time HLOAD of the off-axis payloads (ENCTAB, IN buffer, test_mem, cluster, paged-call, sysreg-data).
4. `slots/` — per-operand-kind encoders, included individually: `xreg`, `imm_small`, `imm12_shifted`, `imm16_shifted`, `extend_op`, `branch_imm`, `adrp_imm`, `logical_imm`, `shifted_reg`, `extended_reg`, `mem`.
5. `ml.asm` — multi-precision / math-library helpers.
6. `expr_eval.asm` — expression-bytecode evaluator.
7. `form_lookup.asm` — instruction form table lookup.
8. `encoder.asm` — top-level instruction encoder (drives the slot encoders).
9. `intercepts.asm` — per-mnemonic/per-directive record handlers.
10. `sysname.asm` — sysreg / pstate / dc / tlbi name matching (reads the page-13 payload).
11. `reader.asm` — `.tbn` record reader (paged-IN bracket).
12. `main_loop.asm` — the two-pass driver and per-record dispatch.
13. `symbols.asm` — symbol table (two-pass resolution).
14. `local_labels.asm` — `1f`/`1b` numeric local labels.
15. `litpool.asm` — literal pool + `.ltorg`.
16. `print.asm` — diagnostic printing.

`start:` (the main program) follows the includes; the boot self-tests run before `load_enctab` (the only hard ordering requirement). `pyz80` has no `END` directive — assembly ends at EOF.

## Build variants: prod vs test

Two binaries are built from the same `assembler.asm`, distinguished by the `BUILD_TESTS` define:

- **test variant** (`build/assembler.bin`, `make m3-asm`) — built with `-D BUILD_TESTS=1`. Includes all boot-time self-test suites (`if defined(BUILD_TESTS)` blocks compile in). Larger binary; catches per-routine regressions before the fixture round-trip runs. This is what `ci-m{3,4,5,6}` and `tests/m{3,4,5,6}/run-roundtrip.sh` use. The build also exports `build/assembler.sym` for the off-axis test modules to import.
- **production variant** (`build/assembler-prod.bin`, `make m3-asm-prod`) — built with `BUILD_TESTS` *undefined*, so the self-test blocks are skipped. Smaller binary, more code budget. Emits identical OUT bytes on every fixture (the self-tests don't touch the assemble path); `ci-m{3,4,5,6}-prod` verify this.

Both variants link at `org &8000` and must stay below the `&C000` stack-page cliff — `tools/check-code-budget.sh` enforces this at the tail of each build and via `make check-budget`. See `docs/notes/memory-layout.md`.

## Off-axis test modules

The boot self-tests outgrew the section-C code budget, so the larger suites were moved **off-axis**: assembled separately as standalone binaries (`org &0000`) that `--importfile=build/assembler.sym` to resolve real section-C/D production addresses, then HLOAD'd at boot into spare physical pages and invoked via an LMPR-swap → `CALL` → restore (a "paged call"). They are *not* `include`d from `assembler.asm`.

| File | Built to | Loaded into | Notes |
|------|----------|-------------|-------|
| `test_mem_offaxis.asm` (wraps `test_mem.asm`) | `build/test_mem.bin` (~780 B) | physical page 13 | Largest suite (memory-operand encoders). Plan-PR 3, `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-28-plan-pr3-test-corpus-off-axis.md`. |
| `test_offaxis_cluster.asm` | `build/test_cluster.bin` (~1225 B) | physical page 12 | "M5 + misc encoder" cluster: wraps `test_slots`, `test_pc_rel`, `test_directives_m5`, `test_ror_imm`, `test_shifted_reg`, `test_extended_reg`, `test_litpool` behind a dispatcher. `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-29-test-variant-budget-relief.md`. |
| `paged_call_test_payload.asm` | `build/paged_call_test_payload.bin` (3 B) | physical page 14 | Trivial `ld a,&42; ret` payload exercising the paged-call mechanism (`test_paged_call.asm`). |

The remaining `test_*.asm` files (e.g. `test_symbols.asm`, `test_local_labels.asm`, `test_expr_eval_m4.asm`, `test_emit_paged.asm`, `test_reader_paged.asm`, `test_sysreg_paged.asm`, `test_trampoline.asm`, `test_paged_call.asm`, `test_assert_eq32.asm`) are still in-section and run from the `BUILD_TESTS` path in `assembler.asm`; the off-axis wrappers above pull in the rest.

## Page-13 sysreg payload (production)

`sysreg_data.asm` builds `build/sysreg_data.bin` (~480 B), a standalone binary holding the four sysname lookup tables (sysreg / pstate / dc / tlbi) plus a self-contained matcher. Unlike the off-axis *test* modules, this is a **production** feature needed by **every** build (sysreg/dc/tlbi/pstate operands appear in shipping sources). It is HLOAD'd at boot into physical page 13 by `loader.asm::load_page13_payload` and read at runtime by `sysname.asm` via a paged call.

(Note: page 13 hosts the `sysreg_data` payload in every build; the off-axis *test* `test_mem.bin` is also documented as page 13 in the assembler header for the `BUILD_TESTS` path — the two are mutually exclusive across the prod/test split. The page assignments are the source-of-truth comment block in `assembler.asm`.)

## See also

- `docs/notes/memory-layout.md` — the consolidated section/page map and code-budget ceilings (mirrors the `assembler.asm` header, which is the source of truth).
- `docs/notes/sam-paging.md` — SAM Coupé paging primer.
- `docs/ROADMAP.md` — milestone overview.
