"""Patch a record number into named CODE overlays inside a Trinity `.mgt`.

The i365e demo record (`build/assemble_first_serve_record.mgt`) carries two
overlays whose RAW absolute-LBA SD paths need to know which record they booted
from — the number is NOT self-discovered at runtime (that would need a
version-specific B-DOS sysvar address, which `bdos_seam.asm` bans):

  * the `render` overlay's `RDB_CFG_RECORD` (LE16) — the raw CMD17 read of the
    `IN` file and the raw CMD24 write of `RELEASESRC` compute
    `csd_base + 1600*(record-1) + linearSec`, so a WRONG value writes RELEASESRC
    into another record's LBA band (a shared-card data-safety hazard);
  * the `nbsrv` overlay's `NB_BOOT_RECORD` (byte) — the large-file disk serve
    reads RELEASESRC/RELEASEIMG from that record's sectors.

`sd_push` picks the first FREE record dynamically, so the number is not known at
compose time; the demo shot resolves it (list-records -> first free), patches
both overlays to it with this module, pushes (asserting the claimed record ==
the patched one), then boots. This mirrors exactly what the emulation gate
`assemble_first_serve_faithful_test.go` does in memory ("the real launcher
patches this").

MGT-structure functions are a faithful port of the Go authority helpers in
`tools/netboot-oracle/z80/netboot_server_largefile_test.go`
(`mgtSectorOffset` / `dirEntryFirstTS` / `patchMGTPayloadByte`); keep them in
lock-step.
"""

import os

ORG = 0x8000
MGT_SIZE = 819200  # 80 cyl x 2 sides x 10 sectors x 512 = one Trinity record


def mgt_sector_offset(track_byte, sector):
    """Byte offset of MGT (track_byte, sector) in a track-major .mgt image.

    Port of Go `mgtSectorOffset`: side rides bit 7 of the track byte, cylinder
    is bits 0-6, sector is 1-based.
    """
    side = (track_byte & 0x80) >> 7
    cyl = track_byte & 0x7F
    return cyl * 10240 + side * 5120 + (sector - 1) * 512


