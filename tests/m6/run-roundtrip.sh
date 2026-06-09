#!/usr/bin/env bash
# tests/m6/run-roundtrip.sh — sweep every fixture under tests/m6/sources/
# and byte-diff the M6 round-trip output against
# `aarch64-*-as + ld -Ttext=0 + objcopy -O binary`.
#
# Per docs/specs/2026-05-27-m6-paged-out-design.md (PR 1 of M6).  M6
# fixtures exercise > 2 KB output via the paged-OUT machinery
# (sections-B emit with LMPR_ENCTAB low zone, LMPR_OUT_HIGH high zone,
# HSAVE auto-paging across &C000).  See also
# docs/specs/2026-05-27-samdos-save-idiom.md.
#
# Invoked by `make test-m6` / `make ci-m6` and the m6 CI job.  Pattern
# mirrors tests/m5/run-roundtrip.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Build all prerequisites once at the top so per-fixture runs are quick.
make -s sam-aarch64 enctab m3-asm build-m3-disk

fail=0
total=0
for src in tests/m6/sources/*.s; do
    base=$(basename "$src" .s)
    total=$((total + 1))
    if "$ROOT/tools/run-m6-roundtrip.sh" "$src" > "/tmp/m6-${base}.log" 2>&1; then
        echo "OK:   $base"
    else
        rc=$?
        echo "FAIL: $base (rc=$rc)"
        tail -12 "/tmp/m6-${base}.log" | sed 's/^/  /'
        fail=$((fail + 1))
    fi
done

echo "---"
echo "$((total - fail))/$total M6 fixtures matched"
exit "$fail"
