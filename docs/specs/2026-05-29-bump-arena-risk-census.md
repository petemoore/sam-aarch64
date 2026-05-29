# Bump-arena risk census — do the fixed-capacity tables need a growable mechanism?

**Date:** 2026-05-29
**Status:** design / analysis only (no code changes)
**Question (Pete's M7 framing):** the SAM-side assembler uses fixed-capacity arrays for every internal data structure. Is any of them a real *overrun time-bomb* — could a *plausible* (not pathological) future spectrum4 source push it past its cap, turning today's clean hard-fail into a recurring hazard? If so, build a per-component bump arena (which needs a small allocator) — but only if genuinely needed. If the caps are comfortably above realistic demand, leave them.

## TL;DR — headline recommendation

**No general bump-arena is needed today, and none of the section-D fixed arrays is the right place to spend that complexity.** The section-D scratch tables (symbol table, OPVAL, litpool, local labels, eval stack, staging) are all **SAFE** under the projected ~5× kernel growth, with the closest call (litpool slots) still landing inside its current cap and trivially bumpable in place. There is ~6 KB of contiguous free section-D RAM (`&F01B..&FFFF`) plus 1.6 KB more (`&C9A4..&CFFF`) sitting unused, so even the safe tables have headroom for an in-place cap bump if a future input demands it.

**The two real ceilings are not section-D arrays — they are the off-axis paged byte buffers:**

1. **OUT buffer — AT-RISK.** Hard 32 KB cap (2 physical pages, fails clean with tag `b0`), observed peak 21,752 B = **68% used**. A 5× kernel emits ~109 KB and would blow this ceiling. *But the fix is "allocate more OUT pages", not an arena* — it is already a bump-style page-walking buffer.
2. **IN `.tbn` buffer — AT-RISK / nearly-overflowing.** Hard 96 KB cap (6 physical pages 7..12, fails clean with tag `03`), observed peak **88,644 B = 92% used today**. This is the single tightest constraint in the whole system. A larger kernel overflows it almost immediately. *Again the fix is "allocate more IN pages" (or compact the `.tbn`), not a section-D arena.*

So the honest answer to "do we need a bump arena?" is **(a) no — not for the fixed arrays Pete was worried about**, with one caveat: the *paging budget* for the IN/OUT byte streams is the thing that will bite first, and that is a separate (cheaper) problem solved by allocating more SAM physical pages, which the codebase already does as a one-line `equ`/bounds bump. The detailed evidence follows.

## 1. Risk-census table

All caps and peaks below are cited to source. "Observed peak" is from the empirical census `docs/notes/2026-05-28-z80-table-sizing-census.md`, which ran the Go reference encoder (`tools/refenc --dump-usage`) over the fresh `release.tbn` — byte-identical to the Z80 assembler and the GNU oracle on the M3..M6 corpora, so its observers fire from the same code paths the Z80 would. Caps below were re-read from the *current* source (several were raised after that census was written, so they differ from the census's "Z80 limit" column — that is expected and noted).

"Plausible-growth peak" applies Pete's stated ~5× full-kernel multiplier to the count-scaling structures. The 5× is a deliberately generous upper bound on a *plausible* (not pathological) spectrum4; structures that scale with operand *width* or expression *depth* rather than input *size* do not multiply.

| # | Structure | Where defined (file:symbol) | Elem size | Capacity (count / bytes) | Observed peak (release.tbn) | Plausible 5× peak | Class |
|---|---|---|---|---|---|---|---|
| 1 | Symbol table — primary buckets | `src/symbols.asm:73` `SYMTAB` | 8 B | 256 buckets / 2 KB | (hash-bucketed; see below) | — | SAFE |
| 2 | Symbol table — overflow chain | `src/symbols.asm:74-75` `SYMTAB_OVERFLOW` / `SYMTAB_OVERFLOW_MAX` | 8 B | 256 entries / 2 KB | 474 total symbols → ≤218 overflow | ~2370 total → would overflow | **AT-RISK at 5×** (SAFE at 2-3×) |
| 3 | Symbol abs/rel bitmap | `src/assembler.asm:147` `SYMTAB_ABS_BITMAP` | 1 bit | ids 0..511 / 64 B | id 474 | ~2370 ids → overflow | **AT-RISK at 5×** (couples to #2) |
| 4 | Local-label table | `src/local_labels.asm:70-71` `LOCAL_LABEL_TABLE` / `LOCAL_LIST_MAX` | 5 B | 255 entries / 1277 B | 172 | ~860 → would overflow | **AT-RISK at 5×** (SAFE at ~3×) |
| 5 | Litpool slot table | `src/litpool.asm:62,67` `LITPOOL_MAX` / `LITPOOL_TABLE` | 14 B | 32 slots / 448 B | 30 (per flush) | bounded per-`.ltorg`, not by kernel size — see below | SAFE (watch) |
| 6 | Litpool PC-map | `src/litpool.asm:63,68` `LITPOOL_PCM_MAX` / `LITPOOL_PC_MAP` | 6 B | 64 entries / 384 B | 44 (per flush) | bounded per-`.ltorg` — see below | SAFE |
| 7 | Litpool cross-pass expr buf | `src/assembler.asm:81-82` `LITPOOL_EXPR_BUF` | byte stream | 2048 B | 210 B | ~1 KB | SAFE |
| 8 | Expression eval stack | `src/expr_eval.asm:81,914` `EXPR_STACK_DEPTH` / `expr_stack` | 8 B | 8 slots / 64 B | 3 | 3 (depth ≠ size) | SAFE |
| 9 | OPVAL array (operands/inst) | `src/assembler.asm:84` `OPVAL_ARRAY`; cap check `src/main_loop.asm:534` | 10 B | 7 operands / 70 B | 4 | 7 (ISA-bounded, not size) | SAFE |
| 10 | STAGING_BUF (per-record) | `src/assembler.asm:79-80`; bounds `src/reader.asm:202` | byte stream | 1024 B | 94 B | ~few hundred B (per-record) | SAFE |
| 11 | OUT buffer (emitted image) | `src/main_loop.asm:2407-2411` `OUT_LEN`/`OUT_ZONE`; cap `src/encoder.asm:479-484` | byte stream | 32 KB (2 pages) / fail tag `b0` | 21,752 B (68%) | ~109 KB → **overflow** | **AT-RISK** |
| 12 | IN `.tbn` buffer (input image) | `src/main_loop.asm:2259` cap check (tag `03`) | byte stream | 96 KB (6 pages 7..12) / fail tag `03` | 88,644 B (**92%**) | ~440 KB → **overflow** | **AT-RISK (tightest today)** |

### Why the symbol table is SAFE today and only AT-RISK at a full 5×

The symbol store is `256` primary hash buckets (`symbol_id mod 256`, `src/symbols.asm:5-8,73`) plus a `256`-entry overflow chain (`src/symbols.asm:74-75`). Total addressable = `512` symbols. The current release populates `474` (`docs/notes/.../sizing-census.md:67-77`: 282 LabelDef + 193 `.equ`/`.set`/`.global`). So we sit at `474/512 = 93%` — comfortable now (the census bumped overflow 128→256 precisely to buy this margin), but it does *not* scale: a 5× kernel implies ~2370 symbols, far past 512. At ~2× (≈950) we already overflow. The bitmap (#3) couples to this — it covers ids 0..511 (`src/assembler.asm:147`) so the same growth busts it.

**This is the structure most worth watching**, but the honest framing is: it is *not* a today-problem (93% is fine for the current and near-future kernel), and the cheap mitigation (raise `SYMTAB_OVERFLOW_MAX`, widen the bitmap, both into the free section-D RAM documented in §2) buys 2-3× before any arena would be warranted. The chain-link `next_off` is already an *index* form explicitly designed to "expand the overflow region to 64 KB without changing the on-disk layout" (`src/symbols.asm:38-43`) — i.e. the data structure was built to be cap-bumped, not rewritten.

### Why litpool slots are SAFE despite looking tight (30/32)

The census flags slots at 30/32 as "tight" and that reads alarming, but the litpool caps are **per-`.ltorg`-segment**, not per-program. `LITPOOL_MAX`/`LITPOOL_PCM_MAX` bound the *active* slots/pc-map entries between flushes; `.ltorg` (or an implicit flush) resets the pending set (`src/litpool.asm:43-55`, mirroring `refenc/pass1.go:127`). So these caps scale with *literal density between two `.ltorg`s*, a property of how the source is written, not of total kernel size. A 5× kernel with the same `.ltorg` cadence has the *same* per-flush peak. The risk is only if a future source clusters many more distinct literals into one un-flushed segment — which is a code-style change, easily diagnosed (clean fail tag), and trivially fixed by a cap bump or an inserted `.ltorg`. Watch it; don't arena it.

### Why OPVAL / eval-stack / staging are structurally SAFE at any kernel size

These scale with the *complexity of a single instruction or record*, not the program. OPVAL holds operands for one instruction (max 4 observed, cap 7, ISA-bounded — no aarch64 instruction we encode takes >5 register/imm operands; `src/main_loop.asm:531-535`). The eval stack depth (3 observed / 8 cap, `src/expr_eval.asm:81`) is bounded by expression nesting, which collapses fast on binary ops. STAGING_BUF (94 B / 1024 B, `src/reader.asm:198-205`) holds one record's payload. None of these multiplies with kernel size; all fail clean if exceeded. SAFE permanently.

### needs-measurement

- **The 5× peak figures in the table are *derived* (current peak × 5), not measured.** No 5×-size spectrum4 `.tbn` exists to run through `refenc --dump-usage`. The multiplier is Pete's stated rough projection; treat the "5× peak" column as order-of-magnitude. The *trigger* in §4 makes this measurable when a bigger kernel actually exists.
- **Symbol-count-to-overflow distribution is hash-dependent.** "474 total → ≤218 overflow" assumes the census's observed collision behaviour; a different id distribution in a future kernel could push more (or fewer) into the chain. The 512 *total* ceiling is hard regardless.

## 2. Physical-RAM budget — is there free RAM for an arena to bump into?

An arena only helps if there is *pooled* free RAM to bump from. There is, but it is modest, and it is in two different physical tiers:

**Section D (logical `&C000..&FFFF`, the assembler's scratch + the tables in #1-#10).** The authoritative map is the `src/assembler.asm` header (lines 7-176) and `docs/notes/memory-layout.md`. Free regions today:

- `&C9A4..&CFFF` — **1628 B** (old SYMTAB_OVERFLOW + LOCAL_LABEL_TABLE regions, freed when both moved off to `&E100+` on 2026-05-28; `src/assembler.asm:153-155`).
- `&E77D..&E7FF` — 131 B (gap between local-labels and symtab-overflow; `src/assembler.asm:29`).
- `&F01B..&FFFF` — **~4 KB** (`src/assembler.asm:34`).

Total ≈ **5.7 KB** of free section-D RAM, in three fragments, the largest ~4 KB. The stack lives at `&C000..&C0FF` (SP=`&C100`, grows down; `src/assembler.asm:21`); nothing currently approaches it from below — the tables stop at `&EFFF` and resume at `&F01B`. The `&C000` cliff that bites is the **code** ceiling (section C `org &8000` growing up into `&C000`), guarded by `scripts/check-code-budget.sh` (tag-free silent boot-hang otherwise — the PR #43 class). That cliff constrains *code size*, not these scratch tables, which live above the stack in section D.

**Off-axis physical pages (the byte streams, #11-#12).** These do *not* compete with section-D RAM — they are SAM physical pages mapped in on demand:

- OUT: pages 5..6 = 32 KB (`docs/notes/memory-layout.md:33`, `src/encoder.asm:457-484`).
- IN: pages 7..12 = 96 KB (`src/main_loop.asm:2252-2262`).
- ENCTAB: page 4; test payloads: pages 13,12,14 (`memory-layout.md:31-37`).

The SAM 400 has 512 KB (32 pages of 16 KB). The assembler currently claims pages 1 (BASIC sys / trampoline), 4 (ENCTAB), 5-6 (OUT), 7-12 (IN), 13 (sysreg/test_mem), 14 (test payload). Pages 15..31 (~272 KB) are **unallocated** — so growing the IN and OUT ceilings is purely a matter of claiming more pages and bumping the two bounds checks. That is the relevant "free RAM" for the only two AT-RISK structures, and it is abundant.

**Conclusion for §2:** there is enough free section-D RAM for in-place cap bumps of the count-scaling tables (#2, #3, #4) to cover 2-3× growth, and enough free *physical pages* to grow the byte buffers (#11, #12) well past 5×. Neither needs a general allocator to access that free RAM — the existing pattern (an `equ` move + a bounds-check constant) reaches it directly.

## 3. Recommendation — **(a) no arena needed**, plus a cheap safety net

The fixed *arrays* Pete was worried about are not the bomb. Of the twelve structures, eight are SAFE at any plausible size (#1, #5-#10, and the 5× framing shows #5/#6 scale with `.ltorg` cadence not kernel size). Three (#2/#3 symbol store, #4 local labels) are SAFE today and only AT-RISK at a *full* 5× — and each is trivially bumpable in place using the free section-D RAM, with data structures already designed for cap growth (`src/symbols.asm:38-43`). The two genuinely tight things (#11 OUT, #12 IN) are paged byte streams whose fix is "allocate more pages", which is not what an arena is for.

A per-component bump arena would add: a small allocator per page, per-op bump overhead on the hot encode path, and reasoning complexity — to solve a problem (artificial per-table ceiling below true OOM) that **does not exist yet** for any structure where an arena would be the right tool. Pete's own rule is to build it only if genuinely needed; the evidence says not yet. Building it now would be complexity for its own sake.

### Cheap safety net to adopt instead (low cost, high value)

1. **Runtime cap-headroom assertion is already the model — extend the doc, not the code.** Every table already fails *clean* with a distinct tag (symbol overflow → `fail_with_tag 21` at `src/symbols.asm:236`; staging → tag `01`; IN pages → tag `03`; OUT → tag `b0`). So today's failure mode is already "loud and diagnosable", not "silent corruption". The one improvement worth making is **documenting the re-tune procedure** (below) so the next person who hits a tag knows it is a one-line bump, not a redesign.

2. **Add the cap/peak watch to the census re-run recipe.** The census doc (`docs/notes/2026-05-28-z80-table-sizing-census.md:196-208`) already has a `refenc --dump-usage` re-run recipe. Note in that doc that it should be re-run whenever spectrum4 grows materially, and that any table crossing ~80% utilisation is the trigger to bump (see §4).

3. **Document the re-tune procedure** (this is the actual deliverable of recommendation (a)): for the three count-scaling tables, bumping is: (i) raise the `_MAX` `equ`, (ii) move the table's base `equ` into one of the free section-D fragments in §2 if it would collide, (iii) re-run `check-code-budget.sh` + the SimCoupé gate. For IN/OUT, bumping is: claim another physical page (15+), bump the bounds-check constant (`cp 7` at `src/main_loop.asm:2259` for IN; the zone logic at `src/encoder.asm:479` for OUT) and the page-walk. No allocator, no arena.

### What would flip this to (b) "arena needed"

The trigger to revisit (and the *only* condition under which an arena earns its complexity) is in §4.

## 4. Trigger to revisit (the "what would change the conclusion")

Revisit and consider a per-component bump arena **only** if *all* of these hold:

1. A real (not projected) spectrum4 `.tbn` pushes **two or more** of the count-scaling tables (#2/#3, #4) past ~80% *simultaneously*, AND
2. the free section-D fragments in §2 can no longer absorb the in-place bumps (i.e. the three fragments are exhausted, or the needed table is large enough that fragmentation across `&C9A4`/`&E77D`/`&F01B` actually blocks a contiguous bump), AND
3. the IN/OUT page ceilings are *also* being pushed such that "just claim more pages" no longer trivially works (e.g. the test payloads on pages 12-14 are in the way and a compaction is needed anyway).

If only #1 holds, the answer stays "bump the cap in place". Only when section-D contiguity itself becomes the binding constraint (#2) does a per-component arena — per the `docs/notes/2026-05-28-memory-layout-brainstorm.md:122,129` direction (per-component arenas on known pages, no global allocator) — become the cheaper option than repeated manual relocation.

### Phased sketch *if* (b) is ever triggered (scoped to at-risk structures only)

Not to be built now. Recorded so the path is known:

1. **First component: the symbol overflow chain (#2 + its bitmap #3)** — it is the count-scaling structure with the lowest hard ceiling (512) and its `next_off` index form already anticipates a 64 KB region (`src/symbols.asm:38-43`). Give it a dedicated off-axis page (one of 15+) with a trivial bump pointer (`next = base; alloc → next += 8`); the existing chain walk multiplies index×8 already. This is the *smallest possible* arena and validates the pattern.
2. **Verify** with the Go harness (`tools/z80-test-harness-go/`, ~1 ms/fixture inner loop) on the symbol fixtures, then the SimCoupé matrix gate (the only CI gate) on the full release byte-match (`scripts/build-spectrum4-release.sh` / the `m6-release` job) — an arena that changes symbol storage must keep the 21,752 B byte-match exact.
3. **Budget impact:** one new physical page (16 KB) per arena'd component; section-D logical footprint *shrinks* (the table's `equ` region is freed back to the free pool). No code-budget (`&C000` cliff) impact since arenas live off-axis.
4. **Then, only if still needed:** local-label table (#4) as a second arena, same recipe. Stop there — #5-#10 never need it.

## Sources

- `docs/notes/2026-05-28-z80-table-sizing-census.md` — empirical peaks (release.tbn via `refenc --dump-usage`).
- `src/assembler.asm:7-176` — authoritative section-D memory map + the `equ`s.
- `src/symbols.asm:5-8,38-43,73-75,236` — symbol hash/overflow design, caps, fail tag.
- `src/local_labels.asm:70-71` — `LOCAL_LABEL_TABLE` / `LOCAL_LIST_MAX = 255`.
- `src/litpool.asm:43-68` — per-`.ltorg`-segment caps `LITPOOL_MAX=32` / `LITPOOL_PCM_MAX=64`.
- `src/expr_eval.asm:81,914` — `EXPR_STACK_DEPTH=8`.
- `src/main_loop.asm:531-535` — OPVAL operand-count cap (`cp 8`).
- `src/main_loop.asm:2252-2264` — IN 96 KB / 6-page ceiling, fail tag `03`.
- `src/encoder.asm:438-497` — OUT emit, 32 KB / 2-page ceiling, fail tag `b0`.
- `src/reader.asm:198-205` — STAGING_BUF 1 KB bound, fail tag `01`.
- `docs/notes/memory-layout.md` — section/page map mirror; `scripts/check-code-budget.sh` — the `&C000` code cliff gate.
- `docs/notes/2026-05-28-memory-layout-brainstorm.md:110,122,129` — per-component-arena-on-known-pages direction; free physical pages.
- `build/m6-release.tbn` — 88,644 B (measured), the current IN demand; `release.bin` — 21,752 B, the current OUT demand.
