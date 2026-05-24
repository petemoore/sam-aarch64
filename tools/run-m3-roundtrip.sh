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

# Mac-side tools.  The assembler binary itself is a per-variant input:
# ASSEMBLER_BIN selects between the test build (default — includes
# boot-time self-tests) and the production build (set
# ASSEMBLER_BIN=build/assembler-prod.bin).  Caller is responsible for
# ensuring the chosen binary is built; we don't `make m3-asm` here
# because that would force the test variant even when the caller wants
# the prod variant.
ASSEMBLER_BIN="${ASSEMBLER_BIN:-$ROOT/build/assembler.bin}"
make -s text2bin enctab build-m3-disk

if [ ! -f "$ASSEMBLER_BIN" ]; then
    echo "ERROR: assembler binary not found: $ASSEMBLER_BIN" >&2
    echo "  (build it with 'make m3-asm' or 'make m3-asm-prod')" >&2
    exit 2
fi

# 1. text2bin → INPUT.tbn
echo "--- text2bin ---"
"$ROOT/build/text2bin" -o "build/${base}.tbn" "$fixture"

# 2. build-m3-disk with the .tbn as IN.
#
# The -p13 flag (page-13 payload for the paged_call self-test) is not
# passed here because the BUILD_TESTS variant's
# `call run_paged_call_self_tests` is currently disabled pending the
# boot-hang investigation in
# docs/notes/2026-05-28-plan-pr1-stuck.md.  When the self-test is
# re-enabled, add `-p13 build/page13_test_payload.bin` to the
# command line below.
echo "--- build-m3-disk ---"
"$ROOT/build/build-m3-disk" \
    "$ASSEMBLER_BIN" build/enctab.enc \
    "build/${base}.tbn" \
    "build/${base}.mgt"

# 3. Run SimCoupé. The wrapper exits ~1 s in either case (both paths
#    do `DI; HALT`) and writes a status file containing "OK" or "FAIL"
#    captured from the SAM's parallel printer channel.  See
#    src/m3/print.asm + tools/run-simcoupe.sh.
echo "--- simcoupe ---"
"$ROOT/tools/run-simcoupe.sh" "build/${base}.mgt" "build/${base}.status.log"

# 3a. Check the SAM-side status banner before bothering with the byte
#     compare.  A "FAIL" status indicates a boot-time self-test
#     failure in the assembler — no point comparing OUT bytes in that
#     case (OUT may be stale from a previous run or absent).
status=$(tr -d '\r\n ' < "build/${base}.status.log" || true)
if [ "$status" != "OK" ]; then
    echo "FAIL: $base — assembler emitted status '${status}' (expected 'OK')"
    if [ -s "build/${base}.status.log" ]; then
        echo "  printer log:"
        sed 's/^/    /' "build/${base}.status.log"
    else
        echo "  (printer log empty — SimCoupé may have hung or crashed before status emit)"
    fi
    exit 1
fi

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
