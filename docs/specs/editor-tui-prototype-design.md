# Editor TUI prototype — host-side UX authority (i76)

**Status:** approved with amendments (Pete 2026-06-12) — implementation may
begin (P1). Decisions recorded in §8. · **Item:** i76 ·
**Milestone:** M9 (the editor era) · **Type:** design spec

Before any SAM-side editor UI work, build a host-side Go terminal (TUI)
prototype of the editor at SAM-faithful dimensions, iterate the functional UX
fast (wrapping, scrolling, comment display, status line, modal commands), and
only then port to the SAM and add the retro elements (music, fonts, graphics
effects). First deliverable feeds **i4** (read-only scrolling viewer); the
mockup backend is the **i5/q1** renderer; the geometry flag is how **i6** (the
screen-mode decision) gets decided empirically.

Grounding reads (cited, not restated): `docs/specs/editor-vision.md` (the
feature vision + the 1980s keyboard-driven interaction model),
`docs/specs/editor-edit-model-design.md` (i41 — the paged block-list, §7
decisions), `docs/specs/i48-syntactic-encoder-design.md` (the symbolic IR),
`docs/specs/comment-storage-design.md` (i60c), `docs/notes/m9-status.md`.
SAM video facts cite the SAM Coupé Technical Manual v3.0 at
`docs/sam/sam-coupe_tech-man_v3-0.txt` ("TM pN" = printed page N).

---

## 1. Purpose and the porting thesis

This project's standing pattern is **Go first as the authority, then a
mechanical Z80 port** (repo CLAUDE.md §6; `aarch64enc`/`refenc` were the
encoding authority for `src/`). i76 applies the same pattern to UX: the
prototype is the **functional authority for the SAM editor's UX**. Every
interaction question — how a 120-char comment wraps at 64 columns, what the
status line shows, how modal commands feel, how errors surface on commit —
gets answered cheaply on the host (compile-test loop of seconds, real
keyboard, screenshotable), recorded as prototype behaviour, and then ported.
When the Z80 editor is built, "what should happen on key X" is a known answer
to mirror, not a design session (CLAUDE.md §6: don't manufacture blockers the
authority already settles).

A key early question it answers empirically: **are the codebase's rather
large comments displayable acceptably at these widths?** §8 measures the real
distribution; the prototype turns those numbers into lived experience.

What the prototype is *not*: it is not the shipped editor, not a Z80 code
generator, and not a pixel-perfect SAM emulator. It is faithful to the SAM's
*constraints* (geometry, colour count, key model), deliberately unfaithful to
its *performance* (host-speed iteration is the point).

## 2. The load-bearing constraint — the SAM screen abstraction

Everything the prototype renders and reads MUST pass through two narrow
interfaces that a SAM port can implement. If the prototype ever uses a
capability the SAM lacks, its authority is void at that point. This is the
one rule that makes the whole exercise transfer.

### 2.1 Output: a fixed W×H cell grid

A single Go interface (working name `samscreen.Screen`):

- `Size() (w, h int)` — fixed at construction from the mode×font choice (§3);
  no resize. The host terminal window being bigger just leaves a border.
- `Set(x, y int, ch byte, ink, paper uint8)` — one character cell. `ch`
  indexes a bitmap font of **our own definition**, not any fixed SAM
  character set: the SAM renders all text in software, so any glyph
  definable as a W×H 1-bpp bit pattern in the active cell geometry is
  permitted (custom fonts are the norm on the SAM). That legitimises
  box-drawing, arrows and special markers as first-class repertoire —
  subject to the mode's colour constraints. The constraint is therefore
  "glyphs definable in the active cell geometry", and the only exclusion is
  anything *outside* a byte-indexed bitmap font: no Unicode passthrough.
