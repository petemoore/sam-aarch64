# M5 spec: compound operand kinds + remaining directives

**Status**: drafted 2026-05-27 autonomously by Claude. Builds on
M4 (`2026-05-24-m4-symbols-multipass-design.md`).

## 1. Goal & boundaries

M4 unlocked branches, PC-relative addressing, and forward references — but the SAM-side encoder still rejects every M1 fixture whose instructions carry a compound operand (shifted register, extended register, memory addressing, system register, literal pool) or whose source uses any directive beyond the M3-era byte/short/word/quad/ascii/asciz set. M5 closes that gap.

After M5, the SAM-side assembler matches GNU `as + ld -Ttext=0 + objcopy` on every M1 fixture that doesn't require source larger than 2 KB (M6 territory).

**In scope for M5**:

- Six compound operand kinds:
  - `OpShiftedReg` (0x06) — `Rm, <shift> #amt` (`tools/sam-aarch64-format/operands.go:17`).
  - `OpExtendedReg` (0x07) — `Rm, <extend> #amt` (`operands.go:18`).
  - `OpMem` (0x08) with all 7 sub-shapes incl. pair, signed loads, unscaled fallback (`operands.go:19`, `operands.go:64-72`).
  - `OpString` (0x09) as an instruction operand — error-path only; no current fixture exercises it, but the dispatch must recognise it (`operands.go:20`).
  - `OpSysName` (0x0B) for `mrs` / `msr` / `dc` / `tlbi` (`operands.go:22`).
  - `OpLitPool` (0x0C) for `ldr Xn, =<expr>` paired with `.ltorg` flushing (`operands.go:23-28`).
- Remaining assembler directives:
  - `.set` / `.equ` (synonyms; `directives.go:8` ID 9, ID 8) — symbol with a non-PC value.
  - `.balign` / `.align` (`directives.go:13` ID 11, ID 16) — PC-aware padding, NOP-fill in `.text`.
  - `.org` (`directives.go:12` ID 12) — direct PC assignment; alias `. = expr` already parsed.
  - `.skip` / `.space` (`directives.go:13` IDs 13, 14) — N zero bytes.
  - `.inst` (ID 15) — one raw word.
  - `.ltorg` (ID 17) — flush the literal pool (paired with `OpLitPool`).
  - No-op acknowledgement of `.global` (10), `.section` (18), `.arch` (19), `.cpu` (20) — currently `jp fail`; M5 makes them size-0 emit-nothing.
- One mnemonic intercept: `ror Rd, Rs, #imm` (`tools/refenc/pass2.go:1460-1492` — the SAM-side hang root cause in `inst_alu_single.s`). ~30 LOC Z80 port; closes the last gap to promoting the full ALU-single fixture.
- Code-budget management: M4 ended at 6077 / 8192 B in the production variant (2115 B headroom). OpSysName's lookup tables and OpMem's encoder breadth are the main pressures; §2.5 documents which lever to pull at what point.

**Out of scope for M5** (each carries forward to its own milestone):

- Source > 2 KB — paged source loading is M6.
- Macros / conditional assembly — handled by Mac-side `text2bin` per `docs/specs/2026-05-25-macro-expansion-research.md`; never reaches SAM.
- On-SAM editor — Phase 2.
- TFTP shipping — Phase 3.
- Per-fail-site diagnostic strings — already deferred (separate from M5).
- 64-bit MUL / DIV on Z80 — still rejected, unchanged from M3 / M4.
- Compact `.tbn` format + built-in disassembler — moved to M6 (was previously labelled M5; see ROADMAP).

## 2. Architecture

### 2.1 New operand-kind handlers

Each entry below describes the parser dispatch point, what slot encoder fires (or direct emit), and where the 64-bit value flow runs.

**`OpShiftedReg`.** Text→bin already populated by `parser.go:982-998` — payload is `[0x06][width][reg][shift_kind][amt_expr_len u16][amt_expr]`. Mac-side reference `tools/refenc/pass2.go:1005-1067` (`encodeShiftedRegInst`) covers add/sub/and/orr/eor/subs/tst/bic/ands with layout `sf|opc|01011|shift|N|Rm|imm6|Rn|Rd`. M3-era 3-reg forms auto-coerce into shifted-with-zero-shift via `pass2.go:214-252`; the Z80 port mirrors that. No new slot kind — direct dispatch from `encodeInst` (`pass2.go:197`). LOC budget: ~120-160.

