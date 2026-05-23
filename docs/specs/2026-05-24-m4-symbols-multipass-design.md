# M4 spec: symbol table, multi-pass, full expression evaluator on Z80

**Status**: drafted 2026-05-24 autonomously by Claude. Builds on
M3 (`2026-05-24-m3-z80-emitter-design.md`).

## 1. Goal & boundaries

M3 emits aarch64 bytes for fully-constant-folded source. M4 adds
the three pieces M3 explicitly deferred:

- **Symbol table** on the Z80 — name → address resolution,
  including forward references.
- **Two-pass design** — pass 1 assigns PC and builds the symbol
  table; pass 2 emits bytes with resolved symbols.
- **Full expression evaluator** — port the rest of M1's
  expression bytecode opcodes: `PUSH_SYM`, `PUSH_LOCAL`, `PUSH_PC`,
  and all `REL_*` operators.

Together these unlock: branches to labels, PC-relative loads,
`:lo12:` / `:hi12:` operators, local-label refs (`1f`/`1b`), and
forward references generally.

**In scope for M4**:

- Z80 symbol table with hash-bucketed name lookup (per Phase 1
  spec §3 "symbol table").
- Two-pass record walker — pass 1 maps PC + populates symbol
  table; pass 2 emits bytes via the M3 encoder, now with all
  expression opcodes available.
- Local-label resolution: `1f` finds the nearest later
  `LOCAL_DEF 1`, `1b` finds the nearest earlier.
- PC-relative slot-kind encoders extended to handle resolved-
  symbol operands (M3 only handled constants):
  - `BranchImm26` / `BranchImm19` / `BranchImm14`: target_addr - PC
  - `AdrpImm`: (target_addr & ~0xFFF) - (PC & ~0xFFF)
- Expanded fixture corpus including every M1 fixture except those
  exercising encoder kinds that aren't yet table-driven.

**Out of scope for M4**:

- 64-bit MUL / DIV on Z80 (still rejected; should remain rare in
  hand-written aarch64).
- `.balign` / `.org` / `.skip` / `.section`. M5 or later.
- Macros / conditional assembly.
- On-SAM editor (Phase 2).
- Networking (Phase 3).

## 2. Architecture

### 2.1 Pass split

M3's single-pass walker becomes two passes, both over the same
`.tbn` record stream:

**Pass 1** — symbol-table build:

