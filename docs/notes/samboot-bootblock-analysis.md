# Trinity EEPROM bootblock — boot chain analysis

**Purpose.** This documents the SAM Coupé reset → EEPROM bootblock → B-DOS load
sequence on a Trinity-equipped machine with Colin Piggot's patched system ROM,
and pins down the one hook the SAMBOOT endeavour needs: the bootblock's unfinished
"auto-load a chosen file/record at power-on" stub. It is grounded entirely in
public source (the `LongSteve/z80` `boot.asm` reproduction of Colin's SAM Revival
issue 23 bootblock) and local static-analysis artifacts; no hardware was used.
The controlling charter for the whole endeavour — goals, the three boot-chain
layers, the injection plan, the item index — is
[`docs/specs/samboot.md`](../specs/samboot.md); this note states only the
bootblock-level facts that live nowhere else and links the charter for everything
it owns.

Scope is the **EEPROM bootblock** (charter §3 layer 2) and where the boot-a-record
hook attaches. The patched system ROM chip (layer 1) and the forked B-DOS 1.5t
(layer 3) are covered only where they touch the bootblock's behaviour; B-DOS's
internals are owned by [`bdos-trinity-fork-analysis.md`](bdos-trinity-fork-analysis.md).

**Provenance / redistributability.** The public bootblock reproduction is
[`LongSteve/z80` `boot.asm`](https://github.com/LongSteve/z80/blob/main/boot.asm)
(cited by URL, never vendored — it is Colin's proprietary code, reproduced by a
third party). The non-redistributable local artifacts under `~/sam-archive` are
cited by path and described, never copied verbatim into this committed doc.

---

## 1. The reset → bootblock → B-DOS load sequence

The chain has three artifacts (charter §3 is the single home for the naming —
patched system ROM chip / EEPROM bootblock / forked B-DOS 1.5t). The steps below
are what each does, confirmed against `boot.asm` and the local sources.

1. **Reset → patched system ROM chip fetches and runs the bootblock.** The SAM is
   fitted with Colin's modified 32 KB system-ROM chip ("SAM ROM 3 (C) Andrew
   Wright / TRINITY BOOT (C) Colin Piggot", SAM Revival issue 23 —
   `~/sam-archive/trinity-docs/DISCOVERY_REPORT.md` §2d, marked UNVERIFIED pending
   photo confirmation). Its only job versus stock is: at reset, instead of waiting
   for the keyboard, read the ~1 KB bootblock from **EEPROM chunk 1** and run it.
   *The boot.asm reproduction is the bootblock only — the ROM-side fetch that
   loads and jumps to it is in the ROM chip and is not in the public source.*
   The product-level confirmation that the patched ROM "replaces the standard SAM
   ROM page, allowing automatic B-DOS load from EEPROM at power-on" is in
   [`trinity-capabilities.md` §1](trinity-capabilities.md).

2. **Bootblock saves the paging state and pages itself into the right window.**
   `boot.asm` runs at `ORG 16384` (`&4000`). At `start:` it reads LMPR (port 250)
   and HMPR (port 251), saves both into its own `restore`/`restore2` immediates so
   it can put them back later, sets `LMPR |= 64` (pages a RAM page over the ROM at
   `&C000` so the routine can run / write freely) and `HMPR = 29`, then `DI`.
   (`boot.asm` `start:`; ports 250/251 are LMPR/HMPR — see `eeprom.asm` provenance
   header for the same Trinity context.)

3. **Bootblock copies the B-DOS image out of EEPROM chunks 2–13 into RAM.** A
   straight-line sequence loads twelve 1 KB chunks via `read_chunk`:
   chunk 2 → `32768` (`&8000`), chunk 3 → `32768+1024`, … chunk 13 →
   `32768+11264`. That is **12 KB landing at `&8000`–`&AFFF`** — twelve chunks
   (2..13), not "2–13 = 12 chunks plus boot = 13 total" being a contradiction;
   chunk 1 is the bootblock, chunks 2..13 are the twelve B-DOS chunks (the "13
   EEPROM chunks total = 1 boot + 12 B-DOS" figure in `DISCOVERY_REPORT.md` §2d /
   §1 matches exactly). The destination is **`&8000`**, confirming the charter.
   (`boot.asm` `start:` chunk-load block; `read_chunk` routine.)

4. **Bootblock launches B-DOS.** After the twelve loads it does `CALL 32876`
   (= `&804C`) — "execute the DOS". The B-DOS image's CODE entry is `&8009`
   (32777) per the fork analysis; `&804C` is an entry point a few dozen bytes into
   the loaded image (the same kind of "jump into the loaded DOS image to
   initialise" the floppy boot path uses). B-DOS initialises (detects Trinity,
   inits the SD card, sets the screen mode, etc. — owned by
   [`bdos-trinity-fork-analysis.md`](bdos-trinity-fork-analysis.md)) and returns
   to the bootblock. (`boot.asm` `CALL 32876`.)

5. **Bootblock redraws the MGT opening stripes.** `CALL stripes` (§2 below).

6. **The auto-load-file hook would go here** — currently just a TODO comment
   (§3 below); this is the crux.

7. **Bootblock restores paging and exits to BASIC.** `restore:`/`restore2:`
   write the saved LMPR/HMPR back, then `JP 4143` (= `&102F`, commented
   "ERRHAND2 (exit to basic)"). Control lands in BASIC with B-DOS resident and
   the stripes on screen, waiting for the user — exactly the stock-looking
   power-on screen. (`boot.asm` `restore:`/`restore2:`/`JP 4143`.)

### The EEPROM chunk map

The EEPROM is a 128 KB SPI device (Microchip 25LC1024, per
`DISCOVERY_REPORT.md` §1, UNVERIFIED) organised as a simple chunked filesystem.
The layout, confirmed from both the public `eeprom.asm` driver and the SR20
"Using the Data EEPROM" article (`DISCOVERY_REPORT.md` §2f, candidate facts
11–13):

