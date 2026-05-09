# SAM Coupé disk format & SAMDOS bootability — findings

Authoritative answers to the four research questions, with sources.

## 1. How does the SAM ROM's BOOT command (F9) find DOS on a disk?

**It does not look at the directory at all. It reads track 4, sector 1 raw, and checks for the 4-byte signature "BOOT" (case-insensitive, bit-7 ignored) at offset 256–259 of that sector. If the signature matches, it `JP 8009H` to run the loaded code; otherwise it issues error 53 ("No DOS").**

The mechanism, from the ROM v3.0 annotated disassembly:

- `BOOT` token (0xE9) handler at `D8CD` — eventually `CALL BOOTEX` then `RST 8 / DB BTHK` (auto-load follow-up).
- `BOOTEX` at `D8E5`: finds a free RAM page, pages it at &8000H, calls floppy reset / index-hole detect, then `LD DE,0401H` (track 4, sector 1) and `CALL RSAD` to read that sector to &8000H..&81FFH.
- Signature check at `D967`–`D97D`:
  ```
  BTNOE: LD DE,80FFH
         LD HL,BTWD       ; "BOOT" token table entry, FB94H
         LD B,4
  BTCK:  INC DE           ; first iteration -> 8100H
         LD A,(DE)
         XOR (HL)
         AND 5FH          ; mask out bit 7 and bit 5 -> case-insensitive
         JR Z,BTLY
         RST 8 / DB 53    ; "No DOS"
  BTLY:  INC HL
         DJNZ BTCK
         JP 8009H         ; entry point: 9 bytes past file header start
  ```
- `BTWD` at FB94H is the BASIC keyword table entry for token "BOOT" (`"B","O","O","T"|0x80`). Because the comparison `AND 5FH`s after `XOR`, the high bit (token marker) and bit 5 (case) are both ignored.

So the *disk-level* convention is purely: **the file whose first data sector is track 4 sector 1 must contain the bytes `B O O T` at offsets 256–259 of that sector** (i.e., 247 bytes into the raw file body, after the 9-byte SAMDOS header). There is no directory-entry type byte for "is this DOS?"; the ROM never reads the directory at boot time.

Implications for self-bootability:
- The first SAMDOS file written to the disk is allocated track 4 sector 1 by SAMDOS's sector allocator (sector-address-map bit 0 of byte 0 = T4S1; see samfile.go `SAMMask()`/`Offset()`), so SAMDOS being the *first* file added to a freshly formatted disk is sufficient to put it on T4S1.
- The samdos2 binary is engineered so that "BOOT" lands at byte 247 of the body (= offset 256 of the on-disk sector after the 9-byte header). Verified in `~/git/samdos/res/samdos2.reference.bin` at offset 0xF7: `42 4F 4F D4 = "B","O","O","T"|0x80`.
- Error 53 ("No DOS") is the user-visible failure, documented as code 53 in `sam-coupe_use-guide.pdf` p.129 and triggered at ROM `D976`.
- `BOOT 1` (with a numeric argument) skips the DOS-load and just does the auto-load — see ROM `D8D4` (`JR NZ,BOOTEX`) — i.e. `BOOT 1` forces a re-boot and does not auto-load `auto`.

Cross-confirmation: SAMDOS's own error string "No \"BOOT\" file" lives at `~/git/samdos/src/d.s` line 500 — but that is the SAMDOS-resident error 88 ("No BOOT file"), produced after SAMDOS has loaded and is itself looking for an `auto`/boot file in its directory; it is unrelated to the ROM's signature check.

**Sources**:
- `~/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.pdf`, address D8CD–D97D (BOOT/BOOTEX/BTNOE/BTCK/BTLY) and FB94H (BTWD).
- `~/git/sam-aarch64/docs/sam/sam-coupe_use-guide.pdf` p.115, p.134 (BOOT command), p.129 (error 53 "No DOS").
- `~/git/samdos/res/samdos2.reference.bin` offset 0xF7 (raw "BOOT" signature in the DOS body).

## 2. How is SAMDOS itself laid out on a bootable SAM disk?

SAMDOS is **a normal SAMDOS file** in the directory. Concretely:

