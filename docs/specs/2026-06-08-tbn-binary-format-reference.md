# `.tbn` binary tokenised format — complete encoding reference

**Status**: living reference for the **v2 / instruction-overlay** format,
current as of 2026-06-09 (M8 / i48a). This is the single normative
description of the on-disk `.tbn` format. It **supersedes the format sections
(§2–§6) of the M1 design spec**
(`docs/specs/2026-05-23-m1-binary-tokenised-format-design.md`) and the i1
compaction note (`docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`),
both now historical milestone records.

### One serialized format — the compact overlay

There is exactly one on-disk `.tbn` form: the **compact overlay** (v2). Every
instruction is folded into a `KindInsnRun` (0x09) run, constant numeric data
into a `KindLitData` (0x08) record, `KindDirective` (0x04) records are carried
verbatim, and the resolved position-labels / numeric-local def sites live in
the two **header position tables** (§2.4). Editor-only data — the name strings,
`.global` provenance, and comments — relocates out of the assembler-facing
record stream into a trailing **editor region** (M8 / i39b-2, §2.3/§2.5): the
SAM assembler never reads it. The `Version` field is `2`. This is the form the
host emits with `sam-aarch64 SRC.s -o OUT.img --emit-tbn OUT.tbn` and the only
form the SAM loads (`src/main_loop.asm`, `src/insn_run.asm`, `src/reader.asm`).

The host front-end does build a *symbolic* intermediate representation while
turning source text into the overlay (one record per instruction / label /
directive). That IR is held **in memory only** — a slice of decoded
`format.Record` structs the front-end hands directly to the assembler in the
same process; the symbolic record kinds are **never serialized** (not to disk,
not even to an in-memory byte stream) (`tools/sam-aarch64/main.go:runAssemble`,
`tools/sam-aarch64/frontend/translate.go`). It is therefore not part of the
on-disk format and is not described here; see the i48 design
(`docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md`,
Decision A) for why the symbolic form is an in-memory IR rather than a
serialized profile. The pre-v2 (v1 / symbolic) on-disk format was never
released; it survives only in git history.

**Authority**: the Go package `tools/sam-aarch64-format/` is the source
of truth; this doc cites it inline (`file.go`). The overlay fold-rules live
in `tools/aarch64enc/overlay.go` (the `FoldSlot` enum) and the compaction
pass that produces overlay records is `tools/sam-aarch64/assemble/compact.go`.
If this doc and the code ever disagree, the code wins and this doc is the bug.
The Z80 side (`src/main_loop.asm`, `src/insn_run.asm`, `src/reader.asm`, …)
mirrors the same constants (see the `REC_KIND_*` / `OP_KIND_*` equs).

---

## 1. What a `.tbn` file is

A `.tbn` ("**t**okenised **b**i**n**ary") file is an aarch64 source file
re-expressed as a compact, length-framed binary record stream — the compact
overlay. It is the hand-off format from the host assembler (`sam-aarch64`
tokenises and folds source text → compact `.tbn`) to the SAM-side Z80
assembler (reads `.tbn`, emits machine code), and eventually the on-SAM
editor (renders `.tbn` → text via the disassembler, edits, re-writes `.tbn`).

Design goals, in priority order: (1) the Z80 can consume it with minimal
work — no text parsing, no expression re-lexing; (2) it round-trips back
to readable source (so the editor and `sam-aarch64 --render` can render it);
(3) it is small enough to hold a real kernel source in SAM RAM.

All multi-byte integers are **little-endian** (Z80-native). All strings
are length-prefixed (u16 LE), never NUL-terminated.

---

## 2. File container

