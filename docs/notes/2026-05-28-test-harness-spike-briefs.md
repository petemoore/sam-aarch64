# Test-harness bake-off — three subagent briefs

**Status:** ready-to-dispatch briefs for a parallel investigation comparing two test-harness approaches. Captured 2026-05-28 at end of session. Dispatch from a fresh session — each is a self-contained Agent prompt.

## Why

Today's plan-PR 1 impl subagent (PR #50) spent substantial time on bisection-style work where each iteration cost ~30s of SimCoupé round-trip, and the only diagnostic was a printer-channel `OK`/`FAIL00` banner. The 30s × hours of iteration likely dominated runtime. Pete's hypothesis: a faster Z80 emulator harness with introspection (PC trace, register dump on HALT, etc.) would massively speed up future iteration on Z80 work.

This bake-off compares two approaches concretely, then a third subagent evaluates which to productise.

## Common shape

All three subagents work on **the same target task** to make the comparison apples-to-apples:

- Run `tests/m3/sources/inst_nop_ret.s` end-to-end through the harness.
- Produce a pass/fail verdict consistent with what SimCoupé produces.
- Expose at least ONE introspection feature: report the last 200 Z80 instructions executed before HALT (PC trace).
- Measure end-to-end timing for that one fixture, compared to SimCoupé baseline (currently ~30s including disk-image build + boot + run + extract).

Each spike is **bounded at 4 hours**. If a spike doesn't have a working `inst_nop_ret.s` runthrough at 4 hours, the subagent stops and reports what blocked it.

---

## Spike A — Go harness using koron-go/z80

```
You are running a bounded 4-hour spike to evaluate the feasibility of a
Go-based Z80 test harness for the sam-aarch64 project. **Read-only on
main; your spike code lives in a new `tools/z80-test-harness-spike-go/`
directory.** Do not commit; PR will be opened by Pete after evaluation.

## Goal

Stand up a minimal Go harness that:

1. Loads `build/assembler-prod.bin` at Z80 address `&8000`.
2. Loads a one-fixture `.tbn` (via existing `tools/text2bin`) into IN
   pages 7-12 (mocked HLOAD; you don't need to faithfully simulate
   disk I/O, just deposit the bytes where the assembler will read them).
3. Stubs SAMDOS hooks (RST 8 / DEFB hook_id ABI — see ~/git/samdos/ for
   the canonical hook table). At minimum HSAVE (so the assembler can
   "save" its OUT buffer and your harness can capture it).
4. Stubs printer-channel ports `&E8` (data) + `&E9` (strobe) — captures
   bytes written to a Go string.
5. Stubs LMPR/HMPR paging — provide a Memory interface to koron-go/z80
   that honours the SAM paging model (LMPR for sections A+B, HMPR for
   C+D; bits 5-7 are mode-3 CLUT, preserve them).
6. Runs the assembler until HALT or 10s timeout.
7. Returns `(passed bool, captured_printer string, captured_out []byte,
   last_200_pc []uint16, exit_reason string)`.

## Target fixture

`tests/m3/sources/inst_nop_ret.s` — the smallest m3 fixture. Should
assemble + emit `D5 03 20 1F` (NOP) + `D6 5F 03 C0` (RET = `ret x30`)
= 8 bytes OUT.

## Deliverables

- Working harness at `tools/z80-test-harness-spike-go/`.
- A Go unit test that runs `inst_nop_ret.s` through it and asserts
  `passed == true`, `captured_out` matches the GNU-as oracle.
- A `SCOPE.md` documenting what's mocked, what's left, and an estimate
  of effort to extend to the full m{3..6} corpora.
- A short README with build/run instructions.
- A measurement: how many milliseconds does one fixture take?

## Bounded

4 hours. If at 4 hours `inst_nop_ret.s` doesn't pass, stop and report
what blocked you. Don't try to finish a half-working thing — a
clean "got stuck at X" report is more valuable than a half-working
shipping artefact.

## Precedent

`tools/basic-emulator-spike/` is the structural cousin — a Go-hosted
koron-go/z80 emulator that boots BASIC. Much of its hardware
abstraction transfers (Memory interface, port I/O, run-until-PC).
Read it first.

## Constraints

- Use `g` not `git`.
- Don't dispatch other subagents.
- Don't modify the SAM-side Z80 source — only `tools/`.
- The Go module path convention is `github.com/petemoore/sam-aarch64/tools/<dir>`.
```

---

## Spike B — SimCoupé fork with instrumentation flags

