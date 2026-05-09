# M0 round-trip — current status (read me first)

This document is the entry point for any session picking up the M0
toolchain-bootstrap work. Read this before starting; everything else
in `docs/notes/` and `docs/sam/` is referenced from here as needed.

Last update: 2026-05-10. Branch: `m0-toolchain-bootstrap`. Latest
commit at the time of writing: `c3ce913`.

## What M0 is

A round-trip oracle that proves the dev pipeline works end-to-end
before Phase 1 (the real assembler). The flow:

1. Mac-side `make ci` builds a Z80 stub (`build/stub.bin`) with
   `pyz80` and a self-bootable .mgt disk image with the stub plus an
   input fixture file `IN`.
2. SimCoupé (patched) boots the disk headlessly, runs the stub.
3. The stub uses SAMDOS RST 8 hooks to read `IN`, write a 4-byte
   aarch64-NOP file `OUT`, and exit cleanly via the magic-port
   sequence.
4. Mac side extracts `OUT` from the disk image and byte-diffs it
   against `aarch64-none-elf-as` of the same source. They must match.

Plan (per-task breakdown): `docs/plans/2026-05-09-m0-toolchain-bootstrap.md`.

## What is verified working

- **Real SAM boot path**. The disk now contains samdos2 as the first
  file (track 4 sector 1, 20 sectors), so SAM ROM's `BOOT` (F9) reads
  T4S1 raw to `&8000`, finds the literal "BOOT" at sector offset 256,
  and `JP &8009` into samdos2's self-loader. SAMDOS becomes resident
  via the real flow — no SimCoupé `dosboot` fake-disk hack is
  involved. Disk is **bootable on real SAM hardware**.
- **BASIC auto-RUN, single-statement**. Verified test: an `auto`
  BASIC file with body `10 OUT 57005, 192` (just the magic-port OUT)
  is loaded by SAMDOS, auto-RUN by BASIC at line 10, fires
  `OUT (&DEAD), &C0`, hits the magic-port detector in the patched
  simcoupe, exits cleanly with `EXIT=0`.

## What is NOT working (the open M0 blocker)

Multi-statement BASIC during auto-RUN. The intended boot line is:

```
10 CLEAR 24575 : LOAD "stub" CODE 24576 : CALL 24576
```

Trace (clean simcoupe with vendored patch only, in-container Rst 8 probe):

```
[Rst8 #1 ROM pc=ed38 byte=50]                            ← copyright
[Rst8 #2 ROM pc=d8e3 byte=80]                            ← BTHK
[Rst8 #3 ROM pc=e2b7 byte=82 type=10 name='auto']        ← HLOAD AUTO BASIC
[Rst8 #4 ROM pc=e1f7 byte=81 type=13 name='stub']        ← FOPHK opens stub
[Rst8 #5 ROM pc=e2b7 byte=82 type=13 name='stub']        ← HLOAD stub
[Rst8 #6 ROM pc=0e00 byte=00]                            ← OK prompt
EXIT=124 (timeout)
```

Reads: BASIC auto-RUN fires, runs CLEAR, runs `LOAD "stub" CODE 24576`,
the stub data is on disk and gets read in. After the LOAD completes
the interpreter goes to the OK prompt — `CALL 24576` either doesn't
fire, or fires and the stub returns silently without writing the
magic OUT.

A Plan-agent review (transcript in this session) suggested the most
likely root cause is **URPORT corruption after LOAD CODE's
auto-execute path**: ROM `PDPSR2` at `0x1279` decodes the saved
exec-page byte and `OUT (URPORT), A` to switch in the requested page,
which displaces the SAMDOS-resident page from where the stub expects
it (`&8000–&BFFF`). The stub then tries to call SAMDOS hooks via RST
8, but the SAMDOS code isn't paged in, so RST 8 dispatches into
garbage and silently returns.

## The next concrete step (option (c) per the agent review)

**Skip the BASIC indirection entirely**. Make `auto` itself a
type-19 (Code) directory entry with auto-execute pointing at the
stub's load address; the stub's first instruction restores URPORT
to SAMDOS's resident page (read from sysvar 5BC2H per Tech Manual
p.85).

Specifics:

- Stub at upper RAM (e.g. `&E000` — section D, where SAMDOS at
  `&8000–&BFFF` doesn't reach) instead of `&6000`.
- Single `auto` directory entry, type 19, auto-exec set per ROM
  E287 / PDPSR2 encoding.
- Stub's first instruction: `LD A, (&5BC2)` then `OUT (URPORT), A`
  to restore the SAMDOS page before the first hook call.
- Drop the BASIC AUTO file and the separate stub file from the disk
  layout.

This pivot was deferred from the previous session for a clean handoff.

## What lives where (orientation)

### Repo files

- `src/stub.asm` — the Z80 stub. Currently at `org &6000`; will move
  to upper RAM as part of option (c).
- `src/sam_io.inc` — SAMDOS-hook wrappers used by the stub.
- `tools/build-disk.sh` — disk constructor. Hand-rolls all directory
  entries (samfile has a sector-mask precedence bug; commit message of
  672a51b documents it).
- `tools/simcoupe-exitonhalt.patch` — vendored simcoupe patch. Adds
  `-exitonhalt 1` semantics via `on_halt` AND magic-port detection on
  `OUT (&DEAD), &C0` via `on_output`. KEEP MINIMAL — no debug
  instrumentation.
- `tools/Dockerfile.dev` — dev container recipe.
- `reference/samdos/samdos2.bin` — vendored SAMDOS binary (10000 bytes,
  built from `~/git/samdos` and verified byte-for-byte against
  `res/samdos2.reference.bin`).
- `.github/workflows/ci.yml` — CI recipe.
- `docs/sam/*.txt` — extracted text of the SAM PDFs (grep-friendly).
  ROM annotated disasm is the gold standard for any "what does the
  ROM actually do?" question.
- `docs/notes/sam-disk-format.md` — disk-format research findings.
- `docs/notes/samdos2-auto-run-analysis.md` — proof that samdos2's
  INIT (BTHK hook 128) does NOT auto-RUN AUTO files despite the Tech
  Manual saying so. The "AUTO file is auto-RUN" convention is real
  but provided by separate boot-loader code on bootable disks (cf.
  FRED 56), not by SAMDOS itself.
- `docs/notes/fred-disk-inspection.md` — A/B reference: a real-world
  SAM disk's directory + boot behaviour.
- `docs/notes/headless-simcoupe.md` — how to run simcoupe headlessly
  in the dev container or CI. Read this before doing anything.
- `docs/notes/simcoupe-batch.md` — historical M0 Task 1 spike. Some
  content is current, the headless-environment claims have been
  superseded by `headless-simcoupe.md`.

### External (in `~/git/`)

- `~/git/samdos/` — SAMDOS source (Bruce Gordon, reconstructed via
  dZ80). Source is grep-able; build verifies match against the
  reference binary.
- `~/git/simcoupe/` — vanilla SimCoupé clone (don't modify here; the
  vendored patch is the source of truth).
- `~/git/samfile/` — Pete's MGT-image inspector / file-injector.
  v2 module path is mandatory. Has a known bug at samfile.go:564
  (operator precedence in SAMMask) — we hand-roll directory entries
  to avoid it.
- `~/git/pyz80/` — Z80 assembler. Now a proper Python package; install
  via `pip install`.

## Hand-off recipe

1. Ensure `MEMORY.md` has been read (it's auto-loaded; check that
   `feedback_docs_first.md` and `headless_setup.md` are present in
   the index).
2. Read this file and `docs/notes/headless-simcoupe.md`.
3. (Re)build the dev container if it isn't running:
   ```bash
   docker rm -f sam-aarch64-ci 2>/dev/null
   docker build -t sam-aarch64-dev:latest -f tools/Dockerfile.dev tools/
   docker run -d --name sam-aarch64-ci \
       -v "$PWD:/work" -w /work \
       sam-aarch64-dev:latest sleep infinity
   ```
   (Add `--platform linux/amd64` at both build and run if you want
   CI-runner parity. Default arch is fine for dev; M0 artefacts are
   architecture-independent.)
4. Build the patched SimCoupé inside it (one-time per container, ~30s):
   see `headless-simcoupe.md` "Building the patched SimCoupé" section.
5. Confirm baseline: `make ci` from `/work` (currently expected to
   time out at the multi-statement BASIC issue — that's the M0 blocker
   above).
6. Implement option (c). Suggested order:
   1. Modify `src/stub.asm` to org `&E000` and prepend a URPORT-restore
      preamble.
   2. Rebuild stub.
   3. Modify `tools/build-disk.sh`: remove the BASIC `auto` and the
      separate `stub` file; emit a single `auto` directory entry that
      is the stub itself, type 19, auto-execute at `&E000`, plus the
      `IN` data file.
   4. Re-run `make ci` and confirm `EXIT=0` plus a valid `OUT` file
      extracted from the disk that byte-matches `aarch64-none-elf-as`.

## Definition of done (M0 milestone)

- `make ci` exits 0 in the dev container.
- Extracted `OUT` matches `aarch64-none-elf-as` byte-for-byte for the
  4-byte NOP fixture.
- Round-trip total time < 25s.
- The same `make ci` passes in GitHub Actions on `ubuntu-latest`.
- The disk image is bootable on real SAM hardware (verifiable by
  comparing the boot mechanism to FRED 56).
