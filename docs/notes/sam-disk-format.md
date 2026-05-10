# SAM Coupé MGT disk-image format — authoritative reference

Scope: the on-disk byte layout of a `.mgt` (Miles Gordon Technology) SAM
Coupé floppy image as written/read by SAMDOS, with every byte cited to
the SAM Coupé Technical Manual v3.0, the annotated ROM v3.0 disassembly,
SAMDOS source, or `samfile.go`. This is the master reference for
`tools/build-disk.sh`'s sector-, directory-, and file-allocation work.

This document covers the **disk-image structure** (geometry, sector
layout, directory area, sector-address-map encoding, file chaining,
boot mechanism). For the **on-disk file body header** and the
**80-byte HDR/HDL buffer** that ROM SAVE/LOAD use, see
`docs/notes/sam-file-header.md`. For the **REL PAGE FORM** address
encoding used in start/length/exec fields, see `docs/notes/sam-paging.md`.

---

## TL;DR

- `.mgt` = 819,200-byte raw image, **cylinder-interleaved**: cylinder N
  (= 10240 bytes) holds side-0-track-N (5120 bytes) followed by
  side-1-track-N (5120 bytes). 80 cylinders × 2 sides × 10 sectors ×
  512 bytes = 819200.
- Side encoded in **bit 7 of the track byte**: tracks 0–79 = side 0,
  tracks 128–207 = side 1.
- **Tracks 0–3 are the directory**: 4 tracks × 10 sectors × 2 entries
  per sector = 80 directory slots, each exactly 256 bytes.
- **Tracks 4–79** (side 0) and **tracks 128–207** (side 1) hold file
  data — 1560 sectors of 512 bytes. Each data sector reserves the last
  2 bytes (offsets 510–511) for the next-sector chain pointer; only 510
  bytes per sector are payload.
- Every file's directory entry contains a 195-byte **sector-address
  map** (1560 bits, one per data sector). SAMDOS computes the
  disk-wide free-space map (BAM) at allocation time as the bitwise OR
  of every entry's map.
- A bootable image must place a file with the literal bytes `B O O T`
  at sector offsets 256–259 of T4S1. ROM `BOOTEX` reads T4S1 raw to
  `&8000`, checks bytes 256–259, and `JP 8009H` on match.
- The 9-byte **on-disk file body header** (every file) and the 256-byte
  **directory entry** are documented in `docs/notes/sam-file-header.md`.
  This doc covers only the disk-image-level structures.

---

## 1. Geometry and image layout

### 1.1 Physical disk parameters

Tech Manual v3.0 (`docs/sam/sam-coupe_tech-man_v3-0.txt:4262-4275`):

> The internal SAM disk drive is a Citizen 3.5" slimline drive. ...
> the disks are formatted as double sided, 80 track per side, 10
> sectors per track, to the IBM 3740 standard. ... 1560 data sectors
> of 512 bytes (798720 bytes).

| Parameter | Value | Source |
|-----------|-------|--------|
| Sides | 2 | `sam-coupe_tech-man_v3-0.txt:4265` |
| Cylinders (tracks per side) | 80 | `sam-coupe_tech-man_v3-0.txt:4271` |
| Sectors per track | 10 | `sam-coupe_tech-man_v3-0.txt:4272` |
| Bytes per sector | 512 | `sam-coupe_tech-man_v3-0.txt:4272` |
| Total raw bytes | 819200 | 80 × 2 × 10 × 512 |
| Directory tracks | 4 (= tracks 0–3 of side 0) | `sam-coupe_tech-man_v3-0.txt:4272-4274, 4340-4343` |
| Data sectors | 1560 | `sam-coupe_tech-man_v3-0.txt:4274` |
| Payload bytes per sector | 510 (last 2 = chain link) | `sam-coupe_tech-man_v3-0.txt:4277-4280` |
| Maximum payload | 1560 × 510 = 795600 bytes | derived |
| Floppy controller chip | VL-1772-02 | `sam-coupe_tech-man_v3-0.txt:4264` |
| Format standard | IBM 3740 | `sam-coupe_tech-man_v3-0.txt:4266` |

### 1.2 Cylinder-interleaved image layout

The Tech Manual does not prescribe an image-file-layout convention; it
describes the disk *as a disk*. The `.mgt` image file convention used
by the SAM emulator ecosystem (SimCoupé, samfile, build-disk.sh) is:

```
image[0       ..  5119]  = cylinder 0, side 0, sectors 1..10
image[5120    .. 10239]  = cylinder 0, side 1, sectors 1..10
image[10240   .. 15359]  = cylinder 1, side 0, sectors 1..10
image[15360   .. 20479]  = cylinder 1, side 1, sectors 1..10
...
image[N*10240+0    .. +5119]   = cylinder N, side 0
image[N*10240+5120 .. +10239]  = cylinder N, side 1
```

Authoritative reference: SimCoupé `Disk.cpp:164`,

```cpp
auto offset = ((static_cast<size_t>(cyl) * MGT_DISK_HEADS + head) * m_sectors
               + sector_index) * NORMAL_SECTOR_SIZE;
```

(`/Users/pmoore/git/simcoupe/Base/Disk.cpp:164`,
`/Users/pmoore/git/simcoupe/Base/Disk.h:28-41` for the
`MGT_DISK_HEADS=2`, `MGT_DISK_CYLS=80`, `MGT_DISK_SECTORS=10`,
`NORMAL_SECTOR_SIZE=512` constants).

### 1.3 Track-byte encoding (MGT/SAMDOS convention)

The track-byte stored in directory entries and sector chain links uses
**bit 7 to flag side**:

| Track byte range | Meaning |
|------------------|---------|
| `0x00`–`0x4F` (0..79)   | Side 0, cylinders 0..79 |
| `0x80`–`0xCF` (128..207)| Side 1, cylinders 0..79 |
| `0x50`–`0x7F`, `0xD0`–`0xFF` | Invalid |

A directory entry's first-sector pointer at byte 0x0D is in this
encoding. The next-sector chain link at sector offsets 510–511 uses
the same encoding.

This convention is implicit in SAMDOS's `dst:` (`samdos/src/b.s:221`)
allocator state and is made explicit in `samfile.go:567-568`:

```go
func (sector *Sector) Offset() int {
    return int(sector.Track>>7)*5120 + (int(sector.Sector)-1)*512 + int(sector.Track&0x7f)*10240
}
```

(`/Users/pmoore/git/samfile/samfile.go:567-568`.) `track>>7` is 0 for
side 0 and 1 for side 1. `track&0x7f` strips the side bit to leave the
cylinder number 0..79.

build-disk.sh's `sector_offset()` (`tools/build-disk.sh:70-71`)
matches:
```python
((track >> 7) * 5120) + ((sector - 1) * 512) + ((track & 0x7f) * 10240)
```

### 1.4 Region table

