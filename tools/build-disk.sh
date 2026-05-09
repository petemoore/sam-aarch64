#!/usr/bin/env bash
# Constructs a .mgt disk image containing:
#   - a tokenised BASIC 'auto' file that auto-runs on boot
#     (CLEAR 24575 : LOAD "stub" CODE 24576 : CALL 24576)
#   - the stub binary as a code file named 'stub' (loaded at 0x6000)
#   - the input fixture as a raw file named 'IN' on the disk
#
# 0x6000 is the standard SAMDOS-coexisting code address — SAMDOS itself
# occupies 0x8000-0xBFFF, so user code below 0x8000 is required.
#
# Usage: build-disk.sh <input.s> <output.mgt>
#
# The 'auto' file is a hand-rolled tokenised SAM BASIC program. Its byte
# sequence is documented in docs/notes/simcoupe-batch.md under "Generating
# the BASIC auto file". samfile add does not support BASIC files, so this
# script uses an inline Python snippet to write the directory entry and sector
# data directly into the MGT image.

set -euo pipefail

input="$1"
output="$2"

cd "$(dirname "$0")/.."

if [ ! -f build/stub.bin ]; then
    echo "ERROR: build/stub.bin not found — run 'make stub' first" >&2
    exit 1
fi

rm -f "$output"

# Create a blank 819200-byte (800 KiB) MGT image (all zero bytes).
# samfile requires this exact size.
dd if=/dev/zero of="$output" bs=1024 count=800 2>/dev/null

# ---------------------------------------------------------------------------
# Add the tokenised BASIC 'auto' file directly into the disk image.
#
# samfile's CLI only supports code files (-c flag). We write the BASIC file
# by hand into the first available directory slot (offset 0) and data sector
# (track 4, sector 1). The 30-byte tokenised BASIC body encodes:
#
#   10 LOAD "stub" CODE : CALL 32768
#
# Directory entry layout (256 bytes at image offset 0):
#   0x00        type byte   0x10 = SAM BASIC
#   0x01-0x0a   filename    "auto      "
#   0x0b-0x0c   sector count (BE uint16)  = 1
#   0x0d        first sector track        = 4
#   0x0e        first sector number       = 1
#   0x0f-0xd1   sector address map (195 bytes); bit 0 = track 4 sector 1
#   0xf0-0xf1   LengthMod16K (LE)         = 30 (0x1e)
#   0xf2        ExecAddrDiv16K            = 0xff (unused for BASIC)
#   0xf3-0xf4   SAMBASICStartLine (LE)    = 10 (0x000a)
#
# Sector data layout (512 bytes at image offset 40960 = track 4, sector 1):
#   Bytes 0-8:   9-byte samfile file header (type, length, page info)
#   Bytes 9-38:  30-byte BASIC program body
#   Bytes 510-511: next-sector link (0, 0 = no next sector)
#
# The 9-byte samfile header for a BASIC file uses:
#   [0]    = 0x10 (SAM BASIC type)
#   [1-2]  = LengthMod16K LE = 30 (0x1e, 0x00)
#   [3-4]  = PageOffset LE = 0x00, 0x00
#   [5-6]  = 0x00, 0x00
#   [7]    = Pages = 0x00
#   [8]    = StartPage = 0x00
# ---------------------------------------------------------------------------
python3 - "$output" <<'EOF'
import sys

# Build the tokenised BASIC line:
#   10 CLEAR 24575 : LOAD "stub" CODE 24576 : CALL 24576
#
# SAM BASIC tokens: CLEAR=0xb3, LOAD=0x95, CODE=0xff,0x6c, CALL=0xe4.
# Numbers carry a 5-byte numeric form right after their ASCII digits,
# prefixed with 0x0e: [0x0e, 0x00, 0x00, lo, hi, 0x00] for an unsigned 16-bit.

def num(n: int) -> bytes:
    return bytes([0x0e, 0x00, 0x00, n & 0xff, (n >> 8) & 0xff, 0x00])

LOAD_ADDR = 24576  # 0x6000 — SAMDOS-coexisting code address

