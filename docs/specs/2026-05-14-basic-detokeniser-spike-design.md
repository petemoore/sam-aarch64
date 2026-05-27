# basic-detokeniser-spike — design

**Status:** approved 2026-05-14 by Pete (verbal in chat); driven through
to completion in a single autonomous session.

## Purpose

Symmetric counterpart to `tools/basic-emulator-spike/` (text → tokenised
BASIC). Takes a tokenised SAM BASIC file (from a `.mgt` or raw
FT_SAM_BASIC body) and recovers the canonical source text using the SAM
ROM running under koron-go/z80 — same stack, same FLAGS/LASTK injection
and Snapshot/Restore plumbing as the forward spike.

Competes with `tools/llist-capture/` (which boots a full SimCoupé and
captures the parallel-port output of an injected `LLIST` line). The new
spike is in-process pure Go, headless, deterministic, ~3 orders of
magnitude faster per file, and — critically — does not have the 80-char
column wrap that LLIST imposes on its printer-channel output.

Lives in this repo, not in samfile. Both directions (text → tokens and
tokens → text) eventually replace samfile's approximate Go-side
implementations once production-grade; this spike is the experimental
stepping-stone for the inverse direction.

## Scope

In scope:

- `tools/basic-detokeniser-spike/` — new spike binary.
- Extension of `tools/llist-sweep/` to accept a `--uut=spike` flag,
  feeding the new spike as the unit-under-test (default behaviour
  unchanged).
- A `wrapToLLIST` library function inside the spike package, applied to
  spike output by the sweep harness so its un-wrapped lines can be
  compared byte-for-byte against llist-capture's wrapped oracle.

Out of scope:

- `tools/llist-capture/`, `tools/llist-sweep/` (beyond the `--uut` flag),
  `tools/basic-emulator-spike/`, samfile, the SimCoupé patch — all
  untouched.
- A `basic-detokeniser-sweep` standalone tool. Reusing llist-sweep
  avoids ~80% duplication; the `--uut` flag does the job.
