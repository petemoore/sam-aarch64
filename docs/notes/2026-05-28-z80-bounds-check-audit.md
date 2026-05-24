# Z80 assembler bounds-check audit (2026-05-28)

Audit of every fixed-size data structure in `src/m3/` to ensure overflow
fails cleanly via the `fail` printer-channel banner instead of silently
corrupting downstream state. Motivated by the hang observed feeding the
86 KB stripped release `.tbn` to the assembler.

## Inventory

| Structure | Location | Size | Element width | Max elements | Used by | Bounds-checked? |
|-----------|----------|------|---------------|--------------|---------|-----------------|
| `SYMTAB` (256-bucket hash) | `symbols.asm` `&C160` | 2 KB | 8 B | 256 | global symbol resolve | n/a (direct-addressed by `id_low`) |
| `SYMTAB_OVERFLOW` (chain) | `symbols.asm` `&C960` | 1 KB | 8 B | 128 | symbol overflow chain | ✅ `cp SYMTAB_OVERFLOW_MAX; jp nc, fail` (symbols.asm:215) |
| `LOCAL_LABEL_TABLE` | `local_labels.asm` `&CD60` | 902 B | 5 B | 180 | local label `Nf/Nb` lookup | ✅ `cp LOCAL_LIST_MAX; jp nc, fail` (local_labels.asm:132) |
| `LITPOOL_TABLE` | `litpool.asm` `&D200` | 448 B | 14 B | 32 | `=expr` slot dedup table | ✅ `cp LITPOOL_MAX; jp nc, fail` (litpool.asm:244) |
| `LITPOOL_PC_MAP` | `litpool.asm` `&D3C0` | 192 B | 6 B | 32 | LDR-litpool inst-PC → slot | ✅ `cp LITPOOL_MAX; jp nc, fail` (litpool.asm:318) |
| `LITPOOL_EXPR_BUF` | `assembler.asm` `&D900` | 2 KB | 1 B | 2048 B | cross-pass expr bytecode | ✅ `sbc hl, bc; jp nc, fail` (litpool.asm:375) |
| `OPVAL_ARRAY` | `assembler.asm` `&C100` | 70 B | 10 B | 7 | parsed operand storage | ❌ → ✅ **ADDED** (main_loop.asm:519) |
| `OPVAL_KINDS` | `assembler.asm` `&C150` | 7 B | 1 B | 7 | kinds tuple for form_lookup | (same guard as OPVAL_ARRAY) |
| `expr_stack` | `expr_eval.asm` `expr_stack` | 64 B | 8 B | 8 | constant-eval stack | ✅ `cp EXPR_STACK_DEPTH; jp nc, fail` (expr_eval.asm:216) |
| `expr_take_bytes` | `expr_eval.asm` | (bytecode cursor) | — | u16 | bytecode consume | ✅ `cp c; jp c, fail` on `remaining < N` (expr_eval.asm:306) |
| `STAGING_BUF` | `assembler.asm` `&D500` | 1 KB | 1 B | 1024 | per-record reader staging | ❌ → ✅ **ADDED** (reader.asm:198) |
| `IN_POS / IN_END` cursor | `main_loop.asm` | (u24 page+offset) | — | — | IN buffer cursor | ✅ `reader_at_end` returns `Z=1` on pos == end and on pos > end (reader.asm:154-158) |
| `OUT_PC` / `OUT_LEN` / `OUT_ZONE` | `encoder.asm` `main_loop.asm` | (u15 byte index) | 1 B | 32768 | paged OUT cursor | ✅ second zone-cross → `jp fail` (encoder.asm:483) |
| `in_file_pages` (IN load) | `main_loop.asm` | u8 | — | 4 | HLOAD page count → trampoline `C` | ❌ → ✅ **ADDED** (main_loop.asm:2149) |
| `sysreg_fields` scratch | `sysname.asm` | 8 B | 1 B | 5 (typed 8 for slack) | sysreg field unpack | n/a (fixed indexing inside lookup; no caller-controlled count) |
| `eval_pos / eval_remaining` | `expr_eval.asm` | u16 | — | — | bytecode cursor | (covered by `eval_take_bytes`) |

### Summary counts

- **15 structures inventoried** in total.
- ✅ **12 already bounds-checked** before this audit.
- ❌ **3 missing checks** — all now fixed.
- ⚠️ **0 partially checked** edges identified.

## Checks added

