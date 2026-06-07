# `tests/` — fixture corpora

The corpora are **cumulative feature tiers**: each tier's fixtures exercise
the new capability of that tier on top of everything below it. Every
fixture `.s` is assembled by our toolchain(s) and byte-compared against GNU
(`as` [+ `ld -Ttext=0` from `m4` up] + `objcopy -O binary`) — see
`docs/ARCHITECTURE.md` §7 for the full pipeline.

| Corpus | Fixtures | Exercises | Runs on |
|--------|----------|-----------|---------|
| `m1/` | 36 | `.tbn` format round-trip + host-assembler unit checks | host (`make ci-m1`/`ci-m2`) |
| `spectrum4/` | 29 | real spectrum4 kernel sources through the host toolchain | host (`make ci-m2`) |
| `disasm/` | — | `aarch64dec` vs binutils `objdump` oracle on the vendored release | host (`make ci-disasm`) |
| `m3/` | 9 | core Z80 emit (read `.tbn`, encode, HSAVE) on SimCoupé | container (`make ci-m3`) |
| `m4/` | 5 | symbols, two-pass, expression evaluation | container (`make ci-m4`) |
| `m5/` | 20 | compound operands + directives | container (`make ci-m5`) |
| `m6/` | 19 | paged IN/OUT at scale (>16 KB output) | container (`make ci-m6`) |
| `m6/release/` | 1 pair | the vendored spectrum4 release (`release.s` + GNU `release.img`) — the 3-way release gate | container (`tools/run-m6-release-gate.sh`) |

**Running a sweep:** each SimCoupé corpus dir carries a `run-roundtrip.sh`
that sweeps its `sources/*.s` through the matching `tools/run-m*-roundtrip.sh`
driver; the `make test-m{3..6}` / `ci-m{3..6}` (and `-prod`) targets drive
them inside the dev container. The `m{3..6}` corpora also have `-prod`
variants in CI that re-run the same fixtures against the production
assembler build.