- Walk records in order. Track PC starting at 0 (or whatever the
  source's `.org` declares, when M5 adds `.org`).
- `KindLabelDef`: insert `(symbol_id → PC)` into the symbol table.
- `KindLocalDef`: append PC to the per-digit list.
- `KindInst`: PC += 4.
- `KindDirective`: PC += data size (use the same dispatch as M3).
- `KindComment`: skip.

**Pass 2** — emission:

- Walk records again. PC starts at 0 again, tracked in lockstep
  with pass 1.
- `KindLabelDef` / `KindLocalDef`: skip (already in symbol table).
- `KindInst`: same flow as M3 (form lookup → encode), but the
  expression evaluator now resolves `PUSH_SYM` / `PUSH_LOCAL` /
  `PUSH_PC` against the symbol table built in pass 1. PC-relative
  slot kinds compute the offset from current PC.
- `KindDirective`: emit bytes (.byte/.short/.word/.quad/.ascii/
  .asciz). PC advances by emitted size.

The same record reader serves both passes; pass-mode is a single
register flag.

### 2.2 Symbol table data structure

Hash-bucketed: 256 buckets indexed by the low byte of a simple
checksum over the name (or by symbol_id mod 256 — both work; the
spec recommends symbol_id mod 256 since IDs are dense from the
.tbn name table).

Each bucket entry: `(symbol_id u16, address u32, next_offset u16)`.
Linked-list chain within the bucket via `next_offset` (offset into
the bucket's overflow area). At ~200 symbols typical, average chain
length < 1. Lookup is O(1).

Memory layout: bucket table in a fixed paged region (page TBD —
default page 5 / `0x4000` after `out (LMPR), A`), overflow area
appended.

Total ≈ 2KB for 200 symbols. Comfortable.

### 2.3 Local-label table

Separate from the global symbol table. For each digit 1–9, a
sorted list of PCs (sorted by definition order, which is also PC
order since pass 1 walks records sequentially).

At pass 2, when the expression evaluator hits `PUSH_LOCAL d, dir`:

- `dir=0` (forward, `1f`): binary search for the smallest PC in
  digit d's list that is strictly greater than the current
  reference's PC. Error if none.
- `dir=1` (backward, `1b`): binary search for the largest PC ≤
  current reference's PC. Error if none.

Both M1's spec (§6) and aarch64enc's Eval implement the same
algorithm; M4 ports it to Z80.

### 2.4 Full expression evaluator on Z80

M3 implements `PUSH_IMM*`, arithmetic, unary, plus error-on-unknown.
M4 adds:

- `PUSH_SYM symbol_id` — look up in symbol table; push 64-bit
  address.
- `PUSH_LOCAL digit, dir` — resolve via local-label table.
- `PUSH_PC` — push current PC (pass 2 tracks PC in a global; pass
  1 doesn't evaluate, so this opcode only fires at pass 2).
- `REL_LO12` / `REL_HI12` / `REL_ABS_G0` / `REL_ABS_G0_NC` /
  `REL_ABS_G1` / `REL_ABS_G1_NC` / `REL_ABS_G2` / `REL_ABS_G2_NC`
  / `REL_ABS_G3` — apply mask/shift per `aarch64enc/expr.go`.

These all need 64-bit arithmetic. M3's `ml.asm` has add/sub/shift;
M4 may need more depending on which fixtures exercise what.

### 2.5 PC-relative slot encoders

The encoder dispatcher from M3 invokes per-slot encoders. For
PC-relative kinds, the *operand value* fed to the encoder is now
the resolved absolute address. The encoder subroutine subtracts
the current PC (or current-PC-page for `AdrpImm`) before applying
its bit layout.

That logic mirrors `tools/refenc/pass2.go`'s post-eval branch:

```
if slot.SlotKind in {BranchImm26, BranchImm19, BranchImm14}:
    value = value - current_pc
elif slot.SlotKind == AdrpImm:
    value = (value & ~0xFFF) - (current_pc & ~0xFFF)
```

The encoder then applies its existing bit-fiddly layout. Range and
alignment checks remain at the encoder level.

## 3. Test pyramid

Same four layers as M3 — Layer 3 (Z80 vs refenc parity) remains
the load-bearing oracle. Now exercises the larger M1 fixture
corpus, including:

- Forward branches: `b target` where `target:` is defined later.
- Backward branches: `b target` after the label.
- Local-label chains: `1: ... 1b ... 1f ... 1:` patterns.
- adrp+:lo12: pairs: `adrp x0, msg; add x0, x0, :lo12:msg`.
- `cbz`/`cbnz`/`tbz`/`tbnz` with forward-referenced labels.

## 4. M4 fixture corpus

Promote from M1 → M3 → M4:

| Fixture | Slot kinds exercised | M4 status |
|---|---|---|
| `inst_bcond.s` (full version with labels) | BranchImm19, CondCode | New |
| `inst_csel.s` | CondCode, SHIFTED_REG | Promote |
| `expr_pcrel.s` | AdrpImm, REL_LO12 | Promote |
| `local_labels.s` (full version) | BranchImm19 + LocalRef | Promote |
| ... others ... | | |

## 5. Implementation plan outline (full plan separate)

1. Refactor M3's reader so pass mode is a register-passed flag.
2. Symbol-table data structure + insert/lookup subroutines.
3. Local-label table + forward/backward lookup.
4. Pass 1: PC tracking + symbol/local insertion.
5. Pass 2 driver — re-walk records, dispatch like M3 but with
   resolved expressions.
6. Expression evaluator: `PUSH_SYM`, `PUSH_LOCAL`, `PUSH_PC` cases.
7. REL_* operators (apply mask/shift after stack pop).
8. PC-relative slot encoder updates (subtract PC, then apply
   existing bit layout).
9. Expand fixture corpus + golden outputs.
10. CI `m4` job + Makefile.
11. `docs/notes/m4-status.md` + declare done.

## 6. Open items, risks, non-goals

### Open items resolved during implementation

1. **Symbol-table hash function**: simple-checksum vs
   symbol_id-mod-256. Resolved per §2.2: symbol_id mod 256.
   Revisit if symbol distribution becomes degenerate.
2. **Pass-1 / pass-2 state separation**: where does PC live
   between passes? Resolved: a single 4-byte location, reset
   before pass 2.

### Risks

- **Forward-reference detection**: a symbol used but never
  defined must error. Pass 1 must conclude with every symbol in
  the .tbn's name table having either a `LabelDef` or a `.equ`
  entry that hit the symbol table. Pass 2 returns an error on
  any unresolved symbol lookup.
- **Local-label binary search edge cases**: empty list, single
  entry, ref-at-def-position. Layer 1 unit tests should cover
  these explicitly.
- **64-bit arithmetic on Z80** when symbol addresses are large:
  the M0 boot path uses addresses near `0x8000` (32-bit fits in
  16 bits effectively), but the aarch64 target may have full
  64-bit values, especially with `REL_ABS_G*` operators. M4's
  `ml.asm` may need expansion.

### Non-goals

- 64-bit MUL/DIV (still rejected if encountered).
- `.balign` / `.org` / `.skip`.
- `.section` beyond a single concatenated layout (per M3).
- Macros, conditional assembly.

## 7. Done criteria

1. The M4 fixture corpus byte-matches `aarch64-none-elf-as`
   end-to-end via Z80 on SimCoupé.
2. Layer 3 refenc parity holds for every M4 fixture.
3. Forward + backward branches, local labels, and adrp+:lo12:
   all work.
4. `make ci-m4` green.
5. `docs/notes/m4-status.md` written.

---

## Beyond M4: M5 + M6 sketch (informal)

The Phase 1 vision spec's "done" criteria require:

- ~76 mnemonics covered.
- A real `~/git/spectrum4` fixture round-trips byte-identical.
- Memory layout has measured headroom.

That suggests:

- **M5**: scale up. Vendor the rest of the MRA subset (≈30 more
  mnemonic XMLs), implement remaining slot-kind variants, fill in
  missing operand encoders (`.balign`, `.org`, full memory shapes,
  full shifted/extended forms). Mostly grunt work; little new
  architecture.
- **M6**: pick a non-trivial fixture from `~/git/spectrum4`'s
  real disassembly, get it round-tripping byte-identical, declare
  Phase 1 done. May surface long-tail issues (rarely-encountered
  encoder edge cases, format-spec gaps).

Both are sequential, achievable, and don't require new spec design
beyond what M1–M4 establish. They get their own spec docs when
M4 ships.
