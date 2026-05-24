# Memory layout — SAM-side assembler (`src/`)

> **Source of truth:** the header comment block in `src/assembler.asm` (currently lines ~7–177) together with the `equ` definitions immediately below it. **This doc mirrors that block; it does not replace it.** When the layout changes, the `assembler.asm` comment and its `equ`s are authoritative — update them, then re-sync this doc. Do not treat this file as the place to *define* addresses; it exists so a reader can grasp the whole map without scrolling through 32 KB of Z80.

The assembler links at `org &8000` (entry `jp start`; `CALL 32768` lands on the first byte). The 64 KB Z80 address space is divided into the four SAM paging sections A/B/C/D (16 KB each). See `docs/notes/sam-paging.md` for the LMPR/HMPR paging primer and `docs/notes/2026-05-28-memory-layout-brainstorm.md` for the design discussion behind the off-axis moves.

## Section map (logical address space)

| Range | Section | Contents |
|-------|---------|----------|
| `&0000-&3FFF` | A | ROM0 by default; **or** ENCTAB (physical page 4) under `LMPR_ENCTAB`; **or** an IN page under `LMPR_IN_BASE + N` inside the reader bracket (see `reader.asm`). |
| `&4000-&7FFF` | B | Page 1 (BASIC sys area, mostly unused). Trampoline copy at `TRAMPOLINE_DST` (`&7E00`). Under `LMPR_ENCTAB`, section B = page 5 = OUT-low (the OUT emit window — see `emit_byte`). |
| `&8000-&AFFF` | C | **Assembler code** (12 KB: `assembler.asm` + all M3/M4/M5/M6 includes). |
| `&B000-&BFFF` | C | Reserved / free (4 KB freed by M6: was IN_BUF + OUT_BUF pre-M6; both now paged out — `&B800-&BFFF` freed by M6 PR 1 (OUT), `&B000-&B7FF` by M6 PR 2 (IN)). |
| `&C000-&C0FF` | D | Stack (`SP = &C100`, grows down). |
| `&C100-&D4FF` | D | Scratch — OPVAL arrays, SYMTAB, litpool table + counters (sub-allocations below). |
| `&D500-&D8FF` | D | `STAGING_BUF` — paged-IN per-record staging area (M6 PR 2). |
| `&D900-&E0FF` | D | `LITPOOL_EXPR_BUF` — cross-pass expr-bytecode pool (M6 PR 2). |
| `&E100-&E27F` | D | `LITPOOL_PC_MAP` (64 × 6 = 384 B; moved here 2026-05-28). |
| `&E280-&E77C` | D | `LOCAL_LABEL_TABLE` (2 + 255 × 5 = 1277 B; moved here 2026-05-28). |
| `&E77D-&E7FF` | D | free (131 B). |
| `&E800-&EFFF` | D | `SYMTAB_OVERFLOW` (256 × 8 = 2 KB; moved here 2026-05-28). |
| `&F000-&F01A` | D | MOV-imm + logical-imm encoder scratch (added 2026-05-29 for the release byte-match encoder fixes). |
| `&F01B-&FFFF` | D | free (~4 KB headroom). |

## Physical (off-axis) pages

These pages are not normally mapped into the address space; they are paged in on demand. (`scratch` `equ`s that point into them are listed above.)

