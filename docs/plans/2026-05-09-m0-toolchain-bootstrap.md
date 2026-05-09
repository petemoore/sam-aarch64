# M0 — Toolchain Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire up the entire dev-to-CI loop end-to-end with a stub Z80 program that emits one fixed `nop` instruction, so subsequent milestones (M1–M6) can iterate confidently against a working test harness.

**Architecture:** Three concentric loops — pyz80 builds a Z80 binary; samfile injects it into a `.mgt` disk image; SimCoupé runs the disk image headlessly; samfile extracts the resulting output file; a Mac-side test harness diffs the output against `aarch64-none-elf-as`. GitHub Actions runs the same loop on every commit.

**Tech Stack:** pyz80 (Mac-side Z80 assembler), SimCoupé (SAM emulator with batch mode), samfile (Pete's Go tool for `.mgt` manipulation), GNU aarch64 binutils (`aarch64-none-elf-as`, `aarch64-none-elf-objcopy`), bash + Make for orchestration, GitHub Actions for CI.

---

## File structure

After M0 completes, the repo gains:

| Path | Responsibility |
|---|---|
| `Makefile` | Top-level orchestration: `make stub`, `make test`, `make clean`, `make ci`. |
| `tools/check-toolchain.sh` | Verifies `pyz80`, `samfile`, `simcoupe`, `aarch64-none-elf-as`, `aarch64-none-elf-objcopy` are on `PATH`. |
| `tools/build-stub.sh` | pyz80-builds `src/stub.asm` → `build/stub.bin`. |
| `tools/build-disk.sh` | Constructs `build/test.mgt` from `build/stub.bin` + a chosen input fixture. |
| `tools/run-simcoupe.sh` | Launches SimCoupé batch-mode against `build/test.mgt`, waits for completion, exits. |
| `tools/extract-output.sh` | Pulls `OUT` file off `build/test.mgt` into `build/out.bin`. |
| `tools/diff-vs-gnu.sh` | Assembles the same `.s` fixture with `aarch64-none-elf-as` + `objcopy`, byte-diffs against `build/out.bin`. |
| `tools/run-roundtrip.sh` | End-to-end driver: chains build-disk → run-simcoupe → extract-output → diff-vs-gnu. Exit code = test result. |
| `src/stub.asm` | Z80 source — opens input file `IN`, ignores contents, writes 4 bytes (`d5 03 20 1f`) to output file `OUT`, returns to MGT/MasterDOS. |
| `src/sam_io.inc` | Reusable Z80 include — wrapper macros for SAM disk file I/O routines, populated from spike findings (Task 6). |
| `tests/fixtures/nop.s` | Plain-text aarch64 source containing exactly `nop`. |
| `docs/notes/simcoupe-batch.md` | Spike output (Task 1): documented batch-mode invocation. |
| `docs/notes/sam-file-io.md` | Spike output (Task 6): documented SAM disk I/O API surface. |
| `.github/workflows/ci.yml` | GitHub Actions workflow: install toolchain, run `make ci`. |

---

## Task 1: Spike — SimCoupé batch-mode invocation

**Files:**
- Create: `docs/notes/simcoupe-batch.md`

This is a **research spike** — the goal is to discover and document the invocation pattern. Output is a markdown document, not code.

**Source available at `~/git/simcoupe/` — read it.** Pete has cloned Simon Owen's repo locally; this is the primary reference. Pete is open to forking SimCoupé and merging features upstream.

**Preliminary finding from `~/git/simcoupe/Base/Options.cpp`**: SimCoupé exposes options like `disk1`, `autoload`, `autoboot`, `fullscreen`, `speed`, but has **no native batch / headless / exit-on-halt mode**. Task 1 must therefore additionally **decide and document**:

- **Option A: drive externally** — `timeout 30 simcoupe ...`, kill the process when the assembled output file appears on the host-mapped disk image, accept timeout exit code as success. Simple but hacky; flaky if timing is wrong.
- **Option B: add batch mode upstream** — patch SimCoupé to accept e.g. `--exit-on-halt` and quit cleanly when the Z80 executes a `HALT` with interrupts disabled. Clean. Pete welcomes upstream PRs; the change is small (probably <30 lines in `Main.cpp`/`CPU.cpp`).

Recommendation in the spike doc must include rationale and a concrete plan for whichever option is chosen. **Strong preference for Option B** if it can be implemented in <half a day — every M-milestone after this one will run thousands of these in CI; flaky timeouts compound.

- [ ] **Step 1: Confirm SimCoupé is installed and locate the binary**

Run:
```bash
which simcoupe || ls -la /Applications/SimCoup*.app 2>/dev/null
simcoupe --help 2>&1 | head -40
```

If neither works, check `simonowen.com/simcoupe/` — install via `brew install simcoupe` or download the macOS bundle. Document the install path.

- [ ] **Step 2: Identify CLI flags relevant to batch operation**

From `simcoupe --help` output, identify flags for:
- Specifying a disk image to mount (likely `-d1 PATH` or positional arg)
- Disabling the GUI / running headless (likely `--no-window`, `--headless`, or similar)
- Auto-quitting on a condition (likely `--quit-on-halt` or similar — investigate)
- Recording video/screenshots (irrelevant to us, but may indicate batch-mode support)

If `--help` output is unclear, check the SimCoupé source/docs. Ask Pete about his typical invocation if uncertain.

- [ ] **Step 2b: Determine how the SAM auto-runs the stub from disk**

A SAM Coupé booting from an `.mgt` runs a file called `auto` if it exists. Investigate:
- Whether `auto` must be a BASIC loader (the COMET disk's `auto COMET` is a tokenised BASIC file that `LOAD`s and `CALL`s the code) or whether a code file named `auto` with load/exec addresses set is auto-executed directly.
- If a BASIC loader is required, document the minimal BASIC source that loads our stub at `&8000` and calls `&8000`. Note that BASIC files on SAM disks are themselves tokenised — `samfile basic-to-text` is the inverse direction; we may need to construct a minimal BASIC file by hand or borrow `reference/comet-disk/auto COMET` as a template and adjust filename/address.
- If running in SimCoupé requires a key press (`F9`/`return`) before the auto-run fires, find a flag that disables the splash/wait — or use a small autotype/expect helper.

- [ ] **Step 3: Write a trivial test invocation**

Find or create a tiny `.mgt` image (any image — even an empty one). Run SimCoupé against it with the discovered flags. Verify the process exits cleanly without user interaction. Time it (`time simcoupe ...`). Target: <10 seconds for a trivial program.

If SimCoupé will not exit without GUI interaction, document the fallback: drive it via expect/AppleScript, kill on timeout, or use a different emulator (CLI `z80em`, `fuse-emulator`-equivalent for SAM).

- [ ] **Step 4: Document findings**

Write `docs/notes/simcoupe-batch.md` covering:
- SimCoupé version and install method
- Exact invocation command (with placeholder for disk path)
- How completion is detected (clean exit / timeout / halt detection)
- Linux equivalent invocation (for CI) — note any flag differences
- Known gotchas (display server requirements on Linux, e.g. `xvfb-run`)
- The auto-run mechanism: required filename on disk (likely `auto`), required file type (BASIC loader vs code), and the minimal loader source if a BASIC file is needed

- [ ] **Step 5: Commit**

```bash
git add docs/notes/simcoupe-batch.md
git commit -m "docs: document SimCoupé batch-mode invocation (M0 spike)"
```

---

## Task 2: Toolchain availability check script

**Files:**
- Create: `tools/check-toolchain.sh`
- Create: `tests/test-check-toolchain.sh`

- [ ] **Step 1: Write the failing test**

Create `tests/test-check-toolchain.sh`:

```bash
#!/usr/bin/env bash
# Verifies tools/check-toolchain.sh exits 0 when all tools are present
# and exits non-zero with a clear error when any tool is missing.

set -uo pipefail

cd "$(dirname "$0")/.."

# Happy path: real PATH should have all tools (assuming dev env is set up).
if ! ./tools/check-toolchain.sh > /tmp/check-out 2>&1; then
    echo "FAIL: check-toolchain.sh exited non-zero on a healthy environment"
    cat /tmp/check-out
    exit 1
fi

# Unhappy path: empty PATH should fail and mention the missing tool.
if PATH=/nonexistent ./tools/check-toolchain.sh > /tmp/check-out 2>&1; then
    echo "FAIL: check-toolchain.sh exited 0 with empty PATH"
    cat /tmp/check-out
    exit 1
fi
if ! grep -qi "missing" /tmp/check-out; then
    echo "FAIL: error output did not mention 'missing'"
    cat /tmp/check-out
    exit 1
fi

echo "PASS"
```

```bash
chmod +x tests/test-check-toolchain.sh
```

- [ ] **Step 2: Run the test, expect it to fail**

Run: `./tests/test-check-toolchain.sh`
Expected: failure with message about `tools/check-toolchain.sh` not being executable / not existing.

- [ ] **Step 3: Implement `tools/check-toolchain.sh`**

```bash
#!/usr/bin/env bash
# Verifies all M0 toolchain dependencies are available.

set -euo pipefail

required=(
    pyz80
    samfile
    simcoupe
    aarch64-none-elf-as
    aarch64-none-elf-objcopy
)

missing=()
for tool in "${required[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        missing+=("$tool")
    fi
done

if [ ${#missing[@]} -ne 0 ]; then
    echo "Missing required tools:" >&2
    for t in "${missing[@]}"; do
        echo "  - $t" >&2
    done
    echo "" >&2
    echo "Install hints:" >&2
    echo "  pyz80                    pip install pyz80   (or clone simonowen/pyz80)" >&2
    echo "  samfile                  go install github.com/petemoore/samfile/cmd/samfile@latest" >&2
    echo "  simcoupe                 see docs/notes/simcoupe-batch.md" >&2
    echo "  aarch64-none-elf-as      brew install aarch64-elf-binutils  (macOS)" >&2
    echo "                           apt-get install binutils-aarch64-linux-gnu  (Linux)" >&2
    exit 1
fi

echo "All required tools present:"
for t in "${required[@]}"; do
    printf "  %-30s %s\n" "$t" "$(command -v "$t")"
done
```

```bash
chmod +x tools/check-toolchain.sh
```

- [ ] **Step 4: Run the test, expect it to pass**

Run: `./tests/test-check-toolchain.sh`
Expected: `PASS`

If a tool genuinely is missing, install it before continuing — M0 cannot proceed without all five.

- [ ] **Step 5: Commit**

```bash
git add tools/check-toolchain.sh tests/test-check-toolchain.sh
git commit -m "feat: toolchain availability check script"
```

---

## Task 3: Top-level Makefile skeleton

**Files:**
- Create: `Makefile`

The Makefile orchestrates everything. Initially has placeholder targets that fail loudly; subsequent tasks fill them in.

- [ ] **Step 1: Write the Makefile**

```make
# M0 toolchain bootstrap — see docs/plans/2026-05-09-m0-toolchain-bootstrap.md

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

BUILD := build
TESTS := tests

.PHONY: all check stub disk run extract diff test ci clean

all: stub

check:
	./tools/check-toolchain.sh

stub: $(BUILD)/stub.bin

$(BUILD)/stub.bin: src/stub.asm
	@mkdir -p $(BUILD)
	./tools/build-stub.sh

disk: $(BUILD)/test.mgt

$(BUILD)/test.mgt: $(BUILD)/stub.bin $(TESTS)/fixtures/nop.s
	./tools/build-disk.sh $(TESTS)/fixtures/nop.s $@

run: disk
	./tools/run-simcoupe.sh $(BUILD)/test.mgt

extract: run
	./tools/extract-output.sh $(BUILD)/test.mgt $(BUILD)/out.bin

diff: extract
	./tools/diff-vs-gnu.sh $(TESTS)/fixtures/nop.s $(BUILD)/out.bin

test: check
	./tools/run-roundtrip.sh $(TESTS)/fixtures/nop.s

ci: check test

clean:
	rm -rf $(BUILD)
```

- [ ] **Step 2: Verify `make check` works**

Run: `make check`
Expected: same output as Task 2's success path. Exit 0.

- [ ] **Step 3: Verify other targets fail cleanly**

Run: `make stub`
Expected: failure mentioning either missing `src/stub.asm` or missing `tools/build-stub.sh`. Either is fine — those tasks come next.

- [ ] **Step 4: Commit**

```bash
git add Makefile
git commit -m "feat: top-level Makefile orchestrating the M0 pipeline"
```

---

## Task 4: Minimal Z80 program (halts immediately)

**Files:**
- Create: `src/stub.asm` (initial halt-only version)
- Create: `tools/build-stub.sh`
- Create: `tests/test-build-stub.sh`

This task verifies pyz80 builds a binary. The Z80 program does nothing but halt — no I/O yet. Task 7 expands it to do file I/O once Task 6's spike has documented the API.

- [ ] **Step 1: Write the failing test**

Create `tests/test-build-stub.sh`:

```bash
#!/usr/bin/env bash
# Verifies pyz80 builds src/stub.asm into a non-empty binary at build/stub.bin.

set -euo pipefail

cd "$(dirname "$0")/.."

rm -f build/stub.bin
make stub

if [ ! -s build/stub.bin ]; then
    echo "FAIL: build/stub.bin missing or empty"
    exit 1
fi

# Sanity: not absurdly large for a halt-only program (<256 bytes).
size=$(wc -c < build/stub.bin)
if [ "$size" -gt 256 ]; then
    echo "FAIL: stub.bin is $size bytes — expected <256 for a halt program"
    exit 1
fi

echo "PASS (stub.bin = $size bytes)"
```

```bash
chmod +x tests/test-build-stub.sh
```

- [ ] **Step 2: Run the test, expect it to fail**

Run: `./tests/test-build-stub.sh`
Expected: failure (`build-stub.sh` doesn't exist or `src/stub.asm` doesn't exist).

- [ ] **Step 3: Write the minimal Z80 source**

Create `src/stub.asm`:

```asm
; M0 stub assembler — halt-only version.
; Loads at the standard SAM external code address and halts.
; Task 7 will expand this to read input and write output.

                org     &8000          ; SAM external memory page boundary

start:          di                     ; disable interrupts
                halt                   ; CPU halts; SimCoupé exits via batch flag

                end     start
```

- [ ] **Step 4: Write `tools/build-stub.sh`**

```bash
#!/usr/bin/env bash
# Builds src/stub.asm into build/stub.bin with pyz80.

set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p build

# pyz80 invocation — exact flags depend on your pyz80 install.
# Common form: pyz80 --obj=output.bin source.asm
# Adjust if your local pyz80 differs.
pyz80 --obj=build/stub.bin src/stub.asm

echo "Built build/stub.bin ($(wc -c < build/stub.bin) bytes)"
```

```bash
chmod +x tools/build-stub.sh
```

**If your local pyz80 uses different flags than `--obj=`** (e.g. it produces a `.dsk` directly, or uses `-o`, or wraps output in a SAM file header), adjust the script and document the choice in a comment at the top.

- [ ] **Step 5: Run the test, expect it to pass**

Run: `./tests/test-build-stub.sh`
Expected: `PASS (stub.bin = N bytes)` where N is small (likely 2–10).

- [ ] **Step 6: Commit**

```bash
git add src/stub.asm tools/build-stub.sh tests/test-build-stub.sh
git commit -m "feat: minimal halt-only Z80 stub built with pyz80"
```

---

## Task 5: Run the halt-only stub end-to-end in SimCoupé

**Files:**
- Create: `tools/build-disk.sh` (initial — disk has only stub, no input file yet)
- Create: `tools/run-simcoupe.sh`
- Create: `tests/test-simcoupe-runs.sh`

This task closes the loop: pyz80 → disk image → SimCoupé runs it → process exits cleanly. No I/O verification yet; that's Task 7.

- [ ] **Step 1: Write the failing test**

Create `tests/test-simcoupe-runs.sh`:

```bash
#!/usr/bin/env bash
# Verifies the halt-only stub can be packaged onto a .mgt and run by SimCoupé to
# clean exit within a reasonable time budget.

set -euo pipefail

cd "$(dirname "$0")/.."

make stub
./tools/build-disk.sh tests/fixtures/nop.s build/test.mgt

start=$(date +%s)
timeout 30 ./tools/run-simcoupe.sh build/test.mgt
elapsed=$(( $(date +%s) - start ))

if [ "$elapsed" -gt 30 ]; then
    echo "FAIL: SimCoupé did not exit within 30s (elapsed: ${elapsed}s)"
    exit 1
fi

echo "PASS (elapsed ${elapsed}s)"
```

```bash
chmod +x tests/test-simcoupe-runs.sh
```

- [ ] **Step 2: Create the input fixture**

Create `tests/fixtures/nop.s`:

```asm
        nop
```

(Single line, just `nop`. Task 7 will read this through the assembler.)

- [ ] **Step 3: Write `tools/build-disk.sh`**

Reads from Task 1's spike output (`docs/notes/simcoupe-batch.md`) for any specific format requirements — particularly whether the disk needs an `auto` file to auto-execute the stub on boot.

```bash
#!/usr/bin/env bash
# Constructs a .mgt disk image containing:
#   - the stub binary (auto-loaded on boot)
#   - the input fixture (named IN on the disk)
#
# Usage: build-disk.sh <input.s> <output.mgt>

set -euo pipefail

input="$1"
output="$2"

cd "$(dirname "$0")/.."

# Start from a blank .mgt. samfile creates a fresh image if -i refers to
# a non-existent file (verify behaviour; if not, dd-fill 819200 zero bytes).
rm -f "$output"

# Construct the auto-loader per docs/notes/simcoupe-batch.md (Task 1).
# If the spike found that a code file named "auto" with load/exec addresses
# auto-runs directly, the steps below are simply: copy the stub to a temp
# file named "auto" and add it. If a BASIC loader is needed instead, the
# spike doc supplies the BASIC source — render it to a tokenised BASIC file
# and add it as "auto", with the stub binary added separately as a code file.

# Default code-file path (override per spike findings):
cp build/stub.bin /tmp/auto
samfile add -i "$output" -f /tmp/auto -c -l 0x8000 -e 0x8000
rm -f /tmp/auto

# Add the input fixture as a plain (non-code) file named IN.
# In M0 the stub ignores its content; Task 7 will read it.
cp "$input" /tmp/IN
samfile add -i "$output" -f /tmp/IN
rm -f /tmp/IN

echo "Built $output"
samfile ls -i "$output"
```

```bash
chmod +x tools/build-disk.sh
```

**Verify the `samfile add` invocation matches the help output** — `samfile --help` shows `add -i IMAGE -f FILE -c -l LOAD_ADDRESS [-e EXECUTION_ADDRESS]`. The `-c` flag makes it a code file; load/exec addresses match the stub's `org`. If samfile cannot create a fresh `.mgt` from nothing, prefix with: `dd if=/dev/zero of="$output" bs=1024 count=800` to create an empty 800K image first.

- [ ] **Step 4: Write `tools/run-simcoupe.sh`**

Adapt from `docs/notes/simcoupe-batch.md` (Task 1's spike output). Skeleton:

```bash
#!/usr/bin/env bash
# Runs SimCoupé in batch/headless mode against the given .mgt and waits for clean exit.
#
# Usage: run-simcoupe.sh <disk.mgt>
#
# Implementation derived from docs/notes/simcoupe-batch.md (M0 spike).

set -euo pipefail

disk="$1"

# EXACT FLAGS DEPEND ON SPIKE FINDINGS. Common shape:
#   simcoupe --no-gui --quit-on-halt -d1 "$disk"
# If SimCoupé does not exit on halt, fall back to a timeout wrapper here.

simcoupe -d1 "$disk" --no-window --quit-on-halt
```

```bash
chmod +x tools/run-simcoupe.sh
```

**Adjust flags to match Task 1's findings.** If SimCoupé does not natively quit on Z80 halt, augment with `timeout 30 simcoupe ...` and accept the timeout exit code as success (the batch is "done" once SimCoupé is killed).

- [ ] **Step 5: Run the test, expect it to pass**

Run: `./tests/test-simcoupe-runs.sh`
Expected: `PASS (elapsed Xs)` with X under 30.

If this fails, the spike from Task 1 was incomplete — go back, identify the gap, update the spike doc, and revise `run-simcoupe.sh`.

- [ ] **Step 6: Commit**

```bash
git add tests/fixtures/nop.s tools/build-disk.sh tools/run-simcoupe.sh tests/test-simcoupe-runs.sh
git commit -m "feat: run halt-only stub end-to-end in SimCoupé batch mode"
```

---

## Task 6: Spike — SAM disk file I/O API

**Files:**
- Create: `docs/notes/sam-file-io.md`
- Create: `src/sam_io.inc` (initial skeleton; populated by spike)

Research spike. Goal: identify which SAM ROM / MasterDOS calls the stub will use to (a) open and read the `IN` file, (b) create and write the `OUT` file. Output is a markdown document plus a stub include file with the discovered routines as labels.

- [ ] **Step 1: Read the relevant decoded COMET source**

Skim `reference/comet-decoded/comet.asm` looking for:
- File-open/read patterns near directives related to source loading
- File-create/write patterns near directives related to output
- Uses of `RST 8` with parameter bytes (the SAM ROM call convention)

Look in particular for sections that handle "load source from disk" and "save assembled output". Note the routine addresses, parameter passing convention, and error returns.

- [ ] **Step 2: Cross-reference with the COMET manual and SAM DOS docs**

Open `docs/comet/comet_v1-3_manual.pdf` for any documented entry points. Search online for "SAM Coupe MasterDOS hook codes" and "MGT DOS programmer reference". Document the canonical call mechanism.

- [ ] **Step 3: Document findings in `docs/notes/sam-file-io.md`**

Cover:
- DOS being targeted (MasterDOS vs SAMDOS — likely whichever ships with the SAM disk we're using).
- Hook addresses or `RST 8` parameter codes for: open/read/close on input, create/write/close on output.
- Register conventions for each call (which registers carry filename pointer, length, error code, etc.).
- Filename format on disk (10-char fixed-length, padded with spaces, à la COMET filenames).
- Where in our stub.asm we'd call them, illustrated with a minimal pseudocode sequence.

- [ ] **Step 4: Sketch `src/sam_io.inc`**

Initial version is a skeleton with the documented call points as Z80 macros or labels:

```asm
; SAM disk file I/O wrappers.
; Derived from docs/notes/sam-file-io.md.
; All routines preserve registers unless documented otherwise.

; open_input:   opens file IN for reading. HL → first byte.
; read_byte:    reads next byte from IN. A = byte, CY = EOF.
; close_input:  closes IN.
; create_output: creates file OUT for writing. HL → first byte.
; write_byte:   writes A to OUT. CY = error.
; close_output: closes OUT, flushes.

; (Bodies filled in Task 7 once stub.asm needs them.)

open_input:     ; TODO: rst 8 / hook call per spike
                ret

read_byte:      ; TODO
                ret

close_input:    ; TODO
                ret

create_output:  ; TODO
                ret

write_byte:     ; TODO
                ret

close_output:   ; TODO
                ret
```

The `TODO` markers are acceptable here because Task 7 fills them — they exist for *exactly one task* between spike and implementation.

- [ ] **Step 5: Commit**

```bash
git add docs/notes/sam-file-io.md src/sam_io.inc
git commit -m "docs: document SAM disk file I/O API (M0 spike)"
```

---

## Task 7: Stub assembler with file I/O — emits one nop

**Files:**
- Modify: `src/stub.asm`
- Modify: `src/sam_io.inc`
- Create: `tests/test-stub-emits-nop.sh`

The stub now opens `IN`, reads-and-discards its contents, then writes 4 bytes to `OUT`: the little-endian aarch64 encoding of `nop` (`0xd503201f` → bytes `1f 20 03 d5`).

- [ ] **Step 1: Write the failing test**

Create `tests/test-stub-emits-nop.sh`:

```bash
#!/usr/bin/env bash
# After running the stub, build/out.bin should contain exactly 4 bytes:
#   1f 20 03 d5  (little-endian aarch64 NOP).

set -euo pipefail

cd "$(dirname "$0")/.."

make stub
./tools/build-disk.sh tests/fixtures/nop.s build/test.mgt
./tools/run-simcoupe.sh build/test.mgt
./tools/extract-output.sh build/test.mgt build/out.bin

actual=$(xxd -p build/out.bin)
expected="1f2003d5"

if [ "$actual" != "$expected" ]; then
    echo "FAIL: out.bin = $actual, expected $expected"
    xxd build/out.bin
    exit 1
fi

echo "PASS (out.bin = $actual)"
```

```bash
chmod +x tests/test-stub-emits-nop.sh
```

- [ ] **Step 2: Implement `tools/extract-output.sh`**

```bash
#!/usr/bin/env bash
# Extracts the OUT file from a .mgt into a host file.
#
# Usage: extract-output.sh <disk.mgt> <output.bin>

set -euo pipefail

disk="$1"
output="$2"

cd "$(dirname "$0")/.."

samfile cat -i "$disk" -f OUT > "$output"

if [ ! -s "$output" ]; then
    echo "extract-output.sh: $output is empty (stub didn't write OUT, or filename mismatch)" >&2
    exit 1
fi

echo "Extracted $(wc -c < "$output") bytes → $output"
```

```bash
chmod +x tools/extract-output.sh
```

- [ ] **Step 3: Run the test, expect it to fail**

Run: `./tests/test-stub-emits-nop.sh`
Expected: failure — the stub still halts without writing anything, so `OUT` doesn't exist.

- [ ] **Step 4: Implement the stub's file I/O**

Update `src/sam_io.inc` with the actual ROM/DOS call bodies discovered in Task 6. Replace each `TODO ret` with the spike-documented call. Then update `src/stub.asm`:

```asm
; M0 stub assembler — opens IN, ignores contents, writes nop bytes to OUT.

                org     &8000

                include "sam_io.inc"

start:          di
                call    open_input
                jp      c, fail        ; CY = error

read_loop:      call    read_byte
                jr      nc, read_loop  ; loop until EOF

                call    close_input

                call    create_output
                jp      c, fail

                ld      a, &1f
                call    write_byte
                ld      a, &20
                call    write_byte
                ld      a, &03
                call    write_byte
                ld      a, &d5
                call    write_byte

                call    close_output

                halt

fail:           ; On error, halt with a distinctive border colour for debug.
                ld      a, &02         ; red border
                out     (&fe), a
                halt

                end     start
```

If the spike showed that the SAM DOS uses a different convention than `CY = error` / `CY = EOF`, adjust the conditional jumps accordingly — but keep the structure the same.

- [ ] **Step 5: Run the test, expect it to pass**

Run: `./tests/test-stub-emits-nop.sh`
Expected: `PASS (out.bin = 1f2003d5)`

If `out.bin` is empty: the stub's `OUT` file isn't being created — re-check the spike doc and the `create_output` body.

If `out.bin` has garbage / wrong bytes: byte order mistake (aarch64 is little-endian: low byte at low address) or write_byte not advancing the file pointer.

- [ ] **Step 6: Commit**

```bash
git add src/stub.asm src/sam_io.inc tools/extract-output.sh tests/test-stub-emits-nop.sh
git commit -m "feat: stub emits one aarch64 NOP via SAM file I/O"
```

---

## Task 8: Mac-side oracle — diff stub output vs `aarch64-none-elf-as`

**Files:**
- Create: `tools/diff-vs-gnu.sh`
- Create: `tests/test-diff-vs-gnu.sh`

The oracle script: assemble the same `.s` fixture with GNU `as`, extract `.text`, byte-diff against our stub's output. This is the validation pattern every future milestone reuses.

- [ ] **Step 1: Write the failing test**

Create `tests/test-diff-vs-gnu.sh`:

```bash
#!/usr/bin/env bash
# Exercises tools/diff-vs-gnu.sh on the nop fixture against the stub output.

set -euo pipefail

cd "$(dirname "$0")/.."

# Re-run the stub to make sure build/out.bin exists and is current.
./tests/test-stub-emits-nop.sh > /dev/null

# Now diff against GNU as.
if ! ./tools/diff-vs-gnu.sh tests/fixtures/nop.s build/out.bin; then
    echo "FAIL: diff-vs-gnu reported divergence"
    exit 1
fi

echo "PASS"
```

```bash
chmod +x tests/test-diff-vs-gnu.sh
```

- [ ] **Step 2: Run the test, expect it to fail**

Run: `./tests/test-diff-vs-gnu.sh`
Expected: failure — `diff-vs-gnu.sh` doesn't exist.

- [ ] **Step 3: Implement `tools/diff-vs-gnu.sh`**

```bash
#!/usr/bin/env bash
# Assembles a .s fixture with aarch64-none-elf-as, extracts .text via objcopy,
# byte-compares against a candidate output. Exit 0 = match, non-zero = differ.
#
# Usage: diff-vs-gnu.sh <fixture.s> <candidate.bin>

set -euo pipefail

fixture="$1"
candidate="$2"

cd "$(dirname "$0")/.."

mkdir -p build/oracle

aarch64-none-elf-as "$fixture" -o build/oracle/expected.o
aarch64-none-elf-objcopy -O binary build/oracle/expected.o build/oracle/expected.bin

if cmp -s build/oracle/expected.bin "$candidate"; then
    echo "MATCH: $candidate == GNU as output for $fixture"
    exit 0
fi

echo "DIVERGE:"
echo "  expected: $(xxd -p build/oracle/expected.bin)"
echo "  actual:   $(xxd -p "$candidate")"
echo
echo "Full diff:"
diff <(xxd build/oracle/expected.bin) <(xxd "$candidate") || true
exit 1
```

```bash
chmod +x tools/diff-vs-gnu.sh
```

- [ ] **Step 4: Run the test, expect it to pass**

Run: `./tests/test-diff-vs-gnu.sh`
Expected: `MATCH: build/out.bin == GNU as output for tests/fixtures/nop.s` followed by `PASS`.

If GNU `as` produces extra padding (sections aligned to a boundary): consider passing `-Ttext=0` via `aarch64-none-elf-ld` between `as` and `objcopy`, or use `aarch64-none-elf-objcopy --only-section=.text -O binary` — adjust until a one-instruction `nop.s` produces exactly 4 bytes.

- [ ] **Step 5: Commit**

```bash
git add tools/diff-vs-gnu.sh tests/test-diff-vs-gnu.sh
git commit -m "feat: byte-diff oracle vs aarch64-none-elf-as"
```

---

## Task 9: End-to-end round-trip driver

**Files:**
- Create: `tools/run-roundtrip.sh`

Wraps Tasks 5–8 into one command: given a `.s` fixture, build, run, extract, diff. This is what `make test` and CI invoke.

- [ ] **Step 1: Write `tools/run-roundtrip.sh`**

```bash
#!/usr/bin/env bash
# End-to-end round-trip test for one .s fixture.
#
# Usage: run-roundtrip.sh <fixture.s>

set -euo pipefail

fixture="$1"

cd "$(dirname "$0")/.."

echo "=== Round-trip: $fixture ==="

make stub
./tools/build-disk.sh "$fixture" build/test.mgt
./tools/run-simcoupe.sh build/test.mgt
./tools/extract-output.sh build/test.mgt build/out.bin
./tools/diff-vs-gnu.sh "$fixture" build/out.bin

echo "=== PASS: $fixture ==="
```

```bash
chmod +x tools/run-roundtrip.sh
```

- [ ] **Step 2: Run end-to-end via `make test`**

Run: `make test`
Expected: full pipeline executes; final line is `=== PASS: tests/fixtures/nop.s ===`.

- [ ] **Step 3: Commit**

```bash
git add tools/run-roundtrip.sh
git commit -m "feat: end-to-end round-trip driver"
```

---

## Task 10: GitHub Actions CI workflow

**Files:**
- Create: `.github/workflows/ci.yml`

Mirrors local `make ci` on every push and pull request. Linux runner, since SimCoupé builds for Linux.

- [ ] **Step 1: Write the workflow**

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

jobs:
  m0-roundtrip:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install GNU aarch64 binutils
        run: |
          sudo apt-get update
          sudo apt-get install -y binutils-aarch64-linux-gnu
          # Symlink the cross prefix our scripts expect.
          sudo ln -s /usr/bin/aarch64-linux-gnu-as     /usr/local/bin/aarch64-none-elf-as
          sudo ln -s /usr/bin/aarch64-linux-gnu-objcopy /usr/local/bin/aarch64-none-elf-objcopy

      - name: Install SimCoupé
        run: |
          # Adjust to the actual install method discovered in the M0 spike.
          # Options: apt-get install simcoupe, build from source, or download
          # a release binary.
          # See docs/notes/simcoupe-batch.md for Linux specifics.
          sudo apt-get install -y simcoupe xvfb || {
              echo "simcoupe not in apt — building from source"
              # Fallback: build from simonowen/simcoupe source.
              exit 1
          }

      - uses: actions/setup-python@v5
        with:
          python-version: "3.x"

      - name: Install pyz80
        run: |
          # pyz80 is distributed as a single Python script. Either pip-install
          # if pyz80 publishes a package, or clone and shim onto PATH.
          git clone https://github.com/simonowen/pyz80 /tmp/pyz80
          sudo ln -s /tmp/pyz80/pyz80.py /usr/local/bin/pyz80
          sudo chmod +x /tmp/pyz80/pyz80.py

      - uses: actions/setup-go@v5
        with:
          go-version: "stable"

      - name: Install samfile
        run: |
          go install github.com/petemoore/samfile/cmd/samfile@latest
          echo "$HOME/go/bin" >> "$GITHUB_PATH"

      - name: Verify toolchain
        run: make check

      - name: Round-trip oracle
        run: xvfb-run -a make ci
```

- [ ] **Step 2: Verify locally that `make ci` is green**

Run: `make ci`
Expected: clean run, exit 0.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: GitHub Actions workflow running M0 round-trip"
```

- [ ] **Step 4: Push the branch and watch CI**

```bash
git push -u origin main
```

Open the GitHub Actions tab. Watch the workflow run. If it fails, inspect logs, identify the failing step, fix, force-push or push-amended commit, repeat until green. **CI green is the gate for declaring M0 complete.**

Common failure modes:
- pyz80 install: simonowen/pyz80 may not be at the path assumed; check the repo and adjust.
- SimCoupé in apt: may not be packaged on Ubuntu; fall back to building from source (simonowen/simcoupe is cmake-based).
- xvfb: needed for any SDL-based program on a headless runner.
- Tool naming: GNU's package may install as `aarch64-linux-gnu-as` rather than `aarch64-none-elf-as` — the symlinks in the workflow handle that, but verify.

---

## Task 11: M0 success — declare done

**Files:**
- Modify: `README.md` (status line)

- [ ] **Step 1: Update README status**

Edit `README.md` and replace:
```
## Status

Brainstorming / design phase. See `docs/specs/` for design documents.
```
with:
```
## Status

M0 (toolchain bootstrap) complete — pyz80 + SimCoupé + samfile + GNU as
round-trip is wired end-to-end and passing in CI. See `docs/specs/` for
design and `docs/plans/` for milestone plans. Next milestone: M1 (binary
tokenised source format + `text2bin` / `bin2text`).
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: M0 toolchain bootstrap complete"
```

- [ ] **Step 3: Sanity check final state**

Run: `make ci`
Expected: green.

Run: `git log --oneline`
Expected: roughly 11 commits since the bootstrap commit, each scoped to one task.

---

## What M0 explicitly does NOT include

- Any aarch64 instruction other than `nop` (M3+ work).
- Any handling of `IN`'s contents (the stub reads-and-discards). M3 is the first milestone where input drives output.
- Symbol table, expressions, two-pass design (M4).
- Any binary tokenised format work (M1).
- Encoder tables (M2).
- Any test fixtures beyond `nop.s`.

These deferrals are deliberate: M0's scope is "everything around the assembler" so the assembler itself can be built confidently against working infrastructure.

## Open items inherited from spec

- `:lo12:` / `:hi12:` actual usage in spectrum4 — verify before M3 (still unresolved at end of M0; tracked in spec).
- Quazar Trinity SD card programming docs — not needed for M0; relevant from Phase 3 onward.
- Encoder tables embedded vs loaded from disk — not decided in M0; resolved during M2 brainstorm.
