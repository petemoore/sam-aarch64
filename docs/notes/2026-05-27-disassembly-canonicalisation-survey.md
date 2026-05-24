# Canonical-alias transformation survey

Empirical survey of aarch64 alias-disassembly transformations applied to
the **spectrum4** corpus. Answers the M6 compact-`.tbn` question: if we
store assembled 4-byte machine code and disassemble with `objdump`-style
alias resolution, how often does the user-visible mnemonic differ from
what was originally typed?

## Method

- Flattened every `.s` entry point under `~/git/spectrum4/src/spectrum4/`
  via `tools/flatten-s` (release.target + debug.target + every
  `tests/test_*.runner`). 73 entry points → 4.5M flat lines.
- Stripped block comments, `//` line comments, macro definitions, macro
  invocations (33 known macros: `_strb`, `ldrxi`, `ventry`, ...),
  labels, directives, blank lines.
- De-duped → **8 885 unique concrete instructions** at the text level,
  **6 765 unique templates** (register numbers replaced with `xN`/`wN`,
  immediate literals with `#IMM`).
- Built `/tmp/canonical_survey.s` with one instruction per line, plus
  `.set` declarations for every symbol referenced (~5 378 names set to 0)
  and local labels `1:`–`99:` interleaved so `1f`/`1b` references resolve.
- Assembled with `aarch64-none-elf-as`, linked with `ld -Ttext=0`,
  disassembled twice — `objdump -d` (alias-preferring) and
  `objdump -d -M no-aliases`.
- Walked input vs disasm pairwise (each instruction occupies exactly
  4 bytes; inter-instruction local labels are zero-sized).
- An instruction is **alias-triggered** iff `alias_out != noalias_out`.
- An instruction is **mnemonic-changing** iff `input_mnemonic != alias_out_mnemonic`.

Toolchain: `aarch64-none-elf-` (GNU binutils on macOS arm64).

## Top-line numbers

