# M3 spec: Z80 emitter — first SAM-side aarch64 assembly

**Status**: drafted 2026-05-24 autonomously by Claude on Pete's
authorisation to "go as far as possible". Read alongside
`2026-05-09-vision.md`, `2026-05-09-phase1-assembler.md`,
`2026-05-23-m1-binary-tokenised-format-design.md`, and
`2026-05-24-m2-encoder-tables-design.md`.

## 1. Goal & boundaries

M3 is the first milestone where **the Z80 produces aarch64 machine
code**. The M0 stub copied bytes through SAMDOS but didn't emit. M2
designed the encoder tables that drive emission and proved
correctness via the Mac-side `refenc`. M3 ports that emission to
the SAM Coupé:

- A Z80 program reads a binary tokenised source (`.tbn`) file via
  SAMDOS, looks up each instruction's form in the loaded encoder
  table (`enctab.enc`), encodes the bytes, and writes a flat
  output file via SAMDOS HSAVE.
- The output is byte-identical to `refenc`'s Mac-side output for
  the same `.tbn` — and therefore byte-identical to GNU `as` for
  any source `refenc` already passes.

**M3 deliberately defers complexity to M4**:

- No symbol table. M3 only handles instructions whose operands are
  constant-folded by `text2bin` (no `PUSH_SYM`, `PUSH_LOCAL`,
  `PUSH_PC`, no `REL_*` operators).
- No multi-pass design. One pass: read record → encode → write.
- No expression evaluator beyond constant push opcodes. If an
  `IMM_EXPR` operand contains anything other than a single
  `PUSH_IMM*` opcode after constant fold, M3 errors out.
- No directives beyond `.byte` / `.short` / `.word` / `.quad` /
  `.ascii` / `.asciz`. No `.balign` / `.org` / `.skip` / `.equ` /
  `.section`.

**In scope for M3**:

- Z80 assembler source (`src/m3/`) using `pyz80`, paged into the
  SAM via SAMDOS HSAVE-compatible delivery.
- A SAMDOS-readable `enctab.enc` file produced by M2's
  `enctab-gen`, loaded into a paged RAM region at startup.
- A SAMDOS-readable `.tbn` source file injected by `samfile`.
- Encoder dispatch covering every slot kind currently implemented
  by `aarch64enc` in Go: `Xreg`, `Wreg`, `XregOrSp`, `WregOrSp`,
  `Imm5`, `Imm6`, `CondCode`, `Imm12Shifted`, `Imm16Shifted`,
  `ShiftAmount`, `ExtendOp`, `BranchImm26`, `BranchImm19`,
  `BranchImm14`, `AdrpImm`, `LogicalImm`, `BitfieldImm`. The
  Mac-side `aarch64enc` is the executable spec; the Z80
  implementations must produce byte-identical output for every
  test input.
- The M0 round-trip pipeline extended: build → inject → boot
  SimCoupé → extract `OUT` → byte-diff `aarch64-none-elf-as` of
  the same source for a small M3 fixture corpus.

**Out of scope for M3**:

- Symbol table / forward references / multi-pass (M4).
- PC-relative operators with non-constant operands.
- Macros, `.section`, conditional assembly.
- The on-SAM editor (Phase 2).
- TFTP / networking (Phase 3).

## 2. Architecture

### 2.1 Z80 program layout

The Z80 emitter is a single program built by `pyz80`, lifted from
M0's `src/stub.asm` structure:

