# Editor rendering rules — the specification + the default-config decision

**Status:** design — the **rendering-rule semantics (§3–§9) are specified** from
the i76 config lab's working implementation (the lab is the executable authority;
this doc is its written contract). The **default shipping config (§10) is a
recommendation pending Pete's sign-off** — choosing the editor's default *look*
from the config space is a taste call, registered as **q13**. · **Item:** i76
(rendering strand) · sibling of
[`editor-tui-prototype-design.md`](editor-tui-prototype-design.md) (the
prototype's *architecture*); this doc is the prototype's *rendering rules*.

**What this doc is for.** The config lab (`tools/editor-prototype -config FILE`)
exposes some forty rendering keys and four starter styles, and the iteration-2 work
measured a compression ladder over the real corpus. The lab *implements* the
rules; what was missing is a **written specification of what each rule does**
(so the eventual Z80 renderer is a faithful port, not a re-derivation) and a
**decision on the default** the editor ships with. This doc supplies both: §3–§9
pin the mechanics (mechanical — the lab is the authority, this is its transcript),
§10 recommends a default and hands the taste call to Pete.

**Reads first:**

- [`editor-tui-prototype-design.md`](editor-tui-prototype-design.md) — the prototype architecture + §8 the locked decisions (geometry presumption MODE 3 64×24, record length u16) + the comment-width corpus data. This doc does not re-decide those.
- The lab itself: `tools/editor-prototype/` — `lab_render.go` (the per-config render ladder + screen layout), `run_iter2.go` (the R0–R6 ladder transforms), `lab_config.go` (the rendering keys + defaults), `configs/` (the four starters). Every §3–§9 rule cites the lab line that implements it.

---

## 1. Scope boundary — what is mechanical here vs decided elsewhere

Three classes of rendering decision, kept distinct so this doc does not over-reach:

| Class | Examples | Where decided |
|---|---|---|
| **Mechanical rule semantics** | how a line wraps, the `&`/AND glyph rule, how the cursor line expands, how `max_instruction_width` interacts with comments | **here (§3–§9)** — the lab is the authority, this is its written form |
| **The default look (taste)** | which of the four styles (or a tuned config) ships as the default; the CLUT quadruple | **q13 → Pete** (§10 recommends; he signs off) |
| **Empirical / gated decisions** | the **geometry** (MODE 3 64×24 vs 85×32 6-px) and the **font**, palette-banking | **i6 / the i76 P3 memo** (real-SAM evidence) + future levers (§11) — *not* re-opened here |

This doc settles only the first class outright. It *recommends* the second and
hands it to Pete. It defers to the third and does not pre-empt it.

---

## 2. The fixed substrate (from the prototype architecture, not re-decided)

- **Geometry presumption: MODE 3, 64 columns × 24 rows, 8×8 font** (prototype
  design §8 R2/D2). The 64-col choice fits 76% of comment text; 85×32 (94%) is
  the upside if the 6-px font survives the real-SAM readability proof — an i6
  call, gated, not made here. The rendering rules below are geometry-parametric
  (they take `cols`/`rows`), so they hold at whichever geometry i6 picks.
- **MODE 3 palette: 4 pens** from a CLUT quadruple (lab key `clut`, default
  `0,15,102,127` = paper / grey-comment / yellow-accent / white-code). An editor
  needs only a few roles (paper, code, comment, accent, chrome), so 4 colours
  suffice; trading colour for columns is right for a text tool (prototype design
  §8 R2). The role→pen map is the `role_*` keys (§7).
- **Document model: per-record render adapter over `format.ReadFile`** (prototype
  design §8 R4) — the renderer walks records, not rendered text, so the
  record↔row mapping is real. Comments arrive from the editor-region sidecar
  (the i39b-2 format + the i78 blank-run extension).

---

## 3. Line composition — the per-record render ladder

A code record renders as `[indent] mnemonic [gap] operands [comment]`. The
transforms apply in the iteration-2 ladder order (`run_iter2.go`; cumulative R0→R6):

1. **R0 baseline** — mnemonic, one space, operands verbatim; `//` comment
   same-line. Reads like `objdump` (`configs/binutils-baseline.config`).
2. **Mnemonic column** (`mnemonic_column`) — `adaptive` aligns operands to the
   *page's* longest mnemonic + 1 (`run_iter2.go:186` `pageMnemCol`, recomputed
   per 24-line page in `lab_render.go:396` `setPage`); `fixed:N` pads to N;
   `fixed:0` is the R0 single space.
