#!/usr/bin/env bash
# Extracts the OUT file from a .mgt into a host file.
#
# Usage: extract-output.sh <disk.mgt> <output.bin>

set -euo pipefail

disk="$1"
output="$2"

cd "$(dirname "$0")/.."

samfile cat -i "$disk" -f OUT > "$output"

if [ ! -s "$output" ]; then
    echo "extract-output.sh: $output is empty (stub didn't write OUT, or filename mismatch)" >&2
    exit 1
fi

echo "Extracted $(wc -c < "$output") bytes → $output"
