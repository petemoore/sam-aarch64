# Comment storage — compressed-resident design (i60c)

**Status:** approved (Pete, 2026-06-11; merged in PR #173). · **Item:** i60 (c) —
the design third of the on-SAM ZX0 work; implementation companion is **i68**.

This is the living spec for how the editor holds the comment corpus resident:
immutable ZX0-compressed blocks + an uncompressed dirty overlay in the i41
block-list, with a streaming O(dirty) save. All numbers are measured; the
source for every figure is `docs/notes/comment-compression-research.md`
("the research note") unless cited otherwise.

---

## 1. Decisions already made (cited, not re-argued)

1. **q9 — the 256/512 KB stance** (Pete, 2026-06-10; `docs/notes/question-registry.md` q9):
   the tool targets both machine sizes *in principle*, but the full unstripped
   release `.tbn` is **allowed to exceed a 256 KB SAM** — no over-optimising
   compression (the kernel grows over time anyway). A 256 KB SAM must still be
   able to *assemble*: a save-without-comments option (**i65**) lets a 512 KB
   user hand a stripped disk to a 256 KB user. Trinity page-persistence is
   deferred (**i66**).
2. **Option 2 — the compressor runs ON the SAM** (Pete, 2026-06-10; item-registry
   i60): a greedy ZX0-bitstream compressor on the SAM, primary rationale
   **self-hostedness** — the development loop never leaves the SAM; a
   host-recompression step would reintroduce a host dependency. Also:
   compression only matters at save / memory-pressure time, and the assembler
   is unaffected (it never reads comments).
3. **The i41 block-list is the editor's document model** (Pete, 2026-06-08;
   `docs/specs/editor-edit-model-design.md` §7): ½-page (8 KB) blocks,
   intra-block gap buffer, records = the i48 in-memory symbolic IR, u24
   no-reuse record-ids. The dirty overlay in this design lives there.
4. **Measured inputs** (research note, all on the real corpus — 7,012 sidecar
   rows, 291,038 body bytes, 298,050 B with NL separators; 73 blocks at 4 KB
   blocking, 37 at 8 KB):
   - Greedy ratio (H=512, D=16, the recommended operating point): **0.4585 /
     0.4057 / 0.3631 / 0.3302** at 1 / 2 / 4 / 8 KB blocks (optimal parse:
     0.4059 / 0.3552 / 0.3162 / 0.2847).
   - Compress, i67-optimized Z80 port: **972.1 T/byte** at 4 KB blocking →
     **~0.66 s per 4 KB dirty block** at 6 MHz (8 KB: 988.1 T/byte → ~1.35 s);
     whole-corpus 48.3 s / 49.1 s. Payload **1,792 B**; scratch workspace
     **9,246 B** at 4 KB blocks (`ScratchBytes` = H×2 + blkLen×2 + 30; 17,438 B
     at 8 KB).
   - Decode, measured per block (turbo): **36.4 ms / 4 KB**, **70.8 ms / 8 KB**.
     Variants on the greedy streams: standard 68 B / 63.6 T/B, turbo 126 B /
     50.9, fast (spke) 187 B / 48.8, mega 673 B / 46.6.
   - Byte-identity: the Z80 compressor matches the Go authority
     (`tools/zx0-greedy`) on **all 110 whole-corpus blocks** at both blockings,
     and all four decoders round-trip them byte-exact.
5. **i67 is complete** — the hot paths are at their structural floors (§9);
   this design takes the numbers as final.

---

## 2. The block architecture

### 2.1 Shape

The comment corpus is held as a sequence of **immutable compressed blocks**
in PC order, plus an **uncompressed dirty overlay** for whatever is being
edited, plus a small **resident block directory**.

- **Block** = a run of whole comment-sidecar rows (i39b-2 sidecar: anchor
  delta-varint + placement byte + body), packed to ≤ 4,096 raw bytes (rows
  never straddle blocks; the longest row is 1,691 B, so every row fits). The
  row metadata compresses *with* the bodies — one ZX0 stream per block. Each
  block's first row anchors against a **per-block base PC held in the
  directory**, so the stream itself is position-independent.
