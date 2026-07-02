#!/usr/bin/env python3
"""simulate-pi-client.py — drive the SAM's integrated netboot server (netboot_server)
from this host with the Pi 400's captured PXE exchange (i95b-b1).

Reproduces the client side of docs/notes/pi-netboot-capture-analysis.md against a
LIVE SAM: DHCP DISCOVER→OFFER, REQUEST→ACK (all assertions filtered on
server-identifier == the SAM, so the house router's competing OFFER is ignored),
a non-PXE negative probe (the vendor-class gate must stay silent), then TFTP
RRQ→OACK→DATA fetches with byte verification and per-block timing (the serve
throughput number Pete asked for, 2026-07-02).

DHCP replies are BROADCAST to UDP :68 (the golden OFFER/ACK template), so the
client needs to hear port 68: it binds it directly when the kernel allows
(net.ipv4.ip_unprivileged_port_start <= 68, or root), else it spawns the
passwordless tcpdump (i272) and parses the pcap. TFTP needs no privilege.

Usage:
    simulate-pi-client.py <sam-ip> [--fetch NAME=FIXTURE]... [--miss NAME]
                          [--repeat N] [--skip-dhcp] [--selftest]

    --fetch config.txt=tools/netboot-oracle/testdata/pi-standins/config.txt
        TFTP-fetch NAME and byte-compare against FIXTURE (repeatable).
    --miss recovery.elf   expect ERROR(1) for NAME (the captured miss).
    --repeat N            re-fetch the first --fetch N times for timing (default 5).
    --selftest            exercise the DHCP builder/parser + reply-capture path
                          locally (loopback), no SAM needed. Run before a shot
                          (capture-readiness, memory feedback_hardware_test_capture_readiness).

Exit 0 = every step passed.
"""

import os
import socket
import struct
import subprocess
import sys
import tempfile
import time

DHCP_SERVER_PORT = 67
DHCP_CLIENT_PORT = 68
TFTP_PORT = 69
MAGIC = b"\x63\x82\x53\x63"
PXE_VENDOR = b"PXEClient:Arch:00000:UNDI:002001"  # the captured Pi 400 opt-60
NON_PXE_VENDOR = b"MSFT 5.0"                      # a plausible non-PXE client
# Synthetic client identity: locally-administered MAC, never a real device's.
CHADDR = bytes([0x02, 0x70, 0x78, 0x65, 0x73, 0x69])  # 02:70:78:65:73:69

REPLY_TIMEOUT = 5.0
BLKSIZE = 1024  # the negotiated size from the capture


def build_bootp(xid: int, msgtype: int, vendor: bytes, extra_opts: bytes = b"") -> bytes:
    """A BOOTREQUEST mirroring the captured Pi 400 DISCOVER/REQUEST option set
    (opt 55 param list, opt 60 vendor class, opt 93 arch, opt 97 UUID)."""
    hdr = struct.pack(
        "!BBBBIHH4s4s4s4s16s64s128s",
        1, 1, 6, 0,            # op, htype, hlen, hops
        xid, 0, 0,             # xid, secs, flags (the Pi sends flags 0)
        b"\0" * 4, b"\0" * 4, b"\0" * 4, b"\0" * 4,  # ciaddr/yiaddr/siaddr/giaddr
        CHADDR + b"\0" * 10, b"\0" * 64, b"\0" * 128,
    )
    opts = MAGIC
    opts += bytes([53, 1, msgtype])
    opts += bytes([55, 14, 1, 3, 43, 60, 66, 67, 128, 129, 130, 131, 132, 133, 134, 135])
    opts += bytes([60, len(vendor)]) + vendor
    opts += bytes([93, 2, 0, 0])
    opts += bytes([97, 17]) + bytes(range(17))
    opts += extra_opts
    opts += bytes([255])
    return hdr + opts


def parse_options(payload: bytes) -> dict:
    """Tag-walk the options region of a BOOTP reply into {tag: value}."""
    i = payload.find(MAGIC)
    if i < 0:
        return {}
    i += 4
    out = {}
    while i < len(payload):
        tag = payload[i]
        if tag == 255:
            break
        if tag == 0:
            i += 1
            continue
        ln = payload[i + 1]
        out[tag] = payload[i + 2:i + 2 + ln]
        i += 2 + ln
    return out


