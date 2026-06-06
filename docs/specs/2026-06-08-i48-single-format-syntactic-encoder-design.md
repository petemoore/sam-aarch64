# i48 — single serialized format + pass-free syntactic encoder (design)

**Date:** 2026-06-08 · **Item:** i48 (strands i48a–i48d) · **Milestone:** M8 ·
**Type:** design spec (decisions agreed with Pete in this session) · **Refines:** i39 / i39a

**Authority:** the code, then the agreed design. This spec records two architectural
decisions taken after i39a PR(a)/(b) landed, and enumerates every touchpoint they
affect so nothing is lost. It is the normative reference for both; the i39 design
(`2026-06-08-compact-tbn-nextgen-design.md`) remains the format design, this sharpens
*how the host and SAM produce and consume it*.

---

## 1. The two decisions

**Decision A — one serialized format.** The compact **overlay** (`INSN_RUN` +
`LIT_DATA`, v2) is the *only* serialized `.tbn` form. The "symbolic" record kinds
(`KindInst`/`KindLabelDef`/`KindLocalDef`/`KindLitInsts`) are **never written to
disk** — they become an **in-memory IR** internal to the host front-end. The old
(v1/symbolic) format is **buried in git history and described in no head doc**: it was
never released, so it is a relic, not a documented format.

**Decision B — text→overlay is a pure syntactic transform; the fold does the
value-dependent work.** Turning source text into an overlay element requires **no
symbol resolution** — it is determined by syntax alone (mnemonic + operand kinds +
register numbers), with every value-dependent bit zeroed and computed in the **fold**
at assemble time (which always runs a full symbol pass, on host *and* SAM).

These are two halves of one principle: **make the host mirror the eventual SAM flow.**

## 2. Why — the target architecture is symmetric

In the target, the host disappears: a user types an instruction into the SAM editor,
and the **SAM itself** must translate text → compressed (overlay). So the capabilities
are symmetric, with the overlay as the one thing in the middle and Go as the authority
for each Z80 port (exactly as `aarch64dec` is the authority for `src/disasm.asm`):

| capability | host (Go) | SAM (Z80) |
|---|---|---|
| text → overlay (tokenize + base word + patch) | `text2bin`+`refenc` front-end | **editor input — future (i48c)** |
| overlay → bytes (assemble) | `refenc` | `src/main_loop.asm` (i39a PR(c)) |
| overlay → text (render) | `bin2text` (i39a PR(b)) | editor display (≈ `src/disasm.asm` + overlay) |

The symbolic form is a *transient parse tree* in text→overlay on **both** sides; it is
never serialized on either. That is the whole content of Decision A. The serialized
symbolic `.tbn` is a host-only artefact that **diverges** from the target SAM flow, so
burying it *aligns* host and SAM.

## 3. Decision A in detail — eliminate the serialized symbolic format

Today `text2bin` tokenizes → a symbolic `.tbn` on disk → `refenc` runs pass-1 (symbol
resolution + PC + litpool) and emits the binary **and** the compact overlay `.tbn`. The
SAM only ever loads the compact overlay. The symbolic `.tbn` is the disk handoff between
the two tools.

**The snag that makes this a real refactor, not a `text2bin` tweak:** emitting the
overlay needs pass-1 (e.g. the mem `ldr`/`ldur` opcode depends on the resolved offset —
see §4), so the overlay can only be produced *after* pass-1. Eliminating the disk handoff
therefore means the tokenize step and the pass-1/overlay-emit step must share a process
or library — i.e. **extract `refenc`'s front-end (tokenize-feed → pass-1 → overlay-emit)
into shared logic** so the symbolic records live only in memory.

**Shape (recommended; see open question Q1):** keep `text2bin` and `refenc` as two CLIs
over a shared front-end library; `text2bin` emits the overlay `.tbn`, `refenc`/Z80
consume it. The symbolic IR is in-memory in the shared lib. (Host pass-1 then runs twice
— once to emit the overlay, once to fold it into the binary — which is minor and
correctness-neutral. The alternative is one merged tool; that removes the duplication
but retires `text2bin` as a command.)

**What drops from the on-disk format / format package:** `KindInst`, `KindLabelDef`,
`KindLocalDef`, `KindLitInsts` serialization. (`KindLitInsts` (0x07) is already dead —
`INSN_RUN` mode 0 subsumes it and `compact_test.go` asserts none survive; this folds in.)
`LABEL_DEF`/`LOCAL_DEF` move to the header label table (i39a PR(d)). `DIRECTIVE`/`COMMENT`/
`LIT_DATA` stay.

## 4. Decision B in detail — syntactic encode, value-work in the fold

