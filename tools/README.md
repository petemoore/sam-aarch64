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
| `enctab-gen/` | Generates the binary `enctab.enc` form table (and can regenerate `aarch64enc/data.go`) from the vendored ARM MRA snapshot. The `.enc` mirrors the Go runtime form table that M3 loads. | `enctab-gen`, `enctab` |
| `build-disk/` | Builds the round-trip `.mgt` disk image: assembler binary + `enctab.enc` + off-axis payloads (test_mem, cluster, paged-call, sysreg-data). The production disk builder. | `build-disk`, `disk` |
| `run-simcoupe.sh` | Runs SimCoupé headless on a `.mgt` (timeout + `-exitonhalt`), capturing the OUT/status. Shared by every round-trip driver and the release gate. | (called by `run-roundtrip.sh` + `run-release-gate.sh`) |
| `run-roundtrip.sh` | Unified end-to-end round-trip driver (sam-aarch64 → disk → SimCoupé → extract → byte-compare vs GNU) for all four SimCoupé corpora. Called as `run-roundtrip.sh <corpus> <fixture.s>`. | (called by `tests/{core,symbols,operands,paged}/run-roundtrip.sh`) |
| `run-release-gate.sh` | Drives the spectrum4 `release.bin` byte-match on SAM (the release CI gate). | — |
| `revendor-release.sh` | Refresh the vendored stripped release `.tbn` fixture from a spectrum4 checkout. | — |
| `session-handover.sh` | Agent-session infrastructure (SessionStart hook via `.claude/settings.json`) — not build tooling. Warns about dated filenames and in-flight plans. | — |
| `Dockerfile.dev` | Dev-container image (pyz80 + SimCoupé + Go). SimCoupé is built from upstream v1.2.16 source, which ships `-exitonhalt` natively. SimCoupé runs only in this container. | — |

## Dev tools

| Path | Purpose | `make` target |
|------|---------|---------------|
| `z80-test-harness-go/` | Go harness over `koron-go/z80` running `src/` fixtures at ~1 ms each for fast inner-loop feedback. **Not a CI gate** — it can crash or mislead; SimCoupé wins on disagreement. See its `README.md` and `SCOPE.md`. | — |

