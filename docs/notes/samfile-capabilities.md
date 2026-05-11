# `samfile` capabilities vs. `tools/build-disk.sh` — full audit

Audit conducted 2026-05-10 against samfile upstream `e64f5d5`
(`/Users/pmoore/git/samfile/.git`, `git log --oneline`) and
`tools/build-disk.sh` at commit `457cac1`. Findings informed M0
(now merged via PR #1) and remain accurate for current samfile;
re-verify if either side moves.

This document compares Pete's MGT-image tool `samfile` (`~/git/samfile/`)
against the hand-rolled `tools/build-disk.sh` for the four files our M0
disk image needs:

| Slot | File     | Type | Sectors                     | Source data                       |
|------|----------|------|-----------------------------|-----------------------------------|
| 0    | `samdos2`| 19   | T4S1..T5S10 (20 sectors)    | `reference/samdos/samdos2.bin`    |
| 1    | `auto`   | 16   | T6S1                        | tokenised AUTO BASIC line 10      |
| 2    | `stub`   | 19   | T6S2                        | `build/stub.bin`                  |
| 3    | `IN`     | 19   | T6S3                        | input fixture (`<input.s>`)       |

Cross-references: `docs/notes/sam-disk-format.md` (disk geometry,
directory layout, sector address map, BOOT mechanism),
`docs/notes/sam-file-header.md` (9-byte body header, 256-byte
directory entry, HDR/HDL), `docs/notes/sam-paging.md` (REL PAGE FORM).
Every claim is cited as `file:line`.

---

## TL;DR

1. **Hand-rolling was justified** for the boot file (slot 0) and for
   the auto-RUN BASIC file (slot 1), because samfile's `add`
   sub-command (`/Users/pmoore/git/samfile/cmd/samfile/usage.go:9`,
   `/Users/pmoore/git/samfile/cmd/samfile/add.go:43`) only knows how
   to add **type-19 CODE files** — it has no support for type 16
   (BASIC), no support for setting the BASIC start-line, no
   directory-entry start-address mirroring at 0xEC–0xEE for code
   files, no way to express a "raw boot blob at T4S1" allocation
   constraint, and no way to leave the body header alone (it
   synthesises one). The CLI also has no `-c` flag for non-CODE types
   (`usage.go:9, 37-38`); `samfile.go:483-499`'s `AddCodeFile` is
   currently the only `addFile` caller.

2. **Hand-rolling for slots 2 (stub) and 3 (IN) was NOT strictly
   necessary** — they are plain CODE blobs and `samfile add` could
   produce them, *if* the `:564` SAMMask bug were patched. With that
   patch, `samfile add` would emit byte-for-byte equivalents to our
   hand-roll modulo two cosmetic differences (PageOffset bit-15
   marker, body-header bytes 5–6).

3. **The `:564` bug is real** and trivial to fix (1-line change). It
   has been unfixed since `d5dfe83` ("samfile add command to add
   files to images"; the file dates from 2022-12).

4. **Recommendation: hybrid (option (c))**. Fix the `:564` bug
   upstream, add a tiny new `samfile sbt` (or equivalent) sub-command
   that places a raw boot file with a chosen first-sector and
   sector-chain length, plus a way to emit BASIC type-16 entries with
   start-line and prog-length triplets. With those two PRs, the
   M0 build can replace `tools/build-disk.sh`'s 270-line Python block
   with about 6 `samfile` invocations. Until then, **keep the
   hand-roll** but fix the remaining bugs documented in
   `docs/notes/sam-file-header.md` §6 (the start-address mirror at
   0xEC–0xEE for IN slot is still missing in the current build of
   `build/test.mgt`, evidenced below in §3.4).

---

## 1. What `samfile` supports today

### 1.1 Sub-commands

`samfile` exposes five top-level commands, defined in
`/Users/pmoore/git/samfile/cmd/samfile/main.go:28-41` and
`/Users/pmoore/git/samfile/cmd/samfile/usage.go:6-15`:

| Command          | Purpose                                              | Source                                                         |
|------------------|------------------------------------------------------|----------------------------------------------------------------|
| `add`            | Add a CODE file from host filesystem                 | `cmd/samfile/add.go:12-51`                                     |
| `cat`            | stdout a file's payload                              | `cmd/samfile/cat.go:10-37`                                     |
| `extract`        | Save all files to a host directory                   | `cmd/samfile/extract.go:12-52`                                 |
| `ls`             | Print directory listing                              | `cmd/samfile/ls.go:9-17`, `samfile.go:312-318`                 |
| `basic-to-text`  | Detokenise SAM BASIC bytes from stdin                | `cmd/samfile/sambasic.go:11-16`, `sambasic.go:20-62`           |

There is no `samfile init` (no way to create an 800K blank), no
`samfile rm`, no `samfile rename`, no support for adding non-CODE
file types.

### 1.2 What `samfile add` accepts

CLI synopsis (`/Users/pmoore/git/samfile/cmd/samfile/usage.go:9`):

```
samfile add -i IMAGE -f FILE -c -l LOAD_ADDRESS [-e EXECUTION_ADDRESS]
```

| Flag | Semantics |
|------|-----------|
| `-i IMAGE`            | Existing 819200-byte `.mgt` image. (`add.go:26-30`) |
| `-f FILE`             | Path on host filesystem to a regular file. (`add.go:13-20`) |
| `-c`                  | "File is a code file." (`usage.go:37`) — this flag is **mandatory** in the docopt grammar but is the only file-type flag accepted; there is no BASIC, screen, array, or snapshot mode. The flag is not actually consumed in `add.go` — it serves only as docopt syntax to keep the CLI evolvable. |
| `-l LOAD_ADDRESS`     | Decimal load address. Parsed by `strconv.Atoi` (`add.go:22-25`). Note: only decimal, no `0x` prefix, no `&` prefix. |
| `-e EXECUTION_ADDRESS`| Optional. Decimal exec address (`add.go:32-38`). |

Filename on disk = `filepath.Base(file)` (`add.go:43`). The host
filename's basename becomes the SAM filename — there is no `-n NAME`
option to override. The 10-character SAM filename limit is enforced
inside `addFile` only by `[]byte(name + "          ")` truncation
semantics (`samfile.go:522`); names longer than 10 characters are
silently truncated.

### 1.3 What `AddCodeFile` writes

`samfile.go:470-499`, the only `add`-path entry point:

```go
func (di *DiskImage) AddCodeFile(name string, data []byte, loadAddress, executionAddress uint32) error {
    ...
    fe := &FileEntry{
        Type:                   FT_CODE,
        StartAddressPage:       uint8(loadAddress>>14) - 1,
        StartAddressPageOffset: uint16(loadAddress & 0x3fff),
        ExecutionAddressDiv16K: 0xff,
        ExecutionAddressMod16K: 0xffff,
    }
    if executionAddress > 0 {
        fe.ExecutionAddressDiv16K = uint8(executionAddress>>14) - 1
        fe.ExecutionAddressMod16K = uint16((executionAddress & 0x3fff) | 0x8000)
    }
    return di.addFile(name, fe, data)
}
```

Notable encoding choices:

- `StartAddressPage = (loadAddress >> 14) - 1` (samfile.go:485). For
  `loadAddress = 0x6000`: `0x6000 >> 14 = 1`, minus 1 → **page byte
  = 0**. This is the "samfile convention" decoded by
  `samfile.go:411-413`'s `StartAddress = (StartAddressPage&0x1F + 1)<<14
  | (StartAddressPageOffset&0x3FFF)`. It is *not* what the ROM SAVE
  routine writes — see §1.6.
- `StartAddressPageOffset = loadAddress & 0x3FFF` (samfile.go:486).
  **No `0x8000` bit set.** This contradicts Tech Manual L4390–4392
  ("PAGE OFFSET (8000-BFFFH)") and Tech Manual L3047 ("offset is
  8000H-BFFFH") and what ROM `HDRNMS` writes (see §1.6).
- `ExecutionAddressMod16K`: bit 15 *is* set when there is an exec
  address (`samfile.go:492`: `| 0x8000`). So the start- and exec-
  address encodings disagree about whether to set the marker bit —
  exec sets it, start does not. (The ROM sets bit 15 on both at SAVE
  time; see §1.6.)
- `ExecutionAddressDiv16K = (execAddr >> 14) - 1` (samfile.go:491) —
  same off-by-one as start-address.

### 1.4 What `addFile` does next

`samfile.go:511-552`. After `AddCodeFile` builds the partial
`FileEntry`:

1. Find a free directory slot (`dj.FreeFileEntries()[0]`,
   samfile.go:513). "Free" means `!Used()` (samfile.go:329-345),
   which treats unknown types and zero first-sector tracks as free
   (samfile.go:347-355).
2. Compute required sector count: `(len(data) + 9 + 509) / 510`
   (samfile.go:517).
3. Pull free sectors from `dj.CombinedSectorMap().FreeSectors()`
   (samfile.go:518) — the disk-wide SAM map is the OR of every
   directory entry's per-file map (samfile.go:321-327, matches Tech
   Manual L4419–4420).
4. Allocate them in iteration order — i.e. **bit-order through the
   sector address map**, starting at T4S1, then T4S2, …, T4S10,
   T5S1, …, T79S10, T128S1, …, T207S10 (samfile.go:104-129's
   `filterSectors` walks `track`/`sector` in that exact order).
5. Build the 9-byte body header from the dir entry
   (`fe.CreateHeader()`, samfile.go:501-509). **Note this passes
   `StartAddressPageOffset` as `PageOffset`** unchanged — i.e. NOT
   re-orred with `0x8000`. So the body header for a stub built by
   `samfile add` would read `13 lenL lenH 00 60 00 00 00 00` for
   load-address 0x6000 — bit 15 of byte 4 is **clear**. (Compare to
   ROM SAVE — see §1.6.)
6. Write the body sectors with chain links at offsets 510–511
   (samfile.go:535-548).
7. Mark each used sector in the new dir entry's per-file SAM map
   (`fe.SectorAddressMap[offset] |= byte(mask)`, samfile.go:546). **THIS
   IS WHERE THE `:564` BUG BITES.** See §1.5.
8. Commit the dir entry to the image (samfile.go:550,
   `WriteFileEntry`).

### 1.5 The `:564` bug

`samfile.go:562-565`:

```go
func (sector *Sector) SAMMask() (offset uint8, mask uint8) {
    bitOffset := (int(sector.Track)&0x7f)*10 + int(sector.Sector) - 1 + ((int(sector.Track)&0x80)>>7)*800 - 40
    return uint8(bitOffset >> 3), 1 << bitOffset & 0x07
}
```

Per the Go spec § "Operators" (`https://go.dev/ref/spec#Operators`),
`<<` and `&` are both multiplicative operators and bind left-to-right
at the same precedence. `1 << bitOffset & 0x07` parses as
`(1 << bitOffset) & 0x07`. The intent — "mask = bit `bitOffset & 7`
within the byte" — would be expressed as `1 << (bitOffset & 0x07)` or
`1 << (bitOffset % 8)`.

Failure mode: for any `bitOffset` where `bitOffset % 8 ≥ 3`, the
expression `(1 << bitOffset) & 0x07` is zero (the only set bit of
`1 << bitOffset` is at position `bitOffset`, which is ≥ 3, hence outside
the `0x07` mask). So the per-file SAM map records only sectors whose
absolute `bitOffset` happens to land in positions 0, 1, or 2 of any
byte — i.e. sectors at offsets 0/1/2, 8/9/10, 16/17/18, …. But the
intended-and-correct behaviour is that the bit at byte offset
`bitOffset / 8`, position `bitOffset % 8`, gets set unconditionally.

Worked example: `samfile add` of samdos2 (chain T4S1..T5S10, bitOffsets
0..19):

| Sector | bitOffset | byte | bit_within_byte | `1 << bitOffset & 0x07` | correct mask |
|--------|-----------|------|-----------------|-------------------------|--------------|
| T4S1   | 0         | 0    | 0               | `(1<<0)&7 = 1` ✓        | 0x01         |
| T4S2   | 1         | 0    | 1               | `(1<<1)&7 = 2` ✓        | 0x02         |
| T4S3   | 2         | 0    | 2               | `(1<<2)&7 = 4` ✓        | 0x04         |
| T4S4   | 3         | 0    | 3               | `(1<<3)&7 = 0` ✗        | 0x08         |
| T4S5   | 4         | 0    | 4               | `(1<<4)&7 = 0` ✗        | 0x10         |
| T4S6   | 5         | 0    | 5               | `(1<<5)&7 = 0` ✗        | 0x20         |
| T4S7   | 6         | 0    | 6               | `(1<<6)&7 = 0` ✗        | 0x40         |
| T4S8   | 7         | 0    | 7               | `(1<<7)&7 = 0` ✗        | 0x80         |
| T4S9   | 8         | 1    | 0               | `(1<<8)&7 = 0` ✗        | 0x01         |
| T4S10  | 9         | 1    | 1               | `(1<<9)&7 = 0` ✗        | 0x02         |
| T5S1   | 10        | 1    | 2               | `(1<<10)&7 = 0` ✗       | 0x04         |
| ...    | ...       | ...  | ...             | ...                     | ...          |

So **with the current samfile, slot 0's per-file SAM map would
record only T4S1, T4S2, T4S3** — three of the twenty sectors. The
remaining 17 are absent from slot 0's map, which means
`dj.CombinedSectorMap().FreeSectors()` (samfile.go:321-327) lists them
as free, so the next `samfile add` allocates them — colliding with
samdos2's body. This makes multi-file builds via `samfile add`
unreliable on the current upstream.

The fix is one line. Audit-trail-friendly form:

```go
return uint8(bitOffset >> 3), 1 << (bitOffset & 0x07)
```

Cited justification: Tech Manual L4407–4413 ("Bit 0 of the first byte
is allocated to track 4 sector 1"), `docs/notes/sam-disk-format.md`
§3.1–§3.3. The corrected expression is consistent with the byte
offset on the same line (`uint8(bitOffset >> 3)`), which already
uses the correct `bitOffset / 8` semantics.

build-disk.sh works around the bug with the explicit
`set_sector_in_map` helper (`tools/build-disk.sh:76-78`):

```python
def set_sector_in_map(sam_map: bytearray, track: int, sector: int) -> None:
    b = sector_bit(track, sector)
    sam_map[b // 8] |= 1 << (b % 8)        # correct (cf. samfile bug above)
```

### 1.6 What ROM SAVE actually writes (the canonical reference)

ROM `HDRNMS` (`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22210-22227`)
encodes a number into HDR+HDN+0..+2 / +3..+5 / +6..+8:

```
E10B CD8C3F   CALL UNSTLEN     ; AHL = page/offset, HL = offset_LSB:offset_MSB in 0x0000-0x3FFF form
E10E D1       POP DE           ; DE saved as the dest pointer
E10F EB       EX DE,HL         ; HL = ptr to dest, ADE = addr (D = offset_MSB)
E110 77       LD (HL),A        ; byte 0: page-byte
E111 23       INC HL
E112 73       LD (HL),E        ; byte 1: offset_LSB
E113 23       INC HL
E114 CBFA     SET 7,D          ; force bit 15 on offset_MSB
E116 72       LD (HL),D        ; byte 2: offset_MSB | 0x80
```

The 3-byte triple is `<page>, <offset_LSB>, <offset_MSB | 0x80>`.
For `loadAddress = 0x6000`: `UNSTLEN(0x6000)` returns `A=1, HL=0x2000`
(Tech Manual L4316–4322 / `UNSTLEN` block-comment at
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:14770`); after
`SET 7, D`, the high byte of HL becomes 0xA0; bytes written to disk =
`01 00 A0`. Compare to **what samfile would write**:

| Field                             | samfile `AddCodeFile`              | ROM `HDRNMS`                      |
|-----------------------------------|------------------------------------|-----------------------------------|
| StartAddressPage (dir 0xEC)       | `(loadAddr>>14) - 1` = `0`         | `loadAddr / 16384` = `1`          |
| StartAddressPageOffset (dir 0xED-EE) | `loadAddr & 0x3FFF` LE = `00 60` | `(loadAddr & 0x3FFF) \| 0x8000` LE = `00 A0` |
| Body PageOffset (body 3-4)        | mirror of dir 0xED-EE = `00 60`    | mirror of dir 0xED-EE = `00 A0`   |

samfile's `Start()` decoder (`samfile.go:411-413`):
```go
return uint32(fe.StartAddressPageOffset&0x3fff) | uint32(fe.StartAddressPage&0x1f+1)<<14
```
…round-trips its own encoding (`(0+1)*16384 + 0x2000 = 0x6000`) but
**does not match what a real ROM SAVE writes**. The Tech Manual
formula at L4316–4322 ("AND with 1FH to get the page number…
multiply the page number by 16384, add the offset, and subtract
4000H") is consistent with the samfile decoder only if you
re-introduce the implicit `+1` page bias. samfile's `+1` and the
Tech Manual's `-4000H` are equivalent (4000H = 16384). The
disagreement is only on the bit-15 marker on the offset.

Concrete consequence: an MGT image saved by ROM `HDRNMS` and read
back by `samfile ls` (`samfile.go:411`) decodes to **the same address**
because samfile masks `& 0x3FFF`. So samfile-as-a-reader is
spec-compliant. But samfile-as-a-writer (`AddCodeFile`) emits
non-spec PageOffset bytes — bit 15 unset — which `PDPSR2`-on-LOAD
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:4499-4527`)
would mis-decode if any consumer used the body-header start address
as-is.

In practice for our use case (`stub` and `IN` are LOAD-CODE-with-
explicit-addr, where the BASIC line provides the actual address) the
discrepancy is benign — but it is non-canonical.

### 1.7 What samfile does NOT do

- **No BASIC type-16 support.** No CLI flag, no Go-level
  `AddBASICFile`. `addFile` (`samfile.go:511-552`) is private; the
  only public entry is `AddCodeFile`. To add a BASIC file you'd have
  to fork samfile.
- **No SCREEN$ / array / snapshot support.** Same reason.
- **No way to set BASIC start-line.** The `FileEntry` struct has the
  field (`samfile.go:34`, `SAMBASICStartLine uint16`) but it is only
  ever *read* (in `Output()` at `samfile.go:386`); it is never
  written by any code path in samfile.
- **No way to set the BASIC prog-length triplets at dir 0xDD-0xE5.**
  Same shape: `samfile.go:395-405` reads `ProgramLength()`,
  `NumericVariableOffset()`, `StringArrayVariableOffset()` from
  `FileTypeInfo[0..8]`; none of these are ever written by
  `AddCodeFile` (which leaves `FileTypeInfo` zero — see
  `samfile.go:483-489`, `addFile` doesn't touch `FileTypeInfo`).
- **No control over allocation order / first sector.** `addFile`
  allocates sequentially from the first free sector (samfile.go:524
  `fe.FirstSector = freeSectors[0]`). For samdos2, on a freshly-zeroed
  disk that happens to be T4S1, but if anything has already allocated
  T4S1, samfile would place samdos2 elsewhere — and the disk would
  not boot (per `docs/notes/sam-disk-format.md` §5).
- **No way to leave the body header alone.** `addFile` always
  prepends the 9-byte body header it synthesises from the FileEntry
  (`samfile.go:529-533`). So you cannot pass samfile a 10000-byte
  pre-headered samdos2 binary and ask it to write only the body.
  (You *could* pass samfile the 9991-byte body and let it construct
  the 9-byte header from the FileEntry's PageOffset / Pages /
  StartPage / Length fields, but: (a) you have to set those exactly
  right, including the non-canonical PageOffset encoding noted
  above; (b) samfile always writes type-19 in the synthesised header
  via `CreateHeader()` (samfile.go:503: `Type: fe.Type`) which is
  fine for samdos2's "type 19" convention but fixed across the call;
  (c) there's no `SetType` API.)

### 1.8 Summary of `samfile add` capabilities for our four files

| Slot | File     | Can `samfile add` produce it? | Why / why not |
|------|----------|-------------------------------|---------------|
| 0    | samdos2  | **Almost** — modulo bit-15 marker on PageOffset, modulo `:564` bug, modulo first-sector placement on a fresh disk happening to be T4S1. samfile would emit the 9-byte body header in front of the samdos2 body, with the `BOOT` literal landing at sector offset 256 (because the header is 9 bytes). | `samfile.go:483-499` would set type=19, name=samdos2, start=load-addr, sectors=20, chain=T4S1..T5S10 (via `freeSectors[0..19]` on empty disk). |
| 1    | auto     | **No.** No type-16 support; no start-line setter; no prog-length triplet writer. | `AddCodeFile` is hardcoded to type 19 (`samfile.go:484`). |
| 2    | stub     | **Yes** (with `:564` patch). | A plain CODE blob with explicit load address. samfile would write everything correctly except: (a) bit-15 marker on PageOffset; (b) bytes 5-6 of body header would be `00 00`, not `FF FF`. |
| 3    | IN       | **Yes** (with `:564` patch). | Same as stub. |

So **with a `:564` patch + a new BASIC-emit feature**, samfile could
build slots 1, 2, 3 unaided, and could build slot 0 if we are willing
to relax the bit-15 marker on the body PageOffset (cosmetic).

---

## 2. Byte-by-byte comparison: hand-roll vs. `samfile add`

For each slot, this section tabulates what `samfile add -i img -f
$name -c -l $loadAddr [-e $exec]` would emit assuming the `:564` bug
is patched, vs. what `tools/build-disk.sh` actually emits at HEAD.
The "hand-roll" column is taken from the build-disk.sh source
(`tools/build-disk.sh:60-323`) and verified against
`build/test.mgt`. The "samfile" column is derived from
`samfile.go:268-310` (`Raw()`), `samfile.go:511-552` (`addFile`), and
`samfile.go:578-590` (`FileHeader.Raw`).

### 2.1 Slot 0: `samdos2` — directory entry (256 bytes at image offset 0x000)

Cannot be produced by current samfile (no way to force first sector
to T4S1; samfile would also emit a 9-byte type-19 body header in
front of the samdos2 body, displacing the BOOT signature by 9
bytes — wait, that's actually what build-disk.sh does too, so the
header is the same problem for both: the BOOT signature at body
offset 247 lands at sector offset 256, exactly where ROM `BOOTEX`
expects it (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:20582-20598`,
checked at `8100H..8103H`).

| Dir byte | Field                         | Hand-roll (build-disk.sh) | samfile add (with :564 fixed) | Cite |
|----------|-------------------------------|---------------------------|-------------------------------|------|
| 0x00     | Type                          | `0x13` (build-disk.sh:170) | `0x13` (samfile.go:484, FT_CODE) | Tech Man L4304-4314 |
| 0x01-0x0A | Name "samdos2   "           | `73 61 6D 64 6F 73 32 20 20 20` | same (samfile.go:522) | Tech Man L4358-4359 |
| 0x0B-0x0C | Sectors (BE)                | `00 14` = 20 (build-disk.sh:90-91) | same (samfile.go:275-276; samdos2 = 10000 bytes; (10000+9+509)/510 = 20) | samfile.go:243; Tech Man L4360-4361 |
| 0x0D     | First-sector track            | `04` (build-disk.sh:93, chain[0][0]) | `04` (samfile.go:524, freeSectors[0] on empty disk = T4S1) | Tech Man L4362 |
| 0x0E     | First-sector sector           | `01` | `01` | Tech Man L4363 |
| 0x0F-0xD1 | SAM map                     | bits set for T4S1..T5S10 (build-disk.sh:94-97) | **with `:564` fixed** — same; **with current bug** — only bits at positions 0,1,2 within the LSByte set; missing 17 of 20 bits | Tech Man L4407-4413; samfile.go:562-565 |
| 0xD2-0xDB | MGTFutureAndPast            | `00 00 ...` | `00 00 ...` (samfile.go:284-286 emits FE.MGTFutureAndPast which is zero from `addFile`) | Tech Man L4366-4368 says "not used"; SAMDOS source `f.s:462-471` shows it IS used — both writers leave it unwritten. |
| 0xDC     | MGTFlags                      | `00` | `00` (samfile.go:287, FE.MGTFlags zero) | Tech Man L4369 |
| 0xDD-0xE7 | FileTypeInfo                | `00 00 00 ... (11 bytes)` (build-disk.sh leaves unset; build/test.mgt confirms) | `00 00 00 ...` (samfile.go:289-291 emits FileTypeInfo, which AddCodeFile leaves zero) | For type-19 these bytes are unused, Tech Man L4374-4378 mentions them only for type 16/17/18/20. |
| 0xE8-0xEB | ReservedA                   | `00 00 00 00` | `00 00 00 00` | Tech Man L4382 |
| 0xEC     | StartPage                     | `00` (build-disk.sh:170 default; not set explicitly) | `01` (samfile.go:485, `(0x8000>>14)-1 = 1`) | Tech Man L4388-4389 |
| 0xED-0xEE | PageOffset (LE)             | `00 00` (build-disk.sh:170 default) | `00 80` (samfile.go:486, `0x8000 & 0x3FFF = 0x0000` — wait — for samdos2 we'd be calling `AddCodeFile(name, body, loadAddr=0x8000, ...)`, so `loadAddr & 0x3FFF = 0`, giving `00 00`. **Both encoders agree.** Note: bit-15 not set; both fail Tech Man L4390-4392's "8000-BFFFH" rule. ROM SAVE would set bit 15.) | Tech Man L4390-4392 |
| 0xEF     | Pages                         | `00` (`length = 10000 < 16384`, build-disk.sh:99) | `00` (samfile.go:525-526; `len(data) >> 14 = 0`) | Tech Man L4393 |
| 0xF0-0xF1 | LengthMod16K (LE)           | `10 27` = 10000 (build-disk.sh:98-99) | `10 27` (samfile.go:526) | Tech Man L4394-4395 |
| 0xF2     | ExecAddrDiv16K                | `FF` (build-disk.sh:175) | `FF` (samfile.go:487, when no exec) | Tech Man L4396-4398; ROM E287 |
| 0xF3-0xF4 | ExecAddrMod16K (LE)         | `FF FF` | `FF FF` (samfile.go:488) | Tech Man L4396-4398 |
| 0xF5-0xFF | ReservedB                   | `00 00 ...` | `00 00 ...` | Tech Man L4399-4400 |

**Verdict (slot 0 dir entry):** Hand-roll and samfile would emit
nearly the same bytes for samdos2's dir entry. Differences:
- 0xEC (StartPage): hand-roll `0x00`, samfile `0x01`. Hand-roll is
  arguably wrong here per Tech Man L4316-4322 / L3037 ("16K page that
  file starts in"); samfile's `0x01` matches the ROM-SAVE convention
  (`UNSTLEN(0x8000)` returns A=2, but with -1 bias = 1).
- 0xED-0xEE (PageOffset): both produce `00 00`. Both fail Tech Man
  L4390-4392 ("PAGE OFFSET (8000-BFFFH)") which would require
  `00 80` (= 0x8000 with bit 15 set). Neither writer matches a real
  ROM SAVE byte-for-byte — but the discrepancy is benign because
  both decoders mask `& 0x3FFF` on read.

### 2.2 Slot 0: `samdos2` — body 9-byte header at sector offset 0 of T4S1 (image offset 0xA000)

| Body byte | Field         | Hand-roll bytes (build-disk.sh:155-162) | samfile add (with :564 fixed) | Cite |
|-----------|---------------|-----------------------------------------|-------------------------------|------|
| 0         | Type          | `13`                                    | `13` (samfile.go:503)         | Tech Man L4286 |
| 1-2       | LengthMod16K  | `10 27` (=10000)                        | `10 27` (samfile.go:504)      | Tech Man L4286-4288 |
| 3-4       | PageOffset    | `00 80` (=0x8000) (build-disk.sh:158)   | `00 00` (samfile.go:505 mirrors dir 0xED-0xEE which is `(loadAddr=0x8000) & 0x3FFF = 0`) | Tech Man L4290 |
| 5-6       | Unused        | `FF FF` (build-disk.sh:159) — canonical SAVE | `00 00` (samfile.go:585-586 hard-codes `0, 0`) | Tech Man L4293 |
| 7         | Pages         | `00`                                    | `00`                          | Tech Man L4294 |
| 8         | StartPage     | `01` (build-disk.sh:162)                | `01` (samfile.go:507)         | Tech Man L4295 |

**Slot 0 body header verdict:** Hand-roll wins on PageOffset
(canonically encodes `0x8000` per Tech Man L4290 — `OFFSET START`,
but samfile decoders accept either). Hand-roll wins on bytes 5-6
(canonical `FF FF` per Tech Man L4293). Samfile.go:578-590 hard-codes
bytes 5-6 to zero, which is non-canonical.

### 2.3 Slot 1: `auto` — directory entry (256 bytes at image offset 0x100)

**Cannot be produced by samfile.** samfile has no type-16 support.
This entire slot is hand-rolled. The fields are documented per
`docs/notes/sam-file-header.md` §4.2-4.3 and verified against
`build/test.mgt` as of `2026-05-10 14:51`:

| Dir byte | Field                          | Hand-roll bytes                  | What samfile WOULD emit if forced (impossible via CLI) | What ROM SAVE writes |
|----------|--------------------------------|-----------------------------------|--------------------------------------------------------|----------------------|
| 0x00     | Type                           | `0x10` (build-disk.sh:225)         | n/a                                                    | `0x10` (Tech Man L4310; ROM E0E1 `LD A,16`) |
| 0x01-0x0A| Name "auto      "             | `61 75 74 6F 20 20 20 20 20 20`    | n/a                                                    | similar (LDIR copy at ROM E06F) |
| 0x0B-0x0C| Sectors                        | `00 01` (build-disk.sh:90-91)      | n/a                                                    | computed by SAMDOS at SAVE |
| 0x0D-0x0E| First sector                   | `06 01` (T6S1)                     | n/a                                                    | first free data sector |
| 0x0F-0xD1| SAM map                        | bit set for T6S1                   | n/a                                                    | bit set per file |
| 0xDD-0xDF| Prog-length triplet            | `39 80 00` (build-disk.sh:240-241; verified in build/test.mgt offset 0x1DD) | n/a | `(NVARS-PROG)` page-form (ROM E0B4-E0E0; `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22163-22180`). For an AUTO program with no variables, equals body length. |
| 0xE0-0xE2| Prog+NVARS triplet             | `39 80 00`                         | n/a                                                    | `(NUMEND-PROG)` page-form |
| 0xE3-0xE5| Prog+NVARS+gap triplet         | `39 80 00`                         | n/a                                                    | `(SAVARS-PROG)` page-form |
| 0xEC-0xEE| Start (StartPage / PageOffset) | `00 D5 9C` (build-disk.sh:245-247) | n/a                                                    | `00 D5 9C` (mirror of body header bytes 8 / 3-4) |
| 0xEF     | Pages                          | `00`                               | n/a                                                    | `00` |
| 0xF0-0xF1| LengthMod16K                   | `39 00` (=57)                      | n/a                                                    | `39 00` |
| 0xF2     | autorun marker                 | `00` (build-disk.sh:106-108; start_line >= 0 path) | n/a                              | `00` (ROM E08E `LD (HL),0`); ROM E287 `CP 0FFH; JR NZ,HDLDEX` confirms `00` triggers auto-RUN |
| 0xF3-0xF4| Auto-RUN line                  | `0A 00` (=10 LE)                   | n/a                                                    | line number LE (ROM E091 `LD (HL),C`; E093 `LD (HL),B`) |

**Slot 1 verdict:** Hand-roll is the only option. Encoding choices
match Tech Man / ROM canonical paths; bytes verified against
`build/test.mgt`. The prog-length triplet encoding `39 80 00` is
"page=0, offset=0x0039 with bit 15 set as marker" — i.e. the same
8000H-form as a length per `UNSTLEN` block-comment at
`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:14770`
("RES 7,H IF 5-BYTE IS A LENGTH"). However, examination of the SAVE
path at L22163-22180 shows ROM uses `SUBAHLCDE` (subtract page-form
values) without re-clearing bit 7, and the per-byte stores at L22177
do not RES bit 7. So whether the saved triplet has bit 15 of byte 2
set is ROM-SAVE-implementation-dependent. The hand-roll's choice to
set it (per build-disk.sh:235-238 `page_form_3byte`'s
`offset = (value % 16384) | 0x8000`) is consistent with the
`UNSTLEN`-then-`SET 7,H` convention used for addresses elsewhere in
the ROM (e.g. `HDRNMS` E114). This is plausible but not
explicitly-cited-as-canonical for the prog-length triplet
specifically. **Flag: uncertain** — to verify, compare against a
real-disk capture (Pete has `/Users/pmoore/Downloads/GoodSamC2/x.mgt`
CHOMPER, which shows triplet `00 05 92` for a body of larger size; the
high bit of the offset MSB is set, consistent with our `00 80`
choice).

### 2.4 Slot 1: `auto` — body 9-byte header at T6S1 sector offset 0 (image offset 0xF000)

| Body byte | Field         | Hand-roll bytes (build-disk.sh:216-223) | What samfile WOULD emit | Cite |
|-----------|---------------|-----------------------------------------|-------------------------|------|
| 0         | Type          | `10`                                    | n/a                     | Tech Man L4286 |
| 1-2       | LengthMod16K  | `39 00`                                 | n/a                     | Tech Man L4287 |
| 3-4       | PageOffset    | `D5 9C` (=0x9CD5; PROG=0x5CD5 in 8000H form) | n/a                | Tech Man L4290; PROG addr from ROM `EBFB`/`EC2B` boot init |
| 5-6       | Unused        | `FF FF`                                 | n/a                     | Tech Man L4293 |
| 7         | Pages         | `00`                                    | n/a                     | Tech Man L4294 |
| 8         | StartPage     | `00`                                    | n/a                     | Tech Man L4295 |

Verdict: matches what real BASIC SAVE writes (per CHOMPER reference
in `docs/notes/sam-file-header.md` §1 example).

### 2.5 Slot 2: `stub` — directory entry (256 bytes at image offset 0x200)

| Dir byte | Field                | Hand-roll | samfile add (`-l 24576`, `:564` fixed) | Verdict / cite |
|----------|----------------------|-----------|----------------------------------------|----------------|
| 0x00     | Type                 | `13`      | `13`                                   | Same. Tech Man L4313 |
| 0x01-0x0A| Name                 | `73 74 75 62 20 20 20 20 20 20` (build-disk.sh:287, "stub      ") | same (samfile would derive from `filepath.Base(file)`; if file is `stub.bin` then SAM filename = "stub.bin   " — different! See note below.) | The basename in `add.go:43` is the on-host filename including extension; "stub.bin" → `samfile ls` would show "stub.bin". To match build-disk.sh's "stub", you'd need to symlink or rename. **Real difference.** |
| 0x0B-0x0C| Sectors              | `00 01`   | `00 01` (`(5 + 9 + 509)/510 = 1`)      | Same |
| 0x0D-0x0E| First sector         | `06 02`   | depends — first free sector after slot 0/slot 1 is consumed. With samdos2 in slots 0, samfile's freeSectors order would be T6S1 (after T5S10 used by samdos2). | Different unless samfile is run in the right order. |
| 0x0F-0xD1| SAM map              | bit for T6S2 set (build-disk.sh:94-97) | bit for whichever sector samfile picks (with `:564` fix: same logic; bug-affected without it) | logic matches with patch |
| 0xDD-0xE7| FileTypeInfo         | `00 ...`  | `00 ...` (samfile.go:289-291 — AddCodeFile leaves FileTypeInfo zero, samfile.go:483-489) | Same |
| 0xEC     | StartPage            | `01` (build-disk.sh:294, addr_to_page_offset(0x6000)→1) | `00` (samfile.go:485, `(0x6000>>14)-1 = 0`) | **Different!** Hand-roll matches `UNSTLEN(0x6000)` page=1; samfile uses (page>>14)-1 convention. ROM `HDRNMS` would write page byte = 1 (E110 `LD (HL),A` where A came from `UNSTLEN`). Hand-roll matches ROM. |
| 0xED-0xEE| PageOffset           | `00 A0` (build-disk.sh:295-296, 8000H form) | `00 60` (samfile.go:486, no bit-15) | **Different!** Hand-roll matches Tech Man L4390-4392 8000H-form requirement and ROM `HDRNMS` `SET 7,D`. |
| 0xEF     | Pages                | `00`      | `00` (samfile.go:525)                  | Same |
| 0xF0-0xF1| LengthMod16K         | `05 00` (=5)| `05 00` (samfile.go:526)             | Same |
| 0xF2-0xF4| Exec                 | `FF FF FF` (no exec) | `FF FF FF` (samfile.go:487-488 default) | Same |

**Slot 2 verdict:** With `:564` fixed, samfile would produce a
`stub` dir entry that is correct in samfile's own decoding system
(round-trips to load=0x6000) but differs from a real ROM SAVE in
two bytes:
- 0xEC: hand-roll has `01`, samfile has `00` (StartPage convention).
- 0xED-0xEE: hand-roll has `00 A0`, samfile has `00 60` (8000H bit).

For our use case (LOAD CODE with explicit address), neither
convention is materially harmful — both samfile and the ROM
preserve `& 0x3FFF` of the offset, so both decode to the same load
address. But the hand-roll is closer to canonical.

### 2.6 Slot 2: `stub` — body 9-byte header

| Body byte | Field         | Hand-roll (build-disk.sh:278-285) | samfile add (`-l 24576`) | Cite |
|-----------|---------------|-----------------------------------|--------------------------|------|
| 0         | Type          | `13`                              | `13`                     | Tech Man L4313 |
| 1-2       | LengthMod16K  | `05 00`                           | `05 00`                  | Tech Man L4287 |
| 3-4       | PageOffset    | `00 A0` (8000H-form)              | `00 60` (samfile.go:505) | **Different.** Hand-roll wins. |
| 5-6       | Unused        | `FF FF` (build-disk.sh:282)        | `00 00` (samfile.go:585-586) | **Different.** Hand-roll matches real BASIC SAVE per `docs/notes/sam-file-header.md` §1. |
| 7         | Pages         | `00`                              | `00`                     | Same |
| 8         | StartPage     | `01` (build-disk.sh:284)           | `00` (samfile.go:507)    | **Different.** Hand-roll matches `UNSTLEN(0x6000) → A=1`. |

### 2.7 Slot 3: `IN`

Same shape as slot 2, with first-sector T6S3 and load_addr=0x6000.
Same byte differences as §2.5/§2.6. **Note**: in the current
build/test.mgt (mtime 2026-05-10 14:51), slot 3's dir bytes 0xEC-0xEE
are still `00 00 00`, which means build-disk.sh either was rebuilt
since test.mgt was generated, or the IN-specific block at
build-disk.sh:319-322 isn't running. Either way, the hand-roll is
correct in source — it's just not reflected in this test.mgt.

---

## 3. Summary table — byte differences

Severity-ranked. "Severity" is operational impact at SAMDOS-LOAD or
ROM-LOAD time; "spec compliance" is per Tech Man + ROM disasm.

| # | Severity | Description | Hand-roll value | samfile value | ROM/Tech-Man canonical | Cite |
|---|----------|-------------|-----------------|---------------|------------------------|------|
| 1 | **Critical for slot 0** | First-sector pin to T4S1 | hand-rolled (build-disk.sh:149) | first free; depends on disk state | T4S1 — required by ROM `BOOTEX` | `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:20524` |
| 2 | **Critical for slot 1** | BASIC type-16 emission | hand-rolled | not supported | type 16 needed for AUTO recognition by SAMDOS | `samdos/src/h.s:201-212` |
| 3 | **Critical for slot 1** | BASIC start-line | hand-rolled (build-disk.sh:227, dir 0xF2=0, 0xF3-F4=line) | not supported | dir 0xF2=0 + line LE | Tech Man L3052 (HDR 37-39); ROM E08E-E093 |
| 4 | **High for slot 1** | BASIC prog-length triplets at 0xDD-0xE5 | hand-rolled (build-disk.sh:240-243) | not supported | three page-form values | Tech Man L4376-4381; ROM E0B4-E0E0 |
| 5 | **High for all slots** | `:564` SAMMask bug | works around (build-disk.sh:76-78) | bug active, mask zero for `bitOffset%8 ≥ 3` | `1 << (bitOffset & 7)` | `samfile.go:564` |
| 6 | Medium | StartPage byte semantics (page = addr/16384, not -1) at 0xEC | hand-roll: `(addr>>14)` (build-disk.sh:266) | samfile: `(addr>>14) - 1` (samfile.go:485) | `UNSTLEN` returns `addr/16384`, ROM E110 stores it directly | `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:14770-14786, 22210-22216` |
| 7 | Medium | PageOffset bit-15 marker at dir 0xED-0xEE and body bytes 3-4 | hand-roll sets it (build-disk.sh:267) | samfile leaves clear (samfile.go:486) | ROM `HDRNMS` sets it via `SET 7,D` | `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22220-22227`; Tech Man L3047-3049, L4390-4392 |
| 8 | Cosmetic | Body header unused bytes 5-6 | hand-roll: `FF FF` (build-disk.sh:159, 220, 282, 310) | samfile: `00 00` (samfile.go:585-586) | Real BASIC SAVE writes `FF FF` | `docs/notes/sam-file-header.md` §1 (CHOMPER worked example) |
| 9 | Cosmetic | Filename source for code files | hand-roll: explicit (build-disk.sh:287, 315) | samfile: `filepath.Base(file)` incl. extension | ROM SAVE: from BASIC syntax | `add.go:43` |
| 10 | Cosmetic | dir bytes 0xD3-0xDB SAMDOS body-header cache | both leave zero | both leave zero | SAMDOS writes 9-byte mirror via `svhd` | `samdos/src/f.s:462-471` |

Note: differences #6 and #7 are cosmetic for our LOAD CODE flow
(LOAD with explicit addr supplies HDR; HDL is consulted only when
LOAD CODE has no explicit addr). They become operational if anything
ever consumes the saved body PageOffset directly via `PDPSR2` — see
`docs/notes/sam-paging.md` §4 worked Example E.

---

## 4. Are samfile's gaps real or fabricated?

For each thing build-disk.sh does that samfile cannot, this section
asks: is the missing capability grounded in real SAM/SAMDOS spec
behaviour, or is it a guess?

### 4.1 Pinning samdos2's first sector to T4S1 — REAL

ROM `BOOTEX` reads track 4 sector 1 raw to `&8000`
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:20524-20564`).
The signature check at L20582-20598 expects the literal "BOOT" at
sector buffer offsets 256-259 (i.e. T4S1 sector body offsets 256-259).
For SAMDOS to be the boot target, its file body's offset 247 (= 256
− 9 for the 9-byte body header) must contain the BOOT signature —
which the canonical samdos2 binary does
(`/Users/pmoore/git/samdos/res/samdos2.reference.bin` offset 0xF7 =
`42 4F 4F D4`). Therefore samdos2 must be the file occupying T4S1.

**This is a real ROM mechanism, not fabricated.** Authoritative
sources: ROM `BOOTEX`/`BTNOE`/`BTCK` flow (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:20473-20598`),
`BTWD` keyword-table entry at `FB94H`
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:26919`),
SAMDOS source `b.s:206-207` (`defm "BOO" / defb "T"+&80`).

### 4.2 BASIC start-line auto-RUN — REAL

Tech Manual L3047-3052 explicitly documents the encoding:

> 37  (1)  ... If Basic program, 00 if there is an auto-run line
>          number in the next two bytes, or FF if there isn't one.
> 38-39 (2) ... If Basic program, auto-run line number if there is
>          one.

ROM SAVE writes this at E08E-E093:
```
E08E 3600       LD (HL),0           ;FLAG 'AUTORUN'
E090 23         INC HL
E091 71         LD (HL),C
E092 23         INC HL
E093 70         LD (HL),B           ;PLACE LINE NO.
```
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22136-22141`,
where `HL = HDR+HDN+6 = HDR+37`.)

ROM LOAD honours this at E281-E289:
```
E281 3A254B     LD A,(HDR+HDN+6)    ; = HDR+37
E284 2A264B     LD HL,(HDR+HDN+7)   ; = HDR+38..39
E287 FEFF       CP 0FFH
E289 2009       JR NZ,HDLDEX
```
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22467-22484`.)

For BASIC type-16 the byte at 0xF2 == 0 means "auto-run; line at
0xF3-0xF4". This is in HDR — i.e. *requested* — and copies into HDL
during LOAD via SAMDOS. For our AUTO file, the dir entry's 0xF2-0xF4
becomes HDL+HDN+6..+8.

**samfile's lack of start-line support is a real gap.** The
field exists in the struct (`samfile.go:34, SAMBASICStartLine
uint16`) and is read by `Output()` (samfile.go:386), but no
public API allows setting it. To support our use case, samfile
would need: (a) a `-b` (BASIC) flag, (b) an optional `--auto-run-line N`
flag, (c) an `AddBASICFile(name string, data []byte, startLine int,
progLength uint32, ...) error` Go API.

### 4.3 BASIC prog-length triplets at dir 0xDD-0xE5 — REAL

ROM SAVE at E0B4-E0E0
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22163-22180`)
writes three 3-byte page-form values at HDR+16/+19/+22 = dir
0xDD/0xE0/0xE3. Tech Manual L3025-3032 documents the semantics:

> 16-18 (3)  If type is 16, holds program length excluding variables.
> 19-21 (3)  If type is 16, holds program length plus numeric variables.
> 22-24 (3)  If type is 16, holds program length plus numeric
>            variables and gap length before string/array variables.
> (The extra data for Basic programs allows NVARS, NUMEND and SAVARS
> to be set up on LOADing).

So at LOAD time, ROM uses these to populate `NVARS`, `NUMEND`,
`SAVARS` sysvars — the variable-area pointers. Without them, after
LOAD the BASIC interpreter's variable bookkeeping is wrong; CLEAR /
DIM / variable allocation would walk into garbage.

**samfile's lack is a real gap, and operationally relevant for any
type-16 file we want to actually run.** Implementation: when BASIC
emit support is added, the writer should compute the three page-form
values from the body length (assuming no variables). The build-disk.sh
helper at lines 235-238 (`page_form_3byte`) shows the encoding.

### 4.4 Sector address map hand-rolling — workaround for `:564`

build-disk.sh's `set_sector_in_map` (build-disk.sh:76-78) replicates
samfile's intended SAMMask logic with the bug fixed. **This is
purely a workaround for the `:564` bug.** With the bug patched,
samfile would produce identical SAM map bytes for any given sector
chain.

There is no other reason to hand-roll this. The 195-byte SAM map
encoding (Tech Man L4407-4413; `docs/notes/sam-disk-format.md` §3) is
unambiguous and matches samfile.go:104-129's bit-walk order
exactly when the mask is computed correctly.

**The "hand-rolling sector address maps" is fabricated-but-justified:
fabricated in the sense that no spec mandates it (the mask can be
computed from the formula); justified by the actual bug in samfile
that makes its computation incorrect.**

### 4.5 Type-19 byte for samdos2 — partially fabricated

build-disk.sh:170 chooses type 19 (CODE) for samdos2's directory
entry. The script's comment says this is to keep `samfile ls` happy
because samfile treats unrecognised types as erased
(samfile.go:347-355: `Type.String()` returns "UNKNOWN (...)" for
types not in the well-known list).

What does SAMDOS itself record? `samdos/src/b.s:288-298` defines the
SAMDOS-internal UIFA for itself:

```asm
uifa:          defb &13                                ; type = 19 (CODE)
               defm "samdos2                  "
               defb &ff,&ff,&ff,&ff,&ff
               defb &7d                                ; StartPage byte (host-page-dependent)
               defb &09
               defb &80                                ; PageOffset = 0x8009 (8000H form)
               defb &00
               defb &10                                ; LengthMod16K = 0x2710 = 10000
               defb &27
               defb &ff
               defb &ff
               defb &ff                                ; exec = FFFFFF (no auto-exec)
```

So the SAMDOS source itself records type **0x13 = 19** for itself,
not type 3. The optional `if defined(include-header)` block at
`b.s:14-22` writes type 3 — but the standard build does not define
`include-header` (`b.s:27 org.adjust = 9`), so the standard
samdos2.reference.bin does NOT have this header. (Verified:
`xxd -s 0 -l 16 /Users/pmoore/git/samdos/res/samdos2.reference.bin`
returns `21 04 00 11 02 04 ...`, which is `LD HL,0004H; LD DE,0402H` —
real Z80 code, not a `defb 3` header.)

**Verdict: build-disk.sh's choice of type 19 matches what SAMDOS
itself records when SAMDOS saves itself via UIFA. The "for samfile
compatibility" justification is correct, AND the choice happens to
match canonical SAMDOS behaviour. Not fabricated.** Type 3 is a
documented-but-default-disabled alternative used only when SAMDOS is
built with `include-header` defined.

### 4.6 Body header bytes 5-6 = `FF FF` — REAL

Tech Manual L4292-4293:

> 5-6     Unused

Real BASIC SAVE behaviour from CHOMPER reference disk:
```
body[0..8] = 10 df 0f d5 9c ff ff 01 00
                       ^^ ^^
                       FF FF (canonical)
```
(`/Users/pmoore/Downloads/GoodSamC2/x.mgt`, captured in
`docs/notes/sam-file-header.md` §1.) ROM `SLMVC` clears the HDR
buffer to `0x20` then `0xFF` at L22082-22087:

```
E031 3620     HDCLP:    LD (HL),20H        ;CLEAR NAMES AREAS WITH SPACES
E033 23                 INC HL
E034 10FB               DJNZ HDCLP
E036 060E               LD B,14
E038 36FF     HDCLP2:   LD (HL),0FFH       ;CLEAR REST WITH FFH
```
(`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22084-22091`.)

So at SAVE time HDR+5..6 are cleared to `FF` and never overwritten
(they're in the "rest" past byte 25). When SAMDOS commits the body
header from HDR+1..2, +3..4, +7-8 it leaves +5..6 untouched at FF.

**Writing FF FF is canonical; writing 00 00 is a samfile choice that
fails to match real SAVE.** Both Tech Man and ROM say "unused", so
both are spec-compliant in the strict sense — but FF FF is what real
disks have. samfile.go:585-586 hard-codes `0, 0`; this should be
changed to `0xFF, 0xFF`.

### 4.7 Putting samdos2 raw at T4S1 — REAL, but with caveats

ROM `BOOTEX` flow (§4.1 above) reads T4S1 to `&8000` and `JP 8009H`.
This is the **canonical** mechanism that real SAM hardware uses to
boot from a disk. The "trick" of placing samdos2 with its body
header at offset 0..8 of T4S1, body content at offset 9..511 (and
chaining to T4S2..T5S10 for the remaining 9991 bytes), is
**exactly** how a SAMDOS disk-image looks to anything that reads it.

That said: a "stock" SAMDOS-formatted disk does NOT contain samdos2
itself in a directory entry. The user FORMATs a disk, then SAMDOS-
internal hooks read SAMDOS from a known location (TBD — Pete's
investigation) and the FORMAT command marks tracks 0-3 as directory
plus initialises sector maps, but samdos2's body is not written to
the disk. To make a self-bootable disk, the SAMDOS body has to be
copied to T4S1 either by:
- A custom bootstrap utility (e.g. FRED 56 publishes a ~8KB
  bootstrap file at T4S1; see `docs/notes/fred-disk-inspection.md`),
- The `BOOT` BASIC command on a disk with an existing SAMDOS file in
  the first directory slot (as far as I can tell from the ROM disasm,
  there is no such auto-write of SAMDOS to the disk — the disk must
  already have SAMDOS at T4S1 when booted).

**build-disk.sh's "samdos2 at T4S1 with synthesised dir entry" is a
real, canonical mechanism**, identical to what FRED 56 and other
auto-running disks do. It is not a fabricated trick. Authoritative
sources cited above plus SAMDOS source `b.s:206-207`.

### 4.8 Filename mapping (`filepath.Base` vs explicit) — REAL gap, minor

samfile's `add` always uses the host file's basename
(`add.go:43`), so to call our code file "stub" rather than "stub.bin"
we'd need to either (a) rename/symlink the host file before
invoking samfile, or (b) add a `-n NAME` flag to samfile. Either is
fine; this is a UX detail, not a fundamental capability gap.

---

## 5. The `:564` bug — full forensic

### 5.1 The literal claim

`/Users/pmoore/git/samfile/samfile.go:560-565`:

```go
}

func (sector *Sector) SAMMask() (offset uint8, mask uint8) {
    bitOffset := (int(sector.Track)&0x7f)*10 + int(sector.Sector) - 1 + ((int(sector.Track)&0x80)>>7)*800 - 40
    return uint8(bitOffset >> 3), 1 << bitOffset & 0x07
}
```

The expression on line 564, `1 << bitOffset & 0x07`, is parsed by Go
as `(1 << bitOffset) & 0x07` because `<<` and `&` are both at
multiplicative precedence with left-to-right associativity (Go spec
§ "Operators": "Binary operators of the same precedence associate
from left to right", and the multiplicative-operator group includes
`* / % << >> & &^`).

### 5.2 Worked verification

For `bitOffset = 5` (T4S6):
- `1 << 5 = 32 = 0b00100000`
- `32 & 0x07 = 0`

For `bitOffset = 9` (T4S10):
- `1 << 9 = 512 = 0b1000000000`
- `512 & 0x07 = 0`

For `bitOffset = 2` (T4S3):
- `1 << 2 = 4`
- `4 & 0x07 = 4` ✓

So `(1 << k) & 0x07` is `2^k` for `k ∈ {0,1,2}` and zero everywhere
else. The intended formula `1 << (bitOffset & 0x07)` would give:

For `bitOffset = 5`:
- `bitOffset & 0x07 = 5`
- `1 << 5 = 32` ✓ — sets bit 5 of the byte.

### 5.3 Upstream status

`git log --oneline` in `/Users/pmoore/git/samfile/`:

```
e64f5d5 Execution Address Page off by one
48a3dc9 Update go.mod for v2
d5dfe83 samfile add command to add files to images
4fa3e38 Use go modules
```

`d5dfe83` introduced `samfile add` and `SAMMask`. The bug has been
present since that commit — not fixed in `e64f5d5` (which addresses
a different off-by-one in the exec page byte; per
`/Users/pmoore/git/samfile/samfile.go:491` `(executionAddress>>14) - 1`,
the off-by-one *was* fixed there in a way that mirrors the start-page
encoding choice). The upstream samfile branch `master` is at
`e64f5d5`; no further commits.

### 5.4 The fix

One-line diff:

```diff
-       return uint8(bitOffset >> 3), 1 << bitOffset & 0x07
+       return uint8(bitOffset >> 3), 1 << (bitOffset & 0x07)
```

Justification (cite-grounded for a PR description):

- The intent of the function is to compute the (byte-offset,
  bit-mask) pair for a sector's bit in the 195-byte SAM map.
- Tech Manual L4407-4413: "Bit 0 of the first byte is allocated to
  track 4 sector 1." Each bit's byte offset is `bitOffset / 8`,
  bit-position-within-byte is `bitOffset % 8`. Mask = `1 <<
  (bitOffset % 8)`.
- The byte offset on the same line uses the correct `>> 3` (=`/ 8`)
  semantics, demonstrating the intent.
- Symmetric tools (build-disk.sh:78, `set_sector_in_map`) implement
  the corrected formula and produce SAM maps that match real SAMDOS
  disks (per `docs/notes/sam-disk-format.md` §3.1).

A test could be added to `samfile/cat_test.go` that round-trips a
`samfile add` of two files into a fresh image and verifies that the
two files don't share sectors. Currently the test suite has only
`TestCatEnolaGayFileFromETrackerDisk`.

---

## 6. Recommended path forward

### 6.1 Decision matrix

| Option                                         | Effort    | Grounded?     | Recommendation |
|------------------------------------------------|-----------|---------------|----------------|
| (a) Keep hand-rolling, fix remaining bugs      | 0.5 day   | yes           | acceptable short-term |
| (b) Replace hand-roll with samfile (no fixes)  | impossible | n/a          | rejected — samfile lacks BASIC support |
| (c) Hybrid: fix samfile, replace stub/IN, keep hand-roll for samdos2/auto | 1-2 days | yes | **recommended** |
| (d) Upstream all features, replace entire hand-roll with samfile invocations | 3-5 days | yes | recommended **long-term** |

### 6.2 Concrete plan for option (c)

Two upstream samfile patches, each a small PR with
citation-justified diffs:

**PR1: Fix the `:564` SAMMask operator-precedence bug.**

- Change one line at `samfile.go:564` per §5.4.
- Add a test in `samfile/cat_test.go` that:
  1. Creates a blank 819200-byte image.
  2. Calls `AddCodeFile` for two files of size > 510 bytes each.
  3. Verifies both directory entries' SAM maps are
     non-overlapping AND together contain the right bits per
     Tech Man L4407-4413.
- Citation footer for PR description: Tech Man L4407-4413, Tech
  Man L4419-4420 (BAM-as-OR-of-per-file-maps),
  `docs/notes/sam-disk-format.md` §3.1-3.4.

**PR2: Make body-header byte 5-6 canonical (FF FF).**

- Change `samfile.go:585-586` from `0, 0` to `0xFF, 0xFF`.
- Add an assertion in the existing
  `TestCatEnolaGayFileFromETrackerDisk` that the body header bytes
  at offsets 5-6 are FF FF (the ETracker disk should match real
  SAVE).
- Citation: Tech Man L4292-4293, real-world disk inspection
  (`/Users/pmoore/Downloads/GoodSamC2/x.mgt` CHOMPER body bytes
  `10 df 0f d5 9c ff ff 01 00`), ROM SLMVC HDR-init at L22082-22091
  (`HDCLP2 / LD (HL), 0FFH`).

After PR1 + PR2 merge, build-disk.sh changes:

- For slot 2 (`stub`) and slot 3 (`IN`), replace the hand-rolled
  Python with two `samfile add` invocations:
  ```bash
  cp build/stub.bin /tmp/stub
  samfile add -i "$output" -f /tmp/stub -c -l 24576
  cp "$input" /tmp/IN
  samfile add -i "$output" -f /tmp/IN -c -l 24576
  ```
- Keep slot 0 (samdos2) and slot 1 (auto) hand-rolled in Python.

Acceptable byte differences in slots 2-3 vs current hand-roll:
- StartPage byte (dir 0xEC) becomes `00` instead of `01`.
- PageOffset (dir 0xED-0xEE, body bytes 3-4) becomes `00 60`
  instead of `00 A0`.
- Both samfile and ROM-LOAD-via-PDPSR2 round-trip these to the
  same address (24576), so behaviour is unchanged.

### 6.3 Concrete plan for option (d) (long-term)

Add a third upstream samfile PR:

**PR3: BASIC type-16 emission with start-line and prog-length triplets.**

- Add `func (di *DiskImage) AddBASICFile(name string, data []byte,
  startAddress uint32, autoRunLine int) error` to `samfile.go`.
  Caller passes:
  - `data` = tokenised body bytes (no body header).
  - `startAddress` = PROG = 0x5CD5 (typical) or via REL PAGE FORM
    decode of the data's intended load location.
  - `autoRunLine` = -1 for no auto-RUN, ≥0 for auto-RUN at that
    line.
- Internally:
  - Set `fe.Type = FT_SAM_BASIC`.
  - Set `fe.StartAddressPage` and `fe.StartAddressPageOffset` from
    `startAddress` per ROM `HDRNMS` encoding (page = `start>>14`,
    offset = `(start & 0x3FFF) | 0x8000`).
  - Set `fe.SAMBASICStartLine = autoRunLine` if ≥0; set
    `fe.ExecutionAddressDiv16K = 0xFF` otherwise (no auto-RUN).
  - Compute three prog-length page-form triplets from `len(data)`,
    write to `FileTypeInfo[0..8]` (= dir 0xDD-0xE5).
  - Modify `Raw()` (samfile.go:268-310) so that when type==16, the
    `SAMBASICStartLine` is written to dir 0xF3-0xF4 with
    `e[0xf2] = 0` (auto-RUN marker).
- Add a `-b` (BASIC) flag and an `--auto-run-line N` option to the
  CLI in `cmd/samfile/usage.go` and `cmd/samfile/add.go`.
- Citations: Tech Man L4376-4381 (per-type FileTypeInfo), Tech Man
  L3022-3032 (HDR 16-26), ROM E0B4-E0E0 (SAVE writes triplets), ROM
  E08E-E093 (SAVE writes auto-RUN line), ROM E287-E294 (LOAD reads
  auto-RUN marker), `docs/notes/sam-file-header.md` §2.

**PR4: First-sector pinning for boot files.**

The ergonomic shape: a `samfile bootify` sub-command that takes a
samdos2 binary and writes it as the first directory entry with a
chain starting at T4S1. Or: add a `--at-track T --at-sector S` flag
to `samfile add` that overrides the default `freeSectors[0]`
allocation. Either works; the second is simpler.

After PR1+PR2+PR3+PR4, build-disk.sh becomes about 10 lines of shell:

```bash
samfile init -i "$output"                # would also be a new sub-command
samfile add -i "$output" -f reference/samdos/samdos2.bin -c -l 0x8000 \
            --at-track 4 --at-sector 1 --type 19
samfile add -i "$output" -f /tmp/auto.basic -b --auto-run-line 10 -l 0x5CD5
samfile add -i "$output" -f build/stub.bin -c -l 24576
samfile add -i "$output" -f "$input" -c -l 24576
samfile ls -i "$output"
```

This is the desirable end-state. Each PR is small, citation-grounded,
and incrementally useful.

### 6.4 What NOT to do

- Don't add features to samfile that aren't grounded in Tech Man /
  ROM disasm / SAMDOS source citations. Pete's prime directive.
- Don't fix `samfile.go:485-486`'s StartPage / PageOffset encoding
  without checking that existing samfile users (the ETracker test
  fixture in `testdata/`) still round-trip — they may rely on the
  current convention. A safer path is to add a *new* method (e.g.
  `AddCodeFileSpecCompliant`) that uses the ROM-canonical encoding,
  and leave `AddCodeFile` alone for back-compat.
- Don't conflate "BASIC start address = 0x5CD5" with the byte value
  to write. PROG (0x5CD5) is hardcoded by ROM init at `EC2B`-`EC2E`
  (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:24517-24522`)
  but on a 256K vs 512K machine the *physical* layout differs;
  PROG-as-an-address is always 0x5CD5, but PROG-as-a-page-form is
  always `(page=0, offset=0x9CD5)` because PROG is in section A page
  0. Don't try to "calculate" it from anywhere other than the
  hardcoded constant.

---

## 7. Source citations index (for this document)

### Tech Manual v3.0
`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_tech-man_v3-0.txt`
- L2974-3068: HDR/HDL buffer layout (the canonical 80-byte buffer).
- L3022-3032: HDR 16-26 — type-specific contents incl. BASIC triplets.
- L3037-3052: REL PAGE FORM offset convention; auto-run line encoding.
- L4256-4275: Disk geometry.
- L4284-4298: 9-byte file body header.
- L4304-4314: File-type byte values 5/16/17/18/19/20.
- L4316-4329: Length / start-address arithmetic.
- L4338-4400: 256-byte directory entry.
- L4366-4368: "MGT future and past" (Tech Manual error; SAMDOS does use
  bytes 211-219 — see `samdos/src/f.s:462-471`).
- L4388-4395: Directory entry 0xEC-0xF1 mirror of body header.
- L4396-4398: Directory entry 0xF2-0xF4 — exec address / auto-RUN line.
- L4407-4413: Sector address map bit ordering.
- L4419-4420: BAM = OR of per-file SAM maps; not stored.

### ROM v3.0 annotated disassembly
`/Users/pmoore/git/sam-aarch64/docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`
- L1183-1218: BTHK/ALHK/HDN/HDR EQUates.
- L4499-4527: PDPSR2 — REL PAGE FORM decoder used by LOAD CODE EXEC.
- L14770-14786: UNSTLEN — REL PAGE FORM encoder; documents
  `RES 7,H IF 5-BYTE IS A LENGTH`.
- L20453-20471: BOOT token handler at D8CD.
- L20473-20598: BOOTEX — boot-page find, RSAD T4S1, signature check,
  `JP 8009H`.
- L20582-20598: BTNOE/BTCK/BTLY signature compare.
- L22025-22054: HDR/HDL buffer documentation block.
- L22057-22119: SLMVC entry / HDR2 / HDR buffer init (HDCLP/HDCLP2).
- L22136-22141: BASIC auto-RUN line setup at HDR+HDN+6.
- L22163-22180: BASIC SAVE-time computation of three prog-length
  triplets at HDR+16/+19/+22.
- L22210-22227: HDRNMS — encodes a number into a 3-byte page-form
  triple including `SET 7,D`.
- L22247: `LD A,19` on LOAD/SAVE CODE path.
- L22467-22484: LOAD CODE EXEC dispatch via HDLDEX, R1OFFCLBC.
- L24517-24522: PROG / RAMTOPP / RAMTOP boot init.
- L26919: BTWD keyword-table entry "BOOT" at FB94H.

### SAMDOS source
`/Users/pmoore/git/samdos/src/`
- `b.s:14-22`: optional 9-byte body header (type 3) — disabled by default.
- `b.s:27`: `org.adjust = 9` — code starts 9 bytes into body.
- `b.s:206-207`: `defm "BOO"` / `defb "T"+&80` — the BOOT signature
  bytes within samdos2 itself.
- `b.s:288-298`: `uifa` — SAMDOS's own DIFA-form record of itself,
  type 19 (NOT type 3).
- `f.s:462-471`: `svhd` — SAVE 9-byte body header to dir 211-219.
- `c.s:1376-1379`: `gtfle` — LOAD 9-byte cache from dir 211-219.
- `h.s:201-212`: `autnam` — `AUTO*` wildcard template (type 16).
- `h.s:215-237`: `init`/`hauto` — second-stage AUTO-load.

### samfile (Go MGT inspector / writer)
`/Users/pmoore/git/samfile/`
- `samfile.go:21-43`: FileEntry struct.
- `samfile.go:34`: `SAMBASICStartLine` field — readable, not writable.
- `samfile.go:51-58`: FileHeader struct.
- `samfile.go:72-80`: FT_* file type constants.
- `samfile.go:89-95`: `Start()` / `Length()` decoders.
- `samfile.go:240-266`: `FileEntryFrom([0x100]byte)`.
- `samfile.go:268-310`: `FileEntry.Raw()`.
- `samfile.go:347-355`: `Used()` test.
- `samfile.go:357-393`: `Output()` formatter (powers `samfile ls`).
- `samfile.go:395-408`: BASIC-specific accessors (read-only).
- `samfile.go:411-413`: `StartAddress()` decoder.
- `samfile.go:444-452`: 9-byte body-header parser inside `File()`.
- `samfile.go:470-499`: `AddCodeFile` — only public file-add path.
- `samfile.go:501-509`: `CreateHeader` — synthesises body header from
  dir entry; bytes 5-6 hardcoded zero.
- `samfile.go:511-552`: `addFile` — top-level write path.
- `samfile.go:554-560`: `WriteFileEntry`.
- `samfile.go:562-565`: `SAMMask` — **the `:564` bug.**
- `samfile.go:567-569`: `Sector.Offset` — track/sector → image byte offset.
- `samfile.go:578-590`: `FileHeader.Raw` — bytes 5-6 hardcoded `0, 0`.
- `cmd/samfile/main.go:28-41`: command dispatch.
- `cmd/samfile/usage.go:6-15`: docopt CLI grammar — only `add` for CODE.
- `cmd/samfile/add.go:12-51`: `add` implementation; calls `AddCodeFile`.
- Upstream HEAD: `e64f5d5` ("Execution Address Page off by one"),
  remote `git@github.com:petemoore/samfile.git`.

### build-disk.sh
`/Users/pmoore/git/sam-aarch64/tools/build-disk.sh`
- L60-323: the hand-roll Python block.
- L70-71: `sector_offset` (matches samfile.go:567-569).
- L73-74: `sector_bit` (matches samfile.go:563 minus the `:564` bug).
- L76-78: `set_sector_in_map` (the bug-free SAMMask).
- L80-113: `write_directory_entry` (the central directory writer).
- L115-131: `write_file_chain` (sector chain + link bytes 510-511).
- L138-176: slot 0 (samdos2).
- L178-248: slot 1 (auto BASIC).
- L250-297: slot 2 (stub).
- L299-323: slot 3 (IN).

### Real-world reference disks
- `/Users/pmoore/Downloads/GoodSamC2/x.mgt` CHOMPER (type 16) —
  body bytes verified `10 df 0f d5 9c ff ff 01 00`; dir 0xDD-0xDF
  = `00 05 92`. Authoritative example of real BASIC SAVE bytes.
- `/Users/pmoore/git/samdos/res/samdos2.reference.bin` — 10000 bytes,
  no embedded body header; BOOT signature at body offset 0xF7 =
  `42 4F 4F D4`.
- `/Users/pmoore/git/sam-aarch64/build/test.mgt` (mtime 2026-05-10) —
  current build output, slots 0-3 verified in this analysis.

---

## 8. Open / unverified

- The high bit on byte 2 of the BASIC prog-length triplet (build-disk.sh's
  `00 80 ...` style vs an unset-bit-15 alternative) is consistent with
  the `UNSTLEN`-then-`SET 7,H` convention used for addresses. ROM
  `E0B4`-`E0E0` does NOT explicitly call `SET 7,H` after computing the
  triplet via `SUBAHLCDE` — which suggests the high bit may not be
  set by ROM SAVE for prog-length triplets. The CHOMPER reference
  disk has triplet `00 05 92`, where 0x92 = 0b10010010 has bit 7 set,
  so on real SAVE the bit IS set. **Hypothesis**: `SUBAHLCDE` (ROM
  L1FE7) preserves bit 15 from the input AHL, and the input
  (NVARS / NUMEND / SAVARS sysvars) is already in 8000H form because
  ROM maintains those sysvars in REL PAGE FORM with bit 15 set.
  *This is a plausible explanation but not directly verified by
  reading SUBAHLCDE.* For our purposes — build-disk.sh writes the
  bit set, matching CHOMPER — this is fine.
- There is no public SAM/SAMDOS spec for the cylinder-interleaved
  on-disk file layout; it's a SimCoupé/samfile/samdisk de-facto
  convention. Trust it; everyone in the SAM emulator ecosystem uses
  it. (`docs/notes/sam-disk-format.md` §1.2 covers this.)
