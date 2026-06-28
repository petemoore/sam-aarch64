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
- At boot BASIC owns pages 0–3; the loaded DOS occupies one page (`DOSFLG`, `&5BC2`);
  the screen occupies two pages — `sam-paging.md:456-485, 569-598`.
- **The "4 BASIC pages" are not 4 reservations.** The IDE owns the machine for
  its session (§7.3), so it `NEW`s BASIC at startup (Pete, 2026-06-22) and
  reclaims the freed BASIC *program + variable* pages into the pool. What stays
  reserved is only what the ROM/DOS routines need: the **system page** holding
  the SAM system variables (all at `&5xxx` → physical **page 1**; the full table
  is `sam-paging.md §7`), the **system stack**, and the **ROM/DOS workspace**;
  plus the **`DOSFLG` page**, the **two screen pages**, and the **resident-code
  page** (section C loads over a page already in the 0–3 set). The exact reclaim
  falls out of the boot survey (`LASTPAGE`/`RAMTOP` + the sysvar extent), not a
  blind reservation of 4.
- **The two screen pages hold a 24 KB screen, leaving an 8 KB tail usable.**
  Modes 3/4 use 24 KB and wrap an even page into the next odd one
  (`sam-paging.md:165-167`, `tech-man_v3-0.txt:947-955`), so the top **8 KB of
  the odd screen page is free RAM**. The system variables are in page 1, *not*
  the screen pages, so they are never at risk here — but the boot survey still
  **validates** the tail (read `VMPR` → the screen pages; confirm the tail isn't
  `DOSFLG`'s page and isn't `ALLOCT`-marked) before tagging it a fixed
  **screen-tail scratch** region (usable for draw-time/screen-coupled state —
  render scratch, the draw trampoline, part of `page_owner[]` — but never handed
  out as a generic pool page). General principle: reclaim sub-page gaps where
  pages splice, boot-validated.
- **Free-page budget** (before the BASIC-program reclaim, the conservative
  floor): ~**9 pages (~144 KB)** on a 256 KB machine, ~**25 pages (~400 KB)** on
  a 512 KB machine — `NEW`ing BASIC adds a page or two on top, and the 8 KB
  screen-tail a little more. This pool is exactly what "claim all free RAM"
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
2. **`NEW` BASIC** (§7.3) so its program/variable pages collapse and become
   reclaimable.
3. Mark `RESERVED` only what the ROM/DOS still need: the **system page** (the
   `&5xxx` sysvars + stack + ROM/DOS workspace, page 1), the **`DOSFLG` page**,
   the **two screen pages** (from `VMPR`), and the **resident-code page** — *not*
   a blind pages-0–3 block. Use `LASTPAGE`/`RAMTOP` + the sysvar extent to keep
   only the genuinely system-critical low page.
4. **Validate + tag the screen-tail:** compute the top 8 KB of the odd screen
   page (`VMPR`); if it is not `DOSFLG`'s page and not `ALLOCT`-marked, tag it a
   fixed `SCRATCH` region (usable, never a generic pool page). If validation
   fails, leave it reserved — never assume.
5. Mark every remaining page `FREE` (this includes the BASIC program pages freed
   by step 2).
6. Surface the free count as the **document budget** shown to the user
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

- **IN buffer** (shipped, i23): sized to the loaded `.tbn` prefix (i40 already
  loads only the assembler-facing prefix), allocated as a **contiguous run**
  (`pp_alloc_run(IN)`) by `load_in_file` — **no 96 KB cap**; the cap is "free
  contiguous pool pages".
- **OUT buffer** (shipped, i24; q45 = option A): one **contiguous run**
  (`pp_alloc_run(OUT)`) sized from the pass-1 total by `reset_out_buffer`,
  every `emit_byte` LMPR-bracketed uniformly, `OUT_LEN` widened to 24-bit —
  **no 32 KB cap**. Design detail: `docs/specs/paged-out-design.md`.
- **ENCTAB / disasm / payloads:** allocated once at boot from the pool (they are
  resident-for-the-session), replacing their hardcoded page numbers.

### 4.5 The lifecycle — edit ⇄ assemble (composes with i40; lazy spill)

```
boot        : alloc ENCTAB, DISASM, payloads; size FREE pool
edit        : document grows  → alloc_page(DOC) per block split
              document shrinks → free_page on block merge / close
assemble    : assembler requests IN/OUT/SCRATCH via alloc_page.
              Eviction is LAZY — DOC pages spill to disk ONLY when FREE
              cannot satisfy a request:
                FREE non-empty → hand out a free page (no disk I/O)
                FREE empty     → serialize one DOC block to .tbn (i40),
                                 free_page(DOC), then satisfy the request
assemble end: free_page(IN/OUT/SCRATCH); reload ONLY the DOC blocks that
              were spilled (none if the pool never ran short)
```

This is i40's evict/reload (`editor-edit-model-design.md` §5.2) expressed in
allocator calls, made **demand-driven**. The key property: the editor's
document and the assembler's resident budget **coexist in RAM whenever the pool
is large enough** — on a 512 KB machine (~25 free pages) assembly touches the
disk **zero** times. Only when free pages run short (a large document on a
256 KB machine) does the editor spill document blocks, and only as many as the
assembler actually needs — so "all free RAM for the document" costs a disk
round-trip *exactly when, and only as much as, RAM pressure demands.* The spill
unit is one i40 block. Spilling itself is a downstream feature (see §5 / §7); an
unconditional refuse-with-message is the i2-baseline when no spill backend
exists.

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