- `Flush()` — present the frame.
- Colours are **CLUT indices, not RGB**, and the backend enforces the mode's
  real capability: MODE 4 = ink/paper each 0–15 (TM p15: 16 colours; p20:
  4 bits per pixel); MODE 3 = ink/paper each 0–3 (TM p15: 4 colours; p20:
  2 bits per pixel, extendable to other CLUT quadruples via HMPR bits 5–6,
  TM p17/p20 — still only 4 distinct colours on screen at once); MODE 2 =
  2 colours per 8×1 cell from 16 (TM p15), i.e. ink/paper are per-cell but a
  cell column is locked to 8 px; MODE 1 = Spectrum-style 8×8 attribute cells
  (TM p15). Passing an out-of-range colour panics — a constraint violation is
  a prototype bug, not a rendering choice.
- No terminal affordances leak through: no 24-bit colour, no terminal
  bold/italic/underline attributes, no cursor styling beyond what a SAM
  editor would draw itself (the cursor is a cell drawn in reverse ink/paper,
  by us). Cell **styling** is expressible only by SAM-renderable means: the
  SAM renders text in software, so a "bold" or "italic" variant is an
  **alternate software font bank**, not a terminal attribute — possible in
  principle, but likely poor at small cell sizes and not to be encouraged
  (Pete 2026-06-12). Default is **no styling**; a styled variant may be
  introduced only where it demonstrably improves the UX, and it costs real
  font RAM + glyph-set space — per additional 96-glyph bank: 8×8 = 768 B
  (8 B/glyph); 6×8 = 768 B row-padded (one byte per row) or 576 B
  bit-packed (48 bits/glyph); 6×6 = 576 B row-padded or 432 B bit-packed
  (36 bits/glyph). If a styled variant is ever adopted, the abstraction
  carries it as a font-bank index on the cell — a capability a SAM port can
  honour — never as a terminal attribute.
- **Flash/blink.** Per-cell flash exists on the SAM only as MODE 1's
  Spectrum-compatible attribute (TM p1: MODE 1 is "Spectrum-attribute
  compatible"; TM p15: it "emulates Spectrum memory mapping" with 0.75 KB of
  attribute memory) — and MODE 1 is not a candidate mode (§3), so **per-cell
  flash is not part of the abstraction**; for MODEs 2–4 the manual documents
  no per-cell flash attribute (TM p1, p15: their cell/pixel descriptions
  are colour selections only).
  Mode-agnostic flashing IS available, by palette swapping: the CLUT is
  software-loaded, and the ROM frame-interrupt routine already alternates
  the palette registers between two 16-byte PALTAB tables "to give flashing
  colours" (TM p30), with per-entry main/flashing colour pairs settable via
  JPALET (TM p40) and per-scan-line palette changes hookable on the line
  interrupt via LINIV (TM p31). The abstraction therefore permits **one
  optional whole-pen "blink" attention cue** (e.g. the status line during a
  long search): a designated CLUT entry alternating between two colours at
  frame rate — palette-swap mechanism, works in any mode, nothing per-cell.

### 2.2 Input: a SAM-expressible key model

One event type (working name `samscreen.Key`): printable bytes, RETURN, ESC,
arrows (up/down/left/right), DELETE, TAB, EDIT, and function keys **F0–F9**,
with SHIFT/SYMBOL/CNTRL as modifiers. That is the SAM's actual surface: a
72-key full-travel keyboard read as a 9×8 matrix (TM p16); a mouse exists on
port 254 (TM p21) but the project's interaction model is explicitly 1980s
keyboard-driven — function keys + status line + single-letter modal commands,
no pointer/hover/click idioms (`editor-vision.md` "Interaction model";
memory `feedback_sam_editor_keyboard_driven`). The terminal backend maps host
keys onto this model (host F1–F10 → F0–F9 etc.) and **discards** anything the
SAM cannot express (no Ctrl+Shift+arrow chords, no mouse events).

### 2.3 Backend (a): interactive terminal — recommend tcell

Three candidates evaluated for fixed-grid faithfulness:

