# SimCoupé batch-mode invocation (M0 spike)

Status: **research spike, M0 Task 1**. Output: this document, plus a draft
patch on a sibling branch in `~/git/simcoupe/` (commit `76e5198` on branch
`exit-on-halt`). The patch is **not pushed** anywhere — Pete to review.

## TL;DR

- SimCoupé v1.2.15 has **no batch / headless / exit-on-halt mode**. Confirmed
  by reading `Base/Options.cpp`, `Base/Main.cpp`, `Base/CPU.cpp`, `SDL/UI.cpp`:
  the only path out of the main emulation loop is an `SDL_QUIT` event from the
  window system, plus the `Ctrl-F12` keybinding which generates one.
- **Recommendation: Option B — patch SimCoupé upstream.** Add a new boolean
  command-line option `-exitonhalt 1` that quits cleanly when the Z80 executes
  `HALT` with interrupts disabled. The patch is 23 lines across 4 files. A
  draft commit exists locally on `~/git/simcoupe` branch `exit-on-halt`.
- The auto-run mechanism on disk is **a BASIC file named `auto`, saved with an
  auto-run line number** (`SAVE "auto" LINE 10`). SAMDOS does *not* auto-execute
  a code file named `auto`; only BASIC files and 48k snapshots are recognised
  as auto-runnable by `autox`. The minimal BASIC loader is two lines:

  ```basic
  10 LOAD "stub" CODE
  20 CALL 32768
  ```

- Linux/macOS CI invocation (after the patch lands and a fresh build is
  available): `simcoupe -exitonhalt 1 work.mgt`. The headless environment
  setup (Xvfb, SDL drivers, Mesa software GL, ImageMagick for screenshots)
  is documented separately in
  [`docs/notes/headless-simcoupe.md`](headless-simcoupe.md). **The earlier
  claim in this doc that `SDL_VIDEODRIVER=dummy` works without Xvfb has
  been retracted** — empirically it does not satisfy SimCoupé's
  `SDL_RENDERER_ACCELERATED` request and the emulator silently exits 0
  before the Z80 starts. Xvfb + Mesa llvmpipe is required.

## Install path

- macOS: pre-installed app bundle at `/Applications/SimCoupe.app/`. Binary at
  `/Applications/SimCoupe.app/Contents/MacOS/SimCoupe`. Bundle dates from
  May 2022 — predates several upstream changes including v1.2.15. **The
  installed binary is NOT a viable build to develop against**: it doesn't have
  `--help`/`--version` (CLI option parsing is positional / `-key value`), and
  it lacks the patch we need. Plan: build from source on Linux for CI, and
  build a fresh local binary from `~/git/simcoupe` on macOS for dev (see
  "Build" notes below).
- Source at `~/git/simcoupe`, on `main` at `0f74cff Updated version to v1.2.15`.
  Patch lives on branch `exit-on-halt`, commit `76e5198`.
- Linux: install via the `simcoupe` package on Debian/Ubuntu derivatives
  (older), or build from source. CI will clone upstream `simonowen/simcoupe`
  at a pinned commit, then `git apply` the `-exitonhalt` patch vendored at
  `tools/simcoupe-exitonhalt.patch` in this repo. See "Acquiring the
  patched SimCoupé in CI" below for the exact recipe.

## CLI option survey

`Base/Options.cpp::Load` parses `-key value` pairs by walking `argv`. Bare
filenames are inserted into drive 1 (then drive 2). Options come from a shared
registry of `setting=value` lines; the same names work in `SimCoupe.cfg` and on
the command line. Notable settings for our use:

| Option       | Effect                                                              |
| ------------ | ------------------------------------------------------------------- |
| `disk1`      | Path to floppy image in drive 1.                                    |
| `disk2`      | Path to floppy image in drive 2.                                    |
| `autoboot`   | Automatically press F9 (BOOT) when a disk is supplied (default on, also forced on at startup). |
| `autoload`   | Auto-run media inserted at the startup screen (default on).         |
| `speed`      | Emulation speed in percent, 50–1000. Bigger = faster.               |
| `turbodisk`  | Run at turbo speed during disk I/O (default on).                    |
| `fastreset`  | Run at turbo speed during the SAM ROM memory test (default on).     |
| `fullscreen` | Start fullscreen (we don't want this).                              |
| `firstrun`   | Triggers the welcome dialog on Linux first run (set 0 to suppress). |

There is **no** `--exit-on-halt`, `--batch`, `--headless`, `--exit-after`,
`--max-cycles`, `--script`, AVI-record-and-quit, or any equivalent. The only
existing exit paths are `SDL_QUIT` (window close) and the `ExitApp` action
(default `Ctrl-F12` / `Alt-F4` keybinding). Sending those programmatically
would require a debugger script or an external X automation tool — both
unattractive.

A *bare-filename* invocation is the canonical way to autoboot a disk:

```
simcoupe path/to/work.mgt
```

`Options::Load` interprets `path/to/work.mgt` as a positional drive-1 disk and
unconditionally sets `autoboot=true` for the run. The boot keystroke (`\xc9` =
F9 = BOOT) is queued; the SAM ROM picks it up at the next `READKEY` ROM hook
and the disk's auto file runs.

## Why no native batch mode

Looking at `SDL/UI.cpp::CheckEvents` and `Base/CPU.cpp::Run`:

```cpp
// CPU.cpp
void Run()
{
    while (UI::CheckEvents())     // returns false only on SDL_QUIT
    {
        if (g_fPaused) continue;
        ...
        ExecuteChunk();
        ...
    }
}
```

`UI::CheckEvents` returns `false` only on `SDL_QUIT`. `ExecuteChunk` exits its
inner loop on `g_fBreak`. There's no global "we are done, kill the emulator"
signal. The cleanest place to inject one is the Z80 core's `on_halt` hook
(see below).

## Decision: Option B (upstream patch)

Recommending Option B over Option A for the following reasons.

1. **Reliability**. Every M-milestone after M0 runs the round-trip suite in
   CI on every commit. Pete's note: "thousands of these in CI; flaky timeouts
   compound." Option A (external timeout, kill on output-file appearance)
   introduces a probabilistic flake on every run, multiplied by every PR.
   Option B is deterministic: the Z80 reaches `DI; HALT`, the emulator exits
   with code 0. No timing windows, no kill signals, no false positives if the
   stub crashes before producing output.

2. **Speed / total CI time**. With external timeouts, every passing run pays
   the timeout's safety margin (or polls a file every N ms). Option B exits
   the *instant* the program finishes — likely sub-second per round-trip with
   `speed=1000` + `turbodisk=1` + `fastreset=1`. Across thousands of fixtures
   this matters.

3. **Clean exit code semantics.** Option A treats `124` (the `timeout(1)`
   timeout exit) as success, which is the wrong inversion: a real bug that
   makes the stub hang would also exit `124` and be silently green. Option B
   exits 0 on success and is free to exit non-zero on detected error
   conditions later (e.g. an `outNN` halt-with-error opcode trap) without any
   wrapper to disambiguate.

4. **Tiny patch**. ~23 lines, mechanical, follows existing code idioms (see
   below). Pete is open to upstream PRs; this is exactly the sort of change
   Simon Owen has been receptive to (see `breakonexec` debug option already
   in tree).

5. **No new dependencies**. The Z80 core (kosarev/z80) already exposes
   `on_halt()` and `on_get_iff1()` as overrideable hooks. SimCoupé's
   `sam_cpu` already overrides several others (`on_ei`, `on_ret`, `on_rst`,
   `on_get_int_vector`). The override pattern is already established.

6. **Convention**. `DI; HALT` is the conventional "we are done, please stop"
   instruction sequence on Z80 systems. There's no possible legitimate use
   of it in normal SAM programs (the CPU would deadlock — no interrupt can
   ever wake it), so flagging it as a clean exit is unambiguous and won't
   false-trigger on the SAM ROM's idle loop (which uses `EI; HALT`).

Trade-off: Option B requires maintaining a fork (or merging upstream)
*before* M0 Task 5 (run halt stub end-to-end in SimCoupé) can be ticked
green. Worst case: ~2 hours to build a Linux CI binary from the fork; ~half
a day if the upstream PR cycle stalls. Within the half-day budget the spike
brief sets.

## The patch (draft)

Local commit `76e5198` on `~/git/simcoupe` branch `exit-on-halt`. Files
changed:

- **`Base/Options.h`** (+2 lines)

  Adds a new `bool exitonhalt = false;` to `struct Config`, alongside the
  existing `breakonexec`/`rasterdebug` debug-style flags.

- **`Base/Options.cpp`** (+3 lines)

  - One line in `SetNamedValue` registering `"exitonhalt"` with the registry
    used by both `SimCoupe.cfg` and the command-line parser.
  - A two-line comment in `Save()` documenting that the flag is *not*
    persisted (it's a transient harness flag, set only via the CLI per run).

- **`Base/CPU.h`** (+16 lines)

  Adds an `on_halt()` override on `sam_cpu`:

  ```cpp
  void on_halt()
  {
      if (GetOption(exitonhalt) && !base::on_get_iff1())
      {
          g_fQuit = true;
          g_fBreak = true;
      }
      base::on_halt();
  }
  ```

  Also declares `extern bool g_fQuit;` next to the existing `g_fBreak`/
  `g_fPaused` globals.

- **`Base/CPU.cpp`** (+2 lines, -1 line)

  - Defines `bool g_fQuit;` next to `g_fBreak`.
  - Changes the `Run()` loop condition from `while (UI::CheckEvents())` to
    `while (!g_fQuit && UI::CheckEvents())`.

That's the entire patch. The semantics are: when `-exitonhalt 1` is in effect
and the Z80 executes `HALT` with `IFF1==0` (interrupts disabled), set the
quit flag *and* break out of the current `ExecuteChunk` immediately; the
outer `Run()` loop sees the flag on its next iteration and falls through to
`Main::Exit()`. With `-exitonhalt` not set, the override calls
`base::on_halt()` unconditionally so behaviour is identical to upstream.

False-trigger analysis: the SAM ROM's keyboard wait uses `EI; HALT` (so the
frame interrupt can wake it), so `IFF1==1` and our condition is false. We
have not been able to find any ROM code path that does `DI; HALT`. User
programs that legitimately want to spin without exiting won't use `DI; HALT`
either (it's a deadlock on real hardware). So the patch is safe by
construction.

The patch does **not** bump `ConfigVersion` because no persisted field
changed; existing `SimCoupe.cfg` files load unchanged.

## Acquiring the patched SimCoupé in CI

The patch is vendored in this repo as a single-commit `git format-patch`
output at `tools/simcoupe-exitonhalt.patch` (~4.5 KB, 4 files changed).
CI clones upstream at a pinned SHA and applies the patch on top — no fork,
no submodule, no external mirror.

- **Pinned upstream commit**: `0f74cff52b96841fe0efa01ffd1a6875b253e72a`
  (`simonowen/simcoupe@main`, "Updated version to v1.2.15"). This is the
  parent commit of the local `exit-on-halt` branch, so `git apply` of the
  vendored patch will apply cleanly without conflicts.
- **Vendored patch**: `tools/simcoupe-exitonhalt.patch` in this repo.
- **Recipe** (Linux CI runner — Ubuntu, `ubuntu-latest` is fine):

  ```sh
  # System deps (SDL2, fmt, build tooling). Network access also required
  # for the cmake FetchContent of kosarev/z80.
  sudo apt-get update
  sudo apt-get install -y \
      build-essential cmake git \
      libsdl2-dev libfmt-dev zlib1g-dev libpng-dev libsamplerate0-dev

  # Fetch source at a pinned SHA.
  git clone https://github.com/simonowen/simcoupe.git
  cd simcoupe
  git checkout 0f74cff52b96841fe0efa01ffd1a6875b253e72a

  # Apply the vendored exit-on-halt patch.
  git apply ../tools/simcoupe-exitonhalt.patch

  # Build.
  cmake -B build -DCMAKE_BUILD_TYPE=Release
  cmake --build build -j

  # Resulting binary: ./build/simcoupe
  ./build/simcoupe -exitonhalt 1 ../path/to/work.mgt
  ```

  Use `git apply --check ../tools/simcoupe-exitonhalt.patch` first if you
  want to assert apply-cleanliness as a separate CI step (it's a useful
  early failure if upstream rebases the pinned SHA out of existence).

- **When upstream merges the patch**: drop the `git apply` step, bump the
  pinned SHA to a commit that contains the merge, and delete
  `tools/simcoupe-exitonhalt.patch` from this repo. The CLI surface
  (`-exitonhalt 1`) is the contract; the rest of the round-trip harness
  is unaffected.
- **When the vendored patch goes stale**: if upstream changes any of the
  4 patched files materially (unlikely for `Base/CPU.cpp` /
  `Base/Options.cpp`), re-apply the patch to a newer SHA locally,
  regenerate with `git -C ~/git/simcoupe format-patch -1 <new-commit>
  --stdout > tools/simcoupe-exitonhalt.patch`, and bump the pinned SHA
  in CI.

### Building locally on Linux

If you already have the source at `~/git/simcoupe` (e.g. Pete's dev box),
skip the clone and just apply the patch on top of the pinned base:

```sh
cd ~/git/simcoupe
git checkout 0f74cff52b96841fe0efa01ffd1a6875b253e72a
git apply /path/to/assembler/tools/simcoupe-exitonhalt.patch
cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build -j
./build/simcoupe -exitonhalt 1 work.mgt
```

(Alternatively, just check out the local `exit-on-halt` branch, which
already contains the same commit as the patch.)

### Building locally on macOS

**Currently broken** — not because of anything in this patch, but because
of pre-existing C++ toolchain issues on Pete's Mac (Apple CLT missing
`<optional>`/`<variant>` headers under `/usr/bin/c++`; building with
Homebrew `llvm` runs into an `fmt`-vs-Apple-`math.h` `isfinite` /
`signbit` macro collision). See "Tested invocation" below for details.
**Use Linux for SimCoupé builds for now.** A future M-milestone task may
revisit this once Pete's CLT install is fixed.

## Auto-run mechanism

This was the second arm of the spike (step 2b). Question: when SAMDOS boots
a disk and finds an `auto` file, must it be a BASIC loader, or can it be a
code file with load/exec addresses?

**Answer: it must be a BASIC file with an auto-run line number.** Evidence:

1. **SAMDOS source (`~/git/samdos/src`).** The autoboot path is:

   - The boot entrypoint `init` / `initx` in `h.s` issues a synthetic
     `LOAD` token (`&95`) and calls into BASIC's command dispatcher at
     `&5b74` — i.e. autoboot is implemented as a *forced BASIC `LOAD
     "auto"` from the ROM*.
   - `h.s::hauto` (line 224) sets up the filename glob `AUTO*` and calls
     into the directory search routine `fdhr` (with control byte `&10`),
     then `gtflx` and jumps to `autox` in `f.s` (line 531).
   - `autox` reads the loaded file's type byte (`difa`) and dispatches:
     - **Type `&14` (= 20 = 48k snapshot):** loads to `&8000` and snaps.
     - **Type `&10` (= 16 = SAM BASIC):** the long path at `dlvm1`
       (f.s:561+) reconstructs the BASIC `LINE` auto-run pointers and
       falls through to BASIC's RUN handler — i.e. the `auto` file's
       saved auto-run line number is honoured.
     - **Anything else (including type `&13` = 19 = code):** falls
       through to `dlvm2` → `txinf`/`txhed`/`endsx`, which prints header
       info and returns to BASIC's `Ready` prompt. The file body has
       *not* been loaded into a useful place and execution is *not*
       auto-CALLed.

   So a code file named `auto` gets dropped at the BASIC `Ready` prompt
   without execution; only BASIC files (with their saved auto-run line)
   actually run.

2. **SimCoupé manual (`~/git/simcoupe/Manual.md`).** Lines 67–69 confirm
   this from the user-visible side:

   > DOS was loaded and an "auto" file was found, but there was no auto-run
   > line number to execute from. Use LIST to check for a BASIC listing,
   > and RUN to execute it.

3. **Existing example.** `reference/comet-disk/auto COMET` (the COMET
   tape's auto file) is a tokenised BASIC file. `xxd` of its first 32 bytes
   shows the SAM BASIC line-number / line-length / token pattern:
   `00 0A 34 00 E7 31 ...` = line 10, length `0034`, token `&E7` (LET),
   etc. Type-byte (in the directory entry, not the file body) would be
   `&10`.

So the minimal stub on disk needs two files:

1. A SAM BASIC file named `auto` with an auto-run line number, e.g.

   ```basic
   10 LOAD "stub" CODE
   20 CALL 32768
   ```

   saved as `SAVE "auto" LINE 10`. The on-disk type byte is `16` (SAM BASIC)
   and the directory entry's auto-run-line field gets `10`.

2. The actual code file, type `19` (code) with load address `&8000` (32768).

Practical creation path on the host (no SAM Coupé needed):

- Build the code file with `pyz80` (M0 Task 4).
- Write the BASIC `auto` file by **hand-rolling the tokenised bytes**.
  See "Generating the BASIC `auto` file (for Task 5's build-disk.sh)"
  below for the full byte sequence and a script-able construction
  recipe. The tokenisation table was reverse-engineered from
  `~/git/samfile/keywords.go` + `sambasic.go` and round-trip-verified
  against `samfile basic-to-text`.
- `samfile add -i work.mgt -f stub -c -l 32768 -e 32768 ...` to add the code
  file with execution address `&8000` (so even if BASIC's `LOAD CODE` is
  redundant, it's safe).
- `samfile` does not currently have a "add BASIC file with auto-run line"
  command. The recipe below produces the BASIC file body bytes; combined
  with a directory-entry type byte of `&10` and the auto-run line in the
  appropriate directory slot, M0 Task 5's `build-disk.sh` can build the
  full disk image directly. If extending `samfile` later proves cleaner,
  the byte sequence below is the contract that command needs to emit.

Note: there's a *possible* simplification. Since `samfile add -c -e <exec>`
sets an execution address on a code file, BASIC's `LOAD "name"` (without
`CODE`) on a code file with an exec address actually does CALL the exec
address — but only when invoked from BASIC. Since the autoboot path does
`SAVE "auto"` style behaviour and not a freeform LOAD, this doesn't help us
skip the BASIC loader. The BASIC loader is required.

## Generating the BASIC `auto` file (for Task 5's build-disk.sh)

We hand-roll the tokenised BASIC. The auto file is exactly **30 bytes**,
encoding a single auto-run line:

```basic
10 LOAD "stub" CODE : CALL 32768
```

`LOAD "stub" CODE` (no address) loads the code file at its saved load
address; `CALL 32768` jumps to `&8000` where pyz80 will have placed the
stub. (Saving the stub with both load- and exec-addresses set to `&8000`
would let us drop the explicit `CALL`, but the explicit form is more
robust and keeps Task 5 unambiguous.)

### File format reference

A SAM BASIC program file is a sequence of lines followed by a `0xff`
terminator. Each line is:

| Field         | Bytes     | Encoding                                    |
| ------------- | --------- | ------------------------------------------- |
| Line number   | 2         | **big-endian** uint16                       |
| Line length   | 2         | **little-endian** uint16, body bytes only   |
| Body          | *length*  | tokens + ASCII + numeric encodings + `0x0d` |

(`0x0d` ends the body and counts toward the length.)

The token bytes we need (cross-verified with the round-trip probe at
`/tmp/samdecode/main.go`, derived from `~/git/samfile/keywords.go`):

| Token  | Encoding   | Notes                                  |
| ------ | ---------- | -------------------------------------- |
| `LOAD` | `0x95`     | Statement keyword (direct byte).       |
| `CODE` | `0xff 0x6c`| Function/expression token (prefixed).  |
| `CALL` | `0xe4`     | Statement keyword (direct byte).       |

Numeric integer literals appear in the source as their ASCII digits
(`'3', '2', '7', '6', '8'`) **followed by** a 6-byte binary encoding:
`0x0e 0x00 0x00 LO HI 0x00`, where `LO`/`HI` are the little-endian uint16
value. So `32768` → `0x0e 0x00 0x00 0x00 0x80 0x00`.

### The 30 bytes

```
00 0a 19 00 95 20 22 73 74 75 62 22 20 ff 6c 3a
e4 33 32 37 36 38 0e 00 00 00 80 00 0d ff
```

Annotated:

```
00 0a              line number 10 (BE)
19 00              line length 0x0019 = 25 bytes (LE), covers everything
                   from the LOAD token up to and including the 0x0d
  95               LOAD
  20               ' '          (cosmetic, optional but kept for clarity)
  22 73 74 75 62 22  "stub"
  20               ' '
  ff 6c            CODE         (function token, 0xff-prefixed)
  3a               ':'          statement separator
  e4               CALL
  33 32 37 36 38   '32768'      ASCII digits (printed form)
  0e 00 00 00 80 00  numeric encoding for 32768 (0x8000)
  0d               line terminator
ff                 file terminator
```

### Constructing it from a script

Trivial in any language. Python one-liner for `build-disk.sh`:

```sh
python3 -c 'open("auto","wb").write(bytes.fromhex(
    "000a1900"           # line 10 (BE), length 0x0019 (LE)
    "9520227374756222"   # LOAD " s t u b "
    "20ff6c"             # _ CODE
    "3a"                 # :
    "e4"                 # CALL
    "3332373638"         # "32768" (printed digits)
    "0e0000008000"       # numeric encoding for 32768
    "0d"                 # line terminator
    "ff"                 # file terminator
))'
```

Or as a shell `printf`:

```sh
printf '\x00\x0a\x19\x00\x95\x20\x22stub\x22\x20\xff\x6c\x3a\xe4'\
'32768\x0e\x00\x00\x00\x80\x00\x0d\xff' > auto
```

### Directory-entry side

The 30 bytes above are the **file body**. The MGT/DSK directory entry that
points at this body must additionally:

- Set the file type byte to `0x10` (= 16, "SAM BASIC") — this is what
  `autox` in SAMDOS dispatches on; type `0x13` (code) would skip BASIC
  execution entirely.
- Set the auto-run line number field to `10` (matching the BASIC line
  number above) so SAMDOS executes from line 10 instead of dropping to
  the `Ready` prompt.

`samfile add` does not currently expose these knobs (it's a code-file-add
tool); M0 Task 5's `build-disk.sh` will need to set them directly when
laying out the directory sector. The MGT directory format is documented
in `~/git/samdos/src/h.s` (entry layout) and `~/git/simcoupe/Manual.md`
(file types).

### Verification

Round-trip the bytes through `samfile basic-to-text`:

```sh
printf '\x00\x0a\x19\x00\x95\x20\x22stub\x22\x20\xff\x6c\x3a\xe4'\
'32768\x0e\x00\x00\x00\x80\x00\x0d\xff' \
  | (cd ~/git/samfile && go run ./cmd/samfile basic-to-text)
# expected:    10 LOAD  "stub"  CODE : CALL 32768
```

This was the verification used to confirm the byte sequence above —
samfile's decoder emits an extra space around tokens, but the source
recovers correctly.

## Tested invocation

I attempted to verify the recommended `simcoupe -exitonhalt 1 work.mgt`
invocation locally. **Could not fully verify in this environment**:

1. The pre-installed macOS app (`/Applications/SimCoupe.app/`, May 2022)
   doesn't have the `-exitonhalt` patch, so it can't be used to verify the
   batch-exit behaviour itself.

2. Building a fresh SimCoupé from `~/git/simcoupe` on this Mac fails:
   - Apple `/usr/bin/c++` advertises C++17 but the Command Line Tools
     install is missing the `<optional>` and `<variant>` headers; CMake's
     `check_include_file_cxx` correctly fails. (Pre-existing issue with
     this Mac's SDK install, unrelated to the patch.)
   - Building with `/opt/homebrew/opt/llvm/bin/clang++` (which *does* have
     C++17 headers) fails further in: the Apple SDK's `math.h` defines
     `isfinite`/`signbit` as macros that conflict with `fmt`'s function
     declarations of the same name. This is a known fmt-vs-Apple-SDK issue
     on certain macOS / CLT version combinations. Again, pre-existing,
     unrelated to the patch.

   *Both are environmental.* They will not bite Linux CI: `g++ 11+` /
   `clang 14+` on Ubuntu builds SimCoupé v1.2.15 cleanly per the upstream
   `linux-ci.yml` workflow.

3. Smoke test of the *unpatched* installed binary:
   `timeout 5 /Applications/SimCoupe.app/Contents/MacOS/SimCoupe test.mgt`
   exited via SIGTERM (143) after 5s with a window opened. Confirms (a) the
   binary accepts a positional `.mgt` and tries to autoboot, (b) there's no
   stdout chatter to lean on as a completion signal, and (c) without the
   patch we'd be stuck on Option-A timeouts.

What's left for **M0 Task 5** (Run halt stub end-to-end in SimCoupé):

- Build the patched SimCoupé on Linux (or fix the Apple SDK gap on Pete's
  Mac and build there).
- Build a halt-only stub via pyz80 (M0 Task 4).
- Build a `work.mgt` containing a BASIC `auto` loader + the code stub
  (M0 Task 4 / 7).
- `simcoupe -exitonhalt 1 work.mgt` — assert exit code 0 and runtime under
  N seconds.

If the patched binary on Linux behaves as designed, the round-trip wrapper
in M0 Task 9 is one-liner-trivial.

## Recommended invocation (final)

```sh
# Local dev (after building patched SimCoupé from source) — macOS native.
simcoupe -exitonhalt 1 work.mgt

# CI on Linux (and Linux dev container) — see headless-simcoupe.md for
# the full setup (Xvfb, SDL drivers, Mesa GL).
DISPLAY=:150 SDL_VIDEODRIVER=x11 SDL_AUDIODRIVER=dummy \
  ./simcoupe -exitonhalt 1 -fullscreen 0 -firstrun 0 work.mgt
```

`-firstrun 0` suppresses the welcome dialog on the first run for a fresh CI
runner. `-fullscreen 0` is paranoia (default is off, but we never want it).

Exit code is 0 on clean halt; non-zero if SimCoupé failed to start (missing
disk, bad config, SDL init failure, etc.). M0 Task 9 will wrap this with a
real timeout (e.g. 30s ceiling) as defence in depth — even with `-exitonhalt`
a stub bug could send the Z80 into a `JR -2` and never reach `HALT`.

## Known gotchas

- **Linux without X**: SDL dummy video drivers are NOT enough — see
  [`headless-simcoupe.md`](headless-simcoupe.md). You need Xvfb backing the
  display plus Mesa software GL.
- **macOS GUI app focus stealing (expected, not yet verified)**: macOS
  `.app` bundles typically steal focus when launched via Cocoa's
  `NSApplication` startup, regardless of `SDL_VIDEODRIVER=dummy`. We have
  *not* observed this directly — the patched binary couldn't be built on
  this Mac (see "Tested invocation" above), and the unpatched installed
  binary was only smoke-tested with a 5s timeout. **Flag during M0 Task 5**
  if it bites: the workaround would be to build the SDL frontend without
  the macOS bundle wrapper (the cmake target `simcoupe` produces a plain
  Mach-O under the build directory). On Linux CI this is a non-issue —
  there's no Cocoa.
- **`autoboot` is forced on per-run**, not loaded from `SimCoupe.cfg`
  (see `Options.cpp:182`: `g_config.autoboot = true;` unconditionally).
  No need to pass it on the CLI.
- **`SimCoupe.cfg` is written on every clean exit**. CI runners get a
  stale config from a previous run. To keep CI hermetic, either: (a) point
  `XDG_CONFIG_HOME` at a fresh tmp dir each run, or (b) accept the stale
  cfg and rely on CLI overrides taking precedence (`Options::Load` reads
  the file *first* and then walks argv, so CLI always wins). I recommend
  option (a) for CI cleanliness.
- **`exitonhalt` is intentionally NOT persisted** by the patch (see
  comment in `Save()`). Even if a CI step omits the flag on a later
  invocation, the previous run's setting won't leak.
- **The Z80 core's `on_halt` fires once** when the HALT opcode is decoded.
  The CPU then sits in halted state re-fetching HALT until an interrupt
  arrives. With `IFF1==0` no interrupt can fire — the patch breaks out of
  the loop *before* this becomes a deadlock.
- **kosarev/z80 is fetched at CMake configure time** via `FetchContent`;
  builds need network access on first configure. CI cache the
  `_deps/z80-src` directory between runs to avoid clone churn.

## Open question

The patch has been written and committed locally on
`~/git/simcoupe@exit-on-halt` but **has not been compile-verified** on
this Mac (environmental issues above). Before we commit to depending on
it for M0 Task 5, the next person to pick this up should either:

- Build it on a Linux box / CI runner and confirm a `DI; HALT` stub exits
  0 in well under a second; **or**
- Fix the macOS SDK / `fmt` issue locally (likely: reinstall Xcode CLT or
  add `-DFMT_USE_FLOAT128=0 -include cmath` to fmt's compile flags) and
  build there.

If either succeeds the patch can be PR'd upstream to simonowen/simcoupe.
If review takes too long Pete is happy to pin a fork in this project's
toolchain detection (M0 Task 2).
