#!/usr/bin/env bash
# tests/symbols/run-roundtrip.sh — sweep every fixture under tests/symbols/sources/
# and byte-diff the round-trip output against
# `aarch64-*-as + ld -Ttext=0 + objcopy -O binary`.
#
# Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m4-symbols-multipass-design.md §3.
# Invoked by `make test-symbols` and the symbols CI job.
#
# Pattern mirrors tests/core/run-roundtrip.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Build all prerequisites once at the top so per-fixture runs are quick.
make -s sam-aarch64 enctab assembler build-disk

fail=0
total=0
for src in tests/symbols/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    if "$ROOT/tools/run-roundtrip.sh" symbols "$src" > "/tmp/symbols-${base}.log" 2>&1; then
        echo "OK:   $base"
    else
        rc=$?
        echo "FAIL: $base (rc=$rc)"
        tail -12 "/tmp/symbols-${base}.log" | sed 's/^/  /'
        fail=$((fail + 1))
    fi
done

echo "---"
echo "$((total - fail))/$total symbols fixtures matched"
exit "$fail"
