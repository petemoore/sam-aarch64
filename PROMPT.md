# Upstream the `-exitonhalt` SimCoupé patch

You are a fresh Claude session dispatched to a specific, focused task. The
other session that spawned you is working on a separate piece of work
(GitHub Actions Docker-image workflow) — **do not work on that**, and do
not modify anything in this repo (`sam-aarch64`) unless absolutely
necessary. Your work happens in a SimCoupé clone you create yourself.

## The task

The file `tools/simcoupe-exitonhalt.patch` in this repo is a vendored patch
to upstream SimCoupé that adds a `-exitonhalt` command-line flag for
batch / CI usage. The patch is **already stable and validated in CI**
(see `Validation` below). Pete (`@petemoore`) wants to upstream it.

**Strategy**: open a draft PR against Pete's fork
(`petemoore/simcoupe`) first, polish it there in private, then once it's
ready, retarget the PR to `simonowen/simcoupe`. The fork doesn't exist
yet at the time this prompt was written — you'll need to create it.

## What the patch does

It adds two ways for a Z80 program running under SimCoupé to signal
"I'm done, please quit cleanly" — necessary for headless / CI use where
SimCoupé must terminate without a human pressing a key:

1. **`DI; HALT` detection** — when the Z80 executes `HALT` with
   interrupts disabled (an unambiguous "done" signal that can never be
   woken by an interrupt), SimCoupé quits with exit code 0. Implemented
   in `Base/CPU.h::sam_cpu::on_halt` (CRTP override).
2. **Magic OUT-port detection** — when the Z80 executes
   `OUT (0xDEAD), 0xC0` (port `0xDEAD` is not decoded by any real SAM
   hardware, so this is collision-free), SimCoupé quits with exit
   code 0. Implemented in `Base/CPU.h::sam_cpu::on_output`.

The patch's own commit message comment explains *why both*: the CRTP
`on_halt` dispatch has been observed to fire unreliably depending on the
compiler — the magic-port mechanism via `on_output` is the primary path,
HALT is defence-in-depth.

The option is purely a per-invocation flag (off by default, set via
`simcoupe -exitonhalt 1 disk.mgt`). It is deliberately NOT persisted to
`SimCoupe.cfg` — see the patch comments in `Base/Options.cpp::Save`.

## Files touched by the patch (4 files, ~33 lines)

- `Base/CPU.cpp` — `g_fQuit` flag + loop guard in `Run()`.
- `Base/CPU.h` — `on_halt` and `on_output` overrides on `sam_cpu`.
- `Base/Options.cpp` — option parser + comment explaining the
  "don't persist" decision.
- `Base/Options.h` — `bool exitonhalt = false;` in `struct Config`.

