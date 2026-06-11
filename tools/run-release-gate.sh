#!/usr/bin/env bash
# run-release-gate.sh — the release-gate (3-way byte-match).
#
# Standing guard for "spectrum4 release.bin byte-match on SAM".  It takes
# the VENDORED spectrum4 release source (tests/release/release.s — the
# whole release flattened into one self-contained file by `sam-aarch64 -E`)
# and proves three independent toolchains agree on its bytes:
#
#   1. GNU binutils — the vendored tests/release/release.img (spectrum4's
#      own as+ld+objcopy build; we trust + freeze it, so CI needs no
#      aarch64 binutils).
#   2. Our Go toolchain — sam-aarch64 (source → binary + compact .tbn).
#   3. Our Z80 toolchain — the SAM-side assembler on SimCoupé (.tbn → OUT).
#
# All three must be byte-identical.  This exercises BOTH our toolchains
# (sam-aarch64's flatten/strip + the Go encoder, and the Z80 encoder) on the
# real release, with no spectrum4 checkout / tup / GNU toolchain at CI
# time — only the two small vendored files.  Refresh them with
# tools/revendor-release.sh when spectrum4's release.img changes.
#
# A mismatch prints the differing bytes (not just "differs"), so you can
# see WHAT diverged.  Run INSIDE the dev container (SimCoupé needs it);
# the ~88 KB two-pass assembly takes ~20 s, beyond run-simcoupe.sh's 30 s
# default, so SIMCOUPE_TIMEOUT is lifted.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

export SIMCOUPE_TIMEOUT="${SIMCOUPE_TIMEOUT:-900}"

FIXTURE_DIR="$ROOT/tests/release"
SRC="$FIXTURE_DIR/release.s"
GNU="$FIXTURE_DIR/release.img"          # toolchain 1 (vendored GNU output)
for f in "$SRC" "$GNU"; do
    if [ ! -f "$f" ]; then
        echo "ERROR: vendored fixture missing: $f" >&2
        echo "       run tools/revendor-release.sh to (re)generate it." >&2
        exit 2
    fi
done

SAMFILE="${SAMFILE:-samfile}"
if ! command -v "$SAMFILE" >/dev/null 2>&1; then
    if [ -x "$HOME/git/samfile/samfile" ]; then
        SAMFILE="$HOME/git/samfile/samfile"
    else
        echo "ERROR: samfile not found on PATH" >&2
        exit 2
    fi
fi

ORIGIN="0xfffffff000000000"
CTBN="$ROOT/build/release.compact.tbn"        # compact .tbn (i1 PR1)
GO_IMG="$ROOT/build/release.go.img"           # toolchain 2 output
GO_IMG_C="$ROOT/build/release.go.compact.img" # toolchain 2 via compact .tbn
SAM_IMG="$ROOT/build/release.sam.img"         # toolchain 3 output

echo "=== [1/5] build SAM + Go tools (+ &C000 budget check) ==="
make -s sam-aarch64 enctab sysreg-data disasm-payload zx0-payload build-disk assembler-prod check-budget

# Keep one-in-20 of release.s's ~335 KB of comments (M8 / i39b-2): the SAM
# can't fit all of them in the 96 KB IN buffer, but a bounded subset (~15 KB →
# the .tbn stays ~4 pages, well under the ceiling) flows a populated editor
# region through the full Z80 round-trip — proving the record walk stops at the
# editor boundary and comments don't leak into the assembled bytes. Comments are
# assembly no-ops, so the 3-way byte-match is unchanged (the assembler-facing
# region is byte-identical to the all-stripped form).
echo "=== [2/5] sam-aarch64: vendored release.s → binary (+ emit compact .tbn) (flatten + thin-comments=20) ==="
"$ROOT/build/sam-aarch64" -flatten -thin-comments=20 -origin "$ORIGIN" -o "$GO_IMG" --emit-tbn "$CTBN" "$SRC"
echo "    compact .tbn: $(wc -c < "$CTBN" | tr -d ' ') bytes"

