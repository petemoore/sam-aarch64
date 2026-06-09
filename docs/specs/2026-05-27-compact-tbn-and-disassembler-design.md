# Compact `.tbn` format + built-in disassembler

> ⚠️ **Superseded baseline.** This note designed the **i1** compaction
> (`KindLitInsts` + `KindLitData`, shipped as PRs #121/#122/#124). The v2
> instruction-overlay format — which unifies literal and symbolic
> instructions into one `KindInsnRun` record — supersedes it. The current
> authoritative on-disk encoding is
> **`docs/specs/2026-06-08-tbn-binary-format-reference.md`** (v2); the v2
> design rationale is `docs/specs/2026-06-08-compact-tbn-nextgen-design.md`
> (i39) and `docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md`
> (i48). Retained as the i1 historical record.

**Status**: planning note, no code yet. Added 2026-05-27 to capture the design discussion before it's lost.

## Why this matters

The current `.tbn` format is ~20× the size of the assembled output (439 KB `.tbn` for spectrum4's 21,752-byte release.img). That's fine for release-on-Mac, but blocks the **debug build (~274 KB output, projected `.tbn` ~1.5 MB)** from ever fitting in the SAM's 512 KB RAM. Tests build is hopeless either way.

Three things drive the bloat in the current encoding:

1. **Comments preserved as full records.** Useful for round-trip, useless for the assembler.
2. **Symbol names stored as repeated strings** in every reference site.
3. **Numeric literals always 8 bytes** regardless of magnitude.

Easy fixes (strip comments, varint numbers, pool symbol names) get us to ~4–5× output size — release would shrink to ~100 KB. Pete's insight goes further: **for literal instructions (no expression operands), store the assembled 4 bytes directly**. The SAM assembler then memcpys those instructions to output with zero encoding work; only expression-bearing instructions need full record handling.

For spectrum4 release, where ~70–80% of instructions are fully literal, this puts the `.tbn` at ~30–60 KB — comfortably inside debug-build viability and well within the editor's working budget.

## The disassembler-as-side-effect

If literal instructions in `.tbn` are stored as their machine code, **the same data structure is both an assembly source AND a disassembly target**. Concretely:

- **Assemble**: walk records, copy the 4-byte machine code to output (or run the encoder for expression-bearing records).
- **Disassemble**: walk the same records, run the *inverse* of the encoder over each 4-byte literal to produce human-readable text.

This gives us, almost for free:

1. **Inspect any aarch64 binary** by importing the raw `.img` as a sequence of 4-byte literal records, then disassembling.
2. **Annotate a closed-source binary** by importing, disassembling, then editing in comments and label names — exactly the reverse-engineering workflow Pete called out.
3. **Cross-assembler import** — start from another aarch64 toolchain's output, disassemble into our `.tbn`, edit in our editor.
4. **Verify our own encoder** — disassemble our refenc output back to text, diff against the input source. Existing fixtures already cover the encode-side; this gives us the decode-side oracle.

The editor becomes a tool for working with *any* aarch64 binary, not just sources we ourselves authored.

## Proposed `.tbn` format extension

The current record kinds are:

```
KindInst       = 0x01  // mnemonic + operand list
KindLabelDef   = 0x02  // label_id + position
KindLocalDef   = 0x03  // digit + position
KindDirective  = 0x04  // directive_id + operand list
KindComment    = 0x05  // text
```

Add (or steal opcodes from the reserved range):

```
KindLitInst    = 0x06  // 4 bytes of machine code, nothing else
KindLitInsts   = 0x07  // count + (4 bytes) * count — runs of consecutive literal insts
```

`KindLitInsts` exploits the common case of multi-instruction sequences that are all literal (e.g. function epilogues like `ldp x29, x30, [sp], #16; ret`). One byte of overhead amortised across N instructions.

Layered on top: **a single-byte dictionary for the top-N most-frequent instructions**, configurable per project. spectrum4's hot list probably includes:

```
ret                          (0xd65f03c0)
nop                          (0xd503201f)
ldp x29, x30, [sp], #16      (0xa8c17bfd)
stp x29, x30, [sp, #-16]!    (0xa9bf7bfd)
add sp, sp, #16              (0x910043ff)
sub sp, sp, #16              (0xd10043ff)
mov x29, sp                  (0x910003fd)
```

Each fully literal at 4 bytes. A 16-entry dictionary makes each ~1 byte (4-bit dict index + escape). For corpora with thousands of such patterns, this saves tens of KB.

Encoded as:

```
KindDictInst   = 0x08  // 1 byte: dictionary index (0..127); MSB=1 reserved for escape
KindDictInsts  = 0x09  // count + (1 byte) * count — runs of dictionary-indexed insts
```

## Compaction levels

Make this a flag on the encoder, not a forced format change:

- **Level 0** — current bloated format. Keep for source-fidelity round-trip (preserves comments etc.). Used by `bin2text` to regenerate source verbatim.
- **Level 1** — strip comments, varint numeric literals, pool symbol names. ~100 KB for release. Compatible with full re-encode and disassembly.
- **Level 2** — also collapse literal instructions to `KindLitInst` / `KindLitInsts`. ~50 KB for release.
- **Level 3** — also use the per-project frequency dictionary. ~30 KB for release.

Each level is a superset of the previous in terms of what the decoder must handle. The on-SAM assembler implements through Level 3.

## Disassembler implementation outline

Per-instruction inverse encoder: given a 4-byte word, walk the form table by `Pattern & Mask` until a match, then unpack slots back into operand text.

```
input:  uint32 word
        []Form forms
        []string mnemonicNames
        operand-kind decoders (Xreg → "x{N}", Imm12Shifted → "#{V}", etc.)

algorithm:
  for each form in forms:
    if (word & form.Mask) == form.Pattern:
      mnemonic := mnemonicNames[form.MnemonicID]
      operands := []
      for each slot in form.Slots:
        bits := (word >> slot.BitPosition) & ((1 << slot.BitWidth) - 1)
        operands += decode(slot.SlotKind, bits)
      return mnemonic + " " + join(operands, ", ")
  return ".inst 0x" + hex(word)   // fallback: unrecognised
```

A few wrinkles to design through but nothing structural:

1. **Alias collapsing**. Multiple forms may match (e.g. `mov reg, reg` vs `orr reg, xzr, reg`). Disassembly should prefer the canonical alias. Solution: order `manualForms` after `generatedForms` in the lookup table, since manualForms contains the aliases (the form lookup change in PR #17 already does this). First-match wins yields the alias.
2. **Symbolic operands**. `ldr xN, =0x30d0088a` was originally a literal-pool reference; after assembly it's `ldr xN, [pc, #imm]`. The disassembler needs heuristics for "this is a pool ref" — recognising the LDR-literal pattern, following the offset, and decoding the pool entry. Optional in V1; the bare `ldr xN, [pc, #N]` is also valid output.
3. **Label recovery**. A branch's target is an absolute address; the disassembler emits `b 0xfffffff000001234` rather than `b some_label`. Label inference is a separate pass that scans for jump targets and emits `L_xxxx:` synthetic labels.
4. **Section / VMA recovery**. We don't have section info from a raw `.img` — the disassembler emits one big flat block. If the input was an ELF (not yet supported), section info propagates; for raw `.img`, the editor lets the user annotate boundaries.

## Editor integration

For the on-SAM editor's "Import Binary" feature:

```
1. Open binary file via SAMDOS HGFLE.
2. Read all bytes into a `.tbn` buffer as a sequence of KindLitInst records.
3. Run the disassembler pass to produce viewable text.
4. User edits text; their edits become regular records (mnemonic + operands).
5. On save, re-encode all records (literal and expression-bearing) into a Level 2/3 `.tbn`.
```

This makes the editor a general-purpose aarch64 binary editor, not just our own toolchain's frontend. Closed-source SAM kernels, third-party binaries, foreign assembler output — all become editable inputs.

## Plan / ordering

Not on the critical path; deferred until M3 Tasks 16–22 complete (the SAM-side assembler reading the *current* `.tbn` format). Once that works, add compaction as an optimisation:

1. **First**: complete M3 Tasks 16-22 against current `.tbn`. Get on-SAM assembly working end-to-end for release.
2. **Then**: profile actual `.tbn` size growth on real workloads. Confirm the bottleneck is what we think it is.
3. **Level 1 (strip + varint + symbol pool)**. Should be straightforward; mostly encoder changes. Adds maybe 200–300 lines of Go and equivalent Z80.
4. **Level 2 (literal instruction encoding)**. New record kinds; encoder logic to detect "is this fully literal?" and emit accordingly; decoder logic to dispatch on the new kinds.
5. **Disassembler**. Build the inverse-encoder library in Go first (uses the existing `aarch64enc` form table; symmetric inverse). Port to Z80 once stable. The disassembler dovetails into editor "Import Binary" feature.
6. **Level 3 (frequency dictionary)**. Profile spectrum4-debug to pick the dictionary; add the new record kinds; measure size reduction.

Each level is incrementally shippable.

## Open questions for future-you

1. **Is the frequency dictionary per-project, or static for spectrum4?** Per-project gives best compression but adds a "dictionary" file. Static is simpler but locks in spectrum4's particular hot list. Suggestion: static for V1; add per-project later if measurement shows it's worth it.
2. **What happens to comments in Level 2/3?** Three options: (a) drop entirely (most compact), (b) keep in a sidecar `.tbn.comments` file, (c) stream-of-comments with byte offsets back to the main `.tbn`. Suggestion: drop in Level 2 (M3 assembly path), keep in Level 1 for source-fidelity. The editor stores its own annotation layer separately.
3. **How does `bin2text` interact with Level 2/3?** Round-trip fidelity needs the comments. `bin2text` should error if asked to detokenise a Level ≥ 2 `.tbn` and the comments aren't available, OR emit best-effort with placeholder comments.
4. **What's the cost of disassembler form-lookup on Z80?** Linear scan through ~130 forms per instruction is ~130 word-comparisons. At ~50 instructions per second through the disassembler that's tolerable; if it becomes a bottleneck, add a hashed lookup index.

## Related notes

- `docs/notes/m2-status.md` — current encoder coverage, the executable spec the disassembler inverts.
- `docs/notes/sam-stub-audit.md` — SAMDOS hook semantics, relevant for "Import Binary" file IO.
- `docs/specs/2026-05-27-samdos-load-idiom.md` — load-into-arbitrary-memory trampoline pattern, relevant for paged source storage.

---

## 2026-05-29 refinement — implementation design (Level 2, Pete's priority)

> **Sequencing note (Pete 2026-05-29, after this section was first drafted):**
> the **standalone Go disassembler is built FIRST**, before this compact-`.tbn`
> format change. A bytes-based `.tbn` is write-only to the editor without a
> disassembler (bytes→text), and decoupling avoids changing the format +
> wiring the assembler in one risky step. Treat this section as the design for
> the *eventual target*; the active work is
> `docs/plans/2026-05-28-go-aarch64-disassembler.md`. See
> `memory/feedback_disassembler_first_decouple` and m7-status open-Q7.

This section refines the plan above for the M7 implementation. It supersedes
the ordering in "Plan / ordering" where they differ. Grounded in a fresh
code read of the toolchain (citations inline). Pete's framing: *make the
internal representation the assembled bytes for expression-free
instructions* (hybrid — expression-bearing instructions keep their symbolic
record for 2-pass resolution). Goal: relieve the IN-buffer ceiling
(currently 92% of 96 KB for release-stripped) and get most of the way to an
on-SAM disassembler (bytes→text is the inverse of this encode).

### Key architectural finding (reshapes the producer side)

`text2bin` does **not** encode instructions — it tokenises symbolically.
The single instruction-encoding authority is `refenc`'s `encodeInst()`
(`tools/refenc/pass2.go:176`, ~160 lines: operand parse → compound-operand
dispatch / form lookup → `aarch64enc.Encode`, `tools/aarch64enc/encode.go:9`).
`text2bin` does not import `aarch64enc` at all.

**Decision: the compaction lives in `refenc`, not `text2bin`.** refenc
already encodes every instruction during pass-2, so it is the natural place
to collapse fully-literal instructions to their bytes — as a **`.tbn` → `.tbn`
transform** that reuses the one encoder. This avoids duplicating ~160 lines
of encoder into text2bin (which would be a second source of truth — exactly
the hand-sync drift the sysreg guard / codegen strand fights). The pipeline
becomes:

```
text2bin  →  symbolic .tbn  →  refenc -emit-compact-tbn  →  compact .tbn
                                              │
                                              └─ (also still emits the binary, unchanged)
```

Both the Go decoder (refenc reading the compact `.tbn`) and the SAM decoder
then consume the compact form.

### Format (Level 2): one new record kind

Add **`KindLitInsts = 0x07`** — a *run* of consecutive fully-literal
instructions:

```
[kind:1 = 0x07][payloadLen:2 LE][count:1][word0:4 LE][word1:4 LE]…[word{count-1}:4 LE]
```

`payloadLen = 1 + 4*count`; `count` ∈ 1..255 (split longer runs into
successive records). Per-record framing is 3 bytes (`kind` + 2-byte `len`),
so the run form is what actually pays off: per-instruction cost is
`(4 + 4R)/R = 4 + 4/R` bytes for a run of length `R` (≈4.4–4.8 B/inst),
versus ~8–14 B for the symbolic `KindInst`. A single literal instruction is
just a run of `count=1`.

> We deliberately do **not** add the spec's separate `KindLitInst = 0x06`
> (single). A `count=1` run costs one extra byte vs a bare single; not worth
> a second kind + second decode path on the Z80 side. `0x06` stays reserved
> if measurement later shows singles dominate.

PC accounting is preserved exactly: a run occupies `4*count` bytes, identical
to `count` separate `KindInst`s, so label positions and the 2-pass values for
expression-bearing instructions are unchanged. pass-1 sizing is trivial
(`pc += 4*count`); pass-2 is a `memcpy` of `4*count` bytes — **zero encoding
work on the SAM**, which is the headline win.

### "Fully literal" determination

A `KindInst` is fully literal iff its encoding depends on neither the PC nor
the symbol table — i.e. none of its operands carry a symbol ref, local-label
ref, PC ref, relocation operator (`:lo12:` etc.), or literal-pool (`=imm`)
ref. Implement `format.IsFullyLiteral(rec)` by scanning the operand stream
and the embedded expression bytecode for the context-dependent opcodes
(`OpPushSym` / `OpPushLocal` / `OpPushPC` / the `Rel*` operators / `OpLitPool`
— verify exact names in `tools/sam-aarch64-format/`). `OpMem`/`OpShiftedReg`/
`OpExtendedReg` are literal only if their offset/amount sub-expressions are
constant. When in doubt, classify as *not* literal (falls back to the
symbolic path — always correct, just less compact). The gate (below) proves
no false-positive ever produces wrong bytes.

### Verification — the m6-release gate is the oracle

`tools/run-m6-release-gate.sh` already does a hermetic 3-way byte-match
(GNU `release.img` == Go refenc == Z80/SAM) over the vendored flattened
release source. Compaction slots straight in:

1. **Go side (PR 1):** produce `compact.tbn` via `refenc -emit-compact-tbn`,
   then assert `refenc compact.tbn` → img **== the vendored GNU release.img**
   (and == the symbolic-path img). If the compact `.tbn` assembles to the
   identical release binary, the literal-collapse is provably correct. Also
   log `compact.tbn` size vs `symbolic.tbn` size (the compression number).
2. **SAM side (PR 2):** once the Z80 decoder lands, feed the SAM the
   `compact.tbn` and assert its OUT still == release.img.

This makes the compression self-policing: any encoder bug or false-positive
literal classification fails the gate.

### Increment plan

- **PR 1 — Go side, no Z80 (self-contained, de-risks the format):**
  `format` gains `KindLitInsts` + reader/writer + `IsFullyLiteral`; `refenc`
  gains `-emit-compact-tbn <path>` (the compaction pass) and the decode path
  (pass-1 `pc += 4*count`, pass-2 memcpy); a Go unit test (TDD) on a small
  literal+symbolic fixture; the m6-release gate extended to build + verify the
  compact `.tbn` and print the size delta. Deliverable: a measured release
  compression ratio + a standing gate guard, zero Z80 risk.
- **PR 2 — SAM/Z80 decoder:** add the `REC_KIND_LIT_INSTS` dispatch in
  `src/main_loop.asm` (after the COMMENT case, ~`:442`); pass-1 sizing +
  pass-2 memcpy-to-OUT handlers; switch the SAM side of the gate to consume
  the compact `.tbn`. Re-measure the IN-buffer headroom (the 92% → ?).
- **PR 3 (constant data runs) — `KindLitData = 0x08` — ✅ DONE (PR #124):** a
  *separate* record kind for runs of consecutive **same-directive, all-constant**
  numeric data (`.byte`/`.short`/`.hword`/`.word`/`.quad`), stored as raw
  assembled bytes: `[kind 0x08][len u16][directive_id u8][raw LE bytes…]`. The
  leading `directive_id` byte **preserves which directive the author wrote**
  (Pete, 2026-06-08) — a `.hword` table and a `.word` table with identical bytes
  must stay distinguishable so the disassembler round-trips the source
  spelling — so only same-`directive_id` runs merge, and symbol-bearing data
  (`.quad label`) stays a symbolic `DIRECTIVE`. **Measured (PR #124):** the
  1,745 collapsible numeric records (21,892 B / 31.8% of the 68,755 B PR1/PR2
  file, assembling to 4,046 B) collapse the compact `.tbn` to **51,117 B —
  −42.3% vs the 88,644 B symbolic**, −25.7% vs PR1/PR2, **5 IN pages → 4**,
  byte-identical to GNU. Records split at 1016 B for the Z80 `STAGING_BUF`. Full
  record layout in `docs/specs/2026-06-08-tbn-binary-format-reference.md` §7.3.
- **PR 4+ (future):** Level 3 per-project frequency dictionary; the
  disassembler (bytes→text) inverts exactly this encode.

### Open questions for Pete (tracked; not blocking — chosen defaults noted)

1. **Frequency dictionary (Level 3) this milestone?** Default: **defer** —
   Level 2 alone should clear the ceiling; the dictionary adds a per-project
   artifact + a 4th level of decoder complexity for diminishing returns.
   Revisit if Level 2's measured ratio is insufficient for the debug build.
2. **Compact constant *data* directives too (`.word`/`.byte` runs)?**
   **RESOLVED → yes, as PR 3 (Pete 2026-06-08).** PR 1/2 shipped
   instructions-only (Pete's framing); PR 3 adds `KindLitData = 0x08`, a
   raw-bytes data run that **preserves the source directive** via a leading
   `directive_id` byte (Pete's explicit requirement). Measurement showed this
   is *not* a smaller win — collapsible numeric data is 31.8% of the compact
   file (~5× bloat today); PR #124 measured **−17,638 B** (68,755 → 51,117).
   See the PR 3 bullet above + the format reference §7.3.
3. **Where should the user-facing compaction flag live long-term?** Default:
   `refenc -emit-compact-tbn` for now (least code, reuses the encoder). If a
   cleaner CLI surface is wanted later (e.g. `text2bin -compact` that links a
   shared encoder package), that's a refactor once the format is proven.