3. **Immediates** (`imm_hex_amp`, `imm_drop_hash`, `render_constants`) — hex →
   `&1F` (SAM convention) and/or drop `#`; `render_constants` forces a base or
   keeps the source spelling (honest, since the format carries base hints). The
   transform is `immTransform` (`lab_render.go:129`).
4. **Separators** (`tight_commas`) — `, ` → `,` in operand lists (`tightenSeps`).
5. **Registers** (`reg_style=strip`) — `x23`→`23`, `w5`→`5`; named regs
   (`sp/lr/fp/xzr/wzr/wsp`) stay named (`stripRegPrefixes`, `run_iter2.go:111`);
   colour then carries register-ness (§5).
6. **Labels** (`label_truncate=N`) — a branch/adr target longer than N is cut to
   N (+ ellipsis if `label_ellipsis`) — `truncLabelOp`, `run_iter2.go:126`. Only
   the *last* operand of a branch/adr instruction is in scope.
7. **R6 expression tightening** (`tighten_exprs`) — collapse spaces inside
   parenthesised expressions (`exprTighten`, `run_iter2.go:224`). Exploratory,
   not in the locked ladder.

The ladder is cumulative and each rule is one config key, so the rendered density
is fully tunable from R0 (objdump) to R6 (maximally compressed).

---

## 4. Long-line handling — wrap vs shift (the exact rule)

This is the rule the prototype design §intro flagged as "the central interaction
question — how a 120-char comment wraps at 64 columns." The lab's answer, as
implemented:

