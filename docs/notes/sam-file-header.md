# SAM Coupé file header — authoritative reference

Scope: the **9-byte on-disk file header** that prefixes every file body
on a SAMDOS .mgt disk, the **256-byte directory entry** that catalogues
each file, and the **80-byte in-memory header buffers HDR/HDL** that
the ROM uses during SAVE / LOAD / VERIFY / MERGE. Cross-references
include the SAM Tech Manual v3.0, the annotated ROM v3.0 disassembly,
SAMDOS source, and `samfile.go`.

This doc validates `tools/build-disk.sh`'s four hand-rolled headers
against the spec, byte by byte, and explains why `samfile ls` reports
`Program length: 0` for our `auto` file.

---

## TL;DR

1. There are **three** distinct header structures involved, and they are
   all subtly different. Conflating them is what got us into trouble:
   - **9-byte on-disk file header** (file body offsets 0–8). Same
     layout for every file type. Carries: type, length-mod-16K,
     page-offset, two unused bytes, num-pages, start-page.
   - **256-byte directory entry** (one per directory slot). Carries
     filename, sector chain, sector address map, MGT flags, and a
     **type-dependent 11-byte block at offsets 0xDD–0xE7** plus a
     parsed metadata mirror at 0xEC–0xF4 (start-page / page-offset /
     pages / length-mod-16K / exec-or-start-line).
   - **80-byte in-memory header buffer** at HDR=`&4B00` (request) and
     HDL=`&4B50` (loaded) used by the BASIC ROM. Contains: type,
     name(10), filler(4), flags(1), type-specific(11), spare(5),
     three 3-byte page-form fields starting at HDN=offset 31, and a
     40-byte comment area.

2. **The 9-byte file body header is the same 9 bytes for all five SAM
   public types** (16/17/18/19/20). The type-specific stuff (BASIC
   prog-length, screen mode, exec-addr) lives in the **directory
   entry**, not in the file body header.

3. **`build-disk.sh` is mostly correct but has three concrete bugs** in
   the on-disk and directory data it writes for the `auto` BASIC file
   and the `stub` / `IN` code files. These are the byte-level fixes:

   | File | Field | Current bytes | Should be | Effect |
   |---|---|---|---|---|
   | `auto` body header bytes 3–4 (PageOffset) | LE word | `00 00` | `D5 9C` (=0x9CD5) | BASIC ROM uses on-disk offset; for SAM BASIC, 0x9CD5 is the canonical "PROG=0x5CD5" load offset (8000H-form). |
   | `auto` dir entry bytes 0xDD–0xE5 (BASIC prog-length triplets) | 9 bytes | `00…00` | three 3-byte page-form values: program length, prog+nvars, prog+nvars+gap | `samfile ls` reads `FileTypeInfo[0..2]` here as "Program length"; with all zeros it prints `Program length: 0`. Real ROM also wants these populated for NVARS / NUMEND / SAVARS recovery on LOAD (Tech Man L3033, ROM L22038–22040). |
   | `auto` dir entry bytes 0xEC–0xEE (StartPage / PageOffset) | 3 bytes | `00 00 00` | `00 D5 9C` | Mirror of body header bytes 3–4 + 8. SAMDOS expects them to match the body header. |
   | `samdos2`, `stub`, `IN` body header bytes 5–6 | 2 bytes | `00 00` | `FF FF` (cosmetic, see below) | A real BASIC SAVE writes `FF FF`; harmless either way per spec ("unused"). Not a bug, just non-canonical. |

4. **"Program length: 0" is `samfile`'s honest report** of dir-entry
   bytes 0xDD–0xDF being all zero. `samfile.go:395-397` reads
   `FileTypeInfo[0]<<16 | FileTypeInfo[1] | FileTypeInfo[2]<<8`. Our
   build-disk.sh leaves those bytes at zero (it never populates them).
   A real BASIC SAVE would put a 3-byte page-form value there equal to
   `(end_of_program - PROG)` — see `~/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22163-22180`
   (the `RNTVL` / `LD IY,HDR+16` loop).

