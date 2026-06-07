#!/usr/bin/env bash
# run-experiment.sh — i62 B-DOS storage-backend portability experiment.
#
# Builds ONE probe binary (i62test.asm) and runs it twice under SimCoupé:
#
#   control run:  SAMDOS 2 boot floppy, no mass storage
#                 -> expect transcript I62 / DOS:SAMDOS / P2 P3 P4 / OK
#   B-DOS run:    B-DOS AL 1.5a boot floppy + emulated Atom Lite with a
#                 B-DOS-formatted HDF attached as -atomdisk0
#                 -> expect transcript I62 / DOS:BDOS V=.. R=.... / P1
#                    P2 P3 P4 / OK
#
# The identical binary passing both runs is the hook-portability proof:
# the only backend-conditional step is the DVAR-7-gated HRECORD call.
# After the B-DOS run the HDF is checked for the saved pattern bytes —
# independent evidence that HSAVE really wrote to the Atom Lite record.
#
# Inputs from outside the repo (worldofsam preservation copies; NOT
# committed — see the publication policy note in
# docs/notes/bdos-version-landscape.md):
#   BDOS_BOOT_MGT  (default ~/sam-archive/bdos/megaboot-alplus.mgt)
#                  any AL disk carrying the bootable "AL-BDOS15a" file.
#
# Host knobs:
#   SIMCOUPE          emulator binary (default: simcoupe on PATH)
#   SIMCOUPE_ARGS     extra args, e.g. "-respath /path/to/Resource
#                     -speed 1000" for an uninstalled local build
#   SIMCOUPE_TIMEOUT  per-run timeout in seconds (default 120)
#
# Headless: SDL_VIDEODRIVER=dummy SDL_AUDIODRIVER=dummy works with a
# SimCoupé whose renderer can fall back to software (see the i62 section
# of docs/notes/bdos-version-landscape.md); the CI container's
# Xvfb+x11 recipe from docs/notes/headless-simcoupe.md works unmodified.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BUILD="$ROOT/build"
EXP="$ROOT/tools/i62-bdos-experiment"
BDOS_ARCHIVE="${BDOS_ARCHIVE:-$HOME/sam-archive/bdos}"
BDOS_BOOT_MGT="${BDOS_BOOT_MGT:-$BDOS_ARCHIVE/megaboot-alplus.mgt}"
SIMCOUPE="${SIMCOUPE:-simcoupe}"
SIMCOUPE_TIMEOUT="${SIMCOUPE_TIMEOUT:-120}"
SIMCOUPE_ARGS="${SIMCOUPE_ARGS:-}"

fail() { echo "FAIL: $*" >&2; exit 1; }

command -v pyz80 >/dev/null || fail "pyz80 not on PATH"
command -v samfile >/dev/null || fail "samfile not on PATH (go build it from github.com/petemoore/samfile)"
command -v "$SIMCOUPE" >/dev/null || [ -x "$SIMCOUPE" ] || fail "simcoupe not found ($SIMCOUPE)"
[ -f "$BDOS_BOOT_MGT" ] || fail "B-DOS AL boot disk not found: $BDOS_BOOT_MGT (set BDOS_BOOT_MGT)"

mkdir -p "$BUILD"

echo "--- assemble probe ---"
pyz80 --obj="$BUILD/i62test.bin" "$EXP/i62test.asm"

echo "--- extract B-DOS AL 1.5a DOS file ---"
samfile cat -i "$BDOS_BOOT_MGT" -f "AL-BDOS15a" > "$BUILD/al-bdos15a.bin"
dos_size=$(wc -c < "$BUILD/al-bdos15a.bin")
echo "AL-BDOS15a: $dos_size bytes"

echo "--- build boot disks ---"
(cd "$EXP/build-i62-disk" && go build -o "$BUILD/build-i62-disk" .)
"$BUILD/build-i62-disk" \
    "$ROOT/reference/samdos/samdos2.bin" "$BUILD/i62test.bin" \
    "$BUILD/i62-samdos.mgt"
"$BUILD/build-i62-disk" -dos-name "AL-BDOS15a" -dos-load 32777 \
    "$BUILD/al-bdos15a.bin" "$BUILD/i62test.bin" \
    "$BUILD/i62-bdos.mgt"

echo "--- build Atom Lite HDF ---"
python3 "$EXP/make-atomlite-hdf.py" "$BUILD/i62-atomlite.hdf"

