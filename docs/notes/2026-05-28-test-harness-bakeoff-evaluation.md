# Test-harness bake-off evaluation — Spike A (Go) vs Spike B (SimCoupé fork)

**Date:** 2026-05-28
**Evaluator:** Spike C agent

---

## Orchestrator-level context (must be read before the numbers)

Three corrections from the orchestrating session apply here and are *not* captured in
the spike SCOPE files:

1. **The "30s SimCoupé baseline" in the briefs is a macOS-native-build artefact.** The
   macOS `.app` binary ignores `-exitonhalt`, so a 30s timeout safety-net fired every
   run. Spike B v2 ran inside the dev container and measured **0.53 s per fixture**. The
   "30 000×" speedup headline for Spike A does not hold; the correct ratio is
   **~1 ms (Spike A) vs ~530 ms (Spike B), roughly 530×**.

2. **The previous Spike B attempt was stopped and re-run.** The v1 run built SimCoupé
   natively on macOS and hit the 30s timeout. v2 moved to Docker and got 0.53 s.
   All Spike B figures in this doc are v2.

3. **Behavioural-divergence risk is asymmetric.** Spike B *is* SimCoupé (the oracle);
   any divergence is a bug in the added instrumentation code, not in the Z80 emulation.
   Spike A is an independent re-implementation; divergence is possible wherever stubs
   differ from real SAM hardware or SAMDOS.

---

## Measured outcomes

### Spike A — Go harness (koron-go/z80)

*Quoting directly from SCOPE.md:*

> `inst_nop_ret.s` end-to-end: **~1 ms** (wall-clock on M-series Mac). Includes:
> loading data into emulated pages, ~4 800 Z80 step() calls to HALT, hook dispatch.

- **Lines of code added**: 529 (harness.go) + 159 (harness_test.go) + 82 (main.go) +
  5 (go.mod) = **775 lines** in `tools/z80-test-harness-spike-go/`.
- **Deliverables complete**: working harness, Go unit test asserting `inst_nop_ret.s`
  passes and OUT bytes match GNU-as oracle (`1F 20 03 D5 C0 03 5F D6`), README,
  SCOPE.md, timing measurement.
- **PC trace**: last-200-PC ring buffer built into the harness; accessible in the
  `Result` struct from every run.

### Spike B — SimCoupé fork (instrumentation flags)

*Quoting directly from SCOPE.md:*

> | Run | Flags | Wallclock |
> |---|---|---|
> | Instrumented (all 3 flags) | `-tracepcfile -dumpstateonhaltfile -bracketwithtag` | **0.532 s** |
> | Vanilla (no flags) | none | **0.538 s** |

- **Lines of code added**: 250 lines across 4 files (`CPU.h` +49, `CPU.cpp` +195,
  `Options.h` +5, `Options.cpp` +3). Verified against `git show f060627 --numstat`.
- **Deliverables complete**: three flags working, build instructions for Docker,
  incremental rebuild time ~5 s, SCOPE.md with future-flag menu.
- **PC trace**: `trace.txt` — 200 entries, format `XXXX YY YY YY YY`
  (hex PC + 4 opcode bytes). Also emits `state.json` with full Z80 register file
  + SAM paging state on HALT.
- **Verification**: `inst_nop_ret.s` byte-matched GNU-as oracle; printer emitted `OK`.

---

## Dimension-by-dimension comparison

| Dimension | Spike A (Go) | Spike B (SimCoupé fork) |
|-----------|-------------|------------------------|
| Lines of code added | 775 Go | 250 C++ |
| Time per fixture | ~1 ms | ~530 ms |
| Speedup vs Docker SimCoupé baseline | ~530× | 1× (IS the baseline) |
| PC-trace quality | last-200 PC values (uint16 only) | last-200 PC + 4 opcode bytes; also state.json with registers + paging |
| Risk of behavioural divergence | **Real**: RST 8 stub, paging, SAMDOS hooks all hand-rolled | **Negligible**: runs real SimCoupé code + real SAMDOS |
| Next instrumentation feature | New Go code; SAM hardware model must be kept in sync | Add ~20 lines in `OnStep()` / `on_write()` per SCOPE.md table |
| Cost to integrate into CI | New `make ci-m3-fast` target + GHA job; self-contained Go | Requires building the fork binary in CI + libSAASound ldconfig |
| Cost to keep in sync with upstream | **None**: self-contained Go, no C++ dependency | Periodic rebase; upstream moves slowly (1 merge since mid-2025) |
| Maintenance surface | Grows with every new SAM-hardware detail the assembler touches | Bounded by SimCoupé's existing abstraction layer |

### Detail: PC trace quality

