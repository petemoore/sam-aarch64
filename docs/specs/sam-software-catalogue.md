# SAM software catalogue — what each piece is, where it lives, how they relate

**Purpose:** a single living index of every key piece of SAM Coupé software this
project touches — system ROMs, Colin's forked ROM, the Trinity EEPROM, the DOS
layers (SAMDOS / B-DOS), trinload, and our own assembler/editor + netboot stack.
For each piece it records *what it is* (one line), *where the binary/source
lives*, *where the disassembly lives and whether it is annotated or raw*, the
*deep doc* that authoritatively covers it, and *how it relates* to the others.

This is an **index, not a textbook.** Each entry points at the authoritative deep
doc and summarises in a line or two; it deliberately **does not restate** their
content (single-source-of-truth, repo `CLAUDE.md`). When a fact lives in a deep
doc, follow the link — do not copy it here, or the two will drift.

The headline relationship to keep straight: **B-DOS *replaces* SAMDOS** (it is
not an add-on); and the boot chain is **forked ROM → EEPROM bootblock → B-DOS →
trinload / our netboot**. Both are spelled out in "How the pieces relate" below.

> Path note: `~/sam-archive/` and the `~/git/*` clones are **local to Pete's
> machines**, not in this repo (redistribution rights vary; the hardware
> captures are Colin Piggot's proprietary artifacts). Paths below are where they
> live on Pete's hosts. The deep docs *are* in this repo.

---

## At a glance

| Piece | What it is | Binary location | Disassembly / source + state | Deep doc |
|-------|-----------|-----------------|------------------------------|----------|
| SAM system ROM | Stock SAM Coupé ROM (BASIC + startup stripes) | none held locally | **none** — we have no SAM ROM binary or disasm; known only conceptually | — (see note in §1) |
| Colin's forked ROM | Patched system ROM that fetches boot code from the Trinity EEPROM | `~/sam-archive/samboot-capture/rom0.bin` + `rom1.bin` (+ `rom.bin` = 32 KB) | boot path analysed (not a full ROM disasm) | [samboot-bootblock-analysis.md](../notes/samboot-bootblock-analysis.md), [samboot.md](samboot.md) |
| Trinity EEPROM | 128 KB EEPROM holding the forked-B-DOS bootblock + network config; layout defined by the Trinity disk's net-config program | `~/sam-archive/samboot-capture/eeprom.bin` (+ `eep0..7.bin`) | layout decoded; chunk-ordering subtlety documented | [samboot-bootblock-analysis.md](../notes/samboot-bootblock-analysis.md), [../notes/trinity-capabilities.md](../notes/trinity-capabilities.md) |
| trinload | Network code-loader for SAM over Trinity (UDP); our reactive bootstrap | `~/git/trinload/trinload.mgt` (+ `.asm` source) | **annotated Z80 source** in `~/git/trinload/*.asm` | [../notes/netboot-trinity-testing.md](../notes/netboot-trinity-testing.md) |
| SAMDOS | The stock SAM disk operating system (SAMDOS 2) | n/a (in stock disks) | **merged annotated source** in `~/git/samdos/src/*.s` | [samdos-file-io.md](samdos-file-io.md) |
| B-DOS | Enhanced DOS that **replaces** SAMDOS; mainline + Trinity 1.5t fork | `~/sam-archive/bdos/` (1.4e/1.5a/1.6c/1.7i/n/q + AL 1.5a; Trinity 1.5t in `~/sam-corpus/`) | 1.5a: **public Z80 source**; 1.5t: **annotated disasm** (private) — both under `~/sam-archive/bdos/analysis/` | [../notes/bdos-version-landscape.md](../notes/bdos-version-landscape.md), [../notes/bdos-trinity-fork-analysis.md](../notes/bdos-trinity-fork-analysis.md), [../notes/trinity-sd-z80-interface.md](../notes/trinity-sd-z80-interface.md) |
| Our assembler/editor | The aarch64 assembler + editor we are building (Z80) | `src/*.asm` (built to disk by the Makefile) | **the source is ours** — fully readable + commented | [../ARCHITECTURE.md](../ARCHITECTURE.md), [../../src/README.md](../../src/README.md) |
| Our netboot stack | Our TFTP/HTTP/TLS client+server + SD/Trinity drivers (Z80) | `src/netboot/*.asm` | **ours** — readable + commented | [../specs/phase3-tftp-design.md](phase3-tftp-design.md), [../notes/netboot-trinity-testing.md](../notes/netboot-trinity-testing.md) |
| Go authority + harnesses | Encoder/decoder authority + emulation harnesses (oracle / fast loop) | `tools/` (Go) | **ours** — readable | [../ARCHITECTURE.md](../ARCHITECTURE.md) §3, [../../tools/README.md](../../tools/README.md) |

---

## The pieces

### 1. SAM system ROM (stock)

The untarnished SAM Coupé ROM — BASIC interpreter, boot stripes, the standard
firmware. **We hold the genuine official v3.0 binary** (i219, 2026-06-24):
`~/sam-archive/samboot-capture/rom_official_v30.bin` (32 KB, md5
`1bc4fa10a9bb05a036e854fa60d151d9`, version byte `&000F=&1E`) — Dr Andy Wright's
original image, published with permission as `roms/ROM30` in
[simonowen/samrom](https://github.com/simonowen/samrom). SimCoupé's bundled dump
(`rom_stock.bin`, `&000F=&1F`) is the **same ROM** with a 4-byte vanity bump
(version stamp + a cosmetic `"plc"`→`"PLC"`); there is no functional v3.1. See
`~/sam-archive/samboot-capture/README.md` for the full ROM index (stock variants,
the fork, and the EEPROM capture) and the banner/é note. The relevant firmware
internals are disassembled/annotated where the fork touches them in
`docs/specs/samboot-fork-analysis.md`; a full standalone stock-ROM disassembly is
not yet written. The ZX Spectrum ROM disassemblies under
`~/git/notes/zxspectrum_roms/` are a *different machine's* ROMs and are **not** the
SAM ROM — do not confuse them.

### 2. Colin's forked ROM

A patched SAM system ROM (Colin Piggot's) whose job is to **fetch boot code from
the Trinity EEPROM** instead of the usual disk-boot path — this is what makes a
Trinity SAM boot B-DOS off the EEPROM bootblock. Captured from Pete's hardware
2026-06-21:

- `~/sam-archive/samboot-capture/rom0.bin` — low 16 KB (ROM0), the half mapped at
  power-on.
- `~/sam-archive/samboot-capture/rom1.bin` — high 16 KB (ROM1). Its capture was
  the hard part: the original dumper crashed the SAM (scratch-page clobber); the
  i188 redesign + i181 paged harness fixed it in emulation, then i87a-b2
  recaptured it cleanly on hardware (see `CAPTURE-NOTES.txt`).
- `~/sam-archive/samboot-capture/rom.bin` — `rom0 + rom1` = the full 32 KB ROM.

We have the **boot path analysed** (which routines run, how the EEPROM bootblock
is located and pulled), not a line-by-line ROM disassembly. Authority:
[samboot-bootblock-analysis.md](../notes/samboot-bootblock-analysis.md) (the mechanism the
i197 work cracked) and [samboot.md](samboot.md) (the controlling overview/index
for the whole SAMBOOT endeavour).

### 3. Trinity EEPROM contents

The 128 KB EEPROM on the Trinity interface holds the **forked-B-DOS bootblock**
plus the **network configuration** ("Trinity Network" name, SAM MAC). Its layout
is defined by the Trinity disk's network-config program. Captured 2026-06-21:

- `~/sam-archive/samboot-capture/eeprom.bin` — full 128 KB.
- `~/sam-archive/samboot-capture/eep0.bin … eep7.bin` — the eight 16 KB chunks,
  concatenated into `eeprom.bin`.

**Critical addressing subtlety** (don't get this wrong): `eeprom.bin` is
**chunk-ordered, not device-linear**. File offset 0 = Trinity device address
`&2000` = chunk 1 = the Boot Block. Any emulator/analysis loading the file must
*un-rotate* it to device-linear first. The exact rule and the worked examples
(MAC, "Trinity Network" offsets) are in `CAPTURE-NOTES.txt` and decoded in
[samboot-bootblock-analysis.md](../notes/samboot-bootblock-analysis.md); the un-rotation
code is `deviceLinearEEPROM` in `tools/netboot-oracle/z80/`. The Trinity port
mechanics around the EEPROM are in [../notes/trinity-capabilities.md](../notes/trinity-capabilities.md).

### 4. trinload

A network code-loader for the SAM over the Trinity interface: it listens on UDP,
accepts data blocks + an execute request, and runs received code — our
**reactive bootstrap** (the i129 lineage; SAM has no native ethernet, so Trinity
is required). Source + built disk:

- `~/git/trinload/trinload.asm` (+ `eeprom.asm`, `encdrv.asm`) — **annotated Z80
  source** (it is Pete's own code).
- `~/git/trinload/trinload.mgt` — the bootable disk.
- `~/git/trinload/ReadMe.txt` — the protocol (request types `?` / `@` / `X`).

We also vendor a copy under `src/netboot/trinload.asm` for the netboot work. Used
operationally to drive hardware captures (see the capture procedure in
`CAPTURE-NOTES.txt`, which `make netboot-dumper` + a trinload push +
TFTP pull). Hands-on guide: [../notes/netboot-trinity-testing.md](../notes/netboot-trinity-testing.md).

### 5. SAMDOS

SAMDOS 2 — the **standard** SAM Coupé disk operating system, providing the
file-I/O hook layer (HGTHD / HLOAD / HSAVE / HOFLE / HSBYT …) that every SAM disk
program uses. Disassembly/source:

- `~/git/samdos/src/*.s` — a **merged annotated source**: the public COMET-format
  source with the disassembled final `samdos2` binary merged in and changes
  surfaced (see `~/git/samdos/README.md` for the reconstruction method).

How our project uses it: the assembler's file read/write is written against the
**SAMDOS-compatible hook surface**, documented in
[samdos-file-io.md](samdos-file-io.md) (the READ-side paging trampoline + the
WRITE-side idiom, with register/error-path facts). That hook layer is the
**portability boundary** to B-DOS (§6).

### 6. B-DOS

B-DOS **replaces** SAMDOS (it is *not* a SAMDOS extension — it is a drop-in
substitute DOS that boots in SAMDOS's place and re-implements the hook surface).
This is the headline confusion this catalogue exists to settle. B-DOS has **two
permanently parallel lineages forking at 1.5a**: the Atom/Atom-Lite mainline
(→ 1.7q) and the **Trinity fork 1.5t** (adds SD/SDHC). Binaries + source:

- `~/sam-archive/bdos/` — releases 1.4e, 1.5a, 1.6c, 1.7i/n/q, **AL 1.5a** (the
  Atom-Lite build for SimCoupé CI), plus the 1.7n manual PDF.
- `~/sam-archive/bdos/analysis/` — the analysis bench: **`bdos15a.src.txt` is the
  only public B-DOS Z80 source** (Edwin Blink's, the 1.5a fork point);
  **`bdos15t-beta6.annotated.dis` is Colin's annotated disasm of the Trinity
  fork** (private — cite by line/address, never copy). Also `ANALYSIS.md`,
  `.dis`/`.src.txt` for several versions, and a `tools/` dir.
- Trinity **1.5t beta 6** binary itself is not redistributable; held only inside
  `~/sam-corpus/disks/trinity.mgt`.

Deep docs: [../notes/bdos-version-landscape.md](../notes/bdos-version-landscape.md)
(which version for which target + why), [../notes/bdos-trinity-fork-analysis.md](../notes/bdos-trinity-fork-analysis.md)
(how 1.5t differs from 1.5a; does the fork touch our hooks?), and
[../notes/trinity-sd-z80-interface.md](../notes/trinity-sd-z80-interface.md) (the
SD-card SPI port authority extracted from the fork's annotated disasm). The
shared hook layer (§5) is why code written against SAMDOS hooks transfers to
B-DOS.

### 7. Our own software (assembler/editor + netboot + Go authority)

The project's own SAM software — fully readable, commented source, not something
we reverse-engineer:

- **Assembler + editor** (`src/*.asm`): the aarch64 assembler (`assembler.asm`,
  `asmlex.asm`/`asmparse.asm`, `encoder.asm`/`insn_encode.asm`, `disasm.asm`,
  `expr_eval.asm`, `symbols.asm`, `litpool.asm`), the editor model
  (`editmodel.asm`), and the paging trampoline (`loader.asm`, `paged_bodies.asm`,
  `pagepool.asm`, etc.). Overview: [../ARCHITECTURE.md](../ARCHITECTURE.md);
  per-dir: [../../src/README.md](../../src/README.md).
- **Netboot stack** (`src/netboot/*.asm`): the TFTP/HTTP/TLS client + server
  (`tftp_*`, `http_*`, `tls_*`, the crypto `chacha20`/`poly1305`/`sha256`/
  `x25519`/`hkdf`), the ENC28J60 + Trinity SD drivers (`encdrv.asm`,
  `enc_link.asm`, `sd_*`, `csd_probe.asm`), DHCP, `samboot_*`, and the bundled
  `trinload.asm`. Designs: [../specs/phase3-tftp-design.md](phase3-tftp-design.md);
  hardware guide: [../notes/netboot-trinity-testing.md](../notes/netboot-trinity-testing.md).
- **Go authority + harnesses** (`tools/`): the encoder/decoder authority
  (`aarch64enc`, `aarch64dec`, `tables-gen`) the Z80 side ports from, the fast
  inner-loop harness (`z80-test-harness-go`), the netboot oracle/emulator
  (`netboot-oracle`), and `build-disk`. Authority model:
  [../ARCHITECTURE.md](../ARCHITECTURE.md) §3; per-dir: [../../tools/README.md](../../tools/README.md).

### 8. Other SAM materials (references, not infrastructure)

Surveyed while building this catalogue; included for findability:

- **`~/git/samfile`** — Go tool for manipulating individual files inside SAM disk
  images (BASIC ↔ text, etc.). A host *tool we use*, not on-SAM software. Docs in
  `~/git/samfile/docs/`.
- **`~/git/cjs-sam-remake`** — a native SAM port of the game *CJ's Elephant
  Antics* (`disasm/` holds the original Spectrum-binary disasm). Pete's separate
  SAM project; not part of this project's stack — listed so it isn't mistaken for
  one of ours.
- **`~/git/spectrum4`** — the aarch64 program this project assembles (the *input*
  to our toolchain; aarch64, not Z80/SAM software).
- **`~/sam-archive/trinity-docs/`** — OCR'd Trinity hardware manuals (photos +
  extracted `text/`); the documentation companion to the trinity deep docs.
- **`~/sam-archive/editor-ui-research/`** — screenshots of period SAM/8-bit
  assemblers/editors for the editor UI design (COMET, GIMon, etc.).

---

## How the pieces relate

**The boot chain** (what actually happens when Pete powers on his Trinity SAM):

```
Colin's forked ROM (§2)
      │  patched to fetch boot code from the EEPROM instead of disk
      ▼
Trinity EEPROM bootblock (§3)   ← chunk 1, file offset 0 = device &2000
      │  the forked-B-DOS bootblock + network config (MAC, "Trinity Network")
      ▼
B-DOS (§6, the 1.5t Trinity fork)   ← replaces SAMDOS; adds the SD-card backend
      │  presents the SAMDOS-compatible hook surface (§5)
      ▼
trinload (§4) / our netboot stack (§7)   ← loaded/served over the network;
                                            runs our assembler/editor
```

**SAMDOS ↔ B-DOS** is a **replacement**, not a layering: B-DOS substitutes for
SAMDOS and re-implements the same hook surface (HGTHD/HLOAD/HSAVE/…). Because the
hooks match, our assembler's file I/O — written against the SAMDOS hooks
([samdos-file-io.md](samdos-file-io.md)) — runs unchanged on B-DOS. That shared
hook layer is the portability boundary, and it is why "SAMDOS vs B-DOS" matters
only at boot/backend level, not in our day-to-day file code.

**The forked ROM ↔ EEPROM ↔ B-DOS** triangle: the forked ROM only knows *how to
pull a bootblock from the EEPROM*; the EEPROM holds *which* DOS (the forked B-DOS
1.5t) plus the network identity; B-DOS then owns the running system. The captured
binaries (§2, §3) are what let us emulate this whole path faithfully (the i190
shared-core + i190a "load the real ROM/EEPROM" work).

---

## Preservation artifacts (irreplaceable hardware captures)

These are **one-off captures from Pete's real SAM + Trinity** — Colin Piggot's
proprietary, non-redistributable artifacts, kept in `~/sam-archive/` and **never
committed to the repo**. If lost they require re-capturing on hardware. Record
here so they are never silently dropped:

| File | Size | What | sha256 (first 12) |
|------|------|------|-------------------|
| `~/sam-archive/samboot-capture/rom0.bin` | 16 KB | forked system ROM, low half (ROM0) | `2cfcfb29b325` |
| `~/sam-archive/samboot-capture/rom1.bin` | 16 KB | forked system ROM, high half (ROM1) | `02012e3ffb48` |
| `~/sam-archive/samboot-capture/rom.bin` | 32 KB | rom0 + rom1, full forked ROM | `13877fba15fc` |
| `~/sam-archive/samboot-capture/eeprom.bin` | 128 KB | full Trinity EEPROM (chunk-ordered) | — |
| `~/sam-archive/samboot-capture/eep0..7.bin` | 8×16 KB | the EEPROM chunks (concatenate → eeprom.bin) | — |

Full provenance, capture procedure, sha256s, and the chunk-ordering rule are in
`~/sam-archive/samboot-capture/CAPTURE-NOTES.txt` (the authority — this table is
a pointer, not a copy). The B-DOS binaries/source under `~/sam-archive/bdos/`
are *re-downloadable* preservation copies (provenance in `~/sam-archive/README.md`),
not one-off captures, but are likewise kept out of the repo.
