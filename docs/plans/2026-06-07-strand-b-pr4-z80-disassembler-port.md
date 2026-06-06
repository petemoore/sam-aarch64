# Strand-B PR-4 — Z80 disassembler port (plan + progress)

Porting the Go disassembler `tools/aarch64dec/` into the on-SAM Z80
disassembler `src/disasm.asm`, family-by-family, test-first. This doc is
the live worklist; it is updated as families land.

## Goal

`src/disasm.asm` decodes a 32-bit aarch64 word to objdump-canonical
text, equivalent to `aarch64dec.DecodeAt`. The Go side is the authority
and already round-trips `release.s` (the two gates in
`tools/run-disasm-roundtrip.sh`); the Z80 side must reach the same
output so it can drive the on-SAM editor's bytes→text and a future
on-SAM round-trip.

## Verification — TDD, two layers

Docker/SimCoupé are not on the dev box; the boot path is gated by
SimCoupé in CI. Local feedback comes from two emulator-driven Go tests
in `tools/z80-test-harness-go/disasm_oracle_test.go` (the harness is a
dev tool, not a CI gate — it never runs in CI):

1. **`TestDisasmOracle` — per-word equivalence (the inner loop).**
   Loads `build/disasm.bin` into a bare koron-go/z80 flat-RAM emulator
   (disasm.asm is self-contained: it executes only its own code and the
   section-B comm buffer, so no paging/ROM is needed). For every 4-byte
   word of `tests/m6/release/release.img` (5438 words, pc = byte offset)
   it calls `disasm_entry` and compares `(mnem, operands)` against
   `aarch64dec.DecodeAt`. Prints the match ratio + a per-Go-mnemonic
   mismatch breakdown (the worklist). Enforces a **ratchet floor**
   (`matchFloor`) — green on main throughout, fails only on regression;
   raise the floor as each family lands. **This is strictly stronger
   than the two release round-trip gates**: if every word's Z80 disasm
   equals Go's, both round-trips reproduce identically to Go's (which
   pass), so per-word equivalence ⟹ round-trip equivalence.

2. **`TestDisasmSelfTest` — boot self-test guard.** Runs
   `run_disasm_self_test` (the on-page boot self-test reached via the
   `&8003` jump-table slot) standalone and asserts `BC=0`. This is the
   only *local* check of the routine that, at SAM boot under
   BUILD_TESTS, halts the assembler in SimCoupé CI on a non-zero
   fail-tag. Keep its fixtures in lock-step with the ported families.

