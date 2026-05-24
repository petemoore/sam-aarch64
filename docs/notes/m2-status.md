# M2 — current status (read me first)

Entry point for any session picking up M2 encoder-tables work.

Last update: 2026-05-24. M2 substantially complete on branch
`worktree-m2-completion`; 19/20 M1 fixtures now byte-match
`aarch64-none-elf-as`. Only `expr_pcrel` (multi-section VMA layout)
remains as a known gap.

## What M2 is (spec recap)

Per `docs/specs/2026-05-24-m2-encoder-tables-design.md`:

- **`tools/aarch64enc/`** — Go library: every operand-encoder slot
  kind (Xreg, Wreg, Imm12Shifted, BranchImm26, AdrpImm, LogicalImm,
  BitfieldImm, etc.), Form / OperandValue types, Encode dispatcher,
  ValidateOperandKinds, symbol-aware Eval.
- **`tools/enctab-gen/`** — Go CLI: reads vendored ARM MRA XML,
  filters to the `MnemonicTable` subset, emits both
  `tools/aarch64enc/data.go` (in-memory form table) and
  `build/enctab.enc` (binary table for M3's Z80).
- **`tools/refenc/`** — Go CLI: consumes a `.tbn` file, two-pass
  encodes against `aarch64enc`, byte-compares against
  `aarch64-none-elf-as` for fixtures.
- **`reference/arm-mra/`** — vendored slice of ARM MRA XML
  (A-Profile 2022-12, SHA pinned in `manifest.json`).

## Layout

```
reference/arm-mra/
  manifest.json                  ARM version + tarball SHA
  shared_pseudocode.xml
  <mnemonic>.xml × 13            (nop, add, sub, ret, b, b.cond,
                                  cmp, ldr, str, cbz, cbnz, bl,
                                  mov_movz, adrp)

tools/aarch64enc/
  types.go                       SlotKind, OperandSlot, Form
  slots_trivial.go               Xreg/Wreg/XregOrSp/WregOrSp/Imm5/Imm6/CondCode
  slots_imm.go                   Imm12Shifted, Imm16Shifted, ShiftAmount, ExtendOp
  slots_branch.go                BranchImm26/19/14
  slots_adrp.go                  AdrpImm (immlo:2 @ 29..30, immhi:19 @ 5..23)
  slots_logical.go               LogicalImm (LLVM processLogicalImmediate)
  slots_bitfield.go              BitfieldImm (BFI + UBFX only)
  encode.go                      Encode dispatcher
  validate.go                    ValidateOperandKinds + FormsForMnemonic hook
  expr.go                        EvalContext, Eval (symbol-aware)
  data.go                        GENERATED — checked in, ~24 forms

tools/enctab-gen/
  main.go                        CLI: filter by MnemonicTable, emit data.go + enctab.enc
  mra/parser.go                  MRA XML → ParsedForm (handles <c colspan>, settings, encoding-level overrides, alias_mnemonic)
  mra/fields.go                  MRA field-name → SlotKind map
  emit/gopkg.go                  RenderGoPackage (deterministic Go output)
  emit/enc.go                    RenderEnc (binary .enc per spec §2)
  scripts/snapshot.sh            One-off MRA tarball downloader/extractor

tools/refenc/
  main.go                        CLI
  pass1.go                       PC + symbols + LocalDefs + .equ/.set resolution
  pass2.go                       Form lookup, expression eval, byte emission

Makefile                         Adds enctab-gen, refenc, enctab, test-m2, ci-m2 targets
.github/workflows/ci.yml         Adds m2 job
```

## Test pyramid status

| Layer | Status | Notes |
|---|---|---|
| 1 — aarch64enc unit tests | ✅ ALL PASS | Every slot kind tested, LogicalImm has pin tests against known-correct ARM encodings |
| 2 — enctab-gen / mra unit tests | ✅ ALL PASS | Parser tested against real `nop.xml` and synthetic XML |
| 3 — refenc roundtrip vs GNU as | ✅ 19/20 | Only `expr_pcrel` (multi-section VMA layout) remains |
| 4 — text2bin operand-kind validation | ❌ NOT DONE | Spec §5 wiring (Task 21) deferred |

## What's verified end-to-end

19 of the 20 M1 fixtures byte-match `aarch64-none-elf-as`:

`comments`, `dir_align_skip`, `dir_data`, `dir_equ`, `dir_string`, `empty`,
`expr_simple`, `inst_bcond`, `inst_csel`, `inst_extended`, `inst_mem_extended`,
`inst_mem_indexed`, `inst_mem_preindex`, `inst_mem_simple`, `inst_nop_ret`,
`inst_reg_imm`, `inst_shifted`, `labels`, `local_labels`

That covers: nop, add/sub (immediate and register variants), cmp/csel/csinc,
ldr/str (all addressing modes: unsigned offset, pre-index, post-index,
register-offset, register-shifted, register-extended), .balign, .skip,
b.cond × 16, .byte/.short/.word/.quad, .ascii/.asciz, .equ/.set,
comments, labels, local labels (1f/1b), pure-constant expressions.

## Known gaps

1. **Section layout in refenc** (`expr_pcrel`). `.text` and `.data`
   sections both resolve to VMA=0 in relocatable objects; GNU as's
   `objcopy -O binary` dumps them in ELF section-table order with
   overlapping VMAs. Implementing this requires tracking section
   boundaries and emitting a VMA-layout-aware binary. Low priority
   since `expr_pcrel` is the only affected fixture.

2. **Bare `ret` form gets clobbered on regen**. The 0-operand
   `ret` form was added manually to `data.go`; running
   `make enctab` overwrites it. Either teach the generator about
   the ret alias, or add a small post-pass that re-injects the
   form after regeneration.

3. **text2bin operand-kind validation** (spec §5, Task 21).
   Currently text2bin doesn't consult `ValidateOperandKinds`, so
   malformed sources are accepted at parse time and only fail at
   refenc/M3 time.

4. **`.org` in refenc**. Not implemented; pass1 returns 0 for size.

## Hand-off recipe

1. From repo root: `make ci-m2`. All Go tests should pass.
2. Inspect coverage: open `tools/aarch64enc/data.go` — search for
   `MnemonicID:` to see what's in the form table.
3. Refenc smoke test:
   ```bash
   echo -e "add x0, x1, #4\nret" > /tmp/t.s
   go run ./tools/text2bin /tmp/t.s -o /tmp/t.tbn
   go run ./tools/refenc /tmp/t.tbn -o /tmp/t.bin
   aarch64-none-elf-as /tmp/t.s -o /tmp/t.o
   aarch64-none-elf-objcopy -O binary /tmp/t.o /tmp/t.gnu.bin
   cmp /tmp/t.bin /tmp/t.gnu.bin && echo OK
   ```
4. Extend coverage by picking a gap from the list above and
   working through it. Layer 3 fixture round-trip is the load-
   bearing oracle — if refenc byte-matches GNU as, the form is
   correct.

## Authoritative references

- Format spec: `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md`
- M2 spec: `docs/specs/2026-05-24-m2-encoder-tables-design.md`
- M2 plan (local-only, gitignored): `docs/superpowers/plans/2026-05-24-m2-encoder-tables.md`
