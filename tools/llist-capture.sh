#!/usr/bin/env bash
# llist-capture.sh — capture the SAM ROM's LLIST output for a named
# BASIC file from a corpus disk, using SimCoupé as a deterministic
# "what would LIST/LLIST actually emit?" oracle.
#
# Mechanism (see tools/llist-capture/main.go for details):
#   1. Build a test disk that, on boot, loads a 2-byte halt stub
#      and then LOADs the target BASIC file (with auto-RUN forced
#      to a synthesised line at 65279 that LLISTs the original
#      lines then DI;HALTs).
#   2. Configure SimCoupé's parallel port for printer-to-file.
#   3. Run SimCoupé with -exitonhalt 1 (via run-simcoupe.sh).
#   4. Find the newest simc####.txt in ~/Documents/SimCoupe/ and
#      emit its contents on stdout.
#
# Usage:
#   llist-capture.sh <source.mgt> <basic-file-name> [<output.txt>]
#
# Examples:
#   llist-capture.sh ~/sam-corpus/disks/avp.mgt avp.music
#   llist-capture.sh ~/sam-corpus/disks/whatever.mgt PROGNAME /tmp/out.txt

set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
    echo "usage: $0 <source.mgt> <basic-file-name> [<output.txt>]" >&2
    exit 1
fi

source_disk="$1"
basic_name="$2"
output_file="${3:-/dev/stdout}"

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
samcoupe_data="$HOME/Documents/SimCoupe"
sim_cfg="$HOME/Library/Preferences/SimCoupe/SimCoupe.cfg"
test_disk="$(mktemp -t llist-capture-XXXXXX).mgt"
trap 'rm -f "$test_disk"' EXIT

mkdir -p "$samcoupe_data"

# Build the llist-capture tool on demand (mirrors build-disk.sh's
# fallback approach for native macOS).
tool_bin="$repo_root/tools/llist-capture/llist-capture"
tool_src="$repo_root/tools/llist-capture/main.go"
if [ ! -x "$tool_bin" ] \
    || [ "$tool_src" -nt "$tool_bin" ] \
    || [ "$repo_root/tools/llist-capture/go.mod" -nt "$tool_bin" ]; then
    (cd "$repo_root/tools/llist-capture" && go build -o llist-capture .)
fi

# Build the test disk.
"$tool_bin" \
    -source "$source_disk" \
    -file "$basic_name" \
    -output "$test_disk" \
    -samdos "$repo_root/reference/samdos/samdos2.bin" > /dev/null

# Snapshot the SimCoupé cfg so we can flip parallel1=1 for this run
# without permanently mutating user preferences.
cfg_backup="$(mktemp -t llist-capture-cfg-XXXXXX)"
cp "$sim_cfg" "$cfg_backup"
restore_cfg() {
    cp "$cfg_backup" "$sim_cfg"
    rm -f "$cfg_backup"
}
trap 'rm -f "$test_disk"; restore_cfg' EXIT

# Flip parallel1 to 1 (printer) and clear printerdev so SimCoupé uses
# its auto-generated simc####.txt file in Documents/SimCoupe/.
python3 -c "
import sys
path = '$sim_cfg'
with open(path) as f: lines = f.readlines()
out = []
seen_p1 = False
seen_pdev = False
for line in lines:
    if line.startswith('parallel1='):
        out.append('parallel1=1\n'); seen_p1 = True
    elif line.startswith('printerdev='):
        out.append('printerdev=\n'); seen_pdev = True
    else:
        out.append(line)
if not seen_p1: out.append('parallel1=1\n')
if not seen_pdev: out.append('printerdev=\n')
with open(path, 'w') as f: f.writelines(out)
"

# Remember the newest pre-existing simc*.txt so we can identify the
# one this run produces.
prev_newest=""
if compgen -G "$samcoupe_data/simc*.txt" > /dev/null 2>&1; then
    prev_newest="$(ls -t "$samcoupe_data"/simc*.txt 2>/dev/null | head -1)"
fi

# Run SimCoupé. run-simcoupe.sh handles the timeout and exitonhalt.
"$repo_root/tools/run-simcoupe.sh" "$test_disk"

# Pick up the newest simc*.txt that wasn't there before.
new_newest=""
if compgen -G "$samcoupe_data/simc*.txt" > /dev/null 2>&1; then
    new_newest="$(ls -t "$samcoupe_data"/simc*.txt 2>/dev/null | head -1)"
fi

if [ -z "$new_newest" ] || [ "$new_newest" = "$prev_newest" ]; then
    echo "ERROR: no new simc*.txt was produced in $samcoupe_data" >&2
    echo "   (parallel1 may not have been honoured, or SimCoupé halted before LLIST)" >&2
    exit 2
fi

cat "$new_newest" > "$output_file"

if [ "$output_file" != "/dev/stdout" ]; then
    echo "$new_newest -> $output_file" >&2
fi
