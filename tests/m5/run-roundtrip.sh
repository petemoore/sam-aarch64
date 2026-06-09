#!/usr/bin/env bash
# tests/m5/run-roundtrip.sh — sweep every fixture under tests/m5/sources/
# and byte-diff the M5 round-trip output against
# `aarch64-*-as + ld -Ttext=0 + objcopy -O binary`.
#
# Per docs/specs/2026-05-27-m5-compound-operands-directives-design.md §3.
# Invoked by `make test-m5` and the m5 CI job (added in a later PR).
#
# Pattern mirrors tests/m4/run-roundtrip.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Build all prerequisites once at the top so per-fixture runs are quick.
make -s sam-aarch64 enctab m3-asm build-m3-disk

fail=0
total=0
for src in tests/m5/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    if "$ROOT/tools/run-m5-roundtrip.sh" "$src" > "/tmp/m5-${base}.log" 2>&1; then
        echo "OK:   $base"
    else
        rc=$?
        echo "FAIL: $base (rc=$rc)"
        tail -12 "/tmp/m5-${base}.log" | sed 's/^/  /'
        fail=$((fail + 1))
    fi
done

echo "---"
echo "$((total - fail))/$total M5 fixtures matched"
exit "$fail"
