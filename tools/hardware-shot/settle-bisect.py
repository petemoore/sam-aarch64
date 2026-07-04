#!/usr/bin/env python3
"""settle-bisect.py — the i291b-b2 live-hardware ENC28J60/shared-PIC settle bisection.

Measures the real post-&38 SD-init settle window on a live Trinity SAM by bisecting
the settle_probe delay N. For each N it patches settle_delay_count into
settle_probe.bin, pushes the patched binary to the auto-booted trinload, and
captures the probe's SATR report (broadcast UDP :9000): status 0=FRESH (the PIC
settled — chk_trinity read 'TR') / 1=STALE (still settling — the &08 select was
dropped). It bisects N between a stale lower bound and a fresh upper bound to
converge on the boundary N* = the smallest N whose chk_trinity, issued N delay
iterations after the &38, reads fresh.

The probe RETs to trinload after each report (tr_terminate on hardware), so EVERY
shot runs in ONE power-cycle — no per-N reboot. Bring the SAM up first
(tools/hardware-shot/run-shot.sh's power-cycle, or ~/bin/tapo.sh on) so trinload is
listening, then run this.

Host-side T-state calibration (from the emulator, settle_probe_test.go): the delay
loop costs T_PER_ITER T-states/iteration and there are OFFSET fixed T-states between
the &38 OUT and chk_trinity's &08 select, so the real settle in Z80 T-states is
  settle_T = OFFSET + N* * T_PER_ITER
which the caller uses to set enc28j60.go's sdInitSettleTStates (measured + margin),
replacing the conservative 1200 (4x the documented ~50us).

--selftest loopback-proves the SATR capture path with NO SAM (the capture-readiness
check mandated before any hardware shot): it crafts a STALE and a FRESH SATR record,
sends each to 127.0.0.1:PORT, and asserts the decode + classification round-trips.

The hardware push is a SAM deploy, gated by the deploy-guard (i252): run with
DEPLOY_CHECKED=1 after confirming the probe binary is the one you intend to push.
--selftest needs no gate (no SAM traffic).

HARDWARE FINDINGS (2026-07-04, first bring-up — the bisection does NOT yet complete):
  The launcher mechanics work: --selftest proves the SATR capture+classification
  path (loopback), and a live push succeeded once — trinload answered discovery '!',
  the 2935 B probe pushed, and 'X ack — executing at page 1 / 0x8000' fired. But:
    (1) NO SATR report was ever captured, and trinload did NOT resume afterward
        (every later discovery failed) — so settle_probe HANGS on real hardware
        BEFORE reaching test_report/tr_terminate. Prime suspect: drv_wait_link
        (the probe does a fresh drv_init that resets the ENC/PHY; PHY link
        renegotiation on silicon takes far longer than the emulator's instant
        link-up, or does not re-establish, so drv_wait_link spins). Secondary
        suspects: test_report's SATR TX path was never hardware-proven (no host
        listener existed before this tool), and the i228 tr_terminate emulation-
        vs-hardware branch (a misdetect would di;halt instead of RET-to-trinload).
    (2) trinload push-readiness lags ping: a push issued ~16 s after the SAM first
        answered ping got NO discovery reply; a later push (more warm-up) worked.
        Wait longer after boot, or retry discovery, before the first shot.
  NEXT (emulation-first): reproduce/triage the hang in the koron-go harness — does
  the probe still report if drv_wait_link is dropped or the model injects a slow
  link-up? Confirm test_report's TX reaches a host listener on hardware (push
  port_probe, which shares drv_init+drv_wait_link+test_report, as an A/B control).
  Only then resume the bisection here.
"""
import argparse
import importlib.util
import os
import socket
import struct
import sys
import time

_HERE = os.path.dirname(os.path.abspath(__file__))
_REPO = os.path.dirname(os.path.dirname(_HERE))

# The probe's SATR report port (TR_PORT in src/netboot/test_report.asm) and record
# layout (magic/version/test_id/status/dlen/detail); mirrors the Go parseSATR.
SATR_PORT = 9000
SATR_MAGIC = b"SATR"
TEST_ID_SETTLE = 3  # TEST_ID_SETTLE in src/netboot/settle_probe.asm
STATUS_FRESH = 0
STATUS_STALE = 1

# Emulator-derived calibration (settle_probe_test.go: boundary N_emu=44 at the 1200
# T-state model constant). T_PER_ITER is the delay loop's per-iteration cost from the
# Z80 ISA (dec bc=6, ld a,b=4, or c=4, jr nz taken=12); OFFSET is the fixed &38->&08
# span, bracketed by the emulator boundary: 1200-44*26 <= OFFSET < 1200-43*26.
T_PER_ITER = 26
OFFSET = 69  # midpoint of [56, 82); the few-T uncertainty is dwarfed by the margin


