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

The sidecar has 7,012 rows versus 7,502 source `//` lines (item i39b-2 in
`docs/notes/item-registry-open.md`). The difference is expected: multi-line block comments `/* … */` are
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

Decoder sizes are documented in the respective project READMEs / assembly source headers.

ZX0 T-states/byte figures are **measured** (see "Measured decode latency" section below); all other figures are unverified estimates.

| Scheme | Decoder bytes | Decode speed | Source |
|--------|---------------|--------------|--------|
| ZX0 standard | 68 | **see table below** | measured via harness; `tools/z80-test-harness-go/zx0_decode_bench_test.go` |
| ZX0 turbo | 126 | **see table below** | measured via harness; `tools/z80-test-harness-go/zx0_decode_bench_test.go` |
| LZSA1 small | 67 | unverified estimate | <https://github.com/emmanuel-marty/lzsa> `asm/z80/unlzsa1_small.asm` header |
| LZSA2 small | 134 | unverified estimate | <https://github.com/emmanuel-marty/lzsa> `asm/z80/unlzsa2_small.asm` header |
| canonical Huffman | ~80–120 est. | ~40–60 T/byte est. | unverified estimate; classic Z80 Huffman decoders |
| word dictionary | ~50–80 est. | ~30–50 T/byte est. | unverified estimate; table lookup + copy loop |
| BPE 256-entry | ~80–120 est. | ~50–100 T/byte est. | unverified estimate; recursive expansion; stack-intensive |
| flate level-9 | N/A (not Z80) | N/A | not feasible on Z80 |

T-state decode speed is relevant for in-editor decompression on demand (e.g. decompressing a comment block when the cursor reaches it). At 6 MHz, 1 T-state ≈ 167 ns.

No credible published Go port of ZX0, ZX7, or LZSA was found on pkg.go.dev (checked 2026-06-10). The LZ77 implementation here is a greedy stand-in and its output should be treated as an upper bound; real ZX0 optimal parse produces ~2–5% smaller output.

## Measured decode latency (i60a)

Real ZX0 decode speed measured by running the upstream Z80 decoders
(`reference/zx0/z80/dzx0_standard.asm`, `dzx0_turbo.asm`, commit
`ecde3a2`) inside the koron-go/z80 emulator over 6 blocks per block size
sampled from the real comment corpus (first 4 + 2 from the corpus midpoint).
Compressed with the real ZX0 v2.2 optimal-parse compressor.

**Counting method**: instruction-level T-state table; koron-go/z80 v0.10.2
does not expose T-state counts, so each `cpu.Step()` call is preceded by
reading the opcode at PC and looking it up in a Zilog UM0080 Table-2 table.
Data-dependent instructions (LDIR, conditional JR/JP/CALL/RET) read
pre-step CPU state to pick the taken/not-taken count.
Label: "instruction-level T-state table (koron-go/z80 v0.10.2)".

**Correctness**: every block's decompressed output was byte-compared against
the original raw block. All 24 blocks × 2 decoders = 48 checks passed.

**Contention caveat**: figures are for uncontended RAM.  Real SAM RAM in
screen-visible VRAM pages can incur up to 1 extra T-state per memory M-cycle
during ULA DMA, but comment pages are allocated in non-VRAM pages so
contention is negligible in normal use.

**Reproduction**:

```
make zx0-blocks   # requires zx0 binary on PATH or at /tmp/zx0
cd tools/z80-test-harness-go && go test -run TestZX0DecodeBench -v -count=1 .
```

### Results

| Block size | ZX0 standard T/byte | Std ms/block | ZX0 turbo T/byte | Turbo ms/block | N | Avg compress ratio |
|------------|---------------------|--------------|-----------------|----------------|---|--------------------|
| 1 KB | 68.7 | 11.7 | 55.2 | 9.4 | 6 | 0.406 |
| 2 KB | 67.2 | 22.9 | 54.2 | 18.5 | 6 | 0.355 |
| 4 KB | 66.2 | 45.2 | 53.3 | 36.4 | 6 | 0.316 |
| 8 KB | 64.4 | 87.9 | 51.9 | 70.8 | 6 | 0.285 |

