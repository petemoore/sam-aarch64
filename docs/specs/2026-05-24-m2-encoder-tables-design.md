# M2 spec: encoder tables + Mac reference encoder

**Status**: approved 2026-05-24. Read alongside `2026-05-09-vision.md`,
`2026-05-09-phase1-assembler.md`, and
`2026-05-23-m1-binary-tokenised-format-design.md`.

## 1. Goal & boundaries

M2 produces the **aarch64 encoder tables** — a structured data
description of every (mnemonic, operand-kind-tuple) form in the Phase 1
mnemonic subset and how its operands map into bits of the 32-bit
instruction word — and ships:

- A vendored slice of ARM Machine Readable Architecture (MRA) XML
  covering the subset.
- A Go **generator** (`enctab-gen`) that ingests the MRA and emits
  both an on-disk binary `enctab.enc` file and an in-memory Go
  representation in package `aarch64enc`.
- A Mac-side **reference encoder** (`refenc`) — a Go program that
  consumes a `.tbn` file, looks up forms via `aarch64enc`, runs each
  operand encoder, and emits a flat aarch64 binary. Byte-diffed
  against `aarch64-none-elf-as` over the M1 fixture corpus.
- Wiring in `text2bin` that uses `aarch64enc` to validate operand-kind
  tuples per mnemonic, honouring Phase 1 spec §5's promise that
  text2bin owns source-side validation.

The on-disk `enctab.enc` file is the artefact M3's Z80 assembler will
eventually load from disk at startup. M2 itself does **not** touch the
Z80.

**In scope for M2**:

- The normative spec of the on-disk `.enc` format (§2 below).
- The slot-kind catalogue (§3): every operand encoder M3 will need.
- The MRA snapshot and pruning procedure (§4).
- The `enctab-gen` Go binary.
- The `aarch64enc` Go package (hand-written interface + generated
  data file).
- The `refenc` Go binary.
- text2bin's operand-kind validation against `aarch64enc`.
- A four-layer Mac-side test pyramid (§6).
- CI integration (`m2` job).

**Out of scope for M2**:

- No Z80 reader/parser/emitter (M3).
- No machine-code emission from the SAM (M3).
- No new aarch64 mnemonics beyond those already in
  `tools/sam-aarch64-format/`'s `MnemonicTable` after M1.
- No support for forms that need expression evaluation past
  constant-fold + label resolution.
- No CRC / signature / compression in the `.enc` format.

## 2. On-disk `.enc` format

A tokenised encoder-tables file (extension `.enc`) is laid out as:

```
┌────────────────────────────┐
│ Magic    "ENC1"  (4 bytes) │
│ Version  u16 LE            │   v1 only
│ Flags    u16 LE            │   reserved; zero in v1
├────────────────────────────┤
│ Form table                 │
│   count          u32 LE    │
│   forms[count]:            │
│     mnemonic_id  u16 LE    │   matches MnemonicTable index
│     operand_count u8       │   0–7
│     pattern      u32 LE    │   instruction word with operand bits zero
│     mask         u32 LE    │   bits the form fixes (vs operand-driven)
│     operand_slots[operand_count]:
│       slot_kind     u8     │   from §3 catalogue
│       expected_kind u8     │   text2bin operand kind (M1 spec §4)
│       bit_position  u8     │   LSB position in 32-bit word
│       bit_width     u8     │   width in bits
├────────────────────────────┤
│ Mnemonic→form-id index     │   sorted by mnemonic_id, ascending
│   count          u32 LE    │
│   entries[count]:          │   one per known-and-encoded mnemonic
│     mnemonic_id   u16 LE   │
│     first_form_id u32 LE   │   inclusive index into form table
│     form_count    u16 LE   │   consecutive forms for this mnemonic
└────────────────────────────┘
```

Conventions:

- All multi-byte integers little-endian (Z80-native, consistent with
  the M1 `.tbn` format).
- The form table is grouped by mnemonic, so a lookup is:
  `index entry → first_form_id .. first_form_id+form_count-1 →
  linear scan to match operand-kind tuple`.
- Forms within a single mnemonic appear in **specificity order**:
  more specific operand-kind tuples first. text2bin's
  `ValidateOperandKinds` walks forms in order and returns the first
  match. This matters when one mnemonic has overlapping forms (e.g.
  alias forms — see §5.2).
- The index entries are sorted by `mnemonic_id`, enabling binary
  search at lookup time. (Linear scan is also fine; the table is
  small.)

**Practical sizing**:

- Form table `count` is u32 LE, supporting any plausible mnemonic
  expansion. Our M2 subset is estimated < 200 forms.