- File type byte = **3** (a custom non-standard type — *not* one of the documented public types 5/16/17/18/19/20). Set in `~/git/samdos/src/b.s` line 16 (`defb 3` inside the `if defined (include-header)` block when the build emits the saved-file form).
- The other 8 bytes of the 9-byte file header are zero (b.s lines 17–22): length 0, offset 0, unused 0, pages 0, start page 0. The DOS does not need them — execution jumps to body byte 0 unconditionally from ROM (`JP 8009H`), so the header values are decorative for type-3.
- Filename is conventionally **"samdos2"** (or "samdos") — but as established above the ROM does not match on filename. Convention only.
- The file must be allocated **track 4 sector 1 as its first data sector**. On a freshly-formatted disk this happens automatically because SAMDOS's allocator hands out the first free sector starting at T4S1 (SectorAddressMap bit 0 of byte 0). On a non-empty disk, the bootability requires that whichever file owns T4S1 contains the BOOT signature; in practice SAM users always wrote SAMDOS first.
- The directory entry follows the standard MGT layout (see Q3). Build-from-source in this repo is `~/git/samdos/build.xml` (pyz80, 10000 bytes output to `obj/samdos2`) and `~/git/samdos/res/samdos2.reference.bin` is the canonical 10000-byte body.

There is **no special "DOS" type byte and no reserved "AUTO" filename** at the disk-format level. The ROM's only concept of "DOS" is the 4-byte signature at sector offset 256–259 of T4S1.

(After SAMDOS itself has loaded, *it* then looks for a file named `auto` in the directory and runs it — that is SAMDOS's auto-load behaviour, hook BTHK=128/ALHK=136, ROM addresses D8DC and D8E2; this is the second-stage convention layered on top of the ROM's signature-only first stage.)

**Sources**:
- `~/git/samdos/src/b.s` lines 1–34 (the `if defined (include-header)` block, header emitted at file save).
- `~/git/samdos/README.md` (samdos2 build provenance).
- `~/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.pdf` D8DC, D8E2 (BTHK/ALHK auto-load follow-up).
- `~/git/samdos/src/h.s` (HBOOT auto-load implementation; the SAMDOS side of the second-stage `auto` load).

## 3. The MGT (Miles Gordon Technology) image format on disk

This is fully documented in the SAM Coupé Technical Manual v3.0, pp.78–81 ("DISK FORMAT", "DISK FILE HEADER", "FILE TYPE", "SAMDOS DIRECTORY", "SECTOR ADDRESS MAP", "BIT ADDRESS MAP"). All byte offsets and field sizes below are taken directly from that text, cross-checked against `~/git/samfile/samfile.go`.

### Geometry

- Double-sided, 80 tracks/side, 10 sectors/track, 512 bytes/sector → 819,200 bytes raw.
- Track numbering for the *MGT image format* uses bit 7 of the track byte to flag side: side 0 = tracks 0–79, side 1 = tracks 128–207. (samfile.go `Sector.Offset()` confirms: `int(track>>7)*5120 + (sector-1)*512 + (track&0x7f)*10240`.)
- IBM 3740 standard (tech-man p.78). FDC is VL-1772-02.
- Tracks 0–3 = directory (4 tracks × 10 sectors × 2 entries/sector = **80 directory entries max**). Tracks 4–79 + 128–207 = data (1560 sectors × 510 usable bytes = **795,600 bytes** payload max).

### Sector chaining

Each data sector's last 2 bytes are the link to the next sector of the same file:
- byte 510 = next track number (0–79 / 128–207, or 0 for end-of-file)
- byte 511 = next sector number (1–10)

(Tech-man p.78; also samfile.go `FilePart`/`SectorData.FilePart()`.)

### 9-byte SAMDOS file header

Stored at the beginning of the file's *data* (not in the directory entry):

| Offset | Size | Field |
|--------|------|-------|
| 0     | 1    | File type (see table below) |
| 1–2   | 2    | Length mod 16K (LE) |
| 3–4   | 2    | Offset start within page (LE) |
| 5–6   | 2    | Unused |
| 7     | 1    | Number of pages (full 16K pages) |
| 8     | 1    | Starting page number (bits 0–4; AND 1FH) |

