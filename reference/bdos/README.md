# `reference/bdos/` — B-DOS 1.5a (vendored)

B-DOS is Edwin Blink's improved SAMDOS with mass-storage support, and this
project's target DOS — hook-compatible with SAMDOS per
[`docs/specs/samdos-file-io.md`](../../docs/specs/samdos-file-io.md).
Version 1.5a is the last release with public source and the fork point for
both descendant lines (see
[`docs/notes/bdos-version-landscape.md`](../../docs/notes/bdos-version-landscape.md)).

## Contents

| File | What it is | Provenance |
|------|------------|------------|
| `Bdos15a.zip` | Pristine 1.5a release disk image (1.4d/1.4e/1.5a Z80 source + binaries). | `ftp.nvg.ntnu.no/pub/sam-coupe/disks/dos/` |
| `bdos15a.src.txt` | Detokenised rendering of the zip's `BDOS15a .S` COMET source. Edwin's labels/comments, but a lossy editor-buffer dump (rotated out of load order, a 141-byte gap, ~130 externally-defined symbols) — **not** reassemblable as-is; kept as the naming/comment reference for i304b/c. | derived from `Bdos15a.zip` |
| `bdos15a.bin` | The freeware B-DOS 1.5a CODE file (10191 B, base `&8009`), extracted from `Bdos15a.dsk`. Byte-match oracle for `bdos15a.asm`. | `Bdos15a.zip` (freeware) |
| `bdos15a.asm` | Byte-exact **pyz80** reconstruction of `bdos15a.bin`. `make bdos15a-bytematch` proves reassembly is identical. The i304a foundation + the editable base i304b splices 1.5t into. | reconstructed (see `gen-bdos15a.py`) |
| `gen-bdos15a.py` | One-time bootstrap that generated `bdos15a.asm` from `bdos15a.bin` (objdump → backward-jr fix → gap/data DEFB → symbolicate). Provenance only, not a CI gate. `make bdos15a-regen`. | this repo |
| `al-bdos15a.bin` | Atom Lite 1.5a binary (10701 B CODE), the build the i62 rig boots. | extracted from the worldofsam.org "B-DOS AL 1.5a" megaboot disk |

## Licence basis

Edwin Blink's `BDOSINFO.T` (in the zip): "The B-DOS code and B-DOS
information are FREEWARE. … no silly restrictions whatso ever. Please pas it
on to other SAM users." Confirmed by the World of SAM copyright grants:
<https://www.worldofsam.org/copyrights/edwin-blink-b-dos> and
<https://www.worldofsam.org/copyrights/edwin-blink> ("Yes please make them
downloadable they are public domain").

The Trinity fork (B-DOS 1.5t) is **not** vendored — World of SAM marks Trinity
software "Copyrights Declined". Its static analysis lives in
[`docs/notes/bdos-trinity-fork-analysis.md`](../../docs/notes/bdos-trinity-fork-analysis.md).
