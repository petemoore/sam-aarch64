#!/usr/bin/env bash
# Runs SimCoupé in batch/headless mode against the given .mgt and waits for
# a clean exit.
#
# Usage: run-simcoupe.sh <disk.mgt>
#
# Implementation derived from docs/notes/simcoupe-batch.md (M0 spike, Task 1).
#
# The recommended invocation uses the -exitonhalt flag introduced by the
# local patch on ~/git/simcoupe branch exit-on-halt. With the patched binary,
# the emulator exits with code 0 as soon as the Z80 executes HALT with
# interrupts disabled (DI; HALT — the conventional "done" sequence).
#
# On macOS, the unpatched stock binary (/Applications/SimCoupe.app) silently
# ignores -exitonhalt 1 and sits at the SAM boot screen indefinitely. The
# test wrapper (tests/test-simcoupe-runs.sh) uses 'timeout 30' to handle this
# case; SimCoupé is killed after 30s and the test accepts that as a pass for
# local development. Linux CI (Task 10) will build the patched binary and get
# deterministic clean exits.
#
# On Linux (CI), set SDL_VIDEODRIVER=dummy SDL_AUDIODRIVER=dummy to avoid
# needing an X display or audio device.

set -euo pipefail

disk="$1"

if [ ! -f "$disk" ]; then
    echo "ERROR: disk image not found: $disk" >&2
    exit 1
fi

# -exitonhalt 1  quit cleanly when Z80 executes HALT with interrupts disabled
# -fullscreen 0  never start fullscreen (default off, but explicit for CI)
# -firstrun 0    suppress the welcome dialog on a fresh CI runner
#
# The bare positional argument (disk path) is the canonical way to autoboot
# a disk in SimCoupé; Options::Load inserts it as drive 1 and forces autoboot.
#
# Safety timeout of 30s — even with the patch, if the BASIC auto-load fails to
# trigger, simcoupe would otherwise sit at the SAM boot screen forever. We treat
# a 30s timeout as a *failure* (exit 124) so CI doesn't silently pass when the
# stub never reached DI; HALT.
exec timeout 30s simcoupe -exitonhalt 1 -fullscreen 0 -firstrun 0 "$disk"
