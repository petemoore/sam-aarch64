#!/usr/bin/env bash
# tests/paged/run-roundtrip.sh — sweep every fixture under tests/paged/sources/
# and byte-diff the round-trip output against
# `aarch64-*-as + ld -Ttext=0 + objcopy -O binary`.
#
# Per docs/specs/paged-out-design.md.  Paged fixtures exercise large output
# via the paged-OUT machinery (the pool-run section-B emit with per-byte
# LMPR bracket, page advances at each 16 KB boundary, HSAVE auto-paging
# across &C000).  inst_out_over32k.s crosses the old two-page / 32 KB
# ceiling (i24).  See also docs/specs/samdos-file-io.md.
#
# Invoked by `make test-paged` / `make ci-paged` and the paged CI job.
# Pattern mirrors tests/operands/run-roundtrip.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Build all prerequisites once at the top so per-fixture runs are quick.
make -s sam-aarch64 enctab assembler build-disk

fail=0
total=0
for src in tests/paged/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    if "$ROOT/tools/run-roundtrip.sh" paged "$src" > "/tmp/paged-${base}.log" 2>&1; then
        echo "OK:   $base"
    else
        rc=$?
        echo "FAIL: $base (rc=$rc)"
        tail -12 "/tmp/paged-${base}.log" | sed 's/^/  /'
        fail=$((fail + 1))
    fi
done

echo "---"
echo "$((total - fail))/$total paged fixtures matched"
exit "$fail"