```
You are running a bounded 4-hour spike to evaluate the feasibility of
forking SimCoupé to add diagnostic instrumentation flags. **Work in
`~/git/simcoupe/` (Pete's local checkout; `sdl-paste-clipboard` branch
exists there).** Do not push to upstream; just commit to a local
branch and report.

## Goal

Add three new SimCoupé command-line flags to the fork:

1. `-trace-pc FILE`: on every Z80 instruction executed, write
   `PC opcode_bytes` to FILE. Cap at the last 200 entries (ring
   buffer flushed on HALT exit).
2. `-dump-state-on-halt FILE`: on HALT, write the Z80 register file +
   memory map state (LMPR, HMPR, VMPR, current page assignments) to
   FILE as JSON.
3. `-bracket-with-tag TAG`: emit `<begin-tag>` and `<end-tag>` markers
   in the trace file when execution enters / exits SAMDOS hooks (RST 8
   territory) so the trace is easier to read.

## Target fixture

`tests/m3/sources/inst_nop_ret.s` — same as Spike A. Run the
SAMDOS-bootloaded MGT image through your forked SimCoupé with
`-trace-pc /tmp/trace.txt -dump-state-on-halt /tmp/state.json`,
verify the output matches what vanilla SimCoupé would produce for
the same fixture, AND that the trace + state files have plausible
content.

## Deliverables

- Working SimCoupé fork (commit on a branch in `~/git/simcoupe/`,
  NOT pushed upstream).
- Build instructions for the fork.
- A patch file you can hand to a CI integration step (or an
  `instrumented` flag for the existing build).
- A `SCOPE.md` documenting what's added, build cost (recompile
  time), and what other instrumentation flags would be easy to
  add (memory write-watches, T-state counting, branch coverage).
- A measurement: how many seconds does one fixture take with
  -trace-pc enabled? Compare to vanilla SimCoupé baseline (~30s
  including disk-image build).

## Bounded

4 hours. If at 4 hours the three flags don't all work, stop and
report what blocked you.

## Precedent

The repo builds SimCoupé from upstream v1.2.16 source, which ships
`-exitonhalt` natively (an earlier vendored patch added it before it
landed upstream). Pete has a `sdl-paste-clipboard` branch in
`~/git/simcoupe/` with macOS Paste support. The codebase is
SimCoupé v1.x — C++ — small enough that adding three flags to the
Z80 instruction dispatch + the HALT handler is plausible work.

## Constraints

- Use `g` not `git`.
- Don't dispatch other subagents.
- Don't push to upstream simcoupe.
- Don't modify the sam-aarch64 repo — only `~/git/simcoupe/`.
```

---

## Spike C — Evaluator

```
You are evaluating two parallel test-harness spikes (A: Go harness;
B: SimCoupé fork) and recommending which to productise.

## Inputs

- Spike A deliverables: `~/git/sam-aarch64/tools/z80-test-harness-spike-go/` + its SCOPE.md + its README + its timing measurement.
- Spike B deliverables: `~/git/simcoupe/` (instrumentation branch) + its SCOPE.md + its timing measurement.

Read both spikes' code, both SCOPE.md files, and both timing
measurements. Then compare on these dimensions:

| Dimension | Why it matters |
|-----------|----------------|
| Lines of code added | Long-term maintenance burden |
| Time to run one fixture | The actual iteration-speed pitch (Spike A: ms; Spike B: seconds — likely faster than vanilla SimCoupé due to no boot overhead?) |
| Quality of PC-trace output | Direct end-user value when debugging FAIL00-style hangs |
| Risk of behavioural divergence from SimCoupé oracle | Whether agents could write tests that pass on the harness but fail on real SAM |
| Ease of adding the NEXT instrumentation feature (e.g. memory write-watches, T-state counting, branch coverage) | Future leverage |
| Cost to integrate into CI (`make ci-m3-fast` or similar) | Shipping cost |
| Cost to keep in sync with upstream — Go harness has none, SimCoupé fork has upstream-rebase pain | Long-term overhead |
| Pete's bias (Go expertise > C++ expertise) | Acknowledged; should NOT dominate the recommendation |

## Deliverable

A 600-1000 word doc at `docs/notes/2026-05-28-test-harness-bakeoff-evaluation.md` that:

- Summarises each spike's measured outcomes (don't re-derive — quote from the SCOPE.md files).
- Compares them on the dimensions above with **concrete numbers, not adjectives**.
- Gives ONE recommendation (Go harness OR SimCoupé fork) with reasoning.
- **Calls out explicitly where Pete's Go bias is being discounted** (he asked for this).
- Lists open questions Pete should answer before productising.

Do NOT fence-sit. Pete is OK with the recommendation being wrong if the reasoning is honest.

## Bounded

1 hour. The work is reading + comparing, not implementing.

## Constraints

- Read-only across both repos.
- Don't dispatch other subagents.
- Don't commit anything except the new doc.
```