- **`gdamore/tcell` (recommended).** Its native model *is* an addressable
  cell grid (`SetContent(x, y, ch, …, style)`) — structurally identical to
  §2.1, so the adapter is a few dozen lines and nothing tempts the prototype
  toward flow layout. Mature key decoding (function keys, modifiers) across
  terminals. External Go deps are established practice here (the harness
  vendors `koron-go/z80`).
- **`charmbracelet/bubbletea`** — rejected. An Elm-style framework that
  renders styled *strings* and diffs frames; its idiom (lipgloss layout,
  flexible boxes) is exactly the pointer-era-adjacent affordance set we must
  not absorb. Fighting the framework to keep a strict grid is negative value.
- **Raw ANSI** — rejected. Re-implements escape-sequence key decoding and
  terminal quirks for zero gain over tcell's solved version of the same.

### 2.4 Backend (b): the mockup renderer — the q1 deliverable

The same `Screen` interface, rendering to files instead of a terminal:

- **PNG at exact mode geometry.** The cell grid is drawn with a real bitmap
  font (8×8: the SAM ROM character set; 6×6/6×8: a vendored web-sourced
  font — see the §5 P1 font-proof leg; licence + provenance recorded) onto
  the mode's true pixel grid — 256×192 (MODE 4) or 512×192 (MODE 3) (TM p15) —
  using real CLUT palette values: 7-bit GRN1/RED1/BLU1/BRIGHT/GRN0/RED0/BLU0
  (TM p19), converted to sRGB with SimCoupé's mapping as the reference.
  **Pixel aspect is corrected on export**: MODE 3 packs 512 pixels into the
  same active scan width as MODE 4's 256 (TM p15), so MODE 3 pixels are
  half-width; we export MODE 4 at 2×2 per pixel and MODE 3 at 1×2, so both
  produce 512×384 PNGs with true proportions. Optional simple CRT simulation
  (scanline darkening) behind a flag — plain output is the default.
- **SAM `SCREEN$` output.** The same frame written as mode-correct display
  bytes + PALTAB, packaged as a Type 20 file (directory byte 0xDD = screen
  MODE; `docs/notes/sam-file-header.md` §Type 20) on an `.mgt` via
  `tools/build-disk`, so any mockup can be eyeballed in SimCoupé or on real
  hardware — the ground-truth readability check.
- **Font comparison sheets**: the deliverable Pete asked for — the same
  editor frame (real `release.s` content, comments included) rendered at each
  §3 geometry, 8×8 vs 6-px fonts side by side, as PNGs + SCREEN$.

This backend resolves **q1** as option (a): a programmatic SAM-faithful
renderer (decision recorded in the question registry).

## 3. Geometry/option table