| Metric | Concrete | Templates |
|---|---:|---:|
| Total instructions | 8 885 | 6 765 |
| Alias-triggered (objdump's alias mechanism fired) | 1 720 (19.4 %) | 651 (9.6 %) |
| ...of which round-trip-safe (user already typed the alias) | **1 715** | **649** |
| ...of which mnemonic-changed by alias resolution | **5** | **2** |
| Non-alias mnemonic changes (cond-code rename / unscaled-load / `bic`-with-imm) | 28 | 27 |
| Total mnemonic-changing transformations (concrete) | **33 / 8 885 = 0.37 %** | |
| Total mnemonic-changing transformations (template) | **29 / 6 765 = 0.43 %** | |

Breakdown of the 1 720 alias-triggered cases by `input_mnemonic → alias_out_mnemonic (noalias_mnemonic)`:

| Count | IN | OUT (alias) | NOALIAS (canonical encoding) | Class |
|---:|---|---|---|---|
| 1 406 | `mov` | `mov` | `movz` | round-trip-safe |
|    95 | `cmp` | `cmp` | `subs` | round-trip-safe |
|    93 | `mov` | `mov` | `orr`  | round-trip-safe |
|    22 | `lsr` | `lsr` | `ubfm` | round-trip-safe |
|    19 | `tst` | `tst` | `ands` | round-trip-safe |
|    18 | `bfi` | `bfi` | `bfm`  | round-trip-safe |
|    15 | `mov` | `mov` | `movn` | round-trip-safe |
|     9 | `lsl` | `lsl` | `ubfm` | round-trip-safe |
|     6 | `mov` | `mov` | `add`  | round-trip-safe |
|     6 | `ubfx`| `ubfx`| `ubfm` | round-trip-safe |
|     5 | `dc`  | `dc`  | `sys`  | round-trip-safe |
|     4 | `bfi` | `bfxil` | `bfm`  | **mnemonic-changed (simplification)** |
|     4 | `bfxil`|`bfxil`| `bfm`  | round-trip-safe |
|     4 | `mul` | `mul` | `madd` | round-trip-safe |
|     3 | `umull`|`umull`|`umaddl` | round-trip-safe |
|     2 | `csetm`|`csetm`|`csinv` | round-trip-safe |
|     1 each: `bfc/bfc/bfm`, `lsr/lsr/lsrv`, `mvn/mvn/orn`, `ror/ror/extr`, `ror/ror/rorv`, `sxtw/sxtw/sbfm`, `tlbi/tlbi/sys`, `wfi/wfi/hint` | | | | round-trip-safe |
|     1 | `ubfx`| `lsr` | `ubfm` | **mnemonic-changed (simplification)** |

## Simplifications (these are wins)

Only **5 concrete instances** (2 unique templates) saw the disassembler
shorten the mnemonic. All of them are improvements:

| Input | Objdump output | Hex | Note |
|---|---|---|---|
| `bfi w0, w8, #0, #8` | `bfxil w0, w8, #0, #8` | `33001d00` | LSB=0: `bfi` and `bfxil` are encoding-equivalent. Both forms accepted; objdump prefers `bfxil` when `lsb==0`. |
| `bfi w3, w4, #0, #1` | `bfxil w3, w4, #0, #1` | `33000083` | same |
| `bfi w4, w0, #0, #1` | `bfxil w4, w0, #0, #1` | `33000004` | same |
| `bfi w6, w8, #0, #8` | `bfxil w6, w8, #0, #8` | `33001d06` | same |
| `ubfx w0, w11, #24, #8` | `lsr w0, w11, #24` | `53187d60` | `ubfx Rd, Rn, #24, #8` on a W-reg extracts the top byte — semantically `lsr #24`. `lsr` is the more natural read. |

## Non-alias mnemonic changes (cosmetic but worth tracking)

These are not "alias resolution" in the objdump sense (`alias_out ==
noalias_out` for all of them), but the disassembled mnemonic still
differs from the input. They split into four pattern families:

### B.1. Condition-code aliasing: `b.hs`/`b.lo` → `b.cs`/`b.cc`

| Input pattern | Disasm | Count |
|---|---|---:|
| `b.hs <target>` | `b.cs <target>  // b.hs, b.nlast` | 8 |
| `b.lo <target>` | `b.cc <target>  // b.lo, b.ul, b.last` | 13 |

These are full encoding-level synonyms (same 4-bit cond field): `hs == cs == cond 0010`, `lo == cc == cond 0011`. objdump picks `cs`/`cc` and helpfully emits the other spelling as a comment. Round-trip-jarring textually but bit-identical. **Easy mitigation**: have the SAM disassembler emit whichever spelling is preferred by spectrum4 conventions (`hs`/`lo`, by the look of the corpus).

### B.2. `bic Wn, Wn, #imm` → `and Wn, Wn, #~imm`

| Input | Disasm | Hex | Count |
|---|---|---|---:|
| `bic w7, w7, #1` | `and w7, w7, #0xfffffffe` | `121f78e7` | 1 |

`bic` with an immediate operand is *not a real encoding*; GAS expands it to `and` with the inverted bitmask at assembly time. The information that the user typed `bic` is lost. Mild jarring. Only **one instance in the entire corpus**.

### B.3. `str Xt, [Xn, #-imm]` → `stur Xt, [Xn, #-imm]`

| Input | Disasm | Count |
|---|---|---:|
| `str w6, [x3, #-0x04]` | `stur w6, [x3, #-4]` | 1 |
| `str wzr, [x3, #-0x08]` | `stur wzr, [x3, #-8]` | 1 |
| `str x2, [x3, #-0x10]` | `stur x2, [x3, #-16]` | 1 |
| `str x5, [x2, #-8]` | `stur x5, [x2, #-8]` | 1 |

`str` with a negative offset isn't a valid scaled-immediate encoding (the immediate field is unsigned). GAS picks `stur` (unscaled). The disassembler shows `stur`. Mild jarring. **4 instances total.**

### B.4. `mov reg, reg, lsl #n` → `orr reg, xzr, reg, lsl #n`

| Input | Disasm | Hex | Count |
|---|---|---|---:|
| `mov x2, x21, lsl #3` | `orr x2, xzr, x21, lsl #3` | `aa150fe2` | 1 |

The `mov` alias of `orr` only covers the **unshifted** register form. As soon as a shift is present, the `mov` alias rule fails and GAS emits `orr Xd, xzr, Xn, lsl #n` directly. The user typed `mov`; the disassembler shows the underlying `orr` with xzr exposed. **Genuinely jarring** — the only such case in the corpus.

### B.5. `sub x, y, UDG_COUNT*32 - 1` → `add x, y, #1`  (ARTIFACT — IGNORE)

| Input | Disasm | Count |
|---|---|---:|
| `sub x18, x14, UDG_COUNT * 32 - 1` | `add x18, x14, #0x1` | 1 |

This is **an artifact of the survey methodology**: I defined every unknown symbol as `0` to get the corpus to assemble, so `UDG_COUNT * 32 - 1` evaluated to `-1`. GAS canonicalises `sub x, y, #-1` as `add x, y, #1`. The real corpus has `UDG_COUNT = 21`, giving `21*32-1 = 671`, which encodes as `sub Xn, Xn, #671` with no rewriting. **Not a real case.**

## Jarring transformations (final tally)

After excluding the symbol-zeroing artifact (B.5), there is exactly **one genuinely-jarring template-level case** in the entire 8 885-instruction corpus:

| Template | Instances | Severity |
|---|---:|---|
| `mov Xn, Xn, lsl #imm` → `orr Xn, xzr, Xn, lsl #imm` | 1 | Annoying — `xzr` appears in the output that wasn't in the input. |

The conditional-code rewrites (`b.hs`→`b.cs`, `b.lo`→`b.cc`) and `str→stur` and `bic→and` rewrites total 27 more instances, but each is at most an unsurprising synonym swap rather than a structural change.

## Ambiguous

None. The cases that *could* be considered ambiguous (e.g. `bfi`↔`bfxil`) all collapse cleanly to "shorter or equally-short alias name with same operand count".

## Inverse multiplicity (alias-triggered only)

Excluding immediate-literal noise (where every distinct `#imm` value is a "different input"), only **3 output templates have multiple distinct input templates**:

| Disassembled template | Distinct input templates that collapse to it | Count |
|---|---|---:|
| `lsr Wn, Wn, #imm` | `lsr Wn, Wn, #imm`; `ubfx Wn, Wn, #imm, #imm` | 2 |
| `bfxil Wn, Wn, #imm, #imm` | `bfi Wn, Wn, #imm, #imm`; `bfxil Wn, Wn, #imm, #imm` | 2 |
| `b.cs <target>` | `b.hs <target>` (8 instances at concrete level) | 1 input template → 1 output template (cosmetic synonym only) |

Everything else is 1:1 at the template level. **Dictionary-style compression of the alias dimension would save almost nothing** — the entropy is in the immediate field, not the mnemonic.

## Register-flavour breakdown

Alias-triggered cases by special-register involvement in the **input** text:

| Special register in input | Count | Notes |
|---|---:|---|
| (none) | 1 703 | Dominated by `mov reg, #imm` (1 406×) and `cmp/tst reg, reg-or-imm` (114×). Special registers exist only in the *encoded* form (e.g. `cmp` uses xzr as the implicit destination), but the user types ordinary registers. |
| `wzr` | 9 | mostly `mov`/`cmp`/`tst` with wzr explicit |
| `sp` | 6 | `mov sp, x?` / `mov x?, sp` (these are `add sp, sp, #0` aliases) |
| `xzr` | 2 | `mov xN, xzr` |

The cluster Pete predicted — aliases around `xzr`/`wzr`/`sp` — *does* exist, but it's nowhere near the dominant pattern. The dominant pattern is `mov Rd, #imm` (the assembler's choice of movz/movn/orr-with-zero-second-operand is invisible at the source level).

