# i253 — no silently-skipping tests (execution plan)

Ephemeral plan (delete in the completing PR). Branch: `i253-no-silent-skips`.

**Directive (Pete, 2026-06-25):** no test may silently `t.Skip` on a missing
precondition — if a precondition is needed, its absence must cause a test
**FAILURE**. The default `go test` run must have **zero** silent skips; the only
sanctioned skip is an **explicit, intentional env-var gate** (`SKIP_PRIVATE_TESTS`).
Surfaced by the i251 finding that `build/netboot_sd_csd.bin` was never built in CI,
so the whole `csd_to_bd_records` suite (incl. the i250/i251 gates) silently skipped
in CI undetected.

## Mechanism (decided)

- **Buildable fixture missing → `t.Fatalf`** (not `t.Skipf`). The fixture is always
  buildable; absence = a CI-wiring bug (exactly the sd_csd case). CI must build every
  fixture its tests load.
- **Proprietary capture missing → `t.Fatalf`, UNLESS `SKIP_PRIVATE_TESTS=true`**
  (then explicit `t.Skipf`). CI sets the env var (the captures are non-redistributable
  and absent in CI). Helper: `requirePrivateCapture(t, name)` in
  `tools/netboot-oracle/z80/samboot_real_boot_test.go`.

## DONE on this branch (verified green)

1. **netboot-oracle/z80 module** (subagent): 97 `t.Skipf("…not built…")` → `t.Fatalf`
   across 76 files; `requirePrivateCapture` helper added; the 4 proprietary-capture
   tests gated on `SKIP_PRIVATE_TESTS` — `samboot_real_boot_test.go` (rom.bin+eeprom.bin),
   `fork_trace_analysis_test.go`, `fork_consequence_test.go`, `csd_decode_colin_test.go`
   (B-DOS 1.5t). Verified: `make ci-netboot-z80` green (captures present → private tests
   RUN+pass); `SKIP_PRIVATE_TESTS=true make ci-netboot-z80` green (those 9 explicitly SKIP).
2. **CI** (`.github/workflows/ci.yml`): the `netboot-z80` job runs
   `SKIP_PRIVATE_TESTS=true make ci-netboot-z80`.
3. **Two cross-module live silent-skip bugs fixed** (same class as sd_csd — were
   skipping in CI every run): `TestEmitLinesOnRealReleaseTbn`
   (`tools/sam-aarch64/render/emit_lines_test.go`) and `TestBinaryGenMatchesValidFixture`
   (`tools/registry/regen_survives_test.go`) → `t.Fatalf`, and their CI jobs now build
   the artifact: `test-format`/`test-encoder` gained a `release-unstripped-tbn` prereq;
   `ci-registry` gained a `registry-gen` prereq. Both verified to run+pass.

## REMAINING (do these, then open the PR completing i247-era… no — completing i253)

### A. Dev-harness packages (not CI-run, but Pete wants no silent skips anywhere)
These packages have NO CI test runner (they're the local inner-loop harness; SimCoupé
is the gate), so their `t.Skipf("…not built…")` can't hide a CI gap — but they're
developer footguns and in scope. Convert their fixture-not-built skips → `t.Fatalf`:
- `tools/z80-test-harness-go/` (many: `boot_self_test`, `coverage`, `zx0_*`,
  `compact_tbn`, `release_paged`, …)
