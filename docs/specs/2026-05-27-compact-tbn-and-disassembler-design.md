# Compact `.tbn` format + built-in disassembler

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
