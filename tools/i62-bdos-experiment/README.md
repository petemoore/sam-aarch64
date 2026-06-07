# i62 — B-DOS storage-backend portability experiment

Empirically proves that storage code written against the project's
SAMDOS hook idioms ([docs/specs/samdos-file-io.md](../../docs/specs/samdos-file-io.md))
runs **unchanged** against B-DOS AL 1.5a + an emulated Atom Lite under
SimCoupé. Results + hook-level findings:
[docs/notes/bdos-version-landscape.md](../../docs/notes/bdos-version-landscape.md)
§"Empirical verification (i62)".

## What it does

One probe binary (`i62test.asm`, pyz80, org `&8000`) is booted twice:

| run | boot disk | mass storage | expected transcript |
|-----|-----------|--------------|---------------------|
| control | SAMDOS 2 (`reference/samdos/samdos2.bin`) | none | `I62` `DOS:SAMDOS` `P2` `P3` `P4` `OK` |
| B-DOS | AL-BDOS15a (`reference/bdos/al-bdos15a.bin`) | Atom Lite HDF (`-drive2 3 -atomdisk0`) | `I62` `DOS:BDOS V=05 R=000B` `P1` `P2` `P3` `P4` `OK` |

The probe: DVAR-7 B-DOS detection (manual MC idiom, from section B) →
`HRECORD` record 1 (B-DOS branch only) → `HSAVE` a 1553-byte pattern →
`HGTHD` + `HLOAD` it back → byte-compare → `OK`/`FAILn` over the
printer status channel → `DI; HALT`. Everything after the
record-selection branch is common code — that's the portability claim
under test.

## Files

- `i62test.asm` — the probe (self-contained; mirrors the production
  idioms from `src/sam_io.inc`, `src/loader.asm`, `src/print.asm`).
- `build-i62-disk/` — Go tool building the bootable `.mgt` (DOS slot +
  auto-RUN BASIC + probe), mirroring `tools/build-disk`.
- `make-atomlite-hdf.py` — builds the B-DOS-formatted Atom Lite HDF
  programmatically (RS-IDE v1.1 header + ATA identify + zeroed records
  with the `BDOS` ID stamped at byte 232 of each record's first
  directory sector). Every field cites its source in the header
  comment.
- `run-experiment.sh` — orchestrates both runs and asserts the
  transcripts, then checks the HDF actually contains the saved file.

## Prerequisites

- `pyz80`, `go`, `python3`.
- SimCoupé ≥ v1.2.16 (`-exitonhalt`). The CI dev container's build
  under Xvfb (`docs/notes/headless-simcoupe.md`) is expected to work
  as-is (same binary + flags as every CI fixture run; not yet executed
  there — the 2026-06-11 runs used a local build, see the landscape
  doc's i62 section).
- The B-DOS AL 1.5a DOS binary is the vendored freeware copy
  ([`reference/bdos/al-bdos15a.bin`](../../reference/bdos/README.md)) — no
  external disk image is needed. To re-extract from a different worldofsam
  AL disk instead, set `BDOS_BOOT_MGT` to that `.mgt` (any AL disk whose
  first file is the bootable `AL-BDOS15a` CODE file) — this path additionally
  needs `samfile` (`go build ./cmd/samfile` from github.com/petemoore/samfile).

## Running

```bash
# CI container (Xvfb recipe from docs/notes/headless-simcoupe.md):
Xvfb :99 -screen 0 1280x1024x24 & export DISPLAY=:99
export SDL_VIDEODRIVER=x11 SDL_AUDIODRIVER=dummy
tools/i62-bdos-experiment/run-experiment.sh

# Fully headless host without X (needs a SimCoupé built with a
# software-renderer fallback — see the landscape doc's i62 section):
export SDL_VIDEODRIVER=dummy SDL_AUDIODRIVER=dummy
SIMCOUPE=/path/to/simcoupe \
SIMCOUPE_ARGS="-respath /path/to/simcoupe/Resource -speed 1000" \
tools/i62-bdos-experiment/run-experiment.sh
```

All artifacts land in `build/` (gitignored): `i62test.bin`,
`i62-samdos.mgt`, `i62-bdos.mgt`, `i62-atomlite.hdf`, plus the two
`*.status.log` transcripts.

This experiment is **not** a CI gate. The AL 1.5a DOS binary is the
vendored freeware copy, but the run still needs SimCoupé and (for a real
hardware confirmation) a B-DOS-formatted medium, so it stays a local /
dev-container check rather than a gate.