| Region | Tracks (track-byte range) | Sectors | Image byte range |
|--------|---------------------------|---------|------------------|
| Directory | 0–3 (side 0) | 1–10 | `0x00000`–`0x04FFF` (20480 bytes) |
| Data side 0 | 4–79 | 1–10 | `0x0A000`–`0x7FFFF` minus side-1 interleave; see §1.5 |
| Data side 1 | 128–207 | 1–10 | `0x01400`–`0x803FF` minus side-0 interleave |

(Tech Manual `sam-coupe_tech-man_v3-0.txt:4271-4275, 4340-4343`.)

### 1.5 Worked example: byte offsets for selected sectors

Computed via `(track>>7)*5120 + (sector-1)*512 + (track&0x7f)*10240`:

| Track / Sector | Computed offset | Hex | Region |
|----------------|-----------------|-----|--------|
| T0 S1   | 0      | `0x00000` | Directory entry 0/1 |
| T0 S2   | 512    | `0x00200` | Directory entry 2/3 |
| T0 S10  | 4608   | `0x01200` | Directory entry 18/19 |
| T1 S1   | 10240  | `0x02800` | Directory entry 20/21 |
| T3 S10  | 35328  | `0x08A00` | Directory entry 78/79 |
| T4 S1   | 40960  | `0x0A000` | First data sector (side 0) — **BOOT signature lives here** |
| T4 S10  | 45568  | `0x0B200` | |
| T5 S1   | 51200  | `0x0C800` | |
| T128 S1 | 5120   | `0x01400` | First data sector (side 1) |
| T207 S10| 814080 | `0xC8C00` | Last data sector (side 1) |

(Note T128 S1 = 5120, which is *before* T4 S1 in image-file order
because side 1 of cylinder 0 sits inside cylinder 0's 10240-byte
block. There is no directory data on side 1 — directory tracks 0–3
exist only on side 0. T128–T131 = side-1 cylinders 0–3 are usable
data sectors, even though T0–T3 of side 0 are not.)

---

## 2. Directory area (tracks 0–3, side 0)

### 2.1 Directory layout

- 4 tracks × 10 sectors × 512 bytes = 20480 bytes.
- 80 directory entries, each 256 bytes (= 2 entries per sector).
- Slot index `N` (0–79) lives at image byte offset `N * 256`.
- Slot 0 and 1 are at T0 S1 (image offsets 0 and 256).
- Slot 78 and 79 are at T3 S10 (image offsets 35328 and 35584).

