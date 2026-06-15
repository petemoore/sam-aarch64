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
- [`source-structure-preservation-design.md`](source-structure-preservation-design.md)
  — preserving blank lines + comment paragraph structure on round-trip (i78):
  a blank-run row kind in the editor-region sidecar + the multi-line-comment
  grouping rule; indentation deferred behind the i76 interface sign-off.
- [`paged-in-design.md`](paged-in-design.md) /
  [`paged-out-design.md`](paged-out-design.md) — the paging-architecture
  rationale for source IN and output OUT.
- [`phase3-delivery-design.md`](phase3-delivery-design.md) — Phase 3
  end-to-end delivery design (i84): the self-hosting loop — SAM as
  **DHCP+TFTP netboot server** (serves firmware + kernel by name, model-
  agnostic) for any aarch64 Pi, plus a TFTP client and on-SAM HTTP firmware
  self-provisioning. **Direction confirmed (Pete 2026-06-14, q11/q12
  resolved)**; remaining gate = go-ahead to implement. Supersedes the sketch
  below on go-ahead.
- [`phase3-tftp-design.md`](phase3-tftp-design.md) — Phase 3 (TFTP to the
  Pi over direct LAN) **prior direction sketch** (2026-05-27); superseded
  on the implementation go-ahead of the delivery design above.
- [`tls13-handshake.md`](tls13-handshake.md) — Phase 3 (i88) on-SAM TLS 1.3
  client: the handshake plan composing the built cipher-suite primitives
  (X25519 + ChaCha20-Poly1305 + SHA-256) into host-verifiable bricks; the
  bootable-integration memory budget is q17.
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
- [`editor-rendering-rules-design.md`](editor-rendering-rules-design.md) —
  the editor's rendering-rule specification (i76): the per-record render
  ladder, long-line wrap/shift, colour roles + token marking, cursor-line
  expansion, comment placement, truncation, chrome — the written contract for
  the config lab. The default shipping config is recommended (§10) pending
  Pete's sign-off (q13); geometry/font stay gated on the i6 P3 memo.
