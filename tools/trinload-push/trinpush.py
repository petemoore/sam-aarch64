#!/usr/bin/env python3
"""Shared TrinLoad push primitives + the serve config-block patcher.

The push half is the `?`/`@`/`X` UDP protocol (port 0xEDB0) that TrinLoad runs on
the SAM (see src/netboot/trinload.asm) — a python3 port of simonowen/trinload's
test/trinload.py, byte-for-byte. `trinload-push.py` (raw push) and
`trinpush-serve.py` (the i121d serve launcher, which patches the placement-strategy
config block first) both build on this module so the wire protocol lives in one place.

Pushed tools share the port and the '?' verb, so discovery replies carry identity
(i329): trinload alone answers a bare '!'; a tool answers '!' + its 2-byte tag
(TOOL_TAGS). push_and_run REFUSES stage-1 against a non-bare responder (a live tool
would swallow the '@' blocks as data), and discover_tool() gives stage-2 launchers a
positive is-it-really-my-tool check.

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

# Discovery-reply identities on port 0xEDB0 (i329). Trinload alone answers a BARE
# b"!"; every pushed tool that runs its own wire loop answers '!' + a 2-byte ASCII
# tool tag (+ optional tool payload), so a launcher can tell WHICH program owns the
# port. Keep in lockstep with the tools' discovery handlers (src/netboot/*.asm).
TOOL_TAGS = {
    b"SP": "sd_push",
    b"LR": "list_records",
}

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


def identify(reply):
    """Name the 0xEDB0 responder from its discovery reply bytes."""
    if reply == b"!":
        return "trinload"
    if reply[:1] == b"!" and len(reply) >= 3:
        tool = TOOL_TAGS.get(reply[1:3])
        return tool if tool else f"an unknown tool (tag {reply[1:3]!r})"
    return f"an unknown responder ({reply!r})"


def stage1_refusal(reply):
    """Why a stage-1 (trinload) push must not proceed given this discovery reply,
    or None if the reply is trinload's bare '!'.

    Anything longer than a bare '!' means a pushed TOOL owns port 0xEDB0, not
    trinload — streaming '@' blocks at it would be interpreted as the tool's own
    data (i329: a stage-1 retry hit a live sd_push, which wrote the pushed binary's
    blocks into an SD record as junk)."""
    if reply == b"!":
        return None
    return (f"{identify(reply)} answered discovery on port 0xEDB0, not trinload — "
            "refusing the stage-1 push (its '@' blocks would be fed to that tool "
            "as data; i329). Quit/finish the tool or power-cycle the SAM, then retry.")


def discover(sock, sam, attempts=5):
    """Run the `?`→`!` discovery handshake; return (sam_addr, reply) or (None, None).

    The reply names the responder: trinload answers a bare b"!"; a pushed tool
    answers '!' + its 2-byte tag (see TOOL_TAGS) + optional payload. Callers decide
    who they will accept (push_and_run: trinload only; discover_tool: one tool)."""
    for attempt in range(attempts):
        sock.sendto(b"?", (sam, PORT))
        try:
            reply, addr = sock.recvfrom(16)
        except socket.timeout:
            print(f"  ? attempt {attempt + 1}: timeout, retrying")
            continue
        if reply[:1] == b"!":
            print(f"  ! discovery reply from {addr[0]} ({identify(reply)})")
            return addr[0], reply
        print(f"  ? got unexpected reply {reply!r} from {addr}")
    return None, None


def discover_tool(sock, sam, tag, attempts=15):
    """Discover a PUSHED TOOL by its 2-byte tag; return (sam_addr, reply) or
    (None, None).

    Retries on timeout (a tool's startup — EEPROM read + ENC init + CSD ladder —
    takes ~12 s on a 64 GB card, so the window must outlast it: 15 × 2 s = 30 s,
    i330). Fails FAST on a '!' reply from anyone else (trinload answering means the
    tool never came up or already exited; another tool answering will not change on
    retry)."""
    dst, reply = discover(sock, sam, attempts)
    if dst is None:
        return None, None
    if reply[1:3] != tag:
        print(f"FAILED: {identify(reply)} answered discovery, not "
              f"{TOOL_TAGS.get(tag, tag)} — the pushed tool is not the one serving")
        return None, None
    return dst, reply


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
    """Discover trinload, push `data`, and execute at (page, addr). Returns True on push.

    REFUSES to push unless the discovery reply is trinload's bare '!' — a tagged
    reply means a pushed tool owns the port and would swallow the '@' blocks (i329)."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.settimeout(2.0)
    dst, reply = discover(sock, sam)
    if dst is None:
        print("FAILED: no '!' discovery reply (is trinload running on the SAM?)")
        return False
    refusal = stage1_refusal(reply)
    if refusal is not None:
        print(f"REFUSED: {refusal}")
        return False
    sent = push_data(sock, dst, data, page)
    print(f"  pushed {sent} data blocks ({len(data)} B)")
    execute(sock, dst, page, addr)
    time.sleep(settle)
    return True
