# `tests/` — fixture corpora

The corpora are **cumulative feature tiers**: each tier's fixtures exercise
the new capability of that tier on top of everything below it. Every
fixture `.s` is assembled by our toolchain(s) and byte-compared against GNU
(`as` [+ `ld -Ttext=0` from the `symbols` corpus up] + `objcopy -O binary`) — see
`docs/ARCHITECTURE.md` §7 for the full pipeline.

| Corpus | Fixtures | Exercises | Runs on |
|--------|----------|-----------|---------|
| `format/` | 36 | `.tbn` format round-trip + host-assembler unit checks | host (`make ci-format`/`ci-encoder`) |
| `spectrum4/` | 29 | real spectrum4 kernel sources through the host toolchain | host (`make ci-encoder`) |
| `disasm/` | — | `aarch64dec` vs binutils `objdump` oracle on the vendored release | host (`make ci-disasm`) |
| `core/` | 9 | core Z80 emit (read `.tbn`, encode, HSAVE) on SimCoupé | container (`make ci-core`) |
| `symbols/` | 5 | symbols, two-pass, expression evaluation | container (`make ci-symbols`) |
| `operands/` | 20 | compound operands + directives | container (`make ci-operands`) |
| `paged/` | 19 | paged IN/OUT at scale (>16 KB output) | container (`make ci-paged`) |
| `release/` | 1 pair | the vendored spectrum4 release (`release.s` + GNU `release.img`) — the 3-way release gate | container (`tools/run-release-gate.sh`) |

**Running a sweep:** each SimCoupé corpus dir carries a `run-roundtrip.sh`
that sweeps its `sources/*.s` through `tools/run-roundtrip.sh <corpus>`;
the `make test-{core,symbols,operands,paged}` / `ci-{core,symbols,operands,paged}`
(and `-prod`) targets drive them inside the dev container. The four corpora
also have `-prod` variants in CI that re-run the same fixtures against the
production assembler build.