stmt_clear = bytes([0xb3, 0x20]) + str(LOAD_ADDR - 1).encode() + num(LOAD_ADDR - 1)
stmt_load  = (bytes([0x95, 0x20, 0x22]) + b"stub" + bytes([0x22, 0x20, 0xff, 0x6c, 0x20])
              + str(LOAD_ADDR).encode() + num(LOAD_ADDR))
stmt_call  = bytes([0xe4, 0x20]) + str(LOAD_ADDR).encode() + num(LOAD_ADDR)

line_body = stmt_clear + b"\x3a" + stmt_load + b"\x3a" + stmt_call + b"\x0d"
line_len = len(line_body)
BASIC_BODY = bytes([0x00, 0x0a, line_len & 0xff, (line_len >> 8) & 0xff]) + line_body + b"\xff"

output = sys.argv[1]
with open(output, "r+b") as f:
    img = bytearray(f.read())
    assert len(img) == 819200, f"Expected 819200-byte image, got {len(img)}"

    # --- Directory entry at offset 0 ---
    entry = bytearray(256)
    entry[0x00] = 0x10                          # SAM BASIC file type
    name = b"auto      "                         # 10 bytes, space-padded
    entry[0x01:0x0b] = name
    entry[0x0b] = 0x00                          # sector count high byte
    entry[0x0c] = 0x01                          # sector count low byte (BE: 1 sector)
    entry[0x0d] = 4                             # first sector track
    entry[0x0e] = 1                             # first sector number
    # Sector address map: 195 bytes at 0x0f.
    # bit_offset = (track & 0x7f)*10 + sector - 1 + ((track & 0x80)>>7)*800 - 40
    # For track=4, sector=1: 40 + 0 + 0 - 40 = 0  => byte_offset=0, bit_mask=1
    entry[0x0f] = 0x01                          # bit 0 = track 4 sector 1
    entry[0xf0] = len(BASIC_BODY) & 0xff
    entry[0xf1] = (len(BASIC_BODY) >> 8) & 0xff
    entry[0xf2] = 0xff                          # ExecutionAddressDiv16K (unused for BASIC)
    entry[0xf3] = 10 & 0xff                     # SAMBASICStartLine low = 10
    entry[0xf4] = (10 >> 8) & 0xff             # SAMBASICStartLine high = 0
    img[0:256] = entry

    # --- Sector data at track 4, sector 1 ---
    # Sector offset = (track>>7)*5120 + (sector-1)*512 + (track&0x7f)*10240
    # = 0 + 0 + 40960 = 40960
    sector_offset = 40960
    sd = bytearray(512)
    # 9-byte samfile file header
    header = bytearray(9)
    header[0] = 0x10                            # type: SAM BASIC
    header[1] = len(BASIC_BODY) & 0xff
    header[2] = (len(BASIC_BODY) >> 8) & 0xff
    # PageOffset, reserved bytes, Pages, StartPage all zero for BASIC
    sd[0:9] = header
    sd[9:9+len(BASIC_BODY)] = BASIC_BODY
    # Bytes 510-511 = next-sector (track, sector); 0,0 means no next sector
    img[sector_offset:sector_offset + 512] = sd

    f.seek(0)
    f.write(img)

print(f"BASIC 'auto' file written ({len(BASIC_BODY)} bytes, track 4 sector 1)")
EOF

# ---------------------------------------------------------------------------
# Add the code stub as a file named 'stub' (type=code, load/exec at 0x6000).
# samfile sees the sector address map of the 'auto' entry (bit 0 set = track 4
# sector 1 used) and allocates track 4 sector 2 for the stub.
# The stub file is named by its basename via samfile; rename so it lands as
# 'stub' rather than 'stub.bin'.
# ---------------------------------------------------------------------------
cp build/stub.bin /tmp/stub
samfile add -i "$output" -f /tmp/stub -c -l 24576 -e 24576
rm -f /tmp/stub

# ---------------------------------------------------------------------------
# Add the input fixture as a raw (non-code) file named 'IN'.
# In M0 the stub ignores its content; Task 7 will read it via file I/O.
# We add it as a code file at a dummy load address (it won't be executed).
# ---------------------------------------------------------------------------
cp "$input" /tmp/IN
samfile add -i "$output" -f /tmp/IN -c -l 24576
rm -f /tmp/IN

echo "Built $output"
samfile ls -i "$output"
