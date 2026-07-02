#!/usr/bin/env python3
"""Push the B-DOS hook round-trip probe to the SAM, run it, report the verdict (i93b).

Two stages, both over UDP port 0xEDB0:

  1. TrinLoad delivers the probe: the standard ?/@/X push-and-run (trinpush)
     loads build/hook_roundtrip.bin to SAM page 1 (the deployment contract) and
     executes it, with HKRT_CFG_RECORD patched to the target record first.
  2. The probe HRECORD-selects that record, HSAVEs a deterministic pattern as
     CODE file "HKPROBE", HGTHDs it back, HLOADs it beside the source, and
     byte-compares — all via the real RST 8 against the resident B-DOS — then
     serves its own small framing on the SAME port:
       '?' -> '!HR'                        (discovery: '!' + the tool tag, i329)
       'R' -> [verdict][phase][detail LE16] (verdict 'P'/'F', '-' = incomplete;
                                            phase = last completed stage digit;
                                            detail &FFFF = recorded-size
                                            mismatch, else first mismatch offset)
       'Q' -> 'q', then exit back to trinload (clean, re-pushable)

A DOS-hook failure longjmps into BASIC's error path and never returns, so a
failed probe answers NO discovery — the on-SAM screen's last stage digit then
localises which hook died ('1' come-up, '2' HRECORD, '3' HSAVE, '4' HGTHD,
'5' HLOAD, '6' verdict computed).

WARNING (data safety): the probe HSAVEs a file into whatever record you name.
Target a fresh SCRATCH record (sd-push an all-zeros .mgt first — the i93b
hardware ladder); a re-run against the same record hits the DOS "file exists"
error (= the longjmp above). The Trinity SD card is a shared user resource.

Usage:
  hook-roundtrip.py <sam-ip> <record> [--bin PATH] [--map PATH] [--page N]

Exit codes: 0 = verdict 'P'; 1 = anything else (verdict 'F', probe incomplete,
tool never came up, or the push failed).
"""
import argparse
import socket
import struct
import sys

from trinpush import LOAD_ORG, PORT, config_offset, discover_tool, parse_map, push_and_run

HKRT_CFG_MAGIC = 0x5A  # HKRT_CFG_MAGIC_VAL in src/netboot/hook_roundtrip.asm

STAGE_NAMES = {
    b"0": "startup (nothing completed)",
    b"1": "come-up (EEPROM + ENC)",
    b"2": "HRECORD select",
    b"3": "HSAVE",
    b"4": "HGTHD",
    b"5": "HLOAD",
    b"6": "verdict computed",
}


def patch_record(data, off, record):
    """Return a copy of `data` with HKRT_CFG_RECORD patched at file offset `off`+1..2.

    Sanity-checks the magic byte at `off`+0 (HKRT_CONFIG), then writes the LE16
    record number. The magic is never written."""
    if off < 0 or off + 3 > len(data):
        raise ValueError(f"config offset {off:#x} outside binary ({len(data)} B)")
    if data[off] != HKRT_CFG_MAGIC:
        raise ValueError(
            f"config magic at {off:#x} is {data[off]:#x}, want {HKRT_CFG_MAGIC:#x} "
            "(wrong binary or wrong symbol?)")
    out = bytearray(data)
    out[off + 1:off + 3] = struct.pack("<H", record)
    return bytes(out)


def fetch_report(sock, dst, attempts=5):
    """Send 'R' and return (verdict, phase, detail) bytes/int, or (None, None, None).

    Retried on a lost reply; stale discovery replies ('!'-led) still in flight
    are consumed and skipped, not mistaken for a report."""
    for attempt in range(attempts):
        sock.sendto(b"R", (dst, PORT))
        try:
            while True:
                reply, _ = sock.recvfrom(16)
                if len(reply) >= 4 and reply[:1] in (b"P", b"F", b"-"):
                    detail = struct.unpack("<H", reply[2:4])[0]
                    return reply[:1], reply[1:2], detail
                # a stale reply — keep listening within this attempt
        except socket.timeout:
            if attempt + 1 < attempts:
                print(f"  R attempt {attempt + 1}: no report reply, retrying")
    return None, None, None


