# Test-variant CI regression on origin/main (post-PR #43)

**2026-05-28**: A fresh dev-container CI run on `origin/main`
(commit `9332407`) shows every M3 test-variant fixture timing out
at the SimCoupé layer with `rc=124`.  This is the boot-self-test
failure pattern the handoff predicted as a possibility after the
PR #43 revert.

## What was run

```
docker run --rm -v "$(pwd):/work" -w /work \
  -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
  -e AARCH64_AS=aarch64-linux-gnu-as -e AARCH64_LD=aarch64-linux-gnu-ld \
  -e AARCH64_OBJCOPY=aarch64-linux-gnu-objcopy \
  ghcr.io/petemoore/sam-aarch64-dev:latest \
  bash -c '
    Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 &
    sleep 1; export DISPLAY=:99
    rm -rf build/
    make ci-m3 ci-m4 ci-m5 ci-m6 ci-m3-prod ci-m4-prod ci-m5-prod ci-m6-prod
  '
```

(Mac host, dev container `ghcr.io/petemoore/sam-aarch64-dev:latest`.)

## Result

```
FAIL: dir_data        (rc=124)
FAIL: dir_hword       (rc=124)
FAIL: dir_string      (rc=124)
FAIL: empty           (rc=124)
FAIL: expr_extras     (rc=124)
FAIL: expr_simple     (rc=124)
FAIL: inst_movz_movn  (rc=124)
FAIL: inst_nop_ret    (rc=124)
FAIL: inst_reg_imm    (rc=124)
0/9 M3 fixtures matched
make: *** [Makefile:146: test-m3] Error 9
```

`make` halted at the first M3 failure, so `m4`/`m5`/`m6` (test
variants) and the four `*-prod` targets weren't reached in this
run.  The prod variants were green at PR-#43 merge time and (per
the handoff's expectation) should remain green; that hasn't been
verified locally in this session.

Full log: `/tmp/ci-verify-full.log` on the Mac (transient).

## Root cause — confirmed

**PR #43's "revert" of PR #42 was incomplete.**  The PR-#43 branch
re-applied the SP=&FFFE change on commit 2 of its own history and
then ran `git revert 1b3951f` (PR #42's commit) as commit 3.  The
re-application and the revert *did not* cancel each other out: the
final tree still has `ld sp, &FFFE` and the uncommented
`call run_reader_paged_self_tests`, identical to what PR #42
intended.  See the squash commit message at `0ade3fb` for the three
preserved commit subjects (`docs: …`, `fix: move boot SP to &FFFE…`,
`Revert "m6: move boot SP …"`).

The handoff's "PR #41 introduced a latent regression" theory is
unlikely: PR #41's table-bump work landed cleanly in `919eb9e` and
nothing in the PR-#43 diff against PR #41's tree touches table
layout.  The only material code change between
`919eb9e..0ade3fb` is the SP value (`&C100` → `&FFFE`) and the
re-enabled self-test call.  Removing those two lines restores the
post-PR-#41 state, which the handoff calls "known green".

The PR-#42 root-cause analysis (stack-vs-code collision at &C100
when the BUILD_TESTS variant's binary spills past &C000) may still
be accurate as far as the *symptom* it explains — but the *fix*
(SP=&FFFE) does not pass test-variant CI on the post-#41 tree.  Why
SP=&FFFE breaks the test variants is a separate question that wants
a dedicated investigation on a clean main; see
`docs/notes/2026-05-28-reader-paged-self-test-investigation.md` for
the analysis as it stands.

## Cleanup applied in this PR

- Restore `ld sp, &C100`.
- Re-comment the `run_reader_paged_self_tests` call with a NOTE
  block explaining the deferred investigation.
- Mark the reader-paged investigation doc as superseded — its
  "verified PASS" CI table was on a tree pre-#41 and didn't predict
  the post-#41 interaction.

## Next steps after this PR merges

1. Confirm CI on the cleaned main goes green (m{3..6} test variants
   should pass, mirroring the pre-#42 state).
2. Dedicated investigation: re-apply SP=&FFFE on a fresh branch
   from cleaned main, run dev-container CI; if it fails, trace why
   (likely candidates: a hardcoded address PR #41 moved, or a
   subtle stack/SP interaction with the table relocations).  Update
   the memory-map comments in `src/assembler.asm` if the
   `&E100..&FFFF` "free" region claim turns out to be inaccurate.
