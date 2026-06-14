# Trinity free-record detection — read the record list directly

**Purpose.** The i82 netboot client (and the i121 WRQ server, the i122
fetch-and-boot path) must save a received file into a free Trinity SD **record**
without clobbering a record the user cares about. The Trinity SD card is a
**shared user resource** (`memory/trinity_storage_shared_resource`): we never
write a record we did not create, and every overwrite passes a mandatory
confirm gate.

This doc is the corrected design for **how a user program enumerates records and
picks a free one**. It supersedes the earlier (PR #442, "B2") conclusion that
the card's central record list "is not reachable from a user program" and that
detection must therefore HRECORD-probe each record and survive a B-DOS-internal
`nmi.sp` error trap. That conclusion was wrong (Pete, 2026-06-19): the list **is**
reachable — the `RECORD` BASIC command lists every record by name, so the Z80
plainly reads it. We read it the same clean way B-DOS does, against the frozen
on-card layout, with no error trap and no dependency on B-DOS internal
addresses.

Sources are cited inline as `file:line`. The authoritative B-DOS implementation
is the annotated disassembly `~/sam-archive/bdos/analysis/bdos15a.src.txt`
(off-repo, not redistributable); the 1.5a↔1.5t fork comparison is
`~/sam-archive/bdos/analysis/ANALYSIS.md`. Repo companions:
[`docs/notes/trinity-capabilities.md`](../notes/trinity-capabilities.md) (verified
Trinity facts), [`docs/specs/samdos-file-io.md`](samdos-file-io.md) (the hook
surface), [`docs/specs/netboot-storage-manifest-design.md`](netboot-storage-manifest-design.md)
(the serve-time name→record layer this slots under).

---

## 1. Problem and safety contract

- **Goal.** Given a received file (a name + a byte length staged in RAM), save it
  to a Trinity SD record as a normal drive-2 SAM disk via the existing i119
  HSAVE path, picking a record that is free / the user designates.
- **The card is shared.** A Trinity SD card holds the user's own disk images
  (records they copied across, named, and boot). We must never overwrite one we
  did not create. The safety rule is `memory/trinity_storage_shared_resource`:
  detect free records, claim only blank/unnamed ones, and *never* silently claim
  a record.
- **Confirm-before-overwrite is mandatory.** Whichever record is chosen — picked
  by number, by name, or auto-selected as free — the client **shows the record's
  name (or "free / unnamed")** and **requires the user to confirm** before any
  write. This gate holds even when detection is confident, because the
  user-facing cost of a wrong claim (destroying the user's data) is far higher
  than the cost of one keypress.

---

## 2. The reachability reality (corrected)

The card's central **record list** lives in the card's boot area, ahead of
record 1, and names every record. It **is** reachable from a user program:

- The `RECORD` BASIC command, with no argument, lists every record's name
  (`bdos15a.src.txt:886-906`, `list.record`; the manual,
  `~/sam-archive/trinity-docs/text/IMG_20260617_162823.txt`: *"If you just type
  `RECORD` … it'll list what RECORDs are used/named on the card"*). That listing
  is produced by Z80 code reading the list off the card — so a Z80 program can
  read it too.
- The narrower **true** fact is only that there is **no dedicated RST-8 hook**
  named "list records" or "read a card-absolute sector". The hook vector table
  (39 entries, `ANALYSIS.md §4`) has no such slot. The AT-sector hooks `HRSAD`
  (160) / `HWSAD` (149) are clamped to the *currently HRECORD-selected* record:
  `hd.seek` validates track ≤ 79 / sector 1..10 and adds the record's base
  sector (`bdos15a.src.txt:1243` region; `ANALYSIS.md §3`, `hd.seek-t`), so they
  address that record's own 800 KB, not the boot-area list.
- B-DOS itself reads the list via its internal routine **`find.rec`**
  (`bdos15a.src.txt:906-919`), which is a **clean sequential read of the boot-area
  record-list sectors** — `sel.base` → `seek.base` → `hd.lbuf`
  (`bdos15a.src.txt:917-919`). No HRECORD, no error 81, no trap.

**The robust approach (Pete's insight):** we write our **own** routine that reads
the list sectors directly and parses the 16-byte entries — depending only on
**frozen interfaces** (§3) and on **zero B-DOS internal routine addresses**.

---

## 3. Frozen-interface principle, and the rejected approaches

We depend only on interfaces that cannot move under us:

1. **The Trinity hardware port map** (`&DC`–`&DF`) —
   [`docs/notes/trinity-capabilities.md §2`](../notes/trinity-capabilities.md).
2. **The SD card SPI protocol** — CMD0/CMD8/ACMD41/CMD16/CMD17 single-block read
   (`ANALYSIS.md §7`; the SR21 article; `trinity-capabilities.md §4`).
3. **The on-card B-DOS format** — sector geometry, the record list, the 16-byte
   entry layout, the per-record `"BDOS"` stamp (§4).

The on-card format is effectively **frozen**: SamDisk reads and writes Trinity
cards with it (the manual,
`~/sam-archive/trinity-docs/text/IMG_20260617_162823.txt`: *"the Trinity formatted
card can also be read/written on a PC using … SamDisk"*); cards must stay
portable across B-DOS versions (a card formatted under 1.5a mounts under 1.5t and
vice versa); and the i71 fork analysis found the record model **identical**
between B-DOS 1.5a and the Trinity 1.5t fork (`ANALYSIS.md §3`, "the 800 KB record
model (1600 × 512-byte sectors) is IDENTICAL"; §4, the hook surface "untouched").

We depend on **no B-DOS routine address**. Those *do* move: `ANALYSIS.md §3`
documents `find.rec`/`seek.base`/`hd.lbuf` call sites relocating
1.5a→1.5t (e.g. `a&8DB0→t&8DB4`), and the whole device layer rewritten
(645 B → 92 B). An address-based design would break on the very fork our hardware
runs.

### Rejected approaches

- **(a) HRECORD-probe each record + survive error 81 with the `nmi.sp` trap.**
  This was the PR #442 conclusion (the framing this doc corrects). It selects
  each record in turn; an unstamped record raises **error 81 'Invalid record'**
  (`bdos15a.src.txt:2851-2858`, `get.label`: no `"BDOS"` stamp ⇒ carry set ⇒
  `rep81`). To keep scanning past error 81, B-DOS's internal **`nmi.sp` error
  trap** would have to be armed. **Rejected:** `nmi.sp` is an undocumented
  B-DOS internal whose address relocated 1.5a→1.5t, is shared with NMI/snapshot
  state, and cannot be verified without hardware. It is also **unnecessary** —
  the list read (§2) is clean and trap-free, gives every record's name in one
  sweep, and tells us which records are free directly. The probe is both more
  fragile *and* coarser (it learns only stamped-vs-error per record, not the
  list of names) than the thing it works around.

- **(b) Call `find.rec` / B-DOS internals by address.** Reuse B-DOS's own list
  reader. **Rejected:** version-fragile — `find.rec`/`seek.base`/`hd.lbuf` move
  between 1.5a / 1.5t / 1.7n (`ANALYSIS.md §3`). Reading the list ourselves
  against the frozen format is the same amount of code with none of the address
  coupling.

- **(c) Drive the `RECORD` command and parse its screen output.** Invoke the
  BASIC `RECORD` lister and scrape the printed names. **Rejected:** clunky and
  fragile — depends on print formatting, screen state, and a BASIC-command entry
  that is not a stable programmatic interface; entangles us with the display.

---

## 4. The on-card data structures (the frozen interface)

All offsets and the geometry math are verified from `bdos15a.src.txt`; the fork
analysis (`ANALYSIS.md`) confirms each is byte-identical on the Trinity 1.5t card
our hardware runs.

### 4.1 Sectors and record geometry

- **Sector size:** 512 bytes (B-DOS / SAM disk sector; `ANALYSIS.md §3`,
  "1600 × 512-byte sectors"; the harness mirrors this as `bdSectorSize = 512`,
  `tools/netboot-oracle/z80/bdos_store.go:61`).
- **Record size:** **1600 sectors = 800 KB** per record (80 tracks × 10 sectors
  × 2 sides; `sel.record` multiplies the record number by 1600 to get its base
  sector: `bdos15a.src.txt:1006-1008`, `LD BC,1600 / CALL mult16`). Each record
  is "essentially a standard SAM disk" acting as drive 2 (the manual,
  `IMG_20260617_162816.txt`).
- **Sector 0 = boot sector.** The card's first sector is the boot block;
  `base` (below) reserves it (`bdos15a.src.txt:1751`, `INC DE ;one extra sct for
  boot`).
- **The record list occupies sectors `1 .. base-1`**, immediately after the boot
  sector and before record 1's data. B-DOS reads it starting at sector 1
  (`bdos15a.src.txt:910-911`, `find.rec`: `LD HL,1 ;start+1 =first t sector`).

### 4.2 Capacity → records → base (the geometry formula)

From `hd.init` (`bdos15a.src.txt:1739-1751`), with `tot.sct` = total 512-byte
sectors on the card:

```
records          = tot.sct / 1600                 ; bdos15a.src.txt:1746 (div24 by 1600)
recordListSectors = (records + 32) / 32           ; bdos15a.src.txt:1748-1750
base             = recordListSectors + 1          ; bdos15a.src.txt:1751 (+1 = boot sector)
usableSectors    = tot.sct - base                 ; bdos15a.src.txt:1757
```

Notes on exact bytes:
- The list-sectors step is literally `EX DE,HL` (HL = records) → `LD DE,32 /
  ADD HL,DE` (HL = records + 32) → `div24` by 32. The inline annotation gloss at
  `:1748` reads "records+1+31 for trunc", but the executed bytes add **32**;
  `ANALYSIS.md §6` states the formula as `(records+32)/32 truncating`. The result
  is the number of sectors needed to hold the record list (each list sector holds
  32 entries — §4.3).
- `base` is therefore `ceil(records / 32) + 1` for record counts that are not
  exact multiples of 32, and `records/32 + 2` when `records` *is* a multiple of
  32 (the `+32` then `/32` adds a full extra sector at the boundary). The
  load-bearing fact for us is the *direction* (read list sectors `1..base-1`); we
  read `base` itself off the card rather than recomputing it where possible (it is
  poked into the seek immediate at card init — `ANALYSIS.md §3`, t&80C2 — but for
  a from-scratch reader we recompute it from `tot.sct` using the formula above
  after the SD CSD gives the capacity).
- These are the **i62-verified** formulas (`ANALYSIS.md §6`: "the same formulas as
  1.5a `hd.init`, the ones the i62 experiment verified against AL 1.5a and
  SimCoupé's `IsBDOSDisk`").

The Trinity 1.5t fork computes `base` identically and pokes it into the seek path
(`ANALYSIS.md §3`, the `sel.record` tail; §6 capacity→records).

### 4.3 The 16-byte record-list entry layout

The record list is an array of **16-byte entries, 32 per 512-byte sector**
(`bdos15a.src.txt:920`, `find.rec`: `LD C,32` entries-per-sector;
`bdos15a.src.txt:923`, `LD B,16` bytes-per-entry). Entry index `n-1` is record
`n` (the list is walked while counting *down* from `last.record`;
`bdos15a.src.txt:937-941`, `get.rno`: `record number = last.record - count + 1`).

Each entry is a verbatim copy of B-DOS's 16-byte `rcd.name` buffer. We know its
shape three ways:

1. **HRECORD reads 16 bytes of a name into `rcd.name`** when selecting by name
   (`bdos15a.src.txt:790-799`, `HRECORD`: `LD B,16 / LD DE,rcd.name / … LD (DE),A`).
2. **`find.rec` compares 16 bytes** of each list entry against `rcd.name`
   (`bdos15a.src.txt:921-930`).
3. **`new.rec` writes the 16-byte `rcd.name` verbatim** into the list entry when
   `RENAME TO` renames a record (`bdos15a.src.txt:2801-2820`: locate the entry at
   `(record-1) … ×16` within its list sector, `LD HL,rcd.name / LD BC,16 / LDIR`).

**Layout of the 16 bytes (offsets within the entry):**

| offset | size | field | citation |
|--------|------|-------|----------|
| +0 | 1 | name byte 0; **bit 7 = write-protect**; bits 0–6 = first char | `bdos15a.src.txt:2778` (`RES 7,(HL) ;no WP` clears bit 7 of `rcd.name[0]` for an un-WP'd name); the free test masks bit 7 (§4.4) |
| +0..+15 | 16 | **the record name** (up to 16 chars), space-padded; bit 7 of each byte stripped on compare/print | `new.rec` copies the full 16-byte `rcd.name` into the entry (`:2801-2820`); `find.rec` compares 16 (`:921-930`); the print loop runs with `B=16`, masking `AND 127` per byte (`L18652`, `:4107-4114`) |

**The whole 16-byte entry is the record name** (corrected 2026-06-19 — Pete
caught this). The manual confirms names run to 16 chars: `RECORD "Trinity
Software"` (16) and `RECORD "Shadebobs Demo"` (14) — a 10-byte field couldn't hold
them (`IMG_20260617_162823.txt`). An earlier draft mis-split this as `name(10) +
trailing(6)`; that "10" came from `new.lab`, which writes a **different** structure
— the per-record **disk label** in the record's *own* sector, where the name is
laid out non-contiguously: `name[0..9]` at +210, the `"BDOS"` id, then
`name[10..15]` (`new.lab:2780-2792`). **Do not conflate the two:** the central
**list** entry is a clean, contiguous **16-byte name**; the per-record **disk
label** (§4.5) is that same name *split around* the `"BDOS"` id. For display, read
the name from the **16-byte list entry** — the disk-label `+210` read yields only
the first 10 chars of a longer name.

### 4.4 The free-entry test

A list entry is **free / unnamed** when its first name byte, with bit 7 (the
write-protect bit) masked off, is zero:

```
A = entry[0]
A = A AND 0x7F        ; strip the write-protect bit
free  ⇔  A == 0       ; an unnamed record
named ⇔  A != 0       ; a named record (in use)
```

This is exactly B-DOS's own test in the `RECORD`-listing walk
(`bdos15a.src.txt:946-948`, `frec3x`: `LD A,(HL) / AND 127 / JR Z,frec4 ;jr no
record name`). It is also what the print path treats as a null name
(`bdos15a.src.txt`: "samdos/bdos null name" comment at the `prtlb2: AND 127 / RET
Z` site). Formatting leaves every record **"empty and unnamed"** (the manual,
`IMG_20260617_162816.txt`: *"After formatting, each RECORD on the SD card is empty
and unnamed"*), so a freshly-formatted record reads as free here.

### 4.5 The per-record `"BDOS"` stamp and disk label

Independently of the list, each record's **own first directory sector** (the
record selected, track 0 / sector 1, linear sector 0) carries:

- a 4-byte **`"BDOS"` stamp at offset +232** — present ⇔ the record is a valid,
  B-DOS-formatted record (`bdos15a.src.txt:2834-2858`, `get.label`: compare the 4
  bytes at +232 against `"BDOS"`; missing ⇒ carry ⇒ `rep81` 'Invalid record');
- a 10-byte **disk label at offset +210**, bit 7 of byte 0 = write-protect
  (`bdos15a.src.txt:2853-2858`, `get.label`: `LD BC,210 / ADD HL,BC ;point to disk
  label`, then `AND 128` for WP).

These per-record fields are what the existing `bdos_inspect_record`
(`src/netboot/bdos_seam.asm`) reads after a record is HRECORD-selected. They are
the **selected-record** view; the list (§4.3) is the **card-level** view. Both
are part of the design — the list enumerates and finds a free record; the
per-record stamp/label confirms the chosen record before the write.

---

## 5. Free-record detection algorithm

```
1. Initialise the SD card and read its capacity (CSD) → tot.sct.
   (SPI ladder: CMD0/CMD8/ACMD41/CMD16; ANALYSIS.md §7. Hardware-gated, §8.)
2. Compute records, recordListSectors, base from tot.sct (§4.2).
3. For listSector in 1 .. base-1:
     read the 512-byte list sector directly off the card (card-absolute,
     CMD17 at the absolute LBA — NOT via HRECORD/HRSAD, which are record-clamped).
     For entry in 0 .. 31 (16 bytes each):
       recordNumber = (listSector-1)*32 + entry + 1
       if recordNumber > records: stop (past the last record).
       firstByte = entry[0] AND 0x7F
       if firstByte == 0:  record is FREE / unnamed.
       else:               record is NAMED (in use); name = entry[0..9], AND 127 per byte.
```

- **Free** = an unnamed list entry (masked byte 0 == 0). This matches the
  manual's "empty and unnamed" formatted state and B-DOS's own `frec3x` test
  (§4.4).
- **Named / in use** = a non-zero masked byte 0; present the name by copying the
  full **16 bytes (+0..+15)** and masking bit 7 off each (the WP/high bits), the
  way `L18652` prints it with `B=16` (`bdos15a.src.txt:4107-4114`).
- **Presenting a record's name.** Trailing spaces are trimmed for display; a free
  record is shown as "free / unnamed". (The name is **16 bytes** space-padded —
  there is no stored length field in the entry. Read it from the list entry, not
  the per-record disk label, which only holds the first 10 chars of a longer name.)
- **Relationship to "empty".** "Unnamed" (list byte 0 == 0) and "empty"
  (formatted, no files) are distinct: a record can be named but empty, or
  formatted-and-unnamed. The manual's formatter produces *empty and unnamed*. For
  the safety gate, "unnamed" is the conservative free signal (a named record is
  never auto-claimed); a deeper "is it empty?" check reads the record's own
  directory after selecting it (§7) and is used only to inform the confirm prompt,
  never to override it.

---

## 6. UX — list, manual-pick, or auto-pick (do both)

Pete's standing intent (recorded in q27, and "do both"): the client supports all
three selection modes, and **always** shows the chosen record's name (or "free /
unnamed") and **confirms** before overwrite. Exact wording is the implementer's
discretion; the three modes are required.

1. **List.** Enumerate every record with its name (the §5 sweep), the way
   `RECORD` with no argument does. Lets the user see what is on the card.
2. **Manual pick — by number or by name.** The user designates a record by
   number (`RECORD 15`) or by name (`RECORD "Shadebobs Demo"` — the manual,
   `IMG_20260617_162823.txt`). We resolve the name against the list (§5), show the
   record's name, and confirm.
3. **Auto-pick a free record.** Scan the list for the first free (unnamed) record
   and propose it. Still show "free / unnamed" and confirm before writing.

In every mode: **show the name (or free/unnamed) → confirm → only then write.**
Never silently claim a record. (The keyboard-input path for the confirm prompt in
emulation is the i138 sysvar-stub work; the detection logic is testable with
injected input meanwhile.)

---

## 7. The write path (once chosen and confirmed)

Unchanged from the existing i119 path, and **correct as shipped** in
`src/netboot/bdos_seam.asm`:

1. **HRECORD-select** the chosen record `n ≥ 1`
   (`bdos_select_record`). The record then behaves as a normal drive-2 SAM disk
   (the manual; `samdos-file-io.md:26-27`, "RECORD 0 = floppy, RECORD n = an
   800 KB mass-storage slice"). (Record 0 is the floppy — the i119 gap-(1)
   record-0 bug; selecting `n ≥ 1` is the fix.)
2. **Read the selected record's own first sector** and run `bdos_inspect_record`
   (`src/netboot/bdos_seam.asm`): the `"BDOS"` stamp at +232 and the disk label at
   +210 (§4.5) confirm the record's identity for the confirm prompt. This is
   normal disk operation (the record is selected; HRSAD reads *its* sectors) — the
   part of PR #442 that is correct and stays.
3. **HSAVE** the staged file as a CODE file (the existing `bdos_fill_save_uifa` +
   `bdos_save_hook` path, the i62-proven HSAVE write). Large files stage through
   paged RAM via the i99 streaming-sink / `fw_span` spanning (i119 gap-(3)).

The detection (§5) finds the record; the write path claims it only after the
confirm gate (§6).

---

## 8. Emulation-first plan

Per CLAUDE.md rule 7, the detection algorithm runs in emulation before hardware.
The harness `CardModel` (`tools/netboot-oracle/z80/bdos_store.go`, the B1
increment) already models a multi-record card: the central `RecordList` of 16-byte
entries **and** per-record sectors (stamp@232, label@210). The B1 implementer
flagged the genuine "B2" follow-up as a **card-absolute list-read path** — the
`seek.base`/`hd.lbuf` card-level read — and that is exactly what this design needs:

- **Add the card-absolute list-read path to the harness.** Model reading list
  sectors `1..base-1` directly (card-absolute LBA, not record-clamped HRSAD), so
  the §5 algorithm — the geometry math, the 16-byte-entry walk, the masked-byte-0
  free test, the name extraction — is **host-verified** against the `CardModel`'s
  `RecordList`. This corrects the B1 `bdos_store.go` doc comment, which currently
  asserts the `RecordList` is "NOT user-reachable"; it *is* reachable, and the
  harness should serve it through a card-absolute read primitive so the detection
  routine runs host-side. (The earlier "this models a hook that does not exist on
  hardware" objection was based on the mistaken not-reachable finding; the
  card-absolute *sector read* is the real SD CMD17 primitive, which does exist.)
- **What stays HARDWARE-GATED.** The raw SD-port I/O — the `&DC`/`&DF` SPI command
  ladder and CMD17 single-block reads (`ANALYSIS.md §7`) — has no emulator
  (`trinity-capabilities.md §8`: "no emulator covers ports `&DC`–`&DF`"). The
  harness models the *result* (the bytes a card-absolute read returns), not the
  SPI transaction. Real-hardware verification of the SPI read against a real
  Trinity card is the final gate; emulation-verified ≠ hardware-verified.

---

## 9. Provenance and open verification items

**Sources.**
- B-DOS implementation (authoritative disassembly):
  `~/sam-archive/bdos/analysis/bdos15a.src.txt` — `list.record`/`find.rec`
  (886-1010), `sel.record`/`sel.base` (993-1027), `seek.base`/`hd.lbuf`
  (1329-1430), `hd.init` geometry (1739-1757), `new.lab`/`new.rec`/`get.label`
  (2773-2858), HRECORD (786-799).
- Fork comparison: `~/sam-archive/bdos/analysis/ANALYSIS.md` — §3 (record model
  identical, device-layer rewrite, routine-address relocation), §4 (hook surface
  untouched, no list hook), §6 (capacity→records→base math, i62-verified), §7
  (SD init / CMD17 read ladder).
- Trinity manual ("Mass Storage Quick Start"):
  `~/sam-archive/trinity-docs/text/IMG_20260617_162816.txt` and
  `IMG_20260617_162823.txt` (records = 800 KB SAM disks in drive 2;
  `RECORD N`/`RECORD "name"`/`RECORD` list; formatter → "empty and unnamed";
  SamDisk reads/writes the card on a PC). Original photos:
  `~/sam-archive/trinity-docs/photos/`.
- Repo: [`docs/notes/trinity-capabilities.md`](../notes/trinity-capabilities.md),
  [`docs/specs/samdos-file-io.md`](samdos-file-io.md),
  `src/netboot/bdos_seam.asm`, `tools/netboot-oracle/z80/bdos_store.go`.

**Open verification items (flagged, not guessed):**
1. **The raw-SD CMD17 / `&DC`–`&DF` port sequence** is reconstructed from the
   B-DOS 1.5t fork RE (`ANALYSIS.md §7`) and `trinity-capabilities.md`. The
   primary period source (SAM Revival 21, "Using the MMC/SD Flash card") is not
   in the photo set, so the exact init/read recipe needs **final confirmation on
   real hardware** before the standalone list-read SPI routine is built. (Until
   then, going through B-DOS's own selected-record reads — §7 — needs no raw SPI;
   the card-absolute list read is the part that needs the standalone SPI routine.)
2. **`base` at the 32-multiple boundary** (§4.2) — the `+32 then /32` adds a full
   extra list sector when `records` is an exact multiple of 32; harmless for
   sweeping `1..base-1` (an empty trailing list sector reads as all-free, and the
   `recordNumber > records` guard in §5 stops the walk), but worth a hardware
   sanity-check on a card whose record count is a multiple of 32.
