# Phase 1 spec: standalone aarch64 assembler

**Status**: approved 2026-05-09. Read alongside `2026-05-09-vision.md`.

## 1. Goal & boundaries

A standalone Z80 program that runs on the SAM Coupé. Reads aarch64
assembly from a binary-tokenised source file on a SAM disk. Produces
three outputs on disk: a flat aarch64 binary (`kernel8.img`-loadable),
a human-readable listing file, and a symbol map.

**No editor.** Source files are produced on the Mac by `text2bin` and
transferred onto the SAM. The on-SAM editor is Phase 2 work.

**No network.** TFTP is Phase 3 work.

**Initial mnemonic subset**: the ~76 mnemonics observed in spectrum4's
`release.disassembly`, `debug.disassembly`, and `tests.disassembly`,
covering ~15-20 distinct encoding patterns. Encoder tables are extensible
by editing a Mac-side data file and rebuilding — adding mnemonics later
is not a code change.

**Validation oracle**: byte-diff against `aarch64-none-elf-as` for every
test fixture. Any disagreement is a Phase 1 bug.

**Architectural choice**: layered. The assembler lifts COMET's well-tested
SAM-specific I/O routines (character I/O, disk I/O, paging helpers) from
the decoded source in `reference/comet-decoded/`. Everything else —
parser, encoder, symbol table, expression evaluator — is fresh code.

## 2. Source language

**Native format**: binary-tokenised. Each `.s` file is a stream of
length-prefixed records. Designed for direct production by the Phase 2
editor, but in Phase 1 produced exclusively by Mac-side `text2bin`.

The exact binary format is an implementation detail to be designed
during the implementation plan, informed by reading
`reference/comet-decoded/comet.asm` to learn how COMET's tokenisation
is laid out. It must support lossless round-trip via `text2bin` /
`bin2text` over the constructs below.

### Accepted constructs (text dialect)

The text form is **GNU `as` aarch64-compatible** so that the byte-diff
oracle is meaningful — the same `.s` file fed to both tools must yield
identical bytes.

- **Mnemonics**: the ~76 from spectrum4 disassemblies. Listed in full
  in the implementation plan.
- **Registers**: `x0`–`x30`, `w0`–`w30`, `sp`, `wsp`, `xzr`, `wzr`, plus
  `fp` (= `x29`) and `lr` (= `x30`).
- **Immediates**: `#imm` or bare `imm`. Hex `0x…`, decimal, binary `0b…`,
  char `'a'`.
- **Operand modifiers**: shifted-register (`, lsl #N`, `lsr`, `asr`,
  `ror`), extended-register (`uxtb/h/w/x`, `sxtb/h/w/x`).
- **Addressing forms**: `[xn]`, `[xn, #imm]`, `[xn, xm]`,
  `[xn, xm, lsl #N]`, pre-index `[xn, #imm]!`, post-index `[xn], #imm`.
- **Labels**: `name:` (global by default).
- **Local labels**: `1:` … `1f`/`1b` (GNU style).
- **Forward references**: required (drives the two-pass design).
- **Directives** (kernel-friendly minimum): `.text`, `.data`, `.byte`,
  `.short`, `.word`, `.quad`, `.ascii`, `.asciz`, `.equ`/`.set`,
  `.global`, `.balign`, `.org`, `.skip`/`.space`, `.inst`.
- **PC-relative operators**: `:lo12:label` for the `adrp` + `add :lo12:`
  pattern. **Action item before implementation**: grep `~/git/spectrum4`
  for actual usage of `:lo12:`/`:hi12:`/`:abs_g0:`/etc. and confirm the
  set we need.
- **Expressions**: `+ - * / & | ^ << >> ~`, parens, full precedence,
  constant-folded.
- **Comments**: `//` line, `/* */` block. (Not `;` — that's a GNU
  aarch64 statement separator, not a comment.)
- **String literals**: `"…"` with `\n \t \\ \" \0 \xNN` escapes.

### Explicitly deferred (not v1)

- Macros (`.macro`/`.endm`, `.rept`/`.endr`).
- Conditional assembly (`.if`/`.endif`).
- `.section` directives beyond `.text`/`.data`.
- Symbol relocations beyond the operators above.
- Multi-file `.include`. (May be reconsidered in Phase 2 once we know
  which `spectrum4` files we actually want to assemble on the SAM.)

## 3. Architecture

### Two passes

Forward references mandate two passes. Pass 1 walks the binary source,
builds the symbol table with provisional addresses, and notes patch
sites. Pass 2 walks again with all symbols resolved, evaluates
expressions, and emits bytes.

### Z80 subsystems

```
Binary source buffer (paged RAM)
        │
        ▼
┌───────────────────┐
│ Stream reader     │  reads length-prefixed records
└───────────────────┘
        │ token stream (instruction record / label / directive / comment)
        ▼
┌───────────────────┐
│ Pass dispatcher   │  pass 1 vs pass 2 mode; drives pc counter
└───────────────────┘
        │
        ├─→ Symbol table          (hash-bucketed: name → {value, defined?})
        │
        ├─→ Expression evaluator  (+,-,*,/,&,|,^,<<,>>,~)
        │
        ├─→ Encoder (table-driven; never "understands" aarch64)
        │
        └─→ Output emitter        (pass 1: advance pc; pass 2: write bytes)

After pass 2:
        ▼
┌───────────────────┐
│ Disk writer       │  flat binary, listing, symbol map → MGT disk
└───────────────────┘
```

