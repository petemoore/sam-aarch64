# Comment-corpus compression research (i57)

**Status**: results complete. No decision here — that is i59 / Pete's call.

## Methodology

The source of truth is `build/release-unstripped.tbn`, produced by:

```
make release-unstripped-tbn   # sam-aarch64 -flatten (no -strip-comments)
```

The benchmark tool (`tools/comment-bench/`) reads the `.tbn` via the format library (`tools/sam-aarch64-format`), extracts every `CommentRow` from the editor-region sidecar (§2.5 of `docs/specs/tbn-binary-format-reference.md`), concatenates bodies in anchor (PC) order, and measures each compression scheme. All compressed sizes reported below **include** their decoder-table / dictionary overhead — the bytes that must live resident on the SAM alongside the assembler.

Re-run at any time:

```
make comment-bench
```

## Corpus sanity

| Metric | Value |
|--------|-------|
| Comment rows in sidecar | 7,012 |
| Total body bytes | 291,038 |
| Corpus bytes (bodies + NL separators) | 298,050 |
| Average body length | 41.5 B |
| Distinct byte values | 103 |

The sidecar has 7,012 rows versus 7,502 source `//` lines (item i39b-2 and
m8-status). The difference is expected: multi-line block comments `/* … */` are
stored as one sidecar row with embedded newlines. Body text (291,038 B) is also
shorter than the 335,461 B source figure: the sidecar bodies carry the text
*after* `//` prefix stripping and `/* */` delimiter removal, while the
335,461 B figure counts raw source characters including the comment markers.

## Whole-corpus compression results

All sizes include table / dictionary overhead. Ratio = compressed / raw (298,050 B corpus).

| Scheme | Bytes | Ratio |
|--------|-------|-------|
| flate level-9 **[NOT Z80-feasible, ceiling]** | 66,888 | 0.224 |
| greedy LZ77/ZX0-style [greedy stand-in] ¹ | 85,526 | 0.287 |
| dict(N=1024)+Huffman hybrid | 133,944 | 0.449 |
| word dict N=1024 | 147,251 | 0.494 |
| BPE 256-entry | 160,853 | 0.540 |
| word dict N=512 | 173,553 | 0.582 |
| word dict N=256 | 205,586 | 0.690 |
| canonical Huffman | 188,705 | 0.633 |
| word dict N=128 | 236,615 | 0.794 |

¹ greedy stand-in — real ZX0 optimal parse typically 2–5% smaller.

## Block-granularity results

PC-order corpus split into blocks; no dictionary/table overhead per block. Values are total compressed bytes across all blocks (ratio vs 298,050 B raw).

| Scheme | 1 KB blocks | 2 KB blocks | 4 KB blocks | 8 KB blocks |
|--------|-------------|-------------|-------------|-------------|
| flate level-9 [ceiling] | 131,282 (0.440) | 112,458 (0.377) | 96,977 (0.325) | 86,352 (0.290) |
| greedy LZ77/ZX0-style ¹ | 177,963 (0.597) | 155,543 (0.522) | 134,651 (0.452) | 119,191 (0.400) |
| BPE-256 | 173,329 (0.582) | 149,193 (0.501) | 137,547 (0.461) | 137,439 (0.461) |
| canonical Huffman | 248,059 (0.832) | 214,675 (0.720) | 199,179 (0.668) | 192,108 (0.645) |

¹ greedy stand-in — real ZX0 optimal parse typically 2–5% smaller.

## Per-comment distribution (dictionary/Huffman families)

Using a global table/dictionary trained on the whole corpus; body-only sizes (no table overhead).

| Scheme | Total B | Min | Max | Median |
|--------|---------|-----|-----|--------|
| raw body | 291,038 | 0 | 1,691 | 40.0 |
| Huffman (global table) | 187,011 | 0 | 1,118 | 26.0 |
| word dict N=256 (global) | 235,646 | 0 | 1,498 | 24.0 |

The per-comment median drops from 40 B raw to ~25 B with Huffman or word-dict.
The maximum comment body is 1,691 B (a large block comment).

## Z80 decoder cost table

Decoder sizes are documented in the respective project READMEs / assembly source headers. Cycle counts (T-states/byte) are not published by those projects; the figures below are unverified estimates based on the instruction-level complexity of each scheme.

| Scheme | Decoder bytes | Decode speed | Source |
|--------|---------------|--------------|--------|
| ZX0 standard | 68 | unverified estimate | <https://github.com/einar-saukas/ZX0> README: "68 bytes only" |
| ZX0 turbo | 126 | ~21% faster than standard | <https://github.com/einar-saukas/ZX0> README |
| LZSA1 small | 67 | unverified estimate | <https://github.com/emmanuel-marty/lzsa> `asm/z80/unlzsa1_small.asm` header |
| LZSA2 small | 134 | unverified estimate | <https://github.com/emmanuel-marty/lzsa> `asm/z80/unlzsa2_small.asm` header |
| canonical Huffman | ~80–120 est. | ~40–60 T/byte est. | unverified estimate; classic Z80 Huffman decoders |
| word dictionary | ~50–80 est. | ~30–50 T/byte est. | unverified estimate; table lookup + copy loop |
| BPE 256-entry | ~80–120 est. | ~50–100 T/byte est. | unverified estimate; recursive expansion; stack-intensive |
| flate level-9 | N/A (not Z80) | N/A | not feasible on Z80 |

