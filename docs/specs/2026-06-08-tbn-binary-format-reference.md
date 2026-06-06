# `.tbn` binary tokenised format — complete encoding reference

**Status**: living reference, current as of 2026-06-08. This is the
single normative description of the on-disk `.tbn` format as actually
implemented. It **supersedes the format sections (§2–§6) of the M1
design spec** (`docs/specs/2026-05-23-m1-binary-tokenised-format-design.md`),
which is now a historical milestone record and predates several
additions (`KindLitInsts`, `OpLitPool`, the grown directive table).

**Authority**: the Go package `tools/sam-aarch64-format/` is the source
of truth; this doc cites it inline (`file.go`). If this doc and the code
ever disagree, the code wins and this doc is the bug. The Z80 side
(`src/main_loop.asm`, `src/reader.asm`, …) mirrors the same constants
(see the `REC_KIND_*` / `OP_KIND_*` equs).

Related: the compaction design lives in
`docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`; this doc
describes the *format* those levels produce.

---

## 1. What a `.tbn` file is

A `.tbn` ("**t**okenised **b**i**n**ary") file is an aarch64 source file
re-expressed as a compact, length-framed binary record stream. It is the
hand-off format between the host tools (`text2bin` tokenises text → `.tbn`;
`refenc` assembles `.tbn` → machine code) and the SAM-side Z80 assembler
(reads `.tbn`, emits machine code), and eventually the on-SAM editor
(renders `.tbn` → text via the disassembler, edits, re-writes `.tbn`).

Design goals, in priority order: (1) the Z80 can consume it with minimal
work — no text parsing, no expression re-lexing; (2) it round-trips back
to readable source (so the editor and `bin2text` can render it);
(3) it is small enough to hold a real kernel source in SAM RAM.

All multi-byte integers are **little-endian** (Z80-native). All strings
are length-prefixed (u16 LE), never NUL-terminated.

---

## 2. File container

```
┌─────────────────────────────────────────────┐
│ Magic    "SA64"      4 bytes  (0x53 41 36 34)│  format.go:7
│ Version  u16 LE      = 1                     │  format.go:10
│ Flags    u16 LE      = 0  (reserved)         │  format.go:13
├─────────────────────────────────────────────┤
│ Name table                                   │  reader.go:130 / writer.go:69
│   count   u16 LE                             │
│   name₀   [len u16 LE][UTF-8 bytes]          │
│   name₁   …                                  │
│   …                                          │
├─────────────────────────────────────────────┤
│ Record stream                                │  reader.go:36
│   record₀ record₁ … recordₙ₋₁                │
│   (length-framed; no terminator)             │
└─────────────────────────────────────────────┘
```

- **Magic / version / flags** (8 bytes total). `ReadFile` rejects a bad
  magic or a version ≠ 1 (`reader.go:117`,`:121`); `Flags` is reserved
  and must be 0.
- **Name table** — the interned label/symbol names, in first-encounter
  order. Each name's **zero-based index is its symbol ID**, referenced by
  `LABEL_DEF` records and `PUSH_SYM` expression opcodes. The interner
  (`symbols.go:16`) assigns IDs sequentially from 0, so re-interning the
  same names in order reproduces identical IDs — which is why the
  compaction pass can rebuild the table from `File.Names`
  (`refenc/main.go:writeCompactTBN`).
- **Record stream** — a flat sequence of length-framed records (§3). EOF
  is implicit: the reader stops when no bytes remain (`reader.go:34`).
  There is no end-of-stream sentinel.

---

## 3. Record stream framing

Every record is a 3-byte header followed by a kind-specific payload:

```
[kind : u8] [len : u16 LE] [payload : len bytes]
```

`len` counts payload bytes only — not the 3-byte header (`reader.go:44`,
`writer.go:14`). Maximum payload is 65535 bytes.

**Forward-compatibility by skip**: because `len` frames every record, a
reader advances over an *unknown* kind without understanding it
(`reader.go:50`). Purely additive kinds therefore do not break older
readers' framing — though an old reader cannot render an unknown kind's
*meaning*, only skip it.

### Record kinds

