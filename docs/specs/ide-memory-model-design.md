# On-SAM IDE memory model — design proposal (item i2)

> **Status: PROPOSAL, awaiting Pete's steer on the headline architecture (q36).**
> This is a worked recommendation to react to, not a committed design. It does
> not change any code. The genuinely-foundational decision — *one dynamic
> page-pool shared by the editor and the assembler, superseding today's
> hardcoded off-axis pages* — is isolated in §7 and filed as **q36** so the
> direction is Pete's, per the editor-era handover.

## 1. Scope — what i2 owns, and what it must not restate

i2 is the **IDE memory model**: how the on-SAM IDE claims and hands out physical
RAM pages at runtime, for both the **editor's document** and the **assembler's
IN/OUT/scratch buffers**, under the framing *claim all free RAM at boot, grow on
demand*.

Single-source-of-truth boundaries (this doc cites, never restates):

- **SAM paging hardware** (16 KB pages, sections A–D pairing, LMPR/HMPR, the
  free-page sysvars) is owned by `docs/notes/sam-paging.md`.
- **The editor document data structure** (the paged block-list, per-block gap
  buffer, record-id anchoring) is owned by `docs/specs/editor-edit-model-design.md`
  (item i41). i2 does **not** re-specify the block-list; it specifies the *pool
  the block-list's pages come from*.
- **The current assembler's static layout** (off-axis pages 4–15, the `&C000`
  code cliff) is owned by `docs/notes/memory-layout.md` + the `src/assembler.asm`
  header. i2 proposes how that static map becomes dynamic.
