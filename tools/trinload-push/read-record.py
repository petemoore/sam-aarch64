#!/usr/bin/env python3
"""Read a Trinity SD record's DISK-BODY sectors over the net via list_records (i362).

The read-only counterpart to list-records.py's LIST view: where 'L' reads the central
record-LIST entry (the record's name/free status), 'S' reads the record's own 800K
disk-body IMAGE, sector by sector. This is the confirmation channel a store that writes
a record's BODY but omits the record-LIST claim needs — that store leaves the record
reading FREE in the list (so list-records.py cannot see it), yet its bytes are on the
card; this reads them straight back.

It reuses list-records.py's TrinLoad plumbing: push build/list_records.bin (the SAME
read-only binary — i362 added the 'S' command to it), then query the program's framing
on UDP port 0xEDB0:

  '?'                           -> '!LR' + records(LE16)            (record count)
  'S' + record(LE16)            -> 's' + record(LE16) + relSector(LE16) + 512 raw bytes
      + relSector(LE16)            'E' + record(LE16) + relSector(LE16)   (out of range /
                                                                          CMD17 read fail)
  'Q'                           -> 'q'                              (quit -> trinload)

A record is 1600 sectors (819,200 B). relSector 0 is the first directory sector (track 0
sector 1): a B-DOS-formatted disk carries the 4-byte "BDOS" catalog stamp at offset 232
of THIS sector (bdos_inspect_record; B-DOS get.label), and the 10-char directory file
names live in the directory sectors (relSectors 0..9, track 0). So this launcher reads
relSectors 0..(--sectors-1), hexdumps them, checks relSector 0 offset 232 for "BDOS",
and scans the directory sectors for the expected filename substring — reporting each
PRESENT/ABSENT.

READ-ONLY: list_records is built without any list-write / CMD24 write path, so it
cannot write to the card at all (trinity_storage_shared_resource).

Usage:
    read-record.py <sam-ip> <record> [--expect NAME] [--sectors N] [--bin PATH] [--page N]
"""
import argparse
import socket
import struct
import sys

from trinpush import LOAD_ORG, PORT, discover_tool, push_and_run

SECTOR = 512
BDOS_STAMP_OFFSET = 232          # bdos_inspect_record: "BDOS" catalog stamp in relSector 0
BDOS_STAMP = b"BDOS"


def discover_inventory(sock, sam, attempts=15):
    """Run list_records' '?' handshake; return (sam_addr, record_count) or (None, None).

    Identical to list-records.py: the reply is '!LR' + records(LE16); discover_tool
    verifies the 'LR' tag so a bare-'!' trinload or another tool fails fast (i329). The
    pushed program's startup (EEPROM + ENC + CSD ladder) takes ~12 s on a 64 GB card, so
    the stage-2 window must outlast it (15 x 2 s = 30 s, i330)."""
    dst, reply = discover_tool(sock, sam, b"LR", attempts)
    if dst is None:
        return None, None
    if len(reply) < 5:
        print(f"  ? malformed list_records discovery reply {reply!r} (want 5 bytes)")
        return None, None
    return dst, struct.unpack("<H", reply[3:5])[0]


def read_body_sector(sock, dst, record, rel, attempts=3):
    """Read record's disk-body relSector `rel`; return its 512 raw bytes, or None on a
    refusal ('E' — out of range / read failure)."""
    query = b"S" + struct.pack("<HH", record, rel)
    for _ in range(attempts):
        sock.sendto(query, (dst, PORT))
        try:
            reply, _ = sock.recvfrom(600)
        except socket.timeout:
            continue
        if (reply[:1] == b"s" and len(reply) == 5 + SECTOR
                and struct.unpack("<HH", reply[1:5]) == (record, rel)):
            return reply[5:]
        if reply[:1] == b"E" and reply[1:5] == struct.pack("<HH", record, rel):
            return None  # the SAM refused: record/relSector out of range or read failure
    return None


def quit_program(sock, dst):
    """Send 'Q' so the program RETs to trinload (best-effort; Esc also works)."""
    sock.sendto(b"Q", (dst, PORT))
    try:
        sock.recvfrom(8)
    except socket.timeout:
        print("  WARN: no ack for 'Q'; press Esc on the SAM to return to trinload")


def hexdump(data, base=0):
    """Yield classic 16-byte-per-line hexdump lines of `data` (offsets from `base`)."""
    for off in range(0, len(data), 16):
        chunk = data[off:off + 16]
        hexs = " ".join(f"{b:02x}" for b in chunk)
        ascii_ = "".join(chr(b) if 0x20 <= b <= 0x7E else "." for b in chunk)
        yield f"  {base + off:04x}  {hexs:<47}  {ascii_}"


def read_record(sam, record, expect, sectors, list_bin, page):
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
    if not 1 <= record <= records:
        print(f"FAILED: record {record} out of range 1..{records}")
        quit_program(sock, dst)
        return False

    print(f"stage 2: card has {records} records; reading record {record} "
          f"body sectors 0..{sectors - 1}")
    bodies = {}
    for rel in range(sectors):
        sec = read_body_sector(sock, dst, record, rel)
        if sec is None:
            print(f"FAILED: record {record} sector {rel} refused/unreadable — aborting")
            quit_program(sock, dst)
            return False
        bodies[rel] = sec
    quit_program(sock, dst)

    # Hexdump the first directory sector (relSector 0) — where the identity bytes live.
    print(f"\nrecord {record} relSector 0 (first directory sector):")
    for line in hexdump(bodies[0]):
        print(line)

    # (1) BDOS catalog stamp at offset 232 of relSector 0.
    stamp = bodies[0][BDOS_STAMP_OFFSET:BDOS_STAMP_OFFSET + len(BDOS_STAMP)]
    bdos_present = stamp == BDOS_STAMP
    print(f"\nBDOS stamp @ relSector 0 +{BDOS_STAMP_OFFSET}: "
          f"{'PRESENT' if bdos_present else 'ABSENT'} (got {stamp!r})")

    # (2) Expected filename substring across the directory sectors.
    ok = bdos_present
    if expect is not None:
        needle = expect.encode("ascii", "replace")
        hits = [rel for rel, sec in bodies.items() if needle in sec]
        name_present = bool(hits)
        print(f"name {expect!r}: {'PRESENT' if name_present else 'ABSENT'}"
              + (f" (in relSector(s) {hits})" if hits else ""))
        ok = ok and name_present

    print(f"\nrecord {record}: {'CONFIRMED' if ok else 'NOT CONFIRMED'}")
    return ok


def main(argv):
    ap = argparse.ArgumentParser(
        description="read a Trinity SD record's disk-body sectors and confirm its contents")
    ap.add_argument("sam", help="the SAM's IP address (TrinLoad must be running)")
    ap.add_argument("record", type=int, help="the 1-based record number to read")
    ap.add_argument("--expect", default=None,
                    help="an ASCII filename substring to scan the directory sectors for "
                         "(e.g. LICENCE — B-DOS caps names ~10 chars, so pass a substring)")
    ap.add_argument("--sectors", type=int, default=10,
                    help="how many record-relative body sectors to read from track 0 "
                         "(default 10 = the whole first directory track)")
    ap.add_argument("--bin", default="build/list_records.bin",
                    help="the program to push (default build/list_records.bin)")
    ap.add_argument("--page", type=int, default=1, help="TrinLoad target page (default 1)")
    args = ap.parse_args(argv)
    if not 1 <= args.sectors <= 1600:
        ap.error("--sectors must be 1..1600 (a record is 1600 sectors)")
    ok = read_record(args.sam, args.record, args.expect, args.sectors, args.bin, args.page)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
