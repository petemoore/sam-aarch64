# editor-prototype

A host-side terminal prototype of the SAM editor, rendered at SAM-faithful
screen geometry. It opens a real compact `.tbn`, displays its records and
comments through a strict SAM-screen abstraction, and scrolls with cursor keys —
the functional UX authority the SAM-side editor is ported from.

Everything passes through `samscreen` (`samscreen/`): a fixed W×H cell grid with
CLUT-index colours bounded by the screen mode, our own 1-bpp bitmap font, and a
SAM-expressible key model. Two backends implement it — `terminal/` (interactive,
tcell) and `mockup/` (PNG at exact mode geometry + a SAM `SCREEN$` `.mgt`). The
read-only viewer (`viewer/`) is written against the abstraction alone, so it
runs on either.

## Run

```
go run . -tbn build/release-unstripped.tbn                 # interactive, MODE 3 64×24
go run . -tbn build/release-unstripped.tbn -geometry mode4-8x8
go run . -mockup -tbn build/release-unstripped.tbn -o build/mockups/   # PNG+SCREEN$ sheets, all geometries
go run . -frames 3 -tbn build/release-unstripped.tbn -o build/mockups/ # non-interactive smoke
```

Keys: arrows scroll · space/b page · g/G top/bottom · w wrap toggle · q/ESC quit.
Geometries: `mode3-8x8` (default) · `mode3-6x8` · `mode3-6x6` · `mode4-8x8` ·
`mode4-6x8` · `mode4-6x6`.

Fonts live in `fonts/` (8×8 vendored; 6-px drop in by path — see its README).
Design authority: `docs/specs/editor-tui-prototype-design.md`.
