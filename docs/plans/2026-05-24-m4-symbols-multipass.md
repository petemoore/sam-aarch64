# M4: Symbols + Multi-pass + Full Expression Evaluator — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Builds on M3; assumes M3 is merged.

**Goal:** Land the three pieces M3 deferred per `docs/specs/2026-05-24-m4-symbols-multipass-design.md`: a Z80 symbol-table data structure with hash-bucketed lookup, a two-pass record walker, and the full expression-bytecode evaluator (with `PUSH_SYM` / `PUSH_LOCAL` / `PUSH_PC` / `REL_*`). Together these unlock branches to labels, PC-relative addressing, local-label refs (`1f` / `1b`), and forward references.

**Architecture:** Refactor M3's single-pass walker into pass-mode-driven dual-pass over the same `.tbn` records. Add `src/m3/symbols.asm` (hash table) and `src/m3/local_labels.asm` (per-digit lists). Expand `src/m3/expr_eval.asm` with symbol-resolution and PC-relative cases. PC-relative slot encoders (`BranchImm*`, `AdrpImm`) gain a "subtract PC" pre-step before their existing bit layout.

**Tech Stack:** Same as M3 — pyz80, SimCoupé patched, SAMDOS, aarch64-none-elf-as oracle.

**Reference:** §N points at `docs/specs/2026-05-24-m4-symbols-multipass-design.md`.

---

## Conventions

- `g` not `git` for commits.
- TDD as in M3 — each piece gets a Layer 1 Z80-side unit-test block plus a Layer 3 round-trip fixture.
- Commit per task. Prefix: `m4: <subject>`.

## Sequence

### Task 1: Pass-mode flag in the record walker

Refactor `src/m3/reader.asm`'s top-level loop to accept a "pass" register (A or a known memory location). Add a stub for pass-2 behaviour (currently identical to pass-1 — that's deliberate; pass 2 diverges in Tasks 4–5).

Commit: `m4: thread pass-mode flag through record walker`.

### Task 2: Symbol table — insert + lookup

`src/m3/symbols.asm`. Hash-bucketed: 256 buckets indexed by `symbol_id mod 256`. Each entry: `(symbol_id u16, address u32, next u16)` chained within the bucket.

Subroutines:

- `symbol_insert` — given `symbol_id` + `address`, insert into the table. Errors on duplicate.
- `symbol_lookup` — given `symbol_id`, return resolved address or CF=1 for undefined.

Unit test: hard-code 5 insertions + 5 lookups in the assembler's startup self-test block.

Commit: `m4: symbol table — hash-bucketed insert + lookup`.

### Task 3: Local-label table

`src/m3/local_labels.asm`. Nine per-digit sorted PC lists; sorted by definition order (= PC order, since pass 1 walks records sequentially).

Subroutines:

- `local_def_append digit, pc` — append PC to digit's list.
- `local_find_forward digit, ref_pc` — return smallest PC > ref_pc in digit's list. CF=1 if none.
- `local_find_backward digit, ref_pc` — return largest PC ≤ ref_pc. CF=1 if none.

Unit test: build a digit-1 list of [4, 12, 24, 100]; test forward/backward lookups for ref_pcs at 0 / 12 / 50 / 200.

Commit: `m4: local-label table — forward/backward resolution`.

### Task 4: Pass 1 — symbol/local insertion

Walk records with pass-mode = 1. For `KindLabelDef`, call `symbol_insert(rec.symbol_id, current_pc)`. For `KindLocalDef`, call `local_def_append(rec.digit, current_pc)`. For `KindInst`, advance pc by 4. For `KindDirective`, advance by directive size (reuse M3's size table). Other kinds skip.

Pass 1 does NOT call the encoder; it just builds the tables.

Commit: `m4: pass 1 — build symbol + local-label tables`.

### Task 5: Pass 2 wired with symbol-resolving evaluator

Pass-mode = 2. Reset PC to 0. Re-walk records. For `KindInst`: invoke the encoder (M3's flow), but the expression evaluator now resolves symbols/locals/PC. For `KindDirective`: emit data bytes. Skip `KindLabelDef` / `KindLocalDef`.

The expression evaluator's new cases:

- `PUSH_SYM symbol_id`: call `symbol_lookup`; on CF, error.
- `PUSH_LOCAL digit, dir`: call `local_find_forward` or `local_find_backward`.
- `PUSH_PC`: push current PC (from the pass-2 PC counter).
- `REL_LO12` / `REL_HI12` / `REL_ABS_G0..G3` / `REL_ABS_G*_NC`: pop top, apply mask/shift per `aarch64enc/expr.go`, push result.

Commit: `m4: pass 2 — emit with symbol-resolving evaluator`.

### Task 6: PC-relative slot encoder updates

For slot kinds `BranchImm26` / `BranchImm19` / `BranchImm14`: subtract current PC from the operand value before applying the existing bit layout. For `AdrpImm`: subtract (current_pc & ~0xFFF) from (value & ~0xFFF).

These are 1-2 line tweaks inside each slot's Z80 subroutine — they consume the operand value, compute the byte offset, and dispatch to the existing range-check + bit-pack logic.

Commit: `m4: PC-relative slot encoders subtract PC before encoding`.

### Task 7: Expand M3 fixture corpus

Promote M1 fixtures that require symbol resolution:

- `inst_bcond.s` with a `target:` label and `b.lt target` branches.
- A `labels.s` variant with forward branches.
- `expr_pcrel.s` (adrp+:lo12:).
- `local_labels.s` (1f/1b chains).

For each fixture, regenerate the M1 Layer 2 golden if `bin2text` formatting drift surfaces.

Commit: `m4: expand fixture corpus — symbol-resolving fixtures`.

### Task 8: Layer 3 round-trip for the new fixtures

Run `tools/run-m3-roundtrip.sh` for each promoted fixture. Iterate any encoder bugs surfaced. Commit per fixture if needed.

Commit: `m4: Layer 3 round-trip passes for M4 corpus`.

### Task 9: Makefile + CI integration

Add `ci-m4` target, parallel to `ci-m3`. New CI job `m4`.

Commit: `m4: Makefile + CI integration`.

### Task 10: Status doc + declare done

`docs/notes/m4-status.md` patterned after `m3-status.md`. README update.

Commit: `docs: M4 complete`.

## What M4 explicitly does NOT include

- 64-bit MUL/DIV on Z80.
- `.balign` / `.org` / `.skip` / `.section`. **M5**.
- Macros, conditional assembly.
- On-SAM editor / TFTP.

## Self-review

- §1 goal/boundaries — Tasks 1-10 collectively.
- §2.1 pass split — Tasks 1, 4, 5.
- §2.2 symbol table — Task 2.
- §2.3 local label table — Task 3.
- §2.4 full evaluator — Task 5.
- §2.5 PC-rel encoders — Task 6.
- §3 test pyramid — Tasks 7, 8, 9.
- §4 fixture corpus — Task 7.
- §6 open items — addressed inline.
- §7 done criteria — Tasks 8 (round-trip), 9 (CI), 10 (declare).