All arithmetic from the TM p15 resolutions (MODE 3 explicitly: "when used
with a character set 6 pixels wide, will give 85 characters per line").
Screen RAM: MODE 1 = 6.75 KB, MODE 2 = 12 KB (TM p29), MODES 3/4 = 24 KB =
two adjacent 16 KB pages (TM p15, p29).

| Mode | Pixels | Colours on screen | Screen RAM | 8×8 font | 6×8 font | 6×6 font |
|---|---|---|---|---|---|---|
| MODE 4 | 256×192, 4bpp | 16 of 128 | 24 KB (2 pages) | **32×24** | 42×24 | 42×32 |
| MODE 3 | 512×192, 2bpp | 4 of 128 | 24 KB (2 pages) | **64×24** | **85×24** | 85×32 |
| MODE 2 | 256×192, 1bpp + 8×1 attrs | 2 per 8×1 cell, of 16 | 12 KB (1 page) | 32×24 | n/a† | n/a† |
| MODE 1 | 256×192, 1bpp, Spectrum layout + 8×8 attrs | 2 per 8×8 cell, of 16 | 6.75 KB (1 page) | 32×24 | n/a‡ | n/a‡ |

† MODE 2's colour granularity is the 8×1 attribute strip, so 6-px-wide
characters cannot carry per-character colour (cells straddle strips); row
*height* is free (8×6 → 32×32). Its niche: half the RAM of MODE 3/4 and
per-scanline colour pairs — a candidate only if two pages for the screen ever
becomes the binding constraint. ‡ MODE 1's 8×8 attribute cells lock both
dimensions; it is the Spectrum-compatibility mode (interleaved layout, TM
p15) and not a serious editor candidate.

Caveats the table cannot settle — exactly why the mockup backend exists:
85×24 means 6-px glyphs on a real CRT/TV; 42 columns in MODE 4 keeps 16
colours but a 6-px font; readability must be judged from §2.4 sheets on real
SAM output, not from the arithmetic.

**Parameterization:** one flag, `-geometry mode3-8x8` (= 64×24, default; see
§8 D2) | `mode3-6x8` | `mode3-6x6` | `mode4-8x8` | `mode4-6x8` | `mode4-6x6`.
The terminal can be **started in any** of these so each can be tried
functionally — comment rendering, wrapping, scrolling (§8 D2). Every layout
decision in the prototype reads `Size()`; nothing hardcodes a width.
Switching geometry IS the i6 experiment: live with each for a session,
compare, decide.

## 4. Document model — wired to the real libs, mapped 1:1 onto i41

The prototype edits **real `.tbn`-derived content from day one**. The Go side
already has everything needed (i48a libs, all in `tools/sam-aarch64*`):

- `format.ReadFile(buf) (*format.File, error)`
  (`tools/sam-aarch64-format/reader.go`) — parses the compact v2 `.tbn`
  including the editor region (names, comment sidecar, placements).
- `render.EmitFile(f) ([]byte, error)` / per-record emit internals
  (`tools/sam-aarch64/render/emit.go`) — overlay→text with comments, the
  former bin2text. P1 adds a small exported per-record variant so the viewer
  gets a `[]Line{recordIdx, text}` mapping instead of one flat byte slice —
  normal lib evolution, same emit logic.
- `frontend` (`tools/sam-aarch64/frontend`) — text→IR for P2's
  parse-on-commit; `assemble` for P3's assemble-in-place.

So the P1 viewer displays the real `release.s` — built by
`make release-unstripped-tbn` → `build/release-unstripped.tbn` (363,295 B,
7,502 comments) — with comments rendered exactly as the host renderer does.

**Relation to i41 + i48 (the contract).** The shipped editor's document model
is the i41 paged block-list whose records are the i48 in-memory symbolic IR
(i41 §7 decision 3). The prototype does NOT simulate Z80 paging: it may hold
a plain Go slice of records. But **its operation set maps 1:1 onto i41's op
set** — the prototype exercises the i41 design's *semantics* (record
identity, raw-until-commit, error records, journal undo), so the port stays
mechanical and i41 is exercised, not bypassed:

| Prototype operation | i41 equivalent (editor-edit-model-design.md) |
|---|---|
| insert line at cursor | allocate u24 record-id, intra-block gap insert (§2.5, §7.5) |
| delete line at cursor | close in-block gap; id never reused (§2.5, §7.5) |
| edit active line (raw text) | active line raw-until-commit (§7.3) |
| commit line: parse → IR record | parse-success is the validation; invalid → flagged raw/error record (§7.3) |
| cursor ±1 line | block-local step / ≤1 page swap (§2.5) |
| goto line N / label | resident block-list scan + 1 swap; name-id→record-id table (§2.5, §3) |
| render screenful | 24-line window walk, 1–2 blocks (§2.5) |
| undo / redo | bounded ring journal (§7.2) |
| save → `.tbn` v2 | one O(n) serialize pass; offsets/PCs computed then (§1.3) |
| load ← `.tbn` v2 | decode records into blocks (§2.5) |

**Pressure points to surface early** (half the point of the prototype is
finding these; each gets reported against i41 rather than silently absorbed):

