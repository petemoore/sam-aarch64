#!/usr/bin/env bash
# Builds src/stub.asm into build/stub.bin with pyz80.
#
# pyz80 flag form: python3 /usr/local/bin/pyz80 --obj=build/stub.bin src/stub.asm
#
# We call pyz80 via "python3 $(which pyz80)" rather than the bare "pyz80"
# command because the pyz80 shebang is "#!/usr/bin/env python", and on this
# Mac only python3 is installed (no "python" symlink). Invoking it via python3
# directly bypasses the shebang issue. "--obj=" produces a flat raw binary;
# without it pyz80 would produce a SAM disk image (.dsk/.mgt) instead.

set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p build

python3 "$(which pyz80)" --obj=build/stub.bin src/stub.asm

echo "Built build/stub.bin ($(wc -c < build/stub.bin) bytes)"
