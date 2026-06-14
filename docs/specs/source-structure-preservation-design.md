# Source-structure preservation in the v2 editor region (i78)

**Status:** design — the **blank-run half (part a) is IMPLEMENTED host-first +
CI-gated** (PR #218; the `kind u8` sidecar discriminator + `FlagTaggedSidecar`,
the `KindBlankRun` IR record, blank emission in both renderers, the corpus
round-trip blank-line assertion); the multi-line-comment grouping (part b) is
**decided here** (single sidecar entry with embedded newlines, §4) and needs no
code beyond the existing format; the indentation half (part c) is **deferred,
gated downstream of the i76 editor-interface sign-off** and only captured, not
designed (§5). The Z80 half (parse/render on the SAM) rides i48c. Finding from
the build (→ i90): blanks inside `%nobits`/BSS sections are dropped by
`-flatten` (the whole section body has no flat-image PC) — a pre-existing
flatten property, not an i78 loss; PROGBITS preservation is exact. · **Item:**
i78 · Lives alongside the editor-region format it extends.

**The invariant this serves (i78 registry row):** *render-to-text reproduces the
source's blank/comment structure exactly.* A round-trip (text → `.tbn` → text)
must give back the original blank lines and comment paragraph breaks, not a
re-flowed approximation. This is the same fidelity bar the assembled-binary
byte-match holds for code; here it is held for the *source layout* the editor
shows.

**Reads first (the format this extends, not restated):**

- [`tbn-binary-format-reference.md`](tbn-binary-format-reference.md) §2.3 (the
  `editor_region_offset` split) + §2.5 (the comment sidecar `[anchor_delta
  uvarint][placement u8][len u16][text]`) — the editor region this design adds to.
- [`comment-storage-design.md`](comment-storage-design.md) §2.1 — the editor holds
  the sidecar as immutable ZX0-compressed blocks; a new row type must compress
  with the bodies and survive the block model.
- [`editor-tui-prototype-design.md`](editor-tui-prototype-design.md) §8 (D1/D2) —
  the i76 lab is the source of the corpus structure measurements below, and the
  interface sign-off that gates part (c).

---

## 1. The problem the current format cannot represent

The v2 editor-region comment sidecar (format reference §2.5) anchors every
comment to the **output PC** it attaches to: a row is `[anchor_delta
uvarint][placement u8][len u16][text]`, and the renderer emits the comment when
its PC walk reaches the anchor. That faithfully reproduces *comments*. It does
**not** reproduce two kinds of **textless source layout**:

1. **Blank lines.** The corpus has **1,472 blank lines in 888 runs** (~11% of all
   lines) — i76 lab finding. A blank line emits no bytes and carries no text, so
   it has no natural anchor PC and no sidecar row. Today a round-trip silently
   drops every blank line; the rendered source is one dense wall.

