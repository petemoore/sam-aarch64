#!/usr/bin/env bash
# run-capture.sh — boot a font-proof disk under SimCoupé and capture the SAM
# screen as a PNG (i76 P1b, editor-tui-prototype-design.md §5).
#
# Usage: run-capture.sh <disk.mgt> <out.png>
#
# Two capture routes, picked by SIMCOUPE_PNGONHALT:
#
#   default (dev container, docs/notes/headless-simcoupe.md): run under
#   Xvfb + x11, wait for the probe's ~11 s display window, then capture the
#   display with ImageMagick `import -window root`. Requires Xvfb already
#   running and DISPLAY/SDL_VIDEODRIVER/SDL_AUDIODRIVER exported per the
#   headless notes.
#
#   SIMCOUPE_PNGONHALT=1 (hosts with no X at all): requires a SimCoupé
#   carrying tools/font-proof/simcoupe-local-capture.patch; runs under
#   SDL_VIDEODRIVER=dummy and lets the emulator itself write the PNG of the
#   probe's final frame when the Z80 hits DI;HALT — exact and timing-free.
#
# Host knobs (same shape as tools/i62-bdos-experiment/run-experiment.sh):
#   SIMCOUPE          emulator binary (default: simcoupe on PATH)
#   SIMCOUPE_ARGS     extra args, e.g. "-respath /path/to/Resource -speed 1000"
#   SIMCOUPE_TIMEOUT  per-run timeout in seconds (default 300)

set -euo pipefail

disk="${1:?usage: run-capture.sh <disk.mgt> <out.png>}"
out="${2:?usage: run-capture.sh <disk.mgt> <out.png>}"

SIMCOUPE="${SIMCOUPE:-simcoupe}"
SIMCOUPE_ARGS="${SIMCOUPE_ARGS:-}"
SIMCOUPE_TIMEOUT="${SIMCOUPE_TIMEOUT:-300}"

[ -f "$disk" ] || { echo "ERROR: disk image not found: $disk" >&2; exit 1; }
command -v "$SIMCOUPE" >/dev/null || [ -x "$SIMCOUPE" ] || {
    echo "ERROR: simcoupe not found ($SIMCOUPE)" >&2; exit 1; }

if [ "${SIMCOUPE_PNGONHALT:-0}" = "1" ]; then
    # Emulator-native capture: the patched SimCoupé writes simcNNNN.png into
    # -outpath when the probe halts, then exits.
    outdir=$(mktemp -d -t font-proof.XXXXXX)
    trap 'rm -rf "$outdir"' EXIT
    SDL_VIDEODRIVER="${SDL_VIDEODRIVER:-dummy}" SDL_AUDIODRIVER="${SDL_AUDIODRIVER:-dummy}" \
        timeout "${SIMCOUPE_TIMEOUT}s" "$SIMCOUPE" \
        -exitonhalt 1 -pngonhalt 1 -fullscreen 0 -firstrun 0 \
        -outpath "$outdir" -nextfile 0 \
        $SIMCOUPE_ARGS "$disk"
    png=$(ls "$outdir"/simc*.png 2>/dev/null | head -1)
    [ -n "$png" ] || { echo "ERROR: no PNG captured (probe never halted?)" >&2; exit 1; }
    cp "$png" "$out"
else
    # Window capture: boot in the background, give the probe time to render
    # (boot + load + render is well under 20 s; it then holds the screen for
    # ~11 s), capture the Xvfb display, then reap SimCoupé.
    command -v import >/dev/null || { echo "ERROR: ImageMagick import not found" >&2; exit 1; }
    timeout "${SIMCOUPE_TIMEOUT}s" "$SIMCOUPE" \
        -exitonhalt 1 -fullscreen 0 -firstrun 0 \
        $SIMCOUPE_ARGS "$disk" &
    simcoupe_pid=$!
    sleep "${CAPTURE_DELAY:-20}"
    import -window root "$out"
    kill "$simcoupe_pid" 2>/dev/null || true
    wait "$simcoupe_pid" 2>/dev/null || true
fi

echo "captured $out"
