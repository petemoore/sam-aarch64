# Running SimCoupé headlessly (CI / Docker)

The single source of truth for what SimCoupé needs in a headless
environment — Linux container, no display, no audio. Everything in
this doc is empirically verified against the live `sam-aarch64-ci`
container and against `simcoupe` v1.2.15 (upstream pinned commit
`0f74cff52b96841fe0efa01ffd1a6875b253e72a`) plus the vendored patch
at `tools/simcoupe-exitonhalt.patch`.

## TL;DR — the working incantation

```bash
# Inside the container.
Xvfb :150 -screen 0 1280x1024x24 &
export DISPLAY=:150
export SDL_VIDEODRIVER=x11
export SDL_AUDIODRIVER=dummy
simcoupe -exitonhalt 1 -fullscreen 0 -firstrun 0 disk.mgt
```

Exit codes:
- `0` — Z80 hit `DI; HALT`; clean exit via the `-exitonhalt` patch.
- `124` — `timeout` killed it (boot didn't reach the exit signal in time).
- `134`, `139` etc. — crash; investigate.

## Why each piece is necessary

### `SDL_AUDIODRIVER=dummy` — required

SimCoupé calls `SDL_OpenAudioDevice` at startup. Inside a stock
container with no `/dev/snd` and no PulseAudio/ALSA daemon, ALSA
fails with `cannot find card '0'` and SDL aborts initialisation.
SimCoupé treats subsystem-init failures as silent exit-0
(`Base/Main.cpp:55` short-circuits), which is the worst-possible
outcome for CI (looks like a passing run). `SDL_AUDIODRIVER=dummy`
gives SDL a no-op audio backend that always opens. Purely
environmental — no patch needed.

### `SDL_VIDEODRIVER=x11` + Xvfb — required

You can't use `SDL_VIDEODRIVER=dummy`. SimCoupé needs a real
renderer because it requests `SDL_RENDERER_ACCELERATED`, which the
SDL dummy video driver doesn't satisfy. Xvfb provides an invisible
X display; `SDL_VIDEODRIVER=x11` attaches SimCoupé to it; Mesa's
software GL (llvmpipe) provides the renderer.

### Xvfb depth ≥24

```
Xvfb :150 -screen 0 1280x1024x24
```

The default Xvfb depth is 8. Mesa llvmpipe needs ≥24 to advertise
the visual SimCoupé asks for. Without `-screen 0 ...x24`,
`SDL_RENDERER_ACCELERATED` fails and SimCoupé aborts.

### Display number — pick fresh per session

Xvfb hard-fails to start if the X11 abstract socket for that display
is bound by another process — including a *zombied* Xvfb. The
`sam-aarch64-ci` container's PID 1 is `sleep infinity`, which does
*not* reap children, so killed/timed-out Xvfb processes stack up as
zombies, each holding their socket. Symptoms:

```
_XSERVTransSocketUNIXCreateListener: ...SocketCreateListener() failed
_XSERVTransMakeAllCOTSServerListeners: server already running
(EE) Cannot establish any listening sockets ...
```

Workaround: pick a display number that hasn't been used in the
container's lifetime (e.g. `:150`, `:151`, `:160`...). The ci.yml
workflow uses a fresh runner per job so this isn't an issue there.

For long dev sessions, periodically:

```bash
docker exec sam-aarch64-ci bash -c 'pkill -9 Xvfb; rm -f /tmp/.X*-lock /tmp/.X11-unix/X*'
```

A future improvement: run the container with `--init` (tini) which
reaps zombies properly.

### Mesa software GL packages

```
libgl1-mesa-dri mesa-utils
```

`libgl1-mesa-dri` is the actual llvmpipe driver. `mesa-utils`
brings `glxinfo` etc. for diagnosis. Both are pinned in
`tools/Dockerfile.dev`.

### ImageMagick `import` (optional, for debugging)

```
import -window root /tmp/screenshot.png
```

Captures the current Xvfb display contents to a PNG. Useful when
investigating why a real-world disk like FRED 56 boots or doesn't —
see `docs/notes/fred-disk-inspection.md` for an example. Comes from
the `imagemagick` apt package.

## Getting the dev container

The pre-built image is published to GitHub Container Registry by the
project's CI workflow on every push to `main`. It's the same image CI
runs the round-trip oracle against, so local and CI are guaranteed
identical:

```bash
docker pull ghcr.io/petemoore/sam-aarch64-dev:latest

cd /Users/pmoore/git/sam-aarch64
docker run -d --name sam-aarch64-ci \
    -v "$PWD:/work" -w /work \
    ghcr.io/petemoore/sam-aarch64-dev:latest sleep infinity
docker exec -it sam-aarch64-ci bash
```

The image is multi-arch (`linux/amd64` + `linux/arm64`); Docker picks
the variant matching your host. On Apple Silicon you get native arm64.

The image has SimCoupé, pyz80, samfile, and the aarch64 cross binutils
pre-installed, along with the SimCoupé ROM resources at
`/usr/local/share/simcoupe/`. From inside the container, `make ci` in
`/work` runs the whole round-trip.

### Building the image locally (instead of pulling)

If you want to test a Dockerfile change before pushing, or you're
working offline:

```bash
cd /Users/pmoore/git/sam-aarch64
docker build -t sam-aarch64-dev:local -f tools/Dockerfile.dev tools/
docker run -d --name sam-aarch64-ci \
    -v "$PWD:/work" -w /work \
    sam-aarch64-dev:local sleep infinity
```

Same image, just locally-tagged. Equivalent in every other way.

## Smoke test

```bash
docker exec sam-aarch64-ci bash -lc '
    Xvfb :151 -screen 0 1280x1024x24 &
    export DISPLAY=:151 SDL_VIDEODRIVER=x11 SDL_AUDIODRIVER=dummy
    cd /work && make ci
'
```

## Working on the simcoupé patch

The image pre-builds SimCoupé from `tools/simcoupe-exitonhalt.patch`,
so changing the patch normally requires rebuilding the image (slow,
multi-arch, ~minutes) before you can test the change. To iterate on
the patch faster, rebuild SimCoupé in-place inside an existing
container instead:

```bash
docker exec sam-aarch64-ci bash -lc '
    PINNED_SHA=0f74cff52b96841fe0efa01ffd1a6875b253e72a
    cd /tmp && rm -rf simcoupe
    git clone https://github.com/simonowen/simcoupe.git
    cd simcoupe && git fetch --depth=1 origin "$PINNED_SHA"
    git checkout "$PINNED_SHA"
    git apply --check /work/tools/simcoupe-exitonhalt.patch
    git apply /work/tools/simcoupe-exitonhalt.patch
    cmake -B build -DCMAKE_BUILD_TYPE=Release
    cmake --build build -j$(nproc)
    cmake --install build
    cp build/_deps/saasound-build/libSAASound.so.3 /usr/local/lib/
    ldconfig
'
```

That replaces `/usr/local/bin/simcoupe` and the ROM resources without
rebuilding the whole Docker image. When you're happy with the patch,
commit it and let CI rebuild the image properly.

## Native macOS (no Docker)

Native macOS works end-to-end with a few quirks. The stock
`/Applications/SimCoupe.app` is unpatched, so the round-trip oracle's
exit detection won't fire against it — the test would hit its 30s
timeout. You need to build a patched binary from source.

```bash
# 1. Brew dep (one-time; sdl2 fmt libpng cmake assumed already present).
brew install libsamplerate

# 2. Clone simcoupé and apply the vendored patch.
cd ~/git
git clone https://github.com/simonowen/simcoupe.git   # if not already there
cd simcoupe
PINNED_SHA=0f74cff52b96841fe0efa01ffd1a6875b253e72a
git fetch --depth=1 origin "$PINNED_SHA"
git checkout "$PINNED_SHA"
git apply /Users/pmoore/git/sam-aarch64/tools/simcoupe-exitonhalt.patch

# 3. Build. The non-obvious CMake hints:
#    - CMAKE_PREFIX_PATH=/opt/homebrew so find_package(SDL2) finds brew SDL2
#    - {CXX,C,OBJC}_FLAGS=-I/opt/homebrew/include because simcoupé uses
#      both `#include "SDL2/SDL.h"` (needs parent on include path) and
#      `#include <SDL_opengl.h>` (needs SDL2 dir itself), and the .m file
#      compiles with the C/OBJC flag set, not the CXX one.
cmake -B build -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_PREFIX_PATH=/opt/homebrew \
    -DCMAKE_CXX_FLAGS=-I/opt/homebrew/include \
    -DCMAKE_C_FLAGS=-I/opt/homebrew/include \
    -DCMAKE_OBJC_FLAGS=-I/opt/homebrew/include
cmake --build build -j

# 4. Make the patched binary the one `make ci` picks up. Either:
#    a) Replace /usr/local/bin/simcoupe symlink (requires sudo):
sudo ln -sfn ~/git/simcoupe/build/SimCoupe.app/Contents/MacOS/SimCoupe \
    /usr/local/bin/simcoupe
#    b) Or put the .app's MacOS dir first on PATH per-session:
export PATH=~/git/simcoupe/build/SimCoupe.app/Contents/MacOS:$PATH
```

Then `make ci` from `/Users/pmoore/git/sam-aarch64` should pass
natively in ~1.5s.

The build produces a full `SimCoupe.app/` bundle with the binary at
`Contents/MacOS/SimCoupe` and ROM resources at `Contents/Resources/`.
SDL's `SDL_GetBasePath()` resolves to the bundle's Resources/ directory
automatically when the binary is invoked from inside the bundle
structure — that's how simcoupé finds `samcoupe.rom` and
`sp0256-al2.bin` without us having to set anything.

### Why the stub ends in `DI; HALT`

The Z80 stub in `src/stub.asm` ends with:

```asm
di
halt              ; HALT with IFF1=0 — caught by sam_cpu::on_halt
```

The patched SimCoupé's `on_halt` override fires when the Z80 executes
HALT with `IFF1=0`, sets a quit flag, and the main `Run()` loop exits
on the next iteration. This is the conventional Z80 "we are done"
idiom — a HALT with interrupts disabled can never be woken by a
maskable interrupt, so it's unambiguous.

The `di` immediately before `halt` is load-bearing. SAMDOS's RST 8
dispatcher (ROM `PTDOS`) does `EI` inside the hook window, so the
`di` at `start:` in the stub has been undone by the time we reach
this point after HSAVE. Without the trailing `di`, `IFF1=1` and
`on_halt`'s quit check correctly does not trigger.

An earlier iteration of the patch added a second exit mechanism — a
magic `OUT (&DEAD), &C0` port write caught by `sam_cpu::on_output` —
in the belief that `on_halt` CRTP dispatch was unreliable on some
platforms. That diagnosis was wrong: the underlying bug was the
missing trailing `di`, not the dispatch. With the `di` in place,
`on_halt` fires reliably on every toolchain tested (Apple clang on
arm64, gcc-13 on Linux amd64+arm64, GHA `ubuntu-latest`). The
`on_output` override was removed and the patch shrank to a single
commit; the upstream PR (`simonowen/simcoupe#109`) reflects that.

## Related files

- `tools/Dockerfile.dev` — image recipe (single source of truth for CI
  and local dev).
- `tools/simcoupe-exitonhalt.patch` — vendored SimCoupé patch.
- `tools/run-simcoupe.sh` — invocation wrapper used by `make`.
- `.github/workflows/ci.yml` — builds + publishes the image; runs the
  round-trip in it.
- `docs/notes/m0-status.md` — current state of the M0 milestone.
- `docs/notes/fred-disk-inspection.md` — example of using ImageMagick
  `import` to verify a real disk boots.
