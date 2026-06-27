#!/usr/bin/env python3
"""Listen for the SAM netboot debug markers (i271) on UDP :9001 and decode them.

Each instrumented step in the bootable serve (built with -D NETBOOT_DEBUG) broadcasts
a 6-byte UDP packet to 255.255.255.255:9001:

    off 0  magic    4   'S','D','B','G'
    off 4  version  1   = 1
    off 5  marker   1   = a DBG_* step code (src/netboot/dbg_marker.asm)

This binds 0.0.0.0:9001 and prints each marker as it arrives, decoding the code to its
name so a hang/timeout localizes to the LAST marker seen. TX-only on the SAM side, so it
survives an SD transaction disturbing the ENC RX path. Ctrl-C (or --seconds) to stop.

Usage:
    listen-markers.py [--seconds N] [--port 9001]

The DBG_* table is kept in lockstep with src/netboot/dbg_marker.asm — update both
together. (Single source would mean parsing the .asm; this small mirror is clearer.)
"""
import argparse
import socket
import sys
import time

# Mirror of the DBG_* equ block in src/netboot/dbg_marker.asm.
MARKERS = {
    0x10: "WRQ_ENTRY",            # handle_wrq entered
    0x11: "WRQ_CLAIMED",          # free record claimed + ENC re-armed
    0x12: "WRQ_NOFREE",           # no free record -> ERROR(3)
    0x13: "WRQ_HANDSHAKE",        # about to send the OACK / ACK-0 handshake
    0x14: "CLAIM_FIND_PRE",       # about to bdos_find_record_for_strategy (CMD17 list reads)
    0x15: "CLAIM_SELECT_PRE",     # free record found; about to bdos_select_record (HRECORD hook)
    0x16: "CLAIM_SELECT_POST",    # HRECORD select returned (the claim succeeded)
    0x17: "REARM_TIMEOUT",        # serve_rearm_enc's ENC busy-poll hit its bound (the i280b contention)
    0x18: "PRIOR_REARM_TIMEOUT",  # at WRQ_ENTRY: the PRIOR WRQ's re-arm timed out (b2f; escapes the §8e dead window)
    0x20: "DATA_BLOCK",           # a DATA block accepted, about to sink/stage it
    0x21: "FLUSH_PRE",            # rrs_flush_sector entered: a full sector staged, about to write it
    0x22: "HWSAD_PRE",            # bdos_write_sector entered: about to RST 8 / DEFB 149 (B-DOS HWSAD)
    0x23: "HWSAD_POST",           # the HWSAD RST 8 returned (the per-sector SD write completed)
    0x30: "FINALIZE",             # final block: entering wd_finalize (flush+validate+claim)
    0x31: "FINALIZE_VALID",       # record validated + claimed -> final ACK
    0x32: "FINALIZE_BAD",         # invalid image -> ERROR(3)
    0x40: "DONE_CTRL",            # "tftp.done" control received -> return to trinload
    # i280b-b2i runtime-paging value reports: each TAG is immediately followed by a
    # marker whose code byte IS the register value (printed as the next UNKNOWN(&XX)).
    0x50: "HMPR_NEXT",            # the NEXT marker's &XX = HMPR (section C/D page) at this point
    0x51: "LMPR_NEXT",            # the NEXT marker's &XX = LMPR (section A/B page) at this point
}


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--seconds", type=float, default=0.0,
                    help="stop after N seconds of silence (0 = run until Ctrl-C)")
    ap.add_argument("--port", type=int, default=9001)
    args = ap.parse_args(argv)

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
    except OSError:
        pass
    sock.bind(("0.0.0.0", args.port))
    print(f"listening for SAM debug markers on udp/:{args.port} "
          f"({'until Ctrl-C' if args.seconds == 0 else f'{args.seconds}s idle timeout'})",
          flush=True)

    seen = []
    while True:
        if args.seconds > 0:
            sock.settimeout(args.seconds)
        try:
            data, addr = sock.recvfrom(65535)
        except socket.timeout:
            print(f"-- {args.seconds:.0f}s idle, stopping --", flush=True)
            break
        except KeyboardInterrupt:
            print("\n-- interrupted --", flush=True)
            break
        ts = time.strftime("%H:%M:%S")
        if len(data) >= 6 and data[0:4] == b"SDBG":
            code = data[5]
            name = MARKERS.get(code, f"UNKNOWN(&{code:02X})")
            print(f"{ts}  &{code:02X}  {name:18s} from {addr[0]}", flush=True)
            seen.append(name)
        else:
            print(f"{ts}  non-marker {len(data)}B from {addr[0]}: {data[:16].hex()}", flush=True)

    if seen:
        print("sequence: " + " -> ".join(seen), flush=True)
        # Order matters: deepest progress first. The per-block write (HWSAD/FLUSH/DATA)
        # means the push hand-shook and reached the B-DOS write — past the §8d claim/
        # handshake blocker (fixed in i280b-b2d via drv_wait_link).
        if "HWSAD_POST" in seen:
            print("RESULT: HWSAD_POST — a per-block SD write COMPLETED.", flush=True)
        elif "HWSAD_PRE" in seen:
            print("RESULT: reached HWSAD_PRE (no _POST) — hand-shake OK + per-block write entered, "
                  "but the B-DOS HWSAD write hangs (the original §8a HWSAD hang).", flush=True)
        elif "DATA_BLOCK" in seen:
            print("RESULT: reached DATA_BLOCK — the push hand-shook and is receiving data "
                  "(past the §8d claim/handshake blocker).", flush=True)
        elif "PRIOR_REARM_TIMEOUT" in seen:
            print("RESULT: PRIOR_REARM_TIMEOUT (&18) — the prior WRQ's re-arm TIMED OUT (bus stayed busy).",
                  flush=True)
        elif "CLAIM_SELECT_POST" in seen:
            print("RESULT: looped at the claim (no DATA_BLOCK) — the push did not hand-shake.", flush=True)
        if "REARM_TIMEOUT" in seen:
            print("NOTE: REARM_TIMEOUT (&17) also escaped this run.", flush=True)
    else:
        print("RESULT: no markers received.", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