- `operand_count` ≤ 7: no aarch64 instruction has more operands.
- Expected total file size ~4 KB; comfortably loadable via a single
  SAMDOS HSAVE-style block. No design ceiling is enforced.

**What is NOT in the file**:

- No mnemonic name strings. The mnemonic table is the source of
  truth and lives in `tools/sam-aarch64-format/mnemonics.go`. Both
  M2 and the Z80 (M3) reach the same `mnemonic_id ↔ name`
  correspondence through that single source.
- No CRC / signature. Disk corruption is rare and `samfile` checks
  MGT-level integrity.

**Rationale for `expected_kind` alongside `slot_kind`** (§3 enumerates
both). They serve different consumers:

- `expected_kind` is consumed by text2bin to validate the parsed
  operand-kind tuple before emitting an INST record. Coarse-grained:
  "this slot accepts an immediate expression", "this slot accepts an
  X register".
- `slot_kind` is consumed by the encoder (refenc and eventually the
  Z80) to compute the bit pattern from the operand value. Fine-grained:
  "this slot is a `LogicalImm` field" vs "this slot is a plain
  `Imm12Shifted`". Both can have `expected_kind = OpImmExpr`.

## 3. Slot-kind catalogue

Each form's operand slot has a `slot_kind` byte. v1 catalogue:

```
─── Trivial: pack the value, range-check ───────────────
0x01  Xreg          5-bit reg index (xzr permitted, sp not)
0x02  Wreg          5-bit reg index (wzr permitted, wsp not)
0x03  XregOrSp      5-bit reg index (sp permitted, xzr not)
0x04  WregOrSp      5-bit reg index (wsp permitted, wzr not)
0x05  Imm5          unsigned 5-bit
0x06  Imm6          unsigned 6-bit (e.g. shift amounts)
0x07  CondCode      4-bit condition (text2bin's CondCode passthrough)

─── Mildly bit-fiddly ────────────────────────────────
0x10  Imm12Shifted  12-bit unsigned with optional lsl #12 flag
0x11  Imm16Shifted  16-bit imm + hw shift slot (0/16/32/48)
0x12  ShiftAmount   shift amount for shifted-register form
0x13  ExtendOp      3-bit extend kind + optional shift amount

─── Non-trivial: each has its own dedicated encoder ───
0x20  BranchImm26   PC-rel ±128MB, /4, sign-ext, alignment
0x21  BranchImm19   for b.cond, cbz, cbnz
0x22  BranchImm14   for tbz, tbnz
0x23  AdrpImm       page-rel ±4GB, /4096, split immlo:2 / immhi:19
0x24  LogicalImm    ARM bitmask-immediate (N:1, immr:6, imms:6)
0x25  BitfieldImm   lsb/width → immr/imms (per-form translation)
```

Reserved: `0x00`, `0x08–0x0F`, `0x14–0x1F`, `0x26–0xFF`.

Each non-trivial encoder (`0x20`–`0x25`) cites a normative reference
in its Go implementation:

- `LogicalImm` — LLVM's `processLogicalImmediate` algorithm. The
  encoder must reject immediates that cannot be expressed; the
  rejection is **not** silent — the error surfaces in refenc with
  the source's expression text.
- `BranchImm26` / `BranchImm19` / `BranchImm14` — divide the byte
  offset by 4, sign-extend, range-check, alignment-check.
- `AdrpImm` — divide the byte offset by 4096, split into
  `immlo:2 / immhi:19` bit fields. Bit positions vary by encoding
  (immlo at bit 29, immhi at bit 5, per the ARM ARM).
- `BitfieldImm` — `bfi` / `ubfx` / `bfxil` translate `lsb`/`width`
  to `immr`/`imms` differently per mnemonic. The form table records
  the translation rule via per-form `pattern` bits and the encoder
  applies the rule.

**`expected_kind` mapping table** (M1 operand kinds → slot kinds):

| slot_kind | expected_kind in text2bin |
|---|---|
| `Xreg` | `OpRegX` |
| `Wreg` | `OpRegW` |
| `XregOrSp` | `OpRegXSP` |
| `WregOrSp` | `OpRegWSP` |
| `Imm5`, `Imm6` | `OpImmExpr` |
| `Imm12Shifted`, `Imm16Shifted` | `OpImmExpr` |
| `LogicalImm`, `BranchImm{26,19,14}`, `AdrpImm`, `BitfieldImm` | `OpImmExpr` |
| `ShiftAmount` | (encoded as part of `OpShiftedReg`, validated within) |
| `ExtendOp` | (encoded as part of `OpExtendedReg`, validated within) |
| `CondCode` | `OpCond` |