(Tech-man p.78. samfile.go `FileHeader.Raw()` matches exactly.)

Length = `pages * 16384 + (length_mod_16k & 0x3FFF)`. Start address = `(start_page+1)*16384 + (offset & 0x3FFF) - 16384` (subtract because page 0 is the ROM at 0–3FFFH; see tech-man p.79). Note the `+1` then `-16384`: practically, start = `(start_page & 0x1F)*16384 + (offset & 0x3FFF)` if you treat page 1 as RAM bank 0. Both formulations appear in the tech man.

### File-type byte (also used as STATUS in the directory)

| Code | Meaning |
|------|---------|
| 0    | Erased entry |
| 3    | SAMDOS itself (see Q2; not a publicly-documented user type) |
| 5    | ZX Snapshot, 48K (SNP) |
| 16   | SAM BASIC program (BAS) |
| 17   | Numeric array (D ARRAY) |
| 18   | String array ($ ARRAY) |
| 19   | Code file (C) |
| 20   | Screen file (SCREEN$) |

Bit 7 set = HIDDEN. Bit 6 set = PROTECTED. Bit values 1–4 are not assigned in the standard table (tech-man p.79–80; samfile.go `FT_*` constants).

### Directory entry (256 bytes; 2 entries per sector)

Tracks 0–3 sectors 1–10 hold the directory; entry index N lives at sector `1 + (N>>1) % 10` of track `(N >> 4)` in the first-half / second-half of the sector.

| Bytes   | UIFA | Field |
|---------|------|-------|
| 0       | 0    | Status / file type byte |
| 1–10    | 1–10 | Filename (10 chars, space-padded; case stored verbatim, matched case-insensitively) |
| 11      |      | MSB of sector count |
| 12      |      | LSB of sector count |
| 13      |      | Track of first sector (0–79 / 128–207) |
| 14      |      | Sector of first sector (1–10) |
| 15–209  |      | Sector address map, 195 bytes (1560 bits — one bit per data sector, bit 0 of byte 0 = T4S1) |
| 210–219 |      | "MGT future and past" — used by Plus D, unused on SAM, reserved for MGT |
| 220     | 15   | MGT flags (MGT use only) |
| 221–231 | 16–26 | File-type-dependent metadata (see below) |
| 232–235 | 27–30 | Reserved (4 bytes spare) |
| 236     | 31   | Start page (bits 0–4 used; bits 5–7 undefined) — same semantics as file header byte 8 |
| 237–238 | 32–33 | Page offset 8000H–BFFFH (LE) — same semantics as file header bytes 3–4 |
| 239     | 34   | Number of pages — same as file header byte 7 |
| 240–241 | 35–36 | Length mod 16K (LE) — same as file header bytes 1–2 |
| 242–244 | 37–39 | Execution address (CODE) or auto-run line number (BASIC) |
| 245–253 | 40–47 | Spare 8 bytes |
| 254–255 |      | Reserved for MGT future use |

(Tech-man pp.79–81. samfile.go `FileEntryFrom` and `FileEntry.Raw()` match field-for-field, with the same big-endian sector count at offsets 11–12 and identical byte ranges. The `MGTFutureAndPast` and `MGTFlags` Go fields correspond directly to bytes 210–219 and 220.)

#### File-type-dependent metadata block (bytes 221–231 in directory; UIFA 16–26)

- **Type 16 (BASIC)**: bytes 221–223 = program length excluding variables; 224–226 = program length including numeric variables; 227–229 = program length including all variables and the gap before string/array vars.
- **Type 17/18 (numeric/string array)**: file-type/length and array name.
- **Type 20 (SCREEN$)**: byte 221 = screen mode.

(Tech-man p.80.)

### How file kinds differ in their directory entry