| Hex    | Name        | Payload | Defined |
|--------|-------------|---------|---------|
| `0x01` | `INST`      | `[mnemonic_id u16][operand_count u8][operands…]` (§4) | kinds.go:7 |
| `0x02` | `LABEL_DEF` | `[symbol_id u16]` — defines a global label at the current PC | kinds.go:8 |
| `0x03` | `LOCAL_DEF` | `[digit u8]` — defines a numeric local label (1–99) | kinds.go:9 |
| `0x04` | `DIRECTIVE` | `[directive_id u8][operand_count u8][operands…]` (§4, §6) | kinds.go:10 |
| `0x05` | `COMMENT`   | `[placement u8][bytes…]` — `0`=standalone, `1`=trailing | kinds.go:11 |
| `0x07` | `LIT_INSTS` | `[count u8][word₀…word₍count₋₁₎]`, each word 4 bytes LE — a run of fully-literal instructions stored as assembled machine code (§7) | kinds.go:17 |

Reserved / not-yet-defined: `0x00`; `0x06` (was earmarked for a *single*
literal instruction — not used, a `count=1` LIT_INSTS run covers it);
`0x08` (**planned** `LIT_DATA`, §7.3); `0x09`–`0xFF`.

Notes:

- `LABEL_DEF` / `LOCAL_DEF` consume no PC. The next `INST` / `DIRECTIVE`
  / `LIT_INSTS` is the labelled site.
- A source line carrying a label *and* an instruction emits **two**
  records back-to-back: `LABEL_DEF` then `INST`.
- `.equ FOO, expr` is a `DIRECTIVE` whose operand list is
  `[symbol-ref operand, value-expression operand]`; there is no separate
  EQU kind (`refenc/pass1.go:resolveEquDirective`).
- `mnemonic_id` indexes `MnemonicTable` (§5); `directive_id` indexes
  `DirectiveTable` (§6.1).

---

## 4. Operand encoding

Inside an `INST` or `DIRECTIVE` payload, `operand_count` operands follow,
each self-describing and beginning with a 1-byte kind (`operands.go:11`,
reader `operands.go:269`). "Self-describing" is deliberate: the inverse
direction (disassembly / `bin2text`) must render an operand without
consulting the encoder form table.

| Hex    | Name           | Payload after the kind byte |
|--------|----------------|-----------------------------|
| `0x01` | `REG_X`        | `[reg u8]` — 0–30 = x0–x30, 31 = xzr |
| `0x02` | `REG_W`        | `[reg u8]` — 0–30 = w0–w30, 31 = wzr |
| `0x03` | `REG_X_SP`     | `[reg u8]` — 31 = sp |
| `0x04` | `REG_W_SP`     | `[reg u8]` — 31 = wsp |
| `0x05` | `IMM_EXPR`     | `[expr_len u16][expr_bytecode]` (§5) |
| `0x06` | `SHIFTED_REG`  | `[width u8][reg u8][shift_kind u8][amt_len u16][amt_bytecode]` |
| `0x07` | `EXTENDED_REG` | `[width u8][reg u8][extend u8][amt_len u16][amt_bytecode]` |
| `0x08` | `MEM`          | `[shape u8][shape payload]` — see below |
| `0x09` | `STRING`       | `[len u16][bytes…]` — `.ascii`/`.asciz` body (escapes pre-decoded) |
| `0x0A` | `COND`         | `[code u8]` — 0=EQ … 15=NV (standard aarch64 cc) |
| `0x0B` | `SYS_NAME`     | `[len u16][bytes…]` — symbolic sysreg / barrier / dc / tlbi name |
| `0x0C` | `LIT_POOL`     | `[width u8][expr_len u16][expr_bytecode]` — `=expr` for `ldr Xn/Wn, =…`; width 8 (X) or 4 (W) |

Reserved: `0x00`, `0x0D`–`0xFF`.

Field conventions (`operands.go`):

- `width`: `0` = W, `1` = X.
- `shift_kind` (`SHIFTED_REG`): 0=lsl, 1=lsr, 2=asr, 3=ror (`operands.go:77`).
- `extend` (`EXTENDED_REG`, `MEM` shape 6): 0=uxtb,1=uxth,2=uxtw,3=uxtx,
  4=sxtb,5=sxth,6=sxtw,7=sxtx (`operands.go:91`).
- `amt_len = 0` in `EXTENDED_REG` means "no `#N`" (bare extend).
- `LIT_POOL` (`0x0C`) is the literal-pool pseudo-operand: it allocates a
  pool slot and the `ldr` becomes a PC-relative load of that slot
  (`refenc/pass1.go:158`, `pass2.go:encodeLdrLitPoolInst`). Because the
  slot's address is layout-dependent, an instruction bearing a `LIT_POOL`
  operand is **never** "fully literal" (§7.1).

