#!/usr/bin/env bash
# Verifies tools/check-toolchain.sh exits 0 when all tools are present
# and exits non-zero with a clear error when any tool is missing.

set -uo pipefail

cd "$(dirname "$0")/.."

# Happy path: real PATH should have all tools (assuming dev env is set up).
if ! ./tools/check-toolchain.sh > /tmp/check-out 2>&1; then
    echo "FAIL: check-toolchain.sh exited non-zero on a healthy environment"
    cat /tmp/check-out
    exit 1
fi

# Unhappy path: empty PATH should fail and mention the missing tool.
if PATH=/nonexistent ./tools/check-toolchain.sh > /tmp/check-out 2>&1; then
    echo "FAIL: check-toolchain.sh exited 0 with empty PATH"
    cat /tmp/check-out
    exit 1
fi
if ! grep -qi "missing" /tmp/check-out; then
    echo "FAIL: error output did not mention 'missing'"
    cat /tmp/check-out
    exit 1
fi

echo "PASS"
