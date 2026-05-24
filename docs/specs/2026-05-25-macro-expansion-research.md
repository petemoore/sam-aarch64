# Design Note — Macro Expansion for `text2bin`

> Source: read-only research agent run 2026-05-25 against `origin/main`.
> Local-only (under gitignored `docs/superpowers/specs/`).

## Source tree pointers
- `text2bin` parser (origin/main): `tools/text2bin/internal/translate/parser.go`, `lexer.go`, `translate.go`
- Directive table: `tools/sam-aarch64-format/directives.go`
- spectrum4 macro defs: `~/git/spectrum4/src/spectrum4/kernel/macros.s` (32 defs, 344 lines), `~/git/spectrum4/src/spectrum4/kernel/entry.s` (4 defs, lines 9–66)
- target config: `~/git/spectrum4/src/spectrum4/targets/one-test-suite.s`

---

## 1. Current state of macro handling in `text2bin`

**There is no macro handling.** A repo-wide grep (`origin/main`, `tools/text2bin/`) for `macro`, `endm`, `altmacro`, `purgem`, `\.if`, `ifdef` yields zero hits. Similarly there is no `.include` handling — `text2bin` is strictly single-file.

The flow (`tools/text2bin/internal/translate/translate.go`):

```
src bytes → Lex → []Tok → Parse → records → format.WriteFile → .tbn
```

Every directive must be in the `format.DirectiveTable` allow-list (`tools/sam-aarch64-format/directives.go`, lines 4–17):

```go
var DirectiveTable = []string{
    ".text", ".data",
    ".byte", ".short", ".word", ".quad",
    ".ascii", ".asciz",
    ".equ", ".set",
    ".global",
    ".balign", ".org",
    ".skip", ".space",
    ".inst",
    ".align",
}
```

`parser.parseDirective` (`parser.go:87–119`) explicitly errors on anything not in that table:
```go
id, ok := format.DirectiveID(t.Text)
if !ok {
    return newErr(t.Pos, "unknown directive %q", t.Text)
}
```

So today any spectrum4 file containing `.macro`, `.endm`, `.if`, `.else`, `.endif`, or `.include` fails immediately with **`unknown directive`** — there is no silent skip. Likewise a macro *invocation* (e.g. `_strb 0x34, BORDCR`) parses as `unknown mnemonic` via `parseInst` (`parser.go:121–125`).

The only macro-like feature `text2bin` *does* implement is the spectrum4 pseudo-instruction `movl`, but it is hard-coded as a special case (`parser.go:138–145`) that expands inline to `movz` + `movk` — i.e. one specific macro is baked into the parser, not driven by user-supplied `.macro` definitions.

---

## 2. What spectrum4 uses

Survey of all 178 `*.s` files under `~/git/spectrum4/src/spectrum4/`:

| Subtree   | files | macro- or `.if`-affected |
|-----------|------:|--------------------------|
| `kernel/` |   16  |  8                       |
| `roms/`   |  118  |  5                       |
| `tests/`  |   34  | **32**                   |
| `demo/`   |    2  |  1                       |
| `libextra/`|   7  |  1                       |
| **Total** | **178** | **47 (26%)** |

The "two-thirds excluded" recollection in the prompt over-counts. The real headline is more pointed: **32 of 34 test fixtures are blocked**, while only 5 of 118 ROM source files are. So fixing macros mostly unblocks the *test corpus*, not the ROM bulk.

### Macro definitions
**36 definitions total**, all in two files (`kernel/macros.s` and `kernel/entry.s`). The full list (with arg names) is in `macros.s:6–344` and `entry.s:9–66`.