5. **Disagreement between sources to flag:**
   - Tech Manual v3.0 p.81 says directory entry bytes 210–219 are "MGT
     future and past, not used by SAMDOS, allocated to MGT for future
     use." But SAMDOS source (`f.s:462`, `c.s:1376`) writes/reads the
     verbatim 9-byte file body header at directory-entry offsets
     211–219. So bytes 210–219 ARE used by SAMDOS as an internal
     mirror of the on-disk body header. The Tech Manual section on
     bytes 210–219 is misleading; trust the SAMDOS source.
   - Tech Manual (Tape Header section, line 3037–3052) and DISK FILE
     HEADER section (line 4286) describe the byte ordering of
     `LengthMod16K`, `PageOffset`, etc. with slightly different
     wording (the ROM-buffer description at L3037 calls bytes 31, 32-33,
     34, 35-36 the "page", "offset", "length-pages", "length-mod16K";
     the disk-header section at L4286 calls bytes 7 and 8 "number of
     pages" and "starting page number"). These ARE consistent if you
     map carefully — see the per-byte tables below.

---

## 1. The 9-byte on-disk file body header

This is the canonical byte sequence stored at offsets 0..8 of every
SAMDOS file body. **Same layout for all file types** (16, 17, 18, 19,
20, plus the SAMDOS-internal type 3).

| Offset | Size | Field          | Notes                                                                   |
|--------|------|----------------|-------------------------------------------------------------------------|
| 0      | 1    | `Type`         | 5/16/17/18/19/20 (or 3 for SAMDOS itself).                              |
| 1–2    | 2    | `LengthMod16K` | LE 16-bit. Combined with `Pages` to get true length.                    |
| 3–4    | 2    | `PageOffset`   | LE 16-bit. By convention bit 15 set, value lies in `0x8000..0xBFFF`.    |
| 5–6    | 2    | (unused)       | Tech Man L3043, L4293. **Real BASIC SAVE writes `FF FF` here**, not 0. |
| 7      | 1    | `Pages`        | Number of full 16K pages.                                               |
| 8      | 1    | `StartPage`    | Bits 0–4 = page number (mask `& 0x1F`). Bits 5–7 undefined.             |

Sources: Tech Man L4284–4295 (`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_tech-man_v3-0.txt:4284-4295`); samfile `FileHeader.Raw()` (`/Users/pmoore/git/samfile/samfile.go:578-590`); samfile `File()` parser (`/Users/pmoore/git/samfile/samfile.go:444-452`).

### Length / start address arithmetic

```
Length      = Pages * 16384 + (LengthMod16K & 0x3FFF)
StartAddress = ((StartPage & 0x1F) + 1) * 16384 + (PageOffset & 0x3FFF) - 16384
             = (StartPage & 0x1F) * 16384 + (PageOffset & 0x3FFF)
```

(Tech Man L4316–4332: "AND with 1FH to get the page number ... multiply
the page number by 16384, add the offset, and subtract 4000H since the
ROM occupies 0–3FFFH". samfile `FileHeader.Start()`/`Length()` at
`/Users/pmoore/git/samfile/samfile.go:89-95`.)

### Worked example: real-world SAM BASIC SAVE

From `/Users/pmoore/Downloads/GoodSamC2/x.mgt`, file `CHOMPER`:

```
body[0..8] = 10 df 0f d5 9c ff ff 01 00
             ^  ^^^^^ ^^^^^ ^^^^^ ^^ ^^
             |  |     |     |     |  StartPage=0
             |  |     |     |     Pages=1
             |  |     |     unused, written FF FF
             |  |     PageOffset=0x9CD5 (=0x8000 | 0x1CD5)
             |  LengthMod16K=0x0FDF (=4063)
             Type=0x10 (BASIC)
True length = 1*16384 + 0x0FDF       = 0x4FDF = 20447
True start  = 0*16384 + (0x9CD5 & 0x3FFF) = 0x1CD5 → +ROM-skip = 0x5CD5 = 23765
```

23765 = 0x5CD5 is the address of `PROG` (start of BASIC programs in
section A). Note that **real BASIC SAVE leaves bytes 5–6 as `FF FF`**,
not zero.

---

## 2. The 256-byte directory entry

Tracks 0–3 of the disk hold the directory: 4 tracks * 10 sectors * 2
entries/sector = **80 directory entries max**. Each entry is exactly
256 bytes. Layout per Tech Man L4338–4400 (`docs/sam/sam-coupe_tech-man_v3-0.txt:4338-4400`)
and samfile `FileEntryFrom`/`Raw()` (`/Users/pmoore/git/samfile/samfile.go:240-310`):

| Offset (hex / dec) | Size | Field                        | Notes                                                                             |
|--------------------|------|------------------------------|-----------------------------------------------------------------------------------|
| 0x00 / 0          | 1    | `Type` (status / file type)  | 0=erased; bit7=hidden; bit6=protected; low5 = file type 5/16..20.                  |
| 0x01–0x0A / 1–10  | 10   | `Filename`                    | 10 bytes, space-padded; matched case-insensitively.                                |
| 0x0B–0x0C / 11–12 | 2    | `Sectors`                     | **Big-endian** 16-bit count. (samfile.go L243: high<<8 \| low.)                    |
| 0x0D / 13         | 1    | `FirstSector.Track`           | 0–79 (side 0) or 128–207 (side 1).                                                |
| 0x0E / 14         | 1    | `FirstSector.Sector`          | 1–10.                                                                              |
| 0x0F–0xD1 / 15–209| 195  | `SectorAddressMap`            | 1560 bits, one per data sector. Bit 0 of byte 0 = T4S1.                            |
| 0xD2–0xDB / 210–219| 10  | `MGTFutureAndPast`            | Tech Man says unused; **SAMDOS uses bytes 211–219 (9 bytes) as a verbatim mirror of the on-disk body header (`f.s:462-471`, `c.s:1376-1379`)**. Byte 210 (=0xD2) is unused. |
| 0xDC / 220        | 1    | `MGTFlags`                    | "MGT use only".                                                                    |
| 0xDD–0xE7 / 221–231| 11  | `FileTypeInfo` (type-dependent)| See per-type tables below.                                                         |
| 0xE8–0xEB / 232–235| 4   | `ReservedA`                   | Spare 4 bytes.                                                                     |
| 0xEC / 236        | 1    | `StartAddressPage`            | Bits 0–4 = page number; mirror of body header byte 8.                              |
| 0xED–0xEE / 237–238| 2   | `StartAddressPageOffset`      | LE; mirror of body header bytes 3–4. By convention bit 15 set (8000H-form).        |
| 0xEF / 239        | 1    | `Pages`                       | Mirror of body header byte 7.                                                      |
| 0xF0–0xF1 / 240–241| 2   | `LengthMod16K`                | LE; mirror of body header bytes 1–2.                                               |
| 0xF2 / 242        | 1    | `ExecutionAddressDiv16K` / BASIC autorun marker | CODE: page (bits 0–4); 0xFF if no auto-exec. BASIC: 0 if auto-RUN line follows; 0xFF if no auto-RUN. |
| 0xF3–0xF4 / 243–244| 2   | `ExecutionAddressMod16K` / `SAMBASICStartLine` | CODE: LE offset (8000H-form). BASIC: LE auto-RUN line number.                       |
| 0xF5–0xFF / 245–255| 11  | `ReservedB`                   | Tech Man: "spare 8 bytes (245–253)" + "MGT future use (254–255)". samfile lumps both into 11-byte ReservedB. |

### Per-type `FileTypeInfo` (dir bytes 0xDD–0xE7, UIFA bytes 16–26)

Sources: ROM disasm `E019` block (`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22030-22052`); Tech Man L3022–3067; samfile.go L395–408.

#### Type 16 (SAM BASIC) — three 3-byte page-form values

| Dir offset | UIFA offset | Bytes | Meaning |
|------------|-------------|-------|---------|
| 0xDD–0xDF  | 16–18       | 3     | Program length excluding variables, page-form: `[page, offset_lo, offset_hi]`. ROM populates from `(NUMEND - PROG)` if `NUMEND > PROG` else 0 (?). |
| 0xE0–0xE2  | 19–21       | 3     | Program length plus numeric variables (excluding gap and string/array vars), page-form. |
| 0xE3–0xE5  | 22–24       | 3     | Program length plus numeric variables and gap-before-strings, page-form. |
| 0xE6–0xE7  | (25–26)     | 2     | Spare per ROM `E019` block; samfile reads the byte at 0xDD–0xE7 as 11-byte `FileTypeInfo`. |

The 3-byte page-form encoding is the same as the ROM's HDN+0..+2: byte
0 is the high "page" byte (16K-page index, bits 0–4), bytes 1–2 are an
LE 16-bit offset within the page (8000H-form). To recover the actual
24-bit value: `addr = (byte0 & 0x1F) * 16384 + (byte1 | byte2 << 8) & 0x3FFF`.

samfile's `ProgramLength()` etc. (`/Users/pmoore/git/samfile/samfile.go:395-408`)
intentionally returns the raw 24-bit composite without page-form
decoding (`(byte0<<16) | byte1 | (byte2<<8)`), so its display value is
not the actual address — but a non-zero value tells you the bytes are
populated.

ROM SAVE writes these in `tapemn.sam` block at `E0B4`–`E0E0`
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22163-22180`):

```
E0B4 0603         LD B,3              ;DO NVARS, NUMEND, SAVARS
E0B6 DD21895A     LD IX,NVARS+1
E0BA FD21104B     LD IY,HDR+16        ;STORE IN HDR+16/+19/+22
E0BE …
E0CD CDE71F       CALL SUBAHLCDE      ;AHL = NVARS-PROG (page-form)
E0D0 FD7700       LD (IY+0),A
E0D3 FD23         INC IY
E0D5 FD7500       LD (IY+0),L
E0D8 FD23         INC IY
E0DA FD7400       LD (IY+0),H
…                                     ;next iteration NUMEND-PROG, then SAVARS-PROG
```

So at SAVE time bytes HDR+16/+19/+22 (= dir 0xDD/0xE0/0xE3) get the
page-form values of `NVARS-PROG` (program length excluding vars),
`NUMEND-PROG` (prog + numeric vars), `SAVARS-PROG` (prog + numerics +
gap-before-strings).

#### Type 17 / 18 (numeric / string array)

UIFA 16–26 holds the type/length byte and the array name. (Tech Man
L3022–3023, ROM `E019` L22036.) samfile reports as `FileTypeInfo` raw
bytes (`/Users/pmoore/git/samfile/samfile.go:372-374`).

#### Type 19 (CODE)

`FileTypeInfo` bytes 0xDD–0xE7 are unused / zero. (samfile's
`AddCodeFile` at `/Users/pmoore/git/samfile/samfile.go:483-499` does
not set them.) The exec address is in bytes 0xF2–0xF4 instead.

#### Type 20 (SCREEN$)

UIFA byte 16 / dir byte 0xDD = screen MODE. Remaining bytes 17–26
unused. (Tech Man L3024, ROM `E019` L22037.) ROM SAVE sets it at
`E146`: `LD (HDR+16),A` (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22259`).

