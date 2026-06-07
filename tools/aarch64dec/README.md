# `tools/aarch64dec` — the decoding authority

Go library + CLI (`cmd/aarch64dec`): the aarch64 disassembler the Z80
`src/disasm.asm` is ported from. Held to GNU binutils — the `disasm` CI
job (`make ci-disasm`, `tests/disasm/run-oracle-comparison.sh`) asserts an
exact word-for-word match against `objdump` on the vendored release; we
match binutils' alias choices rather than inventing our own
(`aliases.go`).

Build: `make aarch64dec` (→ `build/aarch64dec`). Gates: `make ci-disasm`
(oracle) and `make ci-disasm-roundtrip` (encode→decode→encode
self-consistency). See `docs/ARCHITECTURE.md` §3.
