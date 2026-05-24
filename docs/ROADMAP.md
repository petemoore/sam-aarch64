# sam-aarch64 roadmap

Canonical index of milestones, design specs, and deferred work. **Update this any time a design doc is added, a milestone changes state, or deferred work gets folded into a milestone.**

## Vision

A SAM Coupé Z80 program that hosts a complete aarch64 development workflow — editor, assembler, disassembler, TFTP shipper — and produces byte-identical binaries to GNU `aarch64-none-elf-as` for the spectrum4 kernel. Daily-driver SAM-as-development-machine for Raspberry Pi 400 bare-metal work.

See `docs/specs/2026-05-09-vision.md` for the long-form pitch and `docs/specs/2026-05-09-phase1-assembler.md` for the Phase 1 (assembler) shape.

## Milestone status

| M | Title | Spec | Status doc | State |
|---|---|---|---|---|
| M0 | Toolchain bootstrap (pyz80 → SimCoupé → samfile → GNU `as` round-trip) | — | `docs/notes/m0-status.md` | ✅ done (PR #1) |
| M1 | Binary tokenised source format (`.tbn`) + text2bin / bin2text | `docs/specs/2026-05-23-m1-binary-tokenised-format-design.md` | `docs/notes/m1-status.md` | ✅ done (PR #6) |
| M2 | Encoder tables + Mac-side refenc; 20/20 M1 fixtures byte-match GNU | `docs/specs/2026-05-24-m2-encoder-tables-design.md` | `docs/notes/m2-status.md` | ✅ done (PR #7, #8); extended via #11, #14, #15, #17 |
| M3 | Z80 emitter: read `.tbn`, encode, HSAVE output (no symbol table; constant-only) | `docs/specs/2026-05-24-m3-z80-emitter-design.md` | `docs/notes/m3-status.md` | ⏳ in progress — Tasks 1-12 done (PRs #9, #12, #16, #17); Tasks 16-22 in flight |
| M4 | Symbol table, multi-pass, full expression evaluator on Z80 | `docs/specs/2026-05-24-m4-symbols-multipass-design.md` | — (not yet) | 📋 designed; ordering after M3 |
| M5 | Compact `.tbn` format + built-in disassembler | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` | — | 📋 designed; ordering after M3 / M4 |
| (Phase 2) | On-SAM editor | `docs/specs/2026-05-09-phase1-assembler.md` §editor + future spec | — | 📋 sketched |
| (Phase 3) | TFTP shipper to Pi 400 (Quazar Trinity) | future spec | — | 📋 sketched; reference: `simonowen/trinload` |

Legend: ✅ done · ⏳ in progress · 📋 designed, not started

## Design notes not strictly inside a milestone

These are patterns or research findings that get applied *within* milestones rather than constituting their own milestone:

| Note | When to apply | Status |
|---|---|---|
| `docs/specs/2026-05-27-samdos-load-idiom.md` | When M3/M4 needs to load paged data (e.g. source > section C, symbol table > section C). Reference the COMET trampoline pattern. | 📋 ready to apply when needed |
| `docs/notes/sam-stub-audit.md` | Already applied (PR #13 loader fix used findings). Keep as reference for any future SAMDOS hook work. | ✅ applied; reference |
| `docs/notes/sam-paging.md` | Reference for any LMPR/HMPR work. | ✅ reference |

## Achievements worth keeping visible

- **2026-05-26: byte-identical spectrum4 release.img** (PR #15). Our toolchain produces a 21,752-byte `release.bin` that exactly matches GNU `as → ld → objcopy -O binary` on the release target. See `docs/notes/2026-05-26-release-bytematch.md` (TBD) and the memory entry `[spectrum4-release-bytematch-achieved]`.
- **2026-05-26: full spectrum4 preprocessing** (PR #14). `text2bin` consumes `release.target` end-to-end via `.include` / `.if` / `.macro` / `\arg`.
- **2026-05-26: M3 Tasks 1-12** — all 9 slot encoders ported to Z80 (PRs #9, #12, #16).
- **2026-05-26: M3 loader fixed** (PR #13). HGTHD+HLOAD replaces broken HGFLE+LBYT.

## Deferred-work review checklist

When closing out each milestone, walk this list and ask: *"does this still belong as deferred, or does it now fold into the milestone in flight?"*

- [ ] `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` — review after M3 + M4 land. Could become its own M5 deliverable, or fold a subset (e.g. just symbol pooling) into M4.
- [ ] `docs/specs/2026-05-27-samdos-load-idiom.md` — review whenever a new on-SAM load path is added; check whether the trampoline pattern is needed.
- [ ] `docs/notes/m2-status.md` known-gaps — `text2bin` operand-kind validation (Task 21) is still deferred; review at M4 close.
- [ ] Cortex-A53 errata workarounds (`--fix-cortex-a53-{835769,843419}`) — not modelled. No-op on release.img today; revisit if/when a target needs them.
- [ ] Multi-section / linker-script honouring in refenc — explicitly punted in favour of the `text2bin -flatten` approach (PR #15). Revisit only if a non-spectrum4-shaped project ever needs it.
- [ ] `SpectrumFourLayout` extraction (`-layout` flag or linker-script parser) — Pete's call: not worth doing unless a second project surfaces.
- [ ] Replace M3 `fail:` 30s-timeout spin with **printer-channel status reporting** — `OUT (&E1), A` to write OK/FAIL strings; test wrapper checks both exit code AND printer log. Drops failure latency from 30s to ~100ms, gives per-fail-site diagnostic messages, sidesteps the silent-success risk. Pete's idea, 2026-05-27. Land after M3 Tasks 16-22 to avoid merge conflicts with the in-flight `src/m3/assembler.asm` work.

## How to extend this doc

When a new design note lands:
1. Add it to the appropriate table above (milestone, or "design notes not strictly inside a milestone").
2. Add a checklist item to "Deferred-work review" if it's not yet implemented.
3. When a milestone closes, walk the checklist and update.

The goal: no design discussion is ever lost to context drift. If it's worth writing down, it's worth indexing here.