### `MEM` (`0x08`) sub-shapes

| shape | Form                      | Payload after `shape` |
|-------|---------------------------|-----------------------|
| `0`   | `[xn]`                    | `[base u8]` |
| `1`   | `[xn, #off]`              | `[base u8][off_len u16][off_bytecode]` |
| `2`   | `[xn, #off]!` pre-index   | `[base u8][off_len u16][off_bytecode]` |
| `3`   | `[xn], #off` post-index   | `[base u8][off_len u16][off_bytecode]` |
| `4`   | `[xn, xm]`                | `[base u8][idx u8][idx_width u8]` |
| `5`   | `[xn, xm, lsl #N]`        | `[base u8][idx u8][idx_width u8][shift_amt u8]` |
| `6`   | `[xn, wm/xm, extend #N]`  | `[base u8][idx u8][idx_width u8][extend u8][shift_amt u8]` |

(`operands.go:64` shape codes; reader `operands.go:295`.) `idx_width`:
0 = W, 1 = X. The offset bytecode in shapes 1–3 may be a constant or a
relocation/symbol expression (e.g. `[x0, #:lo12:sym]`).

---

## 5. Expression bytecode

Operand positions carrying arithmetic — and possibly forward-referenced
labels — embed a length-prefixed **expression bytecode**: a flat opcode
stream run by a small signed-64-bit stack machine (`expr.go`). On entry
the stack is empty; on exit it must hold exactly one value (the result).
There is no `END` opcode — the length prefix bounds the stream.

Why a bytecode rather than text or an AST: text2bin cannot resolve
forward-referenced labels (their PCs aren't known until the Z80's pass 2),
so the expression is stored structured; the Z80 evaluates it at pass-2
time without a text parser.

### Opcodes (`expr.go:11`)

```
── Push (operand inline) ───────────────────────────────
0x01  PUSH_IMM8    [s8]
0x02  PUSH_IMM16   [s16 LE]
0x03  PUSH_IMM32   [s32 LE]
0x04  PUSH_IMM64   [s64 LE]
0x05  PUSH_SYM     [symbol_id u16 LE]
0x06  PUSH_LOCAL   [digit u8][dir u8 — 0=f, 1=b]
0x07  PUSH_PC                              (the `.` operator)

── Binary (pop 2, push 1) ──────────────────────────────
0x10 ADD  0x11 SUB  0x12 MUL  0x13 DIV
0x14 AND  0x15 OR   0x16 XOR  0x17 SHL (<<)  0x18 SHR (>> arithmetic)

── Unary (pop 1, push 1) ───────────────────────────────
0x20 NEG (unary -)   0x21 NOT (~)

── PC-relative / relocation (pop 1, push 1) ────────────
0x30 REL_LO12          value & 0xFFF
0x31 REL_HI12          (value >> 12) & 0xFFF
0x32 REL_ABS_G0   0x33 REL_ABS_G0_NC
0x34 REL_ABS_G1   0x35 REL_ABS_G1_NC
0x36 REL_ABS_G2   0x37 REL_ABS_G2_NC
0x38 REL_ABS_G3
```

Conventions:

- text2bin **constant-folds** aggressively: any sub-expression whose
  leaves are all literals collapses to a single shortest-fit `PUSH_IMMn`
  (`expr.go:WriteImm` chooses the width). So `lsl #(8*4)` becomes one
  `PUSH_IMM8 32`.
- `EvalConst` (`expr.go:226`) evaluates a stream **iff** it contains no
  `PUSH_SYM`/`PUSH_LOCAL`/`PUSH_PC`/`REL_*` opcode — i.e. it is
  context-independent. This is the predicate behind "fully literal" (§7.1).
- `>>` is arithmetic. Reserved opcodes: any not listed; a reader
  hard-errors on an unknown opcode (`expr.go:178`).

**Worked example** — `b target + 4` (with `target` at symbol id 7) is one
`INST`:

```
INST  kind=0x01  len=12
  mnemonic_id   = <id of b>     (u16 LE)
  operand_count = 1             (u8)
  operand 0: IMM_EXPR (0x05) expr_len=6
             PUSH_SYM 7   (0x05 07 00)
             PUSH_IMM8 4  (0x01 04)
             ADD          (0x10)
```