```
src/m3/
  assembler.asm        top-level: boot, load enctab, read .tbn, loop
  reader.asm           .tbn record streamer (M1 format)
  symbols.asm          name-table loader (consumes M1 §2 name table)
  expr_eval.asm        constant-only expression evaluator (M3 subset)
  encoder.asm          form lookup + per-slot encoder dispatch
  slots/
    xreg.asm           Xreg, Wreg, XregOrSp, WregOrSp (trivial pack)
    immN.asm           Imm5, Imm6, ShiftAmount (trivial range-checked)
    imm12_shifted.asm  add/sub immediate
    imm16_shifted.asm  movz/movk
    extend_op.asm      extended-register option:imm3 pair
    cond.asm           condition code (4-bit pack)
    branch_imm.asm     BranchImm26, BranchImm19, BranchImm14
    adrp_imm.asm       AdrpImm split into immlo:2 / immhi:19
    logical_imm.asm    LogicalImm (port of LLVM processLogicalImmediate)
    bitfield_imm.asm   BFI + UBFX translations
  io.asm               SAMDOS HSAVE / HGFLE wrappers (lifted from M0)
  paging.asm           LMPR / HMPR utilities
```

Estimated size: ~6–8 KB of Z80 assembly, well under SAM's 512 KB
budget. Memory layout (per Phase 1 spec §3, simplified for M3):

| Region | Pages | Notes |
|---|---|---|
| Assembler code | 1 | Page 0 — bootable from SAMDOS |
| `enctab.enc` (form table) | 1 | Loaded at startup; ~4 KB |
| Source `.tbn` buffer | 1–2 | Up to 32 KB; M1 §2 stream |
| Name table (parsed) | <1 | For round-tripping; not used until M4 |
| Output binary buffer | 1–2 | Up to 32 KB of emitted aarch64 |
| Working stack | <1 | |

### 2.2 Disk artifacts

Per the M0 boot-path conventions (see `docs/notes/m0-status.md`),
the test disk contains:

| File | Source | Notes |
|---|---|---|
| samdos2 | T4S1 | SAMDOS binary, vendored |
| auto BASIC | T6S1 | `CLEAR&7FFF; LOAD CODE "assembler" 32768; CALL 32768` |
| assembler | T6S2 | M3's Z80 binary |
| enctab.enc | T6S3 | Encoder tables produced by `enctab-gen` |
| IN | T6S4 | The .tbn source file being assembled |
| OUT | T6S5 (created at runtime) | Output binary |

