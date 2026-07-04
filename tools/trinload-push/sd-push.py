#!/usr/bin/env python3
"""Push a .mgt disk image into a free Trinity SD record via sd_push (i293).

Two stages, both over UDP port 0xEDB0:

  1. TrinLoad delivers sd_push itself: the standard ?/@/X push-and-run (trinpush)
     loads build/sd_push.bin to SAM page 1 and executes it. (Page 1 is REQUIRED —
     sd_push's HWSAD source buffer BD_WRITE_BUF is at a logical address whose high
     byte names page 1 for the B-DOS HWSAD paged-pointer prelude; see
     src/netboot/sd_push.asm and tools/netboot-oracle/z80/sd_push_faithful_test.go.)

  2. sd_push then listens on the SAME port with its OWN small framing:
       '?'                         -> '!SP' (discovery: '!' + the tool tag, i329)
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

Exit codes: 0 = pushed and finalize-confirmed ('D'); 1 = failure; 2 = usage;
3 = finalize reply lost but sd_push exited back to trinload — the push very
likely completed; verify with list-records.py (i335).
"""
import os
import socket
import struct
import sys
import time

from trinpush import PORT, discover, discover_tool, identify, push_and_run

SECTOR = 512
RECORD_SECTORS = 1600  # a Trinity record is exactly 1600 sectors (819200 bytes)
WINDOW = 4             # outstanding @-block acks (mirrors trinload's windowed push)


def stream_mgt(sock, dst, data, max_rounds=10):
    """Stream `data` as '@' blocks (one 512-byte sector each), windowed at WINDOW
    outstanding acks, RETRANSMITTING un-acked sectors across up to `max_rounds`
    rounds until every sector is acknowledged (i348). Returns (nsec, acked_count).

    sd_push acks each block with '.' + linearSec(LE16) (+ one data byte), so an ack
    identifies WHICH sector it confirms; a sector stays in the un-acked set until its
    ack is seen and is resent otherwise. This recovers BOTH failure modes of a lossy
    ~1600-sector stream: a lost '@' frame (the sector never landed) and a lost ack
    (the sector landed but looked un-acked). Because writes are absolute-LBA and
    idempotent, and sd_push counts UNIQUE sectors (src/netboot/sd_push.asm), a
    resent-but-already-written sector is free — it never over-counts past 1600.

    The old one-pass stream just WARNed on an ack timeout and moved on, so a single
    lost frame anywhere in the stream left sd_push short of 1600 and finalize replied
    'E' — the whole ~87 s push had to be deleted and retried from scratch."""
    nsec = (len(data) + SECTOR - 1) // SECTOR
    acked = set()

    def block(s):
        return b"@" + struct.pack("<H", s) + data[s * SECTOR:(s + 1) * SECTOR]

    def take_ack():
        """Read one ack; mark the sector it echoes acked. False on timeout."""
        try:
            reply, _ = sock.recvfrom(16)
        except socket.timeout:
            return False
        if reply[:1] == b"." and len(reply) >= 3:
            acked.add(struct.unpack("<H", reply[1:3])[0])
        return True

    stalled = 0
    for rnd in range(max_rounds):
        todo = [s for s in range(nsec) if s not in acked]
        if not todo:
            break
        if rnd:
            print(f"  round {rnd}: retransmitting {len(todo)} un-acked sector(s)")
        before = len(acked)
        outstanding = 0
        for s in todo:
            sock.sendto(block(s), (dst, PORT))
            outstanding += 1
            if outstanding >= WINDOW:
                # keep <= WINDOW blocks in flight; a timeout ends this burst's drain.
                outstanding = outstanding - 1 if take_ack() else 0
        while outstanding and take_ack():
            outstanding -= 1
        if len(acked) == before:
            stalled += 1
            if stalled >= 2:
                break  # two rounds with zero progress: the SAM is not answering
        else:
            stalled = 0

    missing = [s for s in range(nsec) if s not in acked]
    if missing:
        print(f"  WARN: {len(missing)} sector(s) still un-acked after {rnd + 1} round(s) "
              f"(first: {missing[0]}); finalize will report the shortfall")
    return nsec, len(acked)


