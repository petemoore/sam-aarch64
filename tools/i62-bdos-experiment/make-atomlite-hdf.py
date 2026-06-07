#!/usr/bin/env python3
"""make-atomlite-hdf.py — build a B-DOS-formatted Atom Lite HDF image.

Produces an HDF v1.1 hard-disk image that (a) SimCoupé attaches via its
Atom Lite path and classifies as a non-byte-swapped B-DOS disk, and
(b) B-DOS AL 1.5a accepts records on (RECORD n passes the BDOS-ID
check).  Everything below is derived from primary sources:

HDF container (SimCoupé Base/HardDisk.cpp, struct RS_IDE +
HDFHardDisk::Create at the CI-pinned SHA 0e8a69f):
  - 22-byte header: "RS-IDE", 0x1A, revision 0x11, flags 0x00 (no
    halved-sector data, not ATAPI), little-endian 16-bit data offset,
    11 reserved zero bytes.
  - 512 bytes of ATA IDENTIFY data (HDF v1.1 stores the full block;
    HDFHardDisk::Open reads it and ATADevice::SetIdentifyData takes
    CHS straight from words 1/3/6 — so the geometry we write here is
    exactly the geometry both SimCoupé and B-DOS will use).
  - raw 512-byte sectors.

IDENTIFY block (ATADevice::SetIdentifyData(nullptr), same file):
  word 0   = 0x848A  (CFA feature set, what SimCoupé advertises)
  word 1/3/6 = cylinders/heads/sectors
  words 10-19/23-26/27-46 = serial/firmware/model (byte-swapped ASCII)
  word 47  = 1       (read/write multiple: 1 sector)
  word 49  = 1<<9    (LBA supported)
  word 53  = 1       (words 54-58 valid)
  words 54-56 = current C/H/S,  words 57-58 = current capacity
  words 60-61 = LBA28 total sectors
  words 83/84/86/87 |= CFA bits (0x4004/0x4000/0x0004/0x4000)

B-DOS record layout (B-DOS 1.5a source, file "BDOS15a .S" on the
worldofsam Bdos15a.zip source disk; routine `hd.init`/`sel.rcd`):
  total      = cylinders * heads * sectors
  base       = floor((total/1600 + 32) / 32) + 1
               (record-list sectors + 1 boot sector; identical to
               SimCoupé's IsBDOSDisk uBase = 1 + total/1600/32 + 1)
  record n   starts at absolute sector  base + (n-1)*1600
  records    = floor((total-base)/1600), +1 if the remainder covers
               at least 5 tracks of 10 sectors (partial last record)
  Each record is laid out like a SAM floppy: 80 tracks x 2 sides x
  10 sectors; its first sector is the first directory sector.

BDOS ID stamp (B-DOS 1.7n manual "Note.2" + 1.5a source `get.label`,
`exp.rcd`: "JR C,rep81 — invalid record if no bdos id"):
  bytes 232-235 of a record's first directory sector = "BDOS".
  Record-name fields (210-219 first 10 chars, 250-255 last 6) start
  with 0x00 = unnamed.  FORMAT zero-fills all 1600 sectors, so a
  zeroed record + stamp is exactly what B-DOS's own formatter leaves.

SimCoupé's IsBDOSDisk reads sector `uBase` and requires "BDOS" at
offset 232 non-byte-swapped for the Atom Lite classification — the
same stamp.

Default geometry: 16 cylinders x 16 heads x 63 sectors = 16128
sectors (~7.9 MB), giving base=2 and 10 full records + 1 partial.
"""

import argparse
import struct
import sys

SECTOR = 512
RECORD_SECTORS = 1600  # 80 tracks * 2 sides * 10 sectors


def swapped_ascii(s: str, nbytes: int) -> bytes:
    """ATA string convention: space-padded, bytes swapped per word."""
    b = s.encode("ascii").ljust(nbytes, b" ")[:nbytes]
    out = bytearray(b)
    for i in range(0, nbytes, 2):
        out[i], out[i + 1] = out[i + 1], out[i]
    return bytes(out)


def identify_block(cyls: int, heads: int, sectors: int) -> bytes:
    total = cyls * heads * sectors
    w = [0] * 256
    w[0] = 0x848A
    w[1], w[3], w[6] = cyls, heads, sectors
    serial = swapped_ascii("i62", 20)
    firmware = swapped_ascii("20260611", 8)
    model = swapped_ascii("I62 BDOS EXPERIMENT", 40)
    w[47] = 1
    w[49] = 1 << 9
    w[53] = 1
    w[54], w[55], w[56] = cyls, heads, sectors
    w[57], w[58] = total & 0xFFFF, (total >> 16) & 0xFFFF
    w[60], w[61] = total & 0xFFFF, (total >> 16) & 0xFFFF
    w[83] |= (1 << 2) | (1 << 14)
    w[84] |= 1 << 14
    w[86] |= 1 << 2
    w[87] |= 1 << 14
    blob = bytearray(struct.pack("<256H", *w))
    blob[20:40] = serial
    blob[46:54] = firmware
    blob[54:94] = model
    return bytes(blob)


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("output", help="output .hdf path")
    p.add_argument("--cyls", type=int, default=16)
    p.add_argument("--heads", type=int, default=16)
    p.add_argument("--sectors", type=int, default=63)
    args = p.parse_args()

    total = args.cyls * args.heads * args.sectors
    records_t = total // RECORD_SECTORS
    base = (records_t + 32) // 32 + 1
    usable = total - base
    records = usable // RECORD_SECTORS
    leftover_tracks = (usable % RECORD_SECTORS) // 10
    if leftover_tracks >= 5:
        records += 1  # B-DOS counts a >=5-track remainder as a partial record

    header = struct.pack(
        "<6sBBBH11x", b"RS-IDE", 0x1A, 0x11, 0x00, 22 + SECTOR
    )
    assert len(header) == 22

    data = bytearray(total * SECTOR)
    stamped = 0
    for n in range(1, records + 1):
        first = (base + (n - 1) * RECORD_SECTORS) * SECTOR
        data[first + 232 : first + 236] = b"BDOS"
        stamped += 1

    with open(args.output, "wb") as f:
        f.write(header)
        f.write(identify_block(args.cyls, args.heads, args.sectors))
        f.write(data)

    print(
        f"make-atomlite-hdf: {args.output}: CHS {args.cyls}/{args.heads}/"
        f"{args.sectors} = {total} sectors, base={base}, "
        f"{records} records stamped ({stamped})"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
