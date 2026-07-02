# Using the koron-go/z80 harness

Practical how-to for the SAM-side assembler harness. For *what it is* and
*what's faithful vs stubbed*, see [README.md](README.md) and
[SCOPE.md](SCOPE.md). This harness is a developer tool, **not a CI gate** —
SimCoupé under Docker is the sole gate. When the two disagree, SimCoupé wins.

## Run the whole suite in one command

From the repo root:

```
make harness-sweep
```

It builds **every** artefact the Go test suite reads (both assembler variants,
`enctab.enc`, the off-axis cluster + `test_mem`, the page-14 paged-call payload,
`sysreg_data.bin`, the disasm + zx0 payloads, and the `sam-aarch64` host tool),
then runs `go test -count=1 ./...`. The per-variant prerequisite lists in the
README cover the *standalone binary*; the full `go test ./...` suite (boot
self-tests + the fold / align / org guards + the disasm oracle + the
compact-`.tbn` round-trip) needs the complete set, which is exactly what this
target assembles. The corpus-dependent zx0 profiling tests skip when their
inputs are absent — that's expected, not a failure.

## Stale artifacts can't false-pass (TestMain build guard)

`go test ./...` in this package rebuilds **every** SAM-side artifact it reads
*before any test runs*, via a `TestMain` (`build_assert_test.go`) that runs
`make <artifact-targets>` and **aborts the suite if the build exits non-zero**.
The Makefile's prerequisite graph (`build/*.bin : src/*.asm`) rebuilds anything
stale and no-ops when everything is fresh (~0.5s warm), so every read in the
suite sees an artifact current with its sources. A failed `pyz80` build (which
leaves the *previous* `.bin` on disk) now makes the suite **FAIL**, not silently
pass against the stale binary (registry item i116; the four-session false-pass
class — sha256-fastest-z80, i88-tls, i48c-expr-parser, the timestamp-keyed
variant).

This makes both historical gotchas structural rather than advisory:

- **Off-axis payloads rebuild automatically.** The off-axis cluster
  (`test_cluster.bin`) and `test_mem.bin` bake *main-binary* routine addresses
  in via `--importfile=assembler.sym`; the TestMain sweep builds the
  Makefile's `harness-artifacts` aggregate (the single source of truth for
  the artifact set this suite reads), so they rebuild together whenever the
  main binary changes — no more stale-address self-test crashes.
- **A stale `build/*.bin` can't survive into a read.** Go's test cache keys on
  Go sources, not the `build/*.bin` the harness reads at runtime, so a bare
  re-run *could* report a cached `ok` against an old binary. The TestMain build
  runs every invocation (it is not part of the cached test body), so the
  artifacts are freshened before the (possibly cached) tests execute. Use
  `go test -count=1` (as `harness-sweep` does) if you also want to defeat Go's
  result cache for the test bodies themselves.

To bypass the pre-build (no `make`/`pyz80` on PATH, or to test a hand-placed
artifact), set `HARNESS_SKIP_BUILD=1` — an explicit, warned opt-out, never the
default.

## Writing a new test

There are two patterns; pick by what you're testing.

### 1. Boot self-test (a `src/test_*.asm` suite)

Best for asserting an assembler internal that runs *during boot*. The suite is
plain Z80, wired in either inline (`src/assembler.asm` under `BUILD_TESTS`) or
off-axis (`src/test_offaxis_cluster.asm`), and asserts with `jp fail` on
mismatch. `tools/z80-test-harness-go` boots the `BUILD_TESTS` assembler and
`TestBootSelfTestsPass` asserts every suite passes. Because a failure is
terminal (`jp fail` halts the boot), a boot self-test can only assert the
**success** path — e.g. `test_emit_paged` §5 emits the 32768th OUT byte and
checks it succeeds.

### 2. Go-side guard test (`RunWithFiles` + a hand-built `.tbn`)

Best for asserting a **failure** path (a specific fail tag) or an edge input the
Go front-end would never emit. Build the `.tbn` in Go with
`format.RecordWriter`, boot the prod assembler, and assert on the result:

```go
tbn := buildSomeTbn(t, ...)                 // format.RecordWriter → bytes
res := RunWithFiles(asm, enc, tbn, nil, 10*time.Second)
// success case:  require res.Passed
// failure case:  require !res.Passed && strings.Contains(res.PrinterCapture, "FAILa1")
```

`res` carries `Passed`, `PrinterCapture` (the printer-channel banner, e.g.
`FAILa1` for fail tag `&a1`), `OutBytes`, the last-200 PC trace, and
`ExitReason`. Worked examples: `fold_guard_test.go` (fold range guards),
`align_guard_test.go` (`.align` exponent), `org_guard_test.go` (backward
`.org`). A good guard test pairs a passing control with the failing case, and
a *negative control* (run the same test against the pre-fix binary to confirm
it actually bites) is the gold standard — see those files' commit history.

## Deeper inspection

On any non-PASS exit the standalone runner prints the register snapshot, step
count, and last 30 PCs. For windowed PC/register traces or a trigger-PC
backtrace use `RunConfig` from Go (`TraceLo`/`TraceHi`, `TrigPC`) — see
`test_variant_test.go` and `SCOPE.md`. The harness is agent-owned: add
inspection features (watchpoints, richer dumps) as normal Z80 work.

## Read-coverage / dead-code report (item i111)

`TestCoverageReport` (in `coverage_test.go`) maps which bytes of the
test-variant `build/assembler.bin` are NEVER read during the run, to find
dead-code candidates for the &C000 footprint pass (item i205). Run it:

```
cd tools/z80-test-harness-go
go test -run TestCoverageReport -v .
```

It boots the assembler through the full self-test path **and** assembles every
aarch64 fixture in `tests/{core,operands,format,symbols}/sources/`, recording
every byte the CPU reads (opcode fetch + data load) via a coverage hook in
`Hardware.Get` (opt-in: `Config.EnableCoverage`, zero overhead when off — see
`coverage.go`). It then maps touched bytes back through `build/assembler.sym`
and prints + writes `build/coverage-report.txt`: a size-ranked list of
untouched symbol ranges (each a dead-code candidate **or** a coverage gap) plus
a total reclaimable-bytes estimate.

**Drift-proofing:** symbol ranges that are legitimately never-read on the happy
path (error-only strings, sentinel equates) are excluded from the reclaimable
total via a small documented allowlist in `coverage_exclusions.go` — add a
prefix + reason there to suppress a future false positive.

**Fidelity caveat:** the resident section-C assembler code is not paged, so its
coverage is faithful; paged off-axis payloads (pages 4, 12–15) are not part of
`assembler.bin`'s image and are not attributed (correct for the section-C
budget question). See the `coverage.go` package doc for the full keying model.
