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

## Decision: adopt the Go harness as an agent-side development tool; SimCoupé under Docker remains the only CI gate

The Go harness (`tools/z80-test-harness-spike-go/`) exists to make agents more agile
during Z80 dev iteration. It is **not** a CI gate. The existing
`make ci-m{3,4,5,6}{,-prod}` SimCoupé matrix remains the sole authoritative gate
because SimCoupé is closest to real SAM hardware.

The decisive argument is **workflow position**. At ~1 ms per fixture the harness slots
into the development inner loop (edit → test → iterate in seconds, locally, on the host,
with no container). SimCoupé under Docker at ~0.5 s per fixture is fine as a final
pre-push confirmation but still forces batching rather than the immediate feedback
agents need while exploring a Z80 change.

The SimCoupé fork (Spike B) was rejected as a productisation target. The fast-iteration
loop it enables is already a tier slower than the Go harness, and SimCoupé itself
(no fork required) already provides the realism layer the project needs. Adopting the
fork would add a C++ build dependency without solving a problem the Go harness doesn't
already solve better.

### Discounting Pete's Go bias

This evaluation explicitly discounts the user's stated Go preference. The decision
stands on the workflow-position argument alone. The Go-expertise advantage is a
secondary benefit (lower ongoing maintenance cost), not the load-bearing reason.

### Steelman of the rejected option (Spike B)

The strongest argument for the SimCoupé fork is **correctness fidelity**. Spike A's
hand-rolled RST 8 dispatch, UIFA page-resolution logic, and SAMDOS hook stubs are
approximations. Any gap between the stub model and real SAMDOS behaviour is invisible
until a fixture fails in SimCoupé but passes in the Go harness. Spike B, being
SimCoupé, cannot produce that class of false positive.

This argument is acknowledged and absorbed by the workflow below rather than rejected:
SimCoupé under Docker remains the pre-push and CI authority, so any divergence Spike A
might miss is caught there. The Go harness never needs to be authoritative for the
project to retain SimCoupé's correctness fidelity.

---

## How the harness fits into the workflow

1. Agent makes a Z80 code change.
2. Quick check via the Go harness (~1 ms/fixture, runs on the host, no Docker). If it
   surfaces a bug, iterate.
3. Before pushing, run SimCoupé under Docker locally (`make ci-m{3..6}{,-prod}` or the
   relevant per-milestone target) for a real-hardware confirmation.
4. Push. CI runs the SimCoupé matrix as the gate.

**The Go harness is never a gate.** If it crashes for an unknown reason, misleads, or
just isn't helpful in a given iteration, the agent skips it and runs SimCoupé under
Docker directly. "The tool wasn't helpful this time" is a fine outcome — it does not
need investigation or escalation.

**When the harness disagrees with SimCoupé, SimCoupé wins.** The right next move is to
fix the harness (stub gap, hook semantics, paging discrepancy), but the SAM-side code
change ships based on SimCoupé's verdict.

**Agents own the harness code.** It exists to serve them. Improvements — richer state
dumps, single-step execution, memory watchpoints, T-state counting, opcode bytes in the
PC trace, branch coverage, or whatever else turns out to be useful when something
crashes or behaves unexpectedly — are part of normal Z80 work. PR them without
ceremony; no design review or permission needed for harness changes that are obviously
making the tool more useful.

---

## Known stub gaps (for future agent reference)

Spike A's SCOPE.md identifies three live approximations. These are not blockers, but
they're where divergences from SimCoupé are most likely to surface. When an agent
notices "this fixture passes the Go harness but fails in SimCoupé", one of these is the
first place to look.

1. **UIFA/&4B50 page resolution.** If LMPR changes before HGTHD is called, the wrong
   physical page may be written. Currently untested for non-default LMPR at hook entry.
2. **Test variant.** Boot-time self-tests do ~200 K+ Z80 steps; the HGTHD stub for
   `enctab.enc` may need to populate `&4B50` to track real SAMDOS behaviour. Spike A
   only ran the prod binary.
3. **Multi-page OUT.** HSAVE multi-page capture logic exists but is untested. Will
   surface when an m6 fixture exceeds the single-page OUT path.

Spike-B-derived ideas worth keeping on the shortlist if they would help in a given
debug session: opcode bytes per PC in the trace (call `hw.Get` for 4 bytes after each
`recordPC`); a `LogHook` mode that records every RST 8 dispatch outside the last-200
ring (the bracket-with-tag pattern from the SimCoupé fork was good; SAMDOS calls in
current fixtures fire ~20 000 instructions before HALT, so they're outside any small
ring).