1. **Long records vs `len: u8`.** i41 sketches a per-line `len: u8/u16`
   (§2.3). The real corpus has lines up to **1,693 bytes** (measured, §8) —
   u8 is insufficient. **Decided: u16** (Pete 2026-06-12, §8 D1) — an
   explicitly reversible experiment-era choice, not an architectural
   commitment; see D1 for the switch-back-to-u8 condition.
2. **Records vs screen rows.** With wrapping, one record occupies 1–N rows;
   "render screenful = 24 lines" (i41 §2.5) becomes "fill 24 rows from ≤24
   records". A display-layout layer above the block-list, not a change to it
   — but the prototype will confirm the cursor/scroll model stays sane.
3. **Search.** i41 has no text-search structure; search = O(n) block walk
   (~25 page swaps at 400 KB). The prototype measures whether linear-scan
   UX (with a "searching…" status) is acceptable before anyone designs an
   index the SAM may not need.

## 5. Phasing

### P1 — i4-parity read-only viewer + the mockup renderer (feeds i5/i6)

Deliverables: the `tools/editor-prototype` module (§6) with the `samscreen`
abstraction, tcell backend, mockup backend, and a read-only viewer: open
`build/release-unstripped.tbn`, scroll with cursor keys (line up/down, page
up/down, top/bottom, centre-locked cursor per i4), comments displayed,
wrap/truncate toggle, status line (file · record kind · line/total · geometry).
The viewer must start in **any** §3 geometry via `-geometry` (§8 D2). Plus
the §2.4 font sheets — PNG mockups for **all six** §3 options, not just the
default — and at least one SCREEN$ on an `.mgt`.

**The real-SAM 6×6 leg (named P1 deliverable — the font-proof).** The §2.4
SCREEN$ route puts host-rendered frames on a SAM screen; this leg goes one
further: the **SAM itself** renders sample editor content with a 6-px font,
under SimCoupé, captured as an actual screenshot — "the only real way to
know if this is a possible path" (Pete 2026-06-12). Concretely:

1. **Source an existing 6-px bitmap font from the web** — web-search for
   candidates, judge from rendered specimens/screenshots, pick one or two
   that look half-decent. Pete explicitly approves using an online font; no
   need to design one. The vendored font lives at
   `tools/editor-prototype/fonts/` (shared by the mockup backend and this
   leg) with licence + provenance recorded in that directory's README, per
   the repo's vendoring norms (cf. `tests/release/README.md`).
2. **Convert it to the SAM format** — a small Go converter in
   `tools/editor-prototype` emitting the font as the row-padded binary the
   Z80 routine consumes (same bytes the mockup backend rasterises from).
3. **Write the small Z80 display routine** — `tools/font-proof/`: a pyz80
   source that sets MODE 3, loads the converted font, and renders sample
   editor content (real `release.s` lines, comments included) at 85×32
   (6×6) on the SAM screen. Built onto a bootable `.mgt` via
   `tools/build-disk` (`make font-proof` → `build/font-proof.mgt`); the
   §2.4 SCREEN$-on-`.mgt` machinery is the carrier reference for getting
   frames on/off the disk.
4. **Run under SimCoupé and capture an actual screenshot** — boot the disk
   in the dev container via `tools/run-simcoupe.sh`, then capture the Xvfb
   display with ImageMagick `import -window root` (the documented capture
   path, `docs/notes/headless-simcoupe.md`) →
   `build/mockups/font-proof-6x6.png`. Additionally HSAVE the rendered
   frame back to the `.mgt` as a Type 20 SCREEN$ (a real SAM API — memory
   `feedback_tests_use_real_sam_apis`), so the same frame can be loaded on
   real hardware.

Demo script (exact commands):

