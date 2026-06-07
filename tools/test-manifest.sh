#!/usr/bin/env bash
# Test-inventory manifest: CI jobs (+durations), Go test names (+durations),
# fixture corpora, boot self-tests, round-trip sweep enumeration.
# Usage: tools/test-manifest.sh <output-file> [--skip-ci]
#
# ROOT defaults to the repo root relative to this script, but can be
# overridden via MANIFEST_ROOT so callers can run the script from any
# directory against any checkout:
#   MANIFEST_ROOT=/path/to/repo bash tools/test-manifest.sh out.txt
set -euo pipefail
ROOT="${MANIFEST_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
OUT="${1:?usage: test-manifest.sh <output-file> [--skip-ci]}"
SKIP_CI="${2:-}"
exec > "$OUT"

echo "## CI jobs"
if [ "$SKIP_CI" != "--skip-ci" ]; then
  RUN_ID=$(gh run list --workflow ci --limit 1 --status completed --json databaseId -q '.[0].databaseId')
  gh run view "$RUN_ID" --json jobs -q '.jobs[] | "\(.name)\t\(( (.completedAt|fromdate) - (.startedAt|fromdate) ))s"' | sort
  echo "ci-job-count: $(gh run view "$RUN_ID" --json jobs -q '.jobs | length')"
fi

echo; echo "## Go tests"
TOTAL=0
for mod in $(cd "$ROOT" && find tools -name go.mod | sort); do
  dir="$ROOT/$(dirname "$mod")"
  echo "### module: $(dirname "$mod")"
  RESULTS=$( (cd "$dir" && go test -count=1 -v ./... 2>&1 | grep -E '^--- (PASS|FAIL|SKIP):' | sed 's/^--- //' | sort) || true )
  echo "${RESULTS:-"(no tests)"}"
  N=$(printf '%s' "$RESULTS" | grep -c ':' || true)
  TOTAL=$((TOTAL + N))
done
echo "go-test-count: $TOTAL"

echo; echo "## Fixture corpora"
for d in "$ROOT"/tests/*/sources; do
  corpus="$(basename "$(dirname "$d")")"
  echo "### corpus: $corpus ($(ls "$d"/*.s | wc -l) fixtures)"
  ls "$d" | sort
done
echo "### release fixture"
find "$ROOT"/tests -name 'release.s' -o -name 'release.img' | sed "s|$ROOT/||" | sort

echo; echo "## Boot self-tests (BUILD_TESTS wiring)"
# Collect names via three routes:
#
# Route 1: explicit "call run_<name>" in the three wiring files (catches the
#   majority of on-axis and off-axis-cluster tests).
#
# Route 2: "call &0000" with LMPR_TEST_MEM context — off-axis test_mem.bin
#   dispatched via LMPR swap; the entry point in test_mem.asm is
#   run_mem_self_tests.
#
# Route 3: "call paged_call" followed by "defw DISASM_SELF_TEST_ENTRY" in
#   assembler.asm — the page-15 disasm self-test, whose entry point function
#   is run_disasm_self_test (src/disasm.asm:7226).
#
# Route 1 covers all tests called via direct call run_* instructions.
# Routes 2+3 add the two tests invoked through indirect mechanisms that the
# Route-1 grep pattern would otherwise miss.
{
  grep -hoE 'call[[:space:]]+run_[a-z0-9_]+' \
    "$ROOT"/src/assembler.asm \
    "$ROOT"/src/test_offaxis_cluster.asm \
    "$ROOT"/src/test_mem_offaxis.asm \
    | awk '{print $2}'

  # Route 2: off-axis run_mem_self_tests (invoked as "call &0000" with LMPR swap)
  if grep -q 'call[[:space:]]*&0000' "$ROOT"/src/assembler.asm 2>/dev/null; then
    echo "run_mem_self_tests"
  fi

  # Route 3: page-15 disasm self-test (invoked via paged_call + DISASM_SELF_TEST_ENTRY)
  if grep -q 'DISASM_SELF_TEST_ENTRY' "$ROOT"/src/assembler.asm 2>/dev/null; then
    echo "run_disasm_self_test"
  fi
} | sort -u

echo; echo "## Round-trip sweep enumeration"
for w in "$ROOT"/tests/*/run-roundtrip.sh; do
  corpus="$(basename "$(dirname "$w")")"
  echo "### sweep: $corpus"
  ls "$(dirname "$w")"/sources/*.s 2>/dev/null | xargs -rn1 basename | sort
done