Where the relationship is many-to-one (multiple slot_kinds →
`OpImmExpr`), text2bin's check is "any immediate expression"; the
slot-kind's bit-fiddly check happens later in the encoder.

`ShiftAmount` and `ExtendOp` are unusual because the parser bundles
the shift/extend into a single `OpShiftedReg` / `OpExtendedReg`
operand. For those, the form table represents the register slot and
the shift/extend slot as **separate** entries with matching
`expected_kind` markers; text2bin treats them as a unit when
validating.

## 4. Generator pipeline

```
┌──────────────────────────┐
│ reference/arm-mra/       │   vendored MRA XML, pruned to our subset
│   (~5–10MB after pruning)│
│ + manifest.json          │   source URL, ARM version, checksum
└──────────────────────────┘
            │ reads
            ▼
┌──────────────────────────┐
│ tools/enctab-gen/        │   Go binary
│   parses MRA XML         │
│   filters to MnemonicTable
│   extracts per-form      │
│     pattern, mask, slots │
└──────────────────────────┘
            │ writes
            ├──→ build/enctab.enc          (binary, per §2)
            ├──→ tools/aarch64enc/data.go  (generated Go source)
            └──→ build/enctab.summary.txt  (human-readable, for diff review)
```

### 4.1 MRA snapshot procedure

The full MRA archive is ~50MB. We do not vendor the whole thing.
Instead, a one-off snapshot script (`tools/enctab-gen/scripts/snapshot.sh`):

1. Downloads the ARMv8.7-A MRA tarball from the URL recorded in
   `reference/arm-mra/manifest.json`. Pins the ARM version.
2. Extracts only the instruction-class XMLs corresponding to our
   42 mnemonics (and their alias families), plus the schema files
   the generator needs.
3. Computes a SHA-256 of the resulting tree and writes it to the
   manifest.
4. The vendored slice plus the updated manifest are committed.

The snapshot script runs rarely — only when ARM publishes an MRA
update we need (e.g. for a new mnemonic). CI does **not** run the
snapshot script; CI assumes the vendored slice is already up to date.

### 4.2 Filtering inside `enctab-gen`

The generator reads the MnemonicTable at generate time (via
`go:embed` of `tools/sam-aarch64-format/mnemonics.go` or by importing
the package). For each mnemonic:

- If MRA contains a matching encoding family, extract every concrete
  form (per encoding variant) and write a row to the form table.
- If MRA contains no matching family, the generator hard-errors with
  the mnemonic name. This means M1 has added a mnemonic that the
  MRA snapshot doesn't cover — fix by extending the snapshot.
- If MRA contains a family for a mnemonic *not* in MnemonicTable,
  silently skip. Reduces output size.

For each form, the generator extracts:

- `pattern` and `mask` from the MRA encoding diagram.
- `operand_slots[]` by interpreting the MRA `<asmtemplate>` and
  field definitions. Each slot's `slot_kind` is chosen by a
  hand-written mapping table in `enctab-gen` keyed on the MRA field
  kind (e.g. `Rd:5 -> Xreg` or `Wreg` depending on register width).

### 4.3 The generated Go package

`tools/aarch64enc/` is **one** Go module. It contains:

- `aarch64enc.go` — hand-written:
  - Type definitions: `Form`, `OperandSlot`, `SlotKind`, `OperandValue`.
  - The `Encode(form, values)` reference encoder.
  - The `ValidateOperandKinds(mnemonic_id, kinds)` lookup.
  - Per-slot-kind encoders for each entry in §3.
- `data.go` — generated by `enctab-gen`; header:
  `// Code generated by enctab-gen from reference/arm-mra/. DO NOT EDIT.`
  Contents: `var formTable = []Form{ … }; var mnemonicIndex = […]indexEntry{ … }`.

Both text2bin and refenc import `aarch64enc`. The Go package does
not know about Z80. M3's Z80 code will reimplement the same `Encode`
algorithm directly against the on-disk `.enc` file.

### 4.4 Sanity invariants checked by the generator

For every emitted form, the generator asserts:

- `pattern & ~mask == pattern` — no operand-bit positions in `pattern`.
- For each operand slot: `0 ≤ bit_position`, `bit_width > 0`,
  `bit_position + bit_width ≤ 32`, and the slot's bit range lies
  entirely within `~mask`.
- No two operand slots in the same form overlap.

Any violation is a generator bug and aborts the generate.

## 5. text2bin integration

text2bin's M1 implementation has hand-coded operand-kind matching:
`matchReg`, `matchShiftKind`, `matchExtend`, `matchCond`. These stay
— they're lexer-level helpers identifying that a token is, say, a
register name.

