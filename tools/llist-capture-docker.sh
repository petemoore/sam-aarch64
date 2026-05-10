#!/usr/bin/env bash
# llist-capture-docker.sh — Mac-side wrapper that runs llist-capture
# headlessly INSIDE the sam-aarch64-dev Docker container, so SimCoupé
# never opens an SDL window on the host desktop and never steals input
# focus from Pete's actively-used Mac apps.
#
# Same interface as tools/llist-capture.sh (drop-in replacement for
# basic-detokeniser-sweep's --llist-capture flag):
#
#   llist-capture-docker.sh <source.mgt> <basic-file-name> <output.txt>
#
# Path translation: rewrites the Mac-side <source.mgt> and <output.txt>
# arguments to container paths via the volume mounts. Both the corpus
# disk and the output file must live under one of the mounted prefixes
# below or the script will refuse — refusing beats silently writing
# nowhere.
#
# Container management:
#   - Single long-running container named ${CONTAINER_NAME:-sam-detok}.
#     Reused across calls.
#   - Started lazily on first call with mounts:
#       $REPO_ROOT → /work
#       $CORPUS_DIR → /corpus    (default ~/sam-corpus)
#       $CAPTURES_DIR → /captures (default /tmp/detok-captures)
#   - Xvfb is started inside the container once per container life.
#
# Idempotent: re-running is safe and cheap (just docker exec).

set -euo pipefail

if [ $# -ne 3 ]; then
    echo "usage: $0 <source.mgt> <basic-file-name> <output.txt>" >&2
    exit 1
fi

source_disk="$1"
basic_name="$2"
output_file="$3"

container_name="${CONTAINER_NAME:-sam-detok}"
image="${IMAGE:-ghcr.io/petemoore/sam-aarch64-dev:latest}"
repo_root="${REPO_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
corpus_dir="${CORPUS_DIR:-$HOME/sam-corpus}"
captures_dir="${CAPTURES_DIR:-/tmp/detok-captures}"
samfile_dir="${SAMFILE_DIR:-$HOME/git/samfile}"
display="${DISPLAY_NUM:-:150}"

mkdir -p "$captures_dir"

# Ensure container exists + is running.
ensure_container() {
    local state
    # docker inspect on a missing container emits a blank line on stdout
    # before the error on stderr (!), so don't combine via `||` — that
    # produces "\nmissing" which doesn't match any case. Split into two
    # statements so an empty/blank docker result stays empty.
    state="$(docker inspect -f '{{.State.Status}}' "$container_name" 2>/dev/null)" || state=missing
    state="${state//[$'\n\r ']/}"
    case "$state" in
    running)
        return 0
        ;;
    exited|created)
        docker start "$container_name" > /dev/null
        ;;
    missing)
        # Try to pull the image first; fall back to a locally-tagged
        # variant if the pull fails (offline / no GHCR auth).
        if ! docker image inspect "$image" > /dev/null 2>&1; then
            docker pull "$image" > /dev/null 2>&1 || true
        fi
        if ! docker image inspect "$image" > /dev/null 2>&1; then
            # Last resort: try the local sam-aarch64-dev:local tag.
            if docker image inspect sam-aarch64-dev:local > /dev/null 2>&1; then
                image=sam-aarch64-dev:local
            else
                echo "ERROR: no usable sam-aarch64-dev image. Pull '$image' or build it via:" >&2
                echo "    docker build -t sam-aarch64-dev:local -f $repo_root/tools/Dockerfile.dev $repo_root/tools/" >&2
                exit 3
            fi
        fi
        # Mount samfile at the same absolute path the patched
        # tools/llist-capture/go.mod expects ($samfile_dir), so the
        # `replace github.com/petemoore/samfile/v3 => ...` directive
        # resolves identically in container and on the Mac.
        docker run -d --rm \
            --name "$container_name" \
            -v "$repo_root":/work \
            -v "$corpus_dir":/corpus \
            -v "$captures_dir":/captures \
            -v "$samfile_dir":"$samfile_dir" \
            -w /work \
            "$image" sleep infinity > /dev/null
        ;;
    *)
        echo "ERROR: container '$container_name' in unexpected state '$state'" >&2
        exit 4
        ;;
    esac
}

# Ensure Xvfb is running inside the container on $display.
ensure_xvfb() {
    if docker exec "$container_name" pgrep -f "Xvfb $display" > /dev/null 2>&1; then
        return 0
    fi
    docker exec -d "$container_name" \
        Xvfb "$display" -screen 0 1280x1024x24
    # Give it a moment to come up.
    for _ in 1 2 3 4 5; do
        if docker exec "$container_name" \
                test -e "/tmp/.X11-unix/X${display#:}" 2>/dev/null; then
            return 0
        fi
        sleep 0.2
    done
    echo "ERROR: Xvfb $display failed to start in container" >&2
    exit 5
}

# Translate a host path to a container path via the known mounts.
host_to_container() {
    local hp="$1"
    case "$hp" in
    "$corpus_dir"/*)   echo "/corpus/${hp#"$corpus_dir/"}" ;;
    "$captures_dir"/*) echo "/captures/${hp#"$captures_dir/"}" ;;
    "$repo_root"/*)    echo "/work/${hp#"$repo_root/"}" ;;
    *)                 return 1 ;;
    esac
}

ensure_container
ensure_xvfb

container_disk="$(host_to_container "$source_disk" || true)"
container_output="$(host_to_container "$output_file" || true)"

if [ -z "$container_disk" ]; then
    echo "ERROR: source disk '$source_disk' is not under any mounted volume:" >&2
    echo "    CORPUS_DIR=$corpus_dir, CAPTURES_DIR=$captures_dir, REPO_ROOT=$repo_root" >&2
    exit 6
fi
if [ -z "$container_output" ]; then
    echo "ERROR: output path '$output_file' is not under any mounted volume" >&2
    exit 7
fi

# Make sure the output's parent dir exists on the host (so the bind
# mount sees a real directory inside the container).
mkdir -p "$(dirname "$output_file")"

docker exec \
    -e "DISPLAY=$display" \
    -e SDL_VIDEODRIVER=x11 \
    -e SDL_AUDIODRIVER=dummy \
    -e REPO_ROOT=/work \
    "$container_name" \
    bash /work/tools/llist-capture-headless.sh \
        "$container_disk" "$basic_name" "$container_output"
