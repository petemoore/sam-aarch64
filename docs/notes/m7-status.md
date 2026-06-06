# M7 — current status (read me first)

Entry point for any session picking up M7. The M7 backlog (gathered from
notes + memory so nothing was lost in the M6 → M7 transition) is the scope
table below; it will keep being refined / re-prioritised as M7 proceeds.

**Items are tracked with stable `iN` ids** — see the **"Item index — the `iN`
registry"** section below for the authoritative id↔item map and the naming
convention. Refer to work by its id (e.g. "i1", "i12a") in PRs and conversation.

**M7 — ACTIVE (opened 2026-05-29; M6 closed via PR #76).** Housekeeping
batch 1 has landed (PRs #78–#82: dead-code removal, sysreg sync guard,
index READMEs + memory-layout doc, decision bookkeeping, the `src/m3` →
flat `src/` rename). See the ROADMAP "Current State" block for the live
session view; this doc is the per-strand source of truth.

## M7 scope (planning snapshot)

Legend: ✅ done · ⏳ in progress · 📋 designed/plan-ready · 🧭 idea/not-yet-designed · ❌ won't-do (YAGNI)

| Strand | Status | Spec/Source | Notes |
|---|---|---|---|
| Bump-arena allocator (Go-slices-vs-fixed-arrays for the Z80 data structures) | ❌ YAGNI | `docs/specs/2026-05-29-bump-arena-risk-census.md` (the census); Pete 2026-05-29 (accepted) | **NOT building it.** The risk-census (PR #84) found no fixed section-D array is a real overrun time-bomb — all SAFE today, only 3 go at-risk at a full ~5× kernel and are trivially bumpable in place. Pete confirmed YAGNI. The genuinely tight ceiling is elsewhere — the IN/OUT paged byte buffers (next row), fixed by claiming free pages, not an allocator. **Revisit trigger** (census §4): a measured at-risk overrun, or the symbol table heading toward ~2× its 512 cap. |
| IN/OUT paged-buffer ceiling (claim more SAM pages) | 🧭 | `docs/specs/2026-05-29-bump-arena-risk-census.md` §IN/OUT | The real near-term constraint surfaced by the bump-arena census: the **IN `.tbn` buffer is at 92% of its 96 KB / 6-page cap today** (88,644 B; fail tag `03`), OUT at 68% of 32 KB (fail tag `b0`). Physical pages 15..31 (~272 KB) are free, so the fix is a bounds bump (more pages), not an allocator. Not urgent (release fits), but the closest ceiling — do before a substantially larger source lands. Pairs with the compact-`.tbn` strand. **Editor-centric reframing (Pete, 2026-06-08):** this strand is really the *on-SAM IDE memory model*, not a batch-assembler-buffer concern. The SAM is the whole platform — editor + assembler + kernel/firmware server, per the project remit — and a codebase is *grown dynamically on-device*, so the edit buffer expands throughout a session (the main use is NOT "load `release.tbn`, assemble once"). The right design is "claim all free RAM at boot (size to `PRAMTP`), show remaining, grow buffers on demand from the free-page pool" — DOS (1 page, `DOSFLG`) + screen (2 pages) stay reserved; discover the rest via `PRAMTP` + the ROM `ALLOCT`/`LASTPAGE` reservation table. Deeper design (contiguous-bump vs per-buffer page-list for a dynamic IN/OUT ratio) deferred until editor development, where it belongs. See ROADMAP "Editor vision". |
| Codegen sysreg / mnemonic / form tables from Go authority | 📋 | M6-closure plan §M7 sketch (PR-A), `tools/sam-aarch64-format/sysregs.go` | Kills hand-sync drift. Depends on M6 PR-2's page-13 binary build glue. |
| On-SAM disassembler (strand B) — Go side | ✅ PR #93 (disasm) ✅ PR #97 (round-trip) ✅ PR #98 (EXTR + .inst-free) ✅ session #3 parity + fix (direct commits) | `docs/plans/2026-05-28-go-aarch64-disassembler.md`; `tools/aarch64dec/`; `docs/specs/2026-06-07-disasm-round-trip-design.md` | **PR #93:** 100.00% objdump match; `disasm` required check. **PR #97 (strand-B PR-2):** `BranchTarget` + `WriteAsm` + `-asm` flag + `tools/run-disasm-roundtrip.sh`; 48/48 M3-M6 instruction fixtures round-trip; `disasm-roundtrip` the 14th required check; 5 new encodings (`udf`, `sxth`, `sxtb`, `uxtb`, `uxth`) + `.inst` in refenc. **PR #98:** `tryDecodeExtr` + per-fixture `.inst`-free assertion; 47/47 fixtures pass. **Session #3 parity work (direct commits on main, all CI green):** (a) 5 new mnemonics across Go + Z80 — **sturh=94 / sturb=95 / ldurh=96 / ldurb=97 / adds=98** — in `mnemonics.go`, `manual_forms.go`, `parser.go`, `refenc/pass2.go`, `src/intercepts.asm`, `src/slots/mem.asm`; madd/msub W-forms added; `ENCTAB_LEN` 3622→3676 (commit 7a2db59). (b) **Non-canonical logical-immediate decoder fix** — `decodeBitMasks` rejects `immr ≥ esize` → falls through to `.inst` preserving exact bits (commit b24b618); `[2c/3]` full-binary round-trip wired (commit 999b060). (c) Z80 budget: range-check compaction saves 25 B (commit c2b970d). **Fixture count: 49.** **Next: PR-3 Z80 port** of the disassembler. SIMD/atomics declined (full-ISA strand). See `memory/feedback_disassembler_first_decouple`, `memory/feedback_align_with_binutils`. |
| On-SAM disassembler (strand B) — release.s round-trip | ✅ code-only [2b/3] commit f2ab814 ✅ full-binary [2c/3] commit 999b060 | `tools/run-disasm-roundtrip.sh`; `tests/m6/release/release.s` | **[2b/3] DONE 2026-06-07 (session #2):** `text2bin -strip-data` + code-only round-trip; 0 `.inst` entries, 3908 instructions. **[2c/3] DONE 2026-06-07 (session #3):** full `release.s` (code+data) assembled → disassembled → reassembled → byte-compared; **PASS: 21752 B, 747 `.inst` entries from embedded data words** (`.word`/`.quad` tables + literal-pool entries — all undecodeable data preserved verbatim via `.inst`). Non-canonical `orr w19, w0, #0x1` (tvt_data bytes `0x13 0x00 0x20 0x32`, immr=32 > esize=32) now correctly emits `.inst 0x32200013`. |
| On-SAM disassembler (strand B) — Z80 NOP stub / PR-3 | ✅ wiring complete (session #5, direct commits on main) | `src/disasm.asm`; `src/disasm_comm.inc`; `src/loader.asm`; `src/assembler.asm`; `docs/notes/2026-06-07-disassembler-page-placement.md` | **All 5 missing items landed (commits bfcd900 + 1b3b7ab, 2026-06-07 session #5):** (1) `DISASM_PAGE`/`DISASM_ENTRY`/`DISASM_COMM_MNEM`/`DISASM_COMM_OPS` defined in `src/disasm_comm.inc` (single source of truth, included by both `trampoline.asm` and `disasm.asm` — eliminates the lock-step duplication that was a footgun); (2) `load_page15_payload` added to `loader.asm` (8-byte stub → shared `load_payload_generic` 28-byte body; refactoring also saved 82 bytes by de-duplicating the 5 HGTHD+HLOAD loaders); (3) "d15" deposited by `build-m3-disk` + `-disasm` flag wired in all roundtrip scripts; (4) `call load_page15_payload` at boot in `assembler.asm`; (5) `run_disasm_self_test` at `DISASM_SELF_TEST_ENTRY` (&8100) inside `disasm.asm` itself — invoked via `paged_call` from assembler.asm (avoids nested paged_call). `test_disasm_paged.asm` deleted (logic absorbed). Budget: test variant &BFEA (22 B headroom), prod &B88B. **All 14 CI checks green.** |
| On-SAM disassembler (strand B) — Z80 decoder port (PR-4) | ✅ COMPLETE (PRs #102 + #103, 2026-06-07 #6) | `src/disasm.asm`; `tools/z80-test-harness-go/disasm_oracle_test.go` + `boot_self_test_test.go`; `docs/plans/2026-06-07-strand-b-pr4-z80-disassembler-port.md` | **Done — full aarch64 disassembler, oracle 100% (5438/5438 release.img words decode identically to `aarch64dec.DecodeAt`).** Ported test-first family-by-family: udf, move-wide, load/store, add/sub-imm, logical-imm (incl. `decodeBitMasks` + non-canonical `immr≥esize` reject), dp-register (shifted/extended + aliases), branch + PC-relative (b/bl/b.cc/cbz/cbnz/tbz/tbnz/adr/adrp/ldr-literal/ret/br/blr), bitfield, condsel, multiply, system (mrs/msr/dc/tlbi/barriers/eret/wfi), udiv/sdiv. TDD keystone `TestDisasmOracle` drives the PROD `build/disasm.bin` standalone in koron-go/z80, word-by-word vs the Go oracle, asserting a plain 100% + the `paged_call` BC/IX/IY ABI every word. **pc ABI:** `DISASM_COMM_PC` (&7EBD section-B slot). **Local full-boot test** `TestBootSelfTestsPass` (~30 ms, + fail-probe) verifies the page-15 boot self-test path locally. **BUILD_TESTS split:** prod `disasm.bin` (9636 B) ships no test code; test `disasm-test.bin` (11803 B) has the self-test; roundtrip scripts route by assembler variant. **Follow-ups (non-blocking):** ~~literal `z80disasm -asm` [2b/2c] mirror~~ **CANCELLED/WONTFIX** (Pete, 2026-06-08 — no added signal; per-word oracle equivalence already implies round-trip equivalence); de-dup the four sysreg name↔encoding tables into one shared `src/sysreg_tables.inc` included by both page-13 (`sysreg_data.asm`) and page-15 (`disasm.asm`) — the `sysreg-sync` guard parses only those four table sections, so it does NOT need to follow includes; just repoint its `asmPath` at the shared `.inc` (Pete, 2026-06-08 — supersedes the earlier "needs include-walking" framing); **Go-vs-Z80 capability parity report** (Pete's idea — surface instructions Go decodes that aren't in release.img, so coverage gaps aren't hidden by the release-only corpus) — ✅ **DELIVERED** `docs/notes/2026-06-08-go-vs-z80-disasm-capability-parity.md` (i10, 2026-06-08): release.img exercises 66 of ~90 Go-decodable families; ~24 families are handled by `disasm.asm` but never compared against Go by the oracle (cond-compare/select, extended-register arith, signed multiplies are the ones that matter); recommends a synthetic fixture sweep + flags `sdiv` as a Go-side `.inst` gap. |
| Test-wiring audit + repair (blocks strand-B PR-4) | ✅ audit done + repairs shipped (session #5) | `src/assembler.asm`; `src/test_*.asm`; `.github/workflows/ci.yml` | **Audit completed (session #5, 2026-06-07):** all test files confirmed wired; the only gap was the disasm wiring from the PR-99 revert (fixed — see row above). No other orphaned tests found. All 14 CI checks green on main (commit 1b3b7ab). **PR-4 is now unblocked.** |
| Source compression / compact `.tbn` | 📋 (sequenced **after** the disassembler) | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` § "2026-05-29 refinement"; Pete 2026-05-29 | **Design done; build deferred until the disassembler lands (Pete 2026-05-29 reorder).** The disassembler is the prerequisite — a compact `.tbn` that stores assembled bytes is write-only to the editor without bytes→text. Design (kept as the eventual target): hybrid format adds **`KindLitInsts = 0x07`** (a *run* of consecutive fully-literal instructions stored as assembled bytes); compaction lives in **`refenc`** (sole encoder authority) as a `.tbn`→`.tbn` transform; the m6-release 3-way gate verifies. Increment plan in the spec. Open-Q6 (defaults chosen). |
| Editor groundwork (Phase 2) | 🧭 | `docs/ROADMAP.md` "Editor vision" section | Instruction-explanation panel, register simulator, sysreg docs, did-you-mean. Not yet spec'd. |
| Repo-cleanup / README housekeeping track | ⏳ | `docs/notes/2026-05-29-repo-audit.md` §6 (prioritised plan) | Track underway. **Landed: PR #78** (dead Go symbols + orphan stub removed), **PR #80** (`tools/` + `src/README.md` index READMEs). Remaining: deep reviews of `main_loop.asm` (#11) / `litpool.asm` (#12), and the `SYMTAB_*` `equ` sentinels (#8). Does NOT block other M7 work. |
| Directory naming & logical organisation (rename `src/m3`) | ⏳ | Pete 2026-05-29 (decision delegated to agent) | **`src/m3` → flat `src/` DONE (PR #82).** The dir is gone; all live references updated. The wider naming review (`tests/m{3..6}`, `ci-m{N}` targets, `build-m3-disk` tool, milestone-named fixtures) remains as later M7 cleanup. |
| Subagent worktree-isolation leak (stray git ops in shared checkout) | 🧭 | Observed 2026-05-29 (Pete flagged) | Worktree-isolated subagents (`Agent` tool, `isolation: worktree`) have twice run `git checkout -b` / `git reset --hard` against the **shared** checkout instead of staying in their worktree (recovered both times, no work lost — the orchestrator commits before dispatching). Investigate why isolation leaks (likely the subagent `cd`s to the repo root, which resolves to the shared dir) and harden: e.g. instruct subagents to operate only via their worktree path, or verify/restore the shared checkout's branch after each subagent returns. Process hazard, not yet causing damage. |
| Canonical memory-layout reference doc | ✅ | PR #80 → `docs/notes/memory-layout.md` | Done. Mirrors the authoritative `src/assembler.asm` header map (source of truth; doc points to it, no drift). |
| Sysreg Go↔Z80 sync guard | ✅ | PR #79 → `tools/sam-aarch64-format/sysregs_z80sync_test.go` + `sysreg-sync` CI job (now a required check) | Done. Go test parses `src/sysreg_data.asm` and asserts every Z80 entry byte-matches the Go authority (the Z80 table is an intentional subset). |
| Linker-layout coupling (flatten hardcodes `spectrum4.ld`) | 🧭 | Pete 2026-05-29; `tools/text2bin/internal/translate/flatten.go` `SpectrumFourLayout` | Byte-equivalence on the release relies on text2bin's flatten pass **hardcoding** spectrum4.ld's section order + ALIGNs + origin (we do NOT parse the `.ld`). The m6-release 3-way gate guards drift (a `.ld` change → re-vendor → gate fails until flatten is updated), but it's an implicit coupling. Consider: parse `spectrum4.ld` directly, or vendor it + a checked cross-reference, or at minimum document the coupling beside `SpectrumFourLayout`. |
| Go-harness fidelity follow-ups | ⏳ | `docs/notes/2026-05-29-go-harness-fidelity-investigation.md` Q4; paged-trap root-cause `docs/notes/2026-05-29-go-harness-paged-trap-rootcause.md` | **Paged-path trap ✅ RESOLVED (PR #88):** NOT a fidelity bug — the 6-page paged-IN HLOAD is faithful; the `&0038` trap was a missing `-sysreg-data` (empty page 13 → NOP-slide). With it supplied, **the harness runs the full release `.tbn` and byte-matches the vendored `release.img`.** Fixed diagnostically (loud "unserved HGTHD file" message) + regression test. **Remaining (lower priority):** write-watchpoint activation, `make harness-sweep` target, USAGE.md ledger. NOT real-ROM execution. |
| Z80↔Go encoding/operator parity audit | ✅ | PR #87 → `docs/notes/2026-05-29-z80-go-parity-audit.md` | **Done — and parity is essentially COMPLETE, not a subset.** The generic encoder consumes `enctab.enc` generated from the same Go `Form` table (can't drift); all 17 slot kinds dispatched; every refenc special-case encoder mirrored 1:1; 27/27 expr ops, 22/22 directives, 12/12 operand kinds. Remaining work is **robustness/scale, not features** → see the seeds row below. |
| Parity robustness seeds (from the #87 audit) | 🧭 | `docs/notes/2026-05-29-z80-go-parity-audit.md` §summary | Three follow-ups the parity audit surfaced: (1) the sysreg named-table subset has one fail-hard path (`src/sysname.asm:421` "no generic form" — likely some PSTATE/DC/TLBI op names) — make it fail-soft or extend the subset; (2) fixed Z80 caps vs unbounded Go (the IN/OUT-ceiling + bump-arena-YAGNI story already covers most); (3) **untested-form-combination sweep** — parity today is *structural*, not empirically tested beyond the M3–M6 fixtures; a fixture sweep through the byte-match harness would convert structural→verified parity (and the harness can now run the full release, per #88). |
| SAM screen-mode decision (editor) | 🧭 | Pete 2026-05-29; ROADMAP "Editor vision" | MODE 3 currently assumed. Decide mode(s) by colour-vs-resolution + aesthetics nearer the editor; the choice consumes display RAM / pages, so it feeds the memory-layout doc (the ✅ row above). **Pete 2026-05-29:** consider offering it as a **user preference** — high-resolution/fewer-colours vs lower-resolution/more-colours — rather than a fixed choice. |
| Full ARMv8-A instruction-set footprint — research | ✅ DELIVERED (`docs/notes/2026-06-08-armv8-a64-isa-footprint-research.md`) | Pete 2026-05-29 | **Isolated research project:** estimate how much additional memory (encoder tables + Z80 code) it would take to support the **full ARMv8.0-A A64 instruction set** (A64 only — no AArch32/Thumb; ARMv8.0 only, not later v8.x), vs today's spectrum4-release-only subset. Include the **FP + Advanced SIMD/NEON** extensions (Pete: "is that NEON? — yes"). **Findings:** ~442 mnemonics full-ISA vs 99 today; NEON/FP is ~40–45% by mnemonic / >50% by encoding-variant — the dominant cost. Estimate +1100–1850 encoder Forms, `enctab.enc` 3.7 KB → ~30–50 KB (cheap — pages onto the free 272 KB), **+15–35 KB of Z80 code**. **Verdict:** does NOT fit the flat `&8000–&C000` window (1.9 KB code headroom vs +15–35 KB) → needs a **paged-code/overlay** subsystem; table growth already solved by the existing off-axis pattern; Trinity storage not required on a 512K machine. Motivation: decide whether broad-ISA support is worth it for future kernel dev or ingesting LLVM-compiled output. Builds on the #87 parity audit (which found the *current* coverage is structurally complete for the subset). |
| Trinity SD/flash storage → bigger-kernel architecture | 🧭 *(beyond-M7)* | Pete 2026-05-29; `memory/trinity_hardware.md` | Trinity's SD/MMC slot lifts the implicit single-floppy ceiling, enabling much larger kernels/debug builds (spectrum4 may be ~5× when complete). The binding constraint eventually shifts from code budget to storage. Quazar docs to be scanned. Distant future. |

## Item index — the `iN` registry (naming convention)

**Convention (Pete, 2026-06-08):** every tracked item has a stable **`iN`** id
(`i` = item). This table is the **registry** — the authoritative id↔item map.
Rules: (1) once an id appears in a PR title, branch, or commit it is **locked** —
never renumber it; (2) sub-items take letter suffixes (`i12a`/`i12b`/`i12c`);
(3) a new item gets the next free integer; (4) reference items by id in
conversation, PRs, and these docs. The scope table above and the deferred-backlog
table below carry the same ids. (The ids below were assigned in the 2026-06-08
planning session; `i13` is locked to "gitignore" to match shipped PR #107, so the
"replace `cls`" item — tentatively i13 in conversation — is registered as `i16`.)

| id | item | status | pointer |
|----|------|--------|---------|
| **i1** | Compact-`.tbn` format change (hybrid bytes/symbolic; `KindLitInsts`) | 📋 planned (the sequenced-next big strand) | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` |
| **i2** | On-SAM IDE memory model (edit buffer + IN/OUT paging; "claim all free RAM, grow on demand") | 🧭 reframed; deferred to editor work | scope row "IN/OUT paged-buffer ceiling" |
| **i3** | Editor groundwork (Phase 2) — full vision | 🧭 | ROADMAP "Editor vision" |
| **i4** | Basic read-only listing/scroll viewer (centre-locked cursor; up/down only) | 🧭 new 2026-06-08 | — (Pete's idea; precursor to i3) |
| **i5** | UI visual prototyping via image generation (MODE 3 64×24 vs MODE 4 32×24 mockups) | 🧭 new 2026-06-08 | — (Pete's idea) |
| **i6** | SAM screen-mode decision (MODE 3 vs 4, or user preference) | 🧭 | ROADMAP "Editor vision"; scope row |
| **i7** | Codegen sysreg/mnemonic/form tables from Go authority | 📋 | scope row |
| **i8** | Sysreg-table de-dup into shared `src/sysreg_tables.inc` | ✅ DONE (PR #108) | `src/sysreg_tables.inc` |
| **i9** | Parity robustness seeds (sysname fail-soft + untested-form empirical sweep) | ⏳ in progress (agent) | `docs/notes/2026-05-29-z80-go-parity-audit.md`; scope row |
| **i10** | Go-vs-Z80 capability parity report | ✅ DONE (PR #109) | `docs/notes/2026-06-08-go-vs-z80-disasm-capability-parity.md` |
| **i11** | Full ARMv8.0-A A64 ISA footprint research | ✅ DONE (PR #110) | `docs/notes/2026-06-08-armv8-a64-isa-footprint-research.md` |
| **i12a** | SimCoupé v1.2.16 bump (pin SHA to upstream, drop vendored `-exitonhalt` patch) | ⏳ in progress (agent) | `tools/Dockerfile.dev` |
| **i12b** | Editor-testing input injection (`-keyin` vs `FLAGS`/`LASTK` memory injection) | 🧭 new 2026-06-08 | `tools/run-simcoupe.sh`; for automated editor tests |
| **i12c** | Rebase/upstream macOS+Linux paste support | ✅ RESOLVED/MOOT — upstreamed (`87f2a69`); arrives free with i12a | `~/.claude/.../memory/project_simcoupe_sdl_paste_branch.md` |
| **i13** | gitignore in-tree Go build binaries | ✅ DONE (PR #107) | `.gitignore` |
| **i14** | Non-canonical logical-immediate decoder tests (`decodeBitMasks` `immr≥esize` reject) | 🧭 | deferred-backlog row |
| **i15** | `adds` 3-register form (Z80) | 🧭 | deferred-backlog row |
| **i16** | Replace `cls` test instruction with a real spectrum4 one | 🧭 (was tentatively "i13" in conversation) | `docs/ROADMAP.md` deferred checklist |
| **i17** | Deep reviews of `main_loop.asm` + `litpool.asm` + `SYMTAB_*` equ sentinels | ⏳ | repo-audit §6 |
| **i18** | Wider naming review (`tests/m{N}`, `ci-m{N}`, `build-m3-disk`, milestone fixtures) | ⏳ | scope row "Directory naming" |
| **i19** | Subagent worktree-isolation leak — harden | 🧭 | scope row |
| **i20** | Linker-layout coupling (`spectrum4.ld` hardcoded in flatten) | 🧭 | scope row |
| **i21** | Go-harness fidelity follow-ups (watchpoint, `make harness-sweep`, USAGE.md) | ⏳ | scope row |
| **i22** | Per-fail-site diagnostic strings | 🧭 | deferred-backlog row |
| **i23** | Paged-IN >16.5 KB HLOAD-ceiling lift | 🧭 | deferred-backlog row |
| **i24** | 64 KB output / 16-bit `OUT_LEN` limit | 🧭 | deferred-backlog row |
| **i25** | `(hksp)` HSAVE/HLOAD error handler | 🧭 | deferred-backlog row |
| **i26** | text2bin operand-kind validation (Task 21) | 🧭 | deferred-backlog row |
| **i27** | Cortex-A53 errata workarounds (`--fix-cortex-a53-*`) | 🧭 | deferred-backlog row |
| **i28** | Absolute `.set` high-word edge vs Go | 🧭 | deferred-backlog row |
| **i29** | Harden slot self-test `PASS_PC` dependency | 🧭 | deferred-backlog row |
| **i30** | LDIR-fan-out for cross-page-shared blocks (smaller binary + faster boot) | 🧭 deferred/possible | `docs/plans/2026-06-07-strand-b-pr4-z80-disassembler-port.md` "Future enhancements" |
| **i31** | On-SAM preprocessor (`.if` build-constraints + macros) | 🧭 beyond-M7 | "Beyond-M7 / future ideas" |
| **i32** | Multiple source files + staged/partial loading | 🧭 beyond-M7 | "Beyond-M7 / future ideas" |
| **i33** | Trinity SD/flash storage → bigger-kernel architecture | 🧭 beyond-M7 | `memory/trinity_hardware.md` |
| **i34** | Untrack accidentally-committed Go binaries | ⏳ partial — `enctab-gen` untracked (PR #111); `llist-normalise` pending LLIST disposition (open-Q5) | `.gitignore`; open-question 5 |
| **i35** | `sdiv` missing across the whole stack (no mnemonic/Form → `.inst`; unencodable) | 🧭 new 2026-06-08 | deferred-backlog row; mirror `udiv` ID 72 |

## Open questions for Pete (awaiting input)

These are decisions an autonomous M7 session is blocked on (or chose to
defer rather than guess). Logged here so they survive context churn — the
agent works around them and Pete answers when available. Remove an entry
once resolved.

**Practice (Pete, 2026-06-08):** *every* question for Pete goes here the moment
it arises — not just in chat — because simultaneous edits mean a chat question
can be missed and left unanswered. This section is the single sure-fire list of
**unanswered** questions: add on ask, remove/mark-resolved on answer, so what's
shown is always exactly what's still open. (See `memory/feedback_capture_open_questions`.)

### ⏳ OPEN — awaiting Pete (added 2026-06-08, evening)

- **OQ-A — i5 graphics tooling.** For editor UI mockups at SAM-accurate
  resolution, which approach do you want? (a) a **programmatic SAM-faithful
  renderer** I write — emits PNGs (or actual SAM `SCREEN$` files viewable in
  SimCoupé) at the exact MODE 3 (512×192, 4 colours/line) or MODE 4 (256×192,
  16 colours) geometry, colours drawn from the real 128-entry palette,
  8×8 attribute cells — deterministic and constraint-accurate; (b) a **dedicated
  retro/pixel-art tool** you'd prefer to drive by hand; (c) a **generic AI image
  generator**. Note on my capability: I do **not** have native image generation
  in this environment, and a generic AI generator wouldn't respect the hard SAM
  constraints (fixed resolution, 4/16-from-128 palette, attribute cells) — so for
  *faithful* mockups, (a) is the honest best option; a dedicated art tool (b) is
  better for hand-authored final art. Your call on which to use for i5.

- **OQ-B — next major strand after the current batch lands.** Once
  i8/i9/i12a/i13/i14/i35 are merged, what's the next big piece: **i1**
  (compact-`.tbn`, the long-sequenced next), **start the editor** (i4 read-only
  listing viewer → i3 groundwork), or **i7** (codegen tables from Go authority)?
  You earlier leaned compact-`.tbn` or editor. (Default if unanswered: I land the
  in-flight PRs but do **not** start a new major strand without your steer.)


1. **`src/m3` rename — ✅ RESOLVED (Pete 2026-05-29).** Pete delegated the
   final structure choice to the agent. **Decision: flatten `src/m3/` up
   into `src/`** (no component subfolder). Rationale: the stub is gone
   (PR #78), so `src/` is just the SAM monolith + the live `src/sam_io.inc`
   (`src/m3/io.asm` includes `../sam_io.inc`); `tools/` already separates
   all non-SAM/host code; and with no editor/music components yet, a
   speculative `src/assembler/` would force a fuzzy assembler-vs-core split
   now for no benefit (YAGNI). Component subfolders become a deliberate
   re-org when those components arrive with real boundaries. **Done in
   PR #82** (mechanical: `git mv` all of `src/m3/*` → `src/`, including
   `src/slots/`; `../sam_io.inc` → `sam_io.inc`; all references updated).
2. **LLIST tool cluster disposition** (`tools/llist-*` + `llist-*.sh`).
   **⏸ PUNTED (Pete 2026-05-29) — revisit later.** Pete hasn't decided;
   leave the cluster exactly in place. `tools/README.md` (PR #80) marks it
   "superseded by the EDIT/EDKY detokeniser spike — disposition pending".
   Nothing to do until Pete chooses archive/delete/keep.
3. **Design-strand handling — ✅ RESOLVED (Pete 2026-05-29).** Pete is
   happy for the agent to drive design + brainstorming solo (review later).
   Editor/screen-mode aesthetic input can come at review time. See the
   bump-arena strand below for Pete's substantive framing on that one.
4. **`src/stub.asm` / the M0 stub chain — ✅ EXECUTED (2026-05-29, branch
   `m7-delete-m0-stub-chain`).** Pete: "delete all the stub chain stuff, it
   is completely subsumed, i agree." The M0 nop-to-disk round-trip oracle is
   fully subsumed by the M3–M6 fixture corpus + the m6-release 3-way gate.
   **Deleted:** `src/stub.asm`, `tools/build-stub.sh`, `tools/build-disk.sh`,
   `tools/build-disk/` (Go tool), `tools/run-roundtrip.sh` (the M0 root one —
   *not* the per-milestone `tests/m{N}/run-roundtrip.sh`), `tools/extract-output.sh`,
   `tools/diff-vs-gnu.sh`, `tools/check-toolchain.sh` (M0-scoped per its own
   header, orphaned once `check` went), the M0 nop fixture + dir
   (`tests/fixtures/nop.s`), and all six M0 dev tests (`tests/test-{build-stub,
   stub-emits-nop,simcoupe-runs,diff-vs-gnu,diff-vs-gnu-unit,check-toolchain}.sh`).
   Makefile `stub`/`disk`/`run`/`extract`/`diff`/`test`/`check`/`ci` targets
   removed; `all:` now builds `m3-asm m3-asm-prod`. The `test` CI job removed
   from `.github/workflows/ci.yml`. Docs swept (README, tools/README).
   **KEPT (reused, verified):** `tools/run-simcoupe.sh` (used by every
   `run-m{3..6}-roundtrip.sh` + the m6-release gate). The historical M0 design
   docs (`docs/plans/2026-05-09-m0-toolchain-bootstrap.md`, `docs/notes/m0-status.md`,
   `docs/notes/sam-stub-audit.md`) were kept as archival reference.
   **✅ DONE at merge:** `test` removed from `main`'s branch-protection
   required-status-checks (was 13 contexts incl. `test` — handover's "14" was
   off by one; now **12**: build-image, m1, m2, m3, m4, m4-prod, m5, m5-prod,
   m6, m6-prod, m6-release, sysreg-sync). PR #91 merged (`8cf1dd7`).
5. **`tools/llist-normalise/llist-normalise` is a committed binary** (an
   accidental check-in spotted during the rename). Folds into the punted
   LLIST-cluster disposition (open question 2) — handle together.
6. **Compact-`.tbn` design choices (non-blocking; defaults chosen, building
   on them).** Captured 2026-05-29 while implementing the compression strand
   (details + rationale in the spec refinement). The agent proceeded with the
   defaults; flag if you'd prefer otherwise: (a) **Level-3 frequency
   dictionary** — *deferred* (Level 2 should clear the ceiling; dictionary
   adds a per-project artifact + decoder complexity). (b) **Compact constant
   data directives** (`.word`/`.byte` runs) — *deferred to PR 3* (PR 1/2 are
   instructions-only, your exact framing). (c) **Compaction flag home** —
   `refenc -emit-compact-tbn` for now (reuses the sole encoder; text2bin
   doesn't encode), revisit the CLI surface once the format is proven.
7. **Sequencing: disassembler BEFORE compact-`.tbn` — ✅ DECIDED (Pete
   2026-05-29).** Pete redirected mid-session: build the standalone Go
   disassembler first, *then* the compact-`.tbn` format change — not both at
   once. Rationale (his + agreed): the disassembler is the prerequisite that
   makes a bytes-based representation usable (the editor needs bytes→text),
   it's a clean independently-testable unit, and doing the format change +
   assembler wiring simultaneously couples two risky changes. Verify the
   disassembler with a **3-translation round-trip** (`source→assemble→v1→
   disassemble→canonical→assemble→v2`, assert `v1==v2` — dodges the
   non-canonical-representation problem) plus alias/canonical-form tests vs
   binutils `objdump`. Captured in `memory/feedback_disassembler_first_decouple`.
   The compact-`.tbn` design (open-Q6 + spec refinement) stays as the
   eventual target.
8. **Disassembler follow-ups — ✅ ALL RESOLVED (Pete 2026-05-29 #2).**
   (a) **Merge PR #93** — ✅ DONE (merged `fbec9d5`). The Go disassembler is
   on `main`.
   (b) **Promote `disasm` to a required status check** — ✅ DONE: `disasm`
   added to `main`'s required-status-checks (now **13**: build-image, m1, m2,
   m3, m4, m4-prod, m5, m5-prod, m6, m6-prod, m6-release, sysreg-sync,
   disasm).
   (c) **Round-trip (plan PR-2) assembler** — ✅ DECIDED: **our own
   text2bin→refenc pipeline** (Pete: we guarantee GNU-equivalence elsewhere,
   and our pipeline is always available where GNU may not be). So PR-2 is:
   `release(code-only) → text2bin → refenc → v1 → aarch64dec disassemble →
   text → text2bin → refenc → v2`, assert `v1==v2`.
   (d) **SIMD/SVE/FP/SME + atomic/exclusive decoding** — ✅ DECIDED: **keep
   declining** (`.inst`). Spectrum4 emits none; the encoder has zero refs.
   This naturally folds into the **full-ARMv8-A-ISA-footprint** M7 research
   strand — if/when we extend instruction support there, the disassembler
   coverage comes with it.
9. **Repo hygiene nit (non-blocking):** `go build` in `tools/refenc` /
   `tools/aarch64dec/cmd/...` drops binaries in-tree (e.g. `tools/refenc/refenc`)
   not covered by `.gitignore` — easy to commit by accident (removed stray
   ones this session). Consider a `.gitignore` rule or always building into
   `build/`.

### Bump-arena allocator (headline structural item) — ❌ YAGNI (closed 2026-05-29)

**Outcome:** the risk-census (PR #84, `docs/specs/2026-05-29-bump-arena-risk-census.md`)
concluded no general bump-arena is needed, and **Pete confirmed YAGNI
(2026-05-29)** — happy not to implement it. The section below is retained
as the original framing/context; the census doc has the evidence and the
precise revisit-trigger. The real near-term ceiling it surfaced (the IN/OUT
paged buffers) is now its own scope-table strand.

The durable answer to the fixed-table-overflow class. Today the SAM-side
assembler uses **fixed-capacity arrays** for every internal data structure
— the symbol table, OPVAL_ARRAY, the literal pool, local labels, the
STAGING_BUF. Each is bumped to a hand-tuned capacity sized against the
peak demand a particular `release.tbn` placed on it (the census at
`docs/notes/2026-05-28-z80-table-sizing-census.md` records those peaks —
e.g. symbol table peaked at 474 entries against a 256+128 cap, and the
litpool PC-map peaked at 44 against a cap of 32). That works for today's
input but is fragile: if the spectrum4 source grows, the caps need
re-tuning, and an undersized cap manifests as a silent hang rather than a
clean error.

The structural fix is to move from **fixed arrays to "Go slices" semantics
for the Z80 data structures** — per-component bump arenas living on known
physical pages, each table allocating out of its own arena so it can grow
without a hardcoded ceiling and without colliding with a neighbouring
table. The direction is recorded in `memory/m6_strand_a_complete.md:51`
("once strand B disassembler lands, this is the durable answer to the
entire fixed-table-overflow class … arena dissolves that") and in the
memory-layout brainstorm at
`docs/notes/2026-05-28-memory-layout-brainstorm.md:122` ("Build per-component
bump arenas from the start … per-component, not global, so each table stays
in a known page") and `:129` ("Bump-arena lands with the disasm-aux PR.
Retrofitting later is more work"). It is a **named direction, not a
designed feature** — no spec or plan yet, hence 🧭.

**Pete's framing (2026-05-29) — needs-driven, not speculative.** Build a
growable-array mechanism (which needs an allocator) **only if we actually
have a fixed-size structure we append to that can realistically overrun**;
do NOT build one for its own sake. Growable arrays cost complexity, an
allocator, and per-op overhead, but are ultimately safer and can keep a
smaller footprint (freeing memory). The decision hinges on a concrete
question the design must answer FIRST: *is any current fixed array a live
time-bomb* — i.e. can a plausible (not pathological) future spectrum4
source push it past its cap, turning today's hard-fail into a recurring
hazard? We earlier replaced several array overruns with hard fails (the
bounds-check work in M6 strand A); if those caps are comfortably above any
realistic demand, leave them. If any is genuinely at risk, do the
groundwork so the **only** failure mode is true whole-program OOM, never an
artificial per-table ceiling. So the design doc's Step 1 is a
risk-census (extend `2026-05-28-z80-table-sizing-census.md`): per fixed
structure, record cap vs observed peak vs *plausible-growth* peak, and
classify safe / at-risk. Only the at-risk set justifies the arena. Pete is
happy for the agent to drive this design solo (review later).

### Codegen sysreg / mnemonic / form tables from Go authority

Today the sysreg / mnemonic / form tables are hand-maintained on both the
Go side and the Z80 side and must agree by hand. The M6-closure plan's M7
sketch (PR-A) and the repo audit (§5) both flag this as a real maintenance
hazard: adding a sysreg on one side and forgetting the other passes each
side's own tests but diverges, and the release byte-match only catches it
if a fixture exercises the new register. The fix is single-source-of-truth
codegen: `tools/sam-aarch64-format/sysregs.go` (the authoritative sysreg
list) generates `build/sysreg_data.bin` Mac-side, with the same pattern
extended to mnemonics, the form table, and the intercept tables. This
depends on M6 PR-2's page-13 binary build glue (the off-axis payload
mechanism). 📋 — designed in sketch form, awaiting the standalone plan.

### On-SAM disassembler (strand B)

A form-table-driven aarch64 disassembler — the symmetric inverse of
`tools/aarch64enc/` — validated against binutils `objdump`. The Go-side
implementation plan exists at `docs/plans/2026-05-28-go-aarch64-disassembler.md`,
and a start is parked on branch `strand-b-1-disassembler` (5 commits).
Per Pete's 2026-05-28 redirect, strand B is disassembler-first: Go
disassembler → round-trip test → Z80 port → compact `.tbn` → editor
integration. It unlocks the editor's "render the `.tbn`" feature and is the
oracle for the eventual Z80-side disassembler. 📋.

### Compact `.tbn` format

A denser tokenised-source encoding, designed at
`docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`. Future-
proofing: needed only if a future source exceeds the paged-IN ceiling
(release-stripped at ~88 KB is comfortably under, but the full unstripped
`release.s` flatten is ~408 KB and trips the IN buffer cap). 📋 — designed,
not started.

### Editor groundwork (Phase 2)

The longer-term on-SAM editor vision, captured in the `docs/ROADMAP.md`
"Editor vision (Phase 2)" section: inline instruction-explanation panel,
register simulator with user seeds, system-register documentation surfaced
from the cursor line, "did you mean a simpler instruction?" rewrite hints,
and a 1980s keyboard-driven (not pointer-era) interaction model. The
unifying north star is "the editor as a thoughtful guide, not just a
mechanical tool." Not yet spec'd; these are design pointers, not a critical
path. 🧭.

### Repo-cleanup / README housekeeping track

A dedicated housekeeping track defined by the repo audit at
`docs/notes/2026-05-29-repo-audit.md` §6 (prioritised cleanup plan). It
covers: removing confirmed-dead Go symbols, adding `tools/` + `src/`
index READMEs, archiving superseded docs and the LLIST tool cluster,
deleting the orphan `src/stub-border-test.asm`, resolving the unused
`SYMTAB_*` `equ` sentinels, and scheduled deep reviews of the two largest
Z80 files (`main_loop.asm` 2362 lines, `litpool.asm` 1138 lines). The audit
recommends running it as a track that does NOT block M6 closure, with the
tiny tool-verified PRs interleaved now and the larger reviews scheduled into
M7 proper. 📋 — track defined.

### Directory naming & logical organisation (rename `src/m3`)

Guiding principle (Pete, 2026-05-29): **a clean project should read as a
coherent product, organised by logical component — not as a fossil record
of how it was built.** Today's layout encodes development chronology:
`src/m3` is "milestone 3", `tests/m{3..6}`, `ci-m{N}` targets, and many
milestone-named fixtures. That chronology is already captured by git
history; the tree itself should describe *what the thing is*.

Concretely for M7's review/cleanup:
- Rename `src/m3` → a name that says what it is (the SAM-side Z80
  aarch64 assembler — e.g. `src/assembler` / `src/sam-as`). Touches the
  Makefile, the include paths, `tools/*-disk`, CI, and many docs — do it
  as one mechanical, well-reviewed PR.
- Review the wider naming surface: `tests/m{N}` corpora, `ci-m{N}` /
  `test-m{N}` targets, milestone-prefixed fixture names, and whether the
  "M3/M4/M5/M6 fixture" framing should become capability-named suites.
- Keep it a deliberate cleanup PR (or small series), not drive-by renames
  scattered through feature work. Pairs naturally with the repo-audit
  housekeeping track above.

🧭 — principle captured; not yet planned in detail.

### Go-harness fidelity follow-ups

Per the fidelity investigation at
`docs/notes/2026-05-29-go-harness-fidelity-investigation.md` Q4, three cheap
inner-loop follow-ups belong in M7: **activate the harness write-watchpoint**
(the `watchSPLo/Hi` + `stackWrites` scaffold already exists but is unused —
`:88`, `:184`) to flag writes into forbidden physical pages at run time;
promote the divergence sweep to a `make harness-sweep` convenience target
(`:185`) as a fast pre-SimCoupé smoke check; and add the
`tools/z80-test-harness-go/USAGE.md` ledger plus a per-PR `harness-used`
checklist field (`:186`). The investigation explicitly recommends **NOT**
scheduling real-ROM / real-SAMDOS execution (`:14`, `:183`) — the
RAM-encroachment benefit Pete wanted is delivered far more cheaply by the
build-time link-map assertion (see the M6-closure plan PR-5) plus this
watchpoint, without re-implementing SimCoupé. 📋.

### Sysreg Go↔Z80 sync guard

The single concrete Go↔Z80 drift risk recorded by the repo audit (§5,
§6 item 9): the sysreg/sysname tables are independently hand-maintained on
both sides and must agree (the Z80 entries at `src/sysreg_data.asm:214`
and `:256` are annotated "verified vs `tools/sam-aarch64-format/sysregs.go`").
The recommendation is a small dev/CI check that diffs the two tables, or a
cross-link comment making the sync obligation impossible to miss. This is
the tactical near-term guard; the codegen-from-Go-authority strand above is
the strategic fix that dissolves the drift entirely. 📋.

## Beyond-M7 / future ideas (captured 2026-05-29, need organising)

Pete dumped a batch of longer-horizon ideas at the end of the M6/M7
session. Captured here so none are lost; they still need to be
**organised, prioritised, dependency-mapped, and scheduled** (likely
re-homed into ROADMAP milestones / a new milestone doc, and split for
parallel-agent execution). That organisation is itself a pending task.

- **UI-prototyping spike (interactive editor).** Before/at the editor
  milestone (Phase 2), do a spike to prototype what the assembler's
  **user interface** could look like — play with visuals + interaction
  ideas for the main editor UI. Tightly coupled to the SAM screen-mode
  decision (scope-table row) and the hi-res/lo-res user-preference idea.
  Pete: "would be nice to play around with some visuals or ideas."
- **Preprocessor capabilities on SAM: `.if` build-constraints + macros.**
  Pete's macOS spectrum4 workflow uses `.if` for debug/release/test
  builds and `.macro`s; moving development onto the SAM would be painful
  without them. text2bin already handles `.if`/`.macro` Mac-side (the
  flatten/`-E` path) — the question is supporting them *in the on-SAM
  toolchain*. Big enabler for real on-SAM kernel development.
- **Multiple source files + staged/partial loading.** Considering the
  above motivates: support kernels **split across multiple source files**;
  **not** requiring the entire source resident in memory at once; staged
  load + includes; and **assembling part of the code without the full
  source loaded**. Directly relieves the memory-ceiling story and pairs
  with source compression + the Trinity SD/flash storage path (bigger
  binaries). Several interdependent enhancements here.
- **(see also)** the **full ARMv8-A ISA footprint research** and
  **source-compression/compact-`.tbn`** scope rows above, and the
  **Trinity SD/flash storage** beyond-M7 row — this cluster
  (compression ↔ multi-file ↔ staged-load ↔ bigger storage ↔ broader ISA)
  is interlinked and wants a proper dependency map.

## Deferred backlog (smaller items)

These are minor items that belong somewhere in or around M7 but don't rise
to the level of a named strand. One line each, with the cited source.

| Item | Source | Note |
|---|---|---|
| Per-fail-site diagnostic strings | `docs/ROADMAP.md:83` | `fail` body emits a generic "FAIL"; a follow-up can take a string ptr in HL so call sites supply specifics. |
| Paged-IN > 16.5 KB HLOAD-ceiling lift | `docs/notes/2026-05-28-hload-16k-limit-investigation.md`; `docs/notes/m6-status.md:269` | Threshold is 16632 B (works) → 16633 B (hangs); SP-switch patch lifts it. (Largely addressed by PR #39 per memory; verify before scoping.) |
| 64 KB output / 16-bit `OUT_LEN` limit | `docs/notes/m6-status.md:277` | Debug builds (~274 KB) exceed the 16-bit OUT_LEN; needs M7+. |
| `(hksp)` HSAVE/HLOAD error handler | `docs/notes/m6-status.md:279` | HSAVE/HGTHD/HLOAD longjmp on error; the assembler currently crashes. |
| text2bin operand-kind validation (Task 21) | `docs/ROADMAP.md:79` | Deferred — the SAM-side encoder rejects unknown operand kinds via `jp fail`, so the gap doesn't silently produce wrong bytes. Promote when diagnostics improve. |
| Cortex-A53 errata workarounds | `docs/ROADMAP.md:80` | `--fix-cortex-a53-{835769,843419}` not modelled; no-op on release.img today. |
| Multi-section / linker-script refenc | `docs/ROADMAP.md:81-82` | Punted in favour of `text2bin -flatten`; revisit only if a non-spectrum4-shaped project needs it (incl. `SpectrumFourLayout` extraction). |
| Replace `cls` test instruction | `docs/ROADMAP.md:87` | `cls` exists in `manual_forms.go` solely for one test; replace with a real spectrum4 instruction. |
| SimCoupé upstream v1.2.16 bump | `docs/ROADMAP.md:88`; `memory/project_simcoupe_upstream_pr.md` | Switch CI's SimCoupé from build-from-source-with-patch to upstream v1.2.16 (`-exitonhalt` landed as `a65a16e`); drop the vendored patch. |
| Absolute `.set` high-word edge vs Go | `docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md` review §2 | An absolute `.set X, 0xfffffff0_NNNNNNNN` whose high word coincidentally equals ORIGIN_HIGH is misclassified origin-relative (Z80 stores low-32 + reconstructs; Go stores full value). Harmless for release; a divergence-from-Go on non-release inputs → fold into the parity-audit row above. |
| Harden slot self-test PASS_PC dependency | `docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md` review §3 | `run_slot_self_tests` computes the ADRP test vs PASS_PC before `pass_pc_reset` (relies on cold-boot RAM=0). Pre-existing; add an explicit `pass_pc_reset` before the page-12 cluster to harden. |
| Non-canonical logical-immediate decoder tests | `tools/aarch64dec/slots_logical.go` | `decodeBitMasks` now rejects non-canonical `immr` (immr ≥ esize) so callers fall through to `.inst`. Add unit tests with crafted words (e.g. `0x32200013`: esize=32, immr=32) asserting decode returns `.inst`, plus roundtrip fixtures that contain such words. These invariants must also be ported to the Z80 `decodeBitMasks` equivalent when the Z80 disassembler is implemented in strand-B PR-3. |
| `adds` 3-register form (Z80) | `src/slots/shifted_reg.asm`; `src/intercepts.asm` | `adds` shifted-reg form needs `is_shifted_reg_mnemonic` to include ID 98 AND the `shifted_reg_table` entry restored. Currently both are absent (deferred to save budget in PR-2b). Not in release.s; add when test coverage exists or budget allows. |
| `sdiv` missing across the whole stack (i35) | i10 report `docs/notes/2026-06-08-go-vs-z80-disasm-capability-parity.md`; `tools/sam-aarch64-format/mnemonics.go`; `tools/aarch64enc/manual_forms.go` | Surfaced by the i10 capability-parity report: `sdiv` is absent everywhere — not in `mnemonics.go`, no `manual_forms.go` Form (so the decoder emits `.inst`), and unencodable. Invisible because release.img never uses it (`udiv` ID 72 exists, `sdiv` does not). Fix: add `sdiv` mirroring `udiv` across encoder authority + decoder (+ Z80 if not already). Small, clean. Excluded from the i9 sweep; tracked here. |

**Beyond M7** (noted so they're not mistaken for M7 strands): Phase 3 (TFTP
shipper to the Pi 400 over the direct LAN cable, `docs/ROADMAP.md` M-table
row), and the further-future aspirations — terminal, chiptune-backed retro
editor UI, DOS-ops via an emulated SAM — sit past M7.

## Authoritative references

Each strand/item above traces to one of these; cite these, not this doc,
when grounding a claim.

- Bump-arena: `memory/m6_strand_a_complete.md:51`;
  `docs/notes/2026-05-28-memory-layout-brainstorm.md:122,129`;
  `docs/notes/2026-05-28-z80-table-sizing-census.md` (sizing data).
- Codegen-from-Go-authority: `docs/plans/2026-05-29-m6-closure-release-bytematch.md`
  § "M7 — sketch" (PR-A); `tools/sam-aarch64-format/sysregs.go`.
- On-SAM disassembler: `docs/plans/2026-05-28-go-aarch64-disassembler.md`;
  branch `strand-b-1-disassembler`.
- Compact `.tbn`: `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`.
- Editor groundwork: `docs/ROADMAP.md` "Editor vision (Phase 2)".
- Housekeeping track + sysreg sync guard: `docs/notes/2026-05-29-repo-audit.md`
  (§5, §6).
- Go-harness follow-ups: `docs/notes/2026-05-29-go-harness-fidelity-investigation.md`
  (Q4).
- M6 closure (the predecessor milestone): `docs/plans/2026-05-29-m6-closure-release-bytematch.md`;
  `docs/notes/m6-status.md`.
- Roadmap index: `docs/ROADMAP.md`.

---

*This is an initial planning doc. When M6 closes (PR-5 of the M6-closure
plan flips M6 ✅), the standalone M7 plan should be written and the strands
above re-prioritised and sequenced. Until then, treat this as a backlog
register, not a committed plan.*