(Tech Manual `sam-coupe_tech-man_v3-0.txt:4340-4343`: "The first 4
tracks of the disk are allocated to the disk directory, starting at
track 0, sector 1. These 4 tracks give us 40 sectors each split into
two 256 bytes entries. Each of these entries will identify one file,
thus allowing up to 80 entries in the directory.")

samfile.go writes a directory entry at slot `index` via
`offset := index << 8; raw[offset:offset+256] = ...`
(`/Users/pmoore/git/samfile/samfile.go:554-559`).

### 2.2 Directory tracks have no sector chain links

The first 4 tracks contain directory entries packed two per sector;
they are NOT part of the sector chain mechanism (there is no chain
*through* the directory). Bytes 510–511 of directory sectors are
simply the trailing bytes of directory entry slot `2k+1`'s last
fields (specifically dir bytes 0xFE–0xFF of the second entry, which
fall in `ReservedB` — Tech Manual: "FOR FUTURE USE BY MGT ONLY",
`sam-coupe_tech-man_v3-0.txt:4400`).

### 2.3 Allocation order

SAMDOS allocates the first free slot (lowest index whose Type byte at
offset 0 is 0x00 / erased). Tech Manual
`sam-coupe_tech-man_v3-0.txt:4351-4356`:

> 0  STATUS/FILE TYPE. ... If the byte is 0 then the file has been
> erased. If the file is HIDDEN then bit 7 is set. If the file is
> PROTECTED then bit 6 is set.

samfile.go's `Used()` (`/Users/pmoore/git/samfile/samfile.go:347-355`)
treats an entry as free if `FirstSector.Track == 0` OR if the type
byte is unrecognised (`UNKNOWN` prefix); the latter is a samfile
quirk, not authoritative. SAMDOS itself only checks Type==0.

build-disk.sh hand-rolls slot allocation, putting samdos2 in slot 0
(T0 S1, image offset 0), auto in slot 1 (T0 S1, image offset 256),
stub in slot 2 (T0 S2, image offset 512), IN in slot 3 (T0 S2, image
offset 768).

### 2.4 256-byte directory entry layout

Refer to `docs/notes/sam-file-header.md` §2 for the full per-byte
table. Summary of regions:

| Bytes (hex) | Field | Source |
|-------------|-------|--------|
| `0x00`        | Status / file type | `sam-coupe_tech-man_v3-0.txt:4351-4356` |
| `0x01`–`0x0A` | Filename (10 bytes, space-padded) | `sam-coupe_tech-man_v3-0.txt:4358-4359` |
| `0x0B`–`0x0C` | Sector count, **big-endian** (MSB at 0x0B, LSB at 0x0C) | `sam-coupe_tech-man_v3-0.txt:4360-4361`; `samfile.go:243` |
| `0x0D`        | First-sector track byte (with side bit 7) | `sam-coupe_tech-man_v3-0.txt:4362` |
| `0x0E`        | First-sector sector number (1..10) | `sam-coupe_tech-man_v3-0.txt:4363` |
| `0x0F`–`0xD1` | Sector address map, 195 bytes (1560 bits) | `sam-coupe_tech-man_v3-0.txt:4364-4365`; see §3 |
| `0xD2`–`0xDB` | "MGT future and past", 10 bytes — Tech Manual says "not used by SAMDOS" but **see note below** | `sam-coupe_tech-man_v3-0.txt:4366-4368`; `samdos/src/f.s:462-471` |
| `0xDC`        | MGT flags ("MGT use only") | `sam-coupe_tech-man_v3-0.txt:4369` |
| `0xDD`–`0xE7` | File-type-dependent metadata, 11 bytes | `sam-coupe_tech-man_v3-0.txt:4370-4381`; details in `docs/notes/sam-file-header.md` §2 |
| `0xE8`–`0xEB` | Reserved 4 bytes | `sam-coupe_tech-man_v3-0.txt:4382` |
| `0xEC`        | Start-page byte (mirror of body header byte 8) | `sam-coupe_tech-man_v3-0.txt:4388-4389` |
| `0xED`–`0xEE` | Page offset LE 16-bit (mirror of body header bytes 3–4) | `sam-coupe_tech-man_v3-0.txt:4390-4392` |
| `0xEF`        | Pages (mirror of body header byte 7) | `sam-coupe_tech-man_v3-0.txt:4393` |
| `0xF0`–`0xF1` | Length-mod-16K LE (mirror of body header bytes 1–2) | `sam-coupe_tech-man_v3-0.txt:4394-4395` |
| `0xF2`–`0xF4` | Execution address (CODE) or auto-RUN line (BASIC) | `sam-coupe_tech-man_v3-0.txt:4396-4398`; ROM `E137-E141` |
| `0xF5`–`0xFD` | Spare 8 bytes | `sam-coupe_tech-man_v3-0.txt:4399` |
| `0xFE`–`0xFF` | "FOR FUTURE USE BY MGT ONLY" | `sam-coupe_tech-man_v3-0.txt:4400` |

**Note on bytes 0xD2–0xDB** (Tech Manual L4366–4368):

> 210-219  MGT FUTURE AND PAST (10 BYTES) These were used in the PLUS D
>          directory but are not used by the SAMDOS. They are allocated
>          to MGT for future use.

This is **a documentation error**. SAMDOS *does* use bytes 211–219 (9
bytes) as a verbatim cache of the file body's 9-byte header. See
`samdos/src/f.s:462-471` (`svhd` writes 9 bytes to `fsa+211` *and* to
the file body via `sbyt`) and `samdos/src/c.s:1376-1379` (`gtfle`
reads 9 bytes from offset 211 of the dir-entry buffer). Byte 210
(`0xD2`) is unused. Trust the SAMDOS source over the Tech Manual.

The build-disk.sh `write_directory_entry` function leaves bytes
0xD2–0xDB at zero. This is benign because SAMDOS consumes the cache
only during in-RAM directory walks — a fresh disk read populates the
in-RAM cache from the file body header on first access. However,
populating bytes 0xD3–0xDB with the same 9 bytes that prefix the file
body would match what a real SAMDOS save produces.

### 2.5 The first-sector pointer is mandatory

A non-erased directory entry has `Type != 0` AND must have a non-zero
`FirstSector.Track`. samfile.go's `Used()` test
(`samfile.go:347-355`) checks both. The track byte is the
side-encoded form (with bit 7 for side 1), so a valid first-sector
track is in {4..79, 128..207}. T0–T3 (directory tracks themselves)
must never appear as a file's first sector.

---

## 3. Sector address map (SAM)

### 3.1 Map size and bit ordering

- 195 bytes = 1560 bits, one per data sector. Tech Manual L4405–4406:

  > SAMDOS allocates 195 bytes to the sector address map, giving 1560
  > bits, which is the exact number of sectors available for storage
  > on the drive.

- Bit 0 of byte 0 = T4 S1 (Tech Manual L4411: "Bit 0 of the first
  byte is allocated to track 4 sector 1").
- Bits proceed sector-then-track-then-side. The encoding is
  authoritative in `samfile.go:562-565`:

```go
func (sector *Sector) SAMMask() (offset uint8, mask uint8) {
    bitOffset := (int(sector.Track)&0x7f)*10 + int(sector.Sector) - 1 +
                 ((int(sector.Track)&0x80)>>7)*800 - 40
    return uint8(bitOffset >> 3), 1 << bitOffset & 0x07     // BUG (see §3.4)
}
```

(`/Users/pmoore/git/samfile/samfile.go:562-565`.)

build-disk.sh's `sector_bit()` matches the bit-offset formula
(`tools/build-disk.sh:73-74`):
```python
(track & 0x7f) * 10 + (sector - 1) + ((track & 0x80) >> 7) * 800 - 40
```

### 3.2 Why `-40`

The first 4 directory tracks (T0..T3) each have 10 sectors, total 40
sectors, which are not in the sector-address-map domain (the map
covers data sectors only). Subtracting 40 makes the bit index for
T4S1 = 0. Confirmed by working through:

- T4 S1: `(4)*10 + 0 + 0 - 40 = 0` → byte 0 bit 0. ✓
- T4 S10: `(4)*10 + 9 + 0 - 40 = 9` → byte 1 bit 1. ✓
- T79 S10: `(79)*10 + 9 + 0 - 40 = 759` → byte 94 bit 7. ✓
- T128 S1: `0 + 0 + 800 - 40 = 760` → byte 95 bit 0. ✓ (first
  side-1 data sector)
- T207 S10: `(127)*10 + 9 + 800 - 40 = 2039`? — wait. T207 is
  side 1 cylinder 79, encoded as `0x80 | 79 = 0xCF = 207`. So
  `track & 0x7f = 79`. `(79)*10 + 9 + 800 - 40 = 1559` → byte 194
  bit 7. ✓ (the 1560th bit, last data sector).

### 3.3 Why `*800`

There are exactly 800 side-1 data sectors (80 cylinders × 10 sectors;
side 1 has *no* directory tracks because the directory lives only on
side 0 cylinders 0..3). So adding 800 to the side-1 bit indices
keeps them after all 760 side-0 data sectors (76 cylinders × 10
sectors).

The `(76)*10 = 760` side-0 figure: side 0 has cylinders 0–79 = 80
total, minus 4 directory cylinders = 76 data cylinders × 10 sectors =
760. The map starts at bit 0 = T4S1 (cylinder 4 side 0) and
contiguously enumerates cylinders 4..79 sectors 1..10 of side 0
(bits 0..759), then jumps to cylinders 0..79 sectors 1..10 of side 1
(bits 760..1559).

### 3.4 The samfile.go `SAMMask` bug

`samfile.go:564` has a real operator-precedence bug:

```go
return uint8(bitOffset >> 3), 1 << bitOffset & 0x07
```

Go's operator precedence (Go spec § "Operators": multiplicative
operators incl. `<<`, `>>`, `&`, `&^` have higher precedence than
additive operators; among same-precedence multiplicative, evaluation
is left-to-right) parses this as:
```
1 << bitOffset & 0x07  ===  (1 << bitOffset) & 0x07
```

For `bitOffset >= 3`, `(1 << bitOffset) & 0x07 == 0`. The correct
expression is `1 << (bitOffset & 0x07)` (or equivalently `1 << (bitOffset % 8)`).

**Effect**: for any sector whose bit-position-within-byte is 0, 1, or
2 (i.e. bits 0/1/2 of the LSByte of `bitOffset`), `SAMMask` returns
non-zero. For positions 3..7 it returns 0, so the corresponding bits
in the map are never set. After multiple `samfile add`s, allocations
collide — sectors get reused. The bug is real and currently
unpatched.

build-disk.sh works around this by hand-rolling the four directory
entries' sector-address maps with a known-correct bit-set helper at
`tools/build-disk.sh:76-78`:
```python
def set_sector_in_map(sam_map: bytearray, track: int, sector: int) -> None:
    b = sector_bit(track, sector)
    sam_map[b // 8] |= 1 << (b % 8)        # correct (cf. samfile bug above)
```

### 3.5 Per-file vs disk-wide map

- The 195-byte field at directory bytes `0x0F`–`0xD1` is **per-file**:
  the bits of all sectors *this file* occupies are set (Tech Manual
  L4408–4414).
- The disk-wide free map is **not stored**: SAMDOS computes it at
  allocation time as the bitwise OR of every entry's per-file map
  (Tech Manual L4419–4420: "The bit address map is not stored on the
  disk by SAMDOS. It is generated by performing a bitwise OR of each
  file's sector address map.").

---

## 4. File body and sector chaining

### 4.1 Per-sector layout

Each data sector is 512 bytes:

```
Byte offset | Use
0..509      | File payload (510 bytes)
510         | Next-sector track byte (with side bit 7), or 0x00 = end-of-file
511         | Next-sector sector number (1..10), or 0x00 = end-of-file
```

Tech Manual `sam-coupe_tech-man_v3-0.txt:4277-4280`:

> Although each data sector can hold 512 bytes, only 510 bytes of them
> are available for storage. The last two bytes of the data sector are
> used by the DOS to locate the next part of the file stored. Byte 511
> [zero-indexed: offset 510] holds the next track used by the file,
> while byte 512 [offset 511] holds the next sector.

(Note Tech Manual uses 1-based byte numbering in this paragraph;
"byte 511" = zero-indexed offset 510 = first link byte.)

build-disk.sh's `write_file_chain` (`tools/build-disk.sh:115-131`):
```python
sd[510] = nt
sd[511] = ns
# last sector: link bytes stay (0,0) = end of file
```

### 4.2 First-sector pointer in directory

The directory entry's bytes 0x0D–0x0E (`FirstSector.Track`,
`FirstSector.Sector`) point at the **first** data sector of the file.
SAMDOS reads that sector, copies bytes 0..509 into the file's RAM
buffer, then follows the `(byte 510, byte 511)` link to the next
sector and repeats until the link is `(0, 0)`.

### 4.3 The 9-byte file body header is part of the payload

The 9-byte file header (Tech Manual L4286–4295,
`docs/notes/sam-file-header.md` §1) is the **first 9 bytes of the
file body**, i.e. the first 9 bytes of the first sector's 510-byte
payload region. SAMDOS does not strip it during sector reads — it
appears in the in-memory file image as part of the data. `LBYT` /
`LDBLK` etc. read it like any other byte.

For a CODE file loaded via the BASIC `LOAD CODE` path, the ROM and
SAMDOS cooperate: SAMDOS reads the header to populate HDL+0..HDL+8
indirectly via the directory entry mirror at bytes 0xEC–0xF4, while
the body bytes 9.. become the actual file payload at the load
address.

For SAMDOS itself loaded by the ROM `BOOT` mechanism (§5), the 9-byte
header is treated as ordinary code — the ROM `JP 8009H` lands 9 bytes
into the loaded sector, skipping the header. SAMDOS source `b.s:27`
sets `org.adjust = 9` precisely so that the assembler places real
code at `&8009`.

### 4.4 Sector count vs file length

The directory entry's **sector count** at bytes 0x0B–0x0C
(big-endian, MSB at 0x0B per Tech Manual L4360–4361 and
`samfile.go:243`) is `ceil((9 + body_length) / 510)`. For a
10000-byte body: `ceil(10009 / 510) = ceil(19.625) = 20`. ✓

build-disk.sh writes:
```python
e[0x0b] = (len(chain) >> 8) & 0xff             # sector count BE high
e[0x0c] = len(chain) & 0xff                    # sector count BE low
```
(`tools/build-disk.sh:90-91`.) ✓

The **file length** at directory bytes 0xEF / 0xF0–0xF1 (Pages,
LengthMod16K) covers only the payload, not the header.

The discrepancy is important if you compute file length from sector
count: `sector_count * 510` includes the 9-byte header. The Tech
Manual does not address this directly; it is implicit in the SAVE
logic.

---

## 5. The BOOT mechanism

### 5.1 What ROM `BOOTEX` does

ROM v3.0 disassembly `D8E5` (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:20473-20598`):

1. `LD HL, ALLOCT+1FH` — scan ALLOCT (page-allocation table) downward
   from page 31 to find a free page or one already tagged as DOS.
   On match, `LD A,L; CALL SELURPG` pages that physical page in at
   `&8000` (section C). (`...rom...txt:20473-20489`)
2. `LD C, 0xD0; CALL SDCX; CALL REST` — reset the floppy controller
   and seek to track 0. (`...rom...txt:20493-20495`)
3. Test for the index hole; raise error 55 ("Missing disc") if none
   detected within 6 × 65536 outer loops.
   (`...rom...txt:20503-20512`)
4. `LD DE, 0401H` — set track=4, sector=1.
   (`...rom...txt:20524`)
5. `RSAD: ...` — read the sector at DE to `HL=8000H`. The 512 bytes
   of T4 S1 land at `&8000..&81FF`.
   (`...rom...txt:20528-20564`)
6. `BTNOE: LD DE, 80FFH; LD HL, BTWD; LD B, 4` — set up signature
   compare. (`...rom...txt:20582-20584`)
7. `BTCK: INC DE; LD A,(DE); XOR (HL); AND 5FH; JR Z,BTLY; RST 8 / DB 53` —
   compare bytes `&8100`, `&8101`, `&8102`, `&8103` against `BTWD`.
   `XOR (HL); AND 5FH` masks bits 5 and 7, so the comparison is
   case-insensitive (bit 5 = case bit) and bit 7 (token marker) is
   ignored. (`...rom...txt:20585-20593`)
8. On all 4 matched: `JP 8009H`. (`...rom...txt:20598`)

### 5.2 BTWD — the literal "BOOT"

`BTWD` is at ROM address `FB94H`, the BASIC keyword table entry for
the `BOOT` token (E9):
```
FB94 424F4FD4   BTWD:   DC "BOOT"   ; E9 USED BY BOOT TO CHECK FILE NAME
```
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:26919`.)

Bytes: `42 4F 4F D4` = ASCII `B O O` followed by `0xD4` (= ASCII `T`
with bit 7 set, the standard SAM BASIC token-end marker). The
signature-check routine masks bit 7 (`AND 5FH`), so the on-disk
bytes can be `42 4F 4F 54` (plain "BOOT") or any case-mix; both
match.

### 5.3 The signature lives at sector offset 256–259

`LD DE, 80FFH` followed by `INC DE` on first `BTCK` iteration gives
DE = `8100H` for the first compare, then `8101H`, `8102H`, `8103H`.
The buffer is at `8000H..81FFH` (512 bytes of T4 S1), so the signature
is at **buffer offsets 256–259** = sector offsets 256–259 of T4 S1.

For a SAMDOS file with the standard 9-byte body header, signature
offset 256 corresponds to **body offset 247** (256 − 9 = 247).
Verified in `~/git/samdos/res/samdos2.reference.bin` at offset 0xF7
(=247): bytes `42 4F 4F D4` = "BOOT" with bit 7 set on T. The
samdos2 source is engineered so that this signature lands there
naturally (the assembler-emitted code at body offset 247 happens to
be a token-table entry for the `BOOT` keyword used in SAMDOS's own
BASIC-keyword handling).

### 5.4 The entry point `JP 8009H`

After signature match, ROM does `JP 8009H` (= 0x8000 + 9) — i.e. it
jumps **9 bytes past the start of the loaded sector**, skipping the
9-byte file body header. SAMDOS's `b.s:27`
(`org.adjust = 9` when `include-header` is not defined) ensures real
code starts there.

### 5.5 Implications for self-bootability

To make a `.mgt` image bootable on real SAM hardware:

1. The file owning T4 S1 (i.e. the file whose dir-entry bytes
   0x0D=0x04, 0x0E=0x01 — track 4, sector 1) must contain bytes
   `42 4F 4F 54` (case-insensitive, bit 7 either) at sector offsets
   256–259. With the standard 9-byte SAMDOS header prepended to the
   body, that means **body offset 247 must contain `B O O T`**.
2. The byte at sector offset `256+256 = 256` of the loaded T4 S1 also
   serves as the entry point: `8009H` is `8 + 9 = 17`(decimal)
   bytes into the body... wait — that's wrong. `JP 8009H` jumps to
   sector buffer offset 9, which is body byte 0 (because the 9-byte
   header occupies sector offsets 0–8 of the buffer, and body byte 0
   is at sector offset 9). So execution starts at body byte 0 of
   the file.
3. The directory entry's type byte at `0x00` is **never read by the
   ROM at boot time**. The signature match is purely on the sector
   contents at T4 S1. SAMDOS's own SAVE writes type 3 for itself
   (samdos source `b.s:16`), but build-disk.sh writes type 19 (CODE)
   to keep `samfile ls` happy — both work. Confirmed by reading the
   ROM `BOOTEX` flow: there is no path that reads the directory.
4. The filename is also never matched by the ROM. By convention
   SAMDOS itself is named `samdos2`, but any name works; what
   matters is that the file owns T4 S1 and contains the BOOT
   signature.

### 5.6 `BOOT 1` — skip-DOS-load variant

`BOOT 1` (with numeric arg ≠ 0) executes the DOS-load+RE-BOOT path
WITHOUT the auto-load follow-up. ROM `D8D4`:
```
3AC25B   LD A,(DOSFLG)
A7       AND A
2803     JR Z,BOOTNR
CF       RST 08H
88       DB ALHK    ; AUTO-LOAD via DOS hook 136
```
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:20453-20471`.)

The `BOOT` token handler at `D8CD` first does syntax checking, then
`GETBYTE` reads the optional numeric arg. If non-zero
(`JR NZ,BOOTEX`), it bypasses the no-arg path which would invoke
the auto-load via `BTHK` (DB 0x80) or `ALHK` (DB 0x88).

### 5.7 Second-stage auto-load (SAMDOS-side)

After SAMDOS has booted (via the ROM signature path), the ROM RST 8 /
DB BTHK invokes SAMDOS's auto-load hook to find a file matching
`AUTO*` (with wildcard) of type 16 (BAS).

SAMDOS source `h.s:201-212` defines the auto-file template `autnam`:
```asm
autnam:        defb 1
               defb &ff
               defb &ff
               defb "D"
               defb &10           ; type = 16 (BAS)
               defm "AUTO*     "  ; 10-char name, '*' wildcard
               defm "    "
               ...
```

`hauto:` (`samdos/src/h.s:224-237`) copies the template into the
search buffer and calls `gtflx` to walk the directory looking for a
type-16 file whose name prefix matches. The match is
case-insensitive (built into SAMDOS's name comparator).

This is why our `auto` file (type 16, lowercase name) is found by
SAMDOS after SAMDOS itself has loaded: SAMDOS searches for a
type-16 entry whose name starts with "AUTO" (any case). The
filename in our directory entry is `b"auto      "` = 10 bytes
space-padded, lowercase. SAMDOS strips/ignores the wildcard and
matches by prefix.

---

## 6. File-type byte (status / file type)

Tech Manual `sam-coupe_tech-man_v3-0.txt:4304-4314`:

> Each file type in the SAMOOS [sic — SAMDOS] is allocated a numeric
> identifier:
>
>             5     - ZX Snapshot file     SNP 48k
>             16    - SAM BASIC program    BAS
>             17    - Numeric array        D ARRAY
>             18    - String array         $ ARRAY
>             19    - Code file            C
>             20    - Screen file          SCREEN$

Cross-referenced with ROM `E019` block
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22032`):