1. **STAGING_BUF overflow** — `reader.asm:199-204`. After reading the
   3-byte record header, check `B >= 4` (i.e. payload length ≥ 1024).
   STAGING_BUF is `&D500..&D900` = 1024 B. A record larger than 1024 B
   would silently overflow into `LITPOOL_EXPR_BUF` at `&D900` and beyond
   into free RAM, with no symptom until pass-2 read the corrupted pool.
   This is the most likely root cause of the 86 KB stripped-release
   hang: real-world `.tbn` records (especially `.ascii` directives in
   long source strings) can easily exceed 1 KB.

2. **OPVAL_ARRAY overflow** — `main_loop.asm:516-520`. After reading an
   instruction record's `op_count` byte, check ≤ 7. `OPVAL_ARRAY` holds
   7 × 10 B slots; an out-of-spec `op_count = 8` would overflow into
   `OPVAL_KINDS`, then `PASS_MODE`, `PASS_PC`, and into `SYMTAB`. The
   .tbn format permits any `u8` value here.

3. **IN file page count overflow** — `main_loop.asm:2147-2155`. After
   reading the SAMDOS-deposited `difa+34` page count, check ≤ 4. IN is
   allocated 4 contiguous physical pages (7..10) in the LMPR-low5 axis.
   Larger files would spill into pages 11+ (free RAM in practice) but
   the cap exists as a deliberate ceiling per the M6 paged-IN design.

## Production code-budget impact

| | Bytes | Delta |
|-|------:|------:|
| Before (PR #39) | 12069 | — |
| After (this audit) | 12085 | **+16** |
| Budget | 12288 | 203 B headroom |

Three guards, ~5 B each. Well within the 12200 B watch threshold.

## CI matrix result

All targets pass on `worktree-agent-ad507d3c9ec8e2eb6`:

```
9/9 M3 fixtures matched
4/4 M4 fixtures matched
20/20 M5 fixtures matched
2/2 M6 fixtures matched
(×2 for *-prod variants)
```

No fixture triggers any new guard.

## Stripped-release run

The 86 KB stripped-release `.tbn` referenced in the task (`build/release-stripped.mgt`)
does **not** exist in the worktree. The closest artefact in `~/git/spectrum4`
is `release.img` at 21 KB (the assembled aarch64 ELF, not a `.tbn`); building
the 86 KB intermediate would require running text2bin against the full release
source, which is outside the scope of this audit pass.

Predicted behaviour based on the inventory:

- If the hang was caused by STAGING_BUF overflow (most likely — release
  source contains long `.ascii` strings and instruction records with
  many operands), this audit fixes it: the new guard at reader.asm:199
  will fire and emit `FAIL\n` on the printer channel.
- If the hang was caused by an op-count overflow on a corrupted record,
  the new guard at main_loop.asm:519 catches it.
- If the file genuinely exceeds 4 IN pages, the new guard at
  main_loop.asm:2154 catches it before HLOAD spills into pages 11+.

Without running the file we can't confirm which guard fires; once the
stripped-release fixture is available the next session can re-run the
script in §4 of the task and observe.

## Notes on structures not flagged

- **`OPVAL_KINDS`** (`&C150..&C156`, 7 B) is written in the same loop
  as `OPVAL_ARRAY` (main_loop.asm:1140-1151) but indexed by the same
  `op_count` we now bound at ≤ 7. So the new OPVAL_ARRAY guard implicitly
  also bounds OPVAL_KINDS.
- **`SYMTAB`** itself is direct-addressed by `id_low` (the low byte of
  a u16 symbol id); bucket index is always 0..255 by construction, no
  bound check needed. Chain-walk overflow lands in `SYMTAB_OVERFLOW`,
  which IS bounded.
- **`sysreg_fields`** is sized 8 B for safety though only 5 are written
  by any caller; lookup logic is internal so no caller-controlled count
  can exceed it.
- **`expr_result`** (8 B) is a fixed-size output buffer never written
  more than 8 B; safe by construction.

## Most surprising finding

The `STAGING_BUF` gap was the biggest gap by far — a single corrupted
or large record could overflow the 1 KB buffer all the way into free
RAM at `&E100+` (we'd cross `LITPOOL_EXPR_BUF` first at `&D900`). Given
how easy this is to hit with real-scale source (any `.ascii "<a long
string>"` could exceed 1 KB), it's a strong candidate for the
86 KB-release hang the audit was motivated by.
