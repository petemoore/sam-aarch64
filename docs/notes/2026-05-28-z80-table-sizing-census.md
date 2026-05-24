# Z80 fixed-table sizing census against the fresh `release.tbn`

**Date:** 2026-05-28
**Worktree:** `.claude/worktrees/agent-a87a046e5fd49fb0e`
**Input:** `build/release-fresh.tbn` (407,730 bytes), built by
`text2bin -flatten` from spectrum4's `release.target`.
**Tool:** `tools/refenc/ --dump-usage` (this commit adds the
instrumentation; the existing refenc tests + GNU byte-match still pass).

## Why

The SAM-side assembler currently hangs on an 86 KB stripped
`release.tbn` — likely a silent fixed-table overflow. This census
records, empirically, what each internal data structure peaks at when
the same input is run through Go-side refenc (which produces a
byte-identical output to the Z80 assembler and the GNU oracle on the
M3..M5 fixture corpora). The peaks here are the minimum capacities the
Z80 tables must support — current Z80 caps that fall short are the
prime suspects for the hang.

## Headline finding

Three structures are currently undersized for `release.tbn`. The single
biggest gap is the **literal-pool PC-map**: the Z80 side allocates
**32** entries but `release.tbn` needs **44** active simultaneously.
The symbol table is also short by ~90 entries (peak 474 vs cap 384).

## Method

Built `refenc --dump-usage` and ran it on the fresh
`release-fresh.tbn`. The Go-side observers fire from the same code
paths the Z80 would (label-def insert, local-def append, pool-slot
allocate, pool-flush, ldr-litpool site, expr eval), so the peaks are
the empirical demand the input places on each table. Z80 limits are
quoted from the source.

## Table-by-table summary