The `tools/build-disk.sh` script (M0's) is extended to add
`enctab.enc` and `IN` (the `.tbn` for the current fixture).

### 2.3 Encoder table format consumed by Z80

The Z80 reads `enctab.enc` verbatim per the format defined in
`2026-05-24-m2-encoder-tables-design.md` §2:

- Magic "ENC1" + version + flags.
- Form table: `count u32`, then forms with `mnemonic_id u16`,
  `operand_count u8`, `pattern u32`, `mask u32`, slots.
- Mnemonic index: `count u32`, entries with `mnemonic_id u16`,
  `first_form_id u32`, `form_count u16`.

Form lookup at instruction-emit time:

1. Binary-search (or linear, M3-acceptable) the mnemonic index for
   the `mnemonic_id`.
2. Walk `form_count` consecutive forms.
3. Pick the first whose slots' `expected_kind` tuple matches the
   `.tbn` record's operand kinds — same algorithm as Mac-side
   `ValidateOperandKinds`.
4. Run the per-slot encoders to build the 32-bit instruction word.

The Z80 implements ValidateOperandKinds *and* Encode, both in the
order specified by the form table.

### 2.4 Encoder-table parser table

The Z80 has a dispatch table mapping the spec §3 `SlotKind` byte
(`0x01`-`0x25`) to a subroutine address. Each subroutine consumes
the appropriate `OperandValue` (register byte / immediate value /
cond code / etc) plus the slot's `BitPosition` / `BitWidth` and
returns the bits ORed into the result word.

LogicalImm is the only encoder that may *reject* an operand (when
the value can't be expressed as a bitmask immediate). On rejection,
the assembler emits an error and exits.

### 2.5 Constant-only expression evaluator

The M1 expression bytecode (§5 of the M1 spec) is a stack machine
with `PUSH_IMM8`/`PUSH_IMM16`/`PUSH_IMM32`/`PUSH_IMM64`, arithmetic
ops, unary ops, and a handful of REL_* operators. M3 implements
the constant subset:

- `PUSH_IMM8`/`PUSH_IMM16`/`PUSH_IMM32`/`PUSH_IMM64`: push.
- `ADD`/`SUB`/`MUL`/`DIV`/`AND`/`OR`/`XOR`/`SHL`/`SHR`/`NEG`/`NOT`:
  apply.

If a `PUSH_SYM`, `PUSH_LOCAL`, `PUSH_PC`, or `REL_*` opcode appears,
the emitter errors. text2bin's constant-folder should have collapsed
fully-constant expressions to a single `PUSH_IMMn` — anything else
indicates a forward reference or PC-relative arithmetic, both M4
territory.

Z80 64-bit arithmetic is non-trivial. M3 implements add, sub, and
shift on 64-bit values; mul/div on 64-bit are deferred (the encoder
will reject `MUL`/`DIV` opcodes whose operands didn't fold). 64-bit
add+sub+shift in Z80 is ~50 lines per op; mul/div are ~300 lines and
not needed for M3's bounded fixture corpus.

### 2.6 SAMDOS I/O

Lifted from M0's `src/stub.asm`:

- HGFLE (hook 130) — open a file for read. Used for `enctab.enc`
  and `IN`.
- LBYT (hook 145) — load one byte from the open file.
- HSAVE (hook 132) — save a buffer as a file. Used for `OUT`.
- fill_uifa — pre-call UIFA setup.

The M0 `src/sam_io.inc` is reused as-is.

## 3. Test pyramid

### Layer 1 — Z80 unit tests via SimCoupé

A self-test harness inside the assembler binary: hard-coded
`(form, values) → expected u32` pairs, asserted via SimCoupé port
output. Pattern lifted from Phase 1 spec §5 "encoder unit tests".

### Layer 2 — fixture round-trip

For each `.s` fixture in `tests/m3/sources/` (a subset of
`tests/m1/sources/` selected for M3-compatibility — fully
constant-folded operands, no symbol refs, no PC-relative):

1. `text2bin` → `.tbn`
2. `samfile add` to a test disk
3. SimCoupé runs the M3 assembler, writes `OUT`
4. `samfile extract OUT` → bytes
5. Byte-diff vs `aarch64-none-elf-as` of the same `.s`

### Layer 3 — `refenc` parity

For every M3 fixture, `refenc` and the Z80 assembler must produce
byte-identical output. This is M3's transitive proof of
correctness: `refenc` is byte-diffed against GNU `as` in M2,
and Z80 is byte-diffed against `refenc` in M3.

### Layer 4 — real-hardware spot check

Periodically, `samfile add` the M3 disk and boot on real SAM.
Same fixture-corpus check via a hardware-side serial dump. Not in
CI.

## 4. M3 fixture corpus

A subset of M1 fixtures whose operands are fully constant-folded.
From the current M1 corpus the candidates are:

- `empty.s` — nothing to emit.
- `inst_nop_ret.s` — nop, ret (no operands).
- `inst_reg_imm.s` — `add x0, x0, #1`, `sub x0, x0, #2`, `mov x0, #0`.
- `inst_bcond.s` — `cmp x0, #10`, branch ops (without PC-rel —
  `b.lt main` is PC-rel, so this fixture would need a variant).
- `dir_data.s` — `.byte 1, 2, 3` etc.
- `dir_string.s` — `.ascii "hi"`.
- `expr_simple.s` — `mov x0, #(1+2*3)` (constant-folded to `#7`).

Branch and PC-relative fixtures are deferred to M4 because they
require symbol resolution / PC tracking.

## 5. Generator pipeline reuse

The Mac side is unchanged from M2:

- `text2bin` produces the `.tbn`.
- `enctab-gen` produces the `enctab.enc`.
- `samfile` builds the disk.
- `simcoupe` (patched, `-exitonhalt`) runs the assembler.
- `samfile cat` extracts `OUT`.
- `aarch64-none-elf-as` + `objcopy -O binary` produces the
  reference bytes.

The only new tooling: an extension to `tools/build-disk.sh` to
add `enctab.enc` + `IN` to the disk image, and a small driver in
`tools/run-m3-roundtrip.sh` to glue the steps.

## 6. Implementation plan outline (full plan separate)

Tasks in rough order:

1. Scaffold `src/m3/` with a halting assembler.asm that exits
   immediately (mirrors M0's halt-only stub).
2. Read `enctab.enc` into a paged RAM region at startup; parse
   header and validate magic/version.
3. Read `IN` (a `.tbn`) into a paged RAM region; parse M1 file
   header and name table.
4. Streaming record reader: walk the statement stream, dispatch
   on `kind`.
5. Form lookup by `mnemonic_id`.
6. Operand-kind tuple match (Z80 port of
   `aarch64enc.matchOperandKinds`).
7. Per-slot encoders, one at a time, with unit tests:
   - Xreg / Wreg / XregOrSp / WregOrSp
   - Imm5 / Imm6 / CondCode
   - ShiftAmount / ExtendOp
   - Imm12Shifted
   - Imm16Shifted
   - BranchImm26 / BranchImm19 / BranchImm14 (constant offset only)
   - AdrpImm (constant only)
   - LogicalImm (port of LLVM algo)
   - BitfieldImm (BFI + UBFX)
8. Output buffer + HSAVE to `OUT` file.
9. `tools/build-disk.sh` extension for the new files.
10. `tools/run-m3-roundtrip.sh` driver.
11. Fixture corpus + CI `m3` job.
12. `docs/notes/m3-status.md` + README update; declare done.

The plan document is at `docs/superpowers/plans/2026-05-24-m3-z80-emitter.md`
(local-only, gitignored).

## 7. Open items, risks, non-goals

### Open items resolved during implementation

1. **Z80 64-bit arithmetic library**: how much do we implement?
   Resolved: add, sub, shift only. Mul/div are M4+ work and don't
   affect the M3 fixture corpus.
2. **`enctab.enc` paging strategy**: which page does it live on?
   Resolved during plan; default candidate is page 6 (32K offset),
   leaving page 0 for the assembler binary and pages 1–5 for source
   + output buffers. Pete may adjust.
3. **Mnemonic index lookup**: binary search vs linear? Resolved:
   linear for M3 (< 200 entries; Z80 linear scan is ~30 µs).
   Revisit if assembler runtime becomes a concern.

### Risks

- **Encoder table layout drift between Mac and Z80.** The format
  spec is the contract. Any change to `.enc` is a coordinated
  change in `enctab-gen`, `aarch64enc`, and `src/m3/`. Mitigation:
  `enctab.enc`'s magic + version byte; M3's loader rejects
  unknown versions.
- **Z80 LogicalImm correctness.** The LLVM algorithm has subtle
  edge cases. Mitigation: Layer 1 unit tests exercise every valid
  encoding (replicate the Go test corpus into Z80 assertions).
  Layer 3 refenc-parity catches any divergence.
- **64-bit immediates on Z80.** Pushing/popping 8-byte values on
  the Z80 stack is slow. Mitigation: keep the evaluator stack in
  a fixed buffer, not the Z80 SP-stack.

### Non-goals

- No symbol table. Forward references error out at parse time.
- No `.section` / `.balign` / `.org` / `.skip` / `.equ` /
  `.global`. M4 work.
- No mul/div in the expression evaluator.
- No on-SAM error reporting beyond a single-byte exit code + a
  failing test.

## 8. Done criteria

1. M3 fixture corpus byte-matches `aarch64-none-elf-as` end-to-end
   on the patched SimCoupé in the CI dev container.
2. Layer 1 self-tests pass on every commit.
3. Layer 3 refenc-parity passes: for every M3 fixture, the Z80's
   output equals refenc's output.
4. Real-hardware spot check passes for at least one fixture.
5. `make ci-m3` green in GitHub Actions.
6. `docs/notes/m3-status.md` written with hand-off recipe.
