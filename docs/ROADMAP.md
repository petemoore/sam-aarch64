# sam-aarch64 roadmap

Canonical index of milestones, design specs, and deferred work. **Update this any time a design doc is added, a milestone changes state, or deferred work gets folded into a milestone.**

## Vision

A SAM Coupé Z80 program that hosts a complete aarch64 development workflow — editor, assembler, disassembler, TFTP shipper — and produces byte-identical binaries to GNU `aarch64-none-elf-as` for the spectrum4 kernel. Daily-driver SAM-as-development-machine for Raspberry Pi 400 bare-metal work.

See `docs/specs/2026-05-09-vision.md` for the long-form pitch and `docs/specs/2026-05-09-phase1-assembler.md` for the Phase 1 (assembler) shape.

## Milestone status

| M | Title | Spec | Status doc | State |
|---|---|---|---|---|
| M0 | Toolchain bootstrap (pyz80 → SimCoupé → samfile → GNU `as` round-trip) | — | `docs/notes/m0-status.md` | ✅ done (PR #1) — plan: `docs/plans/2026-05-09-m0-toolchain-bootstrap.md` |
| M1 | Binary tokenised source format (`.tbn`) + text2bin / bin2text | `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md` | `docs/notes/m1-status.md` | ✅ done (PR #6) — plan: `docs/plans/2026-05-24-m1-binary-tokenised-format.md` |
| M2 | Encoder tables + Mac-side refenc; 20/20 M1 fixtures byte-match GNU | `docs/specs/2026-05-24-m2-encoder-tables-design.md` | `docs/notes/m2-status.md` | ✅ done (PR #7, #8); extended via #11, #14, #15, #17 — plan: `docs/plans/2026-05-24-m2-encoder-tables.md` |
| M3 | Z80 emitter: read `.tbn`, encode, HSAVE output (no symbol table; constant-only) | `docs/specs/2026-05-24-m3-z80-emitter-design.md` | `docs/notes/m3-status.md` | ✅ done (PRs #9, #12, #13, #16, #17, #19); 9/9 fixtures byte-match GNU end-to-end via SimCoupé — plan: `docs/plans/2026-05-24-m3-z80-emitter.md` |
| M4 | Symbol table, multi-pass, full expression evaluator on Z80 | `docs/specs/2026-05-24-m4-symbols-multipass-design.md` | `docs/notes/m4-status.md` | ✅ done (PRs #21, #22, #23); 4/4 M4 fixtures byte-match GNU end-to-end via SimCoupé — plan: `docs/plans/2026-05-24-m4-symbols-multipass.md` |
| M5 | Compound operands (`OpShiftedReg`, `OpExtendedReg`, `OpMem`, `OpSysName`, `OpLitPool`) + remaining directives (`.set`/`.equ`, `.balign`/`.align`, `.org`, `.skip`/`.space`, `.inst`, `.ltorg`) + `ror`-imm intercept | `docs/specs/2026-05-27-m5-compound-operands-directives-design.md` | — | 📋 designed; plan: `docs/plans/2026-05-27-m5-compound-operands-directives.md` |
| M6 | Compact `.tbn` format + built-in disassembler (paged source > 2 KB; real spectrum4 fixture round-tripping byte-identical via SAM) | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` | — | 📋 designed; ordering after M5 (was previously labelled M5; renumbered when compound-operands work became the M4 → M5 critical path) |
| (Phase 2) | On-SAM editor | `docs/specs/2026-05-09-phase1-assembler.md` §editor + future spec | — | 📋 sketched |
| (Phase 3) | TFTP shipper to Pi 400 (Quazar Trinity) over direct LAN cable | `docs/specs/2026-05-27-phase3-tftp-direct-lan-design.md` | — | 📋 design direction captured; reference: `simonowen/trinload` |

Legend: ✅ done · ⏳ in progress · 📋 designed, not started

## Design notes not strictly inside a milestone

These are patterns or research findings that get applied *within* milestones rather than constituting their own milestone:

| Note | When to apply | Status |
|---|---|---|
| `docs/specs/2026-05-27-samdos-load-idiom.md` | When M3/M4 needs to load paged data (e.g. source > section C, symbol table > section C). Reference the COMET trampoline pattern. | 📋 ready to apply when needed |
| `docs/notes/sam-stub-audit.md` | Already applied (PR #13 loader fix used findings). Keep as reference for any future SAMDOS hook work. | ✅ applied; reference |
| `docs/notes/sam-paging.md` | Reference for any LMPR/HMPR work. | ✅ reference |

## Achievements worth keeping visible

- **2026-05-27: production/test M3 build split** (PR #25). `m3-asm` (test variant, includes self-tests) coexists with `m3-asm-prod` (no self-tests, 1977 bytes smaller). Production has 2115 bytes free in the 8 KB code budget (vs 138 bytes for test); the gap is M5's runway. Both variants byte-match GNU on every fixture; CI verifies via `m4` (test) + `m4-prod` (production).
- **2026-05-27: printer-channel fast-fail** (PR #24). `fail:` now emits "FAIL\n" via PRINTL1 and `DI; HALT`s cleanly instead of spinning until the wrapper's 30 s timeout. Failure detection in ci-m3 drops from ~270 s to ~12 s (23×). Wrapper captures the banner via SimCoupé `-parallel1 1 -outpath` and the round-trip scripts grep `^OK$` before bothering with the byte compare.
- **2026-05-27: M4 complete** (PRs #21, #22, #23). The SAM-side Z80 assembler now does symbol resolution, local-label resolution, full expression evaluation (PUSH_SYM / PUSH_LOCAL / PUSH_PC / REL_*), two-pass assembly, and PC-relative branch / adrp encoding. 4/4 M4 fixtures byte-match GNU `as + ld -Ttext=0 + objcopy` end-to-end via SimCoupé. See `docs/notes/m4-status.md`.
- **2026-05-26: byte-identical spectrum4 release.img** (PR #15). Our toolchain produces a 21,752-byte `release.bin` that exactly matches GNU `as → ld → objcopy -O binary` on the release target. See `docs/notes/2026-05-26-release-bytematch.md` (TBD) and the memory entry `[spectrum4-release-bytematch-achieved]`.
- **2026-05-26: full spectrum4 preprocessing** (PR #14). `text2bin` consumes `release.target` end-to-end via `.include` / `.if` / `.macro` / `\arg`.
- **2026-05-26: M3 Tasks 1-12** — all 9 slot encoders ported to Z80 (PRs #9, #12, #16).
- **2026-05-26: M3 loader fixed** (PR #13). HGTHD+HLOAD replaces broken HGFLE+LBYT.

## Deferred-work review checklist

When closing out each milestone, walk this list and ask: *"does this still belong as deferred, or does it now fold into the milestone in flight?"*

- [ ] `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` — reviewed at M5 design close (2026-05-27): kept as standalone milestone (now **M6**) rather than folded into M5. Rationale: M5's critical path is compound operands + directives (closes the M1 → M4 fixture-promotion gap); compact `.tbn` only becomes load-bearing when real spectrum4 sources need to fit in SAM RAM. Revisit at M5 close.
- [ ] `docs/specs/2026-05-27-samdos-load-idiom.md` — review whenever a new on-SAM load path is added; check whether the trampoline pattern is needed.
- [ ] `docs/notes/m2-status.md` known-gaps — `text2bin` operand-kind validation (Task 21) is still deferred; review at M4 close.
- [ ] Cortex-A53 errata workarounds (`--fix-cortex-a53-{835769,843419}`) — not modelled. No-op on release.img today; revisit if/when a target needs them.
- [ ] Multi-section / linker-script honouring in refenc — explicitly punted in favour of the `text2bin -flatten` approach (PR #15). Revisit only if a non-spectrum4-shaped project ever needs it.
- [ ] `SpectrumFourLayout` extraction (`-layout` flag or linker-script parser) — Pete's call: not worth doing unless a second project surfaces.
- [x] ~~Replace M3 `fail:` 30s-timeout spin with **printer-channel status reporting**~~ — **DONE 2026-05-27 in PR #24**. Both success and fail paths now `DI; HALT` cleanly and emit `"OK\n"` / `"FAIL\n"` via PRINTL1 (`&E8` data + `&E9` strobe — note that `&E0`-`&E7` is the floppy controller, NOT a printer port as I first thought). SimCoupé runs with `-parallel1 1 -outpath <tmp> -nextfile 0` so the printer file lands at a known path; `tools/run-simcoupe.sh` exposes the captured banner via a status file. The round-trip scripts grep it for `^OK$` before bothering with the byte compare. Measured: ci-m3 failure detection drops from ~270 s (9 fixtures × 30 s timeout) to ~12 s. Per-fail-site diagnostic strings still TODO — the current `fail` body emits a generic "FAIL"; a follow-up can take a string ptr in HL and have call sites set it for specifics.
- [x] ~~Split M3 assembler build into **production vs test** variants~~ — **DONE 2026-05-27 in PR #25**. `make m3-asm` (test variant) still includes all boot-time self-tests; new `make m3-asm-prod` wraps the test includes + `call run_*_self_tests` lines in `if defined(BUILD_TESTS)` and omits the `-D BUILD_TESTS=1` flag. Measured: 8054 → 6077 bytes (saves 1977 bytes, ~24%). Production has 2115 bytes free in the 8 KB budget vs 138 bytes free for the test variant. Both variants byte-match GNU on every fixture (`ci-m{3,4}` for test, `ci-m{3,4}-prod` for prod). New GH Actions job `m4-prod` guards against accidental divergence. The `print_status` primitive stays in both variants (production needs it for end-user errors).
- [ ] M1 fixtures not yet promotable to M4 — folded into **M5** (see `docs/plans/2026-05-27-m5-compound-operands-directives.md`). The list: `inst_shifted.s`, `inst_ands.s` (second half: shifted-reg), `inst_movz_movk_sym.s` (`.set`), `inst_alu_single.s` (hang root cause = `ror`-imm intercept missing; covered in M5 Task 4), `dir_align_skip.s` / `dir_skip_symbolic.s` (`.balign` / `.skip`). Coverage gaps documented in `docs/notes/m4-status.md`; M5 spec §4 enumerates the promoted fixtures.
- [ ] Replace `cls` in `tests/m1/golden/inst_alu_single.s` with an instruction spectrum4 actually uses. `cls` was added to `tools/aarch64enc/manual_forms.go` solely for that test; spectrum4 doesn't emit it. Picking a real spectrum4 instruction strengthens the test and may let us drop `cls` from `manual_forms.go` + `mnemonics.go`.
- [ ] Switch CI's SimCoupé build from "build-from-source-with-patches" to **upstream SimCoupé v1.2.16** (released 2026-05-27 — see https://github.com/simonowen/simcoupe/releases/tag/v1.2.16). PR #109 was closed unmerged but Simon re-implemented the `-exitonhalt` feature on upstream `main` (commit `a65a16e`); v1.2.16 is the first tagged release that includes it. Action: in `tools/Dockerfile.dev`, replace the git-clone-and-patch-and-build steps with downloading the v1.2.16 binary release (or, if Linux binaries aren't published, build from the v1.2.16 source tarball without applying any local patch). Drop `tools/simcoupe-exitonhalt.patch` from the repo. Update `docs/notes/headless-simcoupe.md` if it references the local build. Not urgent — the current vendored-patch build still works fine — but tracking so we don't carry the patch indefinitely.

## How to extend this doc

When a new design note lands:
1. Add it to the appropriate table above (milestone, or "design notes not strictly inside a milestone").
2. Add a checklist item to "Deferred-work review" if it's not yet implemented.
3. When a milestone closes, walk the checklist and update.

The goal: no design discussion is ever lost to context drift. If it's worth writing down, it's worth indexing here.