```
┌─────────────────────────────────────────────┐
│ Magic    "SA64"      4 bytes  (0x53 41 36 34)│  format.go:7
│ Version  u16 LE      = 2                     │  format.go:13
│ Flags    u16 LE      = 0  (reserved)         │  format.go:16
│ editor_region_offset u32 LE  (§2.3)          │  writer.go / reader.go
├─────────────────────── assembler-facing region ──────────────┤
│ Label table  (§2.4)                          │  header_tables.go:35,90
│   count   u16 LE                             │
│   row₀    [name_id u16 LE][offset_delta uvarint]
│   …                                          │
├─────────────────────────────────────────────┤
│ Local table  (§2.4)                          │  header_tables.go:64,117
│   count   u16 LE                             │
│   row₀    [digit u8][offset_delta uvarint]   │
│   …                                          │
├─────────────────────────────────────────────┤
│ Record stream                                │  reader.go:71
│   record₀ record₁ … recordₙ₋₁                │
│   (length-framed; ends at editor_region_offset)
├───────── editor region (at editor_region_offset, §2.3) ───────┤
│ Name table                                   │  editor_region.go
│   count   u16 LE                             │
│   name₀   [shared_prefix_len uvarint][suffix_len uvarint][suffix]
│   …  (front-coded against the previous name; i39b-1)
│ Global flags  [count u16][name_id u16]…      │  editor_region.go
│ Comment sidecar (§2.5)                       │  editor_region.go
│   [count u16] then [anchor_delta uvarint][placement u8][len u16][text]…
└─────────────────────────────────────────────┘
```

The file splits at `editor_region_offset` into an **assembler-facing region**
(everything the SAM Z80 assembler reads) and a trailing **editor region**
(data only the renderer / editor needs). The editor-region split is M8 / i39b-2;
see `docs/specs/2026-06-08-compact-tbn-nextgen-design.md` §3.5/§3.6/§3.7.

- **Magic / version / flags / editor_region_offset** (12 bytes total).
  `ReadFile` rejects a bad magic or a version ≠ 2; `Flags` is reserved and
  must be 0. The version check is a clean break — a v1 file is rejected, not
  down-converted.
- **Label table** and **local table** — the v2 header **position tables**
  (§2.4). They head the assembler-facing region. They carry one row per
  resolved position-label / numeric-local def site, so a `KindInsnRun` run
  spans the labels embedded in it without interruption. The rows reference a
  `name_id` but never the name *string*, so the name table can follow the
  records (it does — in the editor region).
- **Record stream** — a flat sequence of length-framed records (§3). It ends
  at `editor_region_offset` (NOT at EOF): the editor region follows. The Z80
  reader sets its walk terminator to that boundary (`src/reader.asm`
  `reader_init`) and never maps the editor region.
- **Name table** — the interned label/symbol names, in first-encounter order,
  front-coded (i39b-1). Each name's **zero-based index is its symbol ID**,
  referenced by the **label table** rows (§2.4) and `PUSH_SYM` expression
  opcodes. The interner (`symbols.go:16`) assigns IDs sequentially from 0, so
  re-interning the same names in order reproduces identical IDs — which is why
  the compaction pass can rebuild the table from `File.Names`. The assembler
  **never decodes a name string** (it resolves by `name_id` via the position
  tables); the name table is editor/renderer-only, which is why it lives in
  the editor region.
- **Global flags / comment sidecar** — the `.global` provenance and the
  relocated comments (§2.5), both editor-only.

### 2.3 Section index — `editor_region_offset`

A `u32 LE` at file offset 8 giving the byte offset where the **editor region**
begins (= the byte just past the record stream). It is the one structural
addition of the editor-region split (M8 / i39b-2): it bounds the assembler's
record walk (the records no longer run to EOF) and marks the start of the
trailing, editor-only data a future build can leave on disk / reuse the RAM of
(item **i40** — assembler-resident eviction; see the nextgen design §7
decision 1). The split is **groundwork**: i39b-2 relocates the editor-only data
so eviction *becomes possible*; it does not itself evict (the loader still
loads the whole file).

### 2.5 Comment sidecar (editor region) — v2 / i39b-2

Comments are pure round-trip data the assembler never reads (it skips
`KindComment` in both passes). In v2 they do **not** appear as `KindComment`
records in the assembler stream — they relocate to a sidecar in the editor
region, each anchored to the **output PC it attaches to**:

```
Comment sidecar   editor_region.go
  count   u16 LE
  row     [anchor_delta uvarint][placement u8][len u16][text]   ×count
```

