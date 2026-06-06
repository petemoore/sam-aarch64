# Go-vs-Z80 disassembler capability parity (i10)

**Date:** 2026-06-08 · **Author:** Pete

## Why this report exists

The on-SAM Z80 disassembler `src/disasm.asm` is validated by an oracle test
(`tools/z80-test-harness-go/disasm_oracle_test.go`, `TestDisasmOracle`) that
compares it word-by-word against the Go authority `aarch64dec.DecodeAt` over the
release corpus `tests/m6/release/release.img` (5438 words) — and reaches **100%
(5438/5438, 0 mismatches)**, confirmed by re-running the test for this report.

That 100% is real but **corpus-bounded**: release.img only contains the
instructions spectrum4 actually uses. Any instruction family the Go decoder
handles that does *not* appear in release.img is decoded by `disasm.asm` but
**never compared against Go by the oracle** — so its Z80 correctness is asserted
structurally (the handler exists, mirrors the Go source) but not *empirically
tested*. This report surfaces exactly those gaps so coverage isn't silently
overstated.

## Method (reproducible)

```
make aarch64dec
./build/aarch64dec tests/m6/release/release.img | awk '{print $3}' | sort | uniq -c | sort -rn
```

The Go decoder's full mnemonic surface comes from two sources:

- **Form-table families** — `aarch64enc.AllForms()` (manual + generated forms),
  walked in `disasm.go:DecodeAt` (`tools/aarch64dec/disasm.go:92`). 74 distinct
  mnemonics.
- **Hand-rolled special-case decoders** — run *before* the form walk:
  `decodeMem` (`mem.go`), `decodeSys` (`sys.go`), `decodeTestBranch`
  (`tbranch.go`), the udf shortcut (`disasm.go:77`), `decodeAlias`
  (`aliases.go`), and `decodeDPReg` (`dpreg.go`, last).

Concrete-word probes (`aarch64dec` on a 4-byte LE blob) confirm each claimed
"Go decodes it" below — these are observed, not assumed.

## Corpus composition (release.img, 5438 words)

| Bucket | Count | Note |
|---|---|---|
| Real instructions | 4691 | 66 distinct families (collapsing `b.<cond>`) |
| `.inst 0x…` (undecoded → data) | 747 | interleaved data past the code (addr ≥ `0x238`) |
| `udf #N` (data table) | 464 | top-16-bits-zero data words, rendered `udf` per objdump |

The 66 families actually exercised:

```
add adds adr adrp and ands b b.<cond> bfc bfi bfxil bic bl blr br cbnz cbz
cmp csel csetm dc dmb dsb eor eret isb ldp ldr ldrb ldrh ldrsh lsl lsr madd
mov movk mrs msr msub mul mvn nop orr ret sbfx stp str strb strh stur sturb
sturh sub subs tbnz tbz tlbi tst ubfx udf udiv umaddl umsubl umulh umull wfi
```

## Capability cross-tabulation

"Go decodes?" = produces a real mnemonic (not `.inst`). "release.img?" = the
family fires at least once over the corpus. "disasm.asm handles?" = a dedicated
handler exists in the dispatch chain (`src/disasm.asm`, mirroring `DecodeAt`
order). "Oracle-tested?" = YES **only if** it appears in release.img (the oracle
compares per-corpus-word; absent-from-corpus ⟹ never compared).

### A. Decoded by Go, exercised by release.img → oracle-tested (the certified set)

All 66 families listed above. Go decodes, release.img exercises, `disasm.asm`
handles, oracle compares them every run. **No action.** (66 families.)

### B. Decoded by Go, NOT in release.img → handled by disasm.asm but UNTESTED by oracle

These are the gap. `disasm.asm` has a handler (the family's whole class is ported
— see the dispatch chain at `src/disasm.asm:95-220`), but no release.img word
exercises it, so the oracle never checks the Z80 output against Go. Each row's
"Go decodes" was probed with a concrete word.

