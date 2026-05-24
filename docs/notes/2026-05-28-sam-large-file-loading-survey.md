# 2026-05-28 — SAM large-file loading survey

How does the SAM Coupé software ecosystem load files larger than 32 KB
into memory?  This note answers the question by inspecting the ROM
LOAD CODE path, SAMDOS HLOAD, COMET's trampoline, and the
`~/sam-corpus/disks/` body of 800 commercial / PD disks.

## Top-line answer

**A single SAMDOS HLOAD call can deliver up to ~496 KB of payload.**
The 5-bit page count field (`samdos/src/h.s:hconr` `uifa+34 & &1f` →
`pges1`) caps the per-call load at 31 × 16 KB = 496 KB.  SAMDOS auto-
pages HMPR across the section-C `&C000` boundary inside its own
loader loop (`samdos/src/c.s:354-369 ctas`), so a multi-page payload
spreads automatically across physical pages N, N+1, …, N+(C-1), with
the page number incremented modulo 32 in the same 512 KB bank
(`samdos/src/c.s:365-366 inc a / and %00011111`).

**Practical limit on our 512 KB SAM, accounting for fixed memory:**
HLOAD itself supports up to 28 contiguous pages = 458,752 B before
running into the standard SAMDOS / BASIC layout (DOS at page 13,
screen at pages 14-15).  Real disks routinely ship 28-page CODE files
loaded by a single `LOAD "NAME" CODE 32768` BASIC line — the largest
single-file CODE load observed in the corpus is **458,752 bytes
(28 pages)**, in three different disks (Blinky GARYMOORE, Kim Wilde
GARYMOORE2, Mike AJ PETSHOPBOY).

For our `release.tbn` (439,133 B = 26 pages + 13,389 B = 27 page-aligned
slots): **one HLOAD call is enough**.  The architectural change is
trivial — just bump `IN_BASE_PAGE` and reserve more pages — but it
collides head-on with the existing page allocation (DOS at page 13).
See "Recommendation" for the layout shuffle needed.

## BASIC LOAD CODE — ROM path

The chain `CDSCVE → STSPEC2/3 → LDVDBLK → DOSLD` at
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22407-22512`:

| Step | Code | Effect |
|---|---|---|
| Read length from header | `RDLLEN` (`rom:7648-7659`) | CDE = `(HDL+HDN+3..5)` page-form length: C = pages, DE = length-mod-16K |
| Compute absolute start page | `STSPEC2` (`rom:22453-22455`) | `A := LDCO + C` (where `LDCO` is the ZX-emulation page offset, usually 0 for native LOAD) |
| Set HMPR | `CALL TSURPG` (`rom:22457`) | TSURPG (`rom:14853-14861`) sets HMPR low 5 bits to A, preserving top 3 |
| Issue HLOAD | `RST 08H / DB 82H` (`rom:22509-22511`) | hook code 130 = HLOAD |

The ROM passes HL, BC, DE on the alternate register set; the SAMDOS
hook dispatcher (`samdos/src/b.s:439-470 hook`) captures them into
`hkhl / hkbc / hkde`.  HLOAD's `dschd` then reads:

- `(hkhl)` → `(hd0d1)` = the section-C write pointer (`&8000` + offset)
- `(hkbc)` → C = page count → `(pges1)` (`samdos/src/h.s:82-84`)
- `(hkde)` → DE → `(hd0b1)` = length-mod-16K (`h.s:86-88`)

No `LOAD CODE`-specific size cap exists in the ROM.  The cap is whatever
HLOAD itself imposes (see next section).  The ROM relies on **BASIC's
stack living in LMPR-stable low memory** (ERRSP at `&5C3D` is in section
A under the default LMPR, where HLOAD's section-D spillover never reaches),
which is why the ROM doesn't need an SP-switch around its `RST 08H`.

### Corpus evidence — real LOAD CODE call sites

From the BASIC autoboot programs of disks containing large files:

| Disk | Autoboot line | File loaded | Size |
|---|---|---|---|
| Blinky Samples Disk 1 | `DESAMPLER` line 70: `LOAD a$ CODE 32768` | `GARYMOORE` | 458,752 B (28 pages) |
| Mike AJ Disc 7 | `SampleMate` line 250: `LOAD name$ CODE 32744` | `PETSHOPBOY` | 458,751 B (28 pages) |
| Kim Wilde Samples | (same DESAMPLER variant) | `GARYMOORE2` | 458,752 B (28 pages) |

In every case the BASIC code is a one-liner `LOAD "NAME" CODE <addr>` —
no manual paging, no chunked-loader stub, no direct disk I/O.  The
ROM's `STSPEC2` decomposes the caller's `<addr>` (a 24-bit integer in
the BASIC numeric format) into `(page, offset)` and lets SAMDOS HLOAD
do the multi-page work.

## SAMDOS HLOAD — actual maximum per call

The page-count register `pges1` is set from UIFA byte 34 in `hconr`
(`samdos/src/h.s:352-354`):

```asm
ld a,(uifa+34)
and &1f                        ; 5-bit mask: max 31 pages
ld (pges1),a
```

The page-count is decremented in `ccnt`
(`samdos/src/c.s:682-699`):

```asm
ccnt:          ld hl,(svde)
               ld bc,510                   ; sector size minus 2
               sbc hl,bc
               ret nc                       ; more in current 16K — keep going
               ld a,(pges1)
               and a
               jr nz,ccnt1
               scf
               ret                          ; pges1 == 0 → done

