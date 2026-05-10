# Byte-by-byte layout of `build/test.mgt`

Every non-zero byte in the M0 round-trip disk image, with citations for
each encoding decision. Generated from a known-good build of
`build/test.mgt` (commit ${TBD}). Regenerate by running
`./tools/build-disk.sh tests/fixtures/nop.s build/test.mgt` and re-running
the dump script in `docs/notes/test-mgt-byte-layout.md` (this doc).

## File-level structure

The image is **819200 bytes** = 80 cylinders × 2 sides × 10 sectors × 512
bytes (Tech Manual v3.0 §disk format; samfile `Sector.Offset` at
`samfile.go:604-607`).

Cylinder-interleaved layout: `sector_offset(track, sector) =
((track>>7)*5120) + ((sector-1)*512) + ((track&0x7f)*10240)`. Bit 7 of the
track byte selects side. (`tools/build-disk.sh:70-71`.)

Track 0 sectors 1-4 (bytes 0x000-0x7FF) hold the **directory**: 80 file
entries × 256 bytes, two entries per sector. Slot N's entry starts at
`T0S{N//2 + 1}` byte offset `(N % 2) * 256`.

Files live on later tracks. The `SectorAddressMap` bitmap at dir bytes
0x0F-0xD1 is authoritative for sector ownership; bytes 0x0D-0x0E give
the FIRST sector and the 510-byte sector body chains via the last 2
bytes of each sector (`tools/build-disk.sh:143-159`, samfile
`AddCodeFile`).

Our disk uses 4 slots (0-3): `samdos2`, `auto`, `stub`, `IN`. Slots
4-79 are zero (unused).

## Directory layout reference

Each 256-byte slot, decoded per `samfile.go:240-266`:

| Range       | Field                       | Notes                                                          |
|-------------|-----------------------------|----------------------------------------------------------------|
| `0x00`      | Type                        | 0=erased, 0x10=BASIC, 0x13=Code, 0x20+=hidden, 0x40+=protected |
| `0x01-0x0A` | Name (10 chars, space-pad)  | ASCII                                                          |
| `0x0B-0x0C` | Sectors (BIG-endian!)       | `samfile.go:243`                                               |
| `0x0D-0x0E` | First sector (track, sec)   |                                                                |
| `0x0F-0xD1` | SectorAddressMap (195B)     | bitmap; bit `(t-1)*10 + (s-1) - 40` for T≥4                    |
| `0xD2`      | (unused)                    |                                                                |
| `0xD3-0xDB` | Body-header cache (9B)      | mirrors the 9-byte file body header. SAMDOS reads here per `samdos/src/c.s:1376` (`rptl=211`). Real ROM SAVE writes both. |
| `0xDC`      | MGTFlags                    | Real-SAVE writes `0x20`. Empirically required for M0 boot.     |
| `0xDD-0xDF` | NVARS-PROG triplet (BASIC)  | page-form 8000H. See `sam-basic-save-format.md`.               |
| `0xE0-0xE2` | NUMEND-PROG triplet (BASIC) | "                                                               |
| `0xE3-0xE5` | SAVARS-PROG triplet (BASIC) | "                                                               |
| `0xE6-0xEB` | (unused for our files)      |                                                                |
| `0xEC`      | StartAddressPage            | bottom 5 bits = real page; lower bit -1 from physical page     |
| `0xED-0xEE` | StartAddressPageOffset (LE) | `(addr & 0x3FFF) | 0x8000`                                     |
| `0xEF`      | Pages count                 |                                                                |
| `0xF0-0xF1` | LengthMod16K (LE)           |                                                                |
| `0xF2`      | ExecAddrDiv16K / 0xFF=none  | ROM E281 checks; `rom-disasm:22471`                            |
| `0xF3-0xF4` | ExecAddrMod16K LE / line#   | doubles as SAMBASIC start line                                 |
| `0xF5-0xFF` | reserved                    |                                                                |

## 9-byte body header (prepended to every file body)

| Byte | Field                  |
|------|------------------------|
| 0    | Type (mirrors dir 0x00)|
| 1-2  | LengthMod16K LE        |
| 3-4  | PageOffset LE (load address, with bit 15 set)|
| 5-6  | LOADED auto-exec marker. `FF FF` = no auto-exec, otherwise `addr_div_16k, addr_mod_16k_LE` |
| 7    | Pages                  |
| 8    | StartPage              |

