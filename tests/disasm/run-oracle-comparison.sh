#!/usr/bin/env bash
#
# tests/disasm/run-oracle-comparison.sh — the strand-B disassembler oracle.
#
# Disassembles a CODE-ONLY build of the spectrum4 release with both our
# aarch64dec and binutils objdump, and asserts they agree on every real
# instruction.  This is the strand-B disassembler gate.
#
# Why code-only: release.img interleaves data (strings, tables, page-table
# entries) with code.  objdump linear-sweeps the flat binary and renders
# those data bytes as whatever they happen to encode — exotic SIMD/SVE/SME/
# atomic instructions — which is noise, not a disassembler gap.  So instead
# of disassembling the real release.img, we take the vendored flattened
# release SOURCE (tests/m6/release/release.s), strip the data-emitting
# directives (.hword/.byte/.asciz/.word/.quad/.space/…) while keeping every
# label + instruction, and reassemble with GNU binutils into a binary that
# is *only* instructions.  Disassembling that is a clean apples-to-apples
# comparison against objdump.  (This stripping is for the disassembler oracle
# ONLY — text2bin and the m6-release byte-match gate keep the full source,
# data and all.)
#
# Pass criterion — NO MIS-DECODES.  A handful of words still differ: GNU as
# materialises `ldr wN, =0xNNNN` constants as a literal pool (a .word in
# .text) that objdump linear-sweeps as an instruction.  Those are data; our
# decoder correctly DECLINES them (renders `.inst 0x…`).  Declining on
# out-of-scope/data encodings is correct; emitting a *different* instruction
# than objdump is a bug.  So the gate fails only on a mis-decode (a differing
# line where our side is not `.inst`), and additionally guards coverage with
# a match-percentage floor.
#
# Env: OBJDUMP / AS / LD / OBJCOPY override the binutils tools (defaults try
# the aarch64-linux-gnu-* then aarch64-none-elf-* variants); RELEASE_S the
# source (default tests/m6/release/release.s); MIN_MATCH the coverage floor.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

RELEASE_S="${RELEASE_S:-tests/m6/release/release.s}"
MIN_MATCH="${MIN_MATCH:-99.0}"
[ -f "$RELEASE_S" ] || { echo "ERROR: release source not found at $RELEASE_S" >&2; exit 2; }

# Resolve a binutils tool from a list of candidate names.
pick() {
    local var="$1"; shift
    if [ -n "${!var:-}" ]; then command -v "${!var}" >/dev/null 2>&1 && { echo "${!var}"; return; }; fi
    local c; for c in "$@"; do command -v "$c" >/dev/null 2>&1 && { echo "$c"; return; }; done
    echo "ERROR: none of [$*] found (set $var=...)" >&2; exit 2
}
AS="$(pick AS aarch64-linux-gnu-as aarch64-none-elf-as)"
LD="$(pick LD aarch64-linux-gnu-ld aarch64-none-elf-ld)"
OBJCOPY="$(pick OBJCOPY aarch64-linux-gnu-objcopy aarch64-none-elf-objcopy)"
OBJDUMP="$(pick OBJDUMP aarch64-linux-gnu-objdump aarch64-none-elf-objdump)"

mkdir -p build
( cd tools/aarch64dec/cmd/aarch64dec && go build -o "$ROOT/build/aarch64dec" . )

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# 1. Strip data-emitting directives, keeping labels + instructions.  No
#    label-and-data-directive-on-one-line cases exist in release.s (verified),
#    so a line-delete is sufficient.  Portable ERE (no \b — BSD sed lacks it):
#    match a leading-optional-whitespace data directive followed by space/EOL.
grep -vE '^[[:space:]]*\.(hword|byte|asciz|ascii|string|quad|word|2byte|4byte|8byte|double|float|fill|zero|space|skip|octa)([[:space:]]|$)' \
    "$RELEASE_S" > "$tmp/code-only.s"

# 2. Assemble → link → flat binary (instructions only).
"$AS" -o "$tmp/co.o" "$tmp/code-only.s"
"$LD" -Ttext=0 -o "$tmp/co.elf" "$tmp/co.o"
"$OBJCOPY" -O binary -j .text "$tmp/co.elf" "$tmp/co.img"

# 3. Disassemble both ways, normalise (strip address-column padding +
#    objdump's `// …` / `; …` comments, squeeze whitespace → compare on
#    address/bytes/mnemonic/operands).
norm() { sed -E 's#[[:space:]]*//.*$##; s#[[:space:]]*;.*$##' | tr '\t' ' ' | sed -E 's/^ +//; s/ +/ /g; s/ +$//'; }
"$OBJDUMP" -D -z -b binary -m aarch64 "$tmp/co.img" 2>/dev/null \
    | grep -E '^[[:space:]]+[0-9a-f]+:' | norm > "$tmp/objdump.txt"
"$ROOT/build/aarch64dec" "$tmp/co.img" | norm > "$tmp/ours.txt"

total="$(wc -l < "$tmp/objdump.txt" | tr -d ' ')"
ours_n="$(wc -l < "$tmp/ours.txt" | tr -d ' ')"
if [ "$total" != "$ours_n" ]; then
    echo "FAIL: line-count mismatch — objdump=$total ours=$ours_n" >&2
    { diff -u "$tmp/objdump.txt" "$tmp/ours.txt" || true; } | head -40 >&2
    exit 1
fi

# 4. Classify the differing lines into mis-decodes (ours is a real but WRONG
#    instruction — a bug) vs declines (ours is `.inst …` — acceptable on the
#    literal-pool data words objdump sweeps as instructions).
paste -d'\t' "$tmp/objdump.txt" "$tmp/ours.txt" \
    | awk -F'\t' '{
        if ($1 != $2) {
            n = split($2, b, " ")
            if (b[3] == ".inst") { declines++; dl = dl sprintf("    decline [%s] objdump: %s\n", $2, $1) }
            else { misdecodes++; ml = ml sprintf("    MIS-DECODE ours:[%s] objdump:[%s]\n", $2, $1) }
        }
    } END {
        printf "%d %d\n", misdecodes+0, declines+0 > "/dev/stderr"
        if (misdecodes) printf "%s", ml
        if (declines)   printf "%s", dl
    }' > "$tmp/diffinfo.txt" 2> "$tmp/counts.txt"
read -r misdecodes declines < "$tmp/counts.txt"

diffn=$((misdecodes + declines))
match="$(awk "BEGIN{printf \"%.2f\", 100 - 100*$diffn/$total}")"

echo "aarch64dec vs $OBJDUMP on code-only release ($total instructions):"
echo "  match ${match}%  —  mis-decodes: $misdecodes, declines (literal-pool data): $declines"

fail=0
if [ "$misdecodes" -ne 0 ]; then
    echo "FAIL: $misdecodes mis-decode(s) — aarch64dec emitted a different instruction than objdump:" >&2
    grep MIS-DECODE "$tmp/diffinfo.txt" >&2 || true
    fail=1
fi
# Coverage floor: catches a regression that turns matches into declines.
if awk "BEGIN{exit !($match < $MIN_MATCH)}"; then
    echo "FAIL: match ${match}% below floor ${MIN_MATCH}% (coverage regressed)" >&2
    fail=1
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "PASS: no mis-decodes; ${match}% of code-only release matches objdump"
echo "  (the $declines declined words are GNU-as literal-pool constants — data, correctly rendered .inst)"
exit 0
