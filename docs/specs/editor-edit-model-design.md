# On-SAM editor edit-buffer / document-model — design exploration (item i41)

**Date:** 2026-06-08 · **Item:** i41 · **Type:** exploratory design (OPTIONS + a
recommendation, not a frozen spec)

**Grounding reads (the real constraints, cited inline):**
`docs/notes/memory-layout.md`, `docs/notes/sam-paging.md`, `src/reader.asm`,
`src/assembler.asm` (header map), `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m7-status.md` ("IN/OUT
paged-buffer ceiling" row + items i2/i40), and the v2 serialize target:
`docs/specs/compact-tbn-nextgen-design.md` (§7 decisions) +
`docs/specs/tbn-binary-format-reference.md`. Editor interaction model:
`docs/ROADMAP.md` "Editor vision (Phase 2)".

This doc designs the **in-RAM document model the editor mutates while you type**.
It is deliberately *not* the `.tbn` (that is the serialized assembly form). The
relationship is settled in §1.3 below: `.tbn` is the cold/serialized form; the
document model is the hot/interactive form; they meet only at load and
save/assemble.

---

## 1. Problem framing

### 1.1 What the editor must hold

The SAM is the *whole* development platform (project remit;
`https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m7-status.md` i2 reframing). A codebase is **grown dynamically on-device over a session** — the
edit buffer expands throughout, and the dominant use is *not* "load
`release.tbn`, assemble once." A serious session plausibly holds **400 KB+ of
aarch64 source text** resident (the flattened `release.s` is ~408 KB;
`https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m7-status.md` line 333). The SAM has 512 KB of RAM in 32 physical 16 KB pages
(`sam-paging.md` §1); after reserving DOS (1 page, `DOSFLG`) and screen (2 pages)
and the assembler/editor code, roughly **~25 pages (~400 KB) are available** for
a document — i.e. a large document fills essentially all free RAM. So the model
must (a) live across many paged 16 KB windows, and (b) be frugal with per-line
overhead, because overhead competes directly with how much source fits.

### 1.2 The hard latency bound

**An edit (insert/delete a line or instruction at the cursor) must feel instant.
Target: ≤ ~1 s worst case; ideally ≲ 100 ms typical.** This is the binding
constraint, and it is what kills the naive design.

The other operations the model must support, drawn from the Editor vision
(`ROADMAP.md` "Editor vision"): **move cursor ±1 line** (arrow keys), **move
cursor far** (jump to a label / line N / search hit), **render a screenful**
(~24 lines around the cursor for the line-editor display), **insert/delete at
cursor**, **serialize to `.tbn`** (save/assemble), **load from `.tbn`** (open),
plus the passive features (instruction-explanation panel, register simulator)
that operate on the *current line* and so only need cheap access to the
cursor-neighbourhood — they do not stress the data structure.

### 1.3 Why a contiguous-shift edit buffer fails — the numbers

The naive design holds the source as one contiguous byte array (the text, or the
`.tbn` record stream) and, on insert at offset *p*, `LDIR`-shifts everything after
*p* up by the inserted length.

Z80 `LDIR` is **21 T-states/byte** for the repeated iterations. The SAM clocks
the Z80 at 6 MHz nominal, but ASIC memory contention on the lower pages drops the
*effective* rate; the working figure used across this project (prompt + observed)
is **~3–6 µs/byte** for an `LDIR`-class block move. Take the *optimistic* end,
3.5 µs/byte (21 T ÷ 6 MHz, zero contention):

| document size shifted | bytes moved (worst case = insert at top) | time @ 3.5 µs/B | time @ 6 µs/B |
|---|---:|---:|---:|
| 64 KB | 65,536 | 0.23 s | 0.39 s |
| 200 KB | 204,800 | 0.72 s | 1.23 s |
| **400 KB** | **409,600** | **1.43 s** | **2.46 s** |

So a worst-case insert near the top of a 400 KB document is **1.4–2.5 s of pure
`LDIR`** — already over the 1 s bound *before* paging overhead.

And paging makes it strictly worse. `LDIR` cannot cross a 16 KB Z80 section
boundary transparently: a 400 KB array spans **~25 physical pages**, and the
source and destination of the shift move through every one of them. The reader's
own copy loop shows the cost — it must test `H ≥ &40` every byte and, on a page
cross, `sub &40 / in a,(250) / inc a / out (250),a` to bump LMPR
(`reader.asm:224-238`). A contiguous shift across the whole document pays a
page-remap (`OUT (250)` + section re-derivation) at every 16 KB boundary in
*both* the read and write streams, plus per-byte boundary tests. Realistically a
full-document shift is **~2–4 s** at 400 KB. **Verdict: contiguous-shift does not
meet the bound at scale. Confirmed.**

The established principle (`compact-tbn-nextgen-design.md` §1, and the prompt)
therefore holds: **`.tbn` is the serialized storage + assembly form** (contiguous,
offset/PC-based — correct for streaming to the assembler and for compact storage);
**the editor holds the source in a separate, insertion-friendly document model**
and serializes to `.tbn` only on save/assemble, recomputing offsets/PCs in one
O(n) pass at serialize time, never per keystroke. The rest of this doc designs
that model. **We do challenge one sub-claim** (§6): the document model should
hold *source-shaped records*, not the compact `.tbn` records — the compact form's
instruction-overlay packing is a serialize-time transform, not an edit-time one.

### 1.4 The unit of the document — line vs record

aarch64 source is overwhelmingly **one statement per line**. The editor is
line-oriented (`ROADMAP.md`: "cursor lands on a line", BBC BASIC / Tasword
idiom). So the document's natural atom is the **source line** = one logical
record (instruction / directive / label / comment / blank). This matters because
it sets *N*: a 400 KB document is **~10,000–16,000 lines** (release.s averages
~25–40 chars/line). The per-line overhead of any structure is multiplied by *N* ≈
**~13,000**, which is the number to keep in mind throughout.

We will model the document as a sequence of **lines**, each line being a short
byte string (the source text of that line, *without* a trailing newline — the
line break is implied by the record boundary). Whether lines are stored as raw
source text or as a lightly-parsed token form is a secondary question (§6.4);
the structural analysis below is the same either way.

---

## 2. The options, costed in paged-SAM terms

For each option: data layout, how it lives across 16 KB windows (a structure
> 64 KB needs **far pointers** = `(page:5b, offset:14b)`, ~3 bytes; traversal
across pages costs an `OUT (250)`/`OUT (251)` remap), and the per-operation cost.

Cost legend: **T** = Z80 T-states (÷ ~6 MHz ≈ 0.17 µs each, or treat as ÷3.5 MHz
≈ 0.29 µs under contention); **swap** = one LMPR/HMPR page remap (a few `OUT`s +
HL re-derivation, ~30–60 T amortised but it also evicts whatever was in that
window). N ≈ 13,000 lines, document ≈ 400 KB. "Far jump" = cursor moves to an
arbitrary distant line (goto-label, search hit).

### 2.1 Option 1 — Gap buffer

**Layout.** One large byte array holding all line text contiguously, with a
**gap** (unused bytes) at the cursor position. Insert at cursor = write into the
gap (no movement). Delete = grow the gap. Moving the cursor moves the gap: copy
the bytes you skip over from one side of the gap to the other.

```
[ text before cursor ][      gap      ][ text after cursor ]
                       ^cursor
```

**Living in paged RAM.** The array is ~400 KB = ~25 pages. The gap is a window
within it. The killer: **moving the gap is an `LDIR` of the skipped bytes**, and a
*far* cursor jump moves the gap across the whole document — exactly the
contiguous-shift cost of §1.3, and it crosses every page boundary between old and
new cursor.

| operation | cost | notes |
|---|---|---|
| insert char/line at cursor | **O(1)** — write into gap | ~10–40 T for a line; the win case |
| delete at cursor | **O(1)** — extend gap | trivial |
| move cursor ±1 line | O(line length) — shift ~30 B across gap | ~600 T (~0.1 ms) + maybe 1 swap if the gap straddles a page; fine |
| **move cursor far (goto line N)** | **O(distance)** — `LDIR` the gap across the jump | **the failure mode: up to 400 KB → 1.4–2.5 s + ~25 swaps** |
| render screenful | O(24 lines) once you can find them — but a gap buffer has **no line index**; finding line N from the top is O(file) | needs an auxiliary line table (→ §2.4) |
| serialize to `.tbn` | O(n) single linear pass; the gap is skipped | clean |
| load from `.tbn` | O(n) — decode records into the array, gap at end | clean |

**Memory overhead.** Near-zero structural overhead (just the two gap pointers) —
this is the gap buffer's great virtue. ~400 KB of text = ~400 KB resident, plus a
chosen gap size (say 1–4 KB). **Lowest overhead of any option.**

**Verdict.** O(1) at the cursor and minimal memory, but **the far-jump shift is
unbounded and crosses all pages** — and "jump to label", "search", "go to line N"
are *core* editor operations (`ROADMAP.md`: jump-to-label, replay-from-label,
did-you-mean). A 400 KB document makes a single goto-label cost up to 2.5 s. The
gap buffer assumes a flat address space and rare far jumps; on a paged SAM with
frequent label navigation it degrades exactly where it must not. **Rejected as a
standalone model at 400 KB.** (It survives as the *intra-block* structure inside
Option 4.)

### 2.2 Option 2 — Piece table / piece tree

**Layout.** The text is never moved. Two append-only byte buffers: **original**
(the loaded document, immutable) and **add** (everything you type, append-only).
The document is a **sequence of "pieces"**, each a descriptor
`{which buffer, start offset, length}`. An edit splits/relinks pieces and appends
to the add buffer; **no text is ever shifted**.

```
pieces:  P0{orig,  0..120}  P1{add, 0..15}  P2{orig, 120..400000}  ...
```

**Living in paged RAM.** Two issues compound on the SAM:

1. **The two text buffers are still ~400 KB total and span ~25 pages.** Reading a
   piece's text (to render) means a far access into original-or-add at
   `(page,offset)` — a page swap if it's not in the current window. Rendering 24
   consecutive screen lines that happen to come from interleaved pieces in
   *different* pages can cost **several swaps per screenful**.
2. **The piece list fragments under heavy editing.** Each insert at a fresh
   location splits one piece into three (before / new / after). A heavily-edited
   400 KB document — the *exact* "grown over a long session" workload — can reach
   **thousands of pieces**. At, say, one piece per ~3 edits and a session of tens
   of thousands of edits, the piece list is itself **tens of KB** and must *also*
   be paged. A flat piece list makes "find the piece containing line N" O(pieces);
   a balanced **piece tree** (red-black / order-statistic on cumulative line
   count) restores O(log pieces) lookup but the tree nodes (left/right/parent =
   three far pointers + line-count + piece descriptor ≈ 16–20 B) live across pages
   too, so each tree descent step risks a swap.

| operation | cost | notes |
|---|---|---|
| insert line at cursor | **O(1) text** (append to add buffer) + **O(1) relink** (with a cached cursor piece); **O(log P)** to locate if cold | no text movement — the headline virtue |
| delete | **O(1)** — split/shrink piece descriptors, text untouched | trivial |
| move cursor ±1 line | O(1) within a piece; cross-piece = follow link | cheap; maybe 1 swap if the next piece's text is in another page |
| **move cursor far (goto line N)** | **O(log P)** tree descent (no text moved!) + **O(1)** to land | **the win vs gap buffer: no shift at all.** A few swaps for the tree descent across pages |
| render screenful | O(24 lines), but text spans pieces in scattered pages → **up to ~5–10 swaps/screenful worst case** | the real cost; bounded but paging-sensitive |
| serialize to `.tbn` | O(n) walk pieces in order, copy text, re-derive offsets/PCs | clean; one linear pass |
| load from `.tbn` | O(n) decode into original buffer; one piece spanning it | clean — and **load is the cheapest of all options** (one piece) |

**Memory overhead.** Text ≈ 400 KB (unchanged — original + add together hold no
more than the live text plus *dead* bytes from deletions; a long session
accumulates dead add-buffer bytes that are only reclaimed by a save/reload
compaction). Plus the piece structures: a heavily-fragmented document at, say,
4,000 pieces × ~16 B (tree node) = **~64 KB of piece metadata** — *4 pages purely
for the index*. That is the hidden cost: the structure that avoids moving text
pays for it in index size under heavy fragmentation.

**Undo/redo — the piece table's famous superpower.** Because text is
append-only and never overwritten, **undo is just restoring the previous piece
list** (or a delta of relink operations). The bytes an edit "deleted" are still
in the original/add buffer; undo re-links to them. This makes unlimited undo
nearly free in *text* terms — you store sequences of piece-list edits, not text
snapshots. This is a genuine advantage for the editor vision's "replay-on-edit /
blast-radius" feature, which wants cheap edit history.

**Verdict.** Eliminates text movement entirely (solves the far-jump that kills the
gap buffer) and gives near-free undo. **But** under the project's *defining*
workload — a 400 KB document grown over a long heavily-edited session — it
fragments into thousands of pieces, the index balloons to multiple pages, and
rendering pays scattered cross-page swaps. **Strong candidate, with fragmentation
as the chief risk** (mitigated by periodic compaction on save, which the i40
disk-swap flow gives us for free — §7).

### 2.3 Option 3 — Line linked-list (one node per source line)

**Layout.** One node per source line:

```
LineNode:
  next     : far ptr (page:5b, offset)   ; ~3 B (or 2 B if intra-page list — see below)
  prev     : far ptr                      ; ~3 B (optional; doubly-linked for ±1 nav)
  len      : u8/u16                        ; line text length
  text     : len bytes (inline) OR far ptr to text
```

Insert = allocate a node, splice it in (relink two pointers). **No shift, ever.**

**Living in paged RAM — this is where it gets SAM-specific.** A naive
"one node anywhere, far `next` pointer" list is a *disaster* on a paged machine:
walking the list to render 24 lines could touch 24 different pages = **24 swaps
per screenful**, and the nodes scatter across the free-page pool with no locality.
Every `next`-follow is a potential `OUT (251)` + HL re-derivation. The pointer
overhead alone is also brutal: a doubly-linked far-pointer node is **~6 B of
pointer + 1–2 B len = ~8 B overhead × 13,000 lines = ~104 KB of pure overhead** —
a *quarter* of the RAM budget spent on links, before any text. That is
unacceptable on a machine where overhead trades directly against document size.

The list only becomes viable with **locality discipline**: allocate nodes from a
*page arena* so that consecutive lines tend to live in the same page (intra-page
`next` = a 2-byte offset, no swap), and only cross-page links are full far
pointers. But once you impose "consecutive lines cluster in a page and links
within a page are cheap," **you have reinvented Option 4 (the paged block-list)**
— the block *is* the per-page node cluster. So the pure linked-list is dominated:
either it has poor locality (fails the swap/overhead budget) or it acquires
locality and becomes Option 4.

| operation | cost (naive far-pointer list) | notes |
|---|---|---|
| insert line at cursor | **O(1)** relink (cursor node cached) | the virtue — no shift |
| delete line | **O(1)** relink | trivial |
| move cursor ±1 line | O(1) follow `next`/`prev` | but **possibly 1 swap per step** if neighbour is in another page |
| **move cursor far (goto line N)** | **O(N) walk** without an index — up to 13,000 hops, each a possible swap → **catastrophic** | needs a line-index (→ §2.4) to be O(1)/O(log) |
| render screenful | O(24) hops, **up to 24 swaps** without locality | the locality problem |
| serialize to `.tbn` | O(n) walk in order | clean, but follows far pointers (swaps) |
| load from `.tbn` | O(n) allocate + link a node per line | slowest load: N allocations |

**Memory overhead.** ~8 B/line naive = **~104 KB** at 13,000 lines (a quarter of
budget). Even a singly-linked, intra-arena variant is ~3–4 B/line = ~40–52 KB.
Plus free-list bookkeeping for node alloc/free from the i2 free-page pool, and
**fragmentation/compaction** of freed nodes (deletes leave holes; compaction is
itself an O(n) walk).

**Verdict.** O(1) edits and conceptually the simplest ("it's just a linked
list"), but **the per-line pointer overhead is ruinous at 400 KB** (a quarter of
RAM on links) and the **goto/render operations either walk O(N) or need an
auxiliary index anyway**, and **without locality every traversal step is a page
swap**. The "simplicity" is illusory on a paged machine — the moment you add the
line-index and the locality arena it needs to be usable, it has the complexity of
Option 4 without Option 4's contiguity benefits. **Rejected at 400 KB**, though it
would be perfectly fine for a small (<64 KB, single-page-set) document — see §5
candidate "Simple."

### 2.4 The line-index — a shared building block, not a standalone option

All three options above need, for fast "goto line N" and "render line N..N+23", an
**array of far pointers to line starts** — the *line table* (classic editor
construct). At 13,000 lines × 3 B (far ptr) = **~39 KB = ~3 pages**. With it:

- goto-line-N = O(1): index into the table, get the far pointer, swap to that page.
- render screenful = O(24): read 24 consecutive table entries, follow each.
- The table itself spans ~3 pages, but it is accessed by *index* (computed
  `(page,offset)` arithmetic), so a goto touches at most 1 table page + 1 text page.

**But the line table is itself a contiguous array** — so **inserting a line shifts
all table entries after it** (`LDIR` of 3-byte entries). At 13,000 lines × 3 B =
39 KB, a worst-case insert at the top shifts 39 KB = **~0.14–0.24 s + ~3 swaps**.
That is *within* the 1 s bound (the table is ~10× smaller than the text it
indexes, so its shift is ~10× cheaper than shifting the text), but it is the
dominant cost of an insert in any "gap-buffer/piece-store **+ flat line table**"
design. Two ways to keep it sub-100 ms:

1. **Block the line table too** (array of blocks, each block a small line-pointer
   array) — then an insert shifts only within one block. This is again Option 4's
   shape applied to the index.
2. **Make the index a tree** (order-statistic tree on line count) — O(log N)
   insert, no shift, but tree nodes across pages. This is Option 2's piece-tree.

Either way, **the index must not be a single flat array at 400 KB**, or insert
re-degrades toward the contiguous-shift cost (just 10× smaller). This observation
is what pushes the recommendation toward a *blocked* structure where the index is
implicit in the block layout.

### 2.5 Option 4 — Paged block-list (the SAM-native structure)

**Layout.** Group lines into **blocks**, each block sized to live within (about)
one 16 KB page (or a half-page, for finer granularity). A block holds a run of
consecutive lines as a small **intra-block gap buffer** (or a packed text array
with a tiny in-block line offset table). Blocks are chained in a **block list**
(an array or short linked list of block descriptors). Edits stay *within* a
block; only a block-split (on overflow) or merge (on underflow) touches block
structure, and a split shifts at most one block's worth (~16 KB) — never the whole
document.

```
block list (small, ~25–50 entries, fits in 1 page):
  B0{page=7,  first_line=0,   line_count=380, used=15800B}
  B1{page=8,  first_line=380, line_count=380, used=14900B}
  ...
  B24{page=31, ...}

each block (one physical page):
  [line-count u16][line offsets: u16 × count][ text with intra-block gap ]
```

**Living in paged RAM — this is the natural fit.** A block *is* a page. To touch a
line, swap that one block's page into a window (one `OUT (251)`), then everything
about that line — find it via the in-block offset table, edit it in the in-block
gap — happens **within a single 16 KB window, no further swaps**. The block list
(25–50 descriptors × ~6 B = ~300 B) fits in a corner of section D alongside the
cursor state, always resident, never swapped.

| operation | cost | notes |
|---|---|---|
| insert line at cursor | **O(intra-block shift) = at most ~16 KB**, usually far less (shift only within the cursor's block, from the in-block gap) → **~0.06 s worst, µs typical**; **0 swaps** (cursor block already mapped) | block already in window; only a block-split crosses pages, and a split is one ~8 KB copy + a block-list insert (one extra page claimed from the i2 pool) |
| delete line | **O(intra-block)** — close the in-block gap; block-merge only on underflow | same window, 0 swaps typical |
| move cursor ±1 line | O(1) within block; crossing a block boundary = **1 swap** | bounded, predictable: at most 1 swap when stepping off a block edge |
| **move cursor far (goto line N)** | **O(blocks)** to find the block (≤50 entries — trivial linear scan with running line-count), then **1 swap** + in-block offset lookup → **O(1)-ish, 1 swap** | the block list is tiny and resident; finding the block is a ~50-entry scan in-RAM; **no text movement, exactly 1 page swap** |
| render screenful | 24 lines span **1–2 blocks** → **1–2 swaps total** | dramatically better than the linked-list's up-to-24; locality is structural |
| serialize to `.tbn` | O(n) walk blocks in order, each block's lines in order, re-derive offsets/PCs | one linear pass; reads each text page exactly once (N swaps total = N pages, optimal) |
| load from `.tbn` | O(n) decode records, fill blocks page-by-page | fills one page, claims next from pool, continues; N-page swaps total |

**Memory overhead.** Per-block: header + in-block line offset table (u16 ×
lines-in-block) + gap slack. At ~380 lines/block × 2 B offset = ~760 B of offset
table per 16 KB block ≈ **~5% overhead**, plus chosen gap slack (say 5–10% of a
block kept free to absorb inserts without an immediate split). Total overhead at
400 KB ≈ **~10–15% (~40–60 KB)** — *better than the linked-list's 104 KB and
comparable to a fragmented piece table's index, but with far better locality.*
The block descriptors themselves are negligible (~300 B). Crucially the overhead
is **bounded and predictable** (it does not grow with edit count, unlike the piece
table's fragmentation or the linked-list's free-list holes).

**Page-swap behaviour — the decisive advantage.** Every core operation touches
**0–2 pages**: insert/delete = 0 swaps (cursor block resident), ±1 nav = ≤1 swap,
goto = 1 swap, render = 1–2 swaps. The *only* multi-page operation is a block
split (claim 1 page, copy ≤½ block ~8 KB → ~0.03–0.05 s), which happens roughly
once per ~380 inserts into the same block. This is the structure the SAM's paging
hardware *wants*: it aligns the data structure's unit (block) with the hardware's
unit (page).

**Verdict.** **The sweet spot for paged memory.** It bounds every edit to an
intra-block operation (≤16 KB worst, typically µs), bounds every navigation to
≤1 swap, renders in 1–2 swaps, has predictable bounded overhead, and maps blocks
1:1 onto the i2 free-page pool. Its cost is **more code** than a gap buffer or
linked list (block split/merge, in-block gap management, the block list), and it
does *not* give the piece table's free undo (undo needs a separate journal —
§4.4). **Recommended core**, see §5.

### 2.6 Cost summary across options (400 KB, N ≈ 13,000 lines)

| operation | Gap buffer | Piece table/tree | Linked-list (naive) | **Paged block-list** |
|---|---|---|---|---|
| insert at cursor | **O(1)** ✅ | **O(1)** ✅ | O(1) ✅ | O(≤16KB) **≤60 ms** ✅ |
| delete at cursor | **O(1)** ✅ | **O(1)** ✅ | O(1) ✅ | O(intra-block) ✅ |
| cursor ±1 line | O(line) ✅ | O(1), ≤1 swap ✅ | O(1), **≤1 swap/step** ⚠️ | O(1), ≤1 swap ✅ |
| **goto line N / label** | **O(dist) up to 2.5 s** ❌ | O(log P), no shift ✅ | **O(N) walk** ❌ / needs index | **O(1), 1 swap** ✅ |
| render screenful | needs line-index | O(24), **up to 5–10 swaps** ⚠️ | O(24), **up to 24 swaps** ❌ | O(24), **1–2 swaps** ✅ |
| serialize `.tbn` | O(n) ✅ | O(n) ✅ | O(n) ✅ | O(n), optimal swaps ✅ |
| load `.tbn` | O(n) ✅ | **O(n), cheapest** ✅ | O(n), N allocs ⚠️ | O(n) ✅ |
| overhead @ 400 KB | **~0%** ✅ | ~64 KB if fragmented ⚠️ | **~104 KB** ❌ | ~40–60 KB, bounded ✅ |
| undo/redo | snapshot/journal | **near-free** ✅ | journal | journal |
| code complexity | **lowest** ✅ | medium-high | medium (deceptive) | medium-high |
| worst-case edit @ 400 KB | **~2.5 s** ❌ | **< 5 ms** ✅ | < 1 ms (but goto O(N)) | **~60 ms** ✅ |

---

## 3. Comment / symbol anchoring (must not trigger a global reindex)

The hard requirement: **an insert must not renumber anything.** Comments and
symbols must be anchored by *stable record reference*, never by byte-offset or
line-number — otherwise inserting line 5 shifts the offsets/line-numbers of all
13,000 following lines and every anchor referring to them, an O(N) reindex per
keystroke. The v2 `.tbn` design already reached the same conclusion from the
serialize side: labels resolve to PC in *one linear pass at serialize time*
(`compact-tbn-nextgen-design.md` §3.4), and comments/`.global`/base-hints live in
the editor region keyed *not* by live offset (§7 decision 1). The document model
must preserve that property at *edit* time.

**Design: stable record-id + side-tables keyed by id.**

1. **Every line/record gets a stable `record_id` (u24) at creation**, allocated
   from a monotonic counter, *never reused on delete* within a session
   (monotonic-with-gaps is simpler than free-list reuse; a long session creates and
   deletes many lines, so u24 ≈ 16 M ids avoids exhaustion where u16's 65 K would
   not). The id is stored *in the line node* (block model: a small per-line id
   field in the in-block table; piece model: in the piece descriptor for the
   line). **The id is independent of position** — inserting a line gives the new
   line a fresh id and changes *no other id*.

2. **Comments anchor to a record-id, not an offset.** A trailing comment is stored
   *inline* in its line's record (it travels with the line automatically — the best
   anchoring is "same record"). A standalone comment is its own line-record with
   its own id; it sits between lines naturally and needs no separate anchor. So in
   the *document model*, comments need **no side-table at all** — they are lines (or
   parts of lines) and move with the structure for free. The byte-offset anchoring
   in the `.tbn` editor region (§7 of i39) is a *serialize-time* artifact: at
   serialize, the linear pass that computes each record's final offset also stamps
   the comment's anchor offset. Edit-time uses record adjacency; serialize-time
   converts to offsets. **No edit-time reindex.**

