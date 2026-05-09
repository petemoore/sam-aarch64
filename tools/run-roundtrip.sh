#!/usr/bin/env bash
# End-to-end round-trip test for one .s fixture.
#
# Usage: run-roundtrip.sh <fixture.s>

set -euo pipefail

fixture="$1"

cd "$(dirname "$0")/.."

echo "=== Round-trip: $fixture ==="

make stub
./tools/build-disk.sh "$fixture" build/test.mgt
./tools/run-simcoupe.sh build/test.mgt
./tools/extract-output.sh build/test.mgt build/out.bin
./tools/diff-vs-gnu.sh "$fixture" build/out.bin

echo "=== PASS: $fixture ==="