def dir_entry_first_ts(mgt, name):
    """Return the body-chain head (track_byte, sector) of directory file `name`.

    Port of Go `dirEntryFirstTS`: scans the .mgt directory (tracks 0-3 side 0,
    two 256-byte entries per sector) for a 10-char space-padded name and reads
    the chain head from directory-entry offset 0x0D..0x0E. Raises KeyError if
    the file is absent.
    """
    field = (name + " " * 10)[:10].encode("latin-1")
    for slot in range(80):
        d = slot // 2
        off = (d // 10) * 10240 + (d % 10) * 512 + (slot % 2) * 256
        e = mgt[off:off + 256]
        if e[0] == 0:
            continue
        if e[1:11] == field:
            return e[0x0D], e[0x0E]
    raise KeyError("directory entry %r not found in the vessel" % name)


def patch_mgt_payload_byte(mgt, track, sector, off, val):
    """Set PAYLOAD byte `off` (0-based, past the 9-byte body header) of the file
    whose body chain starts at (track, sector).

    Port of Go `patchMGTPayloadByte`: follows the MGT [next-track, next-sector]
    links at +510/+511 of each 512-byte sector. `mgt` must be a bytearray.
    """
    body_off = off + 9
    base = 0
    while True:
        soff = mgt_sector_offset(track, sector)
        if body_off < base + 510:
            mgt[soff + (body_off - base)] = val & 0xFF
            return
        nt, ns = mgt[soff + 510], mgt[soff + 511]
        if nt == 0 and ns == 0:
            raise IndexError("payload offset %d runs past the MGT chain end" % off)
        track, sector = nt, ns
        base += 510


def parse_map_symbol(map_text, symbol):
    """Return the absolute address of `symbol` from a pyz80 mapfile
    ("ADDR=SYMBOL" per line, hex address). Mirrors trinpush.parse_map."""
    for line in map_text.splitlines():
        line = line.strip()
        addr, _, name = line.partition("=")
        if name.strip() == symbol:
            return int(addr.strip(), 16)
    raise KeyError("symbol %s not found in mapfile" % symbol)


def patch_record_overlays(mgt, record, specs):
    """Patch `record` into each overlay spec, in place on the `mgt` bytearray.

    `specs` is a list of (store_name, map_text, symbol, width) tuples. Each
    symbol's payload offset is (addr - ORG); `width` bytes are written
    little-endian. Returns a list of (store_name, symbol, payload_off, width)
    describing what was patched (for the operator log).
    """
    if not isinstance(mgt, bytearray):
        raise TypeError("mgt must be a bytearray (mutable)")
    if len(mgt) != MGT_SIZE:
        raise ValueError("mgt is %d bytes, want %d (one Trinity record)" % (len(mgt), MGT_SIZE))
    if not (1 <= record <= 0xFFFF):
        raise ValueError("record %d out of range 1..65535" % record)
    done = []
    for store_name, map_text, symbol, width in specs:
        if record > (1 << (8 * width)) - 1:
            raise ValueError(
                "record %d does not fit in %d-byte field %s" % (record, width, symbol))
        addr = parse_map_symbol(map_text, symbol)
        payload_off = addr - ORG
        if payload_off < 0:
            raise ValueError("symbol %s addr &%04X below org &%04X" % (symbol, addr, ORG))
        track, sector = dir_entry_first_ts(mgt, store_name)
        for i in range(width):
            patch_mgt_payload_byte(mgt, track, sector, payload_off + i, (record >> (8 * i)) & 0xFF)
        done.append((store_name, symbol, payload_off, width))
    return done


def read_record_overlay(mgt, store_name, map_text, symbol, width):
    """Read back the `width`-byte LE value the overlay currently carries for
    `symbol` — the inverse of patch_record_overlays, for verification."""
    addr = parse_map_symbol(map_text, symbol)
    payload_off = addr - ORG
    track, sector = dir_entry_first_ts(mgt, store_name)
    val = 0
    for i in range(width):
        # Walk the chain per byte (cheap; widths are 1-2).
        b = _read_payload_byte(mgt, track, sector, payload_off + i)
        val |= b << (8 * i)
    return val


def _read_payload_byte(mgt, track, sector, off):
    body_off = off + 9
    base = 0
    while True:
        soff = mgt_sector_offset(track, sector)
        if body_off < base + 510:
            return mgt[soff + (body_off - base)]
        nt, ns = mgt[soff + 510], mgt[soff + 511]
        if nt == 0 and ns == 0:
            raise IndexError("payload offset %d runs past the MGT chain end" % off)
        track, sector = nt, ns
        base += 510


# The single authority for the i365e demo-record overlay layout: which overlay
# (store name) carries which record symbol, in which map, at what width. The CLI
# (patch-demo-record.py) and the test (test_trinpush.py) both build their patch
# specs from this — do not re-inline the tuples elsewhere (single source of truth).
DEMO_RECORD_SPECS = [
    # (store_name, map_path_relative_to_repo, symbol, width)
    ("render", "build/render_chain.map", "RDB_CFG_RECORD", 2),
    ("nbsrv", "build/netboot_server.map", "NB_BOOT_RECORD", 1),
]


def load_demo_specs(repo_root, overrides=None):
    """Build the patch specs [(store_name, map_text, symbol, width)] from
    DEMO_RECORD_SPECS, reading each overlay's map. `overrides` maps a store name
    to an explicit map path (else the repo-relative default is used)."""
    overrides = overrides or {}
    specs = []
    for store_name, default_map, symbol, width in DEMO_RECORD_SPECS:
        map_path = overrides.get(store_name) or os.path.join(repo_root, default_map)
        with open(map_path) as f:
            specs.append((store_name, f.read(), symbol, width))
    return specs