> ;0 - TYPE (16=BAS, 17=NUM ARRAY, 18=STR ARRAY, 19=CODE, 20=SCREEN$)

| Code | Mnemonic | Description |
|------|----------|-------------|
| `0`    | (erased) | Status byte = 0 marks the entry as erased / free. (`sam-coupe_tech-man_v3-0.txt:4351-4354`) |
| `3`    | (SAMDOS) | SAMDOS-internal type for SAMDOS itself when it self-saves. NOT in any public type list — appears only in `samdos/src/b.s:16`. |
| `5`    | SNP      | ZX 48K snapshot. (`sam-coupe_tech-man_v3-0.txt:4309`) |
| `16`   | BAS      | SAM BASIC program. (`sam-coupe_tech-man_v3-0.txt:4310`) |
| `17`   | D ARRAY  | Numeric (D) array. (`sam-coupe_tech-man_v3-0.txt:4311`) |
| `18`   | $ ARRAY  | String ($) array. (`sam-coupe_tech-man_v3-0.txt:4312`) |
| `19`   | C        | Code file. Generic blob. (`sam-coupe_tech-man_v3-0.txt:4313`) |
| `20`   | SCREEN$  | Screen file. (`sam-coupe_tech-man_v3-0.txt:4314`) |

### 6.1 Bit 6 / bit 7 attributes