def bootp_fields(payload: bytes) -> dict:
    op, _, _, _, xid, _, _ = struct.unpack("!BBBBIHH", payload[:12])
    return {
        "op": op,
        "xid": xid,
        "yiaddr": socket.inet_ntoa(payload[16:20]),
        "siaddr": socket.inet_ntoa(payload[20:24]),
        "opts": parse_options(payload),
    }


class ReplySink:
    """Delivers UDP:68 broadcast payloads: a bound socket when the kernel
    allows an unprivileged port-68 bind, else a passwordless-tcpdump pcap."""

    def __init__(self):
        self.sock = None
        self.proc = None
        self.pcap = None
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind(("", DHCP_CLIENT_PORT))
            s.settimeout(0.2)
            self.sock = s
            print("  reply sink: bound UDP :68 directly")
        except PermissionError:
            # The path must NOT pre-exist: tcpdump drops privileges and cannot
            # O_TRUNC a foreign-owned file; -Z root keeps the savefile writable
            # anywhere, and the read-back below goes through sudo for the same
            # reason. Both invocations are the i272 passwordless-tcpdump grant.
            self.dir = tempfile.mkdtemp(prefix="pi-client-")
            self.pcap = os.path.join(self.dir, "dhcp68.pcap")
            self.proc = subprocess.Popen(
                ["sudo", "-n", "tcpdump", "-Z", "root", "-i", "any", "-w", self.pcap,
                 "--immediate-mode", "-U", "udp", "port", "68"],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
            time.sleep(1.5)  # tcpdump arm-up before the request goes out
            print("  reply sink: passwordless tcpdump on UDP :68")

    def drain(self, deadline: float):
        """Yield BOOTP payloads seen until the deadline."""
        if self.sock:
            while time.time() < deadline:
                try:
                    data, _ = self.sock.recvfrom(2048)
                    yield data
                except socket.timeout:
                    continue
        else:
            time.sleep(max(0.0, deadline - time.time()))
            time.sleep(0.5)  # let tcpdump flush
            out = subprocess.run(
                ["sudo", "-n", "tcpdump", "-r", self.pcap, "-x", "udp", "port", "68"],
                capture_output=True, text=True,
            ).stdout
            yield from self._frames_from_hexdump(out)

    @staticmethod
    def _frames_from_hexdump(text: str):
        frames, cur = [], []
        for line in text.splitlines():
            line = line.strip()
            if line.startswith("0x"):
                cur += bytes.fromhex(line.split(":", 1)[1].replace(" ", ""))
            elif cur:
                frames.append(bytes(cur))
                cur = []
        if cur:
            frames.append(bytes(cur))
        for raw in frames:
            i = raw.find(MAGIC)
            if i >= 0:
                # back up to the BOOTP header start: magic sits at +236
                start = i - 236
                if start >= 0:
                    yield raw[start:]

    def close(self):
        if self.sock:
            self.sock.close()
        if self.proc:
            self.proc.terminate()
            self.proc.wait()
            os.unlink(self.pcap)  # root-owned, but the dir is ours — unlink is a dir-write
            os.rmdir(self.dir)


def dhcp_round(sam_ip: str, xid: int, msgtype: int, vendor: bytes,
               extra_opts: bytes, expect_reply: bool, want_type: int, label: str):
    """One request→reply round. Only replies with server-id/siaddr == the SAM
    count (the house router answers broadcasts too). Returns the reply fields
    (or None for an expected silence)."""
    sink = ReplySink()
    try:
        tx = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        tx.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
        payload = build_bootp(xid, msgtype, vendor, extra_opts)
        tx.sendto(payload, ("255.255.255.255", DHCP_SERVER_PORT))
        tx.close()
        deadline = time.time() + REPLY_TIMEOUT
        for data in sink.drain(deadline):
            f = bootp_fields(data)
            if f["op"] != 2 or f["xid"] != xid:
                continue
            sid = f["opts"].get(54)
            from_sam = (sid and socket.inet_ntoa(sid) == sam_ip) or f["siaddr"] == sam_ip
            if not from_sam:
                continue
            if not expect_reply:
                print(f"FAIL [{label}]: the SAM answered a frame it must ignore: {f}")
                sys.exit(1)
            got = f["opts"].get(53, b"\0")[0]
            if got != want_type:
                print(f"FAIL [{label}]: reply type {got}, want {want_type}")
                sys.exit(1)
            return f
        if expect_reply:
            print(f"FAIL [{label}]: no reply from the SAM within {REPLY_TIMEOUT}s")
            sys.exit(1)
        print(f"  ok [{label}]: SAM silent, as required")
        return None
    finally:
        sink.close()


def run_dhcp(sam_ip: str):
    print("DHCP: DISCOVER -> OFFER")
    xid = int.from_bytes(os.urandom(4), "big")
    offer = dhcp_round(sam_ip, xid, 1, PXE_VENDOR, b"", True, 2, "DISCOVER")
    yiaddr = offer["yiaddr"]
    opt43 = offer["opts"].get(43, b"")
    if b"Raspberry Pi Boot" not in opt43:
        print(f"FAIL: OFFER option 43 lacks the Pi boot menu blob: {opt43!r}")
        sys.exit(1)
    if not offer["opts"].get(60, b"").startswith(b"PXEClient"):
        print("FAIL: OFFER does not echo the PXEClient vendor class")
        sys.exit(1)
    if offer["siaddr"] != sam_ip:
        print(f"FAIL: OFFER siaddr {offer['siaddr']} != {sam_ip}")
        sys.exit(1)
    print(f"  ok: OFFER yiaddr={yiaddr} siaddr={offer['siaddr']} opt43+PXE echo present")

    print("DHCP: REQUEST -> ACK")
    extra = bytes([54, 4]) + socket.inet_aton(sam_ip) + bytes([50, 4]) + socket.inet_aton(yiaddr)
    ack = dhcp_round(sam_ip, xid, 3, PXE_VENDOR, extra, True, 5, "REQUEST")
    if ack["yiaddr"] != yiaddr:
        print(f"FAIL: ACK yiaddr {ack['yiaddr']} != OFFER yiaddr {yiaddr} (lease unstable)")
        sys.exit(1)
    print(f"  ok: ACK confirms {yiaddr} (stable per-MAC lease)")

    print("DHCP: non-PXE DISCOVER must be ignored (the vendor-class gate)")
    dhcp_round(sam_ip, xid ^ 0xFFFFFFFF, 1, NON_PXE_VENDOR, b"", False, 0, "non-PXE")


def tftp_fetch(sam_ip: str, name: str, timeout: float = 10.0):
    """RRQ with blksize option; returns (bytes, per-block timestamps)."""
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(timeout)
    rrq = b"\0\x01" + name.encode() + b"\0octet\0blksize\0" + str(BLKSIZE).encode() + b"\0"
    s.sendto(rrq, (sam_ip, TFTP_PORT))
    server_tid = None
    data = b""
    stamps = [time.monotonic()]
    expected_block = 1
    while True:
        pkt, (rip, rport) = s.recvfrom(4 + BLKSIZE + 64)
        if server_tid is None:
            server_tid = rport
        elif rport != server_tid:
            continue  # unknown TID, ignore (RFC 1350 §4)
        op = struct.unpack("!H", pkt[:2])[0]
        if op == 6:  # OACK — acknowledge with ACK(0)
            s.sendto(b"\0\x04\0\0", (rip, rport))
            continue
        if op == 5:
            code = struct.unpack("!H", pkt[2:4])[0]
            s.close()
            return None, code, stamps
        if op != 3:
            print(f"FAIL: unexpected TFTP op {op} for {name}")
            sys.exit(1)
        block = struct.unpack("!H", pkt[2:4])[0]
        if block != expected_block:
            continue  # duplicate, re-ACK below
        chunk = pkt[4:]
        data += chunk
        stamps.append(time.monotonic())
        s.sendto(b"\0\x04" + pkt[2:4], (rip, rport))
        expected_block += 1
        if len(chunk) < BLKSIZE:
            s.close()
            return data, None, stamps


def run_tftp(sam_ip: str, fetches, miss, repeat: int):
    first_name = None
    for spec in fetches:
        name, _, fixture = spec.partition("=")
        first_name = first_name or name
        want = open(fixture, "rb").read()
        print(f"TFTP: RRQ {name} ({len(want)} B expected)")
        got, err, stamps = tftp_fetch(sam_ip, name)
        if err is not None:
            print(f"FAIL: {name} -> ERROR({err})")
            sys.exit(1)
        if got != want:
            print(f"FAIL: {name} content mismatch ({len(got)} B vs {len(want)} B)")
            sys.exit(1)
        blocks = len(stamps) - 1
        span = stamps[-1] - stamps[0]
        print(f"  ok: {len(got)} B in {blocks} block(s), {span*1000:.0f} ms")

    if first_name and repeat > 0:
        print(f"TFTP: timing {first_name} x{repeat} (serve throughput)")
        total_bytes, total_s, per_block = 0, 0.0, []
        for _ in range(repeat):
            got, err, stamps = tftp_fetch(sam_ip, first_name)
            if err is not None or got is None:
                print("FAIL: timing fetch errored")
                sys.exit(1)
            total_bytes += len(got)
            total_s += stamps[-1] - stamps[0]
            per_block += [b - a for a, b in zip(stamps, stamps[1:])]
        kbs = (total_bytes / 1024) / total_s if total_s else 0.0
        avg_ms = 1000 * sum(per_block) / len(per_block)
        print(f"  MEASURED: {kbs:.1f} KB/s effective, {avg_ms:.0f} ms/{BLKSIZE}B block "
              f"({total_bytes} B over {total_s:.2f} s)")

    if miss:
        print(f"TFTP: RRQ {miss} must yield ERROR(1)")
        got, err, _ = tftp_fetch(sam_ip, miss)
        if err != 1:
            print(f"FAIL: miss returned {('data', err)}")
            sys.exit(1)
        print("  ok: ERROR(1) file-not-found")


def selftest():
    """Capture-readiness: the builder/parser round-trip + the reply sink."""
    payload = build_bootp(0x11223344, 1, PXE_VENDOR)
    f = bootp_fields(payload)
    assert f["xid"] == 0x11223344 and f["opts"][60] == PXE_VENDOR, f
    reply = bytearray(payload)
    reply[0] = 2
    idx = payload.find(MAGIC) + 4 + 2
    reply[idx] = 2  # msgtype OFFER
    f = bootp_fields(bytes(reply))
    assert f["op"] == 2 and f["opts"][53][0] == 2
    print("builder/parser: ok")
    sink = ReplySink()
    tx = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    tx.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
    tx.sendto(bytes(reply), ("127.0.0.1", DHCP_CLIENT_PORT))
    seen = any(bootp_fields(d)["xid"] == 0x11223344 for d in sink.drain(time.time() + 3))
    sink.close()
    if not seen:
        print("FAIL: reply sink did not deliver the loopback OFFER")
        sys.exit(1)
    print("reply sink: ok (loopback OFFER delivered)")


def main(argv):
    if "--selftest" in argv:
        selftest()
        return 0
    if not argv:
        print(__doc__)
        return 2
    sam_ip = argv[0]
    fetches = []
    miss = None
    repeat = 5
    skip_dhcp = "--skip-dhcp" in argv
    i = 1
    while i < len(argv):
        if argv[i] == "--fetch":
            fetches.append(argv[i + 1]); i += 2
        elif argv[i] == "--miss":
            miss = argv[i + 1]; i += 2
        elif argv[i] == "--repeat":
            repeat = int(argv[i + 1]); i += 2
        else:
            i += 1
    if not skip_dhcp:
        run_dhcp(sam_ip)
    run_tftp(sam_ip, fetches, miss, repeat)
    print("PASS: full simulated Pi client session")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
