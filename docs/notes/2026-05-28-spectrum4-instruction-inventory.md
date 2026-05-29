# spectrum4 aarch64 instruction inventory vs sam-aarch64 support

2026-05-28. Snapshot taken from spectrum4 commit at `~/git/spectrum4`
(local working tree) and sam-aarch64 `main` at e36ea8a.

## Top line

| | count |
|---|---|
| Unique mnemonics used by spectrum4 (per all `.disassembly` outputs) | **76** |
| Mnemonics supported by sam-aarch64 (`MnemonicTable` in `tools/sam-aarch64-format/mnemonics.go`) | **88** |
| Gaps — in spectrum4 but not in sam-aarch64 | **4** |
| Release-blocking gaps | **1** (`ccmp`, already in flight) |

## How "supported" was computed

`tools/sam-aarch64-format/mnemonics.go` is the canonical mnemonic
table. A name in that table means refenc + text2bin can look it up;
encoding is then handled either by a Form (in
`tools/aarch64enc/data.go` / `manual_forms.go`) or by hard-coded
dispatch in `tools/refenc/pass2.go` (e.g. ldp/stp pair instructions,
ldrb/strb/ldrh/strh sized loads, mrs/msr/dc/tlbi system ops). Z80-side
support tracks Go-side automatically via `enctab.enc`.

Inventory recipe (spectrum4 side):

```
find ~/git/spectrum4 -name '*.disassembly' | xargs cat \
  | sed 's/^...........................\t//' | grep -v ':' \
  | sed 's/[[:space:]].*//' | sort -u | sed -n '/^[a-z]/p'
```

## Missing mnemonics

