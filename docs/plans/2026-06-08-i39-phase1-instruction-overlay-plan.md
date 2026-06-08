# i39 Phase 1 — instruction overlay + header label table (Format A) implementation plan

**Date:** 2026-06-08 · **Item:** i39a (i39 Phase 1 / Format A) · **Milestone:** M8 · **Type:** implementation plan (execute PR-by-PR)

**Authority:** the code, then the agreed design. Grounding reads done for this plan:
`docs/specs/2026-06-08-compact-tbn-nextgen-design.md` (§3.2/§3.3/§3.4, §5 Phase 1 = Format A,
§7 Pete's decisions: Format B target, **clean breaking v2**, one file, evictable editor region,
uniform symbol resolution, numeric locals first-class, `.global` preserved non-destructively);
`docs/specs/2026-06-08-tbn-binary-format-reference.md` (the v1 encoding this evolves);
`tools/sam-aarch64-format/{kinds,format,reader,writer,litinsts,operands,expr,symbols}.go`;
`tools/refenc/{compact,pass1,pass2}.go`; `tools/aarch64enc/{types,encode,slots_branch,slots_adrp,data}.go`;
`tools/aarch64dec/disasm.go`; `src/{main_loop,reader,encoder,expr_eval,symbols}.asm`;
`tools/run-m6-release-gate.sh`, `tools/run-disasm-roundtrip.sh`, `tools/revendor-m6-release.sh`.

This is a planning document only — **no implementation here.** Another agent (or this one) executes
it PR-by-PR with the m6-release 3-way byte-match gate proving correctness at each step.

---

## 1. Scope of Phase 1

Phase 1 implements **Format A** from the design §5:

