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

M0 (toolchain bootstrap) round-trip passes locally — pyz80 → patched
SimCoupé → samfile → GNU `as` is wired end-to-end and the stub's
HSAVE-written `OUT` byte-matches `aarch64-none-elf-as`. PR #1 is the
active draft on `m0-toolchain-bootstrap`; pending GitHub Actions
green-on-amd64 before merging to main. See `docs/specs/` for design
documents and `docs/plans/` for the M0 plan.

## Repository layout

```
docs/
├── specs/        Design documents (vision + per-phase specs)
├── plans/        Per-milestone implementation plans
├── notes/        Spike outputs (SimCoupé batch mode, SAM file I/O)
├── comet/        COMET assembler reference: PDF manual, decoded source
├── sam/          SAM Coupé hardware refs: tech manual, user guide, ROM disasm
├── saa1099/      SAA-1099 sound chip datasheet (for future chiptune work)
├── aarch64/      ARM ISA notes and (later) generated encoder tables
└── trinity/      Quazar Trinity hardware programming notes

reference/
├── comet-disk/      Original COMET 1.44" disk, files extracted as-is
└── comet-decoded/   Same files run through Simon Owen's comet2txt to give
                    plain-text Z80 source — for study and selective porting

src/             Z80 assembler source for the new tool (Phase 1: assembler)
tools/           Mac-side helpers (encoder-table generator, test harness,
                 vendored simcoupe-exitonhalt patch)
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