def load_probe(bin_path, map_path):
    """Return (image bytes, file offset of settle_delay_count)."""
    with open(bin_path, "rb") as f:
        image = bytearray(f.read())
    org, addr = 0x8000, None
    for line in open(map_path):
        line = line.strip()
        if line.endswith("=settle_delay_count"):
            addr = int(line.split("=", 1)[0], 16)
            break
    if addr is None:
        raise SystemExit(f"settle_delay_count not in {map_path} — rebuild `make netboot-settle-probe`")
    off = addr - org
    if not (0 <= off <= len(image) - 2):
        raise SystemExit(f"settle_delay_count offset 0x{off:X} outside the image")
    return image, off


def patched(image, off, n):
    """A copy of the probe image with settle_delay_count (16-bit LE) set to n (>=1)."""
    if not (1 <= n <= 0xFFFF):
        raise ValueError(f"N={n} out of range 1..65535 (0 would be a 65536-iteration loop)")
    out = bytearray(image)
    struct.pack_into("<H", out, off, n)
    return out


def parse_satr(pkt):
    """Decode a SATR record -> (test_id, status, detail) or None. Mirrors Go parseSATR."""
    i = pkt.find(SATR_MAGIC)
    if i < 0 or len(pkt) < i + 9:
        return None
    p = pkt[i:]
    dlen = p[8]
    if len(p) < 9 + dlen:
        return None
    test_id = p[5] | (p[6] << 8)
    return test_id, p[7], p[9:9 + dlen]


def open_satr_socket(port):
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
    except OSError:
        pass
    sock.bind(("0.0.0.0", port))
    return sock


def capture_satr(sock, timeout):
    """Wait up to `timeout`s for a settle-probe SATR record; return (status, detail) or None."""
    deadline = time.monotonic() + timeout
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return None
        sock.settimeout(remaining)
        try:
            data, _ = sock.recvfrom(65535)
        except socket.timeout:
            return None
        rec = parse_satr(data)
        if rec and rec[0] == TEST_ID_SETTLE:
            return rec[1], rec[2]  # (status, detail)


def one_shot(trinpush, ip, image, off, n, port, retries, verbose, capture=4.0):
    """Push settle_probe with settle_delay_count=n, capture its SATR status. Returns
    STATUS_FRESH / STATUS_STALE, or None if no report was captured after `retries`."""
    for attempt in range(1, retries + 1):
        sock = open_satr_socket(port)
        try:
            ok = trinpush.push_and_run(ip, bytes(patched(image, off, n)), settle=2.5)
            if not ok:
                if verbose:
                    print(f"    N={n} attempt {attempt}: push failed (trinload not answering?)")
                time.sleep(1.0)
                continue
            got = capture_satr(sock, timeout=capture)
        finally:
            sock.close()
        if got is None:
            if verbose:
                print(f"    N={n} attempt {attempt}: no SATR report captured")
            continue
        status, detail = got
        echoed = detail[0] | (detail[1] << 8) if len(detail) >= 2 else None
        rt = detail[2] if len(detail) >= 3 else None
        rr = detail[3] if len(detail) >= 4 else None
        if echoed != n:
            print(f"    N={n}: WARNING probe echoed N={echoed} (poke/patch mismatch) — ignoring")
            continue
        label = "FRESH" if status == STATUS_FRESH else "STALE"
        print(f"    N={n:5d} -> {label}  (readT=0x{rt:02X} readR=0x{rr:02X})")
        return status
    return None


def bisect(trinpush, ip, image, off, lo, hi, port, retries, verbose):
    """Bisect for N* = smallest N in (lo, hi] that reads FRESH. Requires lo STALE, hi FRESH."""
    lo_status = one_shot(trinpush, ip, image, off, lo, port, retries, verbose)
    hi_status = one_shot(trinpush, ip, image, off, hi, port, retries, verbose)
    if lo_status is None or hi_status is None:
        raise SystemExit("bracket shot(s) produced no report — is the SAM up and reachable?")
    if lo_status != STATUS_STALE:
        raise SystemExit(f"lower bracket N={lo} did not read STALE (got {lo_status}); widen it downward")
    if hi_status != STATUS_FRESH:
        raise SystemExit(f"upper bracket N={hi} did not read FRESH (got {hi_status}); widen it upward")
    while hi - lo > 1:
        mid = (lo + hi) // 2
        status = one_shot(trinpush, ip, image, off, mid, port, retries, verbose)
        if status is None:
            raise SystemExit(f"shot N={mid} produced no report after {retries} retries")
        if status == STATUS_FRESH:
            hi = mid
        else:
            lo = mid
    return hi  # smallest FRESH


