# On-SAM preprocessor design (i31)

**Status: awaiting Pete's approval — no implementation starts until the gating
question is answered** (CLAUDE.md development discipline 1, the spec gate).

This is the design spec for i31: an on-SAM preprocessor supporting `.if`
build-constraints and macro expansion, so text authored on the SAM no longer
relies on the host-side preprocess step. Pete already approved the *direction*
(q38 answered **GO**, 2026-06-21, on the i209 scope-and-cost assessment — full
usage survey and cost calibration at
[the assessment's final blob](https://github.com/petemoore/sam-aarch64/blob/ff14947b/docs/specs/on-sam-macro-assessment.md),
which this spec supersedes). What became concrete since: the B8 text→`.tbn`
chain is corpus-proven on-SAM (the preprocessor's sole consumer now exists),
and the i2 page pool shipped (the buffer-placement question is settled).

## 1. What it is

A faithful Z80 port of the host preprocessor
`tools/sam-aarch64/frontend/preprocess.go` (~660 lines — the authority;
CLAUDE.md §6: this is a port with a known answer, not a design from scratch).
It is a **text→text pass in front of the lexer**: it consumes raw source text
and produces expanded source text plus cpp-style `# <line> "<file>"`
directives. It never sees tokens, IR, or `.tbn`; the `.tbn`→bytes assemble hot
path is completely untouched.

```
host:   source text → Preprocess → Lex → Parse → … → .tbn
on-SAM: source text → prep_run  → b8d chain (lex→parse→pass1→compact→serialize) → .tbn
```

## 2. Scope — the closed construct set

Exactly the host subset, which the i209 survey showed is *exactly* what the
spectrum4 sources use (33 macro defs, ~3,700 invocations, 77 bare-symbol
`.if`, 677 `.include`, 83 `.set`; every out-of-scope GNU-as construct has
zero uses):

| Construct | Semantics (as `preprocess.go`) |
|---|---|
| `.include "file"` | Inline a file, searched along the include path; recursive. |
| `.macro NAME a, b` … `.endm` | Positional named params; nested `.macro` is an error. |
| macro invocation | Arity-checked, paren-aware arg split, recursion-cycle guard, body re-preprocessed. |
| `\param` substitution | Purely textual; token-paste and inside-string substitution; `\\` literal. |
| `.if SYMBOL` / `.else` / `.endif` | Bare-symbol only, truthy iff a prior `.set` non-zero; unknown ⇒ false; nesting. |
| `.set NAME, INT` | Literal integers captured for `.if`; the line also passes through to the parser. |
| `# line "file"` emission | At file boundaries and after each expansion — byte-identical to host `-E`. |

**Out of scope, permanently** (zero callers): `\@`, `.rept`/`.irp`,
`.altmacro`, `.purgem`, `.exitm`, `.ifdef`/`.ifndef`/`.ifc`, macro defaults,
`:req`/`:vararg`, `.if <expression>`, cpp `#`-directives.

## 3. Chain integration

The b8d chain contract today: source bytes at page 8 offset `&0800`
(`LEX_SRC`, 2048-byte window), `BC` = length (`src/chain_paged_driver.asm`).
The preprocessor slots in front: input is raw source text in page-pool pages;
output is expanded text written to `LEX_SRC`, then the existing chain runs
unchanged. The chain **preserves comments in the `.tbn`** (i78 source-structure
preservation), so a `# line` directive lexed as an ordinary comment would
wrongly appear in the output. To match the host lexer, `asmlex` therefore
**consumes** line-start `# <n> "<file>"` directives (position markers, no
token) via `lex_try_line_directive` — a port of `lexer.go`'s
`tryConsumeLineDirective`; any other line-start `#` is still an ordinary
comment. This keeps the expanded text byte-comparable to host `-E` while the
`.tbn` byte-matches `CompactTBNBytes` over the same expanded text (i31b-b4b).

**Expanded-text ceiling.** Expansion output must fit the `LEX_SRC` window;
over-cap is an explicit user-facing error tag, never a silent truncation.
This is the chain's *existing* source-size ceiling, not a new limitation —
lifting it (a pool-run source buffer with a runtime base, the i23
`IN_BASE_LMPR` pattern) is a separately-tracked follow-up that benefits the
whole chain, and is not part of i31.

## 4. Memory model — the i2 page pool

A new owner tag (`PP_PREP`) in `src/pagepool.asm`. Buffers, all pool-allocated
at `prep_run` entry and freed on exit:

- **Macro table** — one pool page: definition headers (name, param names,
  body ptr/len) plus raw body lines. 16 KB dwarfs the real corpus (36 defs).
- **`.set` table** — name→value pairs; shares the macro-table page.
- **`.if` frame stack and the substituted-line buffer** (~256 B) — in-image
  statics, not pool.

Zero bytes against the `&C000` core budget: the preprocessor is editor-path
code, packaged off-axis (§5).

## 5. Packaging

Its own standalone image (`prep_paged.bin`), seeded into a physical page with
an LMPR window and cross-image symbols via `--importfile` — the proven b8d
two-image pattern (`src/chain_paged_driver.asm`,
`tools/netboot-oracle/z80/compact_ir_b8d_test.go`). Size estimate ~2–3.5 KB
of Z80 (calibration: `asmlex.bin` = 1732 B for a comparable text pass;
`preprocess.go` is the same complexity class as `lexer.go`). Exact page
assignment is a plan-level detail, not a design question.

## 6. Port plan — Go-authority-first bricks

Each brick is a faithful port of named `preprocess.go` functions, verified in
the netboot-oracle Z80 harness before the next brick starts (the same way
asmlex/asmparse were built):

1. **`.set` table + `.if`/`.else`/`.endif` stack** — port `evalIfCondition`,
   `tryParseSet`, and the frame-stack logic in `processLines`.
2. **Macros** — port `parseMacroHeader`, `collectMacroBody`,
   `splitMacroArgs`, `buildSubstituter`, `tryExpandMacroInvocation`,
   including the recursion-cycle guard.
3. **`.include` + `# line` emission** — port `handleInclude`. File access
   goes through a **pluggable reader vector** set at init (no conditional
   assembly): the harness supplies a memory-backed provider; the SAM build
   supplies the real UIFA + `HGTHD` + trampoline-`HLOAD` reader (the
   `load_in_file` pattern, `src/main_loop.asm`). The real reader is exercised
   under SimCoupé with real DOS — both providers get emulator coverage,
   no `HOSTTEST` carve-outs (discipline rule 7 / i231).
4. **Wiring + corpus gate** — `prep_run` in front of `b8d_chain_paged`;
   the §7 oracle gates go green.

## 7. Verification — the oracle gates

The existing 122-fixture corpus is **already preprocessed** (zero
macro/`.if`/`.include` occurrences — verified 2026-07-03), so the
preprocessor needs its own fixtures:

- **Unit oracle:** port the ~30 cases in
  `tools/sam-aarch64/frontend/preprocess_test.go` as harness fixtures;
  byte-compare Z80 `prep_run` output against host `Preprocess` (standalone:
  `sam-aarch64 -E`).
- **Corpus extension:** new preprocessor-bearing fixtures, including a
  spectrum4 slice (starting with `tests/test_print_w0.s`, the smallest real
  macro consumer), gated two ways: expanded text byte-equal to host `-E`,
  then end-to-end text→`.tbn` byte-equal to host `CompactTBNBytes` through
  the full chain.
- **SimCoupé** remains the pre-merge gate, including the real-`HLOAD`
  include path (§6 brick 3).

## 8. Sequencing and non-goals

The consumer is the editor's save/assemble path; like asmlex/asmparse, the
preprocessor is built and oracle-proven under the harness before the editor
exists, so i31 does not wait on the open editor design questions (q51/q60).

Non-goals for v1: the §2 out-of-scope constructs; the expanded-text ceiling
lift (§3, tracked separately); on-SAM error-position mapping through
expansions (host tracks positions via `# line`; on-SAM, v1 errors report
expanded-text lines — revisit only if it hurts in practice).
