#!/usr/bin/env bash
#
# Unit test for the i147 structural stop-after-merge detection helper in
# monitor.sh. new_merges_since() must:
#   - report a PR merge (a 2-parent --no-ff merge commit) landing on the ref;
#   - IGNORE a direct doc-only push (a single-parent commit straight on the ref) --
#     this project lands prose/registry-view docs directly on main, and those must
#     NOT force a checkpoint;
#   - report nothing once the baseline has caught up to the tip (idempotent).
#
# Run: tools/autonomous-loop/test-merge-detection.sh   (exits non-zero on failure)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Sourcing monitor.sh defines its functions and returns WITHOUT running the
# monitor loop (the `(return 0 …)` source-guard before its preflight).
# shellcheck source=monitor.sh
source "$here/monitor.sh"

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
gitc() { git -C "$tmp" "$@"; }
gitc init -q -b main
gitc config user.email test@example.com
gitc config user.name "Test"

pass() { printf 'PASS: %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

# base commit
echo a > "$tmp/a"; gitc add -A; gitc commit -qm "base"
base="$(gitc rev-parse HEAD)"

# (1) a direct doc-only push: single-parent commit straight on main -> NOT a merge
echo b >> "$tmp/a"; gitc commit -qam "docs: direct push"
after_doc="$(gitc rev-parse HEAD)"
out="$(new_merges_since "$tmp" "$base" "main")"
[ -z "$out" ] || fail "direct doc push counted as a merge: [$out]"
pass "direct doc-only push is ignored (no checkpoint)"

# (2) a PR merge: feature branch merged --no-ff -> a 2-parent merge commit -> MERGE
gitc checkout -q -b feat
echo c > "$tmp/c"; gitc add -A; gitc commit -qm "feature work"
gitc checkout -q main
gitc merge -q --no-ff feat -m "Merge pull request #999 from feat"
out="$(new_merges_since "$tmp" "$after_doc" "main")"
[ -n "$out" ] || fail "PR merge commit was not detected"
pass "PR merge commit is detected ([$out])"

# (3) idempotency: from the current tip onward, nothing new
tip="$(gitc rev-parse HEAD)"
out="$(new_merges_since "$tmp" "$tip" "main")"
[ -z "$out" ] || fail "spurious merge reported at tip: [$out]"
pass "no spurious detection once baseline is at the tip"

# (4) a doc push AFTER a merge (the realistic mix): from the merge tip, a later
#     single-parent doc commit must still NOT count
echo d >> "$tmp/a"; gitc commit -qam "docs: roadmap update after merge"
out="$(new_merges_since "$tmp" "$tip" "main")"
[ -z "$out" ] || fail "post-merge doc push counted as a merge: [$out]"
pass "post-merge doc-only push is ignored"

echo "ALL PASS"