def finalize(sock, dst, attempts=5):
    """Send 'F' and return (status, record): status is the reply byte ('D' done /
    'E' error; None if no reply after all attempts) and record is the claimed
    1-based record number (LE16 after the status byte, i308), or None if the
    reply carries none (an older sd_push binary).

    'F' is retried on a lost reply (i335 — one lost UDP reply misreported a
    completed 1600-sector push as failure). A retry is safe both ways: if the
    'F' itself was lost, sd_push is still serving and finalizes on the retry;
    if only the REPLY was lost, sd_push has RET'd to trinload, which ignores
    'F'. Stale block acks ('.') still in flight from the stream are consumed
    and skipped, not mistaken for a finalize reply."""
    for attempt in range(attempts):
        sock.sendto(b"F", (dst, PORT))
        try:
            while True:
                reply, _ = sock.recvfrom(8)
                if reply[:1] in (b"D", b"E"):
                    record = (struct.unpack("<H", reply[1:3])[0]
                              if len(reply) >= 3 else None)
                    return reply[:1], record
                # a stale block ack — keep listening within this attempt
        except socket.timeout:
            if attempt + 1 < attempts:
                print(f"  F attempt {attempt + 1}: no finalize reply, retrying")
    return None, None


def lost_reply_verdict(reply):
    """Map the post-finalize '?' probe reply to (exit_code, message) (i335).

    Called only after finalize() got no 'D'/'E' through every attempt. A bare
    '!' means trinload owns the port again — sd_push processed a finalize and
    exited, so the push very likely completed and only the reply was lost:
    exit 3 (verify, not failure). Anything else is a real failure (exit 1)."""
    if reply == b"!":
        return 3, ("finalize reply lost, but sd_push has exited back to trinload — "
                   "the push very likely completed; verify with list-records.py "
                   "(the record should be claimed and named)")
    if reply is None:
        return 1, "no finalize reply and no discovery response — the SAM may be wedged"
    return 1, (f"no finalize reply and {identify(reply)} still owns the port — "
               "the finalize never got through")


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
    """Run the two-stage push; return `(code, record)`.

    `code` is a process exit code: 0 = pushed and finalize-confirmed ('D'),
    1 = failure, 3 = finalize reply lost but sd_push exited back to trinload —
    verify with list-records.py (i335). `record` is the 1-based Trinity SD
    record sd_push claimed (from the finalize 'D'/'E' reply, i308), or None
    when it is unknown (a pre-finalize failure, or an older sd_push binary that
    reports no record). The record is what push-and-boot.py feeds boot-record.py
    to boot exactly the record just written (i284)."""
    data = open(mgt_path, "rb").read()
    if len(data) != RECORD_SECTORS * SECTOR:
        print(f"WARNING: {mgt_path} is {len(data)} B, not {RECORD_SECTORS * SECTOR} "
              f"(a full Trinity record); finalize will reply 'E'.")

    # Stage 1: TrinLoad delivers sd_push to page 1 and runs it.
    print(f"stage 1: pushing {sd_push_bin} to {sam} (page 1) via TrinLoad")
    push_bin = open(sd_push_bin, "rb").read()
    if not push_and_run(sam, push_bin, page=1, addr=0x8000):
        print("FAILED: could not push/run sd_push (is TrinLoad listening on the SAM?)")
        return 1, None

    # Stage 2: talk sd_push's own protocol on the same port. sd_push's startup
    # (EEPROM read + ENC init + CSD ladder + free-record list scan) takes ~12 s
    # on a 64 GB card, so the discovery window must outlast it: 15 attempts x
    # 2 s timeout = 30 s (i330; 5 x 2 s declared failure while it was still
    # coming up).
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.settimeout(2.0)
    dst, _ = discover_tool(sock, sam, b"SP", attempts=15)
    if dst is None:
        print("FAILED: sd_push did not answer discovery (it may not have come up)")
        return 1, None

    record_name = os.path.basename(mgt_path)
    send_name(sock, dst, record_name)
    print(f"  record name: {record_name!r} (the record's catalogue entry)")

    print(f"stage 2: streaming {mgt_path} ({len(data)} B) as @-blocks")
    t0 = time.time()
    sent, acked = stream_mgt(sock, dst, data)
    print(f"  streamed {sent} sectors ({acked} acked) in {time.time() - t0:.1f}s; finalizing")
    reply, record = finalize(sock, dst)
    where = f"record {record}" if record else "the free record (number unreported)"
    if reply == b"D":
        print(f"DONE: sd_push validated a complete record and wrote it to {where}")
        return 0, record
    if reply == b"E":
        print(f"ERROR: sd_push reported an incomplete record (sector count != 1600; "
              f"the target was {where})")
        return 1, record
    # No 'D'/'E' through every attempt: ask who owns the port now (i335).
    _, disc = discover(sock, sam, attempts=3)
    code, message = lost_reply_verdict(disc)
    print(("WARNING: " if code == 3 else "ERROR: ") + message)
    return code, record


if __name__ == "__main__":
    SAM = sys.argv[1] if len(sys.argv) > 1 else "192.168.2.75"
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(2)
    MGT = sys.argv[2]
    BIN = sys.argv[3] if len(sys.argv) > 3 else "build/sd_push.bin"
    code, _ = push_mgt(SAM, MGT, BIN)
    sys.exit(code)
