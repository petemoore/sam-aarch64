#!/usr/bin/env bash
# run-release-sam.sh — run the SAM-side assembler over the stripped release
# .tbn under SimCoupé (in-container), extract OUT to build/release.sam.bin.
# Assumes build/release-stripped.tbn, build/assembler-prod.bin already exist.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Rebuild Go binaries in-container (ELF, not Mach-O).
make -s text2bin enctab build-m3-disk sysreg-data disasm-payload

./build/build-m3-disk \
    -sysreg-data build/sysreg_data.bin \
    -disasm build/disasm.bin \
    build/assembler-prod.bin build/enctab.enc \
    build/release-stripped.tbn build/release-stripped.mgt

./tools/run-simcoupe.sh build/release-stripped.mgt build/release-stripped.status.log

status=$(tr -d '\r\n ' < build/release-stripped.status.log || true)
echo "STATUS: ${status}"

SAMFILE="${SAMFILE:-samfile}"
if ! command -v "$SAMFILE" >/dev/null 2>&1; then
    if [ -x "$HOME/git/samfile/samfile" ]; then SAMFILE="$HOME/git/samfile/samfile"; fi
fi
"$SAMFILE" cat -i build/release-stripped.mgt -f OUT > build/release.sam.bin
echo "OUT bytes: $(stat -c%s build/release.sam.bin 2>/dev/null || stat -f%z build/release.sam.bin)"
