#!/usr/bin/env python3
"""Push the "boot Trinity SD record N" program to the SAM and run it (i316).

The non-interactive network-driven counterpart to the i264 hold-key picker: this
launcher patches the record number into build/boot_record.bin's BOOT_CONFIG block
and then pushes the program over the wire via TrinLoad (the ?/@/X protocol in
trinpush.py) for trinload to load + run at &8000. The program HRECORD-selects that
record and fires ALHK to load + run its AUTO file — so the autonomous loop can build
a disk, push it into a record (sd-push.py, i293), then BOOT it (this launcher) and
observe, all without a human at the SAM.

The record number is delivered by PATCHING the binary before the push (not a network
message): the program has no wire loop — trinload's X packet lands it at &8000, it
reads the patched byte from RAM, and boots. This mirrors trinpush-serve.py, which
patches netboot_serve's SERVE_CONFIG block the same way.

BOOT_CONFIG byte layout (src/netboot/boot_record.asm; the launcher patches by file
offset from the BOOT_CONFIG map symbol):
  +0  BOOT_CONFIG      1 byte  magic/version = 0x5A; a sanity-check the block was found.
  +1  BOOT_CFG_RECORD  1 byte  the record number to boot (0 = floppy). Patched in.

Usage:
    boot-record.py <sam-ip> <record> [--bin PATH] [--map PATH] [--page N]

<record> is the 1-based Trinity SD record to boot (0 = floppy), 0..255.

WARNING (CLAUDE.md §5): the actual on-hardware auto-load + boot routes through the
B-DOS loader + the Trinity SD driver and is emulation-verified only (boot_record_test.go
asserts the HRECORD select + ALHK fire under the harness); the real-Trinity boot is a
separate hardware shot. This launcher will HRECORD-select + boot whatever record you
name — make sure it holds a bootable disk.
"""
import argparse
import sys

from trinpush import LOAD_ORG, config_offset, parse_map, push_and_run

BOOT_CFG_MAGIC = 0x5A  # BOOT_CFG_MAGIC_VAL in src/netboot/boot_record.asm


def patch_record(data, off, record):
    """Return a copy of `data` with BOOT_CFG_RECORD patched at file offset `off`+1.

    Sanity-checks the magic byte at `off`+0 (BOOT_CONFIG), then writes the 1-byte
    record number at `off`+1. The magic is never written.
    """
    if off < 0 or off + 2 > len(data):
        raise ValueError(f"config offset {off:#x} outside binary ({len(data)} B)")
    if data[off] != BOOT_CFG_MAGIC:
        raise ValueError(
            f"config magic at {off:#x} is {data[off]:#x}, want {BOOT_CFG_MAGIC:#x} "
            "(wrong binary or wrong symbol?)")
    out = bytearray(data)
    out[off + 1] = record & 0xFF
    return bytes(out)


def main(argv):
    ap = argparse.ArgumentParser(description="push the boot-a-record program and run it")
    ap.add_argument("sam", help="the SAM's IP address (TrinLoad must be running)")
    ap.add_argument("record", type=lambda s: int(s, 0),
                    help="the record number to boot (0 = floppy), 0..255")
    ap.add_argument("--bin", default="build/boot_record.bin",
                    help="the program to push (default build/boot_record.bin)")
    ap.add_argument("--map", dest="mapfile", default=None,
                    help="the pyz80 mapfile (default: the --bin path with a .map suffix)")
    ap.add_argument("--page", type=int, default=1, help="TrinLoad target page (default 1)")
    args = ap.parse_args(argv)

    if not 0 <= args.record <= 255:
        ap.error(f"record {args.record} out of range 0..255")

    mapfile = args.mapfile or args.bin.rsplit(".", 1)[0] + ".map"
    data = open(args.bin, "rb").read()
    syms = parse_map(open(mapfile).read())
    off = config_offset(syms, symbol="BOOT_CONFIG")
    data = patch_record(data, off, args.record)

    print(f"patched BOOT_CONFIG @ offset {off:#x}: record={args.record}")
    print(f"pushing {args.bin} ({len(data)} B) -> {args.sam} page={args.page} addr=0x{LOAD_ORG:04X}")
    if not push_and_run(args.sam, data, args.page, LOAD_ORG):
        return 1
    rec = "the floppy" if args.record == 0 else f"record {args.record}"
    print(f"done — the SAM should now be booting {rec}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
