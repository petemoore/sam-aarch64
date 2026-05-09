#!/usr/bin/env bash
# Unit test for tools/diff-vs-gnu.sh — no SimCoupé dependency.
# Synthesises known-good and known-bad candidate binaries and checks that
# diff-vs-gnu.sh exits 0 / non-zero accordingly.
#
# aarch64 NOP encodes as 1f 20 03 d5 (little-endian).

set -euo pipefail

cd "$(dirname "$0")/.."

FIXTURE="tests/fixtures/nop.s"
PASS=0
FAIL=0

# --- positive case: candidate matches GNU as output -------------------------
printf '\x1f\x20\x03\xd5' > /tmp/diff-vs-gnu-unit-match.bin

if ./tools/diff-vs-gnu.sh "$FIXTURE" /tmp/diff-vs-gnu-unit-match.bin; then
    echo "PASS (positive case: MATCH correctly detected)"
    PASS=$((PASS + 1))
else
    echo "FAIL (positive case: expected MATCH but got non-zero exit)"
    FAIL=$((FAIL + 1))
fi

# --- negative case: candidate diverges from GNU as output -------------------
printf '\x00\x00\x00\x00' > /tmp/diff-vs-gnu-unit-diverge.bin

if ./tools/diff-vs-gnu.sh "$FIXTURE" /tmp/diff-vs-gnu-unit-diverge.bin; then
    echo "FAIL (negative case: expected DIVERGE but got zero exit)"
    FAIL=$((FAIL + 1))
else
    echo "PASS (negative case: DIVERGE correctly detected)"
    PASS=$((PASS + 1))
fi

# --- summary ----------------------------------------------------------------
echo ""
echo "Results: $PASS passed, $FAIL failed"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
