# Spectrum4 ROM1 Assembly Pipeline — Status

Branch: `worktree-spectrum4-rom1`
Last updated: 2026-05-25

## Goal

Get `text2bin` + `refenc` to assemble
`~/git/spectrum4/src/spectrum4/roms/rom1_full.gen-s` (4665 lines) and
byte-match `aarch64-none-elf-as` → `ld -Ttext=0` → `objcopy -O binary`
for self-contained subsets.

---

## Current status

### text2bin (parse phase)

**PASSES 100% of rom1_full.gen-s** — no parse errors.

Changes required:
- Two-digit local labels (10:, 11: … 15:, and 10f/10b/11b etc.)
- `movl` pseudo-instruction (spectrum4-specific; expands to MOVZ + MOVK)

### refenc (encode phase)

**Fails only on undefined external symbols** (e.g. `CH_ADD`, `sysvars`,
`X_PTR`, `ERR_NR`, `BORDER_COLOUR`, etc.). These are defined in the
spectrum4 build system's target files, not in `rom1_full.gen-s` itself.

GNU assembler (`aarch64-none-elf-as`) fails identically on the same symbols
when given the gen-s standalone. The file is a "reference document" that is
not meant to be assembled in isolation.

**No encoding gaps** were found: every instruction form in rom1 that can be
resolved (i.e., no undefined symbols) encodes correctly and byte-matches GNU.

### Regression tests

- m1 roundtrip: **20/20** ✓
- spectrum4 snippets: **4/4** ✓ (add_ch_1, copy_buff, po_change, report_bb)

---

## Mnemonics added this session

All byte-matched against `aarch64-none-elf-as`.

| ID | Mnemonic | Notes |
|----|----------|-------|
| 42 | b.hs     | alias for b.cs (cond=2) |
| 43 | b.lo     | alias for b.cc (cond=3) |
| 44 | blr      | branch with link to register |
| 45 | subs     | subtract setting flags (full Rd form) |
| 46 | tst      | ANDS Rd=xzr, imm and register forms |
| 47 | bic      | bit clear; imm (AND ~mask) and reg (N=1) |
| 48 | adr      | PC-relative address, ±1MB raw offset |
| 49 | bfi      | bit field insert via BFM alias |
| 50 | bfxil    | bit field extract+insert low via BFM alias |
| 51 | ubfx     | unsigned bit field extract via UBFM alias |
| 52 | csetm    | conditional set mask = CSINV with !cond |
| 53 | movk     | move wide with keep; lsl #N parsed specially |
| 54 | ldrb     | load byte (size=00, scale=1) |
| 55 | strb     | store byte |
| 56 | ldrh     | load halfword (size=01, scale=2) |
| 57 | strh     | store halfword |
| 58 | madd     | multiply-add |
| 59 | msub     | multiply-subtract |
| 60 | umull    | unsigned multiply long = UMADDL Ra=xzr |
| 61 | umulh    | unsigned multiply high |
| 62 | umaddl   | unsigned multiply-add long |
| 63 | umsubl   | unsigned multiply-subtract long |
| 64 | movl     | spectrum4 pseudo: expand to MOVZ+MOVK |

---

## Encoder fixes (bugs found during rom1 work)

### LogicalImm nimmsTop formula (slots_logical.go)

The original formula `(0x3F << sizeLog2) & 0x3F` was computing the wrong
element-size marker. For size=32 (len=5) it produced nimmsTop=0x20, which
the decoder would interpret as size=16. The correct formula for the marker
bits (bits above position `len`) is:

```go
nimmsTop = ^uint32((1 << (sizeLog2 + 1)) - 1) & 0x3F
```

This gives 0 for size=32, 0x20 for size=16, 0x30 for size=8 etc., matching
GNU assembler output.

### LogicalImm is64 propagation (encode.go)

The `encodeSlot` function was always passing `is64=true` to `encodeLogicalImm`.
Fixed by deriving `is64 = (form.Pattern >> 31) & 1 == 1` from the form's sf
bit and threading it through `encodeSlot`.

### stp/ldp (3-operand memory pair) (pass2.go)

`encodeMemInst` assumed operand[1] was the memory operand. For `stp`/`ldp`
the memory is operand[2]. Added `encodePairInst` for the pair case.

### mov Xd, SP / mov SP, Xn (data.go)

`mov` form-table only had (Xreg, Xreg) and (Wreg, Wreg). Added 3 XregOrSp
forms to handle `mov x29, sp` and `mov sp, x0`.

---

## Outstanding gaps

### External symbols (fundamental limitation of gen-s standalone assembly)

`rom1_full.gen-s` references ~236 occurrences of externally-defined symbols
(sysvar offsets, colours, hardware addresses). These are provided by the
spectrum4 build system target files but not by the gen-s itself. Neither our
pipeline nor GNU assembler can resolve them in isolation.

To test full rom1 encoding: either inject a preamble `.set` block defining
all referenced symbols, or assemble individual self-contained snippets
(as the 4 existing spectrum4 fixture tests do).

### Instruction forms not yet covered

These appear rarely or not at all in rom1:
- `br Xn` (direct register branch, ID 11) — not tested
- `tbz`/`tbnz` (ID 22/23) — basic forms exist but not heavily tested
- `cmp` shifted-register 32-bit (only 64-bit CMP-shiftedReg is in data.go)
- `csel` / `csinc` 32-bit (only 64-bit forms in data.go)
- `lsl`/`lsr` register form (LSLV/LSRV instruction) — not yet supported
- `neg` (SUB Rd, XZR, Rm) — not yet in table
- `asr` — not yet in table
- `ror` — not yet in table

These would need to be added when the full rom (with symbol definitions)
is assembled and new errors appear.
