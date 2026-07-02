#!/usr/bin/env bash
# check-artifacts-fresh.sh — assert every make-managed file under build/ that
# exists on disk is up to date with its (transitive) prerequisites.
#
# This is the mechanised form of the manual mtime check that caught a stale
# netboot .bin being tested as current (i309). make's own prerequisite graph
# is the authority: the target list is derived from make's rule database, so
# there is no second hand-maintained list to drift.
#
# A test artifact that does NOT exist is fine here — its consumer fails loudly
# at open time; the dangerous case is present-but-stale, which reads as a
# valid artifact while embodying old sources.
#
# Run it before trusting a `go test` invoked directly (i.e. not through a make
# target that builds its own prerequisites). The two big harness suites
# (tools/netboot-oracle/z80, tools/z80-test-harness-go) self-rebuild via
# TestMain, so they never need this; direct consumers of other artifacts do.
set -euo pipefail
cd "$(dirname "$0")/.."

# File targets under build/ from make's database. build/asm-deps.mk is
# excluded: it is FORCE-regenerated every run, so -q always reports it stale.
mapfile -t targets < <(
    make -np FORCE 2>/dev/null \
        | awk '/^build\/[^ :=]+:([^=]|$)/ && $1 !~ /asm-deps\.mk/ { sub(/:.*/, "", $1); print $1 }' \
        | sort -u
)
if [ "${#targets[@]}" -eq 0 ]; then
    echo "check-artifacts-fresh: found no build/ file targets in the make database" >&2
    exit 1
fi

existing=()
for t in "${targets[@]}"; do
    [ -e "$t" ] && existing+=("$t")
done
if [ "${#existing[@]}" -eq 0 ]; then
    echo "check-artifacts-fresh: no build/ artifacts present (nothing to be stale) — OK"
    exit 0
fi

if make -q "${existing[@]}" >/dev/null 2>&1; then
    echo "check-artifacts-fresh: ${#existing[@]} build/ artifacts up to date — OK"
    exit 0
fi

echo "check-artifacts-fresh: STALE artifacts — these would rebuild:" >&2
# -n names the recipes that would run; enough to identify the stale targets.
# (make's own "is up to date" chatter also names build/ paths — drop it.)
make -n "${existing[@]}" 2>/dev/null | grep -v "is up to date" \
    | grep -oE 'build/[^ ]+\.(bin|map|sym|inc|enc)\b' | sort -u >&2 || true
echo "run make on the targets above (or the relevant aggregate) before trusting test results" >&2
exit 1
