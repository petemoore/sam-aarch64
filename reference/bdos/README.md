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
| `bdos15a.src.txt` | Detokenised, readable rendering of the zip's `BDOS15a .S` COMET source. | derived from `Bdos15a.zip` |
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
