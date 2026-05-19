#!/usr/bin/env bash
# llist-capture-headless.sh — Linux variant of llist-capture.sh designed
# to run INSIDE the sam-aarch64-dev container.
#
# Why a separate script: the macOS-side llist-capture.sh uses
# /Applications/SimCoupe.app, $HOME/Documents/SimCoupe, and
# $HOME/Library/Preferences/SimCoupe/SimCoupe.cfg — all macOS-specific.
# Inside the Debian/Ubuntu container, SimCoupé lives at
# /usr/local/bin/simcoupe and stores its config at $HOME/.simcoupe/
# SimCoupe.cfg.
#
# This script assumes Xvfb is already running on $DISPLAY (the Docker
# wrapper sets it up once per container life). Sets SDL_VIDEODRIVER=x11
# + SDL_AUDIODRIVER=dummy per docs/notes/headless-simcoupe.md.
#
# Usage:
#   llist-capture-headless.sh <source.mgt> <basic-file-name> <output.txt>

set -euo pipefail

if [ $# -ne 3 ]; then
    echo "usage: $0 <source.mgt> <basic-file-name> <output.txt>" >&2
    exit 1
fi

source_disk="$1"
basic_name="$2"
output_file="$3"

repo_root="${REPO_ROOT:-/work}"
# Cfg lives at $HOME/.simcoupe/, but SimCoupé writes its auto-named
# simc####.txt output to a different location: PathType::Output in
# SDL/OSD.cpp resolves to $HOME/Desktop (if it exists) or $HOME/SimCoupe.
# In the headless container neither $HOME/Desktop nor an `outpath` cfg
# override exists, so output lands in $HOME/SimCoupe/.
sim_cfg_dir="$HOME/.simcoupe"
sim_cfg="$sim_cfg_dir/SimCoupe.cfg"
sim_output_dir="$HOME/SimCoupe"
test_disk="$(mktemp /tmp/llist-capture-XXXXXX.mgt)"
trap 'rm -f "$test_disk"' EXIT

mkdir -p "$sim_cfg_dir" "$sim_output_dir"
# Seed an empty cfg if SimCoupé has never run in this container.
if [ ! -f "$sim_cfg" ]; then
    touch "$sim_cfg"
fi

# Build the llist-capture binary on-demand inside the container if
# it's not already present (cached across runs in /usr/local/bin).
tool_bin="/usr/local/bin/llist-capture-builder"
if [ ! -x "$tool_bin" ]; then
    (cd "$repo_root/tools/llist-capture" && go build -o "$tool_bin" .)
fi

# Build the test disk.
"$tool_bin" \
    -source "$source_disk" \
    -file "$basic_name" \
    -output "$test_disk" \
    -samdos "$repo_root/reference/samdos/samdos2.bin" > /dev/null

# Flip parallel1=1, printerdev= (empty → auto-named simc####.txt in
# $HOME/.simcoupe/), and crank emulation flat-out: speed=1000 (10x),
# turbodisk=1, fastreset=1. Use the same Python trick as the macOS script.
python3 -c "
path = '$sim_cfg'
overrides = {
    'parallel1': '1',
    'printerdev': '',
    'speed': '1000',
    'turbodisk': '1',
    'fastreset': '1',
}
with open(path) as f: lines = f.readlines()
out = []
seen = set()
for line in lines:
    key = line.split('=', 1)[0] if '=' in line else None
    if key in overrides:
        out.append(f'{key}={overrides[key]}\n'); seen.add(key)
    else:
        out.append(line)
for k, v in overrides.items():
    if k not in seen:
        out.append(f'{k}={v}\n')
with open(path, 'w') as f: f.writelines(out)
"

# Remember newest pre-existing simc*.txt so we can identify the
# one this run produces.
prev_newest=""
if compgen -G "$sim_output_dir/simc*.txt" > /dev/null 2>&1; then
    prev_newest="$(ls -t "$sim_output_dir"/simc*.txt 2>/dev/null | head -1)"
fi

# Run SimCoupé. -exitonhalt 1 makes it quit cleanly on DI;HALT (the
# stub exit signal); the safety timeout backstops any boot/load hang.
# Stderr is captured silent unless something goes wrong.
sim_log="$(mktemp /tmp/simcoupe-log-XXXXXX)"
trap 'rm -f "$test_disk" "$sim_log"' EXIT
# Capture exit status directly. The previous `if ! cmd; then rc=$?; fi`
# pattern is broken: after a `!`-negated pipeline bash sets $? to the
# inverted value, so timeouts (124) were being reported as exit 0 and
# silently swallowed. Disable set -e around the call so we can inspect
# rc ourselves.
set +e
timeout 60s /usr/local/bin/simcoupe \
        -exitonhalt 1 \
        -fullscreen 0 \
        -firstrun 0 \
        "$test_disk" >"$sim_log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 0 ]; then
    echo "ERROR: simcoupe exit $rc, log:" >&2
    cat "$sim_log" >&2
    exit "$rc"
fi

# Pick up the newest simc*.txt that wasn't there before.
new_newest=""
if compgen -G "$sim_output_dir/simc*.txt" > /dev/null 2>&1; then
    new_newest="$(ls -t "$sim_output_dir"/simc*.txt 2>/dev/null | head -1)"
fi

if [ -z "$new_newest" ] || [ "$new_newest" = "$prev_newest" ]; then
    echo "ERROR: no new simc*.txt produced in $sim_output_dir" >&2
    exit 2
fi

cp "$new_newest" "$output_file"
rm -f "$new_newest"