The overlay is already "base word with relocated field zeroed + patch, folded at
assemble." Decision B closes the gap that still forces a value pass into the *encoder*:

**(a) Move value-derived base-word bits into the fold.** Today a couple of folds cheat
by baking a value-derived bit into the base word at compaction time (which has pass-1):

- **`FoldMovzAuto`** (`mov Rd, #expr` auto-collapsed to movz): the `hw` shift currently
  comes from the pre-baked base word; the fold reads it. **Change:** zero `hw` in the
  base word (`ZeroSlot` clears bits 22:21 too) and have the fold **compute `hw` from the
  resolved value** (find the non-zero 16-bit chunk) and set both `hw` and `imm16`. This
  is the one change that alters overlay *bytes* (the movz-auto base word), so it must
  land **before/with i39a PR(c)** so the Z80 fold port matches (see §7).
- Explicit `movz`/`movk` (`FoldMovkImm16`) already take `hw` from the source `lsl #N`
  (syntactic) — **no change**.

**(b) Forego GNU's silent form-rewriting for *symbolic* operands → make it syntactic.**
GNU silently rewrites `ldr`→`ldur` when a scaled offset doesn't fit, and `add #big`→
`add #x, lsl #12`. For a *symbolic* operand the chosen form depends on the resolved
value, which is the only thing forcing the value-pass into classification. **Decision:
the mnemonic the user types fixes the form.** `ldr` with a symbolic offset is always the
scaled form; if it resolves to something only the unscaled form can hold (negative /
unaligned), that is a **clear error** ("use `ldur`"), not a silent rewrite. Same for
`add`/`adds` large-immediate `lsl #12` (syntactic / explicit, never auto-rewritten).
*Constant* operands keep their parse-time choice (the value is in hand at parse, no pass
needed) — so `ldr x0,[x1,#-8]`→`ldur` still works for constants.

- Cost is ~zero on the corpus: of the symbolic mem offsets in the vendored release,
  **306 are scaled (`FoldMemImm12`) and 0 ever needed the unscaled rewrite**
  (`FoldMemImm9`); the only unscaled uses are constant offsets the author wrote as
  `ldur` explicitly.
- It is pedagogically on-brand: the editor's ethos is "understand the encoding," so
  surfacing the `ldr`/`ldur` distinction beats hiding it.
- Effect on classification: a symbolic mem operand maps to `FoldMemImm12` **by syntax**
  (the `ldr` mnemonic), never by evaluating the offset. `classifyMem`'s value-eval
  branch (imm12-vs-imm9 by resolved value) is **removed** for symbolic operands.

**(c) Keep aliases, pick the family syntactically, narrow assemble-time fallback for the
one value-dependent *opcode*.** `mov #x`/`ldr =x` stay. The only residue is a
value-dependent *opcode* (not just a field): `mov #x` → `movz` vs `orr`(bitmask) vs
`movn`. In the corpus it is **always `movz`**. So default `mov`→`movz` syntactically, and
fall back to `orr`/`movn` **only at assemble time** if the resolved value isn't
`movz`-able. This is the narrow form of "try-then-fallback," applied to the handful of
genuinely opcode-ambiguous aliases, not as a pervasive feature.

**Net:** text→overlay needs no symbol pass — which is exactly what makes the SAM editor's
input path realistic (at edit time a referenced symbol may not be defined yet). All
value-dependent computation lives in the fold, which has pass-1 on both host and SAM.

## 5. Literal pools / `ldr Xn, =val`

No fundamental problem; they fit the same principle. The pool is **synthesised at
assemble time, not stored** in the overlay: the overlay keeps the pseudo-op as a
`FoldLitpool19` patch carrying the *value*; the assembler (host *and* SAM —
`src/litpool.asm` already does this for the M6 byte-match) collects the `=val`s, dedups,
places them at `.ltorg`/section-end, and folds each `imm19 = poolPC − pc`. So text→overlay
emits the patch syntactically; all pool work is assemble-time. Two honest subtleties:
(a) `FoldLitpool19` is *special* — its folded value is the **pool slot's PC** (a pass-1
*layout* result), not `eval(expr)`; that's inherent and already handled. (b) The pool is
**invisible in the editor** (you see `ldr =val`, never the synthesised `.word`), which is
the correct UX (matches GNU; the user owns the pseudo-op, the assembler owns the pool).
64-bit values (`=0x1b51c351c` → 8-byte `.quad` slot) already work.

## 6. Impact & touchpoints (the enumeration)

Decisions A (eliminate serialized symbolic) and B (syntactic encode / fold value-work)
touch the following. `[A]`/`[B]` tag which decision; "NEW" = file added by i39a PR #131.