| Page(s) | Contents |
|---------|----------|
| 4 | ENCTAB body — paged into section A on demand for encoder reads. See `trampoline.asm`. |
| 5..6 | OUT buffer. Page 5 reached via section B under `LMPR_ENCTAB` (low zone, bytes 0..16383); page 6 via `LMPR_OUT_HIGH` per emit (high zone, 16384..32767). HSAVE at end of pass 2 reads via section C with `UIFA[31] = OUT_BASE_PAGE`. |
| 7..12 | IN `.tbn` buffer — 6 contiguous pages = 96 KB ceiling (bumped from 4 pages / 64 KB on 2026-05-28 to fit spectrum4's 88 KB stripped `release.tbn`). HLOAD'd once at startup; read per-record via an LMPR bracket. |
| 13 | `sysreg_data.bin` (production payload, every build) **and** `test_mem.bin` (`BUILD_TESTS` only) — mutually exclusive across the prod/test split. Off-axis HLOAD at boot. |
| 12 | `test_cluster.bin` (`BUILD_TESTS` only) — the off-axis "M5 + misc encoder" suite (overlaps the IN-buffer page range; test variant only). |
| 14 | `paged_call_test_payload.bin` (`BUILD_TESTS` only). |

(The exact page assignments for the test payloads are defined in `Makefile` recipes and `loader.asm`; the IN/OUT/ENCTAB physical pages are the source-of-truth in the `assembler.asm` header.)

## Scratch region `equ`s (section D)

Defined in `assembler.asm` (and allocated/used by the named files). Addresses, copied from the header — the `equ`s in `assembler.asm` are authoritative:

| `equ` | Address | Size | Notes |
|-------|---------|------|-------|
| `OPVAL_ARRAY` | `&C100` | 70 B | 7 × 10 operand value array. |
| `OPVAL_KINDS` | `&C150` | 7 B | kinds[] for `form_lookup_match`. |
| `PASS_MODE` | `&C158` | 1 B | current pass (`PASS_PASS1`=1 / `PASS_PASS2`=2). |
| `PASS_PC` | `&C159` | 4 B | current pass PC (u32 LE). |
| `SYMTAB` | `&C160-&C95F` | 2 KB | 256 buckets × 8 B. |
| `ORIGIN_HIGH` | `&C960` | 4 B | high word of OriginVMA (u32 LE); applied to origin-relative values. |
| `SYMTAB_ABS_BITMAP` | `&C964` | 64 B | per-symbol-id absolute flag (1=absolute const, 0=origin-relative). |
| (free) | `&C9A4-&CFFF` | 1628 B | old SYMTAB_OVERFLOW / LOCAL_LABEL_TABLE regions, freed 2026-05-28. |
| `OPMEM_OFF` | `&D100` | 8 B | OpMem offset (s64 LE). |
| `LITPOOL_TABLE` | `&D200-&D3BF` | 448 B | 32 slots × 14 B. |
| `LITPOOL_COUNT` | `&D3C0` | 1 B | |
| `LITPOOL_PCM_COUNT` | `&D3C1` | 1 B | |
| `LITPOOL_SEGMENT_ALLOC` | `&D3C2` | 1 B | |
| `LITPOOL_SEGMENT_FLUSH` | `&D3C3` | 1 B | |
| `LITPOOL_SAVED_PC` | `&D3C4-&D3C7` | 4 B | |
| `STAGING_BUF` | `&D500` (end `&D900`) | 1 KB | record staging area. |
| `LITPOOL_EXPR_BUF` | `&D900` (end `&E100`) | 2 KB | cross-pass expr pool. |
| `LITPOOL_PC_MAP` | `&E100-&E27F` | 384 B | 64 entries × 6 B. |
| `LOCAL_LABEL_TABLE` | `&E280-&E77C` | 1277 B | 255 entries. |
| `SYMTAB_OVERFLOW` | `&E800-&EFFF` | 2 KB | 256 entries. |

## Code-budget ceiling — the `&C000` cliff

Both assembler variants link at `org &8000`; their scratch/stack starts at `&C000` (`SP = &C100`). If a build's `code_end` reaches `&C000` it collides with the stack page, and the failure manifests as a **silent deterministic boot-hang** (rc=124) with no diagnostic — the exact "test-variant fragility" class that bit PR #43 (see `memory/feedback_test_variant_fragility.md`).

`scripts/check-code-budget.sh` turns that silent cliff into a build/CI failure **with a number** (`code_end &C0xx ≥ ceiling &C000 — N bytes over`). It runs at the tail of every `make m3-asm` / `m3-asm-prod` recipe, and `make check-budget` checks both variants explicitly (the M6-closure CI gate). Defaults: `ORG=0x8000`, `CEILING=0xC000`.

- **prod variant** (`build/assembler-prod.bin`) — smaller; self-tests `#ifdef`'d out.
- **test variant** (`build/assembler.bin`) — larger; includes in-section self-tests. The off-axis moves (test_mem → page 13, the M5/misc cluster → page 12, the IN/OUT/ENCTAB buffers → pages 4–12) exist specifically to keep both variants under the cliff. The script prints the headroom-to-ceiling for each.

## Related docs

- `src/assembler.asm` — **the source of truth** (header comment + `equ`s).
- `src/README.md` — assembler taxonomy (prod/test, off-axis modules, include order).
- `docs/notes/sam-paging.md` — SAM Coupé paging primer (sections, LMPR/HMPR).
- `docs/notes/2026-05-28-memory-layout-brainstorm.md` — design discussion behind the off-axis layout.
- `scripts/check-code-budget.sh` — the budget gate.