#### Type 5 (ZX SNP)

Not used by Pete's project; SAMDOS-specific extension; out of scope.

---

## 3. The 80-byte in-memory header buffer (HDR / HDL)

Two buffers in BASIC's workspace:
- **HDR** = `&4B00` — Header Requested. Built up by SAVE/LOAD before
  the operation, also re-used as the SAMDOS UIFA pointer. Length 80 bytes.
- **HDL** = `&4B50` — Header Loaded. After LOAD/VERIFY, contains the
  on-disk header for comparison/setup.

EQUs at `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:1215-1237`:
```
HFG    EQU 15      ; DISP TO HEADER FLAG
HDN    EQU 31      ; DISP TO HEADER NUMBERS
HDRL   EQU 80      ; HDR BUFFER LEN
HDR    EQU 4B00H
HDL    EQU 4B50H
```

Authoritative buffer layout from the ROM `E019` block
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22030-22054`):

| Offset | Size | Field            | Meaning                                                                                   |
|--------|------|------------------|-------------------------------------------------------------------------------------------|
| 0      | 1    | Type             | 5 / 16 / 17 / 18 / 19 / 20.                                                               |
| 1–10   | 10   | Filename         | Space-padded.                                                                              |
| 11–14  | 4    | Long-name extra  | "Allows longer file name if SLDEV<>T" (i.e. `D2:filename` etc.). For disk: padding.        |
| 15     | 1    | Flags (HFG)      | bit 0 = invisible-name; bit 1 = protected code.                                            |
| 16–26  | 11   | Type-specific    | 17/18: array TLBYTE/NAME. 20: byte 16=screen MODE. 16: bytes 16–18 / 19–21 / 22–24 hold the three 3-byte page-form prog-length triplets. |
| 27     | 1    | DIRE             | "Directory entry number, HDR only, NOT USED."                                              |
| 28–30  | 3    | Spare            | Tech Man L3036 ("27–30 reserved (5 bytes)" — note 5-byte count is incorrect in Tech Man; ROM `E019` says 28–30 = 3 spare bytes after the 1-byte DIRE at 27, totalling 4). |
| **31–33** | 3 | Start (HDN+0)    | CODE: REL PAGE FORM start address. BAS/DATA: actual addr. (Or `FF XX XX` for "no array".)  |
| **34–36** | 3 | Length (HDN+3)   | Data length in PAGE FORM, or `FF FF FF` for `LOAD "" CODE`. For BAS=PROG+VARS length.       |
| **37–39** | 3 | Exec (HDN+6)     | CODE: REL PAGE FORM exec addr, or `FF XX XX` for no exec. BAS: byte 37 = `0` if auto-RUN line follows in 38–39, `FF` if no auto-RUN. |
| 40–79  | 40   | Comment          | Not initialised. Saved/loaded but DOS only saves first 8 bytes here.                        |

(REL PAGE FORM is the same encoding as the directory entry's
`Start_Page / PageOffset` triple at 0xEC–0xEE, i.e. 1-byte page in bits
0–4 + LE 16-bit offset with bit 15 set. See
`docs/notes/sam-paging.md` if/when that doc lands; otherwise refer to
Tech Man L3037–3052 and the ROM `E019` description above.)

### How the 9 disk-body bytes populate HDL (loaded-header buffer)

The on-disk **9-byte body header** does NOT directly correspond to the
**80-byte in-memory HDR/HDL buffer** byte-for-byte. The mapping is
done by SAMDOS when populating the in-memory buffer from the directory
entry + body bytes. Roughly:

| In-memory HDL offset | Source                                 |
|----------------------|----------------------------------------|
| 0 (Type)             | body byte 0 (= dir byte 0)             |
| 1–10 (Name)          | dir bytes 1–10                         |
| 11–14 (Long-name)    | usually space- or FF-filled            |
| 15 (Flags)           | dir byte 0xDC (MGT flags)              |
| 16–26 (TypeInfo)     | dir bytes 0xDD–0xE7                    |
| 27–30 (DIRE+spare)   | dir bytes 0xE8–0xEB (`ReservedA`)      |
| 31–33 (HDN start)    | dir byte 0xEC + dir bytes 0xED–0xEE; equivalent to body bytes 8 + 3–4. |
| 34–36 (HDN length)   | dir byte 0xEF + dir bytes 0xF0–0xF1; equivalent to body bytes 7 + 1–2. |
| 37–39 (HDN exec)     | dir bytes 0xF2 + 0xF3–0xF4              |
| 40–79 (Comment)      | dir bytes 0xF5+ if used                 |

The crucial implication for build-disk.sh: **the body 9-byte header
does NOT carry the auto-RUN line, the screen mode, or the BASIC
program-length triplets** — those live ONLY in the directory entry.
But conversely, if the directory entry's 0xDD–0xE7 / 0xEC–0xF4 fields
are zero or wrong, the in-memory HDL is wrong even if the body header
is right.

### SAMDOS-side cache

SAMDOS additionally keeps an *internal cache* of the 9 body-header
bytes at directory-entry offsets 211–219 (= 0xD3–0xDB). Written by
`svhd` (`/Users/pmoore/git/samdos/src/f.s:462-471`):

```asm
svhd:  ld hl,hd001        ; SAMDOS in-RAM 9-byte header copy
       ld de,fsa+211      ; offset 211 of dir-entry buffer
       ld b,9
