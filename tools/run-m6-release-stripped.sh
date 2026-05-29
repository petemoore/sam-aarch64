#!/usr/bin/env bash
# run-m6-release-stripped.sh — drive the full spectrum4 release source
# through the SAM-side assembler and byte-compare against the GNU oracle.
#
# This is the M6 headline check: "spectrum4 release.bin byte-match on SAM".
#
# Pipeline:
#   1. text2bin -flatten -strip-comments release.target → release-stripped.tbn (~88 KB)
#   2. build the production assembler + a disk image with the .tbn as IN
#   3. SimCoupé assembles it (two passes, paged IN across pages 7..12,
#      paged OUT, HSAVE) and emits OUT
#   4. GNU oracle: as + ld -T spectrum4.ld + objcopy -O binary (via
#      scripts/build-spectrum4-release.sh → build/release.gnu.img)
#   5. cmp the SAM OUT against the oracle — exit 0 on byte-match
#
# SimCoupé must run in the dev container (the host macOS app pops a GUI
# window).  This script uses `tools/run-simcoupe.sh`, which finds
# `simcoupe` on PATH — so invoke this script INSIDE the dev container,
# e.g.:
#
#   docker run --rm -v "$(pwd):/work" -w /work \
#       -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
#       -e XDG_RUNTIME_DIR=/tmp/xdg -e SIMCOUPE_TIMEOUT=900 \
#       ghcr.io/petemoore/sam-aarch64-dev:latest bash -c \
#       'Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 & sleep 1; \
#        export DISPLAY=:99; tools/run-m6-release-stripped.sh'
#
# The 88 KB two-pass assembly takes ~20 s under SimCoupé, well beyond the
# 30 s default timeout of run-simcoupe.sh — SIMCOUPE_TIMEOUT lifts it.
#
# Env:
#   SPECTRUM4_SRC   spectrum4 source tree (default ~/git/spectrum4/src/spectrum4)
#   SIMCOUPE_TIMEOUT  seconds for the SimCoupé run (default here: 900)

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

export SIMCOUPE_TIMEOUT="${SIMCOUPE_TIMEOUT:-900}"

SAMFILE="${SAMFILE:-samfile}"
if ! command -v "$SAMFILE" >/dev/null 2>&1; then
    if [ -x "$HOME/git/samfile/samfile" ]; then
        SAMFILE="$HOME/git/samfile/samfile"
    else
        echo "ERROR: samfile not found on PATH" >&2
        exit 2
    fi
fi

echo "=== [1/5] build Mac/Go-side tools + production assembler ==="
make -s text2bin refenc enctab sysreg-data build-m3-disk m3-asm-prod

echo "=== [2/5] flatten + strip release.target → release-stripped.tbn ==="
make -s release-stripped-tbn

echo "=== [3/5] build production disk with release-stripped.tbn as IN ==="
"$ROOT/build/build-m3-disk" \
    -sysreg-data "$ROOT/build/sysreg_data.bin" \
    "$ROOT/build/assembler-prod.bin" "$ROOT/build/enctab.enc" \
    "$ROOT/build/release-stripped.tbn" \
    "$ROOT/build/release-stripped.mgt"

echo "=== [4/5] run SimCoupé (assemble + HSAVE OUT) ==="
"$ROOT/tools/run-simcoupe.sh" \
    "$ROOT/build/release-stripped.mgt" \
    "$ROOT/build/release-stripped.status.log"
status=$(tr -d '\r\n ' < "$ROOT/build/release-stripped.status.log" || true)
if [ "$status" != "OK" ]; then
    echo "FAIL: assembler status '${status}' (expected OK)" >&2
    sed 's/^/    /' "$ROOT/build/release-stripped.status.log" >&2 || true
    exit 1
fi
"$SAMFILE" cat -i "$ROOT/build/release-stripped.mgt" -f OUT > "$ROOT/build/release-stripped.sam.bin"

echo "=== [5/5] build GNU oracle + byte-compare ==="
# scripts/build-spectrum4-release.sh leaves the GNU oracle at build/release.gnu.img.
bash "$ROOT/scripts/build-spectrum4-release.sh" >/dev/null

sam="$ROOT/build/release-stripped.sam.bin"
gnu="$ROOT/build/release.gnu.img"
sam_sz=$(wc -c < "$sam" | tr -d ' ')
gnu_sz=$(wc -c < "$gnu" | tr -d ' ')
echo "    SAM OUT : $sam_sz bytes"
echo "    GNU img : $gnu_sz bytes"
if cmp -s "$sam" "$gnu"; then
    echo
    echo "BYTE-MATCH OK — SAM-side assembler matches GNU on spectrum4 release ($sam_sz bytes)"
    exit 0
fi
echo
echo "BYTE-MATCH MISMATCH — differing bytes:"
cmp -l "$gnu" "$sam" | head -20 || true
echo "    total differing bytes: $(cmp -l "$gnu" "$sam" | wc -l | tr -d ' ')"
exit 1
