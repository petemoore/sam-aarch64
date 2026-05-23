# M1 spec: binary tokenised source format + text2bin / bin2text

**Status**: approved 2026-05-23. Read alongside `2026-05-09-vision.md`
and `2026-05-09-phase1-assembler.md`.

## 1. Goal & boundaries

M1 designs and implements the **binary tokenised source format** for
Phase 1 aarch64 source files, and ships the two Mac-side tools that
read and write it: `text2bin` and `bin2text`.

The format is the on-disk representation of an aarch64 source file as
consumed by the Z80 assembler (M3 onward) and as eventually produced by
the on-SAM editor (Phase 2). M1 itself ships no SAM-side code.

**In scope for M1**:

- A complete, normative specification of the binary tokenised format
  covering every construct in the Phase 1 spec §2 text dialect.
- `text2bin` — plain-text aarch64 source → binary tokenised file.
  Owns all syntactic validation and operand-kind classification.
- `bin2text` — binary tokenised file → canonically-formatted plain
  text. Pure inverse for round-trip testing and developer export.
- A shared Go library `tools/sam-aarch64-format/` used by both tools.
- A four-layer Mac-side test pyramid (§8).

**Out of scope for M1**:

- No Z80 reader, parser, or emitter (M3).
- No encoder tables / form lookup (M2).
- No machine-code emission (M3).
- No on-SAM editor (Phase 2).
- No macros / conditional assembly / `.section` directives beyond
  `.text` / `.data` / multi-file `.include` — these are explicitly
  deferred by the Phase 1 spec.
- No CRC or signature in the file header — easy to add post-v1.

## 2. File container

A tokenised binary file (suggested extension `.tbn`) is laid out as:

```
┌────────────────────────────┐
│ Magic   "SA64"   (4 bytes) │
│ Version u16 LE             │   bumped on incompatible change
│ Flags   u16 LE             │   reserved; zero in v1
├────────────────────────────┤
│ Name table                 │   [count u16][name₀][name₁]…[nameₙ₋₁]
│                            │   each name: [len u16 LE][UTF-8 bytes]
├────────────────────────────┤
│ Statement stream           │   sequence of length-prefixed records
│                            │   (§3), no terminator
└────────────────────────────┘
```

Conventions:

- All multi-byte integers are little-endian (Z80-native).
- All strings are length-prefixed by a u16 LE; no NUL terminators.
- The name table is written once at file start. Each entry's
  zero-based index is its **symbol ID**, used by `PUSH_SYM` and
  `LABEL_DEF` downstream. Order is text2bin's first-encounter order;
  given a fixed input that order is deterministic.
- End-of-file is implicit. The record reader stops when no bytes
  remain. There is no end-of-stream sentinel record.

**Why magic + version.** Cheap self-identification for `samfile` and
`bin2text`. Version is a single-step counter; we do not encode minor
versions or feature bits. Forward-compatibility for additive changes
is achieved via the record-kind skip mechanism (§3).

**SAM file type byte.** Proposed `0x07` (CODE), reusing existing
samfile machinery. Allocating a dedicated SAM file type is a
samfile/SAMDOS-coordinated change and not strictly necessary in v1.

## 3. Record kinds

The statement stream is a flat sequence of records. Each record has a
3-byte header followed by a kind-specific payload:

```
[kind : u8] [len : u16 LE] [payload : len bytes]
```

`len` covers payload bytes only — not the 3-byte header. Maximum
record size is 65535 bytes; long string literals in `.ascii` /
`.asciz` that exceed this must be split across multiple directives
by text2bin.

The reader uses `len` to advance over unknown record kinds, so
additive extensions in future versions remain readable: a v1
`bin2text` reading a v2 file emits text for every v1-known record
and prints a diagnostic comment for each unknown record encountered
(see §7). Unknown records are *not* re-emittable as text — bin2text
does not preserve raw bytes — so binary round-trip across version
boundaries is not a v1 goal.

v1 kinds:

| Hex   | Name        | Payload |
|-------|-------------|---------|
| `0x01` | `INST`      | `[mnemonic_id u16][operand_count u8][operand₀ … operandₙ₋₁]` — see §4. |
| `0x02` | `LABEL_DEF` | `[symbol_id u16]` — defines a global label at the current PC. |
| `0x03` | `LOCAL_DEF` | `[digit u8]` — defines a local label (1–9). |
| `0x04` | `DIRECTIVE` | `[directive_id u8][operand_count u8][operand₀ … operandₙ₋₁]`. |
| `0x05` | `COMMENT`   | `[placement u8][bytes…]` — `0`=standalone, `1`=trailing. |

Reserved kinds: `0x00`, `0x06`–`0xFF`. text2bin never emits a reserved
kind; bin2text preserves them as opaque skipped regions.

Notes:

- `LABEL_DEF` and `LOCAL_DEF` consume no PC by themselves. The
  next `INST` / `DIRECTIVE` is the labelled site.
- `mnemonic_id` and `directive_id` come from append-only tables
  in `tools/sam-aarch64-format/`. Existing IDs never shift; new
  mnemonics/directives get the next free ID. See open item §9.1.
- A label-defined source line with an instruction on the same line
  emits **two** records back-to-back: `LABEL_DEF` then `INST`.
- `.equ FOO, expr` is a `DIRECTIVE` whose operand list is
  `[symbol_id_operand, expression_operand]`. There is no separate
  `EQU` record kind.

## 4. Operand kinds

Every operand begins with `[kind : u8]`. The Z80 dispatches on `kind`
to the right encoder routine. v1 kinds:

| Hex   | Name           | Payload |
|-------|----------------|---------|
| `0x01` | `REG_X`        | `[reg u8]` — 0–30 = x0–x30, 31 = xzr. |
| `0x02` | `REG_W`        | `[reg u8]` — 0–30 = w0–w30, 31 = wzr. |
| `0x03` | `REG_X_SP`     | `[reg u8]` — 0–30 = x0–x30, 31 = sp. |
| `0x04` | `REG_W_SP`     | `[reg u8]` — 0–30 = w0–w30, 31 = wsp. |
| `0x05` | `IMM_EXPR`     | `[expr_len u16][expr_bytecode]` — see §5. |
| `0x06` | `SHIFTED_REG`  | `[width u8][reg u8][shift_kind u8][amt_expr_len u16][amt_bytecode]`. |
| `0x07` | `EXTENDED_REG` | `[width u8][reg u8][extend u8][amt_expr_len u16][amt_bytecode]`. |
| `0x08` | `MEM`          | `[shape u8][shape-specific payload]` — see below. |
| `0x09` | `STRING`       | `[len u16][bytes…]` — for `.ascii` / `.asciz` / `.inst`. Escapes already decoded by text2bin. |
| `0x0A` | `COND`         | `[code u8]` — 0=EQ … 15=NV. |
| `0x0B` | `SYS_NAME`     | `[len u16][bytes…]` — symbolic system-reg or barrier name. |

Reserved kinds: `0x00`, `0x0C`–`0xFF`.

Field conventions inside operands:

- `width` byte: `0` = W, `1` = X.
- `shift_kind` byte for `SHIFTED_REG`: `0`=lsl, `1`=lsr, `2`=asr,
  `3`=ror.
- `extend` byte for `EXTENDED_REG`: `0`=uxtb, `1`=uxth, `2`=uxtw,
  `3`=uxtx, `4`=sxtb, `5`=sxth, `6`=sxtw, `7`=sxtx.
- `amt_expr_len = 0` in `EXTENDED_REG` means "no `#N`" (the extend
  form without an explicit shift amount).
- `COND` codes match the standard aarch64 cc encoding.

### `MEM` sub-shapes