- **Directory** (resident, section-D scratch): per block
  `{base_pc u32, location far-ptr 3 B, comp_len u16, raw_len u16, flags u8}`
  ≈ 12 B × 73 blocks ≈ **0.9 KB** — negligible. `flags` carries
  **compressed?** and **dirty/superseded**.
- **Store**: compressed blocks packed back-to-back in dedicated store pages.
  At greedy 4 KB ratios the full corpus is **108,222 B ≈ 6.6 pages → 7 store
  pages**. Superseded blocks leave holes; compaction is a walk-and-repack
  `LDIR` (no recompression — ZX0 streams are self-contained), run at save or
  under memory pressure.

### 2.2 Dirty overlay (the i41 dovetail)

Instruction/directive/label records load into the i41 block-list as usual;
comment rows do **not** — they stay compressed. The editor interacts with
them in two tiers:

- **Read (clean)**: rendering a screenful decodes the covering block(s) into a
  small **decoded-block cache** (clean, read-only, instantly discardable).
  Rows render interleaved by PC anchor. ~36 ms per cold block (§6), cached
  thereafter.
- **Write (dirty)**: editing any comment in block B — or inserting/deleting an
  *instruction inside B's PC range* (which changes an intra-block anchor
  delta) — **materialises B**: decode B, insert its rows as records in the
  i41 block-list (record-anchored per i41 §3, so subsequent edits never
  reindex), mark B superseded in the directory. Edits before/after B's range
  only bump later blocks' `base_pc` in the directory — clean blocks stay
  clean. Dirtying therefore tracks **edit locality**: working in one routine
  dirties the one or two blocks covering it.

### 2.3 Streaming save — O(dirty), never O(corpus)

Save walks the directory in PC order:

- **clean + compressed** → copy the compressed bytes straight through to the
  output file, `compressed?` flag set. No decode, no recompress.
- **dirty** → serialize the materialised rows from the block-list, greedy-
  compress (**0.66 s per 4 KB block**), write with the flag set, and install
  the new compressed bytes in the store (save doubles as eviction: afterwards
  everything is clean and the materialised records can be dropped).
- **carve-out**: a dirty block may instead be written **raw with the flag
  clear** (e.g. a hurried save with many dirty blocks). It compresses lazily
  at the next save or pressure event.

Compression work is proportional to *dirty blocks only*; recompressing the
whole corpus (48.3 s) never happens on the save path. The disk write itself
is O(file) and disk-bound, as for any save.

### 2.4 Greedy everywhere — and the `.tbn`-shrinks-or-holds carve-out

**The host emits the same greedy bitstream** (`tools/zx0-greedy`, the Go
authority), not the upstream optimal parse. Rationale: host and SAM then
produce **byte-identical blocks** (the foundation of the i68 baked-bytes
self-tests, §7), there is no host-side `zx0`-binary dependency
(self-hostedness, decision 2), and the cost is bounded: optimal parse would
save 108,222 − 94,243 = **13,979 B ≈ 0.85 page** at 4 KB blocking — exactly
the over-optimising q9 declines.

The i39 invariant (`docs/ROADMAP.md`: binary-identity + round-trip +
`.tbn`-shrinks-or-holds) gains one carve-out: a **SAM-emitted** `.tbn` may
carry raw flag-clear dirty blocks (§2.3) and so may grow versus its
predecessor. A **host-emitted** `.tbn` always compresses every block, so host
round-trips still shrink-or-hold — the carve-out is bounded to on-SAM saves
between host round-trips, and the format flip itself shrinks the file
(editor-region comment payload 291 KB → ~108 KB).

---

## 3. Block size: 4 KB (recommendation)

The trade table, all measured (research note; store pages = corpus ×
ratio ÷ 16,384):

| | 2 KB | **4 KB** | 8 KB |
|---|---|---|---|
| greedy ratio | 0.4057 | **0.3631** | 0.3302 |
| store bytes / pages | 120,919 B / 7.4 → 8 | **108,222 B / 6.6 → 7** | 98,416 B / 6.0 → 7¹ |
| decode latency (turbo) | 18.5 ms | **36 ms** | 71 ms |
| compress per dirty block | ~0.33 s² | **0.66 s** | 1.35 s |
| scratch workspace | 5,150 B | **9,246 B** | 17,438 B |
| fits page-13 co-residency (§5) | yes | **yes** | **no** (> 15,436 B free) |

