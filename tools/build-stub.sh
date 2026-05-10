#!/usr/bin/env bash
# Builds src/stub.asm into build/stub.bin with pyz80.
#
# Expects `pyz80` on PATH as a bash wrapper that handles the python3
# invocation internally — both macOS (manual install) and Linux CI install
# pyz80 this way, since pyz80.py's shebang is `#!/usr/bin/env python` and
# modern systems may not have a bare `python` symlink.
#
# `--obj=` produces a flat raw binary; without it pyz80 would produce a
# SAM disk image (.dsk/.mgt) instead.

set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p build

pyz80 --obj=build/stub.bin src/stub.asm

echo "Built build/stub.bin ($(wc -c < build/stub.bin) bytes)"
