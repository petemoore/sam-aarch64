#!/usr/bin/env python3
# Push a binary to TrinLoad on the SAM and execute it (the ?/@/X protocol; see
# src/netboot/trinload.asm). A python3 port of simonowen/trinload's test/trinload.py
# (which is python2-only) — same protocol, byte-for-byte. The wire protocol lives in
# trinpush.py (shared with trinpush-serve.py). See README.md.
import sys

from trinpush import LOAD_ORG, push_and_run

SAM = sys.argv[1] if len(sys.argv) > 1 else "192.168.2.75"
PATH = sys.argv[2] if len(sys.argv) > 2 else "build/netboot_dumper.bin"
PAGE = int(sys.argv[3]) if len(sys.argv) > 3 else 1
ADDR = int(sys.argv[4], 0) if len(sys.argv) > 4 else LOAD_ORG

data = open(PATH, "rb").read()
print(f"pushing {PATH} ({len(data)} B) -> {SAM} page={PAGE} addr=0x{ADDR:04X}")

if not push_and_run(SAM, data, PAGE, ADDR):
    sys.exit(1)
print("done — the pushed program should now be running")
