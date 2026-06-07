# `docs/` — map

Start with [`ARCHITECTURE.md`](ARCHITECTURE.md) — the synthesized system
overview and the first document to read after the root `README.md`. Then:

- [`ROADMAP.md`](ROADMAP.md) — the canonical entry point for *state*: the
  session-handover contract, the live current-state block, and the
  milestone table.
- [`specs/`](specs/README.md) — the living design docs (evergreen names,
  one per subsystem still being designed against).
- [`plans/`](plans/README.md) — ephemeral implementation plans; empty is
  the healthy state.
- [`notes/`](notes/README.md) — durable technical references, the `iN`/`qN`
  tracking registries, and the active milestone status doc.
- `sam/`, `comet/`, `saa1099/` — vendored third-party reference material
  (SAM Coupé ROM disassembly + Technical Manual + User Guide, the COMET
  assembler manual, the SAA1099 sound-chip datasheet). Not ours; never
  edited.
