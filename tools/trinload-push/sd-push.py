#!/usr/bin/env python3
"""Push a .mgt disk image into a free Trinity SD record via sd_push (i293).

Two stages, both over UDP port 0xEDB0:

  1. TrinLoad delivers sd_push itself: the standard ?/@/X push-and-run (trinpush)
     loads build/sd_push.bin to SAM page 1 and executes it. (Page 1 is REQUIRED —
     sd_push's HWSAD source buffer BD_WRITE_BUF is at a logical address whose high
     byte names page 1 for the B-DOS HWSAD paged-pointer prelude; see
     src/netboot/sd_push.asm and tools/netboot-oracle/z80/sd_push_faithful_test.go.)

  2. sd_push then listens on the SAME port with its OWN small framing:
       '?'                         -> '!'   (discovery)
       'N' + name(<=16 bytes)      -> '.'   (record name for the catalogue entry)
       '@' + linearSec(LE16) + data-> '.'×4 (data block; <=512 data bytes)
       'F'                         -> 'D' + record(LE16)  (finalize: complete
                                      1600-sector record, written to record N)
                                      'E' + record(LE16)  (finalize: wrong sector
                                      count; record N was the target)
     We stream the .mgt as one '@' block per 512-byte sector (linearSec = the
     0-based sector index, track-major: track*10 + (sector-1)), windowed at 4
     outstanding acks like trinload, then finalize.

WARNING (data safety): sd_push auto-picks the FIRST FREE record (one whose 16-byte
list-entry name reads unnamed) and writes ONLY that record — it never targets a
USED record. The own-CMD24 write path is hardware-proven (i294/i295: a pushed
record booted on the real SAM), but the Trinity SD card is a SHARED user resource
(trinity_storage_shared_resource): treat every push with care.

Usage:
  tools/trinload-push/sd-push.py [SAM_IP] [MGT_PATH] [SD_PUSH_BIN]
Defaults: 192.168.2.75  <required>  build/sd_push.bin
"""
import os
import socket
import struct
import sys
import time

from trinpush import PORT, discover, push_and_run

SECTOR = 512
RECORD_SECTORS = 1600  # a Trinity record is exactly 1600 sectors (819200 bytes)
WINDOW = 4             # outstanding @-block acks (mirrors trinload's windowed push)


def stream_mgt(sock, dst, data):
    """Stream `data` as '@' blocks (one 512-byte sector each, linearSec ascending),
    windowed at WINDOW outstanding acks. Returns the number of sectors sent."""
    nsec = (len(data) + SECTOR - 1) // SECTOR
    outstanding = 0
    for s in range(nsec):
        chunk = data[s * SECTOR:(s + 1) * SECTOR]
        sock.sendto(b"@" + struct.pack("<H", s) + chunk, (dst, PORT))
        outstanding += 1
        if outstanding >= WINDOW:
            try:
                sock.recvfrom(8)
            except socket.timeout:
                print(f"  WARN: ack timeout at sector {s}")
            outstanding -= 1
    while outstanding:
        try:
            sock.recvfrom(8)
        except socket.timeout:
            break
        outstanding -= 1
    return nsec


def finalize(sock, dst):
    """Send 'F' and return (status, record): status is the reply byte ('D' done /
    'E' error; None on timeout) and record is the claimed 1-based record number
    (LE16 after the status byte, i308), or None if the reply carries none (an
    older sd_push binary)."""
    sock.sendto(b"F", (dst, PORT))
    try:
        reply, _ = sock.recvfrom(8)
    except socket.timeout:
        return None, None
    record = struct.unpack("<H", reply[1:3])[0] if len(reply) >= 3 else None
    return reply[:1], record


def send_name(sock, dst, name):
    """Send the record name as an 'N' message (<=16 ASCII bytes) so sd_push catalogues
    the pushed record under its own filename instead of the hardcoded default. Sent once,
    after discovery and before the '@' block stream (the deferred claim reads it on the
    first block). Best-effort: a missing ack is non-fatal — sd_push falls back to its
    built-in default name."""
    payload = name.encode("ascii", "replace")[:16]
    sock.sendto(b"N" + payload, (dst, PORT))
    try:
        sock.recvfrom(8)
    except socket.timeout:
        print("  WARN: no ack for the record-name ('N') message; sd_push uses its default")


def push_mgt(sam, mgt_path, sd_push_bin):
    data = open(mgt_path, "rb").read()
    if len(data) != RECORD_SECTORS * SECTOR:
        print(f"WARNING: {mgt_path} is {len(data)} B, not {RECORD_SECTORS * SECTOR} "
              f"(a full Trinity record); finalize will reply 'E'.")

    # Stage 1: TrinLoad delivers sd_push to page 1 and runs it.
    print(f"stage 1: pushing {sd_push_bin} to {sam} (page 1) via TrinLoad")
    push_bin = open(sd_push_bin, "rb").read()
    if not push_and_run(sam, push_bin, page=1, addr=0x8000):
        print("FAILED: could not push/run sd_push (is TrinLoad listening on the SAM?)")
        return False

    # Stage 2: talk sd_push's own protocol on the same port. sd_push's startup
    # (EEPROM read + ENC init + CSD ladder + free-record list scan) takes ~12 s
    # on a 64 GB card, so the discovery window must outlast it: 15 attempts x
    # 2 s timeout = 30 s (i330; 5 x 2 s declared failure while it was still
    # coming up).
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.settimeout(2.0)
    dst = discover(sock, sam, attempts=15)
    if dst is None:
        print("FAILED: sd_push did not answer discovery (it may not have come up)")
        return False

    record_name = os.path.basename(mgt_path)
    send_name(sock, dst, record_name)
    print(f"  record name: {record_name!r} (the record's catalogue entry)")

    print(f"stage 2: streaming {mgt_path} ({len(data)} B) as @-blocks")
    t0 = time.time()
    sent = stream_mgt(sock, dst, data)
    print(f"  streamed {sent} sectors in {time.time() - t0:.1f}s; finalizing")
    reply, record = finalize(sock, dst)
    where = f"record {record}" if record else "the free record (number unreported)"
    if reply == b"D":
        print(f"DONE: sd_push validated a complete record and wrote it to {where}")
        return True
    if reply == b"E":
        print(f"ERROR: sd_push reported an incomplete record (sector count != 1600; "
              f"the target was {where})")
        return False
    print("ERROR: no finalize reply from sd_push")
    return False


if __name__ == "__main__":
    SAM = sys.argv[1] if len(sys.argv) > 1 else "192.168.2.75"
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(2)
    MGT = sys.argv[2]
    BIN = sys.argv[3] if len(sys.argv) > 3 else "build/sd_push.bin"
    sys.exit(0 if push_mgt(SAM, MGT, BIN) else 1)