def quit_tool(sock, dst):
    """Send 'Q' so the probe exits back to trinload (re-pushable). Best-effort:
    a lost ack is non-fatal — the next launcher's stage-1 guard re-checks."""
    sock.sendto(b"Q", (dst, PORT))
    try:
        sock.recvfrom(8)
    except socket.timeout:
        print("  WARN: no ack for 'Q'; the probe may still own port 0xEDB0")


def main(argv):
    ap = argparse.ArgumentParser(description="push the hook round-trip probe and report its verdict")
    ap.add_argument("sam", help="the SAM's IP address (TrinLoad must be running)")
    ap.add_argument("record", type=lambda s: int(s, 0),
                    help="the 1-based scratch record to probe (1..65535)")
    ap.add_argument("--bin", default="build/hook_roundtrip.bin",
                    help="the program to push (default build/hook_roundtrip.bin)")
    ap.add_argument("--map", dest="mapfile", default=None,
                    help="the pyz80 mapfile (default: the --bin path with a .map suffix)")
    ap.add_argument("--page", type=int, default=1, help="TrinLoad target page (default 1)")
    args = ap.parse_args(argv)

    if not 1 <= args.record <= 0xFFFF:
        ap.error(f"record {args.record} out of range 1..65535")

    mapfile = args.mapfile or args.bin.rsplit(".", 1)[0] + ".map"
    data = open(args.bin, "rb").read()
    syms = parse_map(open(mapfile).read())
    off = config_offset(syms, symbol="HKRT_CONFIG")
    data = patch_record(data, off, args.record)

    # Stage 1: TrinLoad delivers the probe (push_and_run runs the i329 stage-1
    # discovery guard: it refuses unless trinload's bare '!' answers).
    print(f"patched HKRT_CONFIG @ offset {off:#x}: record={args.record}")
    print(f"pushing {args.bin} ({len(data)} B) -> {args.sam} page={args.page} addr=0x{LOAD_ORG:04X}")
    if not push_and_run(args.sam, data, args.page, LOAD_ORG):
        print("FAILED: could not push/run the probe (is TrinLoad listening on the SAM?)")
        return 1

    # Stage 2: the probe runs HRECORD/HSAVE/HGTHD/HLOAD before it serves, so
    # the discovery window must outlast the come-up + probe (15 x 2 s = 30 s,
    # the i330 window). No '!HR' answer = the probe died mid-hook (the on-SAM
    # stage digit says which) or never came up.
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.settimeout(2.0)
    dst, _ = discover_tool(sock, args.sam, b"HR", attempts=15)
    if dst is None:
        print("FAILED: the probe did not answer discovery — a DOS hook likely "
              "longjmped; read the last stage digit on the SAM screen "
              "('2' HRECORD / '3' HSAVE / '4' HGTHD / '5' HLOAD)")
        return 1

    verdict, phase, detail = fetch_report(sock, dst)
    if verdict is None:
        print("FAILED: no report reply to 'R'")
        quit_tool(sock, dst)
        return 1
    stage = STAGE_NAMES.get(phase, f"unknown stage {phase!r}")
    print(f"report: verdict={verdict.decode()} phase={phase.decode()} ({stage}) detail={detail}")
    quit_tool(sock, dst)

    if verdict == b"P":
        print(f"PASS: the full HRECORD/HSAVE/HGTHD/HLOAD round-trip verified on record {args.record}")
        return 0
    if verdict == b"F":
        why = ("recorded size mismatch (HGTHD returned the wrong length)"
               if detail == 0xFFFF else f"first byte mismatch at offset {detail}")
        print(f"FAIL: {why}")
    else:
        print(f"FAIL: probe incomplete (last completed stage: {stage})")
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