svhd1: ld a,(hl)
       ld (de),a
       call sbyt          ; ALSO write to file body via sector chain
       inc hl
       inc de
       djnz svhd1
       ret
```

And read back by `gtfle` (`/Users/pmoore/git/samdos/src/c.s:1376-1379`):

```asm
ld (ix+rptl),211           ; offset 211 inside the dir-entry buffer
call grpnt
ld bc,9
ldir                       ; → into hd001 / hd002 (SAMDOS RAM)
```

So SAMDOS caches the 9-byte body header in dir bytes 211–219 to avoid
re-reading the file body sector when listing files / verifying.
**Tech Manual p.81 calls this region "MGT future and past, not used by
SAMDOS" — but that is a documentation bug; SAMDOS does use it.** Keep
it in sync if you write directory entries by hand. In practice
build-disk.sh leaves dir bytes 211–219 zero; that is fine because
SAMDOS only consumes them when populating its DIFA-form metadata, and
will fall back to the real body header on a fresh disk read.

---

## 4. Validation of `tools/build-disk.sh` headers

Reference: `tools/build-disk.sh` (lines 60–248), commit at the time of
writing. All four files are constructed by the same Python block.

### 4.1 `samdos2_header` (slot 0, type 19, line 152–159)

```python
samdos2_header = bytes([
    0x13,                                 # type 19 (Code)
    10000 & 0xff, (10000 >> 8) & 0xff,    # LengthMod16K LE = 10000
    0x00, 0x80,                           # PageOffset = &8000
    0x00, 0x00,                           # reserved
    0x00,                                 # Pages = 0 (10K < 16K)
    0x01,                                 # StartPage = 1
])
```

| Byte | Value | Field         | Verdict |
|------|-------|---------------|---------|
| 0    | 0x13  | Type          | OK — 19 = CODE; sidesteps SAMDOS's type-3 / `samfile`'s "erased" handling per the script comment at L141–148. |
| 1–2  | 10 27 | LengthMod16K  | OK — 0x2710 = 10000, matches `samdos2.bin` body length. |
| 3–4  | 00 80 | PageOffset    | OK — 0x8000, the SAMDOS load address. |
| 5–6  | 00 00 | unused        | OK; cosmetically a real SAVE writes `FF FF` (Tech Man L4293 "unused"). Either is spec-compliant. |
| 7    | 00    | Pages         | OK — 10000 < 16384 so 0 pages. |
| 8    | 01    | StartPage     | OK — page 1 + offset 0x8000 → start address `(StartPage & 0x1F)*16384 + (PageOffset & 0x3FFF)*16384-skip = 1*16384 + 0x8000 - 16384 = 0x8000` per Tech Man L4326–4329. samfile's `Start()` (samfile.go:89-91) computes `(0x8000 & 0x3FFF) \| ((1 & 0x1F + 1)<<14) = 0 \| 0x8000 = 0x8000`. Both interpretations agree. ✓ |

**Verdict for samdos2:** Header is well-formed and would resolve to
load-address 0x8000 if anything ever consumed it. In practice ROM
BOOT bypasses the header entirely (`D8E5`: read raw to 0x8000, JP
0x8009 — see `docs/notes/sam-disk-format.md` §1), so the bytes are
cosmetic. **No fix needed.**

Cited values: `tools/build-disk.sh:138-165`; ROM BOOT at
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` D8CD–D97D.