3. **Symbols (labels) anchor by record-id → resolved to PC at serialize.** A label
   definition is a line-record (kind `LABEL_DEF`); a reference (`bl foo`,
   `:lo12:foo`) stores the **symbol's name-id** (an index into the name table), not
   a position. The name-id ↔ name-string map is stable across inserts (a new label
   appends a name, changing no existing id — `compact-tbn-nextgen-design.md` §3.7).
   **Symbol → PC resolution happens only at serialize/assemble**, in the existing
   single linear pass (`reader.asm` walk + pass 1): walk records in order
   accumulating PC, and when a `LABEL_DEF` record is seen, record `name-id → PC`.
   An insert changes the eventual PCs of following labels, but **that recomputation
   is deferred to the next serialize/assemble**, not done per keystroke. Between
   edits, the editor needs label *positions* only for "jump to label," which it
   serves from a **name-id → record-id side-table** (the label's *record*, found in
   O(1)/O(log)), not from a PC. Jumping to a label = look up its record-id → find
   the block holding that record (a record-id → block index, maintained cheaply on
   block split/merge) → 1 swap. No PC needed for navigation; PC needed only for
   assembly, computed then.

4. **The side-tables that *are* needed, and why they don't reindex:**
   - `record-id → location` (which block / piece holds the record). Updated only
     when a record *moves between blocks* (a block split moves ~½ a block's
     records to a new block — O(½ block) id-table touches, amortised over ~380
     inserts). An ordinary insert touches **one** id-table entry (the new line).
   - `name-id → defining-record-id` (for goto-label / rename). Updated only when a
     label is created/deleted/renamed — O(1) per such event, not per keystroke.
   - Both tables are keyed by a *stable* id, so an insert that doesn't cross a block
     boundary updates **O(1)** entries. This is the whole point: stable ids convert
     "renumber everything after p" into "touch the one thing that changed."