def settle_tstates(nstar):
    return OFFSET + nstar * T_PER_ITER


def run_selftest(port):
    """Loopback-prove the SATR capture + classification path with no SAM."""
    def record(status, n, rt, rr):
        detail = struct.pack("<HBB", n, rt, rr)
        return SATR_MAGIC + struct.pack("<BHBB", 1, TEST_ID_SETTLE, status, len(detail)) + detail

    tx = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    for status, n, rt, rr in ((STATUS_STALE, 1, 0x04, 0x52), (STATUS_FRESH, 500, 0x54, 0x52)):
        sock = open_satr_socket(port)
        try:
            tx.sendto(record(status, n, rt, rr), ("127.0.0.1", port))
            got = capture_satr(sock, timeout=2.0)
        finally:
            sock.close()
        if got is None:
            print(f"SELFTEST FAIL: no SATR captured for status={status}")
            return 1
        gstatus, detail = got
        gn = detail[0] | (detail[1] << 8)
        if gstatus != status or gn != n or detail[2] != rt or detail[3] != rr:
            print(f"SELFTEST FAIL: round-trip mismatch: sent (status={status},N={n},{rt:#x},{rr:#x}) "
                  f"got (status={gstatus},N={gn},{detail[2]:#x},{detail[3]:#x})")
            return 1
        label = "FRESH" if gstatus == STATUS_FRESH else "STALE"
        print(f"  selftest {label}: N={gn} readT=0x{detail[2]:02X} readR=0x{detail[3]:02X}  OK")
    print("SELFTEST OK — SATR capture + classification path verified (loopback).")
    return 0


def import_trinpush():
    path = os.path.join(_REPO, "tools", "trinload-push", "trinpush.py")
    spec = importlib.util.spec_from_file_location("trinpush", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--ip", default="192.168.2.75", help="the SAM's IP (trinload running)")
    ap.add_argument("--bin", default=os.path.join(_REPO, "build", "settle_probe.bin"))
    ap.add_argument("--map", default=os.path.join(_REPO, "build", "settle_probe.map"))
    ap.add_argument("--lo", type=int, default=1, help="lower bracket (must read STALE)")
    ap.add_argument("--hi", type=int, default=400, help="upper bracket (must read FRESH)")
    ap.add_argument("--port", type=int, default=SATR_PORT)
    ap.add_argument("--retries", type=int, default=3, help="per-shot retries on a dropped report")
    ap.add_argument("--selftest", action="store_true",
                    help="loopback-prove the capture path with no SAM, then exit")
    ap.add_argument("--single", type=int, default=None, metavar="N",
                    help="diagnostic: push one shot at N (with --capture window), then exit")
    ap.add_argument("--capture", type=float, default=4.0,
                    help="per-shot SATR capture window in seconds")
    ap.add_argument("-v", "--verbose", action="store_true")
    args = ap.parse_args(argv)

    if args.selftest:
        return run_selftest(args.port)

    if os.environ.get("DEPLOY_CHECKED") != "1":
        print("settle-bisect: set DEPLOY_CHECKED=1 (this pushes settle_probe.bin to the SAM) "
              "after confirming the binary. --selftest needs no gate.", file=sys.stderr)
        return 2

    trinpush = import_trinpush()
    image, off = load_probe(args.bin, args.map)

    if args.single is not None:
        print(f"diagnostic single shot N={args.single} on {args.ip} "
              f"(capture {args.capture}s, settle_delay_count @ file+0x{off:X})")
        status = one_shot(trinpush, args.ip, image, off, args.single, args.port,
                          args.retries, True, capture=args.capture)
        if status is None:
            print("RESULT: no SATR report captured — the probe did not report in the window.")
            return 1
        print(f"RESULT: N={args.single} -> {'FRESH' if status == STATUS_FRESH else 'STALE'}")
        return 0

    print(f"settle bisection on {args.ip}: bracket N in [{args.lo} STALE, {args.hi} FRESH], "
          f"settle_delay_count @ file+0x{off:X}")
    nstar = bisect(trinpush, args.ip, image, off, args.lo, args.hi,
                   args.port, args.retries, args.verbose)
    st = settle_tstates(nstar)
    print()
    print(f"RESULT: boundary N* = {nstar} (smallest FRESH); N*-1={nstar-1} reads STALE.")
    print(f"  real settle ~= OFFSET + N* * T_PER_ITER = {OFFSET} + {nstar}*{T_PER_ITER} = {st} T-states")
    print(f"  suggested sdInitSettleTStates = {st} * margin (e.g. *2 -> {st * 2}), "
          f"replacing the conservative 1200.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