### Go code — host tools (`tools/`)
- `sam-aarch64-format/kinds.go` `[A]` — remove `KindInst`/`KindLabelDef`/`KindLocalDef`/`KindLitInsts` consts + `Name()` arms once they are in-memory-only.
- `sam-aarch64-format/reader.go`, `writer.go` `[A]` — drop the symbolic record read/write paths; serialize/parse only `INSN_RUN`/`LIT_DATA`/`DIRECTIVE`/`COMMENT` + header label table.
- `sam-aarch64-format/litinsts.go` `[A]` — drop `KindLitInsts` (dead; subsumed by `INSN_RUN` mode 0).
- `aarch64enc/overlay.go` (NEW) `[B]` — `FoldMovzAuto` compute `hw` from value (not base); `ZeroSlot` clear `hw` for the movz-auto slot; confirm `FoldAddSubImm12` `sh` is syntactic.
- `refenc/overlay.go` (NEW) `[A][B]` — `overlayClassify`/`classifyMem`: symbolic mem → `FoldMemImm12` by syntax, **error** (not imm9 rewrite) when it doesn't fit; `classifyMovImm` keep movz default + assemble-time orr/movn fallback.
- `refenc/compact.go`, `pass1.go`, `pass2.go` `[A][B]` — overlay emit moves into the shared front-end; pass-2 fold-dispatch gains the movz-auto `hw` computation; the symbolic `KindInst` read path is retired.
- `refenc/main.go`, `text2bin/main.go`, `text2bin/internal/translate/{parser,flatten,translate}.go` `[A]` — restructure into the shared front-end so the symbolic IR is in-memory; `text2bin` emits the overlay `.tbn`. `[B]` — parser errors on a symbolic operand that doesn't fit its syntactic form.
- `bin2text/emit/{emit,overlay}.go` (overlay.go NEW) `[A]` — render only the overlay (already done for the overlay; remove the symbolic `KindInst` render path once symbolic is gone).

### Go tests
- `sam-aarch64-format/{kinds,reader,writer,litinsts}_test.go` `[A]` — drop/rewrite symbolic-format assertions.
- `aarch64enc/overlay_test.go` (NEW), `encode_test.go` `[B]` — add `FoldMovzAuto` hw-from-value vectors; assert the syntactic form errors.
- `refenc/compact_test.go`, `pass2_test.go` `[A][B]` — v2-only compaction; fold-dispatch tests.
- `text2bin/internal/translate/{parser_*,golden,integration}_test.go`, `strip_test.go` `[A]` — in-memory IR / v2 assertions. **The M1 string-matched goldens (`tests/m1/golden/`) become re-assemble-and-byte-check round-trips** so a frozen-wrong golden can't slip again (the `dir_skip_symbolic` 80-vs-96 incident).
- `bin2text/emit/*_test.go` `[A]` — overlay-only render.

### Z80 code (`src/`)
- `reader.asm` `[A]` — version check 1→2 (i39a PR(c); without it the SAM rejects every v2 `.tbn`).
- `main_loop.asm` `[A]` — add the `INSN_RUN` (0x09) decoder; **delete** the symbolic form-table path (`main_handle_inst`) + `REC_KIND_INST`/`REC_KIND_LABEL_DEF`/`REC_KIND_LOCAL_DEF`/`REC_KIND_LIT_INSTS` handlers. `[B]` — the mode-1 fold jump-table must port the **refined** folds (movz-auto computes `hw`).
- `litpool.asm` `[A]` — litpool registration keyed off the `FoldLitpool19` patch (unchanged synthesis; m6 byte-match holds).
- `disasm.asm` `[A]` — unaffected (word→text; overlay render for the editor is later).
- **Future (i48c):** the Z80 **text→overlay encoder** — the editor input path — is NEW Z80 code that does not exist yet; Go (`refenc` front-end) is its authority.

### Z80 tests
- `tools/z80-test-harness-go/compact_tbn_test.go` `[A]` — feed the v2 overlay through the boot path; assert OUT == release.img.
- `tools/z80-test-harness-go/{boot self-test, disasm_oracle}_test.go` — keep; must stay green after PR(c).
- `src/test_*.asm` — keep (exercise encoding; unaffected by symbolic removal).

### End-to-end / round-trip gates
- `tools/run-disasm-roundtrip.sh` `[A][B]` — overlay legs already added (i39a PR(b)); they exercise the refined folds via byte-identity.
- `tools/run-m6-release-gate.sh` `[A][B]` — 3-way byte-match; Z80 arm green after PR(c) with the refined folds.
- `tests/disasm/run-oracle-comparison.sh` — keep (aarch64dec vs objdump; unaffected).
- `tools/revendor-m6-release.sh` — keep (vendors `release.s`/`release.img`, not `.tbn`).

