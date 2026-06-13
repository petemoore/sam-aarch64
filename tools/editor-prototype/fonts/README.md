# fonts (vendored)

Bitmap fonts the prototype rasterises through `samscreen.Font`. Each is a 1-bpp
table in the SAM's row-padded software-font layout — one byte per glyph row,
MSB = leftmost pixel — so a SAM port loads the same bytes.

| Font | Source | Licence |
|---|---|---|
| 8×8 (`font8x8.go`) | [dhepper/font8x8](https://github.com/dhepper/font8x8) basic-latin block, derived from Marcel Sondaar / IBM public-domain VGA fonts | Public Domain |
| 6×8 (`font6x8.sam`) | *pending* — vendored by the 6-px font-proof leg | — |
| 6×6 (`font6x6.sam`) | *pending* — vendored by the 6-px font-proof leg | — |

The 8×8 table is embedded as Go source: the dhepper bytes are LSB-first and were
bit-reversed at vendor time so the table matches `samscreen.Font`'s MSB-leftmost
convention (`TestFontGlyphAShape` pins the result).

The 6-px fonts are loaded by path: `FontFor` reads `font6x8.sam` / `font6x6.sam`
(row-padded binary, the format `LoadRowPadded` consumes and `RowPaddedBytes`
emits) when present, and reports `ErrFontNotVendored` otherwise so the mockup
backend renders a placeholder note. Dropping a vendored binary in here is the
only step needed to light up the 6-px geometries.