```
make release-unstripped-tbn
go run ./tools/editor-prototype -tbn build/release-unstripped.tbn            # MODE 3 64×24 default
go run ./tools/editor-prototype -tbn build/release-unstripped.tbn -geometry mode4-8x8
go run ./tools/editor-prototype -tbn build/release-unstripped.tbn -geometry mode3-6x6   # any §3 option starts
go run ./tools/editor-prototype -mockup -tbn build/release-unstripped.tbn -o build/mockups/
ls build/mockups/   # one PNG + one SCREEN$ per §3 geometry (all six), same frame, aspect-correct
make font-proof                          # build/font-proof.mgt — Z80 6×6 renderer + vendored font + sample text
tools/run-simcoupe.sh build/font-proof.mgt   # boot in SimCoupé (Docker); import -window root → build/mockups/font-proof-6x6.png
```

Exit criteria: viewer scrolls the whole release smoothly at every §3
geometry; sheets delivered for all six options; the font-proof screenshot
captured from a real SAM screen under SimCoupé; i5 satisfied; i6 has its
evidence base.

### P2 — editing interactions

Line edit / insert / delete with the i41 §7.3 raw-until-commit model: typing
opens the active line as raw text; RETURN commits (parse via `frontend`;
errors flag the line as an error record and surface on the status line, no
modal interruption); ESC abandons. Undo via a ring journal. Save serializes
to a compact `.tbn` v2 and the round-trip is gated: `--render` of the saved
file matches the prototype's document (the existing host round-trip pattern).
Demo: open release, edit an instruction, break it (see the error record),
fix it, save, re-render, diff clean.

### P3 — command surface + the i6 memo

Single-letter modal commands (`editor-vision.md` idiom): search, goto
line/label, assemble-in-place (invoke `assemble`; show error or output size
on the status line — the inner-loop feedback the SAM editor will give).
Closing deliverable: a short **mode/font recommendation memo** for i6 —
geometry verdict from lived P1–P3 use + the font sheets + §8 data. Demo:
goto a label, search a comment, assemble, read the result without leaving
the editor.

## 6. Naming and placement

**`tools/editor-prototype`** — a new Go module, added to `go.work`, with the
mandatory ≤30-line README in its first implementation PR (none in this spec
PR — no code yet). Named for its function (the doc-lifecycle rule: never
milestone names); "prototype" is the function — the name says exactly what it
is and warns exactly how much to lean on it.

Relationship to the shipped editor: **scaffolding-with-authority**, per the
`project_go_tools_are_z80_scaffolding` philosophy. It informs the Z80 port
(the UX authority, as refenc was the encoding authority); once the SAM editor
reaches UX parity its authority role is fulfilled, and its disposition
(retain as UX oracle for editor tests vs delete per the superseded-tooling
rule) becomes a registry item raised at that time — not parked indefinitely.

## 7. Out of scope

- **Z80 code, with one exception.** The editor itself stays host-side;
  nothing of it runs on the SAM (the SCREEN$ files are data). The single
  exception is the §5 P1 font-proof display routine — a throwaway probe
  that proves 6-px rendering on a real SAM screen, not editor code.
- **Retro polish** — music, period fonts beyond the readability-test fonts,
  animations, palette effects: explicitly the post-port phase
  (`editor-vision.md` "Retro UI affordances").
- **Final art** — q1's hand-authored-art half stays with Pete, later.
- **TFTP / Phase 3.** Unrelated.
- **i41/i48c implementation.** The prototype maps onto i41; it does not
  implement the Z80 block-list or the Z80 text→overlay encoder.

## 8. The comment-width data + decisions record

**Measured comment-length distribution** (vendored `tests/release/release.s`
at c14f6f4: 13,187 lines, 7,502 carrying `//`; lengths in characters; "fits"
= length ≤ columns; measured this session with a python length histogram —
trivially re-runnable):

| Population | n | mean | median | ≤32 | ≤42 | ≤64 | ≤85 |
|---|---|---|---|---|---|---|---|
| all source lines | 13,187 | 50.9 | 49 | 41.2% | 46.4% | 58.0% | 85.8% |
| comment text (`//`→EOL) | 7,502 | 44.7 | 42 | 40.4% | 50.5% | 75.9% | 94.1% |
| — standalone comment lines | 5,340 | 47.1 | 46 | 38.1% | 46.3% | 70.6% | 93.6% |
| — inline (trailing) comments | 2,162 | 38.8 | 35 | 46.2% | 60.9% | 88.8% | 95.4% |
| full lines carrying a comment | 7,502 | 64.4 | 67 | 24.1% | 28.6% | 47.7% | 75.3% |

