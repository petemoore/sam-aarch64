#!/usr/bin/env bash
# Thin shim around the build-disk Go program. The full disk-image
# constructor is at tools/build-disk/main.go and is compiled into
# /usr/local/bin/build-disk by tools/Dockerfile.dev so the dev image
# can invoke it without re-fetching Go modules per run.
#
# Outside the dev image (e.g. native macOS), fall back to building
# the binary on demand into the build-disk module dir. Subsequent
# invocations use the cached binary unless main.go / go.mod /
# go.sum is newer.
#
# Usage: build-disk.sh <input.s> <output.mgt>

set -euo pipefail

cd "$(dirname "$0")/.."

# Prefer the pre-built binary from the dev image.
if command -v build-disk >/dev/null 2>&1; then
    exec build-disk "$@"
fi

# Local fallback: build into a per-repo location so subsequent
# invocations don't pay the build cost. Rebuild on source change.
LOCAL_BIN=tools/build-disk/build-disk
if [ ! -x "$LOCAL_BIN" ] \
    || [ tools/build-disk/main.go -nt "$LOCAL_BIN" ] \
    || [ tools/build-disk/go.mod -nt "$LOCAL_BIN" ] \
    || [ tools/build-disk/go.sum -nt "$LOCAL_BIN" ]; then
    (cd tools/build-disk && go build -o build-disk .)
fi
exec ./"$LOCAL_BIN" "$@"
