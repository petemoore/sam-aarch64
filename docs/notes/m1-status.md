# M1 — current status (read me first)

Entry point for any session picking up M1 binary-tokenised-format work.

Last update: 2026-05-24. M1 implementation complete on branch
`worktree-m1-binary-tokenised-format`; ready for draft PR.

## What M1 is

The Mac-side toolchain that lifts Phase 1 aarch64 source files into
a binary tokenised on-disk format and back. Three deliverables:

1. **Format spec**: `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md`.
   Defines magic, version, name table, record kinds, operand kinds,
   expression bytecode, local-label semantics, comment placement.
2. **`text2bin`**: plain-text aarch64 → `.tbn`. Hand-written lexer +
   parser. Constant-folds expressions. Reports errors as
   `file:line:col: message`. Owns all source-side validation.
3. **`bin2text`**: `.tbn` → canonically-formatted plain text. Pure
   inverse for round-trip testing and developer export.

Shared package `tools/sam-aarch64-format/` is the single source of
truth for record/operand/opcode catalogues plus the symbol-table
interner and all reader/writer primitives.

Plan: `docs/superpowers/plans/2026-05-24-m1-binary-tokenised-format.md`
(local-only — gitignored under `docs/superpowers/`).

## Layout

```
tools/
  sam-aarch64-format/         shared library (format primitives)
    format.go kinds.go operands.go expr.go
    symbols.go mnemonics.go directives.go
    writer.go reader.go + *_test.go

  text2bin/                   CLI binary
    main.go
    internal/translate/       lexer + parser + Translate (importable
                              from text2bin tests and golden harness)

  bin2text/                   CLI binary
    main.go
    emit/                     Emit (importable; non-internal because
                              text2bin's internal/translate imports it
                              for the idempotency and reachability tests)

tests/m1/
  sources/                    19 .s fixtures, one per construct family
  golden/                     canonical text outputs
  run-gnu-as-check.sh         Layer 4 wrapper
```

## Test pyramid

| Layer | Where | What |
|-------|-------|------|
| 1     | `tools/*/(*_test.go)` | Format-unit: every record kind, operand kind, opcode |
| 2     | `tests/m1/golden/*` + `golden_test.go` | `.s` round-trip golden corpus |
| 3     | `reachability_test.go` | Hand-crafted `.tbn` files round-trip via Emit→Translate |
| 4     | `run-gnu-as-check.sh` | Every fixture is also valid GNU `as` aarch64 |

All four pass locally. CI job `m1` in `.github/workflows/ci.yml`
runs them on `ubuntu-latest` with `aarch64-linux-gnu-as`.

## Known gaps (deferred deliberately)

- `msr` / `mrs` mnemonics. `OpSysName` is reserved in the format
  but the parser doesn't wire it. Add when a Phase 1 fixture demands.
- Macros, conditional assembly, `.section` beyond `.text`/`.data`,
  multi-file `.include` — Phase 1 spec defers all of these.
- `PUSH_IMMn` numeric-base hint — deferred to format v2 (only added
  if M3 needs deterministic hex output).
- CRC / signature in the file header.

## Hand-off recipe

1. From repo root: `cd tools/sam-aarch64-format && go test ./...`,
   then likewise for `tools/text2bin/` and `tools/bin2text/`. All pass.
2. `make text2bin bin2text` → builds the binaries into `build/`.
3. `./tests/m1/run-gnu-as-check.sh` if `aarch64-none-elf-as` is
   installed (or set `AARCH64_AS=aarch64-linux-gnu-as` on Linux).
4. To extend the mnemonic table: append entries to
   `tools/sam-aarch64-format/mnemonics.go`'s `MnemonicTable`. IDs are
   append-only; existing IDs never shift.
5. To add a new fixture: drop it under `tests/m1/sources/`, run
   `cd tools/text2bin && go test -run TestGoldenCorpus ./... -update`,
   review the generated golden, commit both.