| Region | Address | Contents |
|---|---|---|
| Index | `0` | 120 × 64-byte headers (7680 B). Header = part(1) + total(1) + name(16) + description(46). part=0 marks the chunk empty. |
| Gap | `7680`–`8191` | 512 B unused (safe scratch). |
| Chunk data | `8192` + (N−1)×1024 | 120 × 1 KB chunks. Chunk 1 at `8192`, chunk N at `8192 + (N−1)×1024`. |

Boot-relevant chunk assignment (charter §3; `DISCOVERY_REPORT.md` §2d/§2f):

| Chunk | Contents |
|---|---|
| 1 | The ~1 KB **bootblock** (this `boot.asm`). |
| 2–13 | The **forked B-DOS 1.5t** image (12 KB = 12 × 1 KB). |
| (named) | The **"Trinity Network "** config chunk (MAC/IP/gateway/…), read by name via `find_index` — see §4. |

`boot.asm`'s `read_chunk` does **not** use the named index — it addresses chunks
by *number* via `get_chunk` (`HL = 28 + 4×N` half-KB-page index → the chunk's
512-byte-page base for the SPI read). So the bootblock relies on B-DOS living at
fixed chunk numbers 2..13 with the bootblock at chunk 1 — a position contract, not
a name lookup. (`boot.asm` `get_chunk`; mirrors `eeprom.asm` `get_chunk`.)

### Cross-check: the EEPROM SPI port semantics

The bootblock's `read_chunk` / `eeprom_enable` / `eeprom_disable` / `wait_ready`
/ `write_enable` / `write_disable` routines are **the same routines as the
vendored `eeprom.asm`** (`src/netboot/eeprom.asm`), with the same port usage:

- `eeprom_enable` = `OUT (&DC), &11`; `eeprom_disable` = `OUT (&DC), &10`
  (`boot.asm` `eeprom_enable:`/`eeprom_disable:` ≡ `src/netboot/eeprom.asm:438-446`).
- `wait_ready` polls `IN A,(&DC); AND &08; JR NZ` — port `&DC` bit 3 = busy
  (`boot.asm` `wait_ready:` ≡ `src/netboot/eeprom.asm:453-457`, commented-out
  there because callers share one copy).
- The chunk read clocks the SPI data through port `&DD` (`LD BC,&00DD; … OUT
  (C),…; IN A,(C)`) — `&DD` = EEPROM SPI data
  (`boot.asm` `read_chunk:`/`read_cloop:` ≡ `src/netboot/eeprom.asm:312-341`).
- `&03` is the EEPROM READ command opcode, followed by a 2-byte address
  (`OUT (C),H` then `OUT (C),L`) and the read length stride
  (`boot.asm` `read_chunk:`, same as `eeprom.asm`).

