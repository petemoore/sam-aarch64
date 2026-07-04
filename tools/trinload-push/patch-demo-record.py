#!/usr/bin/env python3
"""Patch the i365e demo record number into a serve `.mgt`'s overlays (i365e).

`build/assemble_first_serve_record.mgt` boots, assembles `release.img`, renders
`release.src`, then serves both over TFTP. Two of its overlays need the number of
the record they booted from baked in, because their RAW absolute-LBA SD paths
compute `csd_base + 1600*(record-1) + linearSec` and there is no runtime
self-discovery (see mgt_patch.py). `sd_push` picks the first FREE record
dynamically, so we resolve that record first, then patch it in here BEFORE the
push. A wrong value is a shared-card DATA-SAFETY hazard (render would write
`release.src` into another record's LBA band), so the demo shot MUST assert the
record `sd_push` claims equals the record patched here, and refuse to boot on a
mismatch.

    make netboot-assemble-first-serve-record netboot-render-chain netboot-server
    # find the first free record, e.g. with list-records.py -> N
    tools/trinload-push/patch-demo-record.py build/assemble_first_serve_record.mgt N

Args: <mgt> <record> [--out PATH] [--render-map …] [--server-map …].
Patches in place unless --out is given. Reads back and prints each patched value.
"""
import argparse
import os
import sys

import mgt_patch

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))


def main():
    ap = argparse.ArgumentParser(description="Patch the demo record number into a serve .mgt")
    ap.add_argument("mgt", help="path to the demo serve .mgt")
    ap.add_argument("record", type=int, help="the record the .mgt will be stored at (1..255)")
    ap.add_argument("--out", help="write the patched .mgt here (default: patch in place)")
    ap.add_argument("--render-map", default=os.path.join(REPO, "build/render_chain.map"),
                    help="pyz80 map for the 'render' overlay (RDB_CFG_RECORD)")
    ap.add_argument("--server-map", default=os.path.join(REPO, "build/netboot_server.map"),
                    help="pyz80 map for the 'nbsrv' overlay (NB_BOOT_RECORD)")
    args = ap.parse_args()

    if not (1 <= args.record <= 255):
        # 0 = floppy (never a data record); NB_BOOT_RECORD is a single byte.
        sys.exit("record %d out of range 1..255" % args.record)

    with open(args.mgt, "rb") as f:
        mgt = bytearray(f.read())

    with open(args.render_map) as f:
        render_map = f.read()
    with open(args.server_map) as f:
        server_map = f.read()
    specs = [
        ("render", render_map, "RDB_CFG_RECORD", 2),
        ("nbsrv", server_map, "NB_BOOT_RECORD", 1),
    ]

    patched = mgt_patch.patch_record_overlays(mgt, args.record, specs)

    # Read back every patched value and confirm it took (belt-and-braces: a wrong
    # value here is a data-safety hazard on the shared card).
    for (store_name, _map, symbol, width) in [(s[0], s[1], s[2], s[3]) for s in specs]:
        got = mgt_patch.read_record_overlay(mgt, store_name, _map, symbol, width)
        if got != args.record:
            sys.exit("VERIFY FAILED: %s in overlay %r read back as %d, want %d"
                     % (symbol, store_name, got, args.record))

    out = args.out or args.mgt
    with open(out, "wb") as f:
        f.write(mgt)

    print("patched record %d into %s:" % (args.record, os.path.basename(out)))
    for (store_name, symbol, payload_off, width) in patched:
        print("  overlay %-6s %-14s payload+0x%04X (%d-byte LE) = %d"
              % (store_name, symbol, payload_off, width, args.record))
    print("verified: both overlays read back record %d" % args.record)


if __name__ == "__main__":
    main()