- **BASIC (type 16)**: bytes 221–229 carry program/variables lengths. Bytes 242–244 carry the auto-run start line number when present (samfile.go `SAMBASICStartLine` aliases the same bytes). Start address from bytes 236/237/238 typically points into the BASIC area.
- **CODE (type 19)**: bytes 221–231 are unused/zero. Bytes 242–244 carry the execution address (samfile.go `ExecutionAddress`); a value of `FF FF FF` means "no auto-execute" (samfile.go `AddCodeFile` uses 0xFF/0xFFFF default). Start address is a free choice in 16K..512K.
- **SCREEN$ (type 20)**: byte 221 = mode.
- **DOS file (type 3)**: directory entry follows the same skeleton; SAMDOS itself emits one when it first writes itself onto a disk via SAVE.

### Sector address map (SAM)

- 195 bytes = 1560 bits, one per data sector.
- bit 0 of byte 0 = track 4 sector 1; bits proceed sector-then-track (samfile.go `SAMMask`):
  `bitOffset = (track & 0x7F)*10 + (sector - 1) + ((track & 0x80)>>7)*800 - 40`
  (the `-40` skips the 4 directory tracks; the `*800` is the side-1 offset, since tracks 128–207 = side 1).
- Per-file SAM is stored in the directory entry at bytes 15–209.
- Disk-wide BAM (Bit Address Map) is **not stored** — SAMDOS computes it at boot time as the bitwise OR of every entry's SAM (tech-man p.81).

### Sector layout summary table

| Region | Tracks | Sectors | Use |
|--------|--------|---------|-----|
| Directory | 0–3   | 1–10 | 80 directory entries (256 bytes each) |
| Data side 0 | 4–79 | 1–10 | File data |
| Data side 1 | 128–207 | 1–10 | File data |