So the bootblock's EEPROM access is byte-for-byte the same mechanism the project
already exercises in `src/netboot/eeprom.asm` and on hardware (the "Trinity
Network " read is first-light-confirmed). This is the structural model for the
new BIOS config-chunk read (§4). Port map authority:
[`trinity-capabilities.md` §2](trinity-capabilities.md).

One difference worth flagging: `boot.asm`'s `read_chunk` carries an extra
`PUSH BC / LD BC,248 / AND 16 / OUT (C),A / POP BC` inside `read_cloop` (an
auto-null / per-byte microcontroller poke at port `&F8`) that the vendored
`eeprom.asm` `read_chunk` does not. It does not change the destination or chunk
math; noted for completeness when comparing the two copies.

---

## 2. The MGT opening-stripes redraw

The bootblock includes a `stripes:` routine — the i112 concern (restore the
trampled opening screen) folded into the injection patch (charter §4 / §6 step 5).
Why it is needed: paging a RAM page over the ROM and running B-DOS init tramples
the boot-time display state, so without this the classic SAM power-on screen never
appears. The header of `boot.asm` says this is precisely what the LongSteve fork
*added* over Colin's original SR23 bootblock: "It's almost all the same, except
for the addition of the bits that display the strips after BDOS has loaded."

What `stripes:` does (`boot.asm` `stripes:` … `wait_for_key:` … `clearscn:`):

1. `CALL clearscn` — reset the display file / attributes workspace (writes a
   fixed pattern through `THFATP` = `&5A44`).
2. **Rainbow palette stripes** (`rbowl:` loop) — walks the ROM palette table
   `PALTAB` (`&55D8`) into the line-colour table `LINICOLS` (`&5600`), stepping
   the colour by `&0B` each line until `&A6`, reproducing the original ROM boot
   stripes ("code taken from original ROM").
3. **The "MILES GORDON TECHNOLOGY PLC … SAM Coupé … 512K" banner** (`print_text:`
   / `print_text2:`) — printed via `RST 16`, with `text1`/`text2` at the end of
   the file.
4. `wait_for_key:` — `CALL JREADKEY` (`&0169`) in a loop; **on a keypress** it
   clears `LINICOLS` and the screen and returns. So in the *stock* bootblock the
   stripes stay up until the user presses a key, then it falls through to the exit
   (this is the wait-for-user behaviour the keyboard-boot workaround relies on —
   `~/sam-archive/trinity-docs/KEYBOARD_BOOT_WORKAROUND.md`).

For the SAMBOOT injection, the stripes must be drawn **unconditionally** (always
redraw, charter §4), and when a record is configured the `wait_for_key` wait is
bypassed so the boot proceeds with no keypress — see §3. The redraw code itself
is already present and correct; the injection changes *when* it is called and
whether it blocks on a key.

---

## 3. The boot-a-record injection hook (the crux)

This is the single most important section: it is what proves the SAMBOOT crux on
paper. The hook is **not yet implemented** in the public bootblock — it exists
only as a TODO comment, exactly where the charter (§3 layer 2) says.

### Exact location and current state

In `boot.asm`, immediately after the DOS is executed and the stripes are drawn,
and immediately before the paging-restore/exit, sits:

```
; execute the DOS
    CALL    32876

; show the old school strips
    CALL    stripes

; TODO: Use a trinity flash ram variable to determine if we should look for an auto* file
; on a specific record, and attempt to load and run it

; restore LMPR / HMPR
restore:
    LD      A,0
    OUT     (250),A
restore2:
    LD      A,0
    OUT     (251),A
    JP      4143    ; ERRHAND2 (exit to basic)
```

(Verbatim from the public `LongSteve/z80` `boot.asm`.)

So **currently the hook is a no-op stub**: the bootblock loads B-DOS, draws the
stripes (which, via `wait_for_key`, blocks until the user presses a key), then
restores paging and exits to BASIC. There is no auto-boot today — the TODO marks
where one would go and even names the intended mechanism: *"use a trinity flash
ram variable"* (an EEPROM config value) *"to determine if we should look for an
auto\* file on a specific record, and attempt to load and run it."* This is the
charter's boot-a-record hook, described by the original author.

### What it would take to make it boot a chosen record with no keypress

The TODO already prescribes the shape, and every primitive it needs is proven
elsewhere in the project. To complete it:

1. **Read a BIOS config value from the EEPROM** — which record (and/or which
   auto-file name) to boot — using the same `find_index` + `read_chunk` mechanism
   the bootblock already uses (and that hardware-confirms via the "Trinity
   Network " read). See §4 for where this chunk lives.
2. **If a record is configured:** redraw the stripes *without* the blocking
   `wait_for_key` wait, then **boot that Trinity record**. Booting a record is the
   same operation BASIC's `BOOT` performs once B-DOS is resident — select the
   record (HRECORD, hook 156, A=0 + record number in HL — register contract from
   [`bdos-trinity-fork-analysis.md`](bdos-trinity-fork-analysis.md)) then issue
   the auto-load hook (`RST 8 / DEFB ALHK` = 136, which the
   `KEYBOARD_BOOT_WORKAROUND.md` BOOT-routine analysis shows is what `BOOT` sends
   to a resident DOS — DASM:20461-20464). The project already has the boot
   primitive (`bdos_seam.asm`, i122a, charter §5). Then it would *not* fall
   through to the plain exit — it would run the booted record's auto-run file.
3. **If no record is configured:** behave as the stock bootblock — leave the
   stripes up and exit to BASIC waiting for the user (the `wait_for_key` path).

### Exactly where the patch attaches

It attaches **at the TODO line** — between `CALL stripes` and the `restore:`
exit. Concretely the patched bootblock would:

- move/duplicate the stripes draw so it is drawn unconditionally and *before* the
  config decision (charter: "always redraw the MGT opening stripes");
- read the BIOS config (§4);
- branch: configured → select+boot the record (no keypress); unconfigured →
  fall into the existing `restore:`/`JP 4143` exit (BASIC, user waits).

The whole change is local to chunk 1 (the bootblock). Chunks 2–13 (B-DOS) and the
patched ROM chip are **not** touched — the injection target is the writable
EEPROM bootblock, flashable in software via `write_chunk`
(`src/netboot/eeprom.asm:389`), never the physical chip (charter §3). This is what
makes the crux tractable: the hook site is in software we can rewrite, and every
primitive it needs (EEPROM read, record select, ALHK boot) is already built and —
for the EEPROM read and the boot primitive — proven.

**Confidence:** the *hook site* and *current no-op state* are CONFIRMED by the
public source. That a completed hook actually auto-boots with no keypress is the
hardware crux (charter §6 step 6, i135c) — proven on paper here, to be proven on
hardware after the emulation prototype (i135d).

---

## 4. Where the SAMBOOT BIOS config chunk attaches

The charter (§4) wants a small, *editable* config chunk — a "BIOS for the SAM",
a persisted default-boot-record setting — structured like the existing
"Trinity Network " config chunk the bootblock infrastructure already reads.

### How the existing "Trinity Network " config chunk is read

`src/netboot/eeprom.asm` provides a named-chunk lookup, `find_index`
(`src/netboot/eeprom.asm:161`): the caller fills the 18-byte key (`part`,
`total`, and the 16-byte `name`, e.g. `"Trinity Network "`), and `find_index`
scans the 120 index headers (64-byte stride) comparing the 18-byte key; on a
match it returns the chunk number in `value`. The caller then `read_chunk`
(`src/netboot/eeprom.asm:312`) to pull the 1 KB payload into the `chunk` buffer.
The netboot code reads MAC at `chunk+0` (6 B) and IP at `chunk+6` (4 B), matching
`trinload.asm` (header of `eeprom.asm`; the layout is the 27-byte network-config
record — MAC/IP/gateway/mask/DNS1/DNS2/DHCP — in `DISCOVERY_REPORT.md` candidate
fact 13). This read is **hardware-confirmed** (charter §5; first-light reads
"Trinity Network ").

### How a parallel "which record to boot" config chunk would live and be read

A SAMBOOT BIOS config chunk would be a **named chunk** of its own — e.g. name
`"SAMBOOT Config "` (16 bytes, space-padded like "Trinity Network ") — holding a
tiny record: a "boot enabled" flag and a 2-byte record number (and optionally an
auto-file name), the rest of the 1 KB reserved/zero.

The patched bootblock (in chunk 1) would, at the §3 hook site:

1. set the 18-byte key to the SAMBOOT config name and `CALL find_index`;
2. if not found (`value = 0`) → treat as "no record configured" → fall through to
   the normal exit;
3. if found → `CALL read_chunk`, read the flag + record number out of the `chunk`
   buffer, and proceed to select+boot that record (§3).

This reuses the exact mechanism already proven for "Trinity Network ", so it adds
no new EEPROM primitive — only a new named chunk and a few bytes of parse + branch
in the bootblock. A host-side and/or on-SAM editor sets the chunk (charter §4:
the "BIOS setup" equivalent; tracked as i176 — the SAMBOOT BIOS config + editor).

Two practical notes for the designer (i135d):

- The bootblock's own `read_chunk` (chunk 1) addresses chunks by *number*; the
  named lookup (`find_index`) lives in the B-DOS image / the vendored
  `eeprom.asm`, not in the 1 KB bootblock. The patched bootblock either includes
  a small `find_index`-equivalent (space permitting in chunk 1) **or** reads the
  config from a *fixed chunk number* (simpler, no scan — pick a reserved chunk
  number and read it directly, the way it already reads chunks 2–13 by number).
  The fixed-number route is the smaller patch and avoids needing the index scan
  in the tight bootblock; the named-chunk route is friendlier to an editor and
  to other tools. This is a design choice for i135d, noted here, not decided.
- Bootblock space is ~1 KB (one chunk). Whatever parse/branch the patch adds must
  fit alongside the existing load loop, `stripes`, and the EEPROM helpers. The
  stock bootblock is well under 1 KB, so there is headroom, but it is finite —
  another reason the fixed-chunk-number read may be preferable.

This section is descriptive design input for i135d, not the design itself.

---

## 5. Open questions / unknowns for the hardware steps

These were the questions for the captured `rom.bin` / `eeprom.bin` (charter §6 steps
2–4: the dumper i173, the hardware capture i87a, the desk analysis i87b). **§6 below
answers them from the actual capture** — most notably Q1 and Q3. Read §6 first; this
list is the original brief, kept for traceability.

1. **Does Pete's EEPROM bootblock (chunk 1) byte-match the `LongSteve/z80`
   `boot.asm` reproduction?** The public source is a *modified* version (it added
   the stripes redraw); Pete's card may carry the LongSteve build, Colin's
   original SR23 build (no stripes), or another. The TODO-hook site and the chunk
   layout are expected to match, but the exact bytes, the presence/absence of the
   stripes redraw, and any free space in chunk 1 must be confirmed from
   `eeprom.bin` chunk 1.

2. **Exact chunk boundaries and assignment on the real card.** Confirm chunk 1 =
   bootblock, chunks 2–13 = B-DOS (12 chunks, `&8000`–`&AFFF` when loaded), and
   which chunk numbers/names are free for a SAMBOOT config chunk. Confirm whether
   a "Trinity Network " chunk is present and at which chunk number (informs the
   named-vs-fixed-number decision in §4).

3. **The patched system ROM chip's reset behaviour (32 KB `rom.bin`).** Diff
   `rom.bin` against stock SAM ROM 3.0 to see exactly how the chip fetches and
   jumps to chunk 1 at reset (the part *not* in the public bootblock source).
   Confirms the "very small patch" claim and whether the ROM, not the bootblock,
   is where any keypress-bypass (`hold SPACE at reset`,
   `DISCOVERY_REPORT.md` §2d, UNVERIFIED) lives.

4. **The `&804C` (32876) B-DOS entry point.** Confirm `&804C` is a stable entry
   into the loaded B-DOS 1.5t image across the build on Pete's card (it is an
   offset into chunks 2–13; if the B-DOS build differs, the entry could differ).

5. **The extra `OUT (&F8)` per-byte poke in the bootblock's `read_chunk`.** The
   public `boot.asm` `read_cloop` has a `LD BC,248 / AND 16 / OUT (C),A` step the
   vendored `eeprom.asm` lacks; confirm it is benign auto-null housekeeping and
   not a card-specific requirement before relying on the vendored read path inside
   a patched bootblock.

6. **Whether ALHK (hook 136) from the bootblock context actually boots a record
   unattended.** The keyboard-boot analysis shows `BOOT` sends ALHK to a resident
   DOS; confirming that the *bootblock* (after `CALL 32876` returns, paging
   partially restored) can issue HRECORD + ALHK and have B-DOS run the record's
   auto-run file is the runtime crux — emulation-prototype first (i135d), then
   hardware (i135c).

---

## 6. Captured-artifact findings (i87b)

Done offline against the captured artifacts (`~/sam-archive/samboot-capture/`,
Colin's proprietary non-redistributable images — cited by file offset, never copied
into the repo) versus the stock SAM ROM 3.0 that ships with SimCoupé
(`~/simcoupe-stock/Resource/samcoupe.rom`). Disassembly: `z80dis` (the patched
bytes) cross-referenced against the annotated stock disasm
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`). File-offset → logical
address: ROM0 `file 0x0000–0x3FFF` → logical `&0000–&3FFF`; ROM1
`file 0x4000–0x7FFF` → logical `&C000–&FFFF`.

### 6.1 The patched system ROM chip (layer 1) — the bootblock fetch, recovered

`rom.bin` (32 KB) differs from stock 3.0 in **141 bytes across 7 regions**. Three are
the substantive Trinity patch; the rest are a version byte and small data-table
edits. This is exactly **the "ROM-side fetch that loads and jumps to the bootblock"
that §1 step 1 said lived in the chip and not in the public source** — now recovered:

1. **`&ED1B` — replaces the stock `RAINBOW SCREEN` boot-stripes routine** (annotated
   disasm `&ED1B ;RAINBOW SCREEN`, right after the `&ED10` boot init `LD SP,ISPVAL`).
   The patch instead **probes for Trinity and reads a key**: `OUT (&DC),&08` (the
   identity-select, `selProbeT`) → `IN A,(&DD)` → the reply byte, stored at `&4000`;
   then `IN A,(&FE) / AND 1` (a key-row read) and, if a key is held, `XOR A /
   LD (&4000),A` — **clearing the flag**. So `&4000` carries the Trinity-present
   marker, *unless a key is held at reset, which forces a stock start* — the
   **keypress bypass lives in the ROM** (answers §5 Q3's bypass sub-question).
   Because this displaces the ROM's own stripe draw, the stripes must be redrawn
   later — which is exactly what the bootblock-side stripes code does (§2).

2. **`&0F7F` — replaces the stock "MGT MESSAGE GIVEN IF REPORT 50H" handler**
   (annotated `&0F7B ERRHAND1: CP 50H` → `&0F7F ;MGT MESSAGE GIVEN IF "REPORT" 50H`).
   The power-on path raises report `&50`; stock prints the MGT sign-on, the patch
   **hijacks it**: if `(&4000) != 'T'` (`&54`) → `JP &102F` (the stock exit-to-BASIC,
   the same target the bootblock's `JP 4143` uses). If `'T'`: set the line-colour
   table, `OUT (&DC),&11` (EEPROM enable), read **1 KB from EEPROM byte-address
   `&002000` (= chunk 1) into `&4000`** via the read routine (§6.1.3), then
   **`JP &4000`** — run the bootblock.

3. **`&F5DD..&F60D` — overwrites the tail of the stock "MILES GORDON TECHNOLOGY PLC"
   copyright string** with the **EEPROM chunk-read routine** + `&F607` `wait_ready`
   (`IN A,(&DC) / AND 8 / JR NZ` — poll bit 3 busy). The read clocks the `&03` READ
   opcode + 3-byte address through port `&DD` then `INI`-loops the bytes to `&4000`
   — the **same EEPROM SPI mechanism as `src/netboot/eeprom.asm`** (port `&DD` data,
   `&DC` enable/disable/busy, opcode 3), carved into spare string space. (This is
   why the patched ROM no longer carries the full "MILES GORDON…" string.)

Supporting / baseline diffs (not the mechanism):

- **`&000F` (1 byte)** — annotated `&000F ;ROM VERSION NUMBER`. Patched reads `&1E`
  (30) where SimCoupé's stock reads `&1F` (31): the two are **slightly different 3.0
  sub-revisions**, so a small part of the 141-byte diff is baseline, not Colin's
  patch. (Honesty: the diff is vs SimCoupé's 3.0 image; Colin built on a `&1E`-30
  base.)
- **`&D902`, `&FBFF..&FC0F`, `&FC44..&FC45`** — small data-table / pointer edits in
  ROM data tables (`&D902` an operand byte `&FE`→`&FC`; `&FC44` a word
  `&F5DD`→`&F611`); minor, supporting.

### 6.2 The EEPROM bootblock does NOT match the public reproduction (§5 Q1 = NO)

> **Correction (i197, §7 below).** The §6.2 search looked at the EEPROM region
> the public model calls "chunk 1" (byte `&002000`) and concluded the boot.asm
> bootblock was *absent from the capture*. That conclusion is **wrong about the
> location, right about the build**: the boot.asm-equivalent bootblock IS in the
> capture — at EEPROM offset **`&000000`** (a pre-chunk region), not at `&2000`
> (which holds a B-DOS routine library). §7 re-derives it. The standing finding
> (Pete's card carries Colin's *own* build, not the LongSteve reproduction) holds;
> only "the signature occurs nowhere in the capture" is superseded.

The capture is sound — the known anchors are present: the **"Trinity Network "**
config string at file `&1E342` (123714) and the **SAM MAC** `02 54 52 49 4e bc` at
`&03400` (13312), both matching `CAPTURE-NOTES.txt`. Colin's own sign-on strings
(`"Trinity"`, `"Piggot"`, `"BDOS"`) are present near the chunk-1/2 region.

But **what the ROM actually loads and runs — EEPROM byte-address `&002000` (chunk 1)
→ `&4000` — is not the public `LongSteve/z80` `boot.asm`.** The boot.asm `start:`
signature (`IN A,(250)` to save LMPR) does **not** occur anywhere in the chunk
region; the MGT banner text, the `CALL 32876` (`&804C`) "execute DOS", the `JP &4000`
and the `; TODO … auto* file` hook comment are all **absent**. The bytes at `&4000`
are coherent Z80 (a routine library calling `&5Cxx`/`&0103`), not boot.asm's
save-paging → load-chunks-2..13 → `CALL &804C` → stripes → exit shape.

**Conclusion:** Pete's card carries **Colin's actual production bootblock/B-DOS
build, which differs from the third-party LongSteve reproduction** §1–§4 were
grounded in. The reset → chip → EEPROM-fetch → `JP &4000` handoff (layer 1) is
confirmed and documented (§6.1); the public source remains a faithful *model* of the
bootblock's role and the EEPROM SPI mechanics, **but the on-card bootblock's exact
code — and therefore the precise injection-hook site (§3) — must be reverse-
engineered from the capture before the i135 injection work.** The §3 hook-at-the-TODO
location is a property of the LongSteve build, not necessarily Colin's; tracked as a
follow-up (registry). This does not change the *plan* (patch the writable EEPROM
bootblock to read a config + boot a record), only the *exact bytes/site*, which i135
must derive against the real build.

---

## 7. Reverse-engineering Colin's real bootblock from the capture (i197)

Done offline, read-only, against `~/sam-archive/samboot-capture/` (Colin's
proprietary, non-redistributable images — cited by file offset, never copied into
the repo). Disassembly: `z80dis` driven by two local scratch helpers (`~/zdis.py`
for the ROM, `~/eedis.py` for the EEPROM; both uncommitted). The patched ROM is
read at the file→logical map of §6 (ROM0 `0x0000–0x3FFF`→`&0000`; ROM1
`0x4000–0x7FFF`→`&C000`). The EEPROM is a 128 KB device; file offset == device
byte address (`CAPTURE-NOTES.txt` anchors confirmed: SAM MAC at `&03400`,
"Trinity Network " at `&1E342`).

### 7.1 The real bootblock is at EEPROM offset `&000000` (not `&2000`)

A byte-signature scan for the boot.asm `start:` opener (`IN A,(250)` →
`LD (nn),A` → `OR 64` → `OUT (250),A`, i.e. `DB FA 32 .. 40 F6 40 D3 FA`) finds
**exactly one** match in the whole 128 KB image, at **`&000000`**. The
execute-DOS `CALL` and the exit `JP &102F` likewise each occur exactly once,
inside that block. So Colin's bootblock is unique and lives at offset 0 — a
region *before* the numbered chunks (§7.3) — and **is** the boot.asm-equivalent
that §6.2 reported missing.

It runs at `ORG &4000` (every internal reference is `&40xx`). Decoded
(EEPROM `&000000–&00015D`, 350 bytes):

```
&4000  IN A,(250) / LD (&40A2),A      ; save LMPR into the restore: immediate
&4005  OR 64 / OUT (250),A            ; page a RAM page over ROM at &C000
&4009  IN A,(251) / LD (&40A6),A      ; save HMPR into the restore2: immediate
&400E  LD A,29 / OUT (251),A          ; HMPR = 29
&4012  DI
       ; load the twelve B-DOS chunks 2..13 into &8000..&AC00:
&4013  LD A,2  / LD (&40BC),A / LD HL,&8000 / CALL &40BD   ; read_chunk
        … chunks 3..12 …
&408C  LD A,13 / LD (&40BC),A / LD HL,&AC00 / CALL &40BD
&4097  XOR A / LD BC,&00F8 / OUT (C),A   ; OUT (&F8),0  (the per-byte auto-null poke)
&409D  EI
&409E  CALL &805F                     ; *** execute the DOS ***
&40A1  LD A,0 / OUT (250),A           ; restore:  (the 0 is overwritten at boot)
&40A5  LD A,0 / OUT (251),A           ; restore2: (likewise)
&40A9  CALL &06B5                     ; ROM call — re-init the screen/display
&40AC  LD A,&FF / LD (&5600),A / LD (&5C44),A
&40B4  LD A,&10 / LD (&5BBE),A        ; line-colour / sysvar fix-ups
&40B9  JP &102F                       ; ERRHAND2 — exit to BASIC
&40BC  (db chunk#) / read_chunk: …    ; helpers: get_chunk &412C, eeprom_enable
        &4103, wait_ready &411A, eeprom_disable &410A, etc. (end &40BD..&4150)
```

This is structurally boot.asm, with **build-specific differences** that pin it as
Colin's own, not the LongSteve reproduction:

- **execute-DOS entry is `&805F`**, not LongSteve's `&804C` (§1 step 4).
- **No inline `stripes:` routine and no `wait_for_key`.** Colin redraws the screen
  via a stock-ROM call (`CALL &06B5`) plus three sysvar pokes (`&5600`/`&5C44`
  line-colour, `&5BBE`), then exits — so the §2 "stripes" code the LongSteve fork
  *added* is simply not how Colin's build redraws. (This re-scopes i112.)
- **No `; TODO … auto* file` comment and no hook stub.** The boot-a-record hook
  (§3) is *unimplemented* here exactly as in the public build, but there is no
  marked site — see §7.2.
- The restore is self-modifying (the `LD A,0` immediates at `&40A2`/`&40A6` are
  overwritten by the saved LMPR/HMPR at entry) — same idiom as boot.asm `restore:`.

### 7.2 The injection-hook site and free space

The boot-a-record hook attaches at the **same logical point** boot.asm marks with
its TODO: **after `CALL &805F` (`&409E`) and before the `restore:` exit
(`&40A1`)** — i.e. once B-DOS is resident, decide whether to select+boot a record
or fall through to the BASIC exit. The primitives (EEPROM read for a config value,
`HRECORD` + `RST 8`/`ALHK` to boot a record) are unchanged from §3.

There is **ample free space in-chunk** for the patch: the bootblock code ends at
`&00015D`, and `&00015E–&000408` (≈683 bytes) is all-zero, immediately following.
The injection can live there with a `CALL`/branch spliced in at `&40A1`. No
second chunk is needed.

### 7.3 The real EEPROM layout is a flat image, not the public 64-byte-header FS

This answers "the public-doc 64-byte-header model did not parse cleanly." Offset 0
is **code (the bootblock), not a 120×64-byte header index**. The chunk addressing
comes straight from the bootblock's own `get_chunk` (`&412C`):

```
get_chunk(N):  HL = 28 + 4*N            ; (LD HL,28; LD DE,4; B=N; ADD HL,DE × N)
read_chunk:    EEPROM byte addr = (HL)<<8  → device address (28+4N)*256
```

so **chunk N is at EEPROM `(28+4N)·256 = &2000 + (N−1)·&400`** (verified: chunk 2
disassembles as coherent B-DOS at its load address `&8000`, calling the section-B
library and the bootblock's own `&40C4`):

| EEPROM offset | Region |
|---|---|
| `&000000–&00015D` | bootblock (this §7), loaded at `&4000` |
| `&00015E–&000408` | free (zero) — injection space |
| `&001C00` | `get_chunk(0)` — B-DOS code (directory/listing routines) |
| `&002000` | **chunk 1** — a B-DOS routine library (the block §6.2 mis-read as "the bootblock") |
| `&002400–&0053FF` | **chunks 2–13** — the 12 KB B-DOS image → `&8000–&AFFF` |
| `&03400` | SAM MAC (anchor) |
| `&1E342` | "Trinity Network " config (anchor) |

The patched-ROM fetch (§6.1) is re-confirmed exactly: `&0F7F` sets `LMPR=&5F`,
enables the EEPROM, and `CALL &F5DD` reads **`&0400` (1024) bytes from device
address `&002000` into `&4000`**, then `JP &4000`. The address is sent big-endian
3-byte `00 20 00` (`OUT (C),H/L/E`), and `&F5DD` hard-codes the destination
`LD HL,&4000`.

### 7.4 The static picture — what closes, and the one ordering question left

Resolving which artifact executes at `&4000` needs the boot **traced in a faithful
emulator** (the patched ROM + SAM paging + Trinity SPI EEPROM serving these real
bytes) — that is **i190a**, and §7.5 carries its result. Two facts from the
capture set up that trace; the second of them was originally misread, and is
corrected here.

1. **The ROM loads chunk 1 (`&2000`), not the bootblock (`&0000`).** §6.1/§7.3
   both show the patched ROM reads device `&002000` → `&4000` → `JP &4000`. But
   `&2000` is the B-DOS *routine library*, whose first byte is `EX (SP),HL` and
   which immediately `CALL`s `&5C26` and jumps to `&4657`/`&5258`/`&603A`. The
   coherent, self-contained bootblock is at `&0000`, which the documented ROM path
   never reads. So the question the trace must settle is **which artifact really
   runs at `&4000`** — chunk 1 (the routine library) or the `&0000` bootblock.

2. **The `&5C26`/`&5C6A` (and `&46xx`) targets are documented sysvars and library
   code — not evidence of a hidden runtime loader.** The chunk-1 prologue's
   `CALL &5C26` and B-DOS's references to `&5C6A` were originally read here as a
   call into an *unloaded section-B support library* plus a *hidden multi-stage
   load* that populates section B at runtime. **That reading was wrong about
   `&5C26`/`&5C6A`.** Both are **documented SAM ROM system variables** in the
   `&5xxx` band (physical page 1 — `sam-paging.md §7`), which a normal ROM/DOS boot
   initialises:

   - **`&5C6A` = `FLAGS2`** — annotated disasm line 1117 (`5C6A= FLAGS2 EQU
     5C6AH`). It sits below the `&5C9F` boundary the disasm marks "NOT CLEARED BY
     NEW:" (line 1136), so FLAGS2 is in the band the cold-boot **NEW** path
     manages, and is read/set/cleared at runtime by the stock channel/keyboard/
     screen handlers (caps-lock toggle `KYCL`, line 1849; "screen is clear" `CLS2`,
     line 2175; the keyboard interrupt `KINT4`, line 19794).
   - **`&5C26` is inside the `STREAMS` table** — `STREAMS EQU 5C10H ;…USES
     5C0C-5C35 FOR STREAMS -5 TO 15` (line 1088), so `&5C26` is a stream-table
     entry in the `&5C0C-&5C35` block. The cold-boot init **writes that whole block
     by name**: in the main-init routine `MNINIT` (`&EBAE`, the reset cold-start —
     `XOR A; LD I,A; IM 1`, then RAM probe → memory-table → channels → streams),
     the `NEW2` step does `LD HL,STREAMS-4 (&5C0C); LD DE,STRMTAB; LD B,9
     ;INITIALISE 9 STREAMS` (lines 24616-24618) then `LD B,24 ;12 MORE STREAM PTRS
     TO ZAP` (lines 24628-24632) — 9×2 + 24 = 42 bytes spanning `&5C0C-&5C35`
     exactly, so `&5C26` is populated by this loop. (Source data `STRMTAB`, line
     752; the 6 BASIC channels are likewise init'd just above at `CHANS`/`CHANTAB`,
     lines 24551-24554.)

   So `&5C26` and `&5C6A` are **normal init state**, not section-B code, and
   chunk-1 referencing them is **exactly what a B-DOS routine library does when
   called with a live, initialised system**. There is no "unloaded support library
   at `&5Cxx`" and no "hidden multi-stage loader" implied by these targets — we
   have the ROM source and can see precisely how they are populated. (The `&46xx`
   targets — `&4657`/`&4677` — are section-B *code* addresses; whether section B is
   resident when chunk 1 runs is the live ordering question, §7.5, distinct from
   the sysvar misread corrected here.)

The chunk-1 prologue itself reads as a **library/routine entry, not a cold-boot
entry**: `EX (SP),HL; PUSH DE; CALL &5C26` manipulates a *live* caller's stack and
calls into the initialised stream area — an idiom that only makes sense when an
initialised system is already running, never as the first instruction off a cold
reset. That is consistent with chunk 1 being a routine library the resident system
calls, reinforcing that the real question is the **boot ordering** (does normal ROM
init run, populating these sysvars, *before* chunk 1 executes?), not a missing
loader.

**Consequence for the endeavour.** The static RE (§7.1–§7.3) is done and corrects
the record (including the sysvar misread above), but the **exact injection site
cannot be finalised, and no EEPROM flash (i135c) should proceed**, until the boot
ordering is confirmed in emulation (§7.5). Tracked: i197 split into the completed
static-RE leaf and an emulation-validation leaf gated on i190a; i135c remains
blocked on the i197 umbrella.

### 7.5 The boot traced in emulation (i190a) — §7.4 point 1 resolved, point 2 pinned

i190a loads the real captured `rom.bin` + `eeprom.bin` into the netboot emulation
core (the faithful `sampage` pager + the Trinity EEPROM SPI model) and boots the
patched ROM **from reset (PC=0)** — the authentic boot, no flat shortcut, no
HOSTTEST carve-out (`tools/netboot-oracle/z80/samboot_real_boot_test.go`). The
trace settles §7.4:

- **Point 1 — which artifact runs at `&4000`: chunk 1, NOT the `&0000` bootblock.**
  The boot follows the documented path in order — stock ROM init → the `&ED1B`
  Trinity probe (which stores the `'T'` marker at `&4000`) → report `&50` → the
  `&0F7F` fetch (LMPR=`&5F`, EEPROM-enable, read 1024 B from device `&002000` into
  `&4000`) → `JP &4000`. The 1024 bytes landed at `&4000` are **byte-for-byte
  EEPROM `&002000..&0023FF`** (chunk 1, first byte `&E3` = `EX (SP),HL`), not the
  coherent `&0000` bootblock (`&DB &FA` = `IN A,(&FA)`). So the ROM really does run
  the chunk-1 routine library, exactly as §7.3 read it — the `&0000` bootblock is
  **dormant on this card's boot path**. The SAMBOOT injection therefore belongs on
  the **ROM-loaded chunk-1 path that executes at `&4000`**, not the `&0000` block.

- **Point 2 — `&5C26` is zero because the from-reset trace hadn't run NEW yet, not
  because a stage failed to load.** Execution at `&4000` immediately runs chunk-1's
  prologue `EX (SP),HL; PUSH DE; CALL &5C26`, and in this trace `&5C26` reads
  **zero**. The original reading took that as evidence of a *failed runtime
  multi-stage load* that should have populated a "section-B support library" at
  `&5Cxx`. **That was the §7.4 misread:** `&5C26` is not section-B code — it is a
  `STREAMS`-table sysvar (`&5C0C-&5C35`, line 1088) populated by the **NEW** cold-boot
  init loop (`MNINIT`/`NEW2`, lines 24616-24632), and `&5C6A` is `FLAGS2` (line
  1117). They read zero here because the boot path this test follows — the patched-ROM
  netboot fetch, entered **from reset (PC=0)** — reaches chunk 1 at `&4000`
  **before** the normal channel/stream/sysvar init has populated that band. It is
  an **init-ordering** observation, not a missing loader: with a live, initialised
  system those sysvars are non-zero (we have the ROM code that sets them), and
  chunk-1's library-entry prologue (§7.4) expects exactly that live system. The
  remaining i197c step is therefore to **verify the boot ordering in this emulator**
  — does the real boot run the normal ROM init (populating `STREAMS`/`FLAGS2`)
  before handing off to chunk 1? — rather than to hunt a hidden loader on hardware.
  **§7.6 carries that verification; it confirms the ordering and corrects the "zero
  because init had not run" wording above.**

### 7.6 The init ordering, verified in the i190a emulator (i197c)

The §7.5 "remaining step" is now done. An instrumented from-reset run
(`tools/netboot-oracle/z80/samboot_real_boot_test.go`,
`TestRealBootInitRunsBeforeChunk1`) traces the cold-init milestones and resolves the
ordering decisively — with a sentinel experiment that removes the last ambiguity.

- **Init runs BEFORE chunk 1 — confirmed.** The from-reset boot visits, in order,
  `MNINIT &EBAE → NEW2 &EC8F → the streams-zap loop &ECB6 → &ECC8`, and only then
  reaches `&4000`. So the normal ROM cold-init (RAM probe → memory table → channels
  → streams) **does** run before the chunk-1 handoff. Chunk 1 is therefore a
  **library entry on a live, initialised system**, not a cold entry — as §7.4
  hypothesised.

- **`&5C26 == 0` is a WRITTEN zero, not unreached RAM — the §7.5 wording corrected.**
  The decisive test plants a `0xAA` sentinel at `&5C26`'s physical-page-0 offset
  (under the boot LMPR `&5F`, section B = physical page 0, so `&5C26` lives at page-0
  offset `&1C26`) *before* the boot, then checks it after. It comes back **`0x00`** —
  so NEW2's `CLSTL` loop (the "12 more stream ptrs to zap", lines 24628-24632)
  **actively wrote** the zero. `&5C26` is one of the `&5C0C-&5C35` STREAMS entries
  NEW2 zeros; its zero is the *initialised* value, **not** evidence that "init had
  not run yet" or that a section-B stage "failed to load." The §7.5 phrasing
  ("reaches chunk 1 … **before** the normal … init has populated that band") is
  superseded by this: init **had** run; `&5C26` is a deliberately-zeroed stream
  pointer. (The whole boot runs under LMPR `&5F`, so NEW2 and chunk 1 see the *same*
  physical page — there is no page-mismatch confound.)

- **The real residual: chunk 1 is a B-DOS *library* the ROM enters without a resident
  B-DOS.** A direct disassembly of the two candidate artifacts (read-only, against
  the capture) makes the shape unambiguous. EEPROM `&0000` is the coherent **boot
  sequencer**: `IN A,(250)` save-LMPR → `OR 64` page → `IN A,(251)`/`OUT (251),A`
  HMPR=29 → `DI` → a chunk-load loop (`chunk 2 → &8000`, `3 → &8400`, `4 → &8800`,
  …). EEPROM `&2000` (chunk 1, the bytes the patched ROM actually reads to `&4000`
  and `JP &4000`s — re-confirmed byte-for-byte from the ROM patch: `&0F8F LMPR=&5F`,
  then `LD HL,&0020`/`LD DE,&0400` and the `&F5DD` reader clocks address `00 20 00`)
  is a **table of small `CALL`/`RET` B-DOS support routines** (`EX (SP),HL; PUSH DE;
  CALL &5C26; … RET`, repeated; later entries `LD E,(HL);INC HL;LD D,(HL);…;RET`,
  `CALL &5C2C; SET 0,(HL); RET`, …). So the ROM's single `&2000`-fetch path runs a
  *library*, and **nothing on that path loads B-DOS** (chunks 2..13 → `&8000`) — only
  the `&0000` sequencer does. That is why the trace wanders: chunk 1's `CALL &5C26`
  expects a resident, fully-initialised B-DOS that this path never brings up.

**What this resolves, and what it leaves open for the injection site.** Resolved:
the init-ordering question that gated i197c (init runs first; `&5C26 == 0` is
initialised state; chunk 1 is a library entry, not a cold entry; no hidden
multi-stage loader is implied). The capture is a **consistent single-machine runtime
snapshot** (CAPTURE-NOTES.txt: `rom.bin` + `eeprom.bin` dumped in one trinload
session, the card serving identically before and after), so the incoherence is **not
a cross-state artifact** — it is real: the captured patched ROM fetches a B-DOS
*library* (chunk 1) as its boot entry and never loads B-DOS. **Still open (the
injection-site follow-on):** the boot-entry contradiction itself — the ROM hard-codes
fetching `&2000` (a library) yet the only coherent sequencer is `&0000`. Finalizing
*where* the boot-a-record hook attaches needs the trace continued **with B-DOS made
resident** in the emulation (load chunks 2..13 → `&8000`, run the `&805F` init, then
re-enter chunk 1 against a live B-DOS) and/or a fresh re-capture — so the re-capture,
"optional belt-and-braces" in §7.5, is better read as **recommended before any
i135c flash**: the current captures do not boot coherently to the point an injection
would attach. No EEPROM flash (i135c) until that is settled.

---

## Sources

- Public bootblock: [`LongSteve/z80` `boot.asm`](https://github.com/LongSteve/z80/blob/main/boot.asm)
  (reproduction of Colin Piggot's SAM Revival issue 23 bootblock; modified to add
  the stripes redraw).
- Repo code: `src/netboot/eeprom.asm` (vendored Trinity EEPROM library — the
  `find_index`/`read_chunk`/`write_chunk` mechanism and the `&DC`/`&DD` port
  semantics), `src/netboot/trinload.asm` (push vehicle + "Trinity Network " read).
- Charter (authority for goals, the three layers, the injection plan, the item
  index): [`docs/specs/samboot.md`](../specs/samboot.md).
- Forked B-DOS internals (hook surface, HRECORD contract, record model):
  [`bdos-trinity-fork-analysis.md`](bdos-trinity-fork-analysis.md),
  [`bdos-version-landscape.md`](bdos-version-landscape.md).
- Trinity ports / EEPROM layout: [`trinity-capabilities.md`](trinity-capabilities.md).
- Local analysis artifacts (non-redistributable, cited by path):
  `~/sam-archive/trinity-docs/DISCOVERY_REPORT.md` (EEPROM filesystem layout,
  13-chunk boot storage, SR23 boot-ROM provenance — much marked UNVERIFIED
  pending photo confirmation), `~/sam-archive/trinity-docs/KEYBOARD_BOOT_WORKAROUND.md`
  (the ROM `BOOT` routine + ALHK analysis), `~/sam-archive/bdos/analysis/`
  (B-DOS 1.5a/1.5t diff, `bootsect.src.txt` floppy boot sector for comparison).
