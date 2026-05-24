#!/usr/bin/env bash
# Runs SimCoupé in batch/headless mode against the given .mgt and waits for
# a clean exit, capturing the SAM-side status banner via the parallel
# printer channel.
#
# Usage: run-simcoupe.sh <disk.mgt> [<status-out-path>]
#
# The assembler emits "OK\n" on success / "FAIL\n" on any self-test or
# loader failure via OUT to ports &E8/&E9 (PRINTL1 data + strobe — see
# src/m3/print.asm and Base/SAMIO.h in vendored SimCoupé).  We route
# printer channel 1 to a file and, after SimCoupé exits, copy that file
# to <status-out-path> (default: <disk>.status.log).  Callers grep it
# for "^OK$" / "^FAIL$" to distinguish clean success from any
# crashed-then-halted scenario.
#
# Why this matters: pre-PR-#24 the `fail` path spun until the wrapper's
# 30-second timeout killed SimCoupé; failure detection cost a full 30 s
# per fixture.  With the printer-channel banner, both paths do `DI;
# HALT` and exit cleanly within ~100 ms, and the status file
# distinguishes OK from FAIL.
#
# Implementation derived from docs/notes/simcoupe-batch.md (M0 spike,
# Task 1) with the printer-channel extension added in PR #24.
#
# The recommended invocation uses the -exitonhalt flag introduced by
# the local patch on ~/git/simcoupe branch exit-on-halt.  With the
# patched binary, the emulator exits with code 0 as soon as the Z80
# executes HALT with interrupts disabled (DI; HALT — the conventional
# "done" sequence used by both the success path and the new fail
# path).
#
# On macOS, the unpatched stock binary
# (/Applications/SimCoupe.app/Contents/MacOS/SimCoupe) silently
# ignores -exitonhalt 1 and sits at the SAM boot screen indefinitely.
# In that environment the 30-second timeout still kicks in (we keep
# the safety net) and the wrapper treats it as a failure.
#
# On Linux (CI), set SDL_VIDEODRIVER=dummy SDL_AUDIODRIVER=dummy to
# avoid needing an X display or audio device.

set -euo pipefail

disk="$1"
status_out="${2:-${disk}.status.log}"

if [ ! -f "$disk" ]; then
    echo "ERROR: disk image not found: $disk" >&2
    exit 1
fi

if command -v simcoupe >/dev/null 2>&1; then
    SIMCOUPE=simcoupe
elif [ -x /Applications/SimCoupe.app/Contents/MacOS/SimCoupe ]; then
    SIMCOUPE=/Applications/SimCoupe.app/Contents/MacOS/SimCoupe
else
    echo "ERROR: simcoupe not found on PATH or at /Applications/SimCoupe.app" >&2
    exit 1
fi

# Use a private outpath so the printer file is in a known place and we
# don't collide with whatever the user already has in
# ~/Documents/SimCoupe (macOS) or ~/Desktop/SimCoupe (Linux).
#
# `-parallel1 1`        route PRINTL1 data writes to PrinterFile
# `-outpath <dir>`      override the default output directory
# `-nextfile 0`         start the auto-incrementing filename at 0000
#
# Per Base/Util.cpp::UniqueOutputPath, SimCoupé writes the printer
# capture to <outpath>/simc<NNNN>.txt where NNNN starts at `nextfile`.
# We clean the outpath first so the post-run glob picks up exactly
# the file from THIS run.
outpath=$(mktemp -d -t simcoupe-printer.XXXXXX)
trap "rm -rf '$outpath'" EXIT

# -exitonhalt 1  quit cleanly on DI; HALT (success path + fail path)
# -fullscreen 0  never fullscreen
# -firstrun 0    suppress the welcome dialog
#
# Outer timeout retained as a safety net for environments where
# -exitonhalt isn't honoured (unpatched macOS SimCoupé) or for genuine
# infinite loops in the assembler.  Default 30 s suits the small
# fixtures; large inputs (e.g. the 88 KB release-stripped.tbn, whose
# two-pass assembly far exceeds 30 s) override via SIMCOUPE_TIMEOUT.
SIMCOUPE_TIMEOUT="${SIMCOUPE_TIMEOUT:-30}"
set +e
timeout "${SIMCOUPE_TIMEOUT}s" "$SIMCOUPE" \
    -exitonhalt 1 \
    -fullscreen 0 \
    -firstrun 0 \
    -parallel1 1 \
    -outpath "$outpath" \
    -nextfile 0 \
    "$disk"
simcoupe_rc=$?
set -e

# Locate the printer-capture file.  In the common case it's
# $outpath/simc0000.txt; defensively glob for any simc*.txt in case
# -nextfile didn't take or SimCoupé's persisted config bumped it.
printer_file=""
for candidate in "$outpath"/simc*.txt; do
    if [ -f "$candidate" ]; then
        printer_file="$candidate"
        break
    fi
done

# Always write the status file (empty if no printer output captured).
# Caller decides what to do with the contents.
if [ -n "$printer_file" ]; then
    cp "$printer_file" "$status_out"
else
    : > "$status_out"
fi

# Propagate SimCoupé's exit code so callers can still detect a
# timeout (exit 124) or other process-level failure even before
# inspecting the status file.
exit "$simcoupe_rc"