**Sources**:
- `~/git/sam-aarch64/docs/sam/sam-coupe_tech-man_v3-0.pdf` pp.78–81 (definitive disk format, SAMDOS directory, file header, sector address map).
- `~/git/samfile/samfile.go` (Go implementation matching the tech man, useful for sector→byte arithmetic and byte-order details).
- `~/git/sam-aarch64/docs/notes/sam-file-io.md` (UIFA layout for the user-facing API; same offsets as the directory entry's UIFA column).

## 4. The samdos2.sbt format

`.sbt` is the "SAM BooTable" file format defined by Andrew Collier. **It is a raw, headerless memory image with no metadata.** Files are conventionally loaded at &8000 and the size is the file size. They are not bootable on real hardware as-is — they are an emulator-side convenience that pretends to be a disk.

Specifically:

- **No file header, no preamble, no magic bytes, no checksum.** Just a stream of bytes that becomes the first byte of memory at the load address. Verified in `~/git/simcoupe/Base/Disk.cpp` lines 605–631 (`FileDisk::IsRecognised` accepts any file ≤ MAX_SAM_FILE_SIZE; `FileDisk::FileDisk(...)` synthesizes the 9-byte SAMDOS header from hardcoded values).
- **Implied load address &8000H, start page 1.** SimCoupé hardcodes `m_data[3]=0x00, m_data[4]=0x80` (offset start &8000H) and `m_data[8]=0x01` (start page 1). See Disk.cpp lines 620–625.
- **Implied file type 19 (CODE)** when wrapped to look like a disk.
- **Implied filename "autoExec  "** when wrapped to look like a disk (Disk.cpp line 654). The "auto" prefix triggers the SAMDOS auto-load, which then runs the file because its execution metadata says auto-execute.
- **Implied auto-execute at &8000H**: SimCoupé sets `data[242]=2; data[243]=m_data[3]; data[244]=m_data[4]` — execution address byte triplet pointing at the load address with the auto-execute marker (Disk.cpp lines 681–683).
- **Maximum size**: roughly 1560×510 bytes minus directory overhead (i.e. the file payload must fit on one disk). The samdos2.sbt that ships with SimCoupé is 10,000 bytes (matches `~/git/samdos/res/samdos2.reference.bin`).
- **Manual description** (`~/git/simcoupe/Manual.md` lines 95–98):
  > .SBT — Sam BooTable files, created by Andrew Collier. These are self-booting files designed to be copied to an empty SAM disk, then booted. While not technically disk images, SimCoupe treats them as such (read-only).

So a `.sbt` is best understood as: "the byte stream you would get back from a SAMDOS `LOAD CODE "name" CODE 32768`, with no header." To use it on real hardware, you would need to either (a) save it to a disk as a CODE file with start=&8000 and execute=&8000 named `auto` (so SAMDOS's auto-load runs it after SAMDOS itself has loaded), or (b) for the *bootstrap* DOS itself, write its bytes verbatim onto T4S1 onwards prefixed with the standard 9-byte SAMDOS header (type=3, length=10000, offset=&8000, pages=0, start_page=1) and ensure "BOOT" lands at sector offset 256.

For samdos2 specifically, the .sbt body **already** contains "BOOT" at body offset 247 (=sector offset 256 once a 9-byte header is prepended) — i.e. the binary is structured so that wrapping it as a CODE file with the standard header produces a valid bootable T4S1.

**Sources**:
- `~/git/simcoupe/Base/Disk.cpp` lines 39–43 (SBT detection by extension), 60–62 (`FileDisk` constructor for SBT), 605–705 (full FileDisk implementation showing the synthesized header values).
- `~/git/simcoupe/Manual.md` lines 95–98 (informal SBT format description, attribution to Andrew Collier).
- `~/git/samdos/res/samdos2.reference.bin` (canonical 10,000-byte body).
- `~/git/samdos/src/b.s` lines 14–34 (the conditional 9-byte file header that the build *would* prepend for a directly-bootable disk file).

## Sources index

- **SAM Coupé Technical Manual v3.0** — `~/git/sam-aarch64/docs/sam/sam-coupe_tech-man_v3-0.pdf` (definitive for disk format, directory entry, file header, file types, sector address map; pp.78–81).
- **SAM Coupé User's Guide / Manual** — `~/git/sam-aarch64/docs/sam/sam-coupe_use-guide.pdf` (BOOT command p.115, p.134; error 53 "No DOS" p.129).
- **SAM ROM v3.0 Annotated Disassembly** — `~/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.pdf` (BOOT routine D8CD; BOOTEX D8E5; T4S1 read-and-signature-check D967–D97D; BTWD keyword table FB94H; auto-load hooks BTHK/ALHK D8DC/D8E2).
- **SAMDOS source** — `~/git/samdos/src/{a,b,c,d,e,f,h,samdos}.s`; in particular `b.s` for the file-header emission on save, `d.s` line 500 for the SAMDOS-side "No \"BOOT\" file" string.
- **SAMDOS reference binary** — `~/git/samdos/res/samdos2.reference.bin` (10,000 bytes; "BOOT" at offset 0xF7).
- **samfile (Go)** — `~/git/samfile/samfile.go` (a near-canonical Go implementation of the on-disk MGT format, byte offsets and bit ordering).
- **SimCoupé SBT handling** — `~/git/simcoupe/Base/Disk.cpp` lines 605–710 (FileDisk class); `~/git/simcoupe/Base/SAMIO.cpp` lines 980–1018 (Rst8Hook DOS substitution); `~/git/simcoupe/Manual.md` lines 95–98 (SBT description).
- **NVG FTP** — Browsed `https://ftp.nvg.ntnu.no/pub/sam-coupe/docs/`, `/docs/manuals/technical/`, `/docs/manuals/software/`. The NVG technical archive's "SAM Coupe Technical Manual" zip is the same v3.0 manual already present locally; no additional authoritative MGT/SAMDOS internals docs were identified beyond what is already covered locally. (The MasterDOS V1/V2 manuals on NVG would extend Q3 toward MasterDOS-specific extensions — out of scope for this brief.)

## What is *not* answered authoritatively

- The exact provenance and full spec of `.sbt` as a *file format* (rather than as SimCoupé's emulator convention) is not in any of the local documents. Andrew Collier authored it; SimCoupé's `FileDisk` is the de facto reference implementation, and matches the load-at-&8000 convention. No header is the format. If you want a pre-2020 spec document, NVG `magazines/` (FORMAT) was not exhaustively browsed.
- Whether MasterDOS or Pro-DOS use a *different* boot signature was not cross-checked; the question asked about SAMDOS specifically and the SAM ROM, both of which only know about the "BOOT" 4-byte signature at sector offset 256 of T4S1.