| Family | Go src (decoder) | Go decodes? | disasm.asm handler | Oracle-tested? | Priority |
|---|---|---|---|---|---|
| `ccmp` / `ccmn` (cond compare) | form table (`ccmp`) | YES | condsel/form region | **NO** | **HIGH** |
| `csinc` / `csinv` / `csneg` (non-alias cond-select) | form (`csinc`) + `decodeCondSelAlias` | YES | `disasm_not_condsel` region | **NO** | **HIGH** |
| `cset` / `csetm` / `cinc` / `cinv` / `cneg` (cond-sel aliases) | `aliases.go:decodeCondSelAlias` | YES (`csetm` only in corpus) | condsel | **NO** (except `csetm`) | MED |
| `cmn` (adds-zr alias, imm + shifted-reg) | `aliases.go:305,391` | YES | addsub-imm / dpreg-alias | **NO** | MED |
| `neg` / `negs` / `mvn` (dp-reg aliases) | `aliases.go:decodeDPRegAlias` | YES (`mvn` ×1 in corpus) | `disasm_not_dpreg_alias` | **NO** (`neg`/`negs`) | MED |
| `smull` / `smnegl` / `smaddl` / `smsubl` (signed long mul) | `aliases.go:decodeMul3Source` | YES | `disasm_not_mul3` | **NO** | MED |
| `smulh` (signed high mul) | `decodeMul3Source` | YES | mul3 | **NO** | MED |
| `mneg` (madd Ra=zr,o0=1) | `decodeMul3Source` | YES | mul3 | **NO** | LOW |
| `extr` / `ror`(imm) | `aliases.go:tryDecodeExtr` | YES | extr region | **NO** | MED |
| `asr` / `lsl`/`lsr`/`ror` *variable* (lslv/lsrv/asrv/rorv) | `aliases.go:decodeShiftVarAlias` | YES | `disasm_not_shiftvar` | **NO** | MED |
| `bic` / `bics` / `eon` / `orn` (logical shifted-reg base) | `dpreg.go:logicalShiftedMnem` | YES (`bic` ×9 in corpus) | dpreg base | **NO** (`bics`/`eon`/`orn`) | MED |
| Extended-register add/sub (`uxtb…sxtx` operand) | `dpreg.go:decodeExtendedReg` | YES | dpreg base | **NO** | MED |
| `sbfiz` / `ubfiz` / `asr`(imm) / `bfc`-via-bfm edge | `aliases.go:decodeBitfieldAlias` | YES (`bfc` ×1, `sbfx` ×1) | bitfield region | **NO** (`sbfiz`/`ubfiz`/`asr`-imm) | MED |
| `sxtb`/`sxth`/`sxtw`/`uxtb`/`uxth` (bitfield extend aliases) | `decodeBitfieldAlias` + forms | YES | bitfield | **NO** | LOW |
| `ldrsb` / `ldrsw` / `ldursb`/`ldursw`/`ldursh`/`ldurb`/`ldurh` | `mem.go:scalarMnem`/`sturMnem` | YES (`ldrsh`/`stur*` in corpus) | mem region | **NO** (the rest) | MED |
| LDR/LDRSW **literal** (PC-rel `ldr Xt,0x…`) | `mem.go:decodeLiteralMem` | YES (`ldr` ×many, base+off; literal form specifically) | mem | **partial** | MED |
| Register-offset load/store (`[Rn, Rm, lsl/uxtw/sxtw/sxtx]`) | `mem.go:decodeRegOffset` | YES | mem | **NO** | MED |
| `dsb`/`dmb` exotic options (`oshld`…`pssbb`/`ssbb`) | `sys.go:barrierOption` | YES (`dsb sy`/`ish` in corpus) | sys/barrier | **partial** (only options that fire) | LOW |
| `at s1e*` (address translate) | `sys.go:atName` | YES | sys-instr | **NO** | LOW |
| `ic ialluis`/`iallu`/`ivau` | `sys.go:icName` | YES | sys-instr | **NO** | LOW |
| `msr` (PSTATE immediate, e.g. `daifset`/`daifclr`) | `sys.go:decodeMsrImm` | YES | sys | **NO** (only reg-form `msr` in corpus) | MED |
| `mov` (logical-imm bitmask alias, non-movz-encodable) | `aliases.go:decodeLogicalImmAlias` | YES (`mov` ×many via movz; bitmask-`mov` specifically) | logimm | **partial** | MED |
| `movz` / `movn` (kept base form, not the `mov` alias) | `aliases.go:decodeMoveWideAlias` | YES (`mov`/`movk` in corpus; kept `movz`/`movn` specifically) | movewide | **NO** | LOW |
| `movk, lsl #N` (non-zero hw) | `aliases.go:decodeMovk` | YES (`movk` ×8 in corpus) | movewide | **partial** | LOW |

### C. NOT decoded by Go (so neither side handles → identical `.inst` fallback)

These are *parity-correct by omission*: Go falls to `.inst`, the Z80 port
faithfully falls to `.inst`, so the oracle would match even if they appeared.
Listed so they aren't mistaken for Z80 regressions.