The `b` opcode comes from the encoder form keyed by `mnemonic_id`; the
operand value (`target + 4 − PC`, ÷4, range-checked) is computed by the
`BranchImm26` slot encoder at pass 2 (`refenc/pass2.go:operandsToValues`).

---

## 6. Directives and mnemonics

### 6.1 Directive table (`directives.go:4`)

`directive_id` indexes this slice; the index is the on-disk ID. Listed
in ID order:

| ID | Name | ID | Name |
|----|------|----|------|
| 0  | `.text`   | 11 | `.balign` |
| 1  | `.data`   | 12 | `.org`    |
| 2  | `.byte`   | 13 | `.skip`   |
| 3  | `.short`  | 14 | `.space`  |
| 4  | `.word`   | 15 | `.inst`   |
| 5  | `.quad`   | 16 | `.align`  |
| 6  | `.ascii`  | 17 | `.ltorg`  |
| 7  | `.asciz`  | 18 | `.section`|
| 8  | `.equ`    | 19 | `.arch`   |
| 9  | `.set`    | 20 | `.cpu`    |
| 10 | `.global` | 21 | `.hword`  |

The table is **append-only by ID**; new directives take the next free
integer.

Data directives and their per-element output width: `.byte`=1,
`.short`/`.hword`=2, `.word`=4, `.quad`=8 (`refenc/pass1.go:directiveSizeAtPC`).
**`.short` and `.hword` are byte-identical at encode time but keep
distinct IDs** so the source spelling round-trips verbatim
(`directives.go:27`). `.text`/`.data`/`.global`/`.section`/`.arch`/`.cpu`
are syntactic no-ops at encode time (single-section flat layout).

### 6.2 Mnemonic table (`mnemonics.go:21`)

`mnemonic_id` indexes `MnemonicTable`; the slice index is the ID.
text2bin maps name → id; refenc / the Z80 map id → encoder form.

**Stability policy** (`mnemonics.go:7`): the table is currently *mutable*
(removals + renumberings allowed in lockstep across all consumers)
**only because no `.tbn` files are persisted outside `build/`/`/tmp/`**.
The policy's own trigger to **freeze it strictly append-only** is "once
`.tbn` files start being shipped or persisted." ⚠️ **The compact-`.tbn`
work (i1) is exactly that trigger** — if compact `.tbn` artefacts ever get
committed/shipped, the mnemonic *and* directive tables must become
append-only and any removal needs a `Version` bump. Until artefacts are
actually persisted, the interim mutable policy still holds.

---

## 7. Compaction (literal collapse)

The base format above is the **symbolic** form (the compaction design doc
calls it Level 0/1). Two further forms collapse literal content to its
assembled bytes; both are produced by `refenc` as a `.tbn`→`.tbn`
transform (`refenc -emit-compact-tbn`, `refenc/compact.go`) and both
assemble to the byte-identical binary — verified by the m6-release 3-way
gate.

### 7.1 "Fully literal" predicate

`format.IsFullyLiteral(rec)` (`litinsts.go`) is true for an `INST` whose
operands carry **no** symbol, local-label, PC, relocation, or literal-pool
reference — i.e. every embedded expression passes `EvalConst` and no
operand is `LIT_POOL`. Such an instruction's encoding depends on neither
the symbol table nor (structurally) the PC. The compaction pass adds a
**PC-invariance guard** on top: it encodes the instruction at two
different PCs and only collapses it if the words match — so a
constant-target PC-relative form (e.g. `b 0x1000`, structurally literal
but PC-dependent) is correctly kept symbolic (`refenc/compact.go:literalWord`).

### 7.2 `LIT_INSTS` (0x07) — instruction runs (**implemented**, i1 PR1/PR2)

A run of consecutive fully-literal, PC-invariant instructions is stored as
their assembled words:

```
[kind 0x07][len u16][count u8][word₀…word₍count₋₁₎]   each word 4 bytes LE
len = 1 + 4*count ;  count ∈ 1..255  (longer runs split into successive records)
```

PC accounting is exact (a run occupies `4*count` bytes, identical to the
`count` `INST` records it replaced), so label positions and the 2-pass
values for the surviving symbolic instructions are unchanged. The Z80
assembler memcpys the words straight to OUT — zero encoding work
(`src/main_loop.asm:main_handle_lit_insts`). On the vendored spectrum4
release this shrinks the `.tbn` from 88,644 → 68,755 B (−22.4%).

