# `docs/specs/` — living design docs

Only **living** documents live here, under evergreen (undated) filenames —
when a design ships, its durable rationale folds into
[`../ARCHITECTURE.md`](../ARCHITECTURE.md) or a reference doc and the
design doc is deleted (git history is the archive).

- [`vision.md`](vision.md) — the north star: SAM Coupé as the daily-driver
  aarch64 development machine.
- [`phase1-assembler.md`](phase1-assembler.md) — the Phase 1/2/3 charter.
- [`tbn-binary-format-reference.md`](tbn-binary-format-reference.md) — the
  **normative** `.tbn` binary encoding reference.
- [`compact-tbn-nextgen-design.md`](compact-tbn-nextgen-design.md) — the
  `.tbn` v2 instruction-overlay design (M8; i39c/i40/i51 pending).
- [`i48-syntactic-encoder-design.md`](i48-syntactic-encoder-design.md) —
  single-format syntactic encoder; drives the future i48c SAM-side work.
- [`codegen-tables-design.md`](codegen-tables-design.md) — generate the
  Z80 data tables (sysreg, constants, mnemonic IDs) from the Go authority
  (i7; approved — phases A–C in flight, phase D queued as i74).
- [`editor-edit-model-design.md`](editor-edit-model-design.md) — the
  Phase-2 editor's in-memory edit model.
- [`comment-storage-design.md`](comment-storage-design.md) — compressed-
  resident comment storage (i60c): ZX0 blocks + dirty overlay + streaming
  save + watermark math + page placement.
- [`paged-in-design.md`](paged-in-design.md) /
  [`paged-out-design.md`](paged-out-design.md) — the paging-architecture
  rationale for source IN and output OUT.
- [`phase3-tftp-design.md`](phase3-tftp-design.md) — Phase 3 (TFTP to the
  Pi over direct LAN) direction.
- [`samdos-file-io.md`](samdos-file-io.md) — SAMDOS read/write idioms:
  HLOAD trampoline, HSAVE UIFA pattern, hook clobber facts, Z80 snippets.
- [`editor-vision.md`](editor-vision.md) — Phase 2 editor design pointers
  (feeds the future Phase-2 spec): explanation panels, register simulator,
  retro UI affordances, keyboard-driven interaction model, edit-model pointer
  (i41).
- [`editor-tui-prototype-design.md`](editor-tui-prototype-design.md) —
  host-side Go TUI editor prototype at SAM-faithful geometry (i76): the
  SAM-screen abstraction, terminal + PNG/SCREEN$ mockup backends, geometry
  matrix, i41 op mapping (approved with amendments — P1 in flight).
