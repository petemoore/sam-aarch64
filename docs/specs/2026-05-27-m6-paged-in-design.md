# M6 PR 2 — paged IN buffer design

**Status**: design spec.  Pairs with `docs/specs/2026-05-27-m6-paged-out-design.md` (the OUT-side counterpart) and `docs/specs/2026-05-27-samdos-load-idiom.md` (the COMET-style HLOAD trampoline pattern).  Date: 2026-05-27.

This is the design for the second M6 PR.  Scope: relocate the IN `.tbn` buffer out of section C so that inputs larger than 2 KB are possible.  Compaction of the `.tbn` format itself is a separate strand (M6 PR 3 — `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`).

## Goal

Allow the SAM-side assembler to load and walk a `.tbn` source of up to ~64 KB.  Real target: the spectrum4 `release.tbn` after the compact-`.tbn` strand lands (today ~440 KB in the fat format — well beyond what we can ever hold resident; the M6 PR 3 compaction work brings it to a few tens of KB).

The HLOAD call site is already known — `load_in_file` in `main_loop.asm:2041-2070`.  The trampoline-around-HLOAD pattern that paged ENCTAB into physical page 4 (PR #31) generalises directly to landing IN in pages 7+.  The runtime read path is the interesting part: pass 1 and pass 2 each walk the entire `.tbn` record-by-record, AND the encoder needs ENCTAB live in section A throughout pass 2 (LMPR = `LMPR_ENCTAB`).  Neither section A nor section B is "free" for a flat IN window during the assemble; we have to stage records.

## Current state — IN load path

| Concern | Location | Behaviour |
|---|---|---|
| IN buffer constants | `src/m3/assembler.asm:31-32` | `IN_BUF = &B000`, `IN_BUF_END = &B800` (2 KB in section C) |
| Disk load | `src/m3/main_loop.asm:2041-2070` (`load_in_file`) | HGTHD → read DIFA from `&4B50+34..36` → direct HLOAD into `IN_BUF` (no trampoline; HMPR unchanged) |
| Pass driver | `src/m3/main_loop.asm:112-150` (`main_assemble`) | `load_in_file` → `enctab_map_in` → pass 1 walk → pass 2 walk → `enctab_map_out` → save.  Disk load happens BEFORE ENCTAB is mapped into section A. |
| Pass reset | `src/m3/main_loop.asm:164-167` (`reset_reader_to_in_buf`) | `IN_POS := IN_BUF; reader_init`.  Called once per pass; pass 2 simply rewinds `IN_POS` to the same in-memory buffer. |
| Reader globals | `src/m3/main_loop.asm:2144-2145` | `IN_POS defw 0`, `IN_END defw 0` (both section-C addresses today). |
| Header skip + walk | `src/m3/reader.asm:46-153` | `reader_init` validates `SA64` magic, version, skips the u16-prefixed name table, leaves `IN_POS` at the first record's `kind` byte.  `reader_next_kind` reads `[kind][len_lo][len_hi]`, returns payload pointer in HL, advances `IN_POS` by `3 + len`. |
| Record consumers | `main_loop.asm` instruction / directive / label / local-def handlers | Receive HL = payload pointer, walk it for operand bytes / mnemonic id / symbol id.  Pointers are not retained past the current record's processing… |
| …except litpool | `src/m3/litpool.asm:148-260` (`litpool_register`) and `:707-841` (`litpool_emit_by_width`) | Pass 1 records `expr_ptr` (a section-C pointer into the operand bytecode in IN) in `LITPOOL_TABLE[slot]+1..2`.  Pass 2 reads from this pointer at `.ltorg` / implicit flush time to evaluate and emit the pool entry.  **This is the only cross-record retention of an IN pointer.** |
| Other places that take IN pointers | `sysname_ptr` (`sysname.asm:799`), `main_payload_ptr` (`main_loop.asm:2111`), `reader_curr_payload` (`reader.asm:161`) | All single-record lifetime; consumed within the same record handler that wrote them.  None survive pass-2 record advance. |

So today's invariant is "the whole `.tbn` is resident in section C, and any IN address is dereferenceable any time".  Paged IN means we have to either keep the whole file reachable through a stable window (impossible if it exceeds one LMPR-controlled section) or break the "any address any time" assumption — every IN pointer becomes scoped to its enclosing record handler, and litpool-style cross-pass retention requires explicit copy-out.

## The COMET source pattern (reference)

Source: `reference/comet-decoded/comet.asm`.  COMET stores source text as a long linked list of `[len][bytes…]` lines spread across multiple physical pages.  Walking primitives (`comet.asm:3030-3049`):

```asm
findnext:      LD   A,(endp)
               LD   C,A
               LD   DE,(endo)
               CALL getcurpo            ; AHL = (page, offset) of current line
               CALL testpo
               CCF
               RET  C
               LD   B,0
               LD   C,(HL)              ; line length
               RES  7,C
               ADD  HL,BC               ; HL += length → next line
               CALL adjustpo            ; normalise (page, offset); bump page if H >= 64
               LD   B,A
               IN   A,(250)
               ADC  0                   ; advance LMPR low bits on carry
selsetcurpo:   OUT  (250), A            ; remap section A (LMPR-controlled)
               LD   A,B
setcurpo:      LD   (curradp),A
               LD   (currado),HL
               RET
```

The source addresses are 24-bit `(page, offset)` pairs throughout (`startp+starto`, `curradp+currado`).  `adjustpo` (`comet.asm:3180-3188`) normalises `HL` back into `&0000-&3FFF` (section A) by subtracting `&4000` and incrementing the page register until `H < 64`.  COMET keeps the **current source page mapped into section A via LMPR**, OR'd with `32` (the RAM0 bit), and re-maps lazily when a page boundary is crossed (`comet.asm:3041-3045` — `adc 0` advances the LMPR low bits by 1 when adjustpo decremented H below 0).

The key COMET insight is that **source-walk only needs the current and next page mapped at any moment**, because record sizes are far smaller than a page.  Walking is per-byte from a stable section-A address, with rare (≪1 per record on average) LMPR swaps on page crossings.

We cannot copy COMET wholesale because section A is committed to ENCTAB during the assemble.  But COMET's "page+offset cursor, lazy LMPR swap on boundary" is the right shape for an IN cursor — we just need a different stage / window.

## HLOAD trampoline — multi-page support

`src/m3/loader.asm:131-143` and `src/m3/trampoline.asm:286-330` document the HLOAD trampoline calling convention.  HLOAD itself supports multi-page loads via SAMDOS's internal `ctas` (`samdos/src/c.s:354-369`): when the destination HL crosses `&C000`, SAMDOS resets `H` to `&80` and increments HMPR's low 5 bits (preserving top 3).  So a multi-page HLOAD started at HL=`&8000` with HMPR=N continues into HMPR=N+1, then N+2, …, automatically.

The UIFA bytes that drive this are populated by HGTHD (samdos copies them from the file's DIFA into `UIFA + 80 = &4B50`):

| UIFA byte | Meaning (`samdos/src/h.s:336-359 hconr`) | Value in our case |
|---|---|---|
| `UIFA + 31` (`page1`) | Destination start page (HMPR low 5 bits) | `IN_BASE_PAGE` (= 7) |
| `UIFA + 32..33` (`hd0d1`) | Destination start offset (HL on entry to HLOAD) | `&8000` (section-C window base) |
| `UIFA + 34` (`pges1`) | Whole-16-KB pages of payload (`& 0x1F`) | from DIFA at `&4B50 + 34` |
| `UIFA + 35..36` (`hd0b1`) | Length modulo 16 KB (`set 7,d` marker cleared by SAMDOS in `hconr`) | from DIFA at `&4B50 + 35..36` |

`load_in_file` already pulls `pges1` and `hd0b1` from the SAMDOS-deposited copy at `&4B50` (`main_loop.asm:2048-2056`).  It currently does a direct `rst 8 / defb HOOK_HLOAD` with HL=`IN_BUF` (= `&B000`, in section C running on the assembler's HMPR page).  Switching to the trampoline gets us a multi-page load into ANY contiguous run of physical pages.

The trampoline calling convention (`trampoline.asm:292-297`):

```
HL = &8000..&BFFF        ; HLOAD section-C window (use &8000 — page-aligned)
B  = target start page   ; IN_BASE_PAGE
IX = UIFA pointer        ; already set by fill_uifa
C  = pages count         ; from DIFA+34 (in_file_pages today)
DE = length modulo 16K   ; from DIFA+35 (in_file_len today, bit 7 of D cleared)
```

The trampoline reprograms HMPR around the RST 8, restores it on exit.  SAMDOS auto-pages HMPR across the `&C000` boundary internally, so a multi-page load to HMPR=N actually leaves the SOURCE FILE spread across physical pages N, N+1, …, N+(C-1), with `(C-1)*16K + DE` bytes total.  On HLOAD return, HMPR is restored to whatever the trampoline saved — for our flow that's the boot HMPR (assembler's page).

## Design

### Storage — Option A: HLOAD straight into pages 7+

IN lands in **physical pages 7..10**.  Four pages = 64 KB ceiling.  That's the cap on `.tbn` size; spectrum4 release.tbn under the compact-`.tbn` strand fits.

| Page | Owner | Mapped via |
|---|---|---|
| 4 | ENCTAB | LMPR_ENCTAB = `&24` → section A |
| 5 | OUT low zone | LMPR_ENCTAB → section B auto-maps to page 5 (LMPR+1) |
| 6 | OUT high zone | LMPR_OUT_HIGH = `&25` → section B |
| **7..10** | **IN, 4 contiguous pages** | LMPR_IN_LO = `&27`, LMPR_IN_HI = `&29` etc — see below |

Pages 7..12 are still in the "00H unused" range per Tech Manual page-allocation table cited at `trampoline.asm:188-208`.  Pages 11-12 are reserved as headroom but the design supports only IN ≤ 64 KB out of the gate.

**Why not load chunked, reusing the existing &B000 buffer?**  That's the alternative Option B.  It avoids consuming 4 extra physical pages but pays for it with constant re-HLOAD churn: pass 2 needs to start over from the file's first record, so we'd HLOAD the first chunk twice (once for pass 1, again for pass 2), and within each pass we'd need a sliding-window re-load every 2 KB.  HLOAD has the file open via HGTHD's `svde` (`samdos/src/c.s:1486`), but seeking back to an arbitrary record offset is not a SAMDOS operation — we'd need to re-issue HGTHD between passes (re-reads the file's track+sector header from disk) and then HLOAD-from-start with a buffer offset, which is also unsupported.  In practice Option B forces "two passes × N chunks × disk re-reads"; even with a fast emulator that's tens of seconds per assemble.  Option A loads once, runs both passes from RAM, costs only the LMPR-bracket on per-byte IN reads.

**Why not the hybrid Option C (paged backing + section-C scratch window)?**  We *do* use a section-D record-staging buffer (see below), but we keep the *bulk store* in pages 7-10 unconditionally — Option C-with-disk-as-backing-store would re-introduce Option B's HLOAD-per-window cost.  Option A + record staging is the same fast-load-once shape we already use for ENCTAB.

### Read mechanism — record staging in section D

Section A is committed to ENCTAB throughout `main_assemble`'s pass loop.  Section B is committed to OUT (page 5 via LMPR_ENCTAB+1; page 6 via LMPR_OUT_HIGH).  Section C runs the assembler code.  Section D is `&C000-&FFFF`, holding the stack and scratch — that's our only LMPR-stable window with room, but it's also HMPR-controlled and shares a physical page with the assembler.

So no section can hold IN at a stable mapping during the encoder window.  We have to either (i) snapshot records into a scratch buffer before entering the encoder, or (ii) bracket every IN read with an LMPR swap (breaks the ENCTAB-A invariant inside the bracket; expensive in T-states per byte).

**We pick (i): per-record staging into section-D scratch.**

`reader_next_kind` is the natural choke point.  At each call:

1. Read 3 bytes `[kind][len_lo][len_hi]` from IN at the current cursor via a brief LMPR-bracket that maps the current IN page into **section A**.  Within the 3-byte read, ENCTAB is temporarily displaced from A; the encoder is NOT running yet (we're between records), so this is safe.
2. Compute payload length `BC = u16(len_lo, len_hi)`.
3. Allocate `BC` bytes in the section-D staging buffer at `STAGING_BUF` (= `&D500`).
4. Copy the payload bytes from IN (current section-A mapping) into `STAGING_BUF` via a tight LDIR-style loop, handling page-cross by re-mapping LMPR on `H == &40` (i.e. when the source pointer would advance into section B from section A).
5. Restore LMPR to `LMPR_ENCTAB` (ENCTAB back in section A).
6. Advance the logical IN cursor.
7. Return `A = kind`, `BC = payload length`, `HL = STAGING_BUF` (a stable section-D address).

Existing callers see exactly the same calling convention as before — `HL` is a flat address they can walk byte-by-byte.  The address just happens to land in `&D500` instead of `&B0xx`.

**Why section A and not section B for the IN window?**  Section B is OUT.  We could in principle bracket OUT-state too (save LMPR, swap, read IN-from-B, restore LMPR_ENCTAB), but that's two bracket layers when reading + writing happen close together.  Section A is simpler: ENCTAB is the thing we displace, ENCTAB is only read by `form_lookup_match` and the encoder slots — none of which fire DURING `reader_next_kind`.  The two-record-walk handlers (label-def, local-def, comment skip, …) do not touch ENCTAB either.  Pass 1 walks records reading mostly mnemonic-id / op-count bytes; pass 2 walks records and only enters ENCTAB-reading code via `form_lookup_match` AFTER the record is staged.

So the bracket nests cleanly:

```
   enctab_map_in              (LMPR = LMPR_ENCTAB; section A = ENCTAB)
       ...
       reader_next_kind:
           LMPR := LMPR_IN_LO + record_page   ; section A = IN page N
           copy [kind][len_lo][len_hi] + payload from section A into STAGING_BUF
           LMPR := LMPR_ENCTAB                 ; section A back to ENCTAB
       ...
       form_lookup_match / encoder reads ENCTAB freely
       emit_byte writes OUT via section B
       ...
   enctab_map_out             (LMPR = LMPR_DEFAULT)
```

### `IN_POS` — logical 24-bit cursor

The flat 16-bit `IN_POS` becomes a `(page, offset)` pair:

```
IN_POS_PAGE    : defb 0     ; LMPR low 5 bits (with RAM0 bit set) for the current
                            ; IN page.  Initialised to LMPR_IN_BASE (= &27).
                            ; Incremented on page cross.
IN_POS_OFFSET  : defw 0     ; offset inside the current page (&0000..&3FFF).
                            ; When IN_POS_OFFSET reaches &4000, page bumps and
                            ; offset wraps to &0000.
```

`IN_END` is similarly stored as `IN_END_PAGE` + `IN_END_OFFSET`.

Helpers:

- `in_pos_normalise` — if `IN_POS_OFFSET >= &4000`, subtract `&4000` and increment `IN_POS_PAGE`'s low 5 bits (preserving the RAM0 bit).  Loop until normalised.  (Mirrors COMET's `adjustpo` at `comet.asm:3180`.)
- `in_pos_at_end` — compare `(IN_POS_PAGE, IN_POS_OFFSET)` vs `(IN_END_PAGE, IN_END_OFFSET)`; Z=1 if equal.
- `in_pos_read_into_de` — set DE := `&0000 + IN_POS_OFFSET` (in section-A address space) and emit the LMPR `OUT (250), A` to map the current IN page in.  Caller then reads through DE.
- `in_pos_advance_bc` — `IN_POS_OFFSET += BC`; renormalise.

The reader changes are concentrated in `reset_reader_to_in_buf`, `reader_init`, `reader_at_end`, `reader_next_kind`.  Everything past `reader_next_kind` is untouched — the staging buffer hands HL to the record handlers identically.

### Pass 1 → pass 2 reset

Pass 2 must walk the SAME records pass 1 saw.  Two options:

- **(R1) Re-load from disk for pass 2.**  Re-issue HGTHD+HLOAD-trampoline at the top of pass 2.  Cost: one extra disk read of the entire file (tens of ms on a real SAM; cheap on SimCoupé).  Simple, low code.
- **(R2) Rewind the in-memory paged cursor.**  Just reset `(IN_POS_PAGE, IN_POS_OFFSET) := (IN_BASE_PAGE, 0)` and re-walk the same RAM.  No disk hit.  Slightly more code (we need to remember `IN_BASE_PAGE` and `IN_END_PAGE`/`OFFSET` set at load time).

**We pick R2 (rewind in-memory).**  Rationale: we already paid the cost of loading the file into resident pages 7-10; throwing that away and re-reading would be silly.  R2 is also robust against a hypothetical SAMDOS quirk where a second HGTHD between two HLOADs leaves the open-file state in a weird half-step (we don't HAVE this bug today, but R2 sidesteps it entirely).

The litpool concern (`expr_ptr` is a section-C pointer captured during pass 1 and dereferenced during pass 2) is addressed separately — see "Cross-pass IN retention: litpool" below.

### Cross-pass IN retention: litpool

`litpool_register` is called from pass 1 with `HL = expr_ptr` (section-C IN address) and `BC = expr_len`.  It stores both in `LITPOOL_TABLE[slot]+1..4`.  `litpool_emit_by_width` in pass 2 reads back the stored pointer and calls `eval_expr_const` to walk it.

Under paged IN, the section-C pointer pass 1 captured points into the staging buffer, which is overwritten by the time pass 2's `litpool_flush` fires.  Even if pass 2 re-walks records, the staging buffer that held the litpool's original record is long-gone by the time litpool_flush emits a slot (flushes happen at `.ltorg` boundaries or end-of-source, NOT at the litpool's record boundary).

**Solution: copy expr bytecode into a dedicated litpool-data buffer at registration time.**

Add a `LITPOOL_EXPR_BUF` region in section D (32 slots × max 64 B/slot = 2 KB) at `&D900..&E0FF`.  `litpool_register` allocates the next free chunk, copies `expr_len` bytes from the staging-buffer expr into the chunk, and stores the chunk's section-D address as `expr_ptr` (instead of the IN pointer).  `litpool_emit_by_width` reads from the section-D copy in pass 2 — page-stable for the lifetime of the assemble.

Capacity: 32 slots × 64 B = 2 KB.  Real expr_lens are short (typically 1-8 bytes of bytecode for a literal); 64 B/slot is over-provisioned.  If we need more headroom, the cap is the section-D space budget — section D currently uses `&C100..&D485` (~5 KB) per `assembler.asm:36-75`, leaving `&D486..&FFFF` (~11 KB) free.  2 KB at `&D900..&E0FF` keeps comfortable headroom for STAGING_BUF (1 KB at `&D500..&D8FF`).

If `expr_len` exceeds the per-slot cap, we'll need either dynamic allocation (linked-list packing) or a larger pool.  M5 / M6 fixtures all have short literal exprs; over-budget would fail loudly via a `jp fail` on the copy bound check.

### Interaction with the OUT buffer (PR #36 already landed)

OUT lives in pages 5 + 6 reached via section B (low zone = LMPR_ENCTAB+1 auto-map; high zone = `LMPR_OUT_HIGH = &25`).  IN's section-A bracket DOES NOT touch LMPR's auto-mapped section-B page — section B during LMPR=`&27` (IN_BASE_PAGE=7) maps to page 8 (`LMPR + 1`), not page 5.

So during a `reader_next_kind` LMPR bracket:
- LMPR=`&27` → section A = page 7 (IN), section B = page 8 (IN+1)
- OUT writes during this window would land in page 8 instead of page 5 → wrong page.

Mitigation: **the IN LMPR bracket is fully encapsulated inside `reader_next_kind`**.  No `emit_byte` calls happen during the bracket.  We restore LMPR to `LMPR_ENCTAB` before returning to the caller, exactly the way `emit_byte`'s high-zone bracket restores LMPR_ENCTAB before returning (`encoder.asm:467`).

Concrete invariant for paged IN PR:

> `reader_next_kind` must restore LMPR to its on-entry value before returning.  Callers see LMPR_ENCTAB live across the call.

### Interaction with ENCTAB (PR #31 already landed)

ENCTAB lives in page 4 mapped into section A via `LMPR_ENCTAB = &24`.  During the `reader_next_kind` IN bracket, LMPR temporarily holds `LMPR_IN_BASE + page_offset` (e.g. `&27` for page 7).  ENCTAB is displaced from section A during this bracket.

Because `reader_next_kind` does not call into the encoder / `form_lookup_match` / any other ENCTAB consumer during its bracket, this is safe — same pattern as `emit_byte`'s high-zone LMPR bracket (which displaces page 5 from section B during a page-6 write, but doesn't call OUT-low consumers during the bracket).

The interrupt-disable invariant is also preserved.  `enctab_map_in` (`trampoline.asm:347-354`) leaves interrupts DI'd; `reader_next_kind` doesn't touch IFF1.

### Memory layout impact (`assembler.asm`)

Drop:

- `IN_BUF`   (`assembler.asm:31`)   — old section-C IN base.
- `IN_BUF_END` (`assembler.asm:32`) — old section-C IN end.

Add (in `assembler.asm` near the existing section-D scratch declarations):

```asm
; IN buffer paging — see docs/specs/2026-05-27-m6-paged-in-design.md.
;   pages 7..10  ── IN .tbn (HLOAD destination); 64 KB ceiling
;   STAGING_BUF  ── per-record staging window in section D
;   LITPOOL_EXPR_BUF ── cross-pass copy of litpool expr bytecode

STAGING_BUF:           equ     &D500          ; 1 KB record staging area
STAGING_BUF_END:       equ     &D900
LITPOOL_EXPR_BUF:      equ     &D900          ; 2 KB cross-pass expr pool
LITPOOL_EXPR_BUF_END:  equ     &E100
```

In `trampoline.asm` (alongside `OUT_BASE_PAGE` and `LMPR_OUT_HIGH`):

```asm
IN_BASE_PAGE:    equ     7              ; first physical page of IN
LMPR_IN_BASE:    equ     &20 + IN_BASE_PAGE
                                        ; = &27; section A = page 7 (IN[0])
```

In `main_loop.asm` (replacing the existing `IN_POS defw 0 / IN_END defw 0`):

```asm
IN_POS_PAGE:    defb    0             ; current LMPR low5+RAM0 for IN
IN_POS_OFFSET:  defw    0             ; current offset inside that page (&0000..&3FFF)
IN_END_PAGE:    defb    0             ; last byte's page (computed from in_file_len + IN_BASE_PAGE)
IN_END_OFFSET:  defw    0             ; last byte's offset
```

### Why-not — alternatives considered

| Alternative | Rejected because |
|---|---|
| **Section B + LMPR_ENCTAB+1 for IN** (mirror OUT's free-mapping) | Section B is page 5 = OUT-low.  We'd have to time-share between IN reads and OUT writes — every emit would clobber the IN window.  emit_byte is on the hot path; making it pay an IN-LMPR-restore per byte is unacceptable. |
| **Section C via HMPR** (use HMPR to point at IN) | The running assembler code is in section C.  Swapping HMPR yanks the instruction stream.  Same issue that motivated the COMET-style trampoline for HLOAD. |
| **No staging — bracket every IN byte read with LMPR swap** | Pass-1's per-byte payload walk would pay ~60 T-states per byte.  Real release.tbn is ~20-40 KB compacted; that's ~1-2 M T-states ≈ 300-600 ms extra per pass, ×2 passes.  Staging amortises the bracket over a whole record (~20 bytes typical) — 3-5× speedup. |
| **Re-load from disk for pass 2 (R1)** | Throws away the resident IN we already loaded.  Disk reads are fast in SimCoupé but slow on real SAM; R2 is strictly better. |
| **Hold all of IN in a flat section-A window via LMPR_IN_BASE during the entire walk, displacing ENCTAB** | Then `form_lookup_match` and encoder slots can't read ENCTAB.  Either we'd bracket every ENCTAB read (and there are HUNDREDS per record) or we'd split the pass into a "decode IN to canonical record form" phase + "encode from canonical form" phase, doubling the per-record processing.  Staging is simpler. |

### LMPR / HMPR state table

| Stage | LMPR | HMPR | Section A | Section B | Notes |
|---|---|---|---|---|---|
| Start of `main_assemble` | LMPR_DEFAULT | (assembler page) | ROM0 | (varies; BASIC sys page) | Captured at boot. |
| **`load_in_file` (paged)** | LMPR_DEFAULT | (assembler page during call; trampoline swaps to IN_BASE_PAGE around RST 8) | ROM0 | (BASIC sys page) | Trampoline used so HMPR can point at IN_BASE_PAGE during the multi-page HLOAD without yanking the running code (which lives in section C under HMPR=assembler-page).  Wait — see "Implementation detail: `load_in_file` already runs in section C" below. |
| `enctab_map_in` | LMPR_ENCTAB = `&24` | (assembler page) | ENCTAB (page 4) | page 5 (OUT-low) | |
| Pass 1 / pass 2 record handler entry | LMPR_ENCTAB = `&24` | (assembler page) | ENCTAB | page 5 (OUT-low when pass 2 emitting) | |
| **`reader_next_kind` IN bracket** | LMPR_IN_LO + record_page (e.g. `&27`) | (assembler page) | IN page N | IN page N+1 | Bracket window: ENCTAB temporarily displaced, OUT temporarily displaced.  No emit / no ENCTAB read during the bracket. |
| `reader_next_kind` returns | LMPR_ENCTAB = `&24` | (assembler page) | ENCTAB | page 5 | Restored before return. |
| `emit_byte` (low zone) | LMPR_ENCTAB = `&24` | (assembler page) | ENCTAB | page 5 (OUT-low) | Untouched by IN PR. |
| `emit_byte` (high zone) | inner bracket = `&25`; outer = `&24` | (assembler page) | ENCTAB | page 6 (OUT-high) inside bracket | Existing PR #36 behaviour. |
| `enctab_map_out` | LMPR_DEFAULT | (assembler page) | ROM0 | BASIC sys page | Restored before save. |
| `save_out_file` | LMPR_DEFAULT | (HSAVE-managed) | ROM0 | BASIC sys page | HSAVE OUTs HMPR per UIFA[31]; LMPR unchanged. |

Port-correctness: every `OUT (port), A` referenced in the table is port **250 (LMPR)**, except `save_out_file`'s HMPR which is internal to HSAVE.  Citation: `trampoline.asm:353` (LMPR write), `trampoline.asm:309` (HMPR read), and the M6 PR 1 `emit_byte` code at `src/m3/encoder.asm:459-467` (post-PR #36).  The spec doc that pre-dates PR #36 has the wrong port (251 vs 250) and must not be trusted.

### `load_in_file` — paged variant

Today's `load_in_file` (`main_loop.asm:2041-2070`) loads into the section-C `IN_BUF`.  Replace with `load_in_file_paged` that uses the existing `TRAMPOLINE_DST` trampoline:

```asm
; load_in_file_paged — HGTHD + trampoline-HLOAD "IN" into pages 7..10.
;
; Sets IN_POS_PAGE / IN_POS_OFFSET / IN_END_PAGE / IN_END_OFFSET so the
; reader can walk the loaded source via section-A LMPR-brackets.
load_in_file_paged:
                ld      hl, name_IN
                call    fill_uifa
                rst     8
                defb    HOOK_HGTHD          ; longjmps on file-not-found

; Read length-mod-16K from the SAMDOS-deposited header at &4B50+35.
                ld      hl, (&4B50 + 35)
                ld      a, h
                and     &7F                 ; clear SAMDOS's `set 7, d` marker
                ld      h, a
                ld      (in_file_len), hl

; Read page count from &4B50+34 (low byte only).
                ld      a, (&4B50 + 34)
                ld      (in_file_pages), a

; Call the section-B trampoline.
;   HL = &8000      (section-C window, satisfies HLOAD constraint)
;   B  = IN_BASE_PAGE
;   C  = pages count
;   DE = length-mod-16K
;   IX = UIFA (already set by fill_uifa)
                ld      hl, &8000
                ld      b, IN_BASE_PAGE
                ld      a, (in_file_pages)
                ld      c, a
                ld      de, (in_file_len)
                call    TRAMPOLINE_DST

; Compute (IN_END_PAGE, IN_END_OFFSET) = IN_BASE_PAGE + total_bytes.
                ld      a, IN_BASE_PAGE
                ld      hl, (in_file_pages) ; A = pages (low byte)
                ld      h, 0
                ld      l, a                ; HL = pages
                ld      a, (in_file_pages)
                add     a, IN_BASE_PAGE
                ld      (IN_END_PAGE), a    ; pages count + base page
                ld      hl, (in_file_len)
                ld      (IN_END_OFFSET), hl ; offset within that page
                ret
```

The end position is `IN_BASE_PAGE + pages` (page count from DIFA byte 34) + `length_mod_16k` (DIFA 35-36) bytes into that page — i.e. the last byte sits in the same page as `IN_BASE_PAGE + pages` with offset `in_file_len`.  If `in_file_len == 0` the file size is exactly `pages * 16384` and the end is the first byte of `IN_BASE_PAGE + pages` (which doesn't exist; same as the "one past end" semantic the existing `IN_END = IN_BUF + filesize` carries).

### `reset_reader_to_in_buf` — paged variant

```asm
reset_reader_to_in_buf:
                ld      a, &20 + IN_BASE_PAGE   ; = LMPR_IN_BASE = &27
                ld      (IN_POS_PAGE), a
                ld      hl, 0
                ld      (IN_POS_OFFSET), hl
                jp      reader_init             ; tail-call; reads header
```

### `reader_init` — paged variant

The header skip (validate `SA64` magic, version, skip name-table) now reads via the IN-bracket primitives:

```asm
reader_init:
; Map current IN page into section A.
                call    in_map_current

; Validate 4-byte magic "SA64".
                ld      hl, 0           ; section-A address of IN[0]
                ld      a, (hl)
                cp      "S"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "A"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "6"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "4"
                jp      nz, fail
                inc     hl

; Validate version u16 LE = 1.
                ld      a, (hl)
                cp      1
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                or      a
                jp      nz, fail
                inc     hl

; Skip flags u16.
                inc     hl
                inc     hl

; Name table — same logic as today but via paged read.
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (reader_name_count), de

reader_init_skip_names_paged:
                ld      a, d
                or      e
                jr      z, reader_init_done_paged
                push    de
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
; HL += BC — may cross page boundary.
                add     hl, bc
                call    in_normalise_hl     ; renormalise into section A
                pop     de
                dec     de
                jp      reader_init_skip_names_paged

reader_init_done_paged:
; Persist position back to IN_POS_PAGE / IN_POS_OFFSET.
                call    in_persist_hl
                call    enctab_map_in       ; restore LMPR_ENCTAB
                ret
```

`in_map_current` does `LD A, (IN_POS_PAGE); OUT (250), A` (port 250 = LMPR).  `in_normalise_hl` is the COMET-style adjust: while H >= &40, subtract &40 from H, increment LMPR low 5 bits (preserving RAM0), `OUT (250), A`.  `in_persist_hl` writes `IN_POS_OFFSET := HL` and `IN_POS_PAGE := current LMPR`.

### `reader_next_kind` — paged variant

```asm
reader_next_kind:
; Map current IN page; read kind+len header at IN_POS.
                call    in_map_current
                ld      hl, (IN_POS_OFFSET)         ; HL = section-A offset
                ld      a, (hl)
                ld      (reader_curr_kind), a
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                ld      (reader_curr_len), bc

; Copy payload from section A to STAGING_BUF via LDIR-style with page-cross.
                ld      de, STAGING_BUF
                ld      (reader_curr_payload), de
                ld      a, b
                or      c
                jr      z, reader_next_kind_no_payload

reader_next_kind_copy_loop:
                ld      a, h
                cp      &40                         ; about to cross into section B?
                jr      c, reader_next_kind_copy_ok
; Page-cross: renormalise (subtract &40, bump LMPR low 5 bits).
                sub     &40
                ld      h, a
                in      a, (250)
                inc     a                            ; LMPR low 5 bits += 1
                                                     ; (preserves RAM0 bit at &20)
                out     (250), a
reader_next_kind_copy_ok:
                ldi                                  ; (DE) := (HL); HL++; DE++; BC--
                ld      a, b
                or      c
                jr      nz, reader_next_kind_copy_loop

reader_next_kind_no_payload:
; HL now points one past the last payload byte in section A.  Persist
; back into IN_POS_PAGE / IN_POS_OFFSET.
                call    in_persist_hl

; Restore LMPR_ENCTAB before returning.
                call    enctab_map_in

; Set return registers.
                ld      hl, (reader_curr_payload)   ; HL = STAGING_BUF
                ld      bc, (reader_curr_len)
                ld      a, (reader_curr_kind)
                ret
```

The inner loop is one LDI per byte plus an `H ?= &40` test — about 30 T-states/byte, ~3× the cost of a plain LDIR.  We could push the page-cross check out of the inner loop (count bytes to next page-cross, LDIR that many, re-map, repeat) for a hot-path savings; defer that optimisation to a follow-on if needed.

### Pass 1 → pass 2 rewind

`main_assemble`'s pass 2 currently calls `reset_reader_to_in_buf` again (`main_loop.asm:138`).  Under paged IN that helper sets `(IN_POS_PAGE, IN_POS_OFFSET)` back to `(LMPR_IN_BASE, 0)` and re-walks `reader_init`.  No disk re-read.

### Litpool fix

`litpool_register` copies the expr bytecode into `LITPOOL_EXPR_BUF`:

```asm
; -- new helper used by litpool_register, called BEFORE storing expr_ptr
; in the table.  Input: HL = src (staging-buf addr), BC = expr_len.
; Output: HL = dest (section-D copy address).
litpool_copy_expr:
                ld      de, (litpool_expr_buf_top)  ; next free byte
                ld      a, d
                cp      &E1                          ; cap at &E100
                jp      nc, fail                     ; expr pool overflow
                push    de
                ldir                                 ; copy BC bytes from HL to DE
                ld      (litpool_expr_buf_top), de
                pop     hl                           ; HL = start of copy
                ret
```

Initialise `litpool_expr_buf_top := LITPOOL_EXPR_BUF` in `litpool_init`.  Reset to the same in pass 2 — pass 2 doesn't re-register slots, so no copy happens, and the pass-1 copies remain valid.

`litpool_register` is changed from:
```
                ld      (litpool_reg_expr_ptr), hl   ; HL = IN ptr
```
to:
```
                call    litpool_copy_expr            ; HL = section-D copy
                ld      (litpool_reg_expr_ptr), hl
```

`litpool_emit_by_width` is untouched — it reads `expr_ptr` from the table and dereferences it, now landing in `LITPOOL_EXPR_BUF` instead of IN_BUF.

### Boot-time self-tests

Add a self-test routine `run_in_paged_self_tests`:

1. Pre-load a synthetic 6-byte `.tbn` blob into section-D scratch.  Manually HLOAD-trampoline it to page 7 (via direct call to TRAMPOLINE_DST — yes, this means the test depends on TRAMPOLINE_DST being installed, so order after `enctab_trampoline_setup` and before `main_assemble`).
2. Call `reset_reader_to_in_buf` with synthetic `IN_END = (page=7, offset=6)`.
3. Assert `reader_init` doesn't `jp fail` on the synthetic magic.
4. Read one record via `reader_next_kind`; assert `A = kind`, `BC = len`, and STAGING_BUF byte 0 matches the expected first payload byte.
5. Assert LMPR has been restored to LMPR_ENCTAB on return (read port 250, compare to `&24`).

Even smaller self-test: just verify the page-cross helper.  Construct a fake `IN_POS_OFFSET = &3FFE`, call `in_normalise_hl` with `HL = &7FFE`, assert the new state is `(IN_POS_PAGE = IN_BASE_PAGE + 1, IN_POS_OFFSET = &3FFE)`.

Gate via `if defined(BUILD_TESTS)` per the existing pattern (M3/M4/M5 + M6 PR 1's `run_emit_paged_self_tests`).

### Fixtures

Add a fixture `tests/m6/sources/in_long_source.s` that emits a long enough source to require IN page-crossing (>16 KB of `.tbn`).  Use `.rept 4000` of a one-line instruction, or generate via shell helper if `.rept` isn't supported by `text2bin`.

Cross-fixture for ENCTAB×IN bracket: run M3/M4/M5 fixtures unchanged — they exercise both ENCTAB reads and IN reads through `reader_next_kind`, so any LMPR-bracket bug surfaces as a wrong byte.

## What's NOT in this PR

- **Compact `.tbn` format.**  Separate strand (M6 PR 3).  Today's fat .tbn for release would be 440 KB; paged IN with a 64 KB ceiling can't handle that alone.  The compaction strand brings real release into range.
- **IN > 64 KB.**  4 pages × 16 KB = 64 KB ceiling.  Debug builds (~274 KB even after compaction) need a streaming-from-disk variant of `reader_next_kind`, out of scope for M6.
- **Disassembler.**  Falls out of compact `.tbn`; separate strand.
- **Streaming read (re-HLOAD per chunk).**  Option B; rejected.
- **`(hksp)` error handling for HGTHD/HLOAD failures.**  Out of scope; current `jp fail` matches M3..M5 + PR #36 behaviour.
- **IN-page bookkeeping in ALLOCT.**  We never return to BASIC, so writing to the ALLOCT page-allocation table is not required (same posture as ENCTAB and OUT — `trampoline.asm:205-208`).

## Risks

1. **LMPR bracket bug in `reader_next_kind`.**  If the routine ever returns without restoring LMPR to LMPR_ENCTAB, the next ENCTAB read corrupts.  Mitigation: structure the routine so the `call enctab_map_in` is the last thing before the `ret`; cover with a boot self-test that reads port 250 after a record fetch.
2. **Page-cross off-by-one.**  If `in_normalise_hl` is called with `H == &40` but the test is `cp &40 / jr c` (i.e. unsigned-less-than-only), `H = &40` exactly maps to "section B, offset 0" — already past the section A boundary.  Spec: the test is `cp &40 / jr c` (less-than) → bump when `H >= &40`.  Off-by-one bug here moves the first byte of the next page into the LAST byte of the previous page, corrupting record boundaries.  Cover by walking a synthetic 2-page record in the self-test.
3. **Litpool expr pool overflow.**  32 slots × 64 B = 2 KB cap.  An over-long expr triggers `jp fail`.  Mitigation: track `litpool_expr_buf_top` and bound-check before each copy.  Real fixtures use very short exprs; budget headroom in section D is ample if we ever need to bump the cap.
4. **Code budget.**  Production code budget pre-PR #36 was 420 B headroom; PR #36 consumed ~100 B for paged OUT, leaving ~320 B.  Paged IN adds: `load_in_file_paged` (~80 B), `reader_next_kind` rewrite (+~70 B over current), `in_normalise_hl` / `in_map_current` / `in_persist_hl` helpers (~60 B), litpool copy helper (~30 B), reader_init rewrite (+~30 B) → ~270 B total.  Tight but fits.  If we overshoot, the section-A bracket can be pushed into a section-B helper (mirror of `TRAMPOLINE_DST` for HLOAD), shifting code out of section C.
5. **Self-test depends on HLOAD trampoline.**  The IN paged self-test loads a synthetic blob via the trampoline.  The trampoline is installed by `enctab_trampoline_setup` (`assembler.asm:184`); the IN self-test must run AFTER this call but BEFORE `load_enctab` displaces section A's bracket-window expectations.  Solution: place `run_in_paged_self_tests` AFTER `run_trampoline_self_tests` and BEFORE `main_assemble`, with a careful note in `assembler.asm`.

## Pre-built code snippets

### `in_map_current`

```asm
; Map IN's current page into section A by writing IN_POS_PAGE to LMPR.
;
; Input:    none.
; Output:   LMPR = IN_POS_PAGE (which has RAM0 bit set + page in low 5 bits).
;           Section A = current IN page.  Interrupts left as caller had
;           them (caller should have DI'd before entering the bracket;
;           enctab_map_in's DI already covers this in the pass-loop case).
; Clobbers: A.
in_map_current:
                ld      a, (IN_POS_PAGE)
                out     (250), a            ; port 250 = LMPR
                ret
```

### `in_persist_hl`

```asm
; Persist HL (a section-A offset inside the current IN page) back into
; IN_POS_OFFSET, and copy the current LMPR value into IN_POS_PAGE.
;
; Input:    HL = current section-A offset (in [&0000, &4000)).
; Output:   IN_POS_OFFSET := HL.  IN_POS_PAGE := current LMPR.
; Clobbers: A.
in_persist_hl:
                ld      (IN_POS_OFFSET), hl
                in      a, (250)
                ld      (IN_POS_PAGE), a
                ret
```

### `in_normalise_hl`

```asm
; If H >= &40, subtract &40 from H and bump LMPR's low 5 bits (preserving
; the RAM0 bit).  Loops until H < &40.  Mirrors COMET adjustpo.
;
; Input:    HL = section-A-ish offset (possibly >= &4000).
; Output:   HL normalised into [&0000, &4000); LMPR's low 5 bits bumped
;           appropriately.
; Clobbers: A.
in_normalise_hl:
in_normalise_loop:
                ld      a, h
                cp      &40
                ret     c                    ; H < &40 → done
                sub     &40
                ld      h, a
                in      a, (250)
                inc     a                    ; low 5 bits += 1; preserves
                                             ; the RAM0 bit at &20
                out     (250), a
                jr      in_normalise_loop
```

### `load_in_file_paged`

```asm
load_in_file_paged:
                ld      hl, name_IN
                call    fill_uifa
                rst     8
                defb    HOOK_HGTHD          ; 129; longjmps on file-not-found

                ld      hl, (&4B50 + 35)
                ld      a, h
                and     &7F                 ; clear SAMDOS's `set 7, d`
                ld      h, a
                ld      (in_file_len), hl

                ld      a, (&4B50 + 34)
                ld      (in_file_pages), a

                ld      hl, &8000           ; section-C window for HLOAD
                ld      b, IN_BASE_PAGE
                ld      a, (in_file_pages)
                ld      c, a
                ld      de, (in_file_len)
                call    TRAMPOLINE_DST      ; trampoline_hload in section B

; Compute IN_END.
                ld      a, (in_file_pages)
                add     a, IN_BASE_PAGE
                or      &20                  ; set RAM0 bit so the stored
                                             ; "page" byte is a complete LMPR value
                ld      (IN_END_PAGE), a
                ld      hl, (in_file_len)
                ld      (IN_END_OFFSET), hl
                ret
```

### `reader_next_kind` (paged)

```asm
reader_next_kind:
                call    in_map_current
                ld      hl, (IN_POS_OFFSET)
                ld      a, (hl)
                ld      (reader_curr_kind), a
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                ld      (reader_curr_len), bc

                ld      de, STAGING_BUF
                ld      (reader_curr_payload), de
                ld      a, b
                or      c
                jr      z, reader_next_kind_no_payload

reader_next_kind_copy_loop:
                ld      a, h
                cp      &40
                jr      c, reader_next_kind_copy_byte
                sub     &40
                ld      h, a
                in      a, (250)
                inc     a
                out     (250), a
reader_next_kind_copy_byte:
                ldi                          ; (DE) := (HL); HL++; DE++; BC--
                jp      pe, reader_next_kind_copy_loop   ; PE = BC != 0

reader_next_kind_no_payload:
                call    in_persist_hl
                call    enctab_map_in        ; LMPR back to LMPR_ENCTAB
                ld      hl, (reader_curr_payload)
                ld      bc, (reader_curr_len)
                ld      a, (reader_curr_kind)
                ret
```

(Note: `LDI` sets P/V = `BC != 0`, so `JP PE, …` continues until BC reaches zero — saves a `ld a, b / or c / jr nz` per iteration.  Citation: Z80 datasheet, LDI flag effects.)

### Litpool copy hook

```asm
; litpool_copy_expr — copy BC bytes from staging-buf (HL) to the section-D
; expr pool.  Bound-checks DE+BC against LITPOOL_EXPR_BUF_END so a long
; expr can't run off the pool.
;
; Input:  HL = src (staging-buf addr), BC = expr_len.
; Output: HL = dest (section-D copy address).  litpool_expr_buf_top
;         advanced by BC.
; Clobbers: A, BC (consumed by LDIR), DE.
litpool_copy_expr:
                ld      de, (litpool_expr_buf_top)

; Bound: verify (DE + BC) <= LITPOOL_EXPR_BUF_END.  Compute end via
;   end = DE + BC; if end > LITPOOL_EXPR_BUF_END → fail.
                push    hl                          ; preserve src
                push    bc                          ; preserve len for LDIR
                ld      h, d
                ld      l, e
                add     hl, bc                      ; HL = DE + BC = post-copy top
                ld      bc, LITPOOL_EXPR_BUF_END
                or      a                           ; clear CF
                sbc     hl, bc                      ; HL = (DE + BC) - END
                jp      nc, fail                    ; post-copy top > END → overflow
                pop     bc
                pop     hl                          ; restore src

                push    de                          ; remember dest for return
                ldir
                ld      (litpool_expr_buf_top), de
                pop     hl                          ; HL = dest (start of copy)
                ret

litpool_expr_buf_top:   defw    LITPOOL_EXPR_BUF
```

And initialisation in `litpool_init`:

```asm
                ld      hl, LITPOOL_EXPR_BUF
                ld      (litpool_expr_buf_top), hl
```

## Open questions deferred to plan / impl

- Whether `STAGING_BUF` and `LITPOOL_EXPR_BUF` should be defined in `assembler.asm` (memory-map authority) or in `main_loop.asm` / `litpool.asm` (consumer-local).  Recommend `assembler.asm` for the map-comment, with `equ` re-export per file if needed.
- Whether `in_map_current` / `in_normalise_hl` / `in_persist_hl` live in `reader.asm` (consumer) or `trampoline.asm` (paging primitives).  Lean towards `reader.asm` because they encode reader-specific semantics (section A as the IN window), but pure-paging helpers feel like a trampoline.asm fit too.
- Whether to push the page-cross test out of `reader_next_kind`'s inner loop for the bulk-payload case (count bytes to page boundary, LDIR that block, re-map, repeat).  Defer to perf-pass after impl.
- Whether `IN_POS_PAGE` should store the full LMPR byte (RAM0 + low 5 bits) or just the page number.  Recommend "full LMPR byte" — keeps `in_map_current` to a single OUT, no OR-32 step.  The constant `IN_BASE_PAGE` stays the page number; the runtime variable stores RAM0 + page.
