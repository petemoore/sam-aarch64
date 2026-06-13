# `docs/notes/` — references, registries, active milestone

Three kinds of file live here:

- **Tracking registries** — [`item-registry-open.md`](item-registry-open.md) (`iN`
  work items) and [`question-registry-open.md`](question-registry-open.md) (`qN`
  open questions for Pete), each with a `*-closed.md` sibling holding resolved
  rows ([items](item-registry-closed.md), [questions](question-registry-closed.md)).
  Read the **open** files for outstanding work; resolving a row moves it open→closed
  in the same PR. Milestone-neutral, never archived; the registry discipline +
  controlled status vocabulary are documented in each open file's header.
- **The active milestone status doc** — [`m9-status.md`](m9-status.md),
  the per-strand source of truth for the milestone in flight. Deleted at
  milestone close after the registry walk (see `docs/ROADMAP.md`).
- **Durable technical references** — everything else: SAM hardware/format
  facts ([`sam-paging.md`](sam-paging.md),
  [`sam-disk-format.md`](sam-disk-format.md),
  [`sam-file-header.md`](sam-file-header.md),
  [`sam-basic-save-format.md`](sam-basic-save-format.md),
  [`test-mgt-byte-layout.md`](test-mgt-byte-layout.md)), the assembler's
  memory map mirror ([`memory-layout.md`](memory-layout.md)), tooling
  references ([`headless-simcoupe.md`](headless-simcoupe.md),
  [`sam-stub-audit.md`](sam-stub-audit.md)), spike findings
  ([`basic-detokeniser-spike.md`](basic-detokeniser-spike.md)), and
  unconsumed research feeding future decisions
  ([`comment-compression-research.md`](comment-compression-research.md),
  [`trinity-capabilities.md`](trinity-capabilities.md),
  [`bdos-version-landscape.md`](bdos-version-landscape.md),
  [`sam-music-playback-research.md`](sam-music-playback-research.md),
  [`a64-isa-footprint-research.md`](a64-isa-footprint-research.md),
  [`simcoupe-paste-control-plane.md`](simcoupe-paste-control-plane.md)).