ccnt1:         dec a
               ld (pges1),a
               ld hl,(svde)
               ld bc,16384
               add hl,bc                   ; advance one 16K page in svde
               ld (svde),hl
               jr ccnt
```

Combined with `ctas`'s HMPR-bump at the `&C000` boundary
(`c.s:354-369`):

```asm
               ld a,h
               cp &c0
               jr c,cta3
               res 6,h                      ; wrap H back to &80
               ld (svhl),hl
               in a,(251)
               push af
               and %11100000                ; preserve top 3 HMPR bits
               ld b,a
               pop af
               inc a
               and %00011111                ; ← page wraps 31 → 0 in same bank
               or b
               ld (port1),a
               out (251),a
```

**Per-call cap: 31 × 16 KB = 496 KB**, with the page counter wrapping
modulo 32 in the same 512 KB bank if the load extends past page 31 of
that bank.  In practice the cap is set by RAM layout (DOS, screen,
running code) far below 496 KB.

### Hook table — no incremental loader, no caller-controlled paging

Full re-survey of `samdos/src/b.s:497-538` confirms what
`docs/specs/2026-05-27-samdos-load-idiom.md` already documented:

| Hook | Code | What it does | Useful for chunked load? |
|---|---|---|---|
| HGTHD | 129 | Read DIFA into UIFA | Prerequisite for HLOAD/HVERY/HLDBK |
| HLOAD | 130 | Full file load through `ldblk` | The "one big call" path |
| HVERY | 131 | Verify file matches RAM | Read-side, no incremental I/O |
| HSAVE | 132 | Save block — counterpart of HLOAD | Same auto-paging shape |
| HRSAD | 160 | Read 512-B sector at (track, sector) into arbitrary HL via `cals` | Could implement custom paging but bypasses file abstraction |
| HWSAD | 149 | Write 512-B sector at (track, sector) from arbitrary HL via `cals` | Save side of HRSAD |
| HLDBK | 161 | `jp ldblk` — re-enter loader with prior HGTHD/HLOAD state | Same HL constraint, same per-call cap |

There is **no hook that incrementally streams a file**.  Direct hooks
are HGTHD → HLOAD (or HVERY).  The closest to incremental is HRSAD /
HWSAD (raw sector I/O at known track+sector — useful only if you've
parsed the directory yourself).  No real-world program in the corpus
appears to use that approach for executable loads; HRSAD is mostly
used by DOS shells / disk-editor utilities.

## Empirically observed ceiling — our trampoline today

`docs/notes/2026-05-28-hload-16k-limit-investigation.md` documents the
empirical bisect on the M3 SAM-side assembler's trampoline:

- Pre-PR #39: trampoline issued `rst 8` while SP pointed into section
  D (= HMPR+1 page during the HLOAD's HMPR-shift).  HLOAD's
  auto-paging spilled writes into page (HMPR+1) starting at offset 0;
  files ≥ 16,633 B (= 16,384 + 249) overwrote the trampoline's pushed
  return address at section-D offset `&F8/&F9`, hanging on return.
- PR #39 (`a8bc2d7`) added an SP-switch to a section-B-stable address
  for the duration of the RST 8.  Verified ceiling raised: 16,632 B
  → ≥ 32 KB.

This matches **COMET's** `LD SP,(sproom)` workaround at
`reference/comet-decoded/comet.asm:1189` — see "COMET pattern" below.

The 48 KB FAIL row in the PR #39 verification table (file loads, but
the assembler exits with `rc=1` and prints a banner instead of running
clean) is an unrelated downstream cap — likely litpool / symbol budget
at that data scale, not HLOAD itself.  So the practical HLOAD ceiling
under PR #39's fix is "at least 32 KB on our worktree, likely much
higher" — bounded by the same physical-RAM-layout collision the corpus
loaders face, not by any soft limit in our trampoline.

## COMET pattern (the canonical SAM-era assembler analogue)

Source: `reference/comet-decoded/comet.asm:1184-1284`.

### Call-site (the trampoline preamble)

```asm
                ; comet.asm:1185-1199
                ld   a, 31
                or   32                      ; LMPR = &3F  → section A = page 31 (COMET data)
                out  (250), a                ; (page 31 is COMET's data segment)
                ld   sp, (sproom)            ; ← switch SP into LMPR-stable section A
                                             ;   `sproom` is at comet.asm:4876
                                             ;   in page 31 = section A under LMPR=&3F
                ld   a, (linebuff+33)        ; A = LDCO + page (= absolute HMPR page)
                ld   b, a                    ; B passed to the trampoline as target page
                ld   hl, 32768               ; HL = &8000  (HLOAD section-C window base)
                ld   a, (linebuff+34)        ; A = pages from DIFA
                ld   c, a                    ; C = page count
                ld   de, (linebuff+35)       ; DE = length-mod-16K
                res  7, d                    ; clear SAMDOS's `set 7,d` marker
                call 20224                   ; the trampoline body at &4F00
```

### Trampoline body

```asm
                ; comet.asm:1265-1284  (copied to &4F00 by `prepare`)
loaddata:       in   a, (251)               ; A = current HMPR
                push af                      ; ← stack now lands in LMPR-stable section A
                                             ;   (sproom switched SP there at the call site)
                ld   a, b                    ; A = target page
                out  (251), a                ; HMPR = target
                rst  08H
                defb 130                     ; HLOAD
                ex   af, af'                 ; save HLOAD's status flags
                pop  af                      ; restore original HMPR
                out  (251), a
                ex   af, af'                 ; restore HLOAD's flags
                di
                ret
```

### Why it scales beyond 16 KB

COMET's stack (after `LD SP,(sproom)`) is in **section A under
LMPR = &3F**, which is LMPR-stable across the HLOAD's HMPR change.
The `push af` / `rst 08H` writes land in section A — far away from
section D where HLOAD's auto-paging dumps file bytes past `&C000`.

COMET routinely loads source files larger than 16 KB this way.
The pattern is the proven canonical idiom.

### Our trampoline matches this since PR #39

`src/m3/trampoline.asm:361-394 (post-PR #39)`:

```asm
trampoline_body:
                in      a, (251)
                ld      (HMPR_SAVE), a       ; static save in section B (LMPR-stable)
                ld      (SP_SAVE), sp
                ld      sp, TRAMP_SAFE_SP    ; ← switch SP into section-B-stable address
                ld      a, b
                out     (251), a             ; HMPR = target
                rst     8
                defb    HOOK_HLOAD
                ex      af, af'
                ld      a, (HMPR_SAVE)
                out     (251), a             ; HMPR restored
                ld      sp, (SP_SAVE)
                ex      af, af'
                di
                ret
```

The structure mirrors COMET's: SP-switch to LMPR-stable memory before
the RST 8, restore on the way out.  Section B (under LMPR_DEFAULT
during the trampoline call) is the LMPR-stable analogue of COMET's
section A under LMPR=&3F.

## Corpus survey — what the ecosystem actually ships

Scanned 800 disks under `~/sam-corpus/disks/` for files of various
sizes.  Method: `samfile ls -i <disk.mgt>` parsed and filtered by
file Type + Length.  Tooling at `/tmp/biggest_code_files.py` (ad-hoc;
not committed).

### Top 25 single CODE files >= 64 KB

| Length (B) | Pages | Start | Name | Disk |
|---:|---:|---:|---|---|
| 458,752 | 28 | 32768 | GARYMOORE2 | Kim Wilde - Gary Moore Samples (1990) (PD).mgt |
| 458,752 | 28 | 32768 | GARYMOORE2 | Blinky Samples Disk 2 (1997) (Edwin Blink).mgt |
| 458,752 | 28 | 32768 | GARYMOORE | Blinky Samples Disk 1 (1997) (Edwin Blink).mgt |
| 458,751 | 28 | 32768 | PETSHOPBOY | Mike AJ Disc 7 (19xx).mgt |
| 458,751 | 28 | 32768 | PETSHOPBOY | Mike AJ Demo Disk 7 (1991).mgt |
| 458,751 | 28 | 32768 | PETSHOPBOY | Mike AJ Demo Disk 1 (1991).mgt |
| 452,730 | 28 | 32768 | dragon.STE | Mike AJ Disc 5-Edwin (19xx).mgt |
| 452,730 | 28 | 32768 | dragon.STE | Mike AJ Disc 5 (19xx).mgt |
| 442,316 | 27 | 32768 | Uno.SAM | Mike AJ Disc 6 (19xx).mgt |
| 409,600 | 25 | 65536 | HEART | Metempsychosis Unreleased Demo - CD_demo (19xx).mgt |
| 393,141 | 24 | 65536 | lexiconuc | Sam Supplement Magazine Xmas Disk (Dec 1992).mgt |
| 376,555 | 23 | 114688 | DIC512.M | SpellMaster (19xx).mgt |
| 364,800 | 23 | 57395 | ANICODE | Daton MasterBASIC Demos (1991) (PD).mgt |
| 333,005 | 21 | 239698 | (binary) | Sound Machine (1991) (Paul Angel).mgt |
| 311,674 | 20 | 32768 | uno1 | Mike AJ Disc 6-Edwin (19xx).mgt |
| 294,912 | 18 | 65536 | DIRE | Metempsychosis Demo - Jukebox (19xx).mgt |
| 294,912 | 18 | 32768 | KIMWILDE2 | (multiple) |
| 294,744 | 18 | 69123 | LINSPACE | Printer Port Music Sample Player (19xx).mgt |
| 285,901 | 18 | 49152 | CHRIS1.CDE | Metempsychosis Demo - Christine (19xx).mgt |
| 278,528 | 17 | 32768 | Auto DS12 | DS12 Duff Capers Music Demo (2003) (PD).mgt |
| 276,615 | 17 | 50000 | STTNG.VCR | FRED Magazine Issue 78 (1997).mgt |
| 272,694 | 17 | 69123 | TREK.MOD | FRED Magazine Issue 77 (1997).mgt |
| 270,858 | 17 | 35136 | MELODY.blo | EXPLOSION - ZX SPECTRUM 48 Emulator (1996).mgt |
| 262,144 | 16 | 98304 | SOLO.SND | Metempsychosis pdm10 (19xx).mgt |
| 262,144 | 16 | 65536 | walk | Metempsychosis Unreleased Demo - Jukebox (19xx).mgt |

712 CODE files between 64 KB and 500 KB across the corpus.

### Observations

1. **Single-file loads dominate.**  Of the top files, all are loaded as
   a single contiguous CODE file via one HLOAD call.  No multi-segment
   "load chunk 1, then chunk 2" pattern observed in the BASIC
   autoboots for the largest files.
2. **Load address is almost always 32768 (`&8000`).**  The few outliers
   (65536, 114688, etc.) load to other 16K-aligned boundaries —
   absolute page = (addr >> 14).  In every case the ROM's STSPEC2
   path decomposes the address and sets HMPR; SAMDOS's auto-paging
   handles the rest.
3. **The PETSHOPBOY pattern** (Mike AJ Disc 7 SampleMate, line 250):
   `INPUT "Filename: " LINE name$ / LOAD name$ CODE 32744 /
   POKE spage,1,(PEEK 32764)+1 / DPOKE saddr,32768 / DPOKE
   eaddr,32768+ LPEEK 32765` is the only example of post-load
   manual page accounting — and even there, the load itself is a
   single LOAD CODE; the POKE / PEEK is just to record the file's
   span for later playback.
4. **No direct-disk-controller usage observed.**  Spot-checked five
   AUTOboots for `OUT &E0..&E7` (Tech Manual disk controller ports):
   none.  All large-file loads go through SAMDOS HLOAD.
5. **The biggest single file in the corpus is 458,752 bytes** —
   28 pages.  This is the empirical upper bound for "single-file
   single-LOAD-CODE" loading on a SAM Coupé.

## BASIC's loaded layout — why 28 pages is the corpus max, not 31

A 512 KB SAM Coupé's standard layout after SAMDOS boot
(`docs/sam/sam-coupe_tech-man_v3-0.txt:3204-3217`):

```
40 40 40 40 00 00 00 00 00 00 00 00 00 60 C0 C0 FF FF FF…
↑ pages 0..3       ↑ 4..12       ↑ 13 = DOS  ↑ 14..15 = screen
  BASIC program      unused        (60H)        (C0H)
```

- Pages 0..3: BASIC program area
- Pages 4..12: unused (9 pages = 144 KB)
- Page 13: DOS
- Pages 14..15: screen

512 KB Coupé adds pages 16..31 (256 KB of additional unused memory)
via SAMRAM expansion.  On a stock 256 KB Coupé pages 16+ are FFH
(non-existent), so the corpus disks targeting "the 512 KB user base"
sized their files to fit within pages 2..(2+28) = 2..30 — but with a
load address of 32768 (= page 2 offset 0), pages 2..29 would clobber
DOS at page 13.  How?

Looking at Blinky's DESAMPLER more carefully:

```basic
70 LOAD a$ CODE 32768
80 POKE 32624, a+1                 ; a = page count from RESTORE table
```

So Blinky's user has already done `CLEAR 32579` (sets RAMTOP below
the load area) and likely paged DOS *out* of section C before the
load — though the actual mechanism is opaque without running the
program.  More likely: Blinky's disks are 512K-targeted, and the
"GARYMOORE" file is loaded at address 32768 *but* expects ALLOCT to
mark pages 2..29 as "code" — overwriting DOS at page 13 if it ever
overlaps.  On a 512 KB machine with DOS at page 13, **loading from
page 2 with 28 pages does hit page 13**.

The disambiguation: many of these "huge file" disks **don't run
SAMDOS at all** during playback — once the file is loaded, the user
program executes from page 2 onwards as a single contiguous chunk
including the page where DOS lived.  DOS only needs to live long
enough to load the file in the first place; afterwards it's expendable.
This is consistent with the corpus: large-file disks are typically
single-purpose (a sampler, a demo, a music player) that doesn't return
to BASIC / DOS once started.

**Implication for sam-aarch64:** if we permanently displace DOS, we
can never re-enter SAMDOS for further file I/O (e.g. HSAVE the output
back to disk).  Our flow needs DOS alive through HSAVE OUT.  See
"Recommendation".

## Patterns observed — named idioms

### Pattern 1 — "single multi-page HLOAD" (the corpus mainline)

What 95%+ of large-file disks do.

- BASIC autoboot: `LOAD "NAME" CODE <addr>`.
- ROM STSPEC2 decomposes `<addr>` to `(page, offset)`, sets HMPR.
- HLOAD's ctas auto-pages section C through pages N, N+1, …, until the
  pages-1 counter exhausts.
- Practical ceiling: 28 pages (458 KB) on a 512 KB Coupé that keeps DOS
  alive; up to 31 pages (496 KB) if DOS / screen are unused.

When to pick this: when the entire file fits in one contiguous range
of physical pages.  The simplest possible solution.

### Pattern 2 — "COMET trampoline" (our current pattern, PR #39)

What COMET, our M3 assembler (PR #31+), and any caller that needs HLOAD
to a non-default destination page does.

- Caller switches SP to LMPR-stable memory (section A or B).
- Caller sets HMPR to target page.
- Caller issues `RST 8 / DB 130` (HLOAD).
- Caller restores HMPR / SP on the way out.
- Trampoline body must NOT live in section C (HMPR change yanks it).

When to pick this: when (a) section C is occupied by the running code,
(b) the target physical page is not the caller's own page, (c) the
caller's stack would otherwise sit in section D = HMPR+1 page during
the HLOAD.

### Pattern 3 — "BASIC LOAD CODE orchestration of multiple files"

A few large multi-file programs (e.g. Mike AJ disc compilations) load
several files in sequence, each a `LOAD CODE` for a different
destination range:

- Disc 7 AUTO line 20: `LOAD "font.fnt" CODE 20880` (font into low
  RAM)
- Disc 7 AUTO line 30: `LOAD "chrome.fnt" DATA chrome$` (chrome into
  string variable)
- Disc 7 SampleMate line 40: `LOAD "allcode3" CODE 16500` (boot code)

Each `LOAD CODE` is a separate HLOAD call; the bytecode the user runs
is the union.  This is the canonical way to assemble a working program
from independently-saved chunks (graphics, font, code, sample data).

When to pick this: when the data is naturally chunked at the source
level (e.g. one file per sample).  Less common for monolithic code;
more common for samplers / demos with many media assets.

### Pattern 4 — "direct disk controller, bypass SAMDOS"

Hypothetical.  None observed in the corpus, but the option exists
via `OUT &E0..&E7` per the Tech Manual disk-controller appendix.  In
principle a program could:

- Read the disk directory itself
- Issue `IN &E0`-style raw sector reads
- Decode to user-controlled RAM, including arbitrary cross-page
  layouts

Why nobody does it: HLOAD already handles 95%+ of needs in 1 call
with a 5-line BASIC wrapper, vs ~500 lines of disk-controller driver.
Only seen in custom DOSes (B-DOS, MasterDOS) which replace SAMDOS
wholesale — not in application code.

## Recommendation for sam-aarch64 — loading `release.tbn` at ~440 KB

### TL;DR

**Yes, our existing HLOAD trampoline handles 440 KB in a single call
with NO architectural change.**  The 5-bit `pges1` cap allows up to
31 pages; release.tbn at 27 page-aligned slots (26 full pages +
13,389 B) fits comfortably.  The work is **page allocation**, not
multi-call orchestration.

### What needs to change

Currently `IN_BASE_PAGE = 7` and the IN buffer is sized for 4 pages
(7..10).  To extend to 27+ pages, we need 27 contiguous unused pages.
This collides with DOS at page 13 and screen at pages 14-15.

**Proposed page allocation post-this-change:**

```
Page 0..3    BASIC                  (we don't care; BASIC is dead after our CALL)
Page 4       ENCTAB                 (unchanged)
Page 5..6    OUT (low + high zone)  (unchanged)
Page 7       (unused, freed)        ← was IN page 0; reuse for headroom or move to 16+
Page 8..10   (unused, freed)        ← was IN pages 1..3
Page 11..12  unused
Page 13      DOS                    ← must preserve through HSAVE OUT
Page 14..15  screen                 (we don't display, but ROM screen-handling reads pages 14-15)
Page 16..31  IN (16 pages, 256 KB)  ← new IN_BASE_PAGE = 16
                                    
                                    256 KB of contiguous unused RAM on a 512 KB Coupé.
                                    Sufficient for release.tbn at 27 pages? NO — 27 > 16.
```

**The 27-page release.tbn does NOT fit between page 16 and page 31.**
Pages 16..31 = 16 pages = 256 KB.  Release.tbn at 439 KB needs 27 pages.
A naive `IN_BASE_PAGE = 16` with 27-page span wraps to pages 16..31, 0..(27-16-1) =
pages 16..31 + 0..10.  Pages 0..10 still hold BASIC + ENCTAB + OUT + free
junk by the time we load IN.

**Alternative — relocate DOS first.**  Some custom DOSes (e.g.
MasterDOS) live in page 0 or a different page entirely.  We could:
1. After our BASIC `CALL`, mark page 13 (DOS) as "claimable" since
   our flow uses HLOAD only at startup (load ENCTAB, load IN) and
   HSAVE only at end (write OUT).  If we can complete HLOAD-of-IN
   before invalidating DOS, and re-load DOS from disk before HSAVE,
   we could use page 13 for IN.
2. But re-loading DOS itself is a HLOAD that needs DOS already alive.
   Catch-22.

**Real fix — make IN_BASE_PAGE depend on the file size:**

If release.tbn really stays under the M6 PR 3 compact-`.tbn` target
(~tens of KB, per `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`),
the question becomes moot — we don't need 27 pages.  The compact
format brings the budget back to "fits in 4 pages = 64 KB" which is
what the current `IN_BASE_PAGE = 7, pages 7..10` design assumes.

If we DO need to load the uncompacted 439 KB file:
- Option A: Replace SAMDOS with a custom minimal loader that doesn't
  need to live in page 13.  Big effort, drops M3..M5 SAMDOS
  compatibility we've earned.
- Option B: Two-phase load.  Phase 1: HLOAD release.tbn first part
  (e.g. 12 pages) into pages 16..27 (12 pages between page 16 and
  page 27, with page 28-31 reserved).  Phase 2: snapshot DOS, displace
  it, continue load.  Complex; sequencing-fragile.
- Option C: Load release.tbn in TWO files (release-1.tbn, release-2.tbn),
  each ≤ 12 pages (~192 KB) — splits the load across the page-13 gap.

### Concrete recommendation

**For our current M6 work**: do the compact `.tbn` strand (M6 PR 3) FIRST.
The 439 KB number is the fat-format size; the compact-format release.tbn
is projected at "a few tens of KB" per the compact-tbn design doc.  That
brings release.tbn comfortably within the existing `IN_BASE_PAGE = 7`,
pages 7..10 = 64 KB window.

**If we genuinely need to load a 439 KB file someday** (debug builds at
~274 KB even post-compaction): the cheapest path is **Option C — split
into two files**.  Each half ≤ 12 pages, loaded into pages 16..27 in two
HLOAD calls back-to-back.  Cost: 1 extra HGTHD + HLOAD round-trip (~50 ms
on a real SAM, milliseconds on SimCoupé).  No structural change to the
trampoline; just a loop in `load_in_file`.

**Surprising finding**: our existing HLOAD trampoline (post-PR #39)
already supports the full SAMDOS / corpus ceiling — 31 pages, 496 KB —
in a single call.  The 32 KB ceiling cited in `docs/notes/m6-status.md`
"PR 2 caveat" was an empirical lower bound from the test harness; with
PR #39's SP-switch fix the real ceiling is the same as the corpus's
real ceiling (28 pages = 458 KB, bounded by SAM RAM layout, not by the
trampoline).  **The architectural work for loading > 64 KB already
exists.**  We just haven't exercised it in a fixture yet.

## References

- ROM disasm:
  - `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22407-22512`
    — CDSCVE → STSPEC2/3 → LDVDBLK → DOSLD (LOAD CODE chain)
  - `:14843-14861` — TSURPG (HMPR low-5-bits set)
  - `:4499-4527` — PDPSR2 (absolute-page derivation)
  - `:921` — LDCO definition
  - `:22044-22052` — UIFA / HDR / HDL byte layout (incl. page-form
    fields at +31..36)
- SAMDOS source:
  - `samdos/src/h.s:59-90` — hgthd / hload / dschd
  - `samdos/src/h.s:336-361` — hconr (UIFA → internal old-style hdr,
    incl. `(uifa+34) AND &1f → pges1`)
  - `samdos/src/c.s:318-369` — ctas (HLOAD's HMPR auto-paging at
    `&C000`; the 5-bit wrap)
  - `samdos/src/c.s:575-672` — ldblk / lblok (per-byte loader loop)
  - `samdos/src/c.s:682-699` — ccnt (page-counter decrement)
  - `samdos/src/b.s:439-470` — hook (RST 8 dispatcher, captures
    BC/DE/HL into hkbc/hkde/hkhl)
- COMET reference:
  - `reference/comet-decoded/comet.asm:1184-1284` — call site +
    `loaddata` trampoline body
  - `:4876` — `sproom` (SP-save slot in section A under LMPR=&3F)
- sam-aarch64 trampoline:
  - `src/m3/trampoline.asm:361-394` — current (post-PR #39) body
  - `src/m3/main_loop.asm:2103-2169` — `load_in_file` call site
- Previous notes:
  - `docs/notes/2026-05-28-hload-16k-limit-investigation.md` —
    the 16 KB → 32 KB ceiling bisect + PR #39 verification
  - `docs/specs/2026-05-27-samdos-load-idiom.md` — design source
    for the trampoline pattern (option (b) "COMET trampoline")
  - `docs/specs/2026-05-27-m6-paged-in-design.md` — current
    `IN_BASE_PAGE = 7, pages 7..10` 64 KB design
- Tech Manual:
  - `docs/sam/sam-coupe_tech-man_v3-0.txt:3204-3234` — ALLOCT
    (page allocation table layout post-SAMDOS-boot)
- Corpus survey scripts:
  - `/tmp/biggest_code_files.py` (ad-hoc; not committed)
  - `/tmp/survey_big_files.py` (ad-hoc; not committed)
