# sam-aarch64

An ARMv8-A (aarch64) assembler that runs on a SAM Coupé — a 6 MHz,
8-bit Z80 home computer from 1989 — and produces binaries
byte-identical to GNU binutils.

The end goal is a self-hosted development loop that never leaves the
SAM: write aarch64 source in an editor on the SAM, assemble it on the
SAM, and serve the binary over ethernet so a Raspberry Pi netboots it
as its kernel. A 1989 machine building and shipping code for a CPU
three decades its junior.

## What it does today

- **Assembles real aarch64 on the SAM.** The complete spectrum4
  bare-metal kernel (21 752 bytes of ARMv8-A) assembles on the SAM —
  under SimCoupé and on real hardware — byte-identical to GNU
  `as + ld + objcopy`. The 256 KB of banked RAM does the heavy
  lifting: paged source-in, paged output, and encoder tables swapped
  through a 32 KB visible window.
- **Disassembles too.** A full aarch64 disassembler runs on the SAM,
  decoding word-for-word identically to `objdump` (its alias choices
  included) across the entire kernel.
- **Compact source format.** Source travels as `.tbn`, an
  instruction-overlay format that fits the whole kernel — comments
  and all — in well under the machine's paged memory budget, with an
  editor-facing region the assembler never has to load.
- **A twin toolchain on the host.** The same assembler and
  disassembler exist in Go (`tools/`). The Go side is the reference
  implementation and test oracle; the Z80 side is its faithful port.

## How we know it's correct

Every fixture in `tests/` is assembled by our toolchains and
byte-compared against GNU binutils — if GNU and this assembler
disagree on a single byte, this assembler is wrong. The headline gate
assembles the full kernel three ways — GNU binutils, the Go
toolchain, and the Z80 toolchain running in SimCoupé — and requires
all three outputs byte-identical. CI runs the whole matrix, including
the emulated SAM, on every push.

## Try it

The CI image is public and has everything pre-installed (SimCoupé,
pyz80, samfile, aarch64 binutils):

```bash
docker pull ghcr.io/petemoore/sam-aarch64-dev:latest
docker run -d --name sam-aarch64-ci \
    -v "$PWD:/work" -w /work \
    ghcr.io/petemoore/sam-aarch64-dev:latest sleep infinity
docker exec sam-aarch64-ci bash -lc 'cd /work && make ci-core ci-symbols ci-operands ci-paged && tools/run-release-gate.sh'
```

That runs the SimCoupé round-trip suites and the three-way kernel
byte-match. The image is multi-arch (`linux/amd64` + `linux/arm64`).
For native macOS setup see `docs/notes/headless-simcoupe.md`.

## Planned

- **A visual editor on the SAM** — not just a text editor but a
  guide: per-instruction explanation panels, a register simulator you
  can seed and step, system-register documentation under a function
  key, and period-correct flourishes (the SAA1099 deserves chiptunes).
  Keyboard-driven throughout, as 1989 intended.
- **TFTP over ethernet** — a Quazar Trinity interface in the SAM
  serves the assembled kernel directly to a netbooting Raspberry Pi,
  closing the loop without a disk ever leaving the room.
- **Further out** — a terminal over TCP, so the SAM earns its place
  as a daily-driver development machine.

## Repository layout

```
docs/
├── ARCHITECTURE.md  System overview — the first read
├── specs/           Living design documents
├── plans/           In-flight implementation plans (usually empty)
├── notes/           Technical references + work tracking
├── comet/           COMET assembler reference: PDF manual
├── sam/             SAM Coupé hardware refs: tech manual, user guide, ROM disasm
└── saa1099/         SAA-1099 sound chip datasheet

reference/
├── arm-mra/         ARM Machine Readable Architecture XML (encoder-table source)
├── comet-disk/      Original COMET disk, files extracted as-is
├── comet-decoded/   The same files as plain-text Z80 source
└── samdos/          SAMDOS 2 binary (disk building)

src/             The SAM-side Z80 assembler + disassembler
tools/           Host toolchain (Go), build scripts, dev-container recipe
tests/           Fixture corpora and round-trip drivers
build/           Build outputs (gitignored)
```

`docs/ARCHITECTURE.md` explains how the pieces fit; `docs/ROADMAP.md`
tracks development state.

## Built with

- [SimCoupé](https://simonowen.com/simcoupe/) — SAM Coupé emulator;
  runs the entire test matrix.
- [pyz80](https://github.com/simonowen/pyz80) — Z80 cross-assembler
  that builds the SAM-side program.
- samfile — Pete's tool for reading/writing `.mgt` SAM disk images.
- comet2txt — Simon Owen's COMET source detokeniser; produced
  `reference/comet-decoded/`.
- [trinload](https://github.com/simonowen/trinload) — Simon Owen's
  SAM netboot loader; the reference for the planned TFTP work.
- spectrum4 — Pete's bare-metal aarch64 kernel; the real-world corpus
  this assembler is proven against.