The auto-exec gate is ROM E281-E299 (`rom-disasm:22471-22484`). Both the
**dir-entry** byte 0xF2 (= REQUESTED, populated from HDR sysvars) AND
the **body-header** byte 5 (= LOADED, populated into HDL+HDN+6) must be
0xFF for `LOAD CODE` to return cleanly to BASIC instead of jumping into
the encoded address.

## Slot 0: `samdos2` (T0S1, dir bytes 0x000-0x0FF)

```
0x00 13                              type 0x13 = Code (samdos/src/b.s:14-22 internally uses
                                     type 3 for itself; we use 0x13 to keep samfile from
                                     hiding the slot — ROM BOOT bypasses the directory entry
                                     entirely so the type byte is irrelevant for boot)
0x01-0x0A 73 61 6d 64 6f 73 32 20 20 20    "samdos2   " (build-disk.sh:198)
0x0B-0x0C 00 14                      sector count BE = 0x0014 = 20 sectors
0x0D-0x0E 04 01                      first sector = T4S1 (build-disk.sh:177)
0x0F-0xD1 ff ff 0f 00 00 ... 00      sector bitmap: 20 bits set for T4S1..T5S10. Bit
                                     `(4-1)*10+(1-1)-40+0` = bit 0 = bit 0 of byte 0 → 0x01.
                                     For T4S1..T4S10 (bits 0..9) → bytes [0xff, 0x03]. For
                                     T5S1..T5S10 (bits 10..19) → bytes [0xff, 0x0F] (rest of
                                     bit 10..15 in byte 1, plus bit 16..19 in byte 2 = 0x0F).
                                     Net: 0xff, 0xff, 0x0f. (set_sector_in_map at
                                     build-disk.sh:73-78.)
0xD2 00                              unused
0xD3-0xDB 13 10 27 09 80 ff ff 00 7d body-header cache (build-disk.sh:196):
                                       byte 0: 0x13 type
                                       bytes 1-2: 0x2710 = 10000 length LE
                                       bytes 3-4: 0x8009 PageOffset (load to 0x8000, JP &8009)
                                       bytes 5-6: ff ff (no auto-exec from BODY perspective —
                                                 ROM BOOT loads samdos2 raw and bypasses
                                                 this check anyway)
                                       byte 7: 00 Pages (10000 < 16384)
                                       byte 8: 0x7d StartPage with decorative bits 6,5,4,3,2,0
                                              set; samfile masks to & 0x1f = 29 → page 30
                                              after the +1 offset. Hence Start = 491529.
                                              The decorative bits are taken verbatim from
                                              FRED/Defender real-SAVE convention
                                              (build-disk.sh:189-195).
0xDC 00                              MGTFlags — we leave at 0 for samdos2
0xE6-0xEB 00 00 00 00 00 00          unused
0xEC-0xEE 7d 09 80                   StartAddressPage/Offset mirror of body-header bytes 8/3/4
                                     (write_directory_entry at build-disk.sh:117-124)
0xEF 00                              Pages
0xF0-0xF1 10 27                      LengthMod16K LE = 10000
0xF2-0xF4 ff ff ff                   no exec-address (build-disk.sh:202 default)
0xF5-0xFF 00 ...                     reserved
```

samdos2 body lives at T4S1..T5S10 (offsets 0xA000..0xB3FF). First
sector starts at 0xA000 with the 9-byte header `13 10 27 09 80 ff ff 00
7d` followed by the SAMDOS2 binary verbatim from
`reference/samdos/samdos2.bin`.

## Slot 1: `auto` BASIC (T0S1 second half, dir bytes 0x100-0x1FF)

