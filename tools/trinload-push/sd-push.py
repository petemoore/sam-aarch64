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
       '@' + linearSec(LE16) + data-> '.'×4 (data block; <=512 data bytes)
       'F'                         -> 'D'   (finalize: complete 1600-sector record)
                                      'E'   (finalize: wrong sector count)
     We stream the .mgt as one '@' block per 512-byte sector (linearSec = the
     0-based sector index, track-major: track*10 + (sector-1)), windowed at 4
     outstanding acks like trinload, then finalize.

WARNING (data safety): sd_push auto-picks the FIRST FREE record and writes ONLY
that record. As of i293 the record-DIRECTED HWSAD write is NOT yet hardware-
confirmed (the HRECORD-via-rst8 select does not redirect B-DOS's write base in
emulation — open i280b/i270; see the PR). Do NOT run this against a card whose
record 1 (or whatever B-DOS's default record base is) holds data you care about
until the record-directed write is confirmed on real hardware.

Usage:
  tools/trinload-push/sd-push.py [SAM_IP] [MGT_PATH] [SD_PUSH_BIN]
Defaults: 192.168.2.75  <required>  build/sd_push.bin
"""
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
    """Send 'F' and return the reply byte ('D' done / 'E' error), or None on timeout."""
    sock.sendto(b"F", (dst, PORT))
    try:
        reply, _ = sock.recvfrom(8)
    except socket.timeout:
        return None
    return reply[:1]


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

    # Stage 2: talk sd_push's own protocol on the same port.
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.settimeout(2.0)
    dst = discover(sock, sam)
    if dst is None:
        print("FAILED: sd_push did not answer discovery (it may not have come up)")
        return False

    print(f"stage 2: streaming {mgt_path} ({len(data)} B) as @-blocks")
    t0 = time.time()
    sent = stream_mgt(sock, dst, data)
    print(f"  streamed {sent} sectors in {time.time() - t0:.1f}s; finalizing")
    reply = finalize(sock, dst)
    if reply == b"D":
        print("DONE: sd_push validated a complete record and wrote it to the free record")
        return True
    if reply == b"E":
        print("ERROR: sd_push reported an incomplete record (sector count != 1600)")
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