**~~Also planned (Pete's explicit ask): mirror the two named round-trip
gates against the Z80 code.~~ CANCELLED / WONTFIX (Pete, 2026-06-08) —
no value.** The idea was a `z80disasm -asm` CLI that runs `disasm.bin` in
the emulator and emits `aarch64dec -asm`-identical text, then runs
`run-disasm-roundtrip.sh` `[2b/3]` and `[2c/3]` through it. Dropped
because it carries **no additional signal**: `TestDisasmOracle` already
proves per-word equivalence to the Go decoder over all 5438 release
words (oracle 100%), and per-word equivalence ⟹ round-trip equivalence
(if every word's Z80 disasm equals Go's, and Go's `-asm` round-trips
pass, the Z80-driven round-trips reproduce identically by construction).
The mirror would only re-confirm, end-to-end, what the oracle already
guarantees — pure duplication, not worth the CLI + glue.

## disasm.asm architecture (established in this PR)

- **Page-top jump table** gives fixed addresses with zero padding:
  `&8000 jp disasm_entry` (DISASM_ENTRY), `&8003 jp run_disasm_self_test`
  (DISASM_SELF_TEST_ENTRY). Decoder body + self-test grow freely after.
- `disasm_entry` holds the word in **B,C,D,E** (B=31:24 … E=7:0); IX/BC
  preserved per the `paged_call` ABI.
- **Dispatch chain** mirrors `aarch64dec.DecodeAt` order; each family
  either handles the word or falls through; the chain ends at the
  `.inst 0xNNNNNNNN` default (always correct, never wrong).
- Working scratch + embedded tables live **in this page** (always
  mapped; section D is unavailable under HMPR=15). Shared helpers:
  `disasm_emit_hex_byte`, `disasm_emit_dec16`, `disasm_mw_emit_hexbuf`
  (minimal-width LE hex), register-name emitter.
- pyz80: strings are `defm "…"` + `defb 0`.

### Dispatch-order invariant

The final chain order must match `DecodeAt`: mem → sys → testbranch →
udf → alias(move-wide, bitfield, addsub-imm, logical-imm, dpreg-alias,
condsel, mul3, shift-var, movk, extr) → (form-walk territory) → dpreg.
Porting out of order is fine for TDD (a too-early family that claims a
word another family should own just shows as an oracle mismatch, never
a silent wrong-green, because the oracle compares against Go's full
decode) — but insert each new family at its correct position so the
chain converges to Go's order at 100%.

### PC-relative families need an ABI extension

`b`/`bl`/`b.cc`/`tbz`/`tbnz`/`adr`/`adrp` render absolute targets
(`DecodeAt(pc, word)`). The Z80 ABI currently passes only the word
(BC:IX). Porting these requires passing `pc` (a section-B staging slot
or a preserved register); the oracle test already supplies pc to the Go
side per word, so the Z80 side must accept it to match. Plan this
before the branch/adr/adrp families.

## Progress (oracle match ratio)

| Increment | Families | Ratio | Δ |
|---|---|---|---|
| stub (start) | nop + `.inst` | 27.2% (1478) | — |
| this PR | + udf | 35.7% (1942) | +464 |
| this PR | + move-wide (movz/movn/movk/mov) | **41.4% (2254)** | +312 |

`matchFloor` = 2254.

## Remaining worklist (by mismatch count, biggest first — port next)

From the breakdown at 41.4%: `mov` 159 (ORR/add-sp aliases),
`stp` 358, `str` 258, `bl` 209, `adr` 181, `add` 170, `ldr` 165,
`ldrb` 132, `ldp` 129, `ret` 125, `strb` 109, `adrp` 105, `cmp` 89,
`orr` 67, `sub` 64, `mrs` 54, `and` 53, `b.ne` 42, … The load/store
families (`stp`/`str`/`ldr`/`ldp`/`ldrb`/`strb` ≈ 1150) and branches
(`bl`/`b`/`b.cc` ≈ 380, need the pc ABI) are the largest blocks.
Suggested next, non-PC families first: the rest of `decodeAlias`
(add/sub-imm → `cmp`/`cmn`/`mov sp`; logical-imm → `and`/`orr`/`tst`/
`mov`; dpreg-alias; condsel), then `decodeMem` (load/store), then
`decodeSys` (`mrs`/`msr`/barriers — needs the shared
`src/sysreg_names.inc`), then the PC-relative set (add the pc ABI), then
`decodeDPReg`. Each family: read the Go source, port, drive its oracle
mismatches to 0, raise `matchFloor`, add boot self-test fixtures.

## Notes carried forward

- `decodeBitMasks` non-canonical `immr ≥ esize` rejection (the
  `slots_logical.go` fix) must be ported when logical-imm lands; words
  like `0x32200013` must fall through to `.inst` (deferred backlog item
  in `m7-status.md`).
- Sysreg names: embed via the shared `src/sysreg_names.inc`
  (page-placement design §6 decision 2) so page-13 and page-15 stay in
  sync by construction.

## Future enhancements (deferred, possible — not scheduled)

- **Sysreg-table source de-dup (page-13 ↔ page-15).** Today the four
  name↔encoding tables (`sysreg_table` / `pstate_table` / `dc_table` /
  `tlbi_table`) exist byte-identically in both `src/sysreg_data.asm`
  (page 13, name→encoding matcher) and `src/sysreg_names.inc` (page 15,
  encoding→name). De-dup: extract the four labelled `defb`/`defm` blocks
  into one shared `src/sysreg_tables.inc`, have both pages `include` it.
  The `sysreg-sync` guard (`tools/sam-aarch64-format/sysregs_z80sync_test.go`)
  parses *only* those four table sections — nothing else from the parent —
  so it does **not** need to learn to follow `include`s (the
  `sysreg_names.inc` header comment overstates this): just repoint its
  `asmPath` at the shared `.inc` and keep the four label names. Faithful
  by construction — the guard still reads the exact bytes both pages
  embed. (Pete, 2026-06-08 — corrects the deferred-design note that
  assumed include-walking was required.)

- **LDIR-fan-out for blocks shared across pages (Pete, 2026-06-08).** For
  any data block that must be resident in more than one page (sysreg
  tables are the archetype, but the pattern is general), an alternative to
  embedding/`HLOAD`ing the block once per page is to store it **once** in
  the binary, load it once from disk, then `LDIR`-copy it into each
  consumer page at startup. Two benefits: (1) **smaller on-disk binary**
  (block stored once, not N times); (2) **faster boot** (one disk load,
  not N — and an LMPR/HMPR-window `LDIR` is microseconds vs. a
  millisecond-scale `HLOAD`). The copy is a **one-time** startup cost, not
  per-use, so it nets positive. Caveats: it does **not** save RAM (the
  bytes still occupy every consumer page at runtime), and it adds a boot
  step + indirection that costs readability — likely an over-optimisation
  for the table sizes we have today. Left here as a possible future
  enhancement; worth an audit of which payload/`.inc` blocks are actually
  loaded into >1 page before pursuing.
