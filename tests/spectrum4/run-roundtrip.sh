#!/usr/bin/env bash
# For every fixture under tests/spectrum4/sources/, build via
# sam-aarch64 and byte-compare against
# `aarch64-none-elf-as` + `ld -Ttext=0` + `objcopy -O binary`.
#
# Mirrors tests/format/run-refenc-roundtrip.sh but for the spectrum4
# fixture corpus: real (or near-real) routines lifted from the
# spectrum4 kernel that exercise our pipeline against actual
# kernel-style code.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
AS="${AARCH64_AS:-aarch64-none-elf-as}"
LD="${AARCH64_LD:-aarch64-none-elf-ld}"
OBJCOPY="${AARCH64_OBJCOPY:-aarch64-none-elf-objcopy}"

if ! command -v "$AS" >/dev/null; then
    echo "missing $AS — install aarch64-none-elf-as or set AARCH64_AS" >&2
    exit 2
fi
if ! command -v "$LD" >/dev/null; then
    LD="${AS%-as}-ld"
fi
if ! command -v "$OBJCOPY" >/dev/null; then
    OBJCOPY="${AS%-as}-objcopy"
fi

cd "$ROOT"
mkdir -p build/spectrum4-roundtrip

SAM="$ROOT/build/sam-aarch64"

if [ ! -x "$SAM" ]; then
    echo "sam-aarch64 not built; run: make sam-aarch64" >&2
    exit 2
fi

fail=0
total=0
for src in "$ROOT"/tests/spectrum4/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    work="$ROOT/build/spectrum4-roundtrip/$base"
    mkdir -p "$work"

    "$SAM" -o "$work/ours.bin" "$src"

    "$AS" "$src" -o "$work/gnu.o"

    if [ ! -s "$work/ours.bin" ]; then
        : > "$work/gnu.bin"
    else
        "$LD" -Ttext=0 "$work/gnu.o" -o "$work/gnu.linked" 2>/dev/null
        "$OBJCOPY" -O binary "$work/gnu.linked" "$work/gnu.bin"
    fi

    if cmp -s "$work/ours.bin" "$work/gnu.bin"; then
        echo "OK:   $base"
    else
        ours_sz=$(wc -c < "$work/ours.bin" | tr -d ' ')
        gnu_sz=$(wc -c < "$work/gnu.bin" | tr -d ' ')
        echo "FAIL: $base (ours=${ours_sz}B gnu=${gnu_sz}B)"
        fail=$((fail + 1))
    fi
done

echo "---"
echo "$((total - fail))/$total spectrum4 fixtures matched"
if [ "$fail" -ne 0 ]; then
    exit 1
fi
