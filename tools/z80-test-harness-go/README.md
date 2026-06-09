# z80-test-harness-go

A Go harness using [koron-go/z80](https://github.com/koron-go/z80) that runs the
sam-aarch64 SAM-side assembler end-to-end without SimCoupé. **It exists to make
agent iteration on Z80 code fast — not to gate CI.** SimCoupé under Docker is the
sole CI gate; this harness is a developer-side tool.

At ~1 ms per fixture (vs ~0.5 s for SimCoupé-in-Docker) it slots into the inner
loop: edit → test → iterate without containers. See
[`docs/notes/2026-05-28-test-harness-bakeoff-evaluation.md`](../../docs/notes/2026-05-28-test-harness-bakeoff-evaluation.md)
for the design decision and full workflow guidance.

## When to use it

- You're iterating on a Z80 code change in the assembler and want sub-millisecond
  feedback.
- You want to inspect last-200-PC traces, register state, or OUT bytes from a
  fixture run.
- You're sweeping over many fixtures locally before pushing.

## When NOT to use it

- As a CI gate. The existing `make ci-m{3,4,5,6}{,-prod}` SimCoupé matrix is the
  authoritative gate.
- When the harness disagrees with SimCoupé. SimCoupé wins — fix the harness, but
  ship based on SimCoupé's verdict.
- When the harness crashes or behaves unexpectedly on your iteration. Skip it,
  run SimCoupé under Docker directly. The harness can be unhelpful without that
  being a problem.

## Prerequisites

Build the required artefacts from the repo root:

```
make m3-asm-prod enctab sam-aarch64
```

## Run the unit test

```
cd tools/z80-test-harness-go
go test -v ./...
```

## Run the standalone binary

Production variant:

```
go build -o /tmp/z80-harness .
/tmp/z80-harness \
    -assembler ../../build/assembler-prod.bin \
    -enctab ../../build/enctab.enc \
    -sysreg-data ../../build/sysreg_data.bin \
    -in /tmp/inst_nop_ret.tbn
```

**Always pass `-sysreg-data` for the prod assembler.**  The prod assembler
*unconditionally* HLOADs the sysreg lookup data (SAMDOS file `"sd13"`) into
physical page 13 at boot (`src/loader.asm::load_page13_payload`, called
unconditionally from `src/assembler.asm`).  Any source that uses a
sysreg / `dc` / `tlbi` / pstate operand then runs the page-13 matcher via
`paged_call`.  If `-sysreg-data` is omitted, page 13 is empty, the matcher
runs into a zero/`nop` slide and falls off the end of section C into the
empty section D, wrapping to `&0038` — a cryptic trap that looks like a deep
paging bug but is really a missing input.  The harness now detects this and
names the unserved `"sd13"` file in the trap message.  (Small sources with no
sysreg operands happen to survive without it, which is why this was easy to
miss.)  Build it with `make sysreg-data`.  See
`docs/notes/2026-05-29-go-harness-paged-trap-rootcause.md`.

BUILD_TESTS variant (runs the boot-time self-test suites, including
`run_reader_paged_self_tests`).  Pass the off-axis test_mem binary and the
page-14 payload so HGTHD/HLOAD can serve them:

```
make m3-asm test-mem-offaxis paged-call-payload enctab sam-aarch64
/tmp/z80-harness \
    -assembler ../../build/assembler.bin \
    -enctab ../../build/enctab.enc \
    -in /tmp/inst_nop_ret.tbn \
    -test-mem ../../build/test_mem.bin \
    -p14 ../../build/paged_call_test_payload.bin
```

On any non-PASS exit the runner prints the register snapshot
(`Regs:`), step count, and the last 30 PCs.  For deeper analysis use
`RunConfig` from Go (windowed PC/register traces via `TraceLo/TraceHi`, or a
trigger-PC backtrace via `TrigPC`) — see `test_variant_test.go` and
`SCOPE.md` "PR-6 additions".

## How it works

1. Loads `assembler-prod.bin` into physical RAM page 2 (section C, &8000-&BFFF).
2. Pre-deposits `enctab.enc` into physical page 4 and the `.tbn` into pages 7-12.
3. Installs a 7-byte RST-8 intercept stub at &0008 in the fake ROM.
4. Runs the Z80 CPU via koron-go/z80 until HALT or timeout.
5. Intercepts SAMDOS hooks via port &FD: HGTHD (129) populates &4B50 with IN file
   geometry; HLOAD (130) is a no-op (data already in pages); HSAVE (132) captures
   OUT bytes from UIFA[31..36] + physical pages 5-6.
6. Captures printer bytes from ports &E8/&E9.
7. Returns `(passed, printerCapture, outBytes, last200PC, exitReason)`.

See [`SCOPE.md`](SCOPE.md) for what's faithful vs stubbed and known limits.

## Evolving the harness

Agents own this code. Improvements (richer state dumps, single-step execution,
memory watchpoints, T-state counting, opcode bytes in the PC trace, branch
coverage, anything else that turns out to be useful when something crashes or
behaves unexpectedly) are part of normal Z80 work. PR them without ceremony — no
design review needed for changes that obviously make the tool more useful.
