#!/usr/bin/env bash
# build-spectrum4-release.sh — reproducibly build spectrum4's release.img
# from sources using our toolchain, then compare against the GNU oracle.
#
# Pipeline:
#
#   1. Build the sam-aarch64 binary.
#   2. sam-aarch64 -flatten release.target → release.bin (our spectrum4.img).
#         (flatten step folds spectrum4's linker script + section layout
#          into a single linear record stream; see flatten.go for the
#          layout encoded in this pass.)
#   3. Build the GNU oracle (release.gnu.img) for byte-match comparison.
#   4. cmp release.bin release.gnu.img — exit 0 on match, non-zero on
#      mismatch (with a brief summary).
#
# This script assumes:
#   - aarch64-none-elf-{as,ld,objcopy} are on PATH (binutils ≥ 2.45).
#   - The spectrum4 source tree lives at ~/git/spectrum4/src/spectrum4
#     (overridable via SPECTRUM4_SRC).
#
# Usage:  scripts/build-spectrum4-release.sh
# Output: build/release.bin and (on success) "BYTE-MATCH OK"
#         build/release.gnu.img is left for inspection.

set -euo pipefail

SAMDIR="${SAMDIR:-$(cd "$(dirname "$0")/.." && pwd)}"
SPECTRUM4_SRC="${SPECTRUM4_SRC:-$HOME/git/spectrum4/src/spectrum4}"
BUILD="${SAMDIR}/build"

if [[ ! -d "$SPECTRUM4_SRC" ]]; then
    echo "spectrum4 source not found at $SPECTRUM4_SRC" >&2
    echo "set SPECTRUM4_SRC to override" >&2
    exit 2
fi

mkdir -p "$BUILD"

# Toolchain selection.  Prefer aarch64-none-elf-* (Pete's macOS install),
# fall back to aarch64-linux-gnu-* (dev container / CI).  Override via the
# AARCH64_AS / AARCH64_LD / AARCH64_OBJCOPY env vars (mirrors
# tools/run-m6-roundtrip.sh).
AS="${AARCH64_AS:-aarch64-none-elf-as}"
LD="${AARCH64_LD:-aarch64-none-elf-ld}"
OBJCOPY="${AARCH64_OBJCOPY:-aarch64-none-elf-objcopy}"
if ! command -v "$AS" >/dev/null 2>&1; then
    AS="aarch64-linux-gnu-as"; LD="aarch64-linux-gnu-ld"; OBJCOPY="aarch64-linux-gnu-objcopy"
fi

# 1. Build tools.
echo "[1/4] Building sam-aarch64..."
make -C "$SAMDIR" sam-aarch64 >/dev/null

# 2. Run sam-aarch64 -flatten on release.target → release.bin.
echo "[2/4] sam-aarch64 -flatten release.target → release.bin"
"$BUILD/sam-aarch64" -flatten \
    -I "$SPECTRUM4_SRC" \
    -I "$SPECTRUM4_SRC/kernel" \
    -I "$SPECTRUM4_SRC/roms" \
    -I "$SPECTRUM4_SRC/tests" \
    -I "$SPECTRUM4_SRC/demo" \
    -I "$SPECTRUM4_SRC/libextra" \
    -origin 0xfffffff000000000 \
    -o "$BUILD/release.bin" \
    "$SPECTRUM4_SRC/targets/release.target"
echo "       $(wc -c <"$BUILD/release.bin") bytes"

# 3. Build the GNU oracle.
echo "[3/4] Building GNU oracle release.gnu.img..."
GNUTMP="$(mktemp -d)"
trap 'rm -rf "$GNUTMP"' EXIT
"$AS" \
    -I "$SPECTRUM4_SRC" \
    -I "$SPECTRUM4_SRC/kernel" \
    -I "$SPECTRUM4_SRC/roms" \
    -I "$SPECTRUM4_SRC/tests" \
    -I "$SPECTRUM4_SRC/demo" \
    -I "$SPECTRUM4_SRC/libextra" \
    -o "$GNUTMP/release.o" \
    "$SPECTRUM4_SRC/targets/release.target"

# Link without cortex-a53 errata workarounds first; those workarounds
# insert NOPs at specific instruction patterns and would be a deferred
# concern if our output matches the no-errata oracle.
"$LD" \
    --no-warn-rwx-segments \
    -T "$SPECTRUM4_SRC/kernel/spectrum4.ld" \
    -o "$GNUTMP/release.elf" \
    "$GNUTMP/release.o"

"$OBJCOPY" "$GNUTMP/release.elf" -O binary "$BUILD/release.gnu.img"
echo "       $(wc -c <"$BUILD/release.gnu.img") bytes"

# 4. Compare.
echo "[4/4] cmp release.bin release.gnu.img"
if cmp -s "$BUILD/release.bin" "$BUILD/release.gnu.img"; then
    echo
    echo "BYTE-MATCH OK"
    exit 0
fi

echo
echo "BYTE-MATCH MISMATCH — first differing bytes:"
cmp -l "$BUILD/release.bin" "$BUILD/release.gnu.img" | head -10 || true
echo
echo "       our size: $(wc -c <"$BUILD/release.bin") bytes"
echo "       gnu size: $(wc -c <"$BUILD/release.gnu.img") bytes"
exit 1