- **`anchor`** is the output PC (byte offset from the origin VMA, ≥ 0) the
  comment attaches to — the same offset space as the label table. Rows are
  sorted by anchor ascending and the anchor is stored as a **delta** from the
  previous row (first row's previous = 0). The renderer, walking the records
  and tracking PC, emits each comment when its PC walk reaches the anchor.
- **`placement`** is `0` = standalone (own line) / `1` = trailing (appends to
  the preceding statement's line) — as for the `KindComment` record (§8). A
  comment whose body carries embedded newlines (a `/* … */` block comment)
  renders as one `//`-prefixed line per body line.
- The host's `-strip-comments` drops comments entirely (empty sidecar);
  `-thin-comments=N` keeps one in every N (a bounded subset that flows a
  populated editor region through the SAM round-trip while the `.tbn` stays
  under the 96 KB IN ceiling — the m6 gate uses this).

### 2.4 Header position tables (label / local) — v2

The v2 compact overlay holds **position-labels and numeric-local def sites
in two header tables** that map a name (or a digit) to a **byte offset from
the origin VMA** (`header_tables.go`), rather than as records in the stream.
The overlay carries no `LABEL_DEF` / `LOCAL_DEF` records — a `KindInsnRun`
run spans the labels embedded in it without interruption
(`compact.go:headerRows`), and the Z80 resolves a label's PC as
`OriginVMA + offset` straight from the table (`pass1.go:309,313`).

Offset = `symbolVMA − OriginVMA`, always ≥ 0 (`compact.go:headerRows`,
`header_tables.go:14`). Both tables share a layout:

```
Label table   header_tables.go:35 (write) / :90 (read)
  count   u16 LE
  row     [name_id u16 LE][offset_delta uvarint]   ×count

Local table   header_tables.go:64 (write) / :117 (read)
  count   u16 LE
  row     [digit u8][offset_delta uvarint]          ×count
```

- **`name_id`** (label table) indexes the name table (§2); **`digit`**
  (local table) is the numeric-local digit (1–99). The same digit may
  repeat across rows (multiple def sites) — pass 2 keeps the full ordered
  list per digit for `Nf`/`Nb` resolution.
- **Rows are sorted by offset ascending** (ties by `name_id` / `digit`
  ascending), and the offset is stored as a **delta from the previous row's
  offset** — the first row's previous is 0, so its delta is its absolute
  offset (`header_tables.go:48,77`). The reader accumulates deltas back into
  absolute offsets (`header_tables.go:109,136`). `WriteFile` sorts copies of
  the caller's rows, so the increasing-offset delta invariant always holds
  regardless of input order (`writer.go:147`).
- **`offset_delta` is an unsigned LEB128 varint** (`binary.PutUvarint` /
  `binary.Uvarint`): 7 data bits per byte, low byte first, high bit = "more
  bytes follow". Each delta is ≥ 0 (offsets are sorted ascending), so the
  unsigned encoding is sufficient.
- A truncated table (count past EOF, or a row's bytes missing) is a hard
  error (`header_tables.go:92`…`:133`).

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

The on-disk overlay record stream uses three record kinds:

| Hex    | Name        | Payload | Defined |
|--------|-------------|---------|---------|
| `0x04` | `DIRECTIVE` | `[directive_id u8][operand_count u8][operands…]` (§4, §6) | kinds.go:10 |
| `0x08` | `LIT_DATA`  | `[directive_id u8][raw LE bytes…]` — a run of constant numeric data stored as assembled bytes, tagged with its source directive (§7.3) | kinds.go:18 |
| `0x09` | `INSN_RUN`  | `[mode u8][elements…]` — a run of instructions; mode 0 = packed literal words, mode 1 = base-word + sparse overlay patches (§7.2) | kinds.go:34 |

Reserved / not used by the overlay record stream: `0x00`; `0x06`; `0x0A`–`0xFF`.
`COMMENT` (`0x05`) is still a `RecordReader`-decodable kind and the host's
in-memory IR vocabulary, but in v2 / i39b-2 comments do **not** appear in the
on-disk record stream — they relocate to the editor-region comment sidecar
(§2.5). The record-kind constants `0x01`–`0x03` and `0x07` exist in `kinds.go`
for the host front-end's in-memory IR but are **never serialized** (§1), so an
on-disk overlay record stream contains only the three kinds above.

Notes:

- `.equ FOO, expr` is a `DIRECTIVE` whose operand list is
  `[symbol-ref operand, value-expression operand]`; there is no separate
  EQU kind (`tools/sam-aarch64/assemble/pass1.go:resolveEquDirective`).
- `directive_id` indexes `DirectiveTable` (§6.1).
- Symbol-bearing data (e.g. `.quad some_label`, which needs relocation)
  stays a `DIRECTIVE` carrying an `IMM_EXPR` operand rather than collapsing
  into a `LIT_DATA` run (§7.3); the operand encoding is §4 and the
  expression bytecode §5.

---

## 4. Operand encoding

Inside a `DIRECTIVE` payload (and inside an `INSN_RUN` mode-1 patch, with the
length framing noted in §7.2), `operand_count` operands follow, each
self-describing and beginning with a 1-byte kind (`operands.go:11`, reader
`operands.go:269`). "Self-describing" is deliberate: the inverse direction
(disassembly / `sam-aarch64 --render`) must render an operand without
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
  (`tools/sam-aarch64/assemble/pass1.go`, `pass2.go:encodeLdrLitPoolInst`).
  In the overlay an `ldr =expr` is an `INSN_RUN` element whose patch carries
  the `FoldLitpool19` slot (§7.2.1), so the `LIT_POOL` *operand* shape
  appears in the overlay only inside the symbolic data a `DIRECTIVE` may
  retain — its in-stream encoding is documented here for completeness and
  used by the renderer.

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
There is no `END` opcode — the length prefix bounds the stream. The same
bytecode is what an `INSN_RUN` mode-1 patch carries (§7.2).

Why a bytecode rather than text or an AST: the host cannot resolve
forward-referenced labels at tokenise time (their PCs aren't known until the
Z80's pass 2), so the expression is stored structured; the Z80 evaluates it
at pass-2 time without a text parser.

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

- The host **constant-folds** aggressively: any sub-expression whose
  leaves are all literals collapses to a single shortest-fit `PUSH_IMMn`
  (`expr.go:WriteImm` chooses the width). So `lsl #(8*4)` becomes one
  `PUSH_IMM8 32`.
- `EvalConst` (`expr.go:226`) evaluates a stream **iff** it contains no
  `PUSH_SYM`/`PUSH_LOCAL`/`PUSH_PC`/`REL_*` opcode — i.e. it is
  context-independent. This is the predicate behind "fully literal" (§7.1).
- `>>` is arithmetic. Reserved opcodes: any not listed; a reader
  hard-errors on an unknown opcode (`expr.go:178`).

**Worked example** — `b target + 4` (with `target` at symbol id 7) is an
`INSN_RUN` mode-1 element: the base word is the `b` opcode with imm26 zeroed,
and one patch holds the `FoldBranch26` slot plus the bytecode for
`target + 4`:

```
INSN_RUN  kind=0x09  mode=1
  element:
    base_word    = <b opcode, imm26 = 0>     (u32 LE)
    patch_count  = 1                          (u8)
    patch 0:  slot     = FoldBranch26 (1)     (u8)
              expr_len = 6                     (u8)
              PUSH_SYM 7   (0x05 07 00)
              PUSH_IMM8 4  (0x01 04)
              ADD          (0x10)
```

At pass 2 the patch expression evaluates to `target + 4`; the `FoldBranch26`
fold computes `(value − pc)/4`, range-checks it, and ORs imm26 into the zeroed
field (`tools/aarch64enc/overlay.go:Fold`, mirroring
`tools/sam-aarch64/assemble/pass2.go:operandsToValues`).

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
`.short`/`.hword`=2, `.word`=4, `.quad`=8
(`tools/sam-aarch64/assemble/pass1.go:directiveSizeAtPC`).
**`.short` and `.hword` are byte-identical at encode time but keep
distinct IDs** so the source spelling round-trips verbatim
(`directives.go:27`). `.text`/`.data`/`.global`/`.section`/`.arch`/`.cpu`
are syntactic no-ops at encode time (single-section flat layout).

### 6.2 Mnemonic table (`mnemonics.go:21`)

`mnemonic_id` indexes `MnemonicTable`; the slice index is the ID. The host
front-end maps name → id while building its in-memory IR; the assembler and
the Z80 map id → encoder form. `mnemonic_id` does **not** appear in the
on-disk overlay (instructions are stored as assembled base words in
`INSN_RUN`, §7.2) — it is an in-memory-IR field. The table is still part of
the format package because the host's IR and the disassembler share it.

**Stability policy** (`mnemonics.go:7`): the table is currently *mutable*
(removals + renumberings allowed in lockstep across all consumers)
**only because no `.tbn` files are persisted outside `build/`/`/tmp/`**.
The policy's own trigger to **freeze it strictly append-only** is "once
`.tbn` files start being shipped or persisted." ⚠️ The compact overlay
`.tbn` is the artefact that would pull this trigger — if a compact `.tbn`
ever gets committed/shipped, the directive table (and, while it is shared
with the IR, the mnemonic table) must become append-only and any removal
needs a `Version` bump. Until artefacts are actually persisted, the interim
mutable policy still holds.

---

## 7. The compact overlay records

The overlay's organising idea is that **literal and symbol/PC-bearing
instructions live in one record kind**: a literal is a bare word, a symbolic
one is a base word (relocated field zeroed) plus a sparse overlay patch. The
compaction pass (`tools/sam-aarch64/assemble/compact.go`) turns the
front-end's in-memory record stream into the overlay: every instruction
becomes an element of an `INSN_RUN` (0x09) run, every constant-data run
becomes a `LIT_DATA` (0x08) record, and position-labels move into the header
tables (§2.4). The overlay assembles to a byte-identical binary to the
literal-encoder path — verified by the m6-release 3-way gate and the
disasm-roundtrip overlay legs.

### 7.1 "Fully literal" predicate

`format.IsFullyLiteral(rec)` (`litinsts.go`) is true for an instruction whose
operands carry **no** symbol, local-label, PC, relocation, or literal-pool
reference — i.e. every embedded expression passes `EvalConst` and no
operand is `LIT_POOL`. Such an instruction's encoding depends on neither
the symbol table nor (structurally) the PC. The compaction pass adds a
**PC-invariance guard** on top: it encodes the instruction at two
different PCs and only collapses it to a bare literal word if they match —
so a constant-target PC-relative form (e.g. `b 0x1000`, structurally literal
but PC-dependent) becomes an overlay patch element instead
(`compact.go:literalWord`).

### 7.2 `INSN_RUN` (0x09) — instruction runs

Every instruction in the overlay is an **element** of an `INSN_RUN` record.
The record is a run of consecutive instructions sharing a `mode`:

```
[kind 0x09][len u16][mode u8][elements…]                    writer.go:82 / reader.go:136
```

**mode 0 — packed literal words.** Each element is a bare 4-byte assembled
word, little-endian; no per-element framing:

```
mode 0:  [00][word₀ u32 LE][word₁ u32 LE]…       payload len = 1 + 4*count
```

The reader requires the post-mode body length to be a multiple of 4
(`reader.go:144`). The Z80 memcpys the words straight to OUT — zero encoding
work, shared with `LIT_DATA` (`main_loop.asm:main_handle_lit_insts`).

**mode 1 — base word + sparse overlay patches.** Each element is an
assembled base word with its relocated bitfield(s) **zeroed**, followed by a
patch count and that many patches. Pass 2 evaluates each patch's expression
to a value, the **slot** byte selects a fold-rule that turns the value into
the field's bits, and those bits are **ORed into the zeroed field**:

```
mode 1:  [01]  then per element:
         [base_word u32 LE]
         [patch_count u8]
         patch_count × ( [slot u8][expr_len u8][expr bytes] )   writer.go:93 / reader.go:152
```

- A **patch-free element** in mode 1 (`patch_count = 0`) is a fully-literal
  instruction absorbed into an overlay frame to avoid splitting a run; it is
  identical in meaning to a mode-0 word. The packer (`compact.go:emitInsnRunFrames`)
  emits maximal patch-free stretches as mode-0 frames and only absorbs short
  literal gaps (< `litBreak` = 4) into a surrounding mode-1 frame — a
  size choice that never changes the assembled bytes.
- **`expr_len` and `patch_count` are single bytes** (the writer panics if a
  patch expression exceeds 255 bytes or an element exceeds 255 patches —
  compaction bugs, `writer.go:95,101`). The patch expression is the same
  length-prefixed expression bytecode as §5, but with a **u8** length here
  (the §4 operand `IMM_EXPR` uses a u16 length — these are distinct framings).
- **PC accounting is exact**: each element occupies 4 output bytes, so label
  offsets and the 2-pass values are unchanged.
- A run's payload is capped at **1016 bytes** (`insnRunMaxPayload`,
  `compact.go:182`; the same Z80 `STAGING_BUF` headroom as `LIT_DATA`, §7.3),
  splitting on whole-element boundaries.

#### 7.2.1 Fold slots (`aarch64enc.FoldSlot`, `overlay.go:16`)

The `slot` byte selects which bitfield the patch writes and the conversion
applied (`Fold`, `overlay.go:45`). Each rule mirrors *exactly* the conversion
the literal encoder performs for that field (cited in `overlay.go` against
the `tools/sam-aarch64/assemble/pass2.go` slot encoders), so the overlay and
literal paths cannot diverge. The byte values are an **append-only wire
contract** shared with the Z80 slot dispatch.

| ID | Slot | Field | Fold (`value` = evaluated patch expr; `pc` = element PC) |
|----|------|-------|----------------------------------------------------------|
| 1  | `FoldBranch26`    | imm26 @0  | `b`/`bl`: `(target − pc)/4` |
| 2  | `FoldBranch19`    | imm19 @5  | `b.cc`/`cbz`/`cbnz`/`ldr`-literal: `(target − pc)/4` |
| 3  | `FoldBranch14`    | imm14 @5  | `tbz`/`tbnz`: `(target − pc)/4` |
| 4  | `FoldAdr`         | immlo@29:immhi@5 | `adr`: `target − pc` (raw byte offset) |
| 5  | `FoldAdrp`        | immlo@29:immhi@5 | `adrp`: `(page(target) − page(pc)) / 4096` |
| 6  | `FoldAddSubImm12` | (sh,imm12) @10 | `add`/`sub`/`cmp` imm: value used directly (`:lo12:`, symbol-diff) |
| 7  | `FoldMemImm12`    | imm12 @10 | `ldr`/`str` scaled: `byteOff / scale` (scale from base-word size field) |
| 8  | `FoldMemImm9`     | imm9 @12  | `stur`/`ldur`/pre/post: signed byte offset |
| 9  | `FoldMovkImm16`   | imm16 @5  | explicit `movz`/`movk`: `value & 0xFFFF` (hw stays in the base word) |
| 10 | `FoldLogical`     | N:immr:imms @10 | `orr`/`and`/`eor`/`bic` imm: bitmask immediate (is64 from sf) |
| 11 | `FoldPairImm7`    | imm7 @15  | `ldp`/`stp`: signed `byteOff / scale` (scale 8/4 from sf) |
| 12 | `FoldLitpool19`   | imm19 @5  | `ldr =expr`: `(poolPC − pc)/4` — `value` is the pool-entry PC, not an eval result |
| 13 | `FoldMovzAuto`    | imm16 @5 + hw @21 | `mov Rd,#value` → `movz` (i48b): the fold computes both the 16-bit chunk and the `hw` shift |

(`FoldSlotForKind`, `overlay.go:146`, maps a form-table `SlotKind` to its
overlay slot for the straightforward families; the hand-rolled families —
mem, litpool, tbz, ldr-literal, mov-imm — are classified by their encoder
path. `ZeroSlot`, `overlay.go:171`, clears exactly the bits a slot's `Fold`
writes; `TestZeroSlotClearsFoldBits` locks the two together. For
`FoldMovzAuto` (i48b) `ZeroSlot` clears both imm16 @5 and hw @22:21 because
the fold computes `hw` from the resolved value rather than the base word.)

### 7.3 `LIT_DATA` (0x08) — constant data runs

The remaining bulk of a compact `.tbn` is numeric data directives
(`.byte`/`.short`/`.hword`/`.word`/`.quad`) whose operands are constants. A
run of consecutive **same-directive, all-constant** data collapses into one
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
`.quad some_label`) stays a `DIRECTIVE` for relocation. The
collapsibility predicate is `format.ConstDataWidth` (`litinsts.go`); the
bytes come from the one encoder (the assemble pass's `encodeDirective`), so a
`LIT_DATA` record is byte-identical to what the per-element directive path
would emit.

A run is split into records of at most **1016 data bytes** (`litDataMaxBytes`,
`compact.go:27`, on whole-element boundaries) so each payload stays under the
Z80 reader's 1024-byte `STAGING_BUF` (`src/reader.asm`). On the SAM the decode
is shared with `INSN_RUN` **mode 0** — both are "skip the 1-byte tag, memcpy
the remaining bytes to OUT" (`src/main_loop.asm:main_handle_lit_insts`); the
`directive_id` matters only to the disassembler.

**Measured result** (i1 baseline; vendored spectrum4 release; byte-identical
to GNU `release.img`): 1,745 collapsible numeric records that occupied
**21,892 B** (31.8%) of the 68,755 B pre-`LIT_DATA` file — assembling to just
4,046 B of output — collapsed to **51,117 B**. `.hword` tables dominate the
collapsible set (17.5 KB) and are contiguous. (These figures predate the v2
instruction overlay — they measure the i1 `LIT_DATA` floor; the v2 overlay
extends the same idea to symbolic instructions and brings the release compact
`.tbn` to **45,189 B**.)

### 7.4 Level 3 dictionary (future)

A per-project single-byte dictionary for the hottest fully-literal words
(`ret`, `nop`, frame-setup pairs, …). Deferred; revisit only if the overlay
data runs don't clear the memory ceiling. Design in the compaction spec.

### Not addressed by compaction

The **name table** (~6.6 KB on release, symbol-name strings) and the
symbol/PC-bearing instructions (branches / `adrp` / litpool loads —
inherently PC/symbol-dependent, stored as mode-1 patch elements) are not
collapsible to bare words. Symbol-name pooling/shortening is a separate
future lever (M1 spec §9 "Level 1").

---

## 8. Resolution semantics (for implementers)

**Local labels**: defined at the current PC; multiple defs of the same digit
are legal. In the overlay the def sites come from the **header local table**
(§2.4); they feed pass-1's per-digit, in-order list of PCs (`pass1.go:313`).
At pass 2 `PUSH_LOCAL d, dir` resolves: `dir=0` (forward `Nf`) → the nearest
def after the reference; `dir=1` (backward `Nb`) → the nearest def
at-or-before. Digits 1–99 are supported (the table is a sorted `(digit, pc)`
list — see the multi-digit-local-labels work).

**Position labels** behave analogously: a **header label table** row (§2.4)
resolves the named symbol to `OriginVMA + offset` (`pass1.go:309`).

**Literal pool**: `ldr Xn/Wn, =expr` allocates a pool slot deduplicated by
`(width, expr-bytes)`; `.ltorg` (or end-of-input) flushes the accumulated
slots, 4-byte entries before 8-byte, each aligned
(`tools/sam-aarch64/assemble/pass1.go:flushPool`). In the overlay the `ldr`
is an `INSN_RUN` element whose patch carries the `FoldLitpool19` slot; the
fold's `value` is the pool slot's PC (`pass2.go:encodeLdrLitPoolInst`), and
it encodes as a PC-relative load of that slot.

**Comments**: in v2 / i39b-2 they live in the editor-region comment sidecar
(§2.5), each anchored to the output PC it attaches to, with `placement`
`0`=standalone (own line[s]) / `1`=trailing (same line as the preceding
statement). The renderer emits bodies verbatim, one `//`-prefixed line per
body line (so a `/* … */` block comment's multi-line body re-parses). The
assembler never reads them. The host's `-strip-comments` drops them entirely
(empty sidecar); `-thin-comments=N` keeps one in every N.

---

## 9. Cross-references

- **v2 overlay design** (the instruction-overlay redesign this doc
  describes): `docs/specs/2026-06-08-compact-tbn-nextgen-design.md` (i39
  format) and `docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md`
  (the single-format + syntactic-encoder decisions — Decision A makes the
  compact overlay the only serialized form and the symbolic records an
  in-memory IR).
- **History / rationale / tooling**: M1 spec
  `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md` (§7 tooling,
  §8 test pyramid, §9 open items still apply; its §2–§6 format tables are
  superseded by this doc).
- **i1 compaction baseline** (`LIT_INSTS`+`LIT_DATA`):
  `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`.
- **Disassembler (the bytes→text inverse)**:
  `docs/plans/2026-05-28-go-aarch64-disassembler.md`; `tools/aarch64dec/`.
- **Code authority**: `tools/sam-aarch64-format/` (`kinds.go`,
  `operands.go`, `expr.go`, `directives.go`, `mnemonics.go`, `symbols.go`,
  `reader.go`, `writer.go`, `header_tables.go`, `litinsts.go`, `format.go`);
  overlay fold-rules `tools/aarch64enc/overlay.go`; the integrated host
  assembler `tools/sam-aarch64/` (`frontend/`, `assemble/`, `render/`);
  compaction pass `tools/sam-aarch64/assemble/compact.go`; Z80 mirror
  `src/main_loop.asm`, `src/insn_run.asm`, `src/reader.asm`.