Spike A returns `[]uint16` PC values — no opcode bytes. Debugging a FAIL00 hang requires
a separate disassembly pass to recover the instruction at each PC. Spike B writes the
opcode bytes directly to the trace file (`XXXX YY YY YY YY`), and the `-bracketwithtag`
feature marks SAMDOS calls inline. For the specific use case that motivated the bake-off
(diagnosing FAIL00-style hangs), Spike B's trace is more immediately useful.

### Detail: divergence risk

Spike A's SCOPE.md identifies three live risk areas:

1. **UIFA/&4B50 page resolution**: if LMPR changes before HGTHD is called, the wrong
   physical page is written. Currently untested for non-default LMPR at hook entry.
2. **Test variant**: boot-time self-tests do ~200 K+ Z80 steps; HGTHD stub for
   `enctab.enc` may need to populate &4B50 after all.
3. **Multi-page OUT**: HSAVE multi-page capture logic exists but is untested.

None of these affect the current `inst_nop_ret.s` spike run. They would surface during
extension to the full m3–m6 corpus. Spike B has none of these risks: it runs the same
code path as the existing CI.

---

## Recommendation: **Go harness (Spike A)**

The ~530× speed improvement is real, substantial, and directly addresses the pain that
motivated the bake-off: bisection iterations costing ~30 s each (even 0.5 s each in
Docker). At 1 ms per fixture, 60 fixtures can run in under a minute on a laptop without
any Docker overhead, making the harness useful for local iteration, not just CI.

The decisive argument is **workflow position**. The Go harness slots into the development
inner loop (edit → test → iterate in seconds), while SimCoupé — even at 0.5 s — is a
CI-gate artifact, not an iteration tool. Spike B addresses the same problem as Spike A
but at a time-budget that still forces batching rather than immediate feedback.

The second argument is **CI isolation**. The Go harness is a self-contained binary with
no C++ build step, no SDL, no Xvfb, no libSAASound runtime dependency. Adding a
`ci-m3-fast` job is a single `go test` line. Spike B requires building a custom
SimCoupé fork in each CI runner, which adds ~2 min cold-build overhead and introduces
a C++ toolchain dependency into the CI image.

### Discounting Pete's Go bias

This evaluation explicitly discounts Pete's stated Go preference. The recommendation
stands on the speed and CI-integration arguments alone. If Pete were a SimCoupé C++
expert and a Go skeptic, the same numbers would still favour Spike A: 530× faster,
zero new C++ build dependencies in CI. The Go-expertise advantage is a secondary
benefit (lower ongoing maintenance cost), not the load-bearing argument.

### Steelman of the rejected option (Spike B)

The strongest argument for the SimCoupé fork is **correctness fidelity**. Spike A's
hand-rolled RST 8 dispatch, UIFA page-resolution logic, and SAMDOS hook stubs are
approximations. Any gap between the stub model and real SAMDOS behaviour is invisible
until a fixture fails in SimCoupé but passes in the Go harness. Spike B, being
SimCoupé, cannot produce that class of false positive. For the specific scenario of
validating paged-call primitives (plan-PR 1), where the failure mode is a subtle
interaction between HMPR writes inside the trampoline and UIFA state, the SimCoupé
oracle is strictly more trustworthy. If the project had no CI speed problem and the
only goal were richer diagnostic output on failures, Spike B would be the right answer.

---

## Open questions Pete should answer before productising

1. **Two-tier CI strategy?** Run the Go harness as a fast smoke test on every push
   (< 5 s for all m3–m6 fixtures), then gate PRs on the full SimCoupé round-trip as
   the oracle check. This captures Spike A's speed benefit while keeping Spike B as the
   correctness authority. The main cost is maintaining two test paths; the main benefit
   is a seconds-fast feedback loop locally.

2. **Test variant coverage.** The Go harness ran only the prod binary. The test variant
   runs ~200 K+ Z80 steps and exercises HGTHD for `enctab.enc`. Does the fast harness
   need to handle the test variant, or is prod-only coverage sufficient for the inner
   loop?

3. **Strict-mode for unknown hooks.** Spike A SCOPE.md item 3: if the assembler calls
   a SAMDOS hook the harness doesn't handle, it silently continues. Should there be a
   panic-on-unknown-hook "strict mode"? Without it, stubs silently hiding hook calls is
   the most likely source of false passes.

4. **PC trace format.** Spike A returns PC values only; recovering opcode bytes
   requires a post-hoc disassembly pass. Is that acceptable, or should the Go harness
   capture the 4 opcode bytes at each PC as Spike B does? (Trivial addition: call
   `hw.Get` for 4 bytes after each `recordPC`.)

5. **`-bracketwithtag` ergonomics.** Spike B SCOPE.md item 2: for the current fixture
   corpus, SAMDOS calls are ~20 000 instructions before HALT, so the bracket sentinels
   are overwritten by the ring. Is the feature useful as-is, or should it become a
   dedicated `-log-rst8-calls FILE` that captures all hook entries regardless of the
   ring size?
