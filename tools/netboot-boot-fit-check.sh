#!/usr/bin/env bash
#
# netboot-boot-fit-check.sh BIN MAX_BYTES LABEL
#
# Fail the build if a bootable netboot image exceeds its load-window budget.
#
# A bootable netboot program is LOADed to &8000 by `LOAD CODE 32768` and run by
# `CALL 32768`. The hard ceiling is &10000 (the top of the Z80 address space): an
# image whose tail runs past &FFFF cannot load (build-disk enforces that). Below
# that ceiling the whole &8000-&FFFF window is RAM at boot: section D (&C000-&FFFF)
# is RAM, not ROM1 (boot LMPR = &1F has bit 6 clear), and LOAD CODE deposits the
# >&BFFF bytes straight into section-D RAM, which the running program reads/executes
# directly with no paging. This is proven in SimCoupe by the section-D loadability
# probe (`make secd-loadability`; see docs/notes/sam-paging.md). pyz80 does NOT
# error on an org overrun, so this check still guards the real ceiling and any
# self-imposed tighter budget; without it an over-&FFFF image assembles silently
# and only fails on hardware (the i119 over-size class; the i125 gate).
#
# MAX_BYTES is the program's load-window budget in bytes from &8000:
#   * 32768 — the full &8000-&FFFF window (the real hardware limit); used by images
#             that need section D (the http fetcher; serve/client, which carry the
#             i145b SD CSD-read overlay).
#   * 16384 — a self-imposed section-C-only limit (&8000-&BFFF) for small images
#             (smoke/server) that have no reason to spill into section D.
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
