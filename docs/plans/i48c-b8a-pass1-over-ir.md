# i48c-b8a — Z80 Pass1-over-IR (implementation plan)

**Goal.** Port the host `assemble.Pass1` (`tools/sam-aarch64/assemble/pass1.go`) to
walk the i48c encoder's **in-memory IR record stream** (the output of
`src/asmparse.asm parse_run`) and compute, for the fixture corpus:

- each record's output **PC**;
- the absolute **offset of every `LABEL_DEF`** (name→PC) and **`LOCAL_DEF`** (digit→PC);
- **`.set`/`.equ`** symbol values;
- **literal-pool placement** (`.ltorg`/section-end flushes, dedup, 4-then-8-byte
  width partitioning with alignment padding).

This is the foundation b8b (Compact) and b8c (serialize) build on. Go authority:
`tools/sam-aarch64/assemble/pass1.go` (515 lines) — port faithfully (CLAUDE.md §6).

## Strategy — Option A: thin IR-walk reusing existing pass1 machinery

The SAM already has a working pass1, but it consumes the **compact v2** format
(header tables + INSN_RUN), reading label/local definitions from the header
tables rather than walking them. b8a reuses the per-record PC/sizing/litpool/
symbol machinery and adds only: (1) an IR record decoder, (2) inline
`LABEL_DEF`/`LOCAL_DEF` position capture, (3) INST PC advance + litpool-operand
detection.

### IR record stream (input — from `parse_run`, `PARSE_RECS`)

Self-describing; each record begins with a `REC_KIND_*` tag:
- `REC_KIND_INST` (0x01): `[kind][mnem_id:2 LE][op_count:1][ops_len:2 LE][ops]`
- `REC_KIND_LABEL_DEF` (0x02): `[kind][len:2 LE][sym_id:2 LE]` (len=2)
- `REC_KIND_LOCAL_DEF` (0x03): `[kind][len:2 LE][digit:1]` (len=1)
- `REC_KIND_DIRECTIVE` (0x04): `[kind][len:2 LE][dir_id:1][op_count:1][ops]`
- `REC_KIND_COMMENT` (0x05) / `REC_KIND_BLANK_RUN` (0x06): `[kind][len:2 LE][payload]` — skip (no PC)

Operand bytes are `format.OperandWriter` form: register `[OP_KIND_REG_*][reg]`;
immediate `[OP_KIND_IMM_EXPR][expr_len:2 LE][expr]`; litpool
`[OP_KIND_LIT_POOL][width:1][expr_len:2 LE][expr]` (see `src/asmparse.asm` b5f
`emit_litpool_operand` for the exact bytes).

### Reuse targets (verify each calling convention by reading the source first)

- PC advance: `pass_pc_advance_4`, `pass_pc_advance_de`, `pass_pc_reset` (`src/main_loop.asm`).
- Directive sizing: `compute_directive_size` (`src/main_loop.asm`, dispatches on `dir_id`).
- Directive pass1 special-cases (`.equ`/`.set`/`.org`/`.ltorg`): `main_handle_directive_pass1` (`src/main_loop.asm`).
- Symbol insert: `symbol_insert` (`src/symbols.asm`); local append: `local_def_append` (`src/local_labels.asm`).
- Litpool: `litpool_init`, `litpool_register`, flush at `.ltorg`/end (`src/litpool.asm`).

The DIRECTIVE record wire format is **identical** between the IR and the compact
stream (same `dir_id`/`op_count`/operands), so the directive handlers reuse
as-is once the record payload is staged where they expect it (check how the
compact walk stages a record into `STAGING_BUF` before calling a handler, and
mirror that).

### New code

A new entry point (e.g. `pass1_ir_walk`, in a new `src/pass1_ir.asm` or folded
into the harness build) that:
1. loops over the IR buffer reading `[kind][len][payload]` (INST has its richer header);
2. dispatches per kind:
   - **INST** → `pass_pc_advance_4`; scan operands for `OP_KIND_LIT_POOL` and
     call `litpool_register` (mirror host Pass1's `litPoolOperand` + pool logic);
   - **LABEL_DEF** → capture `sym_id`→current PC via `symbol_insert`;
   - **LOCAL_DEF** → capture `digit`→current PC via `local_def_append`;
   - **DIRECTIVE** → stage payload, call `main_handle_directive_pass1` /
     `compute_directive_size` exactly as the compact walk does;
   - **COMMENT/BLANK_RUN** → skip;
3. flushes the pending literal pool at end-of-input (mirror the compact walk's
   end-of-records flush).

### Harness / build

The reusable handlers live in the full-assembler source, so the b8a harness
build must include that machinery (study the existing `asmparse-z80` /
full-assembler build targets in `Makefile` and how `tools/netboot-oracle/z80`
loads a built `.bin`+`.map`). Add a flat-memory harness test (closest pattern:
`tools/netboot-oracle/z80/asmparse_test.go` `loadAsmparse`/`CallEntry`/`Sym`)
that: feeds an IR record buffer (produce it via the Go front-end or
`parse_run`), runs `pass1_ir_walk`, reads back the resolved symbol/local
position tables + litpool table, and asserts they match host `assemble.Pass1`
over a set of fixtures (start with `tests/core/sources/*.s`, expand toward the
corpus). If the build integration proves a larger blocker than expected, STOP
and report rather than hacking around it.

## Done criterion

The Z80 pass1-over-IR resolves label/local positions, `.set` values, and
literal-pool slot PCs **byte-identically to host `assemble.Pass1`** over the
fixture set, verified in the koron-go/z80 harness. Delete this plan in the
completing PR.
