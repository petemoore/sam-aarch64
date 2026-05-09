#!/usr/bin/env bash
# After running the stub, build/out.bin should contain exactly 4 bytes:
#   1f 20 03 d5  (little-endian aarch64 NOP).

set -euo pipefail

cd "$(dirname "$0")/.."

make stub
./tools/build-disk.sh tests/fixtures/nop.s build/test.mgt
./tools/run-simcoupe.sh build/test.mgt
./tools/extract-output.sh build/test.mgt build/out.bin

actual=$(xxd -p build/out.bin)
expected="1f2003d5"

if [ "$actual" != "$expected" ]; then
    echo "FAIL: out.bin = $actual, expected $expected"
    xxd build/out.bin
    exit 1
fi

echo "PASS (out.bin = $actual)"