- **`wrap=on` (default):** **non-cursor code lines do NOT wrap** — they render at
  full width (a long line simply runs to the document's virtual width). **Only the
  cursor line's trailing comment unfolds** (§6), and it word-wraps. There is no
  per-code-line soft-wrap today. *This is a deliberate density choice:* the body
  stays one-row-per-statement (scannable), and the reader expands the one line
  they are on. (`lab_render.go:806` `expandComment` is the only wrapping path.)
- **`wrap=off` (horizontal shift):** each line renders to a 256-cell virtual row;
  the screen shows `[offset, offset+cols)` (`lab_render.go:782`). `viewport_step`
  (default 8) is the columns-per-shift. When `offset>0` the status bar's right
  edge shows `←<offset>` (the `labGlyphArrowLeft` 0x82 indicator,
  `lab_render.go:899`) so the off-screen left is signalled.

**Wrapping is word-boundary, never mid-word.** The wrapper (`wrapWords`,
`run_designs.go:448`) is greedy: append words until the next would exceed the
width, then break. Wrapped comment rows start at a fixed indent (`indent+2`),
with **no extra continuation indent or marker** — a known simplicity (§11 lists
"continuation cue" as a possible future lever). Spec consequence for the Z80
port: implement word-wrap (greedy, space-delimited), not character-wrap; match
the indent rule exactly so round-tripped display is identical.

---

## 5. Colour roles + token marking (the 4-pen budget)

MODE 3 gives 4 pens; the roles map onto them (`role_*` keys, `lab_config.go`):

- **Structural roles:** `role_paper` (0), `role_code` (3), `role_comment` (1),
  `role_label`, `role_chrome_fg/bg`, `role_current_line`, `role_status`.
- **Fine syntax roles** (split `role_code` by token class):
  `role_mnemonic`, `role_expression`, `role_register`, `role_immediate` — each a
  pen + optional `inverse`.
- **Token marking** (carry token-class by *appearance*, freeing the digit-only
  register form to stay unambiguous): when `reg_style=strip`, `reg_x_style` /
  `reg_w_style` / `reg_named_style` (each `none|fg|bg|inverse`) mark the three
  register classes (`lab_render.go:513`). **Iteration-2 insight
  (`mark_immediates`):** registers are the *common* token, so marking them floods
  the screen; marking the *rarer* immediate class (`mark_immediates`,
  `mark_immediates_style`) is calmer for the same disambiguation win. The default
  recommendation (§10) follows that insight.
- **`relax_palette`** lifts the 4-pen cap to 16 for *dreaming* (terminal only;
  it **blocks SAM PNG export** — `lab_png.go` refuses it). Never a shippable SAM
  config; it exists to explore, then map back into 4 pens.

The Z80 port carries one pen index per cell (and an inverse bit); the marking
styles are pen/inverse choices, nothing the SAM cannot do per-cell in MODE 3.

---

## 6. The cursor line — reading-flow expansion

Pete's reading-flow feature: the line under the cursor gets room for its full
comment without bloating every row. Two modes (`expand_cursor_line`):

- **`wrap:K`** — the cursor line's trailing comment unfolds **inline** to ≤K rows
  (word-wrapped at `cols-indent-2`), and the page below shifts down by the added
  rows (`lab_render.go:732`). Reading flows in place.
- **`panel:K`** — K **fixed bottom rows** hold the full comment; the page above
  never reflows (`lab_render.go:630`, `renderPanel` at `:864`). Steadier page,
  detached comment.
- **`expand_only_if_needed`** — skip expansion when code + comment already fit on
  one row (`lab_render.go:669`/`:635`); only the genuinely-overflowing line
  expands.
- **`cursor_block_style`** demarcates an expanded block: `none`, `frame` (box-
  drawing top/bottom rows), `bracket` (left half-block edge `labGlyphBracket`
  0x8a), `band` (background repainted with the current-line pen). Applies only
  when a line actually expanded (`lab_render.go:735`).

Spec note: `wrap:K` reflows the viewport (the row the cursor sits on changes the
rows below), so the Z80 renderer must recompute the page on cursor move; `panel:K`
does not (cheaper redraw). That cost difference is real on the SAM and is one
input to the §10 default (which picks `wrap:3` for the inline feel, accepting the
redraw — sub-page, cheap at 64×24).

---

## 7. Comments — placement + column rules

- **`comment_layout`:** `sameline` (text at `comment_column`, the default),
  `above` (own row above the code, marker-prefixed), `marker-only` (a gutter
  marker glyph `labGlyphMarker` 0x80 at column 0, no inline text — for a dense
  code view) (`lab_render.go:454`).
- **`comment_column`:** `page` clears the column past the page's widest *code*
  line; `commented-lines-only` clears past the widest *comment-carrying* line, so
  wide uncommented lines run under the comment column harmlessly (denser)
  (`lab_render.go:376` `pageCommentCol`). `comment_gap` (default 2) is the code↔
  comment spacing.
- **`comment_semicolon`** prefixes `;` (era convention) instead of the bare text.
- **Multi-line comments + blank lines** render per the **i78 design**
  (`source-structure-preservation-design.md`): a grouped comment is one entry
  with body newlines (one `//` row per body line); blank-run rows emit blank
  lines. The renderer here consumes that structure; it does not re-derive it.

---

## 8. Truncation interplay (`max_instruction_width`)

When `max_instruction_width=N` and a **non-cursor** code line exceeds N: it is cut
to N−1 + the ellipsis glyph (0x81), **and its same-line comment is suppressed** on
that row (`lab_render.go:570`,`:710`). The **cursor line is never truncated** (it
shows in full; `expand_cursor_line` handles its overflow). This keeps the body
scannable (no line past N) while guaranteeing the focused line is whole. The Z80
port must couple the two: truncating a row implies dropping its trailing comment,
and the cursor row is exempt from both.

---

## 9. Chrome (status line)

`status_line` is a template; fields expand (`lab_render.go:593` `fillTemplate`):
`{file}` `{line}` `{total}` `{pct}` (= line×100/total) `{mode}`. The default
template is `{file}  L{line}/{total}  {pct}  {mode}`. The horizontal-shift
indicator (§4) is appended at the right edge independently of the template (it is
not a template field). `status_message` is an optional second line.
`current_line_highlight` paints a full-width band on the cursor row.

---

## 10. The recommended default config (q13 — Pete's sign-off)

**This is the one taste decision in this doc.** Choosing the editor's default
*look* is Pete's — registered as **q13**. The recommendation, synthesising the
iteration-2 corpus measurements + the four starter styles, is a **tuned
`compressed.config`** (the iteration-2 recommendation), because the corpus data
says the default must make room for comments on a 64-col screen:

| Key | Recommended default | Why |
|---|---|---|
| `mnemonic_column` | `adaptive` | aligns operands without a fixed waste column |
| `imm_hex_amp` + `imm_drop_hash` | on | SAM-native immediates, narrower |
| `tight_commas` | on | reclaims operand width |
| `reg_style` | `strip` | `x23`→`23`; colour carries width |
| `mark_immediates` | on (not register flooding) | iteration-2 insight — calmer (§5) |
| `label_truncate` | 16 (+ ellipsis) | bounds long branch targets |
| `comment_layout` | `sameline`, `comment_column = commented-lines-only` | comments visible, column tight |
| `expand_cursor_line` | `wrap:3`, `expand_only_if_needed` on | reading-flow on the focused line only |
| `cursor_block_style` | `bracket` | a light, non-boxy cue |
| `max_instruction_width` | 40 (non-cursor) | keeps the body scannable |
| `clut` | `0,15,102,127` | the iteration-2 quadruple |

**Why a recommendation, not a lock:** the corpus *measurements* justify
compression (64 cols fits only 76% of comments uncompressed), but *which* style
feels right — baseline-clean vs compressed-dense vs comet-minimal — is a lived-use
judgement Pete makes against the real PNGs / SimCoupé SCREEN$. The four starter
configs exist precisely so he can A/B them. **The editor must ship with the
default user-overridable** (it is just a config file), so this is a starting
point, not a constraint — q13 records which starting point.

Pete's sign-off options for q13: (a) accept the tuned-`compressed` recommendation;
(b) pick a different starter (baseline / comet-minimal); (c) tune specific keys
after seeing the PNGs. Any answer is a one-line q13 resolution + a default config
file; none blocks the renderer port (the rules §3–§9 are independent of the
default chosen).

---

## 11. Deferred / future rendering levers (not decided here)

- **Geometry + font** — i6 / the i76 P3 memo (real-SAM evidence); §2 holds the
  presumption, the rules are geometry-parametric.
- **Palette banking** — a per-scanline CLUT swap (line interrupt) could buy more
  than 4 colours on screen at once (prototype design §2.2 note). Not exposed in
  the lab; a real-SAM optimisation to weigh later, not a rendering rule.
- **Font banks / styled glyphs** (bold/italic via a font-bank index per cell) —
  costs font RAM (prototype design §2.1); deferred until a need appears.
- **Per-cell blink** — MODE 3 has no per-cell flash; only a palette-swap blink
  (a whole pen alternating) is possible (prototype design §2.1). Not used.
- **Continuation cue on wrapped rows** (§4) — today wrapped rows have no marker;
  a future lever if lived use wants one.
- **Word-wrap on non-cursor code lines** (§4) — today only the cursor comment
  wraps; soft-wrapping the body is a possible mode, deliberately not the default
  (one-row-per-statement is more scannable).

These are tracked as future levers, not gaps — the rendering *rules* (§3–§9) are
complete for the default and the lab; these widen the design space if a future
need (or lived use) calls for them.

---

## 12. Sequencing

- **Now:** §3–§9 are the written contract for the lab's rendering (host
  authority). They land as this spec; no code change (the lab already implements
  them).
- **q13:** Pete signs off the default (§10) → a one-line q13 resolution + the
  chosen default config committed under `tools/editor-prototype/configs/` (or
  promoted to the editor's default).
- **Z80 port:** the eventual on-SAM renderer ports §3–§9 faithfully (the "Go is
  the authority, the Z80 is a port" rule); it consumes the i78-structured
  editor-region sidecar (§7). Tracked under the editor implementation strands
  (i4 viewer → i3 editor), not here.

---

## Sources

- [`editor-tui-prototype-design.md`](editor-tui-prototype-design.md) — the prototype architecture + §8 locked decisions (geometry/record-length) + the comment-width corpus data.
- [`source-structure-preservation-design.md`](source-structure-preservation-design.md) — the i78 blank-run + comment-grouping structure the renderer consumes (§7).
- `tools/editor-prototype/` — the executable authority: `lab_render.go`, `run_iter2.go`, `lab_config.go`, `configs/` (every §3–§9 rule cites the implementing line).
- Item registry: i76 (the prototype + this rendering strand), i6/i5 (geometry/mockups — gated on the P3 memo), i4/i3 (the viewer/editor that port these rules), i78 (the structure the renderer consumes).
- Question registry: **q13** (the default-config sign-off — Pete's call).
