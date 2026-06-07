# `tools/sam-aarch64-format` — the `.tbn` format library

Go library (`package format`): the code authority for the `.tbn` v2
container — record kinds, operand encodings, expression bytecode,
directive/mnemonic tables, header position tables, the editor region, and
name front-coding. The normative prose reference is
`docs/specs/tbn-binary-format-reference.md`; the Z80 reader mirrors this
package's constants.

`sysregs.go` is also the Go authority for the Z80 sysreg/pstate/dc/tlbi
table — `make sysreg-sync-check` (CI job `sysreg-sync`) asserts the two
cannot drift.

Imported by `sam-aarch64` and `aarch64dec`; tested via `make ci-format`/`ci-encoder`.
