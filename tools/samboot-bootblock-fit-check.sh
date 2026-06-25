#!/usr/bin/env bash
#
# samboot-bootblock-fit-check.sh MAPFILE
#
# Fail the build if the combined patched-bootblock injection (samboot_bootblock.asm,
# org &415E) does not fit the bootblock's free space.
#
# Colin's Trinity bootblock (chunk 1, ORG &4000) ends with the final RET at &415D;
# bytes &415E..&43FF (674 bytes) are all zero — the only room a SAMBOOT patch may
# use. `inject` is org'd at &415E, so its assembled extent must end at or before
# &43FF, else the splice would overwrite live bootblock bytes past the chunk or spill
# past chunk 1's 1 KB window. pyz80 does NOT error on an org overrun, so this guard
# is the only thing that catches an over-budget image at build time rather than on a
# real EEPROM flash (the prime directive: never ship un-fitted boot code).
#
# The check reads the .map (the authority for the assembled layout): the highest
# symbol address that lands in the FLASHED range (&415E..&BFFF — below the
# SAMBOOT_SCRATCH RAM home at &E000, which is runtime scratch, never flashed) is the
# end of the emitted image. The image fits iff that end <= &43FF.
set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 MAPFILE" >&2
	exit 2
fi

mapfile=$1

ORG=0x415E
BUDGET_END=0x43FF      # last flashable byte (org + 674 - 1)
SCRATCH=0xE000         # SAMBOOT_SCRATCH RAM home — runtime scratch, not flashed

# Highest map symbol strictly below SCRATCH (the flashed image's extent).
end=$(python3 - "$mapfile" "$((SCRATCH))" <<'PY'
import sys
path, scratch = sys.argv[1], int(sys.argv[2])
mx = 0
with open(path) as f:
    for line in f:
        line = line.strip()
        if '=' not in line:
            continue
        a = line.split('=', 1)[0]
        try:
            v = int(a, 16)
        except ValueError:
            continue
        if v < scratch and v > mx:
            mx = v
print(mx)
PY
)

if [ "$end" -gt "$((BUDGET_END))" ]; then
	printf 'samboot_bootblock overflows the bootblock free space: ends &%04X, budget end &%04X (%d bytes over &415E+674).\n' \
		"$end" "$((BUDGET_END))" "$(( end - BUDGET_END ))" >&2
	printf '  The inject must fit &415E..&43FF (674 bytes). Gate more of eeprom.asm / bdos_seam.asm out under SAMBOOT_BOOTBLOCK,\n' >&2
	printf '  or relocate more storage to SAMBOOT_SCRATCH. See docs/plans/i229-combined-bootblock.md.\n' >&2
	exit 1
fi

used=$(( end - ORG + 1 ))
printf 'samboot_bootblock: fit OK — ends &%04X, %d/674 bytes used (%d free under &43FF)\n' \
	"$end" "$used" "$(( BUDGET_END - end ))"