Longest line: 1,693 chars. Readings: **wrapping is a mandatory core UX at
every candidate geometry** (even at 85 cols, ~14% of all lines overflow; at
64, 42% do; at 32, under half of *everything* fits — MODE 4 8×8 means most
comment-carrying lines wrap to 2–4 rows). And the standalone:inline ratio
(5,340:2,162) means comment display is mostly whole lines, which wrap more
gracefully than trailing comments — a real argument the prototype will test.

**Approach (R1–R4, approved with the decisions below — Pete 2026-06-12):**

- **R1 — tcell** for the terminal backend (§2.3).
- **R2 — default geometry MODE 3 64×24 (8×8 font)** as the first prototyped
  point, the full §3 matrix one flag away. Justification from the data: 32
  cols (MODE 4 8×8) fits only 41% of lines — wrap-dominated; 64 cols fits 76%
  of comment text with a known-readable 8×8 font; 85 cols (94% of comments)
  is the upside *if* the 6-px font survives the readability sheets — which is
  precisely what P1 produces. MODE 3's 4 colours suffice for an editor
  (text/comment/status/error); trading colours for columns is the right
  default for a text tool, and i6 stays open until the sheets + lived use
  decide it.
- **R3 — mockup output as plain PNG by default**, scanline-CRT variant behind
  a flag, plus the SCREEN$-on-`.mgt` route for ground truth in SimCoupé (the
  only fully honest readability test).
- **R4 — P1 wires the viewer via `format.ReadFile` + a per-record render
  adapter** (§4), not by parsing rendered text — so the record↔row mapping
  (the i41-shaped part) is real from day one.

**Decisions (Pete 2026-06-12) — both former open questions are answered; no
open questions remain in this spec:**

- **D1 — i41 record length field: u16 now, explicitly reversible.** `len`
  is pinned to u16 (no entry cap, soft-wrapped display); the i41 §2.3
  sketch's u8 cannot hold the real corpus (1,693-char outlier, table
  above). This is a **temporary issue resolved by the experiment, not an
  architectural commitment**: if the experiment ends with all comments
  < 256 B — possibly after rewriting the few overlong `release.s` comments
  for content, section by section, agent-assisted (Pete prefers that over
  programmatic stripping; he suspects he "went a bit overboard with
  comments originally") — switch back to u8. Considered and disfavoured:
  splitting u16-length comments across multiple u8 records — the per-record
  overhead likely costs more space than u16 does.
- **D2 — geometry: empirical, with a MODE 3 64×24 presumption.** The
  presumption (and the P1 default) is MODE 3 64×24 per R2, but the decision
  is empirical and the prototype must support deciding it: (a) the terminal
  can be **started in any** §3 geometry via `-geometry`, so each option can
  be tried functionally — comment rendering, wrapping, scrolling; (b) PNG
  mockups are produced for **all** §3 options, not just the default; (c)
  the 6-px path is proven or killed on a **real SAM screen** — the §5 P1
  font-proof leg, "the only real way to know if this is a possible path".
  i6 closes on this evidence in the P3 memo.

## 9. Registry / lifecycle

i76 in `docs/notes/item-registry.md`; strand row in `docs/notes/m9-status.md`
(i4 folds into P1; i5 into the §2.4 mockup backend; i6 into the §3 flag +
P3 memo). q1 resolved as §2.4. This spec is a living design doc (evergreen
filename); when the prototype ships and its durable rationale folds into
`docs/ARCHITECTURE.md` / the editor's docs, it is deleted in that PR per the
doc-lifecycle rules.