### CI (`.github/workflows/ci.yml`) + Makefile
- No config change. Required checks `m1`/`m2`/`disasm`/`disasm-roundtrip`/`sysreg-sync` stay green; `m3`/`m4{,-prod}`/`m5{,-prod}`/`m6{,-prod}`/`m6-release` go green when PR(c) lands the v2 Z80 reader + refined folds.

### Docs (the scrub — Decision A's "no old format in head")
- `docs/specs/2026-06-08-tbn-binary-format-reference.md` `[A]` — **full rewrite to overlay-only** (i48d). It currently says `Version = 1` and documents the symbolic record kinds as current; that is correct for `main` *today* (v2 is unmerged), so the rewrite lands **with** the v2/elimination, not before — until then it carries a banner pointing here. **This is the doc Pete specifically flagged.**
- `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md`, `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` `[A]` — mark **historical baseline** (dated design records; they may describe the old format *as the baseline they improved on*, but get a clear "superseded — not the current format" banner).
- `docs/specs/2026-06-08-compact-tbn-nextgen-design.md` — the v2 format design; this spec refines it (cross-link).
- `docs/notes/m8-status.md`, `docs/ROADMAP.md`, `docs/notes/item-registry.md` `[A][B]` — i48 strands + status (this change).
- `docs/notes/question-registry.md` — residual open questions q5/q6/q7 (§8).
- `docs/plans/2026-06-08-i39-phase1-instruction-overlay-plan.md` `[B]` — note the fold-rule refinement lands with PR(c).

### PR comments
- **PR #131** (the active i39a branch) — record decisions A+B so the PR reflects the agreed design (the fold refinement is in-scope for PR(c) on that branch).

## 7. Work strands & sequencing

- **i48a** — host front-end unification: shared tokenize/pass-1/overlay-emit lib; `text2bin` emits the overlay; drop symbolic serialization (`KindInst`/`LabelDef`/`LocalDef`/`LitInsts`); in-memory IR. *(Absorbs the standalone `KindLitInsts`-removal item.)*
- **i48b** — syntactic encoder + fold-time value-work: `FoldMovzAuto` computes `hw`; symbolic mem → `FoldMemImm12`-or-error (no imm9 rewrite); add/sub `lsl #12` syntactic; `mov` movz-default + assemble-time orr/movn fallback. **Has a format-byte effect (movz-auto base word), so its Go change must precede/accompany i39a PR(c)** so the Z80 fold port targets the final rule.
- **i48c** — Z80 text→overlay encoder (editor input path) — future (editor phase); Go i48b is the authority.
- **i48d** — doc unification: rewrite the tbn format reference to overlay-only; scrub head docs; historical banners on M1/i1 design docs. Lands with the v2/elimination so docs and code agree.

**Sequencing vs i39a:** i48b's fold refinement should land **before or with PR(c)**
(PR(c) ports the Z80 folds — it must port the refined ones). i48a (front-end unification)
is independent of the Z80 and can proceed in parallel. i48d lands with the merge to `main`
so no head doc describes a format the code doesn't produce.

## 8. Open questions (→ `question-registry.md`)

- **q5 — host packaging.** Two CLIs over a shared front-end lib (recommended; keeps
  `text2bin` as a command; minor host pass-1 duplication) vs one merged tool (no
  duplication; retires `text2bin`). Pete to confirm.
- **q6 — editor in-memory model for value-dependent base words.** At edit time a
  referenced symbol may be unresolved, so the editor can't always pick the final base
  word. Strategy (keep tentative/symbolic until assemble vs default+validate) interacts
  with **i41** (edit-model). Resolve at editor phase.
- **q7 — strictness scope.** We forego `ldr→ldur` and `add` `lsl #12` auto-rewrite.
  Any other GNU "generous" rewrites in the corpus to treat the same way? (Sweep when
  implementing i48b.)

## 9. Risks

- **Byte-fidelity of the `FoldMovzAuto` change.** Moving `hw` into the fold changes the
  movz-auto base word; the m6-release 3-way gate + `disasm-roundtrip` overlay legs catch
  any divergence. Land the Go change and confirm green before porting to Z80.
- **Front-end extraction churn.** `text2bin`'s preprocessing (`.include`/`.if`/`.macro`/
  `-flatten`) is substantial; the shared-lib extraction is the bulk of i48a. Mitigate by
  keeping the in-memory record types identical to today's and only moving *where* pass-1/
  overlay-emit run.
- **Doc/code skew during transition.** The format-reference rewrite (i48d) must land with
  the code (not before), else a head doc describes a format `main` doesn't yet produce —
  the inverse of the problem we're fixing.