1. **The instruction overlay (§3.2 + §3.3).** Every instruction — fully-literal *or*
   symbol/PC/reloc-bearing — becomes one element type inside a single **instruction-run** record
   kind. A literal instruction is its 4-byte assembled word (today's `LIT_INSTS` floor, preserved);
   a symbol-bearing instruction is its assembled word **with the relocated bitfield(s) zeroed**, plus
   a compact overlay = `{slot, expression-bytecode}` per patched field. Pass 2 resolves each patch
   expression, applies the slot's fold-rule (the PC conversion the encoder already does), and ORs the
   result into the zeroed bit-range. This **unifies `KindLitInsts` + symbolic `KindInst`** into one run
   and is the byte-match-critical centrepiece.

2. **The header label/offset table (§3.4).** Labels (`LABEL_DEF`) and numeric-local defs (`LOCAL_DEF`)
   move out of the record stream into a **header table** mapping symbol-id/digit → byte-offset
   (= PC from origin), stored as delta-varint offsets. This stops labels from splitting the instruction
   run and makes pass 1 read the symbol/local tables up front instead of dispatching mid-walk.

**Explicitly OUT of Phase 1** (Phase 2/3, per §5):
- Name-table front-coding and paging the name strings out of the assembler's mapped set (Phase 2).
- Comment / `.global` / numeric-base editor sidecars, and the contiguous evictable editor region (Phase 2 / i40).
- Bitfield-packing of the overlay's slot bytes (Phase 3).
- Format C extras (generic runs-of-anything, varint symbol ids).

Phase-1 target file size (design §4 Format A table): ~44 KB on the vendored release (−13.6% vs today's 51,117 B).

---

## 2. The v2 format delta (concrete layouts)

This is a **clean breaking v2** (§7 decision 3): nothing persists v1 (`.tbn` lives only in `build/`/`/tmp/`),
so the version constant flips `1 → 2` and v1 record kinds are removed, not coexisted. There is **no v1/v2
dual-read** in the format package — the reader rejects `version != 2`. (refenc's *transition* strategy —
whether the compactor can emit both during development — is a build-flag question handled per-PR in §7, not a
format-package concern.)

### 2.1 File container (v2)

```
Magic    "SA64"     4 bytes  (unchanged)
Version  u16 LE     = 2                        format.go:Version 1→2
Flags    u16 LE     = 0  (reserved)
Header label/offset table  (NEW — §2.4)        ; sits AFTER the name table
Name table                 (unchanged in Phase 1; front-coded in Phase 2)
Record stream
```

Placement note: the label table goes **after** the name table because a label row references a
`name_id` into the name table, and the editor/disassembler wants both present before walking records.
The assembler reads the label table at pass-1 start (§5) and never needs the name *strings* for
assembly — that decoupling is what Phase 2 exploits, so locating the label table here (not interleaved
with names) keeps the Phase-2 page-group split clean.

### 2.2 Record kinds in v2

| Hex | v1 kind | v2 disposition |
|-----|---------|----------------|
| `0x01` | `INST` (symbolic) | **REMOVED** — folded into `INSN_RUN` overlay elements |
| `0x02` | `LABEL_DEF` | **REMOVED** — moved to header label table (§2.4) |
| `0x03` | `LOCAL_DEF` | **REMOVED** — moved to header local-label table (§2.4) |
| `0x04` | `DIRECTIVE` | **RETAINED** unchanged (`.equ`/`.balign`/`.ascii`/`.quad`-with-symbol/…) |
| `0x05` | `COMMENT` | **RETAINED** in Phase 1 (moves to editor sidecar in Phase 2) |
| `0x07` | `LIT_INSTS` | **REMOVED** — subsumed by `INSN_RUN` mode 0 |
| `0x08` | `LIT_DATA` | **RETAINED** unchanged (Phase 1 does not touch data runs) |
| `0x09` | `INSN_RUN` | **NEW** (§2.3) |

`0x06` stays reserved. `0x09` is the next free kind; per the format reference's append-only rule it takes
the next integer, but since this is a breaking v2 we may equally renumber — leaving the retained kinds at
their v1 values (`0x04`,`0x05`,`0x08`) minimises Z80 dispatch churn, so **keep retained kinds' bytes and add
`INSN_RUN = 0x09`**.

### 2.3 `INSN_RUN` (0x09) — the unified instruction run

One frame per run (≤ ~1 KB STAGING_BUF chunk), a mode byte, then a sequence of elements. Two modes
(design §3.3) so the literal-only floor of 4 B/instruction is preserved exactly:

```
INSN_RUN:
  [kind 0x09][len u16][mode u8][payload …]

  mode 0  "all literal":   payload = packed 4-byte words.  element = [word u32 LE]   (4 B, == v1 LIT_INSTS)
  mode 1  "overlayed":     payload = variable-length elements:
            element:
              [base_word u32 LE]      ; assembled word, relocated bitfields ZEROED
              [patch_count u8]        ; 0 = literal-in-an-overlay-run, 1 typical, 2 rare (movk #imm+hw, adrp pairs are separate insns)
              patch[0 .. patch_count-1]:
                [slot u8]             ; slot/fold enum id (§3) — names (lsb,width,fold-rule)
                [expr_len u8][expr bytes]   ; the existing expression bytecode, verbatim (expr.go)
```

Notes:
- `expr_len` is **u8** (was u16 for v1 operand exprs). Release expressions are tiny (a `PUSH_SYM id16`
  is 3 bytes; `+REL_LO12` etc. 4–6). A u8 cap of 255 is ample and saves 1 B/patch. (If a future fixture
  needs >255-byte expressions, that is a v2-append decision — flag, don't silently widen.)
- A `mode 1` element with `patch_count = 0` is a literal instruction carried in an overlay run (5 B). The
  compactor (§4) uses mode 0 for maximal literal stretches (4 B floor) and mode 1 only where symbolic
  instructions live, absorbing short literal gaps between them when `[u32][00]` (5 B) beats closing+reopening
  a mode-0 sub-run (4 B frame). This is a local optimisation in `compact.go`.
- **STAGING_BUF discipline:** mode-1 elements are variable-length, so a run must split on **whole-element**
  boundaries — never mid-element (mirrors `litDataMaxBytes` whole-element discipline). The splitter caps a
  run's payload at ≤ ~1016 B (same headroom as `litDataMaxBytes`) and only closes a run between elements.

### 2.4 Header label/offset table (delta-varint)

```
Label table (global named labels):
  [count u16]
  rows in increasing-PC order:
    [name_id u16][offset_delta varint]     ; offset = prev_offset + delta; first row delta is from origin (0)
Local-label table (numeric 1f/1b def sites):
  [count u16]
  rows in increasing-PC order:
    [digit u8][offset_delta varint]        ; per-site rows; same digit may repeat (multiple def sites)
```

- **Offsets are byte offsets from origin (= PC from origin)**, identical to the values pass 1 currently
  computes when it inserts `(symbol_id, PASS_PC)` / appends `PASS_PC` to a digit list.
- **Delta-varint:** rows are emitted in increasing-PC order, so consecutive deltas are small; LEB128-style
  unsigned varint (1 byte for deltas <128) cuts the offset column from 4 B to ~1.5 B avg → label table
  ~2.3 KB → ~1.4 KB (design §3.4, −0.7 KB).
- **Local-label rows are one-per-def-site** so the pass-2 forward/backward (`Nf`/`Nb`) resolution still has
  the full ordered PC list per digit (design §3.5 keeps numeric locals first-class).
- **`.equ`/`.set` value-symbols do NOT go here** — they are *values*, not positions, and stay as `DIRECTIVE`
  records resolved in pass 1 (`resolveEquDirective`). Don't conflate position-labels with value-symbols.

---

## 3. The slot / fold-rule enum (byte-match-critical)

This enum is **the** correctness hinge. Output byte-matches GNU iff each overlay slot's fold-rule
*exactly* mirrors the PC conversion `pass2.go` already performs for that field. The enum is derived
mechanically from the encoder's existing `SlotKind`s (`tools/aarch64enc/types.go`) and the per-slot PC
conversions in `operandsToValues` / the hand-rolled mem/branch/litpool encoders.

The overlay slot enum entries — each = `(lsb, width, fold-rule)`. `lsb`/`width` are the bit-range the
resolved value ORs into; the fold-rule is the value transform applied before masking. **All fold math
already exists in `pass2.go`; the overlay reuses it verbatim.**

| overlay slot | lsb | width | fold-rule (exactly as pass2.go does it) | source |
|---|---:|---:|---|---|
| `SLOT_BRANCH26` | 0 | 26 | `(target − pc) / 4`, align-check %4, range ±2^25 instr | `encodeBranchImm` (BranchImm26) |
| `SLOT_BRANCH19` | 5 | 19 | `(target − pc) / 4`, %4, ±2^18 | `encodeBranchImm` (BranchImm19); also `encodeLdrLitDirect` imm19 |
| `SLOT_BRANCH14` | 5 | 14 | `(target − pc) / 4`, %4, ±2^13 | `encodeTbzTbnz` imm14 |
| `SLOT_ADRP` | (immlo@29:2, immhi@5:19) | 21 split | `diff = (target&~0xFFF) − (pc&~0xFFF)`; mask to 33 bits sign-extended; `/4096`; split immlo/immhi | `operandsToValues` AdrpImm + `encodeAdrpImm` |
| `SLOT_ADR` | (immlo@29:2, immhi@5:19) | 21 split | `(target − pc)`; split immlo/immhi (no /4096) | `operandsToValues` AdrImm + `encodeAdrImm` |
| `SLOT_LOGICAL` | 10 | 13 | encode value as N:immr:imms logical-immediate (is64 from bit31) | `encodeLogicalImm` (LogicalImm) — used by orr-imm, bic-imm |
| `SLOT_MEM_IMM12` | 10 | 12 | `byteOffset / scale` (scale from Rt width), unsigned, %scale, <2^12 | `encodeMemInst` MemBaseOff |
| `SLOT_MEM_IMM9` | 12 | 9 | `encodeSignedImm9(byteOffset)` (pre/post-index, stur/ldur) | `encodeUnscaledMemInst` / pre/post |
| `SLOT_MOVK_IMM16` | 5 | 16 | low-16 of value at the element's hw shift; hw selected as in `encodeImm16Shifted` | `Imm16Shifted` slot |
| `SLOT_LITPOOL_IMM19` | 5 | 19 | `(poolEntry.PC − pc) / 4` (pool slot resolved by pass1 `LdrPoolIdx`) | `encodeLdrLitPoolInst` |

Register positions (`Rt`/`Rn`/`Rm` at bits 0/5/16) and condition codes are **always literal in the base
word** — they never carry a symbol/expression, so they are baked into `base_word` and need no slot. This
is why most symbolic instructions have `patch_count = 1` (one immediate field) — the registers are already
in the zeroed-base word.

**Derivation rule for the executor:** to build the enum, walk every slot kind in `aarch64enc/types.go`
that an expression-bearing operand can land in (the `Imm*`/`Branch*`/`Adrp`/`Adr`/`Logical` families), plus
the four hand-rolled families (mem imm12/imm9, litpool imm19, movk hw). Each maps to one enum entry whose
fold-rule copies the corresponding `pass2.go` conversion line-for-line. **A round-trip property test (§6) is
the guard that the enum is exhaustive and faithful** — if a fold-rule diverges by one bit, the m6-release
gate goes red.

Two subtleties to bake into the enum design now (append-only-stable per §7 decision 3):
- **ADRP/ADR split layout** (immlo@29:2 + immhi@5:19) is *not* a contiguous `(lsb,width)`. Represent these
  slots with their own enum id whose decoder/encoder knows the split (as `encodeAdrpImm` already does — it
  ignores `slot.BitPosition`). Do not try to force them into the contiguous-range mechanism.
- **The base word already carries the size/opc/option bits** for mem and logical forms (they come from the
  Rt width and mnemonic, both literal). So `SLOT_MEM_IMM12`'s fold only needs `scale`, which is recoverable
  from the base word's size field — but simpler and safer is to **store scale implicitly via the base word**
  (the overlay encoder computes the base word with size bits set, zeroes only imm12, and the fold reads
  scale from the size field). Decide this in PR(a) and pin it with a test.

---

## 4. Go-side work

Files/functions touched, in dependency order. (PR grouping in §7.)

### 4.1 Format package (`tools/sam-aarch64-format/`)
- `format.go`: `Version 1 → 2`.
- `kinds.go`: remove `KindInst`/`KindLabelDef`/`KindLocalDef`/`KindLitInsts` constants and `Name()` arms;
  add `KindInsnRun = 0x09`. Keep `KindDirective`/`KindComment`/`KindLitData`.
- `reader.go`: `ReadFile` — accept `version == 2`; parse the header label table + local-label table
  (delta-varint accumulate) into `File.Labels []LabelRow{NameID, Offset}` and `File.LocalDefs map[byte][]int64`.
  `RecordReader.Next` — add an `INSN_RUN` arm decoding `mode` + the element list into a typed
  `Record{Kind: KindInsnRun, Mode, Elements []InsnElement}` where `InsnElement{BaseWord uint32, Patches []Patch{Slot, Expr}}`.
- `writer.go`: `WriteFile` — emit the header label/local tables (varint). Add `WriteInsnRun(mode, elements)`
  and a varint helper. Remove `WriteInst`/`WriteLabelDef`/`WriteLocalDef`/`WriteLitInsts`.
- New file `overlay.go` (or extend `litinsts.go`): the **slot/fold enum** (§3) as Go constants +
  a `FoldSlot(slot, value, pc) (bits uint32, err)` that mirrors `pass2.go` (shared with refenc — see 4.2),
  and `ZeroSlot(baseWord, slot) uint32` to clear a slot's bit-range. Keep the enum here so both refenc
  (emit/decode) and aarch64dec (round-trip) import one source of truth.

### 4.2 refenc compaction — emit overlay (`tools/refenc/compact.go`)
- Rework `Compact`: replace the `instRun []uint32` accumulator with an instruction-run accumulator that
  holds *elements* (literal word OR base+patches). For each `KindInst` record:
  - Compute the **base word with relocated slots zeroed and the patch captured**. This is the key reuse:
    `encodeInst` already ORs slot values into a base pattern. Add an `encodeInstOverlay(rec, pc, …)` that runs
    the same dispatch but, for each expression-bearing slot, (a) computes the literal base contribution of
    every *other* slot, (b) leaves the symbolic slot's bits zero, and (c) records `Patch{slot-enum, exprBytes}`.
    The cleanest shape: have `encodeInst`'s slot loop, when the operand carries a context-dependent expression
    (`exprHasContextDep`), emit a zero into that slot and append a patch instead of folding. A fully-literal
    instruction yields `patch_count = 0` (and the PC-invariance guard from `literalWord` still decides mode 0
    vs mode 1 placement).
  - The fold-rule the overlay records must be **the same conversion** `operandsToValues`/the mem/branch/litpool
    encoders apply — factor those conversions into `format.FoldSlot` (4.1) and call it from both the literal
    encode path (pass2) and the overlay decode path, so they cannot drift.
- Run-mode selection + STAGING_BUF split: emit mode-0 sub-runs for maximal literal stretches; open a mode-1
  sub-run for symbolic instructions, absorbing short literal gaps as `[u32][00]`; split any run on whole-element
  boundaries at the ~1016 B cap.
- `pass1.go`: stop relying on `LABEL_DEF`/`LOCAL_DEF` records mid-stream — populate `res.Symbols` /
  `res.LocalDefs` from `File.Labels` / `File.LocalDefs` at pass-1 start; the record walk only advances PC
  (`INSN_RUN` contributes `4 × element_count` for both modes; `LIT_DATA`/`DIRECTIVE` unchanged).

### 4.3 refenc decode — assemble the overlay (`tools/refenc/pass2.go`)
- Pass 1 sizing: `INSN_RUN` advances PC by `4 × element_count`.
- Pass 2: for each `INSN_RUN` element: copy `base_word`; for each patch, `EvalUsage(patch.Expr, ctx)`
  → `format.FoldSlot(patch.Slot, value, pc)` → `base_word |= bits`; emit 4 bytes LE; `pc += 4`. Literal
  elements (mode 0 or `patch_count = 0`) emit `base_word` directly. **This replaces the per-form
  `encodeInst` dispatch in pass 2 for run elements** — the form table is consulted only at *compaction*
  time (overlay emission), not at assemble time.

### 4.4 Header label table build/consume
- Build (compaction, `compact.go` or a header-emit step): after pass 1, emit label rows in increasing-PC
  order from `res.Symbols` (filtered to position-labels, not `.equ` values) and local rows from
  `res.LocalDefs`, delta-varint encoded.
- Consume: `ReadFile` (4.1) parses them; `Pass1` seeds the tables from them.

### 4.5 Disassembler round-trip (`tools/aarch64dec/`)
- `DecodeAt` is unchanged for a plain word. Add an overlay-aware path used by the round-trip tool / editor:
  given `(base_word, patches[])`, disassemble `base_word`, then for each patch **suppress the zeroed field's
  literal rendering and substitute the symbolic operand**: render the label name (via the name table) /
  `:lo12:sym` (from the slot's reloc fold-rule) / pool `=expr`. The slot enum tells the disassembler *which*
  field to suppress (it must never render the zeroed bits literally). Local labels render `1:`/`1f`/`1b` from
  the local table exactly as today.
- The round-trip gate (`run-disasm-roundtrip.sh`) extends to: assemble→overlay (compact v2)→disassemble→
  re-assemble, assert byte-identical (§6).

---

## 5. Z80-side work

The overlay decoder **replaces** the heavier form-table symbolic path (`main_handle_inst` pass-2 body:
operand parse loop → form lookup → `encode_inst`). For symbol-bearing instructions it is *less* code and
*fewer* T-states (design §3.2): copy base word, eval patch expr (the evaluator already exists), fold, OR, emit.

### 5.1 `INSN_RUN` decode (`src/main_loop.asm`)
- **Dispatch:** add `REC_KIND_INSN_RUN = &09` to `walk_records`. Remove the `REC_KIND_INST` /
  `REC_KIND_LABEL_DEF` / `REC_KIND_LOCAL_DEF` / `REC_KIND_LIT_INSTS` arms (folded/moved). `LIT_DATA` keeps
  its memcpy handler; the unified `{LIT_INSTS,LIT_DATA}` range-check (`sub REC_KIND_LIT_INSTS`) collapses to
  just `LIT_DATA`.
- **Pass 1:** walk the run's elements advancing `PASS_PC += 4` per element (mode 0: `len/4` words; mode 1:
  one advance per element, stepping over `patch_count` + each `[slot][expr_len][expr]`). No expression eval
  in pass 1 (matches `main_handle_inst_pass1`). Litpool scanning that `main_handle_inst_pass1` did via
  `litpool_scan_inst_record` must still happen — but litpool registration is keyed by PC and the `=expr`
  now arrives as a `SLOT_LITPOOL_IMM19` patch; pass-1 litpool registration moves to "when an element carries
  a litpool-slot patch." (Confirm against `litpool.asm` during PR(c).)
- **Pass 2, mode 0 (literal):** identical to today's `main_handle_lit_insts` — memcpy the words to OUT.
- **Pass 2, mode 1 element:** copy `base_word` (4 bytes) into a scratch word; `ld` `patch_count`; for each
  patch: read `slot`, read `expr_len`+bytes, call the existing context-aware evaluator
  (`expr_eval.asm::eval_expr` — already handles PUSH_SYM/PUSH_PC/PUSH_LOCAL/REL_LO12/…), then a per-slot
  **fold routine** (a small jump table on `slot`: subtract-PC + `/4` for branches, page-diff mask for ADRP,
  `encodeSignedImm9` for imm9, scale-divide for imm12, logical-immediate encode for LOGICAL, hw-shift for
  MOVK, pool-PC diff for LITPOOL) producing the masked bits, then OR into the scratch word's bit-range; after
  all patches, emit the 4 bytes via the existing emit path. The fold routines are direct ports of the
  `pass2.go` conversions (CLAUDE.md §6 — mechanical, known answer).

### 5.2 Header label table load (`src/main_loop.asm` + `src/symbols.asm` + `src/reader.asm`)
- At **pass-1 start**, before walking records, read the header label table into the existing symbol table
  (`symbol_insert` / the structures `symbol_lookup` reads) and the local-label table into the per-digit PC
  lists. The offsets are absolute byte offsets from origin = the same value `copy_pass_pc_to_symbol_value_buf`
  would have stored. Varint-decode + delta-accumulate sequentially (one upfront pass; removes per-record
  `LABEL_DEF`/`LOCAL_DEF` dispatch from the walk loop). The reader needs a small "read header table region"
  entry alongside `reader_next_kind` (the name-table skip logic in `reader.asm` is the model).

### 5.3 STAGING_BUF run-splitting on whole-element boundaries
- The reader stages each record's payload into the 1024-byte `STAGING_BUF` (`reader.asm` tag 01 on overflow).
  The compactor guarantees each `INSN_RUN` payload ≤ ~1016 B and split only between whole elements — so the
  Z80 decoder always sees complete elements. No mid-element handling needed on the Z80 (the discipline lives
  in `compact.go`, mirrored from `litDataMaxBytes`). The Z80 element loop must, however, not read past
  `payload_len` (BC) — bound the per-element walk by BC, same as the existing handlers.

### 5.4 Z80 code-budget angle (`&C000` test-variant budget)
- The overlay decoder *replaces* the form-table symbolic path, so it should be **net smaller**. But this is a
  claim to **measure**, not assume: run `make check-budget` (the `&C000` assertion in the m6-release gate's
  step 1) after the Z80 PR and confirm the budget holds. The new fold jump-table is the main addition; the
  deleted form-dispatch + operand-parse loop (`main_handle_inst_parse_loop` and its `main_parse_*` arms) is
  the main saving. If the budget regresses, the parse-loop deletion should more than cover it — investigate
  before relaxing anything (do **not** ratchet the budget; CLAUDE.md §5).

---

## 6. Tests + gates

**Hard constraint: the m6-release 3-way byte-match gate stays green at every shippable PR.** That gate
(`run-m6-release-gate.sh`) regenerates `.tbn` and compact `.tbn` from `tests/m6/release/release.s` each run
and byte-compares GNU == Go(refenc) == Go(compact) == Z80(SAM). The only vendored artefacts are `release.s`
(source) and `release.img` (GNU output) — **neither is a `.tbn` blob**, so the v1→v2 flip needs **no
re-vendoring** (the compactor simply emits v2; the gate re-derives it). This is a key de-risking fact.

Gates, by PR:
- **m6-release 3-way byte-match** (existing, the spine): must pass after the Go PRs (Go == Go-compact == GNU)
  and after the Z80 PR (adds Z80 == GNU via the v2 compact `.tbn`). Until the Z80 decoder lands, the gate's
  Z80 step would fail on a v2 `.tbn` — so the Go PRs must keep the gate green by having refenc still able to
  produce a **Z80-consumable** artefact (see §7 sequencing: the Go PRs land the v2 emit *and* keep the Z80
  building from v1 until the Z80 PR flips it — or the Go+Z80 land together behind a feature branch). Pin the
  exact mechanism in PR(a)'s description.
- **`disasm-roundtrip`** (existing, extend): add assemble→overlay(v2)→disassemble→re-assemble byte-identical
  over the M3–M6 fixtures. This is the **primary guard for the slot-enum fidelity** (§3) — a one-bit
  fold-rule error fails it deterministically without needing SimCoupé.
- **Go-harness compact-tbn test** (`tools/z80-test-harness-go/compact_tbn_test.go`, mirror
  `TestCompactTbnAssembly`): feed the **v2** compact `.tbn` through the SAM boot path and assert OUT ==
  release.img. Add a v2 variant; this is the fast (~ms) inner-loop check before SimCoupé.
- **Format-package unit tests:** round-trip `WriteInsnRun`/reader; varint label-table encode/decode;
  `FoldSlot` per slot kind against a hand-computed vector (lock each fold-rule with a literal expected value).
- **Boot self-tests** (`TestBootSelfTestsPass`): unchanged, must stay green after the Z80 decoder change.

Inner loop (CLAUDE.md): iterate with `tools/z80-test-harness-go/` + `pyz80` + `go test ./...`; reserve the
SimCoupé matrix for the **final pre-merge gate** of each PR, not per-iteration.

---

## 7. PR sequencing

Smallest independently-shippable, independently-green PRs. Mirrors i1's Go-first-then-Z80 shape (PR #121
LIT_INSTS Go emit/decode + gate → PR #122 Z80 decode → PR #124 LIT_DATA). The breaking v2 flip forces a
decision on transition (§8); the recommended shape keeps `main` green throughout by **landing the Go v2 emit
+ decode + disassembler together (so refenc round-trips v2), then the Z80 decoder, then the header table** —
with the **whole Phase-1 sequence developed on a single feature branch** so `main` never sees a half-flipped
format (CLAUDE.md §5: a breaking format change is exactly the "long-lived feature branch until green" case).

Within that branch, the reviewable commits/PRs are:

- **(a) Format structs + Go emit/decode + slot enum + gate.** Format package v2 (kinds, version, reader/writer,
  `INSN_RUN`, `FoldSlot`/`ZeroSlot` enum); refenc overlay emit (`encodeInstOverlay` reusing `encodeInst`) +
  overlay assemble (pass2). **Proves:** Go(refenc) assembles the v2 compact `.tbn` byte-identical to GNU
  release.img (and to the symbolic path) — the m6-release Go arms green. The instruction overlay round-trips
  through refenc. *This is the largest, highest-risk PR (the slot enum).* Keep `INSN_RUN` without the header
  table yet — labels can still be `DIRECTIVE`-free position markers carried as `patch_count=0` boundaries, OR
  land the header table here too if cleaner; recommend table in PR(d) to keep (a) focused on the overlay.
- **(b) Disassembler round-trip.** aarch64dec overlay-aware rendering + extend `run-disasm-roundtrip.sh` to
  assemble→overlay→disassemble→re-assemble. **Proves:** the slot enum is exhaustive and faithful (the
  fidelity guard for (a)); round-trip byte-identical over M3–M6.
- **(c) Z80 overlay decoder.** `INSN_RUN` dispatch + mode-0/mode-1 pass-1/pass-2 handlers in `main_loop.asm`,
  fold jump-table (ports of pass2 conversions), reusing `expr_eval.asm`; delete the form-table symbolic path.
  **Proves:** Z80(SAM) assembles the v2 compact `.tbn` to OUT == release.img (m6-release Z80 arm green);
  `&C000` budget holds (measured); boot self-tests green.
- **(d) Header label/offset table.** Move `LABEL_DEF`/`LOCAL_DEF` to the delta-varint header table; Go build/
  consume + Z80 upfront load; remove the per-record label dispatch on both sides. **Proves:** labels no longer
  split runs (single long instruction run); m6-release stays green; label table ~−0.7 KB.

Land order: **(a) → (b) → (c) → (d)**, all on the feature branch, merged to `main` once the m6-release gate is
green for the full v2 stack. (If the team prefers smaller `main` increments, (a)+(b)+(c) can merge as the
"overlay" unit and (d) as a follow-up, since the overlay is correct with labels still as records — but the
header table is what makes runs un-split, so landing it in the same milestone is cleaner.)

Each PR runs the mandatory pre-merge review (CLAUDE.md §3) — note especially item #5 (no gap-masking skips):
the `disasm-roundtrip` skips are *principled* (non-4-byte data fixtures, litpool-data-as-instruction) and
carry over unchanged; do not add new skips to hide a slot-enum gap (fix the fold-rule instead).

---

## 8. Risks & open decisions

**#1 risk — slot-enum byte-match fidelity.** The whole format's correctness reduces to the slot/fold-rule
enum (§3) exactly mirroring `pass2.go`'s per-slot PC conversions. A one-bit divergence (a missed `&` mask, a
wrong ADRP sign-extension, an off-by-one scale) fails the m6-release gate. **Mitigation:** (1) factor the fold
conversions into one `format.FoldSlot` shared by the literal encode path and the overlay path so they cannot
drift; (2) the `disasm-roundtrip` property test as a fast deterministic guard; (3) per-slot `FoldSlot` unit
vectors with hand-computed expected bits. This is the dominant execution risk and the reason PR(a)+(b) ship
together.

**v1→v2 flip mechanics (decision, mostly settled):**
- The m6-release gate's **vendored fixture does NOT need re-vendoring** — it is `release.s` + `release.img`,
  regenerated to `.tbn`/compact-`.tbn` each run. Confirmed by reading the gate. ✅ resolved by inspection.
- **Hard cut vs dual-emit during transition:** recommend a **hard cut** — refenc emits v2 only; the reader
  accepts v2 only (§7 keeps `main` green by developing on one feature branch, so a hard cut never lands
  half-done). No v1/v2 coexistence in the format package (matches §7 decision 3 "clean breaking v2"). The one
  place transition friction appears is between PR(a) (Go v2) and PR(c) (Z80 v2): on the feature branch the
  Z80 build consumes v2 only after (c), so the m6-release **Z80 arm** is red between (a) and (c) — acceptable
  on the feature branch (dev-only red), green before merge to `main`.

**Z80 code budget.** The overlay decoder should be net-smaller than the deleted form-table path, but **must
be measured** against `&C000` (`make check-budget`). If it regresses, investigate (don't ratchet the budget).

**STAGING_BUF splitting of variable-length elements.** Mechanical (mirror `litDataMaxBytes`), but the
whole-element discipline must be tested — a mid-element split corrupts the Z80 decode silently. Add a fixture
with a run that straddles the ~1016 B boundary.

**Genuinely needs Pete before execution starts — likely NONE.** Per §7 of the design, all five open questions
are resolved (Format B, breaking v2, one file, evictable editor region deferred to Phase 2/i40, uniform symbol
resolution, numeric locals first-class). The fold-rules are a mechanical port of existing Go (CLAUDE.md §6 —
not a design decision). The two micro-choices to settle *in PR(a)* (not blockers, agent's call): (i) `expr_len`
u8 vs u16 in the patch (recommend u8, flag if a >255-byte expr appears); (ii) whether `SLOT_MEM_IMM12` reads
`scale` from the base word's size field or stores it — recommend read-from-base-word, pinned by a test. Flag
to Pete only if, during PR(a), a release instruction is found whose symbolic field does **not** reduce to one
of the §3 slot kinds (that would be a genuinely new fold-rule, not in the Go authority).