## Recommendation

**Yes, the "store 4-byte machine code, disassemble with objdump-style alias resolution" strategy is safe for spectrum4.**

- 8 885 unique concrete instructions in the corpus
- 33 (0.37 %) undergo any mnemonic-changing transformation
- Of those 33, only **1** is genuinely jarring (`mov Xn, Xn, lsl #n` exposing `xzr`)
- The remaining 32 are either:
  - simplifications (5: `bfi`→`bfxil`, `ubfx`→`lsr`)
  - cond-code synonym swaps (21: `b.hs`→`b.cs`, `b.lo`→`b.cc`) — fixable by choosing the spectrum4-preferred spelling when rendering
  - `str`-with-negative-offset → `stur` (4) — semantically equivalent
  - `bic`-with-immediate → `and`-with-inverted-immediate (1) — semantically equivalent
  - artifact from our `.set X, 0` test rig (1, ignore)

**Two cheap fixes** would reduce the user-visible jarring count to essentially zero for spectrum4:

1. Have the SAM disassembler emit `b.hs`/`b.lo` instead of `b.cs`/`b.cc` (the spectrum4 corpus uses the `hs`/`lo` spelling consistently). This removes 21 of the 27 non-alias cases.
2. Have the SAM disassembler emit `str` instead of `stur` when the immediate is negative-but-in-`stur`-range. This removes 4 more.