What changes in M2: when text2bin's parser finishes collecting
operand kinds for an `INST` record, **before** emitting the record,
it calls:

```go
form, ok, diag := aarch64enc.ValidateOperandKinds(mnemonicID, kinds)
```

- If `ok`, text2bin emits the INST record. The chosen `form` is
  attached to the record purely as a sanity-check cross-reference;
  the record on disk does not encode the form id (text2bin's job is
  validation, not encoder-table coupling).
- If `!ok`, text2bin reports `file:line:col: invalid operand kinds
  for <mnemonic>: <diag>` and fails fast.

`ValidateOperandKinds` does a linear scan over the mnemonic's forms
in specificity order (§2) and returns the first one whose
`expected_kind` tuple matches the input. The diagnostic on failure
lists every form's expected kinds, so the user can see what would
have been accepted.

### 5.1 Form-selection determinism

text2bin's choice of form is purely by `expected_kind` tuple. If two
forms have identical tuples, the first one (lowest form id) wins.
The generator orders forms to make this deterministic and
documented (see §2).

### 5.2 Alias handling

aarch64 has many alias forms: `mov (register)` is an alias for `orr
(shifted register)` with `Rn = 31`. For text2bin's purposes, **the
generator synthesises a distinct form for each documented alias**.
So `mov x0, x1` matches `mov (register)`'s form directly, not the
underlying `orr` form. The encoder fills in the alias's constant
fields per the alias rule.

The alternative — having text2bin rewrite aliases at parse time
— would couple text2bin to alias semantics. We avoid that.

## 6. Reference encoder & testing

### 6.1 `tools/refenc/`

Pipeline:

1. Read `.tbn` via `tools/sam-aarch64-format`'s `ReadFile`.
2. **Pass 1** — assign PC to every `INST` record (always 4 bytes)
   and every PC-allocating directive (`.byte`, `.short`, `.word`,
   `.quad`, `.ascii`, `.asciz`, `.skip`, `.balign`, `.org`). Build
   the symbol table by walking `LABEL_DEF` and `LOCAL_DEF` records.
3. **Pass 2** — for each `INST`:
   - Look up the form via `aarch64enc.FormsForMnemonic` and the
     parsed operand-kind tuple. (Recall text2bin already validated
     this; refenc re-validates as a sanity check.)
   - For each operand: evaluate the expression bytecode with the
     now-complete symbol table.
   - Call `aarch64enc.Encode(form, operandValues)` to produce a
     `uint32`. Write the bytes (little-endian) to the output
     buffer.
   - Directives emit their bytes / skip / align directly.
4. Write the output buffer as a flat binary.

### 6.2 Expression evaluator

The M1 `EvalConst` evaluator handled fully-constant expressions
only. M2's refenc has a generalised version that resolves symbols
and PC via the symbol table built in Pass 1. Same opcodes, same
semantics, just non-empty leaves. Lives in `tools/aarch64enc/expr.go`
(or in `refenc/` — implementation detail for the plan).

### 6.3 Test layers

| Layer | Where | What |
|---|---|---|
| 1 | `tools/enctab-gen/*_test.go` | Generator: filter correctness, sanity invariants. |
| 2 | `tools/aarch64enc/*_test.go` | Encoder unit: every slot-kind exercised with edge cases (`LogicalImm` valid + invalid, `BranchImm*` range/alignment, `AdrpImm` ±4GB). |
| 3 | `tools/refenc/*_test.go` | End-to-end: for every `tests/m1/sources/*.s` fixture, `text2bin → refenc → bytes` byte-equals `aarch64-none-elf-as` over the same `.s`. |
| 4 | `tools/text2bin/internal/translate/*_test.go` | Validation: text2bin's table-driven check rejects invalid forms with `file:line:col: invalid operand kinds for <mnemonic>`. |

Layer 3 is the load-bearing oracle: if the encoder tables are wrong,
refenc and GNU `as` disagree byte-for-byte. This is M2's true
correctness test.

### 6.4 Fixture-corpus growth

Some M1 fixtures probably don't cover all encoder kinds (e.g.
`LogicalImm` near the encoding boundary, `BranchImm26` at range
edge, every `AdrpImm` orientation). The M2 implementation plan
audits coverage and adds fixtures under `tests/m1/sources/` as
needed. Adding fixtures regenerates Layer 2's `bin2text` goldens
in M1; the plan includes a regeneration + manual-diff-review
step.

### 6.5 CI

Add an `m2` job to `.github/workflows/ci.yml` paralleling `m1`:

