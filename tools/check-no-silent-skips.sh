#!/usr/bin/env bash
# check-no-silent-skips.sh — anti-regression guard for the i253 "no silent skips"
# policy.
#
# Policy: a test that needs a missing precondition must FAIL (t.Fatal/t.Fatalf),
# not silently t.Skip — a silent skip hides a CI-wiring gap (the i251 sd_csd bug:
# a whole suite skipped in CI undetected). The ONLY sanctioned skip is the explicit
# `SKIP_PRIVATE_TESTS` env gate for non-redistributable proprietary captures, which
# CI sets (and our own CI runs via secrets — i254). See CLAUDE.md "Testing policy".
#
# This guard fails if any `t.Skip` / `t.Skipf` / `t.SkipNow` call in a Go test does
# NOT reference SKIP_PRIVATE_TESTS on its own line. Keep the sanctioned set tiny:
# adding a new exception is a deliberate edit to THIS file, reviewed on its own.
set -uo pipefail

cd "$(dirname "$0")/.."

# Every t.Skip* call line must carry the SKIP_PRIVATE_TESTS marker (the message of
# the one sanctioned gate). Anything else is a silent skip.
offenders="$(grep -rnE 't\.Skip(f|Now)?\(' --include='*_test.go' tools/ 2>/dev/null \
             | grep -vF 'SKIP_PRIVATE_TESTS' || true)"

if [ -n "$offenders" ]; then
  {
    echo "ERROR: silent test skip(s) found (i253 policy: no t.Skip on a missing precondition)."
    echo "  Fix: convert to t.Fatal/t.Fatalf so the missing precondition FAILS loudly;"
    echo "  or, ONLY for a non-redistributable proprietary capture, gate it on SKIP_PRIVATE_TESTS."
    echo "  Offending sites:"
    echo "$offenders" | sed 's/^/    /'
  } >&2
  exit 1
fi

echo "check-no-silent-skips: OK — every t.Skip is a sanctioned SKIP_PRIVATE_TESTS gate."