The upstream README claims "~21% faster" for the turbo decoder; the measured
ratio is (55.2−68.7)/68.7 = −19.6% for 1 KB blocks and (51.9−64.4)/64.4 =
−19.4% for 8 KB blocks — very close to the advertised figure.

**Design implication**: at 1 KB blocks (smallest practical size), the turbo
decoder decompresses in 9.4 ms at 6 MHz — 9.4 ms to recover 1 KB of comment
text. A 4 KB block takes 36 ms (turbo). For cursor-on-block interactive
decompression, 4 KB is near the boundary of "perceptible but acceptable"
(< 100 ms is the usual threshold for UI responsiveness). 8 KB blocks at
70 ms are still within the interactive budget. The sweet spot is likely 4–8 KB
blocks balancing compression ratio (0.316–0.285) against latency.

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

## Greedy ZX0 compressor — ratio, scratch-RAM, and Z80 time model (i60b-1)

The measurements below come from `TestZX0GreedyOracle` in
`tools/z80-test-harness-go/zx0_greedy_oracle_test.go`.  Each corpus block
is compressed with the Go greedy compressor (`tools/zx0-greedy`) at every
(H, D) operating point, then decompressed with the real upstream
`dzx0_standard` Z80 decoder running in the koron-go/z80 emulator.  All 384
block × (H,D) combinations passed byte-for-byte.  Optimal-parse sizes come
from the `.zx0` files produced by the upstream `zx0` tool (optimal parse).

Reproduce at any time:

```
make zx0-blocks
cd tools/z80-test-harness-go && go test -run TestZX0GreedyOracle -v -count=1 .
```

### Greedy vs optimal ratio, all block sizes

Ratio = compressed / raw.  Gap% = (greedy − optimal) / optimal × 100.
Scratch = Z80 scratch-RAM in bytes (hash table + chain array + fixed state).

| H | D | blkKB | greedy ratio | optimal ratio | gap% | scratch |
|---|---|-------|-------------|---------------|------|---------|
| 256 | 4 | 1 | 0.4650 | 0.4059 | +14.6% | 2590 B |
| 256 | 16 | 1 | 0.4587 | 0.4059 | +13.0% | 2590 B |
| 256 | 32 | 1 | 0.4567 | 0.4059 | +12.5% | 2590 B |
| 512 | 4 | 1 | 0.4635 | 0.4059 | +14.2% | 3102 B |
| 512 | 16 | 1 | 0.4585 | 0.4059 | +13.0% | 3102 B |
| 512 | 32 | 1 | 0.4567 | 0.4059 | +12.5% | 3102 B |
| 2048 | 32 | 1 | 0.4567 | 0.4059 | +12.5% | 6174 B |
| 256 | 4 | 2 | 0.4173 | 0.3552 | +17.5% | 4638 B |
| 256 | 16 | 2 | 0.4058 | 0.3552 | +14.2% | 4638 B |
| 256 | 32 | 2 | 0.4033 | 0.3552 | +13.5% | 4638 B |
| 512 | 4 | 2 | 0.4142 | 0.3552 | +16.6% | 5150 B |
| 512 | 16 | 2 | 0.4057 | 0.3552 | +14.2% | 5150 B |
| 512 | 32 | 2 | 0.4033 | 0.3552 | +13.5% | 5150 B |
| 2048 | 32 | 2 | 0.4033 | 0.3552 | +13.5% | 8222 B |
| 256 | 4 | 4 | 0.3830 | 0.3162 | +21.1% | 8734 B |
| 256 | 16 | 4 | 0.3630 | 0.3162 | +14.8% | 8734 B |
| 256 | 32 | 4 | 0.3604 | 0.3162 | +14.0% | 8734 B |
| 512 | 4 | 4 | 0.3784 | 0.3162 | +19.7% | 9246 B |
| 512 | 16 | 4 | 0.3631 | 0.3162 | +14.8% | 9246 B |
| 512 | 32 | 4 | 0.3603 | 0.3162 | +13.9% | 9246 B |
| 1024 | 32 | 4 | 0.3602 | 0.3162 | +13.9% | 10270 B |
| 2048 | 32 | 4 | 0.3602 | 0.3162 | +13.9% | 12318 B |
| 256 | 4 | 8 | 0.3572 | 0.2847 | +25.5% | 16926 B |
| 256 | 16 | 8 | 0.3316 | 0.2847 | +16.5% | 16926 B |
| 256 | 32 | 8 | 0.3274 | 0.2847 | +15.0% | 16926 B |
| 512 | 4 | 8 | 0.3505 | 0.2847 | +23.1% | 17438 B |
| 512 | 16 | 8 | 0.3302 | 0.2847 | +16.0% | 17438 B |
| 512 | 32 | 8 | 0.3269 | 0.2847 | +14.8% | 17438 B |
| 1024 | 32 | 8 | 0.3265 | 0.2847 | +14.7% | 18462 B |
| 2048 | 32 | 8 | 0.3265 | 0.2847 | +14.7% | 20510 B |

