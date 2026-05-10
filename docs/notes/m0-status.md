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

**Full round-trip: WORKING locally.** As of 2026-05-10 (late), the
HSAVE-based stub writes `OUT` correctly inside the dev container,
`samfile cat -i build/test.mgt -f OUT` produces exactly `1f 20 03 d5`,
and `diff-vs-gnu.sh` confirms byte-identity with `aarch64-none-elf-as`.
`make ci` in the container returns 0.

Two compounding fixes got us here:

1. **Boot path** (resolved earlier on 2026-05-10): three bugs in
   `tools/build-disk.sh` — body header bytes 5-6 `0x00 → 0xFF`, BASIC
   tokeniser dropping spurious `0x20` bytes, and missing `vars + gap = 604`
   allocation. See `docs/notes/sam-basic-save-format.md` and
   `docs/notes/test-mgt-byte-layout.md`.
2. **OUT write** (this commit): switched stub output mechanism from
   `HOFLE`/`SBYT`/`CFSM` (hooks 147/148/152) to `HSAVE` (hook 132). The
   streaming-byte trio is broken externally in canonical SAMDOS 2 — their
   bodies never `call gtixd` at entry, so they treat the caller's UIFA
   address as a `dchan` FCB and `ofsm` overwrites SAMDOS's own running
   code in section B. HSAVE calls `gtixd` at `h.s:145` and works
   correctly externally. Full audit: `docs/notes/sam-stub-audit.md`.

## What's NOT done yet

- Nothing. CI passed cleanly on commit `1578bad` (2026-05-11). PR #1
  ready for review pending Pete's approval.

## SimCoupé runtime requirements (latent CI gotcha, fixed in `1578bad`)

The patched simcoupé binary on its own is **not** runnable. It needs
two things at runtime that `cp build/simcoupe /usr/local/bin/` alone
doesn't deliver:

1. **ROM resources at `/usr/local/share/simcoupe/`** (specifically
   `samcoupe.rom` and `sp0256-al2.bin`). CMake bakes
   `RESOURCE_DIR = CMAKE_INSTALL_FULL_DATAROOTDIR/simcoupe` into the
   binary. Without these, `Base/Memory.cpp:228` and
   `Base/VoiceBox.cpp:44` each pop a modal `MsgBox` warning. The modal
   pushes onto `GUI::s_dialogStack`, so `GUI::IsModal()` returns true
   forever, and `Base/CPU.cpp:151-167`'s `Run()` loop guards
   `ExecuteChunk()` behind `!GUI::IsModal()` — the Z80 emulator never
   runs, no port writes happen, our stub is never reached. The 30s
   timeout fires with `OUT` absent from disk.
2. **`libSAASound.so.3` in a standard library path.** It's fetched via
   CMake FetchContent and built into `build/_deps/saasound-build/`.
   `cmake --install` strips the binary's RUNPATH, so the loader can't
   find it at the build path post-install. Copy to `/usr/local/lib/`
   and `ldconfig`.

The local dev container had these from a long-ago `cmake --install`
that nobody recorded; CI never had them. CI failed all 21 times on
this branch (most recently with a "30s timeout, no OUT on disk"
signature) until commit `1578bad` added `cmake --install build` and
the libSAASound copy to the workflow.

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

- `src/stub.asm` — Z80 stub. 119 bytes. Opens `IN` via HGFLE, then writes
  `OUT` via HSAVE (whole-file write — the only externally-correct SAMDOS
  write path).
- `src/sam_io.inc` — SAMDOS-hook wrappers (HGFLE, LBYT, fill_uifa).
  HOFLE/SBYT/CFSM intentionally omitted — see header comment for why.
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

- `make ci` exits 0 in the dev container ✓
- Extracted `OUT` byte-matches `aarch64-none-elf-as` for the 4-byte NOP
  fixture ✓
- Round-trip total time < 25s ✓ (typically a few seconds)
- Same `make ci` passes in GitHub Actions on `ubuntu-latest` (pending push)
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
