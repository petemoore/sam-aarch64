#!/usr/bin/env bash
# check-code-budget.sh — fail the build when an assembler variant grows
# into the stack page.
#
# Both assembler variants link at org &8000.  Their scratch/stack sits at
# &C000-&C0FF (SP = &C100).  If a variant's code_end reaches &C000 it
# collides with the loader spillover + stack, and the failure manifests
# as a *silent deterministic boot-hang* (rc=124) with no diagnostic —
# exactly the test-variant fragility class that bit PR #43 and that
# memory/feedback_test_variant_fragility.md tracks.
#
# This script turns that silent cliff into a CI/build failure WITH A
# NUMBER ("code_end &C0xx ≥ &C000 — N bytes over").  It is the structural
# fix recommended in https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-29-go-harness-fidelity-investigation.md
# and promoted into the M6-closure CI gate (PR-5).
#
# Usage:
#   tools/check-code-budget.sh                  # checks both default binaries
#   tools/check-code-budget.sh <binary> <label> # checks one binary
#
# Env overrides (defaults match the current memory map):
#   ORG      load origin            (default 0x8000)
#   CEILING  first forbidden byte   (default 0xC000 — the stack page)

set -euo pipefail

ORG="${ORG:-0x8000}"
CEILING="${CEILING:-0xC000}"

org=$((ORG))
ceiling=$((CEILING))

check_one() {
    local bin="$1" label="$2"
    if [[ ! -f "$bin" ]]; then
        echo "ERROR: $label binary not found: $bin" >&2
        return 2
    fi
    local size end over
    size=$(wc -c < "$bin" | tr -d ' ')
    end=$((org + size))
    if (( end >= ceiling )); then
        over=$((end - ceiling + 1))
        printf 'BUDGET FAIL: %-12s code_end &%04X ≥ ceiling &%04X — %d bytes over the stack-page cliff (%s, %d B)\n' \
            "$label" "$end" "$ceiling" "$over" "$bin" "$size" >&2
        return 1
    fi
    printf 'budget ok:   %-12s code_end &%04X  (%d B headroom to ceiling &%04X; %s, %d B)\n' \
        "$label" "$end" "$((ceiling - end))" "$ceiling" "$bin" "$size"
    return 0
}

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

rc=0
if [[ $# -ge 2 ]]; then
    check_one "$1" "$2" || rc=$?
else
    # Default: check both variants.  Either being absent is a hard error
    # (the caller asked for a budget check; missing binaries mean the
    # build didn't run).
    check_one "$ROOT/build/assembler.bin"      "test" || rc=$?
    check_one "$ROOT/build/assembler-prod.bin" "prod" || rc=$?
fi
exit "$rc"