**Diminishing returns above H=512, D=16**: moving from H=512 D=16 to
H=2048 D=32 at 4 KB blocks changes ratio from 0.3631 to 0.3602 — a 0.8%
improvement for 3.3× more scratch RAM (9246 B → 12318 B) and ~4× more chain
steps per byte.  The gain is real but small; the chain-depth effect (D=4 vs
D=32) matters more than the hash-size effect (H=256 vs H=2048).

### Scratch-RAM model at 4 KB blocks

`ScratchBytes(p, 4096) = p.HashSize × 2 + 4096 × 2 + 30`.

| H | D | scratch | greedy ratio | gap% vs optimal |
|---|---|---------|-------------|-----------------|
| 256 | 4 | 8734 B | 0.3830 | +21.1% |
| 256 | 8 | 8734 B | 0.3694 | +16.8% |
| 256 | 16 | 8734 B | 0.3630 | +14.8% |
| 256 | 32 | 8734 B | 0.3604 | +14.0% |
| 512 | 4 | 9246 B | 0.3784 | +19.7% |
| 512 | 8 | 9246 B | 0.3679 | +16.3% |
| 512 | 16 | 9246 B | 0.3631 | +14.8% |
| 512 | 32 | 9246 B | 0.3603 | +13.9% |
| 1024 | 4 | 10270 B | 0.3750 | +18.6% |
| 1024 | 8 | 10270 B | 0.3665 | +15.9% |
| 1024 | 16 | 10270 B | 0.3622 | +14.5% |
| 1024 | 32 | 10270 B | 0.3602 | +13.9% |
| 2048 | 4 | 12318 B | 0.3745 | +18.4% |
| 2048 | 8 | 12318 B | 0.3664 | +15.9% |
| 2048 | 16 | 12318 B | 0.3622 | +14.5% |
| 2048 | 32 | 12318 B | 0.3602 | +13.9% |

### Z80 T-state time model (H=512, D=16, recommended point)

Probe stats collected by `zx0greedy.CollectProbeStats` on the first 4 KB
and 8 KB corpus blocks (block_0004kb_0000.raw, block_0008kb_0000.raw):

| Block size | Chain steps/byte | T-states/byte (model) | Time at 6 MHz |
|------------|------------------|----------------------|---------------|
| 4 KB | 9.85 | 414 T | **283 ms** |
| 8 KB | — | 483 T | **660 ms** |

**Model**: T/byte = 20 (hash + insert) + chainSteps/byte × 40 (chain step).
These are model-based estimates; the actual Z80 port will need cycle-exact
measurement.

