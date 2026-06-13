# fonts (vendored)

Bitmap fonts the prototype rasterises through `samscreen.Font`, plus the i76
P1b real-SAM font-proof assets (`docs/specs/editor-tui-prototype-design.md`
§2.4, §5). Each loadable font is a 1-bpp table in the SAM's row-padded
software-font layout — one byte per glyph row, MSB = leftmost pixel — so a SAM
port loads the same bytes. (The SAM-side proof's 8×8 comparison font is not
vendored: it uses the ROM charset read from the live machine — sysvar CHARS
`&5C36`; Tech Manual "CHARACTER SET", base `&5190`.)

## Contents

| File | What it is | Provenance / licence |
|------|------------|----------------------|
| `font8x8.go` | 8×8 font embedded as Go source; the upstream bytes are LSB-first and were bit-reversed at vendor time to match `samscreen.Font`'s MSB-leftmost convention (`TestFontGlyphAShape` pins the result). | [dhepper/font8x8](https://github.com/dhepper/font8x8) basic-latin block, derived from Marcel Sondaar / IBM public-domain VGA fonts; Public Domain |
| `five_pixel_font.h` | Pristine upstream source: the 5×5-ink / 6×6-cell ASCII font as an RLE-compressed C header (chars 32–126 plus a hollow-box fallback and cursor glyphs). | <https://github.com/ChrisG0x20/five-pixel-font> @ `a3d4011` (2013-08-31), by Chris Gassib; The Unlicense (public domain — the header carries the full dedication; repository `LICENSE` and GitHub licence metadata agree, SPDX `Unlicense`) |
| `fpf_texture_atlas_full_size.png` | Pristine upstream specimen: the 64×64 glyph atlas at 1:1. | same |
| `fpf_texture_atlas_6x_size.png` | Pristine upstream specimen: the atlas at 6× for eyeballing. | same |
| `five-pixel-font-6x6.bin` | Converted SAM form: 96 glyphs (chars 32–127), 6 bytes/glyph, one byte per row, the 6 pixel columns in bits 7..2 (MSB = leftmost). Char 127 = the atlas hollow box, mirroring upstream's `fpf_get_glyph_position`. | generated from `five_pixel_font.h` by `fontproof font` (`tools/font-proof/main.go`) |

The 6-px fonts are loaded by path: `FontFor` reads `font6x8.sam` / `font6x6.sam`
(row-padded binary, the format `LoadRowPadded` consumes and `RowPaddedBytes`
emits) when present, and reports `ErrFontNotVendored` otherwise so the mockup
backend renders a placeholder note. Dropping a vendored binary in here is the
only step needed to light up a 6-px geometry; `font6x8.sam` remains unvendored.

Evaluated and passed over: Tom Thumb 3×5-ink/4×6-cell (CC0/CC-BY-3.0,
<https://robey.lag.net/2010/01/23/tiny-monospace-font.html>) — proven
readable but only 3 px of ink width where a 6×6 cell affords 5.
