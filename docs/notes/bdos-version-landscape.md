# B-DOS Version Landscape

**Purpose:** Map the B-DOS releases — authorship, dates, hardware backends, SAMDOS compatibility — to settle two decisions: (a) which version to target for SimCoupé + emulated Atom Lite CI experiments, and (b) which version to target for real Quazar Trinity SD hardware. (Research: i62 prep, 2026-06-10. Companion to `trinity-capabilities.md`.)

## The short version

B-DOS has two permanently parallel lineages forking at version 1.5a: the **Atom/Atom-Lite mainline** (Edwin Blink → Martijn Groen, reaching 1.7q by 2002, adding CD-ROM/ATAPI) and the **Trinity fork 1.5t** (Colin Piggot + Chris Pile, 2008–2014, adding SD/SDHC to 64 GB). They were never merged — mainline has no Trinity support; 1.5t has no CD-ROM. **For SimCoupé CI use B-DOS AL 1.5a** (the Atom-Lite build); **for real Trinity hardware use B-DOS 1.5t beta 6** (January 2014). The hook layer is shared, so code written against SAMDOS-compatible hooks transfers between them — that hook layer is the portability boundary.

## Release timeline

```
1997–1998  Edwin Blink:   1.4d → 1.4e → 1.5a          (Atom IDE)
2001–2009  Blink/Groen:   AL 1.5a                      (Atom Lite / CF)
1999–2002  Martijn Groen: 1.6c → 1.7c → 1.7d → 1.7g
                           → 1.7i (Jun 2000) → 1.7j
                           → 1.7n (Dec 2001) → 1.7q (Jan 2002)   (Atom + CD-ROM)
2008–2014  Colin Piggot
           + Chris Pile:  1.5t beta 4 (2008, SD ≤1 GB)
                           → beta 5 (2009, faster reads)
                           → beta 6 (Jan 2014, SDHC ≤64 GB, hot-swap)   (Trinity)
```

Authorship per worldofsam.org product pages and the disk-image version strings. Edwin Blink (B-DOS originator) is also COMET's author — the same code lineage this project's SAMDOS file-io idioms were originally studied from.

## Hardware matrix

| Version | Atom (16-bit IDE) | Atom Lite (8-bit CF) | Trinity (SD/MMC) | CD-ROM | Max storage |
|---------|:---:|:---:|:---:|:---:|---|
| 1.4d–1.5a | YES | — | — | — | ~8 GB |
| AL 1.5a | — | YES | — | — | 8 GB CF |
| 1.5t beta 6 | — | — | YES | — | 64 GB SDHC* |
| 1.6c–1.7q | YES | — | — | YES | 8 GB hard cap (documented in the 1.7n manual) |

*The 64 GB figure applies to block-addressed SDHC/SDXC cards; byte-addressed MMC/SDv1 cards cap at 4 GB under the fork (the 32-bit byte address caps there). Per the B-DOS 1.5t analysis (i71, `bdos-trinity-fork-analysis.md`), beta 6 selects block vs byte addressing per card at init from the CMD58 CCS bit.

**SimCoupé:** emulates Atom (byte-swapped HDF) and Atom Lite (non-byte-swapped), with B-DOS disk detection (`HardDisk::IsBDOSDisk()`). It does **not** emulate the Trinity (no `&DC`–`&DF` port handling). The "Atom Lite? (or Trinity media under 8GB)" comment in `Base/HardDisk.cpp` reflects an *accidental* media-format compatibility: Trinity-formatted media under 8 GB carries the same signature as Atom Lite media, so SimCoupé can mount such images via the Atom Lite path. Code touching the Trinity SPI ports directly will not run in SimCoupé.

## SAMDOS compatibility (from the B-DOS 1.7n manual)

> "B-DOS offers several hook codes including the SAMDOS hookcodes. All other codes which are not included in this list are ignored by B-DOS."

Verified hook table highlights: **every hook the project's `samdos-file-io.md` idioms use — HGTHD (129), HLOAD (130), HVERY (131), HSAVE (132), HOFLE (147), HSBYT (148), HWSAD (149), HSVBK (150), HCFSM (152), HRSAD (160), HLDBK (161), HERAZ (166) — is present and SAMDOS-compatible in B-DOS.** B-DOS-only hooks: HRECORD (&9C, record selection), HVEBK (&9D), HLBYT (&9F), HDINIT (&87), HVMSAD (&86). DVARs 0/1/2/5/7 are SAMDOS-compatible; all others B-DOS-specific (DVAR 7 = B-DOS version, the documented detection idiom).