| Structure | Peak observed | Z80 limit | Z80 source | Status |
|---|---|---|---|---|
| Symbol table — total entries | **474** | 256 primary + 128 overflow = 384 | `src/m3/symbols.asm:64-66` | **UNDERSIZED** (~90 short) |
| Symbol name length (max) | 39 B | n/a (Z80 doesn't store names; hashes on `symbol_id mod 256`) | `src/m3/symbols.asm:8` | n/a |
| Local labels — total | 172 | LOCAL_LIST_MAX = 180 | `src/m3/local_labels.asm:67` | tight (8 slack) |
| Local labels — distinct digits seen | 15 | LOCAL_MAX_DIGIT = 99 | `src/m3/local_labels.asm:71` | OK |
| Local labels — peak per digit | 60 (digit `1`) | shared list (no per-digit cap) | — | OK |
| Litpool — active pool slots / flush | 30 | LITPOOL_MAX = 32 | `src/m3/litpool.asm:55` | tight (2 slack) |
| Litpool — active PC-map / flush | **44** | LITPOOL_MAX = 32 (PC_MAP shares the cap) | `src/m3/litpool.asm:55,60` | **UNDERSIZED** (12 short) |
| Litpool — explicit `.ltorg` flushes | 1 | unbounded | — | OK |
| Litpool — total expr bytes | 210 B | LITPOOL_EXPR_BUF = 2048 B | `src/m3/assembler.asm:62-63` | OK (10% used) |
| Expression evaluator — peak stack depth | 3 | EXPR_STACK_DEPTH = 8 | `src/m3/expr_eval.asm:76` | OK (38% used) |
| Expression evaluator — longest single bytecode | 24 B | (streamed; no buffer cap) | — | n/a |
| OPVAL — peak operands per INST record | 4 | 7 | `src/m3/assembler.asm:65` | OK (57% used) |
| OPVAL — peak operand bytes per INST record | 29 B | 70 B (7×10) | `src/m3/assembler.asm:65` | OK |
| OPVAL — peak operands per DIRECTIVE record | 16 | n/a (directives stream one operand at a time; `main_handle_directive` in `main_loop.asm`) | — | n/a |
| STAGING_BUF — peak operand-payload bytes | 94 B | 1024 B | `src/m3/assembler.asm:60-61` | OK (9% used) |
| Record-stream total | 13,072 (3318 Inst + 282 LabelDef + 172 LocalDef + 2288 Directive + 7012 Comment) | streamed; no buffer | — | OK |
| OUT bytes emitted | 21,752 | OUT buffer = 32 KB (post-PR #25, page 5 only) | `src/m3/assembler.asm:31-32` | OK |

(The 86 KB hung input would only stress structures that scale with
*input* — primarily symbol/label/litpool counts, not OUT bytes or
operand width. The candidates above are the right places to look.)

## Detailed findings

### 1. Symbol table — UNDERSIZED, ~23% short

- **Peak symbol count: 474**
  - 282 from LabelDef records
  - 193 from `.equ`/`.set`/`.global` directives
  - (Total observed via observeSymbolAdd: 475 unique inserts; one
    duplicate landed pre-existing.)
- **Name-length distribution:** max=39, mean≈11.9, median=10.
- **Z80 layout** (`src/m3/symbols.asm:64-66`):
  - SYMTAB: 256 buckets × 8 bytes = 2 KB at `&C160..&C95F`.
  - SYMTAB_OVERFLOW: 128 entries × 8 bytes = 1 KB at `&C960..&CD5F`.
  - Max symbols storable = 256 + 128 = 384.
- **Gap:** 474 − 384 = **90 entries (~23% overflow)**.
- **Recommendation:** raise `SYMTAB_OVERFLOW_MAX` from 128 to at least
  **256** entries (1 KB → 2 KB region) — gives peak/cap = 474/512 =
  92.6% with ~10% margin. The overflow region currently abuts
  LOCAL_LABEL_TABLE at `&CD60`, so growing it in place requires
  shifting LOCAL_LABEL_TABLE up by 1 KB (to `&D160`). That collides
  with OPMEM_OFF at `&D100` — so the whole section-D layout has to
  shuffle. Alternative: move SYMTAB_OVERFLOW into the &E100+ free
  region (7.7 KB unused after LITPOOL_EXPR_BUF) and leave the
  in-place buckets alone. That's the lowest-risk move: only changes
  `SYMTAB_OVERFLOW` and `SYMTAB_OVERFLOW_MAX`; doesn't touch the
  hash function (`symbol_id mod 256`) or the bucket walk.

### 2. Local labels — tight (172/180, 4% slack)

- **Peak total entries: 172.**
- **Distinct digits: 15.**
- **Per-digit peak:** digit 1 = 60, digit 2 = 40, digit 3 = 23, digit
  4 = 12, digit 5 = 8, digit 6 = 6, digits 7..15 = 1..6 each.
- **Z80 cap:** LOCAL_LIST_MAX = 180 (post-PR #35), at
  `src/m3/local_labels.asm:67`.
- **Recommendation:** bump LOCAL_LIST_MAX to **256** entries
  (5 B each = 1282 bytes total). PR #35 already addressed this once;
  release.tbn's 172 is uncomfortably close. The table currently runs
  `&CD60..&D0E5`; raising to 256 entries extends it to `&D165`,
  which overlaps OPMEM_OFF at `&D100`. Again — section-D shuffle, or
  move local-label table into the free `&E100+` region.

### 3. Literal pool PC-map — UNDERSIZED, 37% short

- **Peak active PC-map entries between flushes: 44.**
- **Peak active pool slots between flushes: 30.**
  - (44 ldr sites referencing 30 distinct slots — dedupe saves 14.)
- **`.ltorg` flushes in input: 1.**
- **Z80 cap:** LITPOOL_MAX = 32 (`src/m3/litpool.asm:55`); this is
  the cap for BOTH LITPOOL_TABLE *and* LITPOOL_PC_MAP, since the
  Z80-side `litpool_register_ldr` (called per ldr site) range-checks
  against LITPOOL_MAX before writing into LITPOOL_PC_MAP.
- **Gap:** 44 − 32 = **12 PC-map entries (37%)**, 30/32 for slots.
- **Recommendation:** decouple the two caps and raise LITPOOL_PC_MAP
  cap to **64** while keeping LITPOOL_TABLE at 32 (or also raising
  to 48 for some margin). Memory delta is small: PC_MAP grows from
  32×6=192 B to 64×6=384 B (+192 B), TABLE grows from 32×14=448 B
  to 48×14=672 B (+224 B). Total extra is <0.5 KB — easily fits in
  the existing `&D486..&D4FF` gap (123 bytes) plus a small bump
  into the OPMEM_OFF region (or moved to `&E100+`).

### 4. Litpool expression buffer — comfortable headroom

- **Total litpool-expr bytes: 210 B.**
- **Peak single bytecode: 13 B.**
- **Z80 cap:** LITPOOL_EXPR_BUF = 2048 B (post-PR #37,
  `src/m3/assembler.asm:62-63`).
- **Headroom: 10× used → 90% spare.** No change needed.

### 5. Expression evaluator stack — comfortable headroom

- **Peak stack depth: 3** (lots of compound expressions but they
  collapse fast via binary ops).
- **Peak single-bytecode length: 24 B.**
- **Z80 cap:** EXPR_STACK_DEPTH = 8 (`src/m3/expr_eval.asm:76`).
- **Headroom:** 5 slack slots / 38% used. No change needed.

### 6. OPVAL — comfortable headroom for instruction records

- **Peak operands per INST record: 4** (a few 4-operand bitfield /
  csel-class instructions). Z80 OPVAL_ARRAY holds 7 — 43% slack.
- **Peak operand-bytes per INST record: 29 B** (one operand can be
  up to ~10 B with embedded expression bytecode).
- **DIRECTIVE records peak at 16 operands / 94 B** — but the Z80
  doesn't store directive operands in OPVAL_ARRAY; it streams them
  through `main_handle_directive` one operand at a time. So the 16
  is informational only.
- **STAGING_BUF:** 1024 B Z80 budget vs 94 B peak record payload =
  comfortable; no change needed.

### 7. OUT bytes emitted

- **21,752 bytes** — matches `release.gnu.img` size (verified via
  `scripts/build-spectrum4-release.sh`: BYTE-MATCH OK). Z80 OUT
  buffer is now 32 KB (page 5 only, post-M6 PR 1), 64 KB if page 6
  is allocated — both comfortable.

## Recommendations (priority order)

1. **`SYMTAB_OVERFLOW_MAX`:** 128 → 256 (or more — input demands ≥ 218
   to absorb the 474−256 = 218 collisions in the worst case; 256
   gives ~17% margin). Cost: +1 KB. Suggested placement: move
   SYMTAB_OVERFLOW out of section D into the `&E100+` free region;
   this avoids cascading shifts.

2. **`LITPOOL_PC_MAP`:** 32 → 64 entries. Cost: +192 B. Easily fits
   the existing `&D486..&D4FF` gap, plus pushing one of the small
   counter bytes if needed. May also be wise to bump LITPOOL_MAX
   itself to 48 (peak 30/32 is uncomfortably tight) — cost +224 B,
   still well within budget.

3. **`LOCAL_LIST_MAX`:** 180 → 256 (peak 172/180 leaves only 8
   slack). Cost: +380 B for the entry array. Suggested: move the
   table to the `&E100+` region to avoid shuffling the rest of
   section D.

No structure needs *paging out* (mirror of ENCTAB's COMET trampoline
pattern) — the free section-D region (`&E100..&FFFF`, 7.7 KB) plus
small in-place bumps absorb every recommended growth comfortably.

## Instrumentation reference

The census uses `tools/refenc --dump-usage`, added in this worktree.
Two implementation notes:

- A single package-level `usage *Usage` variable is enabled by
  `--dump-usage` and observed from Pass1/Pass2; when nil all observe
  calls short-circuit (the existing refenc tests and the
  byte-match pipeline run with the same code path, no observation
  overhead).
- A local `EvalUsage` evaluator wraps `aarch64enc.Eval` to track
  peak stack depth without changing the aarch64enc API.

Re-run via:

```
make text2bin refenc
# Re-generate release-fresh.tbn from spectrum4 source (one-time):
./build/text2bin -flatten -I ~/git/spectrum4/src/spectrum4 \
    -I ~/git/spectrum4/src/spectrum4/{kernel,roms,tests,demo,libextra} \
    -origin 0xfffffff000000000 \
    -o build/release-fresh.tbn \
    ~/git/spectrum4/src/spectrum4/targets/release.target

./build/refenc -o /tmp/x --dump-usage build/release-fresh.tbn
```
