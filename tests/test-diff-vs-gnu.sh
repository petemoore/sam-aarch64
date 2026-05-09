#!/usr/bin/env bash
# Exercises tools/diff-vs-gnu.sh on the nop fixture against the stub output.

set -euo pipefail

cd "$(dirname "$0")/.."

# Re-run the stub to make sure build/out.bin exists and is current.
./tests/test-stub-emits-nop.sh > /dev/null

# Now diff against GNU as.
if ! ./tools/diff-vs-gnu.sh tests/fixtures/nop.s build/out.bin; then
    echo "FAIL: diff-vs-gnu reported divergence"
    exit 1
fi

echo "PASS"