After those two fixes: **2 jarring instances out of 8 885 (0.02 %)** — `bic→and` and `mov-with-shift→orr-with-xzr`. Both are accepted as "normal" by anyone familiar with aarch64 (e.g. they appear all over LLVM/binutils disassembly).

**Pete's hypothesis is confirmed empirically.** The "compact 4-byte" `.tbn` representation will produce disassembly that looks essentially the same as what the user typed, with the rare deviations being either improvements or trivially-mitigable synonym swaps.

## Reproducing

```bash
# Flatten
INC="-I /Users/pmoore/git/spectrum4/src/spectrum4/tests \
     -I /Users/pmoore/git/spectrum4/src/spectrum4/kernel \
     -I /Users/pmoore/git/spectrum4/src/spectrum4/roms \
     -I /Users/pmoore/git/spectrum4/src/spectrum4/demo \
     -I /Users/pmoore/git/spectrum4/src/spectrum4/libextra \
     -I /Users/pmoore/git/spectrum4/src/spectrum4"
: > /tmp/spectrum4_all_flat.s
for f in /Users/pmoore/git/spectrum4/src/spectrum4/targets/release.target \
         /Users/pmoore/git/spectrum4/src/spectrum4/targets/debug.target \
         /Users/pmoore/git/spectrum4/src/spectrum4/tests/test_*.runner; do
  /Users/pmoore/git/sam-aarch64/tools/flatten-s/flatten-s $INC "$f" >> /tmp/spectrum4_all_flat.s 2>/dev/null
done

# Extract instructions
grep -E "^[[:space:]]*\.macro[[:space:]]" /tmp/spectrum4_all_flat.s | awk '{print $2}' | sort -u > /tmp/macro_names.txt
python3 /tmp/extract_insns.py      # → /tmp/concrete.txt
python3 /tmp/build_survey.py       # → /tmp/canonical_survey.s

# Assemble + disassemble
aarch64-none-elf-as /tmp/canonical_survey.s -o /tmp/canonical_survey.o
aarch64-none-elf-ld -Ttext=0 -o /tmp/canonical_survey.elf /tmp/canonical_survey.o
aarch64-none-elf-objdump -d /tmp/canonical_survey.elf > /tmp/canonical_survey.objdump.txt
aarch64-none-elf-objdump -d -M no-aliases /tmp/canonical_survey.elf > /tmp/canonical_survey.noalias.txt

# Analyse
python3 /tmp/compare2.py
```

The five scripts (`extract_insns.py`, `build_survey.py`, `compare2.py`, `inv_alias.py`) live in `/tmp/` for this survey — re-create from this doc if needed.
