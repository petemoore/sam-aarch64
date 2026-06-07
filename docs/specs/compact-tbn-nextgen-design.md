# Compact `.tbn` next-gen format — design exploration (item i39)

**Date:** 2026-06-08 · **Item:** i39 · **Type:** exploratory design (OPTIONS + recommendation, not a frozen spec)

**Authority for the current format:** the code, not prose. Grounding reads for
this doc: `tools/sam-aarch64-format/{kinds,operands,expr,directives,symbols,reader,writer,litinsts}.go`,
`tools/refenc/{compact,pass1,pass2}.go`, `tools/aarch64dec/`, and the Z80 side
`src/{reader,main_loop,encoder}.asm`. The prior format/disassembler design note
is `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`; this doc is
its successor for the *density* question.

---

## 1. Goal & the 512 KB framing

The on-SAM assembler/editor holds the **entire source** in RAM as a `.tbn`
record stream (paged into physical pages 7..12 today — see `src/reader.asm`).
The SAM Coupé has 512 KB. The binding constraint on "how big a program can I
edit/assemble on real hardware" is therefore **how many bytes of `.tbn` the
source compresses to**, not the size of the assembled output (which streams to
OUT and need not be resident in full).

So the optimisation target is unambiguous: **minimise resident `.tbn` bytes per
line of aarch64 source**, subject to four hard constraints that create the real
tension (§ "Hard constraints"). Everything below is measured against the
vendored spectrum4 release corpus, whose current compact `.tbn` is **51,117 B**
(down from 88,644 B symbolic — the i1 LIT_INSTS/LIT_DATA work, −42.3%).

The release is ~21,752 B of assembled binary. A useful sanity figure: the
release's *assembled bytes* are smaller than its *compact source* (51 KB). Any
format that approaches "source ≈ assembled-binary + sidecar" is close to the
information-theoretic floor for the literal-heavy majority of the corpus.

---

## 2. Where the bytes go today (targets ranked)

From the corpus breakdown of the 51,117 B compact release `.tbn`:

| chunk | bytes | % of file | what it is |
|---|---:|---:|---|
| **symbolic INST records** | ~18,125 | 35.5% | branches/adrp/ldr-litpool/symbol-bearing instructions, stored `[mnem u16][opcount u8][self-describing operands…]` |
| **LIT_INSTS** | ~11,576 | 22.6% | assembled 4-byte words of fully-literal PC-invariant instructions (= their machine code) |
| **name/symbol table** | ~6,598 | 12.9% | `[count u16]` then per-name `[len u16][bytes]` |
| **other symbolic directives** | ~6,000 | 11.7% | `.equ`/`.align`/`.ascii`/`.quad`-with-symbol/… |
| **LIT_DATA** | ~4,058 | 7.9% | constant numeric data runs, raw LE bytes, tagged with directive_id |
| **LABEL_DEF + LOCAL_DEF** | ~2,098 | 4.1% | `[kind][len u16][symbol_id u16]` / `[digit u8]` position markers |
| framing/headers/other | ~2,662 | 5.2% | per-record `[kind][len u16]` overhead, magic, flags |

Ranked by attackable size: **symbolic INST (18 KB) ≫ LIT_INSTS (11.6 KB) ≈
name table (6.6 KB) ≈ other directives (6 KB) > LIT_DATA (4 KB) > label defs (2 KB)**.

Two structural facts drive the whole design:

1. **Per-record framing is 3 bytes** (`[kind u8][len u16]`). With ~thousands of
   records, framing alone is multiple KB. Anything that *merges records into
   runs* deletes framing wholesale — this is why LIT_INSTS already wins so big,
   and why "runs of anything" (idea 3) is structurally the highest-leverage move
   after the instruction overlay.