Each subsystem is a small, well-bounded module with a clear interface;
the symbol table never sees source bytes, the encoder never sees source
text. Each can be unit-tested independently.

### Mac-side companions (Phase 1 deliverables)

- `text2bin` — plain-text aarch64 source → binary tokenised file.
- `bin2text` — binary tokenised file → plain text (for diff/git/export).
- Encoder-table generator — Mac-side tools that read ARM MRA XML, filter
  to the project's mnemonic subset, and emit the binary tables consumed
  by the Z80 assembler at startup or at build time.
- Test harness — drives the round-trip oracle (text → bin → SimCoupé →
  bytes vs text → GNU `as` → bytes).

These live in `tools/`. Without `text2bin` we cannot exercise the
assembler, so it is on the critical path; it is not "infrastructure to
build later".

### Memory layout (512K SAM, 16K pages)

| Region | Pages | Notes |
|---|---|---|
| Z80 assembler code | 1–2 | ~16–32KB. Lifted COMET I/O + parser/encoder/passes. |
| Encoder tables | 1 | ~16KB, embedded or loaded once. |
| Binary source buffer | 1–4 | up to 64KB for kernel-scale source. |
| Symbol table | 1–2 | hash + entries; ~32KB supports thousands of symbols. |
| Output binary buffer | 1–4 | up to 64KB for the kernel image. |
| Listing buffer | 1 | optional; spilled to disk if large. |
| Working/stack | <1 | |

Worst case ≈ 200KB. Comfortable in 512K with headroom for Phase 2 and 3.

### Phase 1 dev loop

1. Pete writes `.s` plain text on Mac in his usual editor.
2. `text2bin foo.s foo.bin` — produce binary tokenised source.
3. `samfile add -i build/tests.mgt -f foo.bin` — inject into disk image.
4. `simcoupe --batch …` — run the assembler against `foo.bin` inside
   SimCoupé.
5. `samfile extract` — pull the assembled `.bin` (and listing, symbol
   map) off the disk image.
6. Diff against `aarch64-none-elf-as foo.s`. Pass/fail.
7. CI runs steps 2–6 on every commit.

## 4. Encoder tables

Aarch64 encoding splits cleanly into *forms* (one per (mnemonic,
operand-kind-tuple) pair) and *operand encoders* (one per operand kind).

### Per-form table

For each form: pattern (32-bit word with operand bits zero), mask (bits
that are fixed by this form), and an operand-slot list — for each slot,
its kind, bit position, and bit width.

Form lookup at parse time: given mnemonic + operand-kind-tuple, scan the
short list of forms for that mnemonic and pick the matching tuple.
Linear scan is fine — at most a handful of forms per mnemonic.

### Operand encoders

Most are trivial ("shift the value into the slot bits"). A handful are
non-trivial and live as small Z80 subroutines:

- `Xreg`/`Wreg`/`XregOrSp`/`WregOrSp` — 5-bit register index; trivial.
- `Imm12Shifted` — 12-bit unsigned with optional `lsl #12` shift bit;
  range check.
- `Imm16Shifted` — `movz`/`movk` 16-bit immediate with `hw` shift slot
  (0/16/32/48). Decomposing a wide immediate into a `movz`+`movk` chain
  is a parser concern, not encoder.
- `BranchImm26`, `BranchImm19`, `BranchImm14` — PC-relative offsets,
  divided by 4, sign-extension and alignment checks.
- `AdrpImm` — page-relative ±4GB, divided by 4096, split into
  `immlo:2 / immhi:19`. Bit-fiddly.
- `LogicalImm` — ARM's bitmask-immediate encoding (`N:1`, `immr:6`,
  `imms:6`). Not all integers can be encoded — invalid immediates must
  produce a clear error. Algorithm well-documented (LLVM's
  `processLogicalImmediate`); ~30–50 lines of Z80.
- `BitfieldImm` — `bfi`/`ubfx`/`bfxil` use `lsb`/`width` which translate
  to `immr`/`imms` differently per mnemonic. Per-form encoder.

### Generating the tables (Mac-side)

```
ARM MRA XML  ──┐
               ├─→ extract.py ──→ all-forms.json
filter list ──┘                       │
(our 76                                ▼
mnemonics)                       generate.py ──→ encoder_tables.bin
                                                 (loaded by Z80 at startup,
                                                  or embedded into its binary)
```

`extract.py` and `generate.py` live in `tools/`. They run rarely (when
the subset changes). Their output is **checked into git** so that
building the assembler does not require having ARM XML at hand.

## 5. Testing, validation & error reporting

### The oracle

For every test fixture, a plain-text `.s` file is converted into two
byte streams:

```
foo.s ─→ aarch64-none-elf-as foo.s -o foo.o          \
       ─→ aarch64-none-elf-objcopy -O binary foo.o A   \  byte-compare
                                                       /
foo.s ─→ text2bin foo.s foo.bin                       /
       ─→ samfile add → SimCoupé batch → samfile extract → B
```

If `A != B`, the assembler is wrong. The diff output shows side-by-side
hex with offset → mnemonic for the differing instructions.

**Constraint**: fixtures must be self-contained — no references to
externally-defined symbols — otherwise GNU `as` emits ELF relocations
that leave zero placeholders where our flat binary has real values.
Either keep all referenced symbols inside the fixture, or for
inter-fragment tests do `aarch64-none-elf-ld -Ttext=0` before objcopy.
Default is the former.

### Test layers

1. **Encoder unit tests (fast)** — a self-test harness *inside* the Z80
   assembler binary. Hardcoded list of `(mnemonic + operand tuple) →
   expected 4 bytes`. Asserts each in one SimCoupé invocation, writes a
   pass/fail summary file. Catches encoder-table and operand-encoder
   bugs without involving `text2bin`.

2. **Round-trip tests (medium)** — each fixture goes through the full
   text → bin → SimCoupé → bytes pipeline and is diffed against GNU
   `as`. Driver inside the SAM loops over `*.bin` on the test disk so
   one SimCoupé run covers many fixtures.

3. **Hardware spot-checks (slow, manual)** — periodically `samfile add`
   the assembler binary onto a real SAM disk, run on real hardware, and
   eyeball results. Not in CI. Catches anything SimCoupé fakes that real
   hardware does not (timing, edge-case I/O).

### Test corpus

- `tests/encoder_unit/` — table of `(text fixture, expected bytes)`
  driving layer 1.
- `tests/roundtrip/` — `.s` fixtures organised by encoding family
  (`add_imm.s`, `add_shreg.s`, `branch_cond.s`, `ldp_signed_offset.s`,
  …). Each fixture exercises one form intensively (many register pairs,
  edge-of-range immediates, every condition code, etc.).
- `tests/programs/` — small but realistic snippets borrowed from
  `~/git/spectrum4`. Catches *combinations* the per-form fixtures miss.

**Coverage target**: every form in the encoder table has at least one
round-trip fixture; every non-trivial operand encoder has dedicated
tests. `LogicalImm` in particular needs many — invalid immediates,
edge of run-length, every valid rotation.

### CI

GitHub Actions on every commit. Steps: install
`aarch64-none-elf-{as,objcopy,ld}`, install pyz80, install SimCoupé
(Linux build), install samfile, run pyz80 to build the assembler `.bin`,
run the layered test suite, report pass/fail. Target: under 2 minutes
for the full suite.

### Pre-work before any assembler code

Before writing the parser/encoder/symbol-table, build the test harness
end-to-end with a stub assembler that emits a fixed `0xd503201f` (one
`nop`). Verify: pyz80 builds it, `samfile` puts it onto an MGT image,
SimCoupé runs it, `samfile` extracts the output, the diff harness
reports a meaningful pass/fail. **De-risks the entire toolchain in a
day.** Then we iterate confidently.

### Error reporting

Two surfaces:

- **`text2bin` errors (Mac side)**: `file:line:col: message`.
  Examples: unknown mnemonic, operand kind mismatch, value out of range,
  undefined identifier in expression. Fail-fast at first error in v1;
  multi-error batching is a Phase 2 nice-to-have.
- **Z80 assembler errors (SAM side)**: given a binary input, the only
  possible errors are *symbolic* — undefined label, expression doesn't
  fit operand width, invalid logical immediate. Reported as
  `<offset>: message` with offset into the binary stream. The `bin2text`
  companion can later resolve the offset back to a source line for
  human consumption.

The Phase 2 editor will eventually subsume `text2bin`'s validation —
you cannot enter an invalid mnemonic because the editor only lets you
pick known ones. Phase 1 keeps validation in `text2bin`.

## Success criteria

Phase 1 is complete when:

1. The Z80 assembler runs in SimCoupé batch mode and produces correct
   output for every fixture in `tests/roundtrip/`.
2. The full layered test suite is wired into GitHub Actions and is
   green on every commit to `main`.
3. The encoder covers every mnemonic in the ~76-instruction subset,
   verified by both layer 1 and layer 2 tests.
4. A `tests/programs/` fixture taken verbatim from `~/git/spectrum4`
   round-trips byte-identical against GNU `as`.
5. The assembler binary, plus the encoder tables, fits in the memory
   layout above with measured headroom.

## Open items

- Confirm `:lo12:` / `:hi12:` / other PC-relative operators actually
  used by `~/git/spectrum4` (grep before designing the operand-encoder
  for them).
- Exact binary tokenised format design — done during implementation
  planning, informed by reading `reference/comet-decoded/comet.asm`.
- Whether encoder tables are embedded into the assembler binary or
  loaded from a separate disk file at startup. Trades binary size
  against load time. Decide during implementation planning.