The **i2 baseline is refuse-with-message** — no page-persistence backend. When
`alloc_page` finds no `FREE` page and no spill backend is available:

- **Document growth (editor):** refuse the insert with a clear "document full —
  N KB max on this machine" message; never silently truncate or clobber. On a
  256 KB machine this is a real ceiling (~144 KB); on 512 KB it is unlikely.
- **Assembly (IN/OUT):** with no spill backend the document stays resident, so
  exhaustion means the document **plus** the output exceed free RAM, surfaced as
  an assembler error with the byte count — the honest failure, not a wrap.

**Page persistence (spill) is a downstream feature, not i2** (Pete, 2026-06-22).
When present it makes `alloc_page` *lazy-spill* a cold DOC block (§4.5) instead
of refusing — to **Trinity if the machine has it, else a physical floppy/tape**,
so a space-limited user on any machine gains headroom. The assembler must run
fully on a floppy/tape-only machine with no Trinity, so the spill backend is
optional and pluggable. Tracked as a separate item that *depends on* i2 — never
blocks it.

## 6. Risks

- **Page-ownership correctness is load-bearing** — a double-free or a handout of
  a reserved page corrupts BASIC/DOS/screen. The `page_owner[]` table + tag
  assertions are the guard; this wants a boot self-test (claim-all/free-all
  round-trip; assert reserved pages never move) in the harness.
- **256 KB headroom is genuinely tight** — ~9 free pages must cover ENCTAB +
  disasm + payloads + the document. The lazy edit/assemble time-share (§4.5)
  makes it fit; on the i2 baseline (no spill backend) a document that cannot
  coexist with the assembler's IN/OUT hits the honest refuse-with-message
  ceiling, which the downstream spill feature (§5/§7) later lifts.
- **Bracket discipline** — every pool page is reached through an LMPR/HMPR
  bracket; the existing `reader.asm`/`emit_byte` patterns generalise, but the
  N-page OUT/IN lists add bracket sites to audit.
- **Screen-tail / sysvar safety** — the 8 KB screen-tail reclaim (§2, §4.2.4)
  must be **boot-validated**, never assumed. The SAM system variables all live
  at `&5xxx` → physical page 1 (`sam-paging.md §7`), *not* in the screen pages,
  so they are not at risk in the tail — but the exact screen page (`VMPR`) and
  DOS page vary, so the survey confirms the tail is neither `DOSFLG`'s page nor
  `ALLOCT`-marked before use, and leaves it reserved on any doubt. Trampling a
  ROM-used byte is the RAM equivalent of clobbering a shared SD record.

## 7. Decision (q36, resolved 2026-06-22)

Pete steered the foundational choice. **The IDE adopts the single dynamic page
pool shared by the editor *and* the assembler (this proposal, §4)** —
superseding the assembler's hardcoded off-axis pages 4–15 and lifting the fixed
96 KB-IN / 32 KB-OUT ceilings (folding i23/i24). Any subsystem allocs and frees
pages; a subsystem that requests a page and cannot get one is responsible for
raising its own out-of-memory error.

Sub-decisions:

1. **Minimum target machine** — **256 KB is first-class** (≈144 KB document
   ceiling). The lazy spill (§4.5) is the headroom mechanism; 512 KB is not a
   floor.
2. **Pool-exhaustion on a full document** — **refuse-with-message** is the i2
   baseline (§5). **Page persistence / spill is a downstream feature, NOT i2** —
   a separate item that *depends on* i2 (spill to Trinity if present, else
   floppy/tape; the assembler must run fully without Trinity). It is added later
   so a space-limited user on any machine gains headroom, but it never blocks
   i2.
3. **`ALLOCT` coexistence / BASIC** — **the IDE owns allocation for its
   session** and **`NEW`s BASIC at startup** to reclaim its program/variable
   pages (§2, §4.2). There is no reason to preserve a user's BASIC program: on an
   autoboot disk the boot stub just `CALL`s the IDE, and a hand-loaded user can
   `NEW` after exit anyway. The IDE keeps only the system-critical low page
   (`&5xxx` sysvars + stack + ROM/DOS workspace), the `DOSFLG` page, the screen
   pages, and its code page reserved. On exit it leaves BASIC in that clean `NEW`
   state with consistent `ALLOCT`/`LASTPAGE`/`RAMTOP` — simpler than restoring a
   program. The 8 KB screen-tail (§2) is a bonus fixed scratch region, always
   boot-validated so the ROM's system variables (which live in page 1, never the
   screen pages) are never trampled.

This is the model i41 §5.2 already assumes, and it delivers the i23/i24 ceiling
lifts for free.

## 8. Relationship & lifecycle

- **Builds on:** `docs/notes/sam-paging.md`, `docs/notes/memory-layout.md`,
  `docs/specs/editor-edit-model-design.md` (i41), `docs/specs/comment-storage-design.md` (i40).
- **Lifts when adopted:** i23 (IN ceiling), i24 (OUT ceiling) fold into the
  dynamic pool rather than being patched in place.
- This is a living design doc (evergreen filename). When the model ships, its
  durable rationale folds into `docs/ARCHITECTURE.md` and this file is deleted in
  that PR, per the doc-lifecycle rules.
