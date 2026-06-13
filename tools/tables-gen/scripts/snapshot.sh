#!/usr/bin/env bash
# Downloads the ARM MRA tarball and extracts the subset of XMLs we
# vendor under reference/arm-mra/. One-off operation; re-run only
# when bumping the ARM version pinned in manifest.json.
#
# Usage: snapshot.sh <list of mnemonic-family stem names>
# Example: snapshot.sh nop add_addsub_imm ret
#
# Note: ARM uses internal stem names for add-immediate:
#   add_addsub_imm.xml  -> vendored as add_immediate.xml
# Pass the ARM stem name (without .xml extension) as the argument.
# The script copies each stem as-is; rename vendored files manually
# if the plan's canonical names differ.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
DEST="$ROOT/reference/arm-mra"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

TARBALL_URL="https://archive.org/download/arm-xml-a-profile-2022-12/ISA_A64_xml_A_profile-2022-12.tar.gz"
TARBALL_SHA256="1839a9722eab26aa3bdcd91adeeaf2e6c5c6df085f0e096f949c55cbee7d1a5a"

cd "$TMP"
echo "Downloading $TARBALL_URL ..."
curl -L --fail -o mra.tar.gz "$TARBALL_URL"
echo "Verifying checksum ..."
echo "$TARBALL_SHA256  mra.tar.gz" | sha256sum -c -

tar xzf mra.tar.gz
EXTRACT_DIR=$(find . -maxdepth 1 -type d -name 'ISA_A64_xml_A_profile-*' | grep -v OPT | head -n 1)

if [ -z "$EXTRACT_DIR" ]; then
    echo "ERROR: could not find ISA_A64_xml_A_profile-* directory in archive" >&2
    exit 1
fi

mkdir -p "$DEST"
cp "$EXTRACT_DIR/shared_pseudocode.xml" "$DEST/"
for stem in "$@"; do
    src="$EXTRACT_DIR/${stem}.xml"
    if [ ! -f "$src" ]; then
        echo "MISSING: $src" >&2
        exit 1
    fi
    cp "$src" "$DEST/"
done

echo "Snapshot complete. ${#@} mnemonic XMLs + shared_pseudocode.xml in $DEST."