echo "=== [3/5] compact .tbn: assemble + verify byte-identical ==="
"$ROOT/build/sam-aarch64" -o "$GO_IMG_C" "$CTBN"
if cmp -s "$GO_IMG" "$GO_IMG_C"; then
    cmp_tbn=$(wc -c < "$CTBN" | tr -d ' ')
    echo "    compact .tbn assembles identically to the source binary."
    printf '    compact .tbn size: %s bytes\n' "$cmp_tbn"
else
    echo "FAIL: compact .tbn assembles to a different binary than the source" >&2
    cmp -l "$GO_IMG" "$GO_IMG_C" | head -20 >&2 || true
    exit 1
fi

echo "=== [4/5] Z80 toolchain: SAM assembler on SimCoupé → OUT (from the COMPACT .tbn) ==="
# The SAM side consumes the COMPACT .tbn (i1 PR2): the Z80
# REC_KIND_LIT_INSTS decode memcpys the pre-assembled literal runs to
# OUT. Proving OUT == release.img from the compact source exercises that
# decode path under SimCoupé (the symbolic Z80 path is covered by the
# core..paged fixture jobs + the harness).
"$ROOT/build/build-disk" \
    -sysreg-data "$ROOT/build/sysreg_data.bin" \
    -disasm "$ROOT/build/disasm.bin" \
    -zx0 "$ROOT/build/zx0.bin" \
    "$ROOT/build/assembler-prod.bin" "$ROOT/build/enctab.enc" \
    "$CTBN" \
    "$ROOT/build/release.mgt"
"$ROOT/tools/run-simcoupe.sh" \
    "$ROOT/build/release.mgt" \
    "$ROOT/build/release.status.log"
status=$(tr -d '\r\n ' < "$ROOT/build/release.status.log" || true)
if [ "$status" != "OK" ]; then
    echo "FAIL: SAM assembler status '${status}' (expected OK)" >&2
    sed 's/^/    /' "$ROOT/build/release.status.log" >&2 || true
    exit 1
fi
"$SAMFILE" cat -i "$ROOT/build/release.mgt" -f OUT > "$SAM_IMG"

echo "=== [5/5] 3-way byte-compare ==="
gnu_sz=$(wc -c < "$GNU" | tr -d ' ')
go_sz=$(wc -c < "$GO_IMG" | tr -d ' ')
sam_sz=$(wc -c < "$SAM_IMG" | tr -d ' ')
printf '    GNU (vendored)      : %s bytes\n' "$gnu_sz"
printf '    Go (sam-aarch64)    : %s bytes\n' "$go_sz"
printf '    Z80 (SAM, compact)  : %s bytes\n' "$sam_sz"

rc=0
report_diff() {
    local a="$1" b="$2" label="$3"
    if cmp -s "$a" "$b"; then
        echo "    MATCH: $label"
    else
        echo "    MISMATCH: $label — first differing bytes:" >&2
        cmp -l "$a" "$b" | head -20 >&2 || true
        echo "        total differing bytes: $(cmp -l "$a" "$b" | wc -l | tr -d ' ')" >&2
        rc=1
    fi
}
report_diff "$GNU" "$GO_IMG"   "GNU vs Go (sam-aarch64)"
report_diff "$GNU" "$GO_IMG_C" "GNU vs Go (compact .tbn)"
report_diff "$GNU" "$SAM_IMG"  "GNU vs Z80 (SAM assembler, compact .tbn)"

echo
if [ "$rc" -eq 0 ]; then
    echo "3-WAY BYTE-MATCH OK — GNU == Go == Z80 on the spectrum4 release ($gnu_sz bytes)"
else
    echo "3-WAY BYTE-MATCH FAILED.  If the vendored release.img/release.s were just" >&2
    echo "refreshed with new instructions, a toolchain may need extending to cover" >&2
    echo "them; otherwise this is a regression in sam-aarch64 or the SAM encoder." >&2
fi
exit "$rc"
