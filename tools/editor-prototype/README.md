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
go run . -tbn build/release-unstripped.tbn                            # raw viewer, MODE 3 64×24
go run . -mockup -tbn ... -o build/mockups/                           # PNG+SCREEN$ sheets, all geometries
go run . -frames 3 -tbn ... -o build/mockups/                         # non-interactive smoke
go run . -iter2-stats -tbn ...                                        # rendering-ladder corpus measurement
```

### The configurable rendering lab

`-config FILE` launches a live lab where every rendering feature is one key in a
hand-editable config (`#` comments, zero deps; documented starters in
`configs/`). Flip features with single keys (`?` shows the overlay), `S`
snapshots a combination to a relaunchable config, then `-sam-png` renders that
config as a SAM-faithful PNG — the zero-translation path from combo to screen.

```
go run . -tbn build/release-unstripped.tbn -config configs/compressed.config        # live lab
go run . -tbn ... -config configs/compressed.config -sam-png out.png -sam-line 53    # SAM PNG
```

Editor keys stay SAM-faithful (arrows, PgUp/Dn, q/ESC); lab keys are lab-only.
Starters: `binutils-baseline` (R0) · `compressed` (recommendation) ·
`comet-minimal` (chromeless) · `dreamer` (`relax_palette`).
Design authority: `docs/specs/editor-tui-prototype-design.md`.

Lab keys include `M` (toggle `max_instruction_width`: truncate over-long
non-cursor rows at N−1 + ellipsis; cursor line always full) and `b` (cycle
`render_constants` source → hex → dec). The format never imposes line-length
limits — overflow is a display concern; truncation is presentation-only.
