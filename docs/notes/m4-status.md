# M4 — current status (read me first)

Entry point for any session picking up where M4 left off.

**M4 COMPLETE.** All 10 tasks done across 3 PRs (#21, #22, this PR). The
SAM-side Z80 assembler, running in SimCoupé, now performs a full
two-pass assembly with symbol resolution, local-label resolution, and
PC-relative encoding — byte-identical to `aarch64-*-as + ld -Ttext=0
+ objcopy -O binary` for the M4 fixture corpus (4 fixtures).

## What M4 is (spec recap)

Per `docs/specs/2026-05-24-m4-symbols-multipass-design.md`:

The three pieces M3 explicitly deferred:

- **Symbol table** on the Z80 — name → address resolution, including
  forward references.
- **Two-pass design** — pass 1 assigns PC and builds the symbol /
  local-label tables; pass 2 emits bytes with resolved references.
- **Full expression evaluator** — port the rest of M1's expression
  bytecode opcodes: `PUSH_SYM`, `PUSH_LOCAL`, `PUSH_PC`, and all
  `REL_*` operators.

Together these unlock: branches to labels, PC-relative loads, `:lo12:`
/ `:hi12:` operators, local-label refs (`1f`/`1b`), and forward
references generally.

## Tasks 1–10 — all done

| Task | What | Landed in |
|---|---|---|
| 1 | Pass-mode flag in record walker | PR #21 |
| 2 | Symbol table — hash-bucketed insert + lookup | PR #21 |
| 3 | Local-label table — forward/backward resolution | PR #21 |
| 4 | Pass 1 — build symbol + local-label tables (PASS_PC counter, walker restructure) | PR #22 |
| 5 | Expression evaluator: PUSH_SYM, PUSH_LOCAL, PUSH_PC, REL_* opcodes | PR #22 |
| 6 | PC-relative slot encoders subtract PC before encoding (BranchImm26/19/14, AdrpImm) | PR #22 |
| 7 | Expand fixture corpus (4 M4 fixtures) | This PR |
| 8 | Layer 3 round-trip for new fixtures (`tools/run-m4-roundtrip.sh`) | This PR |
| 9 | Makefile + CI integration (`ci-m4` target + GH workflow `m4` job) | This PR |
| 10 | Status doc + declare done | This PR |

## Test status (all green)

| Layer | Status | Notes |
|---|---|---|
| `make m3-asm` | ✅ PASS | ~8 KB assembler binary (M3 + M4 features) |
| `make ci-m3` | ✅ PASS | 9/9 M3 fixtures still match (regression check) |
| `make ci-m4` | ✅ PASS | 4/4 M4 fixtures match GNU end-to-end via SimCoupé |
| Boot-time self-tests | ✅ PASS | Slot encoders + symbol table + local-label table + M4 expr_eval opcodes + PC-relative slot encoders |
| GitHub `m4:` job | ✅ PASS | Wired into `.github/workflows/ci.yml` |

## M4 fixture corpus (4 fixtures)

| Fixture | Exercises |
|---|---|
| `inst_bcond.s` | LabelDef (`main:`) + PUSH_SYM (backward) + BranchImm19 + CondCode (`b.lt`, `b.ne`, `b.eq`) + `ret` |
| `inst_csel.s` | CondCode with bare-register operands (`csel`, `csinc`) — promoted from M1, didn't need M4 to encode but kept here as a CondCode smoke test |
| `local_labels.s` | LocalDef (`1:`) + PUSH_LOCAL backward (`1b`) + BranchImm19 (`cbnz`) — exercises the local-label table |
| `expr_pcrel.s` | LabelDef (`msg:`) + PUSH_SYM (forward) + AdrpImm + REL_LO12 (`adrp x0, msg; add x0, x0, :lo12:msg`) + `.ascii` |

Coverage by M4 feature:
- forward symbol refs ✓ (expr_pcrel: `adrp` references `msg:` defined later)
- backward symbol refs ✓ (inst_bcond: `b.lt main` references `main:` defined earlier)
- forward local refs (`1f`) — not in corpus today; the local_labels fixture only uses `1b`. Worth adding when an M5 fixture needs it.
- backward local refs (`1b`) ✓ (local_labels)
- PC-relative branch resolution ✓ (inst_bcond, local_labels)
- AdrpImm + REL_LO12 page/offset split ✓ (expr_pcrel)
- `.ascii` size computation in pass 1 ✓ (expr_pcrel)

## Oracle: `as + ld -Ttext=0 + objcopy`

The M4 round-trip oracle invokes the full GNU pipeline including the link step. The link step resolves the relocations GNU's `as` leaves in the .o file as zero placeholders. `ld -Ttext=0` chooses base address 0, matching the M4 assembler's pass-1 PC start. This is the same oracle M1's `tests/m1/run-refenc-roundtrip.sh` has used since M1 — see its header for the spec citation.

The earlier M3 oracle (`tools/run-m3-roundtrip.sh`) skips the link step because M3 fixtures carry no relocations.

## Memory layout (during assembly)

```
&8000-&9FFF  assembler code (~8 KB; test variant 8054 B, prod variant 6077 B post-PR-#25)
&A000-&AFFF  enctab.enc buffer (4 KB)
&B000-&B7FF  IN .tbn buffer (2 KB)
&B800-&BFFF  OUT buffer (2 KB)
&C000-&C0FF  stack (SP = &C100)
&C100-&C157  scratch — OPVAL arrays, OPVAL_KINDS
&C158        PASS_MODE                   (1 byte)
&C159        PASS_PC                     (4 bytes LE)
&C160-&C95F  SYMTAB                      (256 buckets × 8 bytes = 2 KB)
&C960-&CD5F  SYMTAB_OVERFLOW             (~1 KB)
&CD60-&D15F  LOCAL_LABEL_TABLE           (9 digits × 98 bytes ≈ 882 B)
&D160-&FFFF  scratch (eval stack, encoder scratch, ~11.6 KB free)
```

## Code budget heads-up

| variant | size | budget | headroom |
|---|---|---|---|
| `m3-asm` (test, includes boot self-tests) | 8054 B | 8192 B (`&8000-&9FFF`) | 138 B |
| `m3-asm-prod` (no self-tests) | 6077 B | 8192 B | **2115 B** |

PR #25 (build split) carved out the production variant. M5 should build against `m3-asm-prod` for maximum runway. The test variant is reserved for dev / CI pre-flight where the boot self-tests catch per-routine regressions before the fixture corpus even runs.

If M5 exhausts the 2115 B production headroom, the next levers are:

1. Move `ENCTAB_BUF` up from `&A000` (currently 4 KB at &A000-&AFFF) to free more code space.
2. Shrink the encoder by factoring out duplicated dispatch.

Out of scope for M4.

## What's NOT in M4 scope (still M5 / later)

- Compound operands: `OpShiftedReg`, `OpExtendedReg`, `OpMem`, `OpString`-as-instruction-operand, `OpSysName`, `OpLitPool`. The M4 dispatcher errors on these (one M1 fixture, `inst_shifted.s`, hits this and was deliberately NOT promoted; same for `inst_ands.s` which uses shifted-reg in its second half).
- `.set` directive (introduces a symbol with a constant value, no PC). Not yet supported — pass 1's directive size table has no entry for `.set` and the symbol-table API doesn't have an "insert with non-PC value" form. Would unblock `inst_movz_movk_sym.s` from M1.
- `.balign` / `.org` / `.skip` — would unblock `dir_align_skip.s` and `dir_skip_symbolic.s` from M1.
- Macros, conditional assembly.

These all carry forward as known gaps to the M5 plan.

## Hand-off recipe (verify locally)

```bash
# Inside the sam-aarch64 dev container or with toolchain locally:
make ci-m3 ci-m4
# expect:
#   9/9 M3 fixtures matched
#   4/4 M4 fixtures matched
```

## Authoritative references

- M4 spec: `docs/specs/2026-05-24-m4-symbols-multipass-design.md`
- M4 plan: `docs/plans/2026-05-24-m4-symbols-multipass.md`
- M3 status (prior milestone): `docs/notes/m3-status.md`
- M1 round-trip oracle (linked): `tests/m1/run-refenc-roundtrip.sh`
- ROADMAP: `docs/ROADMAP.md`