- **Editor-region eviction during assembly** is owned by i40
  (`docs/specs/comment-storage-design.md`, PR #181). i2 specifies the
  *page free/reclaim* half of that evict/reload cycle.

What i2 newly owns and must pin down: the **page allocator** (`alloc_page` /
`free_page` + ownership bitmap), the **boot-time pool-sizing** procedure, the
**editor↔assembler page-sharing** discipline, and the **pool-exhaustion** policy.

## 2. The constraints (cited, not restated)

From `docs/notes/sam-paging.md` (§§1–6) and `docs/notes/memory-layout.md`:

- Physical page = 16 KB. A 256 KB SAM has pages 0–15; a 512 KB SAM has 0–31.
  `PRAMTP` (`&5CB4`) holds the highest physical page present at boot
  (`0x0F`/`0x1F`) — `sam-paging.md:82-84`.
- Sections A/B are an inseparable physical pair (LMPR, LMPR+1); C/D likewise
  (HMPR, HMPR+1) — `sam-paging.md:86-91`. The CPU sees four 16 KB windows; the
  pool hands out **physical pages**, mapped into a window on demand via an
  LMPR/HMPR bracket.
- At boot BASIC owns pages 0–3; SAMDOS occupies one page (`DOSFLG`, `&5BC2`);
  the screen occupies two pages — `sam-paging.md:456-485, 569-598`.
- **Free-page budget after reserving ROM-shadow/BASIC(4) + DOS(1) + screen(2) +
  resident IDE code:** ~**9 pages (~144 KB)** on a 256 KB machine, ~**24 pages
  (~384 KB)** on a 512 KB machine. This pool is exactly what "claim all free RAM"
  refers to.

The IDE's resident code lives in section C (`&8000–&BFFF`) under the same
`code_end < &C000` cliff the assembler already enforces
(`tools/check-code-budget.sh`); the stack page is section D's first page.

## 3. The reality the model must replace — today's static layout

The assembler today hardcodes off-axis pages (`memory-layout.md` "Physical
(off-axis) pages"):

| Page(s) | Today's fixed use | Fixed ceiling |
|---------|-------------------|---------------|
| 4 | ENCTAB body | 1 page |
| 5–6 | OUT buffer | **32 KB** (i24: 16-bit `OUT_LEN`, 2 pages) |
| 7–12 | IN `.tbn` buffer | **96 KB** (i23: 6 contiguous pages) |
| 13–14 | production payloads / ZX0 staging | fixed |
| 15 | disassembler | 1 page |

These are **statically assigned at link time** and assume a fixed machine. The
IDE wants the opposite: a runtime pool sized to the machine actually present,
with IN/OUT growing past 96 KB/32 KB when the free pages exist (folding i23 and
i24 into the dynamic model), and shrinking on a 256 KB machine.

## 4. The proposed model — one dynamic page pool

### 4.1 The allocator (the new mechanism i2 adds)

A tiny **resident page allocator** in section D, alongside the editor's block
index and cursor state (never itself swapped):

- **`page_owner[]`** — one byte per physical page (≤32 bytes total). Each entry
  is `RESERVED` (ROM-shadow/BASIC/DOS/screen/resident-code — never handed out),
  `FREE`, or an owner tag (`DOC`, `IN`, `OUT`, `SCRATCH`, `ENCTAB`, `DISASM`,
  `PAYLOAD`). The table is the IDE's single source of truth for who holds what.
- **`alloc_page(owner) → page | FAIL`** — pops a `FREE` page, tags it.
- **`free_page(page)`** — returns a page to `FREE`; asserts the caller's tag.

This is deliberately the same shape as the Trinity shared-resource discipline
(`memory/trinity_storage_shared_resource`): **the IDE never hands out a page it
did not establish as free**, and it leaves BASIC/DOS/screen pages untouched. A
page-ownership bug here is the RAM equivalent of clobbering another user's SD
record.

### 4.2 Boot-time pool sizing

At IDE start, before any document or assembly:

1. Read `PRAMTP` → highest physical page → total page count.
2. Mark `RESERVED`: pages 0–3 (BASIC), `DOSFLG`'s page, the two screen pages
   (`sam-paging.md` §§5–6), and the page(s) holding resident IDE code.
3. Mark every remaining page `FREE`.
4. Surface the free count as the **document budget** shown to the user
   (e.g. "≈144 KB free" / "≈384 KB free").

This is the boot-survey i41 §5.2 refers to ("sizes the pool via
`PRAMTP`/`ALLOCT`/`LASTPAGE`"), specified here as the allocator's init.

### 4.3 The editor document draws from the pool

Per i41 (`editor-edit-model-design.md` §2.5, §4.2, §5.2): the document is a
**paged block-list**; each block is one page **claimed from this pool**
(`alloc_page(DOC)`), and a block split claims one more. The "grow on demand"
framing *is* `alloc_page(DOC)` per split. The per-block page-list (vs a
contiguous bump) is already settled by i41 §5.2 — the document grows
non-contiguously and must interleave with IN/OUT/scratch after eviction, so it
**cannot** assume a contiguous window. i2 inherits that answer; it does not
reopen it.

### 4.4 IN / OUT / scratch draw from the same pool (the lift)

The assembler's buffers become pool allocations instead of link-time fixed
pages:

- **IN buffer:** sized to the loaded `.tbn` prefix (i40 already loads only the
  assembler-facing prefix). `alloc_page(IN)` per page needed — **no 96 KB cap**;
  the cap becomes "free pages remaining" (folds **i23**).
- **OUT buffer:** `alloc_page(OUT)` per 16 KB of output; `OUT_LEN` widens to the
  pool's reach — **no 32 KB cap** (folds **i24**). The 16-bit `OUT_LEN` and the
  `emit_byte` low/high-zone bracket generalise to an N-page list.
- **ENCTAB / disasm / payloads:** allocated once at boot from the pool (they are
  resident-for-the-session), replacing their hardcoded page numbers.

### 4.5 The lifecycle — edit ⇄ assemble (composes with i40)

```
boot        : alloc ENCTAB, DISASM, payloads; size FREE pool
edit        : document grows  → alloc_page(DOC) per block split
              document shrinks → free_page on block merge / close
assemble    : editor serializes doc to .tbn on disk (i40), then
              free_page(DOC) for every block page  ──► pool refills
              assembler alloc_page(IN/OUT/SCRATCH) from the now-large pool
assemble end: free_page(IN/OUT/SCRATCH); editor reloads .tbn,
              alloc_page(DOC) refills blocks (the natural compaction point)
```

This is i40's evict/reload (`editor-edit-model-design.md` §5.2) expressed in
allocator calls. The key property: **the editor's ~25-page document and the
assembler's ~3-page resident budget never need to coexist in RAM** — they
time-share the same pool across the edit/assemble boundary, which is what makes
"all free RAM for the document" affordable on a 256 KB machine.

### 4.6 The unified memory map (logical windows × dynamic pages)

| Window | Section | Role |
|--------|---------|------|
| `&0000–&3FFF` | A | ROM0 / bracket window for ENCTAB or an IN/DOC page |
| `&4000–&7FFF` | B | resident sys area + trampoline; bracket window for OUT-low |
| `&8000–&BFFF` | C | **resident IDE code** (editor + assembler + allocator) |
| `&C000–…` | D | stack + allocator tables + block index + scratch |

Physical pages above the reserved set are no longer a fixed table — they are the
`FREE` pool, tagged in `page_owner[]` as the session runs.

## 5. Pool-exhaustion policy

When `alloc_page` finds no `FREE` page:

- **Document growth (editor):** refuse the insert with a clear "document full —
  N KB max on this machine" message; never silently truncate or clobber. On a
  256 KB machine this is a real ceiling (~144 KB); on 512 KB it is unlikely.
  (A future enhancement could spill cold blocks to disk, but that is out of
  scope for i2 — track separately if wanted.)
- **Assembly (IN/OUT):** the editor has already evicted, so the whole pool is
  available; exhaustion here means the *output* exceeds free RAM, surfaced as an
  assembler error with the byte count — the honest failure, not a wrap.

## 6. Risks

- **Page-ownership correctness is load-bearing** — a double-free or a handout of
  a reserved page corrupts BASIC/DOS/screen. The `page_owner[]` table + tag
  assertions are the guard; this wants a boot self-test (claim-all/free-all
  round-trip; assert reserved pages never move) in the harness.
- **256 KB headroom is genuinely tight** — ~9 free pages must cover ENCTAB +
  disasm + payloads + the document. The evict-on-assemble time-share (§4.5) is
  what makes it fit; if a future feature needs editor and assembler resident
  *simultaneously*, the model breaks and 512 KB becomes the floor.
- **Bracket discipline** — every pool page is reached through an LMPR/HMPR
  bracket; the existing `reader.asm`/`emit_byte` patterns generalise, but the
  N-page OUT/IN lists add bracket sites to audit.

## 7. The decision for Pete (q36)

Everything above is a worked recommendation. One choice is genuinely
foundational and is Pete's to steer (the editor-era handover reserved exactly
this):

**Does the IDE adopt a single dynamic page pool shared by the editor *and* the
assembler (this proposal, §4) — superseding the assembler's hardcoded off-axis
pages 4–15 and lifting the fixed 96 KB-IN / 32 KB-OUT ceilings (i23/i24) — or
does the editor layer a separate pool *on top of* the assembler's existing
static layout (smaller change, but two memory regimes and no IN/OUT lift)?**

Sub-decisions that ride the same answer:

1. **Minimum target machine** — is 256 KB a first-class target (≈144 KB document
   ceiling, hard reliance on evict-on-assemble), or is 512 KB the floor? This
   sets the headroom budget (§6).
2. **Pool-exhaustion on a full document** — refuse-with-message (recommended,
   §5) now, with disk-spill deferred to a tracked follow-up; or is disk-spill
   in-scope for i2?
3. **`ALLOCT` coexistence** — does the IDE own page allocation outright once it
   starts (BASIC not returned to mid-session), or must it keep BASIC's `ALLOCT`
   bookkeeping consistent so a clean hand-back is possible?

**Agent recommendation:** the unified pool (§4), 256 KB supported but documents
correspondingly smaller, refuse-with-message exhaustion (disk-spill deferred),
and the IDE owns allocation for its session (rebuild BASIC's view only on exit).
This is the model i41 §5.2 already assumes, it delivers the i23/i24 ceiling lifts
for free, and it matches the "claim all free RAM, grow on demand" framing
directly. The alternative (separate editor pool over the static assembler map) is
a smaller change but leaves two memory regimes and the IN/OUT caps in place — a
local optimum that the editor era will likely outgrow.

## 8. Relationship & lifecycle

- **Builds on:** `docs/notes/sam-paging.md`, `docs/notes/memory-layout.md`,
  `docs/specs/editor-edit-model-design.md` (i41), `docs/specs/comment-storage-design.md` (i40).
- **Lifts when adopted:** i23 (IN ceiling), i24 (OUT ceiling) fold into the
  dynamic pool rather than being patched in place.
- This is a living design doc (evergreen filename). When the model ships, its
  durable rationale folds into `docs/ARCHITECTURE.md` and this file is deleted in
  that PR, per the doc-lifecycle rules.