| Family | Why Go declines | Evidence |
|---|---|---|
| **`sdiv`** | no manual form; `decodeShiftVarAlias` only handles lsl/lsr/asr/ror | probe `0x1ac40c00` → `.inst`; `aliases.go:840` declines "udiv/sdiv/…"; only `udiv` has a form (`manual_forms.go:542`) |
| Atomics (`ldadd`/`swp`/`ldclr`/…) | encoder never emits; `mem.go` declines bit21=1 sub-space | `mem.go:218-228` |
| SIMD/FP (any `v*`/`f*`) | entirely out of the encoder's scope | absent from `AllForms()` |
| `svc`/`hlt`/`brk`/exception-gen | no decoder | probe → `.inst` |
| PRFM (literal + register) | `mem.go` declines opc=11 | `mem.go:89` |

## Prioritized worklist (what actually matters)

The oracle's 100% certifies the **spectrum4 instruction set**. The untested set
(§B) is "could be wrong on Z80 and we wouldn't know from CI." Ranked by
likelihood of being hit by *future* SAM-side targets (a kernel, LLVM/clang
output, hand-written aarch64), highest first:

1. **HIGH — `ccmp`/`ccmn` and the cond-select family (`csinc`/`csinv`/`csneg`,
   `cset`/`cinc`/`cinv`/`cneg`).** Compiler-ubiquitous (every ternary / flag
   merge). `csetm` is the *only* cond-select word in release.img, so the entire
   alias-inversion logic (`decodeCondSelAlias`, `aliases.go:502`) is essentially
   untested on Z80. Most valuable place to add fixtures.
2. **HIGH — extended-register add/sub and the dp-reg aliases (`neg`/`negs`/`cmn`).**
   Address arithmetic (`add x0, x1, w2, uxtw`) is everywhere in real code; the
   corpus exercises only the shifted-reg base, never the extended operand
   (`decodeExtendedReg`, `dpreg.go:193`).
3. **MED — signed multiply long (`smull`/`smaddl`/`smulh`/`smnegl`/`smsubl`) and
   `extr`/`ror`-imm.** The corpus has the *unsigned* longs (`umull`/`umulh`/…)
   but zero signed ones, despite identical decode structure — a clean
   "structurally proven, never run" case.
4. **MED — full load/store option matrix:** register-offset (`[Rn,Rm,uxtw]`),
   signed loads (`ldrsb`/`ldrsw`), and the LDR-literal PC-rel form. Memory is the
   single biggest decoder (`mem.go`); the corpus hits maybe a third of its
   branches.
5. **MED — logical shifted-reg base (`bics`/`eon`/`orn`), variable shifts
   (`lslv`…`rorv`), bitfield `sbfiz`/`ubfiz`/`asr`-imm, PSTATE `msr`
   (`daifset`/`daifclr`).** Plausible but less hot.
6. **LOW — `at`/`ic` cache/AT ops, exotic barrier options, kept `movz`/`movn`.**
   Kernel-only or rare; correctness still mirrors Go by construction.

### Recommended action

Add a **synthetic fixture corpus** to `TestDisasmOracle` (or a sibling test): one
hand-built `.bin` of ~30-40 words covering every §B family (concrete words for
each are already in the Go decoders' verification comments — e.g.
`aliases.go:497-501` for cond-select, `dpreg.go:184-192` for extended-reg). Run
the same per-word Z80-vs-Go comparison over it. This converts §B from
"structurally ported" to "empirically certified" at near-zero cost (the harness
already loads `disasm.bin` and calls `DecodeAt`; only the input words change).
This is the natural completion of the parity-audit's "untested-form-combination
sweep" follow-up (`docs/notes/2026-05-29-z80-go-parity-audit.md` §summary, item 3;
`docs/notes/m7-status.md` "Parity robustness seeds").

Note the one genuine **Go-side** gap to fix independently of the Z80 port:
**`sdiv` decodes to `.inst`** (§C) while `udiv` decodes — add the `sdiv` manual
form (or handle it in `decodeShiftVarAlias`) so both sides decode it, then it
joins the testable set.

## Bottom line

- Oracle = **100% over release.img**, but release.img exercises only **66 of the
  ~90 mnemonic families** the Go decoder can produce.
- **~24 families (§B) are handled by `disasm.asm` but never compared against Go
  by the oracle** — untested-by-corpus, not unhandled.
- The ones that matter: **conditional compare/select, extended-register
  arithmetic, signed multiplies** — all compiler-common and all currently
  asserted only structurally.
- One Go-side gap to close separately: **`sdiv`** (decodes to `.inst` today).
- Fix: a small synthetic fixture sweep through the existing oracle harness.