**`OpExtendedReg`.** Payload `[0x07][width][reg][extend][amt_expr_len u16][amt_expr]` (`operands.go:164`). Today only `add`/`sub` use it (`pass2.go:1131-1179`, `encodeExtendedRegInst`). Layout `sf|opc|01011|001|Rm|option(3)|imm3|Rn|Rd`. No new slot kind — M3's `ExtendOp` (0x13) is a separate, unrelated slot. LOC: ~80-110.

**`OpMem`** is the biggest single chunk. Payload is variable per shape (`operands.go:170-199`); the seven shapes are unscaled, scaled, pre-index, post-index, register, extended, and pair (`operands.go:64-72`). Mac-side encoder spans `pass2.go:721-988` (`encodeMemInst`) + `encodePairInst` for ldp/stp. It auto-promotes negative scaled offsets to STUR/LDUR (`pass2.go:776-779`). Mnemonics covered: ldr/str/ldrb/strb/ldrh/strh/ldur/stur/ldrsb/ldrsh/ldrsw + ldp/stp. No new slot kind — direct path. LOC: ~400-600 — split into sub-tasks (basic indexed → pair → extended → unscaled-fallback) for sanity.

**`OpString` as an instruction operand.** No current fixture exercises this and `parser.go:127-131` only routes `TokString` to directives. The M5 work is defensive: the inst-record dispatch must recognise `0x09` in the operand-kind byte and fail cleanly rather than wedging. ~10-30 LOC.

**`OpSysName` (mrs / msr / dc / tlbi).** Payload `[0x0B][len u16][bytes]`. Mac-side at `pass2.go:1544-1672` looks the name up in `sysregs.go`, OR (for unnamed regs) packs `(op0, op1, CRn, CRm, op2)` onto a per-mnemonic base. The dispatch bypasses the form table via mnemonic-ID intercepts (`pass2.go:316-324`). The Z80 port needs the lookup table itself (bulky — see §2.5) plus the four small encoder branches. LOC: ~250-400.

**`OpLitPool`** is the most architecturally novel. Payload `[0x0C][width][expr_len u16][expr]`; only `ldr <Xn|Wn>, =<expr>` produces it (`parser.go:922-968` `tryParseLdrLitPool`). Mac-side is two-phase: pass-1 (`refenc/pass1.go:140-159`) allocates pool slots, deduplicates via `poolKey`, tracks `LdrPoolIdx[pc]`, flushes at `.ltorg`/EOF. Pass-2 (`pass2.go:601-634` `encodeLdrLitPoolInst`) computes `imm19 = (pool_pc - pc)/4` and ORs onto `0x18000000` / `0x58000000` for Wn/Xn. Flush logic at `pass2.go:21-76`. No new slot kind — but a brand-new pass-1 mechanism (pool table, dedup, deferred layout). See §2.3. LOC: ~250-400.

### 2.2 Directive handlers

**`.set` / `.equ`.** Standard 2-operand directive (symref + value). Pass 1 size 0; the handler extracts PUSH_SYM id, evals expr, calls `symbol_insert(id, value)`. M4's evaluator already handles `.set`-chains in source order — value can be arbitrary u32 (the symbol table accepts arbitrary u32 already; `src/m3/symbols.asm:117`). Pass 2 emits nothing. No schema change. Reference: `refenc/pass1.go:166-170`, `:241-271` `resolveEquDirective`.

**`.balign` / `.align`.** Pass 1 size = `(N - pc%N) % N` — PC-dependent (`refenc/pass1.go:365-381`). Pass 2 emits `alignPadBytes(pc, pad)` (`pass2.go:1729-1746`, `:403-429`): NOP-fill in `.text`, zero elsewhere. M4's walker already passes PC through, so the only new mechanism is the pad helper. `.align N` is the `2^N` sibling — fold into the same handler.

