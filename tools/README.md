# `tools/` — index

This directory holds the Go-side toolchain, build helpers, and dev tools that support the SAM-side Z80 assembler in `src/`. Tools fall into two buckets:

- **Production toolchain** — part of the build / CI critical path. Touching these affects shipped behaviour.
- **Dev tools** — aid agent iteration but are *not* CI gates (SimCoupé is the only gate).

`make` targets below are the ones defined in the repo-root `Makefile`; tools without a target are invoked directly by other tools or scripts.

## Production toolchain

| Path | Purpose | `make` target |
|------|---------|---------------|
| `sam-aarch64/` | The **integrated host assembler** — one binary built from three Go-authoritative shared libs (`frontend` text→symbolic-IR + strip, `assemble` pass1/pass2/compact/overlay, `render` overlay→text). Modes: `source → {binary, compact .tbn}` (`--emit-tbn`), `.tbn → binary`, `--render` (`.tbn → text`), `-E` (preprocess); supports `-flatten`/`-strip-comments`/`-strip-data`/`-I`/`-origin`/`--dump-usage`. Replaces the former `text2bin`/`refenc`/`bin2text` trio; the symbolic record stream is an in-memory IR, never serialized to disk. | `sam-aarch64`, `release-stripped-tbn` |
| `sam-aarch64-format/` | Go library (`package format`) implementing the binary tokenised `.tbn` format (v2 instruction-overlay). Imported by `sam-aarch64` (frontend/assemble/render) + `aarch64dec`. | (library; covered by `test-format`/`test-encoder`) |
| `aarch64enc/` | Go library: the aarch64 instruction encoder (form table + per-slot encoders) + the overlay `Fold`/`ZeroSlot` slot rules. The encoding *authority* that the Z80 encoder is ported from. `data.go` is MRA-projected; `manual_forms.go` is hand-curated. | (library; `enctab-regen-source` regenerates `data.go`) |
| `aarch64dec/` | Go library + CLI: the aarch64 disassembler the Z80 `src/disasm.asm` is ported from. The decoding *authority*, oracle-gated against GNU binutils `objdump`. | `aarch64dec`, `test-disasm`, `ci-disasm` |
| `tables-gen/` | Generates the Z80 data tables whose authority is Go source: the binary `enctab.enc` form table (and `aarch64enc/data.go`) from the vendored ARM MRA snapshot, plus `src/sysreg_tables.inc` (sysreg/pstate/dc/tlbi) from `sam-aarch64-format/sysregs.go`. Generated tables mirror the Go authority by construction. | `tables-gen`, `enctab`, `tables` |
| `build-disk/` | Builds the round-trip `.mgt` disk image: assembler binary + `enctab.enc` + off-axis payloads (test_mem, cluster, paged-call, sysreg-data). The production disk builder. | `build-disk`, `disk` |
| `run-simcoupe.sh` | Runs SimCoupé headless on a `.mgt` (timeout + `-exitonhalt`), capturing the OUT/status. Shared by every round-trip driver and the release gate. | (called by `run-roundtrip.sh` + `run-release-gate.sh`) |
| `run-roundtrip.sh` | Unified end-to-end round-trip driver (sam-aarch64 → disk → SimCoupé → extract → byte-compare vs GNU) for all four SimCoupé corpora. Called as `run-roundtrip.sh <corpus> <fixture.s>`. | (called by `tests/{core,symbols,operands,paged}/run-roundtrip.sh`) |
| `run-release-gate.sh` | Drives the spectrum4 `release.bin` byte-match on SAM (the release CI gate). | — |
| `run-disasm-roundtrip.sh` | Drives the Go encoder/decoder round-trip gate: two pure-Go round-trips (binary + compact `.tbn` overlay render) over the SimCoupé fixture corpora and `release.s`. | `ci-disasm-roundtrip` |
| `revendor-release.sh` | Refresh the vendored stripped release `.tbn` fixture from a spectrum4 checkout. | — |
| `build-spectrum4-release.sh` | Builds spectrum4 `release.img` from sources via our toolchain, then byte-compares against the GNU oracle. Requires `aarch64-none-elf-{as,ld,objcopy}` on `PATH`. | — |
| `check-code-budget.sh` | Fails the build when an assembler variant's code end reaches the `&C000` stack-page cliff; prints headroom. Runs inline after every assembler build and as `make check-budget`. | `check-budget` |
| `check-doc-links.sh` | Verifies every relative markdown link in the entry docs and under `docs/`, `tools/`, `tests/` resolves to an existing path; skips URLs and immutable blob-SHA links. | `check-doc-links` |
| `check-no-silent-skips.sh` | i253 guard — every Go-test `t.Skip*` must reference the one sanctioned `SKIP_PRIVATE_TESTS` gate; a silent skip on a missing precondition fails the build. | `check-no-silent-skips` |
| `check-hosttest-carveouts.sh` | i231 emulation-first ratchet — counts opening `if defined(NETBOOT_HOSTTEST)==0` carve-outs (code excluded from the host/emulation build → ships to hardware un-emulated) per file and exact-matches `hosttest-carveout-allowlist.txt`; a new carve-out fails, eliminating one requires ratcheting the ledger down to zero. | `check-hosttest-carveouts` |
| `check-trinity-authority.sh` | i273 authority guard — each Trinity-hardware Go model (EEPROM/SD/ENC/B-DOS seam) must be in `trinity-authority-ledger.txt` naming the in-repo SAM/Colin authority it is DERIVED FROM (never the reverse); a marked-but-unledgered model, a stale entry, or a missing authority path fails. | `check-trinity-authority` |
| `session-handover.sh` | Agent-session infrastructure (SessionStart hook via `.claude/settings.json`) — not build tooling. Warns about dated filenames and in-flight plans. | — |
| `Dockerfile.dev` | Dev-container image (pyz80 + SimCoupé + Go). SimCoupé is built from upstream v1.2.16 source, which ships `-exitonhalt` natively. SimCoupé runs only in this container. | — |

## Dev tools

| Path | Purpose | `make` target |
|------|---------|---------------|
| `comment-bench/` | Benchmarks Z80-feasible compression schemes (word dict, BPE, Huffman, LZ77/ZX0-style, flate) against the full release comment corpus from the unstripped `.tbn`. Reports ratio × decoder footprint × capacity in pages. See `docs/notes/comment-compression-research.md`. | `comment-bench` |
| `z80-test-harness-go/` | Go harness over `koron-go/z80` running `src/` fixtures at ~1 ms each for fast inner-loop feedback. **Not a CI gate** — it can crash or mislead; SimCoupé wins on disagreement. See its `README.md` and `SCOPE.md`. | — |
| `test-manifest.sh` | Snapshot tool: enumerates CI jobs, Go test names, fixture corpora, boot self-tests, and round-trip sweeps into a single report file. Queries GitHub Actions via `gh`. | — |