### 4.2 `auto_header` (slot 1, type 16, line 202–209)

```python
auto_header = bytes([0x10, len(BASIC_BODY) & 0xff, (len(BASIC_BODY) >> 8) & 0xff,
                    0, 0, 0, 0, 0, 0])
```

For `len(BASIC_BODY)` = ~71 bytes (the tokenised LOAD CODE / CALL
line), so concretely this writes `10 47 00 00 00 00 00 00 00`.

Compare against real SAVE (CHOMPER from x.mgt):
```
real:    10 df 0f d5 9c ff ff 01 00
ours:    10 47 00 00 00 00 00 00 00
         ^  ^^^^^ ^^^^^ ^^^^^ ^^ ^^
         |  |     |     |     |  StartPage — should be 0 (✓ for us)
         |  |     |     |     Pages — should be 0 since 0x47 < 16384 (✓)
         |  |     |     unused — real SAVE writes FF FF; we write 00 00 (cosmetic)
         |  |     PageOffset — REAL writes 0x9CD5; we write 0x0000
         |  LengthMod16K — body length, OK
         Type 16 — OK
```

| Byte | Value | Field         | Verdict |
|------|-------|---------------|---------|
| 0    | 0x10  | Type          | OK — 16 = SAM BASIC. |
| 1–2  | 47 00 | LengthMod16K  | OK — matches `len(BASIC_BODY)`. |
| 3–4  | 00 00 | PageOffset    | **WRONG** — should be `D5 9C` (= 0x9CD5, encoding load addr 0x5CD5 = PROG, in 8000H-form). With 0x0000 the in-memory HDL+32/+33 (start address LSB/MSB) is computed by ROM as 0x4000 (per Tech Man formula) or 0x8000 (per samfile). Neither is the intended PROG=0x5CD5. **Fix:** `0xD5, 0x9C`. |
| 5–6  | 00 00 | unused        | Cosmetic; real SAVE writes `FF FF`. Leave or fix to taste. |
| 7    | 00    | Pages         | OK — body fits in one page. |
| 8    | 00    | StartPage     | OK — 0 (PROG is in section A, low end). |

**Concrete fix:**
```python
auto_header = bytes([0x10, len(BASIC_BODY) & 0xff, (len(BASIC_BODY) >> 8) & 0xff,
                    0xD5, 0x9C, 0xFF, 0xFF, 0, 0])
```

Sources: real SAVE comparison from `/Users/pmoore/Downloads/GoodSamC2/x.mgt`
slot for `CHOMPER` (Type 16, body bytes verified by direct inspection).
Also `/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_tech-man_v3-0.txt:4286-4329`.

### 4.3 `auto` directory entry (line 204–208)

The `write_directory_entry` call passes only `start_line=10` and uses
defaults for everything else. Resulting bytes 0xDD–0xF4:

```
real CHOMPER:  00 05 92 00 30 93 00 61 94 20 ff   00  d5 9c  01  df 0f  00 01 00
                ^^^^^^^^ ^^^^^^^^ ^^^^^^^^ ^^ ^^   ^^  ^^^^^  ^^  ^^^^^  ^^ ^^^^^
                prog-len prog+nv  prog+nv+gp  ?    sP  PgOff   Pg  L16K  exec/line

ours auto:     00 00 00 00 00 00 00 00 00 00 00   00  00 00  00  47 00  00 0a 00
                all zero                            zero PgOff  zero L16K  start-line=10
```

