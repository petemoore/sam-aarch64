# M5: Compound Operands + Remaining Directives — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Builds on M4; assumes M4 (PRs #21, #22, #23) is merged.

**Goal:** Land the six remaining operand kinds and the remaining directives per `docs/specs/2026-05-27-m5-compound-operands-directives-design.md`. After M5, the SAM-side assembler byte-matches GNU `as + ld -Ttext=0 + objcopy` on every M1 fixture that doesn't require source larger than 2 KB.

**Architecture:** Extend the M4 walker with new operand encoders (`OpShiftedReg`, `OpExtendedReg`, `OpMem` × 7 shapes, `OpSysName`, `OpLitPool`, `OpString`-error) and new directive handlers (`.set` / `.equ`, `.balign` / `.align`, `.org`, `.skip` / `.space`, `.inst`, `.ltorg`, plus `.global` / `.section` / `.arch` / `.cpu` no-ops). One mnemonic-ID intercept (`ror`-imm) closes the `inst_alu_single.s` blocker. New pass-1 mechanism for the literal pool; new walker mechanism for `.org` (PC-set rather than PC-add). Code-budget lever fires when production-variant build crosses 7500 B.

**Tech Stack:** Same as M3 / M4 — pyz80, SimCoupé patched, SAMDOS, aarch64-none-elf-as oracle.

**Reference:** §N points at `docs/specs/2026-05-27-m5-compound-operands-directives-design.md`.

---

## Conventions

- `g` not `git` for commits.
- TDD as in M3 / M4 — each new operand encoder and each new directive gets a Layer 1 Z80-side unit-test block plus a Layer 3 round-trip fixture.
- Commit per task. Prefix: `m5: <subject>`.
- After every task that adds new code, measure `make m3-asm-prod` size. Note the number in the commit message body. If size crosses 7500 B, schedule the §2.5 lever before continuing.

## Sequence

### Task 1: Cheap directives — `.set` / `.equ`, `.skip` / `.space`, `.inst`, no-op family

Land the directives that need no new walker mechanism, all together:

- `.set` (ID 9) / `.equ` (ID 8) — synonyms. Pass-1 handler extracts PUSH_SYM id, evals expr, calls `symbol_insert(id, value)`. Pass 1 size 0; pass 2 emits nothing. Reuse the M4 symbol table — it already accepts arbitrary u32 values (`src/symbols.asm:117`).
- `.skip` (ID 13) / `.space` (ID 14) — same handler. Pass 1 size = eval(expr); pass 2 emits N zero bytes. M4's evaluator handles `.set`-symbols in the operand.
- `.inst` (ID 15) — one 32-bit raw word. Pass 1 size 4; pass 2 emits the word LE.
- `.global` (ID 10), `.section` (ID 18), `.arch` (ID 19), `.cpu` (ID 20) — swap each `jp fail` for a size-0 emit-nothing handler.

Layer 1 unit tests: insert a `.equ FOO, 0x1234`, look up `FOO`; emit 16 bytes via `.skip 16`; emit a `0x12345678` via `.inst`. Promote `inst_movz_movk_sym.s`, `dir_equ.s`, `dir_skip_symbolic.s`, `dir_align_skip.s` (the skip half) for Layer 3 verification once Task 2 lands.

Commit: `m5: cheap directives — .set/.equ, .skip/.space, .inst, no-op family`.

### Task 2: `.balign` / `.align` — PC-aware pad

Pass-1 size = `(N - pc%N) % N` (PC-dependent). Pass-2 emits `alignPadBytes(pc, pad)`: NOP-fill in `.text`, zero elsewhere (we assume `.text` for now — section tracking is out of scope; mirror `refenc/pass2.go:1729-1746`). `.align N` is the `2^N` sibling — fold into the same handler with a different size formula.

Layer 1 unit test: a synthetic walk with `pass_pc = 6, .balign 8` should produce pad = 2. Layer 3: `dir_align_skip.s` promotes here.

Commit: `m5: .balign / .align — PC-aware NOP/zero pad`.

### Task 3: `.org ADDR` — direct PC assignment

New walker mechanism. Today the pass-1 walker does `PASS_PC += size`; `.org` needs `PASS_PC = target`. Pass 2 mirrors: when in `.text`-only flat layout, mid-stream `.org` zero-fills `pc → target` (`refenc/pass2.go:102-128`). Backward `.org` is an error.

