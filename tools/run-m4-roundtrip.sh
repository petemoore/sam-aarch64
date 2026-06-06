#!/usr/bin/env bash
# run-m4-roundtrip.sh — end-to-end M4 fixture round-trip driver.
#
# Pipeline (Layer 2 in docs/specs/2026-05-24-m4-symbols-multipass-design.md
# §3 — same shape as M3's Layer 2 but with a link step in the oracle so
# :lo12: / :hi12: / branch-to-label relocations resolve):
#
#   1. text2bin INPUT.s   → INPUT.tbn
#   2. build-m3-disk assembler.bin enctab.enc INPUT.tbn → OUT.mgt
#      (the M3 assembler binary is M4-capable post-PR-#22 — same .tbn
#       record format, just more record-handler / opcode coverage)
#   3. SimCoupé runs the disk; the assembler reads IN (two passes),
#      writes OUT.
#   4. samfile cat OUT → ours.bin
#   5. aarch64-*-as INPUT.s → INPUT.o
#      aarch64-*-ld -Ttext=0 INPUT.o → INPUT.elf
#      aarch64-*-objcopy -O binary INPUT.elf → gnu.bin
#   6. cmp ours.bin gnu.bin
#
# Why the link step (vs M3's as+objcopy-only oracle):
#
#   M4 fixtures carry relocations (label refs, :lo12:, etc.).
#   `objcopy -O binary` on an unlinked .o leaves the relocatable
#   fields zero and drops the relocation records — so the bytes don't
#   match a real load image.  `ld -Ttext=0` resolves the relocations
#   assuming .text loads at 0, which is the same assumption our
#   pass-1 PC tracker makes.  This is the same oracle M1's
#   tests/m1/run-refenc-roundtrip.sh has used since M1; see its
#   header for the M1-spec citation.
#
# Empty fixtures: not supported here (an M4 fixture by definition has
# at least one record); use M3 for empty.s.
#
# Usage: run-m4-roundtrip.sh <fixture.s>

set -euo pipefail

if [ $# -ne 1 ]; then
    echo "usage: $0 <fixture.s>" >&2
    exit 2
fi

fixture="$1"
base=$(basename "$fixture" .s)
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

cd "$ROOT"

# -------------------------------------------------------------------------
# Toolchain selection.  Try aarch64-none-elf-* first (Pete's macOS
# install), fall back to aarch64-linux-gnu-* (dev container / CI).
# -------------------------------------------------------------------------
AS="${AARCH64_AS:-aarch64-none-elf-as}"
LD="${AARCH64_LD:-aarch64-none-elf-ld}"
OBJCOPY="${AARCH64_OBJCOPY:-aarch64-none-elf-objcopy}"

if ! command -v "$AS" >/dev/null 2>&1; then
    AS="aarch64-linux-gnu-as"
    LD="aarch64-linux-gnu-ld"
    OBJCOPY="aarch64-linux-gnu-objcopy"
fi

for tool in "$AS" "$LD" "$OBJCOPY"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "ERROR: $tool not found on PATH" >&2
        exit 2
    fi
done

# -------------------------------------------------------------------------
# Build the Mac-side artefacts.
# -------------------------------------------------------------------------
echo "=== M4 round-trip: $fixture ==="

mkdir -p build

# Mac-side tools.  ASSEMBLER_BIN is per-variant — see header of
# tools/run-m3-roundtrip.sh for the test vs prod rationale.
ASSEMBLER_BIN="${ASSEMBLER_BIN:-$ROOT/build/assembler.bin}"
make -s text2bin enctab build-m3-disk sysreg-data disasm-payload

if [ ! -f "$ASSEMBLER_BIN" ]; then
    echo "ERROR: assembler binary not found: $ASSEMBLER_BIN" >&2
    echo "  (build it with 'make m3-asm' or 'make m3-asm-prod')" >&2
    exit 2
fi

# 1. text2bin → INPUT.tbn
echo "--- text2bin ---"
"$ROOT/build/text2bin" -o "build/${base}.tbn" "$fixture"

# 2. build-m3-disk with the .tbn as IN.
#
# For the test variant only, also deposit:
#   - the off-axis test_mem.bin (plan-PR 3), and
#   - the paged_call self-test page-14 payload (plan-PR 1).
# See docs/notes/2026-05-28-paged-call-architecture.md.
echo "--- build-m3-disk ---"
test_variant_flags=()
if [ "$ASSEMBLER_BIN" = "$ROOT/build/assembler.bin" ]; then
    make -s test-mem-offaxis paged-call-payload cluster-offaxis
    test_variant_flags=(
        -test-mem "$ROOT/build/test_mem.bin"
        -paged-call "$ROOT/build/paged_call_test_payload.bin"
        -cluster "$ROOT/build/test_cluster.bin"
    )
fi
"$ROOT/build/build-m3-disk" \
    "${test_variant_flags[@]}" \
    -sysreg-data "$ROOT/build/sysreg_data.bin" \
    -disasm "$ROOT/build/disasm.bin" \
    "$ASSEMBLER_BIN" build/enctab.enc \
    "build/${base}.tbn" \
    "build/${base}.mgt"

# 3. Run SimCoupé.  See run-m3-roundtrip.sh for the printer-channel
#    status-banner rationale.
echo "--- simcoupe ---"
"$ROOT/tools/run-simcoupe.sh" "build/${base}.mgt" "build/${base}.status.log"

status=$(tr -d '\r\n ' < "build/${base}.status.log" || true)
if [ "$status" != "OK" ]; then
    echo "FAIL: $base — assembler emitted status '${status}' (expected 'OK')"
    if [ -s "build/${base}.status.log" ]; then
        echo "  printer log:"
        sed 's/^/    /' "build/${base}.status.log"
    else
        echo "  (printer log empty — SimCoupé may have hung or crashed before status emit)"
    fi
    exit 1
fi

# 4. Extract OUT.
echo "--- extract OUT ---"
SAMFILE="${SAMFILE:-samfile}"
if ! command -v "$SAMFILE" >/dev/null 2>&1; then
    if [ -x "$HOME/git/samfile/samfile" ]; then
        SAMFILE="$HOME/git/samfile/samfile"
    else
        echo "ERROR: samfile not found on PATH" >&2
        exit 2
    fi
fi
"$SAMFILE" cat -i "build/${base}.mgt" -f OUT > "build/${base}.bin"

# 5. GNU oracle — as + ld -Ttext=0 + objcopy.
echo "--- GNU oracle (as + ld -Ttext=0 + objcopy) ---"
"$AS" "$fixture" -o "build/${base}.o"
"$LD" -Ttext=0 -o "build/${base}.elf" "build/${base}.o" 2>&1 \
    | grep -v "cannot find entry symbol _start" || true
"$OBJCOPY" -O binary "build/${base}.elf" "build/${base}.gnu.bin"

# 6. Byte-compare.
echo "--- cmp ---"
if cmp -s "build/${base}.bin" "build/${base}.gnu.bin"; then
    bytes=$(wc -c < "build/${base}.bin" | tr -d ' ')
    echo "PASS: $base ($bytes bytes)"
else
    ours_sz=$(wc -c < "build/${base}.bin" | tr -d ' ')
    gnu_sz=$(wc -c < "build/${base}.gnu.bin" | tr -d ' ')
    echo "FAIL: $base"
    echo "  ours: $ours_sz bytes"
    echo "  gnu : $gnu_sz bytes"
    echo "  ours hex:"
    od -An -tx1 "build/${base}.bin" | head -4
    echo "  gnu hex:"
    od -An -tx1 "build/${base}.gnu.bin" | head -4
    exit 1
fi
