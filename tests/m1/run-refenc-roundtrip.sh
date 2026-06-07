#!/usr/bin/env bash
# Layer 3 round-trip: for every fixture under tests/m1/sources/,
# build via sam-aarch64 and byte-compare against
# `aarch64-none-elf-as` + `ld -Ttext=0` + `objcopy -O binary`.
#
# The `ld -Ttext=0` step is what makes :lo12: / :hi12: / etc.
# relocations resolve to the same values sam-aarch64 bakes in — per
# the M1 spec §5.1, this is the prescribed oracle for fixtures
# carrying relocations.
#
# Empty fixtures are special-cased: an empty source has no
# sections, so `ld` produces an objcopy-incompatible image. For
# those the expected output is an empty binary.

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
    # On the Linux dev container the tools have linux-gnu names.
    LD="${AS%-as}-ld"
fi
if ! command -v "$OBJCOPY" >/dev/null; then
    OBJCOPY="${AS%-as}-objcopy"
fi

cd "$ROOT"
mkdir -p build/refenc-roundtrip

SAM="$ROOT/build/sam-aarch64"

if [ ! -x "$SAM" ]; then
    echo "sam-aarch64 not built; run: make sam-aarch64" >&2
    exit 2
fi

fail=0
total=0
for src in "$ROOT"/tests/m1/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    work="$ROOT/build/refenc-roundtrip/$base"
    mkdir -p "$work"

    # Build via our pipeline.
    "$SAM" -o "$work/ours.bin" "$src"

    # Build via GNU pipeline.
    "$AS" "$src" -o "$work/gnu.o"

    if [ ! -s "$work/ours.bin" ]; then
        # sam-aarch64 emitted nothing; expect an empty oracle binary.
        : > "$work/gnu.bin"
    else
        # Link and convert. The "_start" warning is harmless.
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
echo "$((total - fail))/$total fixtures matched"
if [ "$fail" -ne 0 ]; then
    exit 1
fi