**Detection across the fork:** 1.5t still reports DVAR 7 = 5, identical to 1.5a and AL 1.5a (the B-DOS 1.5t analysis, i71, `bdos-trinity-fork-analysis.md`). DVAR-7 detection therefore treats 1.5t as 1.5a-family — good for portability (the same detection branch works), useless for fingerprinting the fork (only the banner text or a Trinity hardware probe distinguishes it).

**Load-bearing differences:**
1. Programs that POKE/CALL directly into SAMDOS addresses break under B-DOS — all file I/O must go through hooks (which is this project's idiom anyway).
2. Record selection (`HRECORD`) has no SAMDOS analogue: under SAMDOS there is only the floppy; under B-DOS, RECORD 0 = floppy and RECORD n = an 800 KB mass-storage slice formatted like a SAM floppy. The record-selection call is the one backend-conditional step; everything after it is common code.
3. Directory filler byte: SAMDOS/MasterDOS use 255 for unused entries, B-DOS uses 32 — only matters to code inspecting raw directory sectors (hook-level code is unaffected).
4. B-DOS stamps an ID in each record's first directory entry and rejects unstamped records — records must be B-DOS-formatted.
5. MasterBasic cannot be combined with B-DOS (irrelevant to machine-code use).

## The portability consequence

Write storage code against the SAMDOS hook layer only → it runs identically on: a floppy-only SAM under SAMDOS (the project's baseline tier — always supported), an emulated Atom Lite under B-DOS AL 1.5a in SimCoupé (the CI tier), and a Trinity SD under B-DOS 1.5t beta 6 (the enhancement tier). The only backend-aware step is record selection, gated on a B-DOS presence check (DVAR 7).

## Downloads and source

Preservation copies of the items below are archived locally (outside the repo).

| Version | Where |
|---------|-------|
| 1.4e / 1.5a / 1.6c / 1.7d / 1.7g / 1.7i / 1.7j / 1.7n / 1.7q | `ftp.nvg.ntnu.no/pub/sam-coupe/disks/dos/` (zip per version) |
| 1.5a, 1.6c, 1.7i, 1.7n (+ the 1.7n manual PDF) | worldofsam.org product pages / system files |
| AL 1.5a (two builds + Megaboot AL+) | worldofsam.org/products/b-dos-al-15a |
| 1.5t beta 6 | **no public download found (2026-06-10)** — distributed via the Trinity product page historically; available in private reference materials |

**Source code:** the `Bdos15a.zip` disk image carries the complete Z80 source for 1.4d, 1.4e and 1.5a ("Last BDOS version sourcecode") — the only public B-DOS source, and the direct ancestor of the 1.5t Trinity fork. No source for 1.6+, 1.7x, or 1.5t is publicly available; per samcoupe.com/samrevival.htm, the SAM Revival issue 21 cover disk carries the 1.5t source and the Trinity SD driver article source (available in private reference materials). Edwin Blink's own site (samcoupe-pro-dos.co.uk) is unreachable as of 2026-06-10 — link rot is active; hence the local preservation copies.

## Recommendations

- **SimCoupé / CI experiments: B-DOS AL 1.5a** (confidence HIGH — sam.speccy.cz documents SimCoupé Atom Lite support with AL 1.5a; SimCoupé's `AtomLiteDevice` enforces the non-byte-swapped B-DOS signature).
- **Real Trinity hardware: B-DOS 1.5t beta 6** (confidence HIGH — the only Trinity-capable B-DOS; beta 6 strictly supersedes betas 4/5).
- Development transfers between them at the hook level — **VERIFIED for the SAMDOS ↔ B-DOS AL 1.5a (Atom Lite) pair** by the i62 dual-run experiment below, and **verified-static for the Trinity 1.5t leg**: per the B-DOS 1.5t analysis (i71, `bdos-trinity-fork-analysis.md`) the hook dispatch, the 39-entry vector table, and the handler code are all 1.5a's bytes under relocation — only the sector-device layer changed. The claim no longer rests on lineage inference; runtime confirmation on real Trinity hardware remains outstanding (no emulator covers the Trinity ports).

## Empirical verification (i62)

**2026-06-11, SimCoupé v1.2.16 (CI-pinned SHA `0e8a69f`), B-DOS AL 1.5a (the 10701-byte 2009 `AL-BDOS15a` build, extracted from the worldofsam `megaboot-alplus.mgt`).** One probe binary (`tools/i62-bdos-experiment/i62test.asm`) was booted twice and passed both runs — the hook-portability claim above is now **execution-verified** for the floppy/SAMDOS ↔ Atom-Lite/B-DOS pair.

### What ran

| run | setup | transcript (printer status channel) |
|-----|-------|--------------------------------------|
| control | SAMDOS 2 boot floppy, no mass storage | `I62` · `DOS:SAMDOS` · `P2` · `P3` · `P4` · `OK` |
| B-DOS | AL-BDOS15a boot floppy + Atom Lite HDF (`-drive2 3 -atomdisk0 … -atombootrom 0`) | `I62` · `DOS:BDOS V=05 R=000B` · `P1` · `P2` · `P3` · `P4` · `OK` |

The probe exercises the full `samdos-file-io.md` sequence: DVAR-7 B-DOS detection → `HRECORD` record 1 (B-DOS branch only) → `HSAVE` a 1553-byte pattern from `&9000` → `HGTHD` (+ length validation from the `&4B50` DIFA) → `HLOAD` into `&A000` → byte-compare. The **same binary** runs both paths; the only backend-conditional step is the DVAR-7-gated `HRECORD` call. After the B-DOS run, the HDF independently contained the `I62DATA` directory entry at record 1's first directory sector and the pattern bytes at record-relative track 4 sector 1 + 9-byte header — `HSAVE` really wrote through the emulated Atom Lite.

### Hook-level findings

1. **DVAR access from machine code works exactly as the 1.7n manual documents** (`LD A,(&5BC2)` / `OUT (&FB),A` / `LD HL,(32768)` → DVAR-0 pointer): under AL 1.5a the pointer lands in `&8000-&BFFF` and `DVAR 7 = 5` (version·10−10 for 1.5x). Under SAMDOS, page offset 0 of the DOS page holds code bytes, so the pointer-range check rejects it cleanly — the probe triple-checks (pointer range, version < 20, record count ≠ 0) before taking the B-DOS branch. The detection must execute from section A/B since it transits the DOS page through section C.
2. **`HRECORD` (hook 156) semantics confirmed**: A=0 + record number in HL selects the record and switches the ambient device to D2; every subsequent hook call (`HGTHD`/`HSAVE`/`HLOAD`) then targets the record with **byte-identical call sites** to the SAMDOS floppy versions — same UIFA at `&4B00`, same DIFA at `&4B50`, same register contracts, same `set 7,d` length marker from HGTHD. No clobber or semantic deltas were observed at any of the call shapes the production assembler uses.
3. **AL 1.5a's record math equals the public 1.5a source's math.** The HDF was built programmatically from the formulas in the `Bdos15a.zip` source (`hd.init`: base = ⌊(⌊total/1600⌋+32)/32⌋+1, record n at base+(n−1)·1600, partial last record counted when ≥5 leftover tracks). The AL build reported `R=000B` = 11 records for the 16128-sector test disk — exactly the predicted 10 full + 1 partial — and found the BDOS ID where those formulas placed it. SimCoupé's `IsBDOSDisk` uses the same base formula, so all three agree.
4. **The BDOS ID stamp is load-bearing and survives file saves.** 1.5a's `exp.rcd` → `get.label` errors with "Invalid record" (rep81) when the stamp is missing, so an unstamped record cannot be `HRECORD`-selected. B-DOS wrote the probe's directory entry into slot 0 of the record's first directory sector while **preserving** the `BDOS` ID at bytes 232-235 of that same 256-byte entry (the entry-0 bytes 210-255 are the record's label/ID region).
5. **A B-DOS record is formattable programmatically**: zero-fill 1600 sectors + stamp `BDOS` at byte 232 of the record's first sector is equivalent to what `FORMAT` leaves (the 1.7n manual: hard-disk FORMAT zero-fills; the ID is the selection gate). B-DOS AL booted against such records, auto-selected its default record, and used them without complaint — no on-SAM formatter run was needed.

### Rig and repro

`tools/i62-bdos-experiment/run-experiment.sh` rebuilds everything and asserts both transcripts plus the HDF post-check; `tools/i62-bdos-experiment/README.md` has the exact invocations. Components: the probe (pyz80), a `build-i62-disk` Go tool (same boot-disk recipe as `tools/build-disk`, with the DOS slot swappable — SAMDOS 2 at start-address 491529 or `AL-BDOS15a` at 32777, both with the 0x60 start-page unused-bits pattern their source disks carry), and `make-atomlite-hdf.py` (RS-IDE v1.1 + ATA identify + stamped records; every field cites SimCoupé's `HardDisk.cpp`/`ATA.cpp` or the B-DOS source). The AL 1.5a DOS binary is the vendored freeware copy (`reference/bdos/al-bdos15a.bin`, i71); the boot disk and HDF are built from it at run time, with a `BDOS_BOOT_MGT` override to re-extract from a worldofsam disk image instead.

Verified on a Linux/ARM host with no X available: SimCoupé at the CI-pinned SHA built with `-DSIMCOUPE_PORTABLE=1` (static SDL2) runs fully headless under `SDL_VIDEODRIVER=dummy SDL_AUDIODRIVER=dummy` **plus a 6-line local patch** falling back to SDL's software renderer when `SDL_RENDERER_ACCELERATED` is unavailable (stock SimCoupé hard-requires an accelerated renderer; the rendering backend has no effect on Z80/ATA emulation — the patched build first reproduced the standard CI fixture round-trip byte-for-byte before being trusted for i62). The CI dev container should run the script unmodified via the stock Xvfb x11 recipe from `headless-simcoupe.md` (same SimCoupé SHA, same flags as every CI fixture run) — expected but not yet executed there, since this host has no Docker and the experiment is deliberately **not** a CI gate (the B-DOS disk images stay outside the repo). Software rendering is slow on small hosts — `-speed 1000` plus a small window keeps wall-clock sane.

`-atombootrom 0` was passed in the B-DOS run so the standard SAM ROM boots the floppy exactly like the control run (with the option on, SimCoupé swaps in its Atom Lite boot ROM, which is the hard-disk-boot path — a separate mechanism from what i62 tests).

### Status consequences

- Hook-level portability SAMDOS ↔ B-DOS AL 1.5a: LIKELY → **VERIFIED** (this experiment).
- The spill route for i40/i59 (SAMDOS hooks → B-DOS records) is proven viable on the CI tier: record selection + whole-file save/load against an 800 KB record slice works with the production call shapes.
- Trinity/1.5t leg: **verified-static** (i71, `bdos-trinity-fork-analysis.md`) — the hook dispatch, vector table, and handler bodies are 1.5a's bytes under relocation, only the sector-device layer is swapped; runtime confirmation on real Trinity hardware is still outstanding (no emulator covers the ports).

## Open questions

1. What 1.5t beta 6's "improved compatibility" covers — NARROWED (i71, `bdos-trinity-fork-analysis.md`): beta 6 implements SDHC/SDXC block addressing (the >4 GB / 64 GB enabler) and card hot-swap via RESTORE DEVICE. Per-beta (4 vs 5 vs 6) attribution remains impossible from beta 6 alone, so the question stays open but narrowed to that residue.
2. ~~Whether 1.5t shares the filler-byte-32 behaviour~~ — **RESOLVED (i71, `bdos-trinity-fork-analysis.md`)**: the directory-management code is relocation-only in the diff, so 1.5a's filler-byte behaviour carries over unchanged.
3. Any post-2014 1.5t development (none found; Trinity hardware revisions v1.1/2019 and v1.2/2023 appear hardware-only).

## Sources

worldofsam.org product pages (b-dos, b-dos-15a, b-dos-15t, b-dos-al-15a, b-dos-16-17, b-dos-17n, b-dos-17i, samdos, trinity-ethernet-interface) · worldofsam.org forum thread 2019-04-06/1734 · the B-DOS 1.7n manual PDF (worldofsam.org system files; hook/DVAR tables extracted via pdftotext) · sam.speccy.cz/atomlite.html · ftp.nvg.ntnu.no/pub/sam-coupe/disks/dos/ (+ Bdos15a.inf directory listing) · samcoupe.com/hardtrin.htm + samcoupe.com/samrevival.htm · local SimCoupé source (`Base/HardDisk.cpp`, `Base/AtomLite.cpp`, `Base/Atom.cpp`) · the B-DOS 1.5t fork analysis (`bdos-trinity-fork-analysis.md`).
