#!/usr/bin/env bash
# run-m3-roundtrip.sh — end-to-end M3 fixture round-trip driver.
#
# Pipeline per docs/specs/2026-05-24-m3-z80-emitter-design.md §3 (Layer 2):
#
#   1. text2bin INPUT.s   → INPUT.tbn
#   2. build-m3-disk assembler.bin enctab.enc INPUT.tbn → OUT.mgt
#   3. SimCoupé runs the disk; M3 reads IN, writes OUT.
#   4. samfile cat OUT → ours.bin
#   5. aarch64-{none,linux-gnu}-as INPUT.s | objcopy -O binary → gnu.bin
#   6. cmp ours.bin gnu.bin
#
# Empty fixtures are special-cased: an empty source produces an empty
# OUT (no HSAVE call); the GNU oracle is `:`.
#
# Usage: run-m3-roundtrip.sh <fixture.s>

set -euo pipefail

if [ $# -ne 1 ]; then
    echo "usage: $0 <fixture.s>" >&2
    exit 2
fi

fixture="$1"
base=$(basename "$fixture" .s)
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

cd "$ROOT"

# -------------------------------------------------------------------------
# Toolchain selection.
# -------------------------------------------------------------------------
AS="${AARCH64_AS:-aarch64-none-elf-as}"
OBJCOPY="${AARCH64_OBJCOPY:-aarch64-none-elf-objcopy}"

if ! command -v "$AS" >/dev/null 2>&1; then
    # Linux-style binutils names fall through here (dev container,
    # ubuntu CI runners).
    AS="aarch64-linux-gnu-as"
    OBJCOPY="aarch64-linux-gnu-objcopy"
fi

if ! command -v "$AS" >/dev/null 2>&1; then
    echo "ERROR: aarch64-{none-elf,linux-gnu}-as not found on PATH" >&2
    exit 2
fi

# -------------------------------------------------------------------------
# Build the Mac-side artefacts.
# -------------------------------------------------------------------------
echo "=== M3 round-trip: $fixture ==="

mkdir -p build

# Mac-side tools and the assembler binary.
make -s text2bin enctab m3-asm build-m3-disk

# 1. text2bin → INPUT.tbn
echo "--- text2bin ---"
"$ROOT/build/text2bin" -o "build/${base}.tbn" "$fixture"

# 2. build-m3-disk with the .tbn as IN.
echo "--- build-m3-disk ---"
"$ROOT/build/build-m3-disk" \
    build/assembler.bin build/enctab.enc \
    "build/${base}.tbn" \
    "build/${base}.mgt"

# 3. Run SimCoupé. The wrapper handles the timeout-vs-clean-exit
#    semantics — see tools/run-simcoupe.sh.
echo "--- simcoupe ---"
"$ROOT/tools/run-simcoupe.sh" "build/${base}.mgt"

# 4. Extract OUT.
echo "--- extract OUT ---"
SAMFILE="${SAMFILE:-samfile}"
if ! command -v "$SAMFILE" >/dev/null 2>&1; then
    # Some environments don't have samfile on PATH but do at a
    # well-known location.
    if [ -x "$HOME/git/samfile/samfile" ]; then
        SAMFILE="$HOME/git/samfile/samfile"
    else
        echo "ERROR: samfile not found on PATH" >&2
        exit 2
    fi
fi
"$SAMFILE" cat -i "build/${base}.mgt" -f OUT > "build/${base}.bin"

# 5. GNU oracle.
echo "--- GNU oracle ---"
"$AS" "$fixture" -o "build/${base}.o"
"$OBJCOPY" -O binary "build/${base}.o" "build/${base}.gnu.bin"

# 6. Byte-compare.
echo "--- cmp ---"
if cmp -s "build/${base}.bin" "build/${base}.gnu.bin"; then
    bytes=$(wc -c < "build/${base}.bin" | tr -d ' ')
    echo "PASS: $base ($bytes bytes)"
else
    ours_sz=$(wc -c < "build/${base}.bin" | tr -d ' ')
    gnu_sz=$(wc -c < "build/${base}.gnu.bin" | tr -d ' ')
    echo "FAIL: $base"
    echo "  ours: $ours_sz bytes"
    echo "  gnu : $gnu_sz bytes"
    echo "  ours hex:"
    od -An -tx1 "build/${base}.bin" | head -4
    echo "  gnu hex:"
    od -An -tx1 "build/${base}.gnu.bin" | head -4
    exit 1
fi