Tech Manual L4351–4356:

> If the byte is 0 then the file has been erased. If the file is
> HIDDEN then bit 7 is set. If the file is PROTECTED then bit 6 is set.

So a CODE file marked HIDDEN+PROTECTED would have type byte
`0x13 | 0x40 | 0x80 = 0xD3`.

### 6.2 No "data" type

There is no public file-type code for arbitrary binary data. The
convention is to use type 19 (CODE) for blobs. samfile's `AddCodeFile`
(`/Users/pmoore/git/samfile/samfile.go:483-499`) is the helper for
this case. build-disk.sh uses type 19 for the `IN` data file
(`tools/build-disk.sh:238`) — this is correct convention.

### 6.3 Type 3 (SAMDOS) — not in the public list

SAMDOS source `samdos/src/b.s:14-22` defines (when `include-header` is
defined at build time):
```asm
if defined (include-header)
    ; disk file header
               defb 3
               defw 0
               defw 0
               defw 0
               defb 0
               defb 0
endif
```

Type 3 with all-zero remaining bytes is what SAMDOS would write for
itself if the `include-header` flag were enabled. The shipped
`samdos/res/samdos2.reference.bin` does NOT have this header (verified:
file is exactly 10000 bytes; first byte is `21`, the opcode for
`LD HL,nn`, not `03`). So type 3 is the *intended* value when SAMDOS
is wrapped with a header for SAVE — but the ROM `BOOT` mechanism
doesn't care.