```
0x100 10                             type 0x10 = SAM BASIC
0x101-0x10A 61 75 74 6f 20 20 20 20 20 20  "auto      "
0x10B-0x10C 00 02                    2 sectors (T6S1+T6S2)
0x10D-0x10E 06 01                    first sector T6S1
0x111 30                             sector bitmap byte 2: bits 4 (T6S1) + 5 (T6S2) set
                                     = 0x10 + 0x20 = 0x30. (T6S1 bit = (6*10)+0-40 = 20,
                                     byte 2 bit 4. T6S2 = 21, byte 2 bit 5.)
0xD3-0xDB 10 90 02 d5 9c ff ff 00 00 body-header cache:
                                       byte 0: 0x10 BASIC
                                       bytes 1-2: 0x0290 = 656 length LE
                                       bytes 3-4: 0x9CD5 (= 0x8000 | 0x1CD5) PageOffset for
                                                  PROG = 0x5CD5 (sam-basic-save-format.md
                                                  references PROG as fixed sysvar)
                                       bytes 5-6: ff ff — no auto-exec from body header
                                                  (BASIC files use start_line in dir, not body)
                                       byte 7: 00 Pages
                                       byte 8: 00 StartPage (PROG is in section C, page 0
                                                after samfile +1 mapping)
0xDC 20                              MGTFlags = 0x20 — required for M0 boot. Defender,
                                     pete-made.mgt, and 50%+ of canonical disks set this.
                                     Semantics not fully documented but clearly load-bearing.
0xDD-0xDF 00 34 80                   NVARS-PROG triplet, page-form 8000H = 52 (= program
                                     length: 4-byte line header + 47-byte body + 1-byte
                                     0xFF sentinel = 52). build-disk.sh page_form_3byte().
0xE0-0xE2 00 90 80                   NUMEND-PROG = 144 = 52 + 92 (vars area follows PROG,
                                     92 bytes per ROM CLRSR, sam-basic-save-format.md §92)
0xE3-0xE5 00 90 82                   SAVARS-PROG = 656 = 144 + 512 (gap after vars area,
                                     512 bytes per ROM MAKEROOM, sam-basic-save-format.md §512)
0xEC 00                              StartAddressPage = 0
0xED-0xEE d5 9c                      StartAddressPageOffset = 0x9CD5 (mirrors body header)
0xEF 00                              Pages
0xF0-0xF1 90 02                      LengthMod16K LE = 656
0xF2 00                              EXEC marker = 0 → BASIC start_line follows
0xF3-0xF4 0a 00                      start_line = 10 (start_line=10 at build-disk.sh)
0xF5-0xFF 00 ...                     reserved
```

### auto file body (T6S1 @ 0xF000, T6S2 @ 0xF200)

```
0xF000 + 0    body header (9 bytes): 10 90 02 d5 9c ff ff 00 00
0xF000 + 9    PROG section (52 bytes):
              09: 00 0a               line number BE = 10
              11: 2f 00               line body length LE = 47
              13: b3                  CLEAR token (Spectrum/SAM BASIC keyword 0xB3)
              14: 33 32 37 36 37      "32767" ASCII (display form)
              19: 0e 00 00 ff 7f 00   binary numeric form: prefix 0x0e, sign byte 0,
                                       16-bit LE value 32767 = 0x7FFF, padding 0
                                       (build-disk.sh num())
              25: 3a                  ":"
              26: 95                  LOAD token (0x95)
              27: 22                  '"'
              28: 73 74 75 62         "stub"
              32: 22                  '"'
              33: ff 6c               CODE token (two-byte 0xFF 0x6C — SAM BASIC extends
                                       Spectrum's single-byte tokens with 0xFFxx for extras)
              35: 33 32 37 36 38      "32768"
              40: 0e 00 00 00 80 00   binary 32768 = 0x8000
              46: 3a                  ":"
              47: e4                  CALL token (0xE4)
              48: 33 32 37 36 38      "32768"
              53: 0e 00 00 00 80 00   binary 32768
              59: 0d                  end-of-line
              60: ff                  end-of-program sentinel
0xF000 + 61   vars area (92 bytes, all 0x00). Per sam-basic-save-format.md, canonical
              SAM SAVE writes a CLRSR-pattern here (46 bytes 0xFF + PSVTAB + 2× PSVT2),
              but ROM CLEAR re-initialises this region on AUTO-RUN, so 0x00 fill is OK.
              Empirically verified: M0 disk passes with all-zeros here.
0xF000 + 153  gap (512 bytes, all 0x00). MAKEROOM-style buffer; not touched by CLEAR.
0xF1FE-0xF1FF 06 02     last 2 bytes of T6S1 = next-sector pointer T6S2
              (write_file_chain at build-disk.sh:143-159)
0xF200        T6S2 = continuation of body's trailer (all-zero gap continues)
0xF3FE-0xF3FF 00 00     next-sector pointer (0,0) = end-of-chain
```

