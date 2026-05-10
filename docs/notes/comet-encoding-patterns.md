# COMET Source Encoding Patterns — M1 Brainstorm Reference

**Purpose:** Reference material for the M1 aarch64 binary tokenised source format
brainstorm. Describes what patterns COMET uses, with concrete byte-level evidence.
Does not propose a design for our format.

**Sources analysed:**
- `/reference/comet-disk/comet   .s` — 56,949-byte binary tokenised file (4,903 lines)
- `/reference/comet-decoded/comet.asm` — detokenised source (4,903 lines)
- `comet2txt.py` — Simon Owen's detokeniser; reading it backwards is the fastest
  format spec
- `comet_v1-3_manual.pdf` — user manual; useful for understanding editor entry
  conventions

---

## TL;DR

COMET stores source as a flat sequence of length-prefixed line records, each
null-terminated, with no global header beyond a single leading `0x00` byte.
Within each record, Z80 keywords (mnemonics, register names, conditions,
directives) are replaced by single-byte tokens in the range `0x80–0xE5` (102
tokens total). Everything else — operand text, label references, hex literals,
expressions, comment text — is stored as raw ASCII. Numeric literals are stored
in their source form (`&AF`, `16384`, `%10110`), not pre-evaluated. Labels at
the definition site are stored as a length-prefixed ASCII run without a colon;
labels referenced in operands or expressions are stored as plain ASCII
substrings, with no interning or index. Comments are stored inline after a `0x3B`
(`;`) byte, followed by a one-byte column-indicator, then the comment text to
end-of-record. Whitespace is discarded entirely at save time; the display engine
reconstructs fixed three-column layout (col 0–14: label, col 15–19: mnemonic,
col 20+: operands) from the binary on every render.

---

## Q1: Record Framing

**Pattern: length-prefixed, null-terminated, variable-size per-line records.**

The file opens with a single `0x00` byte (file-start sentinel). Every subsequent
record is:

```
[len: 1 byte][payload: len-1 bytes]
```

The `len` byte counts itself. The final byte of every payload is `0x00` (end-of-line
terminator). An empty line is two bytes: `02 00`. The file ends with a `00` byte
(len=0 = end-of-file sentinel).

**Evidence:**

`comet2txt.py` lines 97–100:
```python
line_len = data[0]
line_data = data[1:line_len]
data = data[line_len:]
```

Verified by parsing the binary: 4,903 records totalling 56,948 bytes match exactly
(plus the leading `0x00` sentinel = 56,949 bytes total). The maximum observed
`line_len` in this file is `67` (66-byte payload). There is no fixed-size cap in the
file format itself — `len` is a single byte, so the theoretical maximum payload is
254 bytes.

**No per-line header beyond the length byte.** There are no line numbers, no address
fields, no type/kind bytes. The format is deliberately minimal.

---

## Q2: Mnemonic Encoding

**Pattern: single-byte tokens for all recognised keywords, 0x80-based, 102 tokens
total.**

`comet2txt.py` defines `token_base = 0x80`. The token table
(`tokentab`, `comet.asm` line 4740) has 102 entries, terminated by `0xFF`. Token
values run from `0x80` (A) through `0xE5` (Z), leaving `0xE6–0xFF` unused in the
token byte range.

The 102 tokens cover:
- **Register names:** A, B, C, D, E, H, L, I, R, AF, BC, DE, HL, IX, IY, SP
- **Condition codes:** Z, NZ, C, NC, M, P, PE, PO
- **Mnemonics:** all Z80 instructions plus COMET-specific directives (DEFB, DEFM,
  DEFS, DEFW, EQU, ORG, DUMP, LIST, MDAT)

**Token matching is case-insensitive** (`comet.asm` line 2476: `RES 5,C` to
clear bit 5 = uppercase mask) and **prefix-safe** (the match algorithm in
`comprline`/`findtoken`, lines 2470–2503, marks the final character of each
keyword with bit 7 set in `tokentab` and verifies that no alphanumeric character
follows the matched keyword — preventing `DEFB` matching inside `DEFBAD`).

**Z80 register-name tokens and mnemonics share the same token space.** `A` = 0x80,
`LD` = 0xB1, `HL` = 0xA3. The assembler's `expr`/`exprr`/`expind` functions
(lines 1417–1500) disambiguate context by which token value appears at each
position — this logic is at assembly time, not at save time.

