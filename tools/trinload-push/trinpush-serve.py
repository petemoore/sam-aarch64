#!/usr/bin/env python3
"""Push the combined RRQ+WRQ serve program to the SAM, with a placement strategy.

The i121d host launcher. It sets the WRQ disk-record PLACEMENT strategy in the serve
program's SERVE_CONFIG block (src/netboot/netboot_serve.asm, i121h) and then pushes
the program over the wire via TrinLoad (the ?/@/X protocol in trinpush.py) for
trinload to load + run at &8000. A subsequent `tftp put <image>` from any LAN machine
then lands in a free Trinity record per the chosen strategy — unattended, never
overwriting a named record (write-to-free-only, q30).

The pushable block is build/netboot_serve_boot.bin (org &8000, entry &8000 =
`jp serve_main`); its mapfile gives the SERVE_CONFIG offset. Build it with
`make netboot-serve-trinload`.

Usage:
    trinpush-serve.py <sam-ip> [--strategy highest|lowest|explicit:N]
                               [--bin PATH] [--map PATH] [--page N]

Strategy (default highest): highest/lowest = the highest/lowest free record; the
default keeps the user's low, memorable slots for their own disks (TFTP storage
grows down from the top). explicit:N targets record N if it is free.
"""
import argparse
import sys

from trinpush import (LOAD_ORG, config_offset, parse_map, parse_strategy,
                      patch_config, push_and_run)


def main(argv):
    ap = argparse.ArgumentParser(description="push the serve program with a placement strategy")
    ap.add_argument("sam", help="the SAM's IP address (TrinLoad must be running)")
    ap.add_argument("--strategy", default="highest",
                    help="WRQ record placement: highest | lowest | explicit:N (default highest)")
    ap.add_argument("--bin", default="build/netboot_serve_boot.bin",
                    help="the serve program to push (default build/netboot_serve_boot.bin)")
    ap.add_argument("--map", dest="mapfile", default=None,
                    help="the pyz80 mapfile (default: the --bin path with a .map suffix)")
    ap.add_argument("--page", type=int, default=1, help="TrinLoad target page (default 1)")
    args = ap.parse_args(argv)

    mapfile = args.mapfile or args.bin.rsplit(".", 1)[0] + ".map"
    try:
        strategy, record = parse_strategy(args.strategy)
    except ValueError as e:
        ap.error(str(e))

    data = open(args.bin, "rb").read()
    syms = parse_map(open(mapfile).read())
    off = config_offset(syms)
    data = patch_config(data, off, strategy, record)

    rec = f" record={record}" if record else ""
    print(f"patched SERVE_CONFIG @ offset {off:#x}: strategy={args.strategy}{rec}")
    print(f"pushing {args.bin} ({len(data)} B) -> {args.sam} page={args.page} addr=0x{LOAD_ORG:04X}")
    if not push_and_run(args.sam, data, args.page, LOAD_ORG):
        return 1
    print("done — the serve program should now be serving TFTP on port 69 "
          "(get to serve out, put to push a disk image in)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
