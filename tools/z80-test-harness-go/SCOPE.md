# z80-test-harness-go — scope and known limits

Originating context: Spike A of the 2026-05-28 test-harness bake-off. The decision
to adopt this harness as the agent-side dev tool (not a CI gate) is recorded in
`docs/notes/2026-05-28-test-harness-bakeoff-evaluation.md`. This doc captures the
design rationale and known stub gaps; see the README for usage.

## Measured timing

- `inst_nop_ret.s` end-to-end: **~1 ms** (wall-clock on M-series Mac), including
  loading data into emulated pages, ~4800 Z80 step() calls to HALT, hook dispatch.
- SimCoupé-in-Docker baseline for comparison: ~0.5 s per fixture. The harness is
  ~530× faster than the real CI gate.

---

## What is stubbed vs faithful

### Faithful (implemented correctly)
- **SAM paging model**: LMPR (port &FA) for sections A+B, HMPR (port &FB) for sections C+D; bits 5-7 of HMPR are mode-3 CLUT, preserved on all HMPR writes.  Sourced verbatim from `tools/basic-emulator-spike/main.go` comments citing Tech Manual v3.0 §6.10.
- **RST 8 dispatch**: a 7-byte stub at &0008 in the fake ROM correctly extracts the hook code from the DEFB immediately following RST 8, dispatches via port &FD, and returns to the instruction after the DEFB.  The EX (SP),HL trick is position-independent and HL-preserving.
- **Printer ports &E8/&E9**: data latched on rising strobe edge, matching SimCoupé's Centronics model.
- **OUT buffer capture (HSAVE, hook 132)**: reads UIFA[31..36] to find start page + length; reads directly from physical pages 5+6 bypassing current LMPR/HMPR (the correct approach since the assembler may have changed paging state between emit and save).
- **HGTHD (hook 129) for IN file**: populates &4B50+34 (page count) and &4B50+35-36 (length-mod-16K with bit 15 set, as real SAMDOS does).
- **HLOAD (hook 130)**: no-op, because data is pre-deposited in the target physical pages before execution begins — this is faithful to the *effect* HLOAD achieves.
- **Physical page pre-deposit**: enctab.enc deposited into page 4, .tbn deposited into pages 7..12 before the Z80 starts running.

### Stubbed / mocked
- **ROM**: only &0008 is implemented (the RST-8 intercept stub); the rest of the 32 KB "ROM" is 0xFF.  The assembler never reads from ROM except via RST 8.
- **SAMDOS disk I/O**: entirely absent.  HGTHD reads nothing from disk; HLOAD does nothing; HSAVE writes nothing to disk.  The harness captures bytes directly from RAM.
- **HGTHD for enctab.enc**: the assembler uses hardcoded constants (B=ENCTAB_PAGE, C=0, DE=ENCTAB_LEN) to call the trampoline, so the harness does not need to populate &4B50 for enctab.enc.
- **Trampoline execution**: the trampoline (copied to &7E00 by `enctab_trampoline_setup`) runs for real in the emulator — it issues `in a,(251); ld (HMPR_SAVE), a; ld (SP_SAVE), sp; ld sp, TRAMP_SAFE_SP; out (251), a; rst 8; ...`.  The HMPR change is faithfully modelled.  HLOAD inside the trampoline is stubbed (no-op).
- **Interrupts**: disabled throughout (assembler does `di` at start; we never enable them).  Interrupt handlers at &0038/&0066 in ROM are unreachable 0xFF bytes.
- **Border port &FE**: writes are silently ignored.

### Not implemented (out of scope for spike)
- Multi-page OUT files (OUT_LEN > 16384): the HSAVE capture reads pages 5+6 via physical page read, so the logic handles it — but it has not been tested.
- HGFLE (158) / LBYT (159): not called by the prod assembler; no-op stubs suffice.
- HOFLE (147) / SBYT (148) / CFSM (152): not called; not stubbed.

---

## Cost-to-extend estimate

### Cover full m3..m6 corpora (all fixtures, both prod and test variants)

**Estimated: 4–8 hours** for one engineer.

| Task | Hours |
|---|---|
| Drive all m3 fixtures (trivial loop around `Run()`) | 0.5 |
| Drive m4 fixtures (same binary, different .tbn files) | 0.5 |
| Drive m5 fixtures (same; larger .tbn, possibly multi-page IN) | 1 |
| Drive m6 fixtures (large OUT; test HSAVE multi-page capture) | 2 |
| Test variant (BUILD_TESTS=1 binary): run self-tests path + fixtures | 1 |
| Integration into Makefile (`make ci-m3-fast`) | 0.5 |
| CI job (`.github/workflows/`) | 0.5 |
| Total | 6 |

The m6 estimate is higher because the m6 fixtures emit >16 KB of OUT, requiring
HSAVE to read across both page 5 and page 6.  The harness `readPageBytes` function
already supports this, but it needs an integration test.

### Most likely complications

1. **Test variant**: the BUILD_TESTS binary runs boot-time self-tests that do many more
   Z80 steps.  Budget may need to increase from 10 s to 30 s for some self-tests.
   The trampoline self-tests interact with HMPR in ways that may expose gaps in the HGTHD stub.

2. **HGTHD for enctab.enc in test variant**: the test variant calls `run_trampoline_self_tests`
   which re-calls `load_enctab`.  The &4B50 population for "enctab.enc" may be needed there.

3. **&4B50 page resolution**: the UIFA and UIFA-copy area at &4B00 / &4B50 are in section B.
   The harness currently resolves section B as page `(lmpr & 0x1F + 1) & 0x1F`.  If LMPR
   changes before HGTHD is invoked (unlikely for IN, possible for re-entrant hook calls), the
   wrong page will be written.  Needs a cross-check.

4. **PC trace ring buffer**: currently records every step — may be expensive for the test
   variant which runs ~200K+ steps.  A simple counter gate (record only last N) is trivial.

