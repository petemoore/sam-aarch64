#!/usr/bin/env bash
#
# netboot-boot-fit-check.sh BIN MAX_BYTES LABEL
#
# Fail the build if a bootable netboot image exceeds its load-window budget.
#
# A bootable netboot program is LOADed to &8000 at boot, and &C000-&FFFF
# (section D) is ROM1 at boot time — a section-C program's code/data above &BFFF
# is never written to RAM, so the program crashes on its first call into the
# un-loaded region (the i119 bug that wasted a hardware session; the i125 gate).
# pyz80 does NOT error on an org overrun, so without this check an over-budget
# image assembles silently and only fails on hardware.
#
# MAX_BYTES is the program's load-window budget in bytes from &8000:
#   * 16384 — a section-C program (&8000-&BFFF); the default for a bootable image.
#   * 32768 — a section-D overlay program that pages RAM into &C000-&FFFF before
#             using it and so may run to &FFFF (e.g. the http fetcher).
#
# The single home for this check: every bootable `*_boot.bin` Makefile rule calls
# it, so the budget logic lives in one place rather than copied per target.
set -euo pipefail

if [ "$#" -ne 3 ]; then
	echo "usage: $0 BIN MAX_BYTES LABEL" >&2
	exit 2
fi

bin=$1
max=$2
label=$3

sz=$(stat -c%s "$bin")
end=$(( 32768 + sz ))

if [ "$sz" -gt "$max" ]; then
	printf '%s overflows its boot window: %d bytes, max %d (ends &%04X, %d over).\n' \
		"$label" "$sz" "$max" "$end" "$(( sz - max ))" >&2
	printf '  A bootable netboot program loads at &8000; bytes past the budget are not written to RAM at boot.\n' >&2
	printf '  Shrink it to section C (<=16384, &8000-&BFFF), or make it a section-D overlay (page RAM into &C000-&FFFF). See i119/i125.\n' >&2
	exit 1
fi

printf '%s: boot fit OK — %d/%d bytes (ends &%04X)\n' "$label" "$sz" "$max" "$end"
