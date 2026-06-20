# On-SAM macro / preprocessor assessment (i209 → q38 input)

**Purpose.** This is the i209 assessment report Pete asked for to decide q38: whether
to pursue an on-SAM preprocessor (item i31 — `.if` build-constraints + macros) and at
what scope. It is **scope-and-cost only** — it does not implement i31 and changes no
assembler / preprocessor code. The headline question q38 hinges on is *"does on-SAM
macro support slip in to the existing design, or does it blow up assemble-time
performance / program size / force a redesign?"* The short answer is in the closing
section: **it slips in** — a clean, bounded front-end text pass sitting in front of the
already-built on-SAM lexer (`src/asmlex.asm`), at an estimated **~2–3.5 KB of Z80**
with **zero impact on the assemble (`.tbn` → bytes) hot path** and **no IR / reader /
two-pass redesign**. The dependency that actually matters is sequencing, not risk: the
preprocessor only becomes *useful* once the on-SAM **text → `.tbn`** editor path
(i48c / B8) lands, because that is the path it feeds.

---

## 1. What the host preprocessor supports today (the authority)

The authority is `tools/sam-aarch64/frontend/preprocess.go` (660 lines), a
GNU-`as`-style **text-substitution pass that runs in front of the lexer**. The data
flow is strictly linear:

```
source text → Preprocess → Lex → Parse → in-memory symbolic IR (→ Compact → .tbn)
```

