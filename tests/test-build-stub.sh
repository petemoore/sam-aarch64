#!/usr/bin/env bash
# Verifies pyz80 builds src/stub.asm into a non-empty binary at build/stub.bin.

set -euo pipefail

cd "$(dirname "$0")/.."

rm -f build/stub.bin
make stub

if [ ! -s build/stub.bin ]; then
    echo "FAIL: build/stub.bin missing or empty"
    exit 1
fi

# Sanity: not absurdly large for a halt-only program (<256 bytes).
size=$(wc -c < build/stub.bin)
if [ "$size" -gt 256 ]; then
    echo "FAIL: stub.bin is $size bytes — expected <256 for a halt program"
    exit 1
fi

echo "PASS (stub.bin = $size bytes)"