Pinned upstream commit the patch applies cleanly against:
**`0f74cff52b96841fe0efa01ffd1a6875b253e72a`** ("Updated version to
v1.2.15"). This is the SHA pinned in `tools/Dockerfile.dev` and
`.github/workflows/ci.yml` in this repo.

## Validation

The patch was extensively validated by Pete's M0 round-trip oracle.
Specifically:

- Commit `1578bad` on `main` of this repo (`sam-aarch64`) runs the
  patch in CI: builds patched simcoupé from source via
  `cmake --install`, then a Z80 stub uses the magic-port mechanism
  (`OUT (0xDEAD), 0xC0`) to exit cleanly after writing a 4-byte file
  via SAMDOS HSAVE. CI is green on amd64 (GHA `ubuntu-latest`).
- Locally Pete has been running it in an arm64 Docker dev container
  for weeks.
- Both exit mechanisms (`DI; HALT` and the magic port) have been
  exercised — the magic port is the primary path that fires reliably,
  HALT is the fallback that the patch's own comment notes can have
  CRTP dispatch issues on amd64 (which my parent session uncovered
  while debugging an unrelated CI issue).

## Suggested workflow

Run these in this order; you can deviate based on what you find.

### 1. Fork and clone

```bash
# Create the fork if it doesn't exist
gh repo fork simonowen/simcoupe --clone=false --remote=false

# Clone Pete's fork to a dedicated workspace (NOT inside sam-aarch64)
git clone git@github.com:petemoore/simcoupe.git ~/git/simcoupe-upstream-pr
cd ~/git/simcoupe-upstream-pr

# Add upstream as a remote for syncing later
git remote add upstream https://github.com/simonowen/simcoupe.git
git fetch upstream

# Branch off the pinned upstream SHA (NOT the current main of either
# repo — applying against later upstream changes may require rebasing
# but the patch is recorded against this specific SHA).
PINNED_SHA=0f74cff52b96841fe0efa01ffd1a6875b253e72a
git checkout -b exit-on-halt "$PINNED_SHA"
```

### 2. Apply the patch as commits

Don't just `git apply` the patch file into a single commit — break it
into reviewable atomic commits if possible. A reasonable split:

1. `feat: add Config::exitonhalt option (off by default, non-persisted)`
   — touches `Base/Options.h`, `Base/Options.cpp`.
2. `feat: quit on Z80 magic-port write (OUT &DEAD, &C0) when exitonhalt=1`
   — touches `Base/CPU.h::on_output`, `Base/CPU.cpp::Run()` (the
   `g_fQuit` global + loop guard).
3. `feat: quit on Z80 HALT with interrupts disabled when exitonhalt=1`
   — touches `Base/CPU.h::on_halt`. Mention the observed CRTP dispatch
   unreliability in the commit message — that context belongs upstream
   so the maintainer knows why both mechanisms exist.

The patch file at
`/Users/pmoore/git/sam-aarch64/tools/simcoupe-exitonhalt.patch` is the
authoritative source for what each change should be. Read its commit
message — it has Pete's explanation that should largely carry forward.

### 3. Verify the patch still works on a real disk

The minimum bar: clone the v1.2.15 SimCoupé, apply your reworked
commits, build, and run a tiny Z80 stub that writes `OUT (&DEAD), &C0`.
Confirm SimCoupé exits cleanly with code 0.

The fastest way is to use Pete's existing dev container as the build
environment, OR build fresh in `~/git/simcoupe-upstream-pr/`. You'll
need `build-essential cmake libsdl2-dev libfmt-dev zlib1g-dev libpng-dev
libsamplerate0-dev xvfb libgl1-mesa-dri` and a few minutes for cmake
FetchContent to pull dependencies.

If you want a Z80 test stub: there's a 9-byte one inline in
`/Users/pmoore/git/sam-aarch64/src/stub-border-test.asm`-equivalent
shape (`org &8000; di; ld bc, &dead; ld a, &c0; out (c), a; halt`).
Assembling with pyz80 and packaging into a disk is a project unto
itself — easier path: use `sam-aarch64`'s `make ci` flow as the
validation oracle (it exercises the patched simcoupé exactly via
the magic port).

### 4. Push and open the draft PR (against the fork)

```bash
git push -u origin exit-on-halt
gh pr create --draft \
    --repo petemoore/simcoupe \
    --base main \
    --head exit-on-halt \
    --title "Add -exitonhalt flag for batch / CI usage" \
    --body-file <path to a PR description you've drafted>
```

**Critical:** make sure the PR is targeting `petemoore/simcoupe:main`,
NOT `simonowen/simcoupe:main`. Simon should not see this PR until
Pete is happy with it.

### 5. Iterate

Improve commit messages, code clarity, splits, etc. Use the PR
description to summarise the design choices. The end-state is a PR
that's ready to retarget at `simonowen/simcoupe`.

### 6. Retarget when ready

```bash
gh pr edit <pr-number> --repo petemoore/simcoupe \
    --base simonowen/simcoupe:main
```

Or via the GitHub UI's "Edit" → change base repo.

## What NOT to do

- **Don't touch `sam-aarch64` source files.** Your work is upstream.
  The only sam-aarch64 file you should read is the patch itself
  (`tools/simcoupe-exitonhalt.patch`).
- **Don't merge anything into petemoore/simcoupe's main.** That fork
  should track simonowen/simcoupe unchanged.
- **Don't open the PR against simonowen/simcoupe yet** — Pete wants
  to perfect it on the fork first.
- **Don't modify the vendored patch file in `sam-aarch64`** — that's
  the existing CI input and shouldn't drift while you're rewriting
  it as upstream commits.

## References for full context

All in `/Users/pmoore/git/sam-aarch64/`:

- `tools/simcoupe-exitonhalt.patch` — the canonical patch.
- `tools/Dockerfile.dev` and `.github/workflows/ci.yml` — show how
  the patch is applied + built in production (post-`1578bad`).
- `docs/notes/headless-simcoupe.md` — the headless dev workflow that
  uses this patch.
- `docs/notes/m0-status.md` (post `a3d6078`) — overall context of how
  the patch fits into the bigger project.

Upstream:
- https://github.com/simonowen/simcoupe — Simon Owen's repo. Pin
  SHA `0f74cff52b96841fe0efa01ffd1a6875b253e72a`.

## When to stop

Stop and report back to Pete when:

- The draft PR is open against petemoore/simcoupe:main with the
  reworked commits, and you've validated the patched simcoupé exits
  cleanly on a real test.
- OR you hit a blocker that needs Pete's input (design choice,
  upstream maintainer's preferences you can't infer, etc.).

Don't merge anything; don't retarget to simonowen until Pete says so.