¹ 98,416 B = 6 pages + 112 B — 7 pages unless the store packs perfectly.
² Extrapolated at the 4 KB T/byte rate; i67 measured only the 4 KB and 8 KB
blockings post-optimization.

**Call: 4 KB blocks, H=512 D=16** (the research note's recommended operating
point). The argument against 8 KB:

1. **The ratio win mostly evaporates under page quantisation** — 9,806 B ≈
   0.6 page on paper, and both blockings land on 7 store pages in practice.
2. **The 17,438 B workspace breaks co-residency**: it exceeds page 13's
   15,436 B of free space *on its own*, forcing a dedicated work page — which
   costs the very page the better ratio was meant to save.
3. **Per-dirty-block compress doubles** (0.66 s → 1.35 s): the watermark
   stall (§4) and the per-block save cost stay comfortably sub-second at
   4 KB and do not at 8 KB.
4. Decode 36 ms vs 71 ms — both interactive, but 36 ms leaves headroom for
   staging copies (§5) while staying well under the ~100 ms feel threshold.

2 KB buys latency we don't need for a whole extra store page. 4 KB it is.

---

## 4. Watermark math (the dirty-data bound)

**Requirement (Pete, i60 registry row):** the editor must never accumulate
more dirty data than free RAM allows recompressing.

The machinery that recompression *needs* is **pre-reserved**, not allocated
at compress time (§5): the 9,246 B workspace and the compressor live in page
13's free space, and the src/dst staging block (one 4 KB block in, one
4 KB + headroom out — 8,448 B, §5) is reserved on page 14. The decoded-block
cache needs no reserve — it holds *clean* copies and is discarded instantly
under pressure (eviction tier 0). So recompressing one block allocates
**zero** pages; the only allocation hazard left is **store growth** (a
recompressed block appends to the store's tail page; when it fills, one new
page is claimed) plus the editor's own worst-case allocation (an i41 block
split claims one page).

**Reserve: R = 2 pages** (1 store-tail growth + 1 block-split).

Page accounting (capacity table, research note; + page 14 reserved by §5):
baseline resident = 11 + 1 = **12 pages**; greedy store = **7 pages** (§3).

| | 512 KB (32 pages) | 256 KB stripped, i65 (16 pages) | 256 KB `-thin-comments=20` |
|---|---|---|---|
| free at clean state | 32 − 12 − 7 = **13** | 16 − 12 − 0 = **4** | 16 − 12 − 1 = **3** |
| dirty ceiling D_max = free − R | **11 pages** | **2 pages** | 1 page |
| ≈ dirty 4 KB blocks (×3.6/page²) | **~40 blocks (~160 KB raw text)** | ~7 blocks (~28 KB) | ~3 blocks |

² 16,384 ÷ (4,096 × ~1.1 i41 block-list overhead) ≈ 3.6 materialised blocks
per overlay page.

**Triggers:**

- **Soft watermark — free ≤ R + 1 (3 pages):** at idle, compress the
  **oldest dirty block** (LRU by last edit) in the background, 0.66 s each.
- **Hard watermark — an allocation would drop free below R (2 pages):**
  compress oldest-dirty synchronously until the allocation can proceed.
  Worst-case stall to liberate one whole overlay page = all ~4 blocks
  resident in it ≈ **2.7 s**; the typical stall is one block, **0.66 s**.
  The soft trigger exists precisely so the hard one almost never fires.

Reading the table: on a 512 KB SAM more than half the entire comment corpus
can be simultaneously dirty before the editor is even *obliged* to compress —
the watermark is a safety fence, not a working constraint. On 256 KB the
fence is real but the workload is too (q9): stripped sources author *new*
comments into the overlay, and ~28 KB of un-evicted new comment text is an
ample buffer between background compressions.

---

## 5. Page placement

Ground truth audited 2026-06-11 (`build/` artifacts on `main`; layout per
`src/assembler.asm` header / `docs/notes/memory-layout.md`):

