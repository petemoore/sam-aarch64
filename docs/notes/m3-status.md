# M3 — current status (read me first)

Entry point for any session picking up M3 Z80-emitter work.

Last update: 2026-05-24. Tasks 1, 2, and 3 of the implementation plan
completed on branch `worktree-m3-z80-emitter`. The scaffolding and
enctab.enc header-validation loader are working end-to-end in SimCoupé.

## What M3 is (spec recap)

Per `docs/specs/2026-05-24-m3-z80-emitter-design.md`:

A Z80 program that reads a binary-tokenised `.tbn` source file from disk,
looks up each instruction's form in the loaded encoder table
(`enctab.enc`), encodes the bytes via per-slot encoders, and writes a
flat output file via SAMDOS HSAVE. Byte-identical to `refenc`'s
Mac-side output (and therefore byte-identical to `aarch64-none-elf-as`).

## What is done (Tasks 1-3)

### Task 1: Scaffold `src/m3/assembler.asm`

`src/m3/assembler.asm` — top-level Z80 program, `org &8000`, boots via
M0's `CLEAR&7FFF; LOAD CODE "assembler" 32768; CALL 32768` BASIC autorun.

Makefile targets: `m3-asm`, `m3-disk`, `test-m3`, `ci-m3`.

### Task 2: SAMDOS I/O wrappers

`src/m3/io.asm` — thin wrapper that includes `../sam_io.inc` (M0's
HGFLE/LBYT/HSAVE wrappers). Included by `assembler.asm` via
`include "io.asm"`.

### Task 3: Load enctab.enc and validate header

`src/m3/loader.asm` — opens `enctab.enc` via SAMDOS HGTHD (hook 129),
loads the whole 1090-byte body via HLOAD (hook 130) into ENCTAB_BUF
(&C100), validates magic "ENC1" and version = 1 (u16 LE = 1), then
returns to `assembler.asm`.

`tools/build-m3-disk/` — Go CLI that builds the test disk:
`samdos2 + auto BASIC + assembler.bin + enctab.enc`.

**Verified end-to-end** (after PR #12 + #13):
```
make test-m3   →   exit 0
```

`make test-m3`'s `fail:` path spins until the wrapper's 30 s timeout,
so an actual loader/self-test failure surfaces as exit 124. Each test
vector in `src/m3/test_slots.asm` and the loader's magic check is
verified by deliberate corruption.

### Key correctness findings (Task 3)

**Original HGFLE + LBYT approach was broken.**
The original loader used HGFLE (hook 158) to open enctab.enc, then
LBYT × 8 to read the header bytes. Every LBYT returned 0x00,
contradicting the audit (`docs/notes/sam-stub-audit.md`) which claimed
HGFLE leaves the read pointer past the 9-byte SAM file header.
Mechanism is still unexplained — likely a SAMDOS-state interaction
with BASIC's prior `LOAD CODE` — but the path is bypassed by using
HGTHD+HLOAD instead (PR #13). HGTHD populates `(svde)` via its
internal `gtfle` call; HLOAD's `dschd` consumes that and `ldblk`
block-copies the file body to the caller's HL.

**Hook register-clobber summary** (from the loader's IMPORTANT block):

- B  — ROM PTDOS reads caller's LMPR into B and never restores.
- HL — ROM PTDOS does `LD HL, 0; ADD HL, SP` (step 2) and never restores.
- E  — SAMDOS's `rfhk` (`b.s:475-479`) does `xor a; ld e, a`, zeroing E
  on every hook return. D is untouched.
- IX — dispatcher saves caller's IX to `(svhdr)` so the hook body can
  reach the UIFA, but `rfhk` never restores; IX ends pointing at
  `dchan` after any `gtixd`-calling hook.

Preserved across `rst 8`: IY, AF (A holds return value), SP, D.

Consequence: don't use B as a djnz counter around RST 8 calls; don't
keep destination pointers in DE or HL across `rst 8`; use absolute
stores or stash via memory if you need them.

## Current test status

| Layer | Status | Notes |
|---|---|---|
| `make m3-asm` | ✅ PASS | 160-byte binary builds clean |
| `make m3-disk` | ✅ PASS | Disk builds with samdos2 + assembler + enctab |
| `make test-m3` | ✅ 0 (patched) / 124 (stock macOS) | Loader validates header in SimCoupé |
| CI `ci-m3` | not yet wired | Will work once Dockerfile.dev includes build-m3-disk |

## Layout so far

```
src/m3/
  assembler.asm     top-level: boot, SP init, call load_enctab, DI; HALT, fail:
  io.asm            thin include of ../sam_io.inc
  loader.asm        enctab.enc HGFLE+LBYT reader + header validator
  (reader.asm       .tbn record streamer — Task 16, not yet written)
  (encoder.asm      form lookup + slot dispatch — Tasks 17-18, not yet)
  ...

tools/build-m3-disk/
  main.go           Go CLI: builds m3 test disk
  go.mod / go.sum

docs/notes/m3-status.md (this file)
```

## Next tasks

Per the plan at `docs/superpowers/plans/2026-05-24-m3-z80-emitter.md`:

- **Task 4**: Xreg / Wreg / XregOrSp / WregOrSp slot encoder
  (`src/m3/slots/xreg.asm`). 5-bit register pack at `slot.BitPosition`.
  About 30 lines Z80. Self-test block exercising known inputs.

- **Task 5**: Imm5 / Imm6 / CondCode / ShiftAmount slot encoders
  (`src/m3/slots/imm_small.asm`). N-bit unsigned pack with range-check.

- **Tasks 4-15**: remaining slot encoders.

- **Task 16**: `.tbn` stream reader (`src/m3/reader.asm`).

- **Tasks 17-18**: form lookup and encoder dispatcher.

- **Task 19**: constant-only expression evaluator.

- **Task 20**: HSAVE to OUT file.

- **Task 21**: `tools/build-m3-disk.sh` + `tools/run-m3-roundtrip.sh`.

- **Task 22**: M3 fixture corpus + CI `m3` job.

## Hand-off recipe

```bash
# Verify build
make m3-asm
ls -la build/assembler.bin   # should be ~160 bytes

# Verify disk build
make m3-disk
samfile ls -i build/m3-test.mgt

# Run in patched SimCoupé (exit 0 = header validated)
timeout 30s /Users/pmoore/git/simcoupe/build/SimCoupe.app/Contents/MacOS/SimCoupe \
  -exitonhalt 1 -fullscreen 0 -firstrun 0 build/m3-test.mgt; echo "exit: $?"
```

## Authoritative references

- M3 spec: `docs/specs/2026-05-24-m3-z80-emitter-design.md`
- M3 plan (local-only, gitignored): `docs/superpowers/plans/2026-05-24-m3-z80-emitter.md`
- M2 encoder-table format: `docs/specs/2026-05-24-m2-encoder-tables-design.md §2`
- SAMDOS hook semantics: `docs/notes/sam-stub-audit.md`
- B-clobbering citation: ROM PTDOS `rom-v3.0_annotated-disassembly.txt:12944-12978`
