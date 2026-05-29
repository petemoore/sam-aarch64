# Z80 ↔ Go encoding/operator parity audit (M7 seed)

Date: 2026-05-29. Branch: `m7-z80-go-parity-audit`. Status: analysis only, no code change.

## Purpose

The Go toolchain is the authority: `tools/sam-aarch64-format/` (record/operand/expression format + tables), `tools/aarch64enc/` (instruction encoders — the binutils-equivalent), `tools/refenc/` (the reference assembler: pass1/pass2/eval/directives). The SAM-side Z80 assembler now lives flat under `src/`. M6 closed only what the spectrum4 `release.bin` byte-match needed, so the natural worry is that the Z80 side is a meaningful functional subset.

This document maps Go-supported vs Z80-implemented across instruction forms, expression operators, directives, and operand kinds, so a future agent can see the real remaining gap for M7. Every support / non-support claim is cited file:line.

## Headline finding

**Functional parity is essentially complete.** The Z80 side implements every expression operator, every directive, every operand kind, every encoder slot kind, and every special-case mnemonic encoder that the Go authority defines. The remaining gaps are about *scale, robustness, and untested operand-kind combinations* — not missing instructions. Three structural facts drive this:

1. The Z80 generic-form instruction encoder consumes `enctab.enc`, which is **generated from the same Go `Form` table** (`tools/enctab-gen/main.go:94-95` appends `enc.ManualForms()` + the MRA-derived `generatedForms`). So any mnemonic/operand-tuple the Go form table covers is, by construction, present in the Z80 form table too — there is no separate hand-maintained Z80 form list that could drift.
2. The Z80 encoder (`src/encoder.asm:111-149`) dispatches **all** of the Go `SlotKind`s (`tools/aarch64enc/types.go:15-36`).
3. The Z80 `try_mnemonic_intercept` (`src/intercepts.asm:24-449`) mirrors the Go pass2 special-case encoders 1:1 (`tools/refenc/pass2.go:296-324` + the shifted/extended-reg coercion and the mem/litpool/mov-imm paths).

So this is a *robustness/scale* audit more than a *missing-feature* audit. The genuinely open items are listed in §6.

---

## 1. Instruction / encoder coverage

### 1a. Encoder slot kinds (the per-operand encoders)

Go `SlotKind` set: `tools/aarch64enc/types.go:15-36`. Go dispatch: `tools/aarch64enc/encode.go:30-84`. Z80 dispatch: `src/encoder.asm:111-149`.

| SlotKind | value | Go encodes | Z80 encodes | Notes |
|----------|-------|-----------|-------------|-------|
| Xreg | 0x01 | yes (encode.go:32) | yes (encoder.asm:111 → `encode_slot_xreg`) | |
| Wreg | 0x02 | yes | yes (encoder.asm:113) | |
| XregOrSp | 0x03 | yes | yes (encoder.asm:115, shares xreg body) | |
| WregOrSp | 0x04 | yes | yes (encoder.asm:117, shares wreg body) | |
| Imm5 | 0x05 | yes (encode.go:35) | yes (encoder.asm:119 → `encode_slot_imm5`) | used by ccmp imm5/nzcv |
| Imm6 | 0x06 | yes | yes (encoder.asm:121) | |
| CondCode | 0x07 | yes (encode.go:37) | yes (encoder.asm:123) | |
| Imm12Shifted | 0x10 | yes (encode.go:38) | yes (encoder.asm:125) | add/sub/cmp/ldr-imm |
| Imm16Shifted | 0x11 | yes (encode.go:40-66) | yes (encoder.asm:127) | movz/movk/movn auto-shift |
| ShiftAmount | 0x12 | yes (encode.go:67) | yes (encoder.asm:129) | |
| ExtendOp | 0x13 | yes (encode.go:69) | yes (encoder.asm:131) | |
| BranchImm26 | 0x20 | yes (encode.go:71) | yes (encoder.asm:133) | b / bl |
| BranchImm19 | 0x21 | yes | yes (encoder.asm:135) | cbz/cbnz/b.cond |
| BranchImm14 | 0x22 | yes | yes (encoder.asm:137) | tbz/tbnz inner |
| AdrpImm | 0x23 | yes (encode.go:73) | yes (encoder.asm:139) | adrp |
| LogicalImm | 0x24 | yes (encode.go:77-79) | yes (encoder.asm:141 → `encode_slot_logimm`) | and/orr/eor-imm bitmask |
| BitfieldImm | 0x25 | dispatched via two-slot pairs only (encode.go:80-81) | handled by `encode_bitfield_word` intercept, not the slot loop (encoder.asm:142-147) | matches Go — both reject a bare 0x25 in the slot loop |
| AdrImm | 0x26 | yes (encode.go:75) | yes (encoder.asm:149) | adr |