2. **LIT_INSTS is already at the floor for literal instructions** (4 B = the
   instruction's own machine code, zero per-instruction framing inside a run).
   You cannot beat 4 B/instruction without a general-purpose compressor over the
   word stream (LZ/Huffman), which the constraints rule out (§3.8). So the
   *symbolic* INST chunk is where the asymmetry — and the opportunity — lives:
   it's spending ~2–4× the bytes of a literal instruction to carry one or two
   symbol references.

---

## 3. Candidate techniques (one per idea; accept / refine / reject)

For each: encoding sketch, saving on the corpus, Z80 decode-cost delta,
round-trip impact, risk. Z80 cost is judged against the current superpower —
LIT_INSTS/LIT_DATA decode to a **memcpy to OUT** (`main_handle_lit_insts` in
`src/main_loop.asm`: `dec bc / inc hl / call main_emit_string_bytes`), i.e.
*near-zero* per-byte cost on a 3.5 MHz Z80, two passes.

### 3.1 Bitfield packing (idea 1) — REFINE, apply narrowly

**Idea.** Stored values are mostly sub-byte: register=5 bits, operand-kind tag,
shift-kind=2 bits, cond=4 bits, width=1 bit, MEM-shape=3 bits. Today each lands
in its own byte (see `OperandWriter.WriteReg` → 2 bytes `[kind][reg]`, etc.).
Pack several sub-byte fields into shared bytes.

**Where it actually bites.** The win is proportional to *how many* operands are
stored symbolically. But recall: **fully-literal instructions are not stored as
operands at all** — they're 4-byte words in LIT_INSTS. So bitfield-packing only
touches the ~18 KB symbolic-INST chunk and the ~6 KB other-directives chunk. And
within symbolic INSTs, the dominant payload is the **expression bytecode** for
the symbol reference (a `PUSH_SYM id16` is 3 bytes; with `+REL_LO12` etc. it's
4–6), not the register bytes.

**Encoding sketch (operand-stream packing).** Replace the byte-per-field operand
encoding with a nibble/bitfield-packed one. E.g. a register operand becomes a
single byte `[kkk rrrrr]` (3-bit kind-class + 5-bit reg) instead of 2 bytes. A
shifted-reg head becomes `[width:1 shiftkind:2 reg:5]` = 1 byte instead of 3
(`[OpShiftedReg][width][reg][shiftkind]`). MEM-base-idx-shifted packs
`shape:3 | idxwidth:1 | shiftamt:4` etc.

**Saving estimate.** Symbolic INSTs in the corpus average ~2.5 operands;
register/immediate-tag/shift bytes are roughly 40% of the 18 KB symbolic-INST
payload (the rest is expression bytecode + framing). Packing those ~7 KB of
field bytes at ~45% reduction ≈ **−3.2 KB**. On other-directives, similar
field-packing ≈ **−0.8 KB**. Total ≈ **−4 KB (−7.8% of file)**.

**Z80 decode-cost delta.** *Non-trivial but bounded.* Today the operand reader
(`OperandReader.Next` mirrored on the Z80) does `ld a,(hl) / inc hl` per field —
a byte fetch. Packed fields need mask+shift: `ld a,(hl) / and 0x1f` for the reg,
`rrca` ×N + `and` for the high bits. That's ~4–8 extra T-states per field, on a
path that only runs in **pass 2** and only for **symbolic** instructions (a
minority). It does **not** touch the LIT_INSTS memcpy fast path. So the global
slowdown is small. Risk: the Z80 operand parser (`main_handle_inst_parse_loop`
in `main_loop.asm`) gets more code and more branches — a real but manageable
maintenance cost.

**Round-trip impact.** None — packing is lossless and the disassembler unpacks
symmetrically.

**Verdict: REFINE / accept narrowly.** −4 KB is real but it's the *least*
leverage per unit of Z80 complexity among the ideas, because it can't touch the
LIT_INSTS chunk at all and the expression bytecode dominates symbolic payloads.
Do it, but as a *second-order* polish layered on top of the structural wins
below — not as the headline. Crucially, the **overlay idea (3.2) makes most
symbolic operands disappear entirely**, which is strictly better than packing
them.

### 3.2 Assembled-word + zeroed-bitfields + sparse expression overlay (idea 2) — ACCEPT (the centrepiece)

**Idea (Pete's central one).** Store EVERY instruction as its 4-byte assembled
word. For operands that are expression/symbol-bearing, *zero* those bitfields in
the base word and attach a compact overlay = {which field(s), the expression
bytecode}. Pass 2 resolves the expression and ORs the value into the zeroed
field. Fully-literal instructions are just the 4 B word (today's LIT_INSTS);
symbol-bearing ones are 4 B + a small overlay. This **unifies the two
instruction representations** and directly compresses the 18 KB symbolic-INST
chunk.

**Why this is the right model.** The encoder *already* works exactly this way:
`encodeInst` in `pass2.go` computes a base pattern and ORs in slot values
(`base | ((imm19 & 0x7ffff) << 5) | rt`, `word := (sf<<31)|(opc<<29)|… `). A
symbol-bearing instruction is just "the same base word, but one slot's bits come
from a resolved expression instead of a literal." So the overlay's "OR the
resolved value into a bit-range of a fixed base word" is *the encoder's own
inner loop*, not new machinery. On the Z80 this is dramatically simpler than the
current symbolic path (which re-derives the entire base pattern from
mnemonic_id + operand kinds via the form table).

**Encoding sketch.**

```
INST_OVL record (replaces symbolic KindInst):
  [base_word u32 LE]          ; assembled word with relocated bitfields zeroed
  [patch_count u8]            ; usually 1, occasionally 2 (e.g. movk #imm + shift)
  patch[0..patch_count-1]:
    [slot u8]                 ; which bit-range + how to fold (see slot table)
    [expr_len u8][expr bytes] ; the existing expression bytecode, unchanged
```

`slot` is a small enum naming a (lsb, width, fold-rule) triple, drawn from the
slot kinds the encoder already distinguishes (`enc.BranchImm26/19/14`,
`AdrpImm`, `AdrImm`, `LogicalImm`, the imm12/imm9 mem offsets, `Rt/Rn/Rm`
register positions, the movk hw-shift, …). The fold-rule encodes the PC-relative
conversions pass2 already does (`v - pc` for branches, the ADRP page-diff masking,
imm scaling by element size). Crucially these rules are **finite and already
enumerated in `operandsToValues`/the mem/branch encoders** — porting them is
mechanical (CLAUDE.md §6: "if Go already implements it, the Z80 side is a port").

**The expression bytecode is reused verbatim** — `PUSH_SYM`/`PUSH_PC`/`REL_LO12`
etc. (`expr.go`). The overlay just says *where the result lands*.

**Saving estimate.** Today a typical symbol-bearing INST is, e.g.,
`bl target`: `[kind][len u16] [mnem u16][opcount u8] [OpImmExpr][len u16][PUSH_SYM id16]`
= 3 (frame) + 3 (mnem/count) + 3 (op header) + 3 (PUSH_SYM) = **~12 B**.
Under the overlay (and inside a run so framing amortises — see 3.3):
`[base u32][patch_count=1][slot u8][expr_len=3][PUSH_SYM id16=3]` = 4+1+1+1+3 =
**10 B**, and *without* the 3 B record frame when run-merged → **~9–10 B** vs 12.
For `adrp x0, sym` + `add x0,x0,:lo12:sym` pairs the win compounds: each becomes
base+overlay and they pack into one run. An `ldr x0,=sym` (litpool) becomes a
base word whose imm19 is patched from the pool index — overlay carries the pool
ref, ~9 B vs ~12.

The bigger structural win: today symbolic INSTs **break LIT_INSTS runs**, paying
3 B of framing every time the stream alternates literal/symbolic. Folding
symbolic instructions into the *same run kind* as literal ones (a literal
instruction is just "overlay with patch_count=0") means **one run spans the
whole `.text`** and framing is paid once per ~1 KB STAGING_BUF chunk, not per
instruction. Estimated effect on the combined 18 KB (symbolic INST) + 11.6 KB
(LIT_INSTS) = 29.6 KB instruction chunk:

- literal instructions: 4 B each, unchanged (~11.6 KB).
- symbol-bearing: from ~12 B to ~9 B average, and framing largely eliminated.
  ~18 KB → ~**12.5 KB**.
- run-framing for the unified instruction stream: a few hundred bytes total.

**Net on the instruction chunks: ~29.6 KB → ~24.2 KB, i.e. −5.4 KB (−10.6% of
file)** — and that is *before* layering bitfield-packing on the overlay's slot
bytes. Combined with 3.3's framing elimination the realistic figure is closer to
**−6 to −7 KB**.

**Z80 decode-cost delta.** *This is the subtle part, and it's favourable.*
- **Pass 2, literal instruction (patch_count=0):** memcpy the 4-byte word to OUT.
  Identical to today's LIT_INSTS. Zero added cost.
- **Pass 2, symbol-bearing:** copy the base word, then for each patch: eval the
  expression (the Z80 *already has* the expression evaluator for symbolic INSTs
  and litpool exprs), apply the fold-rule (a subtract-PC or shift — small fixed
  code per slot kind), and OR the result into the base word's bit-range, then
  emit. This **replaces** today's far heavier symbolic path (form-table lookup +
  per-operand-kind dispatch + `encode_inst`). So for symbol-bearing instructions
  the overlay is *cheaper to decode than today*, not more expensive.
- **Pass 1:** every instruction (literal or overlay) advances PASS_PC by 4. No
  operand parse at all in pass 1 (matches today's `main_handle_inst_pass1`).

So idea 2 is the rare case where the denser encoding is also the *faster*
decoder, because it pushes work the encoder was redoing into precomputed base
words. **This is the headline technique.**

**Round-trip impact.** The disassembler (`aarch64dec`) already turns a 4-byte
word into text. To reconstruct the *symbolic* operand it needs the overlay:
disassemble the base word, then for each patch replace the rendered immediate/
register field with the symbolic expression (label name via the name table,
`:lo12:` from the fold-rule, etc.). This is *more* faithful than today, not less —
the base word carries the exact instruction form, and the overlay carries the
exact symbol. The one subtlety: the base word has zeroed relocated bits, so the
disassembler must not render those literally — it must always consult the
overlay for patched slots. Tractable; the slot enum tells it which fields to
suppress.

**Implementation risk.** *Medium.* The slot/fold-rule enum must be exhaustive
and exactly mirror the encoder, or output won't byte-match GNU (the m6-release
gate). Mitigation: derive the slot table mechanically from the encoder's own
slot kinds; add a round-trip property test (assemble→overlay→assemble, require
byte-identical) extending the existing `disasm-roundtrip` gate.

**Verdict: ACCEPT — this is the design's spine.** It attacks the single largest
chunk, unifies two representations, and is *faster* to decode for the symbolic
case. It subsumes most of idea 1 (the operands that 3.1 would pack mostly vanish).

### 3.3 Runs of anything (idea 3) — ACCEPT (generalise the run container)

**Idea.** Generalise LIT_INSTS/LIT_DATA into a run over ANY homogeneous record
sequence, not just literal instructions / constant data.

**Refinement — the right granularity.** The win from a run is **deleting the 3 B
`[kind][len u16]` frame per element**. The two record families that occur in
long homogeneous stretches are (a) instructions and (b) numeric data. With idea
3.2, *all* instructions become one element type (overlay records, patch_count=0
for literals), so a single **instruction run** spans `.text` regardless of
symbolic/literal mix. That's the high-value generalisation. Generic "run of
arbitrary records" buys little beyond that because the other record kinds
(LABEL_DEF, COMMENT, .equ) are interspersed and short — a run needs ≥2–3
consecutive same-kind records to beat per-record framing.

**Encoding sketch (instruction run).**

```
INSN_RUN record:
  [kind=INSN_RUN][len u16]            ; ONE frame for the whole run (≤ ~1 KB chunk)
  repeated, until len consumed:
    [base_word u32][patch_count u8][patches…]
```

A literal instruction is `[u32][00]` = 5 B inside a run (vs 4 B in today's
LIT_INSTS — costs +1 byte/literal for the patch_count). To avoid that
regression, use a **1-bit tag in the base word's spare encoding** is impossible
(all 32 bits are the instruction), so instead use a *run-mode byte* at run start:

```
INSN_RUN:
  [kind][len u16][mode u8]
  mode 0 = "all literal": payload is packed 4-byte words (== today's LIT_INSTS, 4 B each)
  mode 1 = "overlayed":   payload is [u32][patch_count][patches…] elements
```

The compactor emits mode-0 sub-runs for maximal literal stretches (preserving
today's 4 B/instruction floor exactly) and mode-1 sub-runs that absorb the
symbolic instructions plus any short literal gaps between them (where carrying a
`[u32][00]` 5 B element is cheaper than closing+reopening a mode-0 run, which
costs a 4 B frame). This is a small local optimisation in `compact.go`.

**Splitting for STAGING_BUF.** Runs already split at `litDataMaxBytes`/255-word
caps (`compact.go`). The same splitter applies; nothing new.

**Saving estimate.** The framing eliminated by merging symbolic INSTs into runs
is counted in 3.2 (~the dominant part of its win). Beyond that, **−0.5 to −1 KB**
from merging short literal/symbolic alternations that today pay double framing.

**Z80 decode-cost delta.** Negligible — the run loop is the existing
`main_handle_lit_insts` memcpy for mode 0, and a tight per-element loop for mode
1 (which is just 3.2's per-instruction decode). One frame read per ~1 KB instead
of per record actually **reduces** reader overhead (fewer `reader_next_kind`
calls, each of which does an LMPR window dance).

**Round-trip impact.** None beyond 3.2.

**Verdict: ACCEPT, but as "one instruction run kind with a literal/overlay
mode," not as fully generic runs-of-arbitrary-records.** Generic runs are a
complexity tax for sub-KB gains; the instruction run is where the framing lives.

### 3.4 Labels shouldn't break runs — header label table (idea 4) — ACCEPT

**Idea.** A LABEL_DEF between two instructions splits the run today
(`compact.go` flushes the instruction run on any non-literal record). A label is
a *position marker*, orthogonal to the instruction stream. Move labels to a
**header table** mapping label → byte-offset (≈ PC) or → record-index, so the
instruction stream stays one long run.

**Why it's correct and cheap.** Pass 1 already computes each label's PC by
walking and accumulating sizes (`main_handle_label_def` inserts `(symbol_id,
PASS_PC)`). If labels are pre-resolved to **byte offsets from origin** and stored
in a header table, pass 1 can populate the symbol table by *reading the table
directly* instead of encountering LABEL_DEF records mid-stream — and the
instruction run is never broken. This deletes the 2,098 B of LABEL_DEF/LOCAL_DEF
records AND removes the run-splits they caused.

**Encoding sketch.**

```
Label table (in header, after name table):
  [count u16]
  repeated: [name_id u16][offset u32]      ; offset = PC from origin
Local-label table:
  [count u16]
  repeated: [digit u8][offset u32]         ; one row per 1:/2:/… definition site
```

For the corpus: ~350–400 global labels × 6 B = ~2.3 KB, plus local defs. That's
*about the same* as the current 2,098 B of label records — so the raw label data
isn't smaller. **The win is indirect**: (a) labels no longer split instruction
runs (framing saved, counted in 3.2/3.3), and (b) a byte-offset label table is
*pass-1-free* — the Z80 reads it once into the symbol table and never dispatches
a LABEL_DEF mid-walk.

**A genuine size win on the table itself:** store offsets as **deltas** between
consecutive labels (most are small, fit in 1–2 bytes via a varint), since labels
are emitted in increasing-PC order. Delta + varint cuts the offset column from
4 B to ~1.5 B average → label table ~2.3 KB → ~**1.4 KB**, **−0.7 KB**, plus the
run-unsplitting benefit.

**Z80 decode-cost delta.** *Lower*, not higher: instead of per-LABEL_DEF
dispatch in the walk loop, pass 1 does one upfront table load (sequential reads,
varint-accumulate). Removes branches from the hot walk loop.

**Round-trip impact.** Positive — labels with names live in one place; the
disassembler annotates the instruction at offset X with "label foo:" by lookup.
Local labels (1f/1b) need the per-site offset list, which the table provides;
the disassembler renders `1:` at each site exactly as today.

**Risk.** *Low-medium.* Forward references still resolve fine because the table
gives every label's final offset up front (strictly better than the 2-pass
mid-stream discovery). One subtlety: `.equ`/`.set` symbols are *values*, not
positions — they stay as directive records (or move to a separate "symbol value"
header table; see 3.5). Don't conflate position-labels with value-symbols.

**Verdict: ACCEPT.** Modest direct saving (−0.7 KB) but it's the enabler for the
single-long-run model in 3.2/3.3, and it makes pass 1 simpler/faster.

### 3.5 Local vs global symbols within one file (idea 5) — PARTIALLY ACCEPT

**Idea.** Does the local/global distinction matter inside a single source file?
Maybe it's only for source-fidelity round-trip.

**Analysis.** Two distinct "local" concepts:
1. **Numeric local labels** `1f`/`1b` (`OpPushLocal`, `LOCAL_DEF`). These are
   *semantically* different from named labels — resolution is direction-relative
   (`makeCtx`'s `LocalLabel` scans for nearest forward/backward). The assembler
   **needs** this distinction; it cannot be merged into the named-label table
   without losing the forward/backward semantics. **Keep.**
2. **`.global` / local-vs-exported named symbols.** For a *single flat-section
   self-contained* program (which is exactly refenc's model — single implicit
   `.text`, see `alignPadBytes` note in `pass2.go`), **`.global` is a no-op at
   encoding time.** The assembler never needs to know a symbol is exported;
   there's no linker. The distinction is **purely round-trip fidelity** (so the
   editor can re-emit `.global foo`).

**Implication for density.** `.global` directives can be dropped from the
**assembler-facing stream** entirely and recorded only in an **editor sidecar**
(a bitset/list of which name_ids were `.global`). Same for any other
encoding-irrelevant attribute. The name table is shared; the sidecar is a few
hundred bytes and need only be **paged in by the editor, not by the assembler** —
so it doesn't count against the assembler's resident budget during a build.

**Saving estimate.** `.global` records in the corpus: modest (~tens of symbols ×
~8 B) ≈ **−0.3 to −0.5 KB** from the assembler stream; the sidecar lives
elsewhere. The bigger structural point this unlocks is the **editor-only
sidecar** pattern (see 3.7 and the name table), which is where the real savings
are.

**Verdict: PARTIALLY ACCEPT.** Keep numeric local labels (assembler needs them).
Move `.global` and other purely-cosmetic symbol attributes to an editor sidecar,
out of the assembler's resident stream.

### 3.6 Embedded comments + the round-trip vision (idea 6) — ACCEPT as the target *shape*, REFINE placement

**Idea.** The compact form ≈ "the disassembly, with expressions embedded as
state machines, comments embedded, a label table in the header, efficient
runs-of-anything, bitfields squashed into shared bytes."

**Evaluation.** This is a coherent and correct *target shape* — it's essentially
"3.2 + 3.3 + 3.4 + 3.1" plus comments. The one tension is **comments vs the
instruction run**: a COMMENT record between two instructions breaks the run just
like a LABEL_DEF does. Comments are pure round-trip data the **assembler never
reads** (the Z80 walk loop already does `cp REC_KIND_COMMENT / jp z, walk_records`
— skip). So comments are the strongest candidate for an **editor-only sidecar**:

```
Comment sidecar (paged in only by the editor):
  repeated: [anchor u32 = byte-offset it attaches to][placement u8][len u16][text]
```

This removes **every** comment from the assembler's resident stream — and the
release likely has substantial comment volume in its non-vendored form (the
vendored release `.tbn` may already be comment-stripped; for *user-authored*
source this is potentially the single largest editor-vs-assembler split).

**Saving estimate.** For the vendored release, comments are minimal so the
direct saving is small. **For the daily-driver vision** (large hand-written
source), moving comments to a sidecar could be the difference between fitting and
not fitting a big program — the assembler pass never pages comment bytes in.
**Strategic accept.**

**Verdict: ACCEPT the shape.** Build toward "compact ≈ disassembly + overlays +
header label table." **Refine:** comments (and `.global`, and original
numeric-base hints) go to an **editor-only sidecar** keyed by byte-offset, never
in the assembler-facing run.

### 3.7 Symbol-name table compression (idea 7) — ACCEPT (prefix pooling + editor split)

**Idea.** The 6.6 KB name table: prefix/suffix pooling, shorter ids, or dropping
names the assembler doesn't need (editor sidecar).

**Analysis — what the assembler actually needs.** The assembler needs a
**name_id → value** map (pass 1 builds `Symbols[name] = pc`). It does **not** need
the *spelling* of the name at all — `makeCtx` looks up `p1.Symbols[f.Names[id]]`,
but that indirection through `f.Names[id]` is only because the symbol table is
keyed by string. **If labels are pre-resolved to offsets in a header table (3.4),
the assembler needs only `id → value`, never `id → string`.** The name *strings*
are pure round-trip / editor data.

**Conclusion: the entire 6.6 KB name-string table can move to an editor sidecar.**
The assembler-facing stream keeps only numeric ids (in overlays' `PUSH_SYM id16`
and the offset table). The disassembler/editor pages in the name strings to
render `foo:` and `bl foo`.

**But** the disassembler is needed for round-trip *in the same build artefact*,
so the names must live *somewhere* in the `.tbn`. The split is **resident-during-
assembly** vs **resident-during-edit/disasm**: paging means the name table can be
a separate page-group the assembler simply doesn't map. Saving against the
*assembler's resident budget*: **−6.6 KB** (the whole table). Saving against
total `.tbn` file size: apply compression to the table itself:

- **Front-coding (shared-prefix).** Symbols in real aarch64 source share long
  prefixes (`spectrum4_`, `__`, `handle_`, …). Sort-and-front-code or
  encounter-order front-code: each name stored as `[shared_prefix_len u8][suffix]`.
  Empirically front-coding aarch64 symbol tables saves 30–50%. On 6.6 KB →
  ~**3.8 KB**, **−2.8 KB** of *file* size (and the editor's decode cost is a
  trivial prefix-copy).
- **Shorter ids.** Ids are u16 (`PUSH_SYM`). With <500 symbols a **varint id**
  (1 byte for ids <128) saves ~1 byte per symbol reference across all overlays —
  but complicates the fixed-width `PUSH_SYM` decode. Marginal; defer.

**Z80 decode-cost delta.** The assembler **never decodes the name table** under
this design → strictly faster (one fewer page mapped, the reader_init name-skip
loop in `reader.asm` is deleted). Front-coding is decoded only by the
editor/disassembler, off the assembler's hot path.

**Round-trip impact.** None — names are fully recoverable from the front-coded
table.

**Verdict: ACCEPT.** Move name *strings* out of the assembler's resident stream
(−6.6 KB resident); front-code the table for −2.8 KB of file size. This is the
second-biggest win after the instruction overlay.

### 3.8 (Considered and REJECTED) general-purpose compression over the word stream

LZ77/Huffman/range-coding over the LIT_INSTS word stream would beat 4 B/literal
on repetitive code. **Rejected** because: (a) it destroys the memcpy-to-OUT
superpower — every literal instruction would need a decompressor pass on the Z80,
violating the "near-zero decode cost" constraint; (b) it makes records
non-splittable for STAGING_BUF (a 1 KB window can't hold arbitrary back-
references); (c) it breaks random-access editing (the editor must decompress to
mutate). The format must stay **streamable and seekable**; entropy coding is the
wrong layer. If raw density were the *only* goal we'd LZ the whole file at rest
and decompress on load — but the binding constraint is *resident* bytes during
interactive assembly, and you can't hold the decompressed form AND assemble from
it on a Z80 without the RAM you were trying to save.

---

## 4. Candidate FORMATS (points on the compression-vs-complexity curve)

Baselines: today symbolic **88,644 B**, today compact **51,117 B**.

### Format A — "Minimal": instruction overlay + header label table

3.2 (overlay, unifying LIT_INSTS + symbolic INST into one INSN_RUN) + 3.4
(header label table, delta-varint offsets). Leaves name table, directives,
LIT_DATA, comments as today.

| chunk | today | Format A |
|---|---:|---:|
| instruction (sym + lit) | 29,701 | ~24,200 |
| label/local defs | 2,098 | ~1,400 (header table) |
| name table | 6,598 | 6,598 |
| other directives | 6,000 | 6,000 |
| LIT_DATA | 4,058 | 4,058 |
| framing/other | 2,662 | ~1,900 |
| **total** | **51,117** | **~44,150 (−13.6%)** |

Z80 decoder: *simpler than today* for symbolic instructions (overlay OR-in
replaces form-table dispatch); pass 1 reads the label table upfront. Round-trip:
strictly more faithful. **Lowest risk, ships the spine.**

### Format B — "Recommended": A + name-table front-coding + editor sidecars

Format A, plus 3.7 (front-code the name table, −2.8 KB file) + 3.6/3.5 (comments
and `.global` to an editor-only sidecar) + 3.1 applied to the overlay slot bytes.

| chunk | Format A | Format B (file) |
|---|---:|---:|
| instruction | 24,200 | ~22,500 (slot-byte packing, 3.1) |
| label/local defs | 1,400 | 1,400 |
| name table | 6,598 | ~3,800 (front-coded) |
| other directives | 6,000 | ~5,200 (packed + `.global` to sidecar) |
| LIT_DATA | 4,058 | 4,058 |
| framing/other | 1,900 | 1,700 |
| **total file** | 44,150 | **~38,650 (−24.4% vs today)** |

**Resident-during-assembly** is smaller still: the name table (~3.8 KB) and
comment sidecar are **not paged in by the assembler** → assembler-resident
budget ≈ **~34.5 KB**, i.e. **−32.5% vs today's 51 KB** for the thing that
actually constrains "how big a program fits." This is the headline number.

Z80 decoder: moderately more code than A (slot-byte unpacking + front-code skip
for the editor), but the assembler's hot path is *unchanged from A* (it ignores
the name/comment pages entirely). Round-trip: full fidelity via sidecars.

### Format C — "Maximal": B + runs-of-anything + varint ids + numeric-base sidecar

Format B plus generic record runs, varint symbol ids, and a numeric-base-hint
sidecar. Projected file ≈ **~36–37 KB**; assembler-resident ≈ **~33 KB**. The
incremental ~2 KB over B comes at disproportionate Z80 complexity (varint id
decode in the overlay hot path, generic run dispatch). **Diminishing returns —
not recommended as a target**, documented to show the curve flattening.

### Summary of the curve

| | file size | vs today | assembler-resident | Z80 complexity | round-trip |
|---|---:|---:|---:|---|---|
| today compact | 51,117 | — | ~51 KB | baseline | full |
| **A minimal** | ~44,150 | −13.6% | ~44 KB | *simpler* (sym path) | better |
| **B recommended** | ~38,650 | −24.4% | **~34.5 KB (−32%)** | moderate | full (sidecars) |
| C maximal | ~36,500 | −28.6% | ~33 KB | high | full |

---

## 5. Recommendation

**Target Format B, reached in three phases — each independently shippable behind
the m6-release byte-match gate.**

The reasoning:

1. **The instruction overlay (3.2) is the spine and is non-negotiable.** It
   attacks the largest chunk, unifies LIT_INSTS with symbolic INST, and is
   *faster* to decode for the symbolic case because it precomputes the base word
   the encoder was re-deriving. It is also a faithful port of the encoder's own
   OR-in-slot inner loop (CLAUDE.md §6 — mechanical, not a design gamble).

2. **The biggest *resident* win is moving name strings + comments out of the
   assembler's paged-in set (3.6/3.7).** The assembler provably never needs name
   spellings or comment text. Paging means "out of the assembler's resident set"
   is achievable without losing round-trip — the editor/disassembler pages those
   groups in on demand. This is where the 512 KB framing pays off most: the
   constraint is *assembler-resident* bytes, and ~10 KB of the current file is
   data the assembler never touches.

3. **The header label table (3.4)** is the enabler for the single-long-run model
   and simplifies pass 1 — adopt it with the overlay.

4. **Bitfield packing (3.1)** is worth doing but only as polish on the overlay's
   slot bytes — it's the lowest leverage-per-complexity and the overlay already
   makes most symbolic operands vanish. **Do not lead with it.**

5. **Reject** entropy coding (3.8) and **don't bother** with fully generic
   runs-of-anything or varint ids (Format C) — the curve has flattened by then
   and the Z80 cost rises.

### Phased path

- **Phase 1 (Format A):** INSN_RUN with literal/overlay modes (3.2 + 3.3) +
  header delta-varint label table (3.4). Replaces LIT_INSTS *and* symbolic INST.
  Gate: m6-release byte-match + extended `disasm-roundtrip` (assemble→overlay→
  assemble byte-identical). Target ~44 KB. **Ship.**
- **Phase 2:** name-table front-coding + page-group split so the assembler
  doesn't map the name page; comments + `.global` → editor sidecar (3.5/3.6/3.7).
  Target ~38.6 KB file, ~34.5 KB assembler-resident. **Ship.**
- **Phase 3 (optional):** slot-byte bitfield packing (3.1). Small win; do it when
  touching the overlay decoder anyway. Skip Format C.

### Biggest risks

- **Byte-match fidelity of the slot/fold-rule enum.** If the overlay's
  fold-rules don't exactly mirror `pass2.go`'s per-slot PC conversions
  (`v - pc`, ADRP page-diff mask, imm scaling), output won't match GNU. Mitigate
  by *deriving* the slot table from the encoder's slot kinds and adding the
  round-trip property test as a required check.
- **Format ossification.** This redesign is a **version bump** (the reader
  validates `version == 1` in `reader.asm`). It's the right moment — no `.tbn`
  artefacts are shipped/persisted yet (per `mnemonics.go`'s stability note). Get
  the slot enum, directive ids, and run modes append-only-stable *now*.
- **Z80 code budget.** The assembler already lives under a tight `&C000` budget
  (M6 gate). The overlay decoder is *smaller* than the form-table path it
  replaces for symbolic INSTs, but the front-code/sidecar logic adds editor-side
  code. Keep editor-only decode paged out of the assembler's section budget.
- **STAGING_BUF splitting of runs.** The 1 KB cap means an INSN_RUN must split on
  whole-element boundaries; an overlay element is variable-length (base+patches),
  so the splitter must not cut mid-element. Mechanical but must be tested (mirror
  `litDataMaxBytes` whole-element discipline in `compact.go`).

---

## 6. Open questions for Pete — ✅ RESOLVED 2026-06-08 (see §7)

1. **Resident-vs-file priority.** The headline win (−32% *assembler-resident*)
   depends on paging name strings + comments out of the assembler's mapped set.
   Is "shrink what the assembler must hold" the true target (then sidecars win
   big), or do you also want the **on-disk file** as small as possible for
   transfer/storage (then front-coding/varints matter more)? They mostly align,
   but the sidecar split optimises the former specifically.
2. **Editor sidecar location.** Should comments/names/`.global`/base-hints live
   in the *same* `.tbn` (separate page-group the assembler skips) or a *separate
   companion file* the editor loads alongside? Same-file is simpler to keep in
   sync; companion-file makes "assemble-only" loads trivially smaller.
3. **Version bump scope.** Do i39 in one breaking v2 bump (cleanest, get the
   slot enum + run modes right once), or keep v1 readable for a transition? Given
   no `.tbn` is shipped yet, I recommend one clean v2 — confirm.
4. **Numeric-base / spelling hints.** How much round-trip fidelity do you want
   for *re-emitting* source — exact hex-vs-decimal, `.short` vs `.hword`,
   original whitespace? Each hint is bytes. The proposal keeps directive-spelling
   (already in LIT_DATA's dir_id) and puts base-hints in the editor sidecar;
   confirm that's the right fidelity bar.
5. **Local-symbol semantics.** Confirmed assumption: numeric locals (1f/1b) stay
   first-class (assembler needs forward/backward resolution); `.global` is
   round-trip-only and moves to the sidecar. OK to treat *all* named symbols as
   value/position entries the assembler resolves identically (no
   local/global behavioural distinction inside one file)?

---

## 7. Decisions (Pete, 2026-06-08) — Format B confirmed

The §6 questions are resolved as follows; **Format B is the agreed target.**

1. **Resident-vs-file priority → resident RAM is the driver; on-disk file size is
   secondary.** Pete's key refinement (the unlock): eviction is **conditional /
   last-resort**, not unconditional. When free RAM allows, the SAM **keeps the
   editor region resident** through the build — no disk round-trip, which is the
   preferable path. Only when free RAM would otherwise be insufficient does the
   SAM **persist the `.tbn` to disk, reuse the editor-region pages as OUT/scratch
   during the build, write the assembled binary, then reload the `.tbn`** to
   restore the editor view. So the editor-only content need not be a separate
   file or a permanently-unmapped page — it lives **inline in the one file** in a
   **contiguous, temporally-evictable region** that is swapped to disk *only if
   needed*. This reconciles "everything in one file" (Q2) with "minimise resident
   bytes" (Q1). **The split of work is sharp: i39b-2 ENABLES, i40 ENFORCES.**
   i39b-2 relocates the editor-only data (comments, name strings, `.global`
   flags) onto a separable trailing region bounded by a section index, so
   eviction *becomes possible*; it does **not** touch the loader and does **not**
   evict. i40 is the assembler-side mechanism that actually does the conditional
   eviction. The concrete justification is measured: the release's comment text
   alone is **335 KB** (`release.s`'s 7,502 `//` lines, ≈49% of the source), far
   too much to hold resident alongside a build — so the full un-stripping of all
   comments on the SAM-side m6 gate is **blocked on i40** (load only the
   assembler-facing prefix) or an IN-buffer expansion (which would defeat the
   resident goal). Tracked as a concrete future mechanism: **i40**.
2. **Editor sidecar location → one `.tbn` file** (no companion file). The
   editor-only content is a section *within* that file (the evictable region above).
3. **Version bump → clean breaking v2.** Pre-release; nothing is committed to v1,
   so get the slot enum / run modes / directive ids append-only-stable in v2.
4. **Numeric-base / spelling hints → editor region.** They don't affect assembled
   output, so they live in the evictable editor metadata, not the assembler stream.
5. **Local/global symbol semantics → uniform assembly + non-destructive
   preservation** (agent's call, Pete confirmed):
   - **Numeric locals (`1f`/`1b`) stay first-class** — a distinct *resolution*
     mechanism (nearest forward/backward), not a visibility attribute.
   - **All named symbols resolve identically** in the assembler — there is no
     linker in the single-file flat-binary path, so `.global` never changes a byte.
   - **`.global`/`.local` is preserved non-destructively** as a ~1-bit/symbol flag
     in the editor region (≪100 B for the release; not resident during a build), so
     SAM↔PC-binutils round-trips keep the visibility split. Best of both: uniform
     assembly, lossless source.

**Net agreed shape:** Format B — instruction overlay (assembled word + zeroed bits
+ expression overlay) unifying literal/symbolic instructions into one run; header
label/offset table; one `.tbn` v2 file with a contiguous **evictable editor region**
(comments, name strings, base hints, `.global` flags); name-table front-coding;
bitfield-packing as later polish. Projected ~38.6 KB file / ~34.5 KB
assembler-resident (−32% vs today's 51 KB), in 3 phases behind the m6-release gate.

**Next:** the Phase 1 implementation plan (instruction overlay + header label
table), to be written under `docs/plans/`.