**Contention note**: figures are for uncontended RAM.  The compressor runs on
dirty comment pages (non-VRAM), so contention is negligible in normal use.

### Measured Z80 compressor T-state performance (i60b-2)

Real measurement from running `src/zx0_compress.asm` (the Z80 port, `build/zx0_compress.bin`, 1182 B)
inside the koron-go/z80 emulator.  Same instruction-level T-state counting method as the decode bench
(Zilog UM0080 Table-2, data-dependent instructions read pre-step CPU state).

All 24 blocks (6 per block size, from `build/zx0-blocks/`) passed byte-identity against Go and round-trip
via `dzx0_standard`.

Reproduce:

```
make zx0-compress-payload zx0-blocks
cd tools/z80-test-harness-go && go test -run TestZX0CompressPort -v -count=1 .
```

| Block size | N | avg T/byte | avg ms/block | vs model (283 ms) |
|------------|---|-----------|--------------|-------------------|
| 1 KB | 6 | 2188 | 373 ms | — |
| 2 KB | 6 | 2272 | 775 ms | — |
| 4 KB | 6 | 2557 | 1745 ms | +517% |
| 8 KB | 6 | 2788 | 3806 ms | — |

The model prediction (283 ms / 4 KB at H=512, D=16) underestimated by ~6×.  The per-byte T-state
cost (~2557 T/byte measured vs 414 T/byte modelled) reflects that the model counted only hash
probe + chain steps and missed the substantial overhead of the main loop, bit-writer calls,
Elias-gamma encoding, literal flushing, and memory-variable access (the Z80 port uses
absolute-address LD (nn),rr / LD rr,(nn) for every state variable rather than register-cached
values, each costing 16 T-states per access).

At 4 KB blocks the compressor takes ~1745 ms at 6 MHz — outside the 100 ms interactive budget
but acceptable for a background save operation.  If the design requires faster
on-SAM compression, the path is to cache hot state variables in registers across the inner loop
rather than reading/writing them via absolute addresses.  That optimisation is deferred to
a later item (see q9 / i60c).

The table above records the *unoptimised* port.  Item i67 (next section)
implements the register-caching path and brings the 4 KB figure from
2557 T/byte to 972 T/byte.

### Recommended operating point

**H=512, D=16** (`tools/zx0-greedy.DefaultParams`) is the recommended
starting point for the Z80 port:

- Scratch RAM at 4 KB blocks: **9246 B** (~9 KB), fits comfortably in the
  ~15.5 KB of free space on page 13 alongside the compressor code itself.
- Ratio at 4 KB blocks: **0.3631** (greedy) vs 0.3162 (optimal) — the greedy
  gap is +14.8%, acceptable for a self-hosted on-SAM compressor.
- Estimated compress time at 6 MHz: **283 ms per 4 KB dirty block** — within
  the interactive budget for a save-triggered background operation.
- Chain-depth D=16 captures most of the quality gain vs D=32 (+0 ppt at
  H=512) while halving the work relative to D=32.

Larger blocks (8 KB) improve ratio to 0.3302 but require 17438 B scratch
(17 KB) and 660 ms to recompress — still within a "save" latency budget but
tight for 256 KB SAMs where free scratch pages are limited.  The 4 KB / 8 KB
choice is Pete's call (q9 / i60c design note).

## T-state optimization of the Z80 ZX0 path (i67)

Night-shift optimization of `src/zx0_compress.asm` under a hard invariant:
**the compressor's output bytes never change** — same algorithm, same
(H,D)=(512,16), same greedy parse, only faster code.  The proof at every step
is the byte-identity oracle (`TestZX0CompressPort`, Z80 vs the Go authority)
plus round-trips through the real upstream decoders.  `TestZX0CorpusTotals`
extends the oracle to **every block of the full 298,050 B corpus at both 4 KB
(73 blocks) and 8 KB (37 blocks) blockings** — all 110 blocks byte-identical
and round-trip clean at every kept step.

