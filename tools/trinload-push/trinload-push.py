#!/usr/bin/env python3
# Push a binary to TrinLoad on the SAM and execute it (the ?/@/X protocol; see
# src/netboot/trinload.asm). A python3 port of simonowen/trinload's test/trinload.py
# (which is python2-only) — same protocol, byte-for-byte. See README.md.
import socket, struct, sys, time

SAM = sys.argv[1] if len(sys.argv) > 1 else "192.168.2.75"
PATH = sys.argv[2] if len(sys.argv) > 2 else "build/netboot_dumper_trinload.bin"
PAGE = int(sys.argv[3]) if len(sys.argv) > 3 else 1
ADDR = int(sys.argv[4], 0) if len(sys.argv) > 4 else 0x8000
PORT = 0xEDB0

data = open(PATH, "rb").read()
print(f"pushing {PATH} ({len(data)} B) -> {SAM} page={PAGE} addr=0x{ADDR:04X}")

s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.settimeout(2.0)

# --- discovery: "?" -> "!" -------------------------------------------------
dst = None
for attempt in range(5):
    s.sendto(b"?", (SAM, PORT))
    try:
        reply, addr = s.recvfrom(8)
    except socket.timeout:
        print(f"  ? attempt {attempt+1}: timeout, retrying")
        continue
    if reply[:1] == b"!":
        dst = addr[0]
        print(f"  ! discovery reply from {dst}")
        break
    print(f"  ? got unexpected reply {reply!r} from {addr}")
if dst is None:
    print("FAILED: no '!' discovery reply (is trinload running on the SAM?)")
    sys.exit(1)

# --- data blocks: "@" page offset <chunk>, 4-byte acks, max 4 outstanding ---
page, offset, outstanding, sent = PAGE, 0, 0, 0
CHUNK = 1468
for i in range(0, len(data), CHUNK):
    chunk = data[i:i+CHUNK]
    s.sendto(b"@" + struct.pack("<BH", page, offset) + chunk, (dst, PORT))
    sent += 1
    outstanding += 1
    if outstanding >= 4:
        try:
            s.recvfrom(8)
        except socket.timeout:
            print("  WARN: ack timeout mid-push")
        outstanding -= 1
    offset += len(chunk)
    if offset >= 0x4000:
        offset -= 0x4000
        page += 1
while outstanding:
    try:
        s.recvfrom(8)
    except socket.timeout:
        break
    outstanding -= 1
print(f"  pushed {sent} data blocks ({len(data)} B)")

# --- execute: "X" page addr ------------------------------------------------
s.sendto(b"X" + struct.pack("<BH", PAGE, ADDR), (dst, PORT))
try:
    s.recvfrom(8)
    print(f"  X ack — executing at page {PAGE} / 0x{ADDR:04X}")
except socket.timeout:
    print("  X sent (no ack seen — the program may already be running)")
time.sleep(2.0)  # let the dumper read config + drv_init + start serving
print("done — dumper should now be serving TFTP on port 69")