**`.org ADDR`.** New walker mechanism. Today's walker does `PASS_PC += size`; `.org` needs `PASS_PC = target`. Mac-side has two modes — at start: `OriginVMA = target`; mid-stream: zero-fill `pc → target` (`refenc/pass1.go:194-201`, `pass2.go:102-128`). `OriginVMA` can be punted (spectrum4 doesn't use `.org` with a non-zero leading address). Backward `.org` errors.

**`.skip` / `.space`.** Pass 1 size = eval(expr); pass 2 emits N zeros (`refenc/pass1.go:337-345`, `pass2.go:1763-1770`). Same handler for both mnemonics (`.skip = ID 13, `.space` = ID 14). M4's evaluator already handles `.set`-symbols here.

**`.inst N`.** Emit one raw 32-bit word (`refenc/pass1.go:346-347`). ~20 LOC.

**`.global` / `.section` / `.arch` / `.cpu`.** No-ops on Mac side; today Z80 hits `jp fail`. M5 swaps the `jp fail` lines for size-0 emit-nothing entries. ~40 LOC total.

### 2.3 Literal pool

Two new data structures sit alongside the symbol table:

```
LITPOOL_TABLE:    array of (value u64, width u8) entries, packed.
LITPOOL_PC_MAP:   sparse map: instruction PC → pool slot index.
```

In pass 1, every `OpLitPool` operand:

1. Evaluates its value expression (already-defined symbols + constants resolve here; forward syms get registered as pending — but `ldr =<sym>` typically refers to known symbols by the time of evaluation, so the common case is closed-form).
2. Looks up `(value, width)` in `LITPOOL_TABLE`; on miss, appends a new entry.
3. Records `LITPOOL_PC_MAP[current_pc] = slot_index`.
4. Reserves 4 bytes at `current_pc` for the LDR opcode (the pool itself is allocated separately).

A `.ltorg` directive (or end-of-source) flushes the current pool:

1. Emit the entries (8-byte aligned for `Xn`; 4-byte for `Wn`).
2. Record each entry's PC in a parallel `LITPOOL_ENTRY_PCS` array.
3. Reset the table for the next pool segment.

Pass 2 computes `imm19 = (pool_entry_pc - inst_pc) / 4` and ORs into the LDR opcode (`0x18000000` for `ldr Wn, lit`; `0x58000000` for `ldr Xn, lit`).

The dedup key is `(value, width)` per `refenc/pool.go`. We mirror that exactly to keep byte-match parity.

### 2.4 The `inst_alu_single` ror-imm intercept

`inst_alu_single.s` currently hangs the SAM-side assembler on its two `ror w/x, _, #5` lines. Root cause: the form table can't express the EXTR alias's repeat (Rn appears at both bit 16 and bit 5). Mac-side handles it via a mnemonic-ID intercept `encodeRorImm` at `tools/refenc/pass2.go:1460-1492`:

```
// EXTR Rd, Rs, Rs, #imm  with Rn == Rm  IS  ROR Rd, Rs, #imm
sf := width == 64
op := 0x13800000 | (sf<<31) | (Rs<<16) | (imm6<<10) | (Rs<<5) | Rd
```

The Z80 port is ~30 LOC: a mnemonic intercept ahead of form lookup, identical structure to the existing `encodeInst` intercepts. Landing it under M5 closes the last fixture-promotion blocker for `inst_alu_single.s` — the other 10/12 lines work on M4 today. Verify by deleting only the ror-imm lines and re-running `ci-m4` before committing.

### 2.5 Code-budget management

M4 left `m3-asm-prod` at **6077 / 8192 B** (2115 B headroom). M5 chews into that headroom. The two levers, in order of preference:

1. **Move `ENCTAB_BUF` up from `&A000`.** Currently a 4 KB buffer at `&A000-&AFFF`; relocating to `&B000` or further frees 4 KB of contiguous code space at `&A000-&AFFF`. The catch: the buffer load path (HGTHD+HLOAD per PR #13) is hard-coded to `&A000`; the lever is "decide the new home + update the loader + update the memory-map table in m5-status.md".
2. **Carve a sysreg sidecar.** OpSysName's name tables are likely the bulkiest single payload. If the encoder code itself fits but the tables don't, load them into a paged region — the SAMDOS load idiom at `docs/specs/2026-05-27-samdos-load-idiom.md` covers the trampoline pattern.

**Trigger condition.** Don't pull a lever speculatively. Measure after each task that touches new code, and pull the first lever **when the production-variant build first crosses 7500 B** (≈690 B headroom — enough margin for the remaining encoders without iterating on the lever). If OpSysName alone pushes prod past 7500, lever 1 fires before OpSysName lands. If lever 1 isn't enough by the time OpSysName ships, lever 2 follows.

This is the same shape as M4's code-budget heads-up in `docs/notes/m4-status.md` — measure, defer until needed, then move.

## 3. Test pyramid

Same four layers as M3 / M4 — Layer 3 (Z80 vs refenc parity) remains the load-bearing oracle. Now exercises a much larger M1 fixture corpus (§4).

- **Layer 1** — Z80-side unit tests in the assembler's boot self-test block. Each new operand encoder (shifted-reg, extended-reg, the seven mem shapes, sysreg, pool) and each new directive (`.set`, `.balign`, `.org`, `.skip`, `.inst`, `.ltorg`) gets a self-test entry. The boot block continues to grow on the **test** variant only; the **production** variant stays measurably under 8192 B (§2.5).
- **Layer 2** — `bin2text` round-trip stays unchanged; M5 adds no new `.tbn` record shapes.
- **Layer 3** — Full pipeline (`.s → text2bin → SAM → HSAVE → byte-cmp vs GNU as + ld + objcopy`) on every promoted fixture. New jobs `ci-m5` (test variant) and `ci-m5-prod` (production variant), parallel to `ci-m4` / `ci-m4-prod`.
- **Layer 4** — Already-green M3 + M4 regressions remain green.

## 4. M5 fixture corpus

Candidates promoted from M1's `tests/m1/golden/`:

| Fixture | Operand kinds / directives exercised | Promote |
|---|---|---|
| `inst_shifted.s` | OpShiftedReg | yes |
| `inst_ands.s` (full) | OpShiftedReg + LogicalImm | yes |
| `inst_extended.s` | OpExtendedReg | yes |
| `inst_mem_indexed.s` | OpMem (indexed) | yes |
| `inst_mem_preindex.s` | OpMem (pre-index) | yes |
| `inst_mem_extended.s` | OpMem (extended) | yes |
| `inst_unscaled_mem.s` | OpMem (unscaled — stur/ldur) | yes |
| `inst_ldrs.s` | OpMem (signed loads) | yes |
| `inst_movz_movk_sym.s` | `.set` + symbol refs | yes |
| `inst_mrs_msr.s` | OpSysName (mrs/msr) | yes |
| `inst_dc_tlbi.s` | OpSysName (dc/tlbi) | yes |
| `inst_ldr_litpool.s` | OpLitPool basic | yes |
| `inst_ldr_litpool_ltorg.s` | OpLitPool + `.ltorg` flush | yes |
| `inst_alu_single.s` | bare ALU + (post-intercept) ror-imm | yes |
| `dir_align_skip.s` | `.balign` + `.skip` | yes |
| `dir_skip_symbolic.s` | `.skip` with symbolic value | yes |
| `dir_equ.s` | `.equ` | yes |
| `inst_ldr_litpool_local.s` | OpLitPool + local labels | covered by implication (lit pool + M4 local-labels already proven) — defer to M6 if not free |
| `inst_bfc_sbfx.s` | BitfieldImm extras | covered by implication (M4 BitfieldImm path) — promote only if a regression surfaces |

That's 17 directly promoted, 2 deferred / covered-by-implication. M5 done-criteria require **all 17 byte-match GNU end-to-end via SimCoupé**.

## 5. Implementation plan outline

Full per-task scope is in `docs/plans/2026-05-27-m5-compound-operands-directives.md`. The headings are:

1. Cheap directives: `.set` / `.equ`, `.skip` / `.space`, `.inst`, `.global` / `.section` / `.arch` / `.cpu` no-ops.
2. `.balign` / `.align` (PC-aware pad).
3. `.org` (new `PASS_PC = target` mechanism).
4. `ror`-imm encoder intercept.
5. `OpShiftedReg` encoder.
6. `OpExtendedReg` encoder.
7. `OpMem` encoder — split: indexed → pair → extended → unscaled → signed-loads.
8. `OpSysName` encoder + sysreg table.
9. `OpLitPool` pass-1 table + pass-2 patch + `.ltorg`.
10. `OpString`-as-inst-operand error path.
11. Code-budget lever (only if §2.5 trigger fires).
12. Fixture corpus + Layer 3 round-trip.
13. CI integration (`ci-m5`, `ci-m5-prod`, GH workflow jobs).
14. `docs/notes/m5-status.md` + ROADMAP flip.

## 6. Open items, risks, non-goals

### Open items resolved by the spec

1. **Should `ror`-imm ship in M5 or be punted?** Ship in M5 (§2.4). ~30 LOC, closes the `inst_alu_single.s` blocker entirely.
2. **`OpLitPool` pass-1 mechanism — table location?** Sits alongside SYMTAB / LOCAL_LABEL_TABLE in the `&C000-` scratch region; sizing is bounded by the fixture corpus's largest single pool (small).
3. **Code-budget lever trigger?** First task to push the production-variant build past 7500 B (§2.5).

### Risks

- **Code budget.** Sysreg tables alone could be 600+ bytes; the lever to pull is documented in §2.5. The risk isn't the lever itself — both options are well-understood — but the measure-and-defer discipline. The plan flags it after every task that touches new code.
- **Literal pool requires brand-new pass-1 mechanism.** Likely the second-biggest piece after OpMem. Dedup-key matching with the Mac side is load-bearing for byte-match parity.
- **OpSysName lookup-table size.** Could need lever 2 (paged sidecar). Defer the decision until the table actually lands.
- **OpMem shape coverage.** Seven shapes; getting any one wrong silently produces non-matching bytes on a different fixture than the one introducing the shape. The plan splits OpMem into one task per shape with its own Layer 3 fixture promotion to catch this.

### Non-goals

- 64-bit MUL/DIV on Z80 — still rejected.
- Source > 2 KB / paged source loading — M6.
- Macros / conditional assembly — Mac-side `text2bin`.
- On-SAM editor, TFTP — Phase 2 / Phase 3.
- Per-fail-site diagnostic strings — separate deferred follow-up.

## 7. Done criteria

1. The M5 fixture corpus byte-matches `aarch64-none-elf-as + ld -Ttext=0 + objcopy` end-to-end via Z80 on SimCoupé (17 fixtures).
2. Layer 3 refenc parity holds for every M5 fixture.
3. All six new operand kinds + remaining directives + ror-imm intercept exercised by at least one fixture.
4. `make ci-m5` and `make ci-m5-prod` green; GH workflow `m5` + `m5-prod` jobs green.
5. Production variant `m3-asm-prod` remains under 8192 B with the M5 lever (if any) applied.
6. `docs/notes/m5-status.md` written; ROADMAP updated.

---

## Beyond M5: M6 sketch (informal)

M6 starts to look like the Phase-1-done picture from `docs/specs/2026-05-09-phase1-assembler.md`:

- **Paged source loading.** Source > 2 KB. M5's IN buffer (`&B000-&B7FF`, 2 KB) is the choke point; the SAMDOS load idiom (`docs/specs/2026-05-27-samdos-load-idiom.md`) is the obvious mechanism.
- **Compact `.tbn` format + built-in disassembler.** Formerly labelled M5 in the ROADMAP; renumbered to M6 since the compound-operands work is the actual M4 → M5 critical path. Compact format unlocks a real `~/git/spectrum4` fixture in the assembler's working memory.
- **Real spectrum4 fixture round-tripping byte-identical via SAM** — the Phase-1 "done" line.

Beyond M6, Phase 2 (on-SAM editor) and Phase 3 (TFTP shipper) take over.
