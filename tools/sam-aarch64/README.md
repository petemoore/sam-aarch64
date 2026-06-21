# `tools/sam-aarch64` — the integrated host assembler

One binary over three libraries: `frontend/` (text → in-memory symbolic IR:
lexer, parser, preprocessor, `-flatten`, strip passes), `assemble/`
(pass 1 / pass 2 / compaction to the `.tbn` overlay), `render/` (overlay →
text). Modes: source → {binary, compact `.tbn`} (`--emit-tbn`), `.tbn` →
binary, `--render`, `-E`. The symbolic IR is in-memory only — never
serialized (`docs/specs/i48-syntactic-encoder-design.md`, Decision A).

Entry point: `main.go`. Build: `make sam-aarch64` (→ `build/sam-aarch64`);
exercised by `make ci-format` / `ci-encoder` and every round-trip driver;
`make release-stripped-tbn` builds the release fixture `.tbn`.
See `docs/ARCHITECTURE.md` §2.2.

For a byte-level worked example tracing one tiny `.s` through every
representation (`.tbn` bytes, binary, disasm, `-flatten`, comment stripping,
and the round-trips), see
[`docs/toolchain-pipeline-walkthrough.md`](../../docs/toolchain-pipeline-walkthrough.md).