**Slot-kind parity: complete.** Every Go slot kind has a Z80 dispatch target.

### 1b. Special-case mnemonic encoders (intercepts ahead of the form table)

Go pass2 intercepts the following mnemonic IDs before the generic form lookup (`tools/refenc/pass2.go:283-324`) plus the mem / mov-imm / ldr-litpool paths earlier in `encodeInst`. The Z80 `try_mnemonic_intercept` (`src/intercepts.asm`) mirrors each.

| Feature / mnemonics | Go | Z80 | Notes |
|---------------------|----|----|-------|
| tbz / tbnz (22, 23) | encodeTbzTbnz (pass2.go:293, 546) | yes (intercepts.asm:356-375 → `encode_tbz_word`) | |
| lsl / lsr (17, 18) | encodeLSLSR (pass2.go:300, 1196) | yes (intercepts.asm:307-328 → `encode_lslsr_word`) | UBFM-imm + LSLV/LSRV-reg |
| bitfield bfi/bfxil/ubfx/bfc/sbfx (49,50,51,83,84) | encodeBitfieldInst (pass2.go:302, 1277) | yes (intercepts.asm:330-354 → `encode_bitfield_word`) | |
| bic-imm (47, with #imm op) | encodeBicImm (pass2.go:304, 1383) | yes (intercepts.asm:70-96; negate then AND) | reg form goes via shifted-reg |
| csetm (52) | encodeCsetm (pass2.go:308, 1425) | yes (intercepts.asm:48-68; invert cond) | |
| barriers isb/dsb/dmb (66,67,68) | encodeBarrierInst (pass2.go:310, 1506) | yes (intercepts.asm:377-398 → `encode_barrier_word`) | |
| ror-imm (71, with #imm op) | encodeRorImm (pass2.go:312, 1460) | yes (intercepts.asm:32-46 → `encode_ror_imm_word`) | EXTR alias |
| mrs (76) | encodeMrs (pass2.go:314, 1544) | yes (intercepts.asm:420-424) | |
| msr (77) | encodeMsr (pass2.go:316, 1571) | yes (intercepts.asm:426-430) | reg + PSTATE-imm |
| dc (78) | encodeDc (pass2.go:318, 1616) | yes (intercepts.asm:432-436) | |
| tlbi (79) | encodeTlbi (pass2.go:320, 1642) | yes (intercepts.asm:438-442) | |
| shifted-reg family add/sub/and/orr/eor/subs/tst/bic/ands (1,2,14,15,16,45,46,47,80) | pass2.go:208-252 coercion + encodeShiftedRegInst (1005) | yes (intercepts.asm:134-206; `is_shifted_reg_mnemonic` at :523 is an exact port of pass2.go:1072-1078) | LSL #0 coercion mirrored (intercepts.asm:556 ← pass2.go:217-251) |
| extended-reg add/sub only (1,2) | encodeExtendedRegInst (pass2.go:1131); fields restricted to add/sub (pass2.go:1170-1176) | yes (intercepts.asm:166-179; `cp 1`/`cp 2` restriction at :171-174) | |
| mem family ldr/str/ldp/stp/ldrb/strb/ldrh/strh/stur/ldur/ldrsb/ldrsh/ldrsw | encodeMemInst / encodeUnscaledMemInst / encodePairInst (pass2.go:721/874/919) | yes (intercepts.asm:208-259 → `encode_mem_word` / `encode_pair_word`) | 11+ mnemonics, all MemShapes |
| ldr Xn/Wn, =expr (litpool) | encodeLdrLitPoolInst (pass2.go:601) | yes (intercepts.asm:261-283 → `litpool_encode_ldr_word`) | |
| ldr Xt/Wt, label (LDR-literal) | encodeLdrLitDirect (pass2.go:508) | yes (intercepts.asm:285-305 → `encode_ldr_lit_direct_word`) | |
| mov Rd, #imm (auto movz/movk/movn, incl. movl pseudo) | tryEncodeMovImm (pass2.go:438) | yes (intercepts.asm:98-132 → `encode_mov_imm_word`) | movl (ID 64) rides this path |

**Special-encoder parity: complete.** Every Go special-case is mirrored.

### 1c. Generic-form mnemonics (everything else)

The remaining mnemonics encode through the shared form table. Because the Z80 form table *is* the Go form table (§Headline fact 1), and all their slots use only the slot kinds enumerated in §1a, these encode identically on both sides. Spot-checked slot kinds for: csel/csinc (24,25 — Xreg/Wreg/CondCode, manual_forms.go:~), madd/msub (59,60), umull/umulh/umaddl/umsubl (61-64), mul/udiv/sxtw (72,73,74), movz/movn/movk (82,83,54), ccmp (88 — Wreg/Xreg/Imm5/Imm5/CondCode, manual_forms.go:640-666), adrp (13), b.cond family (26-44), cbz/cbnz (20,21), adr (48), cmp/subs (19,45), nop (0), ret/br/blr (12,11,44), eret/wfi (65,69). All use slot kinds in §1a.

**Mnemonic-table census.** Go `MnemonicTable` has 89 entries (IDs 0-88; `tools/sam-aarch64-format/mnemonics.go:23-120`). Of those, 21 have no form-table entry and are handled purely by special encoders — exactly the set in §1b (ldp/stp, tbz/tbnz, ldrb/strb/ldrh/strh, movl, isb/dsb/dmb, stur/ldur, mrs/msr/dc/tlbi, ldrsb/ldrsh/ldrsw). The other 68 IDs have ≥1 form. No mnemonic ID is orphaned (no form AND no special encoder).

**Instruction coverage verdict: full parity.** No Go-encodable mnemonic is unreachable on the Z80 side.

---

## 2. Expression operators

Go `ExprOp` set: `tools/sam-aarch64-format/expr.go:11-42`. Go evaluator: `tools/aarch64enc/expr.go:19-96` (Eval). Z80 evaluator: `src/expr_eval.asm` (opcode dispatch at lines 142-194; documented at 24-53).

| Operator / opcode | value | Go | Z80 | Notes |
|-------------------|-------|----|----|-------|
| PUSH_IMM8 | 0x01 | yes | yes (expr_eval.asm:142) | |
| PUSH_IMM16 | 0x02 | yes | yes (:144) | |
| PUSH_IMM32 | 0x03 | yes | yes (:146) | |
| PUSH_IMM64 | 0x04 | yes | yes (:148) | |
| PUSH_SYM | 0x05 | yes (expr.go:36) | yes (:150) | symbol resolve |
| PUSH_LOCAL | 0x06 | yes (expr.go:46) | yes (:152) | 1f/1b locals |
| PUSH_PC | 0x07 | yes (expr.go:61) | yes (:154) | `.` operator |
| ADD `+` | 0x10 | yes | yes (:156) | |
| SUB `-` | 0x11 | yes | yes (:158) | |
| MUL `*` | 0x12 | yes | yes (:160) | |
| DIV `/` | 0x13 | yes | yes (:162) | |
| AND `&` | 0x14 | yes | yes (:164) | |
| OR `\|` | 0x15 | yes | yes (:166) | |
| XOR `^` | 0x16 | yes | yes (:168) | |
| SHL `<<` | 0x17 | yes | yes (:170) | |
| SHR `>>` | 0x18 | yes | yes (:172) | |
| NEG (unary `-`) | 0x20 | yes (expr.go:72) | yes (:174) | |
| NOT (unary `~`) | 0x21 | yes (expr.go:74) | yes (:176) | |
| REL_LO12 (`:lo12:`) | 0x30 | yes (expr.go:76) | yes (:178) | |
| REL_HI12 | 0x31 | yes | yes (:180) | |
| REL_ABS_G0 | 0x32 | yes | yes (:182) | |
| REL_ABS_G0_NC | 0x33 | yes | yes (:184) | alias of G0 |
| REL_ABS_G1 | 0x34 | yes | yes (:186) | |
| REL_ABS_G1_NC | 0x35 | yes | yes (:188) | |
| REL_ABS_G2 | 0x36 | yes | yes (:190) | |
| REL_ABS_G2_NC | 0x37 | yes | yes (:192) | |
| REL_ABS_G3 | 0x38 | yes | yes (:194) | |

**Operator parity: complete (27/27 opcodes).** Note: the operator *set* lives in the bytecode the Mac-side text2bin parser emits; the Z80 is the consumer/evaluator. Any surface-syntax operator the parser could ever emit reduces to one of these 27 opcodes, and the Z80 evaluates all 27. One scale caveat: Z80 `EXPR_STACK_DEPTH = 8` (expr_eval.asm:81) vs Go's unbounded stack — see §6.

---

## 3. Directives

Go directive table: `tools/sam-aarch64-format/directives.go:4-31` (22 entries, IDs 0-21). Go pass1 sizing: `tools/refenc/pass1.go:186-246`. Go pass2 emit: `tools/refenc/pass2.go:1688-1773`. Z80 IDs: `src/main_loop.asm:67-88`. Z80 pass1: `:1288-1305`. Z80 pass2 emit: `:1307-1354`. Z80 sizing: `:1367-1414`.

| Directive | ID | Go | Z80 | Notes |
|-----------|----|----|----|-------|
| .text | 0 | yes | yes (no-op, main_loop.asm:1324) | |
| .data | 1 | yes | yes (no-op, :1326) | |
| .byte | 2 | yes | yes (:1310) | |
| .short | 3 | yes | yes (:1312) | |
| .word | 4 | yes | yes (:1316) | |
| .quad | 5 | yes | yes (:1318) | |
| .ascii | 6 | yes | yes (:1320) | |
| .asciz | 7 | yes | yes (:1322) | |
| .equ | 8 | yes | yes (pass1 SYMTAB insert, :1291) | |
| .set | 9 | yes | yes (:1293; .equ synonym) | |
| .global | 10 | yes (no-op) | yes (no-op, :1333) | |
| .balign | 11 | yes | yes (:1347) | |
| .org | 12 | yes | yes (pass1 sets PC, :1295/1351) | backward .org fails on both |
| .skip | 13 | yes | yes (:1341) | |
| .space | 14 | yes | yes (:1343; .skip synonym) | |
| .inst | 15 | yes | yes (:1345) | |
| .align | 16 | yes | yes (:1349) | 2^N bytes |
| .ltorg | 17 | yes | yes (:1353 → `main_dir_ltorg`) | pool flush |
| .section | 18 | yes (no-op) | yes (no-op, :1335) | single flat section both sides |
| .arch | 19 | yes (no-op) | yes (no-op, :1337) | |
| .cpu | 20 | yes (no-op) | yes (no-op, :1339) | |
| .hword | 21 | yes (= .short) | yes (:1314; routed to .short emit) | |

**Directive parity: complete (22/22).** Both sides treat `.section`/`.arch`/`.cpu`/`.global` as no-ops and both are single-flat-section. The known multi-section gap (`.section` is structurally a no-op) is a *shared* limitation, present on both sides — not a Z80-vs-Go gap.

---

## 4. Operand kinds

Go `OperandKind` set: `tools/sam-aarch64-format/operands.go:11-29` (0x01-0x0C). Z80 constants: `src/main_loop.asm:42-53`. Z80 instruction-operand parse dispatch: `:558-607`.

| OperandKind | value | Go (defined/read) | Z80 (parses as inst operand) | Notes |
|-------------|-------|-------------------|------------------------------|-------|
| REG_X | 0x01 | yes | yes (main_loop.asm:559 → `main_parse_reg`) | |
| REG_W | 0x02 | yes | yes (:561) | |
| REG_X_SP | 0x03 | yes | yes (:563) | |
| REG_W_SP | 0x04 | yes | yes (:565) | |
| IMM_EXPR | 0x05 | yes | yes (:567 → `main_parse_imm`) | |
| SHIFTED_REG | 0x06 | yes | yes (:571 → `main_parse_shifted_reg`) | |
| EXTENDED_REG | 0x07 | yes | yes (:573 → `main_parse_extended_reg`) | |
| MEM | 0x08 | yes | yes (:575 → `main_parse_mem`) | all 7 MemShapes (operands.go:64-72) parsed in slots/mem.asm |
| STRING | 0x09 | yes | intentional `jp fail` as inst operand (:591-592) | matches Go: text2bin only emits STRING in directive records (operands.go path; main_loop.asm:579-590 documents this). NOT a gap. |
| COND | 0x0A | yes | yes (:569 → `main_parse_cond`) | |
| SYS_NAME | 0x0B | yes | yes (:577 → `main_parse_sys_name`) | |
| LIT_POOL | 0x0C | yes | yes (:605 → `main_parse_litpool`) | |

**Operand-kind parity: complete.** 11 of 12 kinds parse as instruction operands; OpString as an instruction operand is a deliberate, Go-matching `fail` (STRING is directive-only on both sides). MEM sub-shapes (MemBase, MemBaseOff/Pre/Post, MemBaseIdx, MemBaseIdxShifted, MemBaseIdxExtended) are all decoded by the Go reader (operands.go:297-321) and the Z80 mem encoder (`src/slots/mem.asm`, routed from `encode_mem_word`).

---

## 5. System-register tables (the one real named-coverage subset)

Go authority: `tools/sam-aarch64-format/sysregs.go` — `namedSysRegs` plus `pstateFields` / `dcOps` / `tlbiOps` (88 grep-counted named entries). Z80: `src/sysreg_data.asm` (page-13 payload, ~86 `defb`/`defm` rows) + `src/sysname.asm` (lookup + generic parser).

The Z80 named-sysreg table is **intentionally a subset** of the Go table — it carries only the entries the M5/M6 fixtures exercise. This is documented and *guarded* by `tools/sam-aarch64-format/sysregs_z80sync_test.go`, which enforces "every entry present in `src/sysreg_data.asm` MUST appear in the Go map with a byte-identical encoding" (subset-direction invariant).

Mitigation: the Z80 has a generic `Sn_op1_Cm_Cn_op2` parser fallback (`src/sysname.asm:603-740`, reached at :391-394) that handles any system register expressible in the raw `S<n>_<op1>_C<m>_C<n>_<op2>` form without needing a named-table entry. So a named register missing from the Z80 table is only a real gap if assembly source uses its *symbolic* name (e.g. `TTBR0_EL1`) rather than the raw form, and that name isn't in `sysreg_data.asm`.

Note one path (`src/sysname.asm:421`) `fail`s on a miss with "no generic form" — likely PSTATE/DC/TLBI op names, which have no `Sn_` fallback. **verify**: enumerate which Go `namedSysRegs` / `pstateFields` / `dcOps` / `tlbiOps` entries are absent from `src/sysreg_data.asm`, and of those, which are reachable only by symbolic name (real gap) vs by `Sn_` form (covered). This needs a diff of the two tables, not done here.

---

## 6. Prioritised gap list (M7 seeds)

The functional surface is at parity. The genuine M7 risks are scale / robustness / coverage, ordered by likely impact:

1. **Sysreg named-table subset — SMALL/MEDIUM, LOW-MEDIUM risk.** §5. The Z80 named table is a subset of Go and guarded only in the subset direction. Real gap = symbolic sysreg names used in source but not in `src/sysreg_data.asm` and not expressible via `Sn_` form (e.g. some PSTATE/DC/TLBI ops, sysname.asm:421 `fail`). **Action:** diff the four Go sysreg maps against `sysreg_data.asm`; classify each missing entry as "Sn_-reachable" vs "symbolic-only"; add the symbolic-only ones if any release/test source needs them. **verify** the actual missing set.

2. **Fixed Z80 table sizes vs unbounded Go — MEDIUM, MEDIUM risk.** The `--dump-usage` instrumentation (`tools/refenc/usage.go`) exists precisely to size these. Hard Z80 caps: `EXPR_STACK_DEPTH = 8` (expr_eval.asm:81), SYMTAB 256 buckets + 128 overflow = 384 (usage.go:36), LOCAL_LABEL_TABLE 180 (usage.go:43), LITPOOL_TABLE 32 / LITPOOL_PC_MAP 32 (usage.go:421-comment), LITPOOL_EXPR_BUF 2 KB (usage.go:93), OPVAL_ARRAY 7 operands × 10 B (usage.go:96-100), STAGING_BUF 1 KB (usage.go:113). Each is fine for the release corpus but will overflow on a larger input. **Action:** run `refenc --dump-usage` over the intended M7 input set; raise any cap the peaks approach; add an explicit over-cap error path where one is missing (audit which caps fail-soft vs fail-hard).

3. **Untested operand-kind / mnemonic combinations — MEDIUM, MEDIUM risk.** Parity here is *structural* (shared form table + mirrored encoders), not *empirically tested* beyond the M3-M6 fixture corpus. Forms exist for many (mnemonic, operand-tuple) pairs that no committed fixture exercises (e.g. ccmp register form, the full umaddl/umsubl family, every b.cond alias, extended-reg with non-zero shift amount). A latent encoder bug in a code path the byte-match never hits would not be caught. **Action:** generate a broad fixture sweep (one fixture per form, or per (mnemonic, slot-tuple)) and run the existing Go-vs-SimCoupé byte-match harness over it. This converts "structural parity" into "verified parity."

4. **Multi-section / relocation model — SMALL gap, but SHARED — LOW risk for parity.** `.section`/`.text`/`.data` are no-ops on *both* sides; the toolchain is single flat section with no real relocations (REL_* opcodes mask/shift in place rather than emitting relocations). This is a Go-side limitation too, so it is not a Z80-vs-Go parity gap — but it is a real *toolchain* limitation worth noting for any M7 goal that needs separate sections or a linker.

5. **OpString as an instruction operand — NOT a gap.** Z80 `jp fail`s (main_loop.asm:591) by design; Go's text2bin never emits STRING in instruction records. Listed only to pre-empt a false positive in future audits.

### Items marked "verify" / uncertain

- §5 / gap #1: the *exact* set of Go sysreg entries missing from the Z80 table, and which are symbolic-only (real gap) vs `Sn_`-reachable (covered). Needs a table diff — not performed here.
- gap #2: which fixed-size tables fail-hard (clean error) vs fail-soft (silent corruption / wrap) on overflow. The dispatch sites were not individually audited for over-cap guards; the `usage.go` comments imply sizing intent but not the failure mode.
- gap #3: whether any *currently-defined form* has a latent Z80 encoder bug in an untested path. Structural parity says "should be fine"; only a fixture sweep proves it.

---

## Appendix: method

- Go form coverage enumerated from `tools/aarch64enc/data.go` (MRA-derived) + `tools/aarch64enc/manual_forms.go` (hand-curated), combined by `tools/enctab-gen/main.go:94-95` into `enctab.enc`.
- Mnemonic-ID ↔ name from `tools/sam-aarch64-format/mnemonics.go:23-120` (IDs 0-88).
- Cross-checked that every mnemonic ID is reachable by either a form-table entry or a special encoder; no orphans.
- Z80 slot dispatch read from `src/encoder.asm:111-149`; special encoders from `src/intercepts.asm:24-449`; directives from `src/main_loop.asm:1288-1414`; operand parsing from `src/main_loop.asm:558-607`; expression opcodes from `src/expr_eval.asm:142-194`.
- This is a breadth-first audit, not an exhaustive byte-level proof. The structural parity claim (shared form table + mirrored encoders) is strong; the empirical-coverage gap (#3) is the honest residual.