build-disk.sh deliberately uses type 19 instead (CODE) for the
samdos2 slot, with the explicit comment at `tools/build-disk.sh:144-148`
that this is to keep `samfile ls` happy. **This is non-canonical but
harmless** — see actionable items below.

---

## 7. Type-specific encoding for each file type

These are partial summaries; full tables are in
`docs/notes/sam-file-header.md` §2.

### 7.1 Type 16 (SAM BASIC, "BAS")

- Body header at bytes 0..8 of file body: `[0x10, lenL, lenH, offsetL, offsetH, FF, FF, pages, startPage]`.
- Directory bytes 0xDD–0xE5 carry **three 3-byte page-form values**:
  - 0xDD–0xDF = `(NVARS - PROG)` page-form (program length excluding vars)
  - 0xE0–0xE2 = `(NUMEND - PROG)` page-form (program + numeric vars)
  - 0xE3–0xE5 = `(SAVARS - PROG)` page-form (program + numeric vars + gap before strings)
  - Source: ROM `E0B4` SAVE block, `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22163-22180`
- Directory bytes 0xF2–0xF4 = auto-RUN line:
  - byte 0xF2 = `0x00` (marker: there IS an auto-RUN line; ROM E137-E141)
  - bytes 0xF3–0xF4 = LE 16-bit auto-RUN line number
  - To opt out of auto-RUN: set 0xF2 = `0xFF`. ROM E287 checks this:
    `CP 0FFH; JR NZ,HDLDEX` — only takes the auto-run path if non-FF.
- Body content: tokenised BASIC program; see §8.

### 7.2 Type 17 / 18 (D ARRAY / $ ARRAY)

- Directory bytes 0xDD–0xE7 carry the array's TLBYTE (type/length
  byte) and 10-byte name. Tech Manual L4371–4372; ROM `E1D7`:
  `LD HL,TLBYTE; LD DE,HDR+16; LD BC,11; LDIR`
  (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22354-22357`).

build-disk.sh does not produce array files; not relevant to the
audit.

### 7.3 Type 19 (CODE, "C")

- Body header at bytes 0..8: `[0x13, lenL, lenH, offsetL, offsetH, FF, FF, pages, startPage]`.
- Directory bytes 0xDD–0xE7 are unused / zero. (samfile.go's
  `AddCodeFile` at `samfile.go:483-499` does not set them.)
- Directory bytes 0xEC–0xF1 mirror the body header's start/length
  fields (same values).
- Directory bytes 0xF2–0xF4 = execution address in REL PAGE FORM
  (page-byte at 0xF2, LE offset at 0xF3–0xF4 with bit 15 set per
  `docs/notes/sam-paging.md`). Or `FF FF FF` to disable auto-exec.

### 7.4 Type 20 (SCREEN$)

- Directory byte 0xDD = screen MODE. (Tech Manual L4373–4374; ROM
  `E146`: `LD (HDR+16),A ;MODE`,
  `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22259`.)
- Body is the raw screen data (length depends on mode, computed in
  `SCRLEN`).

build-disk.sh does not produce screen files.

### 7.5 Type 5 (ZX 48K snapshot)

- Body is a Spectrum snapshot. The header semantics are documented in
  the Tech Manual at `sam-coupe_tech-man_v3-0.txt:2985`: "(NB There
  is an extra file type in SAMDOS - .SNP)." Detailed format not
  needed for build-disk.sh.

---

## 8. Tokenised SAM BASIC body format

Used by build-disk.sh's `auto` file (type 16). Format reverse-engineered
from `samfile/sambasic.go:20-62` (the `Output()` parser):

### 8.1 Per-line structure

```
[lineNum_BE_hi, lineNum_BE_lo, lineLen_LE_lo, lineLen_LE_hi,
 ...lineLen bytes of body content including 0x0D terminator...,
 [next line or 0xFF]]
```

| Bytes | Field | Notes |
|-------|-------|-------|
| 0–1   | Line number, **big-endian** 16-bit | `sambasic.go:26` |
| 2–3   | Line length, **little-endian** 16-bit; counts body bytes incl. the 0x0D terminator | `sambasic.go:27` |
| 4..   | Line body | See §8.2 |

End-of-program marker: **`0xFF` at the start of where the next line's
high-byte-of-line-number would be** (`sambasic.go:23-25`). Build-disk.sh
emits this at the end of `BASIC_BODY` (`tools/build-disk.sh:194`).

### 8.2 Line body content

| Byte / range | Meaning |
|--------------|---------|
| `0x0D`         | End-of-line terminator. (`sambasic.go:44-46`) |
| `0x0E`         | Numeric-form marker; followed by 5 fixed bytes. (`sambasic.go:42-43`; ROM `15B1`/`15B7`/`MAKESIX` at `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:5247-5267`) |
| `0xFF`         | Extended-keyword prefix; the next byte indexes into the keyword table. (`sambasic.go:34-41`) |
| `0x85`–`0xF6`  | Direct keyword token; index = byte − 0x3B into `keywords[]`. (`sambasic.go:49-54`) |
| `< 0x20` (other than 0x0D, 0x0E) | Control / flag chars; printed as `{N}` by samfile output. (`sambasic.go:47-48`) |
| `0x20`–`0x7F` (printable ASCII) | Literal characters. (`sambasic.go:55-57`) |

### 8.3 Numeric form (the 5 bytes after 0x0E)

The bytes match the SAM internal floating-point stack representation
(ROM STKSTORE at `1CF0`,
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:6872-6884`):

```
[ exponent, sign(E), D, C, B ]
```

For a small (≤ 16-bit) integer:
- `exponent = 0x00` (small-int marker)
- `sign = 0x00` (positive) or `0xFF` (negative)
- `[D, C]` is unused / padding (`D = 0x00`)
- `[C, B]` would be `[lo, hi]` but per ROM convention small integers
  are stored as `00 sign lo hi 00` — i.e. exponent=0, sign, lo, hi,
  trailing zero.

build-disk.sh's `num()` helper at `tools/build-disk.sh:181-182`:
```python
def num(n: int) -> bytes:
    return bytes([0x0e, 0x00, 0x00, n & 0xff, (n >> 8) & 0xff, 0x00])
```

This produces the 6-byte sequence `[0x0E (marker), 0x00 (exp), 0x00 (sign+),
lo, hi, 0x00 (padding)]`, which matches the small-positive-integer
convention. ✓