```yaml
m2:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version: '1.26.1'
    - name: Install aarch64 binutils
      run: sudo apt-get update && sudo apt-get install -y binutils-aarch64-linux-gnu
    - name: Run M2 tests
      env:
        AARCH64_AS: aarch64-linux-gnu-as
      run: make ci-m2
```

`m2` runs in parallel with `m1`. Target wall time < 30s.

`Makefile` additions:

```makefile
enctab-gen:
	cd tools/enctab-gen && go build -o $(CURDIR)/$(BUILD)/enctab-gen .

refenc:
	cd tools/refenc && go build -o $(CURDIR)/$(BUILD)/refenc .

enctab: enctab-gen
	$(BUILD)/enctab-gen \
	    --mra reference/arm-mra \
	    --out build/enctab.enc \
	    --gopkg tools/aarch64enc/data.go

test-m2: enctab refenc text2bin
	cd tools/enctab-gen && go test ./...
	cd tools/aarch64enc && go test ./...
	cd tools/refenc && go test ./...
	cd tools/text2bin && go test ./...
	./tools/refenc-roundtrip.sh

ci-m2: test-m2
```

`tools/refenc-roundtrip.sh` is a small shell wrapper that:

1. Runs `text2bin` on every `tests/m1/sources/*.s` → `.tbn`.
2. Runs `refenc` on each `.tbn` → `.bin`.
3. Runs `aarch64-none-elf-as ... && objcopy -O binary` on each
   `.s` → expected `.bin`.
4. Byte-diffs the two `.bin` outputs.

## 7. Open items, risks, non-goals

### 7.1 Open items resolved during implementation planning

1. **Concrete MRA file list.** Which XML files in the MRA tarball
   correspond to which of our mnemonics. Resolved by inventorying
   during M2 plan.
2. **Per-mnemonic form-count audit.** How many concrete forms each
   mnemonic actually has after alias synthesis. Resolved by writing
   a tiny exploration script as the first M2 plan task; informs
   table sizing.
3. **`BitfieldImm` per-form translation rules.** `bfi`, `ubfx`,
   `bfxil` use different `lsb`/`width` → `immr`/`imms` translations.
   The form table needs to encode the rule; the encoder applies it.
   Resolved by reading the ARM ARM and tabulating during M2 plan.
4. **Expression-evaluator pass-2 layering.** Whether refenc's
   symbol-resolving evaluator lives in `aarch64enc` (reusable by M3)
   or `refenc` (one-off). Recommended: `aarch64enc`, so M3 can port
   directly. Resolved during M2 plan.

### 7.2 Risks

- **MRA schema drift.** ARM publishes new MRA versions periodically;
  the schema may change. Mitigation: pin a specific ARM version in
  the manifest. Bumps are explicit and reviewed.
- **Reference encoder = double implementation.** The Go `Encode`
  in M2 and the Z80 `Encode` in M3 must agree. Mitigation: M2's
  Layer 3 byte-compares Go vs GNU `as`; M3 will compare Z80 vs
  Go-on-same-input, which is a transitive proof.
- **Table size growth.** ~42 mnemonics × ~3 forms average ≈ 130
  forms × ~16 bytes per form header + 4 bytes per slot ≈ 3–4KB.
  Comfortably within memory budget. If the subset grows materially
  in a future phase, revisit.
- **Determinism of the generator.** A second `enctab-gen` run on
  the same MRA snapshot must produce a byte-identical `.enc`. The
  generator must impose a total order on every iteration (no Go
  `range` over maps without sorting). Layer 1 includes a
  determinism test that runs the generator twice and diffs.

### 7.3 Explicit non-goals

- No Z80 implementation of anything. M3's responsibility.
- No machine-code emission from the SAM. M3.
- No new aarch64 mnemonics beyond MnemonicTable as it stands after
  M1 (42 entries including 16 b.cond variants).
- No support for forms that need expression evaluation past
  constant-fold + label resolution.
- No CRC / signature / compression in `.enc`.
- No support for SVE, NEON, or FP instructions. Phase 1 deferral.

## 8. Done criteria for M2

1. `enctab.enc` checked into git, byte-stable across regenerations
   (deterministic ordering enforced in the generator).
2. `aarch64enc` Go package compiles; full unit-test coverage of
   every slot-kind in §3.
3. `refenc` byte-matches `aarch64-none-elf-as` on every fixture
   under `tests/m1/sources/`.
4. text2bin rejects malformed-operand-kind programs with a clear
   `file:line:col: invalid operand kinds for <mnemonic>` error.
5. `ci-m2` job green in GitHub Actions.
6. This document is the single normative source; both `enctab-gen`
   and `refenc` cite section numbers from here for every encoded
   shape.
