#!/usr/bin/env bash
# run-roundtrip.sh — unified end-to-end fixture round-trip driver.
#
# Usage: run-roundtrip.sh <corpus> <fixture.s>
#   corpus ∈ core | symbols | operands | paged
#
# Pipeline per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m3-z80-emitter-design.md §3 (Layer 2):
#
#   1. sam-aarch64 INPUT.s → INPUT.img + INPUT.compact.tbn
#   2. build-disk assembler.bin enctab.enc INPUT.compact.tbn → OUT.mgt
#   3. SimCoupé runs the disk; the assembler reads IN, writes OUT.
#   4. samfile cat OUT → ours.bin
#   5. GNU oracle (corpus-dependent):
#      core:    aarch64-*-as INPUT.s | objcopy -O binary → gnu.bin
#      symbols/operands/paged:
#               aarch64-*-as INPUT.s → INPUT.o
#               aarch64-*-ld -Ttext=0 INPUT.o → INPUT.elf
#               aarch64-*-objcopy -O binary INPUT.elf → gnu.bin
#   6. cmp ours.bin gnu.bin
#
# The `ld -Ttext=0` step (corpora symbols/operands/paged) resolves
# :lo12:/:hi12: and branch-to-label relocations — the same assumption our
# pass-1 PC tracker makes.
#
# Empty fixtures (core only) are special-cased: an empty source produces
# no HSAVE call; the GNU oracle is `:`.

set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <corpus> <fixture.s>" >&2
    echo "  corpus ∈ core | symbols | operands | paged" >&2
    exit 2
fi

corpus="$1"
fixture="$2"
base=$(basename "$fixture" .s)
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

case "$corpus" in
    core|symbols|operands|paged) ;;
    *) echo "ERROR: unknown corpus '$corpus' (must be core|symbols|operands|paged)" >&2; exit 2 ;;
esac

cd "$ROOT"

# -------------------------------------------------------------------------
# Toolchain selection.
# -------------------------------------------------------------------------
AS="${AARCH64_AS:-aarch64-none-elf-as}"
LD="${AARCH64_LD:-aarch64-none-elf-ld}"
OBJCOPY="${AARCH64_OBJCOPY:-aarch64-none-elf-objcopy}"

if ! command -v "$AS" >/dev/null 2>&1; then
    # Linux-style binutils names (dev container, ubuntu CI runners).
    AS="aarch64-linux-gnu-as"
    LD="aarch64-linux-gnu-ld"
    OBJCOPY="aarch64-linux-gnu-objcopy"
fi

if ! command -v "$AS" >/dev/null 2>&1; then
    echo "ERROR: aarch64-{none-elf,linux-gnu}-as not found on PATH" >&2
    exit 2
fi

# The ld step is required for all corpora except core.
if [ "$corpus" != "core" ]; then
    for tool in "$LD" "$OBJCOPY"; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            echo "ERROR: $tool not found on PATH" >&2
            exit 2
        fi
    done
fi

# -------------------------------------------------------------------------
# Build the host-side artefacts.
# -------------------------------------------------------------------------
echo "=== ${corpus} round-trip: $fixture ==="

mkdir -p build

# ASSEMBLER_BIN selects between the test build (default — includes
# boot-time self-tests) and the production build (set
# ASSEMBLER_BIN=build/assembler-prod.bin).  Caller is responsible for
# ensuring the chosen binary is built; we don't `make assembler` here
# because that would force the test variant even when the caller wants
# the prod variant.
ASSEMBLER_BIN="${ASSEMBLER_BIN:-$ROOT/build/assembler.bin}"
make -s sam-aarch64 enctab build-disk sysreg-data disasm-payload zx0-payload

if [ ! -f "$ASSEMBLER_BIN" ]; then
    echo "ERROR: assembler binary not found: $ASSEMBLER_BIN" >&2
    echo "  (build it with 'make assembler' or 'make assembler-prod')" >&2
    exit 2
fi

# 1. sam-aarch64 INPUT.s → INPUT.img + INPUT.compact.tbn.
#
# The SAM assembler consumes the COMPACT v2 .tbn (the INSN_RUN decoder);
# the integrated tool assembles the source and emits the compact .tbn
# directly via --emit-tbn.  build-disk below gets the compact .tbn as IN.
echo "--- sam-aarch64 (assemble + emit compact .tbn) ---"
"$ROOT/build/sam-aarch64" -o "build/${base}.img" --emit-tbn "build/${base}.compact.tbn" "$fixture"