| Dir bytes | Field                       | Real (CHOMPER) | Ours (auto) | Verdict |
|-----------|-----------------------------|----------------|-------------|---------|
| 0xDD–0xDF | BASIC prog-length triplet   | `00 05 92`     | `00 00 00`  | **WRONG / Missing.** ROM writes `(NVARS-PROG)` page-form on SAVE (ROM L22163-22180). With zeros, `samfile ls` reports `Program length: 0`. **Fix:** for our 0x47-byte tokenised body whose program ends at PROG + 0x47 = 0x5D1C, we want `(0x5D1C - 0x5CD5) = 0x47` = 71. Page-form encoding of just an offset of 71 = `[page=0, offset_lo=0x47, offset_hi=0x00]` plus 8000H-form bit on offset_hi → bytes `00 47 80`. (Match real semantics: offset has bit 15 set per the convention.) |
| 0xE0–0xE2 | prog+NVARS triplet          | `00 30 93`     | `00 00 00`  | Same as above; for an AUTO file with no variables, NVARS == NUMEND, so this triplet should equal the prog-length triplet. **Fix:** same bytes as 0xDD–0xDF. |
| 0xE3–0xE5 | prog+NVARS+gap triplet      | `00 61 94`     | `00 00 00`  | Same again; SAVARS == NVARS for our AUTO. **Fix:** same as above. |
| 0xE6–0xE7 | spare                        | `20 FF`        | `00 00`     | Cosmetic. |
| 0xE8–0xEB | ReservedA                    | `FF FF FF FF` (typical) | `00 00 00 00` | Cosmetic. |
| 0xEC      | StartAddressPage             | `00`           | `00`        | OK. |
| 0xED–0xEE | StartAddressPageOffset       | `D5 9C`        | `00 00`     | **WRONG.** Should mirror body header bytes 3–4. **Fix:** `0xD5, 0x9C`. (build-disk.sh's `write_directory_entry` doesn't accept this — it takes `exec_addr_div_16k` but no `start_addr_*` parameter, so the dir entry's start fields are always zero. This is a missing feature.) |
| 0xEF      | Pages                        | `01`           | `00`        | OK only because body length < 16384. |
| 0xF0–0xF1 | LengthMod16K                 | `DF 0F`        | `47 00`     | OK — set by `length=len(BASIC_BODY)` in `write_directory_entry`. |
| 0xF2      | autorun marker               | `00`           | `00`        | OK — `start_line=10` triggers `e[0xf2] = 0` in `write_directory_entry` (build-disk.sh:106-108). |
| 0xF3–0xF4 | auto-RUN line                | `01 00`        | `0a 00`     | OK — line 10. |

**The "Program length: 0" mystery is fully explained:** build-disk.sh's
`write_directory_entry` does not write bytes 0xDD–0xE5 at all, leaving
them zero. samfile reads bytes 0xDD–0xDF as the program-length triplet
and faithfully reports `0`.

**Whether it matters at run time:** ROM LOAD of a BASIC file uses HDL
+16/+17/+18 etc. for setting NVARS, NUMEND, SAVARS sysvars after load
(Tech Man L3033 "the extra data for Basic programs allows NVARS,
NUMEND and SAVARS to be set up on LOADing"). If BASIC is loading our
`auto` BASIC into PROG and then auto-RUNning line 10, and HDL+16..+24
are all zero, the post-LOAD sysvar values become page-form-decode of
all-zero = address 0x4000 (or 0x8000 depending on interpretation).
Whether subsequent BASIC operations (CLEAR, variable allocation, etc.)
crash on those bogus sysvars is the open question Pete is investigating
separately. **What this doc establishes:** the bytes ARE wrong; the
ROM WILL consume them. Whether the resulting state crashes CLEAR is a
follow-up question.

### 4.4 `stub_header` (slot 2, type 19, line 225–227)

```python
stub_header = bytes([0x13, len(stub_body) & 0xff, (len(stub_body) >> 8) & 0xff,
                     LOAD_ADDR & 0xff, (LOAD_ADDR >> 8) & 0xff,
                     0, 0, 0, (LOAD_ADDR >> 14) - 1])
```

With `LOAD_ADDR = 24576 = 0x6000`:

| Byte | Computed | Field         | Verdict |
|------|----------|---------------|---------|
| 0    | 0x13     | Type          | OK — CODE. |
| 1–2  | LE len   | LengthMod16K  | OK. |
| 3–4  | 00 60    | PageOffset    | **WRONG by Tech-Man convention.** Tech Man L3043 says PageOffset must lie in `0x8000–0xBFFF` (bit 15 set). 0x6000 has bit 15 clear. Real CODE SAVE for `LOAD "stub" CODE 0x6000` would store `PageOffset = 0x6000 + 0x8000 = 0xE000`? No — wait. Actually the convention is that the offset is taken mod 0x4000 and bit 15 is set as a marker. For load address 0x6000 = section B at 24576, page=0 (section A page when paged in at 0x4000), offset within page = 0x2000. Bit 15 set → `0xA000`. **Fix:** `LOAD_ADDR & 0x3FFF \| 0x8000`. For 0x6000: `0x2000 \| 0x8000 = 0xA000`, bytes `0x00, 0xA0`. |
| 5–6  | 00 00    | unused        | Cosmetic; real SAVE writes `FF FF`. |
| 7    | 00       | Pages         | OK if `len(stub_body) < 16384`. |
| 8    | 0x00     | StartPage     | `(0x6000 >> 14) - 1 = 1 - 1 = 0`. OK — page 0 means actual address `(0+1)*16384 + 0x2000 = 0x6000`. ✓ |

**Verdict for stub:** PageOffset bytes 3–4 are non-canonical. samfile
parses our value `0x6000` as `(0x6000 & 0x3FFF) | (0+1)<<14 = 0x2000 |
0x4000 = 0x6000` — which happens to round-trip to the right address
because 0x6000 satisfies `0x6000 & 0x3FFF == 0x2000`. So the value
"works" in samfile's interpretation but doesn't match what a real ROM
SAVE writes. **Recommended fix:** OR with 0x8000 to set the
spec-compliant bit (Tech Man L3043). For 0x6000: bytes `0x00, 0xA0`.

Real-world evidence (CODE file `SAMDOS` from `/Users/pmoore/Downloads/GoodSamC2/x.mgt`):
body bytes 0..8 = `13 10 27 09 80 ff ff 00 6d` — note PageOffset =
`09 80` (= 0x8009) **with bit 15 set**. Also note `unused` bytes are
`FF FF`, not `00 00`. The ROM's `PDPSR2` routine
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:4499-4527`)
expects exec/start addresses already in 8000H-BFFFH form when called
direct (only the SAVE-side `PDPSUBR` at L4496 explicitly forces bit 15
via `SET 7,H`). A LOAD-CODE-with-exec path that consumed our bare
`0x6000` PageOffset would mis-resolve via PDPSR2.

| Dir entry bytes 0xEC–0xF4 | Computed | Verdict |
|--------------------------|----------|---------|
| 0xEC StartPage           | 0 (default in `write_directory_entry`) | **MISSING.** `write_directory_entry` doesn't set this. With value 0, samfile computes `Start = (0+1)<<14 + (0)&0x3FFF = 0x4000`, not 0x6000. **Fix:** `e[0xec] = (LOAD_ADDR >> 14) - 1`. |
| 0xED–0xEE PageOffset     | 0 (default) | **MISSING.** Should be `LOAD_ADDR & 0x3FFF \| 0x8000` LE. **Fix:** `e[0xed] = LOAD_ADDR & 0xff; e[0xee] = ((LOAD_ADDR >> 8) & 0x3F) \| 0x80`. |
| 0xEF Pages                | 0 (set by `length` arg via `(length>>14)`?) | Actually `write_directory_entry` does NOT compute Pages from length — it only sets bytes 0xF0–0xF1 (LengthMod16K). Pages defaults to 0. OK if length < 16K. |
| 0xF0–0xF1 LengthMod16K    | LE length | OK. |
| 0xF2 ExecutionAddrDiv16K  | 0xFF (no auto-exec, set explicitly L231) | OK. |
| 0xF3–0xF4 ExecutionAddrMod16K | 0xFFFF | OK. |

So the dir entry for `stub` is missing the start address mirror in
0xEC–0xEE. samfile would report `Start: 16384` (= 0x4000) instead of
24576 (= 0x6000). On real LOAD, the ROM uses HDL+31/+32–33 (start
page + offset) when LOAD CODE is invoked without an explicit address —
so ours says load to 0x4000. **However**, our BASIC AUTO does
`LOAD "stub" CODE 24576`, providing an explicit start address, which
overrides the HDL value via HDR (see ROM `E281`–`E294`). So in
practice the missing fields don't affect us — but they're still wrong.

### 4.5 `in_header` (slot 3, type 19, line 240–242)

Identical structure to `stub_header`. Same verdict applies: PageOffset
should have bit 15 set; dir-entry start fields are missing. Since `IN`
is a data file consumed by stub via `LOAD "IN" CODE 24576` (also with
explicit addr), the missing fields don't break runtime — but they're
still off.

---

## 5. Why `samfile ls` reports "Program length: 0"

samfile's `Output()` for a Type 16 file prints (samfile.go:378):

```go
fmt.Printf("  Program length:                    %v\n", fe.ProgramLength())
```

where (samfile.go:395-397):

```go
func (fe *FileEntry) ProgramLength() uint32 {
    return uint32(fe.FileTypeInfo[0])<<16 | uint32(fe.FileTypeInfo[1]) | uint32(fe.FileTypeInfo[2])<<8
}
```

`FileTypeInfo[0..2]` is directory bytes 0xDD–0xDF. build-disk.sh's
`write_directory_entry` (lines 80–113) writes the dir entry's
file-type-info area as part of the 256-byte buffer initialised to
zero, but never populates bytes 0xDD–0xE7. Result: zeros in, zero out.

**Where would real BASIC SAVE write these bytes?** ROM SAVE handler
for a BASIC program at `E0B4`–`E0E0`
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22163-22180`)
loops three times, writing page-form `(NVARS-PROG)`, `(NUMEND-PROG)`,
`(SAVARS-PROG)` triplets to HDR+16, HDR+19, HDR+22 — which then become
directory bytes 0xDD–0xE5 when SAMDOS commits the directory entry to
disk.

For our hand-rolled `auto` BASIC, NVARS = NUMEND = SAVARS = end of
tokenised stream (no vars), so all three triplets should encode the
same value: the byte length of `BASIC_BODY` (or, more precisely, an
end-of-program offset relative to PROG). Tech Man L3025–3032
documents these three values; ROM `E019` block lines 22038–22040
documents the on-buffer offsets.

If we want `samfile ls` to report a non-zero length AND have ROM LOAD
populate NVARS/NUMEND/SAVARS to non-bogus values, build-disk.sh would
need to write those triplets in the directory entry. The page-form
encoding of `(NVARS - PROG)` for our small body is just the byte
length, since PROG = 0x5CD5 is in section A page 0 and the body fits
within section A. So the three triplets become approximately:

```python
end_offset = len(BASIC_BODY)              # NVARS - PROG (for a no-vars program)
e[0xdd] = 0x00                             # page byte
e[0xde] = end_offset & 0xff                # offset LSB
e[0xdf] = ((end_offset >> 8) & 0x3F) | 0x80  # offset MSB with 8000H bit
e[0xe0:0xe3] = e[0xdd:0xe0]                # NUMEND-PROG (same)
e[0xe3:0xe6] = e[0xdd:0xe0]                # SAVARS-PROG (same)
```

Caveat: I have not run this through ROM LOAD to confirm the encoding
recovers correctly — Tech Man, ROM disasm, and samfile.go all agree on
the byte order, but the page-form interpretation has a detail I want
to flag for the paging agent (if `(NVARS-PROG) < 0x4000` should the
high bit on byte 2 still be set?). Cross-reference Tech Man L3037–3052
when finalising. **Pete decides; this doc just identifies where the
zeros come from and what real SAVE writes there.**

---

## 6. Summary of bugs in `tools/build-disk.sh`

| # | File   | Location                  | Issue                                                                                                              | Recommended fix                                                                                                         |
|---|--------|---------------------------|--------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------|
| 1 | auto   | header bytes 3–4          | PageOffset = 0; should encode PROG=0x5CD5 in 8000H-form.                                                           | `auto_header[3] = 0xD5; auto_header[4] = 0x9C`.                                                                          |
| 2 | auto   | dir entry 0xDD–0xE5       | BASIC prog-length triplets all zero; cause of `samfile ls` "Program length: 0" and a potentially-bad HDL+16..+24.   | Write three identical 3-byte page-form values for `len(BASIC_BODY)` (see §5).                                            |
| 3 | auto   | dir entry 0xEC–0xEE       | Start-address mirror not populated.                                                                                | Add `start_address` arg to `write_directory_entry` and write `e[0xec]=0; e[0xed]=0xD5; e[0xee]=0x9C`.                    |
| 4 | stub   | header bytes 3–4          | PageOffset = 0x6000; Tech-Man convention requires bit 15 set (8000H-form encoding).                                 | `stub_header[3] = LOAD_ADDR & 0xff; stub_header[4] = ((LOAD_ADDR >> 8) & 0x3F) \| 0x80`.                                  |
| 5 | stub   | dir entry 0xEC–0xEE       | Start-address mirror not populated.                                                                                | Pass through `start_address=LOAD_ADDR` and write the page-form triplet.                                                  |
| 6 | IN     | same as stub              | Same issues as stub.                                                                                               | Same fix.                                                                                                                |
| 7 | All    | header bytes 5–6          | Real SAVE writes `FF FF`; we write `00 00`. Cosmetic only; both are spec-compliant per Tech Man L4293 ("unused").    | Change to `FF FF` if you want bit-identical to real SAVE; otherwise leave.                                               |
| 8 | samdos2 | (none)                    | Header is consistent under both Tech-Man and samfile decoders → load addr 0x8000. Cosmetic only.                     | No fix needed; ROM BOOT bypasses the header anyway.                                                                      |

None of bugs #1–#6 affect the `samdos2` boot signature (T4S1 raw-load
path) — that's purely about file body offsets 247–250.