wired in `frontend/translate.go:17-25` (`TranslateWithOptions` calls
`Preprocess` then `Lex` then `Parse`) and exposed standalone as `sam-aarch64 -E`
(`tools/sam-aarch64/main.go:115-119, 215-219`, "preprocess only: emit expanded
source… like cpp -E"). Preprocessing produces **expanded source text**, not tokens or
IR — it is purely text→text. The lexer then consumes that text; the only coupling is
cpp-style line directives `# <line> "<file>"` that `Preprocess` emits at file/macro
boundaries (`preprocess.go:260-262`) and the lexer recognises at line-start to keep
error positions pointing at original source (`lexer.go:124-139`).

Supported constructs (from `preprocess.go`, header comment lines 3-41 and the
implementation):

| Construct | Where implemented | Semantics |
|---|---|---|
| `.include "file"` | `tryParseInclude` (421-433), `handleInclude` (470-492) | Inlines a file, searched in the including file's dir then `-I` dirs; recurses through `processFile`. |
| `.macro NAME a, b, c` … `.endm` | `parseMacroHeader` (267-298), `collectMacroBody` (304-317) | Positional named params; body stored as raw lines; nested `.macro` is an error (as in GNU as). |
| macro **invocation** | `tryExpandMacroInvocation` (502-555) | First word matching a known macro name; args comma-split with paren-aware splitting (`splitMacroArgs`, 569-599); arity-checked; recursion-cycle-guarded; body re-preprocessed after substitution. |
| `\param` substitution | `buildSubstituter` (612-660) | Purely textual; substitutes inside identifiers (token-paste, `msg_\reg`) and inside string literals; longest-param-first; `\\` is a literal backslash. |
| `.if SYMBOL` / `.else` / `.endif` | `processLines` switch (133-174), `evalIfCondition` (321-340) | **Bare-symbol-only** conditional: truthy iff a prior `.set` gave it a non-zero integer; unknown ⇒ 0/false (GNU-compatible); nesting via a frame stack. **No `.elseif`, no expression conditions.** |
| `.set NAME, INT` | `tryParseSet` (360-380) | Captured for `.if` evaluation (literal `0x/0b/decimal` only); the line **also passes through** so the parser still sees the original `.set`. |

**Explicitly out of scope host-side** (`preprocess.go:39-41`, *"no spectrum4
callers"*): `\@` uniquifier, `.rept`/`.irp`/`.endr`, `.altmacro`, `.purgem`,
`.exitm`, `.ifdef`/`.ifndef`/`.ifc`, macro default values, `:req`/`:vararg`, and
`.if <expression>`. This is the crucial scoping fact: **the host authority is already
the minimal subset Pete's sources need** (next section confirms it), so an on-SAM port
is a port of *this* small surface, not of GNU `as`.

---

## 2. What Pete's real sources actually use

Survey of the Mac-side aarch64 sources at `~/git/spectrum4/src/spectrum4/**/*.s`
(the real-hardware kernel Pete develops with GNU `aarch64-none-elf-as`). The build
invokes `as` **directly with no cpp pass** (`~/git/spectrum4/src/spectrum4/targets/Tupfile:12`,
`$(AARCH64_TOOLCHAIN_PREFIX)as $(ASFLAGS_AARCH64) -o %o %f`); `ASFLAGS` carries only
`-I` include dirs (`-I ..`, `-I ../kernel`, `-I ../roms`, …) and **no `-D` defines** —
every conditional symbol (`UART_DEBUG`, `PCI_INCLUDE`, `TESTS_INCLUDE`, …) is set in
source via `.set`. So GNU-as directives are the entire preprocessor surface; there is
**no cpp-style `#`-directive layer to worry about**.

| Construct | Count | Load-bearing? | Example citation |
|---|---|---|---|
| `.macro` / `.endm` definitions | 33 | **yes** | `~/git/spectrum4/src/spectrum4/kernel/macros.s:6` `.macro _strb val, addr` |
| macro **invocations** (total) | ~3,700+ | **yes (heavy)** | `~/git/spectrum4/src/spectrum4/kernel/font.s:20` `_hwordbe 0b…` (1568 calls of `_hwordbe` alone) |
| `.if` (all bare-symbol) | 77 | **yes** | `~/git/spectrum4/src/spectrum4/kernel/macros.s:95` `.if UART_DEBUG` |
| `.else` | 23 | yes (paired) | `~/git/spectrum4/src/spectrum4/kernel/kernel.s:1062` |
| `.endif` | 77 | yes | matches `.if` |
| `.include` | 677 | **yes (critical)** | `~/git/spectrum4/src/spectrum4/kernel/kernel.s:29` `.include "macros.s"` |
| `.set` | 83 | **yes** | `~/git/spectrum4/src/spectrum4/kernel/kernel.s:17` `.set SCREEN_WIDTH, 1920` |
| `\param` substitution | 98 | **yes** | `~/git/spectrum4/src/spectrum4/kernel/macros.s:7` `mov w0, \val & 0xff` |
| `.ifdef` / `.ifndef` / `.ifc` / `.ifeq` | 0 | unused | — |
| `.elseif` | 0 | unused | — |
| `.equ` | 0 | unused | — |
| `.rept` / `.irp` / `.irpc` | 0 | unused | — |
| `\@` uniquifier | 0 | unused | — |
| `:req` / `:vararg` / default params | 0 | unused | — |
| cpp `#`-directives | 0 | unused | — |
| `.if <expression>` | 0 | unused | all `.if` are bare symbols |

The 33 defined macros, by call frequency: `_hwordbe` (1568), `_pixel` (388),
`_strhbe` (368), `msgreg` (302), `_strb` (250), `strwi` (83), `_resbit` (76),
`_str` (56), `logarm` (47), `_setbit` (44), `nzcv` (42), `ldrwi` (35), `logreg` (31),
`strhi` (24), `movl` (22), `ventry` (16), `handle_invalid_entry` (15), and a long tail.

**Conclusion: the host-supported subset and Pete's actual usage are the same set.**
Every construct Pete leans on is already implemented host-side; everything `preprocess.go`
declared out-of-scope has **zero** uses in his sources. An on-SAM preprocessor that
mirrors `preprocess.go` exactly preserves Pete's workflow with no feature gap.

---

## 3. SAM-side integration point + budget impact

### 3.1 The critical architectural fact: the SAM does not assemble text today

Today the SAM assembler **never sees source text**. It reads a pre-tokenised **`.tbn`**
file (`docs/ARCHITECTURE.md` §6; `src/reader.asm`) whose macros / `.if` / `.include`
were **already fully resolved host-side** by `Preprocess` when the `.tbn` was created.
The on-disk `.tbn` is *post-preprocessor output*. So "on-SAM macros" is **not** a change
to the assemble path (`reader.asm` → two-pass → encoder); that path consumes already-expanded
material and is entirely unaffected.

"On-SAM macros" means: when Pete authors/edits source **text on the SAM**, the
text → `.tbn` conversion must run a preprocessor. That text path is the **i48c / B8
editor strand**, already under construction:

- `src/asmlex.asm` (the SAM-side **tokenizer**, a faithful Z80 port of `lexer.go`;
  built standalone to `build/asmlex.bin` = **1732 bytes**) — i48c lexer **COMPLETE**.
- `src/asmparse.asm` (the SAM-side **parser**, port of `parser.go`; `build/asmparse.bin`
  = 16159 bytes) — i48c parser front-end **COMPLETE**.
- These are **not** `include`d in `assembler.asm` (confirmed: no `asmlex`/`asmparse`
  reference in `src/assembler.asm`); they are built and verified separately under the
  netboot-oracle Z80 harness (`tools/netboot-oracle/z80/asmlex_test.go`,
  `asmparse_test.go`). They are the editor-input pipeline, distinct from the
  `.tbn`-assemble pipeline.

So the integration point is exact and already designed-for: an on-SAM `Preprocess`
sits **in front of `asmlex`**, mirroring the host's `Preprocess → Lex → Parse`. The
`asmlex.asm` header even anticipates this — *"cpp line-directives are out of scope (a
preprocessor artifact, like the host Preprocess pass — a line-start '#' lexes as a line
comment)"* (`src/asmlex.asm:14-17`). The on-SAM preprocessor is the missing brick that
produces the expanded text (and `# line` directives) that `asmlex` already knows how to
consume.

### 3.2 Where the code and buffers live (budget)

Because the preprocessor is part of the **editor / text-input** subsystem, not the core
`.tbn` assembler, it does **not** compete for the ultra-tight test-variant `&C000`
budget (the test variant of `assembler.bin` has only **20 B** of headroom to the
`&C000` stack-page cliff — `tools/check-code-budget.sh` reports `code_end &BFEC`).
Two reasons it avoids that cliff:

1. The preprocessor is **editor-path code**, the same family as `asmlex`/`asmparse`,
   which already live **off-axis** (separate binaries on physical pages, reached by
   paging — `docs/ARCHITECTURE.md` §2.1; `src/README.md`), not inside the `&8000–&BFFF`
   section-C core. An on-SAM preprocessor would be packaged the same way (its own
   off-axis payload paged in for the text→`.tbn` operation), so it does not touch the
   core assembler's section-C budget at all.
2. Even were it counted against the production core, the **prod variant has 4943 B of
   `&C000` headroom** (`check-code-budget.sh`: `prod code_end &ACB1`) — comfortably more
   than the whole engine's estimated size (§5). The 20-B test-variant figure is a
   self-test artefact, not the production ceiling.

The one genuine constraint to respect is the **scratch-table collision**: the parser
and assembler scratch tables overlap in `&C100–&EFFF` (ROADMAP B8 note; relates to
q36 / the IDE memory model in `docs/specs/ide-memory-model-design.md`). A preprocessor
needs **expansion buffers** (the macro table, the substituted-line buffer, the `.if`
stack, the `.set`-value table). These are text buffers measured in low single-KB and
should be allocated from the **i2 page pool / IDE memory model**, *not* carved out of
the `&C100–&EFFF` scratch region — the same discipline B8d/editor already has to
follow. This is a placement decision the IDE memory model already owns, not a new
redesign.

---

## 4. Go-authority-first brick plan (sketch only)

Per CLAUDE.md §6 ("if Go already implements it, the Z80 side is a port, not a design"),
the work is overwhelmingly a **port**, because the authority already exists and matches
the needed subset. Sketch, not detailed design:

- **Brick 0 — authority gap check (likely empty).** Confirm `preprocess.go` covers the
  surveyed subset (§2 says it does, exactly). No new host features are required for
  Pete's sources. *Optional* host work only if Pete later wants a construct currently
  out-of-scope (none in his sources today).
- **Brick 1 — `.set`-value table + `.if`/`.else`/`.endif` stack (Z80).** Port
  `evalIfCondition` + the frame-stack logic in `processLines`. Smallest brick;
  bare-symbol conditions only. Verify against `preprocess_test.go` fixtures via the
  netboot-oracle Z80 harness (the same harness that already gates `asmlex`/`asmparse`).
- **Brick 2 — `.macro`/`.endm` capture + `\param` substitution + invocation (Z80).**
  Port `parseMacroHeader`, `collectMacroBody`, `splitMacroArgs`, `buildSubstituter`,
  `tryExpandMacroInvocation`, with the recursion-cycle guard. The substantive brick.
- **Brick 3 — `.include` resolution (Z80).** Port `handleInclude`'s search-path logic
  against the SAM's storage backend (DOS hooks / Trinity), and `# line` directive
  emission so `asmlex` keeps positions. Storage-backend-shaped; smallest risk is I/O,
  not preprocessing.
- **Brick 4 — wiring + corpus round-trip.** Run the on-SAM `Preprocess → asmlex` chain
  and byte-compare the expanded text against host `sam-aarch64 -E` on the `preprocess_test.go`
  corpus and a spectrum4 slice, under the netboot-oracle Z80 harness (mirrors how
  asmlex/asmparse are already oracle-gated). This is the emulation-first gate
  (CLAUDE.md discipline rule 7 / memory `feedback_emulation_first`).

Each brick is a faithful port with a named Go authority function, verifiable in the
Go-based Z80 harness **without** needing the full editor wired — exactly how i48c
lexer/parser were built and proven before the editor exists.

---

## 5. Cost estimates (the q38-deciding section)

### 5.1 Assemble-time performance — **no impact on the assemble hot path; one bounded text pass on the author path**

- The preprocessor runs **only when text is converted to `.tbn`** (an editor / "save
  source" action), **not** when a `.tbn` is assembled to bytes. The two-pass
  assemble loop (`reader.asm` → pass1/pass2 → encoder), the inner-loop hot path, is
  **completely untouched** — it still reads already-expanded `.tbn`. So "does macro
  expansion add a pass over the assemble path?" — **no**.
- On the text→`.tbn` path, preprocessing is **one linear text pass** in front of the
  lexer (host model: `Preprocess` then `Lex`, each O(source bytes)). It does **not**
  add a pass to the two-pass assembler model; it slots *before* lexing, exactly as
  host-side. Macro expansion is bounded by the expanded text size (the same bytes the
  lexer would otherwise read), with a small per-line substitution scan.
- Order-of-magnitude: the SAM-side lexer already tokenizes text at editor-interactive
  speed; the preprocessor is a comparable byte-scan with macro-table lookups. The cost
  is linear in expanded source size and incurred once per save/convert, not per
  assemble — **invisible to the assemble-time budget Pete cares about**. There is no
  redesign of the pass count.

### 5.2 Program size — **~2–3.5 KB of Z80, off-axis, no core-budget pressure**

Calibration from comparable already-built ports:

- `asmlex.bin` = **1732 B** for ~1200 lines of source (the tokenizer — a similar
  character-scanning text pass).
- `preprocess.go` is **660 lines** vs `lexer.go`'s 557 — the preprocessor is roughly
  the same complexity class as the lexer, plus a macro table and a small `.if` stack.

Estimate: the engine (macro table + `\param` substituter + `.if`/`.set` stack +
`.include` + `# line` emission) is **~2,000–3,500 bytes of Z80 code**, plus a few KB of
**runtime buffers** (macro-definition storage, substituted-line buffer, `.set` table)
allocated from the **page pool**, not section C. Packaged off-axis like
`asmlex`/`asmparse`, this costs the core assembler **0 bytes** of its `&C000` budget;
even counted against the prod core it fits inside the 4943-B prod headroom several times
over. Size is **not** a q38 blocker.

### 5.3 Redesign risk — **low; clean front-end text pass, no IR / reader / two-pass change**

- The preprocessor is **text→text**, terminating *before* the IR exists. It does not
  touch the `.tbn` format, the record kinds, the reader, the symbolic IR, or the
  two-pass driver. Host-side this is proven: `Preprocess` is a self-contained pass
  whose only downstream coupling is the `# line` directive the lexer already parses —
  and `asmlex.asm` **already** handles a line-start `#` (as a line comment) and notes
  the preprocessor seam explicitly. So the Z80 lexer is already shaped for this.
- The only real coupling is **sequencing**: the preprocessor is *useful* only once the
  on-SAM **text → `.tbn`** path (i48c / B8 editor strand) is wired end-to-end —
  because that path is its sole consumer. Until then an on-SAM preprocessor would have
  no producer feeding it text and no consumer for its output. This makes i31 naturally
  a **post-B8** item, not a redesign of anything in flight.
- The one placement decision (expansion buffers vs the `&C100–&EFFF` parser/assembler
  scratch collision) is **already owned by the IDE memory model**
  (`docs/specs/ide-memory-model-design.md`, q36→i2): allocate from the page pool. No
  new mechanism is invented.

---

## 6. Go / no-go inputs for Pete

- **Verdict: it SLIPS IN.** On-SAM macros are a clean, bounded **front-end text pass**
  (a faithful Z80 port of the existing `preprocess.go`), inserted in front of the
  already-built on-SAM lexer (`src/asmlex.asm`). No redesign of the IR, reader, `.tbn`
  format, or two-pass assembler.
- **Performance: zero impact on assemble-time.** Preprocessing runs on the text→`.tbn`
  *author* path, once per save/convert — never on the `.tbn`→bytes assemble hot path
  Pete optimises. It adds **no pass** to the two-pass model; it is one linear pre-lex
  text pass, the same model GNU as and the host use.
- **Size: ~2–3.5 KB of Z80**, packaged off-axis like `asmlex`/`asmparse`, costing the
  core assembler **0 bytes** of `&C000` budget (and fitting inside the 4943-B prod
  headroom even if counted). Buffers come from the page pool, not the contended
  `&C100–&EFFF` scratch.
- **Scope is naturally minimal.** Pete's sources use *exactly* the subset the host
  preprocessor already implements (`.macro`/`.endm` + `\param`, `.if SYMBOL`/`.else`/
  `.endif`, `.include`, `.set`); every out-of-scope GNU-as construct has **zero** uses
  in his kernel. The port is small and the feature set is closed.
- **The one real dependency is sequencing, not risk.** i31 only delivers value after
  the on-SAM **text → `.tbn`** editor path (i48c / B8) lands — that path is the
  preprocessor's only producer/consumer. **Recommendation:** answer q38 **go**, scope
  i31 to a faithful port of the §1/§2 subset via the §4 bricks, and **sequence i31
  after the B8 editor-input path is wired** (depending on the B8 umbrella, not a single
  done leaf). No redesign, no perf hit, modest off-axis size.