**Operand-form disambiguation is not encoded in the binary.** `LD HL,&1234` vs
`LD (HL),A` are distinguished at assembly time by parsing the ASCII that follows
the `LD` token. The binary stores `[0xB1][0xA3][TEXT ',&1234']` vs `[0xB1][TEXT
'('][0xA3][TEXT '),'][0x80]` — i.e., the parentheses are inline ASCII.

---

## Q3: Operand Encoding

**Pattern: operands are stored as a mix of tokens (for register/condition names)
and raw ASCII (for everything else — immediates, parentheses, commas, operators,
label references).**

Verified by annotating the binary (Python decode script, 30 representative lines):

```
textmark EQU &AF  ; ...
→ [LABEL "textmark"] [TOK EQU] [TEXT "&AF"] [COMMENT ...]

LD HL,&511F
→ [TOK LD] [TOK HL] [TEXT ",&511F"] [END]

LD (asspage-oflo),A
→ [TOK LD] [TEXT "(asspage-oflo),"] [TOK A] [END]

IN A,(250)
→ [TOK IN] [TOK A] [TEXT ",(250)"] [END]

EX AF,AF'
→ [TOK EX] [TOK AF] [TEXT ","] [TOK AF] [TEXT "'"] [END]
```

The `fithex` function (`comet.asm` lines 2538–2551) strips internal spaces from
hex literals and upper-cases digits during compression, but stores the result as
ASCII text starting with `&`. From the binary: `&511F`, `&BA00`, `&0112`, `&5A4D`
all appear as ASCII. There is no binary encoding of numeric values.

**Immediates are not pre-evaluated.** `EQU 16384` stores `TEXT "16384"`, not bytes
`00 40`. Expression strings like `start-oflo`, `temp2-ofhi`, `messend-message` are
all stored verbatim as ASCII substrings (`comet.asm` line 1957 comment: "Hex
numbers are not tokenized if there are spaces inside number" confirms the
text-preservation design intent).

---

## Q4: Identifiers / Labels

**Pattern: labels at definition site are length-prefixed ASCII runs (no colon stored);
label references in operands are plain ASCII substrings with no interning.**

**Definition site:** A label at the start of a line is stored as a 1-byte length (1–14)
followed by the ASCII characters. There is no trailing `:` in the binary.
`comet2txt.py` lines 52–57 reconstruct the colon on output.

```
0x08 "textmark" → label length=8, chars "textmark"
0x04 "oflo"     → label length=4, chars "oflo"
```

The label-length byte occupying `0x01–0x0E` is how the parser distinguishes labels
from tokens (`0x80+`) and printable ASCII (`0x20+`). Maximum label length is
hardcoded at 14 (`max_label_len = 14` in `comet2txt.py`; `CP 15 / JP NC` at
`comet.asm` line 361).

**Reference site:** Any occurrence of a label name in an operand or expression is
stored as part of the inline ASCII text stream. From the binary:
`"(asspage-oflo),"`, `"start-oflo"`, `"free"`, `"freesearch"` are all naked ASCII.
There is no symbol table in the binary format, no index reference, no interning.

**Forward references:** fully supported at assembly time (two-pass assembler,
`comet.asm` lines 767–770 handle the unresolved case), but the binary format does
nothing special to flag them — they are just ASCII label names like any other.

---

## Q5: Comments

**Pattern: inline semicolon marker followed by a one-byte column indicator, then raw
ASCII text to the end of the record (the record's `0x00` terminator doubles as
comment terminator).**

The encoding is:
```
0x3B  ; (semicolon — starts comment)
0xNN  ; column indicator: number of spaces to pad before ';' + 1
      ;   (so 0x01 = column 0, 0x0A = 9 spaces before ';', etc.)
...   ; comment text as raw ASCII
0x00  ; end-of-record (shared with line terminator)
```

`comet2txt.py` lines 40–44:
```python
elif data[0:1] == b';':
    pad_len = data[1] - 1
    out_line += ' ' * pad_len + ';'
    out_line += data[2:-1].decode("ascii", errors="ignore")
    data = data[-1:]
```

Observed from the binary: column indicator values range from `0x01` (comment at
column 0 = pure comment line) through `0x15` (20 spaces before `;`). The
`comprline`/`testremark` code (`comet.asm` lines 2578–2601) counts trailing spaces
before the `;` position and stores that count+1 as the column byte.

**There is no length prefix on comment text.** The comment runs to the record's
`0x00`. This means a comment always consumes the rest of its record — a line
cannot have anything after a comment (which is correct for assembler syntax).

**There is no max comment width enforced in the format.** The editor's 64-column
display is a UI constraint, not a format constraint. The 254-byte per-record
maximum is the only hard limit.

