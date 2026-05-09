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
- `0` — Z80 hit either `DI; HALT` or the magic `OUT (&DEAD), &C0` sequence; clean.
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

## Building the patched SimCoupé inside the container

The dev container does NOT pre-build SimCoupé — keep the patch
iteration loop fast. From inside the container:

```bash
# Pinned upstream commit (cf. .github/workflows/ci.yml).
PINNED_SHA=0f74cff52b96841fe0efa01ffd1a6875b253e72a

cd /tmp
rm -rf simcoupe
git clone --depth 1 https://github.com/simonowen/simcoupe.git
cd simcoupe
git fetch --depth=1 origin "$PINNED_SHA"
git checkout "$PINNED_SHA"
git apply --check /work/tools/simcoupe-exitonhalt.patch  # sanity check
git apply /work/tools/simcoupe-exitonhalt.patch
cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build -j"$(nproc)"
cp build/simcoupe /usr/local/bin/simcoupe
```

After this, `make ci` from `/work` should run end-to-end.

## Recreating the dev container from scratch

When the container is gone (host reboot, `docker rm`, etc.):

```bash
cd /Users/pmoore/git/sam-aarch64
docker build -t sam-aarch64-dev:latest -f tools/Dockerfile.dev tools/
docker run -d --name sam-aarch64-ci --platform linux/amd64 \
    -v "$PWD:/work" -w /work \
    sam-aarch64-dev:latest sleep infinity
docker exec sam-aarch64-ci bash -c '
    PINNED_SHA=0f74cff52b96841fe0efa01ffd1a6875b253e72a
    cd /tmp && git clone --depth 1 https://github.com/simonowen/simcoupe.git
    cd simcoupe && git fetch --depth=1 origin "$PINNED_SHA"
    git checkout "$PINNED_SHA"
    git apply /work/tools/simcoupe-exitonhalt.patch
    cmake -B build -DCMAKE_BUILD_TYPE=Release
    cmake --build build -j$(nproc)
    cp build/simcoupe /usr/local/bin/simcoupe
'
```

## Smoke test

```bash
docker exec sam-aarch64-ci bash -lc '
    Xvfb :151 -screen 0 1280x1024x24 &
    export DISPLAY=:151 SDL_VIDEODRIVER=x11 SDL_AUDIODRIVER=dummy
    cd /work && make ci
'
```

## Related files

- `tools/Dockerfile.dev` — container recipe
- `tools/simcoupe-exitonhalt.patch` — vendored simcoupe patch
- `tools/run-simcoupe.sh` — invocation wrapper used by `make`
- `.github/workflows/ci.yml` — CI recipe (apt-installs the same packages
  inline; keep in sync)
- `docs/notes/m0-status.md` — current state of the M0 milestone
- `docs/notes/simcoupe-batch.md` — historical/M0-Task-1 spike (this
  document supersedes its headless-environment claims)
- `docs/notes/fred-disk-inspection.md` — example of using ImageMagick
  `import` to verify a real disk boots