### 8.4 The keyword token table

`keywords[]` in `samfile/keywords.go:1-194` is indexed by `byte − 0x3B`.
Direct one-byte keyword tokens are in the range `0x85..0xF6`
(`sambasic.go:49`); two-byte keyword tokens are `0xFF` followed by an
index byte (`sambasic.go:34-41`).

Tokens used by build-disk.sh:

| Token bytes | Index into keywords[] | String | Source |
|-------------|----------------------|--------|--------|
| `0x95`        | 90 (= 0x95 − 0x3B)   | `LOAD` | `keywords.go:95` |
| `0xFF, 0x6C`  | 49 (= 0x6C − 0x3B)   | `CODE` | `keywords.go:54` |
| `0xE4`        | 169 (= 0xE4 − 0x3B)  | `CALL` | `keywords.go:174` |

Build-disk.sh writes the line body at `tools/build-disk.sh:188-192`:
```python
stmt_load = (bytes([0x95, 0x20, 0x22]) + b"stub"           # LOAD <space> "
             + bytes([0x22, 0x20, 0xff, 0x6c, 0x20])        # " <space> CODE <space>
             + str(LOAD_ADDR).encode() + num(LOAD_ADDR))    # 24576<numeric form>
stmt_call = bytes([0xe4, 0x20]) + str(LOAD_ADDR).encode() + num(LOAD_ADDR)
                                                           # CALL <space> 24576<numeric form>
line_body = stmt_load + b"\x3a" + stmt_call + b"\x0d"      # ... : ... <CR>
```

The `\x3a` (= ":") is the SAM BASIC statement separator; `\x0d` is
the line terminator. The string `str(LOAD_ADDR).encode()` is the
ASCII representation that the BASIC editor stores alongside the
numeric form (the numeric form is the "invisible" 5-byte form per
ROM `15A5–15D9`).

### 8.5 BASIC body length and the directory entry

Build-disk.sh constructs:
```python
BASIC_BODY = (bytes([0x00, 0x0a, len(line_body) & 0xff, (len(line_body) >> 8) & 0xff])
              + line_body + b"\xff")
```
(`tools/build-disk.sh:193-194`.)

This produces:
- `00 0a` = line number 10 (BE).
- `lo hi` = `len(line_body)` LE.
- `line_body` = the line tokens including final `0x0D`.
- `0xFF` = end-of-program marker.

The body length stored in the body header bytes 1–2 (LengthMod16K)
is `len(BASIC_BODY)`, including the 4 prefix bytes and the trailing
0xFF marker.

---

## 9. Self-bootability summary (build-disk.sh-relevant checklist)

For an `.mgt` image to boot SAMDOS on real SAM hardware:

1. ✅ Image is exactly 819200 bytes, all zero except for written
   regions. (`tools/build-disk.sh:54`.)
2. ✅ A SAMDOS file (any directory entry type — ROM does not check)
   with its first sector at T4 S1 — i.e. `dir[0x0D]=0x04, dir[0x0E]=0x01`.
   build-disk.sh ensures by allocating samdos2 to slot 0 with chain
   `[(4,1), (4,2), ..., (5,10)]` (`tools/build-disk.sh:149`).
3. ✅ Bytes `42 4F 4F 54` (or any case mix) at sector offsets 256–259
   of T4 S1. With the standard 9-byte SAMDOS body header, this
   means body offset 247 must be `B O O T`. The vendored
   `reference/samdos/samdos2.bin` satisfies this (`offset 0xF7 = 42 4F 4F D4`).
4. ✅ The 9-byte header prepended to the samdos2 body must be exactly
   9 bytes, so that `JP 8009H` lands on body byte 0 (= start of
   samdos2 code). The header bytes themselves are not read by ROM
   BOOT — only the length matters.
5. ✅ Image's directory tracks may otherwise contain anything; ROM
   doesn't read them.

For SAMDOS to then auto-load a user `auto` file:
6. A directory entry with type `0x10` (BAS) and name starting with
   `AUTO` (case-insensitive). build-disk.sh uses lowercase `auto`
   (`tools/build-disk.sh:199`) — matched by SAMDOS's wildcard
   template `AUTO*` (`samdos/src/h.s:206`).

---

## 10. Sources index

**SAM Coupé Technical Manual v3.0**
(`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_tech-man_v3-0.txt`):
- L2974–3068: 80-byte HDR/HDL buffer layout (header buffer format).
- L4256–4266: SAMDOS / disk drive overview (Citizen 3.5", VL-1772-02
  controller, IBM 3740 format).
- L4269–4280: DISK FORMAT — geometry, sector chain bytes 510–511.
- L4284–4298: DISK FILE HEADER — 9-byte body header.
- L4304–4314: FILE TYPE — type-byte values 5/16/17/18/19/20.
- L4316–4329: MODULO LENGTH / OFFSET / STARTING PAGE arithmetic.
- L4338–4400: SAMDOS DIRECTORY — 256-byte directory entry layout.
- L4403–4414: SECTOR ADDRESS MAP — 195-byte / 1560-bit format.
- L4417–4427: BIT ADDRESS MAP — disk-wide BAM is computed, not stored.
- L4459–4502: USER INFORMATION FILE AREA (UIFA) — 48 bytes used by
  SAMDOS hook codes.
- L4505–4536: SAMDOS HOOK CODES — INIT, HGTHD, HLOAD, HSAVE, etc.

**SAM Coupé User's Guide**
(`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_use-guide.txt`):
- L4938–4946: BOOT command user-facing semantics.
- L5457–5459: error 53 "No DOS".
- L5664–5666: BOOT in glossary.

**SAM ROM v3.0 annotated disassembly**
(`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`):
- L1183, L1194: BTHK (128) / ALHK (136) DOS-hook EQUates.
- L20453–20471: BOOT token handler (`D8CD`).
- L20473–20598: BOOTEX routine (page-find, RSAD T4S1, signature check).
- L20582–20598: BTNOE/BTCK/BTLY signature compare and `JP 8009H`.
- L26919: `BTWD` keyword-table entry "BOOT" (`FB94H`).
- L22025–22054: `E019` block — HDR/HDL buffer documentation.
- L22057–22119: SLMVC / HDR2 — SAVE/LOAD entry.
- L22136–22141: BASIC autorun-line setup at `HDR+HDN+6`.
- L22163–22180: SAVE-time computation of three BASIC prog-length
  triplets at HDR+16/+19/+22 (= dir bytes 0xDD/0xE0/0xE3).
- L22247: `LD A,19` on the LOAD/SAVE CODE path.
- L22259: `LD (HDR+16),A ;MODE` for SCREEN$.
- L22467–22484: LOAD CODE exec-address logic (HDLDEX, R1OFFCLBC).
- L4499–4527: PDPSR2 — REL PAGE FORM decoder.
- L14773–14786: UNSTLEN — REL PAGE FORM encoder.
- L6872–6884: STKSTORE — small-integer numeric storage format.
- L5247–5267: `LK0ELP` / `MAKESIX` / `INSERT5B` — inline `0x0E`
  numeric-form insertion in BASIC line bodies.