---

## 7. Source citations index

- **Tech Manual v3.0** — `/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_tech-man_v3-0.txt`:
  - L2974–3068: ROM HDR/HDL buffer format (the canonical 80-byte layout).
  - L3037–3052: REL PAGE FORM and the 8000H-form offset convention.
  - L4284–4332: 9-byte on-disk file header layout, length/start arithmetic.
  - L4338–4400: 256-byte SAMDOS directory entry layout.
  - L4403–4427: Sector address map / BAM.
  - L4459–4502: UIFA / DIFA layouts (essentially the same fields as the directory entry's metadata block).
  - L4517–4555: SAMDOS hook-code summary including HGTHD.

- **ROM v3.0 annotated disassembly** — `/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`:
  - L1185, L1215–1237: `HFG`, `HDN`, `HDRL`, `HDR`, `HDL` EQUs.
  - L22025–22054: `E019` block — the authoritative HDR/HDL buffer documentation.
  - L22057–22119: `SLMVC` / `HDR2` — SAVE/LOAD entry, header buffer initialisation (clears area to 0x20 / 0xFF).
  - L22136–22141: BASIC autorun-line setup at `HDR+HDN+6` (= HDR+37): `(HL)=0` + line in next two bytes.
  - L22163–22180: SAVE-time computation of three BASIC prog-length triplets at HDR+16/+19/+22.
  - L22247: `LD A,19` on the LOAD/SAVE CODE path (type=CODE).
  - L22259: `LD (HDR+16),A ;MODE` — SCREEN$ mode setup.
  - L22467–22484: LOAD CODE exec-address logic (`HDLDEX`, `R1OFFCLBC`).

- **SAMDOS source** — `/Users/pmoore/git/samdos/src/`:
  - `b.s:12-22`: optional `if defined (include-header)` 9-byte body header for samdos2 itself.
  - `b.s:255-260`: `hd001`–`page1` — SAMDOS in-RAM cache of a file's 9-byte header.
  - `b.s:278-290`: example UIFA-form constants for samdos2.
  - `f.s:462-471`: `svhd` — saves 9-byte header to `fsa+211` and writes via `sbyt`.
  - `f.s:494-497`: `ldhd` — loads 9 bytes via `lbyt`.
  - `c.s:1359-1487`: `gtflx` / `gtfle` — directory walker; reads dir bytes 211–219 (9-byte header) and 220–252 (file metadata).
  - `c.s:557-570`: `lbyt` — read byte from current sector chain position.
  - `c.s:533-551`: `sbyt` — write byte to current sector chain position.
  - `h.s:8-26`: `rxhed` — copies 48 bytes of UIFA from caller's IX.
  - `h.s:59-67`: `hgthd` — HGTHD (hook 129) implementation.
  - `h.s:74-90`: `dschd` — populates SAMDOS RAM from on-disk header.
  - `h.s:336-361`: `hconr` — converts UIFA into legacy `nstr1`/`hd001`/`page1`/`pges1` form.

- **samfile (Go)** — `/Users/pmoore/git/samfile/samfile.go`:
  - L21–43: `FileEntry` struct — directory entry layout.
  - L51–58: `FileHeader` struct — 9-byte body header layout.
  - L72–80: `FT_*` file type constants (5/16/17/18/19/20).
  - L89–95: `Start()` and `Length()` decoders.
  - L240–266: `FileEntryFrom([0x100]byte)` — parses raw directory entry.
  - L268–310: `Raw()` — emits raw directory entry.
  - L347–355: `Used()`.
  - L357–393: `Output()` — `samfile ls -i` per-entry output, including BASIC `ProgramLength()` line.
  - L395–408: BASIC-specific accessors.
  - L411–413: `StartAddress()` — page-form decoder for dir bytes 0xEC–0xEE.
  - L444–452: 9-byte body header parser inside `File()`.
  - L470–499: `AddCodeFile` — code file emitter (carries the build-disk.sh-relevant CODE conventions).
  - L501–509: `CreateHeader` — builds the 9-byte body header from a directory entry.
  - L511–552: `addFile` — top-level write path.
  - L554–559: `WriteFileEntry` — flush directory entry to image.
  - L562–565: `SAMMask` — known-buggy operator-precedence bug (`1 << bitOffset & 0x07`); see build-disk.sh comment.
  - L567–569: `Sector.Offset` — track/sector → byte offset.
  - L578–595: `FileHeader.Raw` and `File.Raw`.

- **samfile (BASIC tokenisation)** — `/Users/pmoore/git/samfile/sambasic.go`:
  - L26: line numbers are stored **big-endian** in tokenised body.
  - L27: line lengths are stored **little-endian**.
  - L40, L53: keyword token range.

- **Existing project notes** — `/Users/pmoore/git/sam-aarch64/docs/notes/`:
  - `sam-disk-format.md` — covers MGT geometry, sector map, BAM, type byte assignment.
  - `sam-file-io.md` — UIFA struct (matches directory entry's metadata block) and SAMDOS hook-code calling convention.
  - `samdos2-auto-run-analysis.md` — orthogonal: why SAMDOS's hook-128 doesn't auto-RUN.

- **Real-world reference disk inspections**:
  - `/Users/pmoore/Downloads/GoodSamC2/x.mgt` — multiple Type 16 SAM BASIC files; CHOMPER body bytes `10 df 0f d5 9c ff ff 01 00`, dir 0xDD–0xDF = `00 05 92`. Concrete evidence for the format of a real SAVE.
  - `/Users/pmoore/Downloads/SupernovaUnfinished.mgt` — type 16 file `auto` with `Start Line: 1`, prog length 32918.
  - `/Users/pmoore/git/sam-aarch64/build/test.mgt` — our current build; auto body = `10 47 00 00 00 00 00 00 00`, dir 0xDD–0xE5 = `00 00 00 00 00 00 00 00 00` (the bug).
