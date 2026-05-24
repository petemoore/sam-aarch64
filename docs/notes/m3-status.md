# M3 — current status (read me first)

Entry point for any session picking up where M3 left off.

**Last update: 2026-05-27. M3 COMPLETE.** All 22 tasks done across 8 PRs (#9, #12, #13, #16, #17, #19). The SAM-side Z80 assembler, running in SimCoupé, reads a `.tbn` file and writes OUT byte-identical to `aarch64-none-elf-as` + `objcopy -O binary` for the M3 fixture corpus (9 fixtures).

## What M3 is (spec recap)

Per `docs/specs/2026-05-24-m3-z80-emitter-design.md`:

A Z80 program that reads a binary-tokenised `.tbn` source file from disk, looks up each instruction's form in the loaded encoder table (`enctab.enc`), encodes the bytes via per-slot encoders, and writes a flat output file via SAMDOS HSAVE. Byte-identical to `refenc`'s Mac-side output (and therefore byte-identical to `aarch64-none-elf-as`).

The M3 scope is **constant-folded operands only** — no symbol table, no forward references, no PC-relative resolution. That's M4's job. The M3 fixture corpus is curated around this constraint.

## Tasks 1–22 — all done

| Task | What | Landed in |
|---|---|---|
| 1 | Scaffold `assembler.asm` | PR #9 |
| 2 | SAMDOS I/O wrappers | PR #9 |
| 3 | Load `enctab.enc` header | PR #9 (fixed via HGTHD+HLOAD in PR #13) |
| 4 | Xreg / Wreg / XregOrSp / WregOrSp slot encoder | PR #12 |
| 5 | Imm5 / Imm6 / CondCode / ShiftAmount slot encoders | PR #12 |
| 6 | Imm12Shifted slot encoder | PR #12 |
| 7 | Imm16Shifted slot encoder | PR #12 |
| 8 | ExtendOp slot encoder | PR #16 |
| 9 | BranchImm26/19/14 slot encoders | PR #16 |
| 10 | AdrpImm slot encoder | PR #16 |
| 11 | LogicalImm slot encoder | PR #16 |
| 12 | BitfieldImm slot encoder (BFI + UBFX) | PR #16 |
| 16 | `.tbn` stream reader (`src/m3/reader.asm`) | PR #19 |
| 17 | Form lookup + operand-kind validation (`src/m3/form_lookup.asm`) | PR #19 |
| 18 | Encoder dispatcher (`src/m3/encoder.asm`) | PR #19 |
| 19 | Constant-only expression evaluator + 64-bit math (`expr_eval.asm`, `ml.asm`) | PR #19 |
| 20 | HSAVE to OUT file (in `main_loop.asm::save_out_file`) | PR #19 |
| 21 | Round-trip driver script (`tools/run-m3-roundtrip.sh`) | PR #19 |
| 22 | M3 fixture corpus + CI `m3` job | PR #19 |

Tasks 13-15 in the original plan turned out to be already-covered subtasks of 4-12; no separate work was needed.

## Test status (all green)

| Layer | Status | Notes |
|---|---|---|
| `make m3-asm` | ✅ PASS | ~8 KB assembler binary builds clean |
| `make m3-disk` | ✅ PASS | Disk builds with samdos2 + auto + assembler + enctab.enc |
| `make test-m3` | ✅ PASS | Boot-time self-tests (each slot encoder) pass; loader header validated |
| `make ci-m3` | ✅ PASS | 9/9 fixtures byte-match GNU after full SAM-side assembly + HSAVE OUT |
| GitHub `m3:` job | ✅ PASS | Wired into `.github/workflows/ci.yml` (PR #19) |
| Spectrum4 regression | ✅ 29/29 | No regression from any M3 work |

## Memory layout (during assembly)

```
&8000-&9FFF  assembler code (~8 KB)
&A000-&AFFF  enctab.enc buffer (was &9000 pre-PR #17; bumped during PR #19)
&B000-&B7FF  IN .tbn buffer (the source being assembled)
&B800-&BFFF  OUT buffer (the bytes being emitted)
&C000-&C0FF  stack (SP = &C100)
&C100-&FFFF  scratch — OPVAL arrays, eval stack
```

`build-m3-disk` takes an optional 4th arg (a `.tbn` to ship as `IN`). Assembler disk slot bumped to 20 sectors.

## Hard-stops (M4 territory)

The M3 encoder dispatcher errors out on these — they need either a symbol table or compound-operand handling, which is M4:

- `BitfieldImm`, `ExtendOp`, `AdrImm` slot kinds (forms exist; dispatcher doesn't yet route to them)
- All compound operands: `OpShiftedReg`, `OpExtendedReg`, `OpMem`, `OpString`, `OpSysName`, `OpLitPool`
- Forward branches to labels (need symbol table)
- `:lo12:` and PC-relative operators on unresolved symbols
- Local labels (`1f`, `1b`)

## Hand-off recipe (verify locally)

```bash
# Inside the sam-aarch64 dev container or with toolchain locally:
make ci-m3
# expect 9/9 fixtures matched + boot-time slot self-tests pass
```

## Authoritative references

- M3 spec: `docs/specs/2026-05-24-m3-z80-emitter-design.md`
- M3 plan: `docs/plans/2026-05-24-m3-z80-emitter.md`
- M4 spec (next): `docs/specs/2026-05-24-m4-symbols-multipass-design.md`
- M4 plan (next): `docs/plans/2026-05-24-m4-symbols-multipass.md`
- ROADMAP: `docs/ROADMAP.md`