## Slot 2: `stub` Code (T0S2 first half, dir bytes 0x200-0x2FF)

```
0x200 13                             type Code
0x201-0x20A "stub      "
0x20B-0x20C 00 01                    1 sector
0x20D-0x20E 06 03                    first sector T6S3
0x211 40                             sector bitmap byte 2 bit 6 (T6S3)
0xD3-0xDB 13 06 00 00 80 ff ff 00 01 body-header cache:
                                       byte 0: 0x13
                                       bytes 1-2: 0x0006 length
                                       bytes 3-4: 0x8000 LOAD_ADDR (32768)
                                       bytes 5-6: ff ff — NO AUTO-EXEC. The fix discovered
                                                  on 2026-05-10: 0x00 0x00 here causes ROM
                                                  E28B HDLDEX-path to take the false branch
                                                  and JP to addr 0x0100, garbage execution.
                                                  Verified empirically against Defender's
                                                  DEFENDER body header which also has FF FF.
                                       byte 7: 00 Pages (6 < 16384)
                                       byte 8: 0x01 StartPage (= (0x8000>>14)-1; samfile
                                                +1 = 2 → mid-page; page-byte canonical
                                                value with decorative bits is 0x61 per
                                                Defender, but 0x01 works — only the bottom
                                                5 bits are functionally significant)
0xDC 00                              MGTFlags
0xEC 01                              StartPage mirror
0xED-0xEE 00 80                      PageOffset mirror = 0x8000
0xEF 00                              Pages
0xF0-0xF1 06 00                      LengthMod16K = 6
0xF2-0xF4 ff ff ff                   no auto-exec from dir entry
```

### stub body (T6S3 @ 0xF400)

```
0xF400 + 0    body header: 13 06 00 00 80 ff ff 00 01
0xF400 + 9    stub binary (6 bytes from build/stub.bin):
              9:  3e 04             LD A, 4
              11: d3 fe             OUT (0xFE), A    ; set border colour register
              13: f3                DI
              14: 76                HALT             ; -exitonhalt patch detects DI;HALT and exits 0
0xF5FE-0xF5FF 00 00     end-of-chain
```

## Slot 3: `IN` Code (T0S2 second half, dir bytes 0x300-0x3FF)

```
0x300 13                             type Code
0x301-0x30A "IN        "
0x30B-0x30C 00 01                    1 sector
0x30D-0x30E 06 04                    first sector T6S4
0x311 80                             sector bitmap byte 2 bit 7 (T6S4)
0xD3-0xDB 13 0c 00 00 80 ff ff 00 01 body-header cache (parallels stub, length = 12)
0xEC-0xEE 01 00 80                   StartPage/Offset mirror
0xF0-0xF1 0c 00                      LengthMod16K = 12
0xF2-0xF4 ff ff ff                   no auto-exec
```

### IN body (T6S4 @ 0xF600)

```
0xF600 + 0    body header: 13 0c 00 00 80 ff ff 00 01
0xF600 + 9    IN content = `tests/fixtures/nop.s` verbatim, 12 bytes:
              "        nop\n"        (8 spaces + "nop" + LF)
0xF7FE-0xF7FF 00 00     end-of-chain
```

The stub doesn't read IN in the current 6-byte minimal version. IN is
present so the dir/body machinery is in place for the upcoming
M0/M1 stub that actually assembles `IN` to `OUT`.

## Verifying this doc is current

```bash
# After modifying build-disk.sh, regenerate test.mgt and re-run a dump:
./tools/build-disk.sh tests/fixtures/nop.s build/test.mgt
xxd build/test.mgt | less

# Spot-check that the directory bytes match what's documented above.
```

Each cited `build-disk.sh` line refers to the current commit. Update
this doc if `build-disk.sh` changes.
