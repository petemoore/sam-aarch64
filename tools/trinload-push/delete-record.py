#!/usr/bin/env python3
"""Push the "free/delete Trinity SD record N" program to the SAM and run it (i317).

The store/boot/DELETE toolkit counterpart to sd-push.py (i293, push a disk into a
record) and boot-record.py (i316, boot a record): this launcher patches the record
number into build/delete_record.bin's DEL_CONFIG block and then pushes the program over
the wire via TrinLoad (the ?/@/X protocol in trinpush.py) for trinload to load + run at
&8000. The program clears that record's central record-LIST name entry so the slot reads
as free/reusable — letting the autonomous loop build a disk, push it into a record, boot
it, then FREE it again and re-push cleanly, all without a human at the SAM and without
exhausting records.

The record number is delivered by PATCHING the binary before the push (not a network
message): the program has no wire loop — trinload's X packet lands it at &8000, it reads
the patched byte from RAM, inits the card, range-checks, and frees the record. This
mirrors boot-record.py, which patches boot_record's BOOT_CONFIG the same way.

DEL_CONFIG byte layout (src/netboot/delete_record.asm; the launcher patches by file
offset from the DEL_CONFIG map symbol):
  +0  DEL_CONFIG      1 byte  magic/version = 0x5A; a sanity-check the block was found.
  +1  DEL_CFG_RECORD  1 byte  the record number to free (1..255). Patched in.

Usage:
    delete-record.py <sam-ip> <record> [--bin PATH] [--map PATH] [--page N]

<record> is the 1-based Trinity SD record to free, 1..255. Record 0 (the floppy) has no
list entry and is rejected — the program also refuses it, and any record beyond the
card's record count, exiting without writing (a data-safety guard).

WARNING (CLAUDE.md §5; trinity_storage_shared_resource): the Trinity SD card is a SHARED
user resource. This FREES whatever in-range record you name — its disk becomes
re-pushable and its catalogue name is cleared. Make sure the record is one you own / may
reuse. The on-hardware free is emulation-verified only (delete_record_test.go asserts the
list-entry clear + neighbour-safety under the SD model); the real-Trinity free is a
separate, Pete-gated hardware shot (i295 family).
"""
import argparse
import sys

from trinpush import LOAD_ORG, config_offset, parse_map, push_and_run

DEL_CFG_MAGIC = 0x5A  # DEL_CFG_MAGIC_VAL in src/netboot/delete_record.asm


def patch_record(data, off, record):
    """Return a copy of `data` with DEL_CFG_RECORD patched at file offset `off`+1.

    Sanity-checks the magic byte at `off`+0 (DEL_CONFIG), then writes the 1-byte record
    number at `off`+1. The magic is never written.
    """
    if off < 0 or off + 2 > len(data):
        raise ValueError(f"config offset {off:#x} outside binary ({len(data)} B)")
    if data[off] != DEL_CFG_MAGIC:
        raise ValueError(
            f"config magic at {off:#x} is {data[off]:#x}, want {DEL_CFG_MAGIC:#x} "
            "(wrong binary or wrong symbol?)")
    out = bytearray(data)
    out[off + 1] = record & 0xFF
    return bytes(out)


def main(argv):
    ap = argparse.ArgumentParser(description="push the free-a-record program and run it")
    ap.add_argument("sam", help="the SAM's IP address (TrinLoad must be running)")
    ap.add_argument("record", type=lambda s: int(s, 0),
                    help="the 1-based record number to free, 1..255")
    ap.add_argument("--bin", default="build/delete_record.bin",
                    help="the program to push (default build/delete_record.bin)")
    ap.add_argument("--map", dest="mapfile", default=None,
                    help="the pyz80 mapfile (default: the --bin path with a .map suffix)")
    ap.add_argument("--page", type=int, default=1, help="TrinLoad target page (default 1)")
    args = ap.parse_args(argv)

    if not 1 <= args.record <= 255:
        ap.error(f"record {args.record} out of range 1..255 (0 = floppy has no list entry)")

    mapfile = args.mapfile or args.bin.rsplit(".", 1)[0] + ".map"
    data = open(args.bin, "rb").read()
    syms = parse_map(open(mapfile).read())
    off = config_offset(syms, symbol="DEL_CONFIG")
    data = patch_record(data, off, args.record)

    print(f"patched DEL_CONFIG @ offset {off:#x}: record={args.record}")
    print(f"pushing {args.bin} ({len(data)} B) -> {args.sam} page={args.page} addr=0x{LOAD_ORG:04X}")
    if not push_and_run(args.sam, data, args.page, LOAD_ORG):
        return 1
    print(f"done — record {args.record} should now be freed (re-pushable)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
