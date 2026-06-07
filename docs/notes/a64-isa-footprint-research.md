# Full ARMv8.0-A A64 ISA — memory-footprint research / estimate

*2026-06-08. Research only — no encoder/decoder changes. Feeds a future
"is broad-ISA support worth it (kernel dev / ingesting LLVM output)?"
decision. This is an **informed estimate with stated assumptions**, not
false precision.*

## 0. Question

How much **additional** memory — encoder Form-table entries, `enctab.enc`
bytes, and Z80 code (slot encoders + disassembler decoders) — would it take
to grow from today's spectrum4-release subset to the **full ARMv8.0-A A64
instruction set**?

Scope, fixed precisely:

- **A64 only.** No AArch32 (A32/T32/Thumb) — a separate decode tree we never
  touch.
- **ARMv8.0 baseline only.** Not the later v8.1–v8.9 extensions (LSE atomics,
  FP16 arithmetic, dot-product, BF16, the v8.4 LSE2/MTE, pointer auth, SVE/SVE2,
  …). Those are large again and explicitly out of scope.
- **Includes FP scalar + Advanced SIMD/NEON.** These are part of the v8.0-A
  *baseline* (FEAT_AdvSIMD and FEAT_FP are mandatory in v8.0-A application
  cores), and they are the dominant contributor — so they are quantified on
  their own below.

## 1. Scope of the full v8.0-A A64 ISA

