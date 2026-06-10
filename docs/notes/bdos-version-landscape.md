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
| 1.5t beta 6 | — | — | YES | — | 64 GB SDHC |
| 1.6c–1.7q | YES | — | — | YES | 8 GB hard cap (documented in the 1.7n manual) |

**SimCoupé:** emulates Atom (byte-swapped HDF) and Atom Lite (non-byte-swapped), with B-DOS disk detection (`HardDisk::IsBDOSDisk()`). It does **not** emulate the Trinity (no `&DC`–`&DF` port handling). The "Atom Lite? (or Trinity media under 8GB)" comment in `Base/HardDisk.cpp` reflects an *accidental* media-format compatibility: Trinity-formatted media under 8 GB carries the same signature as Atom Lite media, so SimCoupé can mount such images via the Atom Lite path. Code touching the Trinity SPI ports directly will not run in SimCoupé.

## SAMDOS compatibility (from the B-DOS 1.7n manual)

> "B-DOS offers several hook codes including the SAMDOS hookcodes. All other codes which are not included in this list are ignored by B-DOS."

Verified hook table highlights: **every hook the project's `samdos-file-io.md` idioms use — HGTHD (129), HLOAD (130), HVERY (131), HSAVE (132), HOFLE (147), HSBYT (148), HWSAD (149), HSVBK (150), HCFSM (152), HRSAD (160), HLDBK (161), HERAZ (166) — is present and SAMDOS-compatible in B-DOS.** B-DOS-only hooks: HRECORD (&9C, record selection), HVEBK (&9D), HLBYT (&9F), HDINIT (&87), HVMSAD (&86). DVARs 0/1/2/5/7 are SAMDOS-compatible; all others B-DOS-specific (DVAR 7 = B-DOS version, the documented detection idiom).

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
- Development transfers between them at the hook level (LIKELY — identical hook tables across B-DOS builds; no cross-backend runtime regression test published, which is exactly what the i62 SimCoupé experiment will provide).

## Open questions

1. What 1.5t beta 6's "improved compatibility" covers (UNKNOWN — release notes not found).
2. Whether 1.5t shares the filler-byte-32 behaviour (LIKELY — same 1.5a codebase; matters only for raw directory inspection).
3. Any post-2014 1.5t development (none found; Trinity hardware revisions v1.1/2019 and v1.2/2023 appear hardware-only).

## Sources

worldofsam.org product pages (b-dos, b-dos-15a, b-dos-15t, b-dos-al-15a, b-dos-16-17, b-dos-17n, b-dos-17i, samdos, trinity-ethernet-interface) · worldofsam.org forum thread 2019-04-06/1734 · the B-DOS 1.7n manual PDF (worldofsam.org system files; hook/DVAR tables extracted via pdftotext) · sam.speccy.cz/atomlite.html · ftp.nvg.ntnu.no/pub/sam-coupe/disks/dos/ (+ Bdos15a.inf directory listing) · samcoupe.com/hardtrin.htm + samcoupe.com/samrevival.htm · local SimCoupé source (`Base/HardDisk.cpp`, `Base/AtomLite.cpp`, `Base/Atom.cpp`).
