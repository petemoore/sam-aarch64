# Running SimCoupé headlessly (CI / Docker)

The single source of truth for what SimCoupé needs in a headless
environment — Linux container, no display, no audio. Everything in
this doc is empirically verified against the live `sam-aarch64-ci`
container and against `simcoupe` v1.2.16 (upstream pinned commit
`0e8a69f3096fe00a00a29bd2303db6a7358021ad`), which ships `-exitonhalt`
natively.

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
- `0` — Z80 hit `DI; HALT`; clean exit via `-exitonhalt`.
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
see `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/fred-disk-inspection.md` for an example. Comes from
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

## Rebuilding simcoupé in-place

The image builds SimCoupé from the pinned upstream SHA. To rebuild it
inside an existing container (e.g. to test a different SHA) without
rebuilding the whole Docker image:

```bash
docker exec sam-aarch64-ci bash -lc '
    PINNED_SHA=0e8a69f3096fe00a00a29bd2303db6a7358021ad
    cd /tmp && rm -rf simcoupe
    git clone https://github.com/simonowen/simcoupe.git
    cd simcoupe && git fetch --depth=1 origin "$PINNED_SHA"
    git checkout "$PINNED_SHA"
    cmake -B build -DCMAKE_BUILD_TYPE=Release
    cmake --build build -j$(nproc)
    cmake --install build
    cp build/_deps/saasound-build/libSAASound.so.3 /usr/local/lib/
    ldconfig
'
```

That replaces `/usr/local/bin/simcoupe` and the ROM resources without
rebuilding the whole Docker image. To make a SHA change permanent, bump
`SIMCOUPE_SHA` in `tools/Dockerfile.dev` and let CI rebuild the image.

## Native macOS (no Docker)

Native macOS works end-to-end with a few quirks. A stale stock
`/Applications/SimCoupe.app` predating v1.2.16 lacks `-exitonhalt`, so
the round-trip oracle's exit detection won't fire against it — the test
would hit its 30s timeout. Build a v1.2.16+ binary from source.

```bash
# 1. Brew dep (one-time; sdl2 fmt libpng cmake assumed already present).
brew install libsamplerate

# 2. Clone simcoupé at the pinned SHA.
cd ~/git
git clone https://github.com/simonowen/simcoupe.git   # if not already there
cd simcoupe
PINNED_SHA=0e8a69f3096fe00a00a29bd2303db6a7358021ad
git fetch --depth=1 origin "$PINNED_SHA"
git checkout "$PINNED_SHA"

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

With `-exitonhalt 1`, SimCoupé's `on_halt` override fires when the Z80
executes HALT with `IFF1=0`, sets a quit flag, and the main `Run()`
loop exits on the next iteration. This is the conventional Z80 "we are
done" idiom — a HALT with interrupts disabled can never be woken by a
maskable interrupt, so it's unambiguous.

The `di` immediately before `halt` is load-bearing. SAMDOS's RST 8
dispatcher (ROM `PTDOS`) does `EI` inside the hook window, so the
`di` at `start:` in the stub has been undone by the time we reach
this point after HSAVE. Without the trailing `di`, `IFF1=1` and
`on_halt`'s quit check correctly does not trigger.

With the `di` in place, `on_halt` fires reliably on every toolchain
tested (Apple clang on arm64, gcc-13 on Linux amd64+arm64, GHA
`ubuntu-latest`). `-exitonhalt` is upstream as of v1.2.16 (Simon's
`a65a16e`, his re-implementation of our closed PR
`simonowen/simcoupe#109`).

## Related files

- `tools/Dockerfile.dev` — image recipe (single source of truth for CI
  and local dev); pins the upstream SimCoupé SHA.
- `tools/run-simcoupe.sh` — invocation wrapper used by `make`.
- `.github/workflows/ci.yml` — builds + publishes the image; runs the
  round-trip in it.
- `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m0-status.md` — current state of the M0 milestone.
- `https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/fred-disk-inspection.md` — example of using ImageMagick
  `import` to verify a real disk boots.
