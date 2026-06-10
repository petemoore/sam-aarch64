#!/usr/bin/env bash
# tests/operands/run-roundtrip.sh — sweep every fixture under tests/operands/sources/
# and byte-diff the round-trip output against
# `aarch64-*-as + ld -Ttext=0 + objcopy -O binary`.
#
# Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-27-m5-compound-operands-directives-design.md §3.
# Invoked by `make test-operands` and the operands CI job.
#
# Pattern mirrors tests/symbols/run-roundtrip.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Build all prerequisites once at the top so per-fixture runs are quick.
make -s sam-aarch64 enctab assembler build-disk

fail=0
total=0
for src in tests/operands/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    if "$ROOT/tools/run-roundtrip.sh" operands "$src" > "/tmp/operands-${base}.log" 2>&1; then
        echo "OK:   $base"
    else
        rc=$?
        echo "FAIL: $base (rc=$rc)"
        tail -12 "/tmp/operands-${base}.log" | sed 's/^/  /'
        fail=$((fail + 1))
    fi
done

echo "---"
echo "$((total - fail))/$total operands fixtures matched"
exit "$fail"