**Net:** comments ride their record (zero anchoring cost at edit time); symbols
resolve to PC only at serialize (one linear pass, exactly as the assembler already
does); navigation uses record-id side-tables updated O(1) per edit. **No operation
reindexes the document.** This composes directly with i39's "labels → header
offset table at serialize" and "comments → editor region keyed by offset at
serialize" — the document model is the *edit-time* representation those
*serialize-time* tables are produced from.

---

## 4. Candidate architectures (points on the simplicity ↔ scalability curve)

### 4.1 Candidate "Simple" — gap buffer + flat line-index, single page-set

**Shape.** A gap buffer for text + a flat line-table (array of offsets). Targets
documents that fit in a **bounded RAM window the editor maps in full** (say ≤ ~3–4
pages of text, ~48–64 KB).

**Projected worst-case edit @ 400 KB:** **does not apply — fails above ~64 KB.**
Within its envelope (≤64 KB): insert = gap write O(1) + line-table shift of ≤ ~12
KB → **< 50 ms**; goto = O(1) via line-table; the gap-buffer far-jump shift is
bounded by the 64 KB envelope (~0.23 s — acceptable for the *small* case). Page
behaviour: the whole document is mapped across ≤4 fixed pages; far jumps stay
within them; **few swaps**.

**Use it for:** the MVP / first editor, small files, learning aarch64 by editing a
few hundred lines. **Do not** use it as the 400 KB answer — the gap far-jump and
the flat line-table shift both blow the bound past ~150–200 KB. But it is the
*right place to start* (ship a working editor on small files), and it shares the
serialize path with the scalable model.

