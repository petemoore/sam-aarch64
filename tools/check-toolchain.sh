#!/bin/bash
# Verifies all M0 toolchain dependencies are available.

set -euo pipefail

required=(
    pyz80
    samfile
    simcoupe
    aarch64-none-elf-as
    aarch64-none-elf-objcopy
)

missing=()
for tool in "${required[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        missing+=("$tool")
    fi
done

if [ ${#missing[@]} -ne 0 ]; then
    echo "Missing required tools:" >&2
    for t in "${missing[@]}"; do
        echo "  - $t" >&2
    done
    echo "" >&2
    echo "Install hints:" >&2
    echo "  pyz80                    pip install pyz80   (or clone simonowen/pyz80)" >&2
    echo "  samfile                  go install github.com/petemoore/samfile/v3/cmd/samfile@latest" >&2
    echo "  simcoupe                 see docs/notes/simcoupe-batch.md" >&2
    echo "  aarch64-none-elf-as      brew install aarch64-elf-binutils  (macOS)" >&2
    echo "                           apt-get install binutils-aarch64-linux-gnu  (Linux)" >&2
    exit 1
fi

echo "All required tools present:"
for t in "${required[@]}"; do
    printf "  %-30s %s\n" "$t" "$(command -v "$t")"
done