---

## Q6: Expressions

**Pattern: stored as tokenised infix source text — operators and operands preserved
as ASCII, with register/keyword names replaced by tokens. Not evaluated at
edit/save time.**

The assembler's expression evaluator (`getnum`, lines 3309–3401; `calcloop`, lines
3482–3496) is invoked at assembly time, not at save time. At save time, the
`comprline` function only replaces recognised keywords with token bytes and passes
everything else through as ASCII.

Operators supported (from `calcloop`, `comet.asm` lines 3482–3496):
`+`, `-`, `*`, `/`, `\` (mod).

Special expressions observed in binary:
- `$` (current PC) — stored as ASCII `$`
- `$-3` — stored as ASCII `$-3`
- `asspage-oflo` — label arithmetic, stored as ASCII
- `messend-message` — stored as ASCII
- `(250)` — parenthesised port address, stored as ASCII including parens
- `start-oflo-ofhi` — stored as multi-operator ASCII expression

**No RPN or tree encoding.** Expressions are infix text exactly as the user typed
them (minus internal whitespace in hex literals, which `fithex` compacts).

---

## Q7: Whitespace and Layout

**Pattern: whitespace is entirely discarded at save time; layout is reconstructed
from fixed column positions at display time.**

`comprline` (`comet.asm` lines 2442–2574) calls `skipspaces` before processing
each element and does not emit any whitespace byte. `explinehl` (`comet.asm` lines
2602–2698) reconstructs the display using two hardcoded indent positions:

- After the first token (mnemonic), it pads to column 20 (`f'{out_line:20s}'` in
  `comet2txt.py` line 69)
- If a label is present, the column after the label is padded to 15
  (`f'{out_line:15s}'` in `comet2txt.py` line 62)

The manual confirms: "spaces are removed and the line will be automatically be
tabulated to give a neat impression on the screen."

**The comment column indicator is the one exception.** The `0x3B` `pad_byte`
sequence encodes the number of spaces before `;`, preserving the alignment the
user chose for that comment. This is a single-byte encoding of column position,
not stored whitespace characters.

**There are zero whitespace bytes in any line payload.** Searching the binary
confirms no `0x20` bytes appear in any record payload — they are all stripped
during `comprline`.

---

## Q8: Pre-evaluated Literals

**Pattern: literals are stored as their source-text form, not pre-evaluated to
numeric values.**

- `EQU &AF` → stored as ASCII `&AF` (2 bytes), not `0xAF` (1 byte)
- `EQU 16384` → stored as ASCII `16384` (5 bytes), not `00 40` (2 bytes)
- `EQU 32768` → stored as ASCII `32768` (5 bytes)
- `CALL &0112` → stored as ASCII `&0112`
- `LD HL,&511F` → stored as ASCII `,&511F`

The `fithex` function (`comet.asm` lines 2538–2551) does perform one
transformation: it strips any spaces that the user typed inside a hex literal
(e.g., `& AF` → `&AF`) and uppercases the digits. The resulting canonical form
is still ASCII text, not a binary number.

Decimal and binary literals (`%10110`) are stored exactly as typed.

---

## Design Implications for Our aarch64 Format

### 1. Record framing — KEEP the per-line, length-prefixed approach; ADAPT the size

**Keep:** Per-line records are the right unit for an assembler source format.
They map naturally to the user's mental model, enable O(n) traversal, and allow
efficient in-place editing. COMET proves the approach scales to 4,900-line files
in 57 KB.

**Adapt:** COMET's 1-byte length field limits records to 254 bytes of payload. An
aarch64 line with a long label, a 4-operand instruction, a full-width comment,
and possibly a `:lo12:label_with_underscores+offset` expression can legitimately
exceed that. Use a 2-byte little-endian length field, giving 65,534 bytes per
record — effectively unlimited in practice.

**Reject:** The trailing null terminator. If the length field is accurate, the null
terminator adds a byte of redundancy per line (4,903 redundant bytes in COMET's
file). Prefer either pure length-prefixing (no terminator) or a sentinel, not both.

### 2. Mnemonic encoding — REJECT single-byte token space; ADAPT the concept

**Reject:** aarch64 has far more than 128 distinct mnemonics, and that is before
counting the variant forms that COMET lumps together (e.g., COMET uses a single
`LD` token and disambiguates at assembly time). A preliminary count of aarch64
base-ISA mnemonics exceeds 300; adding SIMD/FP and system instructions pushes
well past 500. A 1-byte token with a 0x80 base gives 128 slots — not enough.

**Adapt:** Keep the high-bit sentinel idea (values ≥ 0x80 are tokens; values < 0x80
are ASCII or structural bytes). Use two-byte tokens: a marker byte (e.g., 0xFF)
followed by a token index byte, giving 256 tokens in the base range and allowing
extension. Alternatively, use a different bit partitioning — e.g., reserve 0xE0–0xFF
for a 5-bit structured token with a following index byte. The exact scheme is for
M1 to decide; the point is that the aarch64 token space does not fit in 7 bits.

**Keep:** The concept of tokenising only well-defined keyword strings (mnemonics,
register names, conditions, shift types) and leaving everything else as ASCII.
This is the right tradeoff between complexity and fidelity: the tokeniser is a
simple prefix-matching scan of a fixed table, not a full parser.

### 3. Operand encoding — KEEP inline ASCII for operands; ADAPT for aarch64 structure

**Keep:** Storing operands as mixed token + ASCII is correct. Commas, parentheses
(or in aarch64, brackets), and operators are cheap ASCII bytes. Trying to
pre-encode operand structure would cost more complexity than it saves.

**Adapt for aarch64-specific syntax:** COMET's operand stream has no analogue for:
- Bracket addressing: `[x0, #16]` — COMET uses `(HL)` but the bracket and comma
  are plain ASCII, so this already works without any format change
- Shifted-register operands: `x1, lsl #2` — `LSL` would be a token; `, lsl #2`
  would encode as [TEXT `,` ][TOK LSL][TEXT ` #2`]. This is fine.
- Extension operands: `x2, uxtb #1` — same treatment as shifted registers.
- Relocation operators: `:lo12:label` — no COMET analogue; these would need to
  be stored as ASCII (`:lo12:` is not a keyword) or get a small set of
  relocation-operator tokens if they appear frequently.
- Predicate suffixes on condition codes: aarch64 uses `eq`, `ne`, `lt`, etc. as
  bare words in condition positions. These are natural token candidates.

**Reject:** Pre-encoding addressing modes as a structured mode byte (like some
RISC assembler formats do). The inline ASCII approach is simpler, supports
arbitrary expressions, and is losslessly round-trippable.

### 4. Identifiers / labels — KEEP the length-prefixed label at definition; KEEP inline ASCII at reference

**Keep:** The length-prefixed label at the definition site is clean and
self-describing. It means the parser can skip over a label in O(1) without
scanning for a `:` terminator.

**Keep:** No symbol-table interning in the source binary. Interning complicates the
format (requires a name table section), makes partial edits expensive (name table
must be updated), and is unnecessary — the assembler builds its own symbol table
at assembly time. The source format is not an object file.

**Adapt:** COMET's 14-character label limit is a tight constraint. aarch64 assembly
commonly has labels like `__start_of_kernel_text`, `system_call_fastpath`, etc.
Raise the limit substantially — 63 or 127 characters is a natural fit for a 6- or
7-bit length field in the label record.

### 5. Comments — ADAPT the column indicator idea; REJECT the implicit-length scheme

**Adapt:** COMET's column indicator (pad byte after `;`) is clever and compact —
one byte encodes alignment. However, the value `pad_len + 1` means column 0
encodes as `0x01`, and the maximum column is 254. For aarch64 files with longer
lines, a two-byte column offset would be safer.

**Adapt:** Store the comment column relative to the start of the line, not as spaces
to pad. COMET's scheme effectively records how many spaces were between the last
token and the `;`, which is display-oriented. A column offset from line start is
more useful for `bin2text` reconstruction with variable-width renders.

**Reject:** The implicit termination by end-of-record. If records have a proper
length field, comment length is derivable from (record end - comment start). This
is fine and we should keep it. But if any future version of the format needs
something after the comment (e.g., a structured source annotation), COMET's design
has no room for that. Consider a comment-length prefix if extensibility matters.

### 6. Expressions — KEEP unevaluated infix text

**Keep:** Storing expressions as unevaluated ASCII is the right choice for a source
editor format. Pre-evaluation would break edits that change label values
(invalidating already-stored constants), prohibit forward references in constant
expressions, and lose the user's intent (was it `256` or `&100` or `1 << 8`?).

**Adapt:** COMET supports `+`, `-`, `*`, `/`, `\` (mod). aarch64 assemblers
conventionally add `|`, `&`, `^`, `~`, `<<`, `>>`. These are all ASCII and flow
naturally through the existing "store as text" design. No format change needed;
just document the set of ASCII operators the assembler will evaluate.

### 7. Whitespace and layout — REJECT normalisation at save time

**Reject:** COMET's whitespace normalisation breaks round-trip fidelity. If the
user writes `LD   HL , &1234` with idiosyncratic spacing, the binary stores the
canonical form `LD HL,&1234` (reconstructed by `explinehl` with fixed tab stops).
There is no way to recover the original spacing from the binary — it is lost.

For our format's `bin2text` requirement, we need lossless round-trip. This means
we must preserve either:
(a) the exact whitespace the user entered (simple: store it), or
(b) a canonical normal form and accept that `text2bin` + `bin2text` produces
    normalised text rather than the literal original.

Option (b) is more compact and is what COMET does. It is acceptable if the design
spec says "normalised text is the canonical form." It is not acceptable if the
spec says "bin2text must reproduce the exact file the user saved."

COMET's three-column fixed-tab layout (label at 0, mnemonic at 15, operands at
20) is Z80-specific (the operand column aligns well for 2-operand Z80 instructions).
For aarch64 with up to 4 operands and longer register names (`x0`–`x30`, `v0`–`v31`),
fixed tab stops at those specific columns will not look right. Either choose
different column positions, make them configurable, or use a single-space separator
between mnemonic and operands.

### 8. Pre-evaluated literals — KEEP text form; ADAPT for lossless round-trip

**Keep:** Storing `&AF`, `16384`, `%10110`, `1 << 8` as ASCII text preserves the
user's intent and supports lossless `bin2text`. Pre-evaluation loses that intent
and complicates re-editing.

**Keep (with caution):** COMET's hex-literal normalisation (strip internal spaces,
uppercase digits: `& a f` → `&AF`) is reasonable. A `text2bin` pass that
normalises case and removes internal spaces in hex literals is not a loss of
semantics. Document it explicitly so `bin2text` output is reproducibly canonical.

**Adapt for aarch64 literal syntax:** aarch64 uses `#imm` notation for immediates
inside instruction operands (`LDR x0, [x1, #8]`). The `#` character is plain ASCII
and needs no special treatment. However, aarch64 also has:
- Floating-point literals in SIMD: `FMOV d0, #1.0` — these are ASCII decimal
  floats, fine to store as text.
- Shifted immediates: `ORR x0, x0, #0xFF00` — fine as ASCII.
- The `adrp`/`add` pair with `:lo12:` / `:hi12:` relocation operators — ASCII.

None of these require format-level encoding; all are naturally handled by the
"store as text, tokenise only keywords" approach.

---

## What to Skip / Pitfalls of Digging Deeper

**Skip: the Z80 token assignments themselves.** COMET's 102 tokens are an ordered
list of Z80-specific names. None of them (A, BC, HL, NZ, DJNZ, …) apply to
aarch64. Studying which token got which byte value (e.g., LD = 0xB1) is
completely irrelevant. The ordering principle (alphabetical) is obvious from the
table; no further study needed.

**Skip: the symbol table structure at assembly time.** COMET maintains a runtime
symbol table in the same memory workspace as the source (growing downwards from
the top of the source region, `comet.asm` lines 4858–4861). This is an in-memory
assembly-time structure, not part of the binary format. The binary format does not
include any symbol table section. The assembler rebuilds the symbol table on every
pass. We will do the same.

**Skip: the opcode dispatch table (`opcotab`).** This is 102 entries × 3 bytes of
Z80-specific opcode bytes and service-routine pointers. It is the assembler's
backend, not the source format. It is entirely Z80-specific and has no structural
lessons for aarch64 encoding.

**Skip: the two-pass assembly logic, relocation, page-bank switching.** All
Z80/SAM-specific. The interesting lessons (how the assembler resolves forward
references, how it handles multi-pass) are generic assembler theory, not
format-specific. Not worth more study time from COMET for M1.

**Skip: the editor key handling and display engine beyond what's documented
above.** The editor logic (cursor movement, block commands, swap markers, insert
mode) is all Z80/SAM-specific and adds no insight into the format.

**Note: COMET cannot losslessly round-trip.** By design, it normalises whitespace
and discards spacing on save. `comet2txt` output is always the canonical fixed-tab
form, not the original file the user typed. If our `bin2text` must reproduce the
original `.s` file exactly, COMET's model is the wrong reference for that
requirement. We either need to store whitespace, or explicitly define "normalised
text is canonical." COMET chose the latter; for M1 we should consciously choose
one or the other rather than inheriting COMET's choice by default.