| Page | Occupant today | Free |
|---|---|---|
| 13 | `sysreg_data.bin` **948 B** (org `&8000`, paged_call target) | **15,436 B** |
| 14 | no production payload (BUILD_TESTS 3 B `paged_call` stub at boot only; earmarked "explanation prose" in the memory-layout brainstorm) | 16,384 B |
| 15 | `disasm.bin` **10,384 B** (org `&8000`, paged_call target) | 6,000 B |
| section C | test variant `code_end` `&BF73` — **141 B** headroom (prod `&B80E`, 2,034 B) | — |

**Placement (illustrative addresses; i68 pins them):** everything on
**page 13**, invoked via `paged_call` (`HMPR=13` → page 13 at section C,
page 14 at section D), exactly the established sysreg/disasm idiom:

| What | Where (section-C view) | Size |
|---|---|---|
| sysreg_data (existing) | `&8000–&83B3` | 948 B |
| **zx0 compressor** | `&8400–&8AFF` (256 B-aligned³) | 1,792 B |
| **zx0 decoder (turbo, §6)** | `&8B00–&8B7D` | 126 B |
| **workspace** | `&8B80–&AF9D` | 9,246 B |
| spare (sysreg growth etc.) | `&AF9E–&BFFF` | 4,194 B |
| **staging: src 4 KB + dst 4 KB + headroom⁴** | page 14 = section D, `&C000–&E0FF` | 8,448 B |

³ The i67 payload contains assemble-time page-aligned hash tables; its load
address must preserve 256 B alignment.
⁴ dst headroom for an incompressible block; i68 pins the exact worst-case
bound from the Go authority (a fully-literal 4,096 B block costs a few dozen
bytes of bitstream overhead — 256 B headroom is generous).

Why this and not the alternatives:

- **Not section C**: 141 B of headroom in the test variant — nothing fits.
- **Not page 15**: 6,000 B free can't hold the workspace, and the
  disassembler grows with ISA coverage; page 13's occupant (948 B of slowly-
  growing sysname tables) is the safer landlord.
- **Page 14 lower half for staging, not a fresh page**: under `HMPR=13` page
  14 is *already mapped* at section D — staging there needs no extra paging
  mechanics, and src/dst cannot live on page 13 (code + workspace + dst
  would leave < 200 B). Page 14 has no production occupant; its planned
  "explanation prose" role keeps the upper ~8 KB. **This ½ page is the only
  new RAM cost of the whole design** and is counted in §4's baseline.
- The caller copies src in / results out through the staging area using the
  established bracket idioms (the reader's LMPR bracket / a directory-driven
  store copy); the copy overhead at 21 T/byte is ~14 ms per 4 KB — noise
  against the 0.66 s compress, and ~20 ms against a 36 ms decode (total cold
  read ≈ 55 ms, still interactive).

**Constraints respected** (compressor header, `src/zx0_compress.asm`): the
code is **self-modifying** (must run from RAM — page 13 is RAM; it could
never be ROM-resident), **non-reentrant**, requires **interrupts disabled**
(bit-writer state in the shadow registers), and clobbers IX/IY and the full
shadow set. `paged_call` is itself single-slot non-reentrant, so the pairing
adds no new constraint; the DI requirement spans the 0.66 s compress call —
acceptable for a save/eviction operation (the SAM does the same around disk
hooks).

Under `BUILD_TESTS`, page 13 is time-multiplexed with `test_mem.bin` at boot
(existing pattern, `src/trampoline.asm`); the zx0 payload loads with the
sysreg payload after the test_mem suite completes — i68 sequencing detail.

---

## 6. Decoder variant: turbo (126 B)

Measured on the greedy streams the editor will actually decode (research
note, i67 §"Decoder variants"):

| Variant | Size | T/byte (4 KB blocking) | per 4 KB block |
|---|---|---|---|
| dzx0_standard | 68 B | 63.6 | ~43 ms |
| **dzx0_turbo** | **126 B** | **50.9** | **~35 ms** |
| dzx0_fast (spke) | 187 B | 48.8 | ~33 ms |
| dzx0_mega | 673 B | 46.6 | ~32 ms |

**Call: turbo.** Standard→turbo buys −20% time for +58 B — the knee of the
curve. Beyond it the returns collapse: fast adds −4% for +61 B, mega −8%
for +547 B. Decode was never the bottleneck (any variant is comfortably
interactive at 4 KB), so the 126 B variant that captures most of the win is
the right product wiring; 126 B is noise in page 13's budget (§5). All four
variants are proven byte-exact on all 110 corpus blocks, so a later swap is
riskless if profiling ever demands one.