- `tools/zx0-greedy/`
- `tools/editor-prototype/`
Method: same as the netboot sweep (delegate per-package to a subagent; convert only
the "build artifact missing" skips). Verify each package's tests pass with its
fixtures built (`make <the named targets>` then `go test ./...` in that module).
**Watch:** if any of these load a *proprietary* capture, gate it with the same
`SKIP_PRIVATE_TESTS` env pattern (replicate a small helper per module — they're
separate Go modules, can't import the netboot one).

### B. Scope / data-driven skips (policy — left by the netboot sweep, listed here)
Not "missing-fixture" — a fixture too big for a harness buffer cap, or data-driven:
- `compact_ir_test.go` :188,371,374,377,380,383,563 — cap-exceeded ("out of harness scope").
- `pass1_ir_test.go` :136 (PASS1_IR_BUF cap), :381 ("Translate error out of fixture scope").
- `tls_client_test.go` :248 ("no encrypted flight record in the capture").
These silently skip individual fixtures, hiding partial coverage. Per the directive,
make them explicit: prefer an **explicit named exclude-list** (a `var knownOversize =
map[string]bool{…}` the test consults and *logs*) so an unexpected oversize fixture
FAILS, while the known-too-big ones are a visible, reviewed list — not a silent
per-fixture skip. Decide with the harness owner; if a cap can simply be raised to fit
all current fixtures, do that instead.

### C. Anti-regression guard
Add a CI guard so new silent skips can't creep back. A `make check-no-silent-skips`
(or a step in an existing lint job) that greps `tools/**/*_test.go` for `t\.Skip`
and fails unless the site is whitelisted (the `SKIP_PRIVATE_TESTS` gates + any
approved scope-excludes). Wire it into a cheap CI job (e.g. alongside `staticcheck`
or `check-doc-links`). Keep the allowlist tiny and commented.

### D. Policy + memory
- Add a **repo `CLAUDE.md`** testing-policy line: "No silently-skipping tests — a
  missing precondition must FAIL (`t.Fatalf`), not `t.Skip`. The only sanctioned skip
  is an explicit env-var gate (`SKIP_PRIVATE_TESTS`) for non-redistributable
  proprietary captures, which CI sets." Reconcile the §3 item-5 "principled skip"
  wording (GNU-toolchain): CI installs binutils so those run; if kept, make them an
  explicit env gate too, not a bare `t.Skip`.
- Add a **memory** entry (`feedback_no_silent_skips`) + index line.

### E. Finish
`make registry` (i253 → DONE w/ the completing PR), delete this plan, full local
verification of every touched module + the §3 pre-merge review, then ONE PR
completing i253. **Do not merge partially** — Pete asked for the full sweep on one
branch.

## Forward goal — run ALL tests in CI (tracked as i254, depends on i253)

Pete (2026-06-25): eventually CI should run **every** test, with nothing silently
excluded. The end state (captured in **i254**):
- `SKIP_PRIVATE_TESTS=true` is the **default** in CI (forks get no secrets → they
  skip the proprietary tests explicitly). i253 already sets this for the netboot-z80
  job; i254 generalises it.
- **Our** repo's CI materialises the proprietary binaries from **encrypted GitHub
  repository secrets** (rom.bin / eeprom.bin / B-DOS 1.5t) into the capture paths and
  overrides `SKIP_PRIVATE_TESTS` to false, so our CI runs those tests too. GitHub
  withholds secrets from fork-PR workflows, so forks never get the binaries.
- **Add the currently-CI-excluded packages to CI** (`z80-test-harness-go`,
  `zx0-greedy`, `editor-prototype` — they have no CI runner today), guarded by
  `SKIP_PRIVATE_TESTS` where needed. **Tension (Pete's call):** `z80-test-harness-go`
  was deliberately *not* a CI gate (it can crash/mislead; SimCoupé is the gate) — so
  adding it may want a non-blocking/separate job, or an explicit reversal.
- **Pete-gated:** only Pete can create the secrets, and he weighs storing Colin's
  non-redistributable binaries as encrypted secrets in a public repo.

So in i253, when converting the dev-harness packages (section A) and adding any
currently-CI-excluded test to CI, **guard private-resource tests with
`SKIP_PRIVATE_TESTS`** (don't drop them) — that keeps i254 a pure wiring/secrets job.

## Licensing reference (for the private-capture gating)
- B-DOS 1.5a = **public domain**, already committed (`reference/bdos/al-bdos15a.bin`).
- Stock ROM v3.0 (`rom_official_v30.bin`) = Andy Wright's, permission-published via
  `simonowen/samrom` — fetchable, but NOT what the capture tests load.
- **Colin's forked ROM (`rom.bin`) + EEPROM (`eeprom.bin`) + B-DOS 1.5t = proprietary,
  non-redistributable** — cannot be in CI even as secrets (public-repo publication
  risk). These are exactly the `SKIP_PRIVATE_TESTS` files.
- GitHub secrets work for own-branch CI but not fork PRs; irrelevant here because the
  files are non-redistributable regardless.
