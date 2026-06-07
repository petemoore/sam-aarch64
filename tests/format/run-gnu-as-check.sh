#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
AS="${AARCH64_AS:-aarch64-none-elf-as}"

if ! command -v "$AS" >/dev/null; then
    echo "missing $AS — install aarch64-none-elf-as or set AARCH64_AS"
    exit 2
fi

fail=0
for f in "$ROOT"/tests/format/sources/*.s; do
    if ! "$AS" -o /dev/null "$f" 2>/dev/null; then
        echo "FAIL: GNU as rejected $f"
        "$AS" -o /dev/null "$f" || true
        fail=1
    fi
done

if [ "$fail" -ne 0 ]; then
    echo "GNU as cross-check failed"
    exit 1
fi

echo "GNU as cross-check passed for $(ls "$ROOT"/tests/format/sources/*.s | wc -l) fixtures"
