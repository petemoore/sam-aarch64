#!/usr/bin/env bash
# llist-vs-b2t.sh — compare the SAM ROM's LLIST output for a named
# BASIC file against `samfile basic-to-text` for the same file body.
#
# Used as a test harness for basic-to-text: the LLIST capture is the
# canonical "what would the SAM ROM emit?" oracle. Any divergence is
# a detok-side discrepancy that may need fixing or documenting.
#
# Usage:
#   llist-vs-b2t.sh <source.mgt> <basic-file-name>
#
# Emits the unified diff (LLIST minus basic-to-text) on stdout. Exit
# code is 0 if identical, 1 otherwise. SAMFILE_BIN env var can point
# at the samfile binary (default: looks up via PATH or builds the
# samfile repo).

set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <source.mgt> <basic-file-name>" >&2
    exit 2
fi

source_disk="$1"
basic_name="$2"

repo_root="$(cd "$(dirname "$0")/.." && pwd)"

samfile_bin="${SAMFILE_BIN:-}"
if [ -z "$samfile_bin" ]; then
    if command -v samfile >/dev/null 2>&1; then
        samfile_bin="$(command -v samfile)"
    elif [ -d "$HOME/git/samfile" ]; then
        samfile_bin="$(mktemp -t samfile-XXXXXX)"
        (cd "$HOME/git/samfile" && go build -o "$samfile_bin" ./cmd/samfile)
    else
        echo "ERROR: samfile not found. Set SAMFILE_BIN or install samfile." >&2
        exit 2
    fi
fi

llist_raw="$(mktemp -t llist-raw-XXXXXX.txt)"
llist_out="$(mktemp -t llist-XXXXXX.txt)"
b2t_out="$(mktemp -t b2t-XXXXXX.txt)"
trap 'rm -f "$llist_raw" "$llist_out" "$b2t_out"' EXIT

# Capture the SAM ROM's LLIST output via SimCoupé.
"$repo_root/tools/llist-capture.sh" "$source_disk" "$basic_name" "$llist_raw"

# Normalise line endings: SimCoupé's print-to-file emits 0D 0A
# (CRLF) per the SAM printer convention. basic-to-text emits 0A
# only. Strip the CRs. LC_ALL=C so `tr` treats high-bit SAM bytes
# (0x80-0xFF for graphics / UDGs) as raw bytes rather than failing
# on invalid UTF-8.
LC_ALL=C tr -d '\r' < "$llist_raw" > "$llist_out"

# Render the same file via samfile basic-to-text.
"$samfile_bin" cat -i "$source_disk" -f "$basic_name" \
    | "$samfile_bin" basic-to-text > "$b2t_out"

# Show the diff. Use `-u` for context.
if diff -u "$b2t_out" "$llist_out"; then
    echo "MATCH: basic-to-text output is identical to SAM ROM LLIST output." >&2
    exit 0
fi
exit 1
