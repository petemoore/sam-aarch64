# sam-aarch64

An aarch64 (ARMv8-A 64-bit) assembler that runs on a SAM Coupé.

A self-hosting development environment for writing aarch64 assembly code on
a SAM Coupé. The eventual product is a single Z80 program that runs on the
SAM and combines:

1. A visual editor for aarch64 source code
2. An aarch64 assembler producing flat binaries
3. A TFTP server that serves the assembled binary directly to a Raspberry Pi
   over a Quazar Trinity ethernet interface

The Pi netboots the kernel built on the SAM. The development loop closes
without ever leaving the SAM Coupé.

## Status

M0–M7 are complete; **M8 is the active milestone** (see
`docs/notes/m8-status.md`). The SAM-side Z80 assembler, running in
SimCoupé, byte-matches GNU `as + ld -Ttext=0 + objcopy -O binary`
end-to-end for the M3–M6 fixture corpora, and assembles the **full
spectrum4 `release.bin` (21 752 bytes) byte-identical to GNU** on
real SAM paging. A full aarch64 **disassembler** runs on the SAM
(oracle-verified word-for-word against the Go authority), and the
source format is the compact **`.tbn` v2** instruction-overlay
(44 KB for the full release, with a separable editor region — the
foundation for the Phase 2 on-SAM editor). The `m6-release` CI job
stands guard as a hermetic 3-way byte-match (GNU == our Go toolchain
== our Z80/SAM toolchain). See `docs/ROADMAP.md` for the milestone
index and `docs/specs/` for design documents.

The round-trip gates pass under every environment we exercise:
GitHub Actions on `ubuntu-latest` (inside the dev image published to
`ghcr.io/petemoore/sam-aarch64-dev` on every push), the dev image
locally under Docker on both `linux/amd64` and `linux/arm64`, and
natively on macOS against a locally-built patched SimCoupé.

## Local development

The same image CI uses is published publicly. Pull it and run the
round-trip sweep inside:

```bash
docker pull ghcr.io/petemoore/sam-aarch64-dev:latest
docker run -d --name sam-aarch64-ci \
    -v "$PWD:/work" -w /work \
    ghcr.io/petemoore/sam-aarch64-dev:latest sleep infinity
docker exec sam-aarch64-ci bash -lc 'cd /work && make ci-m3 ci-m4 ci-m5 ci-m6 && tools/run-m6-release-gate.sh'
```

The image is multi-arch (`linux/amd64` + `linux/arm64`); Docker picks
the variant matching your host. SimCoupé, pyz80, samfile, the
aarch64 cross binutils, and the SimCoupé ROM resources are all
pre-installed in it — the round-trip targets work against it with no
further setup.

For native macOS (no Docker), see the "Native macOS" section of
`docs/notes/headless-simcoupe.md`; setup is a one-time brew + CMake
step.

## Repository layout

```
docs/
├── specs/        Design documents (vision + per-phase specs)
├── plans/        Per-milestone implementation plans
├── notes/        Technical references (paging, disk format) + the iN/qN registries
├── comet/        COMET assembler reference: PDF manual, decoded source
├── sam/          SAM Coupé hardware refs: tech manual, user guide, ROM disasm
└── saa1099/      SAA-1099 sound chip datasheet (for future chiptune work)

reference/
├── arm-mra/         ARM Machine Readable Architecture XML (encoder-table source)
├── comet-disk/      Original COMET 1.44" disk, files extracted as-is
├── samdos/          SAMDOS 2 binary (disk building)
└── comet-decoded/   Same files run through Simon Owen's comet2txt to give
                    plain-text Z80 source — for study and selective porting

src/             Z80 assembler source for the new tool (Phase 1: assembler)
tools/           Mac-side helpers (encoder-table generator, test harness,
                 SimCoupé dev-container recipe)
scripts/         Build-gate helpers (code budget, release pipeline)
tests/           Test fixtures and round-trip scripts
build/           Build outputs (gitignored)
```

## Validation strategy

Every aarch64 instruction we emit is round-tripped through
`aarch64-none-elf-as`. If GNU `as` and our assembler disagree on the bytes
for the same input, our assembler is wrong.

## External tools and references

- `~/git/comet2txt` — Simon Owen's COMET source detokeniser (used to
  populate `reference/comet-decoded/`).
- `~/git/trinload` — Simon Owen's SAM netboot loader. Source for the
  ENC28J60 ethernet driver and IP/UDP stack.
- `~/git/samfile` — Pete's tool for adding/extracting files in `.mgt` SAM
  disk images. Used by the test harness to round-trip source files into
  SimCoupé.
- pyz80 (https://github.com/simonowen/pyz80) — Mac-side Z80 assembler
  used to build this tool.
- SimCoupé (https://simonowen.com/simcoupe/) — SAM Coupé emulator used
  for automated test runs before deploying to real hardware.
- COMET manual: `docs/comet/comet_v1-3_manual.pdf`

## Phase plan

- **Phase 1** — Standalone assembler. Source from disk, binary to disk.
  Validates encoding against `aarch64-none-elf-as`.
- **Phase 2** — Visual editor on the SAM. Replaces "load source from
  external disk" with on-SAM editing.
- **Phase 3** — TFTP server. Replaces "transfer binary out manually" with
  "Pi pulls directly from the SAM over the LAN". May also serve Pi
  firmware files from SD card on the Trinity.
- **Future** — Terminal app over TCP, so the SAM can be a daily-driver
  workstation for SSH-tunnel-from-Mac sessions.