---

## 7. The i68 verification plan

i68 is the implementation companion; its gate is the full SimCoupé CI matrix
green with the new self-tests in the test variant.

1. **BUILD_TESTS boot self-tests, expected bytes baked from the Go
   authority.** At build time, `tools/zx0-greedy` compresses a fixed corpus
   block; the raw block, the expected compressed bytes, and the expected
   length are baked into the test payload. At boot the SAM (a) runs the
   page-13 compressor over the raw block and **byte-compares** against the
   baked expected output, and (b) runs the turbo decoder over the baked
   compressed block and byte-compares against the raw original. Greedy is
   deterministic and the Z80 port is byte-identical to Go (§1.4), so these
   are *exact* tests — any drift in either direction is a hard FAIL, the
   same discipline as the encoder self-tests.
2. **CI's SimCoupé matrix becomes the standing execution proof.** SimCoupé's
   CPU core is **kosarev/z80** (fetched in its CMakeLists), so every CI run
   executes the compressor's undocumented IXH/IXL register-half opcodes on a
   third independent implementation (beyond pyz80's encodings and
   koron-go/z80's harness execution — the support matrix in the research
   note), continuously.
3. **Decoder promotion testdata → src.** `dzx0_turbo.asm` currently lives in
   `tools/z80-test-harness-go/testdata/` (vendored from `reference/zx0/`,
   pinned `ecde3a2`); it becomes product code in `src/`, with the harness
   re-pointed at the product copy.
4. **Loader + disk wiring** per the pre-merge checklist: the zx0 payload
   joins the page-13 load (extend `load_page13_payload` or a combined
   page-13 binary with a stable jump table), boot call in `assembler.asm`,
   filename in `Makefile` / `tools/build-disk.sh`.
5. **comment-bench vendored-corpus switch.** `release-unstripped-tbn`
   currently defaults to `SPECTRUM4_SRC ?= ~/git/spectrum4/...`; re-point it
   at the vendored `tests/release/release.s` so the corpus targets
   (`comment-bench`, `zx0-blocks`, `zx0-corpus`) need no external checkout.

---

## 8. ARCHITECTURE §5 — approved opening wording

The actual edit lands with i68; this is the agreed paragraph (Pete's
framing, 2026-06-11), to open the section before the existing section map:

> The right mental model for SAM memory is BASIC's, not the Z80's: the
> machine is **one flat linear address space over all physical RAM** — 256
> or 512 KB in 16 KB pages, addressed page:offset, the space SAM BASIC's
> own POKE/PEEK address directly — and that flat space is the canonical
> home of everything: code, buffers, tables, the document. The CPU's 64 KB
> address space is not the memory; it is a **4-section window (A–D, 16 KB
> each) that slides over the flat space**, LMPR selecting the page pair
> under A/B and HMPR the pair under C/D. **Paging is the linker**: a
> routine reaches code or data elsewhere in the flat space by mapping its
> page into a section for the duration of the access — `paged_call`, the
> reader bracket, and the emit bracket are this model's link-time fixups,
> binding a window-visible address to a flat-space location at run time.

---

## 9. Future work — explicitly out of scope

- **Further hot-path optimization sweeps.** The i67 log stopped on the
  diminishing-returns rule: after step 8 the compressor's remaining cost is
  spread across routines each worth ≤5% at growing complexity — 972 T/byte
  is its structural floor for this algorithm shape. The turbo decoder at
  ~51 T/byte is similarly at the format's practical floor (mega's last 8%
  costs 547 B). No more sweeps unless the workload changes.
- **Trinity / B-DOS spill (i66 / i62).** Deferred per q9. When a spill
  backend is wanted, the store's block directory is the natural unit to
  spill, and `docs/notes/bdos-version-landscape.md` settles the portability
  story (SAMDOS hooks → floppy / B-DOS records; record selection is the one
  backend-conditional step).
- **The full editor save machinery** (i41 block-list implementation, the
  i48c Z80 text→overlay encoder, the i65 stripped-save option) — editor
  phase. This design fixes the *contract* those build against: the block
  format, the directory, the watermark, and the page placement.