# 2. build-disk with the COMPACT .tbn as IN.
#
# For the test variant only, also deposit:
#   - the off-axis test_mem.bin (HLOADed into physical page 13), and
#   - the paged_call self-test page-14 payload.
# The disasm + zx0 self-tests run at boot only under BUILD_TESTS (the
# test assembler), so the test disk ships disasm-test.bin + zx0-test.bin
# and prod disks ship the stripped disasm.bin + zx0.bin.  The enc-tests
# variant (BUILD_TESTS_ENCODE, i234) deposits the page-11 enc_fix
# fixture payload on top of the prod set.
echo "--- build-disk ---"
test_variant_flags=()
disasm_bin="$ROOT/build/disasm.bin"
zx0_bin="$ROOT/build/zx0.bin"
# i207: pass -variant so build-disk refuses to build a disk missing any payload
# the boot loader HLOADs (a missing one silently HANGS SimCoupé — the i69 bug,
# where this script once omitted -enc-fix). prod by default; the self-test
# variants below.
disk_variant="prod"
if [ "$ASSEMBLER_BIN" = "$ROOT/build/assembler.bin" ]; then
    disk_variant="test"
    # Test variant — needs the off-axis payloads + the self-test disasm + zx0.
    make -s test-mem-offaxis paged-call-payload cluster-offaxis disasm-test-payload zx0-test-payload
    disasm_bin="$ROOT/build/disasm-test.bin"
    zx0_bin="$ROOT/build/zx0-test.bin"
    test_variant_flags=(
        -test-mem "$ROOT/build/test_mem.bin"
        -paged-call "$ROOT/build/paged_call_test_payload.bin"
        -cluster "$ROOT/build/test_cluster.bin"
    )
elif [ "$ASSEMBLER_BIN" = "$ROOT/build/assembler-enc-tests.bin" ]; then
    # Encode self-test variant (i234) — needs the page-11 enc_fix fixture
    # payload (without it on the disk the encode boot self-test hangs);
    # disasm/zx0 stay the prod binaries (no disasm/zx0 self-test runs in
    # this variant).
    disk_variant="enc-tests"
    make -s enc-fix-payload
    test_variant_flags=(
        -enc-fix "$ROOT/build/enc_fix_payload.bin"
    )
fi
"$ROOT/build/build-disk" \
    -variant "$disk_variant" \
    "${test_variant_flags[@]}" \
    -sysreg-data "$ROOT/build/sysreg_data.bin" \
    -disasm "$disasm_bin" \
    -zx0 "$zx0_bin" \
    "$ASSEMBLER_BIN" build/enctab.enc \
    "build/${base}.compact.tbn" \
    "build/${base}.mgt"

# 3. Run SimCoupé. The wrapper exits ~1 s in either case (both paths
#    do `DI; HALT`) and writes a status file containing "OK" or "FAIL"
#    captured from the SAM's parallel printer channel.  See
#    src/print.asm + tools/run-simcoupe.sh.
echo "--- simcoupe ---"
"$ROOT/tools/run-simcoupe.sh" "build/${base}.mgt" "build/${base}.status.log"

# 3a. Check the SAM-side status banner before bothering with the byte
#     compare.  A "FAIL" status indicates a boot-time self-test failure.
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

# 5. GNU oracle — corpus-dependent.
if [ "$corpus" = "core" ]; then
    # core corpus: as + objcopy only (no relocations to resolve).
    # Empty fixtures are special-cased: no sections → no output.
    echo "--- GNU oracle (as + objcopy) ---"
    "$AS" "$fixture" -o "build/${base}.o"
    "$OBJCOPY" -O binary "build/${base}.o" "build/${base}.gnu.bin" 2>/dev/null || : > "build/${base}.gnu.bin"
else
    # symbols/operands/paged: as + ld -Ttext=0 + objcopy.
    # The link step resolves :lo12:/:hi12: and branch-to-label relocations.
    echo "--- GNU oracle (as + ld -Ttext=0 + objcopy) ---"
    "$AS" "$fixture" -o "build/${base}.o"
    "$LD" -Ttext=0 -o "build/${base}.elf" "build/${base}.o" 2>&1 \
        | grep -v "cannot find entry symbol _start" || true
    "$OBJCOPY" -O binary "build/${base}.elf" "build/${base}.gnu.bin"
fi

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
