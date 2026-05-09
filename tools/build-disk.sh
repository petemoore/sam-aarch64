#!/usr/bin/env bash
# Constructs a .mgt disk image containing:
#   - a tokenised BASIC 'auto' file that auto-runs on boot (LOAD "stub" CODE : CALL 32768)
#   - the stub binary as a code file named 'stub' (loaded at 0x8000)
#   - the input fixture as a raw file named 'IN' on the disk
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

BASIC_BODY = bytes.fromhex(
    "000a1900"           # line 10 (BE), line length 0x0019 = 25 bytes (LE)
    "9520227374756222"   # LOAD " s t u b "
    "20ff6c"             # _ CODE (0xff-prefixed token)
    "3a"                 # : (statement separator)
    "e4"                 # CALL
    "3332373638"         # "32768" (ASCII digits)
    "0e0000008000"       # numeric encoding for 32768
    "0d"                 # line terminator
    "ff"                 # file terminator
)
assert len(BASIC_BODY) == 30, f"Expected 30 bytes, got {len(BASIC_BODY)}"

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
    entry[0xf0] = 30 & 0xff                     # LengthMod16K low
    entry[0xf1] = (30 >> 8) & 0xff             # LengthMod16K high
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
    header[1] = 30 & 0xff                       # LengthMod16K low
    header[2] = (30 >> 8) & 0xff               # LengthMod16K high
    # PageOffset, reserved bytes, Pages, StartPage all zero for BASIC
    sd[0:9] = header
    sd[9:39] = BASIC_BODY
    # Bytes 510-511 = next-sector (track, sector); 0,0 means no next sector
    img[sector_offset:sector_offset + 512] = sd

    f.seek(0)
    f.write(img)

print("BASIC 'auto' file written (30 bytes, track 4 sector 1)")
EOF

# ---------------------------------------------------------------------------
# Add the code stub as a file named 'stub' (type=code, load/exec at 0x8000).
# samfile sees the sector address map of the 'auto' entry (bit 0 set = track 4
# sector 1 used) and allocates track 4 sector 2 for the stub.
# The stub file is named by its basename via samfile; rename so it lands as
# 'stub' rather than 'stub.bin'.
# ---------------------------------------------------------------------------
cp build/stub.bin /tmp/stub
samfile add -i "$output" -f /tmp/stub -c -l 32768 -e 32768
rm -f /tmp/stub

# ---------------------------------------------------------------------------
# Add the input fixture as a raw (non-code) file named 'IN'.
# In M0 the stub ignores its content; Task 7 will read it via file I/O.
# We add it as a code file at a dummy load address (it won't be executed).
# ---------------------------------------------------------------------------
cp "$input" /tmp/IN
samfile add -i "$output" -f /tmp/IN -c -l 32768
rm -f /tmp/IN

echo "Built $output"
samfile ls -i "$output"
