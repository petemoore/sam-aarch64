# `tools/editor-prototype/fonts/` — bitmap fonts (vendored)

Fonts for the editor prototype's mockup backend and the i76 P1b real-SAM
font-proof (`docs/specs/editor-tui-prototype-design.md` §2.4, §5). The 8×8
comparison font is not vendored: the proof uses the SAM ROM charset read
from the live machine (sysvar CHARS `&5C36`; Tech Manual "CHARACTER SET",
base `&5190`).

## Contents

| File | What it is | Provenance |
|------|------------|------------|
| `five_pixel_font.h` | Pristine upstream source: the 5×5-ink / 6×6-cell ASCII font as an RLE-compressed C header (chars 32–126 plus a hollow-box fallback and cursor glyphs). | <https://github.com/ChrisG0x20/five-pixel-font> @ `a3d4011` (2013-08-31), by Chris Gassib |
| `fpf_texture_atlas_full_size.png` | Pristine upstream specimen: the 64×64 glyph atlas at 1:1. | same |
| `fpf_texture_atlas_6x_size.png` | Pristine upstream specimen: the atlas at 6× for eyeballing. | same |
| `five-pixel-font-6x6.bin` | Converted SAM form: 96 glyphs (chars 32–127), 6 bytes/glyph, one byte per row, the 6 pixel columns in bits 7..2 (MSB = leftmost). Char 127 = the atlas hollow box, mirroring upstream's `fpf_get_glyph_position`. | generated from `five_pixel_font.h` by `fontproof font` (`tools/font-proof/main.go`) |

## Licence basis

The Unlicense (public domain). `five_pixel_font.h` carries the full
dedication in its header: "This is free and unencumbered software released
into the public domain. … In jurisdictions that recognize copyright laws,
the author or authors of this software dedicate any and all copyright
interest in the software to the public domain." Repository `LICENSE` file
and GitHub licence metadata agree (SPDX: `Unlicense`).

Evaluated and passed over: Tom Thumb 3×5-ink/4×6-cell (CC0/CC-BY-3.0,
<https://robey.lag.net/2010/01/23/tiny-monospace-font.html>) — proven
readable but only 3 px of ink width where a 6×6 cell affords 5.
