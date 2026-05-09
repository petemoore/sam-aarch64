# M0 round-trip — current status (read me first)

Entry point for any session picking up M0 toolchain-bootstrap work.

Last update: 2026-05-10. Branch: `m0-toolchain-bootstrap`.

## What M0 is

A round-trip oracle that proves the dev pipeline works end-to-end
before Phase 1 (the real assembler):

1. Mac builds a Z80 stub + bootable .mgt image with the stub plus an
   input fixture file `IN`.
2. SimCoupé (vendored patch) boots the disk headlessly, runs the stub.
3. Stub uses SAMDOS RST 8 hooks to read `IN`, write a 4-byte aarch64-NOP
   file `OUT`, and exit cleanly via the magic-port sequence
   (`OUT (&DEAD), &C0`).
4. Mac side extracts `OUT` and byte-diffs it against
   `aarch64-none-elf-as` of the same source.

Plan: `docs/plans/2026-05-09-m0-toolchain-bootstrap.md`.

## Current state

**Boot path: WORKING.** As of 2026-05-10 evening, the AUTO BASIC line
`10 CLEAR 32767: LOAD "stub" CODE 32768: CALL 32768` boots cleanly,
LOADs the stub at 0x8000, CALLs it, and the stub's `OUT (&FE), 4 : DI :
HALT` triggers simcoupé's `-exitonhalt` patch and exits 0. Verified in
the dev container against a 6-byte minimal stub.

The crash that blocked the boot path for several sessions (red /
pink-bordered page-displaced screen → cold-boot reset) was caused by
three independent bugs in `tools/build-disk.sh`, all now fixed:

1. **stub/IN body header bytes 5-6** were `0x00 0x00` instead of `0xFF 0xFF`,
   triggering ROM's LOAD-CODE auto-exec path to JP into garbage.
2. **BASIC tokeniser inserted spurious `0x20` bytes** after each keyword,
   producing visible double-spacing in LIST and corrupting the parser.
3. **BASIC body had no vars/gap allocation.** Canonical SAM SAVE always
   reserves `vars + gap = 604` bytes after the program text; with our
   triplets all pointing to PROG+`prog_length`, CLEAR walked off the
   program into the next file's body.

See `docs/notes/sam-basic-save-format.md` for the format reference and
`docs/notes/test-mgt-byte-layout.md` for the byte-by-byte explanation.

## What's NOT done yet

The 6-byte stub (LD A,4; OUT (FE),A; DI; HALT) only proves boot —
it doesn't read `IN` or write `OUT`. The full M0 round-trip needs:

1. Restore the SAMDOS-using `src/stub.asm` (~124 bytes per
   `2026-05-10-handoff.md`).
2. Verify it reads `IN` via SAMDOS hooks (`HGTHD`/`HLOAD` per `sam-stub-audit.md`)
   and writes `OUT` via `HSAVE`.
3. Add `OUT` extraction step to the Mac side and byte-diff vs
   `aarch64-none-elf-as`.
4. Confirm in GitHub Actions runner.

## What's verified

- **Real SAM boot path**: samdos2 at T4S1 with the canonical body header
  (`13 10 27 09 80 ff ff 00 7d`) lets ROM BOOT find the "BOOT" magic at
  sector offset 256 and JP to &8009. Bootable on real hardware.
- **BASIC AUTO-RUN with CLEAR + LOAD CODE + CALL** completes cleanly.
- **Headless dev container**: see `docs/notes/headless-simcoupe.md`. The
  `-exitonhalt 1` simcoupé patch detects `DI; HALT` and exits 0.
- **Empirical validation of the canonical SAVE format**: scanned 161
  disks under `~/Downloads/`, 94% (632/673) of well-formed BASIC files
  satisfy `SAVARS-NVARS == 604`. Our build matches the dominant 92+512
  split (~50% of disks).

## What lives where

### Repo files

- `src/stub.asm` — Z80 stub. Currently a 6-byte minimal "set border
  green and HALT". Needs to be restored to the SAMDOS-using version.
- `src/sam_io.inc` — SAMDOS-hook wrappers.
- `tools/build-disk.sh` — disk constructor. Trimmed (2026-05-10) of
  experimental notes; format authority is `sam-basic-save-format.md`
  and `test-mgt-byte-layout.md`.
- `tools/simcoupe-exitonhalt.patch` — vendored simcoupé patch:
  `on_halt` for `DI; HALT`, `on_output` for `OUT (&DEAD), &C0`.
- `tools/Dockerfile.dev` — dev container recipe.
- `reference/samdos/samdos2.bin` — vendored SAMDOS binary (10000 bytes,
  byte-identical to upstream).
- `.github/workflows/ci.yml` — CI recipe.
- `docs/sam/*.txt` — extracted text of the SAM PDFs.
- `docs/notes/sam-basic-save-format.md` — vars/gap invariant + ROM
  citations.
- `docs/notes/test-mgt-byte-layout.md` — byte-by-byte map of `build/test.mgt`.
- `docs/notes/sam-disk-format.md` — broader disk format.
- `docs/notes/sam-file-header.md` — 9-byte body header.
- `docs/notes/sam-paging.md` — paging.
- `docs/notes/sam-stub-audit.md` — SAMDOS hook semantics for the full
  stub (HSAVE, not HOFLE/SBYT/CFSM).
- `docs/notes/samfile-capabilities.md` — samfile bug list and what
  samfile lacks; informs the hand-roll-directory-entries decision.
- `docs/notes/headless-simcoupe.md` — Docker recipe for headless tests.
- `docs/notes/comet-encoding-patterns.md` — BASIC tokenisation reference.
- `docs/notes/samdos2-auto-run-analysis.md` — why hook 128 (BTHK)
  doesn't actually auto-RUN despite Tech Manual claims.
- `docs/notes/fred-disk-inspection.md` — A/B methodology against a real
  SAM disk.
- `docs/notes/archive/` — refuted CLEAR-investigation theories (kept
  for forensic interest, do not rely on).

### External (in `~/git/`)

- `~/git/samdos/` — SAMDOS source.
- `~/git/simcoupe/` — vanilla SimCoupé.
- `~/git/samfile/` — MGT inspector / file-injector. Has the `:564`
  SAMMask bug; we hand-roll directory entries to avoid it. A separate
  PR fixes EDSK detection + basic-to-text empty-input panic at
  `~/git/samfile-edsk-fix/` (see that worktree's `PROMPT.md`).
- `~/git/pyz80/` — Z80 assembler (pip-installable).

## Definition of done (M0 milestone)

- `make ci` exits 0 in the dev container ✓ (boot path)  / ✗ (round-trip)
- Extracted `OUT` byte-matches `aarch64-none-elf-as` for the 4-byte NOP
  fixture
- Round-trip total time < 25s
- Same `make ci` passes in GitHub Actions on `ubuntu-latest`
- Disk image bootable on real SAM hardware ✓

## Hand-off recipe

1. Read this file plus `docs/notes/headless-simcoupe.md`.
2. Ensure dev container is running:
   ```bash
   docker ps --filter name=sam-aarch64-ci
   # if missing, see headless-simcoupe.md "Recreating the dev container"
   ```
3. Verify baseline: from `/Users/pmoore/git/sam-aarch64`:
   ```bash
   make stub
   ./tools/build-disk.sh tests/fixtures/nop.s build/test.mgt
   docker exec sam-aarch64-ci bash -lc '
       Xvfb :150 -screen 0 1280x1024x24 &
       export DISPLAY=:150 SDL_VIDEODRIVER=x11 SDL_AUDIODRIVER=dummy
       ./tools/run-simcoupe.sh build/test.mgt
       echo "exit=$?"
   '
   ```
   Expected: `exit=0`. If anything else, the boot path has regressed —
   diff `build/test.mgt` against the known-good reference per
   `test-mgt-byte-layout.md`.
4. Restore the full stub and continue M0 round-trip work.