| Shape | Form                       | Payload after `shape` |
|-------|----------------------------|-----------------------|
| `0`   | `[xn]`                     | `[base u8]` |
| `1`   | `[xn, #off]`               | `[base u8][off_len u16][off_bytecode]` |
| `2`   | `[xn, #off]!` (pre-index)  | `[base u8][off_len u16][off_bytecode]` |
| `3`   | `[xn], #off` (post-index)  | `[base u8][off_len u16][off_bytecode]` |
| `4`   | `[xn, xm]`                 | `[base u8][idx u8][idx_width u8]` |
| `5`   | `[xn, xm, lsl #N]`         | `[base u8][idx u8][idx_width u8][shift_amt u8]` |
| `6`   | `[xn, wm/xm, extend #N]`   | `[base u8][idx u8][idx_width u8][extend u8][shift_amt u8]` |

`idx_width`: `0` = W, `1` = X. Drives whether `extend` for shape 6
must be a `uxtw` / `sxtw` (W index) variant.

**Rationale for an explicit `width` on `SHIFTED_REG` / `EXTENDED_REG`
even though it is recoverable from the encoder form**: bin2text must
print the register name without consulting the encoder table. Each
operand record is self-describing for the inverse direction.

**Rationale for an expression in shift amounts**: source idioms like
`lsl #(8*N)` survive text2bin's constant folder when `N` is a label
or `.equ`-defined symbol. The cost is two bytes (length prefix) per
shift operand in the common case where the amount is a literal; text
representing `lsl #4` is one `PUSH_IMM8` opcode plus its byte (2
bytes payload, total 4 bytes once length-prefix is counted). Cheap.

## 5. Expression bytecode

Operand positions that contain arithmetic — and may involve
forward-referenced labels — embed a length-prefixed **expression
bytecode**: a flat opcode stream evaluated by a tiny stack machine.

