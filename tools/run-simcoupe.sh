#!/usr/bin/env bash
# Runs SimCoupé in batch/headless mode against the given .mgt and waits for
# a clean exit, capturing the SAM-side status banner via the parallel
# printer channel.
#
# Usage: run-simcoupe.sh <disk.mgt> [<status-out-path>]
#
# The assembler emits "OK\n" on success / "FAIL\n" on any self-test or
# loader failure via OUT to ports &E8/&E9 (PRINTL1 data + strobe — see
# src/print.asm and Base/SAMIO.h in vendored SimCoupé).  We route
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
# Implementation derived from https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/archive/simcoupe-batch.md (M0 spike,
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

# -keyin "STRING" injects keystrokes into the SAM by internally poking
# LASTK (&5C08) and FLAGS (&5C3B bit 5 = key-available) — the same two
# sysvars the Go harness InjectKeys/PendingKeys stub writes (i138,
# keyboard_test.go).  They are the same mechanism; -keyin is SimCoupé's
# built-in interface to it.
#
# DETERMINATION (i12b — the single home for this finding): SimCoupé's -keyin
# is a *boot-prompt autotyper*, NOT a general key feed for a running program.
# It cannot reliably deliver keys to a post-boot SAM program that polls
# FLAGS/LASTK (the editor-input use case).  This was established locally
# (SDL_VIDEODRIVER=offscreen, isolated HOME):
#   - A diagnostic stub booted via SIMCOUPE_KEYIN='BOOT\nABCABC\n', confirmed
#     CanType() satisfied at the stub (read LMPR=&1F → Section A=ROM0, Section
#     B=page 0; HMPR=&01), then polled FLAGS in a tight loop for several
#     seconds.  Bit 5 NEVER set and ZERO keys (ABCABC) arrived.
# Root cause, from the SimCoupé source (Base/SAMIO.cpp, Base/Keyin.cpp):
#   1. The whole -keyin string is loaded ONCE, at the ROM startup screen, when
#      Rst48Hook() hits READKEY and AutoLoad() finds the WTFK wait-key loop on
#      the Z80 stack (TestStartupScreen).  There is NO re-arm: a program that
#      starts after boot never re-triggers typing.
#   2. Keys stream one-per-frame from EiHook() at the IMEXIT ROM hook — so the
#      target must run with interrupts ENABLED (a DI poll loop never receives a
#      key), and CanType() must hold (Section A=ROM0, Section B=page 0).
#   3. Rst8Hook()'s `default: Keyin::Stop()` clears the pending string on any
#      RST-8 code outside a small whitelist (&00/&1d/&35/&13/&50).  DOS/BASIC
#      issue RST-8 calls during the boot→program handoff, so the remaining keys
#      are dropped before the program can poll them.
# The reliable, deterministic, CI-gated vehicle for automated editor INPUT
# tests is therefore the Go harness InjectKeys (i138, keyboard_test.go) — the
# same LASTK/FLAGS mechanism, driven directly rather than through the boot
# autotyper.  SimCoupé remains the boot/render gate via the existing fixture
# jobs (core/symbols/operands/…), which exercise the full paged boot path.
#
# SIMCOUPE_KEYIN remains wired (opt-in, default-off) for the niche where it
# does work: typing AT the startup/BASIC prompt before any program runs (e.g.
# the default 'BOOT\n' autotype an existing fixture relies on).  It is NOT a
# supported way to feed keys to a running CODE program.
#
# Default-off: existing fixtures do not set SIMCOUPE_KEYIN, so this path is
# never taken for them.
set +e
if [ -n "${SIMCOUPE_KEYIN:-}" ]; then
    timeout "${SIMCOUPE_TIMEOUT}s" "$SIMCOUPE" \
        -exitonhalt 1 \
        -fullscreen 0 \
        -firstrun 0 \
        -parallel1 1 \
        -outpath "$outpath" \
        -nextfile 0 \
        -keyin "$SIMCOUPE_KEYIN" \
        "$disk"
else
    timeout "${SIMCOUPE_TIMEOUT}s" "$SIMCOUPE" \
        -exitonhalt 1 \
        -fullscreen 0 \
        -firstrun 0 \
        -parallel1 1 \
        -outpath "$outpath" \
        -nextfile 0 \
        "$disk"
fi
simcoupe_rc=$?
set -e

# Assemble the printer-capture into the status file.  SimCoupé auto-flushes
# the parallel-printer file after an inactivity gap (a poll loop that emits a
# marker, pauses to wait for input, then emits another marker) and writes the
# later output to a fresh auto-incrementing file: simc0000.txt, simc0001.txt,
# simc0002.txt …  (Base/Util.cpp::UniqueOutputPath).  Capturing only the FIRST
# file silently drops everything emitted after the first gap — which is exactly
# the output an input-polling stub produces.  So concatenate ALL simc*.txt in
# numeric order into the status file.
#
# `ls -v` sorts the glob in version/numeric order so simc0009 precedes
# simc0010; the glob is quoted-safe because we cd into $outpath first.
printer_files=()
if [ -d "$outpath" ]; then
    while IFS= read -r f; do
        printer_files+=("$outpath/$f")
    done < <(cd "$outpath" && ls -v simc*.txt 2>/dev/null)
fi

# Always write the status file (empty if no printer output captured).
# Caller decides what to do with the contents.
if [ "${#printer_files[@]}" -gt 0 ]; then
    cat "${printer_files[@]}" > "$status_out"
else
    : > "$status_out"
fi

# Propagate SimCoupé's exit code so callers can still detect a
# timeout (exit 124) or other process-level failure even before
# inspecting the status file.
exit "$simcoupe_rc"