**SAMDOS source** (`/Users/pmoore/git/samdos/src/`):
- `b.s:7-22`: optional 9-byte body header (type 3, all zero) emitted
  when `include-header` is defined.
- `b.s:27`: `org.adjust = 9` — code starts at offset 9 of body so
  `JP 8009H` lands on real code.
- `b.s:220-240`: SAMDOS RAM cache variables (`dct`, `dst`, `port1`,
  `port2`, `port3`).
- `b.s:249-260`: SAMDOS RAM cache of last-loaded file's 9-byte header
  (`hd001` ... `page1`).
- `f.s:462-471`: `svhd` — writes 9 bytes from `hd001` to dir-entry
  offset 211, also via `sbyt` to file body.
- `c.s:1376-1379`: `gtfle` — reads 9 bytes from dir-entry offset 211
  back into `hd001`.
- `h.s:8-26`: `rxhed` — copy 48-byte UIFA from caller IX into SAMDOS
  workspace.
- `h.s:38-56`: `txhed`/`txrom` — copy DIFA back to caller IX+80.
- `h.s:201-212`: `autnam` — `AUTO*` wildcard template for second-stage
  auto-load.
- `h.s:215-237`: `init`/`initx`/`hauto` — SAMDOS hook 128 (INIT)
  implementation: looks for `AUTO*` type-16 file and runs it.
- `h.s:308-321`: `cals` — SAMDOS's PDPSR2-equivalent helper (page in
  HL's page at section C, rewrite HL).

**SAMDOS reference binary** (`/Users/pmoore/git/samdos/res/samdos2.reference.bin`):
- 10000 bytes; "BOOT" signature at body offset 0xF7 (= sector offset
  0x100 once 9-byte header is prepended).
- `~/git/samdos/build.xml` — build provenance via pyz80, compares
  output against `samdos2.reference.bin`.

**samfile (Go MGT inspector)** (`/Users/pmoore/git/samfile/`):
- `samfile.go:21-43`: `FileEntry` struct — directory entry.
- `samfile.go:51-58`: `FileHeader` struct — 9-byte body header.
- `samfile.go:72-80`: `FT_*` constants for file types.
- `samfile.go:89-95`: `Start()` and `Length()` decoders.
- `samfile.go:240-266`: `FileEntryFrom([0x100]byte)` — directory entry
  parser.
- `samfile.go:268-310`: `FileEntry.Raw()` — directory entry emitter.
  Note big-endian sector count at L275–276.
- `samfile.go:347-355`: `Used()` — slot-occupied test.
- `samfile.go:357-393`: `Output()` — `samfile ls -i` formatter.
- `samfile.go:395-412`: BASIC-specific accessors.
- `samfile.go:444-452`: 9-byte body header parser inside `File()`.
- `samfile.go:483-499`: `AddCodeFile` — code-file emitter.
- `samfile.go:501-509`: `CreateHeader` — synthesises body header from
  directory entry.
- `samfile.go:511-552`: `addFile` — top-level write path.
- `samfile.go:554-560`: `WriteFileEntry` — directory entry → image.
- `samfile.go:562-565`: `SAMMask` — **buggy** sector-bit mask
  computation (operator-precedence bug; see §3.4).
- `samfile.go:567-569`: `Sector.Offset` — track/sector → image byte
  offset.
- `samfile.go:578-595`: `FileHeader.Raw` and `File.Raw`.
- `sambasic.go:20-62`: `Output()` BASIC tokeniser-aware printer.
- `sambasic.go:26`: line numbers stored big-endian.
- `sambasic.go:27`: line lengths stored little-endian.
- `keywords.go:1-194`: keyword token table indexed by `byte − 0x3B`.

**SimCoupé**
(`/Users/pmoore/git/simcoupe/Base/`):
- `Disk.h:28-41`: MGT geometry constants — `MGT_DISK_HEADS=2`,
  `MGT_DISK_CYLS=80`, `MGT_DISK_SECTORS=10`, `MGT_TRACK_SIZE=5120`,
  `MGT_IMAGE_SIZE=819200`.
- `Disk.h:35-37`: `MGT_DIRECTORY_TRACKS=4`.
- `Disk.cpp:164`: `(cyl * heads + head) * sectors + sector_index)`
  cylinder-interleaved offset formula (matches samfile.go and
  build-disk.sh).
- `Disk.cpp:605-705`: SBT (Sam BooTable) wrapping — synthesises a
  directory entry around a raw memory image.
- `Manual.md:95-98`: SBT format informal description.

**Cross-references in this repo**:
- `docs/notes/sam-file-header.md` — 9-byte body header, 256-byte
  directory entry per-byte tables, HDR/HDL 80-byte buffer.
- `docs/notes/sam-paging.md` — REL PAGE FORM, LMPR/HMPR/VMPR, the
  exec-address-encoding question.
- `docs/notes/sam-file-io.md` — UIFA layout (48 bytes; same fields as
  directory entry's metadata block, different offsets).
- `docs/notes/samdos2-auto-run-analysis.md` — second-stage auto-load
  hook 128 details (separate from the ROM BOOT mechanism).

---

## 11. Open / unverified

- **Is the cylinder-interleaved layout an MGT specification or a
  SimCoupé/samfile convention?** I could not find an explicit
  description in the Tech Manual. SimCoupé and samfile both implement
  it, and `.mgt` files in the wild use it (the GoodSamC2 collection
  loaded by Pete inspects correctly with samfile's layout). Treating
  it as the de-facto `.mgt` standard is safe.
- **Tech Manual L4366–4368 vs SAMDOS source**: the Tech Manual claims
  bytes 210–219 are not used by SAMDOS, but `samdos/src/f.s:462-471`
  clearly writes 9 bytes there. Trust SAMDOS source. Tech Manual is
  wrong.
- **Tech Manual L4393 vs L4341**: Tech Manual says directory has 40
  sectors at L4341 (4 tracks × 10 sectors), and earlier prose at
  L4271–4274 says "1560 data sectors of 512 bytes (798720 bytes)". It
  also says "The first 4 tracks of the disk are given up to the
  SAMDOS directory, leaving 156 tracks available for storage." 156 ×
  10 = 1560 data sectors. ✓ Internally consistent.
- **Is there a "data" file type?** Search of Tech Manual file-type
  table (L4304–4314) shows only 5/16/17/18/19/20. Type 19 (CODE) is
  the conventional choice for arbitrary blobs. ROM `E019` block
  (L22032) confirms only types 16, 17, 18, 19, 20 are recognised by
  BASIC SAVE/LOAD.