### Whole-corpus totals: before → after

Measured by `TestZX0CorpusTotals` (instruction-level T-state table,
koron-go/z80, uncontended RAM, 6 MHz).  Reproduce:

```
make zx0-corpus zx0-compress-payload
cd tools/z80-test-harness-go && go test -run TestZX0CorpusTotals -v -count=1 .
```

| Phase | Blocking | Before (T) | After (T) | Before (s) | After (s) | T/byte | Saved |
|-------|----------|-----------:|----------:|-----------:|----------:|-------:|------:|
| Z80 compress | 4 KB | 804,132,274 | 289,731,306 | 134.0 | 48.3 | 2698.0 → 972.1 | **64.0%** |
| Z80 compress | 8 KB | 866,544,662 | 294,513,715 | 144.4 | 49.1 | 2907.4 → 988.1 | **66.0%** |

Per 4 KB dirty block the compress cost drops from ~1.84 s to ~0.66 s at
6 MHz (the i60b-2 numbers above were measured on the 6-block sample; the
whole-corpus average is slightly higher than the sample average).

### Optimization log

Every step assembled, oracle-checked (byte-identity + round-trip), measured
on the full corpus, and committed individually.  T/byte figures are the 4 KB
whole-corpus averages.

| # | Technique | T/byte | Kept |
|---|-----------|--------|------|
| 0 | baseline (i60b-2 port) | 2698.0 | — |
| 1 | drop dead chain-array init (write-before-read proof), LDIR hash fill | 2698.0 → 2604.2 | ✅ |
| 2 | table-driven hash2 (512 B page-aligned `x*31` tables) returning `&hashHead[h]`; boundary check dropped with unobservability proof | 2604.2 → 2322.1 | ✅ |
| 3 | stack-free insert; hash2 preserves DE | 2322.1 → 2215.5 | ✅ |
| 4 | register-resident chain walk: DE=candidate, BC=pos_ptr, IXh=bestLen, IXl=depth counter, IYl=compare budget; SMC per-call constants; screen byte at offset bestLen; ceiling exit at bestLen==maxML; off-range checks dropped (provably dead) | 2215.5 → 1168.0 | ✅ |
| 5 | self-contained insert_tail (count in IXl, inlined hash, parked chain slot); orphaned zxc_insert removed | 1168.0 → 1113.8 | ✅ |
| 6 | bit-writer state in shadow registers (D'E'=out_ptr, H'L'=holder, C'=mask, B'=backtrack); A-only-clobber bit routines; de-push'd gamma loops; LDIR literal flush | 1113.8 → 1000.7 | ✅ |
| 7 | incremental chain slot in insert_tail; SMC main-loop length | 1000.7 → 978.9 | ✅ |
| 8 | fused bit-pair emitter for gamma loops (one EXX round per pair; value bit skips the provably-clear backtrack test) | 978.9 → 972.1 | ✅ |

Nothing was reverted; every step passed the oracle first try except step 4,
which initially computed pos_ptr into BC before calling the BC-clobbering
hash2 (caught immediately by the oracle as a no-matches-found output blowup,
fixed by hashing first).

The flat profiler used to aim each step is
`TestZX0CompressProfile` (per-routine T-state attribution via the pyz80 map
file).  After step 8 the remaining cost is spread across the chain-walk
advance (~11%), the deep compare (~8%), the tail-insert hash (~19%), and the
parse/emit glue — all near their structural floors for this algorithm shape;
further gains would be ≤5% each at growing complexity, so the work stopped
here (diminishing-returns rule).

Cost of the speed: the payload grows from 1182 B to 1792 B (page-aligned
hash tables generated at assemble time), and the routine now requires
interrupts disabled (bit-writer lives in the shadow registers), runs from RAM
(self-modifying immediates), and clobbers IX/IY.  The workspace contract
(`ScratchBytes`, 9246 B at 4 KB blocks) is unchanged.

