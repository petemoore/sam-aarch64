#!/usr/bin/env bash
#
# tests/disasm/run-oracle-comparison.sh — the strand-B disassembler oracle.
#
# Disassembles the vendored spectrum4 release.img with our aarch64dec and
# with binutils objdump, and asserts the two agree line-for-line.  This is
# the TDD target for the Go-side disassembler: it starts RED (the decoder is
# incomplete) and the diff is the worklist — each instruction family we
# teach aarch64dec shrinks it, and "done" is a clean diff.
#
# Why release.img: it's the real spectrum4 kernel binary (already vendored
# for the m6-release gate), so it exercises the actual instruction mix the
# project emits — including data interleaved with code.  objdump linear-
# sweeps the flat binary just as we do, so wherever it renders non-code as
# `.inst`/`udf` we must match that rendering too (those are just more lines
# to agree on, not a special case).
#
# objdump -z disables its zero-run collapsing (`...`) so the two outputs are
# 1:1 by line.  Both sides are normalised (strip address-column padding,
# drop objdump's cosmetic `// b.any`-style alias comments, squeeze runs of
# whitespace to single spaces) so the comparison is on semantic content —
# address, bytes, mnemonic, operands — not column formatting.
#
# Env:
#   OBJDUMP        objdump to use (default: first of aarch64-linux-gnu-objdump,
#                  aarch64-none-elf-objdump found on PATH)
#   RELEASE_IMG    binary to disassemble (default: tests/m6/release/release.img)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

RELEASE_IMG="${RELEASE_IMG:-tests/m6/release/release.img}"
if [ ! -f "$RELEASE_IMG" ]; then
    echo "ERROR: release image not found at $RELEASE_IMG" >&2
    exit 2
fi

# Pick an objdump.
if [ -z "${OBJDUMP:-}" ]; then
    for cand in aarch64-linux-gnu-objdump aarch64-none-elf-objdump; do
        if command -v "$cand" >/dev/null 2>&1; then OBJDUMP="$cand"; break; fi
    done
fi
if [ -z "${OBJDUMP:-}" ] || ! command -v "$OBJDUMP" >/dev/null 2>&1; then
    echo "ERROR: no aarch64 objdump found (set OBJDUMP=...)" >&2
    exit 2
fi

# Build our disassembler CLI.
mkdir -p build
( cd tools/aarch64dec/cmd/aarch64dec && go build -o "$ROOT/build/aarch64dec" . )

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Shared normaliser: strip objdump's trailing comments — both `// ...`
# (e.g. `b.ne 0x1e8  // b.any`) and `; ...` (e.g. `.inst 0x00010001 ;
# undefined`); these are cosmetic annotations, not semantic content.
# Then tabs->spaces, strip the leading address-column padding, squeeze
# internal whitespace so the comparison is on address/bytes/mnemonic/operands.
norm() {
    sed -E 's#[[:space:]]*//.*$##; s#[[:space:]]*;.*$##' \
        | tr '\t' ' ' \
        | sed -E 's/^ +//; s/ +/ /g; s/ +$//'
}

"$OBJDUMP" -D -z -b binary -m aarch64 "$RELEASE_IMG" 2>/dev/null \
    | grep -E '^[[:space:]]+[0-9a-f]+:' \
    | norm > "$tmp/objdump.txt"

"$ROOT/build/aarch64dec" "$RELEASE_IMG" \
    | norm > "$tmp/ours.txt"

total="$(wc -l < "$tmp/objdump.txt" | tr -d ' ')"
ours_n="$(wc -l < "$tmp/ours.txt" | tr -d ' ')"

if [ "$total" != "$ours_n" ]; then
    echo "FAIL: line-count mismatch — objdump=$total ours=$ours_n (decoder dropped/added lines)" >&2
    { diff -u "$tmp/objdump.txt" "$tmp/ours.txt" || true; } | head -40 >&2
    exit 1
fi

# diff returns 1 when files differ; guard so `set -e` doesn't abort here.
diffn="$( { diff -y --suppress-common-lines "$tmp/objdump.txt" "$tmp/ours.txt" || true; } 2>/dev/null | wc -l | tr -d ' ')"
match="$(awk "BEGIN{printf \"%.1f\", 100 - 100*$diffn/$total}")"

if [ "$diffn" -eq 0 ]; then
    echo "PASS: aarch64dec matches $OBJDUMP on $RELEASE_IMG ($total lines)"
    exit 0
fi

echo "FAIL: aarch64dec differs from $OBJDUMP on $RELEASE_IMG" >&2
echo "  $diffn / $total lines differ (${match}% match)" >&2
echo "  worklist — differing lines bucketed by objdump mnemonic:" >&2
paste -d'|' "$tmp/objdump.txt" "$tmp/ours.txt" \
    | awk -F'|' '$1!=$2{split($1,a," "); print a[3]}' \
    | sort | uniq -c | sort -rn | head -25 >&2
echo "  (sample diffs: diff -y on the two normalised dumps)" >&2
exit 1
