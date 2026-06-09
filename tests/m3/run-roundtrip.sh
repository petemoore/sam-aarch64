#!/usr/bin/env bash
# tests/m3/run-roundtrip.sh — sweep every fixture under tests/m3/sources/
# and byte-diff the M3 round-trip output against `aarch64-{none-elf,linux-gnu}-as`.
#
# Per docs/specs/2026-05-24-m3-z80-emitter-design.md §3 (Layer 2).
# Invoked by `make test-m3` and the m3 CI job.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Build all prerequisites once at the top so per-fixture runs are quick.
make -s sam-aarch64 enctab m3-asm build-m3-disk

fail=0
total=0
for src in tests/m3/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    if "$ROOT/tools/run-m3-roundtrip.sh" "$src" > "/tmp/m3-${base}.log" 2>&1; then
        echo "OK:   $base"
    else
        rc=$?
        echo "FAIL: $base (rc=$rc)"
        tail -12 "/tmp/m3-${base}.log" | sed 's/^/  /'
        fail=$((fail + 1))
    fi
done

echo "---"
echo "$((total - fail))/$total M3 fixtures matched"
exit "$fail"
