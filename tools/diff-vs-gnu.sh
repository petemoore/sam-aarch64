#!/usr/bin/env bash
# Assembles a .s fixture with aarch64-none-elf-as, extracts .text via objcopy,
# byte-compares against a candidate output. Exit 0 = match, non-zero = differ.
#
# Usage: diff-vs-gnu.sh <fixture.s> <candidate.bin>

set -euo pipefail

fixture="$1"
candidate="$2"

cd "$(dirname "$0")/.."

mkdir -p build/oracle

aarch64-none-elf-as "$fixture" -o build/oracle/expected.o
aarch64-none-elf-objcopy -O binary build/oracle/expected.o build/oracle/expected.bin

if cmp -s build/oracle/expected.bin "$candidate"; then
    echo "MATCH: $candidate == GNU as output for $fixture"
    exit 0
fi

echo "DIVERGE:"
echo "  expected: $(xxd -p build/oracle/expected.bin)"
echo "  actual:   $(xxd -p "$candidate")"
echo
echo "Full diff:"
diff <(xxd build/oracle/expected.bin) <(xxd "$candidate") || true
exit 1
