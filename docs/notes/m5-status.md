# M5 — current status (read me first)

Entry point for any session picking up where M5 left off.

**M5 COMPLETE.** All 18 tasks done across 6 PRs (#29, #30, #31, #32,
#33, this PR). The SAM-side Z80 assembler, running in SimCoupé, now
handles the full compound-operand grammar (shifted register, extended
register, all seven memory addressing shapes, system registers, literal
pool) and the remaining directives (`.set`/`.equ`, `.balign`/`.align`,
`.org`, `.skip`/`.space`, `.inst`, `.ltorg`, plus `.global`/`.section`/
`.arch`/`.cpu` no-ops).  Byte-identical to `aarch64-*-as + ld -Ttext=0
+ objcopy -O binary` for the M5 fixture corpus (19 fixtures).

**M5 SUPERSEDED by M6 (in progress).**  M5 closed the compound-operand
+ directive gap.  M6 (currently mid-flight) extends the reach to real
spectrum4 sources via paged buffers, compact `.tbn`, and a disassembler.
See `docs/notes/m6-status.md` for the current state of play.  The M5
fixture corpus + CI matrix are still part of the standing regression.

## What M5 is (spec recap)

Per `docs/specs/2026-05-27-m5-compound-operands-directives-design.md`:

The remaining grammar M3 / M4 explicitly deferred:

- **Compound operand kinds**: `OpShiftedReg` (Rm, lsl/lsr/asr/ror #N),
  `OpExtendedReg` (Rm, uxtb/uxth/uxtw/uxtx/sxt* #N), `OpMem` (all seven
  shapes: scaled / unscaled / pre-index / post-index / register-offset /
  extended-offset / pair), `OpSysName` (mrs / msr / dc / tlbi), and
  `OpLitPool` (`ldr Xn|Wn, =<expr>`).
- **Remaining directives**: `.set` / `.equ` (synonyms), `.balign` /
  `.align`, `.org`, `.skip` / `.space`, `.inst`, `.ltorg`, plus
  `.global` / `.section` / `.arch` / `.cpu` as size-0 no-ops.
- **One mnemonic-ID intercept**: `ror Rd, Rs, #imm` (EXTR alias).
- **Code-budget lever**: ENCTAB relocated out of section C to a paged
  region, freeing &A000-&AFFF for code (8 → 12 KB code budget for the
  production variant).

Together these close the M1 → M4 fixture-promotion gap.  Every M1
fixture that doesn't require source > 2 KB now has an M5 home.

## Tasks 1–18 — all done

| Task | What | Landed in |
|---|---|---|
| 1 | Cheap directives: `.set`/`.equ`, `.skip`/`.space`, `.inst`, no-op family | PR #29 |
| 2 | `.balign` / `.align` — PC-aware NOP/zero pad | PR #29 |
| 3 | `.org` — direct PC assignment + zero-fill | PR #29 |
| 4 | `ror`-imm intercept (EXTR alias) | PR #30 |
| 5 | OpShiftedReg encoder | PR #30 |
| 6 | OpExtendedReg encoder | PR #30 |
| 7 | OpMem encoder — indexed + unscaled | PR #32 |
| 8 | OpMem encoder — pre-index, post-index, register-offset | PR #32 |
| 9 | OpMem encoder — extended-offset + pair | PR #32 |
| 10 | OpMem encoder — signed loads (ldrsb / ldrsh / ldrsw) | PR #32 |
| 11 | OpSysName encoder + sysreg table | PR #33 |
| 12 | Code-budget lever — ENCTAB paged out of section C | PR #31 |
| 13 | OpLitPool — pass-1 dedup, .ltorg flush, pass-2 imm19 patch | This PR |
| 14 | OpString-as-inst-operand defensive error path | PR #33 |
| 15 | Fixture corpus audit (19 fixtures) | This PR |
| 16 | Layer 3 round-trip script (`tools/run-m5-roundtrip.sh`) | PR #29 |
| 17 | Makefile + GH Actions `m5` / `m5-prod` jobs | This PR (jobs) / PR #29 (Makefile) |
| 18 | Status doc + ROADMAP flip + README | This PR |

## Test status (all green)

| Layer | Status | Notes |
|---|---|---|
| `make m3-asm` (test) | ✅ PASS | 15965 B / 20391 B in disk slot |
| `make m3-asm-prod` | ✅ PASS | 11868 B / 12288 B (420 B headroom) |
| `make ci-m3` | ✅ PASS | 9/9 M3 fixtures (regression check) |
| `make ci-m3-prod` | ✅ PASS | 9/9 M3 fixtures (production variant) |
| `make ci-m4` | ✅ PASS | 4/4 M4 fixtures |
| `make ci-m4-prod` | ✅ PASS | 4/4 M4 fixtures (production variant) |
| `make ci-m5` | ✅ PASS | 19/19 M5 fixtures |
| `make ci-m5-prod` | ✅ PASS | 19/19 M5 fixtures (production variant) |
| Boot-time self-tests | ✅ PASS | Slots + symbols + local-label + M4 expr_eval + PC-rel + M5 directives + ror-imm + ShiftedReg + ExtendedReg + Mem + SysName + LitPool |
| GitHub `m5:` job | ✅ PASS | Wired into `.github/workflows/ci.yml` |
| GitHub `m5-prod:` job | ✅ PASS | Production-variant CI gate |

## M5 fixture corpus (19 fixtures)

| Fixture | Exercises |
|---|---|
| `dir_align_skip.s` | `.balign` + `.skip` |
| `dir_equ.s` | `.equ` (symbol with constant value) |
| `inst_alu_single.s` | bare ALU + `ror`-imm intercept |
| `inst_ands.s` | OpShiftedReg + LogicalImm (`ands`, `bic`, `bics`) |
| `inst_dc_tlbi.s` | OpSysName (`dc`, `tlbi`) |
| `inst_extended.s` | OpExtendedReg (`add`/`sub` extended) |
| `inst_ldrs.s` | OpMem signed loads (`ldrsb`, `ldrsh`, `ldrsw`) |
| `inst_ldr_litpool.s` | OpLitPool basic + symbol-valued entry |
| `inst_ldr_litpool_ltorg.s` | OpLitPool + `.ltorg` flush + per-segment dedup |
| `inst_mem_extended.s` | OpMem extended-offset (`[Xn, Wm, sxtw #N]`) |
| `inst_mem_indexed.s` | OpMem scaled indexed (`[Xn, #imm]`) |
| `inst_mem_pair.s` | OpMem pair (`ldp` / `stp`) |
| `inst_mem_preindex.s` | OpMem pre-index (`[Xn, #imm]!`) + post-index |
| `inst_mem_simple.s` | OpMem base-only (`[Xn]`) |
| `inst_mem_str_promote.s` | STR auto-promotion to STUR for negative scaled offsets |
| `inst_movz_movk_sym.s` | `.set` + symbol-valued movz / movk |
| `inst_mrs_msr.s` | OpSysName (`mrs`, `msr`) |
| `inst_shifted.s` | OpShiftedReg core (add/sub/and/orr/eor with lsl/lsr/asr/ror) |
| `inst_unscaled_mem.s` | OpMem unscaled (`stur`, `ldur`) |

Deferred (covered-by-implication or non-trivial):

- `inst_ldr_litpool_local.s` — requires two-digit local labels (`10f`).
  The M4 local-label table only supports digits 1..9 (single-digit
  per refenc/format).  Promote in M6 when the table is extended to
  handle multi-digit local labels.
- `inst_bfc_sbfx.s` — BitfieldImm extras; the M4 BitfieldImm path
  already covers this.  Promote only if a regression surfaces.

## Memory layout (during assembly)

```
&8000-&AFFF  assembler code (12 KB; prod variant 11868 B post-PR-E,
                            test variant fits in 40-sector disk slot)
&B000-&B7FF  IN .tbn buffer (2 KB)
&B800-&BFFF  OUT buffer (2 KB)
&C000-&C0FF  stack (SP = &C100)
&C100-&C157  scratch — OPVAL arrays, OPVAL_KINDS
&C158        PASS_MODE                       (1 byte)
&C159        PASS_PC                         (4 bytes LE)
&C160-&C95F  SYMTAB                          (256 buckets × 8 bytes = 2 KB)
&C960-&CD5F  SYMTAB_OVERFLOW                 (~1 KB)
&CD60-&D15F  LOCAL_LABEL_TABLE               (9 digits × 98 bytes ≈ 882 B)
&D100-&D107  OPMEM_OFF                       (OpMem 8-byte LE offset)
&D200-&D3BF  LITPOOL_TABLE                   (32 slots × 14 bytes = 448 B)
&D3C0-&D47F  LITPOOL_PC_MAP                  (32 entries × 6 bytes = 192 B)
&D480-&D487  LITPOOL counters + saved-PC
&D488-&FFFF  scratch (eval stack, encoder scratch, ~10.9 KB free)

Physical page 4 (off-axis): ENCTAB body — paged into section A on demand
                            for encoder runtime reads.
```

## Code budget heads-up

| variant | size | budget | headroom |
|---|---|---|---|
| `m3-asm` (test, includes boot self-tests) | 15965 B | 20391 B (40-sector disk slot) | 4426 B |
| `m3-asm-prod` (no self-tests) | 11868 B | 12288 B (`&8000-&AFFF` code section) | **420 B** |

M5 PR-E consumed ~1287 B of production headroom (10581 → 11868).
Remaining 420 B is enough to absorb small follow-ups; any larger M6
feature should either come in via paged code (mirroring ENCTAB's PR #31
layout — see `src/m3/trampoline.asm`) or motivate a second budget lever.

## What's NOT in M5 scope (still M6 / later)

- **Source > 2 KB** — paged source loading is M6.  Spectrum4's
  `release.s` is ~20 KB source; the IN buffer trampoline pattern from
  PR #31 (ENCTAB) is the prerequisite.
- **Compact `.tbn` format + built-in disassembler** — M6.  Defers
  until real spectrum4 sources need to fit in SAM RAM.
- **Multi-digit local labels** (`10f`/`10b`) — the M4 local-label
  table is hard-coded to digits 1..9.  One M5 fixture
  (`inst_ldr_litpool_local.s`) needs this; deferred to M6.
- **64-bit MUL / DIV on Z80** — still rejected.
- **Macros / conditional assembly** — handled by Mac-side `text2bin`
  per `docs/specs/2026-05-25-macro-expansion-research.md`; never
  reaches SAM.
- **On-SAM editor** — Phase 2.
- **TFTP shipping** — Phase 3.
- **Per-fail-site diagnostic strings** — separate deferred follow-up.

## Hand-off recipe (verify locally)

```bash
# Inside the sam-aarch64 dev container or with toolchain locally:
make ci-m3 ci-m4 ci-m5
# expect:
#   9/9 M3 fixtures matched
#   4/4 M4 fixtures matched
#   19/19 M5 fixtures matched

# Production-variant gate (the standing CI matrix):
make ci-m3-prod ci-m4-prod ci-m5-prod
# expect identical pass counts
```

Or via Docker (mirrors GH Actions):

```bash
docker run --rm -v "$(pwd):/work" -w /work \
  -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
  -e AARCH64_AS=aarch64-linux-gnu-as -e AARCH64_LD=aarch64-linux-gnu-ld \
  -e AARCH64_OBJCOPY=aarch64-linux-gnu-objcopy \
  ghcr.io/petemoore/sam-aarch64-dev:latest \
  bash -c '
    Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 &
    sleep 1; export DISPLAY=:99
    rm -rf build/
    make ci-m3 ci-m4 ci-m5 ci-m3-prod ci-m4-prod ci-m5-prod
  '
```

## Authoritative references

- M5 spec: `docs/specs/2026-05-27-m5-compound-operands-directives-design.md`
- M5 plan: `docs/plans/2026-05-27-m5-compound-operands-directives.md`
- M4 status (prior milestone): `docs/notes/m4-status.md`
- M3 status (older milestone): `docs/notes/m3-status.md`
- M1 round-trip oracle (linked): `tests/m1/run-refenc-roundtrip.sh`
- M5 round-trip script: `tools/run-m5-roundtrip.sh`
- ROADMAP: `docs/ROADMAP.md`