| mnemonic | description | source-side use? | release-blocking? | complexity | similar to existing |
|---|---|---|---|---|---|
| `ccmp` | Conditional compare (Rn vs Rm or imm5, set NZCV to `#nzcv` if cond false) | `kernel/macros.s` (`nzcv` macro), used by `tests/test_po_scr.*` | **No** — only test-side. Not in `release.disassembly`. | Medium | n/a (4-operand new shape: Rn, Rn|#imm5, #nzcv, cond) — **already in flight in worktree `agent-aa54678290f23211b`** |
| `adds` | Add setting flags (3-operand form, like `subs`) | **No** — appears in `.disassembly` only because objdump decoded data bytes (e.g. inside `msg_po_*` strings) as instructions | No | Low — mirror `subs` (ID 45) exactly, swap opc field | yes: `subs` already handled (refenc/pass2.go) |
| `stxrb` | Store-Exclusive Byte (atomic) | **No** — also only an objdump artefact of decoding string-table data bytes | No | Medium — introduces a new operand pattern (store-exclusive: `Ws, Wt, [Xn]`) not currently encoded | no direct analogue |
| `udf` | Permanently Undefined (issues UNDEFINED exception) | **No** — also only an objdump artefact (decode of zero-words inside string tables) | No | Low — single 16-bit imm operand, fixed `0x0000xxxx` encoding | no — but trivially small |

Notes:

- **adds, stxrb, udf are not real usage.** They appear in spectrum4's
  `.disassembly` files only because `objdump -d` disassembles bytes in
  the text section, including bytes that are actually string-table /
  message data (`msg_po_any_*` etc.) emitted by `.word` / `.byte`
  directives. Spectrum4's `.s` sources never use any of these three
  mnemonics. `release.disassembly` contains **zero** occurrences of
  all three. They would only need to be added if/when the SAM-side
  assembler is used to round-trip a pre-built disassembly, which is
  not a milestone goal.
- **Only ccmp is a real source-side mnemonic in spectrum4.** It is
  used by the `nzcv` macro in `kernel/macros.s` and referenced from
  `tests/test_po_scr.lowerscreen.gen-s` only. It does **not** appear
  in `release.disassembly`.

## Cross-check with ccmp work in flight

Worktree `agent-aa54678290f23211b` is adding `ccmp`:

- `tools/sam-aarch64-format/mnemonics.go`: appends `"ccmp"` (ID 88).
- `tools/aarch64enc/manual_forms.go`: 4 Forms (Wn/Xn × imm5/Rm), all
  bits-checked against ARM ARM C6.2.43 / C6.2.44.
- Tests added under `tests/m1/sources/inst_ccmp.s`,
  `tests/m1/golden/inst_ccmp.s`, `tests/m4/sources/inst_ccmp.s`.

The ccmp PR does **not** add `ccmn` (Conditional Compare Negative,
ARM ARM C6.2.41 / C6.2.42). `ccmn` shares the encoding skeleton with
`ccmp` modulo a single bit (op field at bit 30: 0 = CCMN, 1 = CCMP).
Adding `ccmn` alongside `ccmp` would be a near-trivial copy-paste of
the four Forms with a different pattern.

That said: **spectrum4 does not use `ccmn`** (no occurrences in any
`.disassembly` or `.s` file), so it is not on the critical path. Worth
adding for completeness if a follow-up PR is dispatched, but not
required.

## Intercepts review

The only mnemonic intercept currently in `src/intercepts.asm` that
affects a spectrum4-used mnemonic is `ror`-imm (PR #30, M5 PR-B). The
intercept fires only when operand 2 is `OpImmExpr`; register-form
`ror` falls through to the normal Form-table path (RORV alias of EXTR).

Spectrum4 uses both forms (`libextra/hex_x0.s`):

```
ror     x0, x0, x2     ; register form — Forms path
ror     x0, x0, #60    ; immediate form — intercept path
```

Both are exercised by existing M5 fixtures; no risk identified.

## Recommendation

1. **Land the in-flight `ccmp` PR** (worktree
   `agent-aa54678290f23211b`). This is the only real spectrum4-source
   gap. It only blocks the test-side targets that use the `nzcv`
   macro; `release.bin` does not depend on it.
2. **Optional follow-up: add `ccmn`** in the same PR or immediately
   after, since the encoding is one bit off from `ccmp`. Cheap
   insurance against future spectrum4 changes, even though current
   spectrum4 doesn't use it.
3. **Defer `adds`, `stxrb`, `udf`** indefinitely — they are not real
   usage, only artefacts of objdump decoding string-table data bytes
   as instructions. Add them only if we ever need round-trip
   disassembly support (out of scope for current milestones).

## Appendix: full lists

### spectrum4's 76 unique mnemonics

```
add adds adr adrp and ands b b.cc b.cs b.eq b.gt b.hi b.le b.ls b.lt b.ne b.pl
bfc bfi bfxil bic bl blr br cbnz cbz ccmp cmp csel csetm dc dmb dsb eor eret isb
ldp ldr ldrb ldrh ldrsh lsl lsr madd mov movk mrs msr msub mul mvn nop orr ret
ror stp str strb strh stur stxrb sub subs sxtw tbnz tbz tlbi tst ubfx udf udiv
umaddl umsubl umulh umull wfi
```

### sam-aarch64's 88 supported mnemonics (in ID order, current `main`)

```
0  nop         22 tbz         44 blr         66 isb         84 sbfx
1  add         23 tbnz        45 subs        67 dsb         85 ldrsb
2  sub         24 csel        46 tst         68 dmb         86 ldrsh
3  mov         25 csinc       47 bic         69 wfi         87 ldrsw
4  mvn         26 b.eq        48 adr         70 ror
5  ldr         27 b.ne        49 bfi         71 mul
6  str         28 b.cs        50 bfxil       72 udiv
7  ldp         29 b.cc        51 ubfx        73 sxtw
8  stp         30 b.mi        52 csetm       74 stur
9  b           31 b.pl        53 movk        75 ldur
10 bl          32 b.vs        54 ldrb        76 mrs
11 br          33 b.vc        55 strb        77 msr
12 ret         34 b.hi        56 ldrh        78 dc
13 adrp        35 b.ls        57 strh        79 tlbi
14 and         36 b.ge        58 madd        80 ands
15 orr         37 b.lt        59 msub        81 movz
16 eor         38 b.gt        60 umull       82 movn
17 lsl         39 b.le        61 umulh       83 bfc
18 lsr         40 b.al        62 umaddl
19 cmp         41 b.nv        63 umsubl
20 cbz         42 b.hs        64 movl
21 cbnz        43 b.lo        65 eret
```

Sixteen mnemonics in our table are not used by spectrum4 (mostly
b.cond aliases not emitted by GCC, plus `movl`, `movz`, `movn`,
`csinc`, `ldur`, `ldrsb`, `ldrsw`, `sbfx`). No action.