A64 instructions are a fixed 32-bit width, decoded as a four-level table:
main encoding → instruction group → decode group → instruction
([weinholt.se, *Structure of the ARM A64 instruction set*](https://weinholt.se/articles/arm-a64-instruction-set/);
mirrors ARM ARM DDI0487 Part C, Chapter C4 "A64 Instruction Set Encoding").

The five top-level encoding groups (ARM ARM C4.1; ARMv8 Instruction Set
Overview PRD03-GENC-010197 §5):

| # | Top-level group | What it holds today's subset touches | Rough v8.0 mnemonic share |
|---|---|---|---|
| 1 | Data processing — immediate | add/sub-imm, logical-imm, move-wide, bitfield, PC-rel (adr/adrp), extract | ~15–20 mnemonics |
| 2 | Branches, exception-generating, system | b, bl, b.cond, cb(n)z, tb(n)z, br/blr/ret, svc/hvc/brk, mrs/msr, dc/at/ic/tlbi, barriers, hint/nop | ~30–40 mnemonics |
| 3 | Loads and stores | ldr/str (+ b/h/sb/sh/sw), ldp/stp, ldur/stur, ld(a)xr exclusives, literal, prfm; **plus all the FP/SIMD load-store + LD1..LD4 structure loads** | ~40 integer + ~15 FP/SIMD-LS |
| 4 | Data processing — register | add/sub-reg (shifted/extended), logical-reg, 2-source (udiv/lslv…), 3-source (madd/…), cond-select, cond-compare, bit-ops (clz/rbit/rev…) | ~40–50 mnemonics |
| 5 | **Data processing — scalar FP + Advanced SIMD** | none in today's subset | **see §1.2 — the dominant block** |

### 1.1 Total counts (authoritative anchors)

- **442 instruction mnemonics** total for ARMv8-A A64, excluding aliases
  ([weinholt.se](https://weinholt.se/articles/arm-a64-instruction-set/), a
  count over the ARM ARM alphabetical list). Caveat: that count is over a
  recent ARM ARM that already includes some post-v8.0 extensions; the pure
  v8.0-A subset is a bit smaller, but 442 is the right order-of-magnitude
  anchor.
- **~1000–2000 instruction *variants*** depending on how you count
  (same source) — i.e. distinct encodings once you expand each mnemonic over
  W/X, vector arrangement, element size, etc. A "Form" in our table is closer
  to a *variant* than a *mnemonic* (one Form per operand-shape), so the
  variant count is the more relevant target for the encoder table.

### 1.2 NEON / FP — the dominant contributor, quantified on its own

The ARMv8 Instruction Set Overview (PRD03-GENC-010197) splits group 5 into:

- **§5.6 Scalar floating-point** — 11 sub-sections: FP load-store, move
  (reg/imm), convert, round-to-integral, arithmetic (1-src + 2-src), min/max,
  multiply-add, compare, conditional-select. Roughly **~30 FP scalar
  mnemonics** (fmov, fadd, fsub, fmul, fdiv, fnmul, fabs, fneg, fsqrt,
  fmadd/fmsub/fnmadd/fnmsub, fcmp/fcmpe, fccmp, fcsel, fcvt*, fcvtz*/scvtf/
  ucvtf, frint*, fmax/fmin/fmaxnm/fminnm, …), each typically over 2–3
  precisions (H/S/D).

- **§5.7 Advanced SIMD (NEON)** — 13 sub-categories: vector arithmetic,
  widening/narrowing arithmetic, unary arithmetic, vector-by-element,
  permute, immediate, shift-immediate, FP/int convert, reduce-across-lanes,
  pairwise, table-lookup (tbl/tbx), and the LD1..LD4 / ST1..ST4 structure
  load-stores. This is **~120–160 mnemonics** (add/sub/mul/mla/mls, the
  saturating q-variants, abs/neg, cmeq/cmgt/cmge/cmhi/cmhs/cmtst, and/orr/eor/
  bic/orn/bsl/bit/bif, the shift family sshl/ushl/sshr/ushr/sli/sri/shrn/…,
  the widening uaddl/usubl/saddw/…, the reduce addv/smaxv/uminv/…, the
  permute zip/uzp/trn/ext/rev{16,32,64}, dup/ins/umov/smov, tbl/tbx,
  the FP-vector fadd/fmul/fmla/fcm*/frecpe/frsqrte/…, the cryptography-adjacent
  but-v8.0 pmull, ld1..ld4/st1..st4, movi/mvni/fmov-vector, …).

**Headline:** of the ~442 mnemonics, the **scalar-FP + Advanced-SIMD block is
~150–190 mnemonics — roughly 40–45% of the ISA by mnemonic, and a *larger*
share by encoding-variant** (a single NEON mnemonic like `add (vector)`
expands over arrangement {8B,16B,4H,8H,2S,4S,2D} = up to 7 encodings, plus
the by-element and scalar forms). Counting variants, **NEON/FP is well over
half the total encoding space** — it is the single biggest cost driver and
the reason "full v8.0-A" is dominated by NEON, not by the integer core.

## 2. Today's subset — concrete current numbers

Measured in this worktree (`make enctab`, `make m3-asm-prod`):

| Metric | Value | Source |
|---|---|---|
| Form-table entries (encoder) | **148** (100 manual + 48 MRA-derived) | `make enctab` output; `grep -c MnemonicID tools/aarch64enc/{manual_forms,data}.go` |
| Distinct mnemonics recognised | **99** | `tools/sam-aarch64-format/mnemonics.go` (`MnemonicTable`) |
| `build/enctab.enc` size | **3676 bytes** | `ls -l build/enctab.enc` |
| `ENCTAB_LEN` (must equal above) | **3676** | `src/loader.asm:92` |
| Encoder **slot kinds** (operand-encoder families) | **19** | `tools/aarch64enc/types.go:16-36` |
| Z80 **slot-encoder** modules | **11** | `src/slots/*.asm` |
| Z80 **disassembler** decode families | **~16** | `src/disasm.asm` `disasm_try_*` (movewide, addsubimm, logimm, bitfield, condsel, mul3, shiftvar, dpreg, dpreg-alias, branch, tbranch, sys, nop, udf, mem, …) |
| prod assembler binary | **14475 B**, code_end `&B88B`, **1909 B headroom** to the `&C000` cliff | `make m3-asm-prod` budget line |

Derived ratios (used for extrapolation in §3):

- **enctab.enc: ~24.8 bytes / Form** (3676 / 148).
- The 11 slot-encoder sources total **~167 KB of Z80 *source***
  (`wc -c src/slots/*.asm`); the heaviest are `mem.asm` (50 KB src),
  `logical_imm.asm` (28 KB), `adrp_imm.asm` (23 KB), `branch_imm.asm` (15 KB).
  These are the *complex operand* families — the bulk of the integer ISA's
  operand kinds is already paid for.
- `src/disasm.asm` is **~304 KB source / 7822 lines** for ~16 families ≈
  **~19 KB source / family** (a coarse proxy; assembled bytes are far smaller,
  but the *relative* family-to-family cost holds).

The 19 existing slot kinds already cover: the GP-register forms (X/W, with
SP variants), every integer immediate shape (imm5/6/12-shifted/16-shifted,
logical-imm bitmask, bitfield, shift-amount, extend-op), all the PC-relative
branch immediates (26/19/14-bit + adr/adrp), and the seven memory-operand
addressing modes. **None of them is a vector/element operand** — that is the
entirely-new operand machinery NEON needs.

## 3. The delta — full v8.0-A minus today

Stated assumptions for the extrapolation:

- **A1.** A "Form" ≈ one operand-shape variant. Full-ISA Form count tracks the
  ~1000–2000 *variant* anchor, not the 442 *mnemonic* anchor.
- **A2.** The 24.8 bytes/Form ratio holds for new forms (it is dominated by
  pattern+mask+slot list, which doesn't change shape for NEON).
- **A3.** Integer-core gaps are *cheap* — most needed operand kinds already
  exist; new integer mnemonics mostly reuse existing slot encoders. NEON is
  *expensive* — it needs whole new operand-kind families (vector arrangement,
  element index, register-list) on both the encode and decode sides.
- **A4.** Order-of-magnitude only. Ranges below are deliberately wide.

### (a) Encoder Form-table entries

| Bucket | Added Forms (est.) | Reasoning |
|---|---|---|
| Integer-core completion (groups 1–4 gaps: atomics excluded as v8.1, but rev/clz/rbit/extr/ccmp/the full 2-src+3-src set, all the ld/st sub-forms, exclusives, prfm) | **+250–400** | ~40–60 new mnemonics × a few operand shapes each; reuses existing slots. |
| Scalar FP (group 5, §5.6) | **+150–250** | ~30 mnemonics × {H,S,D} × operand shapes. |
| **Advanced SIMD / NEON (group 5, §5.7)** | **+700–1200** | ~120–160 mnemonics, each over up to 7 arrangements + by-element + scalar forms. **The dominant bucket.** |
| **Total** | **≈ +1100–1850 Forms** (from 148 → ~1250–2000) | Matches the 1000–2000-variant ISA anchor. |

### (b) `enctab.enc` bytes

At ~24.8 B/Form: **+1100–1850 Forms × ~25 B ≈ +27–46 KB**, taking
`enctab.enc` from 3.7 KB to roughly **30–50 KB**. NEON alone is **~17–30 KB**
of that. (NEON forms may be slightly *cheaper* per entry than the compound
integer immediates, so treat the low end as more likely — call it
**~25–40 KB total**, NEON ~60% of the growth.)

### (c) Z80 code — new slot encoders + disassembler decoders

The expensive part. New work splits into:

- **New operand-kind encoders/decoders (the structural cost).** NEON needs at
  minimum: vector-arrangement specifier (`Vn.8B`/`.16B`/…/`.2D`), element
  index (`Vn.S[2]`), the scalar FP/SIMD register kinds (B/H/S/D/Q), register
  lists (`{V0.16B-V3.16B}` for tbl/ld4), and FP immediate (`fmov #1.0`). Call
  it **~10–15 new operand-kind families** on the encode side and a matching
  set on the decode side. Each existing slot family is roughly a few-hundred
  to ~1–2 KB of *assembled* Z80 (the 11 current slot sources are 3.6–50 KB of
  *source*; assembled is much smaller). Estimate **~6–14 KB assembled** for the
  new NEON/FP encoder slot families, plus **~6–14 KB assembled** for the
  matching disassembler decode families.
- **Per-mnemonic dispatch glue.** Smaller — table-driven, amortised.

Order-of-magnitude: **+15–35 KB of assembled Z80 code** for full v8.0-A,
of which **NEON + FP is ~10–25 KB** (the majority). This is the number that
collides with the address-space budget (see §4) — `enctab.enc` is paged and
cheap; *code* is the scarce resource.

### (d) New operand kinds

From **19 slot kinds today → ~30–35**. New kinds, all NEON/FP:

- Vector register with arrangement (`Vn.<T>`), ~1 kind covering all
  arrangements via a sub-field.
- Vector element / indexed (`Vn.<Ts>[index]`).
- Scalar FP/SIMD registers B/H/S/D/Q (1–2 kinds).
- Vector register list (`{Vn.<T>, …}`) for tbl/tbx and LD1..LD4 (1 kind +
  count sub-field).
- FP immediate (8-bit `abcdefgh` → fp constant) — 1 kind.
- SIMD modified-immediate (movi/mvni `cmode`/`op` machinery) — 1 kind, but a
  fiddly one.

So **~6–8 genuinely new operand-kind families**, every one NEON/FP. The
integer core adds essentially **zero** new operand kinds — its gaps reuse the
existing 19.

### Delta summary

| Axis | Today | Full v8.0-A (est.) | Delta | NEON/FP share of delta |
|---|---|---|---|---|
| Encoder Forms | 148 | ~1250–2000 | **+1100–1850** | ~55–65% |
| `enctab.enc` | 3.7 KB | ~30–50 KB | **+25–45 KB** | ~60% |
| Z80 code (assembled) | (14.5 KB total binary) | — | **+15–35 KB code** | ~65–75% |
| Operand kinds | 19 | ~30–35 | **+~12** | **100%** |

## 4. Feasibility vs the SAM memory model

The hard constraint is **address-space code budget**, not total RAM.

### The `&C000` cliff (the binding constraint)

Both assembler variants link at `org &8000`; scratch/stack starts at `&C000`
(`SP = &C100`). If `code_end` reaches `&C000` the build silently boot-hangs —
turned into a numbered failure by `scripts/check-code-budget.sh`
(`CEILING=0xC000`), run at the tail of every `make m3-asm{,-prod}`
(`docs/notes/memory-layout.md` §"Code-budget ceiling"; `scripts/check-code-budget.sh:28`).

- Today's prod binary: code_end `&B88B`, **only 1909 B of headroom**.
- The §3(c) estimate is **+15–35 KB of code**.

**+15–35 KB of code into 1.9 KB of headroom is a ~10–18× overflow of the code
window.** Full-ISA support **cannot** live in the flat `&8000–&C000` window —
it is structurally impossible as-is. This is the headline feasibility fact.

### What *does* fit cheaply: the table (paging already solved this)

`enctab.enc` already lives **off-axis on physical page 4** (a dedicated 16 KB
RAM page, paged into section A on demand via the COMET trampoline —
`src/loader.asm:10-39`). A 16 KB page holds 16384 B; the §3(b) estimate of a
**30–50 KB** table overflows *one* page but trivially spans **2–4 pages**. And
physical pages are plentiful: a 512K machine has **32 pages (0–31)**;
the project already uses 4 (BASIC) + ENCTAB(4) + OUT(5–6) + IN(7–12) +
payloads(13–14), leaving **pages 15..31 ≈ 272 KB free**
(`https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m7-status.md`, IN/OUT-ceiling row; `docs/notes/sam-paging.md:80`).
So the **encoder table cost (§3b) is a non-issue** — paging it across a few
free pages is exactly the established off-axis pattern.

### The real problem: paging *code*, not data

The blocker is the **+15–35 KB of executable Z80** (§3c). The off-axis
mechanism today pages *data* (tables, buffers) — code still has to be resident
in `&8000–&C000` to run. Supporting full-ISA would force one of:

1. **Paged code / overlays.** Split the encoder+disassembler into overlay
   banks, paged into a code window on demand (the trampoline already proves
   section-B-resident code can survive an HMPR page-flip — the same idea, at
   larger scale). This is a real architectural project: an overlay manager,
   a partitioning of mnemonic families into banks, and a per-instruction
   dispatch that pages in the right bank. Plausible (272 KB of free pages is
   ample for 15–35 KB of banked code), but **not free** — it's the single
   biggest piece of new mechanism the full ISA would demand.

2. **Bigger storage / a different host story (Trinity).** If the working set
   genuinely won't partition cleanly, the longer-term answer is the Trinity
   LAN / external-storage path (memory index *trinity_hardware*) — stream
   code banks from the Pi rather than holding them on SAM pages. Heavier; only
   needed if overlays prove insufficient, which on a 512K machine they
   probably don't.

### Feasibility verdict

- **Fits as-is?** ❌ No. +15–35 KB of code vs 1.9 KB of code headroom is a
  structural impossibility in the flat `&8000–&C000` window.
- **Needs paging?** ✅ Yes — and specifically **paged *code* / overlays**, a
  new mechanism beyond today's data-only off-axis paging. The *table* growth
  (§3b) is already handled by the existing page-4 off-axis pattern spread over
  the free pages 15..31 (272 KB), so the table is the easy half.
- **Needs bigger storage (Trinity)?** ➖ Probably not *required* on a 512K
  machine — 272 KB of free pages comfortably exceeds the 15–35 KB code budget
  — but Trinity becomes attractive if the overlay-partitioning cost is judged
  worse than streaming banks over the LAN.

**Bottom line for the future decision:** full v8.0-A is **~40–45% NEON/FP by
mnemonic and >50% by encoding-variant**; that block alone is ~17–30 KB of
table and ~10–25 KB of code. The table cost is cheap (paging solved). The
**code cost forces a paged-code/overlay subsystem** — the one genuinely new
piece of architecture. Whether that's worth it for kernel-dev / LLVM-output
ingestion is a judgement call this doc deliberately leaves open; the point is
that the cost is concentrated in NEON and concentrated in *code paging*, not
in raw memory.

## Sources

Repo facts (file:line cited inline): `tools/aarch64enc/manual_forms.go`,
`tools/aarch64enc/data.go`, `tools/aarch64enc/types.go:16-36`,
`tools/sam-aarch64-format/mnemonics.go`, `src/loader.asm:92`,
`src/slots/*.asm`, `src/disasm.asm`, `docs/notes/memory-layout.md`,
`scripts/check-code-budget.sh:28`, `docs/notes/sam-paging.md:80`,
`https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m7-status.md`. Live measurements: `make enctab` (148 forms /
3676 B), `make m3-asm-prod` (code_end &B88B, 1909 B headroom).

ARM ISA facts:
- [weinholt.se — *Structure of the ARM A64 instruction set*](https://weinholt.se/articles/arm-a64-instruction-set/) (442 mnemonics; ~1000–2000 variants; four-level decode table).
- ARMv8 Instruction Set Overview, PRD03-GENC-010197 ([cs.princeton.edu copy](https://www.cs.princeton.edu/courses/archive/spr19/cos217/reading/ArmInstructionSetOverview.pdf)) — §5.6 scalar FP (11 sub-sections), §5.7 Advanced SIMD (13 sub-categories), group structure.
- [Arm A64 Instruction Set Architecture index, DDI0596](https://developer.arm.com/documentation/ddi0596/latest/) — authoritative Base vs SIMD&FP instruction lists (the per-group breakdown anchor; JS-rendered, not machine-quotable here).
- [Arm Architecture Reference Manual for A-profile, DDI0487](https://developer.arm.com/documentation/ddi0487/mb/) — Part C, Chapter C4 (encoding) / C6 (base) / C7 (SIMD&FP).