### Argument style
- **Only positional with names**: every macro is declared `.macro NAME a, b, c` and refs use `\a`, `\b`, `\c`. (GNU also supports calling these by name at the call site, but spectrum4 never does.)
- **No default values** (`name=default`), no `:req`, no `:vararg`.
- **No `\@` uniquifier**, and **no labels are defined inside macro bodies** at all — verified by awk-extracting every `.macro…\.endm` body. (The one apparent counter-example, `msgreg`'s `msg_\regname:` at `macros.s:341`, is itself emitted from within `.if UART_DEBUG`; it relies on token-pasting, not `\@`.)
- **Token-paste of arg into identifier**: yes. `macros.s:224` `adrp x0, msg_\reg`, `macros.s:225` `msg_\reg`, and `macros.s:341–342` `msg_\regname:` / `.asciz "\regname: "`. So substitution must work inside identifiers and inside string literals.

### Conditional assembly
- `.if`, `.else`, `.endif` only — **no** `.ifdef/.ifndef/.ifc/.ifeq/.ifne/.ifb`.
- 7 distinct condition symbols, all of the form `.if SYMBOL`: `UART_DEBUG` (38×), `ROMS_INCLUDE` (21×), `PCI_INCLUDE` (10×), `TESTS_INCLUDE` (5×), `TESTS_AUTORUN`, `ROMS_AUTORUN`, `DEMO_AUTORUN` (1× each).
- All symbols are defined by `.set` at the top of a target file, e.g. `targets/one-test-suite.s:6` `.set UART_DEBUG, 1`. Effectively boolean compile-time flags.
- `.if` appears **both inside macro bodies** (e.g. `macros.s:95` inside `log`, 9 sites in `macros.s` alone) **and at file scope** (e.g. `kernel/armregs.s:6 .if UART_DEBUG` … `:309 .endif` wraps the whole file).
- No `.if expr` with arithmetic: every condition is a bare symbol (truthy/falsy).

### Recursion / nesting
- **No recursion.** Mapped every "macro body → instruction" edge: no macro names itself, no cycles. The deepest nesting chain is `logregs → logreg → (mrs, mov, bl, …)` and `logregs → lognzcv → logarm` — two levels.
- Several nested chains exist: `_setbit → _setmsk`, `_resbit → _resmsk`, `*i → read_write_immediate`. So **expansion must be recursive in implementation**, but the input is bounded and acyclic.

### Advanced features NOT used
- No `.rept` / `.irp` / `.irpc` / `.endr`
- No `.altmacro` / `.purgem` / `.exitm`
- No `\@`, no `:req`, no `:vararg`, no default values

### Representative samples

**Small** (`macros.s:6–11`):
```
.macro _strb val, addr
  mov     w0, \val & 0xff
  adrp    x1, \addr
  add     x1, x1, :lo12:\addr
  strb    w0, [x1]
.endm
```

**Medium / nested** (`macros.s:77–84`):
```
.macro _setbit bit, address
  _setmsk (1<<\bit), \address
.endm

.macro _resbit bit, address
  _resmsk (1<<\bit), \address
.endm
```

**Complex** (`macros.s:217–237` — `.if`-guarded body + token-paste into ident & string):
```
.macro logarm reg
.if UART_DEBUG
  stp     x29, x30, [sp, #-16]!
  …
  adrp    x0, msg_\reg
  add     x0, x0, :lo12:msg_\reg
  bl      uart_puts
  mrs     x0, \reg
  …
.endif
.endm
```

---

## 3. Minimal viable subset

Distinguishing what is **must-have** to unblock real spectrum4 sources vs **later**:

### Must have (unblocks all 47 affected files)
1. **`.macro NAME a, b, c` … `.endm`**, positional named args only.
2. **Substitution of `\name`** in instruction operands, directive operands, identifiers (token-paste, e.g. `msg_\reg`), and string-literal contents (`"\regname: "`).
3. **Recursive expansion** of macro calls *inside* macro bodies (bounded, acyclic — but the expander must drive itself until no calls remain).
4. **`.include "file.s"`** with a search-path (probably the directory of the including file). 677 sites, no globbing, no conditionals around includes. This is unavoidable for any whole-spectrum4 build because of the `rom1.s`-style include chains.
5. **`.if SYMBOL` / `.else` / `.endif`** where SYMBOL is a `.set`-defined integer constant; truthy = non-zero. No `.if expr`, no `.ifdef`. This *is* used heavily (75 `.endif`s, 36 across `UART_DEBUG`) and gates the entire `armregs.s` file plus most of `macros.s`. Skipping it would require source-edits to spectrum4.

### Later (no current users in spectrum4)
- `\@` macro-instance uniquifier (zero call sites)
- `.rept` / `.irp` / `.endr` (zero)
- `.altmacro`, `.purgem`, `.exitm` (zero)
- `.ifdef`, `.ifndef`, `.ifc`, `.ifeq`/`.ifne` with arithmetic (zero — only bare `.if SYMBOL` is used)
- macro arg default values, `:req`, `:vararg` (zero)
- `\()` empty-substitution / explicit-end-of-name token-paste delimiter (zero — spectrum4 relies on non-ident chars naturally terminating `\reg` etc.)

This minimum is small enough that it can be specified informally and implemented incrementally — for instance, the `.if`-handling can land in a second commit after macro-substitution alone, because `.if`-guarded blocks at file scope (e.g. wrapping all of `armregs.s` in `.if UART_DEBUG`) can be worked around by ensuring `UART_DEBUG=1` is always set when assembling those files.

---

## 4. Suggested implementation approach

**Where it lives.** Add a pre-tokenisation source-transformation pass in front of `Lex` — call it `Preprocess` — so that by the time tokens reach the parser, all macros are expanded, all `.include`s are inlined, and all `.if` branches are resolved. Keep the parser unaware of macros entirely. Rationale: this matches GNU `as` semantics (macros are essentially text substitution, not syntactic constructs), keeps the existing `.tbn` byte format unchanged, and means existing parser unit-tests (`parser_directives_test.go`, etc.) don't need to learn macros.

Concretely: insert a new file `tools/text2bin/internal/translate/preprocess.go` with a `Preprocess(src []byte, path string, opts) ([]byte, error)` that runs *before* `Lex` in `translate.Translate`. It returns post-expansion source bytes ready to feed straight into `Lex`. Keep error positions trackable by either (a) carrying a synthetic position map alongside the expanded text, or (b) the simpler approach of emitting `# <line> <file>` cpp-style line directives at expansion boundaries and teaching `Lex` to consume them.

**Data structures.**
```
type macro struct {
    name string
    params []string                 // declared arg names
    body   []sourceLine             // raw lines between .macro and .endm
    defPos Position
}
type preprocessor struct {
    macros   map[string]*macro      // global table
    setvars  map[string]int64       // for .if conditions; seeded from prior .set
    incdirs  []string               // search path for .include
    expanding map[string]bool       // cycle detection (defence-in-depth)
}
```

The expander walks the input line-by-line. State machine: scanning vs collecting-macro-body vs skipping-false-`.if`-branch. For an identifier that matches a macro name, substitute `\paramN` → `argN` literally in each body line (a simple `strings.ReplaceAll`-with-word-boundaries, ordered longest-name-first to avoid `\addr` being shadowed by `\a`), then **recurse** by feeding the substituted body lines back through the same expander. Cycle-detection on the `expanding` set keeps recursive macros from looping forever (spectrum4 doesn't need this but a sane error beats a hang). `.set` directives encountered during preprocessing populate `setvars` so subsequent `.if SYM` can be evaluated.

**`.include` interaction.** `.include` is processed by the same pass — open the file relative to the including file's directory, recursively preprocess it, splice the result inline. This is also when the M0/M2 line-attribution problem becomes real, so either start with synthetic `# line file` markers from day one, or accept that error messages will report post-expansion line numbers until a follow-up. Given the existing pipeline has no `.include` at all, this is genuinely new mechanism.

**Interaction with the existing `movl` hard-coding.** Once user-defined macros work, the parser's special-case for `movl` (`parser.go:138–145`, `parseMovl()`) becomes redundant — `macros.s:88–91` defines `movl` itself. Either delete the special case once macro expansion is in, or leave it as a fast-path (it's harmless because `parser.go:80` checks built-in mnemonics before reaching `parseDirective`; if `text2bin` sees the post-expansion `movl` *call*, the inline-expander will have already turned it into `movz`+`movk`).

---

## 5. Spike test

**Pick: `~/git/spectrum4/src/spectrum4/tests/test_print_w0.s` (38 lines).** It is the *smallest* macro-using file in the entire tree and uses exactly one macro call (`_str fake_channel_block, CURCHL` at line 15).

To make this file pass through the existing M2/M3 pipeline, the minimal-subset implementation must handle:

1. **Pre-expansion source must include `macros.s`** — either via an `.include "macros.s"` prepended to the input, or by treating `kernel/macros.s` as an implicit prelude when assembling spectrum4 tests. (test_print_w0.s itself has no `.include`.)
2. **Parse `.macro _str val, addr` … `.endm`** (the 6-line definition at `macros.s:37–42`).
3. **Expand the call `_str fake_channel_block, CURCHL`** to the 4-line body, substituting `\val` → `fake_channel_block` and `\addr` → `CURCHL` everywhere:
    ```
    ldr     x0, =fake_channel_block
    adrp    x1, CURCHL
    add     x1, x1, :lo12:CURCHL
    str     x0, [x1]
    ```
4. **Already supported downstream**: `ldr xN, =sym` (literal pool), `adrp`, `add :lo12:`, `str` — confirmed by `docs/notes/spectrum4-status.md` (rom1_full.gen-s already round-trips). External symbols (`fake_channel_block`, `CURCHL`) become unresolved at refenc time, which is the same fundamental limitation already documented for ROM assembly — expected.

What this spike does **not** exercise (and that's the point — it isolates the macro-expander):
- No `.if` (the `_str` body has none)
- No nested macro calls (`_str` calls no other macro)
- No token-pasting (`\val`/`\addr` appear in isolation, not concatenated into identifiers)
- No `.include` (the file is self-contained once `macros.s` is appended)

Once that file passes, the natural next spike is `tests/test_temps.s` (49 lines) which adds nested expansion (`_setbit → _setmsk`) and lets the second-layer recursion get exercised. After that, `kernel/macros.s` itself (already needs `.if UART_DEBUG`) plus `kernel/armregs.s` (whole-file `.if` gate) cover conditional assembly. Those three steps in order would cleanly validate the must-have subset.