- FDC emulation (the spike loads PROG by direct memory poke, same shape
  as the forward spike's bypass philosophy).

## Architecture — two stages with an explicit go/no-go gate

### Stage 1 — empirical probe (derisks the EDIT-buffer hypothesis)

The whole design rests on one unverified assumption: that SAM's `EDIT N`
command places **source text** (not tokens) into ELINE. Sinclair
Spectrum BASIC, the SAM ancestor, stores EDITed lines as tokens with
on-the-fly display rendering. SAM may inherit that, in which case
Approach A is a dead end.

The probe answers this empirically and quickly:

1. Build the boot loop from the forward spike (ROM + paging logic +
   banner-skip at 0x0F75 → 0x0F78 + FLAGS/LASTK intercept in `Get`/`Set`
   + Snapshot/Restore). Effectively a copy of the forward spike's
   `Hardware` type and boot phase, in a new Go module.
2. Take `/tmp/test.bas` (3 trivial lines from the forward-spike
   verification work), run the forward spike to produce a tokenised
   `.mgt`. Extract the FT_SAM_BASIC body bytes via samfile.
3. Boot the spike to MAINELP. Copy the tokenised body bytes into RAM at
   the address `sysPROG` points to. Update the sysvar bracket
   (`NVARS` / `VARS` / `WORKSP` / `STKEND` — exact list determined by ROM
   disasm during probe build; probe will surface anything missing).
4. Inject `EDIT 10` + CR through the FLAGS/LASTK channel.
5. Wait for the editor to come back to a "ready" state (PC analogue of
   the forward spike's `ERROR2` trap — to be identified during probe
   build via PC trace).
6. Dump ELINE bytes — length, hex, ASCII rendering — alongside the
   sysvar state.

**Decision criteria, applied at probe completion:**

- **PASS — proceed to Stage 2:** ELINE contains source-text bytes
  resembling `"10 PRINT 1"` (i.e. ASCII characters spelling SAM
  keywords, possibly with normalisations like upper-case keywords).
- **FAIL, recoverable — iterate on probe:** `EDIT` raises an error, or
  the editor wanders. Smells like missing sysvar state. Iterate the
  load step using ROM disasm.
- **FAIL, fundamental — abandon Approach A:** ELINE contains the same
  tokenised bytes that we poked into PROG. SAM inherits ZX behaviour;
  EDIT does not detokenise. Stop work on this spike, write a follow-up
  spec for Approach B (CURCHL print-vector stub — see "Fallback" below).

The probe is checked in as a `--probe` mode of the spike binary, kept
after the gate so it can be re-run for debugging or regression checks.

### Stage 2 — production extractor, contingent on Stage 1 PASS

1. Same boot to MAINELP, snapshot. Banner-skip hijack as in Stage 1 /
   the forward spike.
2. Read input: either an `.mgt` path + `--filename` for the entry name,
   or a raw FT_SAM_BASIC body file. Parse via `samfile`/`sambasic` to a
   `*sambasic.File`. Walk `ProgBytes()` to enumerate `{lineNumber}` in
   ascending order.
3. Load PROG once: copy `ProgBytes()` into RAM at `sysPROG`, update the
   sysvar bracket determined during Stage 1. Snapshot post-load — every
   per-line extraction restores from this snapshot, mirroring the
   forward spike's amortisation of the ~30 ms cold boot.
4. For each line number in ascending order:
   - Restore from post-load snapshot.
   - Inject `EDIT <N>` + CR via FLAGS/LASTK.
   - Step CPU until the "EDIT done" PC (identified in Stage 1).
   - Snapshot ELINE bytes between the edit-area start and the trailing
     0x0D.
   - Append to results.
5. Output: one logical BASIC line per output line, in line-number order,
   format `<n> <text>\n`. (Matches LLIST's first-line-of-each-entry
   layout. Un-wrapping is implicit — the spike never wraps.)

### CLI surface

```
basic-detokeniser-spike --rom <path> --in <mgt-or-body> \
    [--filename <name-in-mgt>] --out <text-file> [--probe N]
```

- `--rom`: path to `samcoupe.rom` (32KB). Default same as forward spike.
- `--in`: input file. Either an `.mgt` (requires `--filename` to pick
  the FT_SAM_BASIC entry) or a raw tokenised body.
- `--filename`: BASIC file name inside an `.mgt`. Ignored when `--in` is
  a raw body.
- `--out`: output text file. One BASIC line per output line.
- `--probe N`: Stage 1 mode. Drives `EDIT N` and dumps ELINE; skips
  full extraction.

## Sweep / corpus validation

Reuse `tools/llist-sweep/`, adding a `--uut` flag:

- `--uut=samfile-b2t` (default) — current behaviour: oracle is
  llist-capture, UUT is `samfile basic-to-text --lossy`.
- `--uut=spike` — new behaviour: oracle is llist-capture, UUT is
  `basic-detokeniser-spike`, with `wrapToLLIST` applied to spike output
  before the byte-compare.

Per-file flow when `--uut=spike`:

1. `llist-capture <disk> <file>` → wrapped LLIST output (oracle).
2. `basic-detokeniser-spike --in <disk> --filename <file>` →
   un-wrapped per-line text.
3. `wrapToLLIST(spike_output)` → re-wrapped to 80-column LLIST format.
4. `cmp` the re-wrapped spike output against the oracle.
5. TSV row: status (`match` / `mismatch` / `error`) / disk / file /
   detail (first divergence context — same shape as today).

`wrapToLLIST` lives at `tools/basic-detokeniser-spike/wrap.go` with
table-driven unit tests in `wrap_test.go`. Initial implementation
models SAM's 80-column wrap rules; precise rule details are refined
empirically as sweep divergences surface. wrap_test cases grow with
each rule learned.

**Why re-wrap spike output, not un-wrap LLIST output:**
Re-wrap is mechanical (deterministic given column width + line
content). Un-wrap requires heuristics ("does this line continue?") and
is fragile. Cleaner direction.

## Fallback — Approach B (only if Stage 1 fails fundamentally)

If `EDIT` does not detokenise into ELINE, the spike abandons keyboard
injection for output. Pete's observation: SAM (inheriting from ZX
Spectrum) routes printing through a per-channel function pointer
(`CURCHL` → channel's PR-routine). We can:

1. Boot ROM, load PROG, snapshot.
2. Install a custom channel whose PR-routine is a small Z80 stub at a
   RAM address we control. The stub does `OUT (port),A : RET` to a port
   we've claimed for output capture.
3. Switch `CURCHL` to point to our channel.
4. Inject `LIST<CR>` (not LLIST). The ROM emits each char to our stub,
   we collect via the IO callback.
5. Per-line text accumulation: watch for `0x0D` separators in the
   captured byte stream.

This avoids the column-wrap that LLIST applies to its printer channel
because we replace the per-char output function with one that doesn't
track column position.

Stage 1 fail → new spec for Approach B, separate session. Not designed
in detail here.

## Testing

### Stage 1 self-tests

- Probe runs cleanly to completion (no HALT before EDIT, no step-budget
  exhaustion).
- ELINE dump is produced and human-inspectable.
- Pete (or in his absence, autonomous judgement based on heuristics
  documented below) decides the gate outcome.

Autonomous gate criteria when Pete isn't available:

- ELINE bytes >50% printable ASCII AND contain at least one SAM keyword
  spelling (`PRINT`, `LET`, etc.) → PASS.
- ELINE bytes match the original PROG bytes for line N → FAIL
  (fundamental).
- Anything else → iterate on the probe (try with extra sysvar setup,
  try a different line number, re-check boot state).

### Stage 2 self-tests

1. **Round-trip:** forward spike (`.bas` → `.mgt`) → inverse spike
   (`.mgt` → `.bas'`), diff `.bas'` against the original `.bas`. Expect
   equality modulo SAM normalisations (case, spacing — discovered
   empirically and documented in a "Known normalisations" section
   added to this spec as findings come in).
2. **wrap unit tests:** `wrap_test.go` table-driven, starting with
   hand-checked cases (sub-80-char line, line crossing 80 chars,
   multi-line program). Tests grow with sweep divergences.

### Stage 2 corpus validation (via extended llist-sweep)

- **Smoke phase:** 10 disks. Must complete cleanly. Any HALT /
  step-budget exhaustion is a spike bug; fix before proceeding.
- **Sample phase:** 100 disks. Target ≥95% match rate, OR every
  mismatch categorised as one of: spike bug, wrap-rule gap, known SAM
  weirdness.
- **Full corpus:** only after sample phase mismatch rate is understood
  (and either fixed or categorised). Output TSV is the artefact;
  divergences feed back into `wrapToLLIST` improvements or, rarely,
  spike fixes.

## Out of scope for this work

- Round-trip across the corpus (forward spike → tokenised → inverse
  spike → text → forward spike). Useful long-term regression but not
  part of initial spec — the forward spike isn't itself corpus-validated
  yet.
- Performance benchmarks as gating criteria. Spike will report
  per-line latency in the same style as the forward spike for
  informational purposes; not a release criterion.
- No upfront refactor of the forward spike's `Hardware` /
  `Snapshot` types into a shared package. Duplicate-then-decide. Once
  both spikes have stabilised, factor commonalities in a follow-up.

## Repo layout

```
tools/basic-detokeniser-spike/
├── go.mod
├── go.sum
├── main.go        — CLI, flag parsing, top-level orchestration
├── hardware.go    — Hardware struct, Get/Set/In/Out, resolve, Snapshot/Restore
├── sysvars.go     — sysvar address constants
├── load.go        — copy tokenised body into RAM, update sysvars
├── extract.go     — drive EDIT N, capture ELINE, decode (Stage 2)
├── probe.go       — Stage 1 entrypoint
├── wrap.go        — wrapToLLIST function
└── wrap_test.go   — table-driven tests for wrap

tools/llist-sweep/
└── main.go        — extended: --uut flag, spike-mode UUT call,
                     wrapToLLIST application
```

## Known unknowns at design time

- **Exact sysvar list to update post-load.** The forward spike's success
  with `PROG` + `NVARS` + canonical NumericVars suggests the bracket
  needs at minimum `PROG`/`NVARS`/`VARS`/`WORKSP`/`STKEND`. The probe
  will reveal what else.
- **The "EDIT done" PC.** Forward spike uses `0x37CE` (ERROR2) as
  end-of-tokenise. EDIT has a different exit path — likely a return to
  MAINELP or a similar editor-idle PC. PC trace during probe build
  identifies it.
- **SAM's wrap rules.** The 80-char wrap is broadly known; whether it's
  exactly 80 vs 79, where it inserts continuation indents, how it
  handles tokens that span the boundary — discovered via sweep
  divergences.