`OriginVMA` (at-start `.org`) can be punted — spectrum4 doesn't use it, and the M5 fixture corpus doesn't either. If a fixture does land that needs it, surface in the Task 12 promotion step.

Layer 1 unit test: pass-1 walk with `.org 0x40` after a 0x10-byte stretch sets PASS_PC to 0x40; pass-2 emits 0x30 zero bytes between current pc and 0x40.

Commit: `m5: .org — direct PC assignment with mid-stream zero-fill`.

### Task 4: `ror`-imm encoder intercept

Port the Mac-side `encodeRorImm` (`tools/refenc/pass2.go:1460-1492`) to a ~30-LOC Z80 mnemonic-ID intercept ahead of form lookup. EXTR with Rn == Rm IS ROR-imm:

```
sf := width == 64
op := 0x13800000 | (sf<<31) | (Rs<<16) | (imm6<<10) | (Rs<<5) | Rd
```

Wire into `encodeInst` alongside any existing intercepts (M4 added intercepts for the M3 / M4 path; M5 adds this one).

Layer 1 unit test: `ror w0, w1, #5` → `0x13851021`-ish (verify exact bytes against the Mac encoder before committing). Layer 3: closes `inst_alu_single.s`.

Commit: `m5: ror Rd, Rs, #imm — encoder intercept (EXTR alias)`.

### Task 5: `OpShiftedReg` encoder

Operand-kind 0x06 with payload `[0x06][width][reg][shift_kind][amt_expr_len u16][amt_expr]` (`tools/sam-aarch64-format/operands.go:17`). Mac reference `tools/refenc/pass2.go:1005-1067` (`encodeShiftedRegInst`). Layout `sf|opc|01011|shift|N|Rm|imm6|Rn|Rd`. Covers add/sub/and/orr/eor/subs/tst/bic/ands. Handle the 3-reg auto-coercion (M3-era bare `add x0, x1, x2` is shifted-with-zero-shift; `pass2.go:214-252`).

Layer 1 unit test: `add x0, x1, x2, lsl #4` byte-checks against the Mac encoder. Layer 3: `inst_shifted.s` + the second half of `inst_ands.s` promote here.

Commit: `m5: OpShiftedReg encoder — add/sub/and/orr/eor/subs/tst/bic/ands`.

### Task 6: `OpExtendedReg` encoder

Operand-kind 0x07 with payload `[0x07][width][reg][extend][amt_expr_len u16][amt_expr]`. Mac reference `pass2.go:1131-1179`. Layout `sf|opc|01011|001|Rm|option(3)|imm3|Rn|Rd`. Only `add`/`sub` use it today.

Layer 1 unit test: `add x0, x1, w2, uxtw #2` byte-checks. Layer 3: `inst_extended.s` promotes here.

Commit: `m5: OpExtendedReg encoder — add/sub extended-register form`.

### Task 7: `OpMem` encoder — basic indexed + unscaled

Operand-kind 0x08, shapes: scaled, unscaled (`operands.go:64-72`). Mac reference `pass2.go:721-988` (`encodeMemInst`). The auto-promotion of negative scaled offsets to STUR/LDUR (`pass2.go:776-779`) is load-bearing for byte-match parity.

Layer 1 unit test: `ldr x0, [x1, #8]` (scaled); `stur x0, [x1, #-4]` (unscaled, both directly and via STR auto-promotion). Layer 3: `inst_mem_indexed.s`, `inst_unscaled_mem.s` promote here.

Commit: `m5: OpMem encoder (1/4) — indexed + unscaled shapes`.

### Task 8: `OpMem` encoder — pre/post-index + register-offset

Shapes: pre-index (`[Xn, #imm]!`), post-index (`[Xn], #imm`), register-offset (`[Xn, Xm]`). Same Mac reference.

Layer 1 unit test: one of each shape. Layer 3: `inst_mem_preindex.s` promotes here.

Commit: `m5: OpMem encoder (2/4) — pre-index, post-index, register-offset shapes`.

### Task 9: `OpMem` encoder — extended-offset + pair

Shapes: extended-offset (`[Xn, Wm, sxtw #2]`), pair (`ldp`/`stp` via `encodePairInst`, also under `OpMem`).

Layer 1 unit test: `ldr x0, [x1, w2, sxtw #3]`; `stp x0, x1, [sp, #-16]!`. Layer 3: `inst_mem_extended.s` promotes here.