2. **Comment paragraph separators.** Of the corpus's **214 textless `//` lines**
   (a `//` with empty body), **114 are paragraph separators inside comment
   blocks** — a blank `//` line breaking a multi-line comment into paragraphs.
   These *do* round-trip today (a textless comment is a `len=0` sidecar row), but
   the i78 row flags the **ambiguity**: a textless `//` and a true blank line are
   different source constructs, and an encoding that conflates them (e.g. "encode
   a blank line as an empty comment") would render one as the other on the way
   back. The remaining `214 − 114 = 100` textless `//` lines are standalone (a
   visual rule between code sections), also legitimately a comment.

The design must encode blank-line runs **distinctly from** textless comments, so
the renderer can tell "emit N blank lines here" apart from "emit a `//` with no
text here."

### 1.1 Why not just store every line

The naive fix — store the source as raw text lines and reconstruct verbatim — is
exactly what i85 measured and Pete rejected (item registry i85, 2026-06-13):
whole-file ZX0-as-text is **+18.5% larger** than the current hybrid (compact
instruction overlay + ZX0 comments), and the overlay earns its keep on assembly
**speed** + the in-memory editable IR, not size. So structure preservation must
be **additive metadata in the editor region**, not a format rewrite. The blank
runs cost ~3 KB (§3.4) on top of the existing sidecar — a rounding error against
the 108 KB compressed comment store, and far below the 35 KB i85 whole-file
penalty.

---

## 2. Design principle — anchor structure the same way comments are anchored

The editor region already has one anchoring mechanism that works: **output-PC
deltas walked in step with the record stream** (the comment sidecar and the
label/local header tables all use it, format reference §2.4/§2.5). Source
structure should reuse it rather than invent a parallel line-number space.

But blank lines and comments share a subtlety: **multiple textless constructs can
sit at the same output PC.** Several blank lines and a comment can all precede the
same instruction — they all anchor to that instruction's PC. The existing comment
sidecar already tolerates this (two comments at the same anchor are two rows with
`anchor_delta = 0` between them, and the renderer emits them in stored order). So
the rule generalises cleanly:

> **All textless-or-comment rows that precede a given statement are stored
> consecutively, anchored to that statement's PC (deltas of 0 between them), and
> the renderer emits them in stored order before the statement's line.** A
> blank-run row and a comment row are just two row *kinds* in that ordered list.

This makes blank runs a **third row kind in the comment sidecar**, not a new
section — they interleave with comments in PC-and-stored order, so a comment
block like

```
// header paragraph one
//
// header paragraph two

    movz w0, #1
```

(comment, textless-comment-separator, comment, blank line, instruction)
round-trips as four sidecar rows at the instruction's anchor, in that order,
then the instruction. The ordering carries the structure; no row needs a line
number.

---

## 3. The blank-run row (part a — build now)

### 3.1 Encoding

Add a **row-kind tag** to the sidecar row. Today every row is implicitly a
comment; introduce a one-byte discriminator at the front of each row:

```
Sidecar row (v2.1)   editor_region.go
  kind   u8        0 = comment, 1 = blank-run   (extensible; 2+ reserved)
  ── if kind == 0 (comment) ──
  anchor_delta uvarint
  placement    u8        0 standalone / 1 trailing  (unchanged, §2.5)
  len          u16
  text         len bytes
  ── if kind == 1 (blank-run) ──
  anchor_delta uvarint
  run_len      uvarint   number of consecutive blank lines (≥ 1)
```

A blank-run row is `[kind=1][anchor_delta][run_len]` — **3–4 bytes** for a
typical run (anchor_delta and run_len are both small uvarints; 888 runs, max run
length in the corpus is short). It carries no text and no placement (a blank line
is always standalone).

### 3.2 Anchoring + ordering

A blank-run row anchors to the **PC of the next statement** (the same anchor the
comment rows preceding that statement use), with `anchor_delta` relative to the
previous sidecar row's anchor — exactly the §2.5 delta rule. Within one anchor's
run of rows, **stored order is source order**: the renderer emits each row as it
walks the list, so `comment / blank / comment` and `blank / comment / blank`
render differently and correctly. Trailing blank lines at end-of-file anchor to
the final PC (the output length) like a trailing comment would.

**Why run-length, not one row per blank line:** 1,472 blanks in 888 runs means
the average run is ~1.66 lines; run-length encoding roughly halves the row count
versus one-row-per-blank and is the natural representation of "N blank lines
here." It also matches how a human reads the structure (a gap, of some size), and
it compresses well (the bodies are empty, so ZX0 sees a dense run of tiny
identical-shaped records).

### 3.3 Renderer + round-trip

- **Render:** the PC-walking renderer, on reaching a blank-run row, emits
  `run_len` newlines before the next statement's line. No `//` prefix, no text.
- **Parse (text → `.tbn`):** the host front-end (and later the i48c Z80
  text→overlay encoder) counts consecutive blank source lines and emits one
  blank-run row at the next statement's anchor. A blank `//` line stays a
  `kind=0`, `len=0` comment row (the §1 disambiguation) — the parser distinguishes
  them by whether the source line is empty (blank-run) or is a `//` with no body
  (textless comment).
- **Invariant test:** a corpus round-trip asserts blank-line count and positions
  are reproduced exactly (extend the existing per-fixture comment-count check in
  the disasm-roundtrip gate with a blank-run-count + position check). The i78
  invariant becomes a CI assertion, the same way the comment count already is.

### 3.4 Size

888 blank-run rows × ~3.5 B ≈ **3.1 KB** raw, in the editor region (matches the
i78 row's "~3 KB" estimate). Compressed into the comment store's ZX0 blocks
(comment-storage-design §2.1) the marginal cost is smaller still — empty-bodied,
uniformly-shaped rows are highly compressible. This is **additive** to the
existing sidecar; the assembler-facing region (what the SAM loads to assemble) is
**unchanged** — blank runs live entirely in the editor region the assembler never
reads (format reference §2.3), so they cost the assembler nothing and do not
touch the IN-buffer ceiling.

### 3.5 Compatibility with the block/compression model

The blank-run row is **just another sidecar row** (comment-storage-design §2.1:
"a block = a run of whole comment-sidecar rows"), so it inherits the entire block
architecture for free: it packs into a 4 KB block with the comment rows, anchors
against the block's base PC, compresses in the same ZX0 stream, and dirties /
materialises / streams-on-save exactly as a comment row does. **No change to the
block directory, the watermark math, or the page placement** — the row-kind tag
is invisible to the block layer, which sees opaque rows. The one touch-point: the
block-row-boundary walk must skip a blank-run row's variable length correctly,
which the `kind`-dispatched length computation handles.

---

## 4. Multi-line-comment grouping (part b — decided)

The i78 row leaves open whether a multi-line comment is **one sidecar entry with
user line-breaks** or **one row per line**. The format reference §2.5 already
states a block comment "renders as one `//`-prefixed line per body line," and a
comment body may carry embedded newlines. **Decision: one sidecar entry per
source comment, with the user's line-breaks preserved as embedded newlines in the
body** — *not* one row per rendered line.

Rationale:

1. **It is what the format already says** (§2.5: "a comment whose body carries
   embedded newlines renders as one `//`-prefixed line per body line"). One row,
   newlines in the `text`, `placement` on the whole group. No format change needed
   for grouped comments beyond what §2.5 already allows.
2. **It keeps the paragraph structure unambiguous.** A multi-paragraph comment
   block authored as

   ```
   // paragraph one, line A
   // paragraph one, line B
   //
   // paragraph two
   ```

   is **one comment entry** whose body is `"paragraph one, line A\nparagraph one,
   line B\n\nparagraph two"` — the blank `//` separator is an empty line *inside*
   the body (a `\n\n`), not a separate textless-comment row. This resolves the §1
   ambiguity at its root for *grouped* comments: a paragraph break inside a block
   is a body newline, and only a textless `//` that stands **between two distinct
   comment groups** (or alone) is a `kind=0 len=0` row.
3. **It anchors once.** The whole block attaches to one PC; the editor moves /
   edits / dirties it as a unit (one i41 record, comment-storage-design §2.2),
   which is the natural editing granularity for a comment paragraph.

**What counts as "one comment"?** A maximal run of consecutive standalone `//`
lines with no intervening code or blank *source* line is one grouped entry; the
internal textless `//` lines become `\n\n` paragraph breaks in its body. A
**blank source line** (not a `//`) between two comment lines **breaks the group**
(it is a `kind=1` blank-run row between two comment rows) — because a blank line
is a stronger visual separator than a `//` and the author meant a real gap. A
**trailing comment** (`placement=1`, on a statement's line) is always its own
single-line entry.

This grouping rule is the parser's join logic (text → rows); the renderer is the
inverse (emit `//` + each body line). Both are mechanical once the rule is fixed.

### 4.1 Interaction with the existing block-comment render bug fix

i39b-2 fixed a "latent multi-line `/* */` block-comment render bug" (item
registry i39b-2). The grouping rule here is consistent with that fix: a `/* */`
block already arrives as one comment with embedded newlines; consecutive `//`
lines now group the same way, so the renderer has **one** multi-line-comment code
path for both syntaxes. No second mechanism.

---

## 5. Code/loop indentation (part c — deferred, gated, captured only)

The i78 row is explicit (Pete, 2026-06-13): whether to **retain code
indentation** is an **output of the editor-interface design / sign-off**, not a
decision to make now. At 64 columns screen estate may not justify leading
whitespace, and the compressed rendering already reclaims it (the i76 lab's
compressed-rendering ladder strips leading whitespace as one of its levers). So:

- **Do not build indentation preservation now.** Building round-trip fidelity for
  a feature the interface may abandon is wasted work (and would bloat the editor
  region for nothing if the interface re-indents programmatically).
- **The mechanism is already in hand if it's wanted.** *If* the signed-off
  interface keeps source indentation, leading whitespace on code lines preserves
  via **the same editor-region mechanism as the blank-run rows** (§3): a
  per-statement "leading-whitespace" datum in the editor region, anchored to the
  statement's PC, that the renderer prepends. It is a small, well-understood
  addition — a `kind=2` row, or a per-record indent byte in a parallel
  editor-region table — not a new design problem. The blank-run row kind (§3.1)
  reserves `kind ≥ 2` precisely so this slots in without a format-version bump.
- **The decision input is the i76 P3 memo** (editor-tui-prototype-design §8 D2):
  the geometry + interface sign-off that resolves i6 also resolves whether
  indentation is kept. i78(c) waits on that memo. Capture now (this section);
  build only if the memo says keep.

This keeps i78(c) tracked without committing format space or parser logic to a
feature that may not ship — the registry-discipline way to hold a deferred
consideration.

---

## 6. Sequencing + scope

- **Now (part a + part b):** the blank-run row kind (§3) + the comment-grouping
  rule (§4) are designable and buildable against the current host front-end and
  format. They land in the **host** first (the encoding authority — the Go
  front-end and renderer), with the corpus round-trip invariant (§3.3) as the CI
  gate, mirroring how every format change lands host-first then ports to Z80. The
  Z80 side is the renderer/parser in the eventual editor (i48c text→overlay
  encoder + the on-SAM renderer), a faithful port of the host logic — not a new
  design (the "Go is the authority, the Z80 is a port" rule).
- **Deferred (part c):** gated on the i76 interface sign-off (§5).
- **Format-version note:** adding the `kind` discriminator is a sidecar-row
  change in the **editor region only**. The assembler-facing region and the
  assembled binary are **untouched** (§3.4), so the `.tbn` byte-match invariant
  (binary-identity + round-trip + `.tbn`-shrinks-or-holds) is unaffected — the
  editor region is not in the assembler's byte-match. The editor region is
  versioned by the file's version/flags (the format header carries a `Version`
  field + a reserved `Flags` u16, format reference §2.1), so the reader knows from
  the version which sidecar shape it is parsing and the host keeps reading both:
  an old (no-tag) sidecar is the untagged shape where every row is definitionally
  a comment (`kind=0`), the new shape carries the discriminator. The exact
  version-flag bit is an implementation detail for the landing PR.

---

## 7. Open / out of scope

- **Exact host wiring** (the `editor_region.go` reader/writer changes, the
  front-end blank-counting + comment-grouping pass, the renderer changes) is the
  implementation PR's plan, not this design.
- **i48c (the Z80 text→overlay encoder)** carries the on-SAM half: counting blank
  runs and grouping comments at edit-entry time. It mirrors the host authority
  (i48 spec; this design is the spec it ports). Tracked under i48c, not here.
- **Indentation (part c)** — §5, gated.
- **Anything that re-flows or normalises the source** is explicitly rejected: the
  invariant is *exact* reproduction of the author's blank/comment structure (the
  dev experience is the product goal, item registry i59/i77 framing).

---

## Sources

- [`tbn-binary-format-reference.md`](tbn-binary-format-reference.md) §2.3, §2.5, §2.4 — the editor region + comment sidecar format this extends.
- [`comment-storage-design.md`](comment-storage-design.md) §2.1, §2.2 — the compressed-block model the new row inherits.
- [`editor-tui-prototype-design.md`](editor-tui-prototype-design.md) §8 — the i76 lab corpus measurements + the interface sign-off that gates part (c).
- Item registry: i78 (this item), i76 (lab findings + interface sign-off), i48c (Z80 port of the encoder), i39b-2 (the editor-region split + the block-comment render fix), i85 (whole-file-as-text measured larger — why this is additive metadata, not a rewrite), i41 (the block-list the dirty rows materialise into).
