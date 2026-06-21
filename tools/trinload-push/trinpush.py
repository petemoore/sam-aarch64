#!/usr/bin/env python3
"""Shared TrinLoad push primitives + the serve config-block patcher.

The push half is the `?`/`@`/`X` UDP protocol (port 0xEDB0) that TrinLoad runs on
the SAM (see src/netboot/trinload.asm) — a python3 port of simonowen/trinload's
test/trinload.py, byte-for-byte. `trinload-push.py` (raw push) and
`trinpush-serve.py` (the i121d serve launcher, which patches the placement-strategy
config block first) both build on this module so the wire protocol lives in one place.

The patch half sets the WRQ disk-record PLACEMENT strategy in the serve program's
SERVE_CONFIG block (src/netboot/netboot_serve.asm) before the program is pushed, so
an unattended `tftp put` lands in a free record per the chosen strategy. The byte
layout mirrors the asm block and the Go authority's patchStrategy
(tools/netboot-oracle/z80/netboot_serve_wrq_record_test.go):

  +0  magic   = 0x5A         (SERVE_CFG_MAGIC_VAL; sanity-check, never written)
  +1  strategy: 0=highest-free, 1=lowest-free, 2=explicit
  +2  record  : explicit record number (U16LE); used only when strategy == 2
"""
import socket
import struct
import time

PORT = 0xEDB0
LOAD_ORG = 0x8000      # the netboot binaries are org &8000; file offset = addr - ORG
CHUNK = 1468           # @-block payload size (matches simonowen/trinload)

# SERVE_CONFIG strategy encoding — keep in lockstep with netboot_serve.asm
# (SERVE_STRAT_*) and serve.go (Strategy* constants).
SERVE_CFG_MAGIC = 0x5A
STRAT_HIGHEST = 0
STRAT_LOWEST = 1
STRAT_EXPLICIT = 2

STRATEGY_NAMES = {"highest": STRAT_HIGHEST, "lowest": STRAT_LOWEST}


def parse_strategy(spec):
    """Parse a --strategy value into (strategy_code, explicit_record).

    Accepts "highest", "lowest", or "explicit:N" (N a 1-based record number).
    Returns (code, record) where record is 0 unless the strategy is explicit.
    Raises ValueError on anything else.
    """
    spec = spec.strip().lower()
    if spec in STRATEGY_NAMES:
        return STRATEGY_NAMES[spec], 0
    if spec.startswith("explicit:"):
        rec = int(spec.split(":", 1)[1], 0)
        if not 1 <= rec <= 0xFFFF:
            raise ValueError(f"explicit record {rec} out of range 1..65535")
        return STRAT_EXPLICIT, rec
    raise ValueError(
        f"bad --strategy {spec!r}: want highest | lowest | explicit:N")


def parse_map(text):
    """Parse a pyz80 mapfile ("ADDR=SYMBOL" per line) into {symbol: addr}."""
    syms = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or "=" not in line:
            continue
        addr, _, name = line.partition("=")
        try:
            syms[name.strip()] = int(addr.strip(), 16)
        except ValueError:
            continue
    return syms


def config_offset(syms, symbol="SERVE_CONFIG", org=LOAD_ORG):
    """File offset of the config block, from the map symbol and the load org."""
    if symbol not in syms:
        raise KeyError(f"symbol {symbol} not found in mapfile")
    return syms[symbol] - org


def patch_config(data, off, strategy, record=0):
    """Return a copy of `data` with the SERVE_CONFIG block patched at offset `off`.

    Sanity-checks the magic byte at +0, then writes the strategy at +1 and (for the
    explicit strategy) the record word at +2..3 (U16LE). The magic is never written.
    """
    if off < 0 or off + 4 > len(data):
        raise ValueError(f"config offset {off:#x} outside binary ({len(data)} B)")
    if data[off] != SERVE_CFG_MAGIC:
        raise ValueError(
            f"config magic at {off:#x} is {data[off]:#x}, want {SERVE_CFG_MAGIC:#x} "
            "(wrong binary or wrong symbol?)")
    out = bytearray(data)
    out[off + 1] = strategy & 0xFF
    if strategy == STRAT_EXPLICIT:
        out[off + 2:off + 4] = struct.pack("<H", record & 0xFFFF)
    return bytes(out)


def discover(sock, sam, attempts=5):
    """Run the `?`→`!` discovery handshake; return the SAM's source address."""
    for attempt in range(attempts):
        sock.sendto(b"?", (sam, PORT))
        try:
            reply, addr = sock.recvfrom(8)
        except socket.timeout:
            print(f"  ? attempt {attempt + 1}: timeout, retrying")
            continue
        if reply[:1] == b"!":
            print(f"  ! discovery reply from {addr[0]}")
            return addr[0]
        print(f"  ? got unexpected reply {reply!r} from {addr}")
    return None


def push_data(sock, dst, data, page):
    """Push `data` as `@`-blocks (max 4 outstanding, 4-byte acks). Returns block count."""
    offset, outstanding, sent = 0, 0, 0
    for i in range(0, len(data), CHUNK):
        chunk = data[i:i + CHUNK]
        sock.sendto(b"@" + struct.pack("<BH", page, offset) + chunk, (dst, PORT))
        sent += 1
        outstanding += 1
        if outstanding >= 4:
            try:
                sock.recvfrom(8)
            except socket.timeout:
                print("  WARN: ack timeout mid-push")
            outstanding -= 1
        offset += len(chunk)
        if offset >= 0x4000:
            offset -= 0x4000
            page += 1
    while outstanding:
        try:
            sock.recvfrom(8)
        except socket.timeout:
            break
        outstanding -= 1
    return sent


def execute(sock, dst, page, addr):
    """Send the `X` execute command (page + address) and report the ack."""
    sock.sendto(b"X" + struct.pack("<BH", page, addr), (dst, PORT))
    try:
        sock.recvfrom(8)
        print(f"  X ack — executing at page {page} / 0x{addr:04X}")
    except socket.timeout:
        print("  X sent (no ack seen — the program may already be running)")


def push_and_run(sam, data, page=1, addr=LOAD_ORG, settle=2.0):
    """Discover the SAM, push `data`, and execute at (page, addr). Returns True on push."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.settimeout(2.0)
    dst = discover(sock, sam)
    if dst is None:
        print("FAILED: no '!' discovery reply (is trinload running on the SAM?)")
        return False
    sent = push_data(sock, dst, data, page)
    print(f"  pushed {sent} data blocks ({len(data)} B)")
    execute(sock, dst, page, addr)
    time.sleep(settle)
    return True