# ---------------------------------------------------------------------
# run_simcoupe <disk> <status-out> [extra simcoupe args...]
# Same printer-channel capture recipe as tools/run-simcoupe.sh.
# ---------------------------------------------------------------------
run_simcoupe() {
    local disk="$1" status_out="$2"
    shift 2
    local outpath
    outpath=$(mktemp -d -t i62-printer.XXXXXX)
    set +e
    # shellcheck disable=SC2086
    timeout "${SIMCOUPE_TIMEOUT}s" "$SIMCOUPE" \
        -exitonhalt 1 -fullscreen 0 -firstrun 0 \
        -parallel1 1 -outpath "$outpath" -nextfile 0 \
        $SIMCOUPE_ARGS "$@" "$disk"
    local rc=$?
    set -e
    : > "$status_out"
    local f
    for f in "$outpath"/simc*.txt; do
        [ -f "$f" ] && cp "$f" "$status_out" && break
    done
    rm -rf "$outpath"
    return $rc
}

expect_line() { # expect_line <file> <exact-line>
    grep -qx "$2" "$1" || fail "expected '$2' in $1 — got: $(tr '\n' '|' < "$1")"
}

echo "--- control run: SAMDOS 2 + floppy ---"
run_simcoupe "$BUILD/i62-samdos.mgt" "$BUILD/i62-samdos.status.log" \
    -drive2 1 \
    || fail "SimCoupé control run exited non-zero (timeout = boot or DOS error)"
expect_line "$BUILD/i62-samdos.status.log" "I62"
expect_line "$BUILD/i62-samdos.status.log" "DOS:SAMDOS"
grep -q "DOS:BDOS" "$BUILD/i62-samdos.status.log" && fail "control run detected B-DOS?!"
expect_line "$BUILD/i62-samdos.status.log" "P2"
expect_line "$BUILD/i62-samdos.status.log" "P3"
expect_line "$BUILD/i62-samdos.status.log" "P4"
expect_line "$BUILD/i62-samdos.status.log" "OK"
echo "control transcript:"
sed 's/^/    /' "$BUILD/i62-samdos.status.log"

echo "--- B-DOS run: AL 1.5a + Atom Lite HDF ---"
# -drive2 3 = Atom Lite in the drive-2 bay; -atombootrom 0 keeps the
# standard SAM ROM so the boot path (floppy F9 boot) is identical to
# the control run.
run_simcoupe "$BUILD/i62-bdos.mgt" "$BUILD/i62-bdos.status.log" \
    -drive2 3 -atomdisk0 "$BUILD/i62-atomlite.hdf" -atombootrom 0 \
    || fail "SimCoupé B-DOS run exited non-zero (timeout = boot or DOS error)"
expect_line "$BUILD/i62-bdos.status.log" "I62"
grep -q "^DOS:BDOS V=" "$BUILD/i62-bdos.status.log" \
    || fail "B-DOS run did not detect B-DOS: $(tr '\n' '|' < "$BUILD/i62-bdos.status.log")"
expect_line "$BUILD/i62-bdos.status.log" "P1"
expect_line "$BUILD/i62-bdos.status.log" "P2"
expect_line "$BUILD/i62-bdos.status.log" "P3"
expect_line "$BUILD/i62-bdos.status.log" "P4"
expect_line "$BUILD/i62-bdos.status.log" "OK"
echo "B-DOS transcript:"
sed 's/^/    /' "$BUILD/i62-bdos.status.log"

echo "--- post-run evidence: HDF contains the saved file ---"
python3 - "$BUILD/i62-atomlite.hdf" <<'EOF'
import sys

path = sys.argv[1]
data = open(path, "rb").read()
data_off = 22 + 512  # RS-IDE v1.1 header + identify block

# Directory-entry name for the saved file.
name_at = data.find(b"I62DATA", data_off)
if name_at < 0:
    sys.exit("FAIL: 'I62DATA' directory entry not found in HDF")

# First 64 bytes of the deterministic pattern HSAVE wrote
# (f(addr) = (2*low ^ high) & 0xFF starting at &9000).
pat = bytes(((2 * (i & 0xFF)) ^ (0x90 + (i >> 8))) & 0xFF for i in range(64))
pat_at = data.find(pat, data_off)
if pat_at < 0:
    sys.exit("FAIL: saved pattern bytes not found in HDF")

print(f"OK: dir entry at HDF offset {name_at} (sector {(name_at-data_off)//512}), "
      f"pattern at offset {pat_at} (sector {(pat_at-data_off)//512})")
EOF

echo "i62 experiment: BOTH RUNS PASSED"