### 4.2 Candidate "Scalable-A" — paged block-list + per-block gap buffer + resident block index (RECOMMENDED)

**Shape.** Option 4. Blocks = pages; intra-block gap buffer; tiny resident block
list with running line-counts; record-id side-tables (§3) for navigation;
serialize/load stream block-by-block.

**Projected worst-case edit @ 400 KB:**
- Typical insert (room in block's gap): in-block gap write + in-block offset-table
  shift (≤380 entries × 2 B = ≤760 B) + 1 id-table entry → **well under 1 ms, 0
  swaps.**
- Worst insert (block full → split): copy ≤½ block (~8 KB for full-page blocks,
  ~4 KB for ½-page) to a freshly-claimed page, fix up two blocks' offset tables,
  insert one block-list entry, move ~½ the block's record-ids in the location
  table → **~0.03–0.08 s, +1 page claimed.**
- **Worst-case bound ≈ 30–80 ms** — comfortably inside the 1 s hard bound and
  inside the 100 ms "typical" aspiration even for the *split* case.
- Goto-label/line-N: ~50-entry resident scan + 1 swap → **< 1 ms.**
- Render screenful: 1–2 swaps → **negligible.**

**Page behaviour:** every operation 0–2 swaps; only a split is multi-page (and
even then bounded to one ½-block copy). Blocks claimed from the i2 free-page pool
on demand; this *is* the "grow buffers on demand" model (§7).

**Risks:** more code (split/merge, in-block gap, block list); block-split
amortisation (a degenerate pattern of always inserting into the same full block
splits repeatedly — but split leaves both halves ~half-full, so amortised cost is
fine); choosing block granularity (full 16 KB page = fewer blocks/less overhead
but bigger split copies; ½-page = more blocks/more overhead but ~4 KB splits and
finer paging — **recommend ½-page (8 KB) blocks**: split copies drop to ~4 KB
(~15–25 ms) and the block window can share a section with other editor state).

### 4.3 Candidate "Scalable-B" — piece tree + add-buffer + record-id pieces

**Shape.** Option 2 with an order-statistic piece tree keyed on cumulative line
count, pieces carrying record-ids, add-buffer append-only, periodic compaction on
save (i40).

**Projected worst-case edit @ 400 KB:** insert = append to add buffer (O(1)) +
tree relink (O(log P), a handful of node touches across maybe 2–3 pages) →
**< 5 ms, 2–4 swaps.** This is the *fastest edit* of any candidate (no
intra-block shift at all). Goto = O(log P) tree descent, no shift. **Worst-case
edit ≈ < 5 ms** — better than block-list's split case.

**The catch is render and fragmentation, not edit:** rendering a screenful can
touch pieces scattered across many pages (**up to ~5–10 swaps/screenful**), and a
long heavily-edited session fragments to thousands of pieces + a multi-page tree +
accumulating dead add-buffer bytes, all of which must be paged. Compaction on save
(i40 already swaps to disk) resets fragmentation each save — so the *steady-state*
fragmentation is bounded by edits-since-last-save, not session length. **Free
undo** (§2.2) is a real bonus for the replay/blast-radius feature.

**Risks:** the piece tree is the most code and the subtlest to get right
(balancing across far pointers, order-statistic maintenance); render-swap cost is
the worst of the scalable options; dead-byte accumulation needs the compaction
discipline. Higher payoff (fastest edits + free undo), higher complexity.

### 4.4 Undo/redo across the candidates

- **Block-list (Scalable-A):** needs an explicit **undo journal** — record each
  edit as `(record-id, op, old-bytes/new-bytes)` and replay inversely. Because
  edits are local (one block), journal entries are small (a line's worth). Cheap
  enough; ~tens of bytes/edit, capped ring buffer. Not *free* like the piece
  table, but simple and bounded.
- **Piece tree (Scalable-B):** undo ≈ restore the previous piece-list state (the
  deleted text still lives in original/add). Near-free, unlimited. This is the
  piece table's signature win and aligns with "replay-on-edit / blast-radius."

If unlimited cheap undo is a hard editor requirement, it tilts toward Scalable-B;
if a bounded undo journal suffices (it does for a 1980s-idiom line editor), the
block-list's other advantages dominate.

---

## 5. Recommendation

**Adopt the paged block-list (Candidate "Scalable-A", §4.2) as the document model,
and ship Candidate "Simple" (§4.1) first as the MVP behind the same serialize/load
interface.**

**Reasoning:**

1. **It is the only option whose every core operation is bounded to 0–2 page
   swaps**, on a machine where the page swap is the characteristic cost. The gap
   buffer's far-jump and the linked-list's render both go O(pages-spanned); the
   piece tree's render goes ~5–10 swaps. The block-list's worst case is a single
   ½-block split. On the SAM, *bounding the swap count* is more important than
   shaving microseconds off the in-RAM work, because a swap evicts a window and
   serialises against the rest of the machine.

2. **It aligns the data-structure unit with the hardware unit.** A block *is* a
   page. This is the SAM-native shape: it makes "lives across 16 KB windows with
   far pointers" trivial (the far pointer is just the block's page; everything
   within a block is a near 14-bit offset) instead of a pervasive tax. It maps 1:1
   onto the i2 "claim pages from the free pool, grow on demand" model — a block
   split *is* a page claim.

3. **Its overhead is bounded and predictable (~10–15%)** and does not grow with
   edit count, unlike the piece table's fragmentation (→ multi-page index) or the
   linked-list's free-list holes (→ ~104 KB and rising). On a machine where
   overhead trades directly against max document size, *predictable* overhead is
   worth more than *occasionally-lower* overhead.

4. **Worst-case edit ≈ 30–80 ms at 400 KB** (the split case, ½-page–full-page
   blocks), with typical edits sub-millisecond — inside both the 1 s hard bound and
   the 100 ms aspiration, *at the full 400 KB scale*, which neither the gap buffer
   (2.5 s far-jump) nor the naive linked-list (O(N) goto) achieves.

5. **It composes cleanly with i39/i40/i2** (§5.2).

**Why not the piece tree (Scalable-B)?** It has the *fastest edits* and *free
undo*, and it is a legitimate alternative if free-unlimited-undo for the
replay/blast-radius feature proves to be a hard requirement. But its
render-swap cost is the worst of the scalable options, its fragmentation makes the
*resident* size unpredictable (the thing we most need to bound on a 512 KB
machine), and it is the most code to get correct across far pointers. The
block-list trades a few ms of edit latency (still sub-100 ms) for bounded swaps,
bounded overhead, and a simpler mental model — the right trade for this machine.
**Keep the piece tree as the documented fallback** if undo or edit-latency
requirements tighten.

**Why ship "Simple" first?** The block-list is real work (split/merge, in-block
gap, side-tables). The gap-buffer-+-line-index MVP is a few hundred lines of Z80,
works perfectly for the small documents an editor is first exercised on, and —
critically — **shares the serialize/load `.tbn` path** with the scalable model.
Build the serialize/load boundary once, ship the editor on small files, then swap
the in-RAM core from gap-buffer to block-list without touching serialize. This is
the "ship the spine, then scale" discipline the i39 design already follows.

### 5.1 Biggest risks of the recommendation

- **Block split/merge correctness** — the in-block offset table and the cross-block
  record-id location table must stay consistent through splits/merges. Mitigate
  with a property test (mirror the harness approach): random insert/delete
  sequences, assert that serialize→load→serialize is byte-stable and that
  goto-by-id always lands on the right line.
- **Block granularity tuning** — ½-page (8 KB) vs full-page (16 KB). Recommend
  ½-page (smaller split copies, finer paging) but this is an empirical knob;
  measure on a realistic edit trace.
- **Degenerate insert pattern** — repeatedly inserting at the front of one block
  could thrash splits. Mitigate by keeping the in-block gap *centred near the
  cursor* (gap buffer discipline) so consecutive inserts at the cursor consume gap
  rather than re-splitting.
- **Undo journal sizing** — a bounded ring; decide the cap. Low risk.

### 5.2 How it composes with i39, i40, i2

- **i39 (serialize target, the v2 `.tbn`).** The block-list serializes to v2 in
  one linear pass: walk blocks in order, walk each block's lines in order, and feed
  the i39 serializer (instruction-overlay packing, header label/offset table,
  front-coded names, editor region for comments/`.global`/base-hints). The
  document model holds **source-shaped line records**; the i39 *instruction-overlay
  packing is applied at serialize, not held in the edit buffer* (challenge to the
  prompt's framing — see §6). PC/offset resolution is the same single linear pass
  the assembler already does (`reader.asm` walk + pass 1) — the document model
  feeds it records in order; stable record-ids mean no edit ever reindexed
  anything (§3). Load is the inverse: i39 reader → line records → fill blocks.

- **i40 (editor-region eviction / disk-swap during assembly).** On assemble: the
  editor *serializes the document to `.tbn` on disk*, then **frees its block pages
  back to the pool** so the assembler can reuse them as OUT/scratch (the assembler
  resident budget is ~34.5 KB under i39 Format B; the document's ~25 pages dwarf
  it). After the build, the editor **reloads the `.tbn`** and refills its blocks —
  and *this reload is the natural compaction point* (it also reclaims any block
  slack/fragmentation, and, if Scalable-B were used, the dead add-buffer bytes).
  The block-list's "free pages to pool / reclaim from pool" maps exactly onto i40's
  evict/reload.

- **i2 (claim all free RAM at boot, grow on demand from the free-page pool).** The
  block-list's blocks *are* the units claimed from the i2 pool: at boot the editor
  sizes the pool via `PRAMTP`/`ALLOCT`/`LASTPAGE` (`sam-paging.md` §5–§6),
  reserving DOS (1 page, `DOSFLG`) + screen (2 pages) + code, and shows the
  remaining as the document budget. A block split claims one page; closing a
  document (or i40 eviction) returns pages. The "grow buffers on demand" model the
  i2 reframing calls for is *literally* block allocation. The deferred i2 question
  ("contiguous-bump vs per-buffer page-list") is answered for the document by the
  block-list: **per-block page-list** (each block a separately-claimed page),
  because the document grows non-contiguously and must interleave with OUT/scratch
  after i40 eviction.

---

## 6. Challenge to the brief: edit-model records vs `.tbn` records

The prompt says the document model serializes to `.tbn`; the principle is right,
but one sub-point deserves an explicit challenge.

**The document model should hold *source-shaped line records*, NOT the compact
`.tbn` instruction-overlay records.** Reasons:

1. **The overlay packing (i39 §3.2) is an assembly-oriented transform**: it stores
   each instruction as its *assembled 4-byte word* with relocated bitfields zeroed
   + an expression overlay. That representation is great for streaming to OUT and
   for density, but it is the *wrong* thing to edit: to render `bl foo` for the
   user, or to let them change `x0` to `x1`, you want the **source operands**, not
   a base word you must disassemble first. Holding the overlay form in the edit
   buffer means *disassembling on every render* and *re-assembling on every edit* —
   exactly the per-keystroke work we are trying to avoid.

2. **The editor already has the disassembler and the form table** (i39 round-trip,
   ROADMAP explanation-panel synergy). Source-shaped records (mnemonic-id +
   source operands, or even lightly-tokenised text) render directly and edit
   directly. The compact overlay is produced *once, at serialize*, by running the
   encoder over the source records — the same encoder the assembler uses.

3. **This keeps the boundary clean:** document model = *editable source records*;
   `.tbn` = *assembled/serialized form*. Serialize = encode (source records →
   overlay + tables). Load = decode (overlay + tables → source records, via the
   disassembler). The expensive transform happens at the load/save boundary
   (O(n), amortised over a whole session), never per keystroke.

**Open sub-question (§7.3):** *how* source-shaped — raw source text per line
(re-lex on every assemble), or a pre-lexed token/record form (parse on edit, no
re-lex on assemble)? Raw text is simplest and most faithful for round-trip
(preserves exact spelling/whitespace/base — the i39 "base hints" become free);
pre-lexed is faster to serialize but must carry spelling hints separately. **Lean
raw-text-per-line** for the MVP (simplest, most faithful), revisit if
serialize-time re-lex proves slow (it is O(n) once per assemble, not per edit, so
likely fine).

---

## 7. Decisions (Pete, 2026-06-08)

All five resolved. The recommendation (paged block-list, §4.2/§5) stands; these pin
the parameters.

1. **Block granularity → ½-page (8 KB).** ~4 KB worst-case split copies (~15–25 ms),
   finer paging; the ~2× block-count/overhead is acceptable. The exact split cost is
   **empirically tuned** on a real edit trace before freezing — ½-page vs full-page is
   a constant the implementation can flip without structural change.

2. **Undo model → bounded ring journal** on the block-list. Depth is tunable and
   almost certainly ample for a 1980s-idiom line editor. Crucially this keeps the
   structure on the **block-list** (not the piece tree) — unlimited free undo was the
   only thing that would have tilted the choice to Scalable-B, and we don't need it.

3. **Edit-model record shape → pre-lexed *parsed* records (= the i48 in-memory
   symbolic IR).** This supersedes the §6 "raw-text" lean. Rationale (Pete's
   validation-on-entry instinct + the i48 unification):
   - **The editor's document model *is* the i48 symbolic IR.** i48 made the symbolic
     form (mnemonic-id + operand tokens + expression bytecode) the in-memory
     intermediate between tokenize and overlay-emit; storing *parsed records* makes
     the editor the i48 front-end holding that IR live. **entry** = parse → record
     (parse-success *is* the validation, the moment a line is committed); **display**
     = detokenize → text (the disassembler/bin2text direction the editor needs anyway
     to render a loaded `.tbn`); **save** = IR → overlay (no re-lex); **load** =
     overlay → IR.
   - **More compact than raw text** — symbol *names* are interned once in the name
     table and referenced by id, not repeated inline at every use; on a symbol-heavy
     source that is a material edit-buffer saving, directly serving i41's large-source
     memory goal.
   - **Layout consistency comes free** — re-rendering each line from tokens *is*
     canonical formatting (Pete's "ensures layout is consistent"). The trade-off vs
     raw text: keystroke-exact spacing isn't byte-preserved (re-rendered canonically),
     which for a learning editor is a feature; any spelling worth keeping
     (hex-vs-decimal, `.short`-vs-`.hword`) rides in i48's base-hints.
   - **Edge case (designed for):** an in-progress / syntactically-invalid line can't
     be a parsed record, so the model is a small **hybrid** — committed lines = parsed
     records; the line under the cursor = raw text, parsed on commit; an invalid line
     on commit is **flagged and held as a raw/error record** (validation feedback
     without forcing an immediate fix; it just won't serialize until valid).

4. **Staging → build the block-list directly** (no throwaway gap-buffer MVP). We
   already argued the simple structures don't scale, so there's no architecture to
   trial. The MVP's only real value was de-risking the **editor↔serialize boundary**;
   we keep that by holding the **serialize/load seam cleanly separable and
   independently unit-tested**, without throwaway core code.

5. **record-id width → u24, no reuse.** ~16 M ids — never exhausted in a real
   session, so no free-list. The premise that the simpler option is *smaller* is
   inverted: u24 costs ~1 byte/id **more** than u16 — on the 400 KB / ~13 K-record
   stress case that's **~18 KB, ~4 % of the buffer** (less on realistic sources). But
   u16 would *require* a free-list (65 K ids can't cover a churny session without
   reuse), and **id reuse breaks correctness** — a recycled id can mis-anchor a
   comment or corrupt an undo-of-a-delete. So u24 is simpler, faster, *and* safer; the
   ~4 % space is well worth it.

---

## 8. Implementation status + i48-IR integration scoping (i41a–i41e)

The host-side Go authority (`tools/sam-aarch64/editmodel`) is built incrementally;
the Z80 port (i41d) mirrors it (CLAUDE.md §6 — Go is the authority).

**Landed:**

- **i41a — core block-list + serialize seam** (PR #383). ½-page blocks, u24
  never-reused ids, the record-id→stable-block-pointer location table maintained
  O(½ block) per split/merge (no global reindex — §3.4), insert/delete/goto-by-id/
  render, behind a separable `EMDL` serialize seam. §5.1 property test green.
- **i41b — bounded ring-journal undo/redo** (PR #384). `maxUndoDepth` ring,
  drop-oldest; undo-of-delete restores the **same id** (§3 id-stability).
- **i41c — real v2 `.tbn` serialize seam** (this PR). `SerializeTBN`/`LoadTBN`
  compose the existing `frontend.Translate` → `assemble.Pass1` →
  `assemble.CompactTBNBytes` (save) and `render.EmitLinesFromBytes` (load), so the
  editor reads/writes the project's native storage format. The `.tbn`-level
  round-trip is byte-stable (mirrors the `disasm-roundtrip` gate). `EMDL` is
  retained as the internal exact-round-trip structural seam the i41a/i41b property
  tests depend on. **Deliberate limitation:** `SerializeTBN` requires a *complete,
  valid* assembly (the encoder resolves all symbols/PCs); an invalid or partial
  document **fails loud**. The line payload is still raw text — the i48-IR payload
  swap is the remaining work (i41e).

**Remaining — i41e (i48-IR record payload + symbol table + hybrid record) is a
larger integration with genuinely-open design surface** (not a mechanical port of
existing Go), so it is split out and gated on design rather than bundled with the
mechanical serialize seam above:

1. **Symbol-table ownership at edit time.** Holding `format.Record` per line
   couples the document to a document-global `*format.SymbolTable` (records
   reference interned name-ids; `frontend/parser.go`). The frontend's symbol table
   is build-once-per-parse, not incrementally-edited; the editmodel needs an
   incremental name-id lifecycle + a `name-id → defining-record-id` side-table
   (§3, none exists yet).
2. **The hybrid record type** (§7.3): committed = parsed `format.Record`, active
   line = raw text, invalid line = raw/error record. `format.Record` has no raw/
   error variant today — a wrapper sum type must be designed.
3. **Per-line text→IR entry.** There is no per-line parser; `frontend.Parse` is a
   whole-token-stream pass threading the shared symbol table. "Parse one line in
   isolation" ≠ the assembler's validation (context, forward refs) — the per-line
   validation semantics need pinning.
4. **Partial / invalid-document serialize (the sharpest, → qN).** The existing
   `Compact`/`Pass1` encoder assumes a fully-resolvable assembly and errors
   otherwise. How a document containing raw/error lines serializes — refuse to
   save until valid (the i41c fail-loud default), save valid-lines-only, or save
   raw text + error records — is an **editor-UX product choice** for Pete, tracked
   in the question registry. i41c's fail-loud is the safe interim; i41e needs the
   answer.
5. **`name-id ↔ record-id` side-tables** (§3) — for goto-label / rename; absent
   today (only the record-id→block `loc` table exists).

### 8.1. i41d — Z80/SAM port decomposition (scoped 2026-06-17)

The Z80 port of the (now-complete) Go authority decomposes into bricks, most of
which are **host-verifiable in flat memory** — they need no SAM paging hardware,
so they run under the existing **koron-go/z80 flat-memory harness**
(`tools/netboot-oracle/z80/`), exactly as the netboot protocol routines do. The
harness already supports the stateful drive-and-compare pattern a block-list
needs: `Load` once, then a sequence of `Call("em_insert")`/`Call("em_delete")`/
`Call("em_goto")` with params poked via `Write`/`WriteU16LE` and results read via
`Read`/`Sym`, comparing the resulting line order + goto results against the i41a
Go `Document` driven through the identical op-sequence (the proven
`tcp_conn_test.go` shape). **No new harness scaffolding is required** — only a
Makefile payload target + the test, gated by the existing `netboot-z80` CI job.

- **Brick 1 — flat-memory block-list core** (split into 1a/1b for reliable
  landing, both green-on-main, NOT red-until-done):
  - **Brick 1a (LANDED — PR #387):** `src/editmodel.asm` — the insert + build-up +
    navigate path: block storage (a fixed descriptor pool, packed `id|len|text`
    records, a document-order array), `em_insert` with intra-block shift, **split
    on overflow**, the **record-id→stable-descriptor location table** (`EM_LOC`,
    re-pointing only moved records on split — the §3 bounded-cost property — with
    an order-array shift that does NOT touch loc), `em_line_at`, and `em_goto`
    (goto-by-id via loc → a scan of the resident order array, §2.5). A faithful
    port of `editmodel.go`'s `doInsert`/`splitBlock`/`IndexOf`/`LineAt`/
    `blockAndLocalIndex`. Verified by `tools/netboot-oracle/z80/editmodel_test.go`
    (120 random-position inserts × 2 seeds vs a parallel oracle → 19/21 blocks,
    every `em_line_at` id+text and every `em_goto` index match, plus a not-found
    check). Flat-memory, small `EM_BLOCK_CAP=256` (the real SAM ½-page 8 KB is set
    at Brick 2); the koron-go/z80 harness gained an additive `CallResult.A` field
    for the found-flag. Gated by the existing `netboot-z80` CI job.
  - **Brick 1b (LANDED — PR #389):** `em_delete` (gap-close + decrement + the
    `EM_LOC_ABSENT=&FF` sentinel so `em_goto` reports a deleted id as not-found)
    + block **merge on underflow** (port of `doDelete`/`tryMerge`: combine with a
    neighbour when `used ≤ EM_BLOCK_CAP`, re-pointing only the moved records'
    `EM_LOC`) + a descriptor **free-list** (so merge/empty-block descriptors are
    reused, replacing 1a's high-water-only allocator). Verified by
    `TestEditModelDeleteMergeZ80` (3 seeds: 120-insert build-up + 200 interleaved
    insert/delete ops vs a parallel oracle → every `em_line_at`/`em_goto`-live
    matches, deleted ids report not-found, block count stays bounded = merges
    fire). **Brick 1 (the flat-memory block-list core) is now complete.**
- **Brick 2 — paging integration:** map blocks onto real i2 free-page-pool pages
  via `OUT (251)`, claim/return pages on split/merge. Each block gets one
  pool-claimed 16 KB physical page (`pp_alloc_page(PP_DOC)`); the descriptor
  stores the *page number* (not a flat pointer), and `em_desc_dataptr_a` pages it
  into section C before access. `EM_BLOCK_CAP` rises from the 256 testing value to
  the 8 KB Go-authority cap (`blockCapacityBytes`). Split claims a page, merge/
  close returns it (`pp_free_page`, tag `PP_DOC`).

  **Host-verifiable (correcting the earlier "SimCoupé-only" note).** The koron-go
  Z80 harness runs on the **one** `sampage` memory model — `OUT (251)`/HMPR pages
  section C/D for real (`tools/netboot-oracle/z80/harness.go`,
  `tools/sampage/sampage.go`; CLAUDE.md §7). So the paged path is exercised in the
  fast harness (seed the page pool, drive paged insert/split/merge vs the Go
  oracle), exactly like Bricks 1/3/4a; SimCoupé remains the pre-merge gate, not the
  only verifier.

  **Two design points the flat Go authority does NOT settle (this paging is
  net-new SAM-side, so it is design, not a mechanical port — CLAUDE.md §6):**
  (1) **Resident-structure home.** Section D = `(HMPR&0x1F)+1` *moves with every
  `OUT (251)`* (`sampage.go:20`), so the resident block-list (`EM_DESC`/`EM_ORDER`/
  `EM_LOC`/undo ring/scratch) must live in **LMPR-mapped low memory**, not section
  C or D — the "resident in section D" phrasing above is imprecise. (2) **Cross-
  block copy.** Section D is forced to C's page +1, so two *arbitrary* pool pages
  cannot be mapped simultaneously; a split/merge copy between blocks must route
  through a **resident scratch buffer** (page A into C → copy its tail to the
  resident buffer → page B into C → copy buffer → B), not a second live window.
  See item i41d for the port.
- **Brick 3 — bounded ring-journal undo/redo** (LANDED — PR #391): port of i41b
  to `src/editmodel.asm`. `em_insert`/`em_delete` split into journaling public
  wrappers over non-journaling `em_do_insert`/`em_do_delete` primitives (the Go
  `doInsert`/`doDelete` split), so `em_undo`/`em_redo` replay with the original id
  and do not re-journal. Undo = a ring of `EM_MAX_UNDO` fixed slots with
  drop-oldest; redo = a LIFO bounded by the same depth, cleared by any fresh edit.
  New exports `em_undo`/`em_redo`/`em_can_undo`/`em_can_redo`. Verified by
  `TestEditModelUndoRedoZ80` (300-step random walk × 3 seeds vs a matched-cap Go
  reference) + `TestEditModelUndoDropOldestZ80` (deterministic drop-oldest).
  Flat-memory, gated by the `netboot-z80` CI job.
- **Brick 4a — EMDL exact serialize/load** (LANDED — PR #392): port of
  `serialize.go` to `src/editmodel.asm`. `em_serialize` writes the document to
  `EM_SER_BUF` in the EMDL v1 format (`"EMDL" | ver | linecount | per line
  {id:3 | textlen:2 | text}`, partition-independent), returning the byte length;
  `em_load` validates the header (fail-loud, A=0, document untouched on a bad
  header), resets, refills in stream order via `em_do_insert`, and restores
  `EM_NEXTID = maxID+1`. Verified by `TestEditModelSerializeZ80` (3 seeds): the
  Z80 bytes match the EMDL spec, the stream round-trips losslessly (same id/text
  sequence and byte-stable reserialize), nextID is restored, and a corrupt header
  fails loud. Flat-memory, gated by the `netboot-z80` CI job.
- **Brick 4b — real v2 `.tbn` path** (port of i41c `SerializeTBN`/`LoadTBN`):
  on-SAM this wires to the existing assembler/reader (`frontend`→`Pass1`→
  `Compact` for save, the `.tbn` reader for load) and requires a complete, valid
  assembly (fail-loud otherwise, per q24). Couples to the full assembler build,
  not the standalone `editmodel.bin` harness — a larger integration, deferred.