Why a bytecode at all: text2bin cannot evaluate expressions
containing forward-referenced labels (their addresses aren't known
until the Z80's pass 2). Storing the expression in a structured form
lets the Z80 evaluate it at pass-2 time without re-parsing text. The
alternatives — embedding original text (forces a string parser into
the Z80) and an AST tree (more bytes, no advantage on Z80) — are
both worse.

The evaluator holds signed 64-bit values. On entry the stack is
empty; on exit it must hold exactly one value, which is the
expression's result. Mismatched stack depth at end is an error.

### Opcodes

```
─── Push operations ──────────────────────────────────────
0x01  PUSH_IMM8       [s8]
0x02  PUSH_IMM16      [s16 LE]
0x03  PUSH_IMM32      [s32 LE]
0x04  PUSH_IMM64      [s64 LE]
0x05  PUSH_SYM        [symbol_id u16 LE]
0x06  PUSH_LOCAL      [digit u8][dir u8 — 0=f, 1=b]
0x07  PUSH_PC                                  (the `.` operator)

─── Binary operators (pop 2, push 1) ─────────────────────
0x10  ADD
0x11  SUB
0x12  MUL
0x13  DIV
0x14  AND
0x15  OR
0x16  XOR
0x17  SHL                                       (`<<`)
0x18  SHR                                       (`>>` arithmetic)

─── Unary operators (pop 1, push 1) ──────────────────────
0x20  NEG                                       (unary `-`)
0x21  NOT                                       (bitwise `~`)

─── PC-relative / relocation operators (pop 1, push 1) ───
0x30  REL_LO12                                  value & 0xFFF
0x31  REL_HI12                                  (value >> 12) & 0xFFF
0x32  REL_ABS_G0       /  0x33  REL_ABS_G0_NC
0x34  REL_ABS_G1       /  0x35  REL_ABS_G1_NC
0x36  REL_ABS_G2       /  0x37  REL_ABS_G2_NC
0x38  REL_ABS_G3
```

Conventions:

- No `END` opcode. The enclosing length prefix tells the evaluator
  when to stop.
- text2bin constant-folds aggressively. Any sub-expression whose
  leaves are all literals collapses to a single `PUSH_IMMn`,
  shortest-fit width.
- `>>` is arithmetic right shift (matches GNU `as`).
- `DIV` / `MUL` opcodes are valid in v1, but M3's Z80 evaluator
  implements them only for constant-folded operands. If a
  non-constant `DIV` / `MUL` survives folding (i.e. one operand is
  a forward-reference), the Z80 emits an `unsupported runtime
  arithmetic` error. The format does not need to change if Z80 MUL/DIV
  is added later.
- Reserved opcodes: any not listed. text2bin never emits a reserved
  opcode; the Z80 hard-errors on encountering one.

### Example

The source line `b target + 4` (with `target` already in the name
table at id 7) becomes one `INST` record:

```
INST    kind=0x01  len=12
        mnemonic_id = <id-of-b>      (u16 LE)
        operand_count = 1            (u8)
        operand 0: kind=IMM_EXPR (0x05)
                   expr_len = 6      (u16 LE)
                   PUSH_SYM 7        (3 bytes:  0x05 07 00)
                   PUSH_IMM8 4       (2 bytes:  0x01 04)
                   ADD               (1 byte:   0x10)
```

The aarch64 opcode for `b` is set by the M2 encoder table keyed by
`mnemonic_id`; the operand value (`target + 4 - PC`, divided by 4,
sign-extension and range-check) is computed by the Z80 operand
encoder `BranchImm26` at pass 2.

## 6. Local labels & comments — resolution semantics

These are subtle enough to spell out so M3's Z80 implementation does
not have to re-derive them.

### Local labels

Per record `LOCAL_DEF [digit]`, the local label is defined at the
current PC. Multiple `LOCAL_DEF` records with the same digit are
legal — that is the point of GNU-style local labels.

At Z80 pass 1, for each digit 1–9 the assembler keeps an in-order
list of `(record_position, pc)` pairs.

At Z80 pass 2, when the expression evaluator hits
`PUSH_LOCAL d, dir`:

- `dir=0` (forward, `1f`): find the smallest `record_position`
  strictly greater than the reference's own record position in
  digit `d`'s list. If none exists, error `no forward local label %d`.
- `dir=1` (backward, `1b`): find the largest `record_position`
  less than or equal to the reference's own record position. If
  none exists, error `no backward local label %d`.

text2bin enforces that `dir` is always 0 or 1 — never "nearest in
either direction" — so resolution is predictable.

### Comments

Per §3, `COMMENT` records have a `placement` byte:

- `placement=0` (standalone): occupies its own line(s) in bin2text
  output. Block comments span multiple lines.
- `placement=1` (trailing): bin2text emits the body on the same line
  as the immediately preceding statement record. text2bin sets
  `placement=1` only when the comment in the source was on the same
  source line *after* a statement's text.

bin2text emits **all** comment bodies verbatim — byte-for-byte. The
canonical-formatting rule governs whitespace and indentation around
comments, never the comment bodies themselves.

A comment lexically inside a statement (e.g. `add x0, /* h */ x1,
#4`) is hoisted by text2bin to one of: trailing-comment after the
statement, or standalone-comment before it (text2bin's choice).
Mid-statement positional fidelity is **not** preserved.

## 7. text2bin & bin2text

Both written in Go, under `tools/text2bin/` and `tools/bin2text/`.
Layout matches the existing Go tools in the repo (`samfile`,
`llist-normalise`, `basic-detokeniser-spike`).

### Shared library: `tools/sam-aarch64-format/`

Single source of truth for the format. Both tools depend on it.
Contents:

- Enum constants for record kinds, operand kinds, expression
  opcodes, shape codes, shift kinds, extend kinds, condition codes.
- Record reader / writer.
- Operand reader / writer.
- Expression bytecode emit / decode + constant folder.
- Symbol-table interner.
- Mnemonic-id and directive-id tables (see open item §9.1).

A change to the format updates this package; the two binaries
recompile against it.

### `text2bin`

CLI: `text2bin INPUT.s [-o OUTPUT.tbn]`. Default output:
`INPUT.tbn` next to the input.

Pipeline (one pass over the source):

1. Lex source into tokens: identifiers, register names, integer
   literals (hex `0x…`, decimal, binary `0b…`, char `'a'`), string
   literals, operator punctuation, EOL, comments. Hand-written
   lexer; no parser-generator dependency.
2. Per logical line, classify: blank, comment-only, label-def,
   local-label-def, directive, or instruction. A line may carry a
   label-def *and* an instruction; both records are emitted in
   order.
3. For instructions: look up mnemonic in the mnemonic-id table.
   Reject unknown mnemonics with
   `file:line:col: unknown mnemonic 'foo'`.
4. For each operand position, parse it into an operand-kind record
   per §4. Reject malformed operand syntax with location-anchored
   errors.
5. For each label reference: intern the name into the symbol table;
   emit `PUSH_SYM <id>`. For local refs: emit `PUSH_LOCAL d, dir`.
6. Constant-fold expressions: any sub-expression whose leaves are
   all literals collapses to a single `PUSH_IMMn` at emit time,
   shortest fit.
7. Write file: magic + version + flags, name table, statement
   records.

Errors are **fail-fast**, single-error, GCC-style
`path:line:col: message`. No multi-error batching in v1.

### Validation ownership

text2bin **owns** (rejects with file-location):

- Unknown mnemonic / directive name / system-reg name.
- Operand-kind shape mismatch (e.g. immediate where register is
  expected).
- Operand count mismatch.
- Out-of-vocabulary register name (`x32`, `wsp` where `sp` expected,
  etc).
- Malformed numeric literal / unterminated string / unterminated
  block comment.
- PC-rel operator applied to a non-label expression.
- Local-label reference with invalid direction byte (parser-level
  invariant).

Z80 (M3) **owns** (reports with binary-offset / record-position):

- Immediate-out-of-range for the matched encoder form.
- Logical-immediate validity (the LLVM-style bitmask algorithm).
- Undefined label / forward reference that no later record defines.
- Stack mismatch at end of an expression bytecode.

This split mirrors the Phase 1 spec §5 error model: text2bin reports
source-side errors; Z80 reports binary-side symbolic errors.

### `bin2text`

CLI: `bin2text INPUT.tbn [-o OUTPUT.s]`. Default output: stdout
(developer use); the test harness uses `-o`.

Pipeline:

1. Read and verify magic + version. Unknown version: hard error.
2. Load the name table into `[]string`.
3. Stream records, dispatching on `kind`. Unknown record kinds are
   skipped via the length prefix; bin2text emits
   `// [skipped unknown record kind 0xNN, %d bytes]` so the skip is
   visible in the output.
4. For each known record, emit canonically-formatted text:
   - One statement per line.
   - Labels at column 0; everything else at column 2 (two spaces).
     No tabs.
   - Operands separated by `, `.
   - `[reg, offset]` rendered with one space after the comma;
     pre-/post-index forms emit `!` / `,` exactly per the spec.
   - Numeric immediates: decimal for absolute values < 256, hex
     otherwise. (A hex/decimal/binary hint is deferred to format
     v2 — see §9.4.)
   - Comments emitted per §6 placement rules.

bin2text is **deterministic**: given the same input it produces
byte-identical output every time. The test suite asserts both this
and the idempotency invariant
`text2bin → bin2text → text2bin → identical bytes`.

## 8. Testing

M1's oracle has no Z80 component yet — that is M3's responsibility.
M1 tests are pure Mac-side and validate the format itself plus the
two tools.

### Layer 1 — format-unit tests (Go, fast)

In `tools/sam-aarch64-format/`. For each record kind, each operand
kind, each expression opcode: hand-write the bytes you expect
text2bin to emit for a one-line source snippet, and the canonical
text you expect bin2text to produce from those bytes.

Catches encoding-table drift and off-by-one mistakes in the
reader/writer at the smallest possible scope.

### Layer 2 — `.s` round-trip golden (Go, medium)

A corpus under `tests/m1/sources/` of `.s` files spanning every
Phase 1 construct (one fixture per family: registers, immediates,
shifted-reg, extended-reg, all six memory shapes, global labels,
local labels, every directive, all comment placements, every
expression operator, every PC-rel operator).

For each fixture:

1. `text2bin fixture.s → fixture.tbn`
2. `bin2text fixture.tbn → fixture.canonical.s`
3. `fixture.canonical.s` is pinned to a golden under
   `tests/m1/golden/`. Subsequent runs diff against the golden;
   mismatch = failure.
4. `text2bin fixture.canonical.s → fixture.canonical.tbn`
5. `fixture.tbn == fixture.canonical.tbn` byte-identical
   (idempotency).

Golden updates are gated by `go test -update` plus a manual review
of the diff.

### Layer 3 — `.tbn` round-trip (Go, fast)

Build hand-crafted `.tbn` files via the format library's writer API
in test code. For each: `bin2text → text2bin → bytes` must equal
the original.

Ensures every encodable shape is reachable from text — no orphan
record kinds or opcode sequences that bin2text can emit but text2bin
cannot produce.

### Layer 4 — GNU `as` cross-check (shell, medium)

For every fixture in `tests/m1/sources/`, run
`aarch64-none-elf-as fixture.s -o /dev/null`. GNU's rejection of a
fixture is a signal that our text dialect has drifted from GNU as;
fix the fixture or the dialect.

No machine-code byte-diff here — that oracle wakes up in M3 when
the Z80 produces real aarch64 bytes.

### CI

`.github/workflows/m1.yml` (or a job added to the existing
`ci.yml`):

- `go test ./tools/...`
- `tests/m1/run-gnu-as-check.sh`

Target wall time: < 30s on `ubuntu-latest`. Reuses the dev
container's `aarch64-none-elf-as`.

## 9. Open items & risks

### Open items resolved during implementation planning

1. **Mnemonic-id table source of truth.** text2bin needs
   `name → id`; M2's encoder generator needs `id → form(s)`.
   Shared `tools/sam-aarch64-format/mnemonics.json` is the natural
   answer, but whether it is hand-curated or filtered from ARM MRA
   XML is an M2-coupled decision. M1 plan picks an interim approach
   and revisits in M2.
2. **Full set of PC-rel relocation operators.** Phase 1 spec carries
   this open item: grep `~/git/spectrum4` for actual usage of
   `:lo12:` / `:hi12:` / `:abs_g0:` / etc. before finalising the §5
   opcode list. The set listed here is a superset of what spectrum4
   appears to use; M1 plan trims it.
3. **`PUSH_IMMn` numeric-base hint.** Currently deferred to format
   v2. If M3 round-trip determinism needs deterministic
   hex-vs-decimal output for specific constants, we will add a
   1-byte hint to the bytecode before freezing v1. Decision
   deferred until the first M3 fixture demands it.
4. **SAM file type byte.** §2 proposes `0x07` (CODE). Allocating a
   dedicated type is a samfile / SAMDOS-coordinated change, not
   required for v1.

### Risks

- **Format ossification.** Once Layer 2 goldens exist, a v1 → v2
  bump regenerates goldens. Mitigation: record-kind and opcode
  spaces have clear reserved ranges, and the record reader skips
  unknown kinds via the length prefix. Purely additive changes do
  not require a version bump. Removals or semantic changes do.
- **Validation-split drift.** text2bin and the (future) Z80 parser
  must agree on which errors live where. Layer 4 catches dialect
  drift but not validation-split drift. Mitigation: §7's
  ownership table is the contract; M3's plan re-cites it.
- **64-bit arithmetic on Z80.** Format commits to signed 64-bit
  expression-stack values. If a Z80 implementation of `MUL` /
  `DIV` proves impractical in M3, the format itself does not
  change — the Z80 simply errors on the offending opcode. Low
  risk.

## 10. Done criteria for M1

1. Every Phase 1 text-dialect construct from spec §2 has at least
   one fixture under `tests/m1/sources/`.
2. All four test layers green locally and in CI.
3. This document is the single normative source: both Go tools and
   the shared library cite section numbers from here for every
   encoded shape.
4. M1 ships `text2bin` and `bin2text` as installed Go binaries
   built by the top-level `Makefile`.
