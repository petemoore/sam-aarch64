#!/usr/bin/env python3
"""List the Trinity SD record inventory over the net via list_records (i322).

The read-only LIST counterpart completing the store/boot/delete toolkit (sd-push.py
i293 / boot-record.py i316 / delete-record.py i317): this launcher pushes
build/list_records.bin to the SAM via TrinLoad (the ?/@/X protocol in trinpush.py),
then queries the program's own framing on the same UDP port 0xEDB0:

  '?'                  -> '!' + records(LE16)   (the card's record count; 0 = the
                                                 CSD was unreadable)
  'L' + listSec(LE16)  -> 'R' + listSec(LE16) + the raw 512-byte list sector
                          'E' + listSec(LE16)   (out of range / CMD17 read failure)
  'Q'                  -> 'q'                   (quit: the program RETs to trinload,
                                                 so the next tool can be pushed)

Each 512-byte list sector holds 32 16-byte record-name entries (record n's entry:
list sector 1+(n-1)//32, offset ((n-1)%32)*16). An entry is FREE iff its first byte,
with bit 7 (the write-protect flag) masked, is zero; names print with bit 7 stripped
per byte (B-DOS's AND-127 print convention). Layout + citations:
docs/specs/trinity-record-detection-design.md §4.

The tool prints one line per USED record ("REC nnnn  name", '*' = write-protected),
then a summary with the first free record — the number the next sd-push.py will
claim. READ-ONLY: the program is built without the list-write primitives, so it
cannot write to the card at all (trinity_storage_shared_resource).

Usage:
    list-records.py <sam-ip> [--bin PATH] [--page N]
"""
import argparse
import socket
import struct
import sys

from trinpush import LOAD_ORG, PORT, push_and_run

SECTOR = 512
ENTRIES_PER_SECTOR = 32


def discover_inventory(sock, sam, attempts=5):
    """Run list_records' '?' handshake; return (sam_addr, record_count) or (None, None)."""
    for attempt in range(attempts):
        sock.sendto(b"?", (sam, PORT))
        try:
            reply, addr = sock.recvfrom(16)
        except socket.timeout:
            print(f"  ? attempt {attempt + 1}: timeout, retrying")
            continue
        if reply[:1] == b"!" and len(reply) >= 3:
            return addr[0], struct.unpack("<H", reply[1:3])[0]
        print(f"  ? got unexpected reply {reply!r} from {addr}")
    return None, None


def fetch_list_sector(sock, dst, idx, attempts=3):
    """Fetch list sector `idx` (1-based); return its 512 raw bytes or None."""
    for _ in range(attempts):
        sock.sendto(b"L" + struct.pack("<H", idx), (dst, PORT))
        try:
            reply, _ = sock.recvfrom(600)
        except socket.timeout:
            continue
        if (reply[:1] == b"R" and len(reply) == 3 + SECTOR
                and struct.unpack("<H", reply[1:3])[0] == idx):
            return reply[3:]
        if reply[:1] == b"E":
            return None  # the SAM refused: out of range or a read failure
    return None


def quit_program(sock, dst):
    """Send 'Q' so the program RETs to trinload (best-effort; Esc also works)."""
    sock.sendto(b"Q", (dst, PORT))
    try:
        sock.recvfrom(8)
    except socket.timeout:
        print("  WARN: no ack for 'Q'; press Esc on the SAM to return to trinload")


def decode_entries(sector, first_record, max_record):
    """Yield (record, name, write_protected, used) for each entry in a list sector."""
    for e in range(ENTRIES_PER_SECTOR):
        rec = first_record + e
        if rec > max_record:
            return
        entry = sector[e * 16:(e + 1) * 16]
        used = (entry[0] & 0x7F) != 0
        prot = bool(entry[0] & 0x80)
        name = bytes(b & 0x7F for b in entry).decode("ascii", "replace").rstrip(" \x00")
        yield rec, name, prot, used


def list_records(sam, list_bin, page):
    print(f"stage 1: pushing {list_bin} to {sam} (page {page}) via TrinLoad")
    push_bin = open(list_bin, "rb").read()
    if not push_and_run(sam, push_bin, page=page, addr=LOAD_ORG):
        print("FAILED: could not push/run list_records (is TrinLoad listening on the SAM?)")
        return False

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.settimeout(2.0)
    dst, records = discover_inventory(sock, sam)
    if dst is None:
        print("FAILED: list_records did not answer discovery (it may not have come up)")
        return False
    if records == 0:
        print("FAILED: the card's CSD was unreadable (0 records) — is a card inserted?")
        quit_program(sock, dst)
        return False

    nlist = (records + ENTRIES_PER_SECTOR - 1) // ENTRIES_PER_SECTOR
    if nlist > 255:
        # The list-read seam addresses sectors with one byte, so the program serves
        # list sectors 1..255 (records 1..8160) and refuses higher indices. Match it.
        print(f"  WARN: {records} records, but the list-read seam reaches only the "
              f"first {255 * ENTRIES_PER_SECTOR}; listing those")
        nlist, records = 255, 255 * ENTRIES_PER_SECTOR
    print(f"stage 2: card has {records} records ({nlist} list sectors); reading the list")

    used, free, first_free = [], 0, None
    for idx in range(1, nlist + 1):
        sector = fetch_list_sector(sock, dst, idx)
        if sector is None:
            print(f"FAILED: list sector {idx} unreadable — inventory incomplete, aborting")
            quit_program(sock, dst)
            return False
        for rec, name, prot, is_used in decode_entries(
                sector, (idx - 1) * ENTRIES_PER_SECTOR + 1, records):
            if is_used:
                used.append((rec, name, prot))
            else:
                free += 1
                if first_free is None:
                    first_free = rec
    quit_program(sock, dst)

    for rec, name, prot in used:
        print(f"REC {rec:4d}  {name}{' *' if prot else ''}")
    print(f"{len(used)} used / {free} free / {records} records"
          + (f"; first free: REC {first_free}" if first_free else "; CARD FULL"))
    return True


def main(argv):
    ap = argparse.ArgumentParser(
        description="push the list-records program and print the SD record inventory")
    ap.add_argument("sam", help="the SAM's IP address (TrinLoad must be running)")
    ap.add_argument("--bin", default="build/list_records.bin",
                    help="the program to push (default build/list_records.bin)")
    ap.add_argument("--page", type=int, default=1, help="TrinLoad target page (default 1)")
    args = ap.parse_args(argv)
    return 0 if list_records(args.sam, args.bin, args.page) else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