Commit: `m5: OpMem encoder (3/4) — extended-offset + pair shapes`.

### Task 10: `OpMem` encoder — signed loads

Mnemonics `ldrsb` / `ldrsh` / `ldrsw`. Mac reference still inside `encodeMemInst`; the differentiator is the opc field.

Layer 1 unit test: `ldrsw x0, [x1]`. Layer 3: `inst_ldrs.s` promotes here.

Commit: `m5: OpMem encoder (4/4) — signed loads`.

### Task 11: `OpSysName` encoder + sysreg table

Operand-kind 0x0B with payload `[0x0B][len u16][bytes]`. Mac reference `pass2.go:1544-1672` (`encodeMrs`/`encodeMsr`/`encodeDc`/`encodeTlbi`). Lookup via `tools/refenc/sysregs.go` table or `(op0, op1, CRn, CRm, op2)` packing.

Port the table itself. It's bulky — if `make m3-asm-prod` crosses 7500 B at this task, schedule Task 12 (lever) before continuing. If the lever is enough, continue; if not, carve the sysreg table into a paged sidecar via the SAMDOS load idiom (`docs/specs/2026-05-27-samdos-load-idiom.md`).

The four encoder branches (mrs / msr-reg / dc / tlbi) are dispatched via mnemonic-ID intercepts ahead of the form table (mirror `pass2.go:316-324`).

Layer 1 unit test: `mrs x0, midr_el1`; `dc cvac, x0`; `tlbi alle1`. Layer 3: `inst_mrs_msr.s`, `inst_dc_tlbi.s` promote here.

Commit: `m5: OpSysName encoder — mrs / msr / dc / tlbi`.

### Task 12: Code-budget lever (only if §2.5 trigger fires)

Schedule this between earlier tasks if `m3-asm-prod` crosses 7500 B. The default lever is "move `ENCTAB_BUF` up from `&A000`":

1. Pick a new home (e.g. `&B800` shifts the IN buffer too — easier: load ENCTAB into a paged region and trampoline reads).
2. Update `src/loader.asm` HGTHD+HLOAD target.
3. Update the memory-map table in `docs/notes/m4-status.md` (the m5-status.md from Task 17 picks up the new layout).
4. Re-run `make ci-m4 ci-m4-prod` — regression check.

If lever 1 isn't enough, lever 2 is "paged sysreg sidecar" — apply during Task 11 itself.

Commit: `m5: code-budget lever — relocate ENCTAB_BUF` (or `m5: sysreg sidecar in paged region`).

### Task 13: `OpLitPool` — pass 1 table + pass 2 patch + `.ltorg`

