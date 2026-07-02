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
    boot-record.py <sam-ip> <record> [--image MGT] [--force] [--bin PATH] [--map PATH] [--page N]

<record> is the 1-based Trinity SD record to boot (0 = floppy), 0..255.

BOOTABILITY GUARD (i334): pass --image <the .mgt you pushed to the record> and the
launcher parses its directory for the AUTO* file B-DOS's ALHK will run, refusing
(exit 1; --force overrides) the two known will-not-boot shapes:
  - the AUTO* image's load range overlaps &7FE0-&7FFF, where B-DOS's record-boot
    continuation runs its working stack — sector deposits interleave with live
    stack frames and the boot wanders deaf instead of exec'ing (the i331 finding;
    a StartPage-0 image larger than &1FE0 bytes, e.g. trinload's 11 KB at &6000).
    Images loading at &8000+ (the i332 vessel class) are unaffected.
  - the AUTO* file is BASIC: a record boot runs the AUTO* file directly and never
    fires a BASIC RUN leg, so a BASIC-auto record NOP-slides into a silent
    livelock (the i332 finding, observed on real hardware).
Without --image the record's content is unknown to the launcher (list_records
serves the card's record LIST, not record bodies), so it prints a one-line
pointer to the check instead.

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

# The record-boot continuation's working stack (i331): SP descends from &8000
# through the ALHK hook dispatch + ROM1 load-continuation while the AUTO image's
# sectors are being deposited.
BOOT_STACK_LO = 0x7FE0
BOOT_STACK_HI = 0x8000

FILETYPE_BASIC = 0x10
FILETYPE_CODE = 0x13


def _matches_auto(raw_name):
    """B-DOS HAUTO's "AUTO*" prefix match on the first 4 RAW name bytes.

    Faithful to B-DOS 1.5a L18929/match.ix (SAMDOS cknam identical):
    `XOR (HL) : AND %11011111` per byte — the case bit (0x20) is ignored,
    every other bit INCLUDING bit 7 is significant, so lowercase "auto"
    matches but a bit-7-set (inverse/flashing) name byte does not.
    """
    return all((b ^ p) & 0xDF == 0 for b, p in zip(raw_name[:4], b"AUTO"))


def parse_mgt_auto_entry(img):
    """Find the AUTO* directory entry B-DOS's ALHK will boot in a .mgt image.

    Faithful to the B-DOS HAUTO scan: the directory is tracks 0-3 SIDE 0 —
    in the side-interleaved .mgt layout, side-0 track t starts at image
    offset t*10240 — each track 10 × 512-byte sectors of 2 × 256-byte
    entries (20 entries per track, 80 total), and the scan TERMINATES at
    the first never-used slot (type byte 0 and first name byte 0, B-DOS
    1.5a L18915) — an entry past such a hole is invisible to B-DOS too.
    Returns (name, filetype, load, length) for the first entry whose raw
    name matches "AUTO*" per _matches_auto — load and length decoded from
    the SAM dir-entry fields (StartAddressPage at +0xEC, page offset
    &8000-form at +0xED-EE, full pages at +0xEF, LengthMod16K at +0xF0-F1)
    — or None if the scan finds no AUTO* entry.
    """
    for track in range(4):
        for slot in range(20):
            off = track * 10240 + slot * 256
            entry = img[off:off + 256]
            if len(entry) < 256:
                return None
            if entry[0] == 0 and entry[1] == 0:
                return None  # never-used slot: B-DOS terminates the scan here
            if (entry[0] & 0x7F) == 0 or not _matches_auto(entry[1:11]):
                continue
            name = bytes(b & 0x7F for b in entry[1:11]).decode("ascii", "replace").rstrip()
            filetype = entry[0] & 0x3F
            start_page = entry[0xEC] & 0x1F
            off_form = entry[0xED] | (entry[0xEE] << 8)
            pages = entry[0xEF]
            len_mod = entry[0xF0] | (entry[0xF1] << 8)
            load = ((start_page + 1) << 14) + (off_form & 0x3FFF)
            length = pages * 16384 + len_mod
            return name, filetype, load, length
    return None


def boot_hazard(auto):
    """Why a record holding this AUTO* entry will not boot, or None if clean.

    `auto` is parse_mgt_auto_entry's result (possibly None). The two known
    will-not-boot shapes are the i331 stack overlap and the i332 BASIC-auto
    livelock; a missing AUTO* file cannot boot at all.
    """
    if auto is None:
        return ("no AUTO* file in the image directory — B-DOS's record boot "
                "(ALHK) has nothing to run")
    name, filetype, load, length = auto
    if filetype == FILETYPE_BASIC:
        return (f"the AUTO* file {name!r} is BASIC — a record boot runs it "
                "directly and never fires a BASIC RUN leg, a silent livelock "
                "(i332); compose a CODE-auto record vessel instead")
    if load < BOOT_STACK_HI and load + length > BOOT_STACK_LO:
        return (f"the AUTO* file {name!r} loads &{load:04X}..&{load + length:04X}, "
                f"overlapping the record-boot continuation stack &{BOOT_STACK_LO:04X}-"
                f"&{BOOT_STACK_HI - 1:04X} — sector deposits interleave with live stack "
                "frames and the boot wanders deaf (i331)")
    return None


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
    ap.add_argument("--image", default=None,
                    help="the .mgt pushed to the record: parsed for its AUTO* entry and "
                         "the boot is REFUSED on a known will-not-boot shape (i334)")
    ap.add_argument("--force", action="store_true",
                    help="boot anyway when --image reports a will-not-boot shape")
    args = ap.parse_args(argv)

    if not 0 <= args.record <= 255:
        ap.error(f"record {args.record} out of range 0..255")

    if args.image is not None and args.record != 0:
        hazard = boot_hazard(parse_mgt_auto_entry(open(args.image, "rb").read()))
        if hazard is not None:
            if not args.force:
                print(f"REFUSED: {hazard} (--force to boot anyway)")
                return 1
            print(f"WARNING (--force): {hazard}")
    elif args.record != 0:
        print("  note: record content unknown to the launcher — pass --image <the pushed "
              ".mgt> to check the AUTO* file for the i331/i332 will-not-boot shapes")

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