### 7.3 `LIT_DATA` (0x08) — constant data runs (**planned**, i1 PR3)

The remaining bulk of a compact `.tbn` is numeric data directives
(`.byte`/`.short`/`.hword`/`.word`/`.quad`) whose operands are constants.
Today each element is a full `IMM_EXPR` operand (kind + length + push +
value ≈ 6–12 B) emitting only 1–8 output bytes — a ~5× bloat. PR3 collapses
a run of consecutive **same-directive, all-constant** data records into one
record storing the raw assembled bytes:

```
[kind 0x08][len u16][directive_id u8][raw LE bytes…]
nbytes = len - 1 ;  element_count = nbytes / width(directive_id)
```

**The `directive_id` byte is load-bearing**: it preserves *which* directive
the author wrote (`.byte` vs `.short` vs `.hword` vs `.word` vs `.quad`) so
the disassembler round-trips the source spelling — a requirement, not an
optimisation (a `.hword` table and a `.word` table with the same bytes must
not become indistinguishable). Only runs of the **same** directive id merge,
so the boundary between, say, a `.word` table and an adjacent `.quad` table
is preserved as two `LIT_DATA` records. Symbol-bearing data (e.g.
`.quad some_label`) stays a symbolic `DIRECTIVE` for relocation.

**Measured estimate** (vendored release, analyser over the compact `.tbn`):
1,745 collapsible numeric records currently occupy **21,892 B** (31.8% of
the 68,755 B file) but assemble to only **4,046 B**. PR3 is projected to
save **~13–18 KB**, taking the compact `.tbn` to **~51–56 KB** (−18% to
−26% vs today; **−37% to −43% vs the original 88,644 B symbolic**). `.hword`
tables dominate the collapsible set (17.5 KB) and tend to be contiguous, so
the realistic result skews toward the merged-run end (~51–53 KB).

### 7.4 Level 3 dictionary (future)

A per-project single-byte dictionary for the hottest fully-literal words
(`ret`, `nop`, frame-setup pairs, …). Deferred; revisit only if Level 2+3
data runs don't clear the memory ceiling. Design in the compaction spec.

### Not addressed by compaction

The **name table** (~6.6 KB on release, symbol-name strings) and the
**symbolic `INST`** records (branches / `adrp` / litpool loads — inherently
PC/symbol-dependent) are not collapsible. Symbol-name pooling/shortening is
a separate future lever (M1 spec §9 "Level 1").

---

## 8. Resolution semantics (for implementers)

**Local labels** (`LOCAL_DEF [digit]`): defined at the current PC; multiple
defs of the same digit are legal. At pass 1 the assembler keeps, per digit,
an in-order list of PCs. At pass 2 `PUSH_LOCAL d, dir` resolves: `dir=0`
(forward `Nf`) → the nearest def after the reference; `dir=1` (backward
`Nb`) → the nearest def at-or-before. Digits 1–99 are supported (the table
is a sorted `(digit, pc)` list — see the multi-digit-local-labels work).

**Literal pool**: `ldr Xn/Wn, =expr` (a `LIT_POOL` operand) allocates a
pool slot deduplicated by `(width, expr-bytes)`; `.ltorg` (or end-of-input)
flushes the accumulated slots, 4-byte entries before 8-byte, each aligned
(`refenc/pass1.go:flushPool`). The `ldr` encodes as a PC-relative load of
its slot.

**Comments** (`COMMENT [placement]`): `0`=standalone (own line[s]),
`1`=trailing (same line as the preceding statement). `bin2text` emits
bodies verbatim. text2bin's `-strip-comments` drops them entirely (the
assembler ignores them either way), which is why a compact `.tbn` carries
none.

---

## 9. Cross-references

- **History / rationale / tooling**: M1 spec
  `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md` (§7 tooling,
  §8 test pyramid, §9 open items still apply; its §2–§6 format tables are
  superseded by this doc).
- **Compaction design + increment plan**:
  `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`.
- **Disassembler (the bytes→text inverse)**:
  `docs/plans/2026-05-28-go-aarch64-disassembler.md`; `tools/aarch64dec/`.
- **Code authority**: `tools/sam-aarch64-format/` (`kinds.go`,
  `operands.go`, `expr.go`, `directives.go`, `mnemonics.go`, `symbols.go`,
  `reader.go`, `writer.go`, `litinsts.go`, `format.go`); Z80 mirror
  `src/main_loop.asm` + `src/reader.asm`.