Three pieces (one commit, since they're inseparable):

**Pass-1 table.** New data structures: `LITPOOL_TABLE` (value+width entries, dedupe key `(value, width)` per `refenc/pool.go`), `LITPOOL_PC_MAP` (sparse: inst_pc → slot index), `LITPOOL_ENTRY_PCS` (pool entry pc-after-flush). Each `OpLitPool` operand evaluates its value, looks up `(value, width)`, appends-on-miss, records `LITPOOL_PC_MAP[pc] = slot`, and reserves 4 bytes for the LDR opcode itself.

**`.ltorg` flush.** Emits the current pool's entries (4-byte aligned for Wn; 8-byte for Xn), records each entry's PC in `LITPOOL_ENTRY_PCS`, resets the working table. End-of-source flushes any pending pool implicitly.

**Pass-2 patch.** For each pc in `LITPOOL_PC_MAP`, look up the slot, fetch its entry pc, compute `imm19 = (entry_pc - pc) / 4`, OR onto `0x18000000` (Wn) or `0x58000000` (Xn) per width.

Layer 1 unit test: build a tiny pool with two distinct values; verify dedup hits on duplicates; flush; check imm19 maths. Layer 3: `inst_ldr_litpool.s`, `inst_ldr_litpool_ltorg.s` promote here. `inst_ldr_litpool_local.s` is covered-by-implication (M4's local-label table + this task's pool); promote opportunistically if the byte-match passes without further work.

Commit: `m5: OpLitPool — pass-1 dedup table, .ltorg flush, pass-2 imm19 patch`.

### Task 14: `OpString` as instruction operand — error path

Operand-kind 0x09. No current fixture uses this; the parser doesn't even route `TokString` through `parseOperand` (`parser.go:127-131`). The defensive change: when the inst-record dispatch sees a 0x09 operand-kind byte, it must fail cleanly with a recognisable error rather than wedging.

~10-30 LOC: add the case to the operand-kind switch, route to `fail`. No Layer 3 fixture (no source produces it today).

Commit: `m5: OpString-as-inst-operand — defensive error path`.

### Task 15: Expand fixture corpus

Promote the M5 fixtures from M1's `tests/m1/golden/`:

| Fixture | Promoted in task |
|---|---|
| `dir_equ.s` | 1 |
| `dir_skip_symbolic.s` | 1 |
| `inst_movz_movk_sym.s` | 1 |
| `dir_align_skip.s` | 2 |
| `inst_alu_single.s` | 4 |
| `inst_shifted.s` | 5 |
| `inst_ands.s` (full) | 5 |
| `inst_extended.s` | 6 |
| `inst_mem_indexed.s` | 7 |
| `inst_unscaled_mem.s` | 7 |
| `inst_mem_preindex.s` | 8 |
| `inst_mem_extended.s` | 9 |
| `inst_ldrs.s` | 10 |
| `inst_mrs_msr.s` | 11 |
| `inst_dc_tlbi.s` | 11 |
| `inst_ldr_litpool.s` | 13 |
| `inst_ldr_litpool_ltorg.s` | 13 |
| `inst_ldr_litpool_local.s` | 13 (opportunistic) |

For each fixture, regenerate the M1 Layer 2 golden if `bin2text` formatting drift surfaces. Each fixture promotion happens inside the task that adds the relevant encoder — this consolidation task is the audit: count promoted fixtures, confirm none missed.

Commit: `m5: expand fixture corpus — M5 promotions audit`.

### Task 16: Layer 3 round-trip — `tools/run-m5-roundtrip.sh`

Pattern after `tools/run-m4-roundtrip.sh`. Same oracle (`as + ld -Ttext=0 + objcopy`). One commit covering the script + any per-fixture encoder bugs surfaced.

Commit: `m5: Layer 3 round-trip passes for M5 corpus`.

### Task 17: Makefile + CI integration

Add `ci-m5` and `ci-m5-prod` targets, parallel to `ci-m4` / `ci-m4-prod`. New CI jobs `m5` and `m5-prod` in `.github/workflows/ci.yml`.

Commit: `m5: Makefile + CI integration (ci-m5, ci-m5-prod)`.

### Task 18: Status doc + ROADMAP flip + declare done

`docs/notes/m5-status.md` patterned after `m4-status.md`:

- Tasks table.
- Test status table.
- M5 fixture corpus table with what each exercises.
- Memory layout (post-lever, if applicable).
- Code budget heads-up (post-M5 production size + remaining headroom).
- What's NOT in M5 scope (M6 hand-off).
- Hand-off recipe (`make ci-m3 ci-m4 ci-m5`).
- Authoritative references.

ROADMAP touch-ups in `docs/ROADMAP.md`:

- Flip M5 row state from `📋 designed` to `✅ done`.
- Add the closure note to the achievements list.
- Mark "M1 fixtures not yet promotable to M4" in the deferred-work checklist as ☑ done.

README update if M5 changes the headline status.

Commit: `docs: M5 complete`.

## What M5 explicitly does NOT include

- Source > 2 KB / paged source loading — **M6**.
- Compact `.tbn` format + built-in disassembler — **M6** (previously labelled M5, renumbered).
- 64-bit MUL/DIV on Z80.
- Macros, conditional assembly.
- On-SAM editor / TFTP.
- Per-fail-site diagnostic strings.

## Self-review

- §1 goal/boundaries — Tasks 1-18 collectively.
- §2.1 operand-kind handlers — Tasks 5 (ShiftedReg), 6 (ExtendedReg), 7-10 (Mem), 11 (SysName), 13 (LitPool), 14 (String-error).
- §2.2 directive handlers — Tasks 1 (cheap set), 2 (balign), 3 (org).
- §2.3 literal pool — Task 13.
- §2.4 ror-imm intercept — Task 4.
- §2.5 code-budget lever — Task 12 (conditional).
- §3 test pyramid — Layer 1 inside each operand/directive task; Layer 3 via Tasks 15, 16.
- §4 fixture corpus — Tasks 1-13 (promotions) + Task 15 (audit).
- §6 open items / risks — addressed inline.
- §7 done criteria — Tasks 16 (round-trip), 17 (CI), 18 (declare).