T-state decode speed is relevant for in-editor decompression on demand (e.g. decompressing a comment block when the cursor reaches it). At 6 MHz, 1 T-state ≈ 167 ns; a 1 KB block at 60 T/byte ≈ 10 ms — perceptible but not jarring. At 40 T/byte ≈ 7 ms.

No credible published Go port of ZX0, ZX7, or LZSA was found on pkg.go.dev (checked 2026-06-10). The LZ77 implementation here is a greedy stand-in and its output should be treated as an upper bound; real ZX0 optimal parse produces ~2–5% smaller output.

## Capacity table

Page-accounting assumptions (from `docs/notes/memory-layout.md` and `docs/specs/tbn-binary-format-reference.md`):

- **Assembler-facing prefix**: 38,584 B (`editor_region_offset` from the stripped `.tbn`), occupying 3 IN pages (pages 7–9 of the 6-page 96 KB window).
- **Assembler code pages**: 1 code page (section C, `org &8000`, < `&C000`); stack/scratch shares section D (within the same page-mapping window).
- **ENCTAB**: 1 page (page 4, paged into section A on demand).
- **OUT buffer**: 2 pages (pages 5–6, 32 KB).
- **sysreg-data**: 1 page (page 13).
- **disasm**: 1 page (page 15).
- **ROM page 0**: 1 page (always resident, not reclaimable in normal use).
- **BASIC sys area (page 1)**: 1 page.

Total non-comment resident minimum: **11 pages** (see breakdown above).

- **256 KB SAM** = 16 pages total → **5 pages (80 KB) free** for comments.
- **512 KB SAM** = 32 pages total → **21 pages (336 KB) free** for comments.

| Scheme | Compressed bytes | Pages needed | Free/256KB SAM | Free/512KB SAM | Fits? |
|--------|-----------------|--------------|----------------|----------------|-------|
| raw (uncompressed) | 298,050 | 19 | −14 | +2 | 512 KB only (barely) |
| greedy LZ77/ZX0-style ¹ | 85,526 | 6 | −1 | +15 | 512 KB only |
| canonical Huffman | 188,705 | 12 | −7 | +9 | 512 KB only |
| BPE 256-entry | 160,853 | 10 | −5 | +11 | 512 KB only |
| dict(N=1024)+Huffman | 133,944 | 9 | −4 | +12 | 512 KB only |

**Key finding**: none of the schemes tested bring the full comment corpus within the 5 free pages available on a 256 KB SAM. Even the best Z80-feasible scheme (greedy LZ77/ZX0-style at 85,526 B = 6 pages) exceeds the 256 KB budget by 1 page. On a 512 KB SAM there is comfortable headroom under every scheme (15 pages free for LZ77/ZX0).

If real ZX0 optimal parse reduces the LZ77 result by 5%, the output is ~81,250 B = still 5 pages, still exceeding the 256 KB budget (needs 6 pages, have 5). A 2% improvement would bring it to ~83,815 B = still 6 pages. To fit on 256 KB the compressed corpus must be ≤ 5 × 16,384 = 81,920 B — flate level-9 achieves 66,888 B (4 pages, well under) but is not Z80-feasible.

The capacity table does not account for the editor's working state (block-list nodes, gap buffers, parsed IR) — those will consume additional pages that reduce the comment budget further. The table is conservative in that sense.

## What the data says

This is a description of what the numbers show, not a decision (that is i59 / Pete's call).

**LZ is the clear winner for ratio**: greedy LZ77/ZX0-style achieves 0.287 ratio whole-corpus (real ZX0 probably ~0.27–0.28), which is 2× better than the next-best Z80-feasible scheme (dict+Huffman at 0.449) and within 28% of the not-Z80-feasible flate ceiling.

**Huffman/BPE/dict are a poor fit for this corpus**: the comment text is highly repetitive at the phrase/sentence level (assembly comments share many whole words and phrases), so byte-level Huffman achieves only 0.633 whole-corpus — the word structure is exactly what LZ matches are capturing. BPE and word-dict improve on Huffman (0.540 and 0.494 respectively at their best) but at much higher decoder complexity.

**Block granularity matters for LZ**: LZ degrades significantly at small block sizes (0.597 at 1 KB vs 0.287 whole-corpus) because the back-reference window is small. At 8 KB blocks it is 0.400, still better than Huffman (0.645) at 8 KB blocks.

**No Z80-feasible scheme fits a 256 KB SAM**: even optimal ZX0 is marginal. Strategies for a 256 KB SAM include: (a) block-compress only the portion of the corpus not currently visible (spill to disk via i40), (b) accept a reduced working set (only load comment pages as needed — same i40 eviction mechanism), or (c) target 512 KB SAMs as the minimum for the full-source editing experience.

**For 512 KB SAMs**: LZ77/ZX0 at ~85 KB leaves 15 pages free — comfortable headroom for editor working state, multiple source files, or future growth.