### Z80 instruction-set notes (incl. undocumented opcodes)

Support matrix established before relying on anything undocumented:

| Feature | pyz80 (assembler) | koron-go/z80 (harness) | Real Z80B / SimCoupé |
|---------|-------------------|------------------------|----------------------|
| IXH/IXL/IYH/IYL halves (LD/ALU/INC/DEC, immediate loads) | ✅ native mnemonics (`single_mapping` includes IXH..IYL); encodings verified byte-exact | ✅ full DD/FD dispatch incl. register-half loads, ALU, INC/DEC, `LD rx,n` | ✅ classic NMOS-universal undocumented ops; SimCoupé implements them |
| SLL (SL1) | ✅ as `SL1` (warns on `SLL`) | ✅ (`SL1 r` / `SL1 (HL)`, CB 30–37) | ✅ NMOS behaviour (shift left, LSB=1) |
| `OUT (C),0` | not needed | ED 71 not dispatched as such | NMOS=0 / CMOS=&FF — avoided |

Decisions taken:

* **Used**: IXh/IXl/IYl as extra 8-bit registers in the match-finder hot
  loops (bestLen, chain counter, compare budget).  Cost model: prefix+4 T per
  access (8 T for LD/ALU/INC/DEC on a half; 11 T for `LD IXL,n`).  The
  harness T-state table gained the DD/FD 26/2E = 11 T immediate-load entries;
  all other used forms were already covered by the default-8 prefix case.
* **Not used**: SLL (no profitable site — `srl c`/`add a,a` cover the bit
  paths), `OUT (C),0` (CMOS-divergent, and the compressor does no I/O).
* Self-modifying code (constants patched into immediates at entry, per-call
  walk parameters) is used throughout — the payload runs from RAM on SAM, so
  this is legal; it is noted in the header as a no-ROM/no-reentrancy
  constraint.
* EXX / EX AF,AF' shadow banking holds the bit-writer state; on real
  hardware this requires DI (or an ISR that preserves shadow registers),
  noted in the header.  The koron-go harness runs interrupt-free.

### Decoder variants (i67)

The upstream ZX0 repo (pinned commit `ecde3a2`) ships two more
forward-direction decoders beyond standard/turbo; both are now vendored
(`reference/zx0/z80/`), ported to pyz80 (syntax-only), and measured.  The
remaining upstream variants are backward-direction (`*_back.asm`) ports and
the obsolete `OLD_V1` format — not applicable to this pipeline.

Whole-corpus decode totals over the **greedy-compressed** streams (the bytes
the editor will actually decode), 6 MHz:

| Decoder | Size | 4 KB blocking T | T/byte | s | 8 KB T/byte |
|---------|-----:|----------------:|-------:|--:|------------:|
| dzx0_standard | 68 B | 18,966,114 | 63.6 | 3.16 | 65.0 |
| dzx0_turbo | 126 B | 15,170,262 | 50.9 | 2.53 | 51.7 |
| dzx0_fast (spke) | 187 B | 14,539,971 | 48.8 | 2.42 | 49.8 |
| dzx0_mega | 673 B | 13,876,751 | 46.6 | 2.31 | 47.5 |

On the optimal-parse `.zx0` bench blocks (`TestZX0DecodeBench`), mega
measures 50.6 / 49.8 / 49.0 / 47.9 T/byte at 1/2/4/8 KB — 26–27% faster than
standard, matching upstream's "28% faster" claim within noise.  All four
decoders are byte-exact on all 110 corpus blocks and all 24 bench blocks.

**Read of the data**: decode was never the bottleneck (a 4 KB block decodes
in 9–13 ms with any variant — comfortably interactive), so the decoder choice
is a size/speed taste call: turbo at 126 B captures most of the win; mega
buys a further ~8% for 547 B more.  The headline i67 win is on the compress
side, where the 4 KB-block save cost drops from ~1.84 s to ~0.66 s.
