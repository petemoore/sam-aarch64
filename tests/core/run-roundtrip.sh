#!/usr/bin/env bash
# tests/core/run-roundtrip.sh — sweep every fixture under tests/core/sources/
# and byte-diff the round-trip output against `aarch64-{none-elf,linux-gnu}-as`.
#
# Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m3-z80-emitter-design.md §3 (Layer 2).
# Invoked by `make test-core` and the core CI job.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Build all prerequisites once at the top so per-fixture runs are quick.
make -s sam-aarch64 enctab assembler build-disk

fail=0
total=0
for src in tests/core/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    if "$ROOT/tools/run-roundtrip.sh" core "$src" > "/tmp/core-${base}.log" 2>&1; then
        echo "OK:   $base"
    else
        rc=$?
        echo "FAIL: $base (rc=$rc)"
        tail -12 "/tmp/core-${base}.log" | sed 's/^/  /'
        fail=$((fail + 1))
    fi
done

echo "---"
echo "$((total - fail))/$total core fixtures matched"
exit "$fail"
